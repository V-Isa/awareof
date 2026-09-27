package repository

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveRoot(t *testing.T) {
	t.Parallel()

	t.Run("explicit relative root wins and is canonical", func(t *testing.T) {
		t.Parallel()
		parent := t.TempDir()
		realRoot := filepath.Join(parent, "real")
		mustMkdir(t, filepath.Join(realRoot, "nested"))
		link := filepath.Join(parent, "link")
		if err := os.Symlink(realRoot, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink unavailable: %v", err)
			}
			t.Fatal(err)
		}

		got, err := ResolveRoot(filepath.Join(realRoot, "nested"), filepath.Join("..", "..", "link"))
		if err != nil {
			t.Fatal(err)
		}
		if got != canonical(t, realRoot) {
			t.Fatalf("ResolveRoot() = %q, want %q", got, canonical(t, realRoot))
		}
	})

	for _, marker := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS", ".dockerignore", "compose.yaml", "docker-bake.hcl", "tsconfig.json"} {
		t.Run(marker+" is a project marker", func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			mustMkdir(t, filepath.Dir(filepath.Join(root, marker)))
			mustWrite(t, filepath.Join(root, marker))
			nested := filepath.Join(root, "nested")
			mustMkdir(t, nested)

			got, err := ResolveRoot(nested, "")
			if err != nil {
				t.Fatal(err)
			}
			if got != canonical(t, root) {
				t.Fatalf("ResolveRoot() = %q, want marker root %q", got, canonical(t, root))
			}
		})
	}

	t.Run("contract marker has highest priority", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustWrite(t, filepath.Join(root, ".awareof.yaml"))
		gitRoot := filepath.Join(root, "repo")
		mustMkdir(t, filepath.Join(gitRoot, ".git"))
		manifestRoot := filepath.Join(gitRoot, "module")
		mustMkdir(t, filepath.Join(manifestRoot, "src"))
		mustWrite(t, filepath.Join(manifestRoot, "go.mod"))

		got, err := ResolveRoot(filepath.Join(manifestRoot, "src"), "")
		if err != nil {
			t.Fatal(err)
		}
		if got != canonical(t, root) {
			t.Fatalf("ResolveRoot() = %q, want contract root %q", got, canonical(t, root))
		}
	})

	t.Run("vcs marker wins over manifest", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustMkdir(t, filepath.Join(root, ".git"))
		module := filepath.Join(root, "module")
		mustMkdir(t, filepath.Join(module, "src"))
		mustWrite(t, filepath.Join(module, "go.mod"))

		got, err := ResolveRoot(filepath.Join(module, "src"), "")
		if err != nil {
			t.Fatal(err)
		}
		if got != canonical(t, root) {
			t.Fatalf("ResolveRoot() = %q, want VCS root %q", got, canonical(t, root))
		}
	})

	t.Run("nearest manifest is selected without vcs", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustWrite(t, filepath.Join(root, "package.json"))
		module := filepath.Join(root, "module")
		mustMkdir(t, filepath.Join(module, "src"))
		mustWrite(t, filepath.Join(module, "go.mod"))

		got, err := ResolveRoot(filepath.Join(module, "src"), "")
		if err != nil {
			t.Fatal(err)
		}
		if got != canonical(t, module) {
			t.Fatalf("ResolveRoot() = %q, want nearest manifest root %q", got, canonical(t, module))
		}
	})

	t.Run("start is root when no marker exists", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		got, err := ResolveRoot(root, "")
		if err != nil {
			t.Fatal(err)
		}
		if got != canonical(t, root) {
			t.Fatalf("ResolveRoot() = %q, want %q", got, canonical(t, root))
		}
	})

	t.Run("invalid marker kinds do not select a root", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustWrite(t, filepath.Join(root, "go.mod"))
		nested := filepath.Join(root, "nested")
		mustMkdir(t, filepath.Join(nested, ".awareof.yaml"))
		mustMkdir(t, filepath.Join(nested, "package.json"))
		mustMkdir(t, filepath.Join(nested, "src"))

		got, err := ResolveRoot(filepath.Join(nested, "src"), "")
		if err != nil {
			t.Fatal(err)
		}
		if got != canonical(t, root) {
			t.Fatalf("ResolveRoot() = %q, want valid marker root %q", got, canonical(t, root))
		}
	})

	t.Run("explicit file is rejected", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		file := filepath.Join(root, "file")
		mustWrite(t, file)
		if _, err := ResolveRoot(root, file); err == nil {
			t.Fatal("ResolveRoot() error = nil, want explicit file error")
		}
	})

	t.Run("missing start is rejected", func(t *testing.T) {
		t.Parallel()
		if _, err := ResolveRoot(filepath.Join(t.TempDir(), "missing"), ""); err == nil {
			t.Fatal("ResolveRoot() error = nil, want missing start error")
		}
	})
}

func TestResolveRootUsesWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	got, err := ResolveRoot("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != canonical(t, root) {
		t.Fatalf("ResolveRoot() = %q, want %q", got, canonical(t, root))
	}
}

func TestHasAnyMarker(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if found, err := hasAnyFileMarker(root, []string{"missing", "also-missing"}); err != nil || found {
		t.Fatalf("hasAnyMarker() = %v, %v; want false, nil", found, err)
	}
	mustWrite(t, filepath.Join(root, "marker"))
	if found, err := hasAnyFileMarker(root, []string{"missing", "marker"}); err != nil || !found {
		t.Fatalf("hasAnyMarker() = %v, %v; want true, nil", found, err)
	}
	mustMkdir(t, filepath.Join(root, "directory-marker"))
	if found, err := hasAnyFileMarker(root, []string{"directory-marker"}); err != nil || found {
		t.Fatalf("hasAnyFileMarker() = %v, %v; want false, nil", found, err)
	}
	if found, err := hasAnyVCSMarker(root, []string{"directory-marker"}); err != nil || !found {
		t.Fatalf("hasAnyVCSMarker() = %v, %v; want true, nil", found, err)
	}
	file := filepath.Join(root, "file")
	mustWrite(t, file)
	if _, err := hasAnyFileMarker(file, []string{"marker"}); err == nil {
		t.Fatal("hasAnyMarker() error = nil, want non-directory error")
	}
}

func mustMkdir(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(name, 0o750); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(name, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func canonical(t *testing.T, name string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
