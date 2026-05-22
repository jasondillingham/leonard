package groundtruth

import (
	"fmt"
	"path/filepath"
	"strings"
)

// relPathSafe returns filepath.Rel(root, path) but errors instead of
// returning a path starting with "..". Used by the filter guards
// where a path outside the project shouldn't be inspected at all.
func relPathSafe(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q is outside project root %q", path, root)
	}
	return rel, nil
}
