package repository

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	contractMarkers = []string{".awareof.yaml", ".awareof.yml"}
	vcsMarkers      = []string{".git"}
	manifestMarkers = []string{
		".github/CODEOWNERS",
		".dockerignore",
		"CODEOWNERS",
		"go.mod",
		"package.json",
		"Cargo.toml",
		"pyproject.toml",
		"compose.yaml",
		"compose.yml",
		"docker-compose.yaml",
		"docker-compose.yml",
		"docker-bake.hcl",
		"docker-bake.json",
		"docs/CODEOWNERS",
		"tsconfig.json",
	}
)

// ResolveRoot returns a canonical project root. An explicit root wins;
// otherwise contract, VCS, and manifest signals are considered in that order.
func ResolveRoot(start, explicit string) (string, error) {
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get working directory: %w", err)
		}
	}

	base, err := canonicalDirectory(start)
	if err != nil {
		return "", fmt.Errorf("resolve starting directory: %w", err)
	}
	if explicit != "" {
		if !filepath.IsAbs(explicit) {
			explicit = filepath.Join(base, explicit)
		}
		root, err := canonicalDirectory(explicit)
		if err != nil {
			return "", fmt.Errorf("resolve explicit root: %w", err)
		}
		return root, nil
	}

	var contractRoot, vcsRoot, manifestRoot string
	for dir := base; ; dir = filepath.Dir(dir) {
		if contractRoot == "" {
			found, err := hasAnyFileMarker(dir, contractMarkers)
			if err != nil {
				return "", fmt.Errorf("inspect contract markers in %q: %w", dir, err)
			}
			if found {
				contractRoot = dir
			}
		}
		if vcsRoot == "" {
			found, err := hasAnyVCSMarker(dir, vcsMarkers)
			if err != nil {
				return "", fmt.Errorf("inspect VCS markers in %q: %w", dir, err)
			}
			if found {
				vcsRoot = dir
			}
		}
		if manifestRoot == "" {
			found, err := hasAnyFileMarker(dir, manifestMarkers)
			if err != nil {
				return "", fmt.Errorf("inspect manifest markers in %q: %w", dir, err)
			}
			if found {
				manifestRoot = dir
			}
		}
		if contractRoot != "" && vcsRoot != "" && manifestRoot != "" {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	switch {
	case contractRoot != "":
		return contractRoot, nil
	case vcsRoot != "":
		return vcsRoot, nil
	case manifestRoot != "":
		return manifestRoot, nil
	default:
		return base, nil
	}
}

func canonicalDirectory(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return filepath.Clean(resolved), nil
}

func hasAnyFileMarker(dir string, markers []string) (bool, error) {
	return hasAnyMarker(dir, markers, func(info os.FileInfo) bool { return info.Mode().IsRegular() })
}

func hasAnyVCSMarker(dir string, markers []string) (bool, error) {
	return hasAnyMarker(dir, markers, func(info os.FileInfo) bool { return info.Mode().IsRegular() || info.IsDir() })
}

func hasAnyMarker(dir string, markers []string, accepted func(os.FileInfo) bool) (bool, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return false, fmt.Errorf("inspect marker directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("inspect marker directory %q: not a directory", dir)
	}
	for _, marker := range markers {
		info, err := os.Lstat(filepath.Join(dir, marker))
		switch {
		case err == nil && accepted(info):
			return true, nil
		case err == nil:
			continue
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return false, fmt.Errorf("inspect marker %q: %w", marker, err)
		}
	}
	return false, nil
}
