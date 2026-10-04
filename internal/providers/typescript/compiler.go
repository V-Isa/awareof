package typescript

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/V-Isa/awareof/internal/pathutil"
	"github.com/V-Isa/awareof/internal/safeexec"
	"github.com/V-Isa/awareof/internal/scope"
)

const (
	maxPackageJSONSize       = 1024 * 1024
	maxFingerprintFileCount  = 512
	maxFingerprintFileSize   = 64 * 1024 * 1024
	maxFingerprintTotalBytes = 128 * 1024 * 1024
)

type platform struct {
	goos   string
	goarch string
}

type compilerInstallation struct {
	packageRoot string
	corePath    string
	coreName    string
	name        string
	version     string
	major       int
}

type packageMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (p *Provider) platform() platform {
	current := platform{goos: p.goos, goarch: p.goarch}
	if current.goos == "" {
		current.goos = runtime.GOOS
	}
	if current.goarch == "" {
		current.goarch = runtime.GOARCH
	}
	return current
}

func findProjectCompiler(root string, config scope.Path, current platform) (compilerInstallation, error) {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return compilerInstallation{}, fmt.Errorf("resolve repository root: %w", err)
	}
	directory := filepath.Dir(filepath.FromSlash(string(config)))
	for {
		packageRoot := filepath.Join(rootAbsolute, directory, "node_modules", "typescript")
		metadata, err := readPackageMetadata(packageRoot)
		if err == nil {
			if metadata.Name != "typescript" {
				return compilerInstallation{}, fmt.Errorf("installed compiler package is named %q, want %q", metadata.Name, "typescript")
			}
			major, err := majorVersion(metadata.Version)
			if err != nil {
				return compilerInstallation{}, err
			}
			switch major {
			case 6:
				return loadInstallation(packageRoot, filepath.Join("lib", "_tsc.js"), metadata)
			case 7:
				platformPackage, err := platformPackageName(current)
				if err != nil {
					return compilerInstallation{}, err
				}
				platformRoot := filepath.Join(filepath.Dir(packageRoot), "@typescript", platformPackage)
				platformMetadata, err := readPackageMetadata(platformRoot)
				if err != nil {
					return compilerInstallation{}, fmt.Errorf("read TypeScript 7 platform package: %w", err)
				}
				wantName := "@typescript/" + platformPackage
				if platformMetadata.Name != wantName || platformMetadata.Version != metadata.Version {
					return compilerInstallation{}, fmt.Errorf(
						"TypeScript platform package is %s@%s, want %s@%s",
						platformMetadata.Name,
						platformMetadata.Version,
						wantName,
						metadata.Version,
					)
				}
				return loadInstallation(platformRoot, filepath.Join("lib", executableName(current.goos)), platformMetadata)
			default:
				return compilerInstallation{}, fmt.Errorf("TypeScript version %q is unsupported; select an exact TypeScript 6 or 7 evaluator", metadata.Version)
			}
		}
		if !errors.Is(err, os.ErrNotExist) {
			return compilerInstallation{}, fmt.Errorf("inspect installed TypeScript package: %w", err)
		}
		if directory == "." || directory == "" {
			break
		}
		directory = filepath.Dir(directory)
	}
	return compilerInstallation{}, errors.New("no installed TypeScript package was found from the project directory to the repository root")
}

func installationFromTarget(target safeexec.Target, current platform) (compilerInstallation, error) {
	base := filepath.Base(target.Path)
	lib := filepath.Dir(target.Path)
	if filepath.Base(lib) != "lib" {
		return compilerInstallation{}, fmt.Errorf("selected evaluator %q is not a TypeScript compiler core under lib", target.Path)
	}
	packageRoot := filepath.Dir(lib)
	metadata, err := readPackageMetadata(packageRoot)
	if err != nil {
		return compilerInstallation{}, fmt.Errorf("read selected TypeScript evaluator metadata: %w", err)
	}
	major, err := majorVersion(metadata.Version)
	if err != nil {
		return compilerInstallation{}, err
	}
	switch major {
	case 6:
		if base != "_tsc.js" || metadata.Name != "typescript" {
			return compilerInstallation{}, errors.New("TypeScript 6 evaluator must be the external typescript/lib/_tsc.js compiler bundle")
		}
	case 7:
		platformPackage, err := platformPackageName(current)
		if err != nil {
			return compilerInstallation{}, err
		}
		if base != executableName(current.goos) || metadata.Name != "@typescript/"+platformPackage {
			return compilerInstallation{}, fmt.Errorf("TypeScript 7 evaluator must be the %s native lib/tsc binary", platformPackage)
		}
		if err := validateNativeExecutable(target.Path, current.goos); err != nil {
			return compilerInstallation{}, err
		}
	default:
		return compilerInstallation{}, fmt.Errorf("selected TypeScript evaluator version %q is unsupported", metadata.Version)
	}
	return loadInstallation(packageRoot, filepath.Join("lib", base), metadata)
}

func (p *Provider) evaluatorCandidates(root string, project compilerInstallation) ([]safeexec.Target, error) {
	discoverer, ok := p.discoverer.(derivedTargetDiscoverer)
	if !ok {
		return []safeexec.Target{}, nil
	}
	paths := []string{}
	if project.major == 7 {
		paths = append(paths, project.corePath)
	} else {
		approved, err := discoverer.ApprovedTargets(root, typescriptTool)
		if err != nil {
			return nil, fmt.Errorf("inspect approved TypeScript evaluators: %w", err)
		}
		for _, target := range approved {
			paths = append(paths, target.Path)
		}
		if candidate, err := p.externalTypeScript6Candidate(); err == nil {
			paths = append(paths, candidate)
		}
	}
	seen := make(map[string]struct{}, len(paths))
	targets := make([]safeexec.Target, 0, len(paths))
	for _, candidate := range paths {
		target, err := discoverer.DiscoverAt(root, typescriptTool, candidate)
		if err != nil {
			continue
		}
		if _, exists := seen[target.Path]; exists {
			continue
		}
		matches, err := evaluatorMatchesProject(project, target, p.platform())
		if err != nil || !matches {
			continue
		}
		seen[target.Path] = struct{}{}
		targets = append(targets, target)
	}
	return targets, nil
}

func (p *Provider) externalTypeScript6Candidate() (string, error) {
	if p.lookPath == nil {
		return "", errors.New("PATH lookup is unavailable")
	}
	wrapper, err := p.lookPath("tsc")
	if err != nil {
		return "", err
	}
	wrapper, err = filepath.Abs(wrapper)
	if err != nil {
		return "", fmt.Errorf("resolve PATH TypeScript launcher: %w", err)
	}
	wrapper, err = filepath.EvalSymlinks(wrapper)
	if err != nil {
		return "", fmt.Errorf("resolve PATH TypeScript launcher symlinks: %w", err)
	}
	if filepath.Base(filepath.Dir(wrapper)) != "bin" || filepath.Base(wrapper) != "tsc" {
		return "", errors.New("PATH tsc is not an official TypeScript package launcher")
	}
	packageRoot := filepath.Dir(filepath.Dir(wrapper))
	metadata, err := readPackageMetadata(packageRoot)
	if err != nil {
		return "", fmt.Errorf("read PATH TypeScript package: %w", err)
	}
	if metadata.Name != "typescript" {
		return "", fmt.Errorf("PATH tsc package is named %q", metadata.Name)
	}
	major, err := majorVersion(metadata.Version)
	if err != nil {
		return "", err
	}
	if major != 6 {
		return "", fmt.Errorf("PATH tsc is TypeScript %s, not TypeScript 6", metadata.Version)
	}
	installation, err := loadInstallation(packageRoot, filepath.Join("lib", "_tsc.js"), metadata)
	if err != nil {
		return "", err
	}
	return installation.corePath, nil
}

func evaluatorMatchesProject(project compilerInstallation, target safeexec.Target, current platform) (bool, error) {
	selected, err := installationFromTarget(target, current)
	if err != nil {
		return false, err
	}
	if project.major != selected.major || project.version != selected.version {
		return false, nil
	}
	if project.major == 6 && target.Origin != safeexec.ExternalOrigin {
		return false, nil
	}
	projectFingerprint, err := packageFingerprint(project)
	if err != nil {
		return false, err
	}
	selectedFingerprint, err := packageFingerprint(selected)
	if err != nil {
		return false, err
	}
	return projectFingerprint == selectedFingerprint, nil
}

func loadInstallation(packageRoot, coreName string, metadata packageMetadata) (compilerInstallation, error) {
	resolvedRoot, err := filepath.EvalSymlinks(packageRoot)
	if err != nil {
		return compilerInstallation{}, fmt.Errorf("resolve TypeScript package root: %w", err)
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return compilerInstallation{}, fmt.Errorf("resolve TypeScript package root path: %w", err)
	}
	corePath := filepath.Join(resolvedRoot, coreName)
	info, err := os.Stat(corePath)
	if err != nil {
		return compilerInstallation{}, fmt.Errorf("inspect TypeScript compiler core: %w", err)
	}
	if !info.Mode().IsRegular() {
		return compilerInstallation{}, errors.New("TypeScript compiler core is not a regular file")
	}
	major, err := majorVersion(metadata.Version)
	if err != nil {
		return compilerInstallation{}, err
	}
	return compilerInstallation{
		packageRoot: resolvedRoot,
		corePath:    corePath,
		coreName:    filepath.ToSlash(coreName),
		name:        metadata.Name,
		version:     metadata.Version,
		major:       major,
	}, nil
}

func readPackageMetadata(packageRoot string) (_ packageMetadata, returnErr error) {
	name := filepath.Join(packageRoot, "package.json")
	file, err := os.Open(name) //nolint:gosec // name is the fixed package.json beneath a safely derived package root.
	if err != nil {
		return packageMetadata{}, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close %s: %w", name, err))
		}
	}()
	content, err := io.ReadAll(io.LimitReader(file, maxPackageJSONSize+1))
	if err != nil {
		return packageMetadata{}, fmt.Errorf("read %s: %w", name, err)
	}
	if len(content) > maxPackageJSONSize {
		return packageMetadata{}, fmt.Errorf("read %s: file exceeds %d bytes", name, maxPackageJSONSize)
	}
	var metadata packageMetadata
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&metadata); err != nil {
		return packageMetadata{}, fmt.Errorf("parse %s: %w", name, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return packageMetadata{}, fmt.Errorf("parse %s: multiple JSON values", name)
		}
		return packageMetadata{}, fmt.Errorf("parse %s: %w", name, err)
	}
	if metadata.Name == "" || metadata.Version == "" {
		return packageMetadata{}, fmt.Errorf("parse %s: name and version are required", name)
	}
	return metadata, nil
}

func majorVersion(version string) (int, error) {
	majorText, _, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil || major <= 0 {
		return 0, fmt.Errorf("invalid TypeScript version %q", version)
	}
	return major, nil
}

func platformPackageName(current platform) (string, error) {
	suffixes := map[platform]string{
		{goos: "darwin", goarch: "amd64"}:   "darwin-x64",
		{goos: "darwin", goarch: "arm64"}:   "darwin-arm64",
		{goos: "linux", goarch: "amd64"}:    "linux-x64",
		{goos: "linux", goarch: "arm"}:      "linux-arm",
		{goos: "linux", goarch: "arm64"}:    "linux-arm64",
		{goos: "linux", goarch: "loong64"}:  "linux-loong64",
		{goos: "linux", goarch: "mips64le"}: "linux-mips64el",
		{goos: "linux", goarch: "ppc64"}:    "linux-ppc64",
		{goos: "linux", goarch: "riscv64"}:  "linux-riscv64",
		{goos: "linux", goarch: "s390x"}:    "linux-s390x",
		{goos: "windows", goarch: "amd64"}:  "win32-x64",
		{goos: "windows", goarch: "arm64"}:  "win32-arm64",
	}
	suffix, ok := suffixes[current]
	if !ok {
		return "", fmt.Errorf("TypeScript 7 is unsupported on %s/%s", current.goos, current.goarch)
	}
	return "typescript-" + suffix, nil
}

func executableName(goos string) string {
	if goos == "windows" {
		return "tsc.exe"
	}
	return "tsc"
}

func validateNativeExecutable(name, goos string) (returnErr error) {
	file, err := os.Open(name) //nolint:gosec // name is the exact selected compiler core inspected before approval.
	if err != nil {
		return fmt.Errorf("open TypeScript 7 evaluator: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close TypeScript 7 evaluator: %w", err))
		}
	}()
	header := make([]byte, 4)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("read TypeScript 7 evaluator header: %w", err)
	}
	valid := false
	switch goos {
	case "linux":
		valid = string(header) == "\x7fELF"
	case "darwin":
		magic := binary.BigEndian.Uint32(header)
		valid = magic == 0xfeedface || magic == 0xfeedfacf || magic == 0xcefaedfe || magic == 0xcffaedfe || magic == 0xcafebabe || magic == 0xbebafeca
	case "windows":
		valid = header[0] == 'M' && header[1] == 'Z'
	}
	if !valid {
		return errors.New("selected TypeScript 7 evaluator is not a native executable for this platform")
	}
	return nil
}

func packageFingerprint(installation compilerInstallation) (_ string, returnErr error) {
	files, err := fingerprintFiles(installation)
	if err != nil {
		return "", err
	}
	if len(files) > maxFingerprintFileCount {
		return "", fmt.Errorf("TypeScript fingerprint contains %d files; limit is %d", len(files), maxFingerprintFileCount)
	}
	root, err := os.OpenRoot(installation.packageRoot)
	if err != nil {
		return "", fmt.Errorf("open TypeScript package root: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close TypeScript package root: %w", err))
		}
	}()

	hash := sha256.New()
	var total int64
	for _, relative := range files {
		absolute := filepath.Join(installation.packageRoot, filepath.FromSlash(relative))
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", fmt.Errorf("resolve TypeScript fingerprint file %q: %w", relative, err)
		}
		if !pathutil.Within(installation.packageRoot, resolved) {
			return "", fmt.Errorf("TypeScript fingerprint file %q resolves outside the package", relative)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return "", fmt.Errorf("inspect TypeScript fingerprint file %q: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("TypeScript fingerprint file %q is not regular", relative)
		}
		if info.Size() < 0 || info.Size() > maxFingerprintFileSize {
			return "", fmt.Errorf("TypeScript fingerprint file %q has size %d; limit is %d", relative, info.Size(), maxFingerprintFileSize)
		}
		total += info.Size()
		if total > maxFingerprintTotalBytes {
			return "", fmt.Errorf("TypeScript fingerprint exceeds %d bytes", maxFingerprintTotalBytes)
		}
		if err := writeFingerprintPart(hash, []byte(relative)); err != nil {
			return "", err
		}
		if err := writeFingerprintLength(hash, uint64(info.Size())); err != nil { //nolint:gosec // size is checked as nonnegative above.
			return "", err
		}
		if err := hashFingerprintFile(root, relative, info.Size(), hash); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fingerprintFiles(installation compilerInstallation) ([]string, error) {
	libDirectory := filepath.Join(installation.packageRoot, "lib")
	resolvedLibrary, err := filepath.EvalSymlinks(libDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve TypeScript library directory: %w", err)
	}
	if !pathutil.Within(installation.packageRoot, resolvedLibrary) {
		return nil, errors.New("TypeScript library directory resolves outside the package")
	}
	entries, err := os.ReadDir(resolvedLibrary)
	if err != nil {
		return nil, fmt.Errorf("read TypeScript library directory: %w", err)
	}
	files := []string{"package.json", installation.coreName}
	libraries := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "lib") || !strings.HasSuffix(name, ".d.ts") {
			continue
		}
		files = append(files, filepath.ToSlash(filepath.Join("lib", name)))
		libraries++
	}
	if libraries == 0 {
		return nil, errors.New("TypeScript package has no standard-library declarations")
	}
	sort.Strings(files)
	return files, nil
}

func writeFingerprintPart(writer io.Writer, content []byte) error {
	if err := writeFingerprintLength(writer, uint64(len(content))); err != nil {
		return err
	}
	if _, err := writer.Write(content); err != nil {
		return fmt.Errorf("write TypeScript fingerprint content: %w", err)
	}
	return nil
}

func writeFingerprintLength(writer io.Writer, size uint64) error {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], size)
	if _, err := writer.Write(length[:]); err != nil {
		return fmt.Errorf("write TypeScript fingerprint length: %w", err)
	}
	return nil
}

func hashFingerprintFile(root *os.Root, relative string, size int64, writer io.Writer) (returnErr error) {
	file, err := root.Open(filepath.FromSlash(relative))
	if err != nil {
		return fmt.Errorf("open TypeScript fingerprint file %q: %w", relative, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close TypeScript fingerprint file %q: %w", relative, err))
		}
	}()
	if _, err := io.CopyN(writer, file, size); err != nil {
		return fmt.Errorf("read TypeScript fingerprint file %q: %w", relative, err)
	}
	var extra [1]byte
	count, err := file.Read(extra[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("finish TypeScript fingerprint file %q: %w", relative, err)
	}
	if count != 0 {
		return fmt.Errorf("TypeScript fingerprint file %q changed while it was read", relative)
	}
	return nil
}
