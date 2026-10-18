package jest

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
		major   string
	}{
		{version: "29.0.0", major: "29"},
		{version: "29.7.0", major: "29"},
		{version: "30.5.2", major: "30"},
		{version: "28.9.0"},
		{version: "31.0.0"},
		{version: "30.0.0-alpha.1"},
		{version: "30.0.0+build"},
		{version: "30.0"},
		{version: "+29.0.0"},
		{version: "029.0.0"},
		{version: "29.-1.0"},
		{version: "29.00.0"},
		{version: "x.0.0"},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			major, err := supportedVersion(test.version)
			if test.major == "" && err == nil {
				t.Fatalf("supportedVersion(%q) = %q, nil", test.version, major)
			}
			if test.major != "" && (err != nil || major != test.major) {
				t.Fatalf("supportedVersion(%q) = %q, %v", test.version, major, err)
			}
		})
	}
}

func TestLoadProjectInstallationUsesNearestPackageWithinRepository(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFile(t, repository, "node_modules/jest/package.json", `{"name":"jest","version":"29.7.0"}`)
	writeFile(t, repository, "node_modules/jest/bin/jest.js", "")
	installation, err := loadProjectInstallation(repository, jestContext{root: "packages/app"})
	if err != nil {
		t.Fatal(err)
	}
	if installation.version != "29.7.0" || installation.major != "29" ||
		!strings.HasSuffix(filepath.ToSlash(installation.entry), "/node_modules/jest/bin/jest.js") {
		t.Fatalf("installation = %#v", installation)
	}
}

func TestLoadInstallationRejectsUnsafeOrInvalidPackages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliable on Windows test hosts")
	}
	t.Run("wrong package", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "package.json", `{"name":"not-jest","version":"29.7.0"}`)
		writeFile(t, root, "bin/jest.js", "")
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
		root := filepath.Join(outer, "jest")
		writeFile(t, root, "package.json", `{"name":"jest","version":"29.7.0"}`)
		writeFile(t, outer, "outside.js", "")
		if err := os.MkdirAll(filepath.Join(root, "bin"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outer, "outside.js"), filepath.Join(root, "bin/jest.js")); err != nil {
			t.Fatal(err)
		}
		if _, err := loadInstallation(root); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("loadInstallation() error = %v", err)
		}
	})
	t.Run("package outside repository", func(t *testing.T) {
		outer := t.TempDir()
		repository := filepath.Join(outer, "repository")
		packageRoot := filepath.Join(outer, "jest")
		writeFile(t, packageRoot, "package.json", `{"name":"jest","version":"29.7.0"}`)
		writeFile(t, packageRoot, "bin/jest.js", "")
		if err := os.MkdirAll(filepath.Join(repository, "node_modules"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(packageRoot, filepath.Join(repository, "node_modules/jest")); err != nil {
			t.Fatal(err)
		}
		if _, err := loadProjectInstallation(repository, jestContext{root: "."}); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("loadProjectInstallation() error = %v", err)
		}
	})
}
