//go:build integration

package eslint

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

const integrationNode = "/usr/local/bin/node"

func TestESLintRootConfigNativeParity(t *testing.T) {
	requireIntegrationContainer(t)
	t.Setenv("NODE_ENV", "awareof-parity")
	t.Setenv("ESLINT_FLAGS", "unstable_config_lookup_from_file")
	versions := []struct {
		version string
		root    string
	}{
		{version: "9.39.5", root: "/opt/eslint939/node_modules"},
		{version: "10.12.0", root: "/opt/eslint10120/node_modules"},
	}
	for _, version := range versions {
		t.Run(version.version, func(t *testing.T) {
			repository := t.TempDir()
			copyNodeModules(t, version.root, filepath.Join(repository, "node_modules"))
			writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"eslint":"`+version.version+`"}}`)
			writeIntegrationFile(t, repository, "eslint.config.js", `
module.exports = [
  { ignores: ["dist/**"] },
  { files: process.env.NODE_ENV === "awareof-parity" ? ["src/**/*.js", "src/**/*.custom"] : [], ignores: ["**/*.skip.js"], rules: {} },
  { basePath: "packages/app", files: ["**/*.ts"], ignores: ["ignored/**"], rules: {} },
];
`)
			paths := []scope.Path{
				"src/app.js",
				"src/app.skip.js",
				"dist/generated.js",
				"notes.txt",
				"packages/app/src/app.ts",
				"packages/app/ignored/app.ts",
				"packages/other/src/app.ts",
				"src/żółć.js",
				"src/line\nbreak.js",
				"src/environment.custom",
			}
			for _, name := range paths {
				writeIntegrationFile(t, repository, string(name), "")
			}

			got := evaluateIntegrationESLint(t, repository, paths, true)
			want := runESLintOracle(t, repository, paths, false)
			for index, name := range paths {
				if got[index].State != want[index] {
					t.Errorf("%s = %s, native lookup %s", name, got[index].State, want[index])
				}
			}
			if got[0].State != scope.In || got[1].State != scope.In || got[2].State != scope.Out ||
				got[3].State != scope.Out || got[4].State != scope.In || got[5].State != scope.Out || got[6].State != scope.Out ||
				got[7].State != scope.In || got[8].State != scope.In || got[9].State != scope.In {
				t.Fatalf("unexpected decision table: %#v", got)
			}
		})
	}
}

func TestESLintEvaluatorContainsConfigOutputAndOpenHandles(t *testing.T) {
	requireIntegrationContainer(t)
	repository := t.TempDir()
	copyNodeModules(t, "/opt/eslint939/node_modules", filepath.Join(repository, "node_modules"))
	writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"eslint":"9.39.5"}}`)
	writeIntegrationFile(t, repository, "eslint.config.js", `
console.log("repository diagnostic");
setInterval(() => {}, 1000);
module.exports = [{files:["**/*.js"]}];
`)
	writeIntegrationFile(t, repository, "src/app.js", "")
	results := evaluateIntegrationESLint(t, repository, []scope.Path{"src/app.js"}, true)
	if results[0].State != scope.In {
		t.Fatalf("delegated result = %#v", results[0])
	}
}

func TestESLintEvaluatorRemovesConfigModeEnvironment(t *testing.T) {
	requireIntegrationContainer(t)
	t.Setenv("ESLINT_USE_FLAT_CONFIG", "false")
	repository := t.TempDir()
	copyNodeModules(t, "/opt/eslint939/node_modules", filepath.Join(repository, "node_modules"))
	writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"eslint":"9.39.5"}}`)
	writeIntegrationFile(t, repository, "eslint.config.js", `module.exports = process.env.ESLINT_USE_FLAT_CONFIG === undefined ? [{files:["**/*.js"]}] : [];`)
	writeIntegrationFile(t, repository, "src/app.js", "")
	results := evaluateIntegrationESLint(t, repository, []scope.Path{"src/app.js"}, true)
	if results[0].State != scope.In {
		t.Fatalf("delegated result = %#v", results[0])
	}
}

func TestESLintESModuleConfig(t *testing.T) {
	requireIntegrationContainer(t)
	repository := t.TempDir()
	copyNodeModules(t, "/opt/eslint10120/node_modules", filepath.Join(repository, "node_modules"))
	writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"eslint":"10.12.0"}}`)
	writeIntegrationFile(t, repository, "eslint.config.mjs", `export default [{files:["**/*.ts"]}];`)
	writeIntegrationFile(t, repository, "src/app.ts", "")
	results := evaluateIntegrationESLint(t, repository, []scope.Path{"src/app.ts"}, true)
	if results[0].State != scope.In {
		t.Fatalf("ES module config result = %#v", results[0])
	}
}

func TestESLintExplicitConfigMatchesRootLookup(t *testing.T) {
	requireIntegrationContainer(t)
	for _, packageRoot := range []string{
		"/opt/eslint939/node_modules",
		"/opt/eslint10120/node_modules",
	} {
		t.Run(filepath.Base(filepath.Dir(packageRoot)), func(t *testing.T) {
			repository := t.TempDir()
			copyNodeModules(t, packageRoot, filepath.Join(repository, "node_modules"))
			writeIntegrationFile(t, repository, "eslint.config.js", `module.exports = [{basePath:"pkg",files:["src/**/*.js"],ignores:["ignored/**"]}];`)
			paths := []scope.Path{"pkg/src/app.js", "pkg/ignored/app.js", "src/app.js"}
			for _, name := range paths {
				writeIntegrationFile(t, repository, string(name), "")
			}
			native := runESLintOracle(t, repository, paths, false)
			explicit := runESLintOracle(t, repository, paths, true)
			for index, name := range paths {
				if explicit[index] != native[index] {
					t.Errorf("%s explicit = %s, native = %s", name, explicit[index], native[index])
				}
			}
		})
	}
}

func TestESLintNestedConfigIsUnknownInsteadOfDiverging(t *testing.T) {
	requireIntegrationContainer(t)
	repository := t.TempDir()
	copyNodeModules(t, "/opt/eslint10120/node_modules", filepath.Join(repository, "node_modules"))
	writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"eslint":"10.12.0"}}`)
	writeIntegrationFile(t, repository, "eslint.config.js", `module.exports = [{files:["**/*.js"]}];`)
	writeIntegrationFile(t, repository, "packages/app/eslint.config.js", `module.exports = [{files:["**/*.ts"]}];`)
	writeIntegrationFile(t, repository, "src/root.js", "")
	writeIntegrationFile(t, repository, "packages/app/src/app.ts", "")
	paths := []scope.Path{"src/root.js", "packages/app/src/app.ts"}

	got := evaluateIntegrationESLint(t, repository, paths, true)
	native := runESLintOracle(t, repository, paths, false)
	if got[0].State != scope.In || got[1].State != scope.Unknown ||
		got[1].Explanation.Code != "eslint/nested-config-unsupported" {
		t.Fatalf("provider results = %#v", got)
	}
	if native[1] != scope.In {
		t.Fatalf("native nested lookup = %s, want IN", native[1])
	}
}

func TestESLintRepositoryCodeRunsOnlyWithExplicitDelegation(t *testing.T) {
	requireIntegrationContainer(t)
	repository := t.TempDir()
	copyNodeModules(t, "/opt/eslint939/node_modules", filepath.Join(repository, "node_modules"))
	writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"eslint":"9.39.5"}}`)
	writeIntegrationFile(t, repository, "eslint.config.js", `require("node:fs").writeFileSync("config-ran", "yes"); module.exports = [{files:["**/*.js"]}];`)
	writeIntegrationFile(t, repository, "src/app.js", "")

	withoutPermission := evaluateIntegrationESLint(t, repository, []scope.Path{"src/app.js"}, false)
	if withoutPermission[0].State != scope.Unknown || withoutPermission[0].Explanation.Code != "eslint/repository-code-not-allowed" {
		t.Fatalf("default result = %#v", withoutPermission[0])
	}
	assertNotCreated(t, repository, "config-ran")

	withPermission := evaluateIntegrationESLint(t, repository, []scope.Path{"src/app.js"}, true)
	if withPermission[0].State != scope.In {
		t.Fatalf("delegated result = %#v", withPermission[0])
	}
	if content, err := os.ReadFile(filepath.Join(repository, "config-ran")); err != nil || string(content) != "yes" {
		t.Fatalf("delegated side effect = %q, %v", content, err)
	}
}

func TestESLintConfigFailureIsPathLocalUnknown(t *testing.T) {
	requireIntegrationContainer(t)
	repository := t.TempDir()
	copyNodeModules(t, "/opt/eslint939/node_modules", filepath.Join(repository, "node_modules"))
	writeIntegrationFile(t, repository, "package.json", `{"private":true,"devDependencies":{"eslint":"9.39.5"}}`)
	writeIntegrationFile(t, repository, "eslint.config.js", `throw new Error("fixture failure");`)
	writeIntegrationFile(t, repository, "src/app.js", "")
	results := evaluateIntegrationESLint(t, repository, []scope.Path{"src/app.js"}, true)
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "eslint/evaluation-unavailable" {
		t.Fatalf("result = %#v", results[0])
	}
}

func evaluateIntegrationESLint(t *testing.T, root string, paths []scope.Path, allowRepoCode bool) []scope.Result {
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

func runESLintOracle(t *testing.T, root string, paths []scope.Path, explicitConfig bool) []scope.State {
	t.Helper()
	script := filepath.Join(t.TempDir(), "oracle.mjs")
	writeIntegrationFile(t, filepath.Dir(script), filepath.Base(script), `
import process from "node:process";
import { pathToFileURL } from "node:url";
const root = process.argv[2];
const paths = JSON.parse(process.argv[3]);
const explicit = process.argv[4] === "true";
const loaded = await import(pathToFileURL(root + "/node_modules/eslint/lib/api.js").href);
const options = { cwd: root, cache: false };
if (explicit) options.overrideConfigFile = root + "/eslint.config.js";
const eslint = new loaded.ESLint(options);
const states = [];
for (const file of paths) {
  if (await eslint.isPathIgnored(root + "/" + file)) {
    states.push("OUT");
    continue;
  }
  states.push((await eslint.calculateConfigForFile(root + "/" + file)) == null ? "OUT" : "IN");
}
process.stdout.write(JSON.stringify(states));
`)
	encoded, err := json.Marshal(paths)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(integrationNode, script, root, string(encoded), map[bool]string{true: "true", false: "false"}[explicitConfig]) //nolint:gosec // fixed integration-only oracle.
	command.Dir = root
	command.Env = append(cleanNodeEnvironment(os.Environ()), "ESLINT_USE_FLAT_CONFIG=true")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("ESLint oracle failed: %v: %s", err, output)
	}
	var states []scope.State
	if err := json.Unmarshal(output, &states); err != nil {
		t.Fatalf("decode ESLint oracle: %v: %s", err, output)
	}
	return states
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
	clean := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if _, exists := blocked[name]; !exists {
			clean = append(clean, entry)
		}
	}
	return clean
}

func requireIntegrationContainer(t *testing.T) {
	t.Helper()
	if os.Getenv("AWAREOF_INTEGRATION_CONTAINER") != "1" {
		t.Fatal("integration tests must run in the hardened integration container")
	}
}
