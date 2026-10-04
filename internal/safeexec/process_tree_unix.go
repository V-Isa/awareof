//go:build darwin || linux

package safeexec

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type processTree struct {
	command   *exec.Cmd
	kill      func(int, syscall.Signal) error
	closeOnce sync.Once
	closeErr  error
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
	t.closeOnce.Do(func() {
		if t.command.Process == nil {
			return
		}
		t.closeErr = t.kill(-t.command.Process.Pid, syscall.SIGKILL)
		if errors.Is(t.closeErr, syscall.ESRCH) {
			t.closeErr = nil
		}
	})
	return t.closeErr
}
