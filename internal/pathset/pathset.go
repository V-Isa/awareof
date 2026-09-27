package pathset

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/V-Isa/awareof/internal/pathutil"
	"github.com/V-Isa/awareof/internal/scope"
)

// Builder constructs one canonical PathSet from user selectors.
type Builder struct {
	Root string
	Base string
}

// BuildOptions controls how command-line path selectors are interpreted.
type BuildOptions struct {
	Literal   bool
	NullInput bool
}

// BuildLiterals normalizes literal output from a path source. It does not
// expand directories or globs, and it preserves missing logical paths.
func (b Builder) BuildLiterals(ctx context.Context, inputs []string) ([]scope.Path, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, _, err := b.directories()
	if err != nil {
		return nil, err
	}
	set := make(map[scope.Path]struct{}, len(inputs))
	for _, input := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if input == "" {
			return nil, errors.New("literal path is empty")
		}
		if path.IsAbs(input) || filepath.IsAbs(input) {
			return nil, fmt.Errorf("literal path %q must be repository-relative", input)
		}
		logical, err := normalizeExact(root, root, input)
		if err != nil {
			return nil, err
		}
		if logical == "." {
			return nil, errors.New("literal path must name a repository entry")
		}
		if isGitMetadata(string(logical)) {
			return nil, fmt.Errorf("path %q is Git metadata, not a repository path", input)
		}
		set[logical] = struct{}{}
	}
	paths := make([]scope.Path, 0, len(set))
	for name := range set {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		paths = append(paths, name)
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })
	return paths, nil
}

// Build expands exact paths, directories, core globs, and stdin. Returned
// paths are root-relative, slash-separated, deduplicated, and sorted.
func (b Builder) Build(
	ctx context.Context,
	inputs []string,
	stdin io.Reader,
	options BuildOptions,
) (_ []scope.Path, returnErr error) {
	if len(inputs) == 0 {
		return nil, errors.New("missing path or pattern")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, base, err := b.directories()
	if err != nil {
		return nil, err
	}
	scopedRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	defer func() {
		if err := scopedRoot.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close repository root: %w", err))
		}
	}()
	type selector struct {
		value   string
		literal bool
	}
	selectors := make([]selector, 0, len(inputs))
	stdinSeen := false
	for _, input := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if input != "-" {
			selectors = append(selectors, selector{value: input, literal: options.Literal})
			continue
		}
		if stdinSeen {
			return nil, errors.New("stdin selector '-' may be used only once")
		}
		stdinSeen = true
		if stdin == nil {
			return nil, errors.New("stdin selector requires input")
		}
		fromStdin, err := readSelectors(ctx, stdin, options.NullInput)
		if err != nil {
			return nil, fmt.Errorf("read paths from stdin: %w", err)
		}
		for _, name := range fromStdin {
			selectors = append(selectors, selector{value: name, literal: true})
		}
	}
	if options.NullInput && !stdinSeen {
		return nil, errors.New("--null-input requires the stdin selector '-'")
	}

	set := make(map[scope.Path]struct{})
	var catalog []scope.Path
	catalogLoaded := false
	for _, query := range selectors {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		selector := query.value
		if selector == "" {
			return nil, errors.New("path or pattern is empty")
		}
		if !query.literal && hasPatternSyntax(selector) {
			pattern, err := normalizePattern(root, base, selector)
			if err != nil {
				return nil, err
			}
			if err := validatePattern(pattern); err != nil {
				return nil, fmt.Errorf("invalid pattern %q: %w", selector, err)
			}
			if !catalogLoaded {
				catalog, err = walkEntries(ctx, scopedRoot, ".")
				if err != nil {
					return nil, err
				}
				catalogLoaded = true
			}
			for _, candidate := range catalog {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				matched, err := match(pattern, string(candidate))
				if err != nil {
					return nil, fmt.Errorf("match pattern %q: %w", selector, err)
				}
				if matched {
					set[candidate] = struct{}{}
				}
			}
			continue
		}

		logical, err := normalizeExact(root, base, selector)
		if err != nil {
			return nil, err
		}
		if isGitMetadata(string(logical)) {
			return nil, fmt.Errorf("path %q is Git metadata, not a repository path", selector)
		}
		if err := rejectSymlinkParents(scopedRoot, string(logical)); err != nil {
			return nil, err
		}
		info, err := scopedRoot.Lstat(string(logical))
		switch {
		case err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0:
			entries, err := walkEntries(ctx, scopedRoot, string(logical))
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				set[entry] = struct{}{}
			}
		case err == nil:
			set[logical] = struct{}{}
		case errors.Is(err, fs.ErrNotExist):
			set[logical] = struct{}{}
		default:
			return nil, fmt.Errorf("inspect path %q: %w", selector, err)
		}
	}

	paths := make([]scope.Path, 0, len(set))
	for item := range set {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		paths = append(paths, item)
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })
	return paths, nil
}

func (b Builder) directories() (string, string, error) {
	if b.Root == "" {
		return "", "", errors.New("repository root is empty")
	}
	root, err := filepath.Abs(b.Root)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository root: %w", err)
	}
	base := b.Base
	if base == "" {
		base = root
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return "", "", fmt.Errorf("resolve query base: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("open repository root: %w", err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return "", "", fmt.Errorf("resolve query base: %w", err)
	}
	if _, err := pathutil.RelativeWithin(root, base); err != nil {
		return "", "", fmt.Errorf("query base: %w", err)
	}
	return filepath.Clean(root), filepath.Clean(base), nil
}

func readSelectors(ctx context.Context, r io.Reader, nullInput bool) ([]string, error) {
	delim := byte('\n')
	if nullInput {
		delim = 0
	}
	reader := bufio.NewReader(r)
	var selectors []string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := reader.ReadString(delim)
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		if len(value) > 0 {
			value = strings.TrimSuffix(value, string(delim))
			if !nullInput {
				value = strings.TrimSuffix(value, "\r")
			}
			if value != "" {
				selectors = append(selectors, value)
			}
		}
		if errors.Is(err, io.EOF) {
			return selectors, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func normalizeExact(root, base, input string) (scope.Path, error) {
	if strings.ContainsRune(input, 0) || !utf8.ValidString(input) {
		return "", errors.New("paths must be valid UTF-8 without NUL bytes")
	}
	absolute := input
	if filepath.IsAbs(absolute) {
		var err error
		absolute, err = rebaseAbsolute(root, absolute)
		if err != nil {
			return "", err
		}
	} else {
		absolute = filepath.Join(base, filepath.FromSlash(input))
	}
	absolute = filepath.Clean(absolute)
	rel, err := pathutil.RelativeWithin(root, absolute)
	if err != nil {
		return "", fmt.Errorf("path %q: %w", input, err)
	}
	return scope.Path(filepath.ToSlash(rel)), nil
}

// Rebase an explicit absolute path through an alias of the root (for example,
// /var on macOS). Stop resolving symlinks at the root to preserve entry identity.
func rebaseAbsolute(root, input string) (string, error) {
	input = filepath.Clean(input)
	if pathutil.Within(root, input) {
		return input, nil
	}
	volume := filepath.VolumeName(input)
	prefix := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(input, prefix), string(filepath.Separator))
	for i, part := range parts {
		prefix = filepath.Join(prefix, part)
		resolved, err := filepath.EvalSymlinks(prefix)
		if err != nil {
			break
		}
		if resolved == root {
			return filepath.Join(append([]string{root}, parts[i+1:]...)...), nil
		}
	}
	return "", fmt.Errorf("path %q escapes repository root", input)
}

func rejectSymlinkParents(root *os.Root, name string) error {
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		parent := strings.Join(parts[:i], "/")
		info, err := root.Lstat(parent)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect parent %q: %w", parent, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q traverses symlink %q; query the symlink entry itself", name, parent)
		}
	}
	return nil
}

func normalizePattern(root, base, input string) (string, error) {
	input = filepath.ToSlash(input)
	var normalized string
	if filepath.IsAbs(filepath.FromSlash(input)) {
		rebased, err := rebaseAbsolute(root, filepath.FromSlash(input))
		if err != nil {
			return "", err
		}
		input = filepath.ToSlash(rebased)
		rootSlash := filepath.ToSlash(root)
		switch {
		case input == rootSlash:
			normalized = "."
		case strings.HasPrefix(input, rootSlash+"/"):
			normalized = strings.TrimPrefix(input, rootSlash+"/")
		default:
			return "", fmt.Errorf("pattern %q escapes repository root", input)
		}
	} else {
		baseRel, err := pathutil.RelativeWithin(root, base)
		if err != nil {
			return "", fmt.Errorf("pattern %q: %w", input, err)
		}
		normalized = path.Join(filepath.ToSlash(baseRel), input)
	}
	normalized = path.Clean(normalized)
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf("pattern %q escapes repository root", input)
	}
	return normalized, nil
}

func walkEntries(ctx context.Context, root *os.Root, start string) ([]scope.Path, error) {
	var entries []scope.Path
	err := fs.WalkDir(root.FS(), start, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == start {
			return nil
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		logical := path.Clean(current)
		if !utf8.ValidString(logical) {
			return fmt.Errorf("path %q is not valid UTF-8", logical)
		}
		if !isGitMetadata(logical) {
			entries = append(entries, scope.Path(logical))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk directory %q: %w", start, err)
	}
	return entries, nil
}

func isGitMetadata(logical string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(logical), "/") {
		if segment == ".git" {
			return true
		}
	}
	return false
}

func hasPatternSyntax(input string) bool {
	for _, char := range input {
		if strings.ContainsRune("*?[", char) {
			return true
		}
	}
	return false
}

func validatePattern(pattern string) error {
	if strings.ContainsAny(pattern, "{}") {
		return errors.New("brace expansion is not supported")
	}
	if strings.ContainsRune(pattern, 0) || !utf8.ValidString(pattern) {
		return errors.New("patterns must be valid UTF-8 without NUL bytes")
	}
	for _, segment := range strings.Split(pattern, "/") {
		if strings.Contains(segment, "**") && segment != "**" {
			return errors.New("'**' must occupy a complete path segment")
		}
		if segment == "**" {
			continue
		}
		if _, err := path.Match(segment, ""); err != nil {
			return err
		}
	}
	if !doublestar.ValidatePattern(pattern) {
		return doublestar.ErrBadPattern
	}
	return nil
}

func match(pattern, candidate string) (bool, error) {
	return doublestar.Match(pattern, candidate)
}

// NormalizeRootPattern validates one repository-root-relative pattern in the
// awareof query dialect. Contract patterns use this same dialect as CLI globs.
func NormalizeRootPattern(input string) (string, error) {
	if input == "" {
		return "", errors.New("pattern is empty")
	}
	if strings.ContainsRune(input, 0) || !utf8.ValidString(input) {
		return "", errors.New("patterns must be valid UTF-8 without NUL bytes")
	}
	input = filepath.ToSlash(input)
	if path.IsAbs(input) || filepath.IsAbs(filepath.FromSlash(input)) {
		return "", errors.New("pattern must be relative to the repository root")
	}
	normalized := path.Clean(input)
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", errors.New("pattern must name paths within the repository root")
	}
	if err := validatePattern(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

// MatchPattern reports whether a normalized repository path matches a
// normalized awareof pattern.
func MatchPattern(pattern string, candidate scope.Path) (bool, error) {
	return match(pattern, string(candidate))
}
