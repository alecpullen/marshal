package repo

import (
	"path/filepath"
)

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
