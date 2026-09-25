package worktree

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"marshal/internal/app/config"
)

// seedDocsArchivePath is seeded into every worktree so subagents can read
// the specs/plans the parent authored there. It becomes a real directory
// of per-entry symlinks rather than one whole-directory link — see
// seedShallowDir for why that distinction is load-bearing.
const seedDocsArchivePath = ".docs-archive"

// seedEntrySpec is one seedable path: the configured entry plus the
// implicit archive entry's extra behaviour.
type seedEntrySpec struct {
	path    string
	mode    string
	shallow bool // real dir in the worktree, one symlink per top-level source entry
}

// SeedIntoWorktree seeds cfg.Seed entries from srcRoot (the project
// checkout) into dstRoot (a fresh worktree). Seed entries exist for
// git-ignored material a toolchain expects to find locally — node_modules,
// caches, .env — which a fresh checkout lacks.
//
// In addition to cfg.Seed, an implicit .docs-archive entry is always
// attempted. It is seeded as a shallow directory: a real directory in the
// worktree holding one symlink per top-level entry of the checkout's
// .docs-archive. A whole-directory symlink would defeat the gitignore that
// keeps the archive out of version control — git's ignore matching is
// type-sensitive, so a directory-form pattern ("dir/") does not match a
// symlink — and the untracked link then gets committed by `git add -A` and
// a later merge destroys the real archive. The shallow directory keeps the
// worktree's status clean and the archive unreachable by commit while
// reads and writes still flow through to the checkout. It is skipped
// silently when the checkout has no .docs-archive, and never clobbers a
// destination that already exists.
//
// Every failure is returned as a warning, never an error: a worktree with
// a missing node_modules symlink is still usable, and seeding must never
// block run startup. An empty return means every entry was seeded or
// legitimately skipped.
func SeedIntoWorktree(cfg config.WorktreeConfig, git GitOps, srcRoot, dstRoot string) []string {
	var warnings []string
	// Prepend the implicit entry, then per-config entries, preserving order.
	// Guards below (skip on missing, skip on tracked, no clobber) keep this
	// safe for both kinds of entry alike.
	seeds := make([]seedEntrySpec, 0, 1+len(cfg.Seed))
	seeds = append(seeds, seedEntrySpec{path: seedDocsArchivePath, mode: "symlink", shallow: true})
	for _, e := range cfg.Seed {
		if e.Path == seedDocsArchivePath {
			// The implicit entry already covers this path — an explicit entry
			// would otherwise warn "already exists in the worktree" on every
			// fresh worktree. It is also never honoured as a whole-directory
			// symlink: that is precisely the shape that gets committed and
			// destroys the checkout's archive on merge-back, so an explicit
			// mode="symlink" (or the default "copy") must not reintroduce it.
			if e.Mode != "symlink" {
				warnings = append(warnings, fmt.Sprintf(
					"seed path %q is seeded as a live shallow link; configured mode %q ignored", e.Path, e.Mode))
			}
			continue
		}
		seeds = append(seeds, seedEntrySpec{path: e.Path, mode: e.Mode})
	}
	for _, entry := range seeds {
		p := entry.path
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
			// check-ignore reports both tracked paths and paths covered by no
			// ignore rule, so name both rather than claiming "tracked" for a
			// repo that simply has no .gitignore entry.
			warnings = append(warnings, fmt.Sprintf("seed path %q is git-tracked or not git-ignored; skipping (would shadow the checkout)", p))
			continue
		}
		dst := filepath.Join(dstRoot, filepath.FromSlash(p))
		if _, err := os.Lstat(dst); err == nil {
			// Never overwrite the checkout's real files, and never clobber
			// whatever created the destination before us.
			warnings = append(warnings, fmt.Sprintf("seed path %q already exists in the worktree; skipping", p))
			continue
		}
		var seedErr error
		if entry.shallow {
			seedErr = seedShallowDir(src, dst)
		} else {
			seedErr = seedEntry(src, dst, entry.mode)
		}
		if seedErr != nil {
			warnings = append(warnings, fmt.Sprintf("seed path %q: %v", p, seedErr))
			continue
		}
	}
	return warnings
}

// seedShallowDir creates dst as a real directory and symlinks each of src's
// top-level entries into it.
//
// This is deliberately NOT a symlink to src itself. Git's ignore matching is
// type-sensitive: a directory-first pattern ("dir/", which is how a
// gitignored directory is normally written) does not match a symlink, so a
// lone symlink to the source tree shows up as untracked. `git add -A` then
// commits it and a later merge back replaces the real directory with a
// self-referential symlink — destroying the source. A real directory matches
// the pattern, keeping the worktree clean and the archive unreachable by
// commit.
//
// Each top-level entry resolves to the live source, so reads and edits of
// paths that already exist in the checkout flow through. A NEW top-level
// entry created inside the worktree is the exception: this directory is
// real, so such a file lands here and does not reach the checkout on finish.
// Treat the archive as read-mostly from a worktree, or author new specs in
// the checkout.
func seedShallowDir(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		// A symlink to a file-kind source is matched by a file-form ignore
		// pattern, so the whole-directory hazard does not apply.
		return seedEntry(src, dst, "symlink")
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		target, err := filepath.Abs(filepath.Join(src, e.Name()))
		if err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := os.Symlink(target, filepath.Join(dst, e.Name())); err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
	}
	return nil
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
