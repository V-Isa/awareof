//go:build integration

package jest

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

const integrationNode = "/usr/local/bin/node"

func TestJestListTestsNativeParity(t *testing.T) {
	requireIntegrationContainer(t)
	versions := []struct {
		version string
		root    string
	}{
		{version: "29.7.0", root: "/opt/jest297/node_modules"},
		{version: "30.5.2", root: "/opt/jest3052/node_modules"},
	}
	for _, version := range versions {
		t.Run(version.version, func(t *testing.T) {
			repository := t.TempDir()
			copyNodeModules(t, version.root, filepath.Join(repository, "node_modules"))
			writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"jest":"`+version.version+`"}}`)
			writeIntegrationFile(t, repository, "jest.config.js", `
const fs = require("node:fs");
module.exports = async () => {
  fs.writeFileSync("config-ran", "yes");
  return {
    roots: ["<rootDir>/src"],
    testMatch: ["**/*.test.js", "**/*.spec.ts"],
    testPathIgnorePatterns: ["/ignored/"],
  };
};
`)
			paths := []scope.Path{
				"src/a.test.js",
				"src/b.spec.ts",
				"src/ignored/c.test.js",
				"src/plain.js",
				"outside/d.test.js",
				"src/żółć.test.js",
				"src/line\nbreak.test.js",
			}
			for _, name := range paths {
				content := ""
				if strings.HasSuffix(string(name), ".test.js") {
					content = `require("node:fs").writeFileSync("test-ran", "bad");`
				}
				writeIntegrationFile(t, repository, string(name), content)
			}

			got := evaluateIntegrationJest(t, repository, paths, true)
			want := runJestOracle(t, repository)
			for index, name := range paths {
				_, selected := want[string(name)]
				wantState := scope.Out
				if selected {
					wantState = scope.In
				}
				if got[index].State != wantState {
					t.Errorf("%s = %s, native oracle %s", name, got[index].State, wantState)
				}
			}
			if content, err := os.ReadFile(filepath.Join(repository, "config-ran")); err != nil || string(content) != "yes" {
				t.Fatalf("config side effect = %q, %v", content, err)
			}
			assertNotCreated(t, repository, "test-ran")
		})
	}
}

func TestJestRepositoryCodeRunsOnlyWithDelegation(t *testing.T) {
	requireIntegrationContainer(t)
	repository := t.TempDir()
	copyNodeModules(t, "/opt/jest297/node_modules", filepath.Join(repository, "node_modules"))
	writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"jest":"29.7.0"}}`)
	writeIntegrationFile(t, repository, "jest.config.js", `require("node:fs").writeFileSync("config-ran", "yes"); module.exports = {};`)
	writeIntegrationFile(t, repository, "src/a.test.js", "")

	withoutPermission := evaluateIntegrationJest(t, repository, []scope.Path{"src/a.test.js"}, false)
	if withoutPermission[0].State != scope.Unknown || withoutPermission[0].Explanation.Code != "jest/repository-code-not-allowed" {
		t.Fatalf("default result = %#v", withoutPermission[0])
	}
	assertNotCreated(t, repository, "config-ran")

	withPermission := evaluateIntegrationJest(t, repository, []scope.Path{"src/a.test.js"}, true)
	if withPermission[0].State != scope.In {
		t.Fatalf("delegated result = %#v", withPermission[0])
	}
}

func TestJestConfigurationFailuresRemainUnknown(t *testing.T) {
	requireIntegrationContainer(t)
	repository := t.TempDir()
	copyNodeModules(t, "/opt/jest297/node_modules", filepath.Join(repository, "node_modules"))
	writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"jest":"29.7.0"}}`)
	writeIntegrationFile(t, repository, "src/a.test.js", "")

	tests := []struct {
		name     string
		config   string
		wantCode string
	}{
		{name: "configuration throws", config: `throw new Error("fixture failure");`, wantCode: "jest/evaluator-failed"},
		{name: "configuration pollutes stdout", config: `process.stdout.write("fixture noise"); module.exports = {};`, wantCode: "jest/evaluator-output-invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writeIntegrationFile(t, repository, "jest.config.js", test.config)
			result := evaluateIntegrationJest(t, repository, []scope.Path{"src/a.test.js"}, true)[0]
			if result.State != scope.Unknown || result.Explanation.Code != test.wantCode {
				t.Fatalf("result = %#v, want UNKNOWN %s", result, test.wantCode)
			}
		})
	}

	if err := os.Remove(filepath.Join(repository, "jest.config.js")); err != nil {
		t.Fatal(err)
	}
	writeIntegrationFile(t, repository, "jest.config.ts", `export default {testMatch:["**/*.test.js"]};`)
	result := evaluateIntegrationJest(t, repository, []scope.Path{"src/a.test.js"}, true)[0]
	if result.State != scope.Unknown || result.Explanation.Code != "jest/evaluator-failed" {
		t.Fatalf("TypeScript config result = %#v, want UNKNOWN jest/evaluator-failed", result)
	}
}

func TestJestPresetAndSequencerCanChangeMembership(t *testing.T) {
	requireIntegrationContainer(t)
	repository := t.TempDir()
	copyNodeModules(t, "/opt/jest297/node_modules", filepath.Join(repository, "node_modules"))
	writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"jest":"29.7.0","fixture-preset":"1.0.0"}}`)
	writeIntegrationFile(t, repository, "node_modules/fixture-preset/package.json", `{"name":"fixture-preset","version":"1.0.0"}`)
	writeIntegrationFile(t, repository, "node_modules/fixture-preset/jest-preset.js", `
require("node:fs").writeFileSync("preset-ran", "yes");
module.exports = { testMatch: ["**/*.candidate.js"] };
`)
	writeIntegrationFile(t, repository, "sequencer.js", `
const Base = require("@jest/test-sequencer").default;
require("node:fs").writeFileSync("sequencer-ran", "yes");
module.exports = class extends Base {
  sort(tests) { return tests.filter(test => !test.path.endsWith("removed.candidate.js")); }
};
`)
	writeIntegrationFile(t, repository, "resolver.js", `
module.exports = (request, options) => {
  require("node:fs").writeFileSync("resolver-ran", "yes");
  return options.defaultResolver(request, options);
};
`)
	writeIntegrationFile(t, repository, "jest.config.js", `module.exports = {preset:"fixture-preset",resolver:"<rootDir>/resolver.js",testSequencer:"<rootDir>/sequencer.js"};`)
	writeIntegrationFile(t, repository, "kept.candidate.js", "")
	writeIntegrationFile(t, repository, "removed.candidate.js", "")

	results := evaluateIntegrationJest(t, repository, []scope.Path{"kept.candidate.js", "removed.candidate.js"}, true)
	if results[0].State != scope.In || results[1].State != scope.Out {
		t.Fatalf("results = %#v", results)
	}
	for _, marker := range []string{"preset-ran", "resolver-ran", "sequencer-ran"} {
		if _, err := os.Stat(filepath.Join(repository, marker)); err != nil {
			t.Fatalf("%s was not executed: %v", marker, err)
		}
	}
}

func TestJestPackageConfigAndProjectsParity(t *testing.T) {
	requireIntegrationContainer(t)
	repository := t.TempDir()
	copyNodeModules(t, "/opt/jest3052/node_modules", filepath.Join(repository, "node_modules"))
	writeIntegrationFile(t, repository, "package.json", `{
  "private": true,
  "devDependencies": {"jest":"30.5.2"},
  "jest": {"projects":["<rootDir>/packages/a", "<rootDir>/packages/b"]}
}`)
	writeIntegrationFile(t, repository, "packages/a/jest.config.js", `module.exports = {testMatch:["**/*.alpha.js"]};`)
	writeIntegrationFile(t, repository, "packages/b/jest.config.js", `module.exports = {testMatch:["**/*.beta.js"]};`)
	writeIntegrationFile(t, repository, "packages/a/a.alpha.js", "")
	writeIntegrationFile(t, repository, "packages/a/a.other.js", "")
	writeIntegrationFile(t, repository, "packages/b/b.beta.js", "")
	paths := []scope.Path{"packages/a/a.alpha.js", "packages/a/a.other.js", "packages/b/b.beta.js"}

	results := evaluateIntegrationJest(t, repository, paths, true)
	if results[0].State != scope.In || results[1].State != scope.Out || results[2].State != scope.In {
		t.Fatalf("results = %#v", results)
	}
}

func TestJestConfigDiscoveryIsVersionSensitive(t *testing.T) {
	requireIntegrationContainer(t)
	versions := []struct {
		version string
		root    string
		want    scope.State
	}{
		{version: "29.7.0", root: "/opt/jest297/node_modules", want: scope.Out},
		{version: "30.5.2", root: "/opt/jest3052/node_modules", want: scope.In},
	}
	for _, version := range versions {
		t.Run(version.version, func(t *testing.T) {
			repository := t.TempDir()
			copyNodeModules(t, version.root, filepath.Join(repository, "node_modules"))
			writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"jest":"`+version.version+`"}}`)
			writeIntegrationFile(t, repository, "jest.config.mts", `export default {testMatch:["**/*.chosen.js"]};`)
			writeIntegrationFile(t, repository, "a.chosen.js", "")

			absolute, output, err := runJestListTests(t, repository, false)
			if err != nil {
				t.Fatalf("Jest auto-discovery failed: %v: %s", err, output)
			}
			native := relativeOracleSet(t, repository, absolute)
			_, nativeIn := native["a.chosen.js"]
			if nativeIn != (version.want == scope.In) {
				t.Fatalf("native a.chosen.js selected = %t, want state %s", nativeIn, version.want)
			}
			results := evaluateIntegrationJest(t, repository, []scope.Path{"a.chosen.js"}, true)
			if results[0].State != version.want {
				t.Fatalf("result = %#v, want %s", results[0], version.want)
			}
		})
	}
}

func TestJestPackageConfigJSONReferenceIsVersionSensitive(t *testing.T) {
	requireIntegrationContainer(t)
	versions := []struct {
		version   string
		root      string
		supported bool
	}{
		{version: "29.7.0", root: "/opt/jest297/node_modules"},
		{version: "30.5.2", root: "/opt/jest3052/node_modules", supported: true},
	}
	for _, version := range versions {
		t.Run(version.version, func(t *testing.T) {
			repository := t.TempDir()
			copyNodeModules(t, version.root, filepath.Join(repository, "node_modules"))
			writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"jest":"`+version.version+`"},"jest":"./config/jest.json"}`)
			writeIntegrationFile(t, repository, "config/jest.json", `{"testMatch":["**/*.chosen.js"]}`)
			writeIntegrationFile(t, repository, "config/a.chosen.js", "")
			writeIntegrationFile(t, repository, "config/a.other.js", "")

			absolute, output, nativeErr := runJestListTests(t, repository, false)
			if version.supported {
				if nativeErr != nil {
					t.Fatalf("Jest package config failed: %v: %s", nativeErr, output)
				}
				native := relativeOracleSet(t, repository, absolute)
				if _, selected := native["config/a.chosen.js"]; !selected {
					t.Fatalf("native test list = %#v", native)
				}
			} else if nativeErr == nil {
				t.Fatalf("Jest 29 unexpectedly accepted package config path: %s", output)
			}

			results := evaluateIntegrationJest(t, repository, []scope.Path{"config/a.chosen.js", "config/a.other.js"}, true)
			if version.supported {
				if results[0].State != scope.In || results[1].State != scope.Out {
					t.Fatalf("results = %#v", results)
				}
			} else if results[0].State != scope.Unknown || results[1].State != scope.Unknown || results[0].Explanation.Code != "jest/config-uninspectable" {
				t.Fatalf("results = %#v", results)
			}
		})
	}
}

func TestJestDefaultConfigDoesNotLoadExternalAncestor(t *testing.T) {
	requireIntegrationContainer(t)
	outer := t.TempDir()
	repository := filepath.Join(outer, "repository")
	copyNodeModules(t, "/opt/jest297/node_modules", filepath.Join(repository, "node_modules"))
	writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"jest":"29.7.0"}}`)
	writeIntegrationFile(t, repository, "a.test.js", "")
	writeIntegrationFile(t, outer, "jest.config.js", `require("node:fs").writeFileSync("ancestor-ran", "bad"); module.exports = {testMatch:[]};`)
	results := evaluateIntegrationJest(t, repository, []scope.Path{"a.test.js"}, true)
	if results[0].State != scope.In {
		t.Fatalf("result = %#v", results[0])
	}
	assertNotCreated(t, repository, "ancestor-ran")
}

func TestJestListTestsDoesNotExecuteRuntimeModules(t *testing.T) {
	requireIntegrationContainer(t)
	repository := t.TempDir()
	copyNodeModules(t, "/opt/jest297/node_modules", filepath.Join(repository, "node_modules"))
	writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"jest":"29.7.0"}}`)
	for _, name := range []string{"setup", "global-setup", "global-teardown", "reporter", "environment", "runner", "transform"} {
		writeIntegrationFile(t, repository, name+".js", `require("node:fs").writeFileSync("`+name+`-ran", "bad"); module.exports = class {};`)
	}
	writeIntegrationFile(t, repository, "jest.config.js", `
module.exports = {
  setupFiles: ["<rootDir>/setup.js"],
  globalSetup: "<rootDir>/global-setup.js",
  globalTeardown: "<rootDir>/global-teardown.js",
  reporters: ["<rootDir>/reporter.js"],
  testEnvironment: "<rootDir>/environment.js",
  runner: "<rootDir>/runner.js",
  transform: {"^.+\\.js$":"<rootDir>/transform.js"},
};
`)
	writeIntegrationFile(t, repository, "a.test.js", "")
	results := evaluateIntegrationJest(t, repository, []scope.Path{"a.test.js"}, true)
	if results[0].State != scope.In {
		t.Fatalf("result = %#v", results[0])
	}
	for _, name := range []string{"setup", "global-setup", "global-teardown", "reporter", "environment", "runner", "transform"} {
		assertNotCreated(t, repository, name+"-ran")
	}
}

func evaluateIntegrationJest(t *testing.T, root string, paths []scope.Path, allowRepoCode bool) []scope.Result {
	t.Helper()
	store := safeexec.FileApprovalStore{Path: filepath.Join(t.TempDir(), "approvals.json")}
	manager, err := safeexec.NewManager([]safeexec.Tool{{ID: nodeTool, Command: integrationNode}}, store)
	if err != nil {
		t.Fatal(err)
	}
	statuses := manager.StatusesForSelections(root, []safeexec.Selection{{Tool: nodeTool}})
	if len(statuses) != 1 || statuses[0].State == safeexec.UnavailableState {
		t.Fatalf("Node status = %+v", statuses)
	}
	if _, err := manager.ApproveTarget(root, statuses[0].Target); err != nil {
		t.Fatal(err)
	}
	current := New(safeexec.Runner{Resolver: manager}, manager)
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 {
		t.Fatalf("Detect() returned %d instances, want one", len(instances))
	}
	evaluation := provider.EvaluationContext{}
	if allowRepoCode {
		evaluation, err = provider.NewEvaluationContext([]scope.ProviderID{providerID})
		if err != nil {
			t.Fatal(err)
		}
	}
	results, err := current.Evaluate(context.Background(), evaluation, provider.Repository{Root: root}, instances[0], paths)
	if err != nil {
		t.Fatal(err)
	}
	return results
}

func runJestOracle(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	absolute, output, err := runJestListTests(t, root, true)
	if err != nil {
		t.Fatalf("Jest oracle failed: %v: %s", err, output)
	}
	return relativeOracleSet(t, root, absolute)
}

func runJestListTests(t *testing.T, root string, explicitConfig bool) ([]string, []byte, error) {
	t.Helper()
	entry := filepath.Join(root, "node_modules/jest/bin/jest.js")
	arguments := []string{entry}
	if explicitConfig {
		arguments = append(arguments, "--config", filepath.Join(root, "jest.config.js"))
	}
	arguments = append(arguments, "--listTests", "--json", "--no-cache", "--no-watchman")
	command := exec.Command(integrationNode, arguments...) //nolint:gosec // fixed integration-only oracle.
	command.Dir = root
	command.Env = cleanNodeEnvironment(os.Environ())
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, output, err
	}
	var absolute []string
	if err := json.Unmarshal(output, &absolute); err != nil {
		return nil, output, err
	}
	return absolute, output, nil
}

func relativeOracleSet(t *testing.T, root string, absolute []string) map[string]struct{} {
	t.Helper()
	result := make(map[string]struct{}, len(absolute))
	for _, name := range absolute {
		relative, err := filepath.Rel(root, name)
		if err != nil {
			t.Fatal(err)
		}
		result[filepath.ToSlash(relative)] = struct{}{}
	}
	return result
}

func copyNodeModules(t *testing.T, source, destination string) {
	t.Helper()
	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	err := fs.WalkDir(os.DirFS(source), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		content, err := fs.ReadFile(os.DirFS(source), name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeIntegrationFile(t *testing.T, root, name, content string) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte(strings.TrimPrefix(content, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNotCreated(t *testing.T, root, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected side effect %s: %v", name, err)
	}
}

func cleanNodeEnvironment(environment []string) []string {
	blocked := make(map[string]struct{}, len(nodeUnsetEnvironment))
	for _, name := range nodeUnsetEnvironment {
		blocked[name] = struct{}{}
	}
	return slices.DeleteFunc(append([]string(nil), environment...), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		_, exists := blocked[name]
		return exists
	})
}

func requireIntegrationContainer(t *testing.T) {
	t.Helper()
	if os.Getenv("AWAREOF_INTEGRATION_CONTAINER") != "1" {
		t.Fatal("integration tests must run in the hardened integration container")
	}
}
