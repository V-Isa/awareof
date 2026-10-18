package eslint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

type fakeResolver struct {
	target safeexec.Target
	err    error
	calls  int
}

func (r *fakeResolver) Resolve(_ string, _ safeexec.ToolID) (safeexec.Target, error) {
	r.calls++
	return r.target, r.err
}

type fakeRunner struct {
	response safeexec.Response
	err      error
	requests []safeexec.Request
	run      func(context.Context, safeexec.Request) (safeexec.Response, error)
}

func (r *fakeRunner) Run(ctx context.Context, request safeexec.Request) (safeexec.Response, error) {
	r.requests = append(r.requests, request)
	if r.run != nil {
		return r.run(ctx, request)
	}
	return r.response, r.err
}

func TestEvaluateRequiresExplicitRepositoryCodePermission(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, ".", "9.39.5")
	if err := os.Mkdir(filepath.Join(repository, "directory"), 0o750); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	providerUnderTest := New(runner, resolver)
	instance := detectedInstance(t, providerUnderTest, repository)

	results, err := providerUnderTest.Evaluate(
		context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instance,
		[]scope.Path{"directory", "src/app.ts"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].State != scope.Out || results[0].Explanation.Code != "eslint/not-file" ||
		results[0].Provenance.Method != scope.SafeParser || results[1].State != scope.Unknown ||
		results[1].Explanation.Code != "eslint/repository-code-not-allowed" {
		t.Fatalf("results = %#v", results)
	}
	if resolver.calls != 0 || len(runner.requests) != 0 {
		t.Fatalf("default evaluation resolved %d tool(s) and ran %d command(s)", resolver.calls, len(runner.requests))
	}
}

func TestEvaluateUsesFixedDelegatedESLintAPI(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, ".", "9.39.5")
	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte(`{"results":[
		{"index":0,"ignored":false,"configured":true},
		{"index":1,"ignored":true,"configured":false},
		{"index":2,"ignored":false,"configured":false}
	]}`)}}
	runner.run = func(_ context.Context, request safeexec.Request) (safeexec.Response, error) {
		content, err := os.ReadFile(request.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(request.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != eslintHelper || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("helper content/mode = %d bytes/%#o", len(content), info.Mode().Perm())
		}
		return runner.response, nil
	}
	providerUnderTest := New(runner, resolver)
	instance := detectedInstance(t, providerUnderTest, repository)
	paths := []scope.Path{"src/app.ts", "generated/app.js", "notes.txt"}

	results, err := providerUnderTest.Evaluate(
		context.Background(), allowESLint(t), provider.Repository{Root: repository}, instance, paths,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []scope.State{scope.In, scope.Out, scope.Out}
	wantCodes := []string{"eslint/selected", "eslint/ignored", "eslint/unconfigured"}
	for index, result := range results {
		if result.State != want[index] {
			t.Errorf("result %s = %s, want %s", result.Path, result.State, want[index])
		}
		if result.Explanation.Code != wantCodes[index] {
			t.Errorf("result %s code = %s, want %s", result.Path, result.Explanation.Code, wantCodes[index])
		}
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner calls = %d, want one batched call", len(runner.requests))
	}
	request := runner.requests[0]
	physicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if request.Tool != nodeTool || !request.ExternalOnly || request.Dir != physicalRepository || len(request.Args) != 1 {
		t.Fatalf("unsafe evaluator request: %#v", request)
	}
	if strings.HasPrefix(request.Args[0], repository+string(filepath.Separator)) {
		t.Fatalf("helper path %q is inside repository", request.Args[0])
	}
	for _, name := range []string{"ESLINT_FLAGS", "ESLINT_USE_FLAT_CONFIG", "NODE_OPTIONS", "NODE_PATH", "NODE_PRESERVE_SYMLINKS", "NODE_V8_COVERAGE"} {
		if !containsString(request.UnsetEnv, name) {
			t.Errorf("unset environment = %v, missing %s", request.UnsetEnv, name)
		}
	}
	if len(request.Env) != 0 {
		t.Errorf("environment = %v", request.Env)
	}
	if _, exists := request.Env["NODE_ENV"]; exists {
		t.Errorf("environment overrides NODE_ENV: %v", request.Env)
	}
	var protocol evaluatorRequest
	if err := json.Unmarshal(request.Stdin, &protocol); err != nil {
		t.Fatal(err)
	}
	if protocol.Config != filepath.Join(physicalRepository, "eslint.config.js") || protocol.CWD != physicalRepository || len(protocol.Paths) != len(paths) {
		t.Fatalf("protocol = %#v", protocol)
	}
	if !strings.HasSuffix(filepath.ToSlash(protocol.ESLintEntry), "/node_modules/eslint/lib/api.js") {
		t.Fatalf("ESLint entry = %q", protocol.ESLintEntry)
	}
}

func TestEvaluateUsesNonRootContextWorkingDirectory(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, "mobile", "9.39.5")
	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte(`{"results":[{"index":0,"configured":true}]}`)}}
	current := New(runner, resolver)
	instance := detectedInstance(t, current, repository)
	results, err := current.Evaluate(
		context.Background(), allowESLint(t), provider.Repository{Root: repository}, instance, []scope.Path{"mobile/src/app.ts"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.In || len(runner.requests) != 1 {
		t.Fatalf("results/requests = %#v/%d", results, len(runner.requests))
	}
	physicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	wantContext := filepath.Join(physicalRepository, "mobile")
	request := runner.requests[0]
	var protocol evaluatorRequest
	if err := json.Unmarshal(request.Stdin, &protocol); err != nil {
		t.Fatal(err)
	}
	if request.Dir != wantContext || protocol.CWD != wantContext || protocol.Config != filepath.Join(wantContext, "eslint.config.js") {
		t.Fatalf("request/protocol context = %q/%#v, want %q", request.Dir, protocol, wantContext)
	}
	if !strings.HasSuffix(filepath.ToSlash(protocol.ESLintEntry), "/node_modules/eslint/lib/api.js") {
		t.Fatalf("hoisted ESLint entry = %q", protocol.ESLintEntry)
	}
}

func TestEvaluateKeepsNestedConfigUncertaintyPathLocal(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, ".", "10.12.0")
	writeFile(t, repository, "packages/app/eslint.config.js", "export default [];\n")
	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte(`{"results":[{"index":0,"configured":true}]}`)}}
	providerUnderTest := New(runner, resolver)
	instance := detectedInstance(t, providerUnderTest, repository)

	results, err := providerUnderTest.Evaluate(
		context.Background(), allowESLint(t), provider.Repository{Root: repository}, instance,
		[]scope.Path{"src/root.ts", "packages/app/src/app.ts"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.In || results[1].State != scope.Unknown || results[1].Explanation.Code != "eslint/nested-config-unsupported" {
		t.Fatalf("results = %#v", results)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner calls = %d, want one", len(runner.requests))
	}
	var protocol evaluatorRequest
	if err := json.Unmarshal(runner.requests[0].Stdin, &protocol); err != nil {
		t.Fatal(err)
	}
	if len(protocol.Paths) != 1 || !strings.HasSuffix(filepath.ToSlash(protocol.Paths[0]), "/src/root.ts") {
		t.Fatalf("protocol paths = %v", protocol.Paths)
	}
}

func TestEvaluatePermissionAndToolApprovalAreIndependent(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, ".", "9.39.5")
	resolver.err = &safeexec.UnavailableError{
		Tool: nodeTool, Code: "tool/not-approved", Summary: "tool is not approved", Action: "run setup",
	}
	runner := &fakeRunner{}
	providerUnderTest := New(runner, resolver)
	instance := detectedInstance(t, providerUnderTest, repository)

	results, err := providerUnderTest.Evaluate(
		context.Background(), allowESLint(t), provider.Repository{Root: repository}, instance, []scope.Path{"src/app.ts"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "tool/not-approved" || len(runner.requests) != 0 {
		t.Fatalf("results = %#v, runner calls = %d", results, len(runner.requests))
	}
}

func TestEvaluateKeepsPreflightFactsWhenContextEvaluationIsUnavailable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		resolverErr error
		runnerErr   error
		wantCode    string
	}{
		{
			name: "tool not approved",
			resolverErr: &safeexec.UnavailableError{
				Tool: nodeTool, Code: "tool/not-approved", Summary: "tool is not approved", Action: "run setup",
			},
			wantCode: "tool/not-approved",
		},
		{
			name: "evaluator timeout", runnerErr: fmt.Errorf("run node: %w", context.DeadlineExceeded),
			wantCode: "command/timeout",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, resolver := evaluatorFixture(t, ".", "9.39.5")
			if err := os.Mkdir(filepath.Join(repository, "directory"), 0o750); err != nil {
				t.Fatal(err)
			}
			resolver.err = test.resolverErr
			current := New(&fakeRunner{err: test.runnerErr}, resolver)
			instance := detectedInstance(t, current, repository)
			results, err := current.Evaluate(
				context.Background(), allowESLint(t), provider.Repository{Root: repository}, instance,
				[]scope.Path{"directory", "src/app.ts"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if results[0].State != scope.Out || results[0].Explanation.Code != "eslint/not-file" ||
				results[1].State != scope.Unknown || results[1].Explanation.Code != test.wantCode {
				t.Fatalf("results = %#v", results)
			}
		})
	}
}

func TestEvaluateReportsStaticConfigFailureWithoutDelegation(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFile(t, repository, "package.json", `{"devDependencies":{"eslint":"9.39.5"}}`)
	resolver := &fakeResolver{}
	current := New(&fakeRunner{}, resolver)
	instance := detectedInstance(t, current, repository)
	results, err := current.Evaluate(
		context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instance,
		[]scope.Path{"src/app.ts"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "eslint/config-unavailable" {
		t.Fatalf("result = %#v", results[0])
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver calls = %d, want zero", resolver.calls)
	}
}

func TestEvaluateRejectsUnsafeInputsWithoutLosingSafePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliable on Windows test hosts")
	}
	repository, resolver := evaluatorFixture(t, ".", "9.39.5")
	writeFile(t, repository, "real.ts", "")
	if err := os.Symlink("real.ts", filepath.Join(repository, "linked.ts")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repository, "real", "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(repository, "linked-directory")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository, "directory"), 0o750); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte(`{"results":[{"index":0,"configured":true}]}`)}}
	providerUnderTest := New(runner, resolver)
	instance := detectedInstance(t, providerUnderTest, repository)
	results, err := providerUnderTest.Evaluate(
		context.Background(), allowESLint(t), provider.Repository{Root: repository}, instance,
		[]scope.Path{"missing.ts", "linked.ts", "linked-directory/nested/app.ts", "directory"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.In || results[1].Explanation.Code != "eslint/path-uninspectable" ||
		results[2].Explanation.Code != "eslint/path-uninspectable" || results[3].State != scope.Out ||
		results[3].Explanation.Code != "eslint/not-file" || results[3].Provenance.Method != scope.SafeParser {
		t.Fatalf("results = %#v", results)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner calls = %d", len(runner.requests))
	}
}

func TestEvaluateRejectsConfigSwappedOutsideRepository(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliable on Windows test hosts")
	}
	repository, resolver := evaluatorFixture(t, ".", "9.39.5")
	runner := &fakeRunner{}
	current := New(runner, resolver)
	instance := detectedInstance(t, current, repository)
	outside := filepath.Join(t.TempDir(), "eslint.config.js")
	if err := os.WriteFile(outside, []byte("module.exports = [];\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(repository, "eslint.config.js")
	if err := os.Remove(config); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, config); err != nil {
		t.Fatal(err)
	}
	results, err := current.Evaluate(
		context.Background(), allowESLint(t), provider.Repository{Root: repository}, instance, []scope.Path{"src/app.ts"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "eslint/config-outside-repository" || len(runner.requests) != 0 {
		t.Fatalf("result = %#v, runner calls = %d", results[0], len(runner.requests))
	}
	if results[0].Provenance.Tool != "" {
		t.Fatalf("config failure provenance tool = %q, want empty", results[0].Provenance.Tool)
	}
}

func TestEvaluateFailuresAreUnknown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		version  string
		prepare  func(*testing.T, string, *fakeResolver)
		response safeexec.Response
		runErr   error
		wantCode string
	}{
		{name: "unsupported package", version: "8.57.1", wantCode: "eslint/package-unavailable"},
		{name: "repository Node", version: "9.39.5", prepare: func(_ *testing.T, repository string, resolver *fakeResolver) {
			resolver.target.Origin = safeexec.RepositoryOrigin
			resolver.target.Path = filepath.Join(repository, "node")
		}, wantCode: "eslint/node-repository-controlled"},
		{name: "Node shim", version: "9.39.5", prepare: func(t *testing.T, _ string, resolver *fakeResolver) {
			resolver.target.Path = filepath.Join(t.TempDir(), "node")
			if err := os.WriteFile(resolver.target.Path, []byte("#!/bin/sh\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantCode: "eslint/node-not-direct"},
		{name: "timeout", version: "9.39.5", runErr: fmt.Errorf("run node: %w", context.DeadlineExceeded), wantCode: "command/timeout"},
		{name: "bounded output", version: "9.39.5", runErr: fmt.Errorf("run node: %w", safeexec.ErrOutputLimit), wantCode: "command/output-limit"},
		{name: "nonzero partial output", version: "9.39.5", response: safeexec.Response{
			ExitCode: 1, Stdout: []byte(`{"results":[{"index":0,"configured":true}]}`), Stderr: []byte("config failed"),
		}, wantCode: "eslint/evaluator-failed"},
		{name: "invalid output", version: "9.39.5", response: safeexec.Response{Stdout: []byte(`{"results":[]}`)}, wantCode: "eslint/evaluator-output-invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, resolver := evaluatorFixture(t, ".", test.version)
			if test.prepare != nil {
				test.prepare(t, repository, resolver)
			}
			runner := &fakeRunner{response: test.response, err: test.runErr}
			providerUnderTest := New(runner, resolver)
			instance := detectedInstance(t, providerUnderTest, repository)
			results, err := providerUnderTest.Evaluate(
				context.Background(), allowESLint(t), provider.Repository{Root: repository}, instance, []scope.Path{"src/app.ts"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if results[0].State != scope.Unknown || results[0].Explanation.Code != test.wantCode {
				t.Fatalf("result = %#v, want UNKNOWN %s", results[0], test.wantCode)
			}
			if test.name == "unsupported package" && results[0].Provenance.Tool != "" {
				t.Fatalf("package failure provenance tool = %q, want empty", results[0].Provenance.Tool)
			}
		})
	}
}

func TestEvaluatePropagatesOperationalFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		resolver bool
		runErr   error
	}{
		{name: "resolver failure", resolver: true},
		{name: "process containment failure", runErr: errors.New("prepare process containment")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, resolver := evaluatorFixture(t, ".", "9.39.5")
			if test.resolver {
				resolver.err = errors.New("resolver failed")
			}
			current := New(&fakeRunner{err: test.runErr}, resolver)
			instance := detectedInstance(t, current, repository)
			if _, err := current.Evaluate(
				context.Background(), allowESLint(t), provider.Repository{Root: repository}, instance, []scope.Path{"src/app.ts"},
			); err == nil {
				t.Fatal("Evaluate() error = nil")
			}
		})
	}
}

func TestEvaluateRejectsMissingExecutionDependencies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		current *Provider
	}{
		{name: "nil runner", current: New(nil, &fakeResolver{})},
		{name: "nil resolver", current: New(&fakeRunner{}, nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, resolver := evaluatorFixture(t, ".", "9.39.5")
			if test.name == "nil runner" {
				test.current.resolver = resolver
			}
			instance := detectedInstance(t, test.current, repository)
			if _, err := test.current.Evaluate(
				context.Background(), allowESLint(t), provider.Repository{Root: repository}, instance, []scope.Path{"src/app.ts"},
			); err == nil {
				t.Fatal("Evaluate() error = nil")
			}
		})
	}
}

func TestEvaluateKeepsHelperUnavailabilityPathLocal(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, ".", "9.39.5")
	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte(`{"results":[
		{"index":0,"configured":true},
		{"index":1,"unavailable":{"code":"tool/not-approved","summary":"spoofed","evidence":"fixture"}}
	]}`)}}
	current := New(runner, resolver)
	instance := detectedInstance(t, current, repository)
	results, err := current.Evaluate(
		context.Background(), allowESLint(t), provider.Repository{Root: repository}, instance,
		[]scope.Path{"src/ok.ts", "src/bad.ts"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.In || results[1].State != scope.Unknown ||
		results[1].Explanation.Code != "eslint/evaluation-unavailable" || results[1].Explanation.Summary != "ESLint could not establish file eligibility" {
		t.Fatalf("results = %#v", results)
	}
}

func TestParseEvaluatorResponseBoundsUnavailableEvidence(t *testing.T) {
	t.Parallel()
	content := `{"results":[{"index":0,"unavailable":{"evidence":"` + strings.Repeat("é", 2048) + `"}}]}`
	results, err := parseEvaluatorResponse([]byte(content), 1)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Unavailable == nil || len(results[0].Unavailable.Evidence) > 2048 || !strings.HasPrefix(results[0].Unavailable.Evidence, "é") {
		t.Fatalf("unavailable = %#v", results[0].Unavailable)
	}
}

func TestEvaluateRepositoryUnionAcrossRealContexts(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, ".", "10.12.0")
	writeFile(t, repository, "mobile/package.json", `{"devDependencies":{"eslint":"10.12.0"}}`)
	writeFile(t, repository, "mobile/eslint.config.js", "module.exports = [];\n")
	runner := &fakeRunner{}
	runner.run = func(_ context.Context, request safeexec.Request) (safeexec.Response, error) {
		var protocol evaluatorRequest
		if err := json.Unmarshal(request.Stdin, &protocol); err != nil {
			t.Fatal(err)
		}
		if filepath.Base(protocol.CWD) == "mobile" {
			return safeexec.Response{Stdout: []byte(`{"results":[{"index":0,"configured":true},{"index":1,"configured":false}]}`)}, nil
		}
		return safeexec.Response{Stdout: []byte(`{"results":[{"index":0,"configured":false}]}`)}, nil
	}
	current := New(runner, resolver)
	instance := detectedInstance(t, current, repository)
	results, err := current.Evaluate(
		context.Background(), allowESLint(t), provider.Repository{Root: repository}, instance,
		[]scope.Path{"root.txt", "mobile/src/in.ts", "mobile/src/out.txt"},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []scope.State{scope.Out, scope.In, scope.Unknown}
	for index := range want {
		if results[index].State != want[index] {
			t.Fatalf("result %s = %s, want %s (%#v)", results[index].Path, results[index].State, want[index], results[index])
		}
	}
	if len(runner.requests) != 2 {
		t.Fatalf("runner requests = %d, want two contexts", len(runner.requests))
	}
}

func TestEvaluateHonorsCallerCancellation(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, ".", "9.39.5")
	current := New(&fakeRunner{}, resolver)
	instance := detectedInstance(t, current, repository)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := current.Evaluate(
		ctx, allowESLint(t), provider.Repository{Root: repository}, instance, []scope.Path{"src/app.ts"},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("Evaluate() error = %v, want context cancellation", err)
	}
}

func TestValidateNodeExecutableMatchesPlatformFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		filename string
		goos     string
		header   []byte
		valid    bool
	}{
		{name: "Linux", filename: "node", goos: "linux", header: []byte{0x7f, 'E', 'L', 'F'}, valid: true},
		{name: "macOS", filename: "node", goos: "darwin", header: []byte{0xcf, 0xfa, 0xed, 0xfe}, valid: true},
		{name: "Windows", filename: "node.exe", goos: "windows", header: []byte{'M', 'Z', 0, 0}, valid: true},
		{name: "wrong basename", filename: "node-wrapper", goos: "linux", header: []byte{0x7f, 'E', 'L', 'F'}},
		{name: "wrong format", filename: "node", goos: "linux", header: []byte{'M', 'Z', 0, 0}},
		{name: "unsupported platform", filename: "node", goos: "plan9", header: []byte{0x7f, 'E', 'L', 'F'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			name := filepath.Join(t.TempDir(), test.filename)
			if err := os.WriteFile(name, test.header, 0o600); err != nil {
				t.Fatal(err)
			}
			err := validateNodeExecutable(name, test.goos)
			if (err == nil) != test.valid {
				t.Fatalf("validateNodeExecutable() error = %v, valid = %t", err, test.valid)
			}
		})
	}
}

func TestParseEvaluatorResponseRejectsAmbiguity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
	}{
		{name: "malformed", content: `{`},
		{name: "extra JSON", content: `{"results":[{"index":0}]} {}`},
		{name: "unknown field", content: `{"results":[{"index":0,"extra":true}]}`},
		{name: "missing index", content: `{"results":[{}]}`},
		{name: "duplicate index", content: `{"results":[{"index":0},{"index":0}]}`},
		{name: "out of range", content: `{"results":[{"index":1}]}`},
		{name: "ignored and configured", content: `{"results":[{"index":0,"ignored":true,"configured":true}]}`},
		{name: "fact and unavailable", content: `{"results":[{"index":0,"configured":true,"unavailable":{"evidence":"test"}}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			count := 1
			if test.name == "duplicate index" {
				count = 2
			}
			if _, err := parseEvaluatorResponse([]byte(test.content), count); err == nil {
				t.Fatal("parseEvaluatorResponse() error = nil")
			}
		})
	}
}

func TestRepositoryUnionDecisionTable(t *testing.T) {
	t.Parallel()
	paths := []scope.Path{"in.ts", "unknown.ts", "ignored.ts", "mixed.ts", "directory", "outside.ts"}
	union := newUnionEvaluation(paths)
	union.addApplicable(paths[:5])
	unknown := eligibilityFact{unavailable: &scope.Explanation{Code: "eslint/test", Summary: "unknown"}}
	ignored := eligibilityFact{code: "eslint/ignored", summary: "ignored", method: scope.SafeNative}
	unconfigured := eligibilityFact{code: "eslint/unconfigured", summary: "unconfigured", method: scope.SafeNative}
	union.addFact("in.ts", ".", unknown)
	union.addFact("in.ts", "mobile", eligibilityFact{in: true, config: "mobile/eslint.config.js", method: scope.SafeNative})
	union.addFact("unknown.ts", ".", ignored)
	union.addFact("unknown.ts", "mobile", unknown)
	union.addFact("ignored.ts", ".", ignored)
	union.addFact("mixed.ts", ".", ignored)
	union.addFact("mixed.ts", "mobile", unconfigured)
	union.addFact("directory", ".", eligibilityFact{code: "eslint/not-file", summary: "not a file", method: scope.SafeParser})
	results := union.results(rootInstanceID)
	wantStates := []scope.State{scope.In, scope.Unknown, scope.Out, scope.Out, scope.Out, scope.NotApp}
	wantCodes := []string{"eslint/selected", "eslint/test", "eslint/ignored", "eslint/not-selected", "eslint/not-file", "eslint/not-applicable"}
	for index := range results {
		if results[index].State != wantStates[index] || results[index].Explanation.Code != wantCodes[index] {
			t.Errorf("result %d = %#v, want %s %s", index, results[index], wantStates[index], wantCodes[index])
		}
	}
	if results[4].Provenance.Method != scope.SafeParser {
		t.Errorf("directory provenance = %s, want safe-parser", results[4].Provenance.Method)
	}
}

func TestRepositoryUnionTreatsIncompleteDiscoveryAsUnknownUnlessInIsProven(t *testing.T) {
	t.Parallel()
	union := newUnionEvaluation([]scope.Path{"selected.ts", "not-selected.ts"})
	union.addApplicable([]scope.Path{"selected.ts", "not-selected.ts"})
	union.addGlobalUnknown(scope.Explanation{Code: "eslint/discovery-unavailable", Summary: "discovery incomplete"}, "")
	union.addFact("selected.ts", ".", eligibilityFact{in: true, method: scope.SafeNative})
	union.addFact("not-selected.ts", ".", eligibilityFact{code: "eslint/unconfigured", summary: "unconfigured", method: scope.SafeNative})
	results := union.results(rootInstanceID)
	if results[0].State != scope.In || results[1].State != scope.Unknown ||
		results[1].Explanation.Code != "eslint/discovery-unavailable" {
		t.Fatalf("results = %#v", results)
	}
}

func evaluatorFixture(t *testing.T, contextRoot, version string) (string, *fakeResolver) {
	t.Helper()
	repository := t.TempDir()
	manifest := `{"private":true,"devDependencies":{"eslint":"` + version + `"}}`
	writeFile(t, repository, filepath.ToSlash(filepath.Join(contextRoot, "package.json")), manifest)
	writeFile(t, repository, filepath.ToSlash(filepath.Join(contextRoot, "eslint.config.js")), "module.exports = [];\n")
	writeFile(t, repository, "node_modules/eslint/package.json", `{"name":"eslint","version":"`+version+`"}`)
	writeFile(t, repository, "node_modules/eslint/lib/api.js", "module.exports = {};\n")
	nodeName := "node"
	if runtime.GOOS == "windows" {
		nodeName = "node.exe"
	}
	node := filepath.Join(t.TempDir(), nodeName)
	writeNativeExecutable(t, node, runtime.GOOS)
	return repository, &fakeResolver{target: safeexec.Target{Tool: nodeTool, Path: node, Origin: safeexec.ExternalOrigin}}
}

func detectedInstance(t *testing.T, providerUnderTest *Provider, repository string) provider.Instance {
	t.Helper()
	instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 {
		t.Fatalf("instances = %d, want one", len(instances))
	}
	return instances[0]
}

func allowESLint(t *testing.T) provider.EvaluationContext {
	t.Helper()
	context, err := provider.NewEvaluationContext([]scope.ProviderID{providerID})
	if err != nil {
		t.Fatal(err)
	}
	return context
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeNativeExecutable(t *testing.T, name, goos string) {
	t.Helper()
	var content []byte
	switch goos {
	case "linux":
		content = []byte{0x7f, 'E', 'L', 'F'}
	case "darwin":
		content = []byte{0xcf, 0xfa, 0xed, 0xfe}
	case "windows":
		content = []byte{'M', 'Z', 0, 0}
	default:
		t.Skipf("native Node validation is unsupported on %s", goos)
	}
	if err := os.WriteFile(name, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
