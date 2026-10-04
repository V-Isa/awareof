package prettier

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/V-Isa/awareof/internal/pathutil"
)

const (
	maxPackageFileCount = 4_096
	maxPackageFileSize  = 32 * 1024 * 1024
	maxPackageTotalSize = 128 * 1024 * 1024
)

type packageInstallation struct {
	root    string
	entry   string
	version string
}

type prettierMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func loadProjectInstallation(repositoryRoot string, current prettierContext) (packageInstallation, error) {
	repositoryAbsolute, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return packageInstallation{}, fmt.Errorf("resolve repository root: %w", err)
	}
	repositoryPhysical, err := filepath.EvalSymlinks(repositoryAbsolute)
	if err != nil {
		return packageInstallation{}, fmt.Errorf("resolve physical repository root: %w", err)
	}
	contextRoot := repositoryPhysical
	if current.root != "." {
		contextRoot = filepath.Join(repositoryPhysical, filepath.FromSlash(string(current.root)))
	}
	for directory := contextRoot; ; directory = filepath.Dir(directory) {
		candidate := filepath.Join(directory, "node_modules", "prettier")
		if _, err := os.Lstat(candidate); err == nil {
			resolved, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				return packageInstallation{}, fmt.Errorf("resolve project Prettier package: %w", err)
			}
			if !pathutil.Within(repositoryPhysical, resolved) {
				return packageInstallation{}, errors.New("project Prettier package resolves outside the repository")
			}
			return loadPackageInstallation(resolved, "")
		} else if !errors.Is(err, os.ErrNotExist) {
			return packageInstallation{}, fmt.Errorf("inspect project Prettier package: %w", err)
		}
		if filepath.Clean(directory) == filepath.Clean(repositoryPhysical) {
			break
		}
		if !pathutil.Within(repositoryPhysical, filepath.Dir(directory)) {
			break
		}
	}
	return packageInstallation{}, errors.New("no installed Prettier package was found from the context root")
}

func loadEvaluatorInstallation(targetPath string) (packageInstallation, error) {
	current := filepath.Dir(targetPath)
	for {
		metadata, err := readPrettierMetadata(current)
		if err == nil && metadata.Name == "prettier" {
			installation, err := loadPackageInstallation(current, targetPath)
			if err != nil {
				return packageInstallation{}, err
			}
			return installation, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return packageInstallation{}, errors.New("selected entry point is not inside a Prettier package")
}

func loadPackageInstallation(packageRoot, selectedEntry string) (packageInstallation, error) {
	resolvedRoot, err := filepath.EvalSymlinks(packageRoot)
	if err != nil {
		return packageInstallation{}, fmt.Errorf("resolve Prettier package root: %w", err)
	}
	metadata, err := readPrettierMetadata(resolvedRoot)
	if err != nil {
		return packageInstallation{}, err
	}
	if metadata.Name != "prettier" || metadata.Version == "" {
		return packageInstallation{}, errors.New("package is not an identified Prettier installation")
	}
	if err := supportedVersion(metadata.Version); err != nil {
		return packageInstallation{}, err
	}
	entry := selectedEntry
	canonicalEntry := filepath.Join(resolvedRoot, "index.mjs")
	if entry == "" {
		entry = canonicalEntry
	}
	resolvedEntry, err := filepath.EvalSymlinks(entry)
	if err != nil {
		return packageInstallation{}, fmt.Errorf("resolve Prettier API entry point: %w", err)
	}
	if !pathutil.Within(resolvedRoot, resolvedEntry) {
		return packageInstallation{}, errors.New("prettier API entry point resolves outside its package")
	}
	resolvedCanonical, err := filepath.EvalSymlinks(canonicalEntry)
	if err != nil {
		return packageInstallation{}, fmt.Errorf("resolve canonical Prettier API entry point: %w", err)
	}
	if filepath.Clean(resolvedEntry) != filepath.Clean(resolvedCanonical) {
		return packageInstallation{}, errors.New("selected entry point is not the Prettier public API index.mjs")
	}
	info, err := os.Stat(resolvedEntry)
	if err != nil {
		return packageInstallation{}, fmt.Errorf("inspect Prettier API entry point: %w", err)
	}
	if !info.Mode().IsRegular() {
		return packageInstallation{}, errors.New("prettier API entry point is not a regular file")
	}
	return packageInstallation{root: resolvedRoot, entry: resolvedEntry, version: metadata.Version}, nil
}

func readPrettierMetadata(packageRoot string) (_ prettierMetadata, returnErr error) {
	root, err := os.OpenRoot(packageRoot)
	if err != nil {
		return prettierMetadata{}, fmt.Errorf("open Prettier package root: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Prettier package root: %w", err))
		}
	}()

	file, err := root.Open("package.json")
	if err != nil {
		return prettierMetadata{}, fmt.Errorf("read Prettier package metadata: %w", err)
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return prettierMetadata{}, fmt.Errorf("read Prettier package metadata: %w", readErr)
	}
	if closeErr != nil {
		return prettierMetadata{}, fmt.Errorf("close Prettier package metadata: %w", closeErr)
	}
	if len(content) > maxManifestBytes {
		return prettierMetadata{}, fmt.Errorf("prettier package metadata exceeds %d bytes", maxManifestBytes)
	}

	var metadata prettierMetadata
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&metadata); err != nil {
		return prettierMetadata{}, fmt.Errorf("parse Prettier package metadata: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return prettierMetadata{}, errors.New("parse Prettier package metadata: multiple JSON values")
		}
		return prettierMetadata{}, fmt.Errorf("parse Prettier package metadata: %w", err)
	}
	return metadata, nil
}

func supportedVersion(version string) error {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) != 3 {
		return fmt.Errorf("unsupported Prettier version %q", version)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major != 3 {
		return fmt.Errorf("unsupported Prettier version %q; supported major version is 3", version)
	}
	if _, err := strconv.Atoi(parts[1]); err != nil {
		return fmt.Errorf("unsupported Prettier version %q", version)
	}
	if _, err := strconv.Atoi(parts[2]); err != nil {
		return fmt.Errorf("unsupported Prettier version %q; prerelease versions are not supported", version)
	}
	return nil
}

func packageFingerprint(installation packageInstallation) (_ string, returnErr error) {
	root, err := os.OpenRoot(installation.root)
	if err != nil {
		return "", fmt.Errorf("open Prettier package root: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Prettier package root: %w", err))
		}
	}()

	files := []string{}
	var total int64
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("prettier package entry %q is a symbolic link", name)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect Prettier package entry %q: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("prettier package entry %q is not regular", name)
		}
		if info.Size() < 0 || info.Size() > maxPackageFileSize {
			return fmt.Errorf("prettier package entry %q has size %d; limit is %d", name, info.Size(), maxPackageFileSize)
		}
		total += info.Size()
		if total > maxPackageTotalSize {
			return fmt.Errorf("prettier package exceeds %d bytes", maxPackageTotalSize)
		}
		files = append(files, filepath.ToSlash(name))
		if len(files) > maxPackageFileCount {
			return fmt.Errorf("prettier package contains more than %d files", maxPackageFileCount)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("fingerprint Prettier package: %w", err)
	}
	if len(files) == 0 {
		return "", errors.New("prettier package is empty")
	}
	slices.Sort(files)

	digest := sha256.New()
	var readTotal int64
	for _, name := range files {
		if err := writeFingerprintPart(digest, []byte(name)); err != nil {
			return "", err
		}
		readTotal, err = hashPackageEntry(root, digest, name, readTotal)
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func hashPackageEntry(root *os.Root, digest hash.Hash, name string, readTotal int64) (_ int64, returnErr error) {
	file, err := root.Open(name)
	if err != nil {
		return 0, fmt.Errorf("open Prettier package entry %q: %w", name, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Prettier package entry %q: %w", name, err))
		}
	}()

	info, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("inspect Prettier package entry %q: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxPackageFileSize {
		return 0, fmt.Errorf("prettier package entry %q changed to an unsupported size or type", name)
	}
	readTotal += info.Size()
	if readTotal > maxPackageTotalSize {
		return 0, fmt.Errorf("prettier package exceeds %d bytes while reading", maxPackageTotalSize)
	}
	if err := writeFingerprintLength(digest, uint64(info.Size())); err != nil { //nolint:gosec // size is checked above.
		return 0, err
	}
	if _, err := io.CopyN(digest, file, info.Size()); err != nil {
		return 0, fmt.Errorf("read Prettier package entry %q: %w", name, err)
	}
	var extra [1]byte
	if count, err := file.Read(extra[:]); err != nil && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("recheck Prettier package entry %q: %w", name, err)
	} else if count != 0 {
		return 0, fmt.Errorf("prettier package entry %q changed while reading", name)
	}
	return readTotal, nil
}

func writeFingerprintPart(destination hash.Hash, value []byte) error {
	if err := writeFingerprintLength(destination, uint64(len(value))); err != nil {
		return err
	}
	if _, err := destination.Write(value); err != nil {
		return fmt.Errorf("hash Prettier package identity: %w", err)
	}
	return nil
}

func writeFingerprintLength(destination hash.Hash, value uint64) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	if _, err := destination.Write(encoded[:]); err != nil {
		return fmt.Errorf("hash Prettier package identity: %w", err)
	}
	return nil
}
