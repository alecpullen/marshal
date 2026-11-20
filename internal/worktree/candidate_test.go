package worktree

import (
	"context"
	"strings"
	"testing"
)

// candidateRepo returns a repository with a candidate branch one commit ahead of
// the base, plus the exact ids the merge assertions need.
func candidateRepo(t *testing.T) (repo, base, candidate string) {
	t.Helper()
	repo = initRealRepo(t)
	base = strings.TrimSpace(realgit(t, repo, "rev-parse", "HEAD"))
	realgit(t, repo, "checkout", "-b", "candidate")
	realWriteFile(t, repo, "candidate.txt", "candidate\n")
	realGitCommitAll(t, repo, "candidate work")
	// The candidate also rewrites a.txt, so a target that edits a.txt
	// conflicts while a target that leaves it alone merges cleanly.
	realWriteFile(t, repo, "a.txt", "candidate side\n")
	realGitCommitAll(t, repo, "candidate edits a.txt")
	candidate = strings.TrimSpace(realgit(t, repo, "rev-parse", "HEAD"))
	realgit(t, repo, "checkout", "main")
	return repo, base, candidate
}

// TestMergeExactMergesTheExactCommitAndKeepsItsHistory pins the merge primitive:
// it merges the commit object it was given (never a branch that may have moved),
// keeps a real merge commit whose parents record both sides, and writes the
// operation trailer the journal recovers by.
func TestMergeExactMergesTheExactCommitAndKeepsItsHistory(t *testing.T) {
	repo, base, candidate := candidateRepo(t)
	git := CLIGitOps{}
	ctx := context.Background()

	if state, err := git.MergeState(repo); err != nil || state != "" {
		t.Fatalf("MergeState before the merge = %q, %v, want idle", state, err)
	}

	const operation = "op-1"
	message := "Integrate subagent candidate\n\nMarshal-Subagent-Operation: " + operation + "\n"
	result, err := git.MergeExact(ctx, repo, candidate, message)
	if err != nil {
		t.Fatalf("MergeExact: %v", err)
	}
	if result == base || result == candidate {
		t.Fatalf("merge result = %s, want a new merge commit", result)
	}

	parents, err := git.Parents(repo, result)
	if err != nil {
		t.Fatalf("Parents: %v", err)
	}
	if len(parents) != 2 || parents[0] != base || parents[1] != candidate {
		t.Fatalf("merge parents = %v, want [%s %s]", parents, base, candidate)
	}

	body := realgit(t, repo, "log", "-1", "--format=%B", result)
	if !strings.Contains(body, "Marshal-Subagent-Operation: "+operation) {
		t.Fatalf("merge message %q does not carry the operation trailer", body)
	}
	// The candidate's own history survives the merge, unrewritten.
	if tip := strings.TrimSpace(realgit(t, repo, "rev-parse", "candidate")); tip != candidate {
		t.Fatalf("candidate branch tip = %s, want %s", tip, candidate)
	}
	if head := strings.TrimSpace(realgit(t, repo, "rev-parse", "HEAD")); head != result {
		t.Fatalf("target HEAD = %s, want the merge commit %s", head, result)
	}
	if state, err := git.MergeState(repo); err != nil || state != "" {
		t.Fatalf("MergeState after a clean merge = %q, %v, want idle", state, err)
	}

	// Only exact commit object ids reach git: a branch name would let the
	// merged work change between review and mutation.
	if _, err := git.MergeExact(ctx, repo, "candidate", message); err == nil {
		t.Fatal("MergeExact accepted a branch name instead of an exact commit")
	}
	if _, err := git.Parents(repo, "HEAD"); err == nil {
		t.Fatal("Parents accepted a ref name instead of an exact commit")
	}
	if _, err := git.MergeExact(ctx, repo, candidate, "second"); err == nil {
		// The candidate is already an ancestor after the first merge, so git
		// reports nothing to do rather than merging twice.
		if state, _ := git.MergeState(repo); state != "" {
			t.Fatalf("a repeated merge left state behind: %q", state)
		}
	}
}

// TestMergeStateReportsOnlyTheInProgressMerge pins the recovery evidence: an
// idle checkout reports nothing, a conflicted merge reports the exact commit
// being merged and the unmerged paths git itself lists, and aborting clears it.
func TestMergeStateReportsOnlyTheInProgressMerge(t *testing.T) {
	repo, base, candidate := candidateRepo(t)
	git := CLIGitOps{}

	realWriteFile(t, repo, "a.txt", "target side\n")
	realGitCommitAll(t, repo, "target edit")
	beforeMerge := strings.TrimSpace(realgit(t, repo, "rev-parse", "HEAD"))

	if _, err := git.MergeExact(context.Background(), repo, candidate, "conflict"); err == nil {
		t.Fatal("a conflicting merge reported success")
	}
	state, err := git.MergeState(repo)
	if err != nil {
		t.Fatalf("MergeState: %v", err)
	}
	info := ParseMergeState(state)
	if len(info.Heads) != 1 || info.Heads[0] != candidate {
		t.Fatalf("merge heads = %v, want [%s]", info.Heads, candidate)
	}
	if len(info.Conflicts) != 1 || info.Conflicts[0] != "a.txt" {
		t.Fatalf("conflict paths = %v, want [a.txt]", info.Conflicts)
	}
	if head := strings.TrimSpace(realgit(t, repo, "rev-parse", "HEAD")); head != beforeMerge {
		t.Fatalf("HEAD moved to %s during a conflicted merge, want %s", head, beforeMerge)
	}
	if err := git.MergeAbort(repo); err != nil {
		t.Fatalf("MergeAbort: %v", err)
	}
	if state, err := git.MergeState(repo); err != nil || state != "" {
		t.Fatalf("MergeState after abort = %q, %v, want idle", state, err)
	}
	if head := strings.TrimSpace(realgit(t, repo, "rev-parse", "HEAD")); head != beforeMerge {
		t.Fatalf("HEAD after abort = %s, want %s", head, beforeMerge)
	}
	if base == beforeMerge {
		t.Fatal("fixture: the target must have moved past the base")
	}

	t.Run("conflicting paths and linked worktrees", func(t *testing.T) {
		repo := initRealRepo(t)
		git := CLIGitOps{}
		base := strings.TrimSpace(realgit(t, repo, "rev-parse", "HEAD"))

		wt, err := EnsureWorktree(git, repo, t.TempDir(), "feat/x", "HEAD", WorktreeSetup{})
		if err != nil {
			t.Fatalf("EnsureWorktree: %v", err)
		}
		realWriteFile(t, wt.Path, "a.txt", "child side\n")
		realGitCommitAll(t, wt.Path, "child edit")
		child := strings.TrimSpace(realgit(t, wt.Path, "rev-parse", "HEAD"))

		realWriteFile(t, repo, "a.txt", "parent side\n")
		realGitCommitAll(t, repo, "parent edit")
		parent := strings.TrimSpace(realgit(t, repo, "rev-parse", "HEAD"))
		if parent == base || child == base {
			t.Fatal("fixture: both sides must move past the base")
		}

		if _, err := git.MergeExact(context.Background(), wt.Path, parent, "conflict"); err == nil {
			t.Fatal("a conflicting merge in a linked worktree reported success")
		}
		state, err := git.MergeState(wt.Path)
		if err != nil {
			t.Fatalf("MergeState in a linked worktree: %v", err)
		}
		info := ParseMergeState(state)
		if len(info.Heads) != 1 || info.Heads[0] != parent {
			t.Fatalf("merge heads = %v, want [%s]", info.Heads, parent)
		}
		if len(info.Conflicts) != 1 || info.Conflicts[0] != "a.txt" {
			t.Fatalf("conflict paths = %v, want [a.txt]", info.Conflicts)
		}
		// The main checkout is untouched by the worktree's merge.
		if state, err := git.MergeState(repo); err != nil || state != "" {
			t.Fatalf("main checkout merge state = %q, %v, want idle", state, err)
		}
		if err := git.MergeAbort(wt.Path); err != nil {
			t.Fatalf("MergeAbort: %v", err)
		}
		if got := strings.TrimSpace(realgit(t, wt.Path, "rev-parse", "HEAD")); got != child {
			t.Fatalf("worktree HEAD after abort = %s, want %s", got, child)
		}
	})
}
