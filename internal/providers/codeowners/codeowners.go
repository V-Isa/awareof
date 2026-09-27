// Package codeowners evaluates local GitHub CODEOWNERS rule coverage.
package codeowners

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/scope"
)

const (
	providerID           scope.ProviderID = "codeowners"
	rootInstanceID       scope.InstanceID = "codeowners"
	maxFileSize                           = 3 * 1024 * 1024
	maxRuleChecks                         = 10_000_000
	contextCheckInterval                  = 256
	reference                             = "docs.github.com/codeowners"
)

var standardLocations = []scope.Path{
	".github/CODEOWNERS",
	"CODEOWNERS",
	"docs/CODEOWNERS",
}

// Provider evaluates whether the final matching local CODEOWNERS rule assigns
// one or more owner references to a repository path. It does not contact
// GitHub to verify owner existence, visibility, or repository permissions.
type Provider struct{}

func New() *Provider {
	return &Provider{}
}

func (*Provider) ID() scope.ProviderID {
	return providerID
}

// Detect uses GitHub's documented location precedence. It reads only the first
// CODEOWNERS file found and never executes a native tool.
func (*Provider) Detect(ctx context.Context, repo provider.Repository) (_ []provider.Instance, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("detect CODEOWNERS: %w", err)
	}
	if repo.Root == "" {
		return nil, errors.New("repository root is empty")
	}

	root, err := os.OpenRoot(repo.Root)
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close repository root: %w", err))
		}
	}()

	for _, location := range standardLocations {
		instance, found, err := detectLocation(root, location)
		if err != nil {
			return nil, err
		}
		if found {
			return []provider.Instance{instance}, nil
		}
	}
	return nil, nil
}

func detectLocation(root *os.Root, location scope.Path) (codeownersInstance, bool, error) {
	info, err := root.Lstat(string(location))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return codeownersInstance{}, false, nil
	case err != nil:
		return unavailableInstance(location, fmt.Sprintf("cannot inspect CODEOWNERS safely: %v", err)), true, nil
	case info.IsDir():
		return codeownersInstance{}, false, nil
	case !info.Mode().IsRegular():
		return unavailableInstance(location, "CODEOWNERS path is not a regular file and cannot be inspected safely"), true, nil
	case info.Size() >= maxFileSize:
		return oversizedInstance(location), true, nil
	}

	content, oversized, err := readFile(root, location, info)
	if err != nil {
		return unavailableInstance(location, fmt.Sprintf("cannot read CODEOWNERS safely: %v", err)), true, nil
	}
	if oversized {
		return oversizedInstance(location), true, nil
	}
	if !utf8.Valid(content) {
		return unavailableInstance(location, "CODEOWNERS is not valid UTF-8"), true, nil
	}

	rules, issues := parseRules(string(content))
	return codeownersInstance{
		descriptor: descriptor(location),
		location:   location,
		rules:      rules,
		issues:     issues,
	}, true, nil
}

func readFile(root *os.Root, location scope.Path, before fs.FileInfo) (_ []byte, oversized bool, returnErr error) {
	file, err := root.Open(path.Clean(string(location)))
	if err != nil {
		return nil, false, fmt.Errorf("open %s: %w", location, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close %s: %w", location, err))
		}
	}()

	after, err := file.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("inspect opened %s: %w", location, err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, false, errors.New("CODEOWNERS changed while it was being inspected")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxFileSize))
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", location, err)
	}
	return content, len(content) >= maxFileSize, nil
}

func descriptor(location scope.Path) provider.InstanceDescriptor {
	return provider.InstanceDescriptor{
		Provider: providerID,
		ID:       rootInstanceID,
		Label:    "CODEOWNERS " + string(location),
	}
}

func unavailableInstance(location scope.Path, reason string) codeownersInstance {
	return codeownersInstance{
		descriptor: descriptor(location),
		location:   location,
		unavailable: &scope.Explanation{
			Code:    "codeowners/file-unavailable",
			Summary: reason,
		},
	}
}

func oversizedInstance(location scope.Path) codeownersInstance {
	return codeownersInstance{
		descriptor: descriptor(location),
		location:   location,
		oversized:  true,
	}
}

func (*Provider) Evaluate(
	ctx context.Context,
	_ provider.EvaluationContext,
	_ provider.Repository,
	instance provider.Instance,
	paths []scope.Path,
) ([]scope.Result, error) {
	current, ok := instance.(codeownersInstance)
	if !ok {
		return nil, errors.New("unsupported CODEOWNERS instance")
	}
	descriptor := current.Descriptor()
	if descriptor.Provider != providerID || descriptor.ID == "" {
		return nil, fmt.Errorf("unsupported instance %q", descriptor.ID)
	}
	if len(paths) == 0 {
		return []scope.Result{}, nil
	}

	results := make([]scope.Result, 0, len(paths))
	remainingChecks := maxRuleChecks
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("evaluate CODEOWNERS: %w", err)
		}
		state, explanation, err := current.evaluate(ctx, name, &remainingChecks)
		if err != nil {
			return nil, fmt.Errorf("evaluate CODEOWNERS: %w", err)
		}
		results = append(results, scope.Result{
			Path:        name,
			Provider:    providerID,
			Instance:    descriptor.ID,
			State:       state,
			Explanation: explanation,
			Provenance:  current.provenance(state),
		})
	}
	return results, nil
}

type codeownersInstance struct {
	descriptor  provider.InstanceDescriptor
	location    scope.Path
	rules       []rule
	issues      []parseIssue
	unavailable *scope.Explanation
	oversized   bool
}

func (i codeownersInstance) Descriptor() provider.InstanceDescriptor {
	return i.descriptor
}

func (i codeownersInstance) evaluate(ctx context.Context, name scope.Path, remainingChecks *int) (scope.State, scope.Explanation, error) {
	if i.unavailable != nil {
		return scope.Unknown, *i.unavailable, nil
	}
	if i.oversized {
		return scope.Out, scope.Explanation{
			Code:    "codeowners/file-too-large",
			Summary: fmt.Sprintf("%s is not loaded by GitHub because it is not under 3 MB", i.location),
		}, nil
	}

	matched, complete, err := i.lastMatch(ctx, string(name), remainingChecks)
	if err != nil {
		return "", scope.Explanation{}, err
	}
	if !complete {
		return scope.Unknown, scope.Explanation{
			Code:    "codeowners/evaluation-limit",
			Summary: "CODEOWNERS evaluation reached its safe work limit",
			Action:  "query fewer paths at once",
		}, nil
	}
	if matched == nil {
		summary := fmt.Sprintf("no matching rule in %s", i.location)
		if count := len(i.issues); count != 0 {
			summary += fmt.Sprintf("; %s skipped", invalidLineCount(count))
		}
		return scope.Out, scope.Explanation{Code: "codeowners/uncovered", Summary: summary}, nil
	}

	location := fmt.Sprintf("%s:%d pattern %q", i.location, matched.line, matched.rawPattern)
	if len(matched.owners) == 0 {
		return scope.Out, scope.Explanation{
			Code:    "codeowners/unowned",
			Summary: location + " assigns no owners",
		}, nil
	}
	return scope.In, scope.Explanation{
		Code:    "codeowners/covered",
		Summary: strings.Join(matched.owners, " ") + " (" + location + ")",
	}, nil
}

func (i codeownersInstance) lastMatch(ctx context.Context, name string, remainingChecks *int) (*rule, bool, error) {
	for index := len(i.rules) - 1; index >= 0; index-- {
		if *remainingChecks == 0 {
			return nil, false, nil
		}
		if *remainingChecks%contextCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
		}
		*remainingChecks--
		if i.rules[index].matches(name) {
			return &i.rules[index], true, nil
		}
	}
	return nil, true, nil
}

func (i codeownersInstance) provenance(state scope.State) scope.Provenance {
	if state == scope.Unknown {
		return scope.Provenance{Method: scope.Unavailable, Reference: reference}
	}
	return scope.Provenance{Method: scope.SafeParser, Reference: reference}
}

func invalidLineCount(count int) string {
	if count == 1 {
		return "1 invalid line"
	}
	return fmt.Sprintf("%d invalid lines", count)
}
