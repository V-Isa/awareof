package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestProviderDetect(t *testing.T) {
	t.Parallel()
	if got := New(&fakeRunner{}).ID(); got != "git" {
		t.Fatalf("ID() = %q, want git", got)
	}

	t.Run("git marker", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o750); err != nil {
			t.Fatal(err)
		}
		instances, err := New(&fakeRunner{}).Detect(context.Background(), provider.Repository{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		want := []provider.Instance{provider.InstanceDescriptor{Provider: "git", ID: "git", Label: "Git worktree"}}
		if !reflect.DeepEqual(instances, want) {
			t.Fatalf("Detect() = %+v, want %+v", instances, want)
		}
	})

	t.Run("no marker", func(t *testing.T) {
		t.Parallel()
		instances, err := New(&fakeRunner{}).Detect(context.Background(), provider.Repository{Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if instances != nil {
			t.Fatalf("Detect() = %+v, want nil", instances)
		}
	})
}

func TestProviderEvaluateDecisionTable(t *testing.T) {
	t.Parallel()
	paths := []scope.Path{
		"tracked-ignored", "tracked", "ignored", "unignored", "ordinary", "module", "module/file.go",
	}
	runner := &fakeRunner{responses: []safeexec.Response{
		{Stdout: []byte(
			"100644 a 0\ttracked-ignored\x00" +
				"100644 b 0\ttracked\x00" +
				"160000 c 0\tmodule\x00",
		)},
		{Stdout: ignoreOutput(paths[:6],
			ignoreFact{Source: ".gitignore", Line: 1, Pattern: "tracked-ignored", Ignored: true},
			ignoreFact{},
			ignoreFact{Source: ".gitignore", Line: 2, Pattern: "ignored", Ignored: true},
			ignoreFact{Source: ".gitignore", Line: 3, Pattern: "!unignored"},
			ignoreFact{},
			ignoreFact{},
		), ExitCode: 0},
	}}

	results, err := New(runner).Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: "/repo"}, provider.InstanceDescriptor{Provider: "git", ID: "git"}, paths)
	if err != nil {
		t.Fatal(err)
	}
	wantStates := []scope.State{scope.In, scope.In, scope.Out, scope.In, scope.In, scope.In, scope.NotApp}
	wantCodes := []string{"git/tracked-ignored", "git/tracked", "git/ignored", "git/unignored", "git/not-ignored", "git/tracked", "git/submodule-content"}
	for i, result := range results {
		if result.State != wantStates[i] || result.Explanation.Code != wantCodes[i] {
			t.Errorf("result[%d] = %s %s, want %s %s", i, result.State, result.Explanation.Code, wantStates[i], wantCodes[i])
		}
		if result.Provenance != (scope.Provenance{Method: "safe-native", Tool: "git"}) {
			t.Errorf("result[%d] provenance = %+v", i, result.Provenance)
		}
	}
	if len(runner.requests) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(runner.requests))
	}
	if got := string(runner.requests[1].Stdin); got != "./"+strings.Join(gotStrings(paths[:6]), "\x00./")+"\x00" {
		t.Fatalf("check-ignore stdin = %q", got)
	}
	for _, request := range runner.requests {
		if request.Tool != "git" || request.Root != "/repo" {
			t.Errorf("request = %+v", request)
		}
		if request.Env["GIT_OPTIONAL_LOCKS"] != "0" || !contains(request.UnsetEnv, "GIT_DIR") || !contains(request.UnsetEnv, "GIT_EXEC_PATH") || !contains(request.UnsetEnv, "GIT_TRACE2_EVENT") {
			t.Errorf("unsafe Git environment request = %+v", request)
		}
	}
}

func TestProviderEvaluateUnavailableToolReturnsUnknown(t *testing.T) {
	t.Parallel()
	unavailable := &safeexec.UnavailableError{
		Code:     "tool/not-approved",
		Summary:  "tool \"git\" is not approved",
		Evidence: "resolved executable \"/usr/bin/git\" has sha256 abc",
		Action:   "run awareof --setup",
	}
	paths := []scope.Path{"a", "b"}
	results, err := New(&fakeRunner{errors: []error{unavailable}}).Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{Root: t.TempDir()},
		provider.InstanceDescriptor{Provider: "git", ID: "git"},
		paths,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(paths) {
		t.Fatalf("results = %d, want %d", len(results), len(paths))
	}
	for _, result := range results {
		if result.State != scope.Unknown || result.Explanation.Code != unavailable.Code || result.Explanation.Action != unavailable.Action || result.Provenance.Method != scope.Unavailable {
			t.Errorf("result = %+v, want UNKNOWN unavailable explanation", result)
		}
	}
}

func TestProviderEvaluateErrors(t *testing.T) {
	t.Parallel()
	instance := provider.InstanceDescriptor{Provider: "git", ID: "git"}
	paths := []scope.Path{"a"}
	validTracked := safeexec.Response{Stdout: []byte("100644 a 0\ta\x00")}
	validIgnore := safeexec.Response{Stdout: ignoreOutput(paths, ignoreFact{})}

	tests := []struct {
		name      string
		provider  *Provider
		instance  provider.Instance
		paths     []scope.Path
		wantError string
	}{
		{name: "nil runner", provider: New(nil), instance: instance, paths: paths, wantError: "runner is nil"},
		{name: "wrong instance", provider: New(&fakeRunner{}), instance: provider.InstanceDescriptor{Provider: "git", ID: "other"}, paths: paths, wantError: "unsupported instance"},
		{name: "tracked runner error", provider: New(&fakeRunner{errors: []error{errors.New("runner failed")}}), instance: instance, paths: paths, wantError: "list tracked paths"},
		{name: "tracked command error", provider: New(&fakeRunner{responses: []safeexec.Response{{ExitCode: 3, Stderr: []byte("bad repo")}}}), instance: instance, paths: paths, wantError: "status 3: bad repo"},
		{name: "malformed tracked record", provider: New(&fakeRunner{responses: []safeexec.Response{{Stdout: []byte("bad\x00")}}}), instance: instance, paths: paths, wantError: "parse record"},
		{name: "malformed tracked metadata", provider: New(&fakeRunner{responses: []safeexec.Response{{Stdout: []byte("100644 a\ta\x00")}}}), instance: instance, paths: paths, wantError: "parse metadata"},
		{name: "ignore runner error", provider: New(&fakeRunner{responses: []safeexec.Response{validTracked}, errors: []error{nil, errors.New("runner failed")}}), instance: instance, paths: paths, wantError: "evaluate ignore rules"},
		{name: "ignore command error", provider: New(&fakeRunner{responses: []safeexec.Response{validTracked, {ExitCode: 2}}}), instance: instance, paths: paths, wantError: "status 2"},
		{name: "wrong ignore field count", provider: New(&fakeRunner{responses: []safeexec.Response{validTracked, {Stdout: []byte("a\x00")}}}), instance: instance, paths: paths, wantError: "returned 1 fields"},
		{name: "wrong ignore path", provider: New(&fakeRunner{responses: []safeexec.Response{validTracked, {Stdout: []byte("\x00\x00\x00wrong\x00")}}}), instance: instance, paths: paths, wantError: "returned path"},
		{name: "bad ignore line", provider: New(&fakeRunner{responses: []safeexec.Response{validTracked, {Stdout: []byte(".gitignore\x00bad\x00a\x00./a\x00")}}}), instance: instance, paths: paths, wantError: "parse line"},
		{name: "nonpositive ignore line", provider: New(&fakeRunner{responses: []safeexec.Response{validTracked, {Stdout: []byte(".gitignore\x000\x00a\x00./a\x00")}}}), instance: instance, paths: paths, wantError: "invalid line"},
		{name: "invalid ignore encoding", provider: New(&fakeRunner{responses: []safeexec.Response{validTracked, {Stdout: []byte(".gitignore\x001\x00a\xff\x00./a\x00")}}}), instance: instance, paths: paths, wantError: "invalid UTF-8"},
		{name: "valid baseline", provider: New(&fakeRunner{responses: []safeexec.Response{validTracked, validIgnore}}), instance: instance, paths: paths},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			results, err := test.provider.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: "/repo"}, test.instance, test.paths)
			if test.wantError == "" {
				if err != nil || len(results) != 1 {
					t.Fatalf("Evaluate() = (%+v, %v), want one result", results, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Evaluate() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestProviderSkipsUnrepresentableTrackedPaths(t *testing.T) {
	t.Parallel()

	paths := []scope.Path{"a"}
	runner := &fakeRunner{responses: []safeexec.Response{
		{Stdout: []byte("100644 a 0\tunrepresentable-\xff\x00" + "100644 b 0\ta\x00")},
		{Stdout: ignoreOutput(paths, ignoreFact{})},
	}}
	results, err := New(runner).Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{Root: "/repo"},
		provider.InstanceDescriptor{Provider: "git", ID: "git"},
		paths,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].State != scope.In || results[0].Explanation.Code != "git/tracked" {
		t.Fatalf("Evaluate() = %+v, want tracked canonical path", results)
	}
}

func TestProviderEvaluateEmptyPathsDoesNotRunCommand(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	results, err := New(runner).Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{}, provider.InstanceDescriptor{Provider: "git", ID: "git"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if results == nil || len(results) != 0 || len(runner.requests) != 0 {
		t.Fatalf("Evaluate() = (%v, %d calls), want non-nil empty and no calls", results, len(runner.requests))
	}
}

func TestDescribePattern(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tests := []struct {
		name string
		fact ignoreFact
		want string
	}{
		{name: "relative", fact: ignoreFact{Source: ".gitignore", Line: 2, Pattern: "*.log"}, want: `.gitignore:2 pattern "*.log"`},
		{name: "absolute inside", fact: ignoreFact{Source: filepath.Join(root, "nested", ".gitignore"), Line: 1, Pattern: "x"}, want: `nested/.gitignore:1 pattern "x"`},
		{name: "absolute outside hides path", fact: ignoreFact{Source: filepath.Join(filepath.Dir(root), "global-ignore"), Pattern: "secret"}, want: `global Git excludes pattern "secret"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := describePattern(root, test.fact); got != test.want {
				t.Fatalf("describePattern() = %q, want %q", got, test.want)
			}
		})
	}
}

type fakeRunner struct {
	requests  []safeexec.Request
	responses []safeexec.Response
	errors    []error
}

func (r *fakeRunner) Run(_ context.Context, request safeexec.Request) (safeexec.Response, error) {
	r.requests = append(r.requests, request)
	index := len(r.requests) - 1
	var response safeexec.Response
	if index < len(r.responses) {
		response = r.responses[index]
	}
	var err error
	if index < len(r.errors) {
		err = r.errors[index]
	}
	return response, err
}

func ignoreOutput(paths []scope.Path, facts ...ignoreFact) []byte {
	var builder strings.Builder
	for i, fact := range facts {
		builder.WriteString(fact.Source)
		builder.WriteByte(0)
		if fact.Line > 0 {
			builder.WriteString(strconv.Itoa(fact.Line))
		}
		builder.WriteByte(0)
		builder.WriteString(fact.Pattern)
		builder.WriteByte(0)
		builder.WriteString(gitInputPath(paths[i]))
		builder.WriteByte(0)
	}
	return []byte(builder.String())
}

func gotStrings(paths []scope.Path) []string {
	values := make([]string, len(paths))
	for i, path := range paths {
		values[i] = string(path)
	}
	return values
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
