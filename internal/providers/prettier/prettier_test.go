package prettier

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestUnionSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		configure  func(*unionEvaluation)
		wantStates []scope.State
	}{
		{
			name:       "no applicable context",
			configure:  func(*unionEvaluation) {},
			wantStates: []scope.State{scope.NotApp, scope.NotApp},
		},
		{
			name: "all resolved out",
			configure: func(e *unionEvaluation) {
				e.addApplicable([]scope.Path{"a.js", "b.txt"})
				e.addFact("a.js", ".", eligibilityFact{code: "prettier/ignored", summary: "ignored"})
				e.addFact("b.txt", ".", eligibilityFact{code: "prettier/unsupported", summary: "unsupported"})
			},
			wantStates: []scope.State{scope.Out, scope.Out},
		},
		{
			name: "in wins over unresolved context",
			configure: func(e *unionEvaluation) {
				e.addApplicable([]scope.Path{"a.js", "b.txt"})
				e.addFact("a.js", "web", eligibilityFact{in: true, parser: "babel"})
				e.addContextUnresolved([]scope.Path{"a.js", "b.txt"}, "tools", scope.Explanation{
					Code: "prettier/evaluator-failed", Summary: "failed",
				}, prettierTool)
			},
			wantStates: []scope.State{scope.In, scope.Unknown},
		},
		{
			name: "path-local unresolved stays local",
			configure: func(e *unionEvaluation) {
				e.addApplicable([]scope.Path{"a.js", "b.txt"})
				e.addFact("a.js", ".", unresolvedFact("prettier/path-uninspectable", "uninspectable", "a.js"))
				e.addFact("b.txt", ".", eligibilityFact{code: "prettier/unsupported", summary: "unsupported"})
			},
			wantStates: []scope.State{scope.Unknown, scope.Out},
		},
		{
			name: "incomplete discovery prevents not applicable",
			configure: func(e *unionEvaluation) {
				e.addGlobalUnresolved("discovery", scope.Explanation{
					Code: "prettier/discovery-unavailable", Summary: "incomplete",
				}, "")
			},
			wantStates: []scope.State{scope.Unknown, scope.Unknown},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			evaluation := newUnionEvaluation([]scope.Path{"a.js", "b.txt"})
			test.configure(evaluation)
			results := evaluation.results(rootInstanceID)
			for index, result := range results {
				if result.State != test.wantStates[index] {
					t.Errorf("result %s = %s, want %s", result.Path, result.State, test.wantStates[index])
				}
				if !result.Explanation.Valid() || !result.Provenance.Valid(result.State) {
					t.Errorf("invalid result: %#v", result)
				}
			}
		})
	}
}

func TestUnionSummarizesMultipleUnresolvedContexts(t *testing.T) {
	t.Parallel()
	evaluation := newUnionEvaluation([]scope.Path{"src/app.js"})
	evaluation.addApplicable([]scope.Path{"src/app.js"})
	evaluation.addContextUnresolved([]scope.Path{"src/app.js"}, "app", scope.Explanation{
		Code: "prettier/config-executable", Summary: "executable config", Action: "use a passive config",
	}, prettierTool)
	evaluation.addContextUnresolved([]scope.Path{"src/app.js"}, "tests", scope.Explanation{
		Code: "prettier/plugins-unsupported", Summary: "plugins configured", Action: "remove plugins",
	}, prettierTool)
	result := evaluation.results(rootInstanceID)[0]
	if result.State != scope.Unknown || result.Explanation.Code != "prettier/contexts-unresolved" {
		t.Fatalf("result = %#v", result)
	}
	for _, want := range []string{"app: executable config", "tests: plugins configured"} {
		if !strings.Contains(result.Explanation.Evidence, want) {
			t.Errorf("evidence = %q, missing %q", result.Explanation.Evidence, want)
		}
	}
}

func TestSetupSelections(t *testing.T) {
	t.Parallel()
	providerUnderTest := New(nil, nil)
	got, err := providerUnderTest.SetupSelections("/repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("SetupSelections(nil) = %v", got)
	}
	got, err = providerUnderTest.SetupSelections("/repo", []provider.Instance{prettierInstance{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("SetupSelections(unavailable package) = %v", got)
	}

	repository, resolver := evaluatorFixture(t, "3.9.9", "3.9.9")
	instance := prettierInstance{contexts: []prettierContext{{root: "."}}}
	if _, err := providerUnderTest.SetupSelections(repository, []provider.Instance{instance}); err == nil {
		t.Fatal("SetupSelections() succeeded without a tool discoverer")
	}
	resolver.errors = map[safeexec.ToolID]error{
		prettierTool: &safeexec.UnavailableError{Tool: prettierTool, Code: "tool/selection-required", Summary: "selection required"},
	}
	providerUnderTest.discoverer = resolver
	got, err = providerUnderTest.SetupSelections(repository, []provider.Instance{instance})
	if err != nil {
		t.Fatal(err)
	}
	want := []safeexec.Selection{{Tool: nodeTool}, {Tool: prettierTool}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("SetupSelections() = %v, want %v", got, want)
	}
}

func TestSetupSelectionsReportsEveryApprovedPackageIdentity(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	rootEvaluator := filepath.Join(base, "external-385", "node_modules", "prettier")
	webEvaluator := filepath.Join(base, "external-399", "node_modules", "prettier")
	writeFixtureFiles(t, repository, map[string]string{
		"package.json":                                    `{"devDependencies":{"prettier":"3.8.5"}}`,
		"node_modules/prettier/package.json":              `{"name":"prettier","version":"3.8.5"}`,
		"node_modules/prettier/index.mjs":                 "export {};",
		"packages/web/package.json":                       `{"devDependencies":{"prettier":"3.9.9"}}`,
		"packages/web/node_modules/prettier/package.json": `{"name":"prettier","version":"3.9.9"}`,
		"packages/web/node_modules/prettier/index.mjs":    "export {};",
	})
	writeFixtureFiles(t, rootEvaluator, map[string]string{
		"package.json": `{"name":"prettier","version":"3.8.5"}`, "index.mjs": "export {};",
	})
	writeFixtureFiles(t, webEvaluator, map[string]string{
		"package.json": `{"name":"prettier","version":"3.9.9"}`, "index.mjs": "export {};",
	})
	resolver := fakeResolver{
		errors: map[safeexec.ToolID]error{
			prettierTool: &safeexec.UnavailableError{Tool: prettierTool, Code: "tool/selection-required", Summary: "selection required"},
		},
		approved: map[safeexec.ToolID][]safeexec.Target{prettierTool: {
			{Tool: prettierTool, Path: filepath.Join(rootEvaluator, "index.mjs"), Origin: safeexec.ExternalOrigin},
			{Tool: prettierTool, Path: filepath.Join(webEvaluator, "index.mjs"), Origin: safeexec.ExternalOrigin},
		}},
	}
	providerUnderTest := New(nil, resolver)
	instance := prettierInstance{contexts: []prettierContext{{root: "."}, {root: "packages/web"}}}
	got, err := providerUnderTest.SetupSelections(repository, []provider.Instance{instance})
	if err != nil {
		t.Fatal(err)
	}
	want := []safeexec.Selection{
		{Tool: nodeTool},
		{Tool: prettierTool, Path: filepath.Join(rootEvaluator, "index.mjs")},
		{Tool: prettierTool, Path: filepath.Join(webEvaluator, "index.mjs")},
	}
	if len(got) != len(want) {
		t.Fatalf("SetupSelections() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("SetupSelections() = %v, want %v", got, want)
		}
	}
}
