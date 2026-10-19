package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
)

// screenY is the screen row of content line l.
func screenY(m *Model, l int) int {
	return m.scrollHintRows() + m.breadcrumbRows() + l - m.viewport.YOffset()
}

// dragTo presses at one cell, drags to another and releases there, running
// the release's command so a local copy lands in the fake clipboard.
func dragTo(t *testing.T, m Model, x0, y0, x1, y1 int) Model {
	t.Helper()
	next, _ := m.Update(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	next, _ = next.Update(tea.MouseMotionMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	next, cmd := next.Update(tea.MouseReleaseMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	m = next.(Model)
	for _, msg := range flattenCmd(cmd) {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func TestDragCopiesTheRenderedTranscript(t *testing.T) {
	m, clip := selectionFixture(t)
	last := len(m.contentLines) - 1
	m = dragTo(t, m, 0, screenY(&m, 0), 79, screenY(&m, last))
	if len(clip.writes) != 1 {
		t.Fatalf("clipboard writes = %d, want 1", len(clip.writes))
	}
	if got := clip.writes[0]; got != selectionFixtureCopy {
		t.Fatalf("copied:\n%s\n--- want:\n%s", got, selectionFixtureCopy)
	}
	if m.sel == nil || m.sel.dragging {
		t.Fatalf("after release the selection must stay, not dragging: %+v", m.sel)
	}
}

func TestPressAloneDoesNotToggle(t *testing.T) {
	m, _ := selectionFixture(t)
	y := screenY(&m, lineOf(t, &m, "thought for 2s"))
	next, _ := m.Update(tea.MouseClickMsg{X: 8, Y: y, Button: tea.MouseLeft})
	mm := asModel(t, next)
	if strings.Contains(stripANSI(strings.Join(mm.contentLines, "\n")), "private reasoning text") {
		t.Fatal("a press alone expanded the thinking row; clicks act on release")
	}
}

func TestClickTogglesOnRelease(t *testing.T) {
	m, _ := selectionFixture(t)
	y := screenY(&m, lineOf(t, &m, "thought for 2s"))
	mm := asModel(t, clickAt(t, m, 8, y))
	if !strings.Contains(stripANSI(strings.Join(mm.contentLines, "\n")), "private reasoning text") {
		t.Fatal("press and release on one cell did not toggle the row")
	}
	if mm.sel != nil {
		t.Fatal("a click must not leave a selection")
	}
}

func TestClickClearsASelection(t *testing.T) {
	m, _ := selectionFixture(t)
	y := screenY(&m, lineOf(t, &m, "Model switched"))
	m = dragTo(t, m, 3, y, 10, y)
	if m.sel == nil {
		t.Fatal("drag left no selection")
	}
	mm := asModel(t, clickAt(t, m, 3, y))
	if mm.sel != nil {
		t.Fatal("a click must clear the selection")
	}
}

func TestDragHoldsTheViewWhileOutputStreams(t *testing.T) {
	m, _ := selectionFixture(t)
	m.resize(80, 30)
	m.viewportFollow = true
	m.invalidateTranscript()
	m.refreshViewport()
	y := screenY(&m, lineOf(t, &m, "Model switched"))
	next, _ := m.Update(tea.MouseClickMsg{X: 3, Y: y, Button: tea.MouseLeft})
	next, _ = next.Update(tea.MouseMotionMsg{X: 10, Y: y, Button: tea.MouseLeft})
	mm := asModel(t, next)
	if !mm.dragging() {
		t.Fatal("setup: the drag did not start")
	}
	offset, hint := mm.viewport.YOffset(), mm.scrollHintRows()
	for i := 0; i < 5; i++ {
		mm.state.AddMessage(session.RoleSystem, "streamed line", session.ContentTypePlain)
	}
	mm.invalidateTranscript()
	mm.refreshViewport()
	if mm.viewport.YOffset() != offset || mm.scrollHintRows() != hint {
		t.Fatalf("output moved the view during a drag: offset %d→%d, hint %d→%d", offset, mm.viewport.YOffset(), hint, mm.scrollHintRows())
	}
	next, _ = mm.Update(tea.MouseReleaseMsg{X: 10, Y: y, Button: tea.MouseLeft})
	mm = asModel(t, next)
	if !mm.viewport.AtBottom() {
		t.Fatal("following must resume on release")
	}
}

func TestDragPastTheBottomAutoScrolls(t *testing.T) {
	m, _ := selectionFixture(t)
	m.resize(80, 30)
	m.viewportFollow = false
	m.invalidateTranscript()
	m.refreshViewport()
	m.viewport.GotoTop()
	top := m.scrollHintRows() + m.breadcrumbRows()
	next, _ := m.Update(tea.MouseClickMsg{X: 3, Y: top, Button: tea.MouseLeft})
	below := top + m.viewport.Height() + 1
	next, cmd := next.Update(tea.MouseMotionMsg{X: 3, Y: below, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("dragging past the bottom armed no auto-scroll tick")
	}
	mm := asModel(t, next)
	if !mm.dragging() {
		t.Fatal("setup: the drag did not start")
	}
	offset := mm.viewport.YOffset()
	if cmd := mm.handleSelScrollTick(selScrollTickMsg{gen: mm.drag.gen}); cmd == nil {
		t.Fatal("tick did not re-arm while the pointer stays past the edge")
	}
	if mm.viewport.YOffset() != offset+1 {
		t.Fatalf("offset %d→%d, want one row down", offset, mm.viewport.YOffset())
	}
	if cmd := mm.handleSelScrollTick(selScrollTickMsg{gen: mm.drag.gen - 1}); cmd != nil || mm.viewport.YOffset() != offset+1 {
		t.Fatal("a tick from an earlier drag must do nothing")
	}
}

func TestWheelDuringADragExtendsTheSelection(t *testing.T) {
	m, _ := selectionFixture(t)
	m.resize(80, 30)
	m.viewportFollow = false
	m.invalidateTranscript()
	m.refreshViewport()
	m.viewport.GotoTop()
	top := m.scrollHintRows() + m.breadcrumbRows()
	next, _ := m.Update(tea.MouseClickMsg{X: 3, Y: top, Button: tea.MouseLeft})
	next, _ = next.Update(tea.MouseMotionMsg{X: 3, Y: top + 2, Button: tea.MouseLeft})
	mm := asModel(t, next)
	if !mm.dragging() {
		t.Fatal("setup: the drag did not start")
	}
	before, _ := mm.resolve(mm.sel.head)
	next, _ = mm.Update(tea.MouseWheelMsg{X: 3, Y: top + 2, Button: tea.MouseWheelDown})
	mm = asModel(t, next)
	after, _ := mm.resolve(mm.sel.head)
	if after.line <= before.line {
		t.Fatalf("head stayed at line %d after the wheel scrolled (was %d)", after.line, before.line)
	}
}

func TestShiftClickExtendsTheSelection(t *testing.T) {
	m, clip := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	m = dragTo(t, m, 3, screenY(&m, l), 10, screenY(&m, l))
	next, cmd := m.Update(tea.MouseClickMsg{X: 79, Y: screenY(&m, l+1), Button: tea.MouseLeft, Mod: tea.ModShift})
	m = asModel(t, next)
	for _, msg := range flattenCmd(cmd) {
		next, _ := m.Update(msg)
		m = asModel(t, next)
	}
	if len(clip.writes) != 2 || clip.writes[1] != "Model switched to gpt-x\nsecond notice line" {
		t.Fatalf("writes = %q, want a second copy reaching the next row", clip.writes)
	}
}

func TestPressDuringADragAbandonsIt(t *testing.T) {
	m, clip := selectionFixture(t)
	y := screenY(&m, lineOf(t, &m, "Model switched"))
	next, _ := m.Update(tea.MouseClickMsg{X: 3, Y: y, Button: tea.MouseLeft})
	next, _ = next.Update(tea.MouseMotionMsg{X: 10, Y: y, Button: tea.MouseLeft})
	next, _ = next.Update(tea.MouseClickMsg{X: 3, Y: y, Button: tea.MouseLeft})
	mm := asModel(t, next)
	if mm.sel != nil || len(clip.writes) != 0 {
		t.Fatalf("a press during a drag must drop it uncopied: sel=%v writes=%q", mm.sel, clip.writes)
	}
}

func TestNoSelectionWithoutMouseCapture(t *testing.T) {
	m, clip := selectionFixture(t)
	m.state.Config.TUI.MouseCapture = false
	l := lineOf(t, &m, "Model switched")
	m = dragTo(t, m, 0, screenY(&m, l), 79, screenY(&m, l))
	if m.sel != nil || len(clip.writes) != 0 {
		t.Fatalf("with mouse capture off: sel=%v writes=%q, want nothing", m.sel, clip.writes)
	}
}
