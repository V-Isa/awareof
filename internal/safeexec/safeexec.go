package safeexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	defaultTimeout        = 15 * time.Second
	defaultMaxOutputBytes = 32 * 1024 * 1024
)

var loaderEnvironment = []string{
	"DYLD_FALLBACK_FRAMEWORK_PATH",
	"DYLD_FALLBACK_LIBRARY_PATH",
	"DYLD_FRAMEWORK_PATH",
	"DYLD_INSERT_LIBRARIES",
	"DYLD_LIBRARY_PATH",
	"LD_AUDIT",
	"LD_LIBRARY_PATH",
	"LD_PRELOAD",
}

// Request is one audited, argument-vector command invocation. Root identifies
// the inspected repository for executable approval. Dir optionally selects a
// separate provider-controlled working directory.
type Request struct {
	Root         string
	Dir          string
	Tool         ToolID
	Interpreter  ToolID
	ExternalOnly bool
	Args         []string
	Stdin        []byte
	Env          map[string]string
	UnsetEnv     []string
}

// Response contains captured process output and its exit status. A non-zero
// process exit is data for the provider to interpret, not a runner error.
type Response struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// CommandRunner is the provider-facing process boundary.
type CommandRunner interface {
	Run(context.Context, Request) (Response, error)
}

// Runner executes audited native commands without a shell.
type Runner struct {
	Timeout        time.Duration
	MaxOutputBytes int
	Resolver       interface {
		Resolve(string, ToolID) (Target, error)
	}
}

func (r Runner) Run(ctx context.Context, request Request) (Response, error) {
	if request.Root == "" {
		return Response{}, errors.New("command root is empty")
	}
	if request.Tool == "" {
		return Response{}, errors.New("command tool is empty")
	}
	if request.Interpreter != "" && request.Interpreter == request.Tool {
		return Response{}, errors.New("command interpreter and tool are the same")
	}
	if r.Resolver == nil {
		return Response{}, errors.New("tool resolver is nil")
	}

	target, err := r.Resolver.Resolve(request.Root, request.Tool)
	if err != nil {
		return Response{}, err
	}
	if request.ExternalOnly {
		if err := requireExternalTarget(target); err != nil {
			return Response{}, err
		}
	}
	executable := target.Path
	arguments := request.Args
	if request.Interpreter != "" {
		interpreter, err := r.Resolver.Resolve(request.Root, request.Interpreter)
		if err != nil {
			return Response{}, err
		}
		if request.ExternalOnly {
			if err := requireExternalTarget(interpreter); err != nil {
				return Response{}, err
			}
		}
		executable = interpreter.Path
		arguments = append([]string{target.Path}, request.Args...)
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	limit := r.MaxOutputBytes
	if limit <= 0 {
		limit = defaultMaxOutputBytes
	}
	stdout := limitedBuffer{limit: limit}
	stderr := limitedBuffer{limit: limit}

	cmd := exec.CommandContext(commandCtx, executable, arguments...) //nolint:gosec // Resolver enforces explicit path-and-digest approval before execution.
	cmd.WaitDelay = time.Second
	cmd.Dir = request.Dir
	if cmd.Dir == "" {
		cmd.Dir = request.Root
	}
	unsetEnvironment := append(append([]string(nil), loaderEnvironment...), request.UnsetEnv...)
	cmd.Env, err = mergeEnvironment(os.Environ(), request.Env, unsetEnvironment)
	if err != nil {
		return Response{}, fmt.Errorf("prepare environment for %q: %w", request.Tool, err)
	}
	cmd.Stdin = bytes.NewReader(request.Stdin)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	processes, err := newProcessTree(cmd)
	if err != nil {
		return Response{}, fmt.Errorf("prepare process containment for %q: %w", request.Tool, err)
	}
	runErr := cmd.Start()
	if runErr == nil {
		if err := processes.attach(cmd.Process); err != nil {
			killErr := cmd.Process.Kill()
			if errors.Is(killErr, os.ErrProcessDone) {
				killErr = nil
			}
			_ = cmd.Wait()
			closeErr := processes.close()
			if commandCtx.Err() != nil {
				return Response{}, fmt.Errorf("run %q: %w", request.Tool, commandCtx.Err())
			}
			return Response{}, fmt.Errorf(
				"contain process tree for %q: %w",
				request.Tool,
				errors.Join(err, killErr, closeErr),
			)
		}
		runErr = cmd.Wait()
	}
	if err := processes.close(); err != nil {
		return Response{}, fmt.Errorf("clean up process tree for %q: %w", request.Tool, err)
	}
	response := Response{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}
	if commandCtx.Err() != nil {
		return Response{}, fmt.Errorf("run %q: %w", request.Tool, commandCtx.Err())
	}
	if stdout.truncated || stderr.truncated {
		return Response{}, fmt.Errorf("run %q: captured output exceeds %d bytes", request.Tool, limit)
	}
	if runErr == nil {
		return response, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		response.ExitCode = exitErr.ExitCode()
		return response, nil
	}
	return Response{}, fmt.Errorf("run %q: %w", request.Tool, runErr)
}

func requireExternalTarget(target Target) error {
	if target.Origin == ExternalOrigin {
		return nil
	}
	return &UnavailableError{
		Tool:     target.Tool,
		Code:     "tool/repository-unsupported",
		Summary:  fmt.Sprintf("tool %q cannot be used from the inspected repository for this operation", target.Tool),
		Evidence: fmt.Sprintf("resolved executable is %q", target.Path),
		Action:   "select and approve an installation outside the inspected repository",
	}
}

func mergeEnvironment(base []string, overrides map[string]string, unset []string) ([]string, error) {
	values := make(map[string]string, len(base)+len(overrides))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			values[environmentLookupKey(key)] = item
		}
	}
	for _, key := range unset {
		if err := validateEnvironmentKey(key); err != nil {
			return nil, err
		}
		delete(values, environmentLookupKey(key))
	}
	for key, value := range overrides {
		if err := validateEnvironmentKey(key); err != nil {
			return nil, err
		}
		if strings.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("environment value for %q contains NUL", key)
		}
		values[environmentLookupKey(key)] = key + "=" + value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, values[key])
	}
	return out, nil
}

func environmentLookupKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

func validateEnvironmentKey(key string) error {
	if key == "" {
		return errors.New("environment key is empty")
	}
	if strings.ContainsAny(key, "=\x00") {
		return fmt.Errorf("environment key %q is invalid", key)
	}
	return nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		write := min(remaining, len(data))
		_, _ = b.buffer.Write(data[:write])
	}
	if len(data) > remaining {
		b.truncated = true
	}
	return len(data), nil
}

func (b *limitedBuffer) Bytes() []byte {
	return bytes.Clone(b.buffer.Bytes())
}
