// Package jest evaluates repository-level effective Jest test-file membership.
package jest

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
	providerID     scope.ProviderID = "jest"
	rootInstanceID scope.InstanceID = "jest"
	nodeTool       safeexec.ToolID  = "node"
	reference                       = "https://jestjs.io/docs/cli#--listtests"
)

type targetResolver interface {
	Resolve(string, safeexec.ToolID) (safeexec.Target, error)
}

// Provider evaluates effective Jest test-file membership.
type Provider struct {
	runner    safeexec.CommandRunner
	resolver  targetResolver
	scanLimit int
}

// New returns a Jest provider using the shared execution boundary.
func New(runner safeexec.CommandRunner, resolver targetResolver) *Provider {
	return &Provider{runner: runner, resolver: resolver}
}

// ID returns the stable provider ID.
func (*Provider) ID() scope.ProviderID { return providerID }

// RepositoryCodePermission marks Jest as requiring explicit authority to load
// its repository-local package, configuration, presets, and discovery modules.
func (*Provider) RepositoryCodePermission() {}

// SetupSelections reports the approved external Node tool needed by runnable
// Jest contexts. It does not execute Jest or repository code.
func (p *Provider) SetupSelections(root string, instances []provider.Instance) []safeexec.Selection {
	for _, instance := range instances {
		current, ok := instance.(jestInstance)
		if !ok {
			continue
		}
		for _, jestContext := range current.contexts {
			if _, _, problem := prepareContext(root, jestContext); problem == nil {
				return []safeexec.Selection{{Tool: nodeTool}}
			}
		}
	}
	return []safeexec.Selection{}
}

// Evaluate returns one repository-level union result per path.
func (p *Provider) Evaluate(
	ctx context.Context,
	evaluationContext provider.EvaluationContext,
	repo provider.Repository,
	instance provider.Instance,
	paths []scope.Path,
) ([]scope.Result, error) {
	current, ok := instance.(jestInstance)
	if !ok {
		return nil, errors.New("unsupported Jest instance")
	}
	descriptor := current.Descriptor()
	if descriptor.Provider != providerID || descriptor.ID != rootInstanceID {
		return nil, fmt.Errorf("unsupported instance %q", descriptor.ID)
	}
	if len(paths) == 0 {
		return []scope.Result{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("evaluate Jest test-file membership: %w", err)
	}

	union := newUnionEvaluation(paths)
	if current.discovery != nil {
		union.addGlobalUnknown(*current.discovery, "")
	}
	for _, issue := range current.issues {
		affected := applicablePaths(issue.root, paths)
		if len(affected) == 0 {
			continue
		}
		union.addApplicable(affected)
		union.addUnknown(affected, issue.root, discoveryExplanation(issue), "")
	}
	for _, jestContext := range current.contexts {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("evaluate Jest test-file membership: %w", err)
		}
		applicable := applicablePaths(jestContext.root, paths)
		if len(applicable) == 0 {
			continue
		}
		union.addApplicable(applicable)
		preflightFacts, evaluable := preflightPaths(repo.Root, applicable)
		for name, fact := range preflightFacts {
			union.addFact(name, jestContext.root, fact)
		}
		if len(evaluable) == 0 {
			continue
		}
		installation, config, problem := prepareContext(repo.Root, jestContext)
		if problem != nil {
			union.addUnknown(evaluable, jestContext.root, *problem, "")
			continue
		}
		if !evaluationContext.RepoCodeAllowed(providerID) {
			union.addUnknown(evaluable, jestContext.root, scope.Explanation{
				Code: "jest/repository-code-not-allowed", Summary: "effective Jest test discovery requires repository code execution",
				Evidence: config.evidence, Action: "rerun with --allow-repo-code=jest only if you trust this repository",
			}, "")
			continue
		}
		facts, unavailable, tool, err := p.evaluateContext(ctx, repo.Root, jestContext, installation, config, evaluable)
		if err != nil {
			return nil, fmt.Errorf("evaluate Jest context %q: %w", jestContext.root, err)
		}
		if unavailable != nil {
			union.addUnknown(evaluable, jestContext.root, *unavailable, tool)
			continue
		}
		for name, fact := range facts {
			union.addFact(name, jestContext.root, fact)
		}
	}
	return union.results(descriptor.ID), nil
}

type membershipFact struct {
	in          bool
	summary     string
	evidence    string
	method      scope.ProvenanceMethod
	unavailable *scope.Explanation
	tool        safeexec.ToolID
}

type contextFact struct {
	context scope.Path
	fact    membershipFact
}

type unionEvaluation struct {
	paths      []scope.Path
	applicable map[scope.Path]bool
	facts      map[scope.Path][]contextFact
	global     []membershipFact
}

func newUnionEvaluation(paths []scope.Path) *unionEvaluation {
	return &unionEvaluation{
		paths: append([]scope.Path(nil), paths...), applicable: make(map[scope.Path]bool, len(paths)),
		facts: make(map[scope.Path][]contextFact, len(paths)),
	}
}

func (e *unionEvaluation) addApplicable(paths []scope.Path) {
	for _, name := range paths {
		e.applicable[name] = true
	}
}

func (e *unionEvaluation) addFact(name, root scope.Path, fact membershipFact) {
	e.facts[name] = append(e.facts[name], contextFact{context: root, fact: fact})
}

func (e *unionEvaluation) addUnknown(paths []scope.Path, root scope.Path, explanation scope.Explanation, tool safeexec.ToolID) {
	for _, name := range paths {
		explanationCopy := explanation
		e.addFact(name, root, membershipFact{unavailable: &explanationCopy, tool: tool})
	}
}

func (e *unionEvaluation) addGlobalUnknown(explanation scope.Explanation, tool safeexec.ToolID) {
	explanationCopy := explanation
	e.global = append(e.global, membershipFact{unavailable: &explanationCopy, tool: tool})
}

func (e *unionEvaluation) results(instance scope.InstanceID) []scope.Result {
	results := make([]scope.Result, 0, len(e.paths))
	for _, name := range e.paths {
		facts := append([]contextFact(nil), e.facts[name]...)
		for _, global := range e.global {
			facts = append(facts, contextFact{context: ".", fact: global})
		}
		result := scope.Result{Path: name, Provider: providerID, Instance: instance}
		switch {
		case hasIn(facts):
			result.State = scope.In
			result.Explanation = scope.Explanation{
				Code: "jest/test-file", Summary: "selected as an effective Jest test file", Evidence: selectedEvidence(facts),
			}
			result.Provenance = scope.Provenance{Method: scope.SafeNative, Tool: string(nodeTool), Reference: reference}
		case hasUnknown(facts):
			unknown := firstUnknown(facts)
			result.State = scope.Unknown
			result.Explanation = *unknown.fact.unavailable
			result.Provenance = scope.Provenance{Method: scope.Unavailable, Tool: string(unknown.fact.tool), Reference: reference}
		case !e.applicable[name]:
			result.State = scope.NotApp
			result.Explanation = scope.Explanation{Code: "jest/not-applicable", Summary: "outside every detected Jest context"}
			result.Provenance = scope.Provenance{Method: scope.SafeParser, Reference: reference}
		default:
			result.State = scope.Out
			result.Explanation = scope.Explanation{
				Code: "jest/not-test-file", Summary: "not selected as a test file by any resolved Jest context", Evidence: outEvidence(facts),
			}
			result.Provenance = resolvedProvenance(facts)
		}
		results = append(results, result)
	}
	return results
}

func hasIn(facts []contextFact) bool {
	return slices.ContainsFunc(facts, func(fact contextFact) bool { return fact.fact.in })
}

func hasUnknown(facts []contextFact) bool {
	return slices.ContainsFunc(facts, func(fact contextFact) bool { return fact.fact.unavailable != nil })
}

func firstUnknown(facts []contextFact) contextFact {
	for _, fact := range facts {
		if fact.fact.unavailable != nil {
			return fact
		}
	}
	return contextFact{}
}

func selectedEvidence(facts []contextFact) string {
	details := []string{}
	for _, fact := range facts {
		if fact.fact.in {
			details = append(details, "context "+string(fact.context)+": "+fact.fact.evidence)
		}
	}
	return strings.Join(details, "; ")
}

func outEvidence(facts []contextFact) string {
	details := []string{}
	for _, fact := range facts {
		if fact.fact.unavailable == nil && fact.fact.summary != "" {
			details = append(details, "context "+string(fact.context)+": "+fact.fact.summary)
		}
	}
	return strings.Join(details, "; ")
}

func resolvedProvenance(facts []contextFact) scope.Provenance {
	for _, fact := range facts {
		if fact.fact.method == scope.SafeNative {
			return scope.Provenance{Method: scope.SafeNative, Tool: string(nodeTool), Reference: reference}
		}
	}
	return scope.Provenance{Method: scope.SafeParser, Reference: reference}
}
