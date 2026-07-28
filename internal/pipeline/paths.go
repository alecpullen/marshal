package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths is the on-disk layout of one pipeline run:
// <repo>/.marshal/pipeline/<plan-slug>/. Briefs, reports, review packages,
// the progress ledger, and per-run worktrees all live under Dir.
type Paths struct {
	Dir string
}

// NewPaths creates the run directory (and the self-ignoring .gitignore one
// level up, so the whole pipeline scratch area stays out of git) and
// returns the layout. Idempotent.
func NewPaths(repoRoot, slug string) (Paths, error) {
	base := filepath.Join(repoRoot, ".marshal", "pipeline")
	dir := filepath.Join(base, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Paths{}, fmt.Errorf("pipeline paths: mkdir %s: %w", dir, err)
	}
	gitignore := filepath.Join(base, ".gitignore")
	if _, err := os.Stat(gitignore); os.IsNotExist(err) {
		if err := os.WriteFile(gitignore, []byte("*\n"), 0o644); err != nil {
			return Paths{}, fmt.Errorf("pipeline paths: write .gitignore: %w", err)
		}
	}
	return Paths{Dir: dir}, nil
}

// Brief is the requirements file handed to task N's implementer.
func (p Paths) Brief(n int) string {
	return filepath.Join(p.Dir, fmt.Sprintf("task-%d-brief.md", n))
}

// Report is the file task N's implementer (and its fixers) write to.
func (p Paths) Report(n int) string {
	return filepath.Join(p.Dir, fmt.Sprintf("task-%d-report.md", n))
}

// Package is the review package for task N. Round 0 is the first review;
// later rounds get their own file so a re-review never reads a stale diff.
func (p Paths) Package(n, round int) string {
	if round == 0 {
		return filepath.Join(p.Dir, fmt.Sprintf("task-%d-review.md", n))
	}
	return filepath.Join(p.Dir, fmt.Sprintf("task-%d-review-round%d.md", n, round))
}

// BranchPackage is the whole-branch review package.
func (p Paths) BranchPackage() string {
	return filepath.Join(p.Dir, "branch-review.md")
}

// Ledger is the durable progress file.
func (p Paths) Ledger() string {
	return filepath.Join(p.Dir, "progress.md")
}

// WorktreesDir is where the run's git worktree is created.
func (p Paths) WorktreesDir() string {
	return filepath.Join(p.Dir, "worktrees")
}
