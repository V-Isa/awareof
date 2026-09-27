// Package pathutil contains shared filesystem path-boundary operations.
package pathutil

import (
	"errors"
	"path/filepath"
	"strings"
)

// RelativeWithin returns the lexical path from root to target. It rejects
// targets outside root without resolving symlinks; callers choose the
// identities to compare.
func RelativeWithin(root, target string) (string, error) {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes repository root")
	}
	return relative, nil
}

// Within reports whether target equals root or is a lexical descendant.
func Within(root, target string) bool {
	_, err := RelativeWithin(root, target)
	return err == nil
}
