//go:build linux

package safeexec

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func processExists(pid int) (bool, error) {
	proc, err := os.OpenRoot("/proc")
	if err != nil {
		return false, err
	}
	defer func() { _ = proc.Close() }()
	status, err := proc.ReadFile(strconv.Itoa(pid) + "/stat")
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stateIndex := strings.LastIndex(string(status), ") ") + 2
	if stateIndex < 2 || stateIndex >= len(status) {
		return false, fmt.Errorf("cannot parse process state for PID %d", pid)
	}
	return status[stateIndex] != 'Z' && status[stateIndex] != 'X', nil
}
