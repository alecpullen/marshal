package sdd

import (
	"path/filepath"
	"testing"
)

func TestAuditGateMissing(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	r := AuditGate(ws, "T1")
	if r.Decision != DecisionBlock || r.Event != "AUDIT_MISSING" {
		t.Fatalf("expected AUDIT_MISSING block, got %+v", r)
	}
}

func TestAuditGatePass(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	writeReportFile(t, ws, "T1-audit.md", "status: PASS\n\nno issues\n")
	r := AuditGate(ws, "T1")
	if r.Decision != DecisionAccept || r.Event != "AUDIT_PASS" {
		t.Fatalf("expected AUDIT_PASS accept, got %+v", r)
	}
}

func TestAuditGateFail(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	writeReportFile(t, ws, "T1-audit.md", "status: FAIL\n\nunused import\n")
	r := AuditGate(ws, "T1")
	if r.Decision != DecisionBlock || r.Event != "AUDIT_FAIL" {
		t.Fatalf("expected AUDIT_FAIL block, got %+v", r)
	}
}

func TestAuditGateMalformed(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	// Report with no status line at all.
	writeReportFile(t, ws, "T1-audit.md", "just some audit notes\n")
	r := AuditGate(ws, "T1")
	if r.Decision != DecisionBlock || r.Event != "AUDIT_MALFORMED" {
		t.Fatalf("expected AUDIT_MALFORMED block, got %+v", r)
	}
}

func TestReviewStateSkipAlreadyReviewed(t *testing.T) {
	git := NewFakeGitOps()
	git.SetRef("sdd/T1", "head123")
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Branch: "sdd/T1", ReviewedHead: "head123"}}}
	r := ReviewState(git, dag, "T1")
	if r.Decision != DecisionSkip || r.Event != "REVIEW_SKIP" {
		t.Fatalf("expected REVIEW_SKIP (already reviewed), got %+v", r)
	}
}

func TestReviewStateRequired(t *testing.T) {
	git := NewFakeGitOps()
	git.SetRef("sdd/T1", "newhead")
	git.SetDiffStat("oldhead", "sdd/T1", " internal/foo.go | 50 ++++++++++++++++++++++++++++++++++++++++++++++++\n")
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Branch: "sdd/T1", ReviewedHead: "oldhead"}}}
	r := ReviewState(git, dag, "T1")
	if r.Decision != DecisionSkip && r.Event != "REVIEW_REQUIRED" {
		// Either REQUIRED (big diff) — check the decision.
	}
	if r.Event != "REVIEW_REQUIRED" {
		t.Fatalf("expected REVIEW_REQUIRED (50-line diff), got %+v", r)
	}
}

func TestReviewStateSkipTrivialDiff(t *testing.T) {
	git := NewFakeGitOps()
	git.SetRef("sdd/T1", "newhead")
	git.SetDiffStat("oldhead", "sdd/T1", " internal/foo.go | 5 +++++\n")
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Branch: "sdd/T1", ReviewedHead: "oldhead"}}}
	r := ReviewState(git, dag, "T1")
	if r.Decision != DecisionSkip || r.Event != "REVIEW_SKIP" {
		t.Fatalf("expected REVIEW_SKIP (trivial 5-line diff), got %+v", r)
	}
}

func TestReviewStateRequiredFirstPass(t *testing.T) {
	git := NewFakeGitOps()
	git.SetRef("sdd/T1", "head123")
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Branch: "sdd/T1", ReviewedHead: ""}}}
	r := ReviewState(git, dag, "T1")
	if r.Event != "REVIEW_REQUIRED" {
		t.Fatalf("expected REVIEW_REQUIRED (first pass), got %+v", r)
	}
}

func TestReviewGuardPass(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	var p Progress
	writeReportFile(t, ws, "T1-review.md", "status: PASS\n\nlooks good\n")
	r := ReviewGuard(ws, &p, "T1")
	if r.Decision != DecisionAccept || r.Event != "REVIEW_PASS" {
		t.Fatalf("expected REVIEW_PASS, got %+v", r)
	}
}

func TestReviewGuardFail(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	var p Progress
	writeReportFile(t, ws, "T1-review.md", "status: FAIL\n\nbug in foo.go:42\n")
	r := ReviewGuard(ws, &p, "T1")
	if r.Decision != DecisionBlock || r.Event != "REVIEW_FAIL" {
		t.Fatalf("expected REVIEW_FAIL block, got %+v", r)
	}
}

func TestReviewGuardOverrideDetected(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	var p Progress
	// Seed progress: REVIEW_FAIL for T1, then no RETRY, then this PASS.
	p.Append(ws, "T1", "REVIEW_FAIL", "finding", "bug")
	writeReportFile(t, ws, "T1-review.md", "status: PASS\n\nlooks good\n")
	r := ReviewGuard(ws, &p, "T1")
	if r.Decision != DecisionReject || r.Event != "ORCHESTRATOR_OVERRIDE" {
		t.Fatalf("expected ORCHESTRATOR_OVERRIDE reject, got %+v", r)
	}
}

func TestReviewGuardOverrideClearedByRetry(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	var p Progress
	p.Append(ws, "T1", "REVIEW_FAIL", "finding", "bug")
	p.Append(ws, "T1", "RETRY", "attempt", "2")
	writeReportFile(t, ws, "T1-review.md", "status: PASS\n\nfixed\n")
	r := ReviewGuard(ws, &p, "T1")
	if r.Decision != DecisionAccept || r.Event != "REVIEW_PASS" {
		t.Fatalf("expected REVIEW_PASS (retry cleared the prior fail), got %+v", r)
	}
}

// writeReportFile is a test helper that writes a report file into ws/reports/.
func writeReportFile(t *testing.T, ws *Workspace, name, content string) {
	t.Helper()
	if err := writeFile(filepath.Join(ws.Dir(), "reports", name), content); err != nil {
		t.Fatal(err)
	}
}
