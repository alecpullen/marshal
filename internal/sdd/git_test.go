package sdd

import (
	"fmt"
	"strings"
	"testing"
)

func TestCLIGitOpsRevParse(t *testing.T) {
	// Runs against the real marshal repo this test lives in.
	ops := CLIGitOps{Dir: "../../"}
	sha, err := ops.RevParse("HEAD")
	if err != nil {
		t.Fatalf("RevParse HEAD: %v", err)
	}
	if len(sha) < 7 {
		t.Fatalf("sha too short: %q", sha)
	}
}

func TestCLIGitOpsCurrentBranch(t *testing.T) {
	ops := CLIGitOps{Dir: "../../"}
	branch, err := ops.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch == "" {
		t.Fatal("branch empty")
	}
}

func TestCLIGitOpsUnknownBranchErrors(t *testing.T) {
	ops := CLIGitOps{Dir: "../../"}
	_, err := ops.RevParse("nonexistent-branch-xyz")
	if err == nil {
		t.Fatal("expected error for unknown branch, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent-branch-xyz") && !strings.Contains(err.Error(), "unknown") && !strings.Contains(err.Error(), "fatal") {
		t.Fatalf("error should mention the branch or git failure, got %v", err)
	}
}

func TestCLIGitOpsBranchExists(t *testing.T) {
	ops := CLIGitOps{Dir: "../../"}
	// HEAD detached ref always resolves; test a clearly fake one is false.
	if ops.BranchExists("definitely-not-a-real-branch-12345") {
		t.Fatal("fake branch should not exist")
	}
}

func TestFakeGitOpsRevParse(t *testing.T) {
	fake := NewFakeGitOps()
	fake.SetRef("HEAD", "abc123")
	fake.SetRef("main", "abc123")
	fake.SetRef("sdd/T1", "abc123")

	got, err := fake.RevParse("HEAD")
	if err != nil || got != "abc123" {
		t.Fatalf("RevParse HEAD = %q, %v, want abc123", got, err)
	}
	if !fake.BranchExists("sdd/T1") {
		t.Fatal("sdd/T1 should exist")
	}
	if fake.BranchExists("sdd/T2") {
		t.Fatal("sdd/T2 should not exist")
	}
}

func TestFakeGitOpsWorktreeAddRecords(t *testing.T) {
	fake := NewFakeGitOps()
	fake.SetRef("main", "abc123")
	if err := fake.WorktreeAdd("/tmp/wt1", "sdd/T1", "main"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	calls := fake.Calls("worktree")
	if len(calls) != 1 {
		t.Fatalf("want 1 worktree call, got %d", len(calls))
	}
	// Adding the same branch again simulates git's real error.
	if err := fake.WorktreeAdd("/tmp/wt1", "sdd/T1", "main"); err == nil {
		t.Fatal("second WorktreeAdd for same branch should error")
	}
}

func TestFakeGitOpsMergeFFConflict(t *testing.T) {
	fake := NewFakeGitOps()
	fake.SetRef("main", "abc123")
	fake.SetRef("sdd/T1", "abc123")
	// Simulate a non-fast-forward.
	fake.SetMergeFFError("sdd/T1", fmt.Errorf("not a fast forward"))
	if err := fake.MergeFF("sdd/T1"); err == nil {
		t.Fatal("expected merge conflict error, got nil")
	}
}
