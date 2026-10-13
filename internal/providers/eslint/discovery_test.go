package eslint

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestDetectDiscoversDeclaredContextsAndNestedConfigs(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFile(t, repository, "package.json", `{"private":true,"devDependencies":{"eslint":"9.39.5"}}`)
	writeFile(t, repository, "eslint.config.mjs", "export default [];\n")
	writeFile(t, repository, "packages/app/package.json", `{"dependencies":{"eslint":"10.12.0"}}`)
	writeFile(t, repository, "packages/app/eslint.config.js", "module.exports = [];\n")
	writeFile(t, repository, "packages/app/src/eslint.config.cjs", "module.exports = [];\n")
	writeFile(t, repository, "node_modules/ignored/package.json", `{"devDependencies":{"eslint":"10.12.0"}}`)
	writeFile(t, repository, "vendor/generated/package.json", `{"devDependencies":{"eslint":"10.12.0"}}`)
	writeFile(t, repository, "vendor/generated/eslint.config.js", "module.exports = [];\n")

	instances, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	current := requireESLintInstance(t, instances)
	if len(current.contexts) != 3 {
		t.Fatalf("contexts = %#v, want root, packages/app, and vendor/generated", current.contexts)
	}
	wantRoots := []scope.Path{".", "packages/app", "vendor/generated"}
	gotRoots := make([]scope.Path, len(current.contexts))
	for index, currentContext := range current.contexts {
		gotRoots[index] = currentContext.root
	}
	if !reflect.DeepEqual(gotRoots, wantRoots) {
		t.Fatalf("context roots = %v, want %v", gotRoots, wantRoots)
	}
	if current.contexts[0].config != "eslint.config.mjs" {
		t.Fatalf("root config = %q", current.contexts[0].config)
	}
	if !reflect.DeepEqual(current.contexts[1].nestedConfigs, []scope.Path{"packages/app/src/eslint.config.cjs"}) {
		t.Fatalf("nested configs = %v", current.contexts[1].nestedConfigs)
	}
}

func TestDetectUsesDocumentedConfigPrecedence(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFile(t, repository, "package.json", `{"devDependencies":{"eslint":"9.39.5"}}`)
	for _, name := range configNames {
		writeFile(t, repository, name, "export default [];\n")
	}
	instances, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	current := requireESLintInstance(t, instances)
	if got := current.contexts[0].config; got != "eslint.config.js" {
		t.Fatalf("selected config = %q, want eslint.config.js", got)
	}
}

func TestDetectRootConfigFallbackAndNoContext(t *testing.T) {
	t.Parallel()
	t.Run("root config", func(t *testing.T) {
		repository := t.TempDir()
		writeFile(t, repository, "eslint.config.js", "module.exports = [];\n")
		instances, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: repository})
		if err != nil {
			t.Fatal(err)
		}
		current := requireESLintInstance(t, instances)
		if len(current.contexts) != 1 || current.contexts[0].root != "." {
			t.Fatalf("contexts = %#v", current.contexts)
		}
	})
	t.Run("nested config alone", func(t *testing.T) {
		repository := t.TempDir()
		writeFile(t, repository, "nested/eslint.config.js", "module.exports = [];\n")
		instances, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: repository})
		if err != nil {
			t.Fatal(err)
		}
		if len(instances) != 0 {
			t.Fatalf("instances = %#v, want none", instances)
		}
	})
	t.Run("root config plus nested package context", func(t *testing.T) {
		repository := t.TempDir()
		writeFile(t, repository, "eslint.config.js", "module.exports = [];\n")
		writeFile(t, repository, "packages/app/package.json", `{"devDependencies":{"eslint":"10.12.0"}}`)
		writeFile(t, repository, "packages/app/eslint.config.js", "module.exports = [];\n")
		instances, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: repository})
		if err != nil {
			t.Fatal(err)
		}
		current := requireESLintInstance(t, instances)
		if got := []scope.Path{current.contexts[0].root, current.contexts[1].root}; !reflect.DeepEqual(got, []scope.Path{".", "packages/app"}) {
			t.Fatalf("context roots = %v, want root and packages/app", got)
		}
	})
	t.Run("configured ancestor covers configless nested package", func(t *testing.T) {
		repository := t.TempDir()
		writeFile(t, repository, "eslint.config.js", "module.exports = [];\n")
		writeFile(t, repository, "packages/app/package.json", `{"devDependencies":{"eslint":"10.12.0"}}`)
		instances, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: repository})
		if err != nil {
			t.Fatal(err)
		}
		current := requireESLintInstance(t, instances)
		if len(current.contexts) != 1 || current.contexts[0].root != "." {
			t.Fatalf("contexts = %#v, want only configured root", current.contexts)
		}
	})
}

func TestDetectKeepsRepositoryControlledProblemsExplicit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliable on Windows test hosts")
	}
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
		code    string
	}{
		{name: "malformed manifest", prepare: func(t *testing.T, root string) {
			writeFile(t, root, "package.json", "{")
		}, code: "eslint/discovery-unavailable"},
		{name: "oversized manifest", prepare: func(t *testing.T, root string) {
			writeFile(t, root, "package.json", strings.Repeat(" ", maxManifestBytes+1))
		}, code: "eslint/discovery-unavailable"},
		{name: "manifest symlink", prepare: func(t *testing.T, root string) {
			writeFile(t, root, "real.json", `{"devDependencies":{"eslint":"9.39.5"}}`)
			if err := os.Symlink("real.json", filepath.Join(root, "package.json")); err != nil {
				t.Fatal(err)
			}
		}, code: "eslint/discovery-unavailable"},
		{name: "config symlink", prepare: func(t *testing.T, root string) {
			writeFile(t, root, "package.json", `{"devDependencies":{"eslint":"9.39.5"}}`)
			writeFile(t, root, "real.js", "module.exports = [];\n")
			if err := os.Symlink("real.js", filepath.Join(root, "eslint.config.js")); err != nil {
				t.Fatal(err)
			}
		}, code: "eslint/config-uninspectable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := t.TempDir()
			test.prepare(t, repository)
			instances, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: repository})
			if err != nil {
				t.Fatal(err)
			}
			current := requireESLintInstance(t, instances)
			got := ""
			if len(current.issues) != 0 {
				got = discoveryExplanation(current.issues[0]).Code
			} else if len(current.contexts) != 0 && current.contexts[0].unavailable != nil {
				got = current.contexts[0].unavailable.Code
			}
			if got != test.code {
				t.Fatalf("problem code = %q, want %q (%#v)", got, test.code, current)
			}
		})
	}
}

func TestEvaluateScopesDiscoveryIssuesToTheirSubtree(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFile(t, repository, "fixtures/bad/package.json", "{")
	current := New(nil, nil)
	instance := detectedInstance(t, current, repository)

	results, err := current.Evaluate(
		context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instance,
		[]scope.Path{"src/app.ts", "fixtures/bad/data.ts"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.NotApp || results[0].Explanation.Code != "eslint/not-applicable" ||
		results[1].State != scope.Unknown || results[1].Explanation.Code != "eslint/discovery-unavailable" {
		t.Fatalf("results = %#v", results)
	}
}

func TestDetectLimitCancellationAndInputErrors(t *testing.T) {
	t.Parallel()
	t.Run("limit", func(t *testing.T) {
		repository := t.TempDir()
		writeFile(t, repository, "eslint.config.js", "module.exports = [];\n")
		writeFile(t, repository, "extra", "")
		currentProvider := New(nil, nil)
		currentProvider.scanLimit = 2
		instances, err := currentProvider.Detect(context.Background(), provider.Repository{Root: repository})
		if err != nil {
			t.Fatal(err)
		}
		current := requireESLintInstance(t, instances)
		if current.discovery == nil || current.discovery.Code != "eslint/discovery-limit" {
			t.Fatalf("discovery = %#v", current.discovery)
		}
		if len(current.contexts) != 1 || current.contexts[0].root != "." || current.contexts[0].config != "eslint.config.js" {
			t.Fatalf("contexts = %#v, want discovered root config", current.contexts)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := New(nil, nil).Detect(ctx, provider.Repository{Root: t.TempDir()}); err == nil {
			t.Fatal("Detect() error = nil")
		}
	})
	t.Run("empty root", func(t *testing.T) {
		if _, err := New(nil, nil).Detect(context.Background(), provider.Repository{}); err == nil {
			t.Fatal("Detect() error = nil")
		}
	})
}

func TestSetupSelectionsOnlyWhenEvaluationCanUseNode(t *testing.T) {
	t.Parallel()
	current := New(nil, nil)
	ready := eslintInstance{contexts: []eslintContext{{root: ".", config: "eslint.config.js"}}}
	unavailable := eslintInstance{contexts: []eslintContext{{root: ".", unavailable: &scope.Explanation{Code: "eslint/config-unavailable", Summary: "missing"}}}}
	repository := t.TempDir()
	writeFile(t, repository, "node_modules/eslint/package.json", `{"name":"eslint","version":"9.39.5"}`)
	writeFile(t, repository, "node_modules/eslint/lib/api.js", "module.exports = {};\n")
	if got := current.SetupSelections(repository, nil); len(got) != 0 {
		t.Fatalf("empty selections = %v", got)
	}
	if got := current.SetupSelections(repository, []provider.Instance{unavailable}); len(got) != 0 {
		t.Fatalf("unavailable selections = %v", got)
	}
	if got := current.SetupSelections(t.TempDir(), []provider.Instance{ready}); len(got) != 0 {
		t.Fatalf("missing-package selections = %v", got)
	}
	if got := current.SetupSelections(repository, []provider.Instance{ready}); !reflect.DeepEqual(got, []safeexec.Selection{{Tool: nodeTool}}) {
		t.Fatalf("ready selections = %v", got)
	}
}

func requireESLintInstance(t *testing.T, instances []provider.Instance) eslintInstance {
	t.Helper()
	if len(instances) != 1 {
		t.Fatalf("instances = %d, want one", len(instances))
	}
	current, ok := instances[0].(eslintInstance)
	if !ok {
		t.Fatalf("instance type = %T", instances[0])
	}
	return current
}
