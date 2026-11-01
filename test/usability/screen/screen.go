package screen

import (
	"fmt"

	"marshal/test/usability/harness"
)

// UIState captures actionable signals parsed from the TUI.
type UIState struct {
	Mode            string // e.g. auto, ask, edit
	Busy            bool
	HelpOpen        bool
	PendingApproval bool
	PendingQuestion bool
	InputValue      string
	LastAgentMsg    string
	ErrorVisible    bool
}

// Screen is a parsed snapshot.
type Screen struct {
	Width   int
	Height  int
	Content string
	Lines   []string
	State   UIState
}

// Parse turns a harness snapshot into a structured Screen. Content and Lines
// are already the plain-UTF-8 visible viewport, so no escape stripping is
// needed; a terminal model error is propagated rather than masked by a
// partial screen.
func Parse(snap harness.Snapshot) (Screen, error) {
	if snap.Err != nil {
		return Screen{}, fmt.Errorf("screen: terminal model error: %w", snap.Err)
	}

	content := string(snap.Content)
	lines := make([]string, len(snap.Lines))
	copy(lines, snap.Lines)

	scr := Screen{
		Width:   snap.Width,
		Height:  snap.Height,
		Content: content,
		Lines:   lines,
		State:   extractState(content, lines),
	}
	return scr, nil
}
