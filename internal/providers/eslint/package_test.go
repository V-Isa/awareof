package eslint

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSupportedVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		version string
		valid   bool
	}{
		{version: "9.0.0", valid: true},
		{version: "9.39.5", valid: true},
		{version: "10.12.0", valid: true},
		{version: "8.57.1"},
		{version: "11.0.0"},
		{version: "10.0.0-alpha.1"},
		{version: "10.0.0+build"},
		{version: "10.0"},
		{version: "+9.0.0"},
		{version: "09.0.0"},
		{version: "9.-1.0"},
		{version: "9.00.0"},
		{version: "x.0.0"},
		{version: "10.x.0"},
		{version: "10.0.x"},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			err := supportedVersion(test.version)
			if (err == nil) != test.valid {
				t.Fatalf("supportedVersion(%q) error = %v, valid = %t", test.version, err, test.valid)
			}
		})
	}
}

func TestLoadProjectInstallationWalksOnlyWithinRepository(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFile(t, repository, "node_modules/eslint/package.json", `{"name":"eslint","version":"9.39.5"}`)
	writeFile(t, repository, "node_modules/eslint/lib/api.js", "module.exports = {};\n")
	installation, err := loadProjectInstallation(repository, eslintContext{root: "packages/app"})
	if err != nil {
		t.Fatal(err)
	}
	if installation.version != "9.39.5" || !strings.HasSuffix(filepath.ToSlash(installation.entry), "/node_modules/eslint/lib/api.js") {
		t.Fatalf("installation = %#v", installation)
	}
}

func TestLoadInstallationRejectsUnsafeOrInvalidPackages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliable on Windows test hosts")
	}
	t.Run("wrong package", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "package.json", `{"name":"not-eslint","version":"9.39.5"}`)
		if _, err := loadInstallation(root); err == nil {
			t.Fatal("loadInstallation() error = nil")
		}
	})
	t.Run("nonregular metadata", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "package.json"), 0o750); err != nil {
			t.Fatal(err)
		}
		if _, err := loadInstallation(root); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("loadInstallation() error = %v", err)
		}
	})
	t.Run("entry outside package", func(t *testing.T) {
		outer := t.TempDir()
		root := filepath.Join(outer, "eslint")
		writeFile(t, root, "package.json", `{"name":"eslint","version":"9.39.5"}`)
		writeFile(t, outer, "outside.js", "module.exports = {};\n")
		if err := os.MkdirAll(filepath.Join(root, "lib"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outer, "outside.js"), filepath.Join(root, "lib", "api.js")); err != nil {
			t.Fatal(err)
		}
		if _, err := loadInstallation(root); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("loadInstallation() error = %v", err)
		}
	})
	t.Run("package outside repository", func(t *testing.T) {
		outer := t.TempDir()
		repository := filepath.Join(outer, "repository")
		packageRoot := filepath.Join(outer, "eslint")
		writeFile(t, packageRoot, "package.json", `{"name":"eslint","version":"9.39.5"}`)
		writeFile(t, packageRoot, "lib/api.js", "module.exports = {};\n")
		if err := os.MkdirAll(filepath.Join(repository, "node_modules"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(packageRoot, filepath.Join(repository, "node_modules", "eslint")); err != nil {
			t.Fatal(err)
		}
		if _, err := loadProjectInstallation(repository, eslintContext{root: "."}); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("loadProjectInstallation() error = %v", err)
		}
	})
}
