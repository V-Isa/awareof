package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/V-Isa/awareof/internal/contract"
	"github.com/V-Isa/awareof/internal/pathset"
	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/providers/git"
	"github.com/V-Isa/awareof/internal/providers/npm"
	"github.com/V-Isa/awareof/internal/providers/prettier"
	"github.com/V-Isa/awareof/internal/providers/typescript"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

func TestParseOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		args      []string
		want      options
		wantError string
	}{
		{name: "paths only", args: []string{"validate", "src"}, want: options{paths: []string{"validate", "src"}}},
		{name: "flags before and after paths", args: []string{"a", "--json", "b", "-0", "--root", "repo"}, want: options{json: true, nullInput: true, root: "repo", paths: []string{"a", "b"}}},
		{name: "root equals", args: []string{"--root=repo", "a"}, want: options{root: "repo", paths: []string{"a"}}},
		{name: "root short", args: []string{"-C", "repo", "a"}, want: options{root: "repo", paths: []string{"a"}}},
		{name: "validate", args: []string{"--validate"}, want: options{validate: true}},
		{name: "staged", args: []string{"--staged"}, want: options{staged: true}},
		{name: "changed from", args: []string{"--changed-from", "main"}, want: options{changedFrom: "main"}},
		{name: "changed from equals", args: []string{"--changed-from=HEAD~1"}, want: options{changedFrom: "HEAD~1"}},
		{name: "tool status", args: []string{"--tools"}, want: options{tools: true}},
		{name: "setup", args: []string{"--setup"}, want: options{setup: true}},
		{name: "tool override", args: []string{"--tool", "git=/usr/bin/git"}, want: options{toolPaths: map[safeexec.ToolID]string{"git": "/usr/bin/git"}}},
		{name: "tool override equals", args: []string{"--tool=git=/usr/bin/git"}, want: options{toolPaths: map[safeexec.ToolID]string{"git": "/usr/bin/git"}}},
		{name: "approve tools", args: []string{"--approve-tool", "git", "--approve-tool=typescript"}, want: options{approve: []safeexec.ToolID{"git", "typescript"}}},
		{name: "revoke tools", args: []string{"--revoke-tool", "git", "--revoke-tool=typescript"}, want: options{revoke: []safeexec.ToolID{"git", "typescript"}}},
		{name: "quiet", args: []string{"-q", "--quiet"}, want: options{quiet: true}},
		{name: "literal", args: []string{"--literal", "name[1].go"}, want: options{literal: true, paths: []string{"name[1].go"}}},
		{name: "help", args: []string{"-h"}, want: options{help: true}},
		{name: "long help", args: []string{"--help"}, want: options{help: true}},
		{name: "version", args: []string{"--version"}, want: options{version: true}},
		{name: "stdin is positional", args: []string{"-"}, want: options{paths: []string{"-"}}},
		{name: "double dash", args: []string{"--", "--validate", "-x"}, want: options{paths: []string{"--validate", "-x"}}},
		{name: "unknown short option", args: []string{"-x"}, wantError: "unknown option"},
		{name: "unknown long option", args: []string{"--unknown"}, wantError: "unknown option"},
		{name: "missing root value", args: []string{"--root"}, wantError: "missing value"},
		{name: "missing short root value", args: []string{"-C"}, wantError: "missing value"},
		{name: "empty separate root value", args: []string{"--root", ""}, wantError: "missing value"},
		{name: "empty root equals", args: []string{"--root="}, wantError: "missing value"},
		{name: "missing changed from", args: []string{"--changed-from"}, wantError: "missing value"},
		{name: "empty changed from", args: []string{"--changed-from="}, wantError: "missing value"},
		{name: "duplicate changed from", args: []string{"--changed-from=main", "--changed-from", "HEAD"}, wantError: "only once"},
		{name: "missing tool override", args: []string{"--tool"}, wantError: "missing value"},
		{name: "malformed tool override", args: []string{"--tool", "git"}, wantError: "ID=PATH"},
		{name: "empty tool id", args: []string{"--tool", "=/git"}, wantError: "ID=PATH"},
		{name: "empty tool path", args: []string{"--tool=git="}, wantError: "ID=PATH"},
		{name: "duplicate tool override", args: []string{"--tool=git=/one", "--tool=git=/two"}, wantError: "selected more than once"},
		{name: "missing approve tool", args: []string{"--approve-tool"}, wantError: "missing value"},
		{name: "empty approve tool", args: []string{"--approve-tool="}, wantError: "missing value"},
		{name: "duplicate approve tool", args: []string{"--approve-tool=git", "--approve-tool=git"}, wantError: "listed more than once"},
		{name: "missing revoke tool", args: []string{"--revoke-tool"}, wantError: "missing value"},
		{name: "empty revoke tool", args: []string{"--revoke-tool="}, wantError: "missing value"},
		{name: "duplicate revoke tool", args: []string{"--revoke-tool=git", "--revoke-tool=git"}, wantError: "listed more than once"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseOptions(test.args)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("parseOptions() error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseOptions() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestRunChangeSources(t *testing.T) {
	t.Parallel()
	base := func(source changeSource) dependencies {
		return dependencies{
			getwd:       func() (string, error) { return "/work/sub", nil },
			resolveRoot: func(string, string) (string, error) { return "/work", nil },
			buildPaths: func(context.Context, pathset.Builder, []string, io.Reader, pathset.BuildOptions) ([]scope.Path, error) {
				return nil, errors.New("positional path builder must not run")
			},
			loadContract: func(string, []scope.ProviderID) (contract.Contract, error) {
				return contract.Contract{File: ".awareof.yaml"}, nil
			},
			changes: source,
		}
	}

	t.Run("staged paths", func(t *testing.T) {
		t.Parallel()
		source := &fakeChangeSource{stagedPaths: []scope.Path{"a.go", "deleted.go"}}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--staged", "--json"}, strings.NewReader(""), &stdout, &stderr, base(source))
		if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"paths": [`) || !strings.Contains(stdout.String(), `"deleted.go"`) {
			t.Fatalf("run() = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
		}
		if source.stagedCalls != 1 || source.changedCalls != 0 || source.root != "/work" {
			t.Fatalf("source = %+v, want one staged call", source)
		}
	})

	t.Run("changed paths", func(t *testing.T) {
		t.Parallel()
		source := &fakeChangeSource{changedPaths: []scope.Path{"src/a.go"}}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--changed-from", "main", "--json"}, strings.NewReader(""), &stdout, &stderr, base(source))
		if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"src/a.go"`) {
			t.Fatalf("run() = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
		}
		if source.changedCalls != 1 || source.revision != "main" || source.root != "/work" {
			t.Fatalf("source = %+v, want changed-from main", source)
		}
	})

	t.Run("empty validation source", func(t *testing.T) {
		t.Parallel()
		source := &fakeChangeSource{stagedPaths: []scope.Path{}}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--validate", "--staged"}, strings.NewReader(""), &stdout, &stderr, base(source))
		if code != 0 || stdout.String() != "Validation passed: no paths matched contract rules.\n" || stderr.Len() != 0 {
			t.Fatalf("run() = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
		}
	})

	t.Run("unapproved tool", func(t *testing.T) {
		t.Parallel()
		source := &fakeChangeSource{stagedErr: &safeexec.UnavailableError{
			Code: "tool/not-approved", Summary: "tool is not approved", Evidence: "git path", Action: "approve git",
		}}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--staged"}, strings.NewReader(""), &stdout, &stderr, base(source))
		for _, want := range []string{"ERROR [tool/not-approved]", "evidence: git path", "action: approve git"} {
			if code != 2 || !strings.Contains(stderr.String(), want) {
				t.Fatalf("run() = %d, stderr=%q, want %q", code, stderr.String(), want)
			}
		}
	})

	t.Run("source failure", func(t *testing.T) {
		t.Parallel()
		source := &fakeChangeSource{changedErr: errors.New("git failed")}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--changed-from=main"}, strings.NewReader(""), &stdout, &stderr, base(source))
		if code != 2 || !strings.Contains(stderr.String(), "ERROR [pathset/source]") || !strings.Contains(stderr.String(), "git failed") {
			t.Fatalf("run() = %d, stderr=%q", code, stderr.String())
		}
	})

	t.Run("missing source", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--staged"}, strings.NewReader(""), &stdout, &stderr, base(nil))
		if code != 2 || !strings.Contains(stderr.String(), "git change path source is unavailable") {
			t.Fatalf("run() = %d, stderr=%q", code, stderr.String())
		}
	})
}

func TestRunRejectsConflictingPathSources(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "both git selectors", args: []string{"--staged", "--changed-from", "main"}, want: "cannot be combined"},
		{name: "staged and path", args: []string{"--staged", "src"}, want: "positional paths"},
		{name: "changed and stdin", args: []string{"--changed-from", "main", "-"}, want: "positional paths"},
		{name: "staged and null input", args: []string{"--staged", "--null-input"}, want: "requires the stdin"},
		{name: "staged and literal", args: []string{"--staged", "--literal"}, want: "requires positional paths"},
		{name: "literal without path", args: []string{"--literal"}, want: "requires at least one positional path"},
		{name: "tool management and staged", args: []string{"--tools", "--staged"}, want: "tool management cannot"},
		{name: "tool management and literal", args: []string{"--tools", "--literal"}, want: "tool management cannot"},
		{name: "setup and path", args: []string{"--setup", "src"}, want: "tool management cannot"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), test.args, strings.NewReader(""), &stdout, &stderr, dependencies{})
			if code != 2 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("run() = %d, stderr=%q, want containing %q", code, stderr.String(), test.want)
			}
		})
	}
}

func TestRunPassesPathBuildOptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var gotContext context.Context
	var gotInputs []string
	var gotOptions pathset.BuildOptions
	deps := dependencies{
		getwd:       func() (string, error) { return "/work", nil },
		resolveRoot: func(string, string) (string, error) { return "/work", nil },
		buildPaths: func(
			buildContext context.Context,
			_ pathset.Builder,
			inputs []string,
			_ io.Reader,
			options pathset.BuildOptions,
		) ([]scope.Path, error) {
			gotContext = buildContext
			gotInputs = inputs
			gotOptions = options
			return []scope.Path{"name[1].go"}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"--literal", "name[1].go"}, strings.NewReader(""), &stdout, &stderr, deps)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("run() = %d, stderr=%q", code, stderr.String())
	}
	if gotContext != ctx || !reflect.DeepEqual(gotInputs, []string{"name[1].go"}) {
		t.Fatalf("buildPaths context/inputs = %v/%v", gotContext, gotInputs)
	}
	if gotOptions != (pathset.BuildOptions{Literal: true}) {
		t.Fatalf("buildPaths options = %+v, want literal", gotOptions)
	}
}

func TestRunControlFlow(t *testing.T) {
	base := func() dependencies {
		return dependencies{
			getwd:       func() (string, error) { return "/work", nil },
			resolveRoot: func(string, string) (string, error) { return "/work", nil },
			buildPaths: func(context.Context, pathset.Builder, []string, io.Reader, pathset.BuildOptions) ([]scope.Path, error) {
				return []scope.Path{"a"}, nil
			},
			loadContract: func(string, []scope.ProviderID) (contract.Contract, error) {
				return contract.Contract{}, nil
			},
		}
	}
	tests := []struct {
		name       string
		args       []string
		configure  func(*dependencies)
		stdout     io.Writer
		wantCode   int
		wantOut    string
		wantErrHas string
	}{
		{name: "unknown option", args: []string{"--bad"}, wantCode: 2, wantErrHas: "ERROR [cli/usage]"},
		{name: "quiet inspection", args: []string{"--quiet", "a"}, wantCode: 2, wantErrHas: "--quiet requires --validate"},
		{name: "missing path", args: nil, wantCode: 2, wantErrHas: "missing path or pattern"},
		{name: "getwd error", args: []string{"a"}, configure: func(d *dependencies) { d.getwd = func() (string, error) { return "", errors.New("cwd failed") } }, wantCode: 2, wantErrHas: "[repository/working-directory]"},
		{name: "root error", args: []string{"a"}, configure: func(d *dependencies) {
			d.resolveRoot = func(string, string) (string, error) { return "", errors.New("root failed") }
		}, wantCode: 2, wantErrHas: "[repository/root]"},
		{name: "path error", args: []string{"a"}, configure: func(d *dependencies) {
			d.buildPaths = func(context.Context, pathset.Builder, []string, io.Reader, pathset.BuildOptions) ([]scope.Path, error) {
				return nil, errors.New("path failed")
			}
		}, wantCode: 2, wantErrHas: "[pathset/build]"},
		{name: "provider error", args: []string{"a"}, configure: func(d *dependencies) {
			d.providers = []provider.Provider{&appProvider{detectErr: errors.New("detect failed")}}
		}, wantCode: 2, wantErrHas: "detect provider"},
		{name: "human success", args: []string{"a"}, wantCode: 0, wantOut: "No providers detected.\n"},
		{name: "json success", args: []string{"a", "--json"}, wantCode: 0, wantOut: "{\n  \"schemaVersion\": 1,\n  \"paths\": [\n    \"a\"\n  ],\n  \"results\": []\n}\n"},
		{name: "render error", args: []string{"a"}, stdout: appErrorWriter{}, wantCode: 2, wantErrHas: "write human output"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := base()
			if test.configure != nil {
				test.configure(&deps)
			}
			var stdout bytes.Buffer
			output := test.stdout
			if output == nil {
				output = &stdout
			}
			var stderr bytes.Buffer
			code := run(context.Background(), test.args, strings.NewReader(""), output, &stderr, deps)
			if code != test.wantCode {
				t.Fatalf("run() code = %d, want %d; stderr=%q", code, test.wantCode, stderr.String())
			}
			if stdout.String() != test.wantOut {
				t.Fatalf("run() stdout = %q, want %q", stdout.String(), test.wantOut)
			}
			if test.wantErrHas != "" && !strings.Contains(stderr.String(), test.wantErrHas) {
				t.Fatalf("run() stderr = %q, want containing %q", stderr.String(), test.wantErrHas)
			}
		})
	}
}

func TestRunValidation(t *testing.T) {
	base := func() dependencies {
		return dependencies{
			getwd:       func() (string, error) { return "/work", nil },
			resolveRoot: func(string, string) (string, error) { return "/work", nil },
			buildPaths: func(_ context.Context, _ pathset.Builder, inputs []string, _ io.Reader, _ pathset.BuildOptions) ([]scope.Path, error) {
				if reflect.DeepEqual(inputs, []string{"**"}) {
					return []scope.Path{"a"}, nil
				}
				return []scope.Path{"a"}, nil
			},
			loadContract: func(string, []scope.ProviderID) (contract.Contract, error) {
				return contract.Contract{File: ".awareof.yaml", Rules: []contract.Rule{{
					Pattern: "a", Assertions: map[scope.ProviderID]scope.State{"test": scope.In},
				}}}, nil
			},
			providers: []provider.Provider{&validationProvider{state: scope.In}},
		}
	}
	tests := []struct {
		name       string
		args       []string
		configure  func(*dependencies)
		stdout     io.Writer
		wantCode   int
		wantOut    string
		wantErrHas string
	}{
		{name: "success", args: []string{"--validate"}, wantOut: "Validation passed: 1 assertions satisfied.\n"},
		{name: "unasserted provider is not evaluated", args: []string{"--validate"}, configure: func(d *dependencies) {
			d.providers = append(d.providers, &unusedProvider{})
		}, wantOut: "Validation passed: 1 assertions satisfied.\n"},
		{name: "success json", args: []string{"--validate", "--json"}, wantOut: "json-valid"},
		{name: "mismatch", args: []string{"--validate"}, configure: func(d *dependencies) {
			d.providers = []provider.Provider{&validationProvider{state: scope.Out}}
		}, wantCode: 1, wantOut: "Validation failed.\na\n  test expected IN, actual OUT (rule \"a\")\n    test: test explanation\n"},
		{name: "unknown is unresolved", args: []string{"--validate"}, configure: func(d *dependencies) {
			d.providers = []provider.Provider{&validationProvider{state: scope.Unknown}}
		}, wantCode: 1, wantOut: "Validation failed.\na\n  test expected IN, actual UNKNOWN (rule \"a\")\n    test: test explanation\n"},
		{name: "quiet mismatch", args: []string{"--validate", "--quiet"}, configure: func(d *dependencies) {
			d.providers = []provider.Provider{&validationProvider{state: scope.Out}}
		}, wantCode: 1},
		{name: "no matching paths", args: []string{"--validate"}, configure: func(d *dependencies) {
			d.loadContract = func(string, []scope.ProviderID) (contract.Contract, error) {
				return contract.Contract{File: ".awareof.yaml", Rules: []contract.Rule{{Pattern: "b"}}}, nil
			}
		}, wantOut: "Validation passed: no paths matched contract rules.\n"},
		{name: "contract load error", args: []string{"--validate"}, configure: func(d *dependencies) {
			d.loadContract = func(string, []scope.ProviderID) (contract.Contract, error) {
				return contract.Contract{}, errors.New("bad contract")
			}
		}, wantCode: 2, wantErrHas: "[contract/load]"},
		{name: "invalid provider registry", args: []string{"--validate"}, configure: func(d *dependencies) {
			d.providers = []provider.Provider{nil}
		}, wantCode: 2, wantErrHas: "provider is nil"},
		{name: "contract match error", args: []string{"--validate"}, configure: func(d *dependencies) {
			d.loadContract = func(string, []scope.ProviderID) (contract.Contract, error) {
				return contract.Contract{Rules: []contract.Rule{{Pattern: "["}}}, nil
			}
		}, wantCode: 2, wantErrHas: "[contract/path-selection]"},
		{name: "ambiguous assertions", args: []string{"--validate"}, configure: func(d *dependencies) {
			d.loadContract = func(string, []scope.ProviderID) (contract.Contract, error) {
				return contract.Contract{Rules: []contract.Rule{
					{Pattern: "*", Assertions: map[scope.ProviderID]scope.State{"test": scope.In}},
					{Pattern: "?", Assertions: map[scope.ProviderID]scope.State{"test": scope.Out}},
				}}, nil
			}
		}, wantCode: 2, wantErrHas: "ambiguous"},
		{name: "render error", args: []string{"--validate"}, stdout: appErrorWriter{}, wantCode: 2, wantErrHas: "write validation output"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := base()
			if test.configure != nil {
				test.configure(&deps)
			}
			var stdout bytes.Buffer
			output := test.stdout
			if output == nil {
				output = &stdout
			}
			var stderr bytes.Buffer
			code := run(context.Background(), test.args, strings.NewReader(""), output, &stderr, deps)
			if code != test.wantCode {
				t.Fatalf("run() code = %d, want %d; stderr=%q", code, test.wantCode, stderr.String())
			}
			if test.wantOut == "json-valid" {
				if !strings.Contains(stdout.String(), `"valid": true`) || !strings.Contains(stdout.String(), `"status": "SATISFIED"`) {
					t.Fatalf("run() JSON = %q", stdout.String())
				}
			} else if stdout.String() != test.wantOut {
				t.Fatalf("run() stdout = %q, want %q", stdout.String(), test.wantOut)
			}
			if test.wantErrHas != "" && !strings.Contains(stderr.String(), test.wantErrHas) {
				t.Fatalf("run() stderr = %q, want containing %q", stderr.String(), test.wantErrHas)
			}
		})
	}
}

func TestRunValidationWithoutPathsStartsAtRepositoryRoot(t *testing.T) {
	t.Parallel()
	var gotBuilder pathset.Builder
	deps := dependencies{
		getwd:       func() (string, error) { return "/work/subdirectory", nil },
		resolveRoot: func(string, string) (string, error) { return "/work", nil },
		buildPaths: func(_ context.Context, builder pathset.Builder, inputs []string, _ io.Reader, _ pathset.BuildOptions) ([]scope.Path, error) {
			gotBuilder = builder
			if !reflect.DeepEqual(inputs, []string{"**"}) {
				t.Fatalf("inputs = %v, want repository glob", inputs)
			}
			return []scope.Path{}, nil
		},
		loadContract: func(string, []scope.ProviderID) (contract.Contract, error) {
			return contract.Contract{File: ".awareof.yaml"}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--validate"}, nil, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run() = %d, stderr=%q", code, stderr.String())
	}
	if gotBuilder.Base != "/work" || gotBuilder.Root != "/work" {
		t.Fatalf("builder = %#v, want repository-root base", gotBuilder)
	}
}

func TestRunHelpAndVersion(t *testing.T) {
	oldVersion := Version
	Version = "v1.2.3"
	t.Cleanup(func() { Version = oldVersion })

	tests := []struct {
		name       string
		args       []string
		stdout     io.Writer
		wantCode   int
		wantOut    string
		wantErrHas string
	}{
		{name: "help", args: []string{"--help"}, wantOut: helpText},
		{name: "version", args: []string{"--version"}, wantOut: "awareof v1.2.3\n"},
		{name: "help write error", args: []string{"--help"}, stdout: appErrorWriter{}, wantCode: 2, wantErrHas: "write help"},
		{name: "version write error", args: []string{"--version"}, stdout: appErrorWriter{}, wantCode: 2, wantErrHas: "write version"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			output := test.stdout
			if output == nil {
				output = &stdout
			}
			var stderr bytes.Buffer
			code := run(context.Background(), test.args, nil, output, &stderr, dependencies{})
			if code != test.wantCode || stdout.String() != test.wantOut {
				t.Fatalf("run() = code %d stdout %q, want code %d stdout %q", code, stdout.String(), test.wantCode, test.wantOut)
			}
			if test.wantErrHas != "" && !strings.Contains(stderr.String(), test.wantErrHas) {
				t.Fatalf("stderr = %q, want containing %q", stderr.String(), test.wantErrHas)
			}
		})
	}
}

func TestRunPublicEntryPointOutsideExplicitRoot(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--root", root, "future.txt"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, stderr=%q", code, stderr.String())
	}
	if stdout.String() != "No providers detected.\n" {
		t.Fatalf("Run() stdout = %q", stdout.String())
	}
}

func TestRunPublicDockerProvider(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".dockerignore"), []byte("*.log\n!keep.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--root", root, "debug.log", "keep.log"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("Run() code = %d, stderr=%q", code, stderr.String())
	}
	want := "debug.log\n" +
		"  docker           OUT     excluded from Docker context by .dockerignore pattern \"*.log\"\n" +
		"keep.log\n" +
		"  docker           IN      included in Docker context by .dockerignore pattern \"!keep.log\"\n"
	if stdout.String() != want {
		t.Fatalf("Run() stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunPublicCODEOWNERSProvider(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CODEOWNERS"), []byte("src/** @team\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--root", root, "src/app.go", "legacy/app.go"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("Run() = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"src/app.go\n  codeowners       IN      @team",
		"legacy/app.go\n  codeowners       OUT     no matching rule",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("Run() stdout = %q, want containing %q", stdout.String(), want)
		}
	}
}

func TestRunPublicCODEOWNERSValidation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for name, content := range map[string]string{
		"CODEOWNERS":    "src/** @team\n",
		"src/app.go":    "package app\n",
		".awareof.yaml": "version: 1\nrules:\n  'src/**':\n    codeowners: in\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--root", root, "--validate"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stdout.String() != "Validation passed: 1 assertions satisfied.\n" || stderr.Len() != 0 {
		t.Fatalf("Run() = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestParserProvidersWorkWithoutApprovalStorage(t *testing.T) {
	t.Parallel()

	deps, err := defaultDependencies(safeexec.UnavailableApprovalStore{Cause: errors.New("config directory unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".dockerignore"), []byte("*.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "CODEOWNERS"), []byte("*.log @team\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps.getwd = func() (string, error) { return root, nil }
	deps.resolveRoot = func(string, string) (string, error) { return root, nil }
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"debug.log"}, strings.NewReader(""), &stdout, &stderr, deps)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "codeowners       IN") || !strings.Contains(stdout.String(), "docker           OUT") {
		t.Fatalf("run() = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunPublicValidation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for name, content := range map[string]string{
		".dockerignore": "*.env\n",
		".env":          "secret",
		".awareof.yaml": "version: 1\nrules:\n  '*.env':\n    docker: out\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--root", root, "--validate"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stdout.String() != "Validation passed: 1 assertions satisfied.\n" {
		t.Fatalf("Run() = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}

	if err := os.WriteFile(filepath.Join(root, ".awareof.yaml"), []byte("version: 1\nrules:\n  '*.env':\n    docker: in\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"--root", root, "--validate"}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), "docker expected IN, actual OUT") || stderr.Len() != 0 {
		t.Fatalf("Run() = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunToolManagement(t *testing.T) {
	base := func(manager toolManager) dependencies {
		return dependencies{
			getwd:       func() (string, error) { return "/work", nil },
			resolveRoot: func(string, string) (string, error) { return "/work", nil },
			buildPaths: func(context.Context, pathset.Builder, []string, io.Reader, pathset.BuildOptions) ([]scope.Path, error) {
				return []scope.Path{"a"}, nil
			},
			tools: manager,
		}
	}

	t.Run("shows deterministic statuses", func(t *testing.T) {
		manager := &fakeToolManager{statuses: []safeexec.Status{
			{Tool: "git", State: safeexec.ApprovedState, Target: safeexec.Target{Path: "/usr/bin/git", Origin: safeexec.ExternalOrigin}},
			{Tool: "typescript", State: safeexec.NotApprovedState, Target: safeexec.Target{Path: "/work/node_modules/.bin/tsc", Origin: safeexec.RepositoryOrigin}},
			{Tool: "npm", State: safeexec.UnavailableState, Problem: &safeexec.UnavailableError{Summary: "tool is not available"}},
		}}
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"--tools"}, strings.NewReader(""), &stdout, &stderr, base(manager)); code != 0 {
			t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
		}
		for _, want := range []string{"git\n  status: APPROVED", "typescript\n  status: NOT APPROVED", "npm\n  status: UNAVAILABLE"} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("output %q does not contain %q", stdout.String(), want)
			}
		}
	})

	t.Run("approves selected tool", func(t *testing.T) {
		manager := &fakeToolManager{approveTarget: safeexec.Target{Tool: "git", Path: "/usr/bin/git", SHA256: strings.Repeat("a", 64), Origin: safeexec.ExternalOrigin}}
		var stdout, stderr bytes.Buffer
		args := []string{"--tool", "git=/usr/bin/git", "--approve-tool", "git"}
		if code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, base(manager)); code != 0 {
			t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
		}
		if manager.overrides["git"] != "/usr/bin/git" || !strings.Contains(stdout.String(), "Approved tool git.") {
			t.Fatalf("manager = %+v, output = %q", manager, stdout.String())
		}
	})

	t.Run("revokes approvals", func(t *testing.T) {
		manager := &fakeToolManager{removed: 2}
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"--revoke-tool", "git"}, strings.NewReader(""), &stdout, &stderr, base(manager)); code != 0 {
			t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Revoked 2 approval(s) for tool git.") {
			t.Fatalf("output = %q", stdout.String())
		}
	})

	t.Run("unavailable approval has structured diagnostic", func(t *testing.T) {
		manager := &fakeToolManager{approveErr: &safeexec.UnavailableError{Code: "tool/not-found", Summary: "tool is not available", Evidence: "not on PATH", Action: "install it"}}
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"--approve-tool", "git"}, strings.NewReader(""), &stdout, &stderr, base(manager)); code != 2 {
			t.Fatalf("run() code = %d, want 2", code)
		}
		for _, want := range []string{"ERROR [tool/not-found]", "evidence: not on PATH", "action: install it"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("diagnostic %q does not contain %q", stderr.String(), want)
			}
		}
	})

	tests := []struct {
		name      string
		args      []string
		manager   toolManager
		wantError string
	}{
		{name: "management with path", args: []string{"--tools", "a"}, manager: &fakeToolManager{}, wantError: "cannot be combined"},
		{name: "management with validation", args: []string{"--tools", "--validate"}, manager: &fakeToolManager{}, wantError: "cannot be combined"},
		{name: "management with JSON", args: []string{"--tools", "--json"}, manager: &fakeToolManager{}, wantError: "cannot be combined"},
		{name: "approve and revoke", args: []string{"--approve-tool", "git", "--revoke-tool", "git"}, manager: &fakeToolManager{}, wantError: "cannot be combined"},
		{name: "setup and status", args: []string{"--setup", "--tools"}, manager: &fakeToolManager{}, wantError: "cannot be combined"},
		{name: "setup and approval", args: []string{"--setup", "--approve-tool", "git"}, manager: &fakeToolManager{}, wantError: "cannot be combined"},
		{name: "missing manager", args: []string{"--tools"}, wantError: "management is unavailable"},
		{name: "override error", args: []string{"--tool", "git=/git", "a"}, manager: &fakeToolManager{setErr: errors.New("bad selection")}, wantError: "bad selection"},
		{name: "approve error", args: []string{"--approve-tool", "git"}, manager: &fakeToolManager{approveErr: errors.New("write failed")}, wantError: "cannot approve tool"},
		{name: "revoke error", args: []string{"--revoke-tool", "git"}, manager: &fakeToolManager{revokeErr: errors.New("write failed")}, wantError: "cannot revoke tool"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), test.args, strings.NewReader(""), &stdout, &stderr, base(test.manager)); code != 2 {
				t.Fatalf("run() code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), test.wantError) {
				t.Fatalf("stderr = %q, want containing %q", stderr.String(), test.wantError)
			}
		})
	}
}

func TestRunSetup(t *testing.T) {
	t.Parallel()

	target := func(id safeexec.ToolID, executable string, origin safeexec.Origin) safeexec.Target {
		return safeexec.Target{
			Tool:   id,
			Path:   executable,
			SHA256: strings.Repeat(string(id[0]), 64),
			Origin: origin,
		}
	}
	base := func(manager toolManager, interactive bool) dependencies {
		return dependencies{
			getwd:       func() (string, error) { return "/work", nil },
			resolveRoot: func(string, string) (string, error) { return "/work", nil },
			tools:       manager,
			interactive: func(io.Reader, io.Writer, io.Writer) bool { return interactive },
		}
	}

	t.Run("discovers without approval in noninteractive mode", func(t *testing.T) {
		t.Parallel()
		manager := &fakeToolManager{statuses: []safeexec.Status{{
			Tool: "git", State: safeexec.NotApprovedState, Target: target("git", "/usr/bin/git", safeexec.ExternalOrigin),
		}}}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--setup"}, strings.NewReader("yes\n"), &stdout, &stderr, base(manager, false))
		if code != 0 || stderr.Len() != 0 || len(manager.approvedSetup) != 0 {
			t.Fatalf("run() = %d, approved=%v, stderr=%q", code, manager.approvedSetup, stderr.String())
		}
		for _, want := range []string{"Native tools", "status: NOT APPROVED", "origin: external", "Setup is noninteractive; no approvals changed."} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout %q does not contain %q", stdout.String(), want)
			}
		}
	})

	t.Run("approves external tools as one batch", func(t *testing.T) {
		t.Parallel()
		gitTarget := target("git", "/usr/bin/git", safeexec.ExternalOrigin)
		npmTarget := target("npm", "/usr/bin/npm", safeexec.ExternalOrigin)
		manager := &fakeToolManager{statuses: []safeexec.Status{
			{Tool: "npm", State: safeexec.NotApprovedState, Target: npmTarget},
			{Tool: "git", State: safeexec.NotApprovedState, Target: gitTarget},
		}}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--setup"}, strings.NewReader("yes\n"), &stdout, &stderr, base(manager, true))
		if code != 0 || len(manager.approvedSetup) != 2 {
			t.Fatalf("run() = %d, approved=%v, stderr=%q", code, manager.approvedSetup, stderr.String())
		}
		if manager.approvedSetup[0].Tool != "git" || manager.approvedSetup[1].Tool != "npm" {
			t.Fatalf("approved order = %v, want git then npm", manager.approvedSetup)
		}
		if got := strings.Count(stderr.String(), "Approve 2 external tools shown above?"); got != 1 {
			t.Fatalf("confirmation count = %d, stderr=%q", got, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Setup complete: approved 2 tools.") {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})

	t.Run("shows only tools relevant to detected instances", func(t *testing.T) {
		t.Parallel()
		manager := &fakeToolManager{statuses: []safeexec.Status{
			{Tool: "git", State: safeexec.NotApprovedState, Target: target("git", "/usr/bin/git", safeexec.ExternalOrigin)},
			{Tool: "node", State: safeexec.NotApprovedState, Target: target("node", "/usr/bin/node", safeexec.ExternalOrigin)},
			{Tool: "npm", State: safeexec.NotApprovedState, Target: target("npm", "/usr/bin/npm", safeexec.ExternalOrigin)},
		}}
		deps := base(manager, true)
		deps.setupTools = func(context.Context, string) ([]safeexec.Selection, error) {
			return []safeexec.Selection{{Tool: "git"}}, nil
		}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--setup"}, strings.NewReader("yes\n"), &stdout, &stderr, deps)
		if code != 0 || len(manager.approvedSetup) != 1 || manager.approvedSetup[0].Tool != "git" {
			t.Fatalf("run() = %d, approved=%v, stdout=%q, stderr=%q", code, manager.approvedSetup, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "node\n") || strings.Contains(stdout.String(), "npm\n") {
			t.Fatalf("setup exposed irrelevant tools: %q", stdout.String())
		}
		if !reflect.DeepEqual(manager.statusRequests, []safeexec.Selection{{Tool: "git"}}) {
			t.Fatalf("status requests = %v, want only git", manager.statusRequests)
		}
	})

	t.Run("needs no tools when no native-backed instance can run", func(t *testing.T) {
		t.Parallel()
		manager := &fakeToolManager{statuses: []safeexec.Status{{
			Tool: "npm", State: safeexec.NotApprovedState, Target: target("npm", "/usr/bin/npm", safeexec.ExternalOrigin),
		}}}
		deps := base(manager, true)
		deps.setupTools = func(context.Context, string) ([]safeexec.Selection, error) {
			return nil, nil
		}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--setup"}, strings.NewReader("yes\n"), &stdout, &stderr, deps)
		if code != 0 || stderr.Len() != 0 || len(manager.approvedSetup) != 0 || len(manager.statusRequests) != 0 {
			t.Fatalf("run() = %d, requested=%v, approved=%v, stderr=%q", code, manager.statusRequests, manager.approvedSetup, stderr.String())
		}
		if !strings.Contains(stdout.String(), "None required for detected providers.") || strings.Contains(stdout.String(), "npm\n") {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})

	t.Run("reports relevance discovery failure", func(t *testing.T) {
		t.Parallel()
		manager := &fakeToolManager{}
		deps := base(manager, false)
		deps.setupTools = func(context.Context, string) ([]safeexec.Selection, error) {
			return nil, errors.New("discovery failed")
		}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--setup"}, strings.NewReader(""), &stdout, &stderr, deps)
		if code != 2 || !strings.Contains(stderr.String(), "ERROR [setup/discovery]") {
			t.Fatalf("run() = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
		}
	})

	t.Run("approves repository tools separately", func(t *testing.T) {
		t.Parallel()
		local := target("typescript", "/work/node_modules/.bin/tsc", safeexec.RepositoryOrigin)
		local.Repository = "/work"
		manager := &fakeToolManager{statuses: []safeexec.Status{{
			Tool: "typescript", State: safeexec.NotApprovedState, Target: local,
		}}}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--setup"}, strings.NewReader("y\n"), &stdout, &stderr, base(manager, true))
		if code != 0 || len(manager.approvedSetup) != 1 || manager.approvedSetup[0].Origin != safeexec.RepositoryOrigin {
			t.Fatalf("run() = %d, approved=%v, stderr=%q", code, manager.approvedSetup, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Repository-controlled tools require separate approval") || !strings.Contains(stderr.String(), "Approve repository-controlled tool typescript at /work/node_modules/.bin/tsc?") {
			t.Fatalf("stdout=%q, stderr=%q", stdout.String(), stderr.String())
		}
	})

	t.Run("declines by default", func(t *testing.T) {
		t.Parallel()
		manager := &fakeToolManager{statuses: []safeexec.Status{{
			Tool: "git", State: safeexec.NotApprovedState, Target: target("git", "/usr/bin/git", safeexec.ExternalOrigin),
		}}}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--setup"}, strings.NewReader("\n"), &stdout, &stderr, base(manager, true))
		if code != 0 || len(manager.approvedSetup) != 0 || !strings.Contains(stdout.String(), "no approvals changed") {
			t.Fatalf("run() = %d, approved=%v, stdout=%q, stderr=%q", code, manager.approvedSetup, stdout.String(), stderr.String())
		}
	})

	t.Run("reports approval failure", func(t *testing.T) {
		t.Parallel()
		manager := &fakeToolManager{
			statuses: []safeexec.Status{{Tool: "git", State: safeexec.NotApprovedState, Target: target("git", "/usr/bin/git", safeexec.ExternalOrigin)}},
			setupErr: &safeexec.UnavailableError{Code: "tool/identity-changed", Summary: "tool changed", Action: "retry setup"},
		}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--setup"}, strings.NewReader("yes\n"), &stdout, &stderr, base(manager, true))
		if code != 2 || !strings.Contains(stderr.String(), "ERROR [tool/identity-changed]") {
			t.Fatalf("run() = %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
		}
	})

	t.Run("reports unavailable tools without prompting", func(t *testing.T) {
		t.Parallel()
		manager := &fakeToolManager{statuses: []safeexec.Status{{
			Tool: "git", State: safeexec.UnavailableState,
			Problem: &safeexec.UnavailableError{Code: "tool/not-found", Summary: "tool is not available", Evidence: "not on PATH", Action: "install Git"},
		}}}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--setup"}, strings.NewReader("yes\n"), &stdout, &stderr, base(manager, true))
		if code != 0 || stderr.Len() != 0 || len(manager.approvedSetup) != 0 {
			t.Fatalf("run() = %d, approved=%v, stderr=%q", code, manager.approvedSetup, stderr.String())
		}
		for _, want := range []string{"status: UNAVAILABLE", "code: tool/not-found", "evidence: not on PATH", "action: install Git", "1 native tool unavailable"} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout %q does not contain %q", stdout.String(), want)
			}
		}
	})

	t.Run("approved tools need no confirmation", func(t *testing.T) {
		t.Parallel()
		manager := &fakeToolManager{statuses: []safeexec.Status{{
			Tool: "git", State: safeexec.ApprovedState, Target: target("git", "/usr/bin/git", safeexec.ExternalOrigin),
		}}}
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"--setup"}, strings.NewReader("yes\n"), &stdout, &stderr, base(manager, true))
		if code != 0 || stderr.Len() != 0 || len(manager.approvedSetup) != 0 || !strings.Contains(stdout.String(), "no approvals required") {
			t.Fatalf("run() = %d, approved=%v, stdout=%q, stderr=%q", code, manager.approvedSetup, stdout.String(), stderr.String())
		}
	})
}

func TestRelevantNativeTools(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		files       map[string]string
		directories []string
		want        []safeexec.Selection
	}{
		{name: "no native provider", want: []safeexec.Selection{}},
		{name: "Git worktree with malformed EAS config", files: map[string]string{"eas.json": "{"}, directories: []string{".git"}, want: toolSelections("git")},
		{name: "public npm package", files: map[string]string{"package.json": `{"name":"public"}`}, want: npmToolsForPlatform()},
		{name: "private npm package", files: map[string]string{"package.json": `{"name":"private","private":true}`}, want: []safeexec.Selection{}},
		{name: "scripted npm package", files: map[string]string{"package.json": `{"name":"scripted","scripts":{"prepare":"build"}}`}, want: []safeexec.Selection{}},
		{name: "Prettier package context", files: map[string]string{
			"package.json":                       `{"private":true,"devDependencies":{"prettier":"3.9.9"}}`,
			"node_modules/prettier/package.json": `{"name":"prettier","version":"3.9.9"}`,
			"node_modules/prettier/index.mjs":    "export {};",
		}, want: toolSelections("node", "prettier")},
		{name: "TypeScript 6 project", files: map[string]string{
			"tsconfig.json":                        `{}`,
			"node_modules/typescript/package.json": `{"name":"typescript","version":"6.0.3"}`,
			"node_modules/typescript/lib/_tsc.js":  "compiler",
			"node_modules/typescript/lib/lib.d.ts": "standard library",
		}, want: toolSelections("node", "typescript")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for _, name := range test.directories {
				if err := os.MkdirAll(filepath.Join(root, name), 0o750); err != nil {
					t.Fatal(err)
				}
			}
			for name, content := range test.files {
				filename := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runner := &countingRunner{}
			got, err := relevantNativeTools(context.Background(), root, nativeProviders{
				git:        git.New(runner),
				npm:        npm.New(runner),
				prettier:   prettier.New(runner, appTargetDiscoverer{}),
				typescript: typescript.New(runner, appTargetDiscoverer{}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if runner.calls != 0 {
				t.Fatalf("discovery executed a native tool %d time(s)", runner.calls)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("relevantNativeTools() = %v, want %v", got, test.want)
			}
		})
	}
}

func npmToolsForPlatform() []safeexec.Selection {
	if runtime.GOOS == "windows" {
		return []safeexec.Selection{}
	}
	return toolSelections("node", "npm")
}

func toolSelections(ids ...safeexec.ToolID) []safeexec.Selection {
	selections := make([]safeexec.Selection, len(ids))
	for index, id := range ids {
		selections[index] = safeexec.Selection{Tool: id}
	}
	return selections
}

type appTargetDiscoverer struct{}

func (appTargetDiscoverer) Discover(_ string, id safeexec.ToolID) (safeexec.Target, error) {
	return safeexec.Target{}, &safeexec.UnavailableError{Tool: id, Code: "tool/selection-required", Summary: "selection required"}
}

func TestSetupConfirmation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		want      bool
		wantError string
	}{
		{name: "yes", input: "yes\n", want: true},
		{name: "short yes and whitespace", input: " Y \n", want: true},
		{name: "no", input: "no\n"},
		{name: "empty defaults to no", input: "\n"},
		{name: "EOF defaults to no"},
		{name: "bounded input", input: strings.Repeat("y", maxSetupAnswerBytes+1) + "\n", wantError: "token too long"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scanner := bufio.NewScanner(strings.NewReader(test.input))
			scanner.Buffer(make([]byte, 128), maxSetupAnswerBytes)
			var output bytes.Buffer
			got, err := setupConfirmation(&output, scanner, "Continue? ")
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("setupConfirmation() error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil || got != test.want || output.String() != "Continue? " {
				t.Fatalf("setupConfirmation() = %v, %v, output %q", got, err, output.String())
			}
		})
	}

	t.Run("writer error", func(t *testing.T) {
		t.Parallel()
		scanner := bufio.NewScanner(strings.NewReader("yes\n"))
		if _, err := setupConfirmation(appErrorWriter{}, scanner, "Continue? "); err == nil || !strings.Contains(err.Error(), "write confirmation") {
			t.Fatalf("setupConfirmation() error = %v", err)
		}
	})
}

func TestTerminalInteractionIsConservative(t *testing.T) {
	t.Parallel()
	if terminalInteraction(strings.NewReader(""), io.Discard, io.Discard) {
		t.Fatal("in-memory streams must not be treated as interactive")
	}
	file, err := os.CreateTemp(t.TempDir(), "regular-file")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if terminalFile(file) {
		t.Fatal("regular file must not be treated as a terminal")
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = null.Close() })
	if terminalFile(null) {
		t.Fatal("null device must not be treated as a terminal")
	}
}

type fakeToolManager struct {
	overrides      map[safeexec.ToolID]string
	setErr         error
	approveTarget  safeexec.Target
	approveErr     error
	approvedSetup  []safeexec.Target
	setupErr       error
	removed        int
	revokeErr      error
	statuses       []safeexec.Status
	statusRequests []safeexec.Selection
}

type countingRunner struct {
	calls int
}

func (r *countingRunner) Run(context.Context, safeexec.Request) (safeexec.Response, error) {
	r.calls++
	return safeexec.Response{}, errors.New("native execution is not expected")
}

type fakeChangeSource struct {
	stagedPaths  []scope.Path
	changedPaths []scope.Path
	stagedErr    error
	changedErr   error
	stagedCalls  int
	changedCalls int
	root         string
	revision     string
}

func (s *fakeChangeSource) Staged(_ context.Context, root string) ([]scope.Path, error) {
	s.stagedCalls++
	s.root = root
	return s.stagedPaths, s.stagedErr
}

func (s *fakeChangeSource) ChangedFrom(_ context.Context, root, revision string) ([]scope.Path, error) {
	s.changedCalls++
	s.root = root
	s.revision = revision
	return s.changedPaths, s.changedErr
}

func (m *fakeToolManager) SetOverrides(overrides map[safeexec.ToolID]string) error {
	m.overrides = overrides
	return m.setErr
}

func (m *fakeToolManager) Approve(_ string, _ safeexec.ToolID) (safeexec.Target, error) {
	return m.approveTarget, m.approveErr
}

func (m *fakeToolManager) ApproveTarget(_ string, target safeexec.Target) (safeexec.Target, error) {
	if m.setupErr != nil {
		return safeexec.Target{}, m.setupErr
	}
	m.approvedSetup = append(m.approvedSetup, target)
	return target, nil
}

func (m *fakeToolManager) Revoke(_ string, _ safeexec.ToolID) (int, error) {
	return m.removed, m.revokeErr
}

func (m *fakeToolManager) Statuses(_ string) []safeexec.Status {
	return m.statuses
}

func (m *fakeToolManager) StatusesFor(_ string, requested []safeexec.ToolID) []safeexec.Status {
	selections := make([]safeexec.Selection, len(requested))
	for index, id := range requested {
		selections[index] = safeexec.Selection{Tool: id}
	}
	return m.StatusesForSelections("", selections)
}

func (m *fakeToolManager) StatusesForSelections(_ string, requested []safeexec.Selection) []safeexec.Status {
	m.statusRequests = append([]safeexec.Selection(nil), requested...)
	wanted := make(map[safeexec.ToolID]struct{}, len(requested))
	for _, selection := range requested {
		wanted[selection.Tool] = struct{}{}
	}
	statuses := make([]safeexec.Status, 0, len(wanted))
	for _, status := range m.statuses {
		if _, ok := wanted[status.Tool]; ok {
			statuses = append(statuses, status)
		}
	}
	return statuses
}

type appProvider struct{ detectErr error }

func (*appProvider) ID() scope.ProviderID { return "test" }

func (p *appProvider) Detect(context.Context, provider.Repository) ([]provider.Instance, error) {
	return nil, p.detectErr
}

func (*appProvider) Evaluate(context.Context, provider.EvaluationContext, provider.Repository, provider.Instance, []scope.Path) ([]scope.Result, error) {
	return nil, nil
}

type appErrorWriter struct{}

func (appErrorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

type validationProvider struct {
	state scope.State
}

type unusedProvider struct{}

func (*unusedProvider) ID() scope.ProviderID { return "unused" }

func (*unusedProvider) Detect(context.Context, provider.Repository) ([]provider.Instance, error) {
	return nil, errors.New("unused provider must not be detected")
}

func (*unusedProvider) Evaluate(context.Context, provider.EvaluationContext, provider.Repository, provider.Instance, []scope.Path) ([]scope.Result, error) {
	return nil, errors.New("unused provider must not be evaluated")
}

func (*validationProvider) ID() scope.ProviderID { return "test" }

func (*validationProvider) Detect(context.Context, provider.Repository) ([]provider.Instance, error) {
	return []provider.Instance{provider.InstanceDescriptor{Provider: "test", ID: "test"}}, nil
}

func (p *validationProvider) Evaluate(_ context.Context, _ provider.EvaluationContext, _ provider.Repository, _ provider.Instance, paths []scope.Path) ([]scope.Result, error) {
	results := make([]scope.Result, 0, len(paths))
	for _, name := range paths {
		results = append(results, scope.Result{
			Path: name, Provider: "test", Instance: "test", State: p.state,
			Explanation: scope.Explanation{Code: "test", Summary: "test explanation"},
			Provenance:  scope.Provenance{Method: scope.SafeParser},
		})
	}
	return results, nil
}
