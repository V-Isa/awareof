//go:build integration

package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidationGitNativeParity(t *testing.T) {
	if os.Getenv("AWAREOF_INTEGRATION_CONTAINER") != "1" {
		t.Fatal("integration tests run only in the disposable container; use 'make integration'")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("integration tests require git on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	runIntegrationGit(t, root, "init", "--quiet")
	writeIntegrationFile(t, root, ".gitignore", ".env\n")
	writeIntegrationFile(t, root, ".env", "secret")
	writeIntegrationFile(t, root, ".awareof.yaml", "version: 1\nrules:\n  .env:\n    git: out\n")

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--root", root, "--setup"}, strings.NewReader("yes\n"), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "status: NOT APPROVED") || !strings.Contains(stdout.String(), "Setup is noninteractive; no approvals changed.") || stderr.Len() != 0 {
		t.Fatalf("noninteractive setup = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	approvalPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "awareof", "tool-approvals.json")
	if _, err := os.Lstat(approvalPath); !os.IsNotExist(err) {
		t.Fatalf("noninteractive setup wrote approval file: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"--root", root, ".env"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "git              UNKNOWN") || strings.Count(stdout.String(), "tool \"git\" is not approved") != 1 || strings.Count(stdout.String(), "Run awareof --setup") != 1 || stderr.Len() != 0 {
		t.Fatalf("unapproved inspection = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}

	approveIntegrationGit(t, root)

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"--root", root, "--validate"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stdout.String() != "Validation passed: 1 assertions satisfied.\n" || stderr.Len() != 0 {
		t.Fatalf("untracked ignored validation = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}

	runIntegrationGit(t, root, "add", "--force", ".env")
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"--root", root, "--validate"}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), "git expected OUT, actual IN") || !strings.Contains(stdout.String(), "tracked; also matches") || stderr.Len() != 0 {
		t.Fatalf("tracked ignored validation = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
}

func approveIntegrationGit(t *testing.T, root string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--root", root, "--approve-tool", "git"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("approve git = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
}

func runIntegrationGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = cleanIntegrationGitEnvironment(os.Environ())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func cleanIntegrationGitEnvironment(environment []string) []string {
	clean := make([]string, 0, len(environment)+3)
	for _, item := range environment {
		if !strings.HasPrefix(item, "GIT_") {
			clean = append(clean, item)
		}
	}
	return append(clean, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
}

func writeIntegrationFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
