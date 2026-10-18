package jest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/V-Isa/awareof/internal/pathutil"
	"github.com/V-Isa/awareof/internal/scope"
)

type selectedConfig struct {
	path     scope.Path
	argument string
	evidence string
}

func prepareContext(repositoryRoot string, current jestContext) (jestInstallation, selectedConfig, *scope.Explanation) {
	installation, err := loadProjectInstallation(repositoryRoot, current)
	if err != nil {
		return jestInstallation{}, selectedConfig{}, unavailable(
			"jest/package-unavailable", "the project Jest installation could not be identified safely", err.Error(),
			"install a supported repository-local Jest 29 or 30 package, then retry",
		)
	}
	config, problem := selectConfig(repositoryRoot, current, installation.major)
	return installation, config, problem
}

func selectConfig(repositoryRoot string, current jestContext, major string) (selectedConfig, *scope.Explanation) {
	supported := supportedConfigNames(major)
	candidates := slices.DeleteFunc(append([]configCandidate(nil), current.configs...), func(candidate configCandidate) bool {
		_, exists := supported[path.Base(string(candidate.name))]
		return !exists
	})
	count := len(candidates)
	if current.packageConfig {
		count++
	}
	if count > 1 {
		names := make([]string, 0, count)
		for _, candidate := range candidates {
			names = append(names, string(candidate.name))
		}
		if current.packageConfig {
			manifest := "package.json"
			if current.root != "." {
				manifest = path.Join(string(current.root), manifest)
			}
			names = append(names, manifest+"#jest")
		}
		slices.Sort(names)
		return selectedConfig{}, unavailable(
			"jest/config-ambiguous", "multiple Jest configurations exist for the same context", strings.Join(names, ", "),
			"keep one effective Jest configuration per detected context or accept UNKNOWN",
		)
	}
	if len(candidates) == 1 {
		candidate := candidates[0]
		if !candidate.regular {
			return selectedConfig{}, unavailable(
				"jest/config-uninspectable", "the selected Jest configuration is not a regular repository file", string(candidate.name),
				"replace the configuration entry with a regular file or accept UNKNOWN",
			)
		}
		resolved, err := repositoryFile(repositoryRoot, candidate.name)
		if err != nil {
			return selectedConfig{}, unavailable(
				"jest/config-uninspectable", "the selected Jest configuration cannot be resolved safely", err.Error(),
				"repair the repository configuration or accept UNKNOWN",
			)
		}
		return selectedConfig{path: candidate.name, argument: resolved, evidence: string(candidate.name)}, nil
	}
	if current.packageConfig {
		manifest := scope.Path("package.json")
		if current.root != "." {
			manifest = scope.Path(path.Join(string(current.root), "package.json"))
		}
		resolved, err := repositoryFile(repositoryRoot, manifest)
		if err != nil {
			return selectedConfig{}, unavailable(
				"jest/config-uninspectable", "the package Jest configuration cannot be resolved safely", err.Error(),
				"repair the package manifest or accept UNKNOWN",
			)
		}
		argument, err := packageConfigArgument(repositoryRoot, current, manifest, resolved, major)
		if err != nil {
			return selectedConfig{}, unavailable(
				"jest/config-uninspectable", "the package Jest configuration cannot be bounded to the repository", err.Error(),
				"use an object or repository-contained JSON configuration, or accept UNKNOWN",
			)
		}
		return selectedConfig{path: manifest, argument: argument, evidence: string(manifest) + "#jest"}, nil
	}
	return selectedConfig{evidence: "version-specific Jest defaults at context " + string(current.root)}, nil
}

func supportedConfigNames(major string) map[string]struct{} {
	result := map[string]struct{}{
		"jest.config.js": {}, "jest.config.ts": {}, "jest.config.mjs": {}, "jest.config.cjs": {}, "jest.config.json": {},
	}
	if major == "30" {
		result["jest.config.mts"] = struct{}{}
		result["jest.config.cts"] = struct{}{}
	}
	return result
}

func packageConfigArgument(repositoryRoot string, current jestContext, manifest scope.Path, manifestPath, major string) (_ string, returnErr error) {
	root, err := os.OpenRoot(repositoryRoot)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()
	content, err := readRegularFile(root, string(manifest), maxManifestBytes)
	if err != nil {
		return "", err
	}
	var packageData struct {
		Jest json.RawMessage `json:"jest"`
	}
	if err := decodeSingleJSON(content, &packageData); err != nil {
		return "", err
	}
	if len(packageData.Jest) == 0 {
		return "", errors.New("package manifest no longer contains a Jest configuration")
	}
	var value any
	if err := json.Unmarshal(packageData.Jest, &value); err != nil {
		return "", err
	}
	switch config := value.(type) {
	case map[string]any:
		return manifestPath, nil
	case string:
		if major != "30" {
			return "", errors.New("package Jest configuration paths require Jest 30")
		}
		if config == "" || filepath.IsAbs(filepath.FromSlash(config)) {
			return "", errors.New("referenced configuration path must be repository-relative")
		}
		logical := path.Clean(path.Join(string(current.root), config))
		if logical == ".." || strings.HasPrefix(logical, "../") || path.Ext(logical) != ".json" {
			return "", errors.New("referenced configuration must be a repository-contained JSON file")
		}
		return repositoryFile(repositoryRoot, scope.Path(logical))
	default:
		return "", errors.New("package Jest configuration must be an object or JSON-file path")
	}
}

func repositoryFile(repositoryRoot string, logical scope.Path) (string, error) {
	repositoryPhysical, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	absolute := filepath.Join(repositoryPhysical, filepath.FromSlash(string(logical)))
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("configuration is not a regular file")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	if !pathutil.Within(repositoryPhysical, resolved) {
		return "", errors.New("configuration resolves outside the repository")
	}
	return resolved, nil
}
