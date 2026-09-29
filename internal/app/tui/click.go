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
	// blockID, when set, is the DOCUMENT identity of the block this region
	// renders. It exists because a click key is not always a document identity:
	// a collapsed group's key carries its FIRST MEMBER's identity, since an
	// itemKey has no way to name a group, while the document names the group
	// "group:<member>". Deriving the rendered span's identity from the key
	// alone put the group in the table under the member's name, so every lookup
	// by the group's real identity missed.
	blockID conversation.BlockID
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

// handleAgentLaneClick opens the inspector's Agents tab when the lane's count
// row is clicked.
//
// The lane is often the only handle on running work: a child's transcript card
// can scroll far out of view while the parent keeps working. Before the
// consolidation each row drilled into one child; now the whole band opens the
// tab that lists them all, which reaches the same information and more — the
// per-child model, elapsed time, and the child's own transcript.
//
// EVERY row of the band is the target, including the separator above the count.
// A one-row band with a dead half would be a trap: a reader has no way to know
// which half responds, and clicking the rule is an unsurprising thing to do.
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
	if !m.openAgentLaneInspector() {
		// Nothing to inspect, or the terminal cannot show the panel. Consume
		// the click so it does not fall through to the transcript underneath:
		// a click on the band is a click on the band, and letting it reach the
		// transcript would act on a row the reader did not aim at.
		return nil, true
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()
	return nil, true
}

// scrollLiveRegionAt routes a wheel event to a bounded live region when the
// cursor is over one AND the region has somewhere to scroll, and reports whether
// it consumed the event.
//
// The gate is the scrollability, not the cursor position. Routing on position
// alone — the previous behaviour — is the implicit consumption path the plan
// removes: the wheel silently stopped scrolling the transcript whenever it
// happened to be over a card, so a reader whose pointer rested on a subagent
// card could not scroll the conversation at all, and nothing on screen said why.
//
// Now the region consumes the wheel only when it has somewhere to go and reports
// not-handled otherwise, so the transcript scrolls. A region's history is
// reached by OPENING it, where scrolling is explicit and carries a position cue.
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
	if !m.liveRegionCanScroll(target.key, msg.Button) {
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

// liveRegionCanScroll reports whether a live region has anywhere to scroll in
// the direction the wheel asked for.
//
// Scrolling back through a region's history is unbounded (a ring buffer holds
// it), and scrolling forward is bounded by the newest end: the offset counts how
// far back the region's body is scrolled, so zero means "at the newest end" and
// a wheel-down there has nothing to do.
func (m Model) liveRegionCanScroll(key itemKey, button tea.MouseButton) bool {
	cur := m.regionOffset[key]
	switch button {
	case tea.MouseWheelUp:
		// Back through history, which the region retains.
		return cur < maxRegionOffset
	case tea.MouseWheelDown:
		// Forward, toward the newest end.
		return cur > 0
	}
	return false
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

	// A press on a block's BODY begins a selection; a press on its HEADER (or
	// on a control like the copy chip) still performs its own action.
	//
	// The split is what makes dragging possible at all. Before it, a press on a
	// block toggled its expansion, so a reader trying to select a phrase in a
	// collapsed group opened the group instead — and a drag that began on body
	// text could not exist, because the press had already done something else.
	cell := m.columnForClick(msg.X)
	if m.pressBeginsSelection(line, cell) {
		if m.beginSelectionAt(line, cell) {
			m.lastTranscriptHash = 0
			m.refreshViewport()
			return nil, true
		}
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

// pressBeginsSelection reports whether a press at a transcript position should
// start a selection rather than perform the region's action.
//
// The rule is positive and narrow: the press must be inside a MAPPED block (so
// there is text to select) and must NOT be on one of the block's own controls.
// Everything else falls through to the region's action, so a click on a subagent
// card, the copy chip, or a collapsed tool group's header still does what it
// always did.
func (m *Model) pressBeginsSelection(row, cell int) bool {
	if _, _, ok := m.mappedBlockAt(row, cell); !ok {
		return false
	}
	// A control inside the block — the copy chip, a subagent card, the
	// active-tool row — keeps its own click meaning. A press there is aimed at
	// the control, not at the text behind it.
	if target, ok := m.regionAt(row); ok {
		if target.copySource != nil || target.subagent != nil || target.isActiveTool {
			return false
		}
	}
	// A press on a block's HEADER line opens or closes it. The header is the
	// block's first line and carries the disclosure affordance; treating the
	// whole block as selectable would make a collapsed group impossible to
	// open with the mouse.
	if m.onBlockHeader(row) {
		return false
	}
	return true
}

// onBlockHeader reports whether a transcript row is the first line of a mapped
// block, which is where its disclosure control lives.
func (m Model) onBlockHeader(row int) bool {
	for _, s := range m.blockRenderSpans {
		if row == s.blockRow {
			return true
		}
	}
	return false
}

// columnForClick converts a screen column to a column inside the transcript's
// viewport.
//
// The transcript rectangle's left edge is the viewport's column 0, so the cell a
// click names is its offset from that edge. Doing the arithmetic in one place is
// what keeps a click's cell and the mapping's cell the same number — and a
// negative column (a click on the frame's own border) clamps to 0 rather than
// indexing backwards.
func (m *Model) columnForClick(x int) int {
	f := m.frameRect()
	col := x - f.Transcript.X
	if col < 0 {
		col = 0
	}
	return col
}

// handleTranscriptMotion extends an in-progress selection.
//
// It reports handled only while a drag is active: motion with no button held is
// ordinary pointer travel, and consuming it would break every hover behaviour
// that comes later.
func (m *Model) handleTranscriptMotion(msg tea.MouseMotionMsg) (tea.Cmd, bool) {
	if !m.selection.dragging {
		return nil, false
	}
	line, ok := m.contentLineForClick(msg.X, msg.Y)
	if !ok {
		// The pointer left the transcript entirely. Keep the drag alive so
		// coming back continues it; the transcript is not the only rectangle on
		// screen, and a drag that passed over the status line should survive.
		return nil, true
	}
	if m.extendSelectionTo(line, m.columnForClick(msg.X)) {
		m.lastTranscriptHash = 0
		m.refreshViewport()
	}
	return nil, true
}

// handleTranscriptRelease ends a selection drag.
func (m *Model) handleTranscriptRelease(msg tea.MouseReleaseMsg) (tea.Cmd, bool) {
	if !m.selection.dragging {
		return nil, false
	}
	// Update to the release position first: a fast drag can deliver motion
	// events the runtime coalesces, and the release is the authoritative end.
	if line, ok := m.contentLineForClick(msg.X, msg.Y); ok {
		m.extendSelectionTo(line, m.columnForClick(msg.X))
	}
	m.endSelection()
	m.lastTranscriptHash = 0
	m.refreshViewport()
	return nil, true
}
