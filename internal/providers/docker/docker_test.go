package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestProviderDetect(t *testing.T) {
	t.Parallel()
	if got := New().ID(); got != "docker" {
		t.Fatalf("ID() = %q, want docker", got)
	}

	t.Run("root context", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeFile(t, root, ".dockerignore", "# comment\n*.log\n!keep.log\n")

		instances, err := New().Detect(context.Background(), provider.Repository{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		if len(instances) != 1 {
			t.Fatalf("Detect() returned %d instances, want 1", len(instances))
		}
		got, ok := instances[0].(dockerInstance)
		if !ok {
			t.Fatalf("Detect() instance has type %T", instances[0])
		}
		want := dockerInstance{
			descriptor:  provider.InstanceDescriptor{Provider: "docker", ID: "docker", Label: "Docker context ."},
			contextRoot: ".",
			ignorePath:  ".dockerignore",
			patterns:    []string{"*.log", "!keep.log"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Detect() = %+v, want %+v", got, want)
		}
	})

	t.Run("no explicit context", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeFile(t, root, "Dockerfile", "FROM scratch\n")
		instances, err := New().Detect(context.Background(), provider.Repository{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		if instances != nil {
			t.Fatalf("Detect() = %+v, want nil", instances)
		}
	})
}

func TestProviderDetectNormalizesOfficialIgnoreSyntax(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, ".dockerignore", "\ufeff# comment\n /dist/ \n\n!dist/keep\n")

	instances, err := New().Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := instances[0].(dockerInstance)
	if !ok {
		t.Fatalf("Detect() instance has type %T", instances[0])
	}
	if want := []string{"dist", "!dist/keep"}; !reflect.DeepEqual(got.patterns, want) {
		t.Fatalf("patterns = %q, want %q", got.patterns, want)
	}
}

func TestProviderDetectErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(*testing.T, string)
		repo      provider.Repository
		ctx       func() context.Context
		wantError string
	}{
		{name: "empty root", repo: provider.Repository{}, wantError: "repository root is empty"},
		{name: "missing root", repo: provider.Repository{Root: filepath.Join(t.TempDir(), "missing")}, wantError: "open repository root"},
		{name: "directory", setup: func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, ".dockerignore"), 0o750); err != nil {
				t.Fatal(err)
			}
		}, wantError: "is a directory"},
		{name: "invalid pattern", setup: func(t *testing.T, root string) {
			writeFile(t, root, ".dockerignore", "[\n")
		}, wantError: "parse .dockerignore"},
		{name: "cancelled", ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, wantError: "context canceled"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if test.setup != nil {
				test.setup(t, root)
			}
			repo := test.repo
			if repo.Root == "" && test.name != "empty root" {
				repo.Root = root
			}
			ctx := context.Background()
			if test.ctx != nil {
				ctx = test.ctx()
			}
			_, err := New().Detect(ctx, repo)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Detect() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestProviderReturnsUnknownWhenIgnoreFileCannotBeReadSafely(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, ".dockerignore", strings.Repeat("a", maxDockerignoreSize+1))

	instances, err := New().Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	results, err := New().Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{Root: root},
		instances[0],
		[]scope.Path{"ordinary", ".dockerignore"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "docker/ignore-unreadable" || !strings.Contains(results[0].Explanation.Summary, "file exceeds") {
		t.Fatalf("ordinary result = %+v, want UNKNOWN with size explanation", results[0])
	}
	if results[1].State != scope.In || results[1].Explanation.Code != "docker/context-metadata" {
		t.Fatalf(".dockerignore result = %+v, want special IN", results[1])
	}
}

func TestProviderEvaluateDecisionTable(t *testing.T) {
	t.Parallel()
	instance := dockerInstance{
		descriptor:  provider.InstanceDescriptor{Provider: "docker", ID: "docker"},
		contextRoot: ".",
		ignorePath:  ".dockerignore",
		patterns: []string{
			"*.log",
			"!keep.log",
			"build",
			"**/*.tmp",
			"secret?",
			"[ab].txt",
		},
	}
	paths := []scope.Path{
		".dockerignore",
		"app.go",
		"debug.log",
		"keep.log",
		"build/output.bin",
		"nested/cache.tmp",
		"secret1",
		"a.txt",
	}

	results, err := New().Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{}, instance, paths)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		state scope.State
		code  string
		text  string
	}{
		{scope.In, "docker/context-metadata", "context metadata"},
		{scope.In, "docker/included", "no matching exclusion"},
		{scope.Out, "docker/excluded", `pattern "*.log"`},
		{scope.In, "docker/included-negation", `pattern "!keep.log"`},
		{scope.Out, "docker/excluded", `pattern "build"`},
		{scope.Out, "docker/excluded", `pattern "**/*.tmp"`},
		{scope.Out, "docker/excluded", `pattern "secret?"`},
		{scope.Out, "docker/excluded", `pattern "[ab].txt"`},
	}
	for i, result := range results {
		if result.State != want[i].state || result.Explanation.Code != want[i].code || !strings.Contains(result.Explanation.Summary, want[i].text) {
			t.Errorf("result[%d] = %+v, want %s %s containing %q", i, result, want[i].state, want[i].code, want[i].text)
		}
		if result.Provenance != (scope.Provenance{Method: "safe-parser", Reference: "github.com/moby/patternmatcher"}) {
			t.Errorf("result[%d] provenance = %+v", i, result.Provenance)
		}
	}
}

func TestProviderEvaluateContextBoundaries(t *testing.T) {
	t.Parallel()
	instance := dockerInstance{
		descriptor:  provider.InstanceDescriptor{Provider: "docker", ID: "context:services/web"},
		contextRoot: "services/web",
		dockerfile:  "services/web/build.Dockerfile",
		ignorePath:  "services/web/build.Dockerfile.dockerignore",
		patterns:    []string{"*"},
	}
	paths := []scope.Path{
		"services/website/file",
		"services/web",
		"services/web/build.Dockerfile",
		"services/web/build.Dockerfile.dockerignore",
		"services/web/other",
	}

	results, err := New().Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{}, instance, paths)
	if err != nil {
		t.Fatal(err)
	}
	wantStates := []scope.State{scope.NotApp, scope.In, scope.In, scope.In, scope.Out}
	wantCodes := []string{"docker/outside-context", "docker/context-root", "docker/dockerfile", "docker/context-metadata", "docker/excluded"}
	for i, result := range results {
		if result.State != wantStates[i] || result.Explanation.Code != wantCodes[i] {
			t.Errorf("result[%d] = %s %s, want %s %s", i, result.State, result.Explanation.Code, wantStates[i], wantCodes[i])
		}
	}
}

func TestProviderEvaluateErrors(t *testing.T) {
	t.Parallel()
	valid := dockerInstance{
		descriptor:  provider.InstanceDescriptor{Provider: "docker", ID: "docker"},
		contextRoot: ".",
		ignorePath:  ".dockerignore",
	}

	tests := []struct {
		name      string
		instance  provider.Instance
		ctx       func() context.Context
		wantError string
	}{
		{name: "wrong type", instance: provider.InstanceDescriptor{Provider: "docker", ID: "docker"}, wantError: "unsupported Docker instance"},
		{name: "wrong provider", instance: dockerInstance{descriptor: provider.InstanceDescriptor{Provider: "git", ID: "docker"}}, wantError: "unsupported instance"},
		{name: "empty ID", instance: dockerInstance{descriptor: provider.InstanceDescriptor{Provider: "docker"}}, wantError: "unsupported instance"},
		{name: "invalid pattern", instance: dockerInstance{descriptor: valid.descriptor, contextRoot: ".", ignorePath: ".dockerignore", patterns: []string{"["}}, wantError: "compile .dockerignore"},
		{name: "cancelled", instance: valid, ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, wantError: "context canceled"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			if test.ctx != nil {
				ctx = test.ctx()
			}
			_, err := New().Evaluate(ctx, provider.EvaluationContext{}, provider.Repository{}, test.instance, []scope.Path{"a"})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Evaluate() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestProviderEvaluateEmptyPaths(t *testing.T) {
	t.Parallel()
	instance := dockerInstance{
		descriptor:  provider.InstanceDescriptor{Provider: "docker", ID: "docker"},
		contextRoot: ".",
		patterns:    []string{"["},
	}
	results, err := New().Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{}, instance, nil)
	if err != nil {
		t.Fatal(err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("Evaluate() = %+v, want non-nil empty", results)
	}
}

func TestRelativeToContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    scope.Path
		root    scope.Path
		want    string
		applies bool
	}{
		{name: "anything", root: ".", want: "anything", applies: true},
		{name: "services/web", root: "services/web", want: ".", applies: true},
		{name: "services/web/a", root: "services/web", want: "a", applies: true},
		{name: "services/website/a", root: "services/web"},
		{name: "services", root: "services/web"},
	}
	for _, test := range tests {
		t.Run(string(test.name)+"_in_"+string(test.root), func(t *testing.T) {
			t.Parallel()
			got, applies := relativeToContext(test.name, test.root)
			if got != test.want || applies != test.applies {
				t.Fatalf("relativeToContext() = (%q, %v), want (%q, %v)", got, applies, test.want, test.applies)
			}
		})
	}
}

func TestDetectRejectsEscapingDockerignoreSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside-ignore")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".dockerignore")); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skip("symlinks are unavailable")
		}
		t.Fatal(err)
	}
	instances, err := New().Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	results, err := New().Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{Root: root},
		instances[0],
		[]scope.Path{"ordinary"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "docker/ignore-unreadable" {
		t.Fatalf("result = %+v, want UNKNOWN", results[0])
	}
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
