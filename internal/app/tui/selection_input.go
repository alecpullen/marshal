package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// selScrollTickMsg advances edge auto-scroll by one row.
type selScrollTickMsg struct{ gen int }

// selScrollInterval is how often edge auto-scroll moves the view one row.
const selScrollInterval = 50 * time.Millisecond

// contentPosAt maps a screen cell to a content position, clamping cells
// outside the viewport to its nearest edge.
func (m *Model) contentPosAt(x, y int) selPos {
	top := m.scrollHintRows() + m.breadcrumbRows()
	h := max(m.viewport.Height(), 1)
	y = min(max(y, top), top+h-1)
	x = min(max(x, 0), max(m.leftWidth-1, 0))
	return selPos{line: m.viewport.YOffset() + y - top, col: x}
}

// inViewport reports whether a screen cell is inside the transcript
// viewport.
func (m *Model) inViewport(x, y int) bool {
	_, ok := m.contentLineForClick(x, y)
	return ok
}

// edgeDir reports which way a pointer at screen row y should auto-scroll the
// transcript: -1 up, 1 down, 0 not at all. When nothing sits above the
// viewport, its first row is the top edge.
func (m *Model) edgeDir(y int) int {
	top := m.scrollHintRows() + m.breadcrumbRows()
	switch {
	case y < top || (top == 0 && y == 0):
		return -1
	case y >= top+m.viewport.Height():
		return 1
	}
	return 0
}

// handleTranscriptMouse owns every mouse event aimed at the transcript:
// presses, drags, releases and the wheel. A left click acts on release, so a
// press can still turn into a drag. handled is false for events it leaves to
// the caller.
func (m *Model) handleTranscriptMouse(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		return m.handleWheel(msg), true
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return nil, false
		}
		mouse := tea.Mouse(msg)
		if msg.Mod&tea.ModShift != 0 && m.sel != nil && !m.sel.dragging && m.inViewport(msg.X, msg.Y) {
			if h, ok := m.anchorAt(m.contentPosAt(msg.X, msg.Y)); ok {
				m.sel.head = h
				return m.finishSelection(), true
			}
		}
		if m.dragging() {
			// The release of the previous drag never arrived.
			m.clearSelection()
		}
		m.press = &mouse
		return nil, true
	case tea.MouseMotionMsg:
		if m.press == nil || !m.selectionEnabled() {
			return nil, false
		}
		m.drag.pointer = tea.Mouse(msg)
		if !m.dragging() {
			if (msg.X == m.press.X && msg.Y == m.press.Y) || !m.inViewport(m.press.X, m.press.Y) {
				return nil, true
			}
			a, ok := m.anchorAt(m.contentPosAt(m.press.X, m.press.Y))
			if !ok {
				return nil, true
			}
			m.drag.gen++
			m.drag.ticking = false
			m.sel = &selection{anchor: a, head: a, dragging: true}
		}
		if h, ok := m.anchorAt(m.contentPosAt(msg.X, msg.Y)); ok {
			m.sel.head = h
		}
		if m.edgeDir(msg.Y) != 0 && !m.drag.ticking {
			m.drag.ticking = true
			return m.selScrollTick(), true
		}
		return nil, true
	case tea.MouseReleaseMsg:
		if m.press == nil {
			return nil, false
		}
		press := *m.press
		m.press = nil
		if m.dragging() {
			m.sel.dragging = false
			m.drag.ticking = false
			if m.viewportFollow {
				m.viewport.GotoBottom()
			}
			return m.finishSelection(), true
		}
		m.sel = nil
		// A rebuild can clear a selection mid-drag (its block vanished),
		// so this release can find a press with no selection and falls
		// through to the click path, toggling the row the drag started
		// on. Rare, and simpler than stranding the press.
		if cmd, handled := m.handleTranscriptClick(press); handled {
			return cmd, true
		}
		return m.handleNowBarClick(press)
	}
	return nil, false
}

// finishSelection copies the selection and remembers what was copied.
func (m *Model) finishSelection() tea.Cmd {
	from, to, ok := m.selRange()
	if !ok {
		m.clearSelection()
		return nil
	}
	m.sel.copied = m.selectedText(from, to)
	return m.copyText(m.sel.copied)
}

// handleWheel scrolls the transcript, or the bounded live region under the
// pointer. During a drag the selection's head follows the pointer to the
// text now under it.
func (m *Model) handleWheel(msg tea.MouseWheelMsg) tea.Cmd {
	// Vertical-only transcript: sideways trackpad pans and diagonal scrolls
	// must not shift the view horizontally.
	if msg.Button == tea.MouseWheelLeft || msg.Button == tea.MouseWheelRight {
		return nil
	}
	// A bounded live region under the cursor scrolls its own body instead of
	// the transcript. This must stay below the horizontal guard: above it, a
	// sideways pan would be consumed here and the vertical-only invariant
	// would be lost.
	if m.scrollLiveRegionAt(msg) {
		return nil
	}
	// Only delivered when tui.mouse_capture is on (see View). AltScreen
	// leaves no terminal scrollback, so without this the wheel does nothing
	// at all.
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	switch msg.Button {
	case tea.MouseWheelUp:
		m.viewportFollow = false
	case tea.MouseWheelDown:
		if m.viewport.AtBottom() {
			m.viewportFollow = true
		}
	}
	if m.dragging() {
		if h, ok := m.anchorAt(m.contentPosAt(msg.X, msg.Y)); ok {
			m.sel.head = h
		}
	}
	return vpCmd
}

// selScrollTick arms the next edge auto-scroll step for the current drag.
func (m *Model) selScrollTick() tea.Cmd {
	gen := m.drag.gen
	return tea.Tick(selScrollInterval, func(time.Time) tea.Msg { return selScrollTickMsg{gen: gen} })
}

// handleSelScrollTick scrolls one row toward the edge the pointer is held
// past and extends the selection, then re-arms while the pointer stays
// there.
func (m *Model) handleSelScrollTick(msg selScrollTickMsg) tea.Cmd {
	if msg.gen != m.drag.gen || !m.dragging() {
		return nil
	}
	dir := m.edgeDir(m.drag.pointer.Y)
	switch {
	case dir < 0 && !m.viewport.AtTop():
		m.viewport.ScrollUp(1)
		m.viewportFollow = false
	case dir > 0 && !m.viewport.AtBottom():
		m.viewport.ScrollDown(1)
		m.viewportFollow = false
	default:
		m.drag.ticking = false
		return nil
	}
	if h, ok := m.anchorAt(m.contentPosAt(m.drag.pointer.X, m.drag.pointer.Y)); ok {
		m.sel.head = h
	}
	return m.selScrollTick()
}
