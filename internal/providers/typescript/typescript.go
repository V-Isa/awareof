// Package typescript evaluates repository-level effective TypeScript Program membership.
package typescript

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

const (
	providerID     scope.ProviderID = "typescript"
	rootInstanceID scope.InstanceID = "typescript"
	typescriptTool safeexec.ToolID  = "typescript"
	nodeTool       safeexec.ToolID  = "node"
	reference                       = "https://www.typescriptlang.org/tsconfig/listFilesOnly.html"
)

type targetDiscoverer interface {
	Discover(string, safeexec.ToolID) (safeexec.Target, error)
}

type derivedTargetDiscoverer interface {
	DiscoverAt(string, safeexec.ToolID, string) (safeexec.Target, error)
	ApprovedTargets(string, safeexec.ToolID) ([]safeexec.Target, error)
}

var _ derivedTargetDiscoverer = (*safeexec.Manager)(nil)

// Provider evaluates membership in the union of safely resolved TypeScript Programs.
type Provider struct {
	runner     safeexec.CommandRunner
	discoverer targetDiscoverer
	goos       string
	goarch     string
	scanLimit  int
	lookPath   func(string) (string, error)
}

// New returns a TypeScript provider using the shared native-tool boundary.
func New(runner safeexec.CommandRunner, discoverer targetDiscoverer) *Provider {
	return &Provider{runner: runner, discoverer: discoverer, lookPath: exec.LookPath}
}

func (*Provider) ID() scope.ProviderID {
	return providerID
}

func (p *Provider) Evaluate(
	ctx context.Context,
	_ provider.EvaluationContext,
	repo provider.Repository,
	instance provider.Instance,
	paths []scope.Path,
) ([]scope.Result, error) {
	current, ok := instance.(typescriptInstance)
	if !ok {
		return nil, errors.New("unsupported TypeScript instance")
	}
	descriptor := current.Descriptor()
	if descriptor.Provider != providerID || descriptor.ID != rootInstanceID {
		return nil, fmt.Errorf("unsupported instance %q", descriptor.ID)
	}
	if len(paths) == 0 {
		return []scope.Result{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("evaluate TypeScript Programs: %w", err)
	}
	if p.runner == nil {
		return nil, errors.New("TypeScript command runner is nil")
	}
	if p.discoverer == nil {
		return nil, errors.New("TypeScript tool discoverer is nil")
	}

	evaluation := newUnionEvaluation(paths)
	queries, pathProblems, err := prepareQueryIndex(repo.Root, paths, p.platform().goos)
	if err != nil {
		return nil, fmt.Errorf("prepare TypeScript path queries: %w", err)
	}
	for name, explanation := range pathProblems {
		evaluation.addPathUnresolved(name, "query", explanation, typescriptTool)
	}
	if current.discovery != nil {
		evaluation.addUnresolved("discovery", *current.discovery, "")
	}
	queue := append([]scope.Path(nil), current.configs...)
	visited := make(map[scope.Path]struct{}, len(queue))
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("evaluate TypeScript Programs: %w", err)
		}
		config := queue[0]
		queue = queue[1:]
		if _, ok := visited[config]; ok {
			continue
		}
		visited[config] = struct{}{}

		project, unavailable, err := p.evaluateProject(ctx, repo.Root, config, queries)
		if err != nil {
			return nil, fmt.Errorf("evaluate TypeScript project %q: %w", config, err)
		}
		if project.included != nil {
			evaluation.addProject(config, project.included)
		}
		if unavailable != nil {
			evaluation.addUnresolved(string(config), *unavailable, project.unavailableTool)
			continue
		}
		queue = append(queue, project.references...)
	}

	return evaluation.results(descriptor.ID), nil
}

// SetupSelections reports native entry points relevant to the detected projects.
// It reads package metadata and approvals but does not execute a compiler.
func (p *Provider) SetupSelections(root string, instances []provider.Instance) ([]safeexec.Selection, error) {
	if len(instances) == 0 {
		return []safeexec.Selection{}, nil
	}
	selections := make(map[safeexec.Selection]struct{})
	typeScriptSelected := false
	if target, err := p.discoverer.Discover(root, typescriptTool); err == nil && target.Path != "" {
		selections[safeexec.Selection{Tool: typescriptTool, Path: target.Path}] = struct{}{}
		typeScriptSelected = true
	}
	for _, instance := range instances {
		current, ok := instance.(typescriptInstance)
		if !ok {
			continue
		}
		for _, config := range current.configs {
			installation, err := findProjectCompiler(root, config, p.platform())
			if err != nil {
				continue
			}
			if installation.major == 6 {
				selections[safeexec.Selection{Tool: nodeTool}] = struct{}{}
			}
			if typeScriptSelected {
				continue
			}
			candidates, err := p.evaluatorCandidates(root, installation)
			if err != nil {
				return nil, err
			}
			for _, candidate := range candidates {
				selections[safeexec.Selection{Tool: typescriptTool, Path: candidate.Path}] = struct{}{}
			}
		}
	}
	if !typeScriptSelected {
		hasCandidate := false
		for selection := range selections {
			if selection.Tool == typescriptTool {
				hasCandidate = true
				break
			}
		}
		if !hasCandidate {
			selections[safeexec.Selection{Tool: typescriptTool}] = struct{}{}
		}
	}
	result := make([]safeexec.Selection, 0, len(selections))
	for selection := range selections {
		result = append(result, selection)
	}
	slices.SortFunc(result, func(left, right safeexec.Selection) int {
		if left.Tool != right.Tool {
			return strings.Compare(string(left.Tool), string(right.Tool))
		}
		return strings.Compare(left.Path, right.Path)
	})
	return result, nil
}

type unionEvaluation struct {
	paths      []scope.Path
	in         map[scope.Path][]scope.Path
	unresolved []unresolvedProject
	pathIssues map[scope.Path][]unresolvedProject
}

type unresolvedProject struct {
	config      string
	explanation scope.Explanation
	tool        safeexec.ToolID
}

func newUnionEvaluation(paths []scope.Path) *unionEvaluation {
	return &unionEvaluation{
		paths:      append([]scope.Path(nil), paths...),
		in:         make(map[scope.Path][]scope.Path, len(paths)),
		pathIssues: make(map[scope.Path][]unresolvedProject),
	}
}

func (e *unionEvaluation) addProject(config scope.Path, included map[scope.Path]struct{}) {
	for _, name := range e.paths {
		if _, ok := included[name]; ok {
			e.in[name] = append(e.in[name], config)
		}
	}
}

func (e *unionEvaluation) addUnresolved(config string, explanation scope.Explanation, tool safeexec.ToolID) {
	e.unresolved = append(e.unresolved, unresolvedProject{config: config, explanation: explanation, tool: tool})
}

func (e *unionEvaluation) addPathUnresolved(name scope.Path, config string, explanation scope.Explanation, tool safeexec.ToolID) {
	e.pathIssues[name] = append(e.pathIssues[name], unresolvedProject{config: config, explanation: explanation, tool: tool})
}

func (e *unionEvaluation) results(instanceID scope.InstanceID) []scope.Result {
	slices.SortFunc(e.unresolved, compareUnresolved)
	for name := range e.pathIssues {
		slices.SortFunc(e.pathIssues[name], compareUnresolved)
	}
	results := make([]scope.Result, 0, len(e.paths))
	for _, name := range e.paths {
		configs := append([]scope.Path(nil), e.in[name]...)
		slices.Sort(configs)
		unresolved := append([]unresolvedProject(nil), e.unresolved...)
		unresolved = append(unresolved, e.pathIssues[name]...)
		state := scope.Out
		explanation := scope.Explanation{
			Code:    "typescript/not-program-member",
			Summary: "not part of any safely evaluated effective TypeScript Program",
		}
		provenance := scope.Provenance{Method: scope.SafeNative, Tool: string(typescriptTool)}
		switch {
		case len(configs) != 0:
			state = scope.In
			explanation = scope.Explanation{
				Code:     "typescript/program-member",
				Summary:  "part of at least one safely evaluated effective TypeScript Program",
				Evidence: pathList(configs),
			}
		case len(unresolved) != 0:
			state = scope.Unknown
			explanation = unresolvedExplanation(unresolved)
			provenance = scope.Provenance{Method: scope.Unavailable, Tool: string(unresolvedTool(unresolved)), Reference: reference}
		}
		results = append(results, scope.Result{
			Path:        name,
			Provider:    providerID,
			Instance:    instanceID,
			State:       state,
			Explanation: explanation,
			Provenance:  provenance,
		})
	}
	return results
}

func compareUnresolved(left, right unresolvedProject) int {
	if left.config != right.config {
		return strings.Compare(left.config, right.config)
	}
	return strings.Compare(left.explanation.Code, right.explanation.Code)
}

func unresolvedTool(unresolved []unresolvedProject) safeexec.ToolID {
	tool := unresolved[0].tool
	if tool == "" {
		return ""
	}
	for _, current := range unresolved[1:] {
		if current.tool != tool {
			return ""
		}
	}
	return tool
}

func unresolvedExplanation(unresolved []unresolvedProject) scope.Explanation {
	first := unresolved[0].explanation
	common := true
	for _, item := range unresolved {
		current := item.explanation
		if current.Code != first.Code || current.Summary != first.Summary || current.Action != first.Action {
			common = false
		}
	}
	details := make([]string, 0, len(unresolved))
	for _, item := range unresolved {
		current := item.explanation
		detail := item.config
		if current.Evidence != "" {
			detail += ": " + current.Evidence
		}
		if !common {
			detail += " (" + current.Summary + ")"
		}
		details = append(details, detail)
	}
	if common {
		return scope.Explanation{
			Code:     first.Code,
			Summary:  first.Summary,
			Evidence: summarizeDetails(details),
			Action:   first.Action,
		}
	}
	return scope.Explanation{
		Code:     "typescript/program-unresolved",
		Summary:  "effective TypeScript Program membership could not be established completely",
		Evidence: summarizeDetails(details),
	}
}

func summarizeDetails(details []string) string {
	const (
		maxDetails     = 3
		maxDetailBytes = 512
	)
	shown := details
	if len(shown) > maxDetails {
		shown = shown[:maxDetails]
	}
	bounded := make([]string, len(shown))
	for index, detail := range shown {
		bounded[index] = truncateText(detail, maxDetailBytes)
	}
	result := strings.Join(bounded, "; ")
	if len(details) > len(shown) {
		result += fmt.Sprintf("; and %d more", len(details)-len(shown))
	}
	return truncateText(result, 2048)
}

func truncateText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "..."
}

func pathList(paths []scope.Path) string {
	values := make([]string, len(paths))
	for index, name := range paths {
		values[index] = string(name)
	}
	return strings.Join(values, ", ")
}
