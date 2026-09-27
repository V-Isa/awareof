//go:build integration

package git

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

func TestProviderNativeParity(t *testing.T) {
	if os.Getenv("AWAREOF_INTEGRATION_CONTAINER") != "1" {
		t.Fatal("integration tests run only in the disposable container; use 'make integration'")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("integration tests require git on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	runGit(t, root, "init", "--quiet")
	writeFixture(t, root, ".gitignore", "ignored.txt\ntracked-ignored.txt\ngenerated/*\n!generated/keep.txt\n")
	writeFixture(t, root, ".git/info/exclude", "local-only.txt\n")
	writeFixture(t, root, "tracked.txt", "tracked")
	writeFixture(t, root, "tracked-ignored.txt", "tracked but ignored")
	writeFixture(t, root, "ignored.txt", "ignored")
	writeFixture(t, root, "generated/drop.txt", "drop")
	writeFixture(t, root, "generated/keep.txt", "keep")
	writeFixture(t, root, "local-only.txt", "locally ignored")
	runGit(t, root, "add", ".gitignore", "tracked.txt")
	runGit(t, root, "add", "--force", "tracked-ignored.txt")

	paths := []scope.Path{"tracked.txt", "tracked-ignored.txt", "ignored.txt", "generated/drop.txt", "generated/keep.txt", "local-only.txt", ":(glob)literal"}
	runner := approvedGitRunner(t, root)
	results, err := New(runner).Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, provider.InstanceDescriptor{Provider: "git", ID: "git"}, paths)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		state scope.State
		code  string
	}{
		{scope.In, "git/tracked"},
		{scope.In, "git/tracked-ignored"},
		{scope.Out, "git/ignored"},
		{scope.Out, "git/ignored"},
		{scope.In, "git/unignored"},
		{scope.Out, "git/ignored"},
		{scope.In, "git/not-ignored"},
	}
	for i := range want {
		if results[i].State != want[i].state || results[i].Explanation.Code != want[i].code {
			t.Errorf("%s = %s %s, want %s %s", paths[i], results[i].State, results[i].Explanation.Code, want[i].state, want[i].code)
		}
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
