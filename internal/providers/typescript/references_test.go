package typescript

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/scope"
)

func TestReadReferences(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{
  // TypeScript accepts comments and trailing commas.
  "references": [
    {"path": "packages/app"},
    {"path": "configs/test.build.json"},
    {"path": "configs/plain.json"},
    {"path": "packages/app"},
  ],
}`, 0o600)
	writeTestFile(t, root, "packages/app/tsconfig.json", `{}`, 0o600)
	writeTestFile(t, root, "configs/test.build.json", `{}`, 0o600)
	writeTestFile(t, root, "configs/plain.json", `{}`, 0o600)

	got, err := readReferences(root, "tsconfig.json")
	if err != nil {
		t.Fatal(err)
	}
	want := []scope.Path{"configs/plain.json", "configs/test.build.json", "packages/app/tsconfig.json"}
	if len(got) != len(want) {
		t.Fatalf("references = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("references = %v, want %v", got, want)
		}
	}
}

func TestReadReferencesFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  string
		prepare func(*testing.T, string)
		want    string
		wantErr error
	}{
		{name: "malformed", config: `{"references":[`, want: "parse"},
		{name: "multiple values", config: `{ } { }`, want: "multiple JSON values"},
		{name: "empty path", config: `{"references":[{"path":""}]}`, want: "invalid empty"},
		{name: "parent escape", config: `{"references":[{"path":"../outside.json"}]}`, want: "leaves the repository"},
		{name: "absolute path", config: `{"references":[{"path":"/outside.json"}]}`, want: "leaves the repository"},
		{name: "missing", config: `{"references":[{"path":"missing"}]}`, wantErr: fs.ErrNotExist},
		{name: "no implicit json extension", config: `{"references":[{"path":"plain"}]}`, prepare: func(t *testing.T, root string) {
			writeTestFile(t, root, "plain.json", `{}`, 0o600)
		}, wantErr: fs.ErrNotExist},
		{name: "non-regular", config: `{"references":[{"path":"pipe.json"}]}`, prepare: func(t *testing.T, root string) {
			if runtime.GOOS == "windows" {
				t.Skip("named pipes differ on Windows")
			}
			if err := os.Mkdir(filepath.Join(root, "pipe.json"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, want: "tsconfig.json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "tsconfig.json", test.config, 0o600)
			if test.prepare != nil {
				test.prepare(t, root)
			}
			_, err := readReferences(root, "tsconfig.json")
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("readReferences() error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
			if test.wantErr == nil && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("readReferences() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestReadReferencesRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "tsconfig.json", `{"references":[{"path":"linked.json"}]}`, 0o600)
	if err := os.Symlink(outside, filepath.Join(root, "linked.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := readReferences(root, "tsconfig.json"); err == nil || !strings.Contains(err.Error(), "outside the repository") {
		t.Fatalf("readReferences() error = %v, want symlink escape", err)
	}
}

func TestReadReferencesSizeLimit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	name := filepath.Join(root, "tsconfig.json")
	if err := os.WriteFile(name, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(name, maxConfigSize+1); err != nil {
		t.Fatal(err)
	}
	if _, err := readReferences(root, "tsconfig.json"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readReferences() error = %v, want size limit", err)
	}
}
