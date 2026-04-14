package git_test

import (
	"strings"
	"testing"

	"github.com/alecpullen/marshal/internal/git"
)

func TestSession_Start(t *testing.T) {
	repo := newTestRepo(t)
	mainSHA := headSHA(t, repo.Root())

	s := git.NewSession(repo, git.SessionOptions{})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}

	// TargetBranch set correctly.
	if s.TargetBranch != "main" {
		t.Errorf("TargetBranch: want main, got %q", s.TargetBranch)
	}
	// TargetStartSHA matches main HEAD.
	if s.TargetStartSHA != mainSHA {
		t.Errorf("TargetStartSHA: want %s, got %s", mainSHA, s.TargetStartSHA)
	}
	// Currently on staging branch.
	if !strings.HasPrefix(s.StagingBranch, "marshal/session-") {
		t.Errorf("StagingBranch: want marshal/session-*, got %q", s.StagingBranch)
	}
	assertCurrentBranch(t, repo.Root(), s.StagingBranch)
	// main is unchanged.
	if shaOfBranch(t, repo.Root(), "main") != mainSHA {
		t.Error("main branch should be unchanged after Start()")
	}
}

func TestSession_StartTwice(t *testing.T) {
	repo := newTestRepo(t)
	s := git.NewSession(repo, git.SessionOptions{})
	_ = s.Start()
	if err := s.Start(); err == nil {
		t.Error("expected error on second Start()")
	}
}

func TestSession_BeginTask(t *testing.T) {
	repo := newTestRepo(t)
	s := git.NewSession(repo, git.SessionOptions{})
	_ = s.Start()

	tx, err := s.BeginTask("abc123")
	if err != nil {
		t.Fatal(err)
	}

	if tx.ID != "abc123" {
		t.Errorf("ID: want abc123, got %q", tx.ID)
	}
	if tx.Branch != "marshal/task-abc123" {
		t.Errorf("Branch: want marshal/task-abc123, got %q", tx.Branch)
	}
	assertCurrentBranch(t, repo.Root(), "marshal/task-abc123")
	// ParentStagingSHA should match staging HEAD at the time of BeginTask.
	if tx.ParentStagingSHA != shaOfBranch(t, repo.Root(), s.StagingBranch) {
		t.Error("ParentStagingSHA mismatch")
	}
}

func TestTaskTx_CommitAndMerge(t *testing.T) {
	repo := newTestRepo(t)
	s := git.NewSession(repo, git.SessionOptions{})
	_ = s.Start()

	stagingBeforeSHA := headSHA(t, repo.Root())

	tx, _ := s.BeginTask("t1")

	// Commit something on the task branch.
	writeFile(t, repo.Root(), "feature.go", "package main\n")
	if err := tx.Commit("add feature.go"); err != nil {
		t.Fatal(err)
	}

	taskDiff, err := tx.Diff()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(taskDiff, "feature.go") {
		t.Errorf("expected feature.go in task diff:\n%s", taskDiff)
	}

	// Merge into staging.
	if err := tx.Merge("task t1: add feature.go"); err != nil {
		t.Fatal(err)
	}

	// Should be back on staging.
	assertCurrentBranch(t, repo.Root(), s.StagingBranch)
	// Task branch deleted.
	assertBranchGone(t, repo.Root(), "marshal/task-t1")
	// Staging has one new commit.
	stagingAfterSHA := headSHA(t, repo.Root())
	if stagingAfterSHA == stagingBeforeSHA {
		t.Error("staging HEAD should have advanced after task merge")
	}
	// main still unchanged.
	if shaOfBranch(t, repo.Root(), "main") != s.TargetStartSHA {
		t.Error("main branch must not be touched by task merge")
	}
}

func TestTaskTx_Abandon(t *testing.T) {
	repo := newTestRepo(t)
	s := git.NewSession(repo, git.SessionOptions{})
	_ = s.Start()
	stagingSHA := headSHA(t, repo.Root())

	tx, _ := s.BeginTask("abandoned")
	writeFile(t, repo.Root(), "discard.go", "package main\n")
	_ = tx.Commit("will be abandoned")

	if err := tx.Abandon(); err != nil {
		t.Fatal(err)
	}

	// Back on staging, staging unchanged, task branch gone.
	assertCurrentBranch(t, repo.Root(), s.StagingBranch)
	if headSHA(t, repo.Root()) != stagingSHA {
		t.Error("staging HEAD must be unchanged after Abandon")
	}
	assertBranchGone(t, repo.Root(), "marshal/task-abandoned")
}

func TestSession_Ship(t *testing.T) {
	repo := newTestRepo(t)
	mainSHABefore := headSHA(t, repo.Root())

	s := git.NewSession(repo, git.SessionOptions{})
	_ = s.Start()

	// Run a task and merge it to staging.
	tx, _ := s.BeginTask("ship1")
	writeFile(t, repo.Root(), "shipped.go", "package main\n")
	_ = tx.Commit("add shipped.go")
	_ = tx.Merge("task: add shipped.go")

	oldStaging := s.StagingBranch

	// Ship.
	newTargetSHA, err := s.Ship("ship: add shipped.go")
	if err != nil {
		t.Fatal(err)
	}

	// main has advanced by exactly 1 commit.
	mainSHAAfter := shaOfBranch(t, repo.Root(), "main")
	if mainSHAAfter == mainSHABefore {
		t.Error("main should have advanced after Ship")
	}
	if mainSHAAfter != newTargetSHA {
		t.Errorf("returned SHA %s != main SHA %s", newTargetSHA, mainSHAAfter)
	}
	assertCommitCount(t, repo.Root(), "main", 2) // initial + squash

	// Old staging branch deleted, new one created.
	assertBranchGone(t, repo.Root(), oldStaging)
	if !strings.HasPrefix(s.StagingBranch, "marshal/session-") {
		t.Errorf("new StagingBranch has unexpected name: %q", s.StagingBranch)
	}
	if s.StagingBranch == oldStaging {
		t.Error("StagingBranch should have been refreshed")
	}

	// Currently on the new staging branch.
	assertCurrentBranch(t, repo.Root(), s.StagingBranch)

	// New staging is rooted at new main HEAD.
	if shaOfBranch(t, repo.Root(), s.StagingBranch) != newTargetSHA {
		t.Error("new staging branch should start from new main HEAD")
	}
}

func TestSession_Teardown_DeletesStaging(t *testing.T) {
	repo := newTestRepo(t)
	s := git.NewSession(repo, git.SessionOptions{})
	_ = s.Start()
	staging := s.StagingBranch

	if err := s.Teardown(); err != nil {
		t.Fatal(err)
	}

	assertCurrentBranch(t, repo.Root(), "main")
	assertBranchGone(t, repo.Root(), staging)
}

func TestSession_Teardown_KeepBranch(t *testing.T) {
	repo := newTestRepo(t)
	s := git.NewSession(repo, git.SessionOptions{KeepBranch: true})
	_ = s.Start()
	staging := s.StagingBranch

	if err := s.Teardown(); err != nil {
		t.Fatal(err)
	}

	assertCurrentBranch(t, repo.Root(), "main")
	assertBranchExists(t, repo.Root(), staging)
}

func TestSession_MultipleTasksThenShip(t *testing.T) {
	repo := newTestRepo(t)
	s := git.NewSession(repo, git.SessionOptions{})
	_ = s.Start()

	// Three tasks, all pass.
	for i, name := range []string{"feat-a", "feat-b", "feat-c"} {
		tx, err := s.BeginTask(name)
		if err != nil {
			t.Fatalf("task %d BeginTask: %v", i, err)
		}
		writeFile(t, repo.Root(), name+".go", "package main\n")
		if err := tx.Commit("add " + name); err != nil {
			t.Fatalf("task %d Commit: %v", i, err)
		}
		if err := tx.Merge("task: " + name); err != nil {
			t.Fatalf("task %d Merge: %v", i, err)
		}
	}

	// Staging should have 3 squash commits on top of initial.
	assertCommitCount(t, repo.Root(), s.StagingBranch, 4) // initial+3

	_, err := s.Ship("ship three features")
	if err != nil {
		t.Fatal(err)
	}

	// main gets exactly ONE squash commit for all three tasks.
	assertCommitCount(t, repo.Root(), "main", 2)
}
