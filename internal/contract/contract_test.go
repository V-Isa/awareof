package contract

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/V-Isa/awareof/internal/scope"
)

func TestLoad(t *testing.T) {
	t.Parallel()

	t.Run("valid contract is normalized and sorted", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeContract(t, root, primaryContractFile, `version: 1
rules:
  ./src/**:
    git: in
  ".env*":
    docker: out
`)
		got, err := Load(root, []scope.ProviderID{"git", "docker"})
		if err != nil {
			t.Fatal(err)
		}
		want := Contract{File: primaryContractFile, Rules: []Rule{
			{Pattern: ".env*", Assertions: map[scope.ProviderID]scope.State{"docker": scope.Out}},
			{Pattern: "src/**", Assertions: map[scope.ProviderID]scope.State{"git": scope.In}},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Load() = %#v, want %#v", got, want)
		}
	})

	t.Run("yml fallback", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeContract(t, root, alternateContractFile, "version: 1\nrules: {}\n")
		got, err := Load(root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.File != alternateContractFile || got.Rules == nil || len(got.Rules) != 0 {
			t.Fatalf("Load() = %#v, want empty yml contract", got)
		}
	})

	tests := []struct {
		name  string
		setup func(*testing.T, string)
		want  string
	}{
		{name: "empty root", setup: func(*testing.T, string) {}, want: "repository root is empty"},
		{name: "missing root", setup: func(t *testing.T, root string) { t.Helper(); _ = os.Remove(root) }, want: "open repository root"},
		{name: "missing contract", setup: func(*testing.T, string) {}, want: "no contract found"},
		{name: "both names", setup: func(t *testing.T, root string) {
			writeContract(t, root, primaryContractFile, "version: 1\nrules: {}\n")
			writeContract(t, root, alternateContractFile, "version: 1\nrules: {}\n")
		}, want: "both .awareof.yaml and .awareof.yml"},
		{name: "directory contract", setup: func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, primaryContractFile), 0o750); err != nil {
				t.Fatal(err)
			}
		}, want: "must be a regular file"},
		{name: "oversized contract", setup: func(t *testing.T, root string) {
			writeContract(t, root, primaryContractFile, strings.Repeat("x", maxContractSize+1))
		}, want: "file exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			test.setup(t, root)
			loadRoot := root
			if test.name == "empty root" {
				loadRoot = ""
			}
			if _, err := Load(loadRoot, nil); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("symlink contract", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		target := filepath.Join(root, "contract-target")
		writeContract(t, root, "contract-target", "version: 1\nrules: {}\n")
		if err := os.Symlink(target, filepath.Join(root, primaryContractFile)); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink unavailable: %v", err)
			}
			t.Fatal(err)
		}
		if _, err := Load(root, nil); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("Load() error = %v, want symlink rejection", err)
		}
	})
}

func TestParseRejectsInvalidContracts(t *testing.T) {
	t.Parallel()
	known := map[scope.ProviderID]struct{}{"git": {}, "docker": {}}
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "invalid yaml", content: "rules: [", want: "did not find expected node content"},
		{name: "multiple documents", content: "version: 1\nrules: {}\n---\nversion: 1\n", want: "multiple YAML documents"},
		{name: "empty", content: "", want: "EOF"},
		{name: "sequence document", content: "[]\n", want: "document must be a mapping"},
		{name: "anchor", content: "version: &v 1\nrules: {}\n", want: "aliases, anchors"},
		{name: "alias", content: "version: 1\nrules: &r {}\ncopy: *r\n", want: "aliases, anchors"},
		{name: "custom tag", content: "!contract\nversion: 1\nrules: {}\n", want: "custom YAML tag"},
		{name: "unknown field", content: "version: 1\nrules: {}\nextra: true\n", want: "unknown document field"},
		{name: "missing version", content: "rules: {}\n", want: "missing required field \"version\""},
		{name: "missing rules", content: "version: 1\n", want: "missing required field \"rules\""},
		{name: "string version", content: "version: \"1\"\nrules: {}\n", want: "version must be integer 1"},
		{name: "future version", content: "version: 2\nrules: {}\n", want: "version must be integer 1"},
		{name: "rules sequence", content: "version: 1\nrules: []\n", want: "rules must be a mapping"},
		{name: "numeric pattern", content: "version: 1\nrules:\n  1: {}\n", want: "rules keys must be strings"},
		{name: "empty pattern", content: "version: 1\nrules:\n  \"\":\n    git: in\n", want: "pattern is empty"},
		{name: "escaping pattern", content: "version: 1\nrules:\n  ../secret:\n    git: out\n", want: "within the repository root"},
		{name: "absolute pattern", content: "version: 1\nrules:\n  /secret:\n    git: out\n", want: "relative to the repository root"},
		{name: "brace pattern", content: "version: 1\nrules:\n  \"{a,b}\":\n    git: in\n", want: "brace expansion"},
		{name: "duplicate normalized pattern", content: "version: 1\nrules:\n  src/**:\n    git: in\n  ./src/**:\n    git: in\n", want: "normalize to the same pattern"},
		{name: "assertions sequence", content: "version: 1\nrules:\n  src/**: []\n", want: "rule \"src/**\" must be a mapping"},
		{name: "empty assertions", content: "version: 1\nrules:\n  src/**: {}\n", want: "has no provider assertions"},
		{name: "numeric provider", content: "version: 1\nrules:\n  src/**:\n    1: in\n", want: "keys must be strings"},
		{name: "unknown provider", content: "version: 1\nrules:\n  src/**:\n    prettier: in\n", want: "unknown provider"},
		{name: "numeric state", content: "version: 1\nrules:\n  src/**:\n    git: 1\n", want: "state must be \"in\" or \"out\""},
		{name: "invalid state", content: "version: 1\nrules:\n  src/**:\n    git: IN\n", want: "got \"IN\""},
		{name: "duplicate root key", content: "version: 1\nversion: 1\nrules: {}\n", want: "duplicate key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parse(primaryContractFile, []byte(test.content), known); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestParseRejectsTooManyRules(t *testing.T) {
	t.Parallel()
	var content strings.Builder
	content.WriteString("version: 1\nrules:\n")
	for index := 0; index <= maxContractRules; index++ {
		content.WriteString("  path-")
		content.WriteString(strconv.Itoa(index))
		content.WriteString(":\n    git: in\n")
	}
	_, err := parse(primaryContractFile, []byte(content.String()), map[scope.ProviderID]struct{}{"git": {}})
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("parse() error = %v, want rule limit", err)
	}
}

func TestMatchingPaths(t *testing.T) {
	t.Parallel()
	current := Contract{Rules: []Rule{{Pattern: ".env*"}, {Pattern: "src/**"}}}
	got, err := current.MatchingPaths([]scope.Path{".env", "README.md", "src/app.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []scope.Path{".env", "src/app.go"}) {
		t.Fatalf("MatchingPaths() = %v", got)
	}
	if _, err := (Contract{Rules: []Rule{{Pattern: "["}}}).MatchingPaths([]scope.Path{"a"}); err == nil {
		t.Fatal("MatchingPaths() error = nil, want invalid internal pattern error")
	}
}

func TestEvaluateAndSpecificity(t *testing.T) {
	t.Parallel()
	current := Contract{File: primaryContractFile, Rules: []Rule{
		{Pattern: "**", Assertions: map[scope.ProviderID]scope.State{"git": scope.In}},
		{Pattern: "src/**", Assertions: map[scope.ProviderID]scope.State{"git": scope.In}},
		{Pattern: "src/generated/**", Assertions: map[scope.ProviderID]scope.State{"git": scope.Out}},
		{Pattern: "src/generated/keep.go", Assertions: map[scope.ProviderID]scope.State{"git": scope.In}},
	}}
	paths := []scope.Path{"src/app.go", "src/generated/file.go", "src/generated/keep.go"}
	results := []scope.Result{
		result("src/app.go", "git", "git", scope.In),
		result("src/generated/file.go", "git", "git", scope.Out),
		result("src/generated/keep.go", "git", "git", scope.In),
	}
	report, err := current.Evaluate(paths, results)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid() || len(report.Checks) != 3 {
		t.Fatalf("Evaluate() = %#v, want three satisfied checks", report)
	}
	wantPatterns := []string{"src/**", "src/generated/**", "src/generated/keep.go"}
	for index, want := range wantPatterns {
		if report.Checks[index].Pattern != want {
			t.Errorf("check %d pattern = %q, want %q", index, report.Checks[index].Pattern, want)
		}
	}
}

func TestEvaluateStatusesAndAggregation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		results []scope.Result
		want    Status
	}{
		{name: "all instances satisfy", results: []scope.Result{result("a", "git", "one", scope.In), result("a", "git", "two", scope.In)}, want: Satisfied},
		{name: "one mismatch fails", results: []scope.Result{result("a", "git", "one", scope.In), result("a", "git", "two", scope.Out)}, want: Mismatch},
		{name: "unknown prevents satisfaction", results: []scope.Result{result("a", "git", "one", scope.In), result("a", "git", "two", scope.Unknown)}, want: Unresolved},
		{name: "definite mismatch after unknown", results: []scope.Result{result("a", "git", "one", scope.Unknown), result("a", "git", "two", scope.Out)}, want: Mismatch},
		{name: "definite mismatch before unknown", results: []scope.Result{result("a", "git", "one", scope.Out), result("a", "git", "two", scope.Unknown)}, want: Mismatch},
		{name: "not applicable instance is ignored", results: []scope.Result{result("a", "git", "one", scope.NotApp), result("a", "git", "two", scope.In)}, want: Satisfied},
		{name: "only not applicable", results: []scope.Result{result("a", "git", "one", scope.NotApp)}, want: Unresolved},
		{name: "provider not detected", want: Unresolved},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			current := Contract{File: primaryContractFile, Rules: []Rule{{Pattern: "a", Assertions: map[scope.ProviderID]scope.State{"git": scope.In}}}}
			report, err := current.Evaluate([]scope.Path{"a"}, test.results)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Checks) != 1 || report.Checks[0].Status != test.want || report.Valid() != (test.want == Satisfied) {
				t.Fatalf("Evaluate() = %#v, want %s", report, test.want)
			}
			if len(report.Checks[0].Actual) != len(test.results) {
				t.Fatalf("actual results = %d, want %d", len(report.Checks[0].Actual), len(test.results))
			}
			for index, actual := range report.Checks[0].Actual {
				if actual.Instance != test.results[index].Instance || actual.State != test.results[index].State {
					t.Fatalf("actual result %d = %+v, want %+v", index, actual, test.results[index])
				}
			}
		})
	}
}

func TestEvaluateRejectsAmbiguousOverlap(t *testing.T) {
	t.Parallel()
	current := Contract{Rules: []Rule{
		{Pattern: "src/*/generated/**", Assertions: map[scope.ProviderID]scope.State{"git": scope.Out}},
		{Pattern: "src/api/**", Assertions: map[scope.ProviderID]scope.State{"git": scope.In}},
	}}
	if _, err := current.Evaluate([]scope.Path{"src/api/generated/a.go"}, nil); err == nil || !strings.Contains(err.Error(), "ambiguous git assertions") {
		t.Fatalf("Evaluate() error = %v, want ambiguity", err)
	}
	current.Rules[1].Assertions["git"] = scope.Out
	if _, err := current.Evaluate([]scope.Path{"src/api/generated/a.go"}, nil); err != nil {
		t.Fatalf("same-state overlap should be valid: %v", err)
	}
}

func TestRequiredProviders(t *testing.T) {
	t.Parallel()
	current := Contract{Rules: []Rule{
		{Pattern: "**", Assertions: map[scope.ProviderID]scope.State{"git": scope.In}},
		{Pattern: "src/**", Assertions: map[scope.ProviderID]scope.State{"docker": scope.Out}},
	}}
	got, err := current.RequiredProviders([]scope.Path{"src/a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []scope.ProviderID{"docker", "git"}) {
		t.Fatalf("RequiredProviders() = %v", got)
	}
	ambiguous := Contract{Rules: []Rule{
		{Pattern: "*", Assertions: map[scope.ProviderID]scope.State{"git": scope.In}},
		{Pattern: "?", Assertions: map[scope.ProviderID]scope.State{"git": scope.Out}},
	}}
	if _, err := ambiguous.RequiredProviders([]scope.Path{"a"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("RequiredProviders() error = %v, want ambiguity", err)
	}
}

func TestStatusValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status Status
		want   bool
	}{
		{name: "satisfied", status: Satisfied, want: true},
		{name: "mismatch", status: Mismatch, want: true},
		{name: "unresolved", status: Unresolved, want: true},
		{name: "empty", status: ""},
		{name: "invented", status: "SKIPPED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.status.Valid(); got != test.want {
				t.Fatalf("Status(%q).Valid() = %v, want %v", test.status, got, test.want)
			}
		})
	}
}

func TestReportValidEmpty(t *testing.T) {
	t.Parallel()
	if !(Report{}).Valid() {
		t.Fatal("empty report should pass")
	}
	contract := Contract{Rules: []Rule{{Pattern: "b"}, {Pattern: "a"}}}
	if got := contract.Patterns(); !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Fatalf("Patterns() = %v", got)
	}
}

func TestMappingRejectsDuplicateAndNonStringKeys(t *testing.T) {
	t.Parallel()
	duplicate := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "a"}, {},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "a"}, {},
	}}
	if _, err := mapping(duplicate, "test"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("mapping() error = %v, want duplicate", err)
	}
	nonString := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!int", Value: "1"}, {}}}
	if _, err := mapping(nonString, "test"); err == nil || !strings.Contains(err.Error(), "keys must be strings") {
		t.Fatalf("mapping() error = %v, want string key", err)
	}
}

func FuzzParseNeverPanics(f *testing.F) {
	f.Add("version: 1\nrules:\n  '**':\n    git: in\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, content string) {
		_, _ = parse(primaryContractFile, []byte(content), map[scope.ProviderID]struct{}{"git": {}})
	})
}

func result(name scope.Path, provider scope.ProviderID, instance scope.InstanceID, state scope.State) scope.Result {
	return scope.Result{
		Path: name, Provider: provider, Instance: instance, State: state,
		Explanation: scope.Explanation{Code: "test", Summary: "test explanation"},
		Provenance:  scope.Provenance{Method: scope.SafeParser},
	}
}

func writeContract(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
