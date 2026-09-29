package tui

import (
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
)

// clickTarget identifies what a click region toggles: either a keyed
// transcript item/group (see itemKey in expand.go), the singleton
// in-flight active-tool-call block, which has no stable key, or a
// subagent card that drills into the subagent's transcript.
type clickTarget struct {
	key          itemKey
	isActiveTool bool
	// toolKey identifies the in-flight tool call an active-tool click target
	// toggles, so the override is keyed by tool-call identity rather than a
	// single global flag.
	toolKey  activeToolKey
	subagent *session.SubagentView
	// isLiveRegion marks a block rendered by liveregion, whose body scrolls
	// independently of the transcript when the wheel is over it.
	isLiveRegion bool
	// copySource marks a click that COPIES rather than toggles, and names
	// which thing it copies. It is a pointer so "no copy" is distinguishable
	// from the answer source, whose zero value a plain bool could not tell
	// apart. A target with a copy source never toggles expansion: one click
	// must mean one thing.
	copySource *conversation.CopySource
}

// copyTarget resolves the target a copy click should put on the clipboard.
//
// It re-resolves against the block rather than capturing bytes at render time,
// so the chip always copies what the block says NOW — a render is not a
// promise about content, and a block whose text changed must not paste the
// text it used to have.
func (t clickTarget) copyTarget(block conversation.Block, source conversation.CopySource) (conversation.CopyTarget, bool) {
	return blockTarget(block, source)
}

// clickRegion is a half-open [startLine, endLine) range of content lines in
// the transcript viewport, in the same coordinate space as
// viewport.Model.YOffset() and viewport.Model.GetContent() split by "\n".
type clickRegion struct {
	startLine, endLine int
	target             clickTarget
}

// contentLineForClick converts screen coordinates from a tea.MouseClickMsg
// into a content-line index into the transcript viewport, or false if the
// click landed outside the viewport.
//
// The transcript's screen rectangle comes from the measured frame (see
// computeFrame in frame.go), so the row a click resolves to is the row the
// renderer actually drew — including the SDD top bar, the scroll hint, and
// the drill-down breadcrumb, all of which shift the content down.
func (m *Model) contentLineForClick(x, y int) (int, bool) {
	f := m.frameRect()
	if !f.Transcript.Contains(x, y) {
		return 0, false
	}
	// The hint and breadcrumb rows are inside the transcript rectangle but
	// above the viewport's content, so they are not content lines.
	chromeRows := m.scrollHintRows() + m.breadcrumbRows()
	row, ok := f.Transcript.Row(y)
	if !ok || row < chromeRows {
		return 0, false
	}
	line := m.viewport.YOffset() + (row - chromeRows)
	if line >= m.viewport.YOffset()+m.viewport.Height() {
		return 0, false
	}
	return line, true
}

// regionAt returns the click target whose range contains line, if any.
// m.clickRegions is small (tens of entries, one per visible transcript
// block) so a linear scan is fine.
func (m *Model) regionAt(line int) (clickTarget, bool) {
	for _, r := range m.clickRegions {
		if line >= r.startLine && line < r.endLine {
			return r.target, true
		}
	}
	return clickTarget{}, false
}

// todoPanelBand returns the half-open screen-row range [top, bottom) the
// pinned todo panel occupies, or false when it isn't rendered. The panel
// sits directly below the transcript frame (scroll hint + breadcrumb +
// viewport) and the turn-spinner row (view.go:95-107).
//
// This math is coupled to viewString()'s layout invariants: the spinner and
// breadcrumb rows are only emitted when their *Rows() helpers return
// nonzero, and each helper returns 0 when that element isn't rendered. The
// viewport height here is used raw (m.viewport.Height()) while the render
// path guards it with max(height, 1); in practice the viewport is always
// >= 1, so the two agree, but keep them in sync if the render guard ever
// changes. The band also depends on todoPanelRows() matching the panel's
// rendered height.
func (m *Model) todoPanelBand() (top, bottom int, ok bool) {
	rows := m.todoPanelRows()
	if rows == 0 {
		return 0, 0, false
	}
	// The activity band stacks spinner, todos, live strip, and lane in that
	// order, so the todo panel starts one spinner row below the top of the
	// band.
	band := m.frameRect().Activity
	if band.Empty() {
		return 0, 0, false
	}
	top = band.Y + m.turnSpinnerRows()
	return top, top + rows, true
}

// handleTodoPanelClick toggles the pinned todo panel between expanded and
// collapsed when a left click lands in its row band. It deliberately does
// NOT cycle into the hidden state — a click should never make the panel
// vanish. Ctrl+T still cycles through all three states (expanded →
// collapsed → hidden).
func (m *Model) handleTodoPanelClick(msg tea.MouseClickMsg) (tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft {
		return nil, false
	}
	if msg.X < 0 || msg.X >= m.leftWidth {
		return nil, false
	}
	top, bottom, ok := m.todoPanelBand()
	if !ok || msg.Y < top || msg.Y >= bottom {
		return nil, false
	}
	m.toggleTodoPanelMode()
	m.lastTranscriptHash = 0
	m.refreshViewport()
	return nil, true
}

// agentLaneBand returns the half-open screen-row range the consolidated
// lane occupies. The frame order (view.go) is: transcript frame, turn
// spinner, todo panel, live strip, consolidated lane — so the lane sits
// directly below the live strip.
func (m *Model) agentLaneBand() (top, bottom int, ok bool) {
	rows := m.laneRows()
	if rows == 0 {
		return 0, 0, false
	}
	// The lane is the last element of the activity band.
	band := m.frameRect().Activity
	if band.Empty() {
		return 0, 0, false
	}
	top = band.Bottom() - rows
	return top, top + rows, true
}

// handleAgentLaneClick drills into the subagent whose row was clicked.
// The lane is often the only handle on a running child: its transcript card
// can scroll far out of view while the parent keeps working.
func (m *Model) handleAgentLaneClick(msg tea.MouseClickMsg) (tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft {
		return nil, false
	}
	if msg.X < 0 || msg.X >= m.leftWidth {
		return nil, false
	}
	top, bottom, ok := m.agentLaneBand()
	if !ok || msg.Y < top || msg.Y >= bottom {
		return nil, false
	}
	// Row 0 is the separator rule, row 1 the caption; agents start at row 2.
	const chromeRows = 2
	idx := msg.Y - top - chromeRows
	entries := m.agentLaneEntries()
	if idx < 0 || idx >= len(entries) {
		// The header line or the overflow row. Consume the click
		// so it does not fall through to the transcript underneath.
		return nil, true
	}
	m.drillIntoSubagent(entries[idx])
	m.lastTranscriptHash = 0
	m.refreshViewport()
	return nil, true
}

// scrollLiveRegionAt routes a wheel event to a bounded live region when the
// cursor is over one, and reports whether it consumed the event.
//
// It returns true even when the region is already at the end of its travel:
// the alternative is that scrolling past a region's top silently starts
// scrolling the transcript underneath it, which reads as the region
// "jumping" out from under the cursor.
func (m *Model) scrollLiveRegionAt(msg tea.MouseWheelMsg) bool {
	line, ok := m.contentLineForClick(msg.X, msg.Y)
	if !ok {
		return false
	}
	target, ok := m.regionAt(line)
	if !ok || !target.isLiveRegion {
		return false
	}
	var delta int
	switch msg.Button {
	case tea.MouseWheelUp:
		delta = 1 // scroll back through the region's history
	case tea.MouseWheelDown:
		delta = -1
	default:
		return false
	}
	if m.regionOffset == nil {
		m.regionOffset = map[itemKey]int{}
	}
	cur := m.regionOffset[target.key]
	next := min(max(cur+delta, 0), maxRegionOffset)
	if next != cur {
		m.regionOffset[target.key] = next
		// Belt and braces alongside the transcriptHash change in Step 6:
		// force the rebuild so the scroll is felt on this very event rather
		// than on the next tick.
		m.lastTranscriptHash = 0
		m.refreshViewport()
	}
	return true
}

// handleTranscriptClick toggles the expand state of the transcript block
// under a left click, if any. handled reports whether the click landed on a
// region (regardless of whether that region was already at its target state
// — a click always consumes the event once it's inside the viewport bounds,
// matching the wheel-scroll handling right above it in Update).
func (m *Model) handleTranscriptClick(msg tea.MouseClickMsg) (tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft {
		return nil, false
	}
	line, ok := m.contentLineForClick(msg.X, msg.Y)
	if !ok {
		return nil, false
	}
	target, ok := m.regionAt(line)
	if !ok {
		return nil, false
	}
	if target.subagent != nil {
		m.drillIntoSubagent(*target.subagent)
	} else if target.isActiveTool {
		m.toggleActiveToolExpanded(target.toolKey)
	} else if target.copySource != nil {
		// A copy click copies and does NOT toggle. Routing it through the
		// toggle path is how one click would both copy and change what is on
		// screen, and the user would have no way to tell which happened.
		//
		// The click is NOT followed by refreshViewport: a copy changes no
		// transcript content, and a rebuild here would drop the reader's
		// anchor for no reason.
		return m.copySelection(*target.copySource), true
	} else {
		m.toggleItemExpanded(target.key)
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()
	return nil, true
}
