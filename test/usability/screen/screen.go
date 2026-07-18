package screen

import "marshal/test/usability/harness"

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

// Parse turns a harness snapshot into a structured Screen.
func Parse(snap harness.Snapshot) (Screen, error) {
	content := StripANSI(snap.Content)
	lines := make([]string, 0, len(snap.Lines))
	for _, ln := range snap.Lines {
		lines = append(lines, StripANSI([]byte(ln)))
	}

	scr := Screen{
		Width:   snap.Width,
		Height:  snap.Height,
		Content: content,
		Lines:   lines,
		State:   extractState(content, lines),
	}
	return scr, nil
}
