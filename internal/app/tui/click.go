package tui

// clickTarget identifies what a click region toggles: either a keyed
// transcript item/group (see itemKey in expand.go) or the singleton
// in-flight active-tool-call block, which has no stable key.
type clickTarget struct {
	key          itemKey
	isActiveTool bool
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
