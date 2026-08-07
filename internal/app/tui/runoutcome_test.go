package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/worktree"
)

func succeededRun() session.SDDProgress {
	return session.SDDProgress{
		Finished: true, Succeeded: true,
		PlanName: "add-retries", Branch: "pipeline/add-retries", BaseRef: "main",
		LedgerPath:   "/repo/.marshal/pipeline/add-retries/ledger.md",
		ArtifactsDir: "/repo/.marshal/pipeline/add-retries",
		TotalTasks:   4, DoneTasks: 4,
		StartedAt: time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 8, 7, 10, 23, 41, 0, time.UTC),
	}
}

func failedRun() session.SDDProgress {
	p := succeededRun()
	p.Succeeded = false
	p.DoneTasks = 2
	p.Error = "task 3 still fails `go test ./...` after 3 fix rounds"
	return p
}

func rowTexts(doc commands.Doc) []string {
	var out []string
	var walk func(rows []commands.Row)
	walk = func(rows []commands.Row) {
		for _, r := range rows {
			out = append(out, r.Header+r.Text+r.Detail+r.ActionLabel)
			walk(r.Children)
		}
	}
	walk(doc.Rows)
	if doc.Footer != "" {
		out = append(out, doc.Footer)
	}
	return out
}

func TestOutcomeDocSuccessOffersTheBranchDiff(t *testing.T) {
	doc := runOutcomeDoc(succeededRun(), worktree.NewFakeGitOps(), "/repo", 100)
	joined := strings.Join(rowTexts(doc), "\n")

	for _, want := range []string{"pipeline/add-retries", "4/4", "diff", "ledger", "artifacts"} {
		if !strings.Contains(strings.ToLower(joined), strings.ToLower(want)) {
			t.Errorf("outcome doc missing %q — the user has no route to their work:\n%s", want, joined)
		}
	}
}

func TestOutcomeDocFailureKeepsTheReason(t *testing.T) {
	doc := runOutcomeDoc(failedRun(), worktree.NewFakeGitOps(), "/repo", 100)
	joined := strings.Join(rowTexts(doc), "\n")

	if !strings.Contains(joined, "go test ./...") {
		t.Errorf("a failed run must keep its reason reachable, not truncate it to 80 chars:\n%s", joined)
	}
	if !strings.Contains(strings.ToLower(joined), "2/4") {
		t.Errorf("a failed run must show how far it got:\n%s", joined)
	}
}

func TestOutcomeDocFailureOffersResume(t *testing.T) {
	doc := runOutcomeDoc(failedRun(), worktree.NewFakeGitOps(), "/repo", 100)
	joined := strings.Join(rowTexts(doc), "\n")
	if !strings.Contains(joined, sddResumeHint) {
		t.Errorf("a failed run must say how to resume, in the shared wording:\n%s", joined)
	}
}

func TestOutcomeDocSuccessDoesNotOfferResume(t *testing.T) {
	doc := runOutcomeDoc(succeededRun(), worktree.NewFakeGitOps(), "/repo", 100)
	if strings.Contains(strings.Join(rowTexts(doc), "\n"), sddResumeHint) {
		t.Error("a completed run has nothing to resume")
	}
}

func TestOutcomeDocHandlesMissingBranchGracefully(t *testing.T) {
	p := succeededRun()
	p.Branch = ""
	doc := runOutcomeDoc(p, worktree.NewFakeGitOps(), "/repo", 100)
	if len(doc.Rows) == 0 {
		t.Error("a run with no branch must still render an outcome, not an empty panel")
	}
}

func TestOutcomeDocWithNoRunAtAll(t *testing.T) {
	doc := runOutcomeDoc(session.SDDProgress{}, worktree.NewFakeGitOps(), "/repo", 100)
	joined := strings.Join(rowTexts(doc), "\n")
	if !strings.Contains(strings.ToLower(joined), "no plan run") {
		t.Errorf("with no run recorded the doc must say so plainly:\n%s", joined)
	}
}

func TestBranchDiffReadsWithoutMutating(t *testing.T) {
	git := worktree.NewFakeGitOps()
	// A fake that records every call lets us assert we only ever read.
	_ = branchDiff(git, "/repo", "main", "pipeline/add-retries", 100)

	for _, call := range git.Calls() {
		switch call {
		case "WorktreeAdd", "WorktreeRemove", "CommitAll", "WorktreePrune":
			t.Errorf("branchDiff performed a mutating git operation: %s", call)
		}
	}
}
