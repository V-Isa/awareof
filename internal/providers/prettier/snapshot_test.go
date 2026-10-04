package prettier

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/V-Isa/awareof/internal/scope"
)

func TestConfigClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		kind    configKind
		valid   bool
	}{
		{name: ".prettierrc.json", content: `{"semi":false}`, kind: configPassive, valid: true},
		{name: ".prettierrc.json5", content: `{semi:false}`, kind: configPassive, valid: true},
		{name: ".prettierrc.yaml", content: "semi: false\n", kind: configPassive, valid: true},
		{name: ".prettierrc", content: `"@company/prettier-config"`, kind: configShareable, valid: true},
		{name: ".prettierrc.yml", content: "'@company/prettier-config'\n", kind: configShareable, valid: true},
		{name: ".prettierrc.toml", content: "semi = false\n", kind: configPassive, valid: true},
		{name: "prettier.config.js", content: "throw new Error('must not run')", kind: configExecutable, valid: true},
		{name: ".prettierrc.json", content: `[]`},
		{name: ".prettierrc.json5", content: `{} {}`},
	}
	for _, test := range tests {
		t.Run(test.name+"_"+string(test.kind), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFixtureFiles(t, root, map[string]string{test.name: test.content})
			_, got, present, err := readConfigCandidate(root, test.name, test.name)
			if test.valid {
				if err != nil || !present || got != test.kind {
					t.Fatalf("readConfigCandidate() = kind %q, present %v, error %v; want %q", got, present, err, test.kind)
				}
			} else if err == nil {
				t.Fatal("readConfigCandidate() succeeded for invalid config root")
			}
		})
	}
}

func TestPackageConfigClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		content string
		present bool
		kind    configKind
	}{
		{content: `{}`},
		{content: `{"prettier":{"semi":false}}`, present: true, kind: configPassive},
		{content: `{"prettier":"@company/prettier-config"}`, present: true, kind: configShareable},
	}
	for index, test := range tests {
		root := t.TempDir()
		writeFixtureFiles(t, root, map[string]string{"package.json": test.content})
		content, kind, present, err := readConfigCandidate(root, "package.json", "package.json")
		if err != nil {
			t.Fatalf("case %d: %v", index, err)
		}
		if present != test.present || kind != test.kind {
			t.Fatalf("case %d: present=%v kind=%q, want present=%v kind=%q", index, present, kind, test.present, test.kind)
		}
		if present && string(content) == test.content {
			t.Fatalf("case %d: package snapshot retained unrelated manifest fields", index)
		}
	}
}

func TestPackageYAMLConfigClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		content string
		present bool
		kind    configKind
	}{
		{content: "name: sample\n"},
		{content: "prettier:\n  semi: false\n", present: true, kind: configPassive},
		{content: "prettier: '@company/prettier-config'\n", present: true, kind: configShareable},
	}
	for index, test := range tests {
		root := t.TempDir()
		writeFixtureFiles(t, root, map[string]string{"package.yaml": test.content})
		_, kind, present, err := readConfigCandidate(root, "package.yaml", "package.yaml")
		if err != nil {
			t.Fatalf("case %d: %v", index, err)
		}
		if present != test.present || kind != test.kind {
			t.Fatalf("case %d: present=%v kind=%q, want present=%v kind=%q", index, present, kind, test.present, test.kind)
		}
	}
}

func TestConfigNamesFollowPrettierVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		version     string
		packageYAML bool
		typeScript  bool
	}{
		{version: "3.2.5"},
		{version: "3.3.0", packageYAML: true},
		{version: "3.4.2", packageYAML: true},
		{version: "3.5.0", packageYAML: true, typeScript: true},
		{version: "3.9.9", packageYAML: true, typeScript: true},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			t.Parallel()
			names, err := configNamesForVersion(test.version)
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(names, "package.yaml"); got != test.packageYAML {
				t.Errorf("package.yaml enabled = %v, want %v", got, test.packageYAML)
			}
			if got := slices.Contains(names, "prettier.config.ts"); got != test.typeScript {
				t.Errorf("prettier.config.ts enabled = %v, want %v", got, test.typeScript)
			}
		})
	}
	if _, err := configNamesForVersion("2.8.8"); err == nil {
		t.Fatal("configNamesForVersion() accepted an unsupported version")
	}
}

func TestSnapshotPreservesContextGeometry(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFixtureFiles(t, repository, map[string]string{
		"mobile/.gitignore":                  "dist\n",
		"mobile/.prettierignore":             "generated\n",
		"mobile/.prettierrc.json":            `{"singleQuote":true}`,
		"mobile/src/nested/.prettierrc.json": `{"overrides":[{"files":"*.custom","options":{"parser":"json"}}]}`,
		"mobile/src/nested/.prettierignore":  "must-not-be-active\n",
		".prettierignore":                    "root-must-not-be-active\n",
	})
	snapshot, err := createSnapshot(repository, prettierContext{root: "mobile"}, []scope.Path{
		"mobile/src/nested/example.custom", "mobile/src/app.ts",
	}, "/external/prettier/index.mjs", "3.9.9")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	}()
	for _, name := range []string{
		".gitignore", ".prettierignore", ".prettierrc.json", "src/nested/.prettierrc.json",
		"src/nested/example.custom", "src/app.ts",
	} {
		if _, err := os.Stat(filepath.Join(snapshot.root, filepath.FromSlash(name))); err != nil {
			t.Errorf("snapshot missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(snapshot.root, "src/nested/.prettierignore")); !os.IsNotExist(err) {
		t.Fatalf("nested .prettierignore was copied: %v", err)
	}
	if len(snapshot.request.IgnorePaths) != 2 {
		t.Fatalf("ignore paths = %v, want context-root pair", snapshot.request.IgnorePaths)
	}
}

func TestSnapshotAddsPassiveSearchBoundary(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFixtureFiles(t, repository, map[string]string{"src/app.js": "ignored contents"})
	snapshot, err := createSnapshot(repository, prettierContext{root: "."}, []scope.Path{"src/app.js"}, "/prettier/index.mjs", "3.9.9")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.close() }()
	sentinel := filepath.Join(snapshot.cleanupRoot, ".prettierrc.json")
	if snapshot.request.ConfigKinds[sentinel] != configSentinel {
		t.Fatalf("sentinel kind = %q", snapshot.request.ConfigKinds[sentinel])
	}
	info, err := os.Stat(filepath.Join(snapshot.root, "src/app.js"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("mirrored target size = %d, want contentless", info.Size())
	}
}

func TestSnapshotPreservesConfigAboveContextCWD(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFixtureFiles(t, repository, map[string]string{
		".prettierrc.json":       `{"overrides":[{"files":"mobile/**/*.custom","options":{"parser":"json"}}]}`,
		"mobile/src/data.custom": "",
	})
	snapshot, err := createSnapshot(repository, prettierContext{root: "mobile"}, []scope.Path{"mobile/src/data.custom"}, "/prettier/index.mjs", "3.9.9")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.close() }()
	parentConfig := filepath.Join(filepath.Dir(snapshot.root), ".prettierrc.json")
	if snapshot.request.ConfigKinds[parentConfig] != configPassive {
		t.Fatalf("parent config kind = %q", snapshot.request.ConfigKinds[parentConfig])
	}
	if snapshot.request.ConfigRoot != filepath.Dir(snapshot.root) {
		t.Fatalf("config root = %q, want %q", snapshot.request.ConfigRoot, filepath.Dir(snapshot.root))
	}
}

func TestSnapshotRejectsQueriedSymlink(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFixtureFiles(t, repository, map[string]string{"target.js": ""})
	if err := os.Symlink("target.js", filepath.Join(repository, "link.js")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := createSnapshot(repository, prettierContext{root: "."}, []scope.Path{"link.js"}, "/prettier/index.mjs", "3.9.9")
	if err == nil {
		t.Fatal("createSnapshot() accepted a queried symlink")
	}
}
