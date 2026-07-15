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

	// Reject explicit upward traversal. Multi-root callers bypass this
	// check by going through resolveAbsolute after they've already
	// determined the winning root.
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

	return resolveAbsolute(absRoot, filepath.Join(absRoot, cleaned))
}

// resolveAbsolute checks that full (already joined to a symlink-resolved
// root) does not escape that root through a symlink component. For paths
// that do not yet exist (new file case), it walks up the path until it
// finds an existing directory, resolves that through symlinks, then
// appends the non-existing tail. Returns the symlink-resolved absolute
// path on success, or ErrPathEscapes (wrapped) on escape.
//
// This is the single source of truth for symlink containment checks. Both
// SafeResolve and resolveWorkspacePathMulti call into it.
func resolveAbsolute(absRoot, full string) (string, error) {
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve %q: %w", full, err)
		}
		// New file: walk up until we find an existing directory,
		// resolve that, then append the non-existing tail.
		resolved, err = resolveUpThenDown(full)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", full, err)
		}
	}
	// In both branches, `resolved` is now the symlink-resolved absolute
	// path that the containment check must use.
	full = resolved

	// Verify containment: the resolved path must be under absRoot.
	relToRoot, err := filepath.Rel(absRoot, full)
	if err != nil {
		return "", fmt.Errorf("compute relative path: %w", err)
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path %q resolves outside root", ErrPathEscapes, full)
	}

	return full, nil
}

// resolveUpThenDown walks up from path until it finds a directory that
// exists, resolves that through symlinks, then appends the non-existing
// suffix back.
func resolveUpThenDown(path string) (string, error) {
	var tail []string
	for {
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("no existing parent in %q", path)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			// Found existing component; append remaining tail.
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		tail = append(tail, filepath.Base(path))
		path = parent
	}
}
