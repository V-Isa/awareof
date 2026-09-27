//go:build windows

package safeexec

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processTree struct {
	mu       sync.Mutex
	job      windows.Handle
	closed   bool
	closeErr error
}

func newProcessTree(command *exec.Cmd) (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	)
	if err != nil {
		return nil, errors.Join(err, windows.CloseHandle(job))
	}
	tree := &processTree{job: job}
	command.Cancel = tree.close
	return tree, nil
}

func (t *processTree) attach(process *os.Process) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(process.Pid),
	)
	if err != nil {
		return err
	}
	assignErr := windows.AssignProcessToJobObject(t.job, handle)
	return errors.Join(assignErr, windows.CloseHandle(handle))
}

func (t *processTree) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return t.closeErr
	}
	t.closed = true
	t.closeErr = windows.CloseHandle(t.job)
	return t.closeErr
}
