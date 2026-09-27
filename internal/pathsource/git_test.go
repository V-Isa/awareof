package pathsource

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

const testObjectID = "0123456789abcdef0123456789abcdef01234567"

func TestGitStaged(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{responses: []safeexec.Response{{Stdout: []byte("z\x00literal*.txt\x00a\x00z\x00")}}}
	got, err := NewGit(runner).Staged(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := []scope.Path{"a", "literal*.txt", "z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Staged() = %v, want %v", got, want)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(runner.requests))
	}
	request := runner.requests[0]
	wantArgs := []string{"--no-pager", "-c", "core.fsmonitor=false", "-c", "diff.submodule=short", "diff", "--name-only", "-z", "--no-ext-diff", "--no-textconv", "--no-renames", "--ignore-submodules=none", "--cached", "--"}
	if request.Tool != "git" || !reflect.DeepEqual(request.Args, wantArgs) {
		t.Fatalf("request = %+v, want staged git diff", request)
	}
	if request.Env["GIT_OPTIONAL_LOCKS"] != "0" || !contains(request.UnsetEnv, "GIT_EXTERNAL_DIFF") {
		t.Fatalf("request environment is not hardened: %+v", request)
	}
}

func TestGitChangedFrom(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{responses: []safeexec.Response{
		{Stdout: []byte(testObjectID + "\n")},
		{Stdout: []byte("src/a.go\x00deleted.go\x00")},
	}}
	got, err := NewGit(runner).ChangedFrom(context.Background(), t.TempDir(), "main")
	if err != nil {
		t.Fatal(err)
	}
	want := []scope.Path{"deleted.go", "src/a.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedFrom() = %v, want %v", got, want)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(runner.requests))
	}
	if wantArgs := []string{"--no-pager", "rev-parse", "--verify", "--quiet", "main^{commit}"}; !reflect.DeepEqual(runner.requests[0].Args, wantArgs) {
		t.Fatalf("rev-parse args = %v, want %v", runner.requests[0].Args, wantArgs)
	}
	wantDiffArgs := []string{"--no-pager", "-c", "core.fsmonitor=false", "-c", "diff.submodule=short", "diff", "--name-only", "-z", "--no-ext-diff", "--no-textconv", "--no-renames", "--ignore-submodules=none", testObjectID, "--"}
	if !reflect.DeepEqual(runner.requests[1].Args, wantDiffArgs) {
		t.Fatalf("diff args = %v, want %v", runner.requests[1].Args, wantDiffArgs)
	}
}

func TestGitRejectsInvalidRevisionWithoutExecution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		revision string
		want     string
	}{
		{name: "empty", want: "empty"},
		{name: "option", revision: "--output=outside", want: "must not start"},
		{name: "nul", revision: "main\x00other", want: "valid UTF-8"},
		{name: "invalid utf8", revision: string([]byte{0xff}), want: "valid UTF-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &recordingRunner{}
			_, err := NewGit(runner).ChangedFrom(context.Background(), t.TempDir(), test.revision)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ChangedFrom() error = %v, want containing %q", err, test.want)
			}
			if len(runner.requests) != 0 {
				t.Fatalf("invalid revision executed %d commands", len(runner.requests))
			}
		})
	}
}

func TestGitReportsCommandFailures(t *testing.T) {
	t.Parallel()
	unavailable := &safeexec.UnavailableError{Code: "tool/not-approved", Summary: "not approved"}
	tests := []struct {
		name      string
		operation func(*Git, string) ([]scope.Path, error)
		runner    *recordingRunner
		want      string
		wantIs    error
	}{
		{name: "nil staged runner", operation: func(source *Git, root string) ([]scope.Path, error) { return source.Staged(context.Background(), root) }, want: "runner is nil"},
		{name: "nil changed runner", operation: func(source *Git, root string) ([]scope.Path, error) {
			return source.ChangedFrom(context.Background(), root, "main")
		}, want: "runner is nil"},
		{name: "staged unavailable", operation: func(source *Git, root string) ([]scope.Path, error) { return source.Staged(context.Background(), root) }, runner: &recordingRunner{errors: []error{unavailable}}, want: "list staged paths", wantIs: unavailable},
		{name: "revision unavailable", operation: func(source *Git, root string) ([]scope.Path, error) {
			return source.ChangedFrom(context.Background(), root, "main")
		}, runner: &recordingRunner{errors: []error{unavailable}}, want: "resolve changed-from", wantIs: unavailable},
		{name: "revision rejected", operation: func(source *Git, root string) ([]scope.Path, error) {
			return source.ChangedFrom(context.Background(), root, "missing")
		}, runner: &recordingRunner{responses: []safeexec.Response{{ExitCode: 1}}}, want: "git exited with status 1"},
		{name: "staged rejected with detail", operation: func(source *Git, root string) ([]scope.Path, error) { return source.Staged(context.Background(), root) }, runner: &recordingRunner{responses: []safeexec.Response{{ExitCode: 129, Stderr: []byte("bad repository\n")}}}, want: "status 129: bad repository"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var runner safeexec.CommandRunner
			if test.runner != nil {
				runner = test.runner
			}
			source := NewGit(runner)
			_, err := test.operation(source, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("operation error = %v, want containing %q", err, test.want)
			}
			if test.wantIs != nil && !errors.Is(err, test.wantIs) {
				t.Fatalf("operation error = %v, want wrapping %v", err, test.wantIs)
			}
		})
	}
}

func TestParseObjectID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		output []byte
		want   string
	}{
		{name: "sha1", output: []byte(testObjectID + "\n"), want: testObjectID},
		{name: "sha256", output: []byte(strings.Repeat("a", 64) + "\r\n"), want: strings.Repeat("a", 64)},
		{name: "without newline", output: []byte(testObjectID), want: testObjectID},
		{name: "empty", output: nil},
		{name: "multiple records", output: []byte(testObjectID + "\n" + testObjectID + "\n")},
		{name: "invalid hex", output: []byte(strings.Repeat("g", 40) + "\n")},
		{name: "wrong length", output: []byte("abcd\n")},
		{name: "invalid utf8", output: []byte{0xff}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseObjectID(test.output)
			if test.want == "" {
				if err == nil {
					t.Fatalf("parseObjectID() = %q, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("parseObjectID() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestParseNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		output []byte
		want   []string
	}{
		{name: "empty", want: []string{}},
		{name: "paths", output: []byte("a\x00b\x00"), want: []string{"a", "b"}},
		{name: "missing terminator", output: []byte("a")},
		{name: "empty record", output: []byte("a\x00\x00")},
		{name: "invalid utf8", output: []byte{'a', 0, 0xff, 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseNames(test.output)
			if test.want == nil {
				if err == nil {
					t.Fatalf("parseNames() = %v, want error", got)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseNames() = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestGitRejectsUnsafePathOutput(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"../outside", ".", ".git/config", "/absolute"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runner := &recordingRunner{responses: []safeexec.Response{{Stdout: append([]byte(name), 0)}}}
			if _, err := NewGit(runner).Staged(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "normalize git path") {
				t.Fatalf("Staged() error = %v, want path rejection", err)
			}
		})
	}
}

type recordingRunner struct {
	requests  []safeexec.Request
	responses []safeexec.Response
	errors    []error
}

func (r *recordingRunner) Run(_ context.Context, request safeexec.Request) (safeexec.Response, error) {
	r.requests = append(r.requests, request)
	index := len(r.requests) - 1
	if index < len(r.errors) && r.errors[index] != nil {
		return safeexec.Response{}, r.errors[index]
	}
	if index < len(r.responses) {
		return r.responses[index], nil
	}
	return safeexec.Response{}, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
