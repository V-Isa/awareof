//go:build integration

package npm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestProviderNativeParity(t *testing.T) {
	if os.Getenv("AWAREOF_INTEGRATION_CONTAINER") != "1" {
		t.Fatal("integration tests run only in the disposable container; use 'make integration'")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Fatal("integration tests require npm on PATH")
	}

	root := t.TempDir()
	writePackage(t, root, ".", `{
  "name":"awareof-npm-fixture",
  "version":"1.0.0",
  "files":["dist"],
  "main":"dist/index.js"
}`)
	writeIntegrationFile(t, root, "README.md", "included automatically")
	writeIntegrationFile(t, root, "dist/index.js", "included by files")
	writeIntegrationFile(t, root, "src/index.js", "not published")
	writeIntegrationFile(t, root, ".env", "not published")
	writeIntegrationFile(t, root, ".npmrc", "onload-script=./onload.js\n")
	writeIntegrationFile(t, root, "onload.js", `require("fs").writeFileSync("npmrc-ran", "unsafe")`)
	writeIntegrationFile(t, root, "preload.js", `require("fs").writeFileSync("preload-ran", "unsafe")`)
	t.Setenv("NODE_OPTIONS", "--require="+filepath.Join(root, "preload.js"))

	current := detectedInstance(t, root)
	runner := approvedNPMRunner(t, root)
	paths := []scope.Path{"package.json", "README.md", "dist/index.js", "src/index.js", ".env", "future.js"}
	results, err := New(runner).Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{Root: root},
		current,
		paths,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []scope.State{scope.In, scope.In, scope.In, scope.Out, scope.Out, scope.Out}
	for index, result := range results {
		if result.State != want[index] {
			t.Errorf("%s = %s (%s), want %s", result.Path, result.State, result.Explanation.Summary, want[index])
		}
	}
	for _, name := range []string{"npmrc-ran", "preload-ran"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("repository-controlled npm startup code ran (%s): %v", name, err)
		}
	}
}

func TestProviderNativeParityForNestedPackageWithoutRootManifest(t *testing.T) {
	if os.Getenv("AWAREOF_INTEGRATION_CONTAINER") != "1" {
		t.Fatal("integration tests run only in the disposable container; use 'make integration'")
	}

	root := t.TempDir()
	writePackage(t, root, "mobile", `{
  "name":"awareof-nested-npm-fixture",
  "version":"1.0.0",
  "files":["dist"]
}`)
	writeIntegrationFile(t, root, "mobile/dist/index.js", "included")
	writeIntegrationFile(t, root, "mobile/src/index.js", "excluded")

	instances, err := New(nil).Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].Descriptor().ID != "package/mobile" {
		t.Fatalf("Detect() = %+v, want nested mobile package", instances)
	}
	results, err := New(approvedNPMRunner(t, root)).Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{Root: root},
		instances[0],
		[]scope.Path{"README.md", "mobile/package.json", "mobile/dist/index.js", "mobile/src/index.js"},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []scope.State{scope.NotApp, scope.In, scope.In, scope.Out}
	for index, result := range results {
		if result.State != want[index] {
			t.Errorf("%s = %s (%s), want %s", result.Path, result.State, result.Explanation.Summary, want[index])
		}
	}
}

func TestProviderNeverRunsPublicationScripts(t *testing.T) {
	if os.Getenv("AWAREOF_INTEGRATION_CONTAINER") != "1" {
		t.Fatal("integration tests run only in the disposable container; use 'make integration'")
	}

	root := t.TempDir()
	writePackage(t, root, ".", `{
  "name":"awareof-unsafe-fixture",
  "version":"1.0.0",
  "scripts":{"prepare":"touch script-ran"}
}`)
	current := detectedInstance(t, root)
	results, err := New(approvedNPMRunner(t, root)).Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{Root: root},
		current,
		[]scope.Path{"package.json", "script-ran"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.State != scope.Unknown || result.Explanation.Code != "npm/publication-script" {
			t.Errorf("result = %+v, want publication-script UNKNOWN", result)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "script-ran")); !os.IsNotExist(err) {
		t.Fatalf("publication script was executed: %v", err)
	}
}

func approvedNPMRunner(t *testing.T, root string) safeexec.Runner {
	t.Helper()
	manager, err := safeexec.NewManager(
		[]safeexec.Tool{{ID: "node", Command: "node"}, {ID: "npm", Command: "npm"}},
		safeexec.FileApprovalStore{Path: filepath.Join(t.TempDir(), "tool-approvals.json")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(root, "npm"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(root, "node"); err != nil {
		t.Fatal(err)
	}
	return safeexec.Runner{Resolver: manager}
}

func writeIntegrationFile(t *testing.T, root, name, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
