package screen

import (
	"testing"

	"marshal/test/usability/harness"
)

func TestStripANSI(t *testing.T) {
	in := "\x1b[31mhello\x1b[0m world"
	want := "hello world"
	got := StripANSI([]byte(in))
	if got != want {
		t.Fatalf("StripANSI = %q, want %q", got, want)
	}
}

func TestParseHelpOpen(t *testing.T) {
	snap := harness.Snapshot{
		Width:   80,
		Height:  24,
		Content: []byte("marshal keys\n  Enter send message\n"),
		Lines:   []string{"marshal keys", "  Enter send message"},
	}
	scr, err := Parse(snap)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !scr.State.HelpOpen {
		t.Fatalf("HelpOpen = false, want true")
	}
}

func TestParsePendingApproval(t *testing.T) {
	snap := harness.Snapshot{
		Content: []byte("Agent wants to run:\n  go test ./...\nRisk: Low\n[Enter] approve"),
	}
	scr, err := Parse(snap)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !scr.State.PendingApproval {
		t.Fatalf("PendingApproval = false, want true")
	}
}
