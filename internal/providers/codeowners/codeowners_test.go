package codeowners

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/contract"
	"github.com/V-Isa/awareof/internal/pathset"
	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/render"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestProviderDetectLocationsAndPrecedence(t *testing.T) {
	t.Parallel()
	if got := New().ID(); got != providerID {
		t.Fatalf("ID() = %q, want %q", got, providerID)
	}

	locations := []scope.Path{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}
	for _, location := range locations {
		t.Run(string(location), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, string(location), "* @owner\n")

			instances, err := New().Detect(context.Background(), provider.Repository{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			got := requireInstance(t, instances)
			if got.location != location || got.Descriptor() != descriptor(location) || len(got.rules) != 1 {
				t.Fatalf("Detect() instance = %+v, want location %q with one rule", got, location)
			}
		})
	}

	t.Run("first location wins", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		for _, location := range locations {
			writeFile(t, root, string(location), string(location)+" @owner\n")
		}
		instances, err := New().Detect(context.Background(), provider.Repository{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		got := requireInstance(t, instances)
		if got.location != ".github/CODEOWNERS" {
			t.Fatalf("location = %q, want .github/CODEOWNERS", got.location)
		}
	})

	t.Run("directory is not a CODEOWNERS file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".github", "CODEOWNERS"), 0o750); err != nil {
			t.Fatal(err)
		}
		writeFile(t, root, "CODEOWNERS", "* @root\n")
		instances, err := New().Detect(context.Background(), provider.Repository{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		if got := requireInstance(t, instances).location; got != "CODEOWNERS" {
			t.Fatalf("location = %q, want CODEOWNERS", got)
		}
	})

	t.Run("not configured", func(t *testing.T) {
		t.Parallel()
		instances, err := New().Detect(context.Background(), provider.Repository{Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if instances != nil {
			t.Fatalf("Detect() = %+v, want nil", instances)
		}
	})
}

func TestProviderDetectErrors(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name string
		ctx  context.Context
		repo provider.Repository
		want string
	}{
		{name: "empty root", ctx: context.Background(), repo: provider.Repository{}, want: "repository root is empty"},
		{name: "missing root", ctx: context.Background(), repo: provider.Repository{Root: filepath.Join(t.TempDir(), "missing")}, want: "open repository root"},
		{name: "cancelled", ctx: cancelled, repo: provider.Repository{Root: t.TempDir()}, want: "context canceled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := New().Detect(test.ctx, test.repo)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Detect() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestProviderDecisionTable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, ".github/CODEOWNERS", strings.Join([]string{
		"* @global",
		"/src/only.bin @exact",
		"/src/exact.go @exact",
		"src/generated/ @generated",
		"docs/* docs@example.com",
		"**/logs @logs",
		"*.js @javascript",
		"*.go @old",
		"*.go @new @org/team dev@example.com",
		"/apps/ @apps",
		"/apps/github",
		`space\ dir/** @space`,
		"file[ab].go @literal-brackets",
		"!secret @invalid-negation",
		"***/*.rb @invalid-pattern",
		"*.txt docs@",
		"*.md @docs # inline comment",
	}, "\n"))

	instances, err := New().Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	current := requireInstance(t, instances)
	if len(current.issues) != 3 {
		t.Fatalf("invalid lines = %+v, want 3", current.issues)
	}

	tests := []struct {
		path  scope.Path
		state scope.State
		code  string
		text  string
	}{
		{path: "README", state: scope.In, code: "codeowners/covered", text: "@global"},
		{path: "src/only.bin", state: scope.In, code: "codeowners/covered", text: "@exact"},
		{path: "src/exact.go", state: scope.In, code: "codeowners/covered", text: "@new @org/team dev@example.com"},
		{path: "src/generated/deep/file.bin", state: scope.In, code: "codeowners/covered", text: "@generated"},
		{path: "docs/getting-started.md", state: scope.In, code: "codeowners/covered", text: "@docs"},
		{path: "docs/build-app/troubleshooting.md", state: scope.In, code: "codeowners/covered", text: "@docs"},
		{path: "docs/build-app/file.bin", state: scope.In, code: "codeowners/covered", text: "@global"},
		{path: "build/logs/output.txt", state: scope.In, code: "codeowners/covered", text: "@logs"},
		{path: "future/file.js", state: scope.In, code: "codeowners/covered", text: "@javascript"},
		{path: "src/file.go", state: scope.In, code: "codeowners/covered", text: "@new @org/team dev@example.com"},
		{path: "apps/service/file", state: scope.In, code: "codeowners/covered", text: "@apps"},
		{path: "apps/github/file", state: scope.Out, code: "codeowners/unowned", text: "assigns no owners"},
		{path: "space dir/file", state: scope.In, code: "codeowners/covered", text: "@space"},
		{path: "file[ab].go", state: scope.In, code: "codeowners/covered", text: "@literal-brackets"},
		{path: "filea.go", state: scope.In, code: "codeowners/covered", text: "@new"},
		{path: "plain.txt", state: scope.In, code: "codeowners/covered", text: "@global"},
		{path: "SRC/EXACT.GO", state: scope.In, code: "codeowners/covered", text: "@global"},
		{path: "link.js", state: scope.In, code: "codeowners/covered", text: "@javascript"},
	}
	paths := make([]scope.Path, len(tests))
	for index, test := range tests {
		paths[index] = test.path
	}
	results, err := New().Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, current, paths)
	if err != nil {
		t.Fatal(err)
	}
	for index, result := range results {
		want := tests[index]
		if result.State != want.state || result.Explanation.Code != want.code || !strings.Contains(result.Explanation.Summary, want.text) {
			t.Errorf("result for %q = %+v, want %s %s containing %q", want.path, result, want.state, want.code, want.text)
		}
		if result.Provenance != (scope.Provenance{Method: scope.SafeParser, Reference: reference}) {
			t.Errorf("provenance for %q = %+v", want.path, result.Provenance)
		}
	}
}

func TestProviderReportsUncoveredAndSkippedLines(t *testing.T) {
	t.Parallel()
	current := codeownersInstance{
		descriptor: descriptor("CODEOWNERS"),
		location:   "CODEOWNERS",
		issues:     []parseIssue{{line: 1, reason: "invalid owner"}},
	}
	results, err := New().Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{}, current, []scope.Path{"future/file"})
	if err != nil {
		t.Fatal(err)
	}
	got := results[0]
	if got.State != scope.Out || got.Explanation.Code != "codeowners/uncovered" || !strings.Contains(got.Explanation.Summary, "1 invalid line skipped") {
		t.Fatalf("result = %+v, want uncovered with skipped-line note", got)
	}
}

func TestProviderEvaluationWorkLimit(t *testing.T) {
	t.Parallel()
	rules, issues := parseRules("first @one\nsecond @two\n")
	if len(issues) != 0 {
		t.Fatalf("parse issues = %+v", issues)
	}
	current := codeownersInstance{
		descriptor: descriptor("CODEOWNERS"),
		location:   "CODEOWNERS",
		rules:      rules,
	}
	remaining := 1
	state, explanation, err := current.evaluate(context.Background(), "unmatched", &remaining)
	if err != nil {
		t.Fatal(err)
	}
	if state != scope.Unknown || explanation.Code != "codeowners/evaluation-limit" || remaining != 0 {
		t.Fatalf("evaluate() = %s %+v with %d checks left, want UNKNOWN evaluation limit", state, explanation, remaining)
	}
	if got := current.provenance(state); got != (scope.Provenance{Method: scope.Unavailable, Reference: reference}) {
		t.Fatalf("provenance = %+v", got)
	}
}

func TestProviderEvaluationCancellationWithinRules(t *testing.T) {
	t.Parallel()
	rules, issues := parseRules("first @one\nsecond @two\n")
	if len(issues) != 0 {
		t.Fatalf("parse issues = %+v", issues)
	}
	current := codeownersInstance{rules: rules}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	remaining := contextCheckInterval
	_, _, err := current.lastMatch(cancelled, "unmatched", &remaining)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lastMatch() error = %v, want context canceled", err)
	}
}

func TestProviderUnavailableAndOversizedFiles(t *testing.T) {
	t.Parallel()

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "rules", "* @owner\n")
		if err := os.Symlink("rules", filepath.Join(root, "CODEOWNERS")); err != nil {
			if errors.Is(err, os.ErrPermission) {
				t.Skip("symlinks are unavailable")
			}
			t.Fatal(err)
		}
		assertDetectedState(t, root, scope.Unknown, "codeowners/file-unavailable", scope.Unavailable)
	})

	t.Run("invalid UTF-8", func(t *testing.T) {
		root := t.TempDir()
		writeBytes(t, root, "CODEOWNERS", []byte{'*', ' ', '@', 0xff})
		assertDetectedState(t, root, scope.Unknown, "codeowners/file-unavailable", scope.Unavailable)
	})

	t.Run("three megabytes is not under limit", func(t *testing.T) {
		root := t.TempDir()
		writeBytes(t, root, "CODEOWNERS", bytes.Repeat([]byte{'a'}, maxFileSize))
		assertDetectedState(t, root, scope.Out, "codeowners/file-too-large", scope.SafeParser)
	})
}

func TestProviderMatchesQueriedSymlinkByLogicalPath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	writeFile(t, root, "CODEOWNERS", "link @link-owner\n")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skip("symlinks are unavailable")
		}
		t.Fatal(err)
	}
	paths, err := (pathset.Builder{Root: root, Base: root}).Build(
		context.Background(),
		[]string{"link"},
		nil,
		pathset.BuildOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	results, err := provider.Evaluate(context.Background(), provider.Repository{Root: root}, paths, []provider.Provider{New()})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].State != scope.In || !strings.Contains(results[0].Explanation.Summary, "@link-owner") {
		t.Fatalf("results = %+v, want logical symlink path covered", results)
	}
}

func TestProviderContractAndHumanOutput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "CODEOWNERS", "src/** @payments-team\n")
	paths := []scope.Path{"src/payments/service.go", "legacy/file.go"}
	results, err := provider.Evaluate(context.Background(), provider.Repository{Root: root}, paths, []provider.Provider{New()})
	if err != nil {
		t.Fatal(err)
	}

	repositoryContract := contract.Contract{File: ".awareof.yaml", Rules: []contract.Rule{{
		Pattern: "src/**", Assertions: map[scope.ProviderID]scope.State{"codeowners": scope.In},
	}}}
	report, err := repositoryContract.Evaluate(paths, results)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid() || len(report.Checks) != 1 || report.Checks[0].Status != contract.Satisfied {
		t.Fatalf("contract report = %+v, want one satisfied assertion", report)
	}

	var output strings.Builder
	if err := render.Human(&output, paths, results); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"src/payments/service.go\n  codeowners       IN      @payments-team",
		"legacy/file.go\n  codeowners       OUT     no matching rule",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("Human() = %q, want containing %q", output.String(), want)
		}
	}
}

func TestProviderEvaluateErrorsAndEmptyPaths(t *testing.T) {
	t.Parallel()
	valid := codeownersInstance{descriptor: descriptor("CODEOWNERS"), location: "CODEOWNERS"}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name     string
		ctx      context.Context
		instance provider.Instance
		want     string
	}{
		{name: "wrong type", ctx: context.Background(), instance: provider.InstanceDescriptor{Provider: providerID, ID: rootInstanceID}, want: "unsupported CODEOWNERS instance"},
		{name: "wrong provider", ctx: context.Background(), instance: codeownersInstance{descriptor: provider.InstanceDescriptor{Provider: "git", ID: rootInstanceID}}, want: "unsupported instance"},
		{name: "empty ID", ctx: context.Background(), instance: codeownersInstance{descriptor: provider.InstanceDescriptor{Provider: providerID}}, want: "unsupported instance"},
		{name: "cancelled", ctx: cancelled, instance: valid, want: "context canceled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := New().Evaluate(test.ctx, provider.EvaluationContext{}, provider.Repository{}, test.instance, []scope.Path{"a"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Evaluate() error = %v, want containing %q", err, test.want)
			}
		})
	}

	results, err := New().Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{}, valid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("Evaluate(nil) = %+v, want non-nil empty", results)
	}
}

func TestParseRules(t *testing.T) {
	t.Parallel()
	content := "\ufeff# comment\r\n" +
		`docs/My\ File.md @docs docs@example.com # note` + "\r\n" +
		"/apps/github\n" +
		"*.txt invalid-owner\n" +
		"\\#cannot-be-escaped @owner\n" +
		"file#fragment @fragment-owner\n"
	rules, issues := parseRules(content)
	if len(issues) != 1 || issues[0].line != 4 || !strings.Contains(issues[0].reason, "invalid owner") {
		t.Fatalf("issues = %+v, want invalid owner on line 4", issues)
	}
	if len(rules) != 3 {
		t.Fatalf("rules = %+v, want 3", rules)
	}
	if rules[0].rawPattern != `docs/My\ File.md` || !reflect.DeepEqual(rules[0].owners, []string{"@docs", "docs@example.com"}) || rules[0].line != 2 {
		t.Fatalf("first rule = %+v", rules[0])
	}
	if !rules[0].matches("docs/My File.md") || rules[0].matches("docs/MyXFile.md") {
		t.Fatal("escaped space did not use literal semantics")
	}
	if len(rules[1].owners) != 0 || rules[1].line != 3 {
		t.Fatalf("ownerless rule = %+v", rules[1])
	}
	if rules[2].rawPattern != "file#fragment" || !reflect.DeepEqual(rules[2].owners, []string{"@fragment-owner"}) || !rules[2].matches("file#fragment") {
		t.Fatalf("hash pattern rule = %+v", rules[2])
	}
}

func TestOwnerSyntax(t *testing.T) {
	t.Parallel()
	tests := []struct {
		owner string
		valid bool
	}{
		{owner: "@user", valid: true},
		{owner: "@org/team-name", valid: true},
		{owner: "@org/team_name", valid: true},
		{owner: "dev@example.technology", valid: true},
		{owner: "dev@localhost", valid: true},
		{owner: "@", valid: false},
		{owner: "@org/team/child", valid: false},
		{owner: "docs@", valid: false},
		{owner: "plain", valid: false},
	}
	for _, test := range tests {
		t.Run(test.owner, func(t *testing.T) {
			t.Parallel()
			if got := validOwner(test.owner); got != test.valid {
				t.Fatalf("validOwner(%q) = %v, want %v", test.owner, got, test.valid)
			}
		})
	}
}

func FuzzParseRulesNeverPanics(f *testing.F) {
	f.Add("*.go @org/team\n")
	f.Add("*** @bad\n")
	f.Add("\xff")
	f.Fuzz(func(t *testing.T, content string) {
		rules, _ := parseRules(content)
		for _, current := range rules {
			_ = current.matches("path/to/file")
		}
	})
}

func assertDetectedState(t *testing.T, root string, state scope.State, code string, method scope.ProvenanceMethod) {
	t.Helper()
	instances, err := New().Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	results, err := New().Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], []scope.Path{"path"})
	if err != nil {
		t.Fatal(err)
	}
	got := results[0]
	if got.State != state || got.Explanation.Code != code || got.Provenance.Method != method {
		t.Fatalf("result = %+v, want %s %s with %s provenance", got, state, code, method)
	}
}

func requireInstance(t *testing.T, instances []provider.Instance) codeownersInstance {
	t.Helper()
	if len(instances) != 1 {
		t.Fatalf("instances = %+v, want exactly one", instances)
	}
	current, ok := instances[0].(codeownersInstance)
	if !ok {
		t.Fatalf("instance has type %T, want codeownersInstance", instances[0])
	}
	return current
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	writeBytes(t, root, name, []byte(content))
}

func writeBytes(t *testing.T, root, name string, content []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
