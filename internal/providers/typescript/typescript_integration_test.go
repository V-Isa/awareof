//go:build integration

package typescript

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

const (
	typeScript6Root = "/opt/typescript6/node_modules/typescript"
	typeScript7Root = "/opt/typescript7/node_modules/typescript"
	integrationNode = "/usr/local/bin/node"
)

func TestTypeScript6NativeParity(t *testing.T) {
	requireIntegrationContainer(t)
	root := t.TempDir()
	linkPackage(t, root, "node_modules/typescript", typeScript6Root)
	linkPackage(t, root, "node_modules/.bin/tsc", filepath.Join(typeScript7Root, "bin", "tsc"))
	writeTestFile(t, root, "base.json", `{
  "compilerOptions": {
    "allowJs": true,
    "resolveJsonModule": true,
    "module": "commonjs",
    "moduleResolution": "node",
    "ignoreDeprecations": "6.0",
    "typeRoots": ["./types"],
    "types": ["fixture"],
    "plugins": [{"name":"malicious-plugin"}],
    "noEmit": true
  }
}`, 0o600)
	writeTestFile(t, root, "node_modules/fixture-config/package.json", `{"name":"fixture-config","version":"1.0.0"}`, 0o600)
	writeTestFile(t, root, "node_modules/fixture-config/base.json", `{"extends":"../../base.json"}`, 0o600)
	writeTestFile(t, root, "node_modules/malicious-plugin/package.json", `{"name":"malicious-plugin","version":"1.0.0","main":"index.js"}`, 0o600)
	writeTestFile(t, root, "node_modules/malicious-plugin/index.js", `require("fs").writeFileSync("plugin-ran", "unsafe")`, 0o600)
	writeTestFile(t, root, "tsconfig.json", `{
  "extends": "fixture-config/base.json",
  "files": ["src/entry.ts"],
  "exclude": ["src/excluded.ts"]
}`, 0o600)
	writeTestFile(t, root, "src/entry.ts", `
/// <reference path="./triple.ts" />
import "./excluded";
import "../shared/outside";
import "./helper.js";
import data from "./data.json";
export { data };
`, 0o600)
	writeTestFile(t, root, "src/excluded.ts", `export {};`, 0o600)
	writeTestFile(t, root, "src/triple.ts", `export {};`, 0o600)
	writeTestFile(t, root, "src/helper.js", `module.exports = {};`, 0o600)
	writeTestFile(t, root, "src/data.json", `{}`, 0o600)
	writeTestFile(t, root, "src/unused.ts", `export {};`, 0o600)
	writeTestFile(t, root, "shared/outside.ts", `export {};`, 0o600)
	writeTestFile(t, root, "types/fixture/index.d.ts", `declare const fixture: string;`, 0o600)
	t.Setenv("NODE_COMPILE_CACHE", filepath.Join(root, "node-compile-cache"))
	t.Setenv("NODE_REDIRECT_WARNINGS", filepath.Join(root, "node-warnings.log"))
	t.Setenv("NODE_V8_COVERAGE", filepath.Join(root, "node-coverage"))

	paths := []scope.Path{
		"src/entry.ts",
		"src/excluded.ts",
		"src/triple.ts",
		"src/helper.js",
		"src/data.json",
		"shared/outside.ts",
		"types/fixture/index.d.ts",
		"src/unused.ts",
		"src/future.ts",
	}
	providerStates := evaluateIntegrationProvider(t, root, paths)
	for _, name := range []string{"plugin-ran", "node-compile-cache", "node-warnings.log", "node-coverage"} {
		assertNotCreated(t, root, name)
	}
	oracle := runTypeScriptOracle(t, root, integrationNode, filepath.Join(typeScript6Root, "bin", "tsc"), paths)
	assertParity(t, paths, providerStates, oracle)
	for index := 0; index < 7; index++ {
		if providerStates[index] != scope.In {
			t.Errorf("%s = %s, want IN", paths[index], providerStates[index])
		}
	}
	for index := 7; index < len(paths); index++ {
		if providerStates[index] != scope.Out {
			t.Errorf("%s = %s, want OUT", paths[index], providerStates[index])
		}
	}
}

func TestTypeScript6ProjectReferencesAndUnion(t *testing.T) {
	requireIntegrationContainer(t)
	root := t.TempDir()
	linkPackage(t, root, "node_modules/typescript", typeScript6Root)
	writeTestFile(t, root, "tsconfig.json", `{
  "files": [],
  "references": [
    {"path":"configs/app.build.json"},
    {"path":"packages/other"}
  ]
}`, 0o600)
	writeTestFile(t, root, "configs/app.build.json", `{"compilerOptions":{"composite":true},"files":["../src/app.ts"]}`, 0o600)
	writeTestFile(t, root, "packages/other/tsconfig.json", `{"compilerOptions":{"composite":true},"files":["../../src/shared.ts"]}`, 0o600)
	writeTestFile(t, root, "src/app.ts", `import "./shared";`, 0o600)
	writeTestFile(t, root, "src/shared.ts", `export {};`, 0o600)
	paths := []scope.Path{"src/app.ts", "src/shared.ts", "src/missing.ts"}

	states := evaluateIntegrationProvider(t, root, paths)
	want := []scope.State{scope.In, scope.In, scope.Out}
	for index := range want {
		if states[index] != want[index] {
			t.Errorf("%s = %s, want %s", paths[index], states[index], want[index])
		}
	}
}

func TestTypeScript7DirectNativeParity(t *testing.T) {
	requireIntegrationContainer(t)
	if runtime.GOOS != "linux" {
		t.Skip("the integration image exercises TypeScript 7 on Linux")
	}
	platformRoot := integrationTypeScript7Platform(t)
	root := t.TempDir()
	linkPackage(t, root, "node_modules/typescript", typeScript7Root)
	linkPackage(t, root, "node_modules/@typescript/"+filepath.Base(platformRoot), platformRoot)
	writeTestFile(t, root, "node_modules/malicious-plugin/package.json", `{"name":"malicious-plugin","version":"1.0.0","main":"index.js"}`, 0o600)
	writeTestFile(t, root, "node_modules/malicious-plugin/index.js", `require("fs").writeFileSync("plugin-ran", "unsafe")`, 0o600)
	writeTestFile(t, root, "tsconfig.json", `{
  "compilerOptions":{"plugins":[{"name":"malicious-plugin"}]},
  "files":["src/app.ts"]
}`, 0o600)
	writeTestFile(t, root, "src/app.ts", `export {};`, 0o600)
	writeTestFile(t, root, "src/unused.ts", `export {};`, 0o600)
	paths := []scope.Path{"src/app.ts", "src/unused.ts"}

	states := evaluateIntegrationProvider(t, root, paths)
	oracle := runTypeScriptOracle(t, root, integrationNode, filepath.Join(typeScript7Root, "bin", "tsc"), paths)
	assertParity(t, paths, states, oracle)
	if states[0] != scope.In || states[1] != scope.Out {
		t.Fatalf("states = %v, want [IN OUT]", states)
	}
	assertNotCreated(t, root, "plugin-ran")
}

func TestTypeScriptRejectsGenerateTraceWithoutWriting(t *testing.T) {
	requireIntegrationContainer(t)
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
	}{
		{name: "TypeScript 6", prepare: func(t *testing.T, root string) {
			linkPackage(t, root, "node_modules/typescript", typeScript6Root)
		}},
		{name: "TypeScript 7", prepare: func(t *testing.T, root string) {
			if runtime.GOOS != "linux" {
				t.Skip("the integration image exercises TypeScript 7 on Linux")
			}
			platformRoot := integrationTypeScript7Platform(t)
			linkPackage(t, root, "node_modules/typescript", typeScript7Root)
			linkPackage(t, root, "node_modules/@typescript/"+filepath.Base(platformRoot), platformRoot)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.prepare(t, root)
			writeTestFile(t, root, "tsconfig.json", `{"compilerOptions":{"generateTrace":"./trace"},"files":["app.ts"]}`, 0o600)
			writeTestFile(t, root, "app.ts", `export {};`, 0o600)
			states := evaluateIntegrationProvider(t, root, []scope.Path{"app.ts"})
			if len(states) != 1 || states[0] != scope.Unknown {
				t.Fatalf("states = %v, want UNKNOWN", states)
			}
			assertNotCreated(t, root, "trace")
		})
	}
}

func integrationTypeScript7Platform(t *testing.T) string {
	t.Helper()
	name, err := platformPackageName(platform{goos: runtime.GOOS, goarch: runtime.GOARCH})
	if err != nil {
		t.Skip(err)
	}
	return filepath.Join("/opt/typescript7/node_modules/@typescript", name)
}

func evaluateIntegrationProvider(t *testing.T, root string, paths []scope.Path) []scope.State {
	t.Helper()
	store := safeexec.FileApprovalStore{Path: filepath.Join(t.TempDir(), "approvals.json")}
	manager, err := safeexec.NewManager([]safeexec.Tool{
		{ID: nodeTool, Command: integrationNode},
		{ID: typescriptTool, SelectionRequired: true},
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	current := New(safeexec.Runner{Resolver: manager}, manager)
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 {
		t.Fatalf("Detect() returned %d instances, want 1", len(instances))
	}
	selections, err := current.SetupSelections(root, instances)
	if err != nil {
		t.Fatal(err)
	}
	statuses := manager.StatusesForSelections(root, selections)
	for _, status := range statuses {
		if status.State == safeexec.UnavailableState {
			t.Fatalf("setup status = %+v", status)
		}
		if _, err := manager.ApproveTarget(root, status.Target); err != nil {
			t.Fatal(err)
		}
	}
	results, err := current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], paths)
	if err != nil {
		t.Fatal(err)
	}
	states := make([]scope.State, len(results))
	for index := range results {
		states[index] = results[index].State
	}
	return states
}

func runTypeScriptOracle(t *testing.T, root, interpreter, launcher string, paths []scope.Path) map[scope.Path]struct{} {
	t.Helper()
	command := exec.Command(interpreter, launcher, "-p", "tsconfig.json", "--listFilesOnly", "--pretty", "false")
	command.Dir = root
	command.Env = append(cleanNodeEnvironment(os.Environ()), "NODE_ENV=production", "NO_COLOR=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("official TypeScript launcher failed: %v: %s", err, output)
	}
	queries, unresolved, err := prepareQueryIndex(root, paths, runtime.GOOS)
	if err != nil || len(unresolved) != 0 {
		t.Fatalf("prepare oracle queries = %v, %v; want no unresolved paths", err, unresolved)
	}
	included, err := parseListFiles(output, queries)
	if err != nil {
		t.Fatal(err)
	}
	return included
}

func cleanNodeEnvironment(environment []string) []string {
	blocked := make(map[string]struct{}, len(typeScriptUnsetEnvironment))
	for _, key := range typeScriptUnsetEnvironment {
		blocked[key] = struct{}{}
	}
	clean := make([]string, 0, len(environment))
	for _, item := range environment {
		key, _, _ := strings.Cut(item, "=")
		if _, skip := blocked[key]; !skip {
			clean = append(clean, item)
		}
	}
	sort.Strings(clean)
	return clean
}

func assertParity(t *testing.T, paths []scope.Path, states []scope.State, oracle map[scope.Path]struct{}) {
	t.Helper()
	for index, name := range paths {
		_, in := oracle[name]
		want := scope.Out
		if in {
			want = scope.In
		}
		if states[index] != want {
			t.Errorf("%s = %s, official launcher membership gives %s", name, states[index], want)
		}
	}
}

func linkPackage(t *testing.T, root, relative, target string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func assertNotCreated(t *testing.T, root, relative string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, relative)); !os.IsNotExist(err) {
		t.Fatalf("unexpected repository entry %q was created: %v", relative, err)
	}
}

func requireIntegrationContainer(t *testing.T) {
	t.Helper()
	if os.Getenv("AWAREOF_INTEGRATION_CONTAINER") != "1" {
		t.Fatal("integration tests run only in the disposable container; use 'make integration'")
	}
}
