package prettier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupportedVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		version string
		valid   bool
	}{
		{version: "3.0.0", valid: true},
		{version: "3.6.2", valid: true},
		{version: "3.8.5", valid: true},
		{version: "3.9.9", valid: true},
		{version: "2.8.8"},
		{version: "4.0.0-alpha.1"},
		{version: "3.9.9-beta.1"},
		{version: "3.9"},
		{version: "garbage"},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			t.Parallel()
			err := supportedVersion(test.version)
			if (err == nil) != test.valid {
				t.Fatalf("supportedVersion(%q) error = %v, valid=%v", test.version, err, test.valid)
			}
		})
	}
}

func TestPackageFingerprintCoversWholeTree(t *testing.T) {
	t.Parallel()
	left := filepath.Join(t.TempDir(), "prettier")
	right := filepath.Join(t.TempDir(), "prettier")
	for _, root := range []string{left, right} {
		writeFixtureFiles(t, root, map[string]string{
			"package.json": `{"name":"prettier","version":"3.9.9"}`,
			"index.mjs":    "export const value = 1;",
			"plugins/a.js": "export {};",
		})
	}
	leftInstallation, err := loadPackageInstallation(left, "")
	if err != nil {
		t.Fatal(err)
	}
	rightInstallation, err := loadPackageInstallation(right, "")
	if err != nil {
		t.Fatal(err)
	}
	leftDigest, err := packageFingerprint(leftInstallation)
	if err != nil {
		t.Fatal(err)
	}
	rightDigest, err := packageFingerprint(rightInstallation)
	if err != nil {
		t.Fatal(err)
	}
	if leftDigest != rightDigest {
		t.Fatalf("equal package trees differ: %s != %s", leftDigest, rightDigest)
	}
	if err := os.WriteFile(filepath.Join(right, "plugins/a.js"), []byte("export const changed = true;"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := packageFingerprint(rightInstallation)
	if err != nil {
		t.Fatal(err)
	}
	if changed == leftDigest {
		t.Fatal("fingerprint did not cover a changed package file")
	}
}

func TestPackageFingerprintRejectsSymlink(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "prettier")
	writeFixtureFiles(t, root, map[string]string{
		"package.json": `{"name":"prettier","version":"3.9.9"}`,
		"index.mjs":    "export {};",
	})
	if err := os.Symlink("index.mjs", filepath.Join(root, "linked.mjs")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	installation, err := loadPackageInstallation(root, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = packageFingerprint(installation)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("packageFingerprint() error = %v, want symlink rejection", err)
	}
}

func TestLoadPackageRejectsEscapingMetadataSymlink(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "prettier")
	external := filepath.Join(t.TempDir(), "package.json")
	writeFixtureFiles(t, root, map[string]string{"index.mjs": "export {};"})
	if err := os.WriteFile(external, []byte(`{"name":"prettier","version":"3.9.9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "package.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := loadPackageInstallation(root, ""); err == nil {
		t.Fatal("loadPackageInstallation() followed escaping package metadata symlink")
	}
}

func TestLoadProjectInstallationFindsHoistedPackage(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFixtureFiles(t, repository, map[string]string{
		"packages/web/package.json":          `{"devDependencies":{"prettier":"3.9.9"}}`,
		"node_modules/prettier/package.json": `{"name":"prettier","version":"3.9.9"}`,
		"node_modules/prettier/index.mjs":    "export {};",
	})
	installation, err := loadProjectInstallation(repository, prettierContext{root: "packages/web"})
	if err != nil {
		t.Fatal(err)
	}
	if installation.version != "3.9.9" {
		t.Fatalf("version = %q", installation.version)
	}
}

func TestLoadProjectInstallationRejectsExternalSymlink(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	external := filepath.Join(t.TempDir(), "prettier")
	writeFixtureFiles(t, repository, map[string]string{
		"package.json": `{"devDependencies":{"prettier":"3.9.9"}}`,
	})
	writeFixtureFiles(t, external, map[string]string{
		"package.json": `{"name":"prettier","version":"3.9.9"}`,
		"index.mjs":    "export {};",
	})
	if err := os.MkdirAll(filepath.Join(repository, "node_modules"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(repository, "node_modules", "prettier")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := loadProjectInstallation(repository, prettierContext{root: "."}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("loadProjectInstallation() error = %v, want outside-repository rejection", err)
	}
}

func TestLoadEvaluatorRequiresPublicAPIEntry(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "prettier")
	writeFixtureFiles(t, root, map[string]string{
		"package.json":     `{"name":"prettier","version":"3.9.9"}`,
		"index.mjs":        "export {};",
		"bin/prettier.cjs": "process.exit(0);",
	})
	_, err := loadEvaluatorInstallation(filepath.Join(root, "bin/prettier.cjs"))
	if err == nil || !strings.Contains(err.Error(), "public API") {
		t.Fatalf("loadEvaluatorInstallation() error = %v, want public API rejection", err)
	}
}
