//go:build darwin || linux

package safeexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type processTree struct {
	command  *exec.Cmd
	kill     func(int, syscall.Signal) error
	mu       sync.Mutex
	closed   bool
	closeErr error
}

func newProcessTree(command *exec.Cmd) (*processTree, error) {
	tree := &processTree{command: command, kill: syscall.Kill}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = tree.close
	return tree, nil
}

func (*processTree) attach(*os.Process) error {
	return nil
}

func (t *processTree) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return t.closeErr
	}
	process := t.command.Process
	if process == nil {
		return nil
	}
	t.closed = true
	// The negated PID targets a process group. Values at or below 1 could
	// instead address the current group, process 1, or every permitted process.
	if process.Pid <= 1 {
		t.closeErr = fmt.Errorf("refusing to signal process group for PID %d", process.Pid)
		return t.closeErr
	}
	t.closeErr = t.kill(-process.Pid, syscall.SIGKILL)
	if errors.Is(t.closeErr, syscall.ESRCH) {
		t.closeErr = nil
	}
	return t.closeErr
}
