// internal/pathutil/resolve.go
package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveWithinRoots resolves a relative path against a primary root and any
// additional roots, following symlinks and verifying that the result stays
// inside the root that claimed it. It mirrors the native file tools' write-side
// resolution rules so a caller that only needs to READ a path the tools are
// about to edit lands on the same file the tools would have edited.
//
// It is the symlink-resolving counterpart to SafeWorkspacePath, which is
// lexical only. Callers that must match tool-layer semantics (multi-root
// projects, symlinked checkouts) want this one.
//
// Absolute paths are rejected: use ResolveAbsolute for a path that is already
// known to be absolute (system access).
func ResolveWithinRoots(root string, additionalRoots []string, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative", rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == "." {
		return root, nil
	}

	roots := make([]string, 1+len(additionalRoots))
	roots[0] = root
	copy(roots[1:], additionalRoots)

	var lastErr error
	for _, r := range roots {
		full := filepath.Join(r, cleaned)
		relToRoot, err := filepath.Rel(r, full)
		if err != nil {
			lastErr = err
			continue
		}
		if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
			lastErr = fmt.Errorf("path escapes workspace root: %s", rel)
			continue
		}
		absRoot, err := filepath.Abs(r)
		if err != nil {
			return "", err
		}
		resolvedRoot, err := filepath.EvalSymlinks(absRoot)
		if err != nil {
			return "", fmt.Errorf("resolve root %q: %w", absRoot, err)
		}
		return resolveContained(resolvedRoot, full)
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("path escapes workspace root: %s", rel)
}

// ResolveAbsolute canonicalizes an absolute path by following symlinks. It is
// the system-access counterpart to ResolveWithinRoots: containment is
// deliberately not checked, but a path that does not exist yet resolves
// through its nearest existing ancestor so a new-file target still yields a
// usable path.
func ResolveAbsolute(abs string) (string, error) {
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve %q: %w", abs, err)
	}
	resolved, err = resolveUpThenDown(abs)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", abs, err)
	}
	return resolved, nil
}

// WorkspaceRelative renders path relative to root, resolving symlinks on both
// sides first. It is the inverse of ResolveWithinRoots for display: a caller
// that resolved a patch target through ResolveWithinRoots can hand the result
// straight back to get the workspace-relative spelling the file tools accept.
//
// The symlink resolution matters because the workspace root and the target can
// disagree about the alias (e.g. root "/tmp/x" resolving to "/private/tmp/x"
// on macOS): comparing the raw strings would report a path inside the
// workspace as outside it and leak an absolute path into a hint that is
// supposed to be copy-pasteable into a tool call. When the path genuinely lies
// outside root, the resolved absolute path is returned.
func WorkspaceRelative(root, path string) string {
	resolvedRoot, err := ResolveAbsolute(root)
	if err != nil {
		return path
	}
	resolvedPath, err := ResolveAbsolute(path)
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return resolvedPath
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return resolvedPath
	}
	return rel
}

// resolveContained verifies that full (already joined to a symlink-resolved
// root) does not escape that root through a symlink component, and returns the
// symlink-resolved absolute path.
func resolveContained(absRoot, full string) (string, error) {
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve %q: %w", full, err)
		}
		resolved, err = resolveUpThenDown(full)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", full, err)
		}
	}
	relToRoot, err := filepath.Rel(absRoot, resolved)
	if err != nil {
		return "", fmt.Errorf("compute relative path: %w", err)
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path resolves outside workspace root: %s", full)
	}
	return resolved, nil
}

// resolveUpThenDown walks up from path until it finds a directory that exists,
// resolves that through symlinks, then appends the non-existing suffix back.
func resolveUpThenDown(path string) (string, error) {
	var tail []string
	for {
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("no existing parent in %q", path)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
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
