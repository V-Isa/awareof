package jest

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/scope"
)

func TestSelectConfigIsVersionSensitiveAndUnambiguous(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFile(t, repository, "jest.config.mts", "export default {};\n")
	current := jestContext{root: ".", configs: []configCandidate{{name: "jest.config.mts", regular: true}}}

	config29, problem29 := selectConfig(repository, current, "29")
	if problem29 != nil || config29.argument != "" || config29.path != "" ||
		config29.evidence != "version-specific Jest defaults at context ." {
		t.Fatalf("Jest 29 config/problem = %#v/%#v", config29, problem29)
	}
	config30, problem30 := selectConfig(repository, current, "30")
	if problem30 != nil || !strings.HasSuffix(filepath.ToSlash(config30.argument), "/jest.config.mts") ||
		config30.path != "jest.config.mts" || config30.evidence != "jest.config.mts" {
		t.Fatalf("Jest 30 config/problem = %#v/%#v", config30, problem30)
	}

	writeFile(t, repository, "jest.config.js", "module.exports = {};\n")
	current.configs = append(current.configs, configCandidate{name: "jest.config.js", regular: true})
	if _, problem := selectConfig(repository, current, "30"); problem == nil || problem.Code != "jest/config-ambiguous" {
		t.Fatalf("ambiguous problem = %#v", problem)
	}
}

func TestSelectConfigAmbiguityIncludesPackageConfiguration(t *testing.T) {
	t.Parallel()
	t.Run("supported file and package config", func(t *testing.T) {
		repository := t.TempDir()
		writeFile(t, repository, "package.json", `{"jest":{}}`)
		writeFile(t, repository, "jest.config.js", "module.exports = {};\n")
		current := jestContext{
			root:          ".",
			packageConfig: true,
			configs:       []configCandidate{{name: "jest.config.js", regular: true}},
		}

		_, problem := selectConfig(repository, current, "29")
		if problem == nil || problem.Code != "jest/config-ambiguous" ||
			problem.Evidence != "jest.config.js, package.json#jest" {
			t.Fatalf("problem = %#v", problem)
		}
	})
	t.Run("version filtering precedes ambiguity", func(t *testing.T) {
		repository := t.TempDir()
		writeFile(t, repository, "package.json", `{"jest":{}}`)
		writeFile(t, repository, "jest.config.mts", "export default {};\n")
		current := jestContext{
			root:          ".",
			packageConfig: true,
			configs:       []configCandidate{{name: "jest.config.mts", regular: true}},
		}

		config29, problem29 := selectConfig(repository, current, "29")
		if problem29 != nil || config29.path != "package.json" ||
			config29.evidence != "package.json#jest" {
			t.Fatalf("Jest 29 config/problem = %#v/%#v", config29, problem29)
		}
		_, problem30 := selectConfig(repository, current, "30")
		if problem30 == nil || problem30.Code != "jest/config-ambiguous" ||
			problem30.Evidence != "jest.config.mts, package.json#jest" {
			t.Fatalf("Jest 30 problem = %#v", problem30)
		}
	})
}

func TestSelectPackageConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		major    string
		root     scope.Path
		manifest string
		extra    map[string]string
		dirs     []string
		wantArg  string
		wantCode string
	}{
		{name: "Jest 29 object", major: "29", manifest: `{"jest":{"testMatch":["**/*.test.js"]}}`, wantArg: "package.json"},
		{name: "Jest 29 JSON reference", major: "29", manifest: `{"jest":"./config/jest.json"}`, extra: map[string]string{"config/jest.json": `{}`}, wantCode: "jest/config-uninspectable"},
		{name: "Jest 30 JSON reference", major: "30", manifest: `{"jest":"./config/jest.json"}`, extra: map[string]string{"config/jest.json": `{}`}, wantArg: "config/jest.json"},
		{name: "executable reference", major: "30", manifest: `{"jest":"./jest.config.js"}`, extra: map[string]string{"jest.config.js": "module.exports = {};"}, wantCode: "jest/config-uninspectable"},
		{name: "outside reference", major: "30", manifest: `{"jest":"../jest.json"}`, wantCode: "jest/config-uninspectable"},
		{name: "empty reference", major: "30", manifest: `{"jest":""}`, wantCode: "jest/config-uninspectable"},
		{name: "missing JSON reference", major: "30", manifest: `{"jest":"./missing.json"}`, wantCode: "jest/config-uninspectable"},
		{name: "directory JSON reference", major: "30", manifest: `{"jest":"./config.json"}`, dirs: []string{"config.json"}, wantCode: "jest/config-uninspectable"},
		{name: "in-repository parent reference", major: "30", root: "packages/app", manifest: `{"jest":"../shared/jest.json"}`, extra: map[string]string{"packages/shared/jest.json": `{}`}, wantArg: "packages/shared/jest.json"},
		{name: "configuration removed after discovery", major: "30", manifest: `{}`, wantCode: "jest/config-uninspectable"},
		{name: "wrong type", major: "30", manifest: `{"jest":true}`, wantCode: "jest/config-uninspectable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := t.TempDir()
			root := test.root
			if root == "" {
				root = "."
			}
			manifest := "package.json"
			if root != "." {
				manifest = filepath.ToSlash(filepath.Join(string(root), manifest))
			}
			writeFile(t, repository, manifest, test.manifest)
			for name, content := range test.extra {
				writeFile(t, repository, name, content)
			}
			for _, name := range test.dirs {
				if err := os.MkdirAll(filepath.Join(repository, filepath.FromSlash(name)), 0o750); err != nil {
					t.Fatal(err)
				}
			}
			config, problem := selectConfig(repository, jestContext{root: root, packageConfig: true}, test.major)
			if test.wantCode == "" {
				wantEvidence := manifest + "#jest"
				if problem != nil || !strings.HasSuffix(filepath.ToSlash(config.argument), test.wantArg) ||
					config.path != scope.Path(manifest) || config.evidence != wantEvidence {
					t.Fatalf("config/problem = %#v/%#v", config, problem)
				}
			} else if problem == nil || problem.Code != test.wantCode {
				t.Fatalf("problem = %#v, want %s", problem, test.wantCode)
			}
		})
	}
	t.Run("absolute reference", func(t *testing.T) {
		repository := t.TempDir()
		absolute := filepath.Join(repository, "config", "jest.json")
		writeFile(t, repository, "package.json", `{"jest":`+strconv.Quote(absolute)+`}`)
		config, problem := selectConfig(repository, jestContext{root: ".", packageConfig: true}, "30")
		if problem == nil || problem.Code != "jest/config-uninspectable" || config.argument != "" {
			t.Fatalf("config/problem = %#v/%#v", config, problem)
		}
	})
}

func TestSelectConfigRejectsNonregularAndOutsideFiles(t *testing.T) {
	t.Run("nonregular", func(t *testing.T) {
		repository := t.TempDir()
		if err := os.Mkdir(filepath.Join(repository, "jest.config.js"), 0o750); err != nil {
			t.Fatal(err)
		}
		_, problem := selectConfig(repository, jestContext{root: ".", configs: []configCandidate{{name: "jest.config.js"}}}, "29")
		if problem == nil || problem.Code != "jest/config-uninspectable" {
			t.Fatalf("problem = %#v", problem)
		}
	})
	t.Run("leaf symlink is nonregular", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation is not reliable on Windows test hosts")
		}
		outer := t.TempDir()
		repository := filepath.Join(outer, "repo")
		writeFile(t, outer, "outside.js", "module.exports = {};\n")
		if err := os.MkdirAll(repository, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outer, "outside.js"), filepath.Join(repository, "jest.config.js")); err != nil {
			t.Fatal(err)
		}
		_, problem := selectConfig(repository, jestContext{root: ".", configs: []configCandidate{{name: scope.Path("jest.config.js"), regular: true}}}, "29")
		if problem == nil || problem.Code != "jest/config-uninspectable" {
			t.Fatalf("problem = %#v", problem)
		}
	})
	t.Run("symlinked parent escapes repository", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation is not reliable on Windows test hosts")
		}
		outer := t.TempDir()
		repository := filepath.Join(outer, "repo")
		writeFile(t, outer, "shared/jest.config.js", "module.exports = {};\n")
		if err := os.MkdirAll(repository, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outer, "shared"), filepath.Join(repository, "config")); err != nil {
			t.Fatal(err)
		}
		_, problem := selectConfig(repository, jestContext{
			root: ".",
			configs: []configCandidate{{
				name: scope.Path("config/jest.config.js"), regular: true,
			}},
		}, "29")
		if problem == nil || problem.Code != "jest/config-uninspectable" ||
			!strings.Contains(problem.Evidence, "outside the repository") {
			t.Fatalf("problem = %#v", problem)
		}
	})
}
