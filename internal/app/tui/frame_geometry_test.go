package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
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

// TestSDDRunPanelDoesNotOverflowRailedFrame is the regression gate for the
// SDD top bar. The run panel spans the full frame width above both columns,
// so the side rail must be measured against the body below it. A rail sized
// to the full frame height renders one row taller than the terminal and
// pushes the status line off the bottom.
func TestSDDRunPanelDoesNotOverflowRailedFrame(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    session.SDDProgress
	}{
		{"active", session.SDDProgress{
			Active: true, PlanName: "p", TotalTasks: 3, CurrentTask: 1,
			Phase: "implementing", StartedAt: time.Unix(100, 0),
		}},
		{"finished", session.SDDProgress{
			Finished: true, Succeeded: true, PlanName: "p", TotalTasks: 3,
			DoneTasks: 3, StartedAt: time.Unix(100, 0), EndedAt: time.Unix(200, 0),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.state.Config.TUI.SidePanel.Enabled = true
			m.resize(160, 40)
			m.state.SetSDDProgress(tc.p)
			m.refreshViewport()

			// Precondition: the top bar must actually be rendering.
			if !strings.Contains(stripANSI(m.viewString()), "task 1/3") &&
				!strings.Contains(stripANSI(m.viewString()), "sdd done") {
				t.Fatalf("SDD top bar missing; scenario no longer exercises the bug:\n%s", stripANSI(m.viewString()))
			}

			rows, widest := frameRows(&m)
			if rows != 40 {
				t.Fatalf("frame rows = %d, want 40 (top bar + rail overflowed the frame)", rows)
			}
			if widest > 160 {
				t.Errorf("widest line = %d, want <= 160", widest)
			}
			lines := strings.Split(stripANSI(m.viewString()), "\n")
			if strings.TrimSpace(lines[39]) == "" {
				t.Errorf("status footer row is blank; footer pushed off screen:\n%s", strings.Join(lines, "\n"))
			}
		})
	}
}

// TestSDDFrameTranscriptRowsMatchHitTargets pins the frame's row mapping:
// with the SDD top bar on row 0, the transcript's first row is row 1 and a
// click there must resolve to the viewport's first visible content line.
func TestSDDFrameTranscriptRowsMatchHitTargets(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(160, 40)
	m.state.SetSDDProgress(session.SDDProgress{
		Active: true, PlanName: "p", TotalTasks: 3, CurrentTask: 1,
		Phase: "implementing", StartedAt: time.Unix(100, 0),
	})
	m.refreshViewport()

	if got := m.frame.TopBar; got.Y != 0 || got.Height != 1 || got.Width != 160 {
		t.Fatalf("TopBar = %+v, want a 1-row full-width bar at row 0", got)
	}
	if got := m.frame.Transcript.Y; got != 1 {
		t.Fatalf("Transcript.Y = %d, want 1 (directly below the top bar)", got)
	}
	line, ok := m.contentLineForClick(0, m.frame.Transcript.Y)
	if !ok {
		t.Fatal("the transcript's first row must be a valid hit target")
	}
	if line != m.viewport.YOffset() {
		t.Fatalf("line = %d, want YOffset %d", line, m.viewport.YOffset())
	}
	// Row 0 is the top bar, not the transcript.
	if _, ok := m.contentLineForClick(0, 0); ok {
		t.Error("row 0 is the SDD top bar and must not hit the transcript")
	}
}

// TestFrameRectsStayInsideBounds checks the frame invariant across the
// supported size range and repeated wide/narrow transitions: every
// non-empty rectangle lies inside the frame, and the composer and footer
// stay on screen.
func TestFrameRectsStayInsideBounds(t *testing.T) {
	sizes := []struct{ w, h int }{
		{80, 24}, {120, 40}, {160, 40}, {200, 60}, {80, 24}, {200, 60}, {20, 5},
	}
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	for _, size := range sizes {
		m.resize(size.w, size.h)
		m.refreshViewport()
		f := m.frame
		wantW, wantH := max(size.w, minTerminalWidth), max(size.h, minTerminalHeight)
		if f.Width != wantW || f.Height != wantH {
			t.Fatalf("frame %dx%d, want %dx%d for terminal %dx%d", f.Width, f.Height, wantW, wantH, size.w, size.h)
		}
		for name, r := range map[string]layout.Rect{
			"TopBar":     f.TopBar,
			"Transcript": f.Transcript,
			"Inspector":  f.Inspector,
			"Activity":   f.Activity,
			"Dock":       f.Dock,
			"Composer":   f.Composer,
			"Footer":     f.Footer,
		} {
			if r.Empty() {
				continue
			}
			if r.X < 0 || r.Y < 0 || r.Right() > f.Width || r.Bottom() > f.Height {
				t.Errorf("%dx%d: %s = %+v escapes the %dx%d frame", size.w, size.h, name, r, f.Width, f.Height)
			}
		}
		if f.Composer.Empty() {
			t.Errorf("%dx%d: composer has zero area", size.w, size.h)
		}
		if f.Footer.Empty() {
			t.Errorf("%dx%d: footer has zero area", size.w, size.h)
		}
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
