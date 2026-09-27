//go:build !darwin && !linux && !windows

package safeexec

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

type processTree struct{}

func newProcessTree(*exec.Cmd) (*processTree, error) {
	return nil, fmt.Errorf("process-tree containment is not supported on %s", runtime.GOOS)
}

func (*processTree) attach(*os.Process) error {
	return nil
}

func (*processTree) close() error {
	return nil
}
