//go:build integration

package pathsource

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestGitNativeParity(t *testing.T) {
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
	runGit(t, root, "config", "user.name", "awareof test")
	runGit(t, root, "config", "user.email", "awareof@example.invalid")
	for name := range map[string]struct{}{
		"deleted.go":  {},
		"modified.go": {},
		"old.go":      {},
		"worktree.go": {},
	} {
		writeFile(t, root, name, "initial")
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "--quiet", "-m", "base")

	writeFile(t, root, "modified.go", "staged change")
	runGit(t, root, "add", "modified.go")
	runGit(t, root, "rm", "--quiet", "deleted.go")
	runGit(t, root, "mv", "old.go", "new.go")
	writeFile(t, root, "added.go", "added")
	writeFile(t, root, "literal*.txt", "literal glob characters")
	runGit(t, root, "add", "added.go", "literal*.txt")
	writeFile(t, root, "worktree.go", "unstaged change")
	writeFile(t, root, "untracked.go", "not returned by git diff")

	source := NewGit(approvedGitRunner(t, root))
	staged, err := source.Staged(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	wantStaged := []scope.Path{"added.go", "deleted.go", "literal*.txt", "modified.go", "new.go", "old.go"}
	if !reflect.DeepEqual(staged, wantStaged) {
		t.Fatalf("Staged() = %v, want %v", staged, wantStaged)
	}

	changed, err := source.ChangedFrom(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	wantChanged := []scope.Path{"added.go", "deleted.go", "literal*.txt", "modified.go", "new.go", "old.go", "worktree.go"}
	if !reflect.DeepEqual(changed, wantChanged) {
		t.Fatalf("ChangedFrom() = %v, want %v", changed, wantChanged)
	}
}

func TestGitStagedWithUnbornHead(t *testing.T) {
	if os.Getenv("AWAREOF_INTEGRATION_CONTAINER") != "1" {
		t.Fatal("integration tests run only in the disposable container; use 'make integration'")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	runGit(t, root, "init", "--quiet")
	writeFile(t, root, "first.go", "first")
	runGit(t, root, "add", "first.go")

	paths, err := NewGit(approvedGitRunner(t, root)).Staged(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []scope.Path{"first.go"}) {
		t.Fatalf("Staged() = %v, want first.go", paths)
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

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
