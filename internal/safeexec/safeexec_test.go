package safeexec

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const descendantPIDFile = "descendant.pid"

func TestRunnerRun(t *testing.T) {
	t.Run("captures streams stdin environment arguments and exit", func(t *testing.T) {
		t.Setenv("AWAREOF_REMOVED", "remove-me")
		root := t.TempDir()
		response, err := (Runner{Resolver: fixedResolver{path: helperExecutable(t)}}).Run(context.Background(), Request{
			Root:     root,
			Tool:     "test",
			Args:     []string{"-test.run=TestHelperProcess", "--", "report", "literal;not-shell"},
			Stdin:    []byte("input"),
			Env:      map[string]string{"AWAREOF_HELPER": "present"},
			UnsetEnv: []string{"AWAREOF_REMOVED"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if response.ExitCode != 7 {
			t.Fatalf("ExitCode = %d, want 7", response.ExitCode)
		}
		if string(response.Stdout) != "literal;not-shell|input|present|" {
			t.Fatalf("stdout = %q", response.Stdout)
		}
		if string(response.Stderr) != "diagnostic" {
			t.Fatalf("stderr = %q", response.Stderr)
		}
	})

	t.Run("success", func(t *testing.T) {
		response, err := (Runner{Resolver: fixedResolver{path: helperExecutable(t)}}).Run(context.Background(), Request{
			Root: t.TempDir(), Tool: "test",
			Args: []string{"-test.run=TestHelperProcess", "--", "success"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if response.ExitCode != 0 || string(response.Stdout) != "ok" {
			t.Fatalf("response = %+v", response)
		}
	})

	t.Run("separate working directory", func(t *testing.T) {
		root := t.TempDir()
		workingDirectory := t.TempDir()
		response, err := (Runner{Resolver: fixedResolver{path: helperExecutable(t)}}).Run(context.Background(), Request{
			Root: root, Dir: workingDirectory, Tool: "test",
			Args: []string{"-test.run=TestHelperProcess", "--", "cwd"},
		})
		if err != nil {
			t.Fatal(err)
		}
		actual, err := os.Stat(string(response.Stdout))
		if err != nil {
			t.Fatal(err)
		}
		expected, err := os.Stat(workingDirectory)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(actual, expected) {
			t.Fatalf("working directory = %q, want same directory as %q", response.Stdout, workingDirectory)
		}
	})

	t.Run("approved interpreter", func(t *testing.T) {
		response, err := (Runner{Resolver: mappedResolver{
			"script":  {Tool: "script", Path: "-test.run=TestHelperProcess", Origin: ExternalOrigin},
			"runtime": {Tool: "runtime", Path: helperExecutable(t), Origin: ExternalOrigin},
		}}).Run(context.Background(), Request{
			Root: t.TempDir(), Tool: "script", Interpreter: "runtime", ExternalOnly: true,
			Args: []string{"--", "success"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if string(response.Stdout) != "ok" {
			t.Fatalf("stdout = %q, want ok", response.Stdout)
		}
	})

	t.Run("repository target rejected when external is required", func(t *testing.T) {
		_, err := (Runner{Resolver: mappedResolver{
			"script": {Tool: "script", Path: "repository/script", Origin: RepositoryOrigin},
		}}).Run(context.Background(), Request{
			Root: t.TempDir(), Tool: "script", ExternalOnly: true,
		})
		unavailable, ok := AsUnavailable(err)
		if !ok || unavailable.Tool != "script" || unavailable.Code != "tool/repository-unsupported" {
			t.Fatalf("Run() error = %v, want repository-unsupported", err)
		}
	})

	t.Run("repository interpreter rejected when external is required", func(t *testing.T) {
		_, err := (Runner{Resolver: mappedResolver{
			"script":  {Tool: "script", Path: "external/script", Origin: ExternalOrigin},
			"runtime": {Tool: "runtime", Path: "repository/runtime", Origin: RepositoryOrigin},
		}}).Run(context.Background(), Request{
			Root: t.TempDir(), Tool: "script", Interpreter: "runtime", ExternalOnly: true,
		})
		unavailable, ok := AsUnavailable(err)
		if !ok || unavailable.Tool != "runtime" || unavailable.Code != "tool/repository-unsupported" {
			t.Fatalf("Run() error = %v, want repository-unsupported", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		_, err := (Runner{Timeout: 20 * time.Millisecond, Resolver: fixedResolver{path: helperExecutable(t)}}).Run(context.Background(), Request{
			Root: t.TempDir(), Tool: "test",
			Args: []string{"-test.run=TestHelperProcess", "--", "wait"},
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run() error = %v, want deadline exceeded", err)
		}
	})

	t.Run("pre-canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := (Runner{Resolver: fixedResolver{path: helperExecutable(t)}}).Run(ctx, Request{
			Root: t.TempDir(), Tool: "test",
			Args: []string{"-test.run=TestHelperProcess", "--", "success"},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context canceled", err)
		}
	})

	t.Run("start failure", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing-executable")
		_, err := (Runner{Resolver: fixedResolver{path: missing}}).Run(context.Background(), Request{
			Root: t.TempDir(), Tool: "test",
		})
		if err == nil || !strings.Contains(err.Error(), "run \"test\"") {
			t.Fatalf("Run() error = %v, want wrapped start error", err)
		}
	})

	t.Run("output limit", func(t *testing.T) {
		_, err := (Runner{MaxOutputBytes: 3, Resolver: fixedResolver{path: helperExecutable(t)}}).Run(context.Background(), Request{
			Root: t.TempDir(), Tool: "test",
			Args: []string{"-test.run=TestHelperProcess", "--", "output"},
		})
		if err == nil || !strings.Contains(err.Error(), "exceeds 3 bytes") {
			t.Fatalf("Run() error = %v, want output limit error", err)
		}
	})

	t.Run("invalid environment", func(t *testing.T) {
		_, err := (Runner{Resolver: fixedResolver{path: helperExecutable(t)}}).Run(context.Background(), Request{
			Root: t.TempDir(), Tool: "test", Env: map[string]string{"BAD=KEY": "value"},
		})
		if err == nil || !strings.Contains(err.Error(), "environment key") {
			t.Fatalf("Run() error = %v, want environment error", err)
		}
	})
}

func TestRunnerRejectsInvalidRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		request Request
		want    string
	}{
		{name: "empty root", request: Request{Tool: "test"}, want: "command root is empty"},
		{name: "empty tool", request: Request{Root: t.TempDir()}, want: "command tool is empty"},
		{name: "missing resolver", request: Request{Root: t.TempDir(), Tool: "test"}, want: "tool resolver is nil"},
		{name: "same interpreter", request: Request{Root: t.TempDir(), Tool: "test", Interpreter: "test"}, want: "interpreter and tool are the same"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := (Runner{}).Run(context.Background(), test.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestMergeEnvironment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		base      []string
		overrides map[string]string
		unset     []string
		want      []string
		wantError string
	}{
		{name: "override unset ignore malformed and sort", base: []string{"B=old", "A=one", "malformed"}, overrides: map[string]string{"B": "new", "C": "three"}, unset: []string{"A"}, want: []string{"B=new", "C=three"}},
		{name: "invalid unset key", unset: []string{""}, wantError: "key is empty"},
		{name: "invalid override key", overrides: map[string]string{"A=B": "value"}, wantError: "is invalid"},
		{name: "nul value", overrides: map[string]string{"A": "x\x00y"}, wantError: "contains NUL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := mergeEnvironment(test.base, test.overrides, test.unset)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("mergeEnvironment() error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("mergeEnvironment() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMergeEnvironmentUsesPlatformKeySemantics(t *testing.T) {
	t.Parallel()
	got, err := mergeEnvironment([]string{"Path=base"}, map[string]string{"PATH": "override"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=override", "Path=base"}
	if runtime.GOOS == "windows" {
		want = []string{"PATH=override"}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeEnvironment() = %v, want %v", got, want)
	}
}

func TestLimitedBuffer(t *testing.T) {
	t.Parallel()
	buffer := limitedBuffer{limit: 3}
	if n, err := buffer.Write([]byte("ab")); n != 2 || err != nil {
		t.Fatalf("first Write() = (%d, %v)", n, err)
	}
	if n, err := buffer.Write([]byte("cdef")); n != 4 || err != nil {
		t.Fatalf("second Write() = (%d, %v)", n, err)
	}
	if string(buffer.Bytes()) != "abc" || !buffer.truncated {
		t.Fatalf("buffer = %q truncated=%v", buffer.Bytes(), buffer.truncated)
	}
	copyBytes := buffer.Bytes()
	copyBytes[0] = 'z'
	if string(buffer.Bytes()) != "abc" {
		t.Fatal("Bytes() did not return a defensive copy")
	}
}

func TestHelperProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "report":
		input, _ := io.ReadAll(os.Stdin)
		_, _ = os.Stdout.WriteString(os.Args[separator+2] + "|" + string(input) + "|" + os.Getenv("AWAREOF_HELPER") + "|" + os.Getenv("AWAREOF_REMOVED"))
		_, _ = os.Stderr.WriteString("diagnostic")
		os.Exit(7)
	case "success":
		_, _ = os.Stdout.WriteString("ok")
		os.Exit(0)
	case "wait":
		time.Sleep(time.Hour)
	case "output":
		_, _ = os.Stdout.WriteString("too much")
	case "cwd":
		workingDirectory, _ := os.Getwd()
		_, _ = os.Stdout.WriteString(workingDirectory)
		os.Exit(0)
	case "spawn-descendant":
		time.Sleep(100 * time.Millisecond)
		if err := startHelperDescendant(); err != nil {
			os.Exit(8)
		}
		time.Sleep(time.Hour)
	case "spawn-descendant-and-exit":
		time.Sleep(100 * time.Millisecond)
		if err := startHelperDescendant(); err != nil {
			os.Exit(8)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(descendantPIDFile); err == nil {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(10)
	case "descendant":
		temporary := descendantPIDFile + ".tmp"
		if err := os.WriteFile(temporary, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(9)
		}
		if err := os.Rename(temporary, descendantPIDFile); err != nil {
			os.Exit(9)
		}
		time.Sleep(time.Hour)
	}
}

func startHelperDescendant() error {
	command := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", "descendant") //nolint:gosec // The test re-executes its own binary with fixed arguments.
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func helperExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

type fixedResolver struct {
	path string
	err  error
}

type mappedResolver map[ToolID]Target

func (r mappedResolver) Resolve(_ string, tool ToolID) (Target, error) {
	target, ok := r[tool]
	if !ok {
		return Target{}, errors.New("target is unavailable")
	}
	return target, nil
}

func (r fixedResolver) Resolve(_ string, tool ToolID) (Target, error) {
	if r.err != nil {
		return Target{}, r.err
	}
	return Target{Tool: tool, Path: r.path}, nil
}
