package jest

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
	errDiscoveryLimit = errors.New("jest context discovery limit reached")
	allConfigNames    = map[string]struct{}{
		"jest.config.js": {}, "jest.config.ts": {}, "jest.config.mjs": {}, "jest.config.mts": {},
		"jest.config.cjs": {}, "jest.config.cts": {}, "jest.config.json": {},
	}
)

type configCandidate struct {
	name    scope.Path
	regular bool
}

type jestContext struct {
	root          scope.Path
	configs       []configCandidate
	packageConfig bool
}

type discoveryIssue struct {
	root scope.Path
	path string
	err  error
}

type jestInstance struct {
	descriptor provider.InstanceDescriptor
	contexts   []jestContext
	issues     []discoveryIssue
	discovery  *scope.Explanation
}

func (i jestInstance) Descriptor() provider.InstanceDescriptor { return i.descriptor }

type packageManifest struct {
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	Jest                 json.RawMessage   `json:"jest"`
}

// Detect finds Jest package contexts without loading configuration or code.
func (p *Provider) Detect(ctx context.Context, repo provider.Repository) (_ []provider.Instance, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("detect Jest contexts: %w", err)
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
			return nil, fmt.Errorf("detect Jest contexts: %w", ctxErr)
		}
		code := "jest/discovery-unavailable"
		summary := "Jest context discovery could not be completed safely"
		if errors.Is(scanErr, errDiscoveryLimit) {
			code = "jest/discovery-limit"
			summary = fmt.Sprintf("Jest context discovery exceeds %d repository entries", limit)
		}
		discovery = &scope.Explanation{Code: code, Summary: summary, Evidence: scanErr.Error()}
	}
	if len(contexts) == 0 && len(issues) == 0 && discovery == nil {
		return nil, nil
	}
	slices.SortFunc(contexts, func(left, right jestContext) int {
		return strings.Compare(string(left.root), string(right.root))
	})
	slices.SortFunc(issues, func(left, right discoveryIssue) int {
		if order := strings.Compare(string(left.root), string(right.root)); order != 0 {
			return order
		}
		return strings.Compare(left.path, right.path)
	})
	return []provider.Instance{jestInstance{
		descriptor: provider.InstanceDescriptor{Provider: providerID, ID: rootInstanceID, Label: "Jest contexts"},
		contexts:   contexts, issues: issues, discovery: discovery,
	}}, nil
}

func discoverContexts(ctx context.Context, root *os.Root, limit int) ([]jestContext, []discoveryIssue, error) {
	packageRoots := map[scope.Path]bool{}
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
		manifestRoot := scope.Path(path.Dir(name))
		if !entry.Type().IsRegular() {
			issues = append(issues, discoveryIssue{root: manifestRoot, path: name, err: errors.New("manifest is not a regular file")})
			return nil
		}
		manifest, err := readManifest(root, name)
		if err != nil {
			issues = append(issues, discoveryIssue{root: manifestRoot, path: name, err: err})
			return nil //nolint:nilerr // The scoped issue remains explicit while discovery continues.
		}
		if declaredJest(manifest) || len(manifest.Jest) != 0 {
			packageRoots[manifestRoot] = len(manifest.Jest) != 0
		}
		return nil
	})
	// Preserve the root-config fallback even when a bounded walk stops before
	// reaching a lexically later configuration entry.
	for name := range allConfigNames {
		if slices.ContainsFunc(configs, func(candidate configCandidate) bool { return string(candidate.name) == name }) {
			continue
		}
		info, statErr := root.Lstat(name)
		if statErr == nil {
			configs = append(configs, configCandidate{name: scope.Path(name), regular: info.Mode().IsRegular()})
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			issues = append(issues, discoveryIssue{root: ".", path: name, err: statErr})
		}
	}
	if slices.ContainsFunc(configs, func(candidate configCandidate) bool {
		return path.Dir(string(candidate.name)) == "."
	}) {
		if _, exists := packageRoots["."]; !exists {
			packageRoots["."] = false
		}
	}
	contexts := buildContexts(packageRoots, configs)
	return contexts, issues, err
}

func buildContexts(packageRoots map[scope.Path]bool, configs []configCandidate) []jestContext {
	contexts := make([]jestContext, 0, len(packageRoots))
	for root, packageConfig := range packageRoots {
		current := jestContext{root: root, packageConfig: packageConfig}
		for _, candidate := range configs {
			if path.Dir(string(candidate.name)) == string(root) {
				current.configs = append(current.configs, candidate)
			}
		}
		slices.SortFunc(current.configs, func(left, right configCandidate) int {
			return strings.Compare(string(left.name), string(right.name))
		})
		contexts = append(contexts, current)
	}
	configuredRoots := make([]scope.Path, 0, len(contexts))
	for _, current := range contexts {
		if current.packageConfig || slices.ContainsFunc(current.configs, func(candidate configCandidate) bool {
			base := path.Base(string(candidate.name))
			return base != "jest.config.mts" && base != "jest.config.cts"
		}) {
			configuredRoots = append(configuredRoots, current.root)
		}
	}
	return slices.DeleteFunc(contexts, func(current jestContext) bool {
		if current.packageConfig || len(current.configs) != 0 {
			return false
		}
		return slices.ContainsFunc(configuredRoots, func(candidate scope.Path) bool {
			return candidate != current.root && pathWithin(candidate, current.root)
		})
	})
}

func pathWithin(root, name scope.Path) bool {
	return root == "." || name == root || strings.HasPrefix(string(name), string(root)+"/")
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
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) {
		return nil, errors.New("file changed while it was read")
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

func declaredJest(manifest packageManifest) bool {
	for _, dependencies := range []map[string]string{
		manifest.Dependencies, manifest.DevDependencies, manifest.OptionalDependencies,
	} {
		if _, exists := dependencies["jest"]; exists {
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

func isConfigName(name string) bool {
	_, exists := allConfigNames[name]
	return exists
}

func applicablePaths(root scope.Path, paths []scope.Path) []scope.Path {
	result := make([]scope.Path, 0, len(paths))
	for _, name := range paths {
		if pathWithin(root, name) {
			result = append(result, name)
		}
	}
	return result
}

func discoveryExplanation(issue discoveryIssue) scope.Explanation {
	return scope.Explanation{
		Code: "jest/discovery-unavailable", Summary: "Jest context discovery could not inspect a repository entry",
		Evidence: issue.path + ": " + issue.err.Error(),
	}
}
