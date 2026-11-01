package screen

import (
	"errors"
	"strings"
	"testing"

	"marshal/test/usability/harness"
)

// viewport builds a snapshot whose Content is the viewport contract's join of
// Lines, so fixtures cannot drift from the plain-UTF-8 screen they describe.
func viewport(width, height int, lines ...string) harness.Snapshot {
	return harness.Snapshot{
		Width:   width,
		Height:  height,
		Content: []byte(strings.Join(lines, "\n")),
		Lines:   lines,
	}
}

func TestParseHelpOpen(t *testing.T) {
	snap := viewport(80, 3, "Keys", "  Enter send message", "")
	scr, err := Parse(snap)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !scr.State.HelpOpen {
		t.Fatalf("HelpOpen = false, want true")
	}
	if scr.Content != strings.Join(snap.Lines, "\n") {
		t.Fatalf("Content = %q, want %q", scr.Content, strings.Join(snap.Lines, "\n"))
	}
}

func TestParsePendingApproval(t *testing.T) {
	snap := viewport(80, 3, "Agent wants to run:", "  go test ./...", "[Enter] approve")
	scr, err := Parse(snap)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !scr.State.PendingApproval {
		t.Fatalf("PendingApproval = false, want true")
	}
}

func TestParseSnapshotError(t *testing.T) {
	modelErr := errors.New("terminal model: unsupported screen control sequence")
	snap := viewport(80, 2, "Keys", "")
	snap.Err = modelErr

	scr, err := Parse(snap)
	if err == nil {
		t.Fatalf("Parse error = nil, want the terminal model error")
	}
	if !errors.Is(err, modelErr) {
		t.Fatalf("Parse error = %v, want it to wrap %v", err, modelErr)
	}
	if scr.Content != "" || len(scr.Lines) != 0 || scr.Width != 0 || scr.Height != 0 {
		t.Fatalf("Parse screen = %+v, want the zero screen on error", scr)
	}
}
