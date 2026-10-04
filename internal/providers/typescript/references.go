package typescript

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/V-Isa/awareof/internal/pathutil"
	"github.com/V-Isa/awareof/internal/scope"
	"github.com/titanous/json5"
)

const maxConfigSize = 4 * 1024 * 1024

type referenceConfig struct {
	References []struct {
		Path string `json:"path"`
	} `json:"references"`
}

func readReferences(rootPath string, config scope.Path) ([]scope.Path, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	content, readErr := readConfig(root, string(config))
	closeErr := root.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close repository root: %w", closeErr)
	}
	var parsed referenceConfig
	decoder := json5.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("parse %s references: %w", config, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("parse %s references: multiple JSON values", config)
		}
		return nil, fmt.Errorf("parse %s references: %w", config, err)
	}

	set := make(map[scope.Path]struct{}, len(parsed.References))
	for _, reference := range parsed.References {
		resolved, err := resolveReference(rootPath, config, reference.Path)
		if err != nil {
			return nil, err
		}
		set[resolved] = struct{}{}
	}
	return sortedPaths(set), nil
}

func readConfig(root *os.Root, name string) (_ []byte, returnErr error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close %s: %w", name, err))
		}
	}()
	content, err := io.ReadAll(io.LimitReader(file, maxConfigSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(content) > maxConfigSize {
		return nil, fmt.Errorf("read %s: file exceeds %d bytes", name, maxConfigSize)
	}
	return content, nil
}

func resolveReference(rootPath string, config scope.Path, reference string) (scope.Path, error) {
	if reference == "" || strings.ContainsRune(reference, 0) {
		return "", fmt.Errorf("%s contains an invalid empty project reference", config)
	}
	normalized := strings.ReplaceAll(reference, "\\", "/")
	if path.IsAbs(normalized) || filepath.IsAbs(reference) {
		return "", fmt.Errorf("%s reference %q leaves the repository", config, reference)
	}
	candidate := path.Clean(path.Join(path.Dir(string(config)), normalized))
	if candidate == ".." || strings.HasPrefix(candidate, "../") {
		return "", fmt.Errorf("%s reference %q leaves the repository", config, reference)
	}
	rootAbsolute, err := filepath.Abs(rootPath)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbsolute)
	if err != nil {
		return "", fmt.Errorf("resolve repository root symlinks: %w", err)
	}

	logical, absolute, err := referenceFile(rootAbsolute, candidate)
	if err != nil {
		return "", fmt.Errorf("resolve %s reference %q: %w", config, reference, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve %s reference %q symlinks: %w", config, reference, err)
	}
	if !pathutil.Within(rootResolved, resolved) {
		return "", fmt.Errorf("%s reference %q resolves outside the repository", config, reference)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect %s reference %q: %w", config, reference, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s reference %q is not a regular config file", config, reference)
	}
	return scope.Path(logical), nil
}

func referenceFile(root, candidate string) (string, string, error) {
	absolute := filepath.Join(root, filepath.FromSlash(candidate))
	info, err := os.Stat(absolute)
	if err == nil && info.IsDir() {
		logical := path.Join(candidate, "tsconfig.json")
		return logical, filepath.Join(absolute, "tsconfig.json"), nil
	}
	if err == nil {
		return candidate, absolute, nil
	}
	return "", "", err
}
