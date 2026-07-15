package native

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscapes is returned by SafeResolve when the resolved path lies
// outside the designated workspace root.
var ErrPathEscapes = errors.New("native: path escapes workspace root")

// SafeResolve resolves a relative path rel against root, following symlinks,
// and verifies that the result is still contained within root.
//
// It rejects absolute paths and paths that traverse upward via "..".
//
// If the resolved file does not yet exist (new file case), SafeResolve
// resolves the parent directory and appends the leaf component, still
// checking containment.
//
// On success it returns the absolute, cleaned, symlink-resolved path.
// On escape it returns ErrPathEscapes (wrapped).
func SafeResolve(root, rel string) (string, error) {
	// Reject absolute paths immediately.
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: path %q is absolute", ErrPathEscapes, rel)
	}

	// Reject explicit upward traversal.
	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path %q traverses upward", ErrPathEscapes, rel)
	}

	// Normalise root and resolve any symlinks in root itself so that
	// containment is checked in the real filesystem namespace.
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", root, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("resolve root symlinks %q: %w", absRoot, err)
	}
	absRoot = resolvedRoot

	full := filepath.Join(absRoot, cleaned)

	// Try to resolve the full path through symlinks.
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		// If the path doesn't exist (new file case), resolve the parent
		// and append the leaf component.
		if os.IsNotExist(err) {
			parent := filepath.Dir(cleaned)
			var resolvedParent string
			if parent == "." {
				resolvedParent = absRoot
			} else {
				resolvedParent, err = filepath.EvalSymlinks(filepath.Join(absRoot, parent))
				if err != nil {
					return "", fmt.Errorf("resolve parent of %q: %w", rel, err)
				}
			}
			full = filepath.Join(resolvedParent, filepath.Base(cleaned))
		} else {
			return "", fmt.Errorf("resolve %q: %w", rel, err)
		}
	} else {
		full = resolved
	}

	// Verify containment: the resolved path must be under absRoot.
	relToRoot, err := filepath.Rel(absRoot, full)
	if err != nil {
		return "", fmt.Errorf("compute relative path: %w", err)
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: resolved path %q escapes root %q", ErrPathEscapes, full, absRoot)
	}

	return full, nil
}
