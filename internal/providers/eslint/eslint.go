package eslint

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
	providerID     scope.ProviderID = "eslint"
	rootInstanceID scope.InstanceID = "eslint"
	nodeTool       safeexec.ToolID  = "node"
	reference                       = "https://eslint.org/docs/latest/integrate/nodejs-api"
)

type targetResolver interface {
	Resolve(string, safeexec.ToolID) (safeexec.Target, error)
}

// Provider evaluates effective ESLint file eligibility.
type Provider struct {
	runner    safeexec.CommandRunner
	resolver  targetResolver
	scanLimit int
}

// New returns an ESLint provider using the shared execution boundary.
func New(runner safeexec.CommandRunner, resolver targetResolver) *Provider {
	return &Provider{runner: runner, resolver: resolver}
}

// ID returns the stable provider ID.
func (p *Provider) ID() scope.ProviderID { return providerID }

// RepositoryCodePermission marks ESLint as requiring explicit authority to
// load its repository-controlled package and effective configuration.
func (*Provider) RepositoryCodePermission() {}

// SetupSelections reports the external native tools relevant to detected
// ESLint contexts. It does not execute ESLint or repository code.
func (p *Provider) SetupSelections(root string, instances []provider.Instance) []safeexec.Selection {
	for _, instance := range instances {
		current, ok := instance.(eslintInstance)
		if !ok {
			continue
		}
		for _, eslintContext := range current.contexts {
			if eslintContext.config == "" || eslintContext.unavailable != nil {
				continue
			}
			if _, err := loadProjectInstallation(root, eslintContext); err == nil {
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
	current, ok := instance.(eslintInstance)
	if !ok {
		return nil, errors.New("unsupported ESLint instance")
	}
	descriptor := current.Descriptor()
	if descriptor.Provider != providerID || descriptor.ID != rootInstanceID {
		return nil, fmt.Errorf("unsupported instance %q", descriptor.ID)
	}
	if len(paths) == 0 {
		return []scope.Result{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("evaluate ESLint eligibility: %w", err)
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
	for _, eslintContext := range current.contexts {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("evaluate ESLint eligibility: %w", err)
		}
		applicable := applicablePaths(eslintContext.root, paths)
		if len(applicable) == 0 {
			continue
		}
		union.addApplicable(applicable)
		preflightFacts, evaluable := preflightPaths(repo.Root, eslintContext, applicable)
		for name, fact := range preflightFacts {
			union.addFact(name, eslintContext.root, fact)
		}
		if len(evaluable) == 0 {
			continue
		}
		if eslintContext.unavailable != nil {
			union.addUnknown(evaluable, eslintContext.root, *eslintContext.unavailable, "")
			continue
		}
		if !evaluationContext.RepoCodeAllowed(providerID) {
			union.addUnknown(evaluable, eslintContext.root, scope.Explanation{
				Code:     "eslint/repository-code-not-allowed",
				Summary:  "effective ESLint configuration requires repository code execution",
				Evidence: configEvidence(eslintContext),
				Action:   "rerun with --allow-repo-code=eslint only if you trust this repository",
			}, "")
			continue
		}
		facts, unavailable, tool, err := p.evaluateContext(ctx, repo.Root, eslintContext, evaluable)
		if err != nil {
			return nil, fmt.Errorf("evaluate ESLint context %q: %w", eslintContext.root, err)
		}
		if unavailable != nil {
			union.addUnknown(evaluable, eslintContext.root, *unavailable, tool)
			continue
		}
		for name, fact := range facts {
			union.addFact(name, eslintContext.root, fact)
		}
	}
	return union.results(descriptor.ID), nil
}

type eligibilityFact struct {
	in          bool
	code        string
	summary     string
	config      scope.Path
	method      scope.ProvenanceMethod
	unavailable *scope.Explanation
	tool        safeexec.ToolID
}

type contextFact struct {
	context scope.Path
	fact    eligibilityFact
}

type unionEvaluation struct {
	paths      []scope.Path
	applicable map[scope.Path]bool
	facts      map[scope.Path][]contextFact
	global     []eligibilityFact
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

func (e *unionEvaluation) addFact(name, root scope.Path, fact eligibilityFact) {
	e.facts[name] = append(e.facts[name], contextFact{context: root, fact: fact})
}

func (e *unionEvaluation) addUnknown(paths []scope.Path, root scope.Path, explanation scope.Explanation, tool safeexec.ToolID) {
	for _, name := range paths {
		explanationCopy := explanation
		e.addFact(name, root, eligibilityFact{unavailable: &explanationCopy, tool: tool})
	}
}

func (e *unionEvaluation) addGlobalUnknown(explanation scope.Explanation, tool safeexec.ToolID) {
	explanationCopy := explanation
	e.global = append(e.global, eligibilityFact{unavailable: &explanationCopy, tool: tool})
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
				Code: "eslint/selected", Summary: "selected by effective ESLint configuration",
				Evidence: selectedEvidence(facts),
			}
			result.Provenance = scope.Provenance{Method: scope.SafeNative, Tool: string(nodeTool), Reference: reference}
		case hasUnknown(facts):
			unknown := firstUnknown(facts)
			result.State = scope.Unknown
			result.Explanation = *unknown.fact.unavailable
			result.Provenance = scope.Provenance{Method: scope.Unavailable, Tool: string(unknown.fact.tool), Reference: reference}
		case !e.applicable[name]:
			result.State = scope.NotApp
			result.Explanation = scope.Explanation{Code: "eslint/not-applicable", Summary: "outside every detected ESLint context"}
			result.Provenance = scope.Provenance{Method: scope.SafeParser, Reference: reference}
		default:
			result.State = scope.Out
			result.Explanation = scope.Explanation{
				Code: outCode(facts), Summary: "not selected by any resolved ESLint context",
				Evidence: outEvidence(facts),
			}
			result.Provenance = resolvedProvenance(facts)
		}
		results = append(results, result)
	}
	return results
}

func outCode(facts []contextFact) string {
	code := ""
	for _, fact := range facts {
		if fact.fact.unavailable != nil || fact.fact.code == "" {
			continue
		}
		if code == "" {
			code = fact.fact.code
			continue
		}
		if code != fact.fact.code {
			return "eslint/not-selected"
		}
	}
	if code == "" {
		return "eslint/not-selected"
	}
	return code
}

func resolvedProvenance(facts []contextFact) scope.Provenance {
	for _, fact := range facts {
		if fact.fact.method == scope.SafeNative {
			return scope.Provenance{Method: scope.SafeNative, Tool: string(nodeTool), Reference: reference}
		}
	}
	return scope.Provenance{Method: scope.SafeParser, Reference: reference}
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
	contexts := make([]string, 0, len(facts))
	for _, fact := range facts {
		if !fact.fact.in {
			continue
		}
		contexts = append(contexts, contextAndConfig(fact))
	}
	slices.Sort(contexts)
	return "contexts: " + strings.Join(contexts, ", ")
}

func outEvidence(facts []contextFact) string {
	entries := make([]string, 0, len(facts))
	for _, fact := range facts {
		if fact.fact.unavailable != nil {
			continue
		}
		entries = append(entries, contextAndConfig(fact)+": "+fact.fact.summary)
	}
	slices.Sort(entries)
	return strings.Join(entries, "; ")
}

func contextAndConfig(fact contextFact) string {
	value := string(fact.context)
	if fact.fact.config != "" {
		value += " (" + string(fact.fact.config) + ")"
	}
	return value
}

func configEvidence(current eslintContext) string {
	if current.config == "" {
		return "context: " + string(current.root)
	}
	return "config: " + string(current.config)
}
