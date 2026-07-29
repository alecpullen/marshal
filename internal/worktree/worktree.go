package worktree

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Worktree is the isolated checkout a run works in. The user's main
// checkout is never touched.
type Worktree struct {
	Path   string
	Branch string
	Base   string
}

// EnsureWorktree returns the run's worktree, creating it on the first call
// and reusing it on resume. A branch that exists without its worktree is an
// error the human must resolve: silently reusing the branch would append a
// resumed run's commits to an unrelated one.
func EnsureWorktree(git GitOps, repoRoot, worktreesDir, branch, startRef string) (Worktree, error) {
	path := filepath.Join(worktreesDir, strings.ReplaceAll(branch, "/", "-"))
	if git.BranchExists(repoRoot, branch) {
		existing, err := git.WorktreeList(repoRoot)
		if err != nil {
			return Worktree{}, fmt.Errorf("worktree: list: %w", err)
		}
		for _, p := range existing {
			if p == path {
				base, err := git.RevParse(repoRoot, branch)
				if err != nil {
					return Worktree{}, fmt.Errorf("worktree: rev-parse %s: %w", branch, err)
				}
				return Worktree{Path: path, Branch: branch, Base: base}, nil
			}
		}
		return Worktree{}, fmt.Errorf("worktree: branch %s already exists but has no worktree at %s; delete the branch or remove it by hand before resuming", branch, path)
	}
	base, err := git.RevParse(repoRoot, startRef)
	if err != nil {
		return Worktree{}, fmt.Errorf("worktree: rev-parse %s: %w", startRef, err)
	}
	if err := git.WorktreeAdd(repoRoot, path, branch, base); err != nil {
		return Worktree{}, fmt.Errorf("worktree: add %s: %w", path, err)
	}
	return Worktree{Path: path, Branch: branch, Base: base}, nil
}

// FakeGitOps is the in-memory GitOps used by every test in this package
// that does not need a real repository. Refs maps ref names to SHAs;
// Heads maps a directory to its current HEAD; Commits records CommitAll
// calls in order.
type FakeGitOps struct {
	Refs      map[string]string
	Branches  map[string]bool
	Worktrees []string
	Added     []string
	Heads     map[string]string
	Commits   []string
	Dirty     bool
	// NextHead, when non-empty, is popped as the SHA of the next CommitAll.
	NextHead  []string
	LogOut    string
	StatOut   string
	DiffOut   string
	CommitErr error
}

func NewFakeGitOps() *FakeGitOps {
	return &FakeGitOps{
		Refs:     map[string]string{},
		Branches: map[string]bool{},
		Heads:    map[string]string{},
	}
}

func (f *FakeGitOps) RevParse(dir, ref string) (string, error) {
	if sha, ok := f.Refs[ref]; ok {
		return sha, nil
	}
	if ref == "HEAD" {
		if sha, ok := f.Heads[dir]; ok {
			return sha, nil
		}
	}
	return "", fmt.Errorf("fake git: unknown ref %q", ref)
}

func (f *FakeGitOps) MergeBase(dir, a, b string) (string, error) {
	if sha, ok := f.Refs["merge-base"]; ok {
		return sha, nil
	}
	return f.RevParse(dir, a)
}

func (f *FakeGitOps) BranchExists(dir, branch string) bool { return f.Branches[branch] }

func (f *FakeGitOps) WorktreeAdd(dir, path, branch, startPoint string) error {
	f.Added = append(f.Added, path)
	f.Branches[branch] = true
	f.Worktrees = append(f.Worktrees, path)
	f.Heads[path] = startPoint
	return nil
}

func (f *FakeGitOps) WorktreeList(dir string) ([]string, error) { return f.Worktrees, nil }

// IsDirty reports the Dirty field. Dirty is sticky: CommitAll does not
// clear it, so a scripted multi-task run keeps producing commits without
// the test resetting the flag between tasks.
func (f *FakeGitOps) IsDirty(dir string) (bool, error) { return f.Dirty, nil }

func (f *FakeGitOps) CommitAll(dir, message string) (string, error) {
	if f.CommitErr != nil {
		return "", f.CommitErr
	}
	f.Commits = append(f.Commits, message)
	head := fmt.Sprintf("commit%03d0000000000000000000000000000000", len(f.Commits))
	if len(f.NextHead) > 0 {
		head = f.NextHead[0]
		f.NextHead = f.NextHead[1:]
	}
	f.Heads[dir] = head
	return head, nil
}

func (f *FakeGitOps) LogOneline(dir, rng string) (string, error) { return f.LogOut, nil }
func (f *FakeGitOps) DiffStat(dir, rng string) (string, error)   { return f.StatOut, nil }
func (f *FakeGitOps) Diff(dir, rng string, contextLines int) (string, error) {
	return f.DiffOut, nil
}
