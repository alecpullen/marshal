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

// writeReportFile is a test helper that writes a report file into ws/reports/.
func writeReportFile(t *testing.T, ws *Workspace, name, content string) {
	t.Helper()
	if err := writeFile(filepath.Join(ws.Dir(), "reports", name), content); err != nil {
		t.Fatal(err)
	}
}
