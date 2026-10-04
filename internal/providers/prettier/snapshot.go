package prettier

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/titanous/json5"
	"go.yaml.in/yaml/v3"

	"github.com/V-Isa/awareof/internal/scope"
)

const (
	maxConfigBytes = 1024 * 1024
	maxIgnoreBytes = 4 * 1024 * 1024
)

type configKind string

const (
	configPassive    configKind = "passive"
	configExecutable configKind = "executable"
	configShareable  configKind = "shareable"
	configSentinel   configKind = "sentinel"
)

type evaluatorRequest struct {
	PrettierEntry string                `json:"prettierEntry"`
	Paths         []string              `json:"paths"`
	IgnorePaths   []string              `json:"ignorePaths"`
	ConfigKinds   map[string]configKind `json:"configKinds"`
	ConfigRoot    string                `json:"configRoot"`
}

type snapshot struct {
	root           string
	cleanupRoot    string
	repositoryRoot string
	request        evaluatorRequest
	originalFiles  map[string]trackedFile
}

type trackedFile struct {
	digest [32]byte
	limit  int
}

func createSnapshot(
	repositoryRoot string,
	current prettierContext,
	paths []scope.Path,
	prettierEntry string,
	prettierVersion string,
) (_ snapshot, returnErr error) {
	directory, err := os.MkdirTemp("", "awareof-prettier-")
	if err != nil {
		return snapshot{}, fmt.Errorf("create Prettier snapshot: %w", err)
	}
	remove := true
	defer func() {
		if remove {
			returnErr = errors.Join(returnErr, os.RemoveAll(directory))
		}
	}()

	repositoryMirror := filepath.Join(directory, "repository")
	contextMirror := repositoryMirror
	if current.root != "." {
		contextMirror = filepath.Join(repositoryMirror, filepath.FromSlash(string(current.root)))
	}
	if err := os.MkdirAll(contextMirror, 0o700); err != nil {
		return snapshot{}, fmt.Errorf("create Prettier context snapshot: %w", err)
	}
	result := snapshot{
		root:           contextMirror,
		cleanupRoot:    directory,
		repositoryRoot: repositoryRoot,
		request: evaluatorRequest{
			PrettierEntry: prettierEntry,
			ConfigKinds:   make(map[string]configKind),
			ConfigRoot:    repositoryMirror,
		},
		originalFiles: make(map[string]trackedFile),
	}
	for _, ignoreName := range []string{".gitignore", ".prettierignore"} {
		logical := joinContext(current.root, ignoreName)
		target := filepath.Join(contextMirror, ignoreName)
		if err := result.copyRepositoryFile(repositoryRoot, logical, target, maxIgnoreBytes); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return snapshot{}, err
		}
		result.request.IgnorePaths = append(result.request.IgnorePaths, target)
	}

	seenDirectories := make(map[string]struct{})
	for _, name := range paths {
		if strings.ContainsAny(string(name), "\r\n") {
			return snapshot{}, fmt.Errorf("queried path %q contains a line break", name)
		}
		if err := rejectSymlinkPath(repositoryRoot, string(name)); err != nil {
			return snapshot{}, err
		}
		target := filepath.Join(repositoryMirror, filepath.FromSlash(string(name)))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return snapshot{}, fmt.Errorf("create Prettier snapshot path: %w", err)
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // target is confined to the new private snapshot root.
		if err != nil && !errors.Is(err, os.ErrExist) {
			return snapshot{}, fmt.Errorf("create Prettier snapshot target: %w", err)
		}
		if err == nil {
			if err := file.Close(); err != nil {
				return snapshot{}, fmt.Errorf("close Prettier snapshot target: %w", err)
			}
		}
		result.request.Paths = append(result.request.Paths, target)

		directoryName := path.Dir(string(name))
		for {
			seenDirectories[directoryName] = struct{}{}
			if directoryName == "." {
				break
			}
			directoryName = path.Dir(directoryName)
		}
	}

	directories := make([]string, 0, len(seenDirectories))
	for directoryName := range seenDirectories {
		directories = append(directories, directoryName)
	}
	slices.Sort(directories)
	versionConfigNames, err := configNamesForVersion(prettierVersion)
	if err != nil {
		return snapshot{}, fmt.Errorf("select Prettier configuration names: %w", err)
	}
	for _, directoryName := range directories {
		for _, configName := range versionConfigNames {
			logicalRepository := path.Join(directoryName, configName)
			content, kind, present, err := readConfigCandidate(repositoryRoot, logicalRepository, configName)
			if err != nil {
				return snapshot{}, err
			}
			if !present {
				continue
			}
			snapshotName := filepath.Join(repositoryMirror, filepath.FromSlash(logicalRepository))
			if err := os.MkdirAll(filepath.Dir(snapshotName), 0o700); err != nil {
				return snapshot{}, fmt.Errorf("create Prettier config directory: %w", err)
			}
			if err := os.WriteFile(snapshotName, content, 0o600); err != nil {
				return snapshot{}, fmt.Errorf("write Prettier snapshot config: %w", err)
			}
			result.request.ConfigKinds[filepath.Clean(snapshotName)] = kind
			result.recordOriginal(logicalRepository, content, maxConfigBytes)
		}
	}
	sentinel := filepath.Join(directory, ".prettierrc.json")
	if err := os.WriteFile(sentinel, []byte("{}\n"), 0o600); err != nil {
		return snapshot{}, fmt.Errorf("write Prettier snapshot boundary: %w", err)
	}
	result.request.ConfigKinds[filepath.Clean(sentinel)] = configSentinel

	remove = false
	return result, nil
}

func configNamesForVersion(version string) ([]string, error) {
	if err := supportedVersion(version); err != nil {
		return nil, err
	}
	parts := strings.SplitN(version, ".", 3)
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("parse Prettier minor version: %w", err)
	}
	names := make([]string, 0, len(configNames))
	for name := range configNames {
		if name == "package.yaml" && minor < 3 {
			continue
		}
		if isTypeScriptConfig(name) && minor < 5 {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

func isTypeScriptConfig(name string) bool {
	return strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".cts") || strings.HasSuffix(name, ".mts")
}

func (s *snapshot) copyRepositoryFile(repositoryRoot, logical, target string, limit int) error {
	content, err := readBoundedRegularFile(repositoryRoot, logical, limit)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create Prettier snapshot directory: %w", err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		return fmt.Errorf("write Prettier snapshot file: %w", err)
	}
	s.recordOriginal(logical, content, limit)
	return nil
}

func (s *snapshot) recordOriginal(logical string, content []byte, limit int) {
	s.originalFiles[logical] = trackedFile{digest: sha256.Sum256(content), limit: limit}
}

func (s snapshot) unchanged() error {
	for logical, original := range s.originalFiles {
		content, err := readBoundedRegularFile(s.repositoryRoot, logical, original.limit)
		if err != nil {
			return fmt.Errorf("recheck repository file %q: %w", logical, err)
		}
		if sha256.Sum256(content) != original.digest {
			return fmt.Errorf("repository file %q changed during evaluation", logical)
		}
	}
	return nil
}

func (s snapshot) close() error {
	if err := os.RemoveAll(s.cleanupRoot); err != nil {
		return fmt.Errorf("remove Prettier snapshot: %w", err)
	}
	return nil
}

func readConfigCandidate(repositoryRoot, logical, name string) ([]byte, configKind, bool, error) {
	content, err := readBoundedRegularFile(repositoryRoot, logical, maxConfigBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	if name == "package.json" {
		var document map[string]json.RawMessage
		if err := decodeJSON(content, &document); err != nil {
			return nil, "", false, fmt.Errorf("parse %q: %w", logical, err)
		}
		value, ok := document["prettier"]
		if !ok {
			return nil, "", false, nil
		}
		kind, err := classifyJSONValue(value)
		if err != nil {
			return nil, "", false, fmt.Errorf("classify %q: %w", logical, err)
		}
		minimal, err := json.Marshal(map[string]json.RawMessage{"prettier": value})
		if err != nil {
			return nil, "", false, fmt.Errorf("snapshot %q: %w", logical, err)
		}
		return append(minimal, '\n'), kind, true, nil
	}
	if name == "package.yaml" {
		kind, present, err := classifyPackageYAML(content)
		if err != nil {
			return nil, "", false, fmt.Errorf("classify %q: %w", logical, err)
		}
		return content, kind, present, nil
	}
	if isExecutableConfig(name) {
		return content, configExecutable, true, nil
	}
	if name == ".prettierrc.toml" {
		return content, configPassive, true, nil
	}
	if name == ".prettierrc" || strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml") {
		kind, err := classifyJSON5(content)
		if err == nil {
			return content, kind, true, nil
		}
		kind, err = classifyYAML(content)
		if err != nil {
			return nil, "", false, fmt.Errorf("classify %q: %w", logical, err)
		}
		return content, kind, true, nil
	}
	kind, err := classifyJSON5(content)
	if err != nil {
		return nil, "", false, fmt.Errorf("classify %q: %w", logical, err)
	}
	return content, kind, true, nil
}

func classifyJSON5(content []byte) (configKind, error) {
	var value any
	decoder := json5.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("multiple JSON5 values")
		}
		return "", err
	}
	if _, ok := value.(string); ok {
		return configShareable, nil
	}
	if _, ok := value.(map[string]any); !ok {
		return "", errors.New("configuration root is not an object")
	}
	return configPassive, nil
}

func classifyJSONValue(content json.RawMessage) (configKind, error) {
	var value any
	if err := decodeJSON(content, &value); err != nil {
		return "", err
	}
	if _, ok := value.(string); ok {
		return configShareable, nil
	}
	if _, ok := value.(map[string]any); !ok {
		return "", errors.New("configuration value is not an object")
	}
	return configPassive, nil
}

func classifyYAML(content []byte) (configKind, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&document); err != nil {
		return "", err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("multiple YAML documents")
		}
		return "", err
	}
	if len(document.Content) != 1 {
		return "", errors.New("configuration root is empty")
	}
	root := document.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!str" {
		return configShareable, nil
	}
	if root.Kind != yaml.MappingNode {
		return "", errors.New("configuration root is not an object")
	}
	return configPassive, nil
}

func classifyPackageYAML(content []byte) (configKind, bool, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&document); err != nil {
		return "", false, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", false, errors.New("multiple YAML documents")
		}
		return "", false, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return "", false, errors.New("package manifest root is not an object")
	}
	root := document.Content[0]
	for index := 0; index < len(root.Content); index += 2 {
		key := root.Content[index]
		value := root.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Value != "prettier" {
			continue
		}
		if value.Kind == yaml.ScalarNode && value.Tag == "!!str" {
			return configShareable, true, nil
		}
		if value.Kind != yaml.MappingNode {
			return "", false, errors.New("prettier configuration value is not an object")
		}
		return configPassive, true, nil
	}
	return "", false, nil
}

func decodeJSON(content []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(value); err != nil {
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

func isExecutableConfig(name string) bool {
	for _, suffix := range []string{".js", ".cjs", ".mjs", ".ts", ".cts", ".mts"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func readBoundedRegularFile(repositoryRoot, logical string, limit int) ([]byte, error) {
	absolute := filepath.Join(repositoryRoot, filepath.FromSlash(logical))
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("repository file %q is not a regular file", logical)
	}
	file, err := os.Open(absolute) //nolint:gosec // logical path is normalized and joined below the repository root.
	if err != nil {
		return nil, err
	}
	content, readErr := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(content) > limit {
		return nil, fmt.Errorf("repository file %q exceeds %d bytes", logical, limit)
	}
	return content, nil
}

func rejectSymlinkPath(repositoryRoot, logical string) error {
	current := repositoryRoot
	for _, component := range strings.Split(filepath.FromSlash(logical), string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect queried path %q: %w", logical, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("queried path %q contains a symbolic link", logical)
		}
	}
	return nil
}

func joinContext(root scope.Path, relative string) string {
	if root == "." {
		return path.Clean(relative)
	}
	return path.Join(string(root), relative)
}
