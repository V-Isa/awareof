package npm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestNeedsNativeTools(t *testing.T) {
	t.Parallel()
	p := New(nil)
	tests := []struct {
		name      string
		instances []provider.Instance
		want      bool
	}{
		{name: "none"},
		{name: "publishable", instances: []provider.Instance{npmInstance{packageRoot: "."}}, want: runtime.GOOS != "windows"},
		{name: "private", instances: []provider.Instance{npmInstance{packageRoot: ".", private: true}}},
		{name: "publication script", instances: []provider.Instance{npmInstance{packageRoot: ".", scripts: []string{"prepare"}}}},
		{name: "unresolved", instances: []provider.Instance{npmInstance{packageRoot: ".", unavailable: &scope.Explanation{Code: "npm/test", Summary: "unresolved"}}}},
		{name: "unrelated instance", instances: []provider.Instance{provider.InstanceDescriptor{Provider: "test", ID: "test"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := p.NeedsNativeTools(test.instances); got != test.want {
				t.Fatalf("NeedsNativeTools() = %t, want %t", got, test.want)
			}
		})
	}
	if got := (&Provider{}).NeedsNativeTools([]provider.Instance{npmInstance{packageRoot: "."}}); got != (runtime.GOOS != "windows") {
		t.Fatalf("zero-value NeedsNativeTools() = %t on %s", got, runtime.GOOS)
	}
}

func TestProviderDetectPackages(t *testing.T) {
	t.Parallel()
	if got := New(nil).ID(); got != providerID {
		t.Fatalf("ID() = %q, want npm", got)
	}

	root := t.TempDir()
	writePackage(t, root, ".", `{
  "name": "root-package",
  "private": true,
  "workspaces": ["packages/*", "tools/cli"]
}`)
	writePackage(t, root, "packages/a", `{"name":"a"}`)
	writePackage(t, root, "packages/b", `{"name":"b","scripts":{"prepare":"build"}}`)
	makeDir(t, root, "packages/empty")
	writePackage(t, root, "tools/cli", `{"name":"cli"}`)
	writePackage(t, root, "node_modules/ignored", `{"name":"ignored"}`)

	instances, err := New(nil).Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 4 {
		t.Fatalf("Detect() returned %d instances, want 4", len(instances))
	}
	want := []struct {
		id          scope.InstanceID
		packageRoot scope.Path
		name        string
		private     bool
		scripts     []string
	}{
		{id: "npm", packageRoot: ".", name: "root-package", private: true},
		{id: "package/packages/a", packageRoot: "packages/a", name: "a"},
		{id: "package/packages/b", packageRoot: "packages/b", name: "b", scripts: []string{"prepare"}},
		{id: "package/tools/cli", packageRoot: "tools/cli", name: "cli"},
	}
	for index, raw := range instances {
		got, ok := raw.(npmInstance)
		if !ok {
			t.Fatalf("instance[%d] has type %T", index, raw)
		}
		expected := want[index]
		if got.Descriptor().ID != expected.id || got.packageRoot != expected.packageRoot || got.name != expected.name || got.private != expected.private || !reflect.DeepEqual(got.scripts, expected.scripts) {
			t.Errorf("instance[%d] = %+v, want %+v", index, got, expected)
		}
	}
}

func TestProviderDetectBoundaries(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name      string
		ctx       context.Context
		root      func(*testing.T) string
		manifest  string
		wantCount int
		wantCode  string
		wantError string
	}{
		{name: "no package", ctx: context.Background(), root: func(t *testing.T) string { return t.TempDir() }},
		{name: "invalid package", ctx: context.Background(), root: func(t *testing.T) string { return t.TempDir() }, manifest: "{", wantCount: 1, wantCode: "npm/package-json-unavailable"},
		{name: "unsupported workspaces", ctx: context.Background(), root: func(t *testing.T) string { return t.TempDir() }, manifest: `{"workspaces":["packages/**"]}`, wantCount: 2, wantCode: "npm/workspace-discovery-unsupported"},
		{name: "cancelled", ctx: cancelled, root: func(t *testing.T) string { return t.TempDir() }, wantError: "context canceled"},
		{name: "empty root", ctx: context.Background(), root: func(*testing.T) string { return "" }, wantError: "repository root is empty"},
		{name: "missing root", ctx: context.Background(), root: func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing") }, wantError: "open repository root"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := test.root(t)
			if test.manifest != "" {
				writePackage(t, root, ".", test.manifest)
			}
			instances, err := New(nil).Detect(test.ctx, provider.Repository{Root: root})
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("Detect() error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(instances) != test.wantCount {
				t.Fatalf("Detect() returned %d instances, want %d", len(instances), test.wantCount)
			}
			if test.wantCode != "" {
				got, ok := instances[len(instances)-1].(npmInstance)
				if !ok {
					t.Fatalf("instance has type %T", instances[len(instances)-1])
				}
				if got.unavailable == nil || got.unavailable.Code != test.wantCode {
					t.Fatalf("unavailable = %+v, want %q", got.unavailable, test.wantCode)
				}
			}
		})
	}
}

func TestProviderEvaluateDecisionTable(t *testing.T) {
	t.Setenv("npm_config_script_shell", "/repository/controlled/shell")
	root := t.TempDir()
	writePackage(t, root, ".", `{"name":"demo"}`)
	instances, err := New(nil).Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte(`[{
  "name":"demo","files":[{"path":"package.json"},{"path":"src/index.js"}]
}]`)}}
	paths := []scope.Path{"future.js", "package.json", "src/index.js"}
	results, err := testProvider(runner).Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], paths)
	if err != nil {
		t.Fatal(err)
	}
	wantStates := []scope.State{scope.Out, scope.In, scope.In}
	for index, result := range results {
		if result.State != wantStates[index] || result.Provenance != (scope.Provenance{Method: scope.SafeNative, Tool: "npm"}) {
			t.Errorf("result[%d] = %+v, want %s safe-native npm", index, result, wantStates[index])
		}
	}
	if len(runner.requests) != 2 {
		t.Fatalf("runner calls = %d, want version and pack", len(runner.requests))
	}
	if !reflect.DeepEqual(runner.requests[0].Args, []string{"--version"}) {
		t.Fatalf("version args = %q", runner.requests[0].Args)
	}
	request := runner.requests[1]
	if request.Interpreter != "node" || runner.requests[0].Interpreter != "node" {
		t.Fatalf("interpreters = %q, %q, want node", runner.requests[0].Interpreter, request.Interpreter)
	}
	if !request.ExternalOnly || !runner.requests[0].ExternalOnly {
		t.Fatal("npm requests do not require external tool targets")
	}
	for _, want := range []string{"pack", root, "--dry-run", "--json", "--ignore-scripts", "--offline", "--workspaces=false"} {
		if !contains(request.Args, want) {
			t.Errorf("request args = %q, missing %q", request.Args, want)
		}
	}
	if request.Tool != "npm" || request.Root != root || request.Dir == "" || request.Dir == root || request.Env["NPM_CONFIG_IGNORE_SCRIPTS"] != "true" || request.Env["NPM_CONFIG_CACHE"] == "" {
		t.Errorf("unsafe npm request = %+v", request)
	}
	for _, want := range []string{"NODE_OPTIONS", "NODE_PATH", "NPM_CONFIG_NODE_OPTIONS"} {
		if !contains(request.UnsetEnv, want) {
			t.Errorf("unset environment = %q, missing %q", request.UnsetEnv, want)
		}
	}
	if !contains(request.UnsetEnv, "npm_config_script_shell") {
		t.Errorf("unset environment = %q, missing ambient npm configuration", request.UnsetEnv)
	}
	if _, err := os.Stat(filepath.Dir(request.Env["NPM_CONFIG_USERCONFIG"])); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("isolated npm directory still exists: %v", err)
	}
}

func TestProviderEvaluateWorkspaceAndNonApplicable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePackage(t, root, "packages/a", `{"name":"a"}`)
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	config, status := readPackage(rootHandle, "packages/a")
	if err := rootHandle.Close(); err != nil {
		t.Fatal(err)
	}
	current := newInstance("packages/a", config, status)
	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte(`[{"files":[{"path":"index.js"}]}]`)}}
	results, err := testProvider(runner).Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, current, []scope.Path{"outside.js", "packages/a/index.js", "packages/a/test.js"})
	if err != nil {
		t.Fatal(err)
	}
	want := []scope.State{scope.NotApp, scope.In, scope.Out}
	for index := range want {
		if results[index].State != want[index] {
			t.Errorf("result[%d] = %+v, want %s", index, results[index], want[index])
		}
	}
	if got := runner.requests[1].Args[1]; got != filepath.Join(root, "packages", "a") {
		t.Fatalf("package spec = %q, want absolute workspace path", got)
	}
}

func TestProviderDoesNotRunForSafeUnknownOrNonApplicableCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		instance   npmInstance
		path       scope.Path
		wantState  scope.State
		wantCode   string
		provenance scope.ProvenanceMethod
	}{
		{name: "private", instance: npmInstance{descriptor: descriptorFor("."), packageRoot: ".", private: true, name: "private"}, path: "a", wantState: scope.NotApp, wantCode: "npm/private-package", provenance: scope.SafeParser},
		{name: "lifecycle", instance: npmInstance{descriptor: descriptorFor("."), packageRoot: ".", scripts: []string{"prepack", "prepare"}}, path: "a", wantState: scope.Unknown, wantCode: "npm/publication-script", provenance: scope.Unavailable},
		{name: "unreadable", instance: npmInstance{descriptor: descriptorFor("."), packageRoot: ".", unavailable: &scope.Explanation{Code: "npm/package-json-unavailable", Summary: "bad manifest"}}, path: "a", wantState: scope.Unknown, wantCode: "npm/package-json-unavailable", provenance: scope.Unavailable},
		{name: "workspace outside", instance: npmInstance{descriptor: descriptorFor("packages/a"), packageRoot: "packages/a"}, path: "outside", wantState: scope.NotApp, wantCode: "npm/outside-package", provenance: scope.SafeParser},
		{name: "workspace discovery", instance: npmInstance{descriptor: provider.InstanceDescriptor{Provider: providerID, ID: workspaceUnknownID}, unavailable: &scope.Explanation{Code: "npm/workspace-discovery-unsupported", Summary: "unsupported"}}, path: "a", wantState: scope.Unknown, wantCode: "npm/workspace-discovery-unsupported", provenance: scope.Unavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeRunner{err: errors.New("must not run")}
			results, err := testProvider(runner).Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: t.TempDir()}, test.instance, []scope.Path{test.path})
			if err != nil {
				t.Fatal(err)
			}
			if len(runner.requests) != 0 || results[0].State != test.wantState || results[0].Explanation.Code != test.wantCode || results[0].Provenance.Method != test.provenance {
				t.Fatalf("Evaluate() = %+v with %d calls", results, len(runner.requests))
			}
		})
	}
}

func TestProviderUnavailableToolReturnsUnknown(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePackage(t, root, ".", `{"name":"demo"}`)
	current := detectedInstance(t, root)
	unavailable := &safeexec.UnavailableError{Tool: "node", Code: "tool/not-approved", Summary: "tool node is not approved", Action: "run awareof --setup"}
	results, err := testProvider(&fakeRunner{err: unavailable}).Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, current, []scope.Path{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.State != scope.Unknown || result.Explanation.Code != unavailable.Code || result.Provenance != (scope.Provenance{Method: scope.Unavailable, Tool: "node", Reference: reference}) {
			t.Errorf("result = %+v", result)
		}
	}
}

func TestProviderDetectsPackageChangeDuringEvaluation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePackage(t, root, ".", `{"name":"demo"}`)
	current := detectedInstance(t, root)
	runner := &fakeRunner{
		response: safeexec.Response{Stdout: []byte(`[{"files":[]}]`)},
		run:      func() { writePackage(t, root, ".", `{"name":"changed"}`) },
	}
	results, err := testProvider(runner).Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, current, []scope.Path{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "npm/package-json-changed" {
		t.Fatalf("result = %+v, want package change UNKNOWN", results[0])
	}
}

func TestProviderEvaluateErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePackage(t, root, ".", `{"name":"demo"}`)
	current := detectedInstance(t, root)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name      string
		ctx       context.Context
		provider  *Provider
		instance  provider.Instance
		repo      provider.Repository
		paths     []scope.Path
		wantError string
	}{
		{name: "wrong type", ctx: context.Background(), provider: testProvider(&fakeRunner{}), instance: provider.InstanceDescriptor{Provider: providerID, ID: rootInstanceID}, repo: provider.Repository{Root: root}, paths: []scope.Path{"a"}, wantError: "unsupported npm instance"},
		{name: "wrong provider", ctx: context.Background(), provider: testProvider(&fakeRunner{}), instance: npmInstance{descriptor: provider.InstanceDescriptor{Provider: "git", ID: rootInstanceID}}, repo: provider.Repository{Root: root}, paths: []scope.Path{"a"}, wantError: "unsupported instance"},
		{name: "cancelled", ctx: cancelled, provider: testProvider(&fakeRunner{}), instance: current, repo: provider.Repository{Root: root}, paths: []scope.Path{"a"}, wantError: "context canceled"},
		{name: "nil runner", ctx: context.Background(), provider: testProvider(nil), instance: current, repo: provider.Repository{Root: root}, paths: []scope.Path{"a"}, wantError: "runner is nil"},
		{name: "runner error", ctx: context.Background(), provider: testProvider(&fakeRunner{err: errors.New("runner failed")}), instance: current, repo: provider.Repository{Root: root}, paths: []scope.Path{"a"}, wantError: "runner failed"},
		{name: "command error", ctx: context.Background(), provider: testProvider(&fakeRunner{responses: []safeexec.Response{{Stdout: []byte("12.0.2\n")}, {ExitCode: 7, Stderr: []byte("bad package")}}}), instance: current, repo: provider.Repository{Root: root}, paths: []scope.Path{"a"}, wantError: "status 7: bad package"},
		{name: "malformed output", ctx: context.Background(), provider: testProvider(&fakeRunner{responses: []safeexec.Response{{Stdout: []byte("12.0.2\n")}, {Stdout: []byte("{")}}}), instance: current, repo: provider.Repository{Root: root}, paths: []scope.Path{"a"}, wantError: "parse npm pack output"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := test.provider.Evaluate(test.ctx, provider.EvaluationContext{}, test.repo, test.instance, test.paths)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Evaluate() error = %v, want containing %q", err, test.wantError)
			}
		})
	}

	runner := &fakeRunner{}
	results, err := testProvider(runner).Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{}, current, nil)
	if err != nil {
		t.Fatal(err)
	}
	if results == nil || len(results) != 0 || len(runner.requests) != 0 {
		t.Fatalf("Evaluate(nil) = (%+v, %d calls), want non-nil empty", results, len(runner.requests))
	}
}

func TestProviderRequiresSafeNPMVersion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePackage(t, root, ".", `{"name":"demo"}`)
	current := detectedInstance(t, root)
	tests := []struct {
		name      string
		response  safeexec.Response
		wantState scope.State
		wantCode  string
		wantError string
	}{
		{name: "npm 10", response: safeexec.Response{Stdout: []byte("10.9.0\n")}, wantState: scope.Unknown, wantCode: "npm/version-unsupported"},
		{name: "invalid", response: safeexec.Response{Stdout: []byte("not-semver\n")}, wantError: "invalid version"},
		{name: "command failure", response: safeexec.Response{ExitCode: 2, Stderr: []byte("broken")}, wantError: "status 2: broken"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeRunner{responses: []safeexec.Response{test.response}}
			results, err := testProvider(runner).Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, current, []scope.Path{"a"})
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("Evaluate() error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if results[0].State != test.wantState || results[0].Explanation.Code != test.wantCode || len(runner.requests) != 1 {
				t.Fatalf("Evaluate() = %+v with %d calls", results, len(runner.requests))
			}
		})
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	t.Parallel()
	if unavailable := unsupportedPlatform("darwin"); unavailable != nil {
		t.Fatalf("darwin unavailable = %+v", unavailable)
	}
	unavailable := unsupportedPlatform("windows")
	if unavailable == nil || unavailable.Tool != "npm" || unavailable.Code != "npm/platform-unsupported" {
		t.Fatalf("windows unavailable = %+v", unavailable)
	}
}

func TestProviderUnsupportedPlatformReturnsUnknown(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePackage(t, root, ".", `{"name":"demo"}`)
	runner := &fakeRunner{}
	p := &Provider{runner: runner, goos: "windows"}
	results, err := p.Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{Root: root},
		detectedInstance(t, root),
		[]scope.Path{"a"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 0 || results[0].State != scope.Unknown || results[0].Explanation.Code != "npm/platform-unsupported" {
		t.Fatalf("Evaluate() = %+v with %d calls, want platform UNKNOWN without execution", results, len(runner.requests))
	}
}

func TestSafeTemporaryDirectoryRejectsRepositoryLocation(t *testing.T) {
	root := t.TempDir()
	temporaryBase := filepath.Join(root, "tmp")
	if err := os.Mkdir(temporaryBase, 0o700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", temporaryBase)
		t.Setenv("TEMP", temporaryBase)
	} else {
		t.Setenv("TMPDIR", temporaryBase)
	}
	_, err := safeTemporaryDirectory(root)
	unavailable, ok := safeexec.AsUnavailable(err)
	if !ok || unavailable.Code != "npm/temporary-state-unavailable" {
		t.Fatalf("safeTemporaryDirectory() error = %v, want temporary-state unavailable", err)
	}
}

func testProvider(runner safeexec.CommandRunner) *Provider {
	return &Provider{runner: runner, goos: "linux"}
}

func TestParsePackageLifecycleSemantics(t *testing.T) {
	t.Parallel()
	config, err := parsePackage([]byte(`{
  "name":"demo",
  "private":false,
  "scripts":{"prepublishOnly":"check","prepack":"build","prepare":"generate","postpack":"cleanup","prepublish":"old"},
  "workspaces":{"packages":["packages/*"]}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if config.Name != "demo" || config.Private || !reflect.DeepEqual(config.PublicationScripts, publicationScripts) {
		t.Fatalf("config = %+v", config)
	}
	patterns, ok := workspacePatterns(config.Workspaces)
	if !ok || !reflect.DeepEqual(patterns, []string{"packages/*"}) {
		t.Fatalf("workspace patterns = %q, %v", patterns, ok)
	}

	for _, content := range []string{`{"name":1}`, `{"private":"yes"}`, `{"scripts":{"prepare":true}}`, `{} {}`} {
		if _, err := parsePackage([]byte(content)); err == nil {
			t.Errorf("parsePackage(%q) error = nil", content)
		}
	}
}

func TestParsePackOutputBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{name: "invalid UTF-8", content: []byte{'[', 0xff}, want: "invalid UTF-8"},
		{name: "multiple reports", content: []byte(`[{"files":[]},{"files":[]}]`), want: "want one"},
		{name: "missing files", content: []byte(`[{}]`), want: "want one"},
		{name: "multiple JSON", content: []byte(`[{"files":[]}] []`), want: "multiple JSON"},
		{name: "empty path", content: []byte(`[{"files":[{"path":""}]}]`), want: "invalid path"},
		{name: "escape", content: []byte(`[{"files":[{"path":"../secret"}]}]`), want: "invalid path"},
		{name: "unclean", content: []byte(`[{"files":[{"path":"a/../b"}]}]`), want: "invalid path"},
		{name: "duplicate", content: []byte(`[{"files":[{"path":"a"},{"path":"a"}]}]`), want: "duplicate path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := parsePackOutput(test.content, ".")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parsePackOutput() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func detectedInstance(t *testing.T, root string) npmInstance {
	t.Helper()
	instances, err := New(nil).Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	instance, ok := instances[0].(npmInstance)
	if !ok {
		t.Fatalf("instance has type %T", instances[0])
	}
	return instance
}

func descriptorFor(packageRoot scope.Path) provider.InstanceDescriptor {
	return newInstance(packageRoot, packageConfig{}, packageReady).Descriptor()
}

type fakeRunner struct {
	response  safeexec.Response
	responses []safeexec.Response
	err       error
	run       func()
	requests  []safeexec.Request
}

func (r *fakeRunner) Run(_ context.Context, request safeexec.Request) (safeexec.Response, error) {
	r.requests = append(r.requests, request)
	if r.run != nil && (len(request.Args) == 0 || request.Args[0] != "--version") {
		r.run()
	}
	if len(r.responses) != 0 {
		response := r.responses[0]
		r.responses = r.responses[1:]
		return response, r.err
	}
	if len(request.Args) != 0 && request.Args[0] == "--version" && r.err == nil {
		return safeexec.Response{Stdout: []byte("12.0.2\n")}, nil
	}
	return r.response, r.err
}

func writePackage(t *testing.T, root, packageRoot, content string) {
	t.Helper()
	directory := root
	if packageRoot != "." {
		directory = filepath.Join(root, filepath.FromSlash(packageRoot))
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func makeDir(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(name)), 0o750); err != nil {
		t.Fatal(err)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
