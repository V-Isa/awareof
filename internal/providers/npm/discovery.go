package npm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/V-Isa/awareof/internal/scope"
)

var errDiscoveryLimit = errors.New("npm package discovery limit reached")

// discoverNestedPackages finds package roots only when the repository root has
// no package.json. This covers repositories whose JavaScript project lives in
// a subdirectory without treating every nested fixture in a root package as an
// independent publication target.
func discoverNestedPackages(ctx context.Context, root *os.Root, maxEntries int) ([]scope.Path, *scope.Explanation, error) {
	set := make(map[scope.Path]struct{})
	entries := 0
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > maxEntries {
			return errDiscoveryLimit
		}
		if entry.IsDir() && name != "." {
			base := entry.Name()
			if base == "node_modules" || strings.HasPrefix(base, ".") {
				return fs.SkipDir
			}
		}
		if name != "package.json" && entry.Name() == "package.json" {
			set[scope.Path(path.Dir(name))] = struct{}{}
		}
		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		code := "npm/package-discovery-unavailable"
		summary := "nested npm package discovery could not be completed safely"
		evidence := err.Error()
		if errors.Is(err, errDiscoveryLimit) {
			code = "npm/package-discovery-limit"
			summary = fmt.Sprintf("nested npm package discovery exceeds %d repository entries", maxEntries)
			evidence = "the repository may contain additional npm packages"
		}
		return sortedPackageRoots(set), &scope.Explanation{
			Code:     code,
			Summary:  summary,
			Evidence: evidence,
			Action:   "use --root to inspect a specific package directory",
		}, nil
	}
	return sortedPackageRoots(set), nil, nil
}

func sortedPackageRoots(set map[scope.Path]struct{}) []scope.Path {
	paths := make([]scope.Path, 0, len(set))
	for packageRoot := range set {
		paths = append(paths, packageRoot)
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })
	return paths
}
