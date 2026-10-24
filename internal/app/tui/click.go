package tui

import (
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/livebox"
	"marshal/internal/viewmodel"
)

// clickTarget identifies what a click region toggles: a node of the
// transcript tree (a step, a tool row, a completion event, a thinking row).
// The subagent field is retained for the drill-in branch below, but no region
// sets it any more: drill-in starts from a completion event or a dispatch row,
// not from an in-place card.
type clickTarget struct {
	// node is what the click toggles (or, for a live region, scrolls).
	node     viewmodel.NodeID
	subagent *session.SubagentView
	// isLiveRegion marks a block rendered by liveregion, whose body scrolls
	// independently of the transcript when the wheel is over it.
	isLiveRegion bool
}

// nodeRegion is a half-open [startLine, endLine) range of content lines in
// the transcript viewport, in the same coordinate space as
// viewport.Model.YOffset() and viewport.Model.GetContent() split by "\n".
// A step's rows record their own ranges inside the step's, and the narrowest
// range under a click wins.
type nodeRegion struct {
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
//
// The narrowest range wins, so a row inside a block beats the block itself.
// On a tie the later range wins: a block's sub-regions are recorded after it,
// and a headless beat (no header line) has a block range identical to its
// single row's, where the row is the more specific target.
func (m *Model) regionAt(line int) (clickTarget, bool) {
	best := -1
	for i, r := range m.nodeRegions {
		if line < r.startLine || line >= r.endLine {
			continue
		}
		if best < 0 || r.endLine-r.startLine <= m.nodeRegions[best].endLine-m.nodeRegions[best].startLine {
			best = i
		}
	}
	if best < 0 {
		return clickTarget{}, false
	}
	return m.nodeRegions[best].target, true
}

// scrollLiveRegionAt routes a wheel event to a bounded live region when the
// cursor is over one, and reports whether it consumed the event.
//
// It returns true even when the region is already at the end of its travel:
// the alternative is that scrolling past a region's top silently starts
// scrolling the transcript underneath it, which reads as the region
// "jumping" out from under the cursor.
func (m *Model) scrollLiveRegionAt(msg tea.MouseWheelMsg) bool {
	// The streaming reasoning draws in the box now, not in the viewport: a
	// wheel over its rows scrolls it exactly as a viewport region scrolls.
	if m.wheelOverBoxReasoning(msg.X, msg.Y) {
		var delta int
		switch msg.Button {
		case tea.MouseWheelUp:
			delta = 1 // scroll back through the region's history
		case tea.MouseWheelDown:
			delta = -1
		default:
			return false
		}
		m.setRegionOffset(viewmodel.LiveThinkingID, delta)
		m.invalidateTranscript()
		m.refreshViewport()
		return true
	}
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
	if m.setRegionOffset(target.node, delta) {
		// Belt and braces alongside the transcriptHash change in Step 6:
		// force the rebuild so the scroll is felt on this very event rather
		// than on the next tick.
		m.invalidateTranscript()
		m.refreshViewport()
	}
	return true
}

// setRegionOffset moves a bounded live region's scroll offset by delta, and
// reports whether it moved.
func (m *Model) setRegionOffset(id viewmodel.NodeID, delta int) bool {
	if m.regionOffset == nil {
		m.regionOffset = map[viewmodel.NodeID]int{}
	}
	cur := m.regionOffset[id]
	next := min(max(cur+delta, 0), maxRegionOffset)
	if next == cur {
		return false
	}
	m.regionOffset[id] = next
	return true
}

// wheelOverBoxReasoning reports whether the pointer sits on a box row that
// scrolls the streaming reasoning.
func (m *Model) wheelOverBoxReasoning(x, y int) bool {
	if m.dock.FullFrameOpen() || x < 0 || x >= m.leftWidth {
		return false
	}
	layout := m.boxLayout()
	if len(layout.Rows) == 0 {
		return false
	}
	top := m.boxTop()
	if y < top || y >= top+len(layout.Rows) {
		return false
	}
	t := layout.Rows[y-top].Target
	return t.Kind == livebox.TargetLive && t.Node == viewmodel.LiveThinkingID
}

// handleTranscriptClick toggles the expand state of the transcript block
// under a left click, if any. handled reports whether the click landed on a
// region (regardless of whether that region was already at its target state
// — a click always consumes the event once it's inside the viewport bounds,
// matching the wheel-scroll handling right above it in Update).
func (m *Model) handleTranscriptClick(msg tea.Mouse) (tea.Cmd, bool) {
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
	// In browse mode a click also moves the cursor to the clicked node. A
	// click never enters browse mode on its own.
	if m.browsing && navigable(target.node.Kind) {
		for _, it := range m.browseItems {
			if it.id == target.node {
				m.cursor = target.node
				break
			}
		}
	}
	switch {
	case target.subagent != nil:
		// Unreachable: no region sets subagent any more. Kept so a future
		// region cannot silently toggle a row that should drill in.
		m.drillIntoSubagent(*target.subagent)
	default:
		m.toggleExpanded(target.node)
		m.invalidateTranscript()
		m.refreshViewport()
	}
	return nil, true
}
