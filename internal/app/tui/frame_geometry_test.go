package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
)

// frameRows renders the frame and reports its row count and widest line.
func frameRows(m *Model) (rows, widest int) {
	lines := strings.Split(m.viewString(), "\n")
	for _, l := range lines {
		if lw := ansi.StringWidth(l); lw > widest {
			widest = lw
		}
	}
	return len(lines), widest
}

// fillTranscript adds enough messages to overflow the viewport, then scrolls
// off the bottom so the "↑ scrolled" hint row appears.
func scrolledModel(t *testing.T, w, h int) *Model {
	t.Helper()
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(w, h)
	for i := 0; i < 200; i++ {
		m.state.AddMessage(session.RoleAssistant, "line", session.ContentTypePlain)
	}
	m.refreshViewport()
	m.viewportFollow = false
	m.refreshViewport()
	return &m
}

// TestScrollHintDoesNotOverflowFrame is the regression gate for the
// disappearing input area. The scroll hint row is rendered above the
// transcript, so it must be part of the row budget — otherwise the left
// column is one row taller than the terminal and the bottom row (the input,
// then the status line) is pushed off screen.
func TestScrollHintDoesNotOverflowFrame(t *testing.T) {
	for _, tc := range []struct {
		name string
		busy bool
	}{
		{"idle", false},
		{"busy", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := scrolledModel(t, 140, 30)
			if tc.busy {
				m.state.SetActivity(session.Activity{Kind: session.ActivityThinking})
				m.refreshViewport()
			}
			// Precondition: the hint must actually be showing.
			if !strings.Contains(stripANSI(m.renderTranscriptFrame()), "scrolled") {
				t.Fatal("scroll hint not rendered; scenario no longer exercises the bug")
			}
			rows, widest := frameRows(m)
			if rows != 30 {
				t.Errorf("frame rows = %d, want 30", rows)
			}
			if widest > 140 {
				t.Errorf("widest line = %d, want <= 140", widest)
			}
		})
	}
}

// TestFollowingFrameFitsTerminal guards the non-scrolled baseline: no hint
// row, and the frame still exactly fills the terminal.
func TestFollowingFrameFitsTerminal(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(140, 30)
	for i := 0; i < 200; i++ {
		m.state.AddMessage(session.RoleAssistant, "line", session.ContentTypePlain)
	}
	m.refreshViewport()
	if rows, widest := frameRows(&m); rows != 30 || widest > 140 {
		t.Errorf("rows = %d (want 30), widest = %d (want <= 140)", rows, widest)
	}
}
