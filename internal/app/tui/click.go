package tui

import (
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
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
// click landed outside the viewport. The transcript viewport is always the
// top-left element of the screen (see viewString in view.go): row
// scrollHintRows() (0 or 1, for the "↑ scrolled" hint) through
// scrollHintRows()+viewport.Height(), column 0 through leftWidth.
func (m *Model) contentLineForClick(x, y int) (int, bool) {
	if x < 0 || x >= m.leftWidth {
		return 0, false
	}
	top := m.scrollHintRows() + m.breadcrumbRows()
	height := m.viewport.Height()
	if y < top || y >= top+height {
		return 0, false
	}
	return m.viewport.YOffset() + (y - top), true
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

// nowBarBand returns the half-open screen-row range [top, bottom) the now
// bar occupies, or false when it isn't rendered. The frame order
// (view.go) is: scroll hint, breadcrumb, transcript viewport, now bar.
//
// This math is coupled to viewString()'s layout: the hint and breadcrumb
// rows are only emitted when their *Rows() helpers return nonzero. The
// viewport height is used raw here while the render path guards it with
// max(height, 1); the viewport is always >= 1, so the two agree.
func (m *Model) nowBarBand(plan nowBarPlan) (top, bottom int, ok bool) {
	if len(plan.rows) == 0 || m.dock.FullFrameOpen() {
		return 0, 0, false
	}
	top = m.scrollHintRows() + m.breadcrumbRows() + m.viewport.Height()
	return top, top + len(plan.rows), true
}

// handleNowBarClick drills into the subagent whose now-bar row was clicked.
// The bar is often the only handle on a running child: its transcript card
// can scroll far out of view while the parent keeps working. Clicks on other
// bar rows are consumed so they do not fall through to the transcript.
func (m *Model) handleNowBarClick(msg tea.MouseClickMsg) (tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft {
		return nil, false
	}
	if msg.X < 0 || msg.X >= m.leftWidth {
		return nil, false
	}
	plan := m.nowBarPlan()
	top, bottom, ok := m.nowBarBand(plan)
	if !ok || msg.Y < top || msg.Y >= bottom {
		return nil, false
	}
	idx := msg.Y - top - plan.agentRowStart
	if idx < 0 || idx >= len(plan.agents) {
		return nil, true
	}
	m.drillIntoSubagent(plan.agents[idx])
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
	} else {
		m.toggleItemExpanded(target.key)
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()
	return nil, true
}
