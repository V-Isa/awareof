package jest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/V-Isa/awareof/internal/pathutil"
)

type jestInstallation struct {
	root    string
	entry   string
	version string
	major   string
}

type jestMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func loadProjectInstallation(repositoryRoot string, current jestContext) (jestInstallation, error) {
	repositoryAbsolute, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return jestInstallation{}, fmt.Errorf("resolve repository root: %w", err)
	}
	repositoryPhysical, err := filepath.EvalSymlinks(repositoryAbsolute)
	if err != nil {
		return jestInstallation{}, fmt.Errorf("resolve physical repository root: %w", err)
	}
	contextRoot := repositoryPhysical
	if current.root != "." {
		contextRoot = filepath.Join(repositoryPhysical, filepath.FromSlash(string(current.root)))
	}
	for directory := contextRoot; ; directory = filepath.Dir(directory) {
		candidate := filepath.Join(directory, "node_modules", "jest")
		if _, err := os.Lstat(candidate); err == nil {
			resolved, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				return jestInstallation{}, fmt.Errorf("resolve project Jest package: %w", err)
			}
			if !pathutil.Within(repositoryPhysical, resolved) {
				return jestInstallation{}, errors.New("project Jest package resolves outside the repository")
			}
			return loadInstallation(resolved)
		} else if !errors.Is(err, os.ErrNotExist) {
			return jestInstallation{}, fmt.Errorf("inspect project Jest package: %w", err)
		}
		if filepath.Clean(directory) == filepath.Clean(repositoryPhysical) {
			break
		}
		parent := filepath.Dir(directory)
		if !pathutil.Within(repositoryPhysical, parent) {
			break
		}
	}
	return jestInstallation{}, errors.New("no installed Jest package was found from the context root")
}

func loadInstallation(packageRoot string) (jestInstallation, error) {
	metadata, err := readJestMetadata(packageRoot)
	if err != nil {
		return jestInstallation{}, err
	}
	if metadata.Name != "jest" || metadata.Version == "" {
		return jestInstallation{}, errors.New("package is not an identified Jest installation")
	}
	major, err := supportedVersion(metadata.Version)
	if err != nil {
		return jestInstallation{}, err
	}
	entry := filepath.Join(packageRoot, "bin", "jest.js")
	resolvedEntry, err := filepath.EvalSymlinks(entry)
	if err != nil {
		return jestInstallation{}, fmt.Errorf("resolve Jest CLI entry point: %w", err)
	}
	if !pathutil.Within(packageRoot, resolvedEntry) {
		return jestInstallation{}, errors.New("jest CLI entry point resolves outside its package")
	}
	info, err := os.Stat(resolvedEntry)
	if err != nil {
		return jestInstallation{}, fmt.Errorf("inspect Jest CLI entry point: %w", err)
	}
	if !info.Mode().IsRegular() {
		return jestInstallation{}, errors.New("jest CLI entry point is not a regular file")
	}
	return jestInstallation{root: packageRoot, entry: resolvedEntry, version: metadata.Version, major: major}, nil
}

func readJestMetadata(packageRoot string) (_ jestMetadata, returnErr error) {
	root, err := os.OpenRoot(packageRoot)
	if err != nil {
		return jestMetadata{}, fmt.Errorf("open Jest package root: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Jest package root: %w", err))
		}
	}()
	content, err := readRegularFile(root, "package.json", maxManifestBytes)
	if err != nil {
		return jestMetadata{}, fmt.Errorf("read Jest package metadata: %w", err)
	}
	var metadata jestMetadata
	if err := decodeSingleJSON(content, &metadata); err != nil {
		return jestMetadata{}, fmt.Errorf("parse Jest package metadata: %w", err)
	}
	return metadata, nil
}

func supportedVersion(version string) (string, error) {
	parts := splitVersion(version)
	if parts == nil {
		return "", fmt.Errorf("unsupported Jest version %q; expected a stable x.y.z version", version)
	}
	if parts[0] != "29" && parts[0] != "30" {
		return "", fmt.Errorf("unsupported Jest version %q; supported major versions are 29 and 30", version)
	}
	return parts[0], nil
}

func splitVersion(version string) []string {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return nil
	}
	for _, component := range parts {
		if component == "" || (len(component) > 1 && component[0] == '0') {
			return nil
		}
		for _, digit := range []byte(component) {
			if digit < '0' || digit > '9' {
				return nil
			}
		}
	}
	return parts
}
