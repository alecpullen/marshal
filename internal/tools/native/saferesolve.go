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

// readAllowedUnderHome are the Marshal-owned directories file.read and
// file.page may reach without a workspace override. Writes NEVER use this
// list. Every entry is a directory subtree; a single-file entry would need
// exact-match handling of its own rather than the prefix rule below.
//
// $HOME/.config/marshal/config.toml is deliberately absent: it can carry
// literal provider api_key values (config types.go: "literal key; wins over
// APIKeyEnv"), and file.read would hand them to the model verbatim. The
// config.read tool already serves that need with the keys masked.
var readAllowedUnderHome = []string{
	".config/marshal/postmortems",
	".config/marshal/skills",
}

// IsReadAllowedOutsideWorkspace reports whether abs is one of the
// explicitly allowlisted Marshal-owned paths. Decoupled so tests
// can inject a fake HOME via t.Setenv.
//
// The check resolves symlinks on BOTH sides before comparing. Comparing the
// unresolved request would let a symlink planted anywhere inside an
// allowlisted tree pivot the read to an arbitrary target; comparing against
// an unresolved entry would break the legitimate case where the user
// symlinks their own ~/.config/marshal directory elsewhere.
func IsReadAllowedOutsideWorkspace(abs string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	resolved, err := resolveSystemPath(filepath.Clean(abs))
	if err != nil {
		return false
	}
	for _, rel := range readAllowedUnderHome {
		allowed := filepath.Join(home, filepath.FromSlash(rel))
		// Refuse an entry whose own root is a symlink. Resolving both sides
		// closes pivots INSIDE an allowlisted tree, but a symlink AT the
		// allowlisted directory makes the resolved entry equal to whatever
		// it points at, which would re-open the pivot for every path under
		// it. A symlinked PARENT (the user keeps ~/.config/marshal
		// elsewhere) is still fine — only the entry itself is checked.
		if info, lerr := os.Lstat(allowed); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		resolvedAllowed, err := resolveSystemPath(allowed)
		if err != nil {
			continue
		}
		if containsPath(resolvedAllowed, resolved) {
			return true
		}
	}
	return false
}

// expandHomeDir expands a leading "~/" in p against the user's home
// directory. Non-~ paths are returned unchanged.
func expandHomeDir(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", p, err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~/")), nil
}

// detectDoubledWorktree reports whether path repeats the same
// .marshal/worktrees/<branch>/ segment. On hit it returns the cleaned
// suggestion with the duplicate collapsed.
func detectDoubledWorktree(p string) (string, bool) {
	// Match on the slash form so the diagnostic works for paths built with
	// filepath.Join on any platform (a hardcoded separator missed Windows
	// paths entirely). Slash and backslash are both one byte, so the
	// offsets computed here slice the ORIGINAL p, and the suggestion keeps
	// the caller's own separator style.
	norm := filepath.ToSlash(p)
	const marker = ".marshal/worktrees/"
	idx := strings.Index(norm, marker)
	if idx < 0 {
		return "", false
	}
	rest := norm[idx+len(marker):]
	segEnd := strings.IndexByte(rest, '/')
	if segEnd < 0 {
		return "", false
	}
	branch := rest[:segEnd]
	dup := marker + branch + "/"
	first := strings.Index(norm, dup)
	second := strings.Index(norm[first+len(dup):], dup)
	if second < 0 {
		return "", false
	}
	// Collapse the duplicate.
	cleaned := p[:first+len(dup)] + p[first+len(dup)+second+len(dup):]
	return cleaned, true
}

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
	// Propagate rather than swallow: a failure here means ~ could not be
	// expanded, and silently treating "~/x" as a relative path named "~"
	// hides the real cause behind a confusing not-found.
	expanded, err := expandHomeDir(rel)
	if err != nil {
		return "", err
	}
	rel = expanded
	if suggestion, ok := detectDoubledWorktree(rel); ok {
		return "", fmt.Errorf("%w: path %q contains a duplicated worktree segment; relative paths resolve from the current worktree root — did you mean %q?", ErrPathEscapes, rel, suggestion)
	}

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
	resolved, err := resolveTarget(full)
	if err != nil {
		return "", err
	}
	if !containsPath(absRoot, resolved) {
		return "", fmt.Errorf("%w: path %q resolves outside root", ErrPathEscapes, resolved)
	}
	return resolved, nil
}

// resolveTarget resolves full through symlinks and returns the
// symlink-resolved absolute target without judging containment. The
// containment judgement is a separate step (containsPath) so callers that
// need to weigh the SAME target against more than one root — the multi-root
// resolver — can do so without resolving twice.
func resolveTarget(full string) (string, error) {
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
	return resolved, nil
}

// containsPath reports whether the already symlink-resolved path lies inside
// absRoot. It is deliberately a pure predicate: an unjudgeable pair (a
// filepath.Rel failure, which needs one path to be relative) is reported as
// not contained, so callers fail closed.
func containsPath(absRoot, resolved string) bool {
	relToRoot, err := filepath.Rel(absRoot, resolved)
	if err != nil {
		return false
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return false
	}
	return true
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
