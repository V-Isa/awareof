package prettier

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

type fakeResolver struct {
	targets  map[safeexec.ToolID]safeexec.Target
	errors   map[safeexec.ToolID]error
	approved map[safeexec.ToolID][]safeexec.Target
}

func (r fakeResolver) ApprovedTargets(_ string, tool safeexec.ToolID) ([]safeexec.Target, error) {
	return append([]safeexec.Target(nil), r.approved[tool]...), nil
}

func (r fakeResolver) Discover(_ string, tool safeexec.ToolID) (safeexec.Target, error) {
	return r.Resolve("", tool)
}

func (r fakeResolver) Resolve(_ string, tool safeexec.ToolID) (safeexec.Target, error) {
	if err := r.errors[tool]; err != nil {
		return safeexec.Target{}, err
	}
	return r.targets[tool], nil
}

type fakeRunner struct {
	response safeexec.Response
	err      error
	onRun    func(safeexec.Request)
	requests []safeexec.Request
}

func (r *fakeRunner) Run(_ context.Context, request safeexec.Request) (safeexec.Response, error) {
	r.requests = append(r.requests, request)
	if r.onRun != nil {
		r.onRun(request)
	}
	return r.response, r.err
}

func TestEvaluateContextUnion(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, "3.9.9", "3.9.9")
	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte(`{"results":[
		{"index":0,"ignored":false,"parser":"babel","config":""},
		{"index":1,"ignored":true,"parser":"babel","config":""},
		{"index":2,"ignored":false,"parser":"","config":""}
	]}`)}}
	providerUnderTest := New(runner, resolver)
	instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	paths := []scope.Path{"src/app.js", "generated/app.js", "README.unknown"}
	results, err := providerUnderTest.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instances[0], paths)
	if err != nil {
		t.Fatal(err)
	}
	want := []scope.State{scope.In, scope.Out, scope.Out}
	for index, result := range results {
		if result.State != want[index] {
			t.Errorf("result %s = %s, want %s", result.Path, result.State, want[index])
		}
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner calls = %d, want one batched call", len(runner.requests))
	}
	request := runner.requests[0]
	if request.Tool != nodeTool || !request.ExternalOnly || len(request.Args) != 1 || request.Dir == repository {
		t.Fatalf("unsafe evaluator request: %#v", request)
	}
	helperRelative, err := filepath.Rel(request.Dir, request.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if helperRelative != filepath.Join("..", "awareof-prettier-helper.mjs") {
		t.Fatalf("helper path relative to repository mirror = %q, want parent directory", helperRelative)
	}
	for _, name := range []string{
		"FORCE_COLOR", "NODE_COMPILE_CACHE", "NODE_OPTIONS", "NODE_PATH", "NODE_PRESERVE_SYMLINKS",
		"NODE_REDIRECT_WARNINGS", "NODE_REPORT_DIRECTORY", "NODE_V8_COVERAGE",
	} {
		if !containsString(request.UnsetEnv, name) {
			t.Errorf("unset environment = %v, missing %s", request.UnsetEnv, name)
		}
	}
	if request.Env["NODE_DISABLE_COMPILE_CACHE"] != "1" || request.Env["NODE_ENV"] != "production" || request.Env["NO_COLOR"] != "1" {
		t.Errorf("environment = %v", request.Env)
	}
	var protocol evaluatorRequest
	if err := json.Unmarshal(request.Stdin, &protocol); err != nil {
		t.Fatal(err)
	}
	if len(protocol.Paths) != len(paths) {
		t.Fatalf("protocol paths = %d, want %d", len(protocol.Paths), len(paths))
	}
}

func TestEvaluatePathUncertaintyIsLocal(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, "3.9.9", "3.9.9")
	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte(`{"results":[{"index":0,"parser":"babel"}]}`)}}
	providerUnderTest := New(runner, resolver)
	instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	results, err := providerUnderTest.Evaluate(
		context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instances[0],
		[]scope.Path{"src/app.js", "src/line\nbreak.js"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.In || results[1].State != scope.Unknown || results[1].Explanation.Code != "prettier/path-unrepresentable" {
		t.Fatalf("results = %#v", results)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner calls = %d, want one", len(runner.requests))
	}
}

func TestEvaluateRejectsPartialOrInvalidEvaluation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		response safeexec.Response
		wantCode string
	}{
		{
			name: "nonzero with partial stdout", response: safeexec.Response{
				ExitCode: 1, Stdout: []byte(`{"results":[{"index":0,"parser":"babel"}]}`), Stderr: []byte("failed"),
			}, wantCode: "prettier/evaluator-failed",
		},
		{name: "invalid output", response: safeexec.Response{Stdout: []byte(`{"results":[]}`)}, wantCode: "prettier/evaluator-output-invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, resolver := evaluatorFixture(t, "3.9.9", "3.9.9")
			providerUnderTest := New(&fakeRunner{response: test.response}, resolver)
			instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: repository})
			if err != nil {
				t.Fatal(err)
			}
			results, err := providerUnderTest.Evaluate(
				context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instances[0], []scope.Path{"src/app.js"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if results[0].State != scope.Unknown || results[0].Explanation.Code != test.wantCode {
				t.Fatalf("result = %#v, want UNKNOWN %s", results[0], test.wantCode)
			}
		})
	}
}

func TestEvaluateDetectsChangingInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		prepare  func(*testing.T, string, fakeResolver) func(safeexec.Request)
		wantCode string
	}{
		{
			name: "config", wantCode: "prettier/config-changed",
			prepare: func(t *testing.T, repository string, _ fakeResolver) func(safeexec.Request) {
				t.Helper()
				config := filepath.Join(repository, ".prettierrc.json")
				if err := os.WriteFile(config, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return func(safeexec.Request) {
					if err := os.WriteFile(config, []byte("{\"singleQuote\":true}\n"), 0o600); err != nil {
						t.Error(err)
					}
				}
			},
		},
		{
			name: "package", wantCode: "prettier/package-changed",
			prepare: func(t *testing.T, repository string, _ fakeResolver) func(safeexec.Request) {
				t.Helper()
				return func(safeexec.Request) {
					filename := filepath.Join(repository, "node_modules", "prettier", "changed.js")
					if err := os.WriteFile(filename, []byte("changed\n"), 0o600); err != nil {
						t.Error(err)
					}
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, resolver := evaluatorFixture(t, "3.9.9", "3.9.9")
			runner := &fakeRunner{
				response: safeexec.Response{Stdout: []byte(`{"results":[{"index":0,"parser":"babel"}]}`)},
				onRun:    test.prepare(t, repository, resolver),
			}
			providerUnderTest := New(runner, resolver)
			instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: repository})
			if err != nil {
				t.Fatal(err)
			}
			results, err := providerUnderTest.Evaluate(
				context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instances[0], []scope.Path{"src/app.js"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if results[0].State != scope.Unknown || results[0].Explanation.Code != test.wantCode {
				t.Fatalf("result = %#v, want UNKNOWN %s", results[0], test.wantCode)
			}
		})
	}
}

func TestEvaluateVersionAndIdentityMismatchAreUnknown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		projectVersion   string
		evaluatorVersion string
		mutateEvaluator  bool
		wantCode         string
	}{
		{
			name: "version", projectVersion: "3.8.5", evaluatorVersion: "3.9.9",
			wantCode: "prettier/evaluator-version-mismatch",
		},
		{
			name: "package identity", projectVersion: "3.9.9", evaluatorVersion: "3.9.9", mutateEvaluator: true,
			wantCode: "prettier/evaluator-identity-mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, resolver := evaluatorFixture(t, test.projectVersion, test.evaluatorVersion)
			if test.mutateEvaluator {
				entry := resolver.targets[prettierTool].Path
				if err := os.WriteFile(filepath.Join(filepath.Dir(entry), "plugin.js"), []byte("export {};"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runner := &fakeRunner{}
			providerUnderTest := New(runner, resolver)
			instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: repository})
			if err != nil {
				t.Fatal(err)
			}
			results, err := providerUnderTest.Evaluate(
				context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instances[0], []scope.Path{"src/app.js"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if results[0].State != scope.Unknown || results[0].Explanation.Code != test.wantCode {
				t.Fatalf("result = %#v, want UNKNOWN %s", results[0], test.wantCode)
			}
			if len(runner.requests) != 0 {
				t.Fatal("runner executed despite evaluator mismatch")
			}
		})
	}
}

func TestEvaluateUnapprovedToolIsUnknown(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, "3.9.9", "3.9.9")
	resolver.errors[prettierTool] = &safeexec.UnavailableError{
		Tool: prettierTool, Code: "tool/not-approved", Summary: "tool is not approved", Action: "run awareof --setup",
	}
	providerUnderTest := New(&fakeRunner{}, resolver)
	instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	results, err := providerUnderTest.Evaluate(
		context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instances[0], []scope.Path{"src/app.js"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "tool/not-approved" {
		t.Fatalf("result = %#v", results[0])
	}
}

func TestEvaluateSafetyFailuresAreUnknown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		prepare  func(*testing.T, *fakeResolver, *fakeRunner)
		wantCode string
	}{
		{
			name: "matching evaluator not selected", wantCode: "prettier/evaluator-selection-required",
			prepare: func(_ *testing.T, resolver *fakeResolver, _ *fakeRunner) {
				resolver.errors[prettierTool] = &safeexec.UnavailableError{
					Tool: prettierTool, Code: "tool/selection-required", Summary: "selection required",
				}
			},
		},
		{
			name: "node not approved", wantCode: "tool/not-approved",
			prepare: func(_ *testing.T, resolver *fakeResolver, _ *fakeRunner) {
				resolver.errors[nodeTool] = &safeexec.UnavailableError{
					Tool: nodeTool, Code: "tool/not-approved", Summary: "not approved",
				}
			},
		},
		{
			name: "repository Node", wantCode: "prettier/node-repository-controlled",
			prepare: func(_ *testing.T, resolver *fakeResolver, _ *fakeRunner) {
				target := resolver.targets[nodeTool]
				target.Origin = safeexec.RepositoryOrigin
				resolver.targets[nodeTool] = target
			},
		},
		{
			name: "Node shim", wantCode: "prettier/node-not-direct",
			prepare: func(t *testing.T, resolver *fakeResolver, _ *fakeRunner) {
				t.Helper()
				target := resolver.targets[nodeTool]
				shim := filepath.Join(t.TempDir(), "volta-shim")
				content, err := os.ReadFile(target.Path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(shim, content, 0o600); err != nil { //nolint:gosec // shim is confined to t.TempDir.
					t.Fatal(err)
				}
				target.Path = shim
				resolver.targets[nodeTool] = target
			},
		},
		{
			name: "bounded execution unavailable", wantCode: "tool/timeout",
			prepare: func(_ *testing.T, _ *fakeResolver, runner *fakeRunner) {
				runner.err = &safeexec.UnavailableError{Tool: nodeTool, Code: "tool/timeout", Summary: "timed out"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, resolver := evaluatorFixture(t, "3.9.9", "3.9.9")
			runner := &fakeRunner{response: safeexec.Response{Stdout: []byte(`{"results":[{"index":0,"parser":"babel"}]}`)}}
			test.prepare(t, &resolver, runner)
			providerUnderTest := New(runner, resolver)
			instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: repository})
			if err != nil {
				t.Fatal(err)
			}
			results, err := providerUnderTest.Evaluate(
				context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instances[0], []scope.Path{"src/app.js"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if results[0].State != scope.Unknown || results[0].Explanation.Code != test.wantCode {
				t.Fatalf("result = %#v, want UNKNOWN %s", results[0], test.wantCode)
			}
		})
	}
}

func TestEvaluateMissingProjectPackageIsUnknown(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFixtureFiles(t, repository, map[string]string{
		"package.json": `{"devDependencies":{"prettier":"3.9.9"}}`,
	})
	providerUnderTest := New(&fakeRunner{}, fakeResolver{})
	instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	results, err := providerUnderTest.Evaluate(
		context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instances[0], []scope.Path{"src/app.js"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "prettier/package-unavailable" {
		t.Fatalf("result = %#v, want package UNKNOWN", results[0])
	}
}

func TestEvaluateOperationalRunnerFailureIsError(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, "3.9.9", "3.9.9")
	providerUnderTest := New(&fakeRunner{err: errors.New("runner failed")}, resolver)
	instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	_, err = providerUnderTest.Evaluate(
		context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instances[0], []scope.Path{"src/app.js"},
	)
	if err == nil || !strings.Contains(err.Error(), "run Prettier evaluator") {
		t.Fatalf("Evaluate() error = %v, want operational runner error", err)
	}
}

func TestEvaluateReusesApprovedMatchingEvaluator(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, "3.9.9", "3.9.9")
	approved := resolver.targets[prettierTool]
	resolver.errors[prettierTool] = &safeexec.UnavailableError{
		Tool: prettierTool, Code: "tool/selection-required", Summary: "selection required",
	}
	resolver.approved = map[safeexec.ToolID][]safeexec.Target{prettierTool: {approved}}
	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte(`{"results":[{"index":0,"parser":"babel"}]}`)}}
	providerUnderTest := New(runner, resolver)
	instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	selections, err := providerUnderTest.SetupSelections(repository, instances)
	if err != nil {
		t.Fatal(err)
	}
	if len(selections) != 2 || selections[1].Path != approved.Path {
		t.Fatalf("SetupSelections() = %v, want approved matching evaluator", selections)
	}
	existingPackage := filepath.Dir(approved.Path)
	base := filepath.Dir(filepath.Dir(filepath.Dir(existingPackage)))
	alternatePackage := filepath.Join(base, "aaa", "node_modules", "prettier")
	writeFixtureFiles(t, alternatePackage, map[string]string{
		"package.json": `{"name":"prettier","version":"3.9.9"}`,
		"index.mjs":    "export {};",
	})
	alternate := safeexec.Target{Tool: prettierTool, Path: filepath.Join(alternatePackage, "index.mjs"), Origin: safeexec.ExternalOrigin}
	resolver.approved[prettierTool] = []safeexec.Target{approved, alternate}
	results, err := providerUnderTest.Evaluate(
		context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instances[0], []scope.Path{"src/app.js"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.In || len(runner.requests) != 1 {
		t.Fatalf("result = %#v, runner calls = %d", results[0], len(runner.requests))
	}
	var request evaluatorRequest
	if err := json.Unmarshal(runner.requests[0].Stdin, &request); err != nil {
		t.Fatal(err)
	}
	wantEntry, err := filepath.EvalSymlinks(alternate.Path)
	if err != nil {
		t.Fatal(err)
	}
	if request.PrettierEntry != wantEntry {
		t.Fatalf("selected evaluator = %q, want deterministic first path %q", request.PrettierEntry, wantEntry)
	}
}

func TestParseEvaluatorResponse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		count   int
		valid   bool
	}{
		{name: "ordered", content: `{"results":[{"index":1},{"index":0}]}`, count: 2, valid: true},
		{name: "duplicate", content: `{"results":[{"index":0},{"index":0}]}`, count: 2},
		{name: "out of range", content: `{"results":[{"index":1}]}`, count: 1},
		{name: "missing", content: `{"results":[]}`, count: 1},
		{name: "unknown field", content: `{"results":[],"extra":true}`, count: 0},
		{name: "trailing", content: `{"results":[]} {}`, count: 0},
		{name: "bad explanation", content: `{"results":[{"index":0,"unavailable":{"code":"BAD","summary":"x"}}]}`, count: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseEvaluatorResponse([]byte(test.content), test.count)
			if (err == nil) != test.valid {
				t.Fatalf("parseEvaluatorResponse() error = %v, valid=%v", err, test.valid)
			}
			if test.valid && len(got) == 2 && (got[0].Index != 0 || got[1].Index != 1) {
				t.Fatalf("results not ordered: %#v", got)
			}
		})
	}
}

func TestValidateNodeExecutable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		goos   string
		base   string
		header []byte
		valid  bool
	}{
		{name: "Linux ELF", goos: "linux", base: "node", header: []byte{0x7f, 'E', 'L', 'F'}, valid: true},
		{name: "Linux PE", goos: "linux", base: "node", header: []byte{'M', 'Z', 0, 0}},
		{name: "macOS Mach-O", goos: "darwin", base: "node", header: []byte{0xcf, 0xfa, 0xed, 0xfe}, valid: true},
		{name: "macOS ELF", goos: "darwin", base: "node", header: []byte{0x7f, 'E', 'L', 'F'}},
		{name: "Windows PE", goos: "windows", base: "node.exe", header: []byte{'M', 'Z', 0, 0}, valid: true},
		{name: "Windows ELF", goos: "windows", base: "node.exe", header: []byte{0x7f, 'E', 'L', 'F'}},
		{name: "version manager shim", goos: "linux", base: "volta-shim", header: []byte{0x7f, 'E', 'L', 'F'}},
		{name: "script", goos: "linux", base: "node", header: []byte("#!/bin/sh\n")},
		{name: "unsupported platform", goos: "freebsd", base: "node", header: []byte{0x7f, 'E', 'L', 'F'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			name := filepath.Join(t.TempDir(), test.base)
			if err := os.WriteFile(name, test.header, 0o600); err != nil {
				t.Fatal(err)
			}
			err := validateNodeExecutable(name, test.goos)
			if (err == nil) != test.valid {
				t.Fatalf("validateNodeExecutable() error = %v, valid=%v", err, test.valid)
			}
		})
	}
}

func TestCommandEvidenceIsBoundedUTF8(t *testing.T) {
	t.Parallel()
	evidence := commandEvidence(safeexec.Response{ExitCode: 1, Stderr: []byte(strings.Repeat("ż", 2048))})
	if len(evidence) > 2048 {
		t.Fatalf("commandEvidence() length = %d, want at most 2048 bytes", len(evidence))
	}
	if !strings.Contains(evidence, "evaluator exited with status 1") || !utf8.ValidString(evidence) {
		t.Fatalf("commandEvidence() = %q, want valid bounded UTF-8", evidence)
	}
}

func TestEvaluateHonorsCancellation(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, "3.9.9", "3.9.9")
	providerUnderTest := New(&fakeRunner{}, resolver)
	instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = providerUnderTest.Evaluate(
		ctx, provider.EvaluationContext{}, provider.Repository{Root: repository}, instances[0], []scope.Path{"src/app.js"},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Evaluate() error = %v, want context canceled", err)
	}
}

func evaluatorFixture(t *testing.T, projectVersion, evaluatorVersion string) (string, fakeResolver) {
	t.Helper()
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	external := filepath.Join(base, "external", "node_modules", "prettier")
	writeFixtureFiles(t, repository, map[string]string{
		"package.json":                       `{"devDependencies":{"prettier":"` + projectVersion + `"}}`,
		"node_modules/prettier/package.json": `{"name":"prettier","version":"` + projectVersion + `"}`,
		"node_modules/prettier/index.mjs":    "export {};",
	})
	writeFixtureFiles(t, external, map[string]string{
		"package.json": `{"name":"prettier","version":"` + evaluatorVersion + `"}`,
		"index.mjs":    "export {};",
	})
	nodeName := "node"
	header := []byte{0x7f, 'E', 'L', 'F'}
	switch runtime.GOOS {
	case "windows":
		nodeName = "node.exe"
		header = []byte{'M', 'Z', 0, 0}
	case "darwin":
		header = []byte{0xcf, 0xfa, 0xed, 0xfe}
	}
	node := filepath.Join(base, "external", "bin", nodeName)
	if err := os.MkdirAll(filepath.Dir(node), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(node, header, 0o600); err != nil {
		t.Fatal(err)
	}
	return repository, fakeResolver{
		targets: map[safeexec.ToolID]safeexec.Target{
			prettierTool: {Tool: prettierTool, Path: filepath.Join(external, "index.mjs"), Origin: safeexec.ExternalOrigin},
			nodeTool:     {Tool: nodeTool, Path: node, Origin: safeexec.ExternalOrigin},
		},
		errors:   make(map[safeexec.ToolID]error),
		approved: make(map[safeexec.ToolID][]safeexec.Target),
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var _ targetDiscoverer = fakeResolver{}
var _ safeexec.CommandRunner = (*fakeRunner)(nil)
