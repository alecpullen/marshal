package worktree

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
	// WorktreeBranch returns the branch a worktree at path is attached to,
	// or an error when path is not a registered worktree of dir. Used to
	// verify ownership before destructive operations.
	WorktreeBranch(dir, path string) (string, error)
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
	// SquashMerge squashes branch into the current HEAD of dir without
	// committing (git merge --squash): the index and working tree carry the
	// merged result while HEAD stays put. Callers commit separately. On
	// failure callers must call ResetMerge — NOT MergeAbort: --squash never
	// writes MERGE_HEAD, so git merge --abort exits 128 and leaves the
	// conflicted index and .git/SQUASH_MSG behind.
	SquashMerge(dir, branch string) error
	// ResetMerge undoes a merge attempt (git reset --merge): it resets the
	// index and restores conflicted working-tree files, and — unlike
	// merge --abort — also clears the staged state and SQUASH_MSG a failed
	// --squash leaves behind. Only safe on a checkout known to be clean
	// before the merge (it discards working-tree changes).
	ResetMerge(dir string) error
	// AheadBehind reports how many commits branch is ahead of and behind
	// base (git rev-list --left-right --count base...branch).
	AheadBehind(dir, base, branch string) (ahead, behind int, err error)
	// BranchAge reports branch's tip commit time.
	BranchAge(dir, branch string) (time.Time, error)
	// ListWorktrees lists every worktree as a (path, branch) pair (git
	// worktree list --porcelain). Branch is "" for detached and bare
	// worktrees. WorktreeList remains for consumers that only need paths.
	ListWorktrees(dir string) ([]WorktreeInfo, error)
	// CheckIgnore reports whether path is git-ignored in dir (git
	// check-ignore). Tracked paths report as not ignored — check-ignore
	// consults the index — which is exactly what the seeder needs to know.
	CheckIgnore(dir, path string) (bool, error)
}

// WorktreeInfo is one entry of `git worktree list --porcelain`.
type WorktreeInfo struct {
	Path   string
	Branch string // "" for detached and bare worktrees
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

// WorktreeBranch parses `git worktree list --porcelain` to find the branch
// attached to the worktree at path. A detached worktree reports "HEAD" and
// is treated as not owned by any agent branch. The path is normalized on
// both sides so a caller passing a path with a trailing slash, redundant
// segments, or a symlinked prefix (e.g. /var vs /private/var on macOS) still
// matches git's canonical listing.
func (g CLIGitOps) WorktreeBranch(dir, path string) (string, error) {
	out, err := g.run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	want := CanonicalPath(path)
	var curPath string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			curPath = CanonicalPath(p)
			continue
		}
		if b, ok := strings.CutPrefix(line, "branch "); ok && curPath == want {
			return strings.TrimPrefix(b, "refs/heads/"), nil
		}
	}
	return "", fmt.Errorf("worktree git: %s is not a registered worktree of %s", path, dir)
}

// CanonicalPath resolves symlinks (so /var and /private/var compare equal)
// and cleans redundant segments. EvalSymlinks fails when the path does not
// exist; in that case we fall back to a plain Clean so the caller still gets
// a meaningful "not a registered worktree" error rather than a spurious one.
func CanonicalPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
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

func (g CLIGitOps) ResetMerge(dir string) error {
	_, err := g.run(dir, "reset", "--merge")
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

// SquashMerge stages the merged result without committing. No editor can
// appear: --squash never creates a merge commit.
func (g CLIGitOps) SquashMerge(dir, branch string) error {
	_, err := g.run(dir, "merge", "--squash", branch)
	return err
}

// AheadBehind parses `git rev-list --left-right --count base...branch`.
// The left count is what only base has (how far branch is behind), the
// right count what only branch has (how far ahead it is).
func (g CLIGitOps) AheadBehind(dir, base, branch string) (int, int, error) {
	out, err := g.run(dir, "rev-list", "--left-right", "--count", base+"..."+branch)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("worktree git: rev-list --count: unexpected output %q", out)
	}
	behind, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("worktree git: rev-list --count: unexpected output %q", out)
	}
	ahead, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("worktree git: rev-list --count: unexpected output %q", out)
	}
	return ahead, behind, nil
}

func (g CLIGitOps) BranchAge(dir, branch string) (time.Time, error) {
	out, err := g.run(dir, "log", "-1", "--format=%ct", branch)
	if err != nil {
		return time.Time{}, err
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("worktree git: log --format=%%ct: unexpected output %q", out)
	}
	return time.Unix(sec, 0), nil
}

// ListWorktrees parses porcelain blocks: each block starts with a
// "worktree <path>" line and may carry "branch refs/heads/<name>"; detached
// and bare worktrees have no branch line and report Branch "".
func (g CLIGitOps) ListWorktrees(dir string) ([]WorktreeInfo, error) {
	out, err := g.run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var infos []WorktreeInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			infos = append(infos, WorktreeInfo{Path: strings.TrimPrefix(line, "worktree ")})
		case strings.HasPrefix(line, "branch refs/heads/"):
			if len(infos) > 0 {
				infos[len(infos)-1].Branch = strings.TrimPrefix(line, "branch refs/heads/")
			}
		}
	}
	return infos, nil
}

// CheckIgnore reads the answer from git's exit code — 0 ignored, 1 not
// ignored (tracked paths included), anything else a real failure — so it
// runs the command directly instead of through run, which collapses all
// non-zero exits into one error.
func (g CLIGitOps) CheckIgnore(dir, path string) (bool, error) {
	cmd := exec.Command("git", "-C", dir, "check-ignore", path)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("worktree git: check-ignore %s: %w: %s", path, err, strings.TrimSpace(string(out)))
}
