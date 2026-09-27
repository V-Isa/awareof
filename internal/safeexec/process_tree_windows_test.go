//go:build windows

package safeexec

import (
	"errors"

	"golang.org/x/sys/windows"
)

func processExists(pid int) (bool, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := windows.CloseHandle(handle); err != nil {
		return false, err
	}
	return true, nil
}
