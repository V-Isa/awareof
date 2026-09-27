package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/scope"
)

const (
	providerID          scope.ProviderID = "docker"
	rootInstanceID      scope.InstanceID = "docker"
	maxDockerignoreSize                  = 4 * 1024 * 1024
)

// Provider evaluates membership in safely discoverable local Docker build
// contexts. It never invokes Docker or BuildKit.
type Provider struct{}

func New() *Provider {
	return &Provider{}
}

func (*Provider) ID() scope.ProviderID {
	return providerID
}

// Detect treats a repository-root .dockerignore as an explicit local context
// rooted at the repository. Dockerfiles alone do not establish a context.
func (*Provider) Detect(ctx context.Context, repo provider.Repository) (_ []provider.Instance, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("detect Docker context: %w", err)
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

	exists, err := pathExists(root, ".dockerignore")
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}

	content, err := readIgnoreFile(root, ".dockerignore")
	if err != nil {
		return []provider.Instance{rootInstance(nil, fmt.Sprintf("cannot read .dockerignore safely: %v", err))}, nil
	}
	patterns, err := ignorefile.ReadAll(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("parse .dockerignore: %w", err)
	}
	if _, err := patternmatcher.New(patterns); err != nil {
		return nil, fmt.Errorf("parse .dockerignore: %w", err)
	}

	return []provider.Instance{rootInstance(patterns, "")}, nil
}

func rootInstance(patterns []string, unavailable string) dockerInstance {
	return dockerInstance{
		descriptor: provider.InstanceDescriptor{
			Provider: providerID,
			ID:       rootInstanceID,
			Label:    "Docker context .",
		},
		contextRoot: ".",
		ignorePath:  ".dockerignore",
		patterns:    append([]string(nil), patterns...),
		unavailable: unavailable,
	}
}

func (*Provider) Evaluate(
	ctx context.Context,
	_ provider.EvaluationContext,
	_ provider.Repository,
	instance provider.Instance,
	paths []scope.Path,
) ([]scope.Result, error) {
	current, ok := instance.(dockerInstance)
	if !ok {
		return nil, errors.New("unsupported Docker instance")
	}
	descriptor := current.Descriptor()
	if descriptor.Provider != providerID || descriptor.ID == "" {
		return nil, fmt.Errorf("unsupported instance %q", descriptor.ID)
	}
	if len(paths) == 0 {
		return []scope.Result{}, nil
	}

	matchers, err := compileMatchers(current.patterns)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", current.ignorePath, err)
	}

	results := make([]scope.Result, 0, len(paths))
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("evaluate Docker context: %w", err)
		}
		state, reason, err := evaluatePath(current, matchers, name)
		if err != nil {
			return nil, fmt.Errorf("evaluate path %q: %w", name, err)
		}
		results = append(results, scope.Result{
			Path:        name,
			Provider:    providerID,
			Instance:    descriptor.ID,
			State:       state,
			Explanation: reason,
			Provenance: scope.Provenance{
				Method:    dockerProvenanceMethod(current, state),
				Reference: "github.com/moby/patternmatcher",
			},
		})
	}
	return results, nil
}

func dockerProvenanceMethod(instance dockerInstance, state scope.State) scope.ProvenanceMethod {
	if state == scope.Unknown && instance.unavailable != "" {
		return scope.Unavailable
	}
	return scope.SafeParser
}

type dockerInstance struct {
	descriptor  provider.InstanceDescriptor
	contextRoot scope.Path
	dockerfile  scope.Path
	ignorePath  scope.Path
	patterns    []string
	unavailable string
}

func (i dockerInstance) Descriptor() provider.InstanceDescriptor {
	return i.descriptor
}

type compiledMatchers struct {
	effective *patternmatcher.PatternMatcher
	rules     []compiledRule
}

type compiledRule struct {
	pattern   string
	exclusion bool
	matcher   *patternmatcher.PatternMatcher
}

func compileMatchers(patterns []string) (compiledMatchers, error) {
	effective, err := patternmatcher.New(patterns)
	if err != nil {
		return compiledMatchers{}, err
	}
	rules := make([]compiledRule, 0, len(effective.Patterns()))
	for _, parsed := range effective.Patterns() {
		matcher, err := patternmatcher.New([]string{parsed.String()})
		if err != nil {
			return compiledMatchers{}, err
		}
		pattern := filepath.ToSlash(parsed.String())
		if parsed.Exclusion() {
			pattern = "!" + pattern
		}
		rules = append(rules, compiledRule{
			pattern:   pattern,
			exclusion: parsed.Exclusion(),
			matcher:   matcher,
		})
	}
	return compiledMatchers{effective: effective, rules: rules}, nil
}

func evaluatePath(instance dockerInstance, matchers compiledMatchers, name scope.Path) (scope.State, scope.Explanation, error) {
	relative, applies := relativeToContext(name, instance.contextRoot)
	if !applies {
		return scope.NotApp, scope.Explanation{
			Code:    "docker/outside-context",
			Summary: fmt.Sprintf("outside Docker context %q", instance.contextRoot),
		}, nil
	}
	if relative == "." {
		return scope.In, scope.Explanation{
			Code:    "docker/context-root",
			Summary: "Docker context root",
		}, nil
	}
	if name == instance.ignorePath {
		return scope.In, scope.Explanation{
			Code:    "docker/context-metadata",
			Summary: "sent as Docker context metadata even when excluded",
		}, nil
	}
	if instance.dockerfile != "" && name == instance.dockerfile {
		return scope.In, scope.Explanation{
			Code:    "docker/dockerfile",
			Summary: "sent as the selected Dockerfile even when excluded",
		}, nil
	}
	if instance.unavailable != "" {
		return scope.Unknown, scope.Explanation{
			Code:    "docker/ignore-unreadable",
			Summary: instance.unavailable,
		}, nil
	}

	ignored, err := matchers.effective.MatchesOrParentMatches(relative)
	if err != nil {
		return "", scope.Explanation{}, err
	}
	last, err := lastMatchingRule(matchers.rules, relative)
	if err != nil {
		return "", scope.Explanation{}, err
	}
	if last == nil {
		if ignored {
			return "", scope.Explanation{}, errors.New("matcher reported exclusion without a matching rule")
		}
		return scope.In, scope.Explanation{
			Code:    "docker/included",
			Summary: "included in Docker context; no matching exclusion",
		}, nil
	}
	if ignored != !last.exclusion {
		return "", scope.Explanation{}, errors.New("matcher result and explanation disagree")
	}
	source := string(instance.ignorePath)
	if ignored {
		return scope.Out, scope.Explanation{
			Code:    "docker/excluded",
			Summary: fmt.Sprintf("excluded from Docker context by %s pattern %q", source, last.pattern),
		}, nil
	}
	return scope.In, scope.Explanation{
		Code:    "docker/included-negation",
		Summary: fmt.Sprintf("included in Docker context by %s pattern %q", source, last.pattern),
	}, nil
}

func lastMatchingRule(rules []compiledRule, name string) (*compiledRule, error) {
	var last *compiledRule
	for i := range rules {
		matched, err := rules[i].matcher.MatchesOrParentMatches(name)
		if err != nil {
			return nil, err
		}
		if matched {
			last = &rules[i]
		}
	}
	return last, nil
}

func relativeToContext(name, contextRoot scope.Path) (string, bool) {
	if contextRoot == "." {
		return string(name), true
	}
	if name == contextRoot {
		return ".", true
	}
	prefix := string(contextRoot) + "/"
	if !strings.HasPrefix(string(name), prefix) {
		return "", false
	}
	return strings.TrimPrefix(string(name), prefix), true
}

func pathExists(root *os.Root, name string) (bool, error) {
	info, err := root.Lstat(name)
	switch {
	case err == nil && info.IsDir():
		return false, fmt.Errorf("%s is a directory", name)
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("inspect %s: %w", name, err)
	}
}

func readIgnoreFile(root *os.Root, name string) (_ []byte, returnErr error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", name, err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("inspect %s: not a regular file", name)
	}
	if before.Size() > maxDockerignoreSize {
		return nil, fmt.Errorf("read %s: file exceeds %d bytes", name, maxDockerignoreSize)
	}
	file, err := root.Open(path.Clean(name))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close %s: %w", name, err))
		}
	}()
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened %s: %w", name, err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("inspect %s: file changed while it was being inspected", name)
	}

	content, err := io.ReadAll(io.LimitReader(file, maxDockerignoreSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(content) > maxDockerignoreSize {
		return nil, fmt.Errorf("read %s: file exceeds %d bytes", name, maxDockerignoreSize)
	}
	return content, nil
}
