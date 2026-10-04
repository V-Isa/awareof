package safeexec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/V-Isa/awareof/internal/pathutil"
	"github.com/V-Isa/awareof/internal/vocabulary"
)

const (
	approvalVersion    = 1
	maxApprovalSize    = 1024 * 1024
	approvalLockSuffix = ".lock"
)

var approvalMutationMu sync.Mutex

// ToolID is the stable identity of a native entry point used by providers.
type ToolID string

// Tool declares one native entry point that awareof knows how to use safely.
type Tool struct {
	ID                ToolID
	Command           string
	SelectionRequired bool
}

// Origin identifies who controls the resolved entry-point path.
type Origin string

const (
	ExternalOrigin   Origin = "external"
	RepositoryOrigin Origin = "repository"
)

// Target is the exact entry-point identity considered for execution.
type Target struct {
	Tool       ToolID `json:"tool"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Origin     Origin `json:"origin"`
	Repository string `json:"repository,omitempty"`
	root       string
}

// Status describes discovery and approval without executing the tool.
type Status struct {
	Tool    ToolID
	Target  Target
	State   ApprovalState
	Problem *UnavailableError
}

// Selection identifies either a tool's default entry point or one exact path
// derived by a provider without executing it.
type Selection struct {
	Tool ToolID
	Path string
}

// ApprovalState is the normalized user-facing state of a tool approval.
type ApprovalState string

const (
	ApprovedState    ApprovalState = "APPROVED"
	NotApprovedState ApprovalState = "NOT_APPROVED"
	UnavailableState ApprovalState = "UNAVAILABLE"
)

// UnavailableError means that safe execution was deliberately not attempted.
// Providers convert this condition to UNKNOWN rather than guessing.
type UnavailableError struct {
	Tool     ToolID
	Code     string
	Summary  string
	Evidence string
	Action   string
	cause    error
}

func (e *UnavailableError) Error() string {
	if e == nil {
		return ""
	}
	if e.Evidence == "" {
		return e.Summary
	}
	return e.Summary + ": " + e.Evidence
}

func (e *UnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// AsUnavailable returns the safe non-execution explanation carried by err.
func AsUnavailable(err error) (*UnavailableError, bool) {
	var unavailable *UnavailableError
	ok := errors.As(err, &unavailable)
	return unavailable, ok
}

// ApprovalStore records explicit approvals outside the inspected repository.
type ApprovalStore interface {
	Approved(Target) (bool, error)
	Add(Target) error
	Remove(string, ToolID) (int, error)
}

type approvalCatalog interface {
	Targets(string, ToolID) ([]Target, error)
}

// UnavailableApprovalStore preserves parser-only operation when the platform
// approval location cannot be resolved. Native execution remains unavailable.
type UnavailableApprovalStore struct {
	Cause error
}

func (s UnavailableApprovalStore) Approved(Target) (bool, error) {
	return false, s.err()
}

func (s UnavailableApprovalStore) Add(Target) error {
	return s.err()
}

func (s UnavailableApprovalStore) Remove(string, ToolID) (int, error) {
	return 0, s.err()
}

func (s UnavailableApprovalStore) err() error {
	if s.Cause == nil {
		return errors.New("tool approval storage is unavailable")
	}
	return fmt.Errorf("tool approval storage is unavailable: %w", s.Cause)
}

// FileApprovalStore stores exact entry-point identities in one JSON document.
type FileApprovalStore struct {
	Path string
}

type approvalDocument struct {
	Version   int      `json:"version"`
	Approvals []Target `json:"approvals"`
}

// DefaultApprovalPath returns the platform-standard per-user configuration
// path. The repository never controls this location by default.
func DefaultApprovalPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user configuration directory: %w", err)
	}
	return filepath.Join(dir, "awareof", "tool-approvals.json"), nil
}

func (s FileApprovalStore) Approved(target Target) (bool, error) {
	document, err := s.load(target.root)
	if err != nil {
		return false, err
	}
	for _, approval := range document.Approvals {
		if sameApproval(approval, target) {
			return true, nil
		}
	}
	return false, nil
}

func (s FileApprovalStore) Add(target Target) error {
	root := target.root
	return s.mutate(root, func(document *approvalDocument) bool {
		for _, approval := range document.Approvals {
			if sameApproval(approval, target) {
				return false
			}
		}
		target.root = ""
		document.Approvals = append(document.Approvals, target)
		sortApprovals(document.Approvals)
		return true
	})
}

func (s FileApprovalStore) Remove(root string, tool ToolID) (int, error) {
	removed := 0
	err := s.mutate(root, func(document *approvalDocument) bool {
		kept := document.Approvals[:0]
		for _, approval := range document.Approvals {
			if approval.Tool == tool {
				removed++
				continue
			}
			kept = append(kept, approval)
		}
		document.Approvals = kept
		return removed > 0
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// Targets returns approvals that may apply to one tool in this repository.
// Callers must rediscover each path before treating it as approved.
func (s FileApprovalStore) Targets(root string, tool ToolID) ([]Target, error) {
	document, err := s.load(root)
	if err != nil {
		return nil, err
	}
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbsolute)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root symlinks: %w", err)
	}
	targets := make([]Target, 0)
	for _, approval := range document.Approvals {
		if approval.Tool != tool {
			continue
		}
		if approval.Origin == RepositoryOrigin && approval.Repository != rootResolved {
			continue
		}
		targets = append(targets, approval)
	}
	return targets, nil
}

func (s FileApprovalStore) mutate(root string, change func(*approvalDocument) bool) (err error) {
	approvalMutationMu.Lock()
	defer approvalMutationMu.Unlock()

	release, err := s.acquireMutationLock(root)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := release(); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
	}()

	document, err := s.load(root)
	if err != nil {
		return err
	}
	if !change(&document) {
		return nil
	}
	return s.write(root, document)
}

func (s FileApprovalStore) acquireMutationLock(root string) (func() error, error) {
	if s.Path == "" {
		return nil, errors.New("approval file path is empty")
	}
	if err := outsideRepository(root, s.Path); err != nil {
		return nil, err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create approval directory: %w", err)
	}
	if err := secureApprovalDirectory(dir, true); err != nil {
		return nil, err
	}

	lockPath := s.Path + approvalLockSuffix
	file, err := openApprovalLockFile(lockPath)
	if err != nil {
		return nil, err
	}
	if err := lockApprovalFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock approval file: %w", err)
	}
	return func() error {
		unlockErr := unlockApprovalFile(file)
		closeErr := file.Close()
		if unlockErr != nil {
			unlockErr = fmt.Errorf("unlock approval file: %w", unlockErr)
		}
		if closeErr != nil {
			closeErr = fmt.Errorf("close approval lock file: %w", closeErr)
		}
		return errors.Join(unlockErr, closeErr)
	}, nil
}

func openApprovalLockFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if err := validateApprovalLockFile(info); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect approval lock file: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // fixed user configuration path
	if err != nil {
		return nil, fmt.Errorf("open approval lock file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect opened approval lock file: %w", err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect approval lock file after open: %w", err)
	}
	if !os.SameFile(info, pathInfo) {
		_ = file.Close()
		return nil, errors.New("approval lock file changed while opening")
	}
	if err := validateApprovalLockFile(pathInfo); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validateApprovalLockFile(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("approval lock file is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return errors.New("approval lock file is writable by other users")
	}
	return nil
}

func (s FileApprovalStore) load(root string) (approvalDocument, error) {
	if s.Path == "" {
		return approvalDocument{}, errors.New("approval file path is empty")
	}
	if err := outsideRepository(root, s.Path); err != nil {
		return approvalDocument{}, err
	}
	if err := secureApprovalDirectory(filepath.Dir(s.Path), false); err != nil {
		return approvalDocument{}, err
	}
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return approvalDocument{Version: approvalVersion, Approvals: []Target{}}, nil
	}
	if err != nil {
		return approvalDocument{}, fmt.Errorf("inspect approval file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return approvalDocument{}, errors.New("approval file is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return approvalDocument{}, errors.New("approval file is writable by other users")
	}

	file, err := os.Open(s.Path)
	if err != nil {
		return approvalDocument{}, fmt.Errorf("open approval file: %w", err)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxApprovalSize+1))
	if err != nil {
		_ = file.Close()
		return approvalDocument{}, fmt.Errorf("read approval file: %w", err)
	}
	if err := file.Close(); err != nil {
		return approvalDocument{}, fmt.Errorf("close approval file: %w", err)
	}
	if len(content) > maxApprovalSize {
		return approvalDocument{}, fmt.Errorf("approval file exceeds %d bytes", maxApprovalSize)
	}
	var document approvalDocument
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return approvalDocument{}, fmt.Errorf("parse approval file: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return approvalDocument{}, err
	}
	if document.Version != approvalVersion {
		return approvalDocument{}, fmt.Errorf("unsupported approval file version %d", document.Version)
	}
	if document.Approvals == nil {
		document.Approvals = []Target{}
	}
	seen := make(map[string]struct{}, len(document.Approvals))
	for index, approval := range document.Approvals {
		if err := validateApproval(approval); err != nil {
			return approvalDocument{}, fmt.Errorf("approval %d: %w", index, err)
		}
		key := approvalKey(approval)
		if _, exists := seen[key]; exists {
			return approvalDocument{}, fmt.Errorf("approval %d duplicates an earlier entry", index)
		}
		seen[key] = struct{}{}
	}
	return document, nil
}

func (s FileApprovalStore) write(root string, document approvalDocument) error {
	if err := outsideRepository(root, s.Path); err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create approval directory: %w", err)
	}
	if err := secureApprovalDirectory(dir, true); err != nil {
		return err
	}
	content, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode approval file: %w", err)
	}
	content = append(content, '\n')
	temporary, err := os.CreateTemp(dir, ".tool-approvals-*")
	if err != nil {
		return fmt.Errorf("create temporary approval file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary approval file: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary approval file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary approval file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary approval file: %w", err)
	}
	if runtime.GOOS == "windows" {
		if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("replace approval file: %w", err)
		}
	}
	if err := os.Rename(temporaryPath, s.Path); err != nil {
		return fmt.Errorf("replace approval file: %w", err)
	}
	removeTemporary = false
	return nil
}

// Manager discovers exact entry-point identities and enforces approval.
type Manager struct {
	tools     map[ToolID]Tool
	overrides map[ToolID]string
	store     ApprovalStore
}

func NewManager(tools []Tool, store ApprovalStore) (*Manager, error) {
	if store == nil {
		return nil, errors.New("approval store is nil")
	}
	manager := &Manager{
		tools:     make(map[ToolID]Tool, len(tools)),
		overrides: make(map[ToolID]string),
		store:     store,
	}
	for _, tool := range tools {
		if err := validateTool(tool); err != nil {
			return nil, err
		}
		if _, exists := manager.tools[tool.ID]; exists {
			return nil, fmt.Errorf("duplicate tool %q", tool.ID)
		}
		manager.tools[tool.ID] = tool
	}
	return manager, nil
}

// SetOverrides replaces entry-point selections for the current invocation.
func (m *Manager) SetOverrides(overrides map[ToolID]string) error {
	if m == nil {
		return errors.New("tool manager is nil")
	}
	selected := make(map[ToolID]string, len(overrides))
	for id, path := range overrides {
		if _, exists := m.tools[id]; !exists {
			return fmt.Errorf("unknown tool %q", id)
		}
		if path == "" {
			return fmt.Errorf("tool %q entry-point path is empty", id)
		}
		selected[id] = path
	}
	m.overrides = selected
	return nil
}

// Resolve returns an approved entry point or a safe non-execution error.
func (m *Manager) Resolve(root string, id ToolID) (Target, error) {
	target, err := m.Discover(root, id)
	return m.resolveTarget(target, err)
}

// ResolveAt returns one approved provider-derived entry point.
func (m *Manager) ResolveAt(root string, id ToolID, path string) (Target, error) {
	target, err := m.DiscoverAt(root, id, path)
	return m.resolveTarget(target, err)
}

// ApprovedTargets returns current approved identities for one tool. Stale,
// missing, or changed approval records are not returned.
func (m *Manager) ApprovedTargets(root string, id ToolID) ([]Target, error) {
	if m == nil {
		return nil, errors.New("tool manager is nil")
	}
	if _, exists := m.tools[id]; !exists {
		return nil, fmt.Errorf("unknown tool %q", id)
	}
	catalog, ok := m.store.(approvalCatalog)
	if !ok {
		return []Target{}, nil
	}
	stored, err := catalog.Targets(root, id)
	if err != nil {
		return nil, fmt.Errorf("list tool %q approvals: %w", id, err)
	}
	targets := make([]Target, 0, len(stored))
	for _, approval := range stored {
		current, err := m.DiscoverAt(root, id, approval.Path)
		if err != nil || !sameApproval(current, approval) {
			continue
		}
		targets = append(targets, current)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Path < targets[j].Path })
	return targets, nil
}

func (m *Manager) resolveTarget(target Target, err error) (Target, error) {
	if err != nil {
		return Target{}, err
	}
	id := target.Tool
	approved, err := m.store.Approved(target)
	if err != nil {
		return Target{}, &UnavailableError{
			Tool:     id,
			Code:     "tool/approval-unavailable",
			Summary:  fmt.Sprintf("cannot verify approval for tool %q", id),
			Evidence: err.Error(),
			Action:   "repair the per-user awareof approval file, then retry",
			cause:    err,
		}
	}
	if !approved {
		action := "run awareof --setup"
		if _, overridden := m.overrides[id]; overridden {
			action += " with the same --tool setting"
		}
		return Target{}, &UnavailableError{
			Tool:     id,
			Code:     "tool/not-approved",
			Summary:  fmt.Sprintf("tool %q is not approved", id),
			Evidence: fmt.Sprintf("resolved entry point %q has sha256 %s", target.Path, target.SHA256),
			Action:   action,
		}
	}
	return target, nil
}

// Discover identifies a tool without executing it.
func (m *Manager) Discover(root string, id ToolID) (Target, error) {
	if m == nil {
		return Target{}, errors.New("tool manager is nil")
	}
	tool, exists := m.tools[id]
	if !exists {
		return Target{}, fmt.Errorf("unknown tool %q", id)
	}
	candidate := tool.Command
	_, overridden := m.overrides[id]
	if tool.SelectionRequired && !overridden {
		return Target{}, &UnavailableError{
			Tool:    id,
			Code:    "tool/selection-required",
			Summary: fmt.Sprintf("tool %q requires an explicit entry-point selection", id),
			Action:  fmt.Sprintf("select it with --tool %s=/absolute/path", id),
		}
	}
	if override, ok := m.overrides[id]; ok {
		candidate = override
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root, candidate)
		}
	}
	return m.discover(root, id, candidate, !overridden)
}

// DiscoverAt identifies one provider-derived entry point without executing it.
func (m *Manager) DiscoverAt(root string, id ToolID, candidate string) (Target, error) {
	if m == nil {
		return Target{}, errors.New("tool manager is nil")
	}
	if _, exists := m.tools[id]; !exists {
		return Target{}, fmt.Errorf("unknown tool %q", id)
	}
	if candidate == "" {
		return Target{}, fmt.Errorf("tool %q entry-point path is empty", id)
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	return m.discover(root, id, candidate, false)
}

func (m *Manager) discover(root string, id ToolID, candidate string, usePath bool) (Target, error) {
	path := candidate
	var err error
	if usePath {
		path, err = exec.LookPath(candidate)
	}
	if err != nil {
		return Target{}, &UnavailableError{
			Tool:     id,
			Code:     "tool/not-found",
			Summary:  fmt.Sprintf("tool %q is not available", id),
			Evidence: err.Error(),
			Action:   fmt.Sprintf("install the tool or select it with --tool %s=PATH", id),
			cause:    err,
		}
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return Target{}, fmt.Errorf("resolve tool %q path: %w", id, err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return Target{}, &UnavailableError{
			Tool:     id,
			Code:     "tool/unresolvable",
			Summary:  fmt.Sprintf("tool %q cannot be resolved safely", id),
			Evidence: err.Error(),
			Action:   "repair or select a valid entry point, then retry",
			cause:    err,
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return Target{}, fmt.Errorf("inspect tool %q: %w", id, err)
	}
	if !info.Mode().IsRegular() {
		return Target{}, &UnavailableError{
			Tool:     id,
			Code:     "tool/not-regular",
			Summary:  fmt.Sprintf("tool %q entry point is not a regular file", id),
			Evidence: fmt.Sprintf("resolved path is %q", path),
			Action:   "select a regular entry-point file, then retry",
		}
	}
	digest, err := fileSHA256(path)
	if err != nil {
		return Target{}, &UnavailableError{
			Tool:     id,
			Code:     "tool/unreadable",
			Summary:  fmt.Sprintf("tool %q cannot be identified safely", id),
			Evidence: err.Error(),
			Action:   "repair or select a readable entry point, then retry",
			cause:    err,
		}
	}

	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return Target{}, fmt.Errorf("resolve repository root: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbsolute)
	if err != nil {
		return Target{}, fmt.Errorf("resolve repository root symlinks: %w", err)
	}
	target := Target{
		Tool:   id,
		Path:   path,
		SHA256: digest,
		Origin: ExternalOrigin,
		root:   rootResolved,
	}
	if pathutil.Within(rootAbsolute, path) || pathutil.Within(rootResolved, path) {
		target.Origin = RepositoryOrigin
		target.Repository = rootResolved
	}
	return target, nil
}

func (m *Manager) Approve(root string, id ToolID) (Target, error) {
	target, err := m.Discover(root, id)
	if err != nil {
		return Target{}, err
	}
	if err := m.store.Add(target); err != nil {
		return Target{}, fmt.Errorf("approve tool %q: %w", id, err)
	}
	return target, nil
}

// ApproveTarget approves a previously discovered identity only when discovery
// still produces that exact target. Setup uses this to bind confirmation to
// the entry-point identity shown to the user.
func (m *Manager) ApproveTarget(root string, expected Target) (Target, error) {
	target, err := m.DiscoverAt(root, expected.Tool, expected.Path)
	if err != nil {
		return Target{}, err
	}
	if !sameApproval(target, expected) {
		return Target{}, &UnavailableError{
			Tool:     expected.Tool,
			Code:     "tool/identity-changed",
			Summary:  fmt.Sprintf("tool %q changed during setup", expected.Tool),
			Evidence: fmt.Sprintf("the displayed entry-point identity no longer matches %q", target.Path),
			Action:   "review the new identity and run awareof --setup again",
		}
	}
	if err := m.store.Add(target); err != nil {
		return Target{}, fmt.Errorf("approve tool %q: %w", expected.Tool, err)
	}
	return target, nil
}

func (m *Manager) Revoke(root string, id ToolID) (int, error) {
	if m == nil {
		return 0, errors.New("tool manager is nil")
	}
	if _, exists := m.tools[id]; !exists {
		return 0, fmt.Errorf("unknown tool %q", id)
	}
	removed, err := m.store.Remove(root, id)
	if err != nil {
		return 0, fmt.Errorf("revoke tool %q: %w", id, err)
	}
	return removed, nil
}

func (m *Manager) Statuses(root string) []Status {
	ids := make([]ToolID, 0, len(m.tools))
	for id := range m.tools {
		ids = append(ids, id)
	}
	return m.StatusesFor(root, ids)
}

// StatusesFor reports discovery and approval only for the requested tools.
func (m *Manager) StatusesFor(root string, requested []ToolID) []Status {
	unique := make(map[ToolID]struct{}, len(requested))
	for _, id := range requested {
		unique[id] = struct{}{}
	}
	ids := make([]ToolID, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	statuses := make([]Status, 0, len(ids))
	for _, id := range ids {
		target, err := m.Discover(root, id)
		if err != nil {
			problem, ok := AsUnavailable(err)
			if !ok {
				problem = &UnavailableError{Tool: id, Code: "tool/discovery-failed", Summary: fmt.Sprintf("cannot inspect tool %q", id), Evidence: err.Error()}
			}
			statuses = append(statuses, Status{Tool: id, State: UnavailableState, Problem: problem})
			continue
		}
		approved, err := m.store.Approved(target)
		if err != nil {
			statuses = append(statuses, Status{Tool: id, Target: target, State: UnavailableState, Problem: &UnavailableError{
				Tool: id, Code: "tool/approval-unavailable", Summary: fmt.Sprintf("cannot verify approval for tool %q", id), Evidence: err.Error(),
			}})
			continue
		}
		state := NotApprovedState
		if approved {
			state = ApprovedState
		}
		statuses = append(statuses, Status{Tool: id, Target: target, State: state})
	}
	return statuses
}

// StatusesForSelections reports approval for default or provider-derived entry points.
func (m *Manager) StatusesForSelections(root string, requested []Selection) []Status {
	unique := make(map[Selection]struct{}, len(requested))
	for _, selection := range requested {
		unique[selection] = struct{}{}
	}
	selections := make([]Selection, 0, len(unique))
	for selection := range unique {
		selections = append(selections, selection)
	}
	sort.Slice(selections, func(i, j int) bool {
		if selections[i].Tool != selections[j].Tool {
			return selections[i].Tool < selections[j].Tool
		}
		return selections[i].Path < selections[j].Path
	})
	statuses := make([]Status, 0, len(selections))
	for _, selection := range selections {
		var target Target
		var err error
		if selection.Path == "" {
			target, err = m.Discover(root, selection.Tool)
		} else {
			target, err = m.DiscoverAt(root, selection.Tool, selection.Path)
		}
		if err != nil {
			problem, ok := AsUnavailable(err)
			if !ok {
				problem = &UnavailableError{Tool: selection.Tool, Code: "tool/discovery-failed", Summary: fmt.Sprintf("cannot inspect tool %q", selection.Tool), Evidence: err.Error()}
			}
			statuses = append(statuses, Status{Tool: selection.Tool, State: UnavailableState, Problem: problem})
			continue
		}
		approved, err := m.store.Approved(target)
		if err != nil {
			statuses = append(statuses, Status{Tool: selection.Tool, Target: target, State: UnavailableState, Problem: &UnavailableError{
				Tool: selection.Tool, Code: "tool/approval-unavailable", Summary: fmt.Sprintf("cannot verify approval for tool %q", selection.Tool), Evidence: err.Error(),
			}})
			continue
		}
		state := NotApprovedState
		if approved {
			state = ApprovedState
		}
		statuses = append(statuses, Status{Tool: selection.Tool, Target: target, State: state})
	}
	return statuses
}

func validateTool(tool Tool) error {
	if tool.ID == "" {
		return errors.New("tool id is empty")
	}
	if !vocabulary.ValidIdentifier(string(tool.ID)) {
		return fmt.Errorf("invalid tool id %q", tool.ID)
	}
	if tool.SelectionRequired && tool.Command != "" {
		return fmt.Errorf("tool %q cannot have both a default command and required selection", tool.ID)
	}
	if !tool.SelectionRequired && tool.Command == "" {
		return fmt.Errorf("tool %q command is empty", tool.ID)
	}
	return nil
}

func validateApproval(approval Target) error {
	if err := validateTool(Tool{ID: approval.Tool, Command: approval.Path}); err != nil {
		return err
	}
	if !filepath.IsAbs(approval.Path) {
		return errors.New("entry-point path is not absolute")
	}
	if len(approval.SHA256) != sha256.Size*2 {
		return errors.New("sha256 digest has invalid length")
	}
	if _, err := hex.DecodeString(approval.SHA256); err != nil {
		return fmt.Errorf("sha256 digest is invalid: %w", err)
	}
	switch approval.Origin {
	case ExternalOrigin:
		if approval.Repository != "" {
			return errors.New("external approval has a repository")
		}
	case RepositoryOrigin:
		if !filepath.IsAbs(approval.Repository) {
			return errors.New("repository approval has no absolute repository")
		}
		if !pathutil.Within(approval.Repository, approval.Path) {
			return errors.New("repository approval entry point is outside its repository")
		}
	default:
		return fmt.Errorf("invalid entry-point origin %q", approval.Origin)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path) //nolint:gosec // path is the exact entry point selected for identity verification
	if err != nil {
		return "", fmt.Errorf("open entry point: %w", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("hash entry point: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close entry point: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse approval file trailing data: %w", err)
	}
	return errors.New("approval file contains multiple JSON values")
}

func sameApproval(left, right Target) bool {
	return left.Tool == right.Tool && left.Path == right.Path && left.SHA256 == right.SHA256 &&
		left.Origin == right.Origin && left.Repository == right.Repository
}

func approvalKey(target Target) string {
	return strings.Join([]string{string(target.Tool), target.Path, target.SHA256, string(target.Origin), target.Repository}, "\x00")
}

func sortApprovals(approvals []Target) {
	sort.Slice(approvals, func(i, j int) bool { return approvalKey(approvals[i]) < approvalKey(approvals[j]) })
}

func outsideRepository(root, path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("approval file path must be absolute")
	}
	if root == "" {
		return nil
	}
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	pathAbsolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve approval file path: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbsolute)
	if err != nil {
		return fmt.Errorf("resolve repository root symlinks: %w", err)
	}
	pathResolved, err := resolveExistingPrefix(pathAbsolute)
	if err != nil {
		return fmt.Errorf("resolve approval file path symlinks: %w", err)
	}
	if pathutil.Within(rootAbsolute, pathAbsolute) || pathutil.Within(rootResolved, pathResolved) {
		return errors.New("approval file must be outside the inspected repository")
	}
	return nil
}

func secureApprovalDirectory(path string, required bool) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect approval directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("approval file parent is not a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return errors.New("approval directory is writable by other users")
	}
	return nil
}

func resolveExistingPrefix(path string) (string, error) {
	current := filepath.Clean(path)
	var suffix []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing path prefix for %q", path)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}
