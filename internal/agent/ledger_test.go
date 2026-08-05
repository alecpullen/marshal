package agent

import (
	"strings"
	"testing"

	"marshal/internal/db"
)

// TestLedgerLine_Compact confirms the canonical example from the plan:
// a turn with mixed tools renders as a single line of "tools: <tool>
// <summary>" entries separated by ", ".
func TestLedgerLine_Compact(t *testing.T) {
	got := LedgerLine([]db.ToolAuditEntry{
		{Tool: "file.read", Summary: "a.go:1-80 (412t)", Ok: true},
		{Tool: "file.write_patch", Summary: "b.go", Ok: true},
		{Tool: "shell.run", Summary: "go test ./...", Ok: true},
	})
	want := "tools: file.read a.go:1-80 (412t), file.write_patch b.go, shell.run go test ./..."
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// TestLedgerLine_TruncatesLong: 50 entries must cap at or below 400
// characters and surface the ", ..." cut marker.
func TestLedgerLine_TruncatesLong(t *testing.T) {
	entries := make([]db.ToolAuditEntry, 50)
	for i := range entries {
		entries[i] = db.ToolAuditEntry{
			Tool:    "file.read",
			Summary: "abcdefghijklmnopqrstuvwxyz",
			Ok:      true,
		}
	}
	got := LedgerLine(entries)
	if len(got) > 400 {
		t.Fatalf("len = %d, want <= 400 (cap %d + slack)", len(got), ledgerLineCap)
	}
	if !strings.HasSuffix(got, ", ...") {
		t.Fatalf("expected trailing ', ...' marker, got %q", got[len(got)-10:])
	}
}

// TestLedgerLine_MarksErrors renders the "(err)" tag for failed calls.
func TestLedgerLine_MarksErrors(t *testing.T) {
	got := LedgerLine([]db.ToolAuditEntry{
		{Tool: "shell.run", Summary: "go test ./...", Ok: false},
	})
	if !strings.Contains(got, "(err)") {
		t.Fatalf("expected (err) marker on failed call, got %q", got)
	}
}

// TestLedgerLine_EmptyReturnsEmpty: the empty case must produce empty
// output (caller falls back to the legacy placeholder).
func TestLedgerLine_EmptyReturnsEmpty(t *testing.T) {
	if got := LedgerLine(nil); got != "" {
		t.Fatalf("nil input should return empty, got %q", got)
	}
	if got := LedgerLine([]db.ToolAuditEntry{}); got != "" {
		t.Fatalf("empty input should return empty, got %q", got)
	}
}
