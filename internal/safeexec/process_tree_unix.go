//go:build darwin || linux

package safeexec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type processTree struct {
	command *exec.Cmd
}

func newProcessTree(command *exec.Cmd) (*processTree, error) {
	tree := &processTree{command: command}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = tree.close
	return tree, nil
}

func (*processTree) attach(*os.Process) error {
	return nil
}

func (t *processTree) close() error {
	if t.command.Process == nil {
		return nil
	}
	err := syscall.Kill(-t.command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
