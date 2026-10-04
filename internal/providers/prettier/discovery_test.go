package prettier

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
)

func TestDetectContexts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		files       map[string]string
		wantRoots   []string
		wantProblem string
		wantNone    bool
	}{
		{
			name:      "root direct dependency",
			files:     map[string]string{"package.json": `{"devDependencies":{"prettier":"3.9.9"}}`},
			wantRoots: []string{"."},
		},
		{
			name: "nested direct dependencies",
			files: map[string]string{
				"package.json":                `{}`,
				"web/package.json":            `{"dependencies":{"prettier":"3.8.5"}}`,
				"tools/package.json":          `{"optionalDependencies":{"prettier":"3.6.2"}}`,
				"node_modules/x/package.json": `{"dependencies":{"prettier":"3.9.9"}}`,
			},
			wantRoots: []string{"tools", "web"},
		},
		{
			name:      "root config fallback",
			files:     map[string]string{".prettierrc.json": `{}`},
			wantRoots: []string{"."}, wantProblem: "prettier/package-unavailable",
		},
		{
			name:     "nested config alone is not an invocation context",
			files:    map[string]string{"web/.prettierrc.json": `{}`},
			wantNone: true,
		},
		{
			name:     "unrelated package manifest",
			files:    map[string]string{"package.json": `{"name":"sample","peerDependencies":{"prettier":"3.x"}}`},
			wantNone: true,
		},
		{
			name:        "malformed manifest leaves discovery unresolved",
			files:       map[string]string{"package.json": `{`},
			wantProblem: "prettier/discovery-unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFixtureFiles(t, root, test.files)
			instances, err := New(nil, nil).Detect(context.Background(), provider.Repository{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			if test.wantNone {
				if len(instances) != 0 {
					t.Fatalf("Detect() = %#v, want no instances", instances)
				}
				return
			}
			if len(instances) != 1 {
				t.Fatalf("Detect() returned %d instances, want 1", len(instances))
			}
			instance, ok := instances[0].(prettierInstance)
			if !ok {
				t.Fatalf("Detect() returned %T, want prettierInstance", instances[0])
			}
			roots := make([]string, 0, len(instance.contexts))
			for _, current := range instance.contexts {
				roots = append(roots, string(current.root))
				if current.unavailable != nil && test.wantProblem != current.unavailable.Code {
					t.Fatalf("context problem = %q, want %q", current.unavailable.Code, test.wantProblem)
				}
			}
			if len(roots) != len(test.wantRoots) {
				t.Fatalf("roots = %v, want %v", roots, test.wantRoots)
			}
			for index := range roots {
				if roots[index] != test.wantRoots[index] {
					t.Fatalf("roots = %v, want %v", roots, test.wantRoots)
				}
			}
			if test.wantProblem != "" && instance.discovery == nil && len(instance.contexts) == 0 {
				t.Fatal("Detect() omitted expected discovery problem")
			}
			if instance.discovery != nil && instance.discovery.Code != test.wantProblem {
				t.Fatalf("discovery code = %q, want %q", instance.discovery.Code, test.wantProblem)
			}
		})
	}
}

func TestDetectDiscoveryLimitIsUnknown(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixtureFiles(t, root, map[string]string{
		"package.json": `{"devDependencies":{"prettier":"3.9.9"}}`,
		"src/a.js":     "",
	})
	providerUnderTest := New(nil, nil)
	providerUnderTest.scanLimit = 1
	instances, err := providerUnderTest.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	instance, ok := instances[0].(prettierInstance)
	if !ok {
		t.Fatalf("Detect() returned %T, want prettierInstance", instances[0])
	}
	if instance.discovery == nil || instance.discovery.Code != "prettier/discovery-limit" {
		t.Fatalf("discovery = %#v, want limit problem", instance.discovery)
	}
}

func writeFixtureFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		absolute := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
