// Package prettier evaluates repository-level Prettier file eligibility.
package prettier

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

const (
	providerID     scope.ProviderID = "prettier"
	rootInstanceID scope.InstanceID = "prettier"
	prettierTool   safeexec.ToolID  = "prettier"
	nodeTool       safeexec.ToolID  = "node"
	reference                       = "https://prettier.io/docs/api#getfileinfo"
)

type targetDiscoverer interface {
	Discover(string, safeexec.ToolID) (safeexec.Target, error)
}

type approvedTargetDiscoverer interface {
	ApprovedTargets(string, safeexec.ToolID) ([]safeexec.Target, error)
}

// Provider evaluates the union of detected Prettier package contexts.
type Provider struct {
	runner     safeexec.CommandRunner
	discoverer targetDiscoverer
	scanLimit  int
}

// New returns a Prettier provider using the shared native-tool boundary.
func New(runner safeexec.CommandRunner, discoverer targetDiscoverer) *Provider {
	return &Provider{runner: runner, discoverer: discoverer}
}

func (*Provider) ID() scope.ProviderID { return providerID }

func (p *Provider) Evaluate(
	ctx context.Context,
	_ provider.EvaluationContext,
	repo provider.Repository,
	instance provider.Instance,
	paths []scope.Path,
) ([]scope.Result, error) {
	current, ok := instance.(prettierInstance)
	if !ok {
		return nil, errors.New("unsupported Prettier instance")
	}
	descriptor := current.Descriptor()
	if descriptor.Provider != providerID || descriptor.ID != rootInstanceID {
		return nil, fmt.Errorf("unsupported instance %q", descriptor.ID)
	}
	if len(paths) == 0 {
		return []scope.Result{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("evaluate Prettier eligibility: %w", err)
	}
	if p.runner == nil {
		return nil, errors.New("prettier command runner is nil")
	}
	if p.discoverer == nil {
		return nil, errors.New("prettier tool discoverer is nil")
	}

	evaluation := newUnionEvaluation(paths)
	if current.discovery != nil {
		evaluation.addGlobalUnresolved("discovery", *current.discovery, "")
	}
	for _, packageContext := range current.contexts {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("evaluate Prettier eligibility: %w", err)
		}
		applicable := applicablePaths(packageContext.root, paths)
		if len(applicable) == 0 {
			continue
		}
		evaluation.addApplicable(applicable)
		facts, unavailable, tool, err := p.evaluateContext(ctx, repo.Root, packageContext, applicable)
		if err != nil {
			return nil, fmt.Errorf("evaluate Prettier context %q: %w", packageContext.root, err)
		}
		if unavailable != nil {
			evaluation.addContextUnresolved(applicable, string(packageContext.root), *unavailable, tool)
			continue
		}
		for name, fact := range facts {
			evaluation.addFact(name, packageContext.root, fact)
		}
	}
	return evaluation.results(descriptor.ID), nil
}

// SetupSelections reports native entry points required by detected contexts.
// Discovery reads files but does not execute Node or Prettier.
func (p *Provider) SetupSelections(root string, instances []provider.Instance) ([]safeexec.Selection, error) {
	if len(instances) == 0 {
		return []safeexec.Selection{}, nil
	}
	type projectRequirement struct {
		version  string
		identity string
	}
	requirements := make(map[projectRequirement]struct{})
	for _, instance := range instances {
		current, valid := instance.(prettierInstance)
		if !valid {
			continue
		}
		for _, packageContext := range current.contexts {
			project, err := loadProjectInstallation(root, packageContext)
			if err != nil {
				continue
			}
			identity, err := packageFingerprint(project)
			if err != nil {
				continue
			}
			requirements[projectRequirement{version: project.version, identity: identity}] = struct{}{}
		}
	}
	if len(requirements) == 0 {
		return []safeexec.Selection{}, nil
	}
	if p.discoverer == nil {
		return nil, errors.New("prettier tool discoverer is nil")
	}

	selections := map[safeexec.Selection]struct{}{{Tool: nodeTool}: {}}
	if target, err := p.discoverer.Discover(root, prettierTool); err == nil && target.Path != "" {
		selections[safeexec.Selection{Tool: prettierTool, Path: target.Path}] = struct{}{}
		return orderedSelections(selections), nil
	}

	matched := make(map[projectRequirement]struct{})
	approved, ok := p.discoverer.(approvedTargetDiscoverer)
	if ok {
		targets, err := approved.ApprovedTargets(root, prettierTool)
		if err != nil {
			return nil, fmt.Errorf("list approved Prettier evaluators: %w", err)
		}
		for _, target := range targets {
			evaluator, err := loadEvaluatorInstallation(target.Path)
			if err != nil {
				continue
			}
			identity, err := packageFingerprint(evaluator)
			if err != nil {
				continue
			}
			requirement := projectRequirement{version: evaluator.version, identity: identity}
			if _, needed := requirements[requirement]; !needed {
				continue
			}
			matched[requirement] = struct{}{}
			selections[safeexec.Selection{Tool: prettierTool, Path: target.Path}] = struct{}{}
		}
	}
	if len(matched) != len(requirements) {
		selections[safeexec.Selection{Tool: prettierTool}] = struct{}{}
	}
	return orderedSelections(selections), nil
}

func orderedSelections(selections map[safeexec.Selection]struct{}) []safeexec.Selection {
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
	return result
}

type eligibilityFact struct {
	in          bool
	code        string
	summary     string
	evidence    string
	config      string
	parser      string
	unavailable *scope.Explanation
}

type contextFact struct {
	root scope.Path
	fact eligibilityFact
}

type unresolvedContext struct {
	name        string
	explanation scope.Explanation
	tool        safeexec.ToolID
}

type unionEvaluation struct {
	paths      []scope.Path
	applicable map[scope.Path]bool
	facts      map[scope.Path][]contextFact
	global     []unresolvedContext
	unresolved map[scope.Path][]unresolvedContext
}

func newUnionEvaluation(paths []scope.Path) *unionEvaluation {
	return &unionEvaluation{
		paths:      append([]scope.Path(nil), paths...),
		applicable: make(map[scope.Path]bool, len(paths)),
		facts:      make(map[scope.Path][]contextFact, len(paths)),
		unresolved: make(map[scope.Path][]unresolvedContext),
	}
}

func (e *unionEvaluation) addApplicable(paths []scope.Path) {
	for _, name := range paths {
		e.applicable[name] = true
	}
}

func (e *unionEvaluation) addFact(name, root scope.Path, fact eligibilityFact) {
	e.facts[name] = append(e.facts[name], contextFact{root: root, fact: fact})
}

func (e *unionEvaluation) addGlobalUnresolved(name string, explanation scope.Explanation, tool safeexec.ToolID) {
	e.global = append(e.global, unresolvedContext{name: name, explanation: explanation, tool: tool})
}

func (e *unionEvaluation) addContextUnresolved(paths []scope.Path, name string, explanation scope.Explanation, tool safeexec.ToolID) {
	for _, path := range paths {
		e.unresolved[path] = append(e.unresolved[path], unresolvedContext{name: name, explanation: explanation, tool: tool})
	}
}

func (e *unionEvaluation) results(instance scope.InstanceID) []scope.Result {
	slices.SortFunc(e.global, func(left, right unresolvedContext) int {
		return strings.Compare(left.name, right.name)
	})
	for name := range e.unresolved {
		slices.SortFunc(e.unresolved[name], func(left, right unresolvedContext) int {
			return strings.Compare(left.name, right.name)
		})
	}
	results := make([]scope.Result, 0, len(e.paths))
	for _, name := range e.paths {
		facts := append([]contextFact(nil), e.facts[name]...)
		slices.SortFunc(facts, func(left, right contextFact) int {
			return strings.Compare(string(left.root), string(right.root))
		})
		result := scope.Result{
			Path: name, Provider: providerID, Instance: instance,
			State: scope.NotApp,
			Explanation: scope.Explanation{
				Code: "prettier/not-applicable", Summary: "no detected Prettier context applies to this path",
			},
			Provenance: scope.Provenance{Method: scope.SafeParser, Reference: reference},
		}
		if e.applicable[name] {
			result.State = scope.Out
			result.Explanation = scope.Explanation{
				Code: "prettier/not-eligible", Summary: "not eligible in any safely evaluated Prettier context",
			}
			result.Provenance = scope.Provenance{Method: scope.SafeNative, Tool: string(prettierTool), Reference: reference}
		}
		for _, current := range facts {
			if current.fact.in {
				result.State = scope.In
				result.Explanation = explanationForIn(current)
				break
			}
			if current.fact.unavailable != nil && result.State != scope.In {
				result.State = scope.Unknown
				result.Explanation = *current.fact.unavailable
				result.Provenance = scope.Provenance{Method: scope.Unavailable, Tool: string(prettierTool), Reference: reference}
				continue
			}
			if result.State == scope.Out && current.fact.code != "" {
				result.Explanation = scope.Explanation{
					Code: current.fact.code, Summary: current.fact.summary, Evidence: current.fact.evidence,
				}
			}
		}
		unresolved := append([]unresolvedContext(nil), e.global...)
		unresolved = append(unresolved, e.unresolved[name]...)
		if result.State != scope.In && (e.applicable[name] || len(e.global) != 0) && len(unresolved) != 0 {
			result.State = scope.Unknown
			result.Explanation = unresolvedExplanation(unresolved)
			result.Provenance = scope.Provenance{Method: scope.Unavailable, Tool: unresolvedTool(unresolved), Reference: reference}
		}
		results = append(results, result)
	}
	return results
}

func explanationForIn(current contextFact) scope.Explanation {
	details := []string{"context " + string(current.root)}
	if current.fact.config != "" {
		details = append(details, "config "+current.fact.config)
	}
	if current.fact.parser != "" {
		details = append(details, "parser "+current.fact.parser)
	}
	return scope.Explanation{
		Code: "prettier/eligible", Summary: "supported and not ignored by Prettier", Evidence: strings.Join(details, "; "),
	}
}

func unresolvedTool(items []unresolvedContext) string {
	for _, item := range items {
		if item.tool != "" {
			return string(item.tool)
		}
	}
	return ""
}

func unresolvedExplanation(items []unresolvedContext) scope.Explanation {
	first := items[0]
	if len(items) == 1 {
		return first.explanation
	}
	details := make([]string, 0, min(len(items), 4))
	for index, item := range items {
		if index == 3 {
			details = append(details, fmt.Sprintf("and %d more", len(items)-index))
			break
		}
		detail := item.name + ": " + item.explanation.Summary
		if item.explanation.Evidence != "" {
			detail += " (" + item.explanation.Evidence + ")"
		}
		details = append(details, detail)
	}
	return scope.Explanation{
		Code: "prettier/contexts-unresolved", Summary: "one or more applicable Prettier contexts could not be evaluated safely",
		Evidence: strings.Join(details, "; "), Action: first.explanation.Action,
	}
}
