//go:build !darwin && !linux && !windows

package safeexec

import (
	"fmt"
	"os"
	"runtime"
)

func lockApprovalFile(*os.File) error {
	return fmt.Errorf("approval file locking is not supported on %s", runtime.GOOS)
}

func unlockApprovalFile(*os.File) error {
	return nil
}
