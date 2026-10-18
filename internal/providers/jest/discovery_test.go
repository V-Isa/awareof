package jest

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestDetectDiscoversDeclaredContexts(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFile(t, repository, "package.json", `{"private":true,"devDependencies":{"jest":"29.7.0"}}`)
	writeFile(t, repository, "jest.config.js", "module.exports = {};\n")
	writeFile(t, repository, "packages/app/package.json", `{"dependencies":{"jest":"30.5.2"}}`)
	writeFile(t, repository, "packages/app/jest.config.mjs", "export default {};\n")
	writeFile(t, repository, "packages/config-only/package.json", `{"jest":{"testMatch":["**/*.spec.js"]}}`)
	writeFile(t, repository, "nested/jest.config.js", "module.exports = {};\n")
	writeFile(t, repository, "node_modules/ignored/package.json", `{"devDependencies":{"jest":"30.5.2"}}`)

	instances, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	current := requireJestInstance(t, instances)
	got := make([]scope.Path, len(current.contexts))
	for index, currentContext := range current.contexts {
		got[index] = currentContext.root
	}
	want := []scope.Path{".", "packages/app", "packages/config-only"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("context roots = %v, want %v", got, want)
	}
	if !current.contexts[2].packageConfig {
		t.Fatal("package Jest configuration was not recorded")
	}
}

func TestDetectRootConfigFallbackAndNoContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		files     map[string]string
		wantCount int
	}{
		{name: "root config", files: map[string]string{"jest.config.cjs": "module.exports = {};\n"}, wantCount: 1},
		{name: "nested config alone", files: map[string]string{"nested/jest.config.js": "module.exports = {};\n"}},
		{name: "unrelated manifest", files: map[string]string{"package.json": `{"private":true}`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := t.TempDir()
			for name, content := range test.files {
				writeFile(t, repository, name, content)
			}
			instances, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: repository})
			if err != nil {
				t.Fatal(err)
			}
			if len(instances) != test.wantCount {
				t.Fatalf("instances = %#v, want %d", instances, test.wantCount)
			}
		})
	}
}

func TestDetectConfiguredAncestorCoversConfiglessPackage(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFile(t, repository, "jest.config.js", "module.exports = {};\n")
	writeFile(t, repository, "packages/app/package.json", `{"devDependencies":{"jest":"29.7.0"}}`)
	instances, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	current := requireJestInstance(t, instances)
	if len(current.contexts) != 1 || current.contexts[0].root != "." {
		t.Fatalf("contexts = %#v, want configured root only", current.contexts)
	}
}

func TestDetectKeepsRepositoryProblemsExplicitAndScoped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliable on Windows test hosts")
	}
	repository := t.TempDir()
	writeFile(t, repository, "fixtures/bad/package.json", "{")
	writeFile(t, repository, "packages/app/package.json", `{"devDependencies":{"jest":"29.7.0"}}`)
	writeFile(t, repository, "node_modules/jest/package.json", `{"name":"jest","version":"29.7.0"}`)
	writeFile(t, repository, "node_modules/jest/bin/jest.js", "")
	writeFile(t, repository, "packages/app/real.js", "module.exports = {};\n")
	if err := os.Symlink("real.js", filepath.Join(repository, "packages/app/jest.config.js")); err != nil {
		t.Fatal(err)
	}
	currentProvider := New(nil, nil)
	instance := detectedInstance(t, currentProvider, repository)
	results, err := currentProvider.Evaluate(
		context.Background(), provider.EvaluationContext{}, provider.Repository{Root: repository}, instance,
		[]scope.Path{"src/app.ts", "fixtures/bad/data.ts", "packages/app/src/app.ts"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.NotApp || results[1].State != scope.Unknown ||
		results[1].Explanation.Code != "jest/discovery-unavailable" || results[2].State != scope.Unknown ||
		results[2].Explanation.Code != "jest/config-uninspectable" {
		t.Fatalf("results = %#v", results)
	}
}

func TestDetectLimitCancellationAndInputErrors(t *testing.T) {
	t.Parallel()
	t.Run("limit retains root config", func(t *testing.T) {
		repository := t.TempDir()
		writeFile(t, repository, "jest.config.js", "module.exports = {};\n")
		writeFile(t, repository, "a", "")
		currentProvider := New(nil, nil)
		currentProvider.scanLimit = 2
		instances, err := currentProvider.Detect(context.Background(), provider.Repository{Root: repository})
		if err != nil {
			t.Fatal(err)
		}
		current := requireJestInstance(t, instances)
		if current.discovery == nil || current.discovery.Code != "jest/discovery-limit" || len(current.contexts) != 1 {
			t.Fatalf("instance = %#v", current)
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
	t.Run("missing root", func(t *testing.T) {
		if _, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: filepath.Join(t.TempDir(), "missing")}); err == nil {
			t.Fatal("Detect() error = nil")
		}
	})
}

func TestReadManifestBoundaries(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeFile(t, repository, "package.json", strings.Repeat(" ", maxManifestBytes+1))
	instances, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: repository})
	if err != nil {
		t.Fatal(err)
	}
	current := requireJestInstance(t, instances)
	if len(current.issues) != 1 || !strings.Contains(current.issues[0].err.Error(), "exceeds") {
		t.Fatalf("issues = %#v", current.issues)
	}
}
