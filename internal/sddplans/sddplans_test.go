package sddplans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/pipeline"
)

func writePlan(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const twoTaskPlan = `# Plan

## Global Constraints

- go test ./... must pass

## Task 1: Scaffold the config

Do the thing.

## Task 2: Wire the resolver

Do the other thing.
`

func TestDiscoverReportsTaskCounts(t *testing.T) {
	root := t.TempDir()
	writePlan(t, filepath.Join(root, ".marshal/plans"), "add-retries.md", twoTaskPlan)

	got := Discover(root, ".marshal/plans")
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	c := got[0]
	if c.Err != nil {
		t.Fatalf("unexpected parse error: %v", c.Err)
	}
	if c.Tasks != 2 {
		t.Errorf("Tasks = %d, want 2", c.Tasks)
	}
	if c.Name != "add-retries.md" {
		t.Errorf("Name = %q, want add-retries.md", c.Name)
	}
	if c.Slug != "add-retries" {
		t.Errorf("Slug = %q, want add-retries", c.Slug)
	}
}

func TestDiscoverSurfacesParseErrorsInsteadOfHiding(t *testing.T) {
	root := t.TempDir()
	writePlan(t, filepath.Join(root, ".marshal/plans"), "broken.md", "# Just a heading\n\nNo tasks here.\n")

	got := Discover(root, ".marshal/plans")
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1 — a malformed plan must be listed, not hidden", len(got))
	}
	if got[0].Err == nil {
		t.Error("a plan with no task sections must carry a parse error")
	}
	if got[0].Tasks != 0 {
		t.Errorf("Tasks = %d, want 0 for an unparseable plan", got[0].Tasks)
	}
}

func TestDiscoverMissingDirectoryIsEmptyNotAnError(t *testing.T) {
	if got := Discover(t.TempDir(), ".marshal/plans"); len(got) != 0 {
		t.Errorf("got %d candidates from a missing plans dir, want 0", len(got))
	}
}

func TestDiscoverSortsByName(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".marshal/plans")
	writePlan(t, dir, "zebra.md", twoTaskPlan)
	writePlan(t, dir, "alpha.md", twoTaskPlan)

	got := Discover(root, ".marshal/plans")
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	if got[0].Name != "alpha.md" {
		t.Errorf("first candidate = %q, want alpha.md (sorted)", got[0].Name)
	}
}

func TestDiscoverReadsLedgerProgress(t *testing.T) {
	root := t.TempDir()
	writePlan(t, filepath.Join(root, ".marshal/plans"), "add-retries.md", twoTaskPlan)

	paths, err := pipeline.NewPaths(root, "add-retries")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	if err := (pipeline.Ledger{Path: paths.Ledger()}).MarkComplete(1, "abc1234def", "9876543fed"); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}

	got := Discover(root, ".marshal/plans")
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	c := got[0]
	if c.Err != nil {
		t.Fatalf("unexpected parse error: %v", c.Err)
	}
	if c.Done != 1 {
		t.Errorf("Done = %d, want 1 (task 1 marked complete in ledger)", c.Done)
	}
	if !c.Resumable() {
		t.Error("a plan with 1 of 2 tasks done must be resumable")
	}
}

func TestResumableRequiresPartialProgress(t *testing.T) {
	if (Candidate{Tasks: 4, Done: 0}).Resumable() {
		t.Error("a run that never started is not resumable")
	}
	if (Candidate{Tasks: 4, Done: 4}).Resumable() {
		t.Error("a finished run is not resumable")
	}
	if !(Candidate{Tasks: 4, Done: 2}).Resumable() {
		t.Error("a partially complete run is resumable")
	}
	if (Candidate{Tasks: 0, Done: 0, Err: os.ErrNotExist}).Resumable() {
		t.Error("an unparseable plan is never resumable")
	}
	if (Candidate{Tasks: 4, Done: 2, LedgerErr: os.ErrPermission}).Resumable() {
		t.Error("a plan with an unreadable ledger is never resumable")
	}
}

func TestDiscoverMissingLedgerIsFreshRun(t *testing.T) {
	root := t.TempDir()
	writePlan(t, filepath.Join(root, ".marshal/plans"), "add-retries.md", twoTaskPlan)

	got := Discover(root, ".marshal/plans")
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	c := got[0]
	if c.Err != nil {
		t.Fatalf("unexpected parse error: %v", c.Err)
	}
	if c.LedgerErr != nil {
		t.Fatalf("a missing ledger must not be an error, got %v", c.LedgerErr)
	}
	if c.Done != 0 {
		t.Errorf("Done = %d, want 0 for a fresh run", c.Done)
	}
	if c.Resumable() {
		t.Error("a fresh run must not be resumable")
	}
}

func TestDiscoverSurfacesUnreadableLedger(t *testing.T) {
	root := t.TempDir()
	writePlan(t, filepath.Join(root, ".marshal/plans"), "add-retries.md", twoTaskPlan)

	paths, err := pipeline.NewPaths(root, "add-retries")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	ledgerPath := paths.Ledger()
	if err := os.WriteFile(ledgerPath, []byte("Task 1: complete (commits aaaaaaa..bbbbbbb, review clean)\n"), 0o644); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
	if err := os.Chmod(ledgerPath, 0o000); err != nil {
		t.Fatalf("chmod ledger: %v", err)
	}
	t.Cleanup(func() { os.Chmod(ledgerPath, 0o644) })

	got := Discover(root, ".marshal/plans")
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	c := got[0]
	if c.Err != nil {
		t.Fatalf("unexpected parse error: %v", c.Err)
	}
	if c.LedgerErr == nil {
		t.Fatal("a present-but-unreadable ledger must surface an error, not a silent 0 done")
	}
	if c.Done != 0 {
		t.Errorf("Done = %d, want 0 when the ledger cannot be read", c.Done)
	}
	if c.Resumable() {
		t.Error("a plan with an unreadable ledger must not be resumable")
	}
}

func TestDiscoverRecognizesNonTerminalRun(t *testing.T) {
	root := t.TempDir()
	writePlan(t, filepath.Join(root, ".marshal/plans"), "my-plan.md", twoTaskPlan)
	paths, err := pipeline.NewPaths(root, "my-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := pipeline.NewRunStore(paths)
	_ = rs.CreateManifest(pipeline.Manifest{RunID: "r1", PlanPath: filepath.Join(root, ".marshal/plans/my-plan.md")})
	_ = rs.AppendCheckpoint(pipeline.Checkpoint{Seq: 1, RunID: "r1", Phase: pipeline.PhaseImplementing, TaskN: 1})

	got := Discover(root, ".marshal/plans")
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if !got[0].Resumable() {
		t.Error("a run with an in-flight checkpoint should be resumable even with 0 done")
	}
}

func TestDiscoverPopulatesExecutionMetadata(t *testing.T) {
	root := t.TempDir()
	body := "# Plan\n\n## Task 1: Add file\n\n" +
		"```marshal.file path=\"created.txt\"\nhello\n```\n"
	writePlan(t, filepath.Join(root, ".marshal/plans"), "exec.md", body)

	got := Discover(root, ".marshal/plans")
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	c := got[0]
	if c.Err != nil {
		t.Fatalf("unexpected parse error: %v", c.Err)
	}
	if !c.HasBlocks {
		t.Error("an executable plan must report HasBlocks")
	}
	if c.DetOps != 1 {
		t.Errorf("DetOps = %d, want 1", c.DetOps)
	}
	if c.Strategy != pipeline.StrategyAdaptive {
		t.Errorf("Strategy = %q, want adaptive", c.Strategy)
	}
}

func TestDiscoverKeepsCandidateWithDiagnostics(t *testing.T) {
	root := t.TempDir()
	// A patch whose search block does not exist in the repo is a blocked
	// operation, but the candidate must still be listed.
	body := "# Plan\n\n## Task 1: Patch\n\n" +
		"```marshal.patch file=\"missing.go\"\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n```\n"
	writePlan(t, filepath.Join(root, ".marshal/plans"), "blocked.md", body)

	got := Discover(root, ".marshal/plans")
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1 — a plan with diagnostics must not be hidden", len(got))
	}
	if got[0].Err != nil {
		t.Fatalf("unexpected parse error: %v", got[0].Err)
	}
	if len(got[0].Diagnostics) == 0 {
		t.Error("a blocked plan must carry diagnostics")
	}
}

func TestDraftPathSlugifiesAndPrefixesDate(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	got, err := DraftPath(root, ".marshal/plans", "Add Retry Logic!", now)
	if err != nil {
		t.Fatalf("DraftPath: %v", err)
	}
	want := filepath.Join(root, ".marshal/plans", "2026-08-08-add-retry-logic.md")
	if got != want {
		t.Errorf("DraftPath = %q, want %q", got, want)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Error("DraftPath must not create the file")
	}
}

func TestDraftPathEmptyGoalFallsBackToPlan(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	got, err := DraftPath(root, ".marshal/plans", "   ", now)
	if err != nil {
		t.Fatalf("DraftPath: %v", err)
	}
	if !strings.HasSuffix(got, "2026-08-08-plan.md") {
		t.Errorf("DraftPath = %q, want plan fallback slug", got)
	}
}

func TestDraftPathAppendsNumericSuffixOnCollision(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	dir := filepath.Join(root, ".marshal/plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "2026-08-08-feature.md")
	if err := os.WriteFile(first, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := DraftPath(root, ".marshal/plans", "Feature", now)
	if err != nil {
		t.Fatalf("DraftPath: %v", err)
	}
	if got != filepath.Join(dir, "2026-08-08-feature-2.md") {
		t.Errorf("DraftPath = %q, want -2 suffix on collision", got)
	}
}

func TestDraftPathRejectsSymlinkedPlansDirOutsideRepo(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, ".marshal", "plans")); err != nil {
		t.Skipf("symlink(2) unavailable on this platform: %v", err)
	}
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	got, err := DraftPath(root, ".marshal/plans", "feature", now)
	if err == nil {
		t.Fatalf("DraftPath succeeded with path %q; want symlink-escape rejection", got)
	}
	if !strings.Contains(err.Error(), "outside the repository root") {
		t.Fatalf("err = %v, want outside-the-repository error", err)
	}
}
