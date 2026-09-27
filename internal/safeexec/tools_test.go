package safeexec

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManagerApprovalLifecycle(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	config := t.TempDir()
	executableName := testExecutableName("tool")
	executable := filepath.Join(repository, "bin", executableName)
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("first"), 0o700); err != nil { //nolint:gosec // isolated executable fixture
		t.Fatal(err)
	}
	store := FileApprovalStore{Path: filepath.Join(config, "approvals.json")}
	manager, err := NewManager([]Tool{{ID: "test", Command: "missing"}}, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetOverrides(map[ToolID]string{"test": filepath.Join("bin", executableName)}); err != nil {
		t.Fatal(err)
	}

	_, err = manager.Resolve(repository, "test")
	unavailable, ok := AsUnavailable(err)
	if !ok || unavailable.Code != "tool/not-approved" || !strings.Contains(unavailable.Action, "--setup") || !strings.Contains(unavailable.Action, "same --tool") {
		t.Fatalf("Resolve() error = %v, want tool/not-approved", err)
	}
	target, err := manager.Approve(repository, "test")
	if err != nil {
		t.Fatal(err)
	}
	if target.Origin != RepositoryOrigin || target.Repository == "" {
		t.Fatalf("approved target = %+v, want repository scope", target)
	}
	resolved, err := manager.Resolve(repository, "test")
	if err != nil || !sameApproval(resolved, target) {
		t.Fatalf("Resolve() = %+v, %v; want approved target", resolved, err)
	}

	if err := os.WriteFile(executable, []byte("second"), 0o700); err != nil { //nolint:gosec // isolated executable fixture
		t.Fatal(err)
	}
	_, err = manager.Resolve(repository, "test")
	unavailable, ok = AsUnavailable(err)
	if !ok || unavailable.Code != "tool/not-approved" {
		t.Fatalf("Resolve() after replacement error = %v, want fresh approval", err)
	}
	if _, err := manager.Approve(repository, "test"); err != nil {
		t.Fatal(err)
	}
	removed, err := manager.Revoke(repository, "test")
	if err != nil || removed != 2 {
		t.Fatalf("Revoke() = %d, %v; want 2 approvals removed", removed, err)
	}
	removed, err = manager.Revoke(repository, "test")
	if err != nil || removed != 0 {
		t.Fatalf("second Revoke() = %d, %v; want no-op", removed, err)
	}
}

func TestManagerStatusesDoesNotExecuteTool(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is not portable to Windows")
	}
	repository := t.TempDir()
	marker := filepath.Join(repository, "executed")
	executable := filepath.Join(repository, testExecutableName("tool"))
	content := "#!/bin/sh\ntouch \"" + marker + "\"\n"
	if err := os.WriteFile(executable, []byte(content), 0o700); err != nil { //nolint:gosec // isolated executable fixture that must not run
		t.Fatal(err)
	}
	manager, err := NewManager(
		[]Tool{{ID: "test", Command: executable}},
		FileApprovalStore{Path: filepath.Join(t.TempDir(), "approvals.json")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if statuses := manager.StatusesFor(repository, nil); len(statuses) != 0 {
		t.Fatalf("StatusesFor() = %+v, want no statuses", statuses)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty discovery executed tool; marker error = %v", err)
	}
	requested := []ToolID{"test", "test"}
	statuses := manager.StatusesFor(repository, requested)
	if len(statuses) != 1 || statuses[0].State != NotApprovedState {
		t.Fatalf("StatusesFor() = %+v, want one unapproved tool", statuses)
	}
	statuses = manager.Statuses(repository)
	if len(statuses) != 1 || statuses[0].State != NotApprovedState {
		t.Fatalf("Statuses() = %+v, want one unapproved tool", statuses)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discovery executed tool; marker error = %v", err)
	}
}

func TestManagerApproveTargetRejectsChangedIdentity(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	executable := filepath.Join(repository, testExecutableName("tool"))
	if err := os.WriteFile(executable, []byte("first"), 0o700); err != nil { //nolint:gosec // isolated executable fixture
		t.Fatal(err)
	}
	store := &fakeApprovalStore{}
	manager, err := NewManager([]Tool{{ID: "test", Command: executable}}, store)
	if err != nil {
		t.Fatal(err)
	}
	target, err := manager.Discover(repository, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("second"), 0o700); err != nil { //nolint:gosec // isolated executable fixture
		t.Fatal(err)
	}
	_, err = manager.ApproveTarget(repository, target)
	unavailable, ok := AsUnavailable(err)
	if !ok || unavailable.Tool != "test" || unavailable.Code != "tool/identity-changed" {
		t.Fatalf("ApproveTarget() error = %v, want tool/identity-changed", err)
	}
	if store.added != 0 {
		t.Fatalf("approval store additions = %d, want 0", store.added)
	}

	current, err := manager.Discover(repository, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApproveTarget(repository, current); err != nil {
		t.Fatal(err)
	}
	if store.added != 1 {
		t.Fatalf("approval store additions = %d, want 1", store.added)
	}
}

func TestManagerExternalApprovalIsGlobal(t *testing.T) {
	t.Parallel()
	store := FileApprovalStore{Path: filepath.Join(t.TempDir(), "approvals.json")}
	manager, err := NewManager([]Tool{{ID: "test", Command: helperExecutable(t)}}, store)
	if err != nil {
		t.Fatal(err)
	}
	firstRoot := t.TempDir()
	target, err := manager.Approve(firstRoot, "test")
	if err != nil {
		t.Fatal(err)
	}
	if target.Origin != ExternalOrigin || target.Repository != "" {
		t.Fatalf("target = %+v, want global external approval", target)
	}
	if _, err := manager.Resolve(t.TempDir(), "test"); err != nil {
		t.Fatalf("global approval did not apply to another repository: %v", err)
	}
	statuses := manager.Statuses(firstRoot)
	if len(statuses) != 1 || statuses[0].State != ApprovedState || statuses[0].Problem != nil {
		t.Fatalf("Statuses() = %+v, want approved", statuses)
	}
}

func TestManagerDiscoveryAndConfigurationFailures(t *testing.T) {
	t.Parallel()
	store := &fakeApprovalStore{}
	tests := []struct {
		name  string
		tools []Tool
		want  string
	}{
		{name: "nil store", tools: []Tool{{ID: "test", Command: "test"}}, want: "approval store is nil"},
		{name: "empty id", tools: []Tool{{Command: "test"}}, want: "tool id is empty"},
		{name: "invalid id", tools: []Tool{{ID: "Test", Command: "test"}}, want: "invalid tool id"},
		{name: "invalid leading digit", tools: []Tool{{ID: "1test", Command: "test"}}, want: "invalid tool id"},
		{name: "empty command", tools: []Tool{{ID: "test"}}, want: "command is empty"},
		{name: "duplicate", tools: []Tool{{ID: "test", Command: "one"}, {ID: "test", Command: "two"}}, want: "duplicate tool"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			currentStore := ApprovalStore(store)
			if test.name == "nil store" {
				currentStore = nil
			}
			_, err := NewManager(test.tools, currentStore)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewManager() error = %v, want containing %q", err, test.want)
			}
		})
	}

	manager, err := NewManager([]Tool{{ID: "test", Command: "awareof-definitely-missing"}}, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetOverrides(map[ToolID]string{"unknown": "/tool"}); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("SetOverrides() error = %v, want unknown tool", err)
	}
	if err := manager.SetOverrides(map[ToolID]string{"test": ""}); err == nil || !strings.Contains(err.Error(), "path is empty") {
		t.Fatalf("SetOverrides() error = %v, want empty path", err)
	}
	if _, err := manager.Discover(t.TempDir(), "unknown"); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("Discover() error = %v, want unknown tool", err)
	}
	_, err = manager.Discover(t.TempDir(), "test")
	unavailable, ok := AsUnavailable(err)
	if !ok || unavailable.Tool != "test" || unavailable.Code != "tool/not-found" {
		t.Fatalf("Discover() error = %v, want tool/not-found", err)
	}
	statuses := manager.Statuses(t.TempDir())
	if len(statuses) != 1 || statuses[0].State != UnavailableState || statuses[0].Problem == nil {
		t.Fatalf("Statuses() = %+v, want unavailable", statuses)
	}
}

func TestManagerApprovalStoreFailure(t *testing.T) {
	t.Parallel()
	store := &fakeApprovalStore{approvedErr: errors.New("store failed")}
	manager, err := NewManager([]Tool{{ID: "test", Command: helperExecutable(t)}}, store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Resolve(t.TempDir(), "test")
	unavailable, ok := AsUnavailable(err)
	if !ok || unavailable.Tool != "test" || unavailable.Code != "tool/approval-unavailable" || !errors.Is(err, store.approvedErr) {
		t.Fatalf("Resolve() error = %v, want approval unavailable wrapping store error", err)
	}
	statuses := manager.Statuses(t.TempDir())
	if len(statuses) != 1 || statuses[0].State != UnavailableState {
		t.Fatalf("Statuses() = %+v, want unavailable", statuses)
	}
}

func TestFileApprovalStoreRejectsUnsafeFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tests := []struct {
		name    string
		content string
		mode    os.FileMode
		want    string
	}{
		{name: "malformed JSON", content: "{", mode: 0o600, want: "parse approval file"},
		{name: "trailing JSON", content: "{\"version\":1,\"approvals\":[]} {}", mode: 0o600, want: "multiple JSON values"},
		{name: "unknown field", content: "{\"version\":1,\"approvals\":[],\"extra\":true}", mode: 0o600, want: "unknown field"},
		{name: "wrong version", content: "{\"version\":2,\"approvals\":[]}", mode: 0o600, want: "unsupported approval file version"},
		{name: "invalid approval", content: "{\"version\":1,\"approvals\":[{\"tool\":\"test\"}]}", mode: 0o600, want: "command is empty"},
		{name: "duplicate approval", content: duplicateApprovalJSON(), mode: 0o600, want: "duplicates an earlier entry"},
		{name: "writable by others", content: "{\"version\":1,\"approvals\":[]}", mode: 0o622, want: "writable by other users"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && test.name == "writable by others" {
				t.Skip("POSIX mode bits are not authoritative on Windows")
			}
			path := filepath.Join(t.TempDir(), "approvals.json")
			if err := os.WriteFile(path, []byte(test.content), test.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, test.mode); err != nil {
				t.Fatal(err)
			}
			_, err := (FileApprovalStore{Path: path}).Approved(Target{root: root})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Approved() error = %v, want containing %q", err, test.want)
			}
		})
	}

	inside := FileApprovalStore{Path: filepath.Join(root, "approvals.json")}
	if _, err := inside.Approved(Target{root: root}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("Approved() inside repository error = %v, want outside-repository rejection", err)
	}

	largePath := filepath.Join(t.TempDir(), "large.json")
	if err := os.WriteFile(largePath, make([]byte, maxApprovalSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (FileApprovalStore{Path: largePath}).Approved(Target{root: root}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Approved() large file error = %v, want size rejection", err)
	}

	if _, err := (FileApprovalStore{Path: "relative.json"}).Approved(Target{root: root}); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("Approved() relative path error = %v, want absolute-path rejection", err)
	}
}

func TestUnavailableApprovalStore(t *testing.T) {
	t.Parallel()

	store := UnavailableApprovalStore{Cause: errors.New("config directory failed")}
	if approved, err := store.Approved(Target{}); err == nil || approved || !strings.Contains(err.Error(), "config directory failed") {
		t.Fatalf("Approved() = %v, %v", approved, err)
	}
	if err := store.Add(Target{}); err == nil || !strings.Contains(err.Error(), "config directory failed") {
		t.Fatalf("Add() error = %v", err)
	}
	if removed, err := store.Remove("", "git"); err == nil || removed != 0 || !strings.Contains(err.Error(), "config directory failed") {
		t.Fatalf("Remove() = %d, %v", removed, err)
	}
	if _, err := (UnavailableApprovalStore{}).Approved(Target{}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("Approved() error = %v", err)
	}
}

func TestFileApprovalStoreRejectsUnsafeDirectory(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not authoritative on Windows")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil { //nolint:gosec // intentionally unsafe permission fixture
		t.Fatal(err)
	}
	store := FileApprovalStore{Path: filepath.Join(dir, "approvals.json")}
	if _, err := store.Approved(Target{root: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "directory is writable by other users") {
		t.Fatalf("Approved() error = %v, want unsafe-directory rejection", err)
	}
}

func TestFileApprovalStoreRejectsSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup is not reliable without additional privileges")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("{\"version\":1,\"approvals\":[]}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "approvals.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (FileApprovalStore{Path: link}).Approved(Target{root: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("Approved() symlink error = %v, want regular-file rejection", err)
	}
}

func TestFileApprovalStoreRejectsSymlinkedDirectoryInsideRepository(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup is not reliable without additional privileges")
	}
	repository := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(outside, "config")
	if err := os.Symlink(repository, link); err != nil {
		t.Fatal(err)
	}
	store := FileApprovalStore{Path: filepath.Join(link, "awareof", "approvals.json")}
	if _, err := store.Approved(Target{root: repository}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("Approved() error = %v, want symlinked-directory rejection", err)
	}
}

func duplicateApprovalJSON() string {
	path := "/tool"
	if runtime.GOOS == "windows" {
		path = `C:\\tool.exe`
	}
	entry := `{"tool":"test","path":"` + path + `","sha256":"` + strings.Repeat("0", 64) + `","origin":"external"}`
	return `{"version":1,"approvals":[` + entry + `,` + entry + `]}`
}

func testExecutableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

type fakeApprovalStore struct {
	approved    bool
	approvedErr error
	addErr      error
	added       int
	removeErr   error
}

func (s *fakeApprovalStore) Approved(Target) (bool, error) {
	return s.approved, s.approvedErr
}

func (s *fakeApprovalStore) Add(Target) error {
	s.added++
	return s.addErr
}

func (s *fakeApprovalStore) Remove(string, ToolID) (int, error) {
	return 0, s.removeErr
}
