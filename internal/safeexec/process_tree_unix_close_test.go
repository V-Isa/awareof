//go:build darwin || linux

package safeexec

import (
	"errors"
	"os"
	"os/exec"
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
		{name: "failure", killErr: wantErr, wantErr: wantErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			tree := &processTree{
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
				if err := tree.close(); !errors.Is(err, test.wantErr) {
					t.Fatalf("close() error = %v, want %v", err, test.wantErr)
				}
			}
			if calls != 1 {
				t.Fatalf("kill calls = %d, want 1", calls)
			}
		})
	}
}
