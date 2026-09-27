package npm

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/V-Isa/awareof/internal/scope"
)

func TestWorkspacePatternDecisionTable(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	for _, name := range []string{"packages/a", "packages/b", "packages/.hidden", "packages/a/nested", "tools/cli", "node_modules/pkg"} {
		makeDir(t, rootPath, name)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()

	tests := []struct {
		pattern   string
		want      []scope.Path
		supported bool
	}{
		{pattern: "packages/*", want: []scope.Path{"packages/a", "packages/b"}, supported: true},
		{pattern: "packages/*/nested", want: []scope.Path{"packages/a/nested"}, supported: true},
		{pattern: "./tools/cli", want: []scope.Path{"tools/cli"}, supported: true},
		{pattern: "packages/@scope", want: []scope.Path{}, supported: true},
		{pattern: "node_modules/*", want: []scope.Path{}, supported: true},
		{pattern: "packages/**"},
		{pattern: "!packages/b"},
		{pattern: "packages/a?"},
		{pattern: "../outside"},
	}
	for _, test := range tests {
		t.Run(test.pattern, func(t *testing.T) {
			got, supported, err := expandWorkspacePattern(context.Background(), root, test.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if supported != test.supported || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("expand = (%q, %v), want (%q, %v)", got, supported, test.want, test.supported)
			}
		})
	}
}

func TestWorkspacePatternConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw       string
		want      []string
		supported bool
	}{
		{raw: "", supported: true},
		{raw: "null", supported: true},
		{raw: `["a","b"]`, want: []string{"a", "b"}, supported: true},
		{raw: `{"packages":["a"]}`, want: []string{"a"}, supported: true},
		{raw: `{}`},
		{raw: `"a"`},
	}
	for _, test := range tests {
		got, supported := workspacePatterns(json.RawMessage(test.raw))
		if supported != test.supported || !reflect.DeepEqual(got, test.want) {
			t.Errorf("workspacePatterns(%q) = (%q, %v), want (%q, %v)", test.raw, got, supported, test.want, test.supported)
		}
	}
}

func TestWorkspaceDiscoveryCancellation(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	_, _, err = discoverWorkspaces(cancelled, root, json.RawMessage(`["packages/*"]`))
	if err == nil {
		t.Fatal("discoverWorkspaces() error = nil, want cancellation")
	}
}
