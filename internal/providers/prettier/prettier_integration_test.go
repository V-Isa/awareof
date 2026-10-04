//go:build integration

package prettier

import (
	"context"
	"encoding/json"
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

func TestPrettierNativeParity(t *testing.T) {
	requireIntegrationContainer(t)
	versions := []struct {
		version string
		root    string
	}{
		{version: "3.0.0", root: "/opt/prettier300/node_modules/prettier"},
		{version: "3.6.2", root: "/opt/prettier362/node_modules/prettier"},
		{version: "3.8.5", root: "/opt/prettier385/node_modules/prettier"},
		{version: "3.9.9", root: "/opt/prettier399/node_modules/prettier"},
	}
	for _, current := range versions {
		t.Run(current.version, func(t *testing.T) {
			root := t.TempDir()
			prepareParityRepository(t, root, current.version, current.root)
			paths := []scope.Path{
				"src/app.js",
				"dist/generated.js",
				"dist/keep.js",
				"ignored.js",
				"shared.js",
				"README.unknown",
				"src/nested/data.custom",
				"src/nested/deep/data.custom",
				"src/future.js",
				"node_modules/example/index.js",
				".hidden.js",
				"src/żółć.js",
			}
			results := evaluateIntegrationProvider(t, root, current.root, paths)
			oracle := runPrettierOracle(t, root, ".", current.root, paths)
			for index, name := range paths {
				if results[index].State != oracle[index] {
					t.Errorf("%s = %s, oracle %s", name, results[index].State, oracle[index])
				}
			}
			if results[0].State != scope.In || results[1].State != scope.Out || results[5].State != scope.Out || results[6].State != scope.In {
				t.Fatalf("unexpected decision-table states: %#v", results)
			}
		})
	}
}

func TestPrettierConfigAboveContextCWDParity(t *testing.T) {
	requireIntegrationContainer(t)
	root := t.TempDir()
	packageRoot := "/opt/prettier399/node_modules/prettier"
	writeIntegrationFile(t, root, ".prettierrc.json", `{"overrides":[{"files":"mobile/src/*.custom","options":{"parser":"json"}}]}`)
	writeIntegrationFile(t, root, "mobile/package.json", `{"private":true,"devDependencies":{"prettier":"3.9.9"}}`)
	target := filepath.Join(root, "mobile/node_modules/prettier")
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(target, os.DirFS(packageRoot)); err != nil {
		t.Fatal(err)
	}
	writeIntegrationFile(t, root, "mobile/src/data.custom", "")
	paths := []scope.Path{"mobile/src/data.custom"}
	results := evaluateIntegrationProvider(t, root, packageRoot, paths)
	oracle := runPrettierOracle(t, root, "mobile", packageRoot, paths)
	if results[0].State != scope.In || results[0].State != oracle[0] {
		t.Fatalf("provider = %s, oracle = %s", results[0].State, oracle[0])
	}
}

func TestPrettierPackageYAMLVersionBoundary(t *testing.T) {
	requireIntegrationContainer(t)
	tests := []struct {
		version string
		root    string
		want    scope.State
	}{
		{version: "3.2.5", root: "/opt/prettier325/node_modules/prettier", want: scope.Out},
		{version: "3.3.0", root: "/opt/prettier330/node_modules/prettier", want: scope.In},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			root := t.TempDir()
			linkPrettierPackage(t, root, test.version, test.root)
			writeIntegrationFile(t, root, "package.yaml", "prettier:\n  overrides:\n    - files: 'src/*.custom'\n      options:\n        parser: json\n")
			writeIntegrationFile(t, root, "src/data.custom", "")
			paths := []scope.Path{"src/data.custom"}
			results := evaluateIntegrationProvider(t, root, test.root, paths)
			oracle := runPrettierOracle(t, root, ".", test.root, paths)
			if results[0].State != test.want || results[0].State != oracle[0] {
				t.Fatalf("provider = %s, oracle = %s, want %s", results[0].State, oracle[0], test.want)
			}
		})
	}
}

func TestPrettierDoesNotInheritConfigOutsideRepository(t *testing.T) {
	requireIntegrationContainer(t)
	outer := t.TempDir()
	root := filepath.Join(outer, "repository")
	packageRoot := "/opt/prettier399/node_modules/prettier"
	linkPrettierPackage(t, root, "3.9.9", packageRoot)
	writeIntegrationFile(t, outer, ".prettierrc.json", `{"overrides":[{"files":"repository/src/*.custom","options":{"parser":"json"}}]}`)
	writeIntegrationFile(t, root, "src/data.custom", "")
	paths := []scope.Path{"src/data.custom"}
	results := evaluateIntegrationProvider(t, root, packageRoot, paths)
	oracle := runPrettierOracle(t, root, ".", packageRoot, paths)
	if results[0].State != scope.Out || oracle[0] != scope.In {
		t.Fatalf("repository-bounded provider = %s, unrestricted oracle = %s", results[0].State, oracle[0])
	}
}

func TestPrettierNeverExecutesConfigurationOrPlugins(t *testing.T) {
	requireIntegrationContainer(t)
	ambientRoot := t.TempDir()
	ambientPaths := []string{
		filepath.Join(ambientRoot, "node-cache"),
		filepath.Join(ambientRoot, "node-report.json"),
		filepath.Join(ambientRoot, "node-warnings.log"),
		filepath.Join(ambientRoot, "node-coverage"),
	}
	t.Setenv("NODE_COMPILE_CACHE", ambientPaths[0])
	t.Setenv("NODE_REPORT_FILENAME", ambientPaths[1])
	t.Setenv("NODE_REDIRECT_WARNINGS", ambientPaths[2])
	t.Setenv("NODE_V8_COVERAGE", ambientPaths[3])
	tests := []struct {
		name       string
		configName string
		config     string
		ignore     string
		want       []scope.State
	}{
		{
			name: "CommonJS config", configName: "prettier.config.cjs",
			config: `require("node:fs").writeFileSync("config-ran", "unsafe"); module.exports = {};`,
			want:   []scope.State{scope.Unknown, scope.Unknown},
		},
		{
			name: "ES module config", configName: "prettier.config.mjs",
			config: `import fs from "node:fs"; fs.writeFileSync("config-ran", "unsafe"); export default {};`,
			want:   []scope.State{scope.Unknown, scope.Unknown},
		},
		{
			name: "shareable config", configName: ".prettierrc",
			config: `"malicious-shareable"`, want: []scope.State{scope.Unknown, scope.Unknown},
		},
		{
			name: "plugin config", configName: ".prettierrc.json",
			config: `{"plugins":["malicious-plugin"]}`, want: []scope.State{scope.Unknown, scope.Unknown},
		},
		{
			name: "ignored before executable config", configName: "prettier.config.cjs",
			config: `require("node:fs").writeFileSync("config-ran", "unsafe"); module.exports = {};`,
			ignore: "ignored.js\n", want: []scope.State{scope.Unknown, scope.Out},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			packageRoot := "/opt/prettier399/node_modules/prettier"
			linkPrettierPackage(t, root, "3.9.9", packageRoot)
			writeIntegrationFile(t, root, test.configName, test.config)
			if test.ignore != "" {
				writeIntegrationFile(t, root, ".prettierignore", test.ignore)
			}
			writeIntegrationFile(t, root, "src/app.js", "")
			writeIntegrationFile(t, root, "ignored.js", "")
			writeIntegrationFile(t, root, "node_modules/malicious-plugin/package.json", `{"name":"malicious-plugin","version":"1.0.0","main":"index.js"}`)
			writeIntegrationFile(t, root, "node_modules/malicious-plugin/index.js", `require("node:fs").writeFileSync("plugin-ran", "unsafe"); module.exports = { languages: [{name:"unsafe",extensions:[".js"],parsers:["unsafe"],isSupported(){require("node:fs").writeFileSync("supported-ran","unsafe");return true;}}] };`)
			writeIntegrationFile(t, root, "node_modules/malicious-shareable/package.json", `{"name":"malicious-shareable","version":"1.0.0","main":"index.js"}`)
			writeIntegrationFile(t, root, "node_modules/malicious-shareable/index.js", `require("node:fs").writeFileSync("shareable-ran", "unsafe"); module.exports = {};`)

			results := evaluateIntegrationProvider(t, root, packageRoot, []scope.Path{"src/app.js", "ignored.js"})
			for index, state := range test.want {
				if results[index].State != state {
					t.Errorf("result %d = %s, want %s (%#v)", index, results[index].State, state, results[index])
				}
			}
			for _, marker := range []string{"config-ran", "plugin-ran", "supported-ran", "shareable-ran"} {
				if _, err := os.Stat(filepath.Join(root, marker)); !os.IsNotExist(err) {
					t.Errorf("unsafe side effect %s exists: %v", marker, err)
				}
			}
		})
	}
	for _, name := range ambientPaths {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Errorf("ambient Node output %s exists: %v", name, err)
		}
	}
}

func prepareParityRepository(t *testing.T, root, version, packageRoot string) {
	t.Helper()
	linkPrettierPackage(t, root, version, packageRoot)
	writeIntegrationFile(t, root, ".gitignore", "dist/**\n!dist/keep.js\nshared.js\n")
	writeIntegrationFile(t, root, ".prettierignore", "ignored.js\n!shared.js\n")
	writeIntegrationFile(t, root, ".prettierrc.json", `{"singleQuote":true}`)
	writeIntegrationFile(t, root, "src/nested/.prettierrc.yaml", "overrides:\n  - files: '**/*.custom'\n    options:\n      parser: json\n")
	for _, name := range []string{
		"src/app.js", "dist/generated.js", "dist/keep.js", "ignored.js", "shared.js", "README.unknown",
		"src/nested/data.custom", "src/nested/deep/data.custom", "node_modules/example/index.js", ".hidden.js", "src/żółć.js",
	} {
		writeIntegrationFile(t, root, name, "")
	}
}

func linkPrettierPackage(t *testing.T, root, version, packageRoot string) {
	t.Helper()
	writeIntegrationFile(t, root, "package.json", `{"devDependencies":{"prettier":"`+version+`"}}`)
	target := filepath.Join(root, "node_modules/prettier")
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(target, os.DirFS(packageRoot)); err != nil {
		t.Fatal(err)
	}
}

func evaluateIntegrationProvider(t *testing.T, root, packageRoot string, paths []scope.Path) []scope.Result {
	t.Helper()
	store := safeexec.FileApprovalStore{Path: filepath.Join(t.TempDir(), "approvals.json")}
	manager, err := safeexec.NewManager([]safeexec.Tool{
		{ID: nodeTool, Command: integrationNode},
		{ID: prettierTool, SelectionRequired: true},
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetOverrides(map[safeexec.ToolID]string{prettierTool: filepath.Join(packageRoot, "index.mjs")}); err != nil {
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
	for _, selection := range selections {
		statuses := manager.StatusesForSelections(root, []safeexec.Selection{selection})
		if len(statuses) != 1 || statuses[0].State == safeexec.UnavailableState {
			t.Fatalf("setup status = %+v", statuses)
		}
		if _, err := manager.ApproveTarget(root, statuses[0].Target); err != nil {
			t.Fatal(err)
		}
	}
	results, err := current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], paths)
	if err != nil {
		t.Fatal(err)
	}
	return results
}

func runPrettierOracle(t *testing.T, root, contextRoot, packageRoot string, paths []scope.Path) []scope.State {
	t.Helper()
	script := filepath.Join(t.TempDir(), "oracle.mjs")
	writeIntegrationFile(t, filepath.Dir(script), filepath.Base(script), `
import process from "node:process";
import path from "node:path";
import { pathToFileURL } from "node:url";
const prettier = await import(pathToFileURL(process.argv[2]).href);
const root = process.argv[3];
const context = process.argv[4];
const paths = JSON.parse(process.argv[5]);
const results = [];
for (const name of paths) {
  const info = await prettier.getFileInfo(path.join(root, name), {
    resolveConfig: true,
    ignorePath: [path.join(context, ".gitignore"), path.join(context, ".prettierignore")],
    withNodeModules: false,
  });
  results.push(info.ignored || !info.inferredParser ? "OUT" : "IN");
}
process.stdout.write(JSON.stringify(results));
`)
	encoded, err := json.Marshal(paths)
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, filepath.FromSlash(contextRoot))
	command := exec.Command(integrationNode, script, filepath.Join(packageRoot, "index.mjs"), root, cwd, string(encoded)) //nolint:gosec // fixed integration-only oracle.
	command.Dir = cwd
	command.Env = append(os.Environ(), "NODE_OPTIONS=", "NODE_PATH=")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Prettier oracle failed: %v: %s", err, output)
	}
	var states []scope.State
	if err := json.Unmarshal(output, &states); err != nil {
		t.Fatalf("decode Prettier oracle: %v: %s", err, output)
	}
	return states
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

func requireIntegrationContainer(t *testing.T) {
	t.Helper()
	if os.Getenv("AWAREOF_INTEGRATION_CONTAINER") != "1" {
		t.Fatal("integration tests must run in the hardened integration container")
	}
}
