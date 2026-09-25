package worktree

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"marshal/internal/app/config"
)

// seedDocsArchivePath is symlinked into every worktree so subagents
// can read specs/plans the parent authored there. Mode is symlink so
// reads/writes on either side stay in sync.
const seedDocsArchivePath = ".docs-archive"

// SeedIntoWorktree seeds cfg.Seed entries from srcRoot (the project
// checkout) into dstRoot (a fresh worktree). Seed entries exist for
// git-ignored material a toolchain expects to find locally — node_modules,
// caches, .env — which a fresh checkout lacks.
//
// In addition to cfg.Seed, an implicit .docs-archive entry is always
// attempted: it is symlinked into the worktree by default so subagents can
// read specs/plans the parent authored in the checkout. It is skipped
// silently when the checkout has no .docs-archive, and never clobbers a
// destination that already exists.
//
// Every failure is returned as a warning, never an error: a worktree with
// a missing node_modules symlink is still usable, and seeding must never
// block run startup. An empty return means every entry was seeded or
// legitimately skipped.
func SeedIntoWorktree(cfg config.WorktreeConfig, git GitOps, srcRoot, dstRoot string) []string {
	var warnings []string
	// Prepend implicit entries, then per-config entries. Guards below
	// (skip on missing, skip on tracked, no clobber) keep this safe.
	seeds := append([]config.WorktreeSeed{
		{Path: seedDocsArchivePath, Mode: "symlink"},
	}, cfg.Seed...)
	for _, entry := range seeds {
		p := entry.Path
		// A seed entry is a path inside the repo. Absolute paths and ..
		// escapes would read or link outside the checkout — refuse them.
		if !filepath.IsLocal(p) {
			warnings = append(warnings, fmt.Sprintf("seed path %q is not a repo-relative path; skipping", p))
			continue
		}
		src := filepath.Join(srcRoot, filepath.FromSlash(p))
		if _, err := os.Lstat(src); err != nil {
			// A source the checkout has not produced yet is normal — skip
			// silently. Anything else is worth a warning.
			if !os.IsNotExist(err) {
				warnings = append(warnings, fmt.Sprintf("seed path %q: stat source: %v", p, err))
			}
			continue
		}
		// Only ignored paths may be seeded. A tracked path already reaches
		// the worktree through the checkout; seeding it would shadow the
		// real file with a copy that silently diverges.
		ignored, err := git.CheckIgnore(srcRoot, p)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("seed path %q: check-ignore: %v", p, err))
			continue
		}
		if !ignored {
			warnings = append(warnings, fmt.Sprintf("seed path %q is git-tracked; skipping (would shadow the checkout)", p))
			continue
		}
		dst := filepath.Join(dstRoot, filepath.FromSlash(p))
		if _, err := os.Lstat(dst); err == nil {
			// Never overwrite the checkout's real files, and never clobber
			// whatever created the destination before us.
			warnings = append(warnings, fmt.Sprintf("seed path %q already exists in the worktree; skipping", p))
			continue
		}
		if err := seedEntry(src, dst, entry.Mode); err != nil {
			warnings = append(warnings, fmt.Sprintf("seed path %q: %v", p, err))
			continue
		}
	}
	return warnings
}

// seedEntry seeds one path. Mode "symlink" links dst to src — resolved to
// an absolute target, so the link works no matter where it is read from —
// and any other mode copies.
func seedEntry(src, dst, mode string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if mode == "symlink" {
		if abs, err := filepath.Abs(src); err == nil {
			src = abs
		}
		if err := os.Symlink(src, dst); err != nil {
			return fmt.Errorf("symlink: %w", err)
		}
		return nil
	}
	return copyPath(src, dst)
}

// copyPath copies src to dst recursively. Seeded material is disposable
// (caches, env files), so the copy uses fixed modes — files 0o644, dirs
// 0o755 — rather than preserving the source's.
func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
