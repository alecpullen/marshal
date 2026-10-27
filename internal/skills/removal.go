package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RemoveScopedSkill removes the discovered direct child under root.
// It rejects invalid, symlinked, missing, or replaced entries.
//
// The removal target is the filesystem provenance recorded by ListScopes
// (SourceName + SourceInfo), never Skill.Name: frontmatter is untrusted
// content and may name something outside the store. The caller selects root
// from the known scope, but the scope is re-validated here so a hand-built
// ScopedSkill cannot reach a store it does not belong to.
func RemoveScopedSkill(root string, skill ScopedSkill) error {
	switch skill.Scope {
	case scopeGlobal, scopeProject:
	default:
		return fmt.Errorf("remove skill %q: unknown scope %q", skill.SourceName, skill.Scope)
	}

	name := skill.SourceName
	if name == "" {
		return errors.New("remove skill: empty source name")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("remove skill: invalid source name %q", name)
	}
	if strings.ContainsRune(name, filepath.Separator) || strings.ContainsRune(name, '/') {
		return fmt.Errorf("remove skill: invalid source name %q: contains a path separator", name)
	}
	if !filepath.IsLocal(name) {
		return fmt.Errorf("remove skill: invalid source name %q: not local to the store", name)
	}
	if skill.SourceInfo == nil {
		return fmt.Errorf("remove skill %q: no discovery snapshot (missing SourceInfo)", name)
	}

	rootDir, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("remove skill %q: open store %s: %w", name, root, err)
	}
	defer rootDir.Close()

	info, err := rootDir.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove skill %q: entry is missing from the store: %w", name, err)
		}
		return fmt.Errorf("remove skill %q: inspect entry: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("remove skill %q: refusing to remove symlink", name)
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return fmt.Errorf("remove skill %q: refusing to remove unsupported entry type %s", name, info.Mode().Type())
	}
	if !os.SameFile(info, skill.SourceInfo) {
		return fmt.Errorf("remove skill %q: entry was replaced since discovery", name)
	}

	if err := rootDir.RemoveAll(name); err != nil {
		return fmt.Errorf("remove skill %q: %w", name, err)
	}
	return nil
}
