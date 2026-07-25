package sdd

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeReportFindsLaterStatusInBody(t *testing.T) {
	in := "Here is what I did.\n\nstatus: DONE\nThe implementation is complete.\n"
	got := NormalizeReport(in)
	if !strings.HasPrefix(got, "status: DONE\n") {
		t.Fatalf("normalized = %q, want DONE status line first", got)
	}
}

func TestNormalizeReportUnchangedWhenNoStatus(t *testing.T) {
	in := "Just some content with no status line anywhere.\n\nAnother paragraph.\n"
	got := NormalizeReport(in)
	if got != in {
		t.Fatalf("normalized = %q, want unchanged input %q", got, in)
	}
}

func TestNormalizeReportKeepsExistingFirstLine(t *testing.T) {
	in := "status: PASS\n\naudit clean\n"
	got := NormalizeReport(in)
	if got != in {
		t.Fatalf("normalized changed existing first-line status: %q vs %q", got, in)
	}
}

func TestNormalizeReportFindsLaterStatus(t *testing.T) {
	in := "Audit details...\n\nstatus: FAIL\nmore details\n"
	got := NormalizeReport(in)
	if !strings.HasPrefix(got, "status: FAIL\n") {
		t.Fatalf("normalized = %q, want FAIL on first line", got)
	}
}

func TestReadReportMissing(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	if _, err := ReadReport(ws, "T1", ""); err == nil {
		t.Fatal("expected error for missing report file, got nil")
	}
}

func TestReportWriteAndRead(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	r := &Report{TaskID: "T1", Status: ReportDone, Body: "implementation done."}
	if err := r.Write(ws, ""); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(ws.Dir(), "reports", "T1.md")
	if _, err := stat(want); err != nil {
		t.Fatalf("expected file at %s: %v", want, err)
	}
	got, err := ReadReport(ws, "T1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ReportDone {
		t.Errorf("Status = %q, want DONE", got.Status)
	}
	if !strings.Contains(got.Body, "implementation done.") {
		t.Errorf("Body = %q", got.Body)
	}
}

func TestReportWriteWithKind(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	r := &Report{TaskID: "T1", Status: ReportPass, Body: "audit clean."}
	if err := r.Write(ws, "-audit"); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(ws.Dir(), "reports", "T1-audit.md")
	if _, err := stat(want); err != nil {
		t.Fatalf("expected file at %s: %v", want, err)
	}
	got, err := ReadReport(ws, "T1", "-audit")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ReportPass {
		t.Errorf("Status = %q, want PASS", got.Status)
	}
}

func TestReportValidate(t *testing.T) {
	cases := []struct {
		name   string
		report *Report
		wantOK bool
	}{
		{"valid", &Report{TaskID: "T1", Status: ReportDone, Body: "x"}, true},
		{"empty body", &Report{TaskID: "T1", Status: ReportDone, Body: ""}, false},
		{"unknown status", &Report{TaskID: "T1", Status: ReportStatus("MAYBE"), Body: "x"}, false},
	}
	for _, c := range cases {
		err := c.report.Validate()
		if c.wantOK && err != nil {
			t.Errorf("%s: expected OK, got %v", c.name, err)
		}
		if !c.wantOK && err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}

func TestParseOrchestratorReportDone(t *testing.T) {
	text := `status: DONE
batch_id: 3
dispatched: [T2, T3]
merged: [T1, T4]
blocked_tasks: []
health_alerts: []
edit_guard: clean
next_action: drain
`
	r, err := ParseOrchestratorReport(text)
	if err != nil {
		t.Fatalf("ParseOrchestratorReport: %v", err)
	}
	if r.Status != ReportDone {
		t.Errorf("Status = %q, want DONE", r.Status)
	}
	if r.BatchID != 3 {
		t.Errorf("BatchID = %d", r.BatchID)
	}
	if len(r.Dispatched) != 2 || r.Dispatched[0] != "T2" {
		t.Errorf("Dispatched = %v", r.Dispatched)
	}
	if len(r.Merged) != 2 || r.Merged[1] != "T4" {
		t.Errorf("Merged = %v", r.Merged)
	}
	if r.NextAction != "drain" {
		t.Errorf("NextAction = %q", r.NextAction)
	}
}

func TestParseOrchestratorReportBlocked(t *testing.T) {
	text := `status: BLOCKED
batch_id: 2
dispatched: [T5]
merged: []
blocked_tasks:
  - task: T5
    reason: REBASE_CONFLICT
    detail: sdd/T5 cannot rebase onto sdd/feature
    files_in_conflict: [internal/foo.go]
health_alerts: []
edit_guard: clean
next_action: surface_blockers
`
	r, err := ParseOrchestratorReport(text)
	if err != nil {
		t.Fatalf("ParseOrchestratorReport: %v", err)
	}
	if r.Status != ReportBlocked {
		t.Errorf("Status = %q, want BLOCKED", r.Status)
	}
	if len(r.BlockedTasks) != 1 {
		t.Fatalf("BlockedTasks len = %d", len(r.BlockedTasks))
	}
	bt := r.BlockedTasks[0]
	if bt.Task != "T5" || bt.Reason != "REBASE_CONFLICT" {
		t.Errorf("BlockedTask = %+v", bt)
	}
	if len(bt.FilesInConflict) != 1 || bt.FilesInConflict[0] != "internal/foo.go" {
		t.Errorf("FilesInConflict = %v", bt.FilesInConflict)
	}
}

func TestParseOrchestratorReportMissingStatus(t *testing.T) {
	text := "batch_id: 1\n"
	_, err := ParseOrchestratorReport(text)
	if err == nil {
		t.Fatal("expected error for missing status")
	}
}
