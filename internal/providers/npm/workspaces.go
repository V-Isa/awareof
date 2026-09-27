package npm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/V-Isa/awareof/internal/scope"
)

type workspaceObject struct {
	Packages json.RawMessage `json:"packages"`
}

func discoverWorkspaces(ctx context.Context, root *os.Root, raw json.RawMessage) ([]scope.Path, string, error) {
	patterns, supported := workspacePatterns(raw)
	if !supported {
		return nil, "package.json uses workspace patterns that awareof cannot match safely", nil
	}
	set := make(map[scope.Path]struct{})
	for _, pattern := range patterns {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		matches, ok, err := expandWorkspacePattern(ctx, root, pattern)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			return nil, fmt.Sprintf("workspace pattern %q uses unsupported npm glob semantics", pattern), nil
		}
		for _, match := range matches {
			set[match] = struct{}{}
		}
	}
	paths := make([]scope.Path, 0, len(set))
	for match := range set {
		paths = append(paths, match)
	}
	sortPaths(paths)
	return paths, "", nil
}

func workspacePatterns(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var patterns []string
	if err := json.Unmarshal(raw, &patterns); err == nil {
		return patterns, true
	}
	var object workspaceObject
	if err := json.Unmarshal(raw, &object); err != nil || len(object.Packages) == 0 {
		return nil, false
	}
	if err := json.Unmarshal(object.Packages, &patterns); err != nil {
		return nil, false
	}
	return patterns, true
}

func expandWorkspacePattern(ctx context.Context, root *os.Root, pattern string) ([]scope.Path, bool, error) {
	normalized := strings.TrimPrefix(pattern, "./")
	normalized = strings.TrimLeft(normalized, "/")
	if normalized == "" || path.Clean(normalized) != normalized || strings.Contains(normalized, "\\") || strings.HasPrefix(normalized, "!") {
		return nil, false, nil
	}
	segments := strings.Split(normalized, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return nil, false, nil
		}
		if segment != "*" && strings.ContainsAny(segment, "*?[]{}()") {
			return nil, false, nil
		}
	}

	candidates := []string{"."}
	for _, segment := range segments {
		var next []string
		for _, candidate := range candidates {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			if segment == "*" {
				entries, err := fs.ReadDir(root.FS(), candidate)
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil {
					return nil, false, fmt.Errorf("read workspace directory %q: %w", candidate, err)
				}
				for _, entry := range entries {
					if !entry.IsDir() || entry.Name() == "node_modules" || strings.HasPrefix(entry.Name(), ".") {
						continue
					}
					next = append(next, path.Join(candidate, entry.Name()))
				}
				continue
			}
			if segment == "node_modules" {
				continue
			}
			name := path.Join(candidate, segment)
			info, err := root.Lstat(name)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				continue
			case err != nil:
				return nil, false, fmt.Errorf("inspect workspace path %q: %w", name, err)
			case info.Mode()&os.ModeSymlink != 0:
				return nil, false, nil
			case !info.IsDir():
				continue
			}
			next = append(next, name)
		}
		candidates = next
	}

	paths := make([]scope.Path, 0, len(candidates))
	for _, candidate := range candidates {
		clean := strings.TrimPrefix(candidate, "./")
		if clean != "." && clean != "" {
			paths = append(paths, scope.Path(clean))
		}
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })
	return paths, true, nil
}
