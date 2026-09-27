// Package eas evaluates membership in EAS Build source archives.
package eas

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/gitexec"
	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"

	"github.com/titanous/json5"
)

const (
	providerID          scope.ProviderID = "eas"
	rootInstanceID      scope.InstanceID = "eas"
	discoveryInstanceID scope.InstanceID = "discovery"
	modeledEASVersion                    = "24.3.0"
	modeledEASReference                  = "EAS CLI " + modeledEASVersion
	maxConfigSize                        = 4 * 1024 * 1024
	maxScanEntries                       = 100_000
	maxGitBatchBytes                     = 24 * 1024
	maxGitBatchPaths                     = 512
)

var errScanLimit = errors.New("EAS repository scan limit reached")

// Provider evaluates the source archive uploaded by the current default EAS
// Git workflow. It never invokes EAS or repository-controlled JavaScript.
type Provider struct {
	runner         safeexec.CommandRunner
	lookupEnv      func(string) (string, bool)
	maxScanEntries int
}

func New(runner safeexec.CommandRunner) *Provider {
	return &Provider{runner: runner, lookupEnv: os.LookupEnv, maxScanEntries: maxScanEntries}
}

func (*Provider) ID() scope.ProviderID {
	return providerID
}

func (p *Provider) Detect(ctx context.Context, repo provider.Repository) (_ []provider.Instance, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("detect EAS projects: %w", err)
	}
	if repo.Root == "" {
		return nil, errors.New("repository root is empty")
	}

	root, err := os.OpenRoot(repo.Root)
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close repository root: %w", err))
		}
	}()

	scanLimit := p.maxScanEntries
	if scanLimit <= 0 {
		scanLimit = maxScanEntries
	}
	facts, err := inspectRepository(ctx, root, scanLimit)
	if err != nil {
		return nil, err
	}
	if len(facts.configs) == 0 {
		if facts.discoveryUnavailable != nil {
			return []provider.Instance{easInstance{
				descriptor: provider.InstanceDescriptor{
					Provider: providerID,
					ID:       discoveryInstanceID,
					Label:    "EAS projects (discovery unresolved)",
				},
				unavailable: []scope.Explanation{*facts.discoveryUnavailable},
			}}, nil
		}
		return nil, nil
	}

	commonUnavailable := append([]scope.Explanation(nil), facts.unavailable...)
	if facts.discoveryUnavailable != nil {
		commonUnavailable = append(commonUnavailable, *facts.discoveryUnavailable)
	}
	if !facts.gitWorktree {
		commonUnavailable = addUnavailable(commonUnavailable, "eas/git-worktree-required", "the default EAS Git workflow requires a Git worktree")
	}
	if p.lookupEnv != nil {
		if value, ok := p.lookupEnv("EAS_NO_VCS"); ok && value != "" {
			commonUnavailable = addUnavailable(commonUnavailable, "eas/no-vcs-unsupported", "EAS_NO_VCS is set; no-VCS archive semantics are not supported yet")
		}
	}

	instances := make([]provider.Instance, 0, len(facts.configs))
	for _, configPath := range facts.configs {
		current := easInstance{
			descriptor: provider.InstanceDescriptor{
				Provider: providerID,
				ID:       instanceID(configPath),
				Label:    instanceLabel(configPath),
			},
			inactiveEASIgnore: append([]scope.Path(nil), facts.nestedEASIgnore...),
			unavailable:       append([]scope.Explanation(nil), commonUnavailable...),
		}
		if facts.rootEASIgnore {
			current.ignoreFile = ".easignore"
		} else {
			current.ignoreFile = ".gitignore"
			current.perDirectory = true
			current.ambiguousNegation = append([]scope.Path(nil), facts.nestedNegation...)
		}

		content, readErr := readLimited(root, configPath)
		if readErr != nil {
			current.unavailable = addUnavailable(
				current.unavailable,
				"eas/config-unreadable",
				fmt.Sprintf("cannot read %s safely", configPath),
				readErr.Error(),
			)
			instances = append(instances, current)
			continue
		}
		config, err := parseConfig(content)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", configPath, err)
		}
		if config.CLI.RequireCommit {
			current.unavailable = addUnavailable(current.unavailable, "eas/require-commit-unsupported", "cli.requireCommit archive semantics are not supported yet")
		}
		instances = append(instances, current)
	}
	return instances, nil
}

func (p *Provider) Evaluate(
	ctx context.Context,
	_ provider.EvaluationContext,
	repo provider.Repository,
	instance provider.Instance,
	paths []scope.Path,
) (_ []scope.Result, returnErr error) {
	current, ok := instance.(easInstance)
	if !ok {
		return nil, errors.New("unsupported EAS instance")
	}
	descriptor := current.Descriptor()
	if descriptor.Provider != providerID || descriptor.ID == "" {
		return nil, fmt.Errorf("unsupported instance %q", descriptor.ID)
	}
	if len(paths) == 0 {
		return []scope.Result{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("evaluate EAS archive: %w", err)
	}
	if len(current.unavailable) != 0 {
		return unknownResults(paths, descriptor.ID, combinedUnavailable(current.unavailable)), nil
	}
	if p.runner == nil {
		return nil, errors.New("command runner is nil")
	}
	if repo.Root == "" {
		return nil, errors.New("repository root is empty")
	}

	ignored, err := p.ignoredPaths(ctx, repo.Root, current, paths)
	if err != nil {
		if unavailable, ok := safeexec.AsUnavailable(err); ok {
			return unavailableToolResults(paths, descriptor.ID, unavailable), nil
		}
		return nil, err
	}
	root, err := os.OpenRoot(repo.Root)
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close repository root: %w", err))
		}
	}()

	note := inactiveIgnoreNote(current.inactiveEASIgnore)
	results := make([]scope.Result, 0, len(paths))
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("evaluate EAS archive: %w", err)
		}
		state, reason := classify(root, name, ignored, current, note)
		results = append(results, scope.Result{
			Path:        name,
			Provider:    providerID,
			Instance:    descriptor.ID,
			State:       state,
			Explanation: reason,
			Provenance:  scope.Provenance{Method: scope.SafeNative, Tool: "git", Reference: modeledEASReference},
		})
	}
	return results, nil
}

func unavailableToolResults(paths []scope.Path, id scope.InstanceID, unavailable *safeexec.UnavailableError) []scope.Result {
	results := make([]scope.Result, 0, len(paths))
	for _, path := range paths {
		results = append(results, scope.Result{
			Path:     path,
			Provider: providerID,
			Instance: id,
			State:    scope.Unknown,
			Explanation: scope.Explanation{
				Code:     unavailable.Code,
				Summary:  unavailable.Summary,
				Evidence: unavailable.Evidence,
				Action:   unavailable.Action,
			},
			Provenance: scope.Provenance{Method: scope.Unavailable, Tool: "git", Reference: modeledEASReference},
		})
	}
	return results
}

type easInstance struct {
	descriptor        provider.InstanceDescriptor
	inactiveEASIgnore []scope.Path
	ambiguousNegation []scope.Path
	ignoreFile        scope.Path
	perDirectory      bool
	unavailable       []scope.Explanation
}

func (i easInstance) Descriptor() provider.InstanceDescriptor {
	return i.descriptor
}

type repositoryFacts struct {
	configs              []string
	nestedEASIgnore      []scope.Path
	gitWorktree          bool
	rootEASIgnore        bool
	nestedNegation       []scope.Path
	unavailable          []scope.Explanation
	discoveryUnavailable *scope.Explanation
}

func inspectRepository(ctx context.Context, root *os.Root, maxEntries int) (repositoryFacts, error) {
	var facts repositoryFacts
	entries := 0
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > maxEntries {
			return errScanLimit
		}
		if entry.IsDir() && name != "." {
			base := entry.Name()
			if name == ".git" {
				facts.gitWorktree = true
			}
			if base == "node_modules" || strings.HasPrefix(base, ".") {
				return fs.SkipDir
			}
		}
		if name == ".git" {
			facts.gitWorktree = true
		}
		if entry.IsDir() {
			return nil
		}

		clean := path.Clean(name)
		switch entry.Name() {
		case "eas.json":
			facts.configs = append(facts.configs, clean)
		case ".easignore":
			if clean == ".easignore" {
				facts.rootEASIgnore = true
				if entry.Type()&fs.ModeSymlink != 0 {
					facts.unavailable = addUnavailable(facts.unavailable, "eas/ignore-unreadable", ".easignore is a symlink and cannot be matched safely")
					return nil
				}
				if _, err := readLimited(root, clean); err != nil {
					facts.unavailable = addUnavailable(facts.unavailable, "eas/ignore-unreadable", "cannot read .easignore safely", err.Error())
				}
			} else {
				facts.nestedEASIgnore = append(facts.nestedEASIgnore, scope.Path(clean))
			}
		case ".gitignore":
			if entry.Type()&fs.ModeSymlink != 0 {
				facts.unavailable = addUnavailable(facts.unavailable, "eas/ignore-unreadable", fmt.Sprintf("%s is a symlink whose EAS and Git semantics can differ", clean))
				return nil
			}
			content, err := readLimited(root, clean)
			if err != nil {
				facts.unavailable = addUnavailable(facts.unavailable, "eas/ignore-unreadable", fmt.Sprintf("cannot read %s safely", clean), err.Error())
				return nil
			}
			if clean != ".gitignore" && hasPotentialNegation(content) {
				facts.nestedNegation = append(facts.nestedNegation, scope.Path(clean))
			}
		}
		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return repositoryFacts{}, fmt.Errorf("scan for EAS configuration: %w", ctxErr)
		}
		code := "eas/discovery-unavailable"
		summary := "EAS project discovery could not be completed safely"
		evidence := err.Error()
		if errors.Is(err, errScanLimit) {
			code = "eas/discovery-limit"
			summary = fmt.Sprintf("EAS project discovery exceeds %d repository entries", maxEntries)
			evidence = "the repository may contain additional EAS projects"
		}
		facts.discoveryUnavailable = &scope.Explanation{
			Code:     code,
			Summary:  summary,
			Evidence: evidence,
			Action:   "use --root to inspect a smaller project directory",
		}
	}
	sort.Strings(facts.configs)
	sort.Slice(facts.nestedEASIgnore, func(i, j int) bool { return facts.nestedEASIgnore[i] < facts.nestedEASIgnore[j] })
	sort.Slice(facts.nestedNegation, func(i, j int) bool { return facts.nestedNegation[i] < facts.nestedNegation[j] })
	return facts, nil
}

func readLimited(root *os.Root, name string) (_ []byte, returnErr error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("file is not a regular file")
	}
	if before.Size() > maxConfigSize {
		return nil, fmt.Errorf("file exceeds %d bytes", maxConfigSize)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, errors.New("file changed while it was being inspected")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxConfigSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxConfigSize {
		return nil, fmt.Errorf("file exceeds %d bytes", maxConfigSize)
	}
	return content, nil
}

type easConfig struct {
	CLI struct {
		RequireCommit bool `json:"requireCommit"`
	} `json:"cli"`
}

func parseConfig(content []byte) (easConfig, error) {
	var config easConfig
	decoder := json5.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&config); err != nil {
		return easConfig{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return easConfig{}, errors.New("multiple JSON values")
		}
		return easConfig{}, err
	}
	return config, nil
}

func instanceID(configPath string) scope.InstanceID {
	dir := path.Dir(configPath)
	if dir == "." {
		return rootInstanceID
	}
	return scope.InstanceID("project/" + dir)
}

func instanceLabel(configPath string) string {
	dir := path.Dir(configPath)
	if dir == "." {
		return "EAS project ."
	}
	return "EAS project " + dir
}

func hasPotentialNegation(content []byte) bool {
	for index, line := range strings.Split(string(content), "\n") {
		if index == 0 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		line = strings.TrimLeft(line, " \t\r")
		if strings.HasPrefix(line, "!") {
			return true
		}
	}
	return false
}

func addUnavailable(existing []scope.Explanation, code, summary string, evidence ...string) []scope.Explanation {
	reason := scope.Explanation{Code: code, Summary: summary}
	if len(evidence) != 0 {
		reason.Evidence = evidence[0]
	}
	return append(existing, reason)
}

func combinedUnavailable(reasons []scope.Explanation) scope.Explanation {
	if len(reasons) == 1 {
		return reasons[0]
	}
	details := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		detail := reason.Code + ": " + reason.Summary
		if reason.Evidence != "" {
			detail += ": " + reason.Evidence
		}
		details = append(details, detail)
	}
	return scope.Explanation{
		Code:     "eas/multiple-uncertainties",
		Summary:  "multiple conditions prevent reliable EAS source archive evaluation",
		Evidence: strings.Join(details, "; "),
	}
}

func unknownResults(paths []scope.Path, id scope.InstanceID, reason scope.Explanation) []scope.Result {
	results := make([]scope.Result, 0, len(paths))
	for _, name := range paths {
		results = append(results, scope.Result{
			Path:        name,
			Provider:    providerID,
			Instance:    id,
			State:       scope.Unknown,
			Explanation: reason,
			Provenance:  scope.Provenance{Method: scope.Unavailable, Reference: modeledEASReference},
		})
	}
	return results
}

func (p *Provider) ignoredPaths(ctx context.Context, root string, instance easInstance, paths []scope.Path) (map[scope.Path]struct{}, error) {
	queryPaths := make([]scope.Path, 0, len(paths))
	for _, name := range paths {
		if isNodeModules(name) {
			continue
		}
		queryPaths = append(queryPaths, name)
	}
	ignored := make(map[scope.Path]struct{})
	if len(queryPaths) == 0 {
		return ignored, nil
	}
	baseArgs := []string{
		"--no-pager",
		"--literal-pathspecs",
		"-c", "core.fsmonitor=false",
		// EAS uses the node-ignore default, which matches case-insensitively.
		"-c", "core.ignorecase=true",
		"ls-files",
		"--cached",
		"--others",
		"--ignored",
		"--full-name",
		"-z",
	}
	if instance.perDirectory {
		baseArgs = append(baseArgs, "--exclude-per-directory=.gitignore")
	} else {
		baseArgs = append(baseArgs, "--exclude-from="+string(instance.ignoreFile))
	}
	for _, batch := range gitPathBatches(queryPaths) {
		args := append(append([]string(nil), baseArgs...), "--")
		wanted := make(map[scope.Path]struct{}, len(batch))
		for _, name := range batch {
			args = append(args, string(name))
			wanted[name] = struct{}{}
		}
		environment := gitexec.Environment()
		environment["GIT_CONFIG_GLOBAL"] = os.DevNull
		environment["GIT_CONFIG_NOSYSTEM"] = "1"
		response, err := p.runner.Run(ctx, safeexec.Request{
			Root:     root,
			Tool:     "git",
			Args:     args,
			Env:      environment,
			UnsetEnv: gitexec.UnsetEnvironment(),
		})
		if err != nil {
			return nil, fmt.Errorf("evaluate EAS Git archive rules: %w", err)
		}
		if response.ExitCode != 0 {
			return nil, commandError(response)
		}
		for _, record := range splitNUL(response.Stdout) {
			if !utf8.ValidString(record) {
				return nil, errors.New("evaluate EAS Git archive rules: Git returned invalid UTF-8")
			}
			name := scope.Path(record)
			if _, ok := wanted[name]; !ok {
				return nil, fmt.Errorf("evaluate EAS Git archive rules: Git returned unexpected path %q", record)
			}
			if _, duplicate := ignored[name]; duplicate {
				return nil, fmt.Errorf("evaluate EAS Git archive rules: Git returned duplicate path %q", record)
			}
			ignored[name] = struct{}{}
		}
	}
	return ignored, nil
}

func gitPathBatches(paths []scope.Path) [][]scope.Path {
	var batches [][]scope.Path
	for len(paths) > 0 {
		count := 0
		bytes := 0
		for count < len(paths) && count < maxGitBatchPaths {
			next := len(paths[count]) + 1
			if count > 0 && bytes+next > maxGitBatchBytes {
				break
			}
			bytes += next
			count++
		}
		batches = append(batches, paths[:count])
		paths = paths[count:]
	}
	return batches
}

func classify(root *os.Root, name scope.Path, ignored map[scope.Path]struct{}, instance easInstance, note string) (scope.State, scope.Explanation) {
	if isNodeModules(name) {
		return scope.Out, scope.Explanation{
			Code:    "eas/default-excluded",
			Summary: "excluded from EAS source archive by the default node_modules rule" + note,
		}
	}
	_, err := root.Lstat(string(name))
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return scope.Out, scope.Explanation{
			Code:    "eas/not-present",
			Summary: "not present in the current working tree, so it is absent from the EAS source archive" + note,
		}
	default:
		return scope.Unknown, scope.Explanation{
			Code:    "eas/path-uninspectable",
			Summary: fmt.Sprintf("cannot inspect path safely: %v", err),
		}
	}
	if _, ok := ignored[name]; ok {
		return scope.Out, scope.Explanation{
			Code:    excludedReasonCode(instance),
			Summary: fmt.Sprintf("excluded from EAS source archive by active %s rules%s", instance.ignoreDescription(), note),
		}
	}
	if source, ok := applicableNegation(name, instance.ambiguousNegation); ok {
		return scope.Unknown, scope.Explanation{
			Code:    "eas/nested-negation-ambiguous",
			Summary: fmt.Sprintf("%s may re-include this path differently because current EAS cross-file ignore precedence differs from Git", source),
		}
	}
	return scope.In, scope.Explanation{
		Code:    "eas/included",
		Summary: "included in EAS source archive by current Git-mode rules" + note,
	}
}

func excludedReasonCode(instance easInstance) string {
	if instance.perDirectory {
		return "eas/gitignore-excluded"
	}
	return "eas/easignore-excluded"
}

func (i easInstance) ignoreDescription() string {
	if i.perDirectory {
		return ".gitignore"
	}
	return string(i.ignoreFile)
}

func applicableNegation(name scope.Path, ignoreFiles []scope.Path) (scope.Path, bool) {
	for _, ignoreFile := range ignoreFiles {
		dir := scope.Path(path.Dir(string(ignoreFile)))
		if name == dir || strings.HasPrefix(string(name), string(dir)+"/") {
			return ignoreFile, true
		}
	}
	return "", false
}

func isNodeModules(name scope.Path) bool {
	for _, segment := range strings.Split(string(name), "/") {
		if segment == "node_modules" {
			return true
		}
	}
	return false
}

func inactiveIgnoreNote(names []scope.Path) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("; %s is inactive because current EAS Git mode only reads .easignore at the Git root", names[0])
	default:
		return fmt.Sprintf("; %d nested .easignore files are inactive, including %s, because current EAS Git mode only reads .easignore at the Git root", len(names), names[0])
	}
}

func commandError(response safeexec.Response) error {
	detail := strings.TrimSpace(string(response.Stderr))
	if detail == "" {
		return fmt.Errorf("evaluate EAS Git archive rules: Git exited with status %d", response.ExitCode)
	}
	return fmt.Errorf("evaluate EAS Git archive rules: Git exited with status %d: %s", response.ExitCode, detail)
}

func splitNUL(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	fields := strings.Split(string(data), "\x00")
	if fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	return fields
}
