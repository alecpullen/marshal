package sdd

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeReportPrependsStatus(t *testing.T) {
	in := "Here is what I did.\n\nstatus: DONE\nThe implementation is complete.\n"
	got := NormalizeReport(in)
	if !strings.HasPrefix(got, "status: DONE\n") {
		t.Fatalf("normalized = %q, want DONE status line first", got)
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
