package sdd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Workspace manages the .marshal/sdd/ scratch directory: spec, dag, contracts,
// reports, diffs, checkpoints, worktrees, archive (spec §10). It replaces
// internal/agent/sdd/workspace.go (removed in P5).
type Workspace struct {
	root string
	dir  string
}

// NewWorkspace creates a Workspace rooted at the given working directory.
// The scratch directory lives at <root>/.marshal/sdd/.
func NewWorkspace(workingDir string) (*Workspace, error) {
	dir := filepath.Join(workingDir, ".marshal", "sdd")
	return &Workspace{root: workingDir, dir: dir}, nil
}

// Dir returns the .marshal/sdd/ path.
func (w *Workspace) Dir() string { return w.dir }

// WorktreesDir returns the .marshal/sdd/worktrees/ path (spec unknown 14).
func (w *Workspace) WorktreesDir() string {
	return filepath.Join(w.dir, "worktrees")
}

// Ensure creates the full directory tree and the self-ignoring .gitignore.
// Idempotent: safe to call repeatedly.
func (w *Workspace) Ensure() (string, error) {
	for _, sub := range []string{"state", "contracts", "reports", "diffs", "checkpoints", "worktrees", "archive"} {
		if err := os.MkdirAll(filepath.Join(w.dir, sub), 0755); err != nil {
			return "", fmt.Errorf("sdd workspace: mkdir %s: %w", sub, err)
		}
	}
	gitignore := filepath.Join(w.dir, ".gitignore")
	if _, err := os.Stat(gitignore); os.IsNotExist(err) {
		if err := os.WriteFile(gitignore, []byte("*\n"), 0644); err != nil {
			return "", fmt.Errorf("sdd workspace: write .gitignore: %w", err)
		}
	}
	return w.dir, nil
}

// Reset archives the current workspace to archive/<ISO>/ and clears the
// per-run artifacts (dag, spec, state, progress, contracts, reports, diffs,
// checkpoints). Worktrees and branches are cleaned separately by P2's
// Cleanup.Stale(); this method only touches the scratch files. Idempotent.
func (w *Workspace) Reset() error {
	if _, err := w.Ensure(); err != nil {
		return err
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	archiveDir := filepath.Join(w.dir, "archive", ts)
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		return fmt.Errorf("sdd workspace: mkdir archive: %w", err)
	}
	// Move per-run files into the archive if they exist.
	for _, name := range []string{"dag.json", "spec.md", "progress.md"} {
		src := filepath.Join(w.dir, name)
		if _, err := os.Stat(src); err == nil {
			if err := os.Rename(src, filepath.Join(archiveDir, name)); err != nil {
				return fmt.Errorf("sdd workspace: archive %s: %w", name, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(w.dir, "state", "repo.json")); err == nil {
		if err := os.Rename(filepath.Join(w.dir, "state", "repo.json"), filepath.Join(archiveDir, "repo.json")); err != nil {
			return fmt.Errorf("sdd workspace: archive repo.json: %w", err)
		}
	}
	// Archive the per-run subdirectory contents, then clear the subdirectories.
	for _, sub := range []string{"state", "contracts", "reports", "diffs", "checkpoints"} {
		d := filepath.Join(w.dir, sub)
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			src := filepath.Join(d, e.Name())
			dst := filepath.Join(archiveDir, sub, e.Name())
			if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return fmt.Errorf("sdd workspace: mkdir archive subdir: %w", err)
			}
			if err := os.Rename(src, dst); err != nil {
				return fmt.Errorf("sdd workspace: archive %s/%s: %w", sub, e.Name(), err)
			}
		}
	}
	return nil
}

// RepoPath returns the path to the pipeline branch state file
// (<dir>/state/repo.json). Used by P2's RepoState load/save.
func (w *Workspace) RepoPath() string {
	return filepath.Join(w.dir, "state", "repo.json")
}

// ProgressPath returns the path to the append-only event log
// (<dir>/progress.md). Used by P2's Progress logger.
func (w *Workspace) ProgressPath() string {
	return filepath.Join(w.dir, "progress.md")
}

// Resume reports whether a prior run's dag.json exists (resumable). It does
// NOT reset; the controller calls this to decide between Reset and continue.
func (w *Workspace) Resume() (bool, error) {
	_, err := os.Stat(filepath.Join(w.dir, "dag.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("sdd workspace: stat dag.json: %w", err)
	}
	return true, nil
}
