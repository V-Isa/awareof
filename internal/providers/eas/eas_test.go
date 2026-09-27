package eas

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
	if got := New(nil).ID(); got != "eas" {
		t.Fatalf("ID() = %q, want eas", got)
	}

	root := t.TempDir()
	makeDir(t, root, ".git")
	writeFile(t, root, "eas.json", `{"cli":{"version":">= 16.18.0"}}`)
	writeFile(t, root, "mobile/eas.json", `{"cli":{"requireCommit":false}}`)
	writeFile(t, root, ".gitignore", ".env*\n!.env.example\n")
	writeFile(t, root, "mobile/.easignore", "*.md\n")
	writeFile(t, root, "node_modules/pkg/eas.json", `{}`)
	writeFile(t, root, ".cache/tool/eas.json", `{}`)

	p := New(nil)
	p.lookupEnv = noEnvironment
	instances, err := p.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 {
		t.Fatalf("Detect() returned %d instances, want 2", len(instances))
	}
	want := []easInstance{
		{
			descriptor:        provider.InstanceDescriptor{Provider: "eas", ID: "eas", Label: "EAS project ."},
			inactiveEASIgnore: []scope.Path{"mobile/.easignore"},
			ignoreFile:        ".gitignore",
			perDirectory:      true,
		},
		{
			descriptor:        provider.InstanceDescriptor{Provider: "eas", ID: "project/mobile", Label: "EAS project mobile"},
			inactiveEASIgnore: []scope.Path{"mobile/.easignore"},
			ignoreFile:        ".gitignore",
			perDirectory:      true,
		},
	}
	for index := range want {
		got, ok := instances[index].(easInstance)
		if !ok {
			t.Fatalf("instance[%d] has type %T", index, instances[index])
		}
		if !reflect.DeepEqual(got, want[index]) {
			t.Fatalf("instance[%d] = %+v, want %+v", index, got, want[index])
		}
	}
}

func TestProviderDetectInstanceIDsCannotCollide(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	makeDir(t, root, ".git")
	writeFile(t, root, "eas.json", `{}`)
	writeFile(t, root, "eas/eas.json", `{}`)
	p := New(nil)
	p.lookupEnv = noEnvironment
	instances, err := p.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	want := []scope.InstanceID{"eas", "project/eas"}
	for index, instance := range instances {
		if got := instance.Descriptor().ID; got != want[index] {
			t.Fatalf("instance[%d].ID = %q, want %q", index, got, want[index])
		}
	}
}

func TestProviderDetectReturnsUnknownWhenDiscoveryIsIncomplete(t *testing.T) {
	t.Parallel()

	t.Run("no project found before limit", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeFile(t, root, "ordinary", "content")
		p := New(nil)
		p.lookupEnv = noEnvironment
		p.maxScanEntries = 1
		instances, err := p.Detect(context.Background(), provider.Repository{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		if len(instances) != 1 || instances[0].Descriptor().ID != discoveryInstanceID {
			t.Fatalf("Detect() = %+v, want unresolved discovery instance", instances)
		}
		current, ok := instances[0].(easInstance)
		if !ok {
			t.Fatalf("instance has type %T", instances[0])
		}
		if len(current.unavailable) != 1 || current.unavailable[0].Code != "eas/discovery-limit" {
			t.Fatalf("unavailable = %+v, want discovery limit", current.unavailable)
		}
	})

	t.Run("known project remains unresolved", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeFile(t, root, "eas.json", `{}`)
		writeFile(t, root, "ordinary", "content")
		p := New(nil)
		p.lookupEnv = noEnvironment
		p.maxScanEntries = 2
		instances, err := p.Detect(context.Background(), provider.Repository{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		if len(instances) != 1 || instances[0].Descriptor().ID != rootInstanceID {
			t.Fatalf("Detect() = %+v, want root project", instances)
		}
		current, ok := instances[0].(easInstance)
		if !ok {
			t.Fatalf("instance has type %T", instances[0])
		}
		if len(current.unavailable) != 2 || combinedUnavailable(current.unavailable).Code != "eas/multiple-uncertainties" {
			t.Fatalf("unavailable = %+v, want Git and discovery uncertainty", current.unavailable)
		}
	})
}

func TestProviderDetectUnavailableModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		setup     func(*testing.T, string)
		environ   func(string) (string, bool)
		wantText  string
		wantError string
	}{
		{name: "not a Git worktree", wantText: "requires a Git worktree"},
		{name: "oversized root easignore", setup: func(t *testing.T, root string) {
			makeDir(t, root, ".git")
			writeFile(t, root, ".easignore", strings.Repeat("a", maxConfigSize+1))
		}, wantText: "cannot read .easignore safely"},
		{name: "require commit", setup: func(t *testing.T, root string) {
			makeDir(t, root, ".git")
			writeFile(t, root, "eas.json", `{"cli":{"requireCommit":true}}`)
		}, wantText: "cli.requireCommit"},
		{name: "no VCS environment", setup: func(t *testing.T, root string) {
			makeDir(t, root, ".git")
		}, environ: func(name string) (string, bool) {
			if name == "EAS_NO_VCS" {
				return "1", true
			}
			return "", false
		}, wantText: "EAS_NO_VCS"},
		{name: "oversized root gitignore", setup: func(t *testing.T, root string) {
			makeDir(t, root, ".git")
			writeFile(t, root, ".gitignore", strings.Repeat("a", maxConfigSize+1))
		}, wantText: "file exceeds"},
		{name: "symlink gitignore", setup: func(t *testing.T, root string) {
			makeDir(t, root, ".git")
			makeDir(t, root, "mobile")
			writeFile(t, root, "rules", "ignored\n")
			if err := os.Symlink("../rules", filepath.Join(root, "mobile", ".gitignore")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		}, wantText: "symlink whose EAS and Git semantics can differ"},
		{name: "escaping eas config symlink", setup: func(t *testing.T, root string) {
			makeDir(t, root, ".git")
			outside := filepath.Join(t.TempDir(), "eas.json")
			if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(root, "eas.json")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "eas.json")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		}, wantText: "cannot read eas.json safely"},
		{name: "malformed config", setup: func(t *testing.T, root string) {
			makeDir(t, root, ".git")
			writeFile(t, root, "eas.json", "{")
		}, wantError: "parse eas.json"},
		{name: "multiple JSON values", setup: func(t *testing.T, root string) {
			makeDir(t, root, ".git")
			writeFile(t, root, "eas.json", "{} {}")
		}, wantError: "multiple JSON values"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, "eas.json", `{}`)
			if test.setup != nil {
				test.setup(t, root)
			}
			p := New(nil)
			p.lookupEnv = test.environ
			if p.lookupEnv == nil {
				p.lookupEnv = noEnvironment
			}
			instances, err := p.Detect(context.Background(), provider.Repository{Root: root})
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("Detect() error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, ok := instances[0].(easInstance)
			if !ok {
				t.Fatalf("instance has type %T", instances[0])
			}
			if len(got.unavailable) == 0 || !strings.Contains(combinedUnavailable(got.unavailable).Summary+" "+combinedUnavailable(got.unavailable).Evidence, test.wantText) {
				t.Fatalf("unavailable = %+v, want containing %q", got.unavailable, test.wantText)
			}
		})
	}
}

func TestParseConfigAcceptsEASJSON5(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "standard JSON", content: `{"cli":{"requireCommit":true}}`, want: true},
		{name: "comments and trailing comma", content: "{/* comment */ cli: {requireCommit: true,},}", want: true},
		{name: "single quoted key", content: `{'cli': {'requireCommit': true}}`, want: true},
		{name: "false value", content: `{cli: {requireCommit: false}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config, err := parseConfig([]byte(test.content))
			if err != nil {
				t.Fatal(err)
			}
			if config.CLI.RequireCommit != test.want {
				t.Fatalf("RequireCommit = %v, want %v", config.CLI.RequireCommit, test.want)
			}
		})
	}
}

func TestProviderRootEASIgnore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	makeDir(t, root, ".git")
	writeFile(t, root, "eas.json", `{}`)
	writeFile(t, root, ".easignore", "docs/**\n!docs/keep.md\n")
	writeFile(t, root, "docs/drop.md", "drop")
	writeFile(t, root, "docs/keep.md", "keep")

	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte("docs/drop.md\x00")}}
	p := New(runner)
	p.lookupEnv = noEnvironment
	instances, err := p.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	instance, ok := instances[0].(easInstance)
	if !ok {
		t.Fatalf("instance has type %T", instances[0])
	}
	if len(instance.unavailable) != 0 || instance.ignoreFile != ".easignore" || instance.perDirectory {
		t.Fatalf("instance = %+v, want supported root .easignore", instance)
	}
	results, err := p.Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{Root: root},
		instance,
		[]scope.Path{"docs/drop.md", "docs/keep.md"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != scope.Out || results[0].Explanation.Code != "eas/easignore-excluded" {
		t.Fatalf("drop result = %+v", results[0])
	}
	if results[1].State != scope.In || results[1].Explanation.Code != "eas/included" {
		t.Fatalf("keep result = %+v", results[1])
	}
	if !contains(runner.requests[0].Args, "--exclude-from=.easignore") || contains(runner.requests[0].Args, "--exclude-per-directory=.gitignore") {
		t.Fatalf("request args = %q", runner.requests[0].Args)
	}
}

func TestProviderDetectBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		repo      provider.Repository
		setup     func(*testing.T, string)
		ctx       func() context.Context
		wantCount int
		wantText  string
	}{
		{name: "empty root", repo: provider.Repository{}, wantText: "repository root is empty"},
		{name: "missing root", repo: provider.Repository{Root: filepath.Join(t.TempDir(), "missing")}, wantText: "open repository root"},
		{name: "no config", setup: func(t *testing.T, root string) { makeDir(t, root, ".git") }},
		{name: "cancelled", ctx: cancelledContext, wantText: "context canceled"},
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
			p := New(nil)
			p.lookupEnv = noEnvironment
			instances, err := p.Detect(ctx, repo)
			if test.wantText != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantText) {
					t.Fatalf("Detect() error = %v, want containing %q", err, test.wantText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(instances) != test.wantCount {
				t.Fatalf("Detect() returned %d instances, want %d", len(instances), test.wantCount)
			}
		})
	}
}

func TestProviderEvaluateDecisionTable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "included.txt", "included")
	writeFile(t, root, "ignored.txt", "ignored")
	writeFile(t, root, "mobile/.easignore", "*.md\n")
	writeFile(t, root, "node_modules/pkg/index.js", "dependency")

	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte("ignored.txt\x00")}}
	p := New(runner)
	instance := easInstance{
		descriptor:        provider.InstanceDescriptor{Provider: "eas", ID: "mobile"},
		inactiveEASIgnore: []scope.Path{"mobile/.easignore"},
		ignoreFile:        ".gitignore",
		perDirectory:      true,
	}
	paths := []scope.Path{"included.txt", "ignored.txt", "missing.txt", "node_modules/pkg/index.js"}
	results, err := p.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instance, paths)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		state scope.State
		code  string
		text  string
	}{
		{scope.In, "eas/included", "mobile/.easignore is inactive"},
		{scope.Out, "eas/gitignore-excluded", "active .gitignore rules"},
		{scope.Out, "eas/not-present", "not present"},
		{scope.Out, "eas/default-excluded", "default node_modules rule"},
	}
	for index, result := range results {
		if result.State != want[index].state || result.Explanation.Code != want[index].code || !strings.Contains(result.Explanation.Summary, want[index].text) {
			t.Errorf("result[%d] = %+v, want %s %s containing %q", index, result, want[index].state, want[index].code, want[index].text)
		}
		if result.Provenance != (scope.Provenance{Method: "safe-native", Tool: "git", Reference: modeledEASReference}) {
			t.Errorf("result[%d] provenance = %+v", index, result.Provenance)
		}
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.requests))
	}
	request := runner.requests[0]
	if request.Tool != "git" || len(request.Stdin) != 0 {
		t.Fatalf("request = %+v", request)
	}
	for _, argument := range []string{"--literal-pathspecs", "core.ignorecase=true", "--exclude-per-directory=.gitignore", "--", "included.txt", "ignored.txt", "missing.txt"} {
		if !contains(request.Args, argument) {
			t.Errorf("request args %q do not contain %q", request.Args, argument)
		}
	}
	if !contains(request.UnsetEnv, "GIT_TRACE2_EVENT") {
		t.Errorf("request does not sanitize Git tracing environment: %q", request.UnsetEnv)
	}
	if request.Env["GIT_CONFIG_GLOBAL"] != os.DevNull || request.Env["GIT_CONFIG_NOSYSTEM"] != "1" {
		t.Errorf("request does not isolate EAS matching from user Git configuration: %q", request.Env)
	}
}

func TestProviderEvaluateScopesNestedNegationUncertainty(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "outside.txt", "outside")
	writeFile(t, root, "mobile/included.txt", "ambiguous")
	writeFile(t, root, "mobile/excluded.txt", "excluded")
	runner := &fakeRunner{response: safeexec.Response{Stdout: []byte("mobile/excluded.txt\x00")}}
	instance := easInstance{
		descriptor:        provider.InstanceDescriptor{Provider: "eas", ID: "mobile"},
		ambiguousNegation: []scope.Path{"mobile/.gitignore"},
		ignoreFile:        ".gitignore",
		perDirectory:      true,
	}
	results, err := New(runner).Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{Root: root},
		instance,
		[]scope.Path{"outside.txt", "mobile/included.txt", "mobile/excluded.txt"},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantStates := []scope.State{scope.In, scope.Unknown, scope.Out}
	wantCodes := []string{"eas/included", "eas/nested-negation-ambiguous", "eas/gitignore-excluded"}
	for index, result := range results {
		if result.State != wantStates[index] || result.Explanation.Code != wantCodes[index] {
			t.Errorf("result[%d] = %+v, want %s %s", index, result, wantStates[index], wantCodes[index])
		}
	}
}

func TestGitPathBatches(t *testing.T) {
	t.Parallel()
	paths := make([]scope.Path, maxGitBatchPaths+1)
	for index := range paths {
		paths[index] = scope.Path(strings.Repeat("a", 48) + string(rune('a'+index%26)))
	}
	batches := gitPathBatches(paths)
	if len(batches) != 2 {
		t.Fatalf("gitPathBatches() returned %d batches, want 2", len(batches))
	}
	for _, batch := range batches {
		if len(batch) > maxGitBatchPaths {
			t.Errorf("batch has %d paths, maximum is %d", len(batch), maxGitBatchPaths)
		}
		bytes := 0
		for _, name := range batch {
			bytes += len(name) + 1
		}
		if len(batch) > 1 && bytes > maxGitBatchBytes {
			t.Errorf("batch has %d bytes, maximum is %d", bytes, maxGitBatchBytes)
		}
	}
	long := scope.Path(strings.Repeat("x", maxGitBatchBytes+1))
	if got := gitPathBatches([]scope.Path{long}); len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("oversized single path batching = %+v", got)
	}
}

func TestIgnoredPathsUsesBoundedBatches(t *testing.T) {
	t.Parallel()
	paths := make([]scope.Path, maxGitBatchPaths+1)
	for index := range paths {
		paths[index] = scope.Path("path-" + strconv.Itoa(index))
	}
	runner := &fakeRunner{}
	ignored, err := New(runner).ignoredPaths(context.Background(), t.TempDir(), easInstance{ignoreFile: ".gitignore", perDirectory: true}, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 0 || len(runner.requests) != 2 {
		t.Fatalf("ignored=%v runner calls=%d, want empty and 2", ignored, len(runner.requests))
	}
}

func TestIgnoredPathsSkipsDefaultExclusions(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{err: errors.New("must not run")}
	ignored, err := New(runner).ignoredPaths(context.Background(), t.TempDir(), easInstance{ignoreFile: ".gitignore", perDirectory: true}, []scope.Path{"node_modules/a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 0 || len(runner.requests) != 0 {
		t.Fatalf("ignored=%v runner calls=%d, want empty and 0", ignored, len(runner.requests))
	}
}

func TestProviderEvaluateUnknownDoesNotExecute(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{err: errors.New("must not run")}
	instance := easInstance{
		descriptor:  provider.InstanceDescriptor{Provider: "eas", ID: "mobile"},
		unavailable: []scope.Explanation{{Code: "eas/test-unavailable", Summary: "safe evaluation unavailable"}},
	}
	results, err := New(runner).Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{},
		instance,
		[]scope.Path{"a", "b"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("runner calls = %d, want 0", len(runner.requests))
	}
	for _, result := range results {
		if result.State != scope.Unknown || result.Explanation.Code != "eas/test-unavailable" || result.Provenance != (scope.Provenance{Method: scope.Unavailable, Reference: modeledEASReference}) {
			t.Fatalf("result = %+v, want UNKNOWN unavailable", result)
		}
	}
}

func TestInstanceIDCannotCollideWithRoot(t *testing.T) {
	t.Parallel()

	if got := instanceID("eas/eas.json"); got != "project/eas" {
		t.Fatalf("instanceID() = %q, want project/eas", got)
	}
	if got := instanceID("eas.json"); got != rootInstanceID {
		t.Fatalf("instanceID() = %q, want %q", got, rootInstanceID)
	}
}

func TestCombinedUnavailablePreservesReasons(t *testing.T) {
	t.Parallel()

	reasons := []scope.Explanation{
		{Code: "eas/first", Summary: "first reason"},
		{Code: "eas/second", Summary: "second reason", Evidence: "detail"},
	}
	got := combinedUnavailable(reasons)
	if got.Code != "eas/multiple-uncertainties" || !strings.Contains(got.Evidence, "eas/first: first reason") || !strings.Contains(got.Evidence, "eas/second: second reason: detail") {
		t.Fatalf("combinedUnavailable() = %+v", got)
	}
}

func TestProviderEvaluateUnavailableToolReturnsUnknown(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	makeDir(t, root, ".git")
	writeFile(t, root, "eas.json", `{}`)
	unavailable := &safeexec.UnavailableError{
		Code:     "tool/not-approved",
		Summary:  "tool \"git\" is not approved",
		Evidence: "resolved executable \"/usr/bin/git\" has sha256 abc",
		Action:   "run awareof --setup",
	}
	p := New(&fakeRunner{err: unavailable})
	instances, err := p.Detect(context.Background(), provider.Repository{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	results, err := p.Evaluate(context.Background(), provider.EvaluationContext{}, provider.Repository{Root: root}, instances[0], []scope.Path{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.State != scope.Unknown || result.Explanation.Code != unavailable.Code || result.Explanation.Action != unavailable.Action || result.Provenance.Method != scope.Unavailable {
			t.Errorf("result = %+v, want UNKNOWN unavailable explanation", result)
		}
	}
}

func TestProviderEvaluateErrors(t *testing.T) {
	t.Parallel()
	valid := easInstance{descriptor: provider.InstanceDescriptor{Provider: "eas", ID: "eas"}}
	tests := []struct {
		name      string
		provider  *Provider
		instance  provider.Instance
		repo      provider.Repository
		paths     []scope.Path
		ctx       func() context.Context
		wantError string
	}{
		{name: "wrong type", provider: New(nil), instance: provider.InstanceDescriptor{Provider: "eas", ID: "eas"}, paths: []scope.Path{"a"}, wantError: "unsupported EAS instance"},
		{name: "wrong provider", provider: New(nil), instance: easInstance{descriptor: provider.InstanceDescriptor{Provider: "git", ID: "eas"}}, paths: []scope.Path{"a"}, wantError: "unsupported instance"},
		{name: "empty ID", provider: New(nil), instance: easInstance{descriptor: provider.InstanceDescriptor{Provider: "eas"}}, paths: []scope.Path{"a"}, wantError: "unsupported instance"},
		{name: "cancelled", provider: New(nil), instance: valid, paths: []scope.Path{"a"}, ctx: cancelledContext, wantError: "context canceled"},
		{name: "nil runner", provider: New(nil), instance: valid, paths: []scope.Path{"a"}, wantError: "command runner is nil"},
		{name: "empty root", provider: New(&fakeRunner{}), instance: valid, paths: []scope.Path{"a"}, wantError: "repository root is empty"},
		{name: "runner error", provider: New(&fakeRunner{err: errors.New("unsafe executable")}), instance: valid, repo: provider.Repository{Root: t.TempDir()}, paths: []scope.Path{"a"}, wantError: "unsafe executable"},
		{name: "Git error", provider: New(&fakeRunner{response: safeexec.Response{ExitCode: 3, Stderr: []byte("bad repo")}}), instance: valid, repo: provider.Repository{Root: t.TempDir()}, paths: []scope.Path{"a"}, wantError: "status 3: bad repo"},
		{name: "unexpected output", provider: New(&fakeRunner{response: safeexec.Response{Stdout: []byte("other\x00")}}), instance: valid, repo: provider.Repository{Root: t.TempDir()}, paths: []scope.Path{"a"}, wantError: "unexpected path"},
		{name: "duplicate output", provider: New(&fakeRunner{response: safeexec.Response{Stdout: []byte("a\x00a\x00")}}), instance: valid, repo: provider.Repository{Root: t.TempDir()}, paths: []scope.Path{"a"}, wantError: "duplicate path"},
		{name: "invalid UTF-8", provider: New(&fakeRunner{response: safeexec.Response{Stdout: []byte{'a', 0xff, 0}}}), instance: valid, repo: provider.Repository{Root: t.TempDir()}, paths: []scope.Path{"a"}, wantError: "invalid UTF-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			if test.ctx != nil {
				ctx = test.ctx()
			}
			_, err := test.provider.Evaluate(ctx, provider.EvaluationContext{}, test.repo, test.instance, test.paths)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Evaluate() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestProviderEvaluateEmptyPaths(t *testing.T) {
	t.Parallel()
	results, err := New(nil).Evaluate(
		context.Background(),
		provider.EvaluationContext{},
		provider.Repository{},
		easInstance{descriptor: provider.InstanceDescriptor{Provider: "eas", ID: "eas"}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("Evaluate() = %+v, want non-nil empty", results)
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	if !hasPotentialNegation([]byte("\ufeff# comment\n !keep.js\n")) {
		t.Fatal("hasPotentialNegation() = false, want true")
	}
	if hasPotentialNegation([]byte("# comment\n\\!literal\n")) {
		t.Fatal("hasPotentialNegation() = true for escaped literal")
	}
	if got := inactiveIgnoreNote([]scope.Path{"b/.easignore", "a/.easignore"}); !strings.Contains(got, "2 nested") {
		t.Fatalf("inactiveIgnoreNote() = %q", got)
	}
	if got := inactiveIgnoreNote(nil); got != "" {
		t.Fatalf("inactiveIgnoreNote(nil) = %q, want empty", got)
	}
	if !isNodeModules("packages/a/node_modules/pkg/index.js") || isNodeModules("node_modules.txt") {
		t.Fatal("isNodeModules() segment handling is incorrect")
	}
	if source, ok := applicableNegation("mobile/src/a.ts", []scope.Path{"mobile/.gitignore"}); !ok || source != "mobile/.gitignore" {
		t.Fatalf("applicableNegation() = (%q, %v)", source, ok)
	}
	if _, ok := applicableNegation("mobile-web/a.ts", []scope.Path{"mobile/.gitignore"}); ok {
		t.Fatal("applicableNegation() matched a sibling prefix")
	}
}

func TestCommandErrorWithoutStderr(t *testing.T) {
	t.Parallel()
	if got := commandError(safeexec.Response{ExitCode: 7}).Error(); !strings.Contains(got, "status 7") {
		t.Fatalf("commandError() = %q", got)
	}
}

type fakeRunner struct {
	response safeexec.Response
	err      error
	requests []safeexec.Request
}

func (r *fakeRunner) Run(_ context.Context, request safeexec.Request) (safeexec.Response, error) {
	r.requests = append(r.requests, request)
	return r.response, r.err
}

func noEnvironment(string) (string, bool) { return "", false }

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func makeDir(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(name)), 0o750); err != nil {
		t.Fatal(err)
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
