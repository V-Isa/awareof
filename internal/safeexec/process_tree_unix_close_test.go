//go:build darwin || linux

package safeexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
)

func TestProcessTreeCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("kill failed")
	tests := []struct {
		name    string
		killErr error
		wantErr error
	}{
		{name: "success"},
		{name: "missing process", killErr: syscall.ESRCH},
		{name: "wrapped missing process", killErr: fmt.Errorf("kill: %w", syscall.ESRCH)},
		{name: "permission denied", killErr: syscall.EPERM, wantErr: syscall.EPERM},
		{name: "failure", killErr: wantErr, wantErr: wantErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			tree := &processTree{
				// close reads only Pid; no operating-system process handle is used.
				command: &exec.Cmd{Process: &os.Process{Pid: 42}},
				kill: func(pid int, signal syscall.Signal) error {
					calls++
					if pid != -42 || signal != syscall.SIGKILL {
						t.Fatalf("kill(%d, %v), want kill(-42, SIGKILL)", pid, signal)
					}
					return test.killErr
				},
			}
			for attempt := 0; attempt < 2; attempt++ {
				err := tree.close()
				if test.wantErr == nil && err != nil {
					t.Fatalf("close() error = %v, want nil", err)
				}
				if test.wantErr != nil && !errors.Is(err, test.wantErr) {
					t.Fatalf("close() error = %v, want %v", err, test.wantErr)
				}
			}
			if calls != 1 {
				t.Fatalf("kill calls = %d, want 1", calls)
			}
		})
	}
}

func TestProcessTreeCloseBeforeStartDoesNotConsumeCleanup(t *testing.T) {
	t.Parallel()
	calls := 0
	command := &exec.Cmd{}
	tree := &processTree{
		command: command,
		kill: func(pid int, signal syscall.Signal) error {
			calls++
			if pid != -42 || signal != syscall.SIGKILL {
				t.Fatalf("kill(%d, %v), want kill(-42, SIGKILL)", pid, signal)
			}
			return nil
		},
	}
	if err := tree.close(); err != nil {
		t.Fatalf("close() before process start: %v", err)
	}
	if calls != 0 {
		t.Fatalf("kill calls before process start = %d, want 0", calls)
	}
	// close reads only Pid; no operating-system process handle is used.
	command.Process = &os.Process{Pid: 42}
	if err := tree.close(); err != nil {
		t.Fatalf("close() after process start: %v", err)
	}
	if calls != 1 {
		t.Fatalf("kill calls after process start = %d, want 1", calls)
	}
}

func TestProcessTreeCloseRejectsUnsafePID(t *testing.T) {
	t.Parallel()
	for _, pid := range []int{-1, 0, 1} {
		t.Run(fmt.Sprintf("PID_%d", pid), func(t *testing.T) {
			t.Parallel()
			calls := 0
			tree := &processTree{
				// close reads only Pid; no operating-system process handle is used.
				command: &exec.Cmd{Process: &os.Process{Pid: pid}},
				kill: func(int, syscall.Signal) error {
					calls++
					return nil
				},
			}
			want := fmt.Sprintf("refusing to signal process group for PID %d", pid)
			for attempt := 0; attempt < 2; attempt++ {
				if err := tree.close(); err == nil || err.Error() != want {
					t.Fatalf("close() error = %v, want %q", err, want)
				}
			}
			if calls != 0 {
				t.Fatalf("kill calls = %d, want 0", calls)
			}
		})
	}
}

func TestProcessTreeCloseIsConcurrentSafe(t *testing.T) {
	t.Parallel()
	const callers = 32
	wantErr := errors.New("kill failed")
	calls := 0
	gotPID := 0
	var gotSignal syscall.Signal
	tree := &processTree{
		// close reads only Pid; no operating-system process handle is used.
		command: &exec.Cmd{Process: &os.Process{Pid: 42}},
		kill: func(pid int, signal syscall.Signal) error {
			calls++
			gotPID = pid
			gotSignal = signal
			return wantErr
		},
	}
	start := make(chan struct{})
	errs := make([]error, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := range callers {
		go func() {
			defer wait.Done()
			<-start
			errs[index] = tree.close()
		}()
	}
	close(start)
	wait.Wait()
	for index, err := range errs {
		if !errors.Is(err, wantErr) {
			t.Errorf("close() caller %d error = %v, want %v", index, err, wantErr)
		}
	}
	if calls != 1 {
		t.Fatalf("kill calls = %d, want 1", calls)
	}
	if gotPID != -42 || gotSignal != syscall.SIGKILL {
		t.Fatalf("kill(%d, %v), want kill(-42, SIGKILL)", gotPID, gotSignal)
	}
}
