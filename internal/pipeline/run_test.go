package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"marshal/internal/worktree"
)

const (
	implDone = "STATUS: DONE\nTESTS: go test ./... — pass\n"
	reviewOK = "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n"
	implAsks = "STATUS: NEEDS_CONTEXT\nQUESTION: user level or system level?\n"
)

func TestRunExecutesEveryTaskThenBranchReview(t *testing.T) {
	d, prompts := scriptedDispatch(t,
		implDone, reviewOK, // task 1
		implDone, reviewOK, // task 2
		reviewOK, // branch review
	)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(*prompts) != 5 {
		t.Fatalf("dispatches = %d, want 2 per task plus the branch review", len(*prompts))
	}
	done, err := c.Ledger.CompletedTasks()
	if err != nil {
		t.Fatalf("CompletedTasks: %v", err)
	}
	if !done[1] || !done[2] {
		t.Errorf("ledger = %v, want both tasks complete", done)
	}
	if !strings.Contains((*prompts)[4], c.Plan.Path) {
		t.Errorf("branch review does not name the plan:\n%s", (*prompts)[4])
	}
	sum := c.Summary()
	for _, want := range []string{c.Worktree.Branch, c.Worktree.Path, "2 of 2"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary missing %q:\n%s", want, sum)
		}
	}
}

func TestRunResumesFromLedger(t *testing.T) {
	d, prompts := scriptedDispatch(t, implDone, reviewOK, reviewOK)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	if err := c.Ledger.MarkComplete(1, "aaaaaaaa", "bbbbbbbb"); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(*prompts) != 3 {
		t.Fatalf("dispatches = %d, want task 2 plus the branch review only", len(*prompts))
	}
	if strings.Contains((*prompts)[0], "Task 1 of") {
		t.Error("task 1 was re-dispatched despite being marked complete in the ledger")
	}
}

func TestRunOpensGateOnQuestion(t *testing.T) {
	d, _ := scriptedDispatch(t, implAsks)
	c := testController(t, d, NewFakeCommandRunner())

	err := c.Run(context.Background())
	if !errors.Is(err, ErrHumanGateRequired) {
		t.Fatalf("Run = %v, want ErrHumanGateRequired", err)
	}
	if c.Question() != "user level or system level?" {
		t.Errorf("Question() = %q", c.Question())
	}
}

func TestRunAutoEscalatesBeforeGating(t *testing.T) {
	d, prompts := scriptedDispatch(t, implAsks, implDone, reviewOK, implDone, reviewOK, reviewOK)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	c.AutoEscalate = true

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(*prompts) < 2 {
		t.Fatalf("dispatches = %d, want the escalated retry", len(*prompts))
	}
	if c.Question() != "" {
		t.Errorf("gate opened despite a successful escalation: %q", c.Question())
	}
}

func TestRunResumesAfterAnswer(t *testing.T) {
	d, prompts := scriptedDispatch(t, implAsks, implDone, reviewOK, implDone, reviewOK, reviewOK)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true

	if err := c.Run(context.Background()); !errors.Is(err, ErrHumanGateRequired) {
		t.Fatalf("first Run = %v, want the gate", err)
	}
	c.Answer("User level, at ~/.config/marshal/.")
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if !strings.Contains((*prompts)[1], "~/.config/marshal/") {
		t.Errorf("the re-dispatch did not carry the answer:\n%s", (*prompts)[1])
	}
}

func TestBranchReviewFixesOnce(t *testing.T) {
	d, prompts := scriptedDispatch(t,
		implDone, reviewOK,
		implDone, reviewOK,
		"SPEC: FAIL\nQUALITY: CHANGES_REQUESTED\nFINDINGS:\n- [Important] task 2 duplicated task 1's helper\n",
		implDone,
		reviewOK,
	)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(*prompts) != 7 {
		t.Fatalf("dispatches = %d, want a branch fix and one re-review", len(*prompts))
	}
	if !strings.Contains((*prompts)[5], "duplicated task 1's helper") {
		t.Errorf("branch fix dispatch lost the finding:\n%s", (*prompts)[5])
	}
	g := c.Git.(*worktree.FakeGitOps)
	last := g.Commits[len(g.Commits)-1]
	if !strings.Contains(last, "branch review fix") {
		t.Errorf("last commit = %q, want the branch review fix", last)
	}
}

func TestBranchReviewCarriesMinorRollup(t *testing.T) {
	d, prompts := scriptedDispatch(t,
		implDone, "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- [Minor] magic number 100\n",
		implDone, reviewOK,
		reviewOK,
	)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains((*prompts)[4], "magic number 100") {
		t.Errorf("branch review did not receive the minor roll-up:\n%s", (*prompts)[4])
	}
	if !strings.Contains(c.Summary(), "magic number 100") {
		t.Errorf("summary omits the open minor findings:\n%s", c.Summary())
	}
}
