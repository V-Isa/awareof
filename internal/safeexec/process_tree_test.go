//go:build darwin || linux || windows

package safeexec

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunnerStopsDescendantsOnCancellation(t *testing.T) {
	root := t.TempDir()
	executable := helperExecutable(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := (Runner{Timeout: 10 * time.Second, Resolver: fixedResolver{path: executable}}).Run(ctx, Request{
			Root: root,
			Tool: "test",
			Args: []string{"-test.run=TestHelperProcess", "--", "spawn-descendant"},
		})
		done <- err
	}()
	t.Cleanup(cancel)

	pid, err := waitForPID(root, 5*time.Second)
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after cancellation")
	}
	if err := waitForProcessExit(pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerStopsDescendantsOnTimeout(t *testing.T) {
	root := t.TempDir()
	_, err := (Runner{Timeout: time.Second, Resolver: fixedResolver{path: helperExecutable(t)}}).Run(context.Background(), Request{
		Root: root,
		Tool: "test",
		Args: []string{"-test.run=TestHelperProcess", "--", "spawn-descendant"},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
	pid, err := waitForPID(root, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForProcessExit(pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerStopsDescendantsAfterCommandReturns(t *testing.T) {
	root := t.TempDir()
	response, err := (Runner{Resolver: fixedResolver{path: helperExecutable(t)}}).Run(context.Background(), Request{
		Root: root,
		Tool: "test",
		Args: []string{"-test.run=TestHelperProcess", "--", "spawn-descendant-and-exit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", response.ExitCode)
	}
	pid, err := waitForPID(root, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForProcessExit(pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func waitForPID(root string, timeout time.Duration) (int, error) {
	scopedRoot, err := os.OpenRoot(root)
	if err != nil {
		return 0, err
	}
	defer func() { _ = scopedRoot.Close() }()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := scopedRoot.ReadFile(descendantPIDFile)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil || pid <= 0 {
				return 0, errors.New("descendant wrote an invalid process ID")
			}
			return pid, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0, errors.New("descendant did not report its process ID")
}

func waitForProcessExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := processExists(pid)
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("descendant process survived cancellation")
}
