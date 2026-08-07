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
	// Removed records paths passed to WorktreeRemove; RemoveErr simulates
	// git refusing (e.g. dirty worktree). Pruned records a prune call.
	Removed   []string
	Pruned    bool
	RemoveErr error
	// Calls records every method invocation in order, so tests can assert
	// a consumer only ever reads (e.g. only Diff, never WorktreeAdd).
	calls []string
}

// Calls returns the recorded method-invocation log in order.
func (f *FakeGitOps) Calls() []string { return f.calls }

func NewFakeGitOps() *FakeGitOps {
	return &FakeGitOps{
		Refs:     map[string]string{},
		Branches: map[string]bool{},
		Heads:    map[string]string{},
	}
}

// record appends the method name to the call log.
func (f *FakeGitOps) record(name string) { f.calls = append(f.calls, name) }

func (f *FakeGitOps) RevParse(dir, ref string) (string, error) {
	f.record("RevParse")
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
	f.record("MergeBase")
	if sha, ok := f.Refs["merge-base"]; ok {
		return sha, nil
	}
	return f.RevParse(dir, a)
}

func (f *FakeGitOps) BranchExists(dir, branch string) bool {
	f.record("BranchExists")
	return f.Branches[branch]
}

func (f *FakeGitOps) WorktreeAdd(dir, path, branch, startPoint string) error {
	f.record("WorktreeAdd")
	f.Added = append(f.Added, path)
	f.Branches[branch] = true
	f.Worktrees = append(f.Worktrees, path)
	f.Heads[path] = startPoint
	return nil
}

func (f *FakeGitOps) WorktreeList(dir string) ([]string, error) {
	f.record("WorktreeList")
	return f.Worktrees, nil
}

func (f *FakeGitOps) WorktreeRemove(dir, path string) error {
	f.record("WorktreeRemove")
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	f.Removed = append(f.Removed, path)
	return nil
}

func (f *FakeGitOps) WorktreePrune(dir string) error {
	f.record("WorktreePrune")
	f.Pruned = true
	return nil
}

// IsDirty reports the Dirty field. Dirty is sticky: CommitAll does not
// clear it, so a scripted multi-task run keeps producing commits without
// the test resetting the flag between tasks.
func (f *FakeGitOps) IsDirty(dir string) (bool, error) { f.record("IsDirty"); return f.Dirty, nil }

func (f *FakeGitOps) CommitAll(dir, message string) (string, error) {
	f.record("CommitAll")
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

func (f *FakeGitOps) LogOneline(dir, rng string) (string, error) {
	f.record("LogOneline")
	return f.LogOut, nil
}
func (f *FakeGitOps) DiffStat(dir, rng string) (string, error) {
	f.record("DiffStat")
	return f.StatOut, nil
}
func (f *FakeGitOps) Diff(dir, rng string, contextLines int) (string, error) {
	f.record("Diff")
	return f.DiffOut, nil
}
