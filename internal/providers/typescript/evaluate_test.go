package typescript

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestParseListFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "src/app.ts", "export {};", 0o600)
	writeTestFile(t, root, "src/other.ts", "export {};", 0o600)
	content := strings.Join([]string{
		filepath.Join(root, "src", "app.ts"),
		filepath.Join(t.TempDir(), "lib.d.ts"),
		"",
	}, "\r\n")
	queries, unresolved, err := prepareQueryIndex(root, []scope.Path{"src/app.ts", "src/other.ts", "future.ts"}, runtime.GOOS)
	if err != nil || len(unresolved) != 0 {
		t.Fatalf("prepareQueryIndex() = %v, %v; want no unresolved paths", queries, unresolved)
	}
	got, err := parseListFiles([]byte(content), queries)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("included = %v, want only src/app.ts", got)
	}
	if _, ok := got["src/app.ts"]; !ok {
		t.Fatalf("included = %v, want src/app.ts", got)
	}
}

func TestParseListFilesWindowsCaseFolding(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "SRC", "APP.TS")
	queries, unresolved, err := prepareQueryIndex(root, []scope.Path{"src/app.ts"}, "windows")
	if err != nil || len(unresolved) != 0 {
		t.Fatalf("prepareQueryIndex() = %v, %v; want no unresolved paths", queries, unresolved)
	}
	got, err := parseListFiles([]byte(path+"\n"), queries)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["src/app.ts"]; !ok {
		t.Fatalf("included = %v, want case-insensitive match", got)
	}
}

func TestParseListFilesRejectsAmbiguousInput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "link-target.ts", "export {};", 0o600)
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{name: "invalid UTF-8", content: []byte{0xff}, want: "UTF-8"},
		{name: "NUL", content: []byte("/tmp/a\x00b\n"), want: "NUL"},
		{name: "empty record", content: []byte(filepath.Join(root, "a.ts") + "\n\n"), want: "empty"},
		{name: "relative output", content: []byte("src/app.ts\n"), want: "not absolute"},
	}
	queries, unresolved, err := prepareQueryIndex(root, []scope.Path{"a.ts", "src/app.ts"}, runtime.GOOS)
	if err != nil || len(unresolved) != 0 {
		t.Fatalf("prepareQueryIndex() = %v, %v; want no unresolved paths", queries, unresolved)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseListFiles(test.content, queries)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseListFiles() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestWriteCausingCompilerOption(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content []byte
		want    string
		wantErr string
	}{
		{name: "safe", content: []byte(`{"compilerOptions":{"strict":true}}`)},
		{name: "generate trace", content: []byte(`{"compilerOptions":{"generateTrace":"./trace"}}`), want: "generateTrace"},
		{name: "invalid UTF-8", content: []byte{0xff}, wantErr: "UTF-8"},
		{name: "malformed", content: []byte(`{`), wantErr: "parse"},
		{name: "multiple values", content: []byte(`{} {}`), wantErr: "multiple"},
		{name: "trailing garbage", content: []byte(`{} x`), wantErr: "parse"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := writeCausingCompilerOption(test.content)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("writeCausingCompilerOption() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("writeCausingCompilerOption() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestPrepareQueryIndexIsolatesAmbiguousPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "src/app.ts", "export {};", 0o600)
	paths := []scope.Path{"src/app.ts", "bad\nname.ts"}
	if runtime.GOOS != "windows" {
		writeTestFile(t, root, "target/file.ts", "export {};", 0o600)
		if err := os.Symlink("target", filepath.Join(root, "linked")); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, "linked/file.ts")
	}
	queries, unresolved, err := prepareQueryIndex(root, paths, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries.wanted) != 1 || unresolved["bad\nname.ts"].Code != "typescript/path-unrepresentable" {
		t.Fatalf("queries = %+v, unresolved = %+v", queries, unresolved)
	}
	if runtime.GOOS != "windows" && unresolved["linked/file.ts"].Code != "typescript/path-symlink-ambiguous" {
		t.Fatalf("linked path = %+v, want path-symlink-ambiguous", unresolved["linked/file.ts"])
	}
}

func TestProviderKeepsPathAmbiguityLocal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	writeTestFile(t, root, "src/app.ts", "export {};", 0o600)
	projectPackage := filepath.Join(root, "node_modules", "typescript")
	writeCompilerPackage(t, projectPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	externalPackage := filepath.Join(t.TempDir(), "typescript")
	writeCompilerPackage(t, externalPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	runner := &fakeRunner{responses: map[string]safeexec.Response{
		"tsconfig.json": {Stdout: []byte(filepath.Join(root, "src", "app.ts") + "\n")},
	}}
	current := New(runner, fixedDiscoverer{target: safeexec.Target{
		Tool:   typescriptTool,
		Path:   resolvedPath(t, filepath.Join(externalPackage, "lib", "_tsc.js")),
		Origin: safeexec.ExternalOrigin,
	}})
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	results, err := current.Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{Root: root},
		instances[0],
		[]scope.Path{"src/app.ts", "bad\nname.ts"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.In || results[1].State != scope.Unknown || results[1].Explanation.Code != "typescript/path-unrepresentable" {
		t.Fatalf("results = %+v, want IN and path-local UNKNOWN", results)
	}
}

func TestProviderTypeScript7Request(t *testing.T) {
	t.Parallel()
	currentPlatform := platform{goos: runtime.GOOS, goarch: runtime.GOARCH}
	platformName, err := platformPackageName(currentPlatform)
	if err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	writeTestFile(t, root, "node_modules/typescript/package.json", `{"name":"typescript","version":"7.0.2"}`, 0o600)
	projectPlatform := filepath.Join(root, "node_modules", "@typescript", platformName)
	writeCompilerPackage(t, projectPlatform, "@typescript/"+platformName, "7.0.2", filepath.Join("lib", executableName(runtime.GOOS)), testNativeCore())
	externalPlatform := filepath.Join(t.TempDir(), platformName)
	writeCompilerPackage(t, externalPlatform, "@typescript/"+platformName, "7.0.2", filepath.Join("lib", executableName(runtime.GOOS)), testNativeCore())
	runner := &fakeRunner{responses: map[string]safeexec.Response{
		"tsconfig.json": {Stdout: []byte(filepath.Join(root, "src", "app.ts") + "\n")},
	}}
	current := New(runner, fixedDiscoverer{target: safeexec.Target{
		Tool:   typescriptTool,
		Path:   resolvedPath(t, filepath.Join(externalPlatform, "lib", executableName(runtime.GOOS))),
		Origin: safeexec.RepositoryOrigin,
	}})
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	results, err := current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], []scope.Path{"src/app.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.In {
		t.Fatalf("result = %+v, want IN", results[0])
	}
	if len(runner.requests) != 2 || !slices.Contains(runner.requests[0].Args, "--showConfig") {
		t.Fatalf("requests = %+v, want show-config preflight and list-files evaluation", runner.requests)
	}
	request := runner.requests[1]
	if request.Interpreter != "" || request.ExternalOnly || strings.Join(request.Args, " ") != "-p tsconfig.json --listFilesOnly --pretty false" {
		t.Fatalf("request = %+v, want direct fixed TypeScript 7 invocation", request)
	}
}

func TestProviderDiscardsResultWhenEvaluatorChanges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	projectPackage := filepath.Join(root, "node_modules", "typescript")
	writeCompilerPackage(t, projectPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	externalPackage := filepath.Join(t.TempDir(), "typescript")
	writeCompilerPackage(t, externalPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	runner := &fakeRunner{run: func(request safeexec.Request) (safeexec.Response, error) {
		writeTestFile(t, externalPackage, "lib/lib.d.ts", "changed", 0o600)
		if slices.Contains(request.Args, "--showConfig") {
			return safeexec.Response{Stdout: []byte(`{"compilerOptions":{}}`)}, nil
		}
		return safeexec.Response{}, nil
	}}
	current := New(runner, fixedDiscoverer{target: safeexec.Target{
		Tool:   typescriptTool,
		Path:   resolvedPath(t, filepath.Join(externalPackage, "lib", "_tsc.js")),
		Origin: safeexec.ExternalOrigin,
	}})
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	results, err := current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], []scope.Path{"a.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "typescript/evaluator-changed" {
		t.Fatalf("result = %+v, want evaluator-changed UNKNOWN", results[0])
	}
}

func TestProviderReturnsUnknownWhenPackageIdentityCannotBeEstablished(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	projectPackage := filepath.Join(root, "node_modules", "typescript")
	writeCompilerPackage(t, projectPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	externalPackage := filepath.Join(t.TempDir(), "typescript")
	writeCompilerPackage(t, externalPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	if err := os.Remove(filepath.Join(projectPackage, "lib", "lib.d.ts")); err != nil {
		t.Fatal(err)
	}
	current := New(&fakeRunner{}, fixedDiscoverer{target: safeexec.Target{
		Tool:   typescriptTool,
		Path:   resolvedPath(t, filepath.Join(externalPackage, "lib", "_tsc.js")),
		Origin: safeexec.ExternalOrigin,
	}})
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	results, err := current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], []scope.Path{"a.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "typescript/evaluator-fingerprint-unavailable" {
		t.Fatalf("result = %+v, want fingerprint-unavailable UNKNOWN", results[0])
	}
}

func TestProviderToolUnavailable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	projectPackage := filepath.Join(root, "node_modules", "typescript")
	writeCompilerPackage(t, projectPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	current := New(&fakeRunner{}, fixedDiscoverer{err: &safeexec.UnavailableError{
		Tool:    typescriptTool,
		Code:    "tool/not-approved",
		Summary: "not approved",
		Action:  "run setup",
	}})
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	results, err := current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], []scope.Path{"a.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Explanation.Code != "tool/not-approved" || results[0].Provenance.Tool != string(typescriptTool) {
		t.Fatalf("result = %+v, want tool/not-approved UNKNOWN", results[0])
	}

	externalPackage := filepath.Join(t.TempDir(), "typescript")
	writeCompilerPackage(t, externalPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	current = New(&fakeRunner{err: &safeexec.UnavailableError{
		Tool:    nodeTool,
		Code:    "tool/not-approved",
		Summary: "not approved",
		Action:  "run setup",
	}}, fixedDiscoverer{target: safeexec.Target{
		Tool:   typescriptTool,
		Path:   resolvedPath(t, filepath.Join(externalPackage, "lib", "_tsc.js")),
		Origin: safeexec.ExternalOrigin,
	}})
	results, err = current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], []scope.Path{"a.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Unknown || results[0].Provenance.Tool != string(nodeTool) {
		t.Fatalf("result = %+v, want Node provenance for unavailable interpreter", results[0])
	}
}

func TestProviderEvaluationGuards(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	instance := typescriptInstance{descriptor: provider.InstanceDescriptor{Provider: providerID, ID: rootInstanceID}}
	tests := []struct {
		name       string
		provider   *Provider
		ctx        context.Context
		paths      []scope.Path
		wantErr    string
		wantResult bool
	}{
		{name: "empty paths", provider: New(nil, nil), ctx: context.Background(), wantResult: true},
		{name: "cancelled", provider: New(&fakeRunner{}, fixedDiscoverer{}), ctx: cancelledContext(), paths: []scope.Path{"a.ts"}, wantErr: "context canceled"},
		{name: "nil runner", provider: New(nil, fixedDiscoverer{}), ctx: context.Background(), paths: []scope.Path{"a.ts"}, wantErr: "runner is nil"},
		{name: "nil discoverer", provider: New(&fakeRunner{}, nil), ctx: context.Background(), paths: []scope.Path{"a.ts"}, wantErr: "discoverer is nil"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			results, err := test.provider.Evaluate(test.ctx, provider.EvaluationContext{}, provider.Repository{Root: root}, instance, test.paths)
			if test.wantResult {
				if err != nil || results == nil || len(results) != 0 {
					t.Fatalf("Evaluate() = %v, %v; want empty result", results, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Evaluate() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestCommandEvidence(t *testing.T) {
	t.Parallel()
	if got := commandEvidence(safeexec.Response{ExitCode: 2}); got != "compiler exited with status 2" {
		t.Fatalf("empty evidence = %q", got)
	}
	if got := commandEvidence(safeexec.Response{ExitCode: 1, Stderr: []byte("bad")}); got != "compiler exited with status 1: bad" {
		t.Fatalf("stderr evidence = %q", got)
	}
	long := strings.Repeat("x", 3000)
	if got := commandEvidence(safeexec.Response{ExitCode: 1, Stdout: []byte(long)}); len(got) >= len(long) || !strings.HasSuffix(got, "...") {
		t.Fatalf("long evidence was not bounded: %d bytes", len(got))
	}
	multibyte := strings.Repeat("é", 1100)
	if got := commandEvidence(safeexec.Response{ExitCode: 1, Stderr: []byte(multibyte)}); !utf8.ValidString(got) || !strings.HasSuffix(got, "...") {
		t.Fatalf("multibyte evidence = %q, want bounded valid UTF-8", got)
	}
}

func TestProviderPropagatesContextRunnerError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, root, "tsconfig.json", `{}`, 0o600)
	projectPackage := filepath.Join(root, "node_modules", "typescript")
	writeCompilerPackage(t, projectPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	externalPackage := filepath.Join(t.TempDir(), "typescript")
	writeCompilerPackage(t, externalPackage, "typescript", "6.0.3", filepath.Join("lib", "_tsc.js"), []byte("compiler"))
	current := New(&fakeRunner{err: context.DeadlineExceeded}, fixedDiscoverer{target: safeexec.Target{
		Tool:   typescriptTool,
		Path:   resolvedPath(t, filepath.Join(externalPackage, "lib", "_tsc.js")),
		Origin: safeexec.ExternalOrigin,
	}})
	instances, err := current.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	_, err = current.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], []scope.Path{"a.ts"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Evaluate() error = %v, want deadline exceeded", err)
	}
}
