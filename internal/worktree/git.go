package worktree

import (
	"fmt"
	"os/exec"
	"strings"
)

// GitOps is the seam over git. Every worktree consumer (the pipeline
// controller and the agent tool) depends on this interface, never on
// exec.Command directly, so the controller can be tested without a real
// repository. Each method takes the working directory it runs in: the
// controller resolves refs against the main checkout while committing
// inside the run's worktree.
type GitOps interface {
	RevParse(dir, ref string) (string, error)
	MergeBase(dir, a, b string) (string, error)
	BranchExists(dir, branch string) bool
	WorktreeAdd(dir, path, branch, startPoint string) error
	WorktreeList(dir string) ([]string, error)
	// WorktreeRemove removes the worktree at path. git refuses when the
	// worktree has uncommitted changes — callers treat that error as
	// "kept", never retry with --force.
	WorktreeRemove(dir, path string) error
	WorktreePrune(dir string) error
	IsDirty(dir string) (bool, error)
	// CommitAll stages every change (including untracked files) and
	// commits it, returning the new HEAD SHA. It returns an error when
	// there is nothing to commit — the caller checks IsDirty first.
	CommitAll(dir, message string) (string, error)
	LogOneline(dir, rng string) (string, error)
	DiffStat(dir, rng string) (string, error)
	Diff(dir, rng string, contextLines int) (string, error)
	// Merge merges branch into the current HEAD of dir. It returns an error
	// when the merge is not clean; callers must call MergeAbort to leave the
	// repository in a usable state.
	Merge(dir, branch string) error
	// MergeAbort aborts an in-progress merge. Safe to call when no merge is
	// in progress — git exits non-zero and the error is returned, which
	// callers may ignore on that path.
	MergeAbort(dir string) error
	// BranchDelete deletes branch. force uses -D, which deletes an unmerged
	// branch; without it git refuses to delete unmerged work.
	BranchDelete(dir, branch string, force bool) error
	// DiffNumstat is DiffStat's machine-readable sibling: one
	// "added\tremoved\tpath" line per file. DiffStat's --stat output is
	// prose and internal/pipeline depends on that format, so this is a
	// separate method rather than a change to it.
	DiffNumstat(dir, rng string) (string, error)
	// DiffPath is Diff scoped to one path. Diff passes rng as a single argv
	// element, so a caller cannot append "-- path" to it.
	DiffPath(dir, rng, path string, contextLines int) (string, error)
}

// CLIGitOps shells out to the git CLI.
type CLIGitOps struct{}

func (CLIGitOps) run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("worktree git: %s: %w: %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}

func (g CLIGitOps) RevParse(dir, ref string) (string, error) {
	// ref may be a compound expression like "--abbrev-ref HEAD"; split it so
	// each token reaches git as its own argument.
	args := append([]string{"rev-parse"}, strings.Fields(ref)...)
	return g.run(dir, args...)
}

func (g CLIGitOps) MergeBase(dir, a, b string) (string, error) {
	return g.run(dir, "merge-base", a, b)
}

func (g CLIGitOps) BranchExists(dir, branch string) bool {
	_, err := g.run(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func (g CLIGitOps) WorktreeAdd(dir, path, branch, startPoint string) error {
	_, err := g.run(dir, "worktree", "add", "-b", branch, path, startPoint)
	return err
}

func (g CLIGitOps) WorktreeList(dir string) ([]string, error) {
	out, err := g.run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree "); ok {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

func (g CLIGitOps) WorktreeRemove(dir, path string) error {
	_, err := g.run(dir, "worktree", "remove", path)
	return err
}

func (g CLIGitOps) WorktreePrune(dir string) error {
	_, err := g.run(dir, "worktree", "prune")
	return err
}

func (g CLIGitOps) IsDirty(dir string) (bool, error) {
	out, err := g.run(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (g CLIGitOps) CommitAll(dir, message string) (string, error) {
	if _, err := g.run(dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := g.run(dir, "commit", "-m", message); err != nil {
		return "", err
	}
	return g.RevParse(dir, "HEAD")
}

func (g CLIGitOps) LogOneline(dir, rng string) (string, error) {
	return g.run(dir, "log", "--oneline", rng)
}

func (g CLIGitOps) DiffStat(dir, rng string) (string, error) {
	return g.run(dir, "diff", "--stat", rng)
}

func (g CLIGitOps) Diff(dir, rng string, contextLines int) (string, error) {
	return g.run(dir, "diff", fmt.Sprintf("-U%d", contextLines), rng)
}

func (g CLIGitOps) Merge(dir, branch string) error {
	// --no-ff keeps a merge commit so the agent's work is identifiable
	// afterwards; --no-edit avoids opening an editor in a headless process.
	_, err := g.run(dir, "merge", "--no-ff", "--no-edit", branch)
	return err
}

func (g CLIGitOps) MergeAbort(dir string) error {
	_, err := g.run(dir, "merge", "--abort")
	return err
}

func (g CLIGitOps) BranchDelete(dir, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := g.run(dir, "branch", flag, branch)
	return err
}

func (g CLIGitOps) DiffNumstat(dir, rng string) (string, error) {
	return g.run(dir, "diff", "--numstat", rng)
}

func (g CLIGitOps) DiffPath(dir, rng, path string, contextLines int) (string, error) {
	return g.run(dir, "diff", fmt.Sprintf("-U%d", contextLines), rng, "--", path)
}
