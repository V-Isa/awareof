package eslint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/V-Isa/awareof/internal/pathutil"
)

type eslintInstallation struct {
	root    string
	entry   string
	version string
}

type eslintMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func loadProjectInstallation(repositoryRoot string, current eslintContext) (eslintInstallation, error) {
	repositoryAbsolute, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return eslintInstallation{}, fmt.Errorf("resolve repository root: %w", err)
	}
	repositoryPhysical, err := filepath.EvalSymlinks(repositoryAbsolute)
	if err != nil {
		return eslintInstallation{}, fmt.Errorf("resolve physical repository root: %w", err)
	}
	contextRoot := repositoryPhysical
	if current.root != "." {
		contextRoot = filepath.Join(repositoryPhysical, filepath.FromSlash(string(current.root)))
	}
	for directory := contextRoot; ; directory = filepath.Dir(directory) {
		candidate := filepath.Join(directory, "node_modules", "eslint")
		if _, err := os.Lstat(candidate); err == nil {
			resolved, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				return eslintInstallation{}, fmt.Errorf("resolve project ESLint package: %w", err)
			}
			if !pathutil.Within(repositoryPhysical, resolved) {
				return eslintInstallation{}, errors.New("project ESLint package resolves outside the repository")
			}
			return loadInstallation(resolved)
		} else if !errors.Is(err, os.ErrNotExist) {
			return eslintInstallation{}, fmt.Errorf("inspect project ESLint package: %w", err)
		}
		if filepath.Clean(directory) == filepath.Clean(repositoryPhysical) {
			break
		}
		parent := filepath.Dir(directory)
		if !pathutil.Within(repositoryPhysical, parent) {
			break
		}
	}
	return eslintInstallation{}, errors.New("no installed ESLint package was found from the context root")
}

func loadInstallation(packageRoot string) (eslintInstallation, error) {
	metadata, err := readESLintMetadata(packageRoot)
	if err != nil {
		return eslintInstallation{}, err
	}
	if metadata.Name != "eslint" || metadata.Version == "" {
		return eslintInstallation{}, errors.New("package is not an identified ESLint installation")
	}
	if err := supportedVersion(metadata.Version); err != nil {
		return eslintInstallation{}, err
	}
	entry := filepath.Join(packageRoot, "lib", "api.js")
	resolvedEntry, err := filepath.EvalSymlinks(entry)
	if err != nil {
		return eslintInstallation{}, fmt.Errorf("resolve ESLint API entry point: %w", err)
	}
	if !pathutil.Within(packageRoot, resolvedEntry) {
		return eslintInstallation{}, errors.New("eslint API entry point resolves outside its package")
	}
	info, err := os.Stat(resolvedEntry)
	if err != nil {
		return eslintInstallation{}, fmt.Errorf("inspect ESLint API entry point: %w", err)
	}
	if !info.Mode().IsRegular() {
		return eslintInstallation{}, errors.New("eslint API entry point is not a regular file")
	}
	return eslintInstallation{root: packageRoot, entry: resolvedEntry, version: metadata.Version}, nil
}

func readESLintMetadata(packageRoot string) (_ eslintMetadata, returnErr error) {
	root, err := os.OpenRoot(packageRoot)
	if err != nil {
		return eslintMetadata{}, fmt.Errorf("open ESLint package root: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close ESLint package root: %w", err))
		}
	}()
	content, err := readRegularFile(root, "package.json", maxManifestBytes)
	if err != nil {
		return eslintMetadata{}, fmt.Errorf("read ESLint package metadata: %w", err)
	}
	var metadata eslintMetadata
	if err := decodeSingleJSON(content, &metadata); err != nil {
		return eslintMetadata{}, fmt.Errorf("parse ESLint package metadata: %w", err)
	}
	return metadata, nil
}

func supportedVersion(version string) error {
	parts := splitVersion(version)
	if parts == nil {
		return fmt.Errorf("unsupported ESLint version %q; expected a stable x.y.z version", version)
	}
	if parts[0] != "9" && parts[0] != "10" {
		return fmt.Errorf("unsupported ESLint version %q; supported major versions are 9 and 10", version)
	}
	return nil
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
