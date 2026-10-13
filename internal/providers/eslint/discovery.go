package eslint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/V-Isa/awareof/internal/provider"
	"github.com/V-Isa/awareof/internal/scope"
)

const (
	maxDiscoveryEntries = 100_000
	maxManifestBytes    = 1024 * 1024
)

var (
	errDiscoveryLimit = errors.New("eslint context discovery limit reached")
	configNames       = []string{
		"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs",
		"eslint.config.ts", "eslint.config.mts", "eslint.config.cts",
	}
)

type eslintContext struct {
	root          scope.Path
	config        scope.Path
	nestedConfigs []scope.Path
	unavailable   *scope.Explanation
}

type eslintInstance struct {
	descriptor provider.InstanceDescriptor
	contexts   []eslintContext
	issues     []discoveryIssue
	discovery  *scope.Explanation
}

func (i eslintInstance) Descriptor() provider.InstanceDescriptor { return i.descriptor }

// Detect finds ESLint package contexts without loading configuration or code.
func (p *Provider) Detect(ctx context.Context, repo provider.Repository) (_ []provider.Instance, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("detect ESLint contexts: %w", err)
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

	limit := p.scanLimit
	if limit <= 0 {
		limit = maxDiscoveryEntries
	}
	contexts, issues, scanErr := discoverContexts(ctx, root, limit)
	var discovery *scope.Explanation
	if scanErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("detect ESLint contexts: %w", ctxErr)
		}
		code := "eslint/discovery-unavailable"
		summary := "ESLint context discovery could not be completed safely"
		if errors.Is(scanErr, errDiscoveryLimit) {
			code = "eslint/discovery-limit"
			summary = fmt.Sprintf("ESLint context discovery exceeds %d repository entries", limit)
		}
		discovery = &scope.Explanation{Code: code, Summary: summary, Evidence: scanErr.Error()}
	}
	if len(contexts) == 0 && len(issues) == 0 && discovery == nil {
		return nil, nil
	}
	slices.SortFunc(contexts, func(left, right eslintContext) int {
		return strings.Compare(string(left.root), string(right.root))
	})
	slices.SortFunc(issues, func(left, right discoveryIssue) int {
		if order := strings.Compare(string(left.root), string(right.root)); order != 0 {
			return order
		}
		return strings.Compare(left.path, right.path)
	})
	return []provider.Instance{eslintInstance{
		descriptor: provider.InstanceDescriptor{Provider: providerID, ID: rootInstanceID, Label: "ESLint contexts"},
		contexts:   contexts, issues: issues, discovery: discovery,
	}}, nil
}

type discoveryIssue struct {
	root scope.Path
	path string
	err  error
}

type packageManifest struct {
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

type configCandidate struct {
	name    scope.Path
	regular bool
}

func discoverContexts(ctx context.Context, root *os.Root, limit int) ([]eslintContext, []discoveryIssue, error) {
	packageRoots := map[scope.Path]struct{}{}
	configs := []configCandidate{}
	issues := []discoveryIssue{}
	entries := 0
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			issueRoot := scope.Path(path.Dir(name))
			if entry != nil && entry.IsDir() {
				issueRoot = scope.Path(name)
			}
			issues = append(issues, discoveryIssue{root: issueRoot, path: name, err: walkErr})
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		entries++
		if entries > limit {
			return errDiscoveryLimit
		}
		if entry.IsDir() && name != "." && skippedDirectory(entry.Name()) {
			return fs.SkipDir
		}
		if isConfigName(entry.Name()) {
			configs = append(configs, configCandidate{name: scope.Path(name), regular: entry.Type().IsRegular()})
		}
		if entry.Name() != "package.json" {
			return nil
		}
		if !entry.Type().IsRegular() {
			issues = append(issues, discoveryIssue{
				root: scope.Path(path.Dir(name)), path: name, err: errors.New("manifest is not a regular file"),
			})
			return nil
		}
		manifest, err := readManifest(root, name)
		if err != nil {
			issues = append(issues, discoveryIssue{root: scope.Path(path.Dir(name)), path: name, err: err})
			return nil //nolint:nilerr // The issue remains explicit while other contexts are discovered.
		}
		if declaredESLint(manifest) {
			packageRoots[scope.Path(path.Dir(name))] = struct{}{}
		}
		return nil
	})
	if slices.ContainsFunc(configs, func(candidate configCandidate) bool {
		return path.Dir(string(candidate.name)) == "."
	}) {
		packageRoots["."] = struct{}{}
	}
	if err != nil {
		return buildContexts(packageRoots, configs), issues, err
	}
	return buildContexts(packageRoots, configs), issues, nil
}

func buildContexts(packageRoots map[scope.Path]struct{}, configs []configCandidate) []eslintContext {
	contexts := make([]eslintContext, 0, len(packageRoots))
	for root := range packageRoots {
		current := eslintContext{root: root}
		for _, configName := range configNames {
			candidate := scope.Path(configName)
			if root != "." {
				candidate = scope.Path(path.Join(string(root), configName))
			}
			index := slices.IndexFunc(configs, func(config configCandidate) bool { return config.name == candidate })
			if index < 0 {
				continue
			}
			current.config = candidate
			if !configs[index].regular {
				current.unavailable = &scope.Explanation{
					Code: "eslint/config-uninspectable", Summary: "the selected ESLint configuration is not a regular repository file",
					Evidence: string(candidate), Action: "replace the configuration entry with a regular file or accept UNKNOWN",
				}
			}
			break
		}
		if current.config == "" {
			current.unavailable = &scope.Explanation{
				Code: "eslint/config-unavailable", Summary: "no supported ESLint flat configuration exists at the context root",
				Evidence: "context: " + string(root), Action: "place a supported eslint.config.* file at the context root or accept UNKNOWN",
			}
		}
		for _, config := range configs {
			if path.Dir(string(config.name)) == string(root) || !pathWithin(root, config.name) {
				continue
			}
			current.nestedConfigs = append(current.nestedConfigs, config.name)
		}
		slices.Sort(current.nestedConfigs)
		contexts = append(contexts, current)
	}
	configuredRoots := make([]scope.Path, 0, len(contexts))
	for _, current := range contexts {
		if current.config != "" {
			configuredRoots = append(configuredRoots, current.root)
		}
	}
	return slices.DeleteFunc(contexts, func(current eslintContext) bool {
		if current.config != "" {
			return false
		}
		return slices.ContainsFunc(configuredRoots, func(candidate scope.Path) bool {
			return candidate != current.root && pathWithin(candidate, current.root)
		})
	})
}

func readManifest(root *os.Root, name string) (packageManifest, error) {
	content, err := readRegularFile(root, name, maxManifestBytes)
	if err != nil {
		return packageManifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest packageManifest
	if err := decodeSingleJSON(content, &manifest); err != nil {
		return packageManifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	return manifest, nil
}

func readRegularFile(root *os.Root, name string, limit int64) (_ []byte, returnErr error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("file is not a regular file")
	}
	if before.Size() > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
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
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return content, nil
}

func decodeSingleJSON(content []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func declaredESLint(manifest packageManifest) bool {
	for _, dependencies := range []map[string]string{
		manifest.Dependencies, manifest.DevDependencies, manifest.OptionalDependencies,
	} {
		if _, ok := dependencies["eslint"]; ok {
			return true
		}
	}
	return false
}

func skippedDirectory(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules":
		return true
	default:
		return false
	}
}

func isConfigName(name string) bool { return slices.Contains(configNames, name) }

func applicablePaths(root scope.Path, paths []scope.Path) []scope.Path {
	result := make([]scope.Path, 0, len(paths))
	for _, name := range paths {
		if pathWithin(root, name) {
			result = append(result, name)
		}
	}
	return result
}

func pathWithin(root, name scope.Path) bool {
	return root == "." || name == root || strings.HasPrefix(string(name), string(root)+"/")
}

func nestedConfigFor(current eslintContext, name scope.Path) (scope.Path, bool) {
	for _, config := range current.nestedConfigs {
		directory := scope.Path(path.Dir(string(config)))
		if pathWithin(directory, name) {
			return config, true
		}
	}
	return "", false
}

func discoveryExplanation(issue discoveryIssue) scope.Explanation {
	return scope.Explanation{
		Code: "eslint/discovery-unavailable", Summary: "ESLint context discovery could not inspect all repository entries",
		Evidence: truncateUTF8(issue.path+": "+issue.err.Error(), 2048),
	}
}
