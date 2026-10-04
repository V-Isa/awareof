package typescript

import (
	"context"
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

func TestProviderUnionSemanticsAndReferences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		rootConfig   string
		responses    map[string]safeexec.Response
		paths        []scope.Path
		wantStates   []scope.State
		wantEvidence string
		keepStdout   bool
	}{
		{
			name:       "in from referenced project",
			rootConfig: `{"files":[],"references":[{"path":"configs/app.build.json"}]}`,
			responses: map[string]safeexec.Response{
				"tsconfig.json":          {},
				"configs/app.build.json": {Stdout: nil},
			},
			paths:        []scope.Path{"src/app.ts"},
			wantStates:   []scope.State{scope.In},
			wantEvidence: "configs/app.build.json",
		},
		{
			name:       "out when every program resolves",
			rootConfig: `{}`,
			responses:  map[string]safeexec.Response{"tsconfig.json": {}},
			paths:      []scope.Path{"src/missing.ts"},
			wantStates: []scope.State{scope.Out},
		},
		{
			name:       "unknown when compiler fails",
			rootConfig: `{}`,
			responses: map[string]safeexec.Response{
				"tsconfig.json": {Stdout: []byte("partial"), Stderr: []byte("bad config"), ExitCode: 2},
			},
			paths:        []scope.Path{"src/app.ts"},
			wantStates:   []scope.State{scope.Unknown},
			wantEvidence: "bad config",
		},
		{
			name:       "unknown when compiler output is ambiguous",
			rootConfig: `{}`,
			responses: map[string]safeexec.Response{
				"tsconfig.json": {Stdout: []byte("relative.ts\n")},
			},
			paths:        []scope.Path{"src/app.ts"},
			wantStates:   []scope.State{scope.Unknown},
			wantEvidence: "not absolute",
			keepStdout:   true,
		},
		{
			name:       "known in takes precedence over unresolved reference parsing",
			rootConfig: `{"references":[{"path":"../outside.json"}]}`,
			responses:  map[string]safeexec.Response{"tsconfig.json": {}},
			paths:      []scope.Path{"src/app.ts", "src/other.ts"},
			wantStates: []scope.State{scope.In, scope.Unknown},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "tsconfig.json", test.rootConfig, 0o600)
			writeTestFile(t, root, "configs/app.build.json", `{}`, 0o600)
			projectPackage := filepath.Join(root, "node_modules", "typescript")
			writeCompilerPackage(t, projectPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
			externalPackage := filepath.Join(t.TempDir(), "typescript")
			writeCompilerPackage(t, externalPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
			targetPath := resolvedPath(t, filepath.Join(externalPackage, "lib", "_tsc.js"))
			runner := &fakeRunner{responses: make(map[string]safeexec.Response, len(test.responses))}
			for config, response := range test.responses {
				if response.ExitCode == 0 && !test.keepStdout {
					included := filepath.Join(root, "src", "app.ts") + "\n"
					if config == "tsconfig.json" && strings.Contains(test.rootConfig, `"files":[]`) {
						included = ""
					}
					response.Stdout = []byte(included)
				}
				runner.responses[config] = response
			}
			current := New(runner, fixedDiscoverer{target: safeexec.Target{
				Tool:   typescriptTool,
				Path:   targetPath,
				Origin: safeexec.ExternalOrigin,
			}})
			instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			results, err := current.Evaluate(
				context.Background(),
				provider.EvaluationContext{},
				provider.Repository{Root: root},
				instances[0],
				test.paths,
			)
			if err != nil {
				t.Fatal(err)
			}
			states := make([]scope.State, len(results))
			for index := range results {
				states[index] = results[index].State
				if results[index].Instance != rootInstanceID || results[index].Provider != providerID {
					t.Fatalf("result identity = %+v", results[index])
				}
			}
			if !reflect.DeepEqual(states, test.wantStates) {
				t.Fatalf("states = %v, want %v", states, test.wantStates)
			}
			if test.wantEvidence != "" && !strings.Contains(results[0].Explanation.Evidence, test.wantEvidence) {
				t.Fatalf("evidence = %q, want containing %q", results[0].Explanation.Evidence, test.wantEvidence)
			}
		})
	}
}

func TestProviderEvaluatorSafety(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		projectVer string
		targetVer  string
		origin     safeexec.Origin
		mutate     bool
		noProject  bool
		wantCode   string
		wantRuns   int
	}{
		{name: "matching external TypeScript 6", projectVer: "6.0.3", targetVer: "6.0.3", origin: safeexec.ExternalOrigin, wantCode: "typescript/not-program-member", wantRuns: 2},
		{name: "project compiler missing", targetVer: "6.0.3", origin: safeexec.ExternalOrigin, noProject: true, wantCode: "typescript/project-compiler-unavailable"},
		{name: "repository TypeScript 6 rejected", projectVer: "6.0.3", targetVer: "6.0.3", origin: safeexec.RepositoryOrigin, wantCode: "typescript/repository-javascript-unsupported"},
		{name: "version mismatch", projectVer: "6.0.3", targetVer: "6.0.4", origin: safeexec.ExternalOrigin, wantCode: "typescript/evaluator-version-mismatch"},
		{name: "package patch mismatch", projectVer: "6.0.3", targetVer: "6.0.3", origin: safeexec.ExternalOrigin, mutate: true, wantCode: "typescript/evaluator-identity-mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
			if !test.noProject {
				projectPackage := filepath.Join(root, "node_modules", "typescript")
				writeCompilerPackage(t, projectPackage, "typescript", test.projectVer, filepath.Join("lib", "_tsc.js"), []byte("compiler"))
			}
			targetPackage := filepath.Join(t.TempDir(), "typescript")
			core := []byte("compiler")
			if test.mutate {
				core = []byte("patched")
			}
			writeCompilerPackage(t, targetPackage, "typescript", test.targetVer, filepath.Join("lib", "_tsc.js"), core)
			runner := &fakeRunner{responses: map[string]safeexec.Response{"tsconfig.json": {}}}
			current := New(runner, fixedDiscoverer{target: safeexec.Target{
				Tool:   typescriptTool,
				Path:   resolvedPath(t, filepath.Join(targetPackage, "lib", "_tsc.js")),
				Origin: test.origin,
			}})
			instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			results, err := current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], []scope.Path{"a.ts"})
			if err != nil {
				t.Fatal(err)
			}
			if results[0].Explanation.Code != test.wantCode || len(runner.requests) != test.wantRuns {
				t.Fatalf("result = %+v, runs = %d; want code %q, runs %d", results[0], len(runner.requests), test.wantCode, test.wantRuns)
			}
			if test.wantRuns != 0 {
				request := runner.requests[1]
				if request.Interpreter != nodeTool || !request.ExternalOnly || request.Env["NODE_ENV"] != "production" || request.Env["NODE_DISABLE_COMPILE_CACHE"] != "1" {
					t.Fatalf("request = %+v, want hardened TypeScript 6 execution", request)
				}
				for _, name := range []string{"FORCE_COLOR", "NODE_COMPILE_CACHE", "NODE_OPTIONS", "NODE_PATH", "NODE_PRESERVE_SYMLINKS", "NODE_REDIRECT_WARNINGS", "NODE_REPORT_DIRECTORY", "NODE_V8_COVERAGE"} {
					if !slices.Contains(request.UnsetEnv, name) {
						t.Errorf("UnsetEnv = %v, want %s removed", request.UnsetEnv, name)
					}
				}
			}
		})
	}
}

func TestProviderReturnsOperationalErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	projectPackage := filepath.Join(root, "node_modules", "typescript")
	writeCompilerPackage(t, projectPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	externalPackage := filepath.Join(t.TempDir(), "typescript")
	writeCompilerPackage(t, externalPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	runner := &fakeRunner{err: errors.New("runner failed")}
	current := New(runner, fixedDiscoverer{target: safeexec.Target{Tool: typescriptTool, Path: resolvedPath(t, filepath.Join(externalPackage, "lib", "_tsc.js")), Origin: safeexec.ExternalOrigin}})
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	_, err = current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], []scope.Path{"a.ts"})
	if err == nil || !strings.Contains(err.Error(), "runner failed") {
		t.Fatalf("Evaluate() error = %v, want runner failure", err)
	}
}

func TestProviderRejectsUnsafeOrUnresolvedEffectiveConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		response safeexec.Response
		wantCode string
	}{
		{
			name:     "write-causing option",
			response: safeexec.Response{Stdout: []byte(`{"compilerOptions":{"generateTrace":"./trace"}}`)},
			wantCode: "typescript/write-causing-option-unsupported",
		},
		{
			name:     "compiler failure",
			response: safeexec.Response{ExitCode: 2, Stderr: []byte("invalid config")},
			wantCode: "typescript/config-preflight-failed",
		},
		{
			name:     "ambiguous output",
			response: safeexec.Response{Stdout: []byte(`{`)},
			wantCode: "typescript/config-preflight-ambiguous",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
			projectPackage := filepath.Join(root, "node_modules", "typescript")
			writeCompilerPackage(t, projectPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
			externalPackage := filepath.Join(t.TempDir(), "typescript")
			writeCompilerPackage(t, externalPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
			runner := &fakeRunner{showConfigs: map[string]safeexec.Response{"tsconfig.json": test.response}}
			current := New(runner, fixedDiscoverer{target: safeexec.Target{
				Tool: typescriptTool, Path: resolvedPath(t, filepath.Join(externalPackage, "lib", "_tsc.js")), Origin: safeexec.ExternalOrigin,
			}})
			instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			results, err := current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], []scope.Path{"a.ts"})
			if err != nil {
				t.Fatal(err)
			}
			if len(runner.requests) != 1 || results[0].State != scope.Unknown || results[0].Explanation.Code != test.wantCode {
				t.Fatalf("requests = %d, result = %+v; want preflight-only UNKNOWN code %q", len(runner.requests), results[0], test.wantCode)
			}
		})
	}
}

func TestProviderContractFailures(t *testing.T) {
	t.Parallel()
	current := New(&fakeRunner{}, fixedDiscoverer{})
	repo := provider.Repository{Root: t.TempDir()}
	paths := []scope.Path{"a.ts"}
	tests := []struct {
		name     string
		instance provider.Instance
		want     string
	}{
		{name: "wrong type", instance: provider.InstanceDescriptor{Provider: providerID, ID: rootInstanceID}, want: "unsupported TypeScript instance"},
		{name: "wrong identity", instance: typescriptInstance{descriptor: provider.InstanceDescriptor{Provider: "git", ID: "bad"}}, want: "unsupported instance"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := current.Evaluate(context.Background(), provider.EvaluationContext{}, repo, test.instance, paths)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Evaluate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestUnresolvedEvidenceIsBounded(t *testing.T) {
	t.Parallel()
	evaluation := newUnionEvaluation([]scope.Path{"a.ts"})
	for index := range 7 {
		evaluation.addUnresolved(
			fmt.Sprintf("project-%d/tsconfig.json", index),
			scope.Explanation{Code: "typescript/compiler-failed", Summary: "compiler failed", Evidence: strings.Repeat("x", 1000)},
			typescriptTool,
		)
	}
	result := evaluation.results(rootInstanceID)[0]
	if result.State != scope.Unknown || !strings.Contains(result.Explanation.Evidence, "and 4 more") || len(result.Explanation.Evidence) > 2051 {
		t.Fatalf("result = %+v, want bounded unresolved evidence", result)
	}
}

type fixedDiscoverer struct {
	target safeexec.Target
	err    error
}

func (d fixedDiscoverer) Discover(string, safeexec.ToolID) (safeexec.Target, error) {
	return d.target, d.err
}

func (d fixedDiscoverer) DiscoverAt(_ string, _ safeexec.ToolID, _ string) (safeexec.Target, error) {
	return d.target, d.err
}

func (fixedDiscoverer) ApprovedTargets(string, safeexec.ToolID) ([]safeexec.Target, error) {
	return []safeexec.Target{}, nil
}

type fakeRunner struct {
	responses   map[string]safeexec.Response
	showConfigs map[string]safeexec.Response
	err         error
	requests    []safeexec.Request
	run         func(safeexec.Request) (safeexec.Response, error)
}

func (r *fakeRunner) Run(_ context.Context, request safeexec.Request) (safeexec.Response, error) {
	r.requests = append(r.requests, request)
	if r.run != nil {
		return r.run(request)
	}
	if r.err != nil {
		return safeexec.Response{}, r.err
	}
	if len(request.Args) < 2 {
		return safeexec.Response{}, errors.New("missing config argument")
	}
	if slices.Contains(request.Args, "--showConfig") {
		if response, ok := r.showConfigs[filepath.ToSlash(request.Args[1])]; ok {
			return response, nil
		}
		return safeexec.Response{Stdout: []byte(`{"compilerOptions":{}}`)}, nil
	}
	return r.responses[filepath.ToSlash(request.Args[1])], nil
}

func writeCompilerPackage(t *testing.T, root, name, version, coreName string, core []byte) {
	t.Helper()
	writeTestFile(t, root, "package.json", `{"name":"`+name+`","version":"`+version+`"}`, 0o600)
	writeTestFile(t, root, filepath.ToSlash(coreName), string(core), 0o700)
	writeTestFile(t, root, "lib/lib.d.ts", "/// standard library", 0o600)
}

func writeTestFile(t *testing.T, root, name, content string, mode os.FileMode) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func resolvedPath(t *testing.T, name string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func testNativeCore() []byte {
	switch runtime.GOOS {
	case "windows":
		return []byte{'M', 'Z', 0, 0}
	case "darwin":
		return []byte{0xcf, 0xfa, 0xed, 0xfe}
	default:
		return []byte{0x7f, 'E', 'L', 'F'}
	}
}
