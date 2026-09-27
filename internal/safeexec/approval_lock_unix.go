//go:build darwin || linux

package safeexec

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockApprovalFile(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func unlockApprovalFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
