package pathset

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/scope"
)

func TestBuilderBuild(t *testing.T) {
	t.Parallel()

	root := pathsetFixture(t)
	tests := []struct {
		name      string
		builder   Builder
		inputs    []string
		stdin     io.Reader
		options   BuildOptions
		want      []scope.Path
		wantError string
	}{
		{name: "exact file", builder: Builder{Root: root}, inputs: []string{"src/a.go"}, want: []scope.Path{"src/a.go"}},
		{name: "absolute exact file", builder: Builder{Root: root}, inputs: []string{filepath.Join(root, "src", "a.go")}, want: []scope.Path{"src/a.go"}},
		{name: "nonexistent logical path", builder: Builder{Root: root}, inputs: []string{"future/file.go"}, want: []scope.Path{"future/file.go"}},
		{name: "directory recursively expands", builder: Builder{Root: root}, inputs: []string{"src"}, want: []scope.Path{"src/a.go", "src/generated/x.ts", "src/nested/b.go"}},
		{name: "query base applies to exact path", builder: Builder{Root: root, Base: filepath.Join(root, "src")}, inputs: []string{"a.go"}, want: []scope.Path{"src/a.go"}},
		{name: "query base applies to directory", builder: Builder{Root: root, Base: filepath.Join(root, "src")}, inputs: []string{"."}, want: []scope.Path{"src/a.go", "src/generated/x.ts", "src/nested/b.go"}},
		{name: "globstar matches recursively", builder: Builder{Root: root}, inputs: []string{"src/**/*.go"}, want: []scope.Path{"src/a.go", "src/nested/b.go"}},
		{name: "globstar matches dotfiles", builder: Builder{Root: root}, inputs: []string{"**/*"}, want: []scope.Path{".env", ".gitignore", "README.md", "link", "src/a.go", "src/generated/x.ts", "src/nested/b.go"}},
		{name: "single segment star matches dotfile", builder: Builder{Root: root}, inputs: []string{"*"}, want: []scope.Path{".env", ".gitignore", "README.md", "link"}},
		{name: "question and class", builder: Builder{Root: root}, inputs: []string{"src/?[.]go", "src/[a-z]enerated/*.ts"}, want: []scope.Path{"src/a.go", "src/generated/x.ts"}},
		{name: "absolute glob", builder: Builder{Root: root}, inputs: []string{filepath.ToSlash(filepath.Join(root, "src", "*.go"))}, want: []scope.Path{"src/a.go"}},
		{name: "stdin newline and CRLF", builder: Builder{Root: root}, inputs: []string{"-"}, stdin: strings.NewReader("src/a.go\r\n\n.env\n"), want: []scope.Path{".env", "src/a.go"}},
		{name: "stdin nul", builder: Builder{Root: root}, inputs: []string{"-"}, stdin: strings.NewReader("src/a.go\x00.env\x00"), options: BuildOptions{NullInput: true}, want: []scope.Path{".env", "src/a.go"}},
		{name: "literal nonexistent metacharacters", builder: Builder{Root: root}, inputs: []string{"future/name[1].go"}, options: BuildOptions{Literal: true}, want: []scope.Path{"future/name[1].go"}},
		{name: "deduplicates selectors", builder: Builder{Root: root}, inputs: []string{"src/a.go", "src/*.go", "src/a.go"}, want: []scope.Path{"src/a.go"}},
		{name: "empty stdin matches nothing", builder: Builder{Root: root}, inputs: []string{"-"}, stdin: strings.NewReader(""), want: []scope.Path{}},
		{name: "unmatched glob matches nothing", builder: Builder{Root: root}, inputs: []string{"missing/**"}, want: []scope.Path{}},
		{name: "missing selector", builder: Builder{Root: root}, wantError: "missing path or pattern"},
		{name: "empty selector", builder: Builder{Root: root}, inputs: []string{""}, wantError: "path or pattern is empty"},
		{name: "duplicate stdin", builder: Builder{Root: root}, inputs: []string{"-", "-"}, stdin: strings.NewReader("a"), wantError: "only once"},
		{name: "nil stdin", builder: Builder{Root: root}, inputs: []string{"-"}, wantError: "requires input"},
		{name: "null input without stdin", builder: Builder{Root: root}, inputs: []string{"src/a.go"}, options: BuildOptions{NullInput: true}, wantError: "requires the stdin selector"},
		{name: "exact escape", builder: Builder{Root: root}, inputs: []string{"../outside"}, wantError: "escapes repository root"},
		{name: "glob escape", builder: Builder{Root: root}, inputs: []string{"../*.go"}, wantError: "escapes repository root"},
		{name: "absolute glob escape", builder: Builder{Root: root}, inputs: []string{filepath.ToSlash(filepath.Join(filepath.Dir(root), "*.go"))}, wantError: "escapes repository root"},
		{name: "git metadata exact", builder: Builder{Root: root}, inputs: []string{".git/config"}, wantError: "Git metadata"},
		{name: "invalid globstar", builder: Builder{Root: root}, inputs: []string{"src/a**.go"}, wantError: "must occupy a complete path segment"},
		{name: "malformed class", builder: Builder{Root: root}, inputs: []string{"src/[.go"}, wantError: "invalid pattern"},
		{name: "brace expansion", builder: Builder{Root: root}, inputs: []string{"src/{a,b}/*.go"}, wantError: "brace expansion is not supported"},
		{name: "nul path", builder: Builder{Root: root}, inputs: []string{"a\x00b"}, wantError: "valid UTF-8 without NUL"},
		{name: "invalid utf8 path", builder: Builder{Root: root}, inputs: []string{string([]byte{0xff})}, wantError: "valid UTF-8 without NUL"},
		{name: "empty root", builder: Builder{}, inputs: []string{"a"}, wantError: "repository root is empty"},
		{name: "missing root", builder: Builder{Root: filepath.Join(root, "missing")}, inputs: []string{"a"}, wantError: "open repository root"},
		{name: "base outside root", builder: Builder{Root: root, Base: filepath.Dir(root)}, inputs: []string{"a"}, wantError: "query base"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := test.builder.Build(context.Background(), test.inputs, test.stdin, test.options)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("Build() error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Build() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestBuilderTreatsStdinPathsLiterally(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	name := "literal[1].txt"
	if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths, err := (Builder{Root: root}).Build(
		context.Background(),
		[]string{"-"},
		strings.NewReader(name+"\n"),
		BuildOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []scope.Path{scope.Path(name)}) {
		t.Fatalf("Build() = %v, want literal stdin path", paths)
	}
}

func TestBuilderTreatsLiteralOptionPathsLiterally(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	names := []string{"name[1].go"}
	inputs := []string{"name[1].go", "future[2].txt"}
	want := []scope.Path{"future[2].txt", "name[1].go"}
	if runtime.GOOS != "windows" {
		names = append(names, "literal*.txt")
		inputs = append(inputs, "literal*.txt", "future?.txt")
		want = []scope.Path{"future?.txt", "future[2].txt", "literal*.txt", "name[1].go"}
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	paths, err := (Builder{Root: root}).Build(
		context.Background(),
		inputs,
		nil,
		BuildOptions{Literal: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("Build() = %v, want literal paths %v", paths, want)
	}
}

func TestBuilderHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	builder := Builder{Root: t.TempDir()}

	if _, err := builder.Build(ctx, []string{"path"}, nil, BuildOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Build() error = %v, want context canceled", err)
	}
	if _, err := builder.BuildLiterals(ctx, []string{"path"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildLiterals() error = %v, want context canceled", err)
	}
}

func TestBuilderCancelsWhileReadingStdin(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelingReader{cancel: cancel}

	_, err := (Builder{Root: t.TempDir()}).Build(ctx, []string{"-"}, reader, BuildOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Build() error = %v, want context canceled", err)
	}
}

func TestBuilderBuildLiterals(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tests := []struct {
		name      string
		builder   Builder
		inputs    []string
		want      []scope.Path
		wantError string
	}{
		{name: "empty set", builder: Builder{Root: root}, want: []scope.Path{}},
		{name: "sort deduplicate and preserve missing", builder: Builder{Root: root}, inputs: []string{"z", "a", "z"}, want: []scope.Path{"a", "z"}},
		{name: "glob characters are literal", builder: Builder{Root: root}, inputs: []string{"literal*.txt", "name[1].go"}, want: []scope.Path{"literal*.txt", "name[1].go"}},
		{name: "directory is not expanded", builder: Builder{Root: root}, inputs: []string{"src"}, want: []scope.Path{"src"}},
		{name: "empty literal", builder: Builder{Root: root}, inputs: []string{""}, wantError: "literal path is empty"},
		{name: "repository root", builder: Builder{Root: root}, inputs: []string{"."}, wantError: "repository entry"},
		{name: "escape", builder: Builder{Root: root}, inputs: []string{"../outside"}, wantError: "escapes repository root"},
		{name: "absolute path", builder: Builder{Root: root}, inputs: []string{filepath.Join(filepath.Dir(root), "outside")}, wantError: "repository-relative"},
		{name: "logical absolute path", builder: Builder{Root: root}, inputs: []string{"/absolute"}, wantError: "repository-relative"},
		{name: "git metadata", builder: Builder{Root: root}, inputs: []string{".git/config"}, wantError: "Git metadata"},
		{name: "nul", builder: Builder{Root: root}, inputs: []string{"a\x00b"}, wantError: "valid UTF-8"},
		{name: "invalid utf8", builder: Builder{Root: root}, inputs: []string{string([]byte{0xff})}, wantError: "valid UTF-8"},
		{name: "empty root", builder: Builder{}, inputs: []string{"a"}, wantError: "repository root is empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := test.builder.BuildLiterals(context.Background(), test.inputs)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("BuildLiterals() error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("BuildLiterals() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestBuilderDoesNotTraverseOutsideSymlink(t *testing.T) {
	t.Parallel()
	root := pathsetFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}

	paths, err := (Builder{Root: root}).Build(context.Background(), []string{"outside"}, nil, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []scope.Path{"outside"}) {
		t.Fatalf("symlink query = %v, want symlink entry only", paths)
	}
	if _, err := (Builder{Root: root}).Build(context.Background(), []string{"outside/secret"}, nil, BuildOptions{}); err == nil {
		t.Fatal("Build() error = nil, want outside-symlink traversal rejection")
	}
}

func TestBuilderReportsStdinReadError(t *testing.T) {
	t.Parallel()
	_, err := (Builder{Root: t.TempDir()}).Build(context.Background(), []string{"-"}, errorReader{}, BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "read paths from stdin") {
		t.Fatalf("Build() error = %v, want wrapped stdin error", err)
	}
}

func TestMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		pattern   string
		candidate string
		want      bool
	}{
		{name: "globstar zero segments", pattern: "**/a", candidate: "a", want: true},
		{name: "globstar many segments", pattern: "a/**/b", candidate: "a/x/y/b", want: true},
		{name: "globstar trailing", pattern: "a/**", candidate: "a/x", want: true},
		{name: "star does not cross slash", pattern: "a/*", candidate: "a/x/y", want: false},
		{name: "dotfile ordinary", pattern: "*", candidate: ".env", want: true},
		{name: "no match", pattern: "a/?", candidate: "a/long", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := match(test.pattern, test.candidate)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("match(%q, %q) = %v, want %v", test.pattern, test.candidate, got, test.want)
			}
		})
	}
}

func TestNormalizeRootPattern(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		want      string
		wantError string
	}{
		{name: "normalizes", input: "./src/**", want: "src/**"},
		{name: "dotfile", input: ".env*", want: ".env*"},
		{name: "empty", wantError: "empty"},
		{name: "root", input: ".", wantError: "within"},
		{name: "escape", input: "../a", wantError: "within"},
		{name: "absolute", input: "/a", wantError: "relative"},
		{name: "nul", input: "a\x00b", wantError: "valid UTF-8"},
		{name: "bad globstar", input: "a**", wantError: "complete path segment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeRootPattern(test.input)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("NormalizeRootPattern() error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("NormalizeRootPattern() = %q, %v; want %q", got, err, test.want)
			}
		})
	}

	matched, err := MatchPattern(".env*", ".env.local")
	if err != nil || !matched {
		t.Fatalf("MatchPattern() = %v, %v; want true", matched, err)
	}
}

func FuzzMatchNeverPanics(f *testing.F) {
	f.Add("**/*.go", "src/main.go")
	f.Add("[a-z]?", ".x")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, pattern, candidate string) {
		_, _ = match(pattern, candidate)
	})
}

func pathsetFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		".env":               "secret",
		".git/config":        "metadata",
		".gitignore":         ".env",
		"README.md":          "readme",
		"src/a.go":           "package a",
		"src/generated/x.ts": "generated",
		"src/nested/b.go":    "package b",
	} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("README.md", filepath.Join(root, "link")); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "link"), []byte("fallback"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type cancelingReader struct {
	cancel func()
	read   bool
}

func (r *cancelingReader) Read(buffer []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	written := copy(buffer, "path\n")
	r.cancel()
	return written, nil
}
