// Package npm evaluates membership in npm publication tarballs.
package npm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/pathutil"
	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

const (
	providerID          scope.ProviderID = "npm"
	rootInstanceID      scope.InstanceID = "npm"
	workspaceUnknownID  scope.InstanceID = "workspaces"
	discoveryUnknownID  scope.InstanceID = "discovery"
	maxPackageJSONSize                   = 1024 * 1024
	maxDiscoveryEntries                  = 100_000
	reference                            = "docs.npmjs.com/cli/v12/commands/npm-publish"
)

var publicationScripts = []string{"prepublishOnly", "prepack", "prepare"}

// Provider evaluates the artifact that npm would publish for each safely
// discoverable package. It never runs package lifecycle scripts.
type Provider struct {
	runner         safeexec.CommandRunner
	goos           string
	versionMu      sync.Mutex
	versionChecked bool
	version        string
}

func New(runner safeexec.CommandRunner) *Provider {
	return &Provider{runner: runner, goos: runtime.GOOS}
}

func (*Provider) ID() scope.ProviderID {
	return providerID
}

// NeedsNativeTools reports whether any detected package can use npm and Node
// during evaluation. Private, scripted, or unresolved packages do not.
func (p *Provider) NeedsNativeTools(instances []provider.Instance) bool {
	if unsupportedPlatform(p.operatingSystem()) != nil {
		return false
	}
	for _, instance := range instances {
		current, ok := instance.(npmInstance)
		if ok && current.canPack() {
			return true
		}
	}
	return false
}

// Detect reads package manifests without executing npm or repository code.
func (*Provider) Detect(ctx context.Context, repo provider.Repository) (_ []provider.Instance, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("detect npm packages: %w", err)
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

	config, status := readPackage(root, ".")
	if status == packageMissing {
		packageRoots, unavailable, err := discoverNestedPackages(ctx, root, maxDiscoveryEntries)
		if err != nil {
			return nil, fmt.Errorf("discover nested npm packages: %w", err)
		}
		instances := make([]provider.Instance, 0, len(packageRoots)+1)
		for _, packageRoot := range packageRoots {
			config, packageStatus := readPackage(root, packageRoot)
			instances = append(instances, newInstance(packageRoot, config, packageStatus))
		}
		if unavailable != nil {
			instances = append(instances, npmInstance{
				descriptor: provider.InstanceDescriptor{
					Provider: providerID,
					ID:       discoveryUnknownID,
					Label:    "npm packages (discovery unresolved)",
				},
				unavailable: unavailable,
			})
		}
		return instances, nil
	}
	rootInstance := newInstance(".", config, status)
	if status != packageReady {
		return []provider.Instance{rootInstance}, nil
	}

	instances := []provider.Instance{rootInstance}
	workspacePaths, unresolved, err := discoverWorkspaces(ctx, root, config.Workspaces)
	if err != nil {
		return nil, fmt.Errorf("discover npm workspaces: %w", err)
	}
	for _, packageRoot := range workspacePaths {
		workspaceConfig, workspaceStatus := readPackage(root, packageRoot)
		if workspaceStatus == packageMissing {
			continue
		}
		instances = append(instances, newInstance(packageRoot, workspaceConfig, workspaceStatus))
	}
	if unresolved != "" {
		instances = append(instances, npmInstance{
			descriptor: provider.InstanceDescriptor{
				Provider: providerID,
				ID:       workspaceUnknownID,
				Label:    "npm workspaces (unresolved)",
			},
			unavailable: &scope.Explanation{
				Code:    "npm/workspace-discovery-unsupported",
				Summary: unresolved,
			},
		})
	}
	return instances, nil
}

func (p *Provider) Evaluate(
	ctx context.Context,
	_ provider.EvaluationContext,
	repo provider.Repository,
	instance provider.Instance,
	paths []scope.Path,
) ([]scope.Result, error) {
	current, ok := instance.(npmInstance)
	if !ok {
		return nil, errors.New("unsupported npm instance")
	}
	descriptor := current.Descriptor()
	if descriptor.Provider != providerID || descriptor.ID == "" {
		return nil, fmt.Errorf("unsupported instance %q", descriptor.ID)
	}
	if len(paths) == 0 {
		return []scope.Result{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("evaluate npm package: %w", err)
	}

	applicable := current.applicablePaths(paths)
	var included map[scope.Path]struct{}
	if len(applicable) != 0 && current.canPack() {
		if p.runner == nil {
			return nil, errors.New("command runner is nil")
		}
		var err error
		included, err = p.packlist(ctx, repo.Root, current)
		if err != nil {
			if unavailable, ok := safeexec.AsUnavailable(err); ok {
				current.unavailable = &scope.Explanation{
					Code:     unavailable.Code,
					Summary:  unavailable.Summary,
					Evidence: unavailable.Evidence,
					Action:   unavailable.Action,
				}
				current.unavailableTool = unavailable.Tool
			} else {
				return nil, err
			}
		}
	}

	results := make([]scope.Result, 0, len(paths))
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("evaluate npm package: %w", err)
		}
		state, explanation := current.evaluate(name, included)
		results = append(results, scope.Result{
			Path:        name,
			Provider:    providerID,
			Instance:    descriptor.ID,
			State:       state,
			Explanation: explanation,
			Provenance:  current.provenance(state),
		})
	}
	return results, nil
}

type npmInstance struct {
	descriptor      provider.InstanceDescriptor
	packageRoot     scope.Path
	name            string
	private         bool
	scripts         []string
	digest          [32]byte
	unavailable     *scope.Explanation
	unavailableTool safeexec.ToolID
}

func (i npmInstance) Descriptor() provider.InstanceDescriptor {
	return i.descriptor
}

func (i npmInstance) applicablePaths(paths []scope.Path) []scope.Path {
	if i.packageRoot == "" || i.private || len(i.scripts) != 0 || i.unavailable != nil {
		return nil
	}
	applicable := make([]scope.Path, 0, len(paths))
	for _, name := range paths {
		if _, ok := relativeToPackage(name, i.packageRoot); ok {
			applicable = append(applicable, name)
		}
	}
	return applicable
}

func (i npmInstance) canPack() bool {
	return i.packageRoot != "" && !i.private && len(i.scripts) == 0 && i.unavailable == nil
}

func (i npmInstance) evaluate(name scope.Path, included map[scope.Path]struct{}) (scope.State, scope.Explanation) {
	if i.packageRoot == "" {
		return scope.Unknown, *i.unavailable
	}
	if _, applies := relativeToPackage(name, i.packageRoot); !applies {
		return scope.NotApp, scope.Explanation{
			Code:    "npm/outside-package",
			Summary: fmt.Sprintf("outside npm package %q", i.packageRoot),
		}
	}
	if i.unavailable != nil {
		return scope.Unknown, *i.unavailable
	}
	if i.private {
		return scope.NotApp, scope.Explanation{
			Code:    "npm/private-package",
			Summary: fmt.Sprintf("npm package %s is private and cannot be published", i.displayName()),
		}
	}
	if len(i.scripts) != 0 {
		return scope.Unknown, scope.Explanation{
			Code:     "npm/publication-script",
			Summary:  "publication scripts can change tarball contents and are not executed",
			Evidence: strings.Join(i.scripts, ", "),
		}
	}
	if _, ok := included[name]; ok {
		return scope.In, scope.Explanation{
			Code:    "npm/included",
			Summary: fmt.Sprintf("included in the npm publish tarball for %s", i.displayName()),
		}
	}
	return scope.Out, scope.Explanation{
		Code:    "npm/excluded",
		Summary: fmt.Sprintf("not included in the npm publish tarball for %s", i.displayName()),
	}
}

func (i npmInstance) displayName() string {
	if i.name != "" {
		return i.name
	}
	return string(i.packageRoot)
}

func (i npmInstance) provenance(state scope.State) scope.Provenance {
	if state == scope.Unknown {
		provenance := scope.Provenance{Method: scope.Unavailable, Reference: reference}
		if i.unavailableTool != "" {
			provenance.Tool = string(i.unavailableTool)
		}
		return provenance
	}
	if state == scope.In || state == scope.Out {
		return scope.Provenance{Method: scope.SafeNative, Tool: "npm"}
	}
	return scope.Provenance{Method: scope.SafeParser, Reference: reference}
}

func newInstance(packageRoot scope.Path, config packageConfig, status packageStatus) npmInstance {
	id := rootInstanceID
	label := "npm package ."
	if packageRoot != "." {
		id = scope.InstanceID("package/" + string(packageRoot))
		label = "npm package " + string(packageRoot)
	}
	instance := npmInstance{
		descriptor:  provider.InstanceDescriptor{Provider: providerID, ID: id, Label: label},
		packageRoot: packageRoot,
		name:        config.Name,
		private:     config.Private,
		scripts:     append([]string(nil), config.PublicationScripts...),
		digest:      config.Digest,
	}
	if status != packageReady {
		instance.unavailable = &scope.Explanation{
			Code:    "npm/package-json-unavailable",
			Summary: status.summary(packageRoot),
		}
	}
	return instance
}

func (p *Provider) packlist(ctx context.Context, root string, instance npmInstance) (_ map[scope.Path]struct{}, returnErr error) {
	if unavailable := unsupportedPlatform(p.operatingSystem()); unavailable != nil {
		return nil, unavailable
	}
	if current, status, err := currentPackage(root, instance.packageRoot); err != nil {
		return nil, err
	} else if status != packageReady || current.Digest != instance.digest {
		return nil, packageChangedError(instance.packageRoot)
	}

	temporary, err := safeTemporaryDirectory(root)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := os.RemoveAll(temporary); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove isolated npm state: %w", err))
		}
	}()

	userConfig := filepath.Join(temporary, "user.npmrc")
	globalConfig := filepath.Join(temporary, "global.npmrc")
	configurationRoot, err := os.OpenRoot(temporary)
	if err != nil {
		return nil, fmt.Errorf("open isolated npm state: %w", err)
	}
	for _, name := range []string{"user.npmrc", "global.npmrc"} {
		file, err := configurationRoot.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = configurationRoot.Close()
			return nil, fmt.Errorf("create isolated npm configuration: %w", err)
		}
		if err := file.Close(); err != nil {
			_ = configurationRoot.Close()
			return nil, fmt.Errorf("close isolated npm configuration: %w", err)
		}
	}
	if err := configurationRoot.Close(); err != nil {
		return nil, fmt.Errorf("close isolated npm state: %w", err)
	}

	environment := map[string]string{
		"NPM_CONFIG_CACHE":          filepath.Join(temporary, "cache"),
		"NPM_CONFIG_USERCONFIG":     userConfig,
		"NPM_CONFIG_GLOBALCONFIG":   globalConfig,
		"NPM_CONFIG_IGNORE_SCRIPTS": "true",
		"NPM_CONFIG_OFFLINE":        "true",
	}
	unsetEnvironment := npmUnsetEnvironment()
	if err := p.requireSupportedVersion(ctx, root, temporary, environment, unsetEnvironment); err != nil {
		return nil, err
	}

	packageSpec := filepath.Join(root, filepath.FromSlash(string(instance.packageRoot)))
	response, err := p.runner.Run(ctx, safeexec.Request{
		Root:         root,
		Dir:          temporary,
		Tool:         "npm",
		Interpreter:  "node",
		ExternalOnly: true,
		Args: []string{
			"pack", packageSpec,
			"--dry-run", "--json", "--ignore-scripts", "--offline",
			"--workspaces=false", "--include-workspace-root=false",
			"--audit=false", "--fund=false", "--update-notifier=false",
		},
		Env:      environment,
		UnsetEnv: unsetEnvironment,
	})
	if err != nil {
		return nil, fmt.Errorf("inspect npm publish tarball: %w", err)
	}
	if response.ExitCode != 0 {
		return nil, npmCommandError(response)
	}
	included, err := parsePackOutput(response.Stdout, instance.packageRoot)
	if err != nil {
		return nil, err
	}
	if current, status, err := currentPackage(root, instance.packageRoot); err != nil {
		return nil, err
	} else if status != packageReady || current.Digest != instance.digest {
		return nil, packageChangedError(instance.packageRoot)
	}
	return included, nil
}

func (p *Provider) operatingSystem() string {
	if p != nil && p.goos != "" {
		return p.goos
	}
	return runtime.GOOS
}

func unsupportedPlatform(goos string) *safeexec.UnavailableError {
	if goos != "windows" {
		return nil
	}
	return &safeexec.UnavailableError{
		Tool:     "npm",
		Code:     "npm/platform-unsupported",
		Summary:  "safe npm evaluation is not supported on Windows yet",
		Evidence: "the default npm.cmd launcher cannot be executed through the separately approved Node interpreter",
	}
}

func safeTemporaryDirectory(root string) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", &safeexec.UnavailableError{
			Code:     "npm/temporary-state-unavailable",
			Summary:  "cannot verify the repository root before creating isolated npm state",
			Evidence: err.Error(),
		}
	}
	temporary, err := os.MkdirTemp("", "awareof-npm-")
	if err != nil {
		return "", &safeexec.UnavailableError{
			Code:     "npm/temporary-state-unavailable",
			Summary:  "cannot create isolated npm state outside the repository",
			Evidence: err.Error(),
		}
	}
	resolved, err := filepath.EvalSymlinks(temporary)
	if err != nil {
		_ = os.RemoveAll(temporary)
		return "", &safeexec.UnavailableError{
			Code:     "npm/temporary-state-unavailable",
			Summary:  "cannot verify isolated npm state",
			Evidence: err.Error(),
		}
	}
	if pathutil.Within(resolvedRoot, resolved) {
		_ = os.RemoveAll(temporary)
		return "", &safeexec.UnavailableError{
			Code:    "npm/temporary-state-unavailable",
			Summary: "temporary state resolves inside the inspected repository",
		}
	}
	return resolved, nil
}

func npmUnsetEnvironment() []string {
	set := map[string]struct{}{
		"INIT_CWD":                {},
		"NODE_AUTH_TOKEN":         {},
		"NODE_OPTIONS":            {},
		"NODE_PATH":               {},
		"NPM_CONFIG_NODE_OPTIONS": {},
		"NPM_TOKEN":               {},
	}
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if ok && strings.HasPrefix(strings.ToUpper(key), "NPM_CONFIG_") {
			set[key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (p *Provider) requireSupportedVersion(
	ctx context.Context,
	root string,
	dir string,
	environment map[string]string,
	unsetEnvironment []string,
) error {
	p.versionMu.Lock()
	defer p.versionMu.Unlock()
	if p.versionChecked {
		return supportedVersion(p.version)
	}
	response, err := p.runner.Run(ctx, safeexec.Request{
		Root:         root,
		Dir:          dir,
		Tool:         "npm",
		Interpreter:  "node",
		ExternalOnly: true,
		Args:         []string{"--version"},
		Env:          environment,
		UnsetEnv:     unsetEnvironment,
	})
	if err != nil {
		return fmt.Errorf("inspect npm version: %w", err)
	}
	if response.ExitCode != 0 {
		return npmVersionCommandError(response)
	}
	version := strings.TrimSpace(string(response.Stdout))
	if err := supportedVersion(version); err != nil {
		if _, unavailable := safeexec.AsUnavailable(err); unavailable {
			p.version = version
			p.versionChecked = true
		}
		return err
	}
	p.version = version
	p.versionChecked = true
	return nil
}

func supportedVersion(version string) error {
	majorText, _, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil || major < 1 {
		return fmt.Errorf("inspect npm version: invalid version %q", version)
	}
	if major < 11 {
		return &safeexec.UnavailableError{
			Code:     "npm/version-unsupported",
			Summary:  "npm 11 or newer is required for safe script-disabled packing",
			Evidence: "resolved npm reports version " + version,
			Action:   "select and approve npm 11 or newer",
		}
	}
	return nil
}

func currentPackage(rootPath string, packageRoot scope.Path) (packageConfig, packageStatus, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return packageConfig{}, packageUnreadable, fmt.Errorf("open repository root: %w", err)
	}
	config, status := readPackage(root, packageRoot)
	if err := root.Close(); err != nil {
		return packageConfig{}, packageUnreadable, fmt.Errorf("close repository root: %w", err)
	}
	return config, status, nil
}

func packageChangedError(packageRoot scope.Path) error {
	name := "package.json"
	if packageRoot != "." {
		name = path.Join(string(packageRoot), "package.json")
	}
	return &safeexec.UnavailableError{
		Code:    "npm/package-json-changed",
		Summary: name + " changed while npm publication membership was being evaluated",
		Action:  "retry the query after repository changes stop",
	}
}

type packReport struct {
	Files []packFile `json:"files"`
}

type packFile struct {
	Path string `json:"path"`
}

func parsePackOutput(content []byte, packageRoot scope.Path) (map[scope.Path]struct{}, error) {
	if !utf8.Valid(content) {
		return nil, errors.New("parse npm pack output: invalid UTF-8")
	}
	var reports []packReport
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	if err := decoder.Decode(&reports); err != nil {
		return nil, fmt.Errorf("parse npm pack output: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, fmt.Errorf("parse npm pack output: %w", err)
	}
	if len(reports) != 1 || reports[0].Files == nil {
		return nil, fmt.Errorf("parse npm pack output: got %d package reports, want one", len(reports))
	}
	included := make(map[scope.Path]struct{}, len(reports[0].Files))
	for _, file := range reports[0].Files {
		logical, err := packPath(packageRoot, file.Path)
		if err != nil {
			return nil, err
		}
		if _, duplicate := included[logical]; duplicate {
			return nil, fmt.Errorf("parse npm pack output: duplicate path %q", logical)
		}
		included[logical] = struct{}{}
	}
	return included, nil
}

func packPath(packageRoot scope.Path, name string) (scope.Path, error) {
	if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, 0) || strings.ContainsRune(name, '\\') {
		return "", fmt.Errorf("parse npm pack output: invalid path %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || clean != name {
		return "", fmt.Errorf("parse npm pack output: invalid path %q", name)
	}
	if packageRoot == "." {
		return scope.Path(clean), nil
	}
	return scope.Path(path.Join(string(packageRoot), clean)), nil
}

func relativeToPackage(name, packageRoot scope.Path) (scope.Path, bool) {
	if packageRoot == "." {
		return name, true
	}
	root := string(packageRoot)
	value := string(name)
	if value == root {
		return ".", true
	}
	if strings.HasPrefix(value, root+"/") {
		return scope.Path(strings.TrimPrefix(value, root+"/")), true
	}
	return "", false
}

func npmCommandError(response safeexec.Response) error {
	detail := strings.TrimSpace(string(response.Stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(response.Stdout))
	}
	if detail == "" {
		return fmt.Errorf("inspect npm publish tarball: npm exited with status %d", response.ExitCode)
	}
	return fmt.Errorf("inspect npm publish tarball: npm exited with status %d: %s", response.ExitCode, detail)
}

func npmVersionCommandError(response safeexec.Response) error {
	detail := strings.TrimSpace(string(response.Stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(response.Stdout))
	}
	if detail == "" {
		return fmt.Errorf("inspect npm version: npm exited with status %d", response.ExitCode)
	}
	return fmt.Errorf("inspect npm version: npm exited with status %d: %s", response.ExitCode, detail)
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("multiple JSON values")
}

func sortPaths(paths []scope.Path) {
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })
}
