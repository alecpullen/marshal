// internal/pathutil/safe.go
package pathutil

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SafeWorkspacePath resolves a relative path against root, rejecting
// absolute paths and upward traversal via "..". It returns the cleaned
// joined path on success. This is the lexical (non-symlink-resolving)
// variant; use native.SafeResolve when symlink containment is needed.
func SafeWorkspacePath(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be relative: %s", rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace root: %s", rel)
	}
	return filepath.Join(root, cleaned), nil
}
