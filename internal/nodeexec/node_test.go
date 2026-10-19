package nodeexec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateForPlatform(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		goos   string
		file   string
		header []byte
		valid  bool
	}{
		{name: "Linux", goos: "linux", file: "node", header: []byte{0x7f, 'E', 'L', 'F'}, valid: true},
		{name: "macOS Mach-O", goos: "darwin", file: "node", header: []byte{0xcf, 0xfa, 0xed, 0xfe}, valid: true},
		{name: "macOS universal", goos: "darwin", file: "node", header: []byte{0xca, 0xfe, 0xba, 0xbe}, valid: true},
		{name: "Windows", goos: "windows", file: "node.exe", header: []byte{'M', 'Z', 0, 0}, valid: true},
		{name: "version manager shim", goos: "darwin", file: "volta-shim", header: []byte{0xcf, 0xfa, 0xed, 0xfe}},
		{name: "script named Node", goos: "linux", file: "node", header: []byte("#!/u")},
		{name: "wrong platform", goos: "linux", file: "node", header: []byte{'M', 'Z', 0, 0}},
		{name: "unsupported platform", goos: "plan9", file: "node", header: []byte{0x7f, 'E', 'L', 'F'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, test.file)
			if err := os.WriteFile(path, test.header, 0o700); err != nil { //nolint:gosec // executable-header fixture
				t.Fatal(err)
			}
			err := ValidateForPlatform(path, test.goos)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateForPlatform() error = %v, valid = %t", err, test.valid)
			}
		})
	}
}

func TestValidateForPlatformReadFailures(t *testing.T) {
	t.Parallel()
	if err := ValidateForPlatform(filepath.Join(t.TempDir(), "node"), "linux"); err == nil {
		t.Fatal("ValidateForPlatform() succeeded for a missing file")
	}
	short := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(short, []byte{0x7f}, 0o700); err != nil { //nolint:gosec // executable-header fixture
		t.Fatal(err)
	}
	if err := ValidateForPlatform(short, "linux"); err == nil {
		t.Fatal("ValidateForPlatform() succeeded for a short file")
	}
}

func TestValidateForPlatformDoesNotExecuteCandidate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	path := filepath.Join(root, "node")
	content := []byte("#!/bin/sh\ntouch \"" + marker + "\"\n")
	if err := os.WriteFile(path, content, 0o700); err != nil { //nolint:gosec // executable fixture that must not run
		t.Fatal(err)
	}
	if err := ValidateForPlatform(path, "linux"); err == nil {
		t.Fatal("ValidateForPlatform() accepted a shell script")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("candidate was executed; marker error = %v", err)
	}
}
