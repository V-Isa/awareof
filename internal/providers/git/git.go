package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/gitexec"
	"github.com/V-Isa/awareof/internal/pathutil"
	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

const (
	providerID scope.ProviderID = "git"
	instanceID scope.InstanceID = "git"
)

// Provider evaluates effective Git tracking scope.
type Provider struct {
	runner safeexec.CommandRunner
}

func New(runner safeexec.CommandRunner) *Provider {
	return &Provider{runner: runner}
}

func (*Provider) ID() scope.ProviderID {
	return providerID
}

func (*Provider) Detect(_ context.Context, repo provider.Repository) ([]provider.Instance, error) {
	_, err := os.Lstat(filepath.Join(repo.Root, ".git"))
	switch {
	case err == nil:
		return []provider.Instance{provider.InstanceDescriptor{Provider: providerID, ID: instanceID, Label: "Git worktree"}}, nil
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	default:
		return nil, fmt.Errorf("inspect .git: %w", err)
	}
}

func (p *Provider) Evaluate(
	ctx context.Context,
	_ provider.EvaluationContext,
	repo provider.Repository,
	instance provider.Instance,
	paths []scope.Path,
) ([]scope.Result, error) {
	if p.runner == nil {
		return nil, errors.New("command runner is nil")
	}
	descriptor := instance.Descriptor()
	if descriptor.Provider != providerID || descriptor.ID != instanceID {
		return nil, fmt.Errorf("unsupported instance %q", descriptor.ID)
	}
	if len(paths) == 0 {
		return []scope.Result{}, nil
	}

	tracked, submodules, err := p.trackedPaths(ctx, repo.Root)
	if err != nil {
		if unavailable, ok := safeexec.AsUnavailable(err); ok {
			return unavailableResults(paths, unavailable), nil
		}
		return nil, err
	}
	// Git rejects paths beneath gitlinks. Only the gitlink itself belongs to
	// the outer index; nested worktree contents need their own instance.
	queryPaths := make([]scope.Path, 0, len(paths))
	for _, name := range paths {
		if submodule, ok := containingPath(name, submodules); !ok || name == submodule {
			queryPaths = append(queryPaths, name)
		}
	}
	ignoreFacts, err := p.ignoreFacts(ctx, repo.Root, queryPaths)
	if err != nil {
		if unavailable, ok := safeexec.AsUnavailable(err); ok {
			return unavailableResults(paths, unavailable), nil
		}
		return nil, err
	}

	results := make([]scope.Result, 0, len(paths))
	for _, path := range paths {
		if submodule, ok := containingPath(path, submodules); ok && path != submodule {
			results = append(results, scope.Result{
				Path:     path,
				Provider: providerID,
				Instance: instanceID,
				State:    scope.NotApp,
				Explanation: scope.Explanation{
					Code:    "git/submodule-content",
					Summary: fmt.Sprintf("belongs to nested Git worktree %q, not the outer worktree", submodule),
				},
				Provenance: scope.Provenance{Method: scope.SafeNative, Tool: "git"},
			})
			continue
		}
		_, isTracked := tracked[path]
		ignore := ignoreFacts[path]
		state, reason := classify(repo.Root, isTracked, ignore)
		results = append(results, scope.Result{
			Path:        path,
			Provider:    providerID,
			Instance:    instanceID,
			State:       state,
			Explanation: reason,
			Provenance: scope.Provenance{
				Method: scope.SafeNative,
				Tool:   "git",
			},
		})
	}
	return results, nil
}

func unavailableResults(paths []scope.Path, unavailable *safeexec.UnavailableError) []scope.Result {
	results := make([]scope.Result, 0, len(paths))
	for _, path := range paths {
		results = append(results, scope.Result{
			Path:     path,
			Provider: providerID,
			Instance: instanceID,
			State:    scope.Unknown,
			Explanation: scope.Explanation{
				Code:     unavailable.Code,
				Summary:  unavailable.Summary,
				Evidence: unavailable.Evidence,
				Action:   unavailable.Action,
			},
			Provenance: scope.Provenance{Method: scope.Unavailable, Tool: "git"},
		})
	}
	return results
}

func (p *Provider) trackedPaths(ctx context.Context, root string) (map[scope.Path]struct{}, map[scope.Path]struct{}, error) {
	response, err := p.runner.Run(ctx, safeexec.Request{
		Root:     root,
		Tool:     "git",
		Args:     []string{"--no-pager", "-c", "core.fsmonitor=false", "ls-files", "--cached", "--stage", "--full-name", "-z"},
		Env:      gitexec.Environment(),
		UnsetEnv: gitexec.UnsetEnvironment(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list tracked paths: %w", err)
	}
	if response.ExitCode != 0 {
		return nil, nil, commandError("list tracked paths", response)
	}

	tracked := make(map[scope.Path]struct{})
	submodules := make(map[scope.Path]struct{})
	for _, item := range splitNUL(response.Stdout) {
		metadata, pathName, ok := strings.Cut(item, "\t")
		if !ok || pathName == "" {
			return nil, nil, fmt.Errorf("list tracked paths: parse record %q", item)
		}
		if !utf8.ValidString(pathName) {
			// Core paths are valid UTF-8, so this entry cannot equal a queried
			// path. Ignore the unrepresentable entry without weakening results
			// for canonical paths.
			continue
		}
		fields := strings.Fields(metadata)
		if len(fields) != 3 {
			return nil, nil, fmt.Errorf("list tracked paths: parse metadata %q", metadata)
		}
		logical := scope.Path(pathName)
		tracked[logical] = struct{}{}
		if fields[0] == "160000" {
			submodules[logical] = struct{}{}
		}
	}
	return tracked, submodules, nil
}

func (p *Provider) ignoreFacts(ctx context.Context, root string, paths []scope.Path) (map[scope.Path]ignoreFact, error) {
	facts := make(map[scope.Path]ignoreFact, len(paths))
	if len(paths) == 0 {
		return facts, nil
	}
	input := make([]byte, 0)
	for _, path := range paths {
		input = append(input, gitInputPath(path)...)
		input = append(input, 0)
	}
	response, err := p.runner.Run(ctx, safeexec.Request{
		Root:     root,
		Tool:     "git",
		Args:     []string{"--no-pager", "-c", "core.fsmonitor=false", "check-ignore", "--no-index", "--verbose", "--non-matching", "--stdin", "-z"},
		Stdin:    input,
		Env:      gitexec.Environment(),
		UnsetEnv: gitexec.UnsetEnvironment(),
	})
	if err != nil {
		return nil, fmt.Errorf("evaluate ignore rules: %w", err)
	}
	if response.ExitCode != 0 && response.ExitCode != 1 {
		return nil, commandError("evaluate ignore rules", response)
	}

	fields := splitNULPreserveEmpty(response.Stdout)
	if len(fields) != len(paths)*4 {
		return nil, fmt.Errorf("evaluate ignore rules: Git returned %d fields for %d paths, want %d", len(fields), len(paths), len(paths)*4)
	}
	for i, expectedPath := range paths {
		offset := i * 4
		if !utf8.ValidString(fields[offset]) || !utf8.ValidString(fields[offset+2]) || !utf8.ValidString(fields[offset+3]) {
			return nil, fmt.Errorf("evaluate ignore rules: Git returned invalid UTF-8 at index %d", i)
		}
		if fields[offset+3] != gitInputPath(expectedPath) {
			return nil, fmt.Errorf("evaluate ignore rules: Git returned path %q at index %d, want %q", fields[offset+3], i, expectedPath)
		}
		line := 0
		if fields[offset+1] != "" {
			parsed, err := strconv.Atoi(fields[offset+1])
			if err != nil {
				return nil, fmt.Errorf("evaluate ignore rules: parse line %q for path %q: %w", fields[offset+1], expectedPath, err)
			}
			if parsed <= 0 {
				return nil, fmt.Errorf("evaluate ignore rules: invalid line %d for path %q", parsed, expectedPath)
			}
			line = parsed
		}
		pattern := fields[offset+2]
		facts[expectedPath] = ignoreFact{
			Ignored: pattern != "" && !strings.HasPrefix(pattern, "!"),
			Source:  fields[offset],
			Line:    line,
			Pattern: pattern,
		}
	}
	return facts, nil
}

func gitInputPath(path scope.Path) string {
	return "./" + string(path)
}

func containingPath(name scope.Path, candidates map[scope.Path]struct{}) (scope.Path, bool) {
	current := name
	for current != "." && current != "" {
		if _, ok := candidates[current]; ok {
			return current, true
		}
		current = scope.Path(path.Dir(string(current)))
	}
	return "", false
}

type ignoreFact struct {
	Ignored bool
	Source  string
	Line    int
	Pattern string
}

func classify(root string, tracked bool, ignore ignoreFact) (scope.State, scope.Explanation) {
	switch {
	case tracked && ignore.Ignored:
		return scope.In, scope.Explanation{
			Code:    "git/tracked-ignored",
			Summary: "tracked; also matches " + describePattern(root, ignore),
		}
	case tracked:
		return scope.In, scope.Explanation{Code: "git/tracked", Summary: "tracked by Git"}
	case ignore.Ignored:
		return scope.Out, scope.Explanation{
			Code:    "git/ignored",
			Summary: "ignored by " + describePattern(root, ignore),
		}
	case ignore.Pattern != "":
		return scope.In, scope.Explanation{
			Code:    "git/unignored",
			Summary: "included by " + describePattern(root, ignore),
		}
	default:
		return scope.In, scope.Explanation{
			Code:    "git/not-ignored",
			Summary: "not ignored; eligible for tracking",
		}
	}
}

func describePattern(root string, fact ignoreFact) string {
	source := fact.Source
	if filepath.IsAbs(source) {
		if rel, err := pathutil.RelativeWithin(root, source); err == nil {
			source = filepath.ToSlash(rel)
		} else {
			source = "global Git excludes"
		}
	}
	location := source
	if fact.Line > 0 {
		location += ":" + strconv.Itoa(fact.Line)
	}
	return fmt.Sprintf("%s pattern %q", location, fact.Pattern)
}

func commandError(action string, response safeexec.Response) error {
	detail := strings.TrimSpace(string(response.Stderr))
	if detail == "" {
		return fmt.Errorf("%s: Git exited with status %d", action, response.ExitCode)
	}
	return fmt.Errorf("%s: Git exited with status %d: %s", action, response.ExitCode, detail)
}

func splitNUL(data []byte) []string {
	fields := splitNULPreserveEmpty(data)
	out := fields[:0]
	for _, field := range fields {
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func splitNULPreserveEmpty(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	fields := strings.Split(string(data), "\x00")
	if fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	return fields
}
