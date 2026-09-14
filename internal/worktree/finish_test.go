package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var finishWT = Worktree{Path: "/repo/.marshal/worktrees/feat-x", Branch: "feat/x", Base: "sha-base"}

// newFinishFake returns a fake with a clean project at HEAD=sha-target and
// a clean worktree — the happy path each test then perturbs.
func newFinishFake() *FakeGitOps {
	g := NewFakeGitOps()
	g.Heads["/repo"] = "sha-target"
	g.Worktrees = append(g.Worktrees, finishWT.Path)
	g.WorktreeBranches[finishWT.Path] = finishWT.Branch
	return g
}

func TestFinishRefusesDirtyWorktreeWithoutMessage(t *testing.T) {
	g := newFinishFake()
	g.DirtyDirs[finishWT.Path] = true

	res, err := FinishBranch(g, "/repo", "sha-target", finishWT, FinishOptions{})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if res.Merged || res.Reason != ReasonDirtyWorktree {
		t.Fatalf("res = %+v, want refused with %s", res, ReasonDirtyWorktree)
	}
	if len(g.Merges)+len(g.SquashMerges) != 0 {
		t.Fatalf("merge attempted despite the guard: %v %v", g.Merges, g.SquashMerges)
	}
	if len(g.Commits) != 0 {
		t.Fatalf("commits = %v, want none", g.Commits)
	}
}

func TestFinishCommitsDirtyWorktreeWhenGivenAMessage(t *testing.T) {
	g := newFinishFake()
	g.DirtyDirs[finishWT.Path] = true
	g.NextHead = []string{"sha-wtcommit"}

	res, err := FinishBranch(g, "/repo", "sha-target", finishWT, FinishOptions{CommitMessage: "wip"})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if len(g.Commits) != 1 || g.Commits[0] != "wip" {
		t.Fatalf("Commits = %v, want one worktree commit", g.Commits)
	}
	if !res.Merged {
		t.Fatalf("res = %+v, want merged", res)
	}
}

func TestFinishRefusesDirtyProject(t *testing.T) {
	g := newFinishFake()
	g.DirtyDirs["/repo"] = true

	res, err := FinishBranch(g, "/repo", "sha-target", finishWT, FinishOptions{})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if res.Merged || res.Reason != ReasonDirtyProject {
		t.Fatalf("res = %+v, want refused with %s", res, ReasonDirtyProject)
	}
	if len(g.Merges)+len(g.SquashMerges) != 0 {
		t.Fatalf("merge attempted despite the guard: %v %v", g.Merges, g.SquashMerges)
	}
}

func TestFinishRefusesMovedTarget(t *testing.T) {
	g := newFinishFake()
	g.Heads["/repo"] = "sha-moved"

	res, err := FinishBranch(g, "/repo", "sha-target", finishWT, FinishOptions{})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if res.Merged || res.Reason != ReasonTargetMoved {
		t.Fatalf("res = %+v, want refused with %s", res, ReasonTargetMoved)
	}
}

func TestFinishSquashSuccess(t *testing.T) {
	g := newFinishFake()
	g.NextHead = []string{"sha-squash"}

	res, err := FinishBranch(g, "/repo", "sha-target", finishWT, FinishOptions{Squash: true, CommitMessage: "squash msg", DeleteBranch: true})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if !res.Merged || !res.SquashCommit {
		t.Fatalf("res = %+v, want a merged squash", res)
	}
	if len(g.SquashMerges) != 1 || g.SquashMerges[0] != finishWT.Branch {
		t.Fatalf("SquashMerges = %v", g.SquashMerges)
	}
	if len(g.Merges) != 0 {
		t.Fatalf("Merge called on the squash path: %v", g.Merges)
	}
	// One commit: the squash commit on the project. The worktree was clean.
	if len(g.Commits) != 1 || g.Commits[0] != "squash msg" {
		t.Fatalf("Commits = %v, want the squash message", g.Commits)
	}
	if res.Commit != "sha-squash" {
		t.Fatalf("Commit = %q, want the new HEAD", res.Commit)
	}
	if got := g.Removed; len(got) != 1 || got[0] != finishWT.Path {
		t.Fatalf("Removed = %v, want the worktree removed", got)
	}
	if len(g.Deleted) != 1 || g.Deleted[0] != finishWT.Branch || !g.DeletedForce[0] {
		t.Fatalf("Deleted = %v force=%v, want forced branch delete", g.Deleted, g.DeletedForce)
	}
}

func TestFinishSquashDefaultMessage(t *testing.T) {
	g := newFinishFake()

	res, err := FinishBranch(g, "/repo", "sha-target", finishWT, FinishOptions{Squash: true})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if len(g.Commits) != 1 || g.Commits[0] != "squash: "+finishWT.Branch {
		t.Fatalf("Commits = %v, want the default squash message", g.Commits)
	}
	if !res.Merged || !res.SquashCommit {
		t.Fatalf("res = %+v, want a merged squash", res)
	}
}

func TestFinishNoFFSuccess(t *testing.T) {
	g := newFinishFake()
	// A real merge moves HEAD; the fake only does so via MergeFunc (or the
	// NextHead queue on a CommitAll, which the no-ff path never calls).
	g.MergeFunc = func(dir, branch string) error {
		g.Heads[dir] = "sha-merge"
		return nil
	}

	res, err := FinishBranch(g, "/repo", "sha-target", finishWT, FinishOptions{DeleteBranch: true})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if !res.Merged || res.SquashCommit {
		t.Fatalf("res = %+v, want a merged no-ff finish", res)
	}
	if len(g.Merges) != 1 || g.Merges[0] != finishWT.Branch {
		t.Fatalf("Merges = %v", g.Merges)
	}
	if len(g.SquashMerges) != 0 {
		t.Fatalf("SquashMerge called on the no-ff path: %v", g.SquashMerges)
	}
	if len(g.Commits) != 0 {
		t.Fatalf("Commits = %v, want none on the no-ff path", g.Commits)
	}
	if res.Commit != "sha-merge" {
		t.Fatalf("Commit = %q, want the merge HEAD", res.Commit)
	}
	if len(g.Deleted) != 1 || g.DeletedForce[0] {
		t.Fatalf("Deleted = %v force=%v, want a plain -d delete", g.Deleted, g.DeletedForce)
	}
}

func TestFinishMergeConflictAbortsAndReportsFiles(t *testing.T) {
	g := newFinishFake()
	g.MergeErr = errors.New("CONFLICT (content): Merge conflict in a.go\nCONFLICT (content): Merge conflict in b.go\n")

	res, err := FinishBranch(g, "/repo", "sha-target", finishWT, FinishOptions{})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if res.Merged || res.Reason != ReasonConflicts {
		t.Fatalf("res = %+v, want refused with %s", res, ReasonConflicts)
	}
	if g.Aborts != 1 {
		t.Fatalf("Aborts = %d, want the merge aborted", g.Aborts)
	}
	want := []string{"a.go", "b.go"}
	if len(res.Conflicted) != len(want) {
		t.Fatalf("Conflicted = %v, want %v", res.Conflicted, want)
	}
	for i := range want {
		if res.Conflicted[i] != want[i] {
			t.Errorf("Conflicted[%d] = %q, want %q", i, res.Conflicted[i], want[i])
		}
	}
	// A refused merge cleans nothing up.
	if len(g.Removed) != 0 || len(g.Deleted) != 0 {
		t.Fatalf("cleanup ran on a refusal: Removed=%v Deleted=%v", g.Removed, g.Deleted)
	}
}

func TestFinishSquashConflictAborts(t *testing.T) {
	g := newFinishFake()
	g.SquashMergeErr = errors.New("CONFLICT (content): Merge conflict in a.go")

	res, err := FinishBranch(g, "/repo", "sha-target", finishWT, FinishOptions{Squash: true})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if res.Merged || res.Reason != ReasonConflicts || len(res.Conflicted) != 1 {
		t.Fatalf("res = %+v, want conflicts with one file", res)
	}
	if g.Aborts != 1 {
		t.Fatalf("Aborts = %d, want the merge aborted", g.Aborts)
	}
}

func TestFinishDeleteBranchFalseRespected(t *testing.T) {
	g := newFinishFake()

	if _, err := FinishBranch(g, "/repo", "sha-target", finishWT, FinishOptions{}); err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if len(g.Deleted) != 0 {
		t.Fatalf("Deleted = %v, want the branch kept", g.Deleted)
	}
	if len(g.Removed) != 1 {
		t.Fatalf("Removed = %v, want the worktree removed", g.Removed)
	}
}

func TestFinishCommitErrorPropagates(t *testing.T) {
	g := newFinishFake()
	g.DirtyDirs[finishWT.Path] = true
	g.CommitErr = errors.New("no identity")

	if _, err := FinishBranch(g, "/repo", "sha-target", finishWT, FinishOptions{CommitMessage: "wip"}); err == nil {
		t.Fatal("CommitAll failure: want an error, got nil")
	}
	if len(g.Merges)+len(g.SquashMerges) != 0 {
		t.Fatalf("merge attempted after a failed commit: %v %v", g.Merges, g.SquashMerges)
	}
}

func TestOwnedByAgentDir(t *testing.T) {
	repo := t.TempDir()
	inside := filepath.Join(repo, ".marshal", "worktrees", "feat-x")
	if !OwnedByAgentDir(repo, inside) {
		t.Errorf("OwnedByAgentDir(%s) = false, want true", inside)
	}
	if OwnedByAgentDir(repo, filepath.Join(repo, "somewhere-else")) {
		t.Error("a sibling of the agent dir must not be owned")
	}
	if OwnedByAgentDir(repo, repo) {
		t.Error("the project root itself must not be owned")
	}
	if OwnedByAgentDir(repo, "/elsewhere/wt") {
		t.Error("an unrelated absolute path must not be owned")
	}
}

func TestOwnedByAgentDirRejectsSymlinkEscape(t *testing.T) {
	repo := t.TempDir()
	agentDir := filepath.Join(repo, ".marshal", "worktrees")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	escape := t.TempDir()
	// The path must exist: CanonicalPath's EvalSymlinks only resolves
	// symlinked parents for paths that are really there, and the fallback
	// for missing paths is purely lexical.
	wt := filepath.Join(escape, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(agentDir, "link")
	if err := os.Symlink(escape, link); err != nil {
		t.Fatal(err)
	}
	if OwnedByAgentDir(repo, filepath.Join(link, "wt")) {
		t.Error("a symlink escaping the agent dir must not be owned")
	}
}
