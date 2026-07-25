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

// writeReportFile is a test helper that writes a report file into ws/reports/.
func writeReportFile(t *testing.T, ws *Workspace, name, content string) {
	t.Helper()
	if err := writeFile(filepath.Join(ws.Dir(), "reports", name), content); err != nil {
		t.Fatal(err)
	}
}
