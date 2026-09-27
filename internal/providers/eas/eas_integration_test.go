//go:build integration

package eas

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestProviderGitOracleParity(t *testing.T) {
	if os.Getenv("AWAREOF_INTEGRATION_CONTAINER") != "1" {
		t.Fatal("integration tests run only in the disposable container; use 'make integration'")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("integration tests require git on PATH")
	}
	t.Setenv("EAS_NO_VCS", "")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	root := t.TempDir()
	runGit(t, root, "init", "--quiet")
	runGit(t, root, "config", "user.name", "awareof test")
	runGit(t, root, "config", "user.email", "awareof@example.invalid")
	writeFixture(t, root, ".gitignore", ".env*\n!.env.example\ntracked-ignored.txt\ncase-secret.txt\n")
	writeFixture(t, root, "mobile/.gitignore", "/ios\n")
	writeFixture(t, root, "mobile/.easignore", "*.md\n__tests__\n")
	writeFixture(t, root, "mobile/eas.json", `{"cli":{"version":">= 16.18.0"}}`)
	writeFixture(t, root, "mobile/README.md", "tracked documentation")
	writeFixture(t, root, "mobile/__tests__/app.test.ts", "tracked test")
	writeFixture(t, root, "mobile/ios/Info.plist", "ignored native output")
	writeFixture(t, root, ".env.production", "ignored")
	writeFixture(t, root, ".env.example", "included by root negation")
	writeFixture(t, root, "tracked-ignored.txt", "working-tree overlay excludes this")
	writeFixture(t, root, "Case-Secret.TXT", "EAS ignore matching is case-insensitive")
	writeFixture(t, root, "untracked.txt", "included working-tree file")
	writeFixture(t, root, "local-only.txt", "local Git exclude must not affect EAS")
	writeFixture(t, root, "deleted.txt", "deleted after commit")
	writeFixture(t, root, "node_modules/tracked/index.js", "EAS default excludes this")
	writeFixture(t, root, ".git/info/exclude", "local-only.txt\n")
	runGit(t, root, "add", ".gitignore", "mobile/.gitignore", "mobile/.easignore", "mobile/eas.json", "mobile/README.md", "mobile/__tests__/app.test.ts", ".env.example", "deleted.txt")
	runGit(t, root, "add", "--force", "tracked-ignored.txt", "node_modules/tracked/index.js")
	runGit(t, root, "commit", "--quiet", "-m", "fixture")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}

	p := New(approvedGitRunner(t, root))
	instances, err := p.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].Descriptor().ID != "project/mobile" {
		t.Fatalf("Detect() = %+v, want mobile instance", instances)
	}
	paths := []scope.Path{
		"mobile/.easignore",
		"mobile/README.md",
		"mobile/__tests__/app.test.ts",
		"mobile/ios/Info.plist",
		".env.production",
		".env.example",
		"tracked-ignored.txt",
		"Case-Secret.TXT",
		"untracked.txt",
		"local-only.txt",
		"deleted.txt",
		"node_modules/tracked/index.js",
	}
	results, err := p.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], paths)
	if err != nil {
		t.Fatal(err)
	}
	want := []scope.State{
		scope.In,
		scope.In,
		scope.In,
		scope.Out,
		scope.Out,
		scope.In,
		scope.Out,
		scope.Out,
		scope.In,
		scope.In,
		scope.Out,
		scope.Out,
	}
	for index, result := range results {
		if result.State != want[index] {
			t.Errorf("%s = %s (%s), want %s", result.Path, result.State, result.Explanation.Summary, want[index])
		}
		if !strings.Contains(result.Explanation.Summary, "mobile/.easignore is inactive") {
			t.Errorf("%s explanation does not expose inactive nested .easignore: %q", result.Path, result.Explanation.Summary)
		}
	}
}

func TestProviderRootEASIgnoreGitOracle(t *testing.T) {
	if os.Getenv("AWAREOF_INTEGRATION_CONTAINER") != "1" {
		t.Fatal("integration tests run only in the disposable container; use 'make integration'")
	}
	t.Setenv("EAS_NO_VCS", "")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	root := t.TempDir()
	runGit(t, root, "init", "--quiet")
	runGit(t, root, "config", "user.name", "awareof test")
	runGit(t, root, "config", "user.email", "awareof@example.invalid")
	writeFixture(t, root, "eas.json", `{}`)
	writeFixture(t, root, ".easignore", "docs/**\n!docs/keep.md\n")
	writeFixture(t, root, "docs/drop.md", "drop")
	writeFixture(t, root, "docs/keep.md", "keep")
	runGit(t, root, "add", "--force", "eas.json", ".easignore", "docs/drop.md", "docs/keep.md")
	runGit(t, root, "commit", "--quiet", "-m", "fixture")

	p := New(approvedGitRunner(t, root))
	instances, err := p.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	results, err := p.Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{Root: root},
		instances[0],
		[]scope.Path{"docs/drop.md", "docs/keep.md"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Out || results[1].State != scope.In {
		t.Fatalf("results = %+v, want OUT then IN", results)
	}
}

func approvedGitRunner(t *testing.T, root string) safeexec.Runner {
	t.Helper()
	manager, err := safeexec.NewManager(
		[]safeexec.Tool{{ID: "git", Command: "git"}},
		safeexec.FileApprovalStore{Path: filepath.Join(t.TempDir(), "tool-approvals.json")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(root, "git"); err != nil {
		t.Fatal(err)
	}
	return safeexec.Runner{Resolver: manager}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = cleanGitEnvironment(os.Environ())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func cleanGitEnvironment(environment []string) []string {
	clean := make([]string, 0, len(environment)+3)
	for _, item := range environment {
		if !strings.HasPrefix(item, "GIT_") {
			clean = append(clean, item)
		}
	}
	return append(clean, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
}

func writeFixture(t *testing.T, root, name, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
