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
	subagent     *session.SubagentView
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
	top := m.scrollHintRows()
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

// handleTranscriptClick toggles the transcript block under a left click, if
// any. handled reports whether the click landed on a region (regardless of
// whether that region was already at its target state — a click always
// consumes the event once it's inside the viewport bounds, matching the
// wheel-scroll handling right above it in Update).
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
		m.activeToolExpanded = !m.activeToolExpanded
	} else {
		m.toggleItemExpanded(target.key)
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()
	return nil, true
}
