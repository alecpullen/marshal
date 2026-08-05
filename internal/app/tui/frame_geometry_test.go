package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/dock"
	"marshal/internal/tools/native"
)

// overRenderingPanel is a dock panel that ignores its height budget,
// simulating any panel whose rendered rows exceed what the layout budgeted.
type overRenderingPanel struct{ rows int }

func (p overRenderingPanel) Update(tea.Msg) tea.Cmd { return nil }
func (p overRenderingPanel) View(_, _ int) string {
	return strings.Repeat("panel row\n", p.rows-1) + "panel row"
}
func (p overRenderingPanel) Sizing() dock.Sizing { return dock.FullFrame }

func TestClipLeftColumn(t *testing.T) {
	three := "a\nb\nc"
	if got := clipLeftColumn(three, 5); got != three {
		t.Errorf("under-height column changed: %q", got)
	}
	if got := clipLeftColumn(three, 3); got != three {
		t.Errorf("exact-height column changed: %q", got)
	}
	if got := clipLeftColumn(three, 2); got != "b\nc" {
		t.Errorf("clipLeftColumn = %q, want %q (surplus dropped from the top)", got, "b\nc")
	}
	if got := clipLeftColumn(three, 0); got != "c" {
		t.Errorf("clipLeftColumn with 0 budget = %q, want %q (floors to 1 row)", got, "c")
	}
}

// TestOverRenderingPanelCannotHideFooter proves the frame-height invariant
// end to end: even when a panel renders far more rows than the layout
// budgeted, the frame stays exactly terminal-height and the status footer
// remains on the last row.
func TestOverRenderingPanelCannotHideFooter(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	m.dock.Open(overRenderingPanel{rows: 45})

	lines := strings.Split(stripANSI(m.viewString()), "\n")
	if len(lines) != 30 {
		t.Fatalf("frame rows = %d, want 30 (over-rendering panel pushed chrome off screen)", len(lines))
	}
	if strings.TrimSpace(lines[29]) == "" {
		t.Errorf("status footer row is blank; footer pushed off screen:\n%s", strings.Join(lines, "\n"))
	}
}

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

// TestInputAndFooterVisibleWithTodosAndFullTranscript is the regression gate
// for the reported bug: with a full (overflowing) transcript and the todo
// panel visible, the text entry box and the status footer must stay on
// screen — both while following the transcript and while scrolled up.
func TestInputAndFooterVisibleWithTodosAndFullTranscript(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scrolled bool
		busy     bool
	}{
		{"following", false, false},
		{"scrolled", true, false},
		{"scrolled_busy", true, true},
		{"following_busy", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := scrolledModel(t, 140, 30)
			if err := m.state.SetTodos([]native.TodoItem{
				{Content: "first task", Status: native.TodoCompleted},
				{Content: "second task", Status: native.TodoInProgress},
				{Content: "third task", Status: native.TodoPending},
			}); err != nil {
				t.Fatalf("SetTodos: %v", err)
			}
			if !tc.scrolled {
				m.viewportFollow = true
			}
			if tc.busy {
				m.busy = true
				m.turnStartedAt = m.now()
			}
			m.refreshViewport()

			// Precondition: the todo panel must actually be showing.
			frame := stripANSI(m.viewString())
			if !strings.Contains(frame, "tasks 1/3") {
				t.Fatalf("todo panel missing; scenario no longer exercises the bug:\n%s", frame)
			}

			lines := strings.Split(frame, "\n")
			if len(lines) != 30 {
				t.Fatalf("frame rows = %d, want 30 (overflow pushes input/footer off screen):\n%s", len(lines), frame)
			}

			inputRow := -1
			for i, line := range lines {
				if strings.Contains(line, "❯") {
					inputRow = i
				}
			}
			if inputRow < 0 {
				t.Fatalf("input box missing from the frame:\n%s", frame)
			}
			if inputRow >= 29 {
				t.Errorf("input box at row %d leaves no room for the status footer:\n%s", inputRow, frame)
			}
			if strings.TrimSpace(lines[29]) == "" {
				t.Errorf("status footer row is blank; footer pushed off screen:\n%s", frame)
			}
		})
	}
}
