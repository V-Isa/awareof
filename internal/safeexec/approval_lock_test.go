package safeexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	approvalHelperMode  = "AWAREOF_APPROVAL_HELPER_MODE"
	approvalHelperStore = "AWAREOF_APPROVAL_HELPER_STORE"
	approvalHelperReady = "AWAREOF_APPROVAL_HELPER_READY"
	approvalHelperStart = "AWAREOF_APPROVAL_HELPER_START"
	approvalHelperIndex = "AWAREOF_APPROVAL_HELPER_INDEX"
)

func TestFileApprovalStoreConcurrentAdds(t *testing.T) {
	t.Parallel()
	if !approvalLockSupported() {
		t.Skip("approval file locking is not supported on this platform")
	}

	store := FileApprovalStore{Path: filepath.Join(t.TempDir(), "approvals.json")}
	const count = 24
	var group sync.WaitGroup
	errorsByAdd := make(chan error, count)
	for index := range count {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsByAdd <- store.Add(approvalTestTarget(store.Path, index))
		}()
	}
	group.Wait()
	close(errorsByAdd)
	for err := range errorsByAdd {
		if err != nil {
			t.Fatal(err)
		}
	}

	document, err := store.load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Approvals) != count {
		t.Fatalf("approval count = %d, want %d", len(document.Approvals), count)
	}
}

func TestFileApprovalStoreConcurrentProcessesPreserveAdds(t *testing.T) {
	if !approvalLockSupported() {
		t.Skip("approval file locking is not supported on this platform")
	}

	dir := t.TempDir()
	store := FileApprovalStore{Path: filepath.Join(dir, "approvals.json")}
	start := filepath.Join(dir, "start")
	const count = 12
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	type child struct {
		command *exec.Cmd
		output  bytes.Buffer
	}
	children := make([]child, count)
	ready := make([]string, count)
	for index := range count {
		ready[index] = filepath.Join(dir, fmt.Sprintf("ready-%d", index))
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestApprovalStoreHelperProcess$") //nolint:gosec // re-executes the current test binary
		command.Env = append(os.Environ(),
			approvalHelperMode+"=add",
			approvalHelperStore+"="+store.Path,
			approvalHelperReady+"="+ready[index],
			approvalHelperStart+"="+start,
			approvalHelperIndex+"="+fmt.Sprint(index),
		)
		command.Stdout = &children[index].output
		command.Stderr = &children[index].output
		children[index].command = command
		if err := command.Start(); err != nil {
			t.Fatalf("start helper %d: %v", index, err)
		}
	}

	if err := waitForApprovalHelperFiles(ctx, ready); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for index := range children {
		if err := children[index].command.Wait(); err != nil {
			t.Fatalf("helper %d: %v\n%s", index, err, children[index].output.String())
		}
	}

	document, err := store.load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Approvals) != count {
		t.Fatalf("approval count = %d, want %d", len(document.Approvals), count)
	}
}

func TestFileApprovalStoreMutationWaitsForProcessLock(t *testing.T) {
	if !approvalLockSupported() {
		t.Skip("approval file locking is not supported on this platform")
	}

	dir := t.TempDir()
	store := FileApprovalStore{Path: filepath.Join(dir, "approvals.json")}
	release, err := store.acquireMutationLock("")
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = release()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ready := filepath.Join(dir, "ready")
	start := filepath.Join(dir, "start")
	var output bytes.Buffer
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestApprovalStoreHelperProcess$") //nolint:gosec // re-executes the current test binary
	command.Env = append(os.Environ(),
		approvalHelperMode+"=add",
		approvalHelperStore+"="+store.Path,
		approvalHelperReady+"="+ready,
		approvalHelperStart+"="+start,
		approvalHelperIndex+"=1",
	)
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := waitForApprovalHelperFiles(ctx, []string{ready}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	select {
	case err := <-done:
		if releaseErr := release(); releaseErr != nil {
			t.Fatal(releaseErr)
		}
		released = true
		t.Fatalf("mutation completed before lock release: %v\n%s", err, output.String())
	case <-time.After(200 * time.Millisecond):
	}

	if err := release(); err != nil {
		t.Fatal(err)
	}
	released = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper: %v\n%s", err, output.String())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestFileApprovalStoreLockFileSafety(t *testing.T) {
	t.Parallel()
	if !approvalLockSupported() {
		t.Skip("approval file locking is not supported on this platform")
	}

	t.Run("private regular file", func(t *testing.T) {
		t.Parallel()
		store := FileApprovalStore{Path: filepath.Join(t.TempDir(), "approvals.json")}
		if err := store.Add(approvalTestTarget(store.Path, 1)); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(store.Path + approvalLockSuffix)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("lock mode = %v, want regular file", info.Mode())
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("lock permissions = %o, want 600", info.Mode().Perm())
		}
	})

	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" {
			t.Skip("symlink setup is not reliable without additional privileges")
		}
		dir := t.TempDir()
		store := FileApprovalStore{Path: filepath.Join(dir, "approvals.json")}
		target := filepath.Join(dir, "target.lock")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, store.Path+approvalLockSuffix); err != nil {
			t.Fatal(err)
		}
		err := store.Add(approvalTestTarget(store.Path, 1))
		if err == nil || !strings.Contains(err.Error(), "lock file is not a regular file") {
			t.Fatalf("Add() error = %v, want unsafe lock-file rejection", err)
		}
	})

	t.Run("writable by others", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" {
			t.Skip("POSIX mode bits are not authoritative on Windows")
		}
		dir := t.TempDir()
		store := FileApprovalStore{Path: filepath.Join(dir, "approvals.json")}
		lockPath := store.Path + approvalLockSuffix
		if err := os.WriteFile(lockPath, nil, 0o622); err != nil { //nolint:gosec // intentionally unsafe permission fixture
			t.Fatal(err)
		}
		if err := os.Chmod(lockPath, 0o622); err != nil { //nolint:gosec // intentionally unsafe permission fixture
			t.Fatal(err)
		}
		err := store.Add(approvalTestTarget(store.Path, 1))
		if err == nil || !strings.Contains(err.Error(), "lock file is writable by other users") {
			t.Fatalf("Add() error = %v, want unsafe lock-file rejection", err)
		}
	})
}

func TestApprovalStoreHelperProcess(t *testing.T) {
	mode := os.Getenv(approvalHelperMode)
	if mode == "" {
		return
	}
	store := FileApprovalStore{Path: os.Getenv(approvalHelperStore)}
	if err := os.WriteFile(os.Getenv(approvalHelperReady), nil, 0o600); err != nil { //nolint:gosec // parent test supplies the isolated marker path
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := waitForApprovalHelperFiles(ctx, []string{os.Getenv(approvalHelperStart)}); err != nil {
		t.Fatal(err)
	}

	switch mode {
	case "add":
		index, err := strconv.Atoi(os.Getenv(approvalHelperIndex))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Add(approvalTestTarget(store.Path, index)); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func approvalTestTarget(storePath string, index int) Target {
	return Target{
		Tool:   "test",
		Path:   filepath.Join(filepath.Dir(storePath), fmt.Sprintf("tool-%d", index)),
		SHA256: fmt.Sprintf("%064x", index+1),
		Origin: ExternalOrigin,
	}
}

func waitForApprovalHelperFiles(ctx context.Context, paths []string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	remaining := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		remaining[path] = struct{}{}
	}
	for len(remaining) > 0 {
		for path := range remaining {
			_, err := os.Stat(path)
			if err == nil {
				delete(remaining, path)
				continue
			}
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect helper marker %q: %w", path, err)
			}
		}
		if len(remaining) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for approval helper markers: %w", ctx.Err())
		case <-ticker.C:
		}
	}
	return nil
}

func approvalLockSupported() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux" || runtime.GOOS == "windows"
}
