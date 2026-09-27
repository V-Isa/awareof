// Package pathsource obtains core PathSets from external selection mechanisms.
package pathsource

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/gitexec"
	"github.com/V-Isa/awareof/internal/pathset"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

// Git obtains changed paths through fixed, read-only Git commands.
type Git struct {
	runner safeexec.CommandRunner
}

// NewGit returns a Git-backed change source.
func NewGit(runner safeexec.CommandRunner) *Git {
	return &Git{runner: runner}
}

// Staged returns paths changed in the index relative to HEAD.
func (g *Git) Staged(ctx context.Context, root string) ([]scope.Path, error) {
	return g.diff(ctx, root, "", true)
}

// ChangedFrom returns tracked paths changed between revision and the current
// worktree and index. Git-untracked paths are intentionally not included.
func (g *Git) ChangedFrom(ctx context.Context, root, revision string) ([]scope.Path, error) {
	objectID, err := g.resolveRevision(ctx, root, revision)
	if err != nil {
		return nil, err
	}
	return g.diff(ctx, root, objectID, false)
}

func (g *Git) resolveRevision(ctx context.Context, root, revision string) (string, error) {
	if err := validateRevision(revision); err != nil {
		return "", err
	}
	if g == nil || g.runner == nil {
		return "", errors.New("git path source command runner is nil")
	}
	response, err := g.runner.Run(ctx, safeexec.Request{
		Root:     root,
		Tool:     "git",
		Args:     []string{"--no-pager", "rev-parse", "--verify", "--quiet", revision + "^{commit}"},
		Env:      gitexec.Environment(),
		UnsetEnv: gitexec.UnsetEnvironment(),
	})
	if err != nil {
		return "", fmt.Errorf("resolve changed-from revision %q: %w", revision, err)
	}
	if response.ExitCode != 0 {
		return "", gitCommandError(fmt.Sprintf("resolve changed-from revision %q", revision), response)
	}
	objectID, err := parseObjectID(response.Stdout)
	if err != nil {
		return "", fmt.Errorf("resolve changed-from revision %q: %w", revision, err)
	}
	return objectID, nil
}

func (g *Git) diff(ctx context.Context, root, objectID string, staged bool) ([]scope.Path, error) {
	if g == nil || g.runner == nil {
		return nil, errors.New("git path source command runner is nil")
	}
	args := []string{
		"--no-pager",
		"-c", "core.fsmonitor=false",
		"-c", "diff.submodule=short",
		"diff",
		"--name-only",
		"-z",
		"--no-ext-diff",
		"--no-textconv",
		"--no-renames",
		"--ignore-submodules=none",
	}
	operation := "list staged paths"
	if staged {
		args = append(args, "--cached")
	} else {
		operation = "list paths changed from revision"
		args = append(args, objectID)
	}
	args = append(args, "--")
	response, err := g.runner.Run(ctx, safeexec.Request{
		Root:     root,
		Tool:     "git",
		Args:     args,
		Env:      gitexec.Environment(),
		UnsetEnv: gitexec.UnsetEnvironment(),
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	if response.ExitCode != 0 {
		return nil, gitCommandError(operation, response)
	}
	names, err := parseNames(response.Stdout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	paths, err := (pathset.Builder{Root: root}).BuildLiterals(ctx, names)
	if err != nil {
		return nil, fmt.Errorf("%s: normalize git path: %w", operation, err)
	}
	return paths, nil
}

func validateRevision(revision string) error {
	switch {
	case revision == "":
		return errors.New("changed-from revision is empty")
	case strings.HasPrefix(revision, "-"):
		return fmt.Errorf("changed-from revision %q must not start with '-'", revision)
	case strings.ContainsRune(revision, 0) || !utf8.ValidString(revision):
		return errors.New("changed-from revision must be valid UTF-8 without NUL bytes")
	default:
		return nil
	}
}

func parseObjectID(output []byte) (string, error) {
	if !utf8.Valid(output) {
		return "", errors.New("git returned a non-UTF-8 object id")
	}
	value := strings.TrimSuffix(string(output), "\n")
	value = strings.TrimSuffix(value, "\r")
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("git returned an invalid object id record")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || (len(decoded) != 20 && len(decoded) != 32) {
		return "", fmt.Errorf("git returned invalid object id %q", value)
	}
	return value, nil
}

func parseNames(output []byte) ([]string, error) {
	if len(output) == 0 {
		return []string{}, nil
	}
	if output[len(output)-1] != 0 {
		return nil, errors.New("git path output is not NUL-terminated")
	}
	records := strings.Split(string(output[:len(output)-1]), "\x00")
	for index, name := range records {
		switch {
		case name == "":
			return nil, fmt.Errorf("git returned an empty path at index %d", index)
		case !utf8.ValidString(name):
			return nil, fmt.Errorf("git returned a non-UTF-8 path at index %d", index)
		}
	}
	return records, nil
}

func gitCommandError(operation string, response safeexec.Response) error {
	detail := strings.TrimSpace(string(response.Stderr))
	if detail == "" {
		return fmt.Errorf("%s: git exited with status %d", operation, response.ExitCode)
	}
	return fmt.Errorf("%s: git exited with status %d: %s", operation, response.ExitCode, detail)
}
