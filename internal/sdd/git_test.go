package sdd

import (
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
