package repo

import (
	"os"
	"path/filepath"
)

// FindRoot returns the nearest ancestor of dir (including dir itself)
// containing a .git entry (directory or worktree file), or "" when dir is
// not inside a git repository. The walk stops at the filesystem root, so
// a non-git directory yields "" and callers fall back to the original
// directory — non-git locations behave exactly as before.
func FindRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	cur := abs
	for {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// Root returns FindRoot(dir) when dir is inside a git repository and dir
// itself otherwise. Callers that need a usable directory (never "") use
// this variant.
func Root(dir string) string {
	if root := FindRoot(dir); root != "" {
		return root
	}
	return dir
}

// Canonical resolves symlinks in path when the path exists. When path (or
// any prefix) does not exist, it walks up to the nearest existing ancestor,
// canonicalizes that, and re-appends the unresolved tail. The result is
// consistent across both existing and not-yet-existing paths, so a symlinked
// directory cannot be canonicalized into a different namespace than its
// parent.
func Canonical(path string) string {
	if path == "" {
		return ""
	}
	tail := ""
	cur := path
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, tail)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			abs, aerr := filepath.Abs(path)
			if aerr != nil {
				return path
			}
			return abs
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}
