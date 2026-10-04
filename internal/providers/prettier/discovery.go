package prettier

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

var errDiscoveryLimit = errors.New("prettier context discovery limit reached")

type prettierContext struct {
	root        scope.Path
	unavailable *scope.Explanation
}

type prettierInstance struct {
	descriptor provider.InstanceDescriptor
	contexts   []prettierContext
	discovery  *scope.Explanation
}

func (i prettierInstance) Descriptor() provider.InstanceDescriptor { return i.descriptor }

func (p *Provider) Detect(ctx context.Context, repo provider.Repository) (_ []provider.Instance, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("detect Prettier contexts: %w", err)
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
			return nil, fmt.Errorf("detect Prettier contexts: %w", ctxErr)
		}
		code := "prettier/discovery-unavailable"
		summary := "Prettier context discovery could not be completed safely"
		if errors.Is(scanErr, errDiscoveryLimit) {
			code = "prettier/discovery-limit"
			summary = fmt.Sprintf("Prettier context discovery exceeds %d repository entries", limit)
		}
		discovery = &scope.Explanation{Code: code, Summary: summary, Evidence: scanErr.Error()}
	} else if len(issues) != 0 {
		discovery = discoveryExplanation(issues)
	}
	if len(contexts) == 0 && discovery == nil {
		return nil, nil
	}
	slices.SortFunc(contexts, func(left, right prettierContext) int {
		return strings.Compare(string(left.root), string(right.root))
	})
	return []provider.Instance{prettierInstance{
		descriptor: provider.InstanceDescriptor{Provider: providerID, ID: rootInstanceID, Label: "Prettier contexts"},
		contexts:   contexts, discovery: discovery,
	}}, nil
}

type discoveryIssue struct {
	path string
	err  error
}

type packageManifest struct {
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	Prettier             json.RawMessage   `json:"prettier"`
}

func discoverContexts(ctx context.Context, root *os.Root, limit int) ([]prettierContext, []discoveryIssue, error) {
	contexts := []prettierContext{}
	issues := []discoveryIssue{}
	entries := 0
	rootSignal := false
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			issues = append(issues, discoveryIssue{path: name, err: walkErr})
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
		if name == ".prettierignore" || (entry.Name() != "package.json" && isConfigName(entry.Name())) {
			rootSignal = rootSignal || path.Dir(name) == "."
		}
		if entry.Name() != "package.json" || !entry.Type().IsRegular() {
			return nil
		}
		manifest, err := readManifest(root, name)
		if err != nil {
			issues = append(issues, discoveryIssue{path: name, err: err})
			return nil //nolint:nilerr // The issue makes discovery unresolved; other contexts remain discoverable.
		}
		declared := declaredPrettier(manifest)
		if path.Dir(name) == "." && len(manifest.Prettier) != 0 {
			rootSignal = true
		}
		if !declared {
			return nil
		}
		packageRoot := path.Dir(name)
		contexts = append(contexts, prettierContext{root: scope.Path(packageRoot)})
		return nil
	})
	if err != nil {
		return contexts, issues, err
	}
	if len(contexts) == 0 && rootSignal {
		contexts = append(contexts, prettierContext{
			root: ".",
			unavailable: &scope.Explanation{
				Code: "prettier/package-unavailable", Summary: "Prettier configuration exists but no direct Prettier package declaration was found",
				Action: "declare and install an exact Prettier 3.x dependency for this context",
			},
		})
	}
	return contexts, issues, nil
}

func readManifest(root *os.Root, name string) (packageManifest, error) {
	file, err := root.Open(name)
	if err != nil {
		return packageManifest{}, fmt.Errorf("open manifest: %w", err)
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return packageManifest{}, fmt.Errorf("read manifest: %w", readErr)
	}
	if closeErr != nil {
		return packageManifest{}, fmt.Errorf("close manifest: %w", closeErr)
	}
	if len(content) > maxManifestBytes {
		return packageManifest{}, fmt.Errorf("manifest exceeds %d bytes", maxManifestBytes)
	}
	var manifest packageManifest
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&manifest); err != nil {
		return packageManifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return packageManifest{}, errors.New("parse manifest: multiple JSON values")
		}
		return packageManifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	return manifest, nil
}

func declaredPrettier(manifest packageManifest) bool {
	for _, dependencies := range []map[string]string{
		manifest.Dependencies, manifest.DevDependencies, manifest.OptionalDependencies,
	} {
		if _, ok := dependencies["prettier"]; ok {
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

func discoveryExplanation(issues []discoveryIssue) *scope.Explanation {
	details := make([]string, 0, min(len(issues), 4))
	for index, issue := range issues {
		if index == 3 {
			details = append(details, fmt.Sprintf("and %d more", len(issues)-index))
			break
		}
		details = append(details, issue.path+": "+issue.err.Error())
	}
	return &scope.Explanation{
		Code: "prettier/discovery-unavailable", Summary: "Prettier context discovery could not inspect all repository entries",
		Evidence: strings.Join(details, "; "),
	}
}

func applicablePaths(root scope.Path, paths []scope.Path) []scope.Path {
	result := make([]scope.Path, 0, len(paths))
	for _, name := range paths {
		if root == "." || name == root || strings.HasPrefix(string(name), string(root)+"/") {
			result = append(result, name)
		}
	}
	return result
}

var configNames = map[string]struct{}{
	"package.json": {}, "package.yaml": {}, ".prettierrc": {}, ".prettierrc.json": {}, ".prettierrc.json5": {},
	".prettierrc.yml": {}, ".prettierrc.yaml": {}, ".prettierrc.toml": {},
	".prettierrc.js": {}, ".prettierrc.cjs": {}, ".prettierrc.mjs": {},
	"prettier.config.js": {}, "prettier.config.cjs": {}, "prettier.config.mjs": {},
	".prettierrc.ts": {}, ".prettierrc.cts": {}, ".prettierrc.mts": {},
	"prettier.config.ts": {}, "prettier.config.cts": {}, "prettier.config.mts": {},
}

func isConfigName(name string) bool {
	_, ok := configNames[name]
	return ok
}
