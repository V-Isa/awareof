package jest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestEvaluateRequiresExplicitRepositoryCodePermission(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, ".", "29.7.0")
	if err := os.Mkdir(filepath.Join(repository, "directory"), 0o750); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	current := New(runner, resolver)
	instance := detectedInstance(t, current, repository)
	results, err := current.Evaluate(
		context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instance,
		[]scope.Path{"directory", "src/app.test.ts"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].State != scope.Out || results[1].State != scope.Unknown ||
		results[1].Explanation.Code != "jest/repository-code-not-allowed" {
		t.Fatalf("results = %#v", results)
	}
	if resolver.calls != 0 || len(runner.requests) != 0 {
		t.Fatalf("default evaluation resolved %d tool(s) and ran %d command(s)", resolver.calls, len(runner.requests))
	}
}

func TestEvaluateUsesFixedJestListTestsInvocation(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, ".", "29.7.0")
	writeFile(t, repository, "src/a.test.ts", "")
	writeFile(t, repository, "src/a.ts", "")
	runner := &fakeRunner{}
	runner.run = func(_ context.Context, request safeexec.Request) (safeexec.Response, error) {
		content, err := json.Marshal([]string{filepath.Join(repository, "src/a.test.ts")})
		if err != nil {
			t.Fatal(err)
		}
		return safeexec.Response{Stdout: content}, nil
	}
	current := New(runner, resolver)
	instance := detectedInstance(t, current, repository)
	paths := []scope.Path{"src/a.test.ts", "src/a.ts", "src/missing.test.ts"}
	results, err := current.Evaluate(context.Background(), allowJest(t), provider.Repository{Root: repository}, instance, paths)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.In || results[1].State != scope.Out || results[2].State != scope.Out {
		t.Fatalf("results = %#v", results)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("requests = %d", len(runner.requests))
	}
	request := runner.requests[0]
	physical, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		filepath.Join(physical, "node_modules/jest/bin/jest.js"),
		"--config", filepath.Join(physical, "jest.config.js"),
		"--listTests", "--json", "--no-cache", "--no-watchman",
	}
	if request.Tool != nodeTool || !request.ExternalOnly || request.Dir != physical || !reflect.DeepEqual(request.Args, wantArgs) || len(request.Stdin) != 0 {
		t.Fatalf("request = %#v, want args %v", request, wantArgs)
	}
	for _, name := range []string{"NODE_OPTIONS", "NODE_PATH", "NODE_PRESERVE_SYMLINKS", "NODE_V8_COVERAGE"} {
		if !slices.Contains(request.UnsetEnv, name) {
			t.Errorf("unset environment = %v, missing %s", request.UnsetEnv, name)
		}
	}
	if strings.Contains(strings.Join(request.Args, " "), "runInBand") {
		t.Errorf("unnecessary runInBand argument: %v", request.Args)
	}
}

func TestEvaluateDefaultConfigurationUsesControlledRoot(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFile(t, repository, "package.json", `{"devDependencies":{"jest":"30.5.2"}}`)
	writeFile(t, repository, "node_modules/jest/package.json", `{"name":"jest","version":"30.5.2"}`)
	writeFile(t, repository, "node_modules/jest/bin/jest.js", "")
	_, resolver := evaluatorFixture(t, ".", "30.5.2")
	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte("[]")}}
	runner.run = func(_ context.Context, request safeexec.Request) (safeexec.Response, error) {
		configIndex := slices.Index(request.Args, "--config")
		if configIndex < 0 || configIndex+1 >= len(request.Args) {
			t.Fatalf("args = %v", request.Args)
		}
		content, err := os.ReadFile(request.Args[configIndex+1])
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]string
		if err := json.Unmarshal(content, &config); err != nil {
			t.Fatal(err)
		}
		physical, err := filepath.EvalSymlinks(repository)
		if err != nil {
			t.Fatal(err)
		}
		if config["rootDir"] != physical {
			t.Fatalf("synthetic rootDir = %q, want %q", config["rootDir"], physical)
		}
		return runner.response, nil
	}
	current := New(runner, resolver)
	instance := detectedInstance(t, current, repository)
	results, err := current.Evaluate(
		context.Background(), allowJest(t), provider.Repository{Root: repository}, instance, []scope.Path{"src/a.test.js"},
	)
	if err != nil || results[0].State != scope.Out {
		t.Fatalf("results/error = %#v/%v", results, err)
	}
}

func TestEvaluatePermissionAndToolApprovalAreIndependent(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, ".", "29.7.0")
	resolver.err = &safeexec.UnavailableError{
		Tool: nodeTool, Code: "tool/not-approved", Summary: "tool is not approved", Action: "run setup",
	}
	runner := &fakeRunner{}
	current := New(runner, resolver)
	instance := detectedInstance(t, current, repository)
	results, err := current.Evaluate(
		context.Background(), allowJest(t), provider.Repository{Root: repository}, instance, []scope.Path{"src/a.test.js"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "tool/not-approved" || len(runner.requests) != 0 {
		t.Fatalf("results/requests = %#v/%d", results, len(runner.requests))
	}
}

func TestEvaluateFailureModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		runnerErr error
		response  safeexec.Response
		wantCode  string
		wantError bool
	}{
		{name: "timeout", runnerErr: fmt.Errorf("node: %w", context.DeadlineExceeded), wantCode: "command/timeout"},
		{name: "output limit", runnerErr: fmt.Errorf("node: %w", safeexec.ErrOutputLimit), wantCode: "command/output-limit"},
		{name: "nonzero", response: safeexec.Response{ExitCode: 1, Stderr: []byte("bad config")}, wantCode: "jest/evaluator-failed"},
		{name: "malformed output", response: safeexec.Response{Stdout: []byte("{")}, wantCode: "jest/evaluator-output-invalid"},
		{name: "containment error", runnerErr: errors.New("containment failed"), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, resolver := evaluatorFixture(t, ".", "29.7.0")
			runner := &fakeRunner{err: test.runnerErr, response: test.response}
			current := New(runner, resolver)
			instance := detectedInstance(t, current, repository)
			results, err := current.Evaluate(
				context.Background(), allowJest(t), provider.Repository{Root: repository}, instance, []scope.Path{"src/a.test.js"},
			)
			if test.wantError {
				if err == nil {
					t.Fatal("Evaluate() error = nil")
				}
				return
			}
			if err != nil || results[0].State != scope.Unknown || results[0].Explanation.Code != test.wantCode {
				t.Fatalf("results/error = %#v/%v", results, err)
			}
		})
	}
}

func TestEvaluateRepositoryUnionPrecedence(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, ".", "29.7.0")
	writeFile(t, repository, "packages/b/shared.test.js", "")
	writeFile(t, repository, "packages/b/package.json", `{"devDependencies":{"jest":"29.7.0"}}`)
	writeFile(t, repository, "packages/b/jest.config.js", "module.exports = {};\n")
	runner := &fakeRunner{}
	runner.run = func(_ context.Context, request safeexec.Request) (safeexec.Response, error) {
		if !strings.HasSuffix(request.Dir, filepath.FromSlash("packages/b")) {
			listed, _ := json.Marshal([]string{filepath.Join(repository, "packages/b/shared.test.js")})
			return safeexec.Response{Stdout: listed}, nil
		}
		return safeexec.Response{ExitCode: 1, Stderr: []byte("broken")}, nil
	}
	current := New(runner, resolver)
	instance := detectedInstance(t, current, repository)
	results, err := current.Evaluate(
		context.Background(), allowJest(t), provider.Repository{Root: repository}, instance,
		[]scope.Path{"packages/b/shared.test.js", "packages/b/unknown.test.js", "README.md"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.In || results[1].State != scope.Unknown || results[2].State != scope.Out {
		t.Fatalf("results = %#v", results)
	}
}

func TestEvaluateKeepsPathAndDiscoveryUncertaintyHonest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliable on Windows test hosts")
	}
	repository, resolver := evaluatorFixture(t, ".", "29.7.0")
	writeFile(t, repository, "src/selected.test.js", "")
	writeFile(t, repository, "src/ordinary.js", "")
	if err := os.Symlink("selected.test.js", filepath.Join(repository, "src/link.test.js")); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	runner.run = func(_ context.Context, _ safeexec.Request) (safeexec.Response, error) {
		content, _ := json.Marshal([]string{filepath.Join(repository, "src/selected.test.js")})
		return safeexec.Response{Stdout: content}, nil
	}
	current := New(runner, resolver)
	current.scanLimit = 1
	instance := detectedInstance(t, current, repository)
	results, err := current.Evaluate(
		context.Background(), allowJest(t), provider.Repository{Root: repository}, instance,
		[]scope.Path{"src/selected.test.js", "src/ordinary.js", "src/link.test.js"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.In || results[1].State != scope.Unknown ||
		results[1].Explanation.Code != "jest/discovery-limit" || results[2].State != scope.Unknown ||
		results[2].Explanation.Code != "jest/path-uninspectable" {
		t.Fatalf("results = %#v", results)
	}
}

func TestParseListTests(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inside := filepath.Join(root, "src", "line\nbreak.test.js")
	outside := filepath.Join(t.TempDir(), "outside.test.js")
	writeFile(t, root, "src/line\nbreak.test.js", "")
	writeFile(t, filepath.Dir(outside), filepath.Base(outside), "")
	valid, _ := json.Marshal([]string{inside, outside})
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseListTests(valid, physical)
	if err != nil || len(got) != 1 {
		t.Fatalf("parseListTests() = %v, %v", got, err)
	}
	duplicate, _ := json.Marshal([]string{inside, inside})
	got, err = parseListTests(duplicate, physical)
	if err != nil || len(got) != 1 {
		t.Fatalf("parseListTests(duplicate) = %v, %v", got, err)
	}
	tests := [][]byte{
		[]byte(`{"not":"a list"}`),
		[]byte(`["relative.test.js"]`),
		append(valid, []byte(` []`)...),
	}
	for _, content := range tests {
		if _, err := parseListTests(content, physical); err == nil {
			t.Errorf("parseListTests(%q) error = nil", content)
		}
	}
}

func TestSetupSelectionsAreRelevantWithoutExecution(t *testing.T) {
	t.Parallel()
	repository, resolver := evaluatorFixture(t, ".", "29.7.0")
	runner := &fakeRunner{}
	current := New(runner, resolver)
	instances, err := current.Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	if got := current.SetupSelections(repository, instances); !reflect.DeepEqual(got, []safeexec.Selection{{Tool: nodeTool}}) {
		t.Fatalf("SetupSelections() = %#v", got)
	}
	if resolver.calls != 0 || len(runner.requests) != 0 {
		t.Fatalf("setup discovery resolved %d tool(s) and ran %d command(s)", resolver.calls, len(runner.requests))
	}
}

func TestValidateNodeExecutable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		goos   string
		name   string
		header []byte
		valid  bool
	}{
		{goos: "linux", name: "node", header: []byte{0x7f, 'E', 'L', 'F'}, valid: true},
		{goos: "darwin", name: "node", header: []byte{0xcf, 0xfa, 0xed, 0xfe}, valid: true},
		{goos: "windows", name: "node.exe", header: []byte{'M', 'Z', 0, 0}, valid: true},
		{goos: "linux", name: "node", header: []byte{'M', 'Z', 0, 0}},
		{goos: "darwin", name: "node.exe", header: []byte{0xcf, 0xfa, 0xed, 0xfe}},
		{goos: "freebsd", name: "node", header: []byte{0x7f, 'E', 'L', 'F'}},
	}
	for _, test := range tests {
		t.Run(test.goos+"/"+test.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), test.name)
			if err := os.WriteFile(name, test.header, 0o600); err != nil {
				t.Fatal(err)
			}
			err := validateNodeExecutable(name, test.goos)
			if (err == nil) != test.valid {
				t.Fatalf("validateNodeExecutable() error = %v, valid = %t", err, test.valid)
			}
		})
	}
	if runtime.GOOS == "plan9" {
		t.Log("platform-specific host is not required by this table")
	}
}

func TestDiagnosticEvidenceIsBoundedAndValidUTF8(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("a", 2047) + "ż" + "tail"
	got := truncateUTF8(value, 2048)
	if len(got) != 2047 || !strings.HasSuffix(got, "a") {
		t.Fatalf("truncateUTF8() length/suffix = %d/%q", len(got), got[len(got)-1:])
	}
	if got := commandEvidence(safeexec.Response{ExitCode: 2}); got != "evaluator exited with status 2" {
		t.Fatalf("commandEvidence() = %q", got)
	}
}
