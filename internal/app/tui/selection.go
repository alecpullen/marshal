package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/viewmodel"
)

// blockKey names a transcript block across refreshes: the node it renders,
// or a name for a block that renders no node (the banner, a turn
// separator, the notice, the queued messages, the reconnect line).
type blockKey struct {
	node viewmodel.NodeID
	name string
}

// blockSpan is one block's half-open range of content lines.
type blockSpan struct {
	key        blockKey
	start, end int
}

// selAnchor is a selection endpoint pinned to content rather than to the
// screen: a block, a row inside it and a display column. Rows added or
// removed in other blocks leave it on the same text.
type selAnchor struct {
	block blockKey
	row   int
	col   int
}

// selection is the transcript text selection.
type selection struct {
	anchor, head selAnchor
	// dragging is true from the first drag motion to the release.
	dragging bool
	// copied is the text put on the clipboard at release. A refresh that
	// changes the covered text clears the selection.
	copied string
}

// dragState is the pointer state of a drag in progress.
type dragState struct {
	// pointer is the last pointer position, read by the auto-scroll tick.
	pointer tea.Mouse
	// gen identifies the drag; a tick from an earlier drag is ignored.
	gen int
	// ticking is true while an auto-scroll tick is armed.
	ticking bool
}

// selectionStyle paints selected text. Reverse video needs no palette slot,
// reads at every colour tier and under NO_COLOR, and stays distinct from the
// browse cursor's BGSelection band.
var selectionStyle = lipgloss.NewStyle().Reverse(true)

// selectionEnabled reports whether the app owns the mouse. When the
// terminal does (Ctrl+S, tui.mouse_capture = false) there is no app
// selection.
func (m *Model) selectionEnabled() bool {
	return !m.mouseReleased && m.state != nil && m.state.Config.TUI.MouseCapture
}

// dragging reports whether a selection drag is in progress.
func (m *Model) dragging() bool { return m.sel != nil && m.sel.dragging }

// clearSelection drops the selection and any pending press.
func (m *Model) clearSelection() {
	m.sel = nil
	m.press = nil
	m.drag.ticking = false
}

// spanIndexAt returns the index of the last block starting at or before
// line, or -1.
func (m *Model) spanIndexAt(line int) int {
	i := sort.Search(len(m.blockSpans), func(i int) bool { return m.blockSpans[i].start > line })
	return i - 1
}

// anchorAt pins a content position to the block that holds it. A blank line
// between two blocks pins to the block before it, one row past its end.
func (m *Model) anchorAt(p selPos) (selAnchor, bool) {
	i := m.spanIndexAt(p.line)
	if i < 0 {
		return selAnchor{}, false
	}
	sp := m.blockSpans[i]
	return selAnchor{block: sp.key, row: p.line - sp.start, col: p.col}, true
}

// resolve turns an anchor back into a content position, clamped to its
// block and the blank line after it. ok is false when the block is gone.
func (m *Model) resolve(a selAnchor) (selPos, bool) {
	for _, sp := range m.blockSpans {
		if sp.key == a.block {
			return selPos{line: sp.start + min(a.row, sp.end-sp.start), col: a.col}, true
		}
	}
	return selPos{}, false
}

// selRange returns the selection's endpoints in reading order.
func (m *Model) selRange() (from, to selPos, ok bool) {
	if m.sel == nil {
		return selPos{}, selPos{}, false
	}
	a, ok1 := m.resolve(m.sel.anchor)
	h, ok2 := m.resolve(m.sel.head)
	if !ok1 || !ok2 {
		return selPos{}, selPos{}, false
	}
	if h.before(a) {
		return h, a, true
	}
	return a, h, true
}

// selRowAt describes content line l for the copier.
func (m *Model) selRowAt(l int) selRow {
	if l < 0 || l >= len(m.contentLines) {
		return selRow{segment: -1}
	}
	r := selRow{text: ansi.Strip(m.contentLines[l]), segment: -1}
	bi := m.spanIndexAt(l)
	if bi < 0 || l >= m.blockSpans[bi].end {
		return r // the blank line between blocks
	}
	sp := m.blockSpans[bi]
	r.segment = bi
	r.inStep = sp.key.node.Kind == viewmodel.KindStep
	// The narrowest region holding the line that is not the block's own
	// region is a sub-region (a tool row, a card, a thinking row).
	sub := -1
	for j, nr := range m.nodeRegions {
		if l < nr.startLine || l >= nr.endLine || nr.target.node == sp.key.node {
			continue
		}
		if sub < 0 || nr.endLine-nr.startLine <= m.nodeRegions[sub].endLine-m.nodeRegions[sub].startLine {
			sub = j
		}
	}
	if sub >= 0 {
		nr := m.nodeRegions[sub]
		r.segment = len(m.blockSpans) + sub
		if l == nr.startLine {
			r.metaHead = true
			r.full = m.fullToolHead(nr.target.node, r.text)
		}
		return r
	}
	if r.inStep {
		// The first glyph row of a step is its headline.
		for k := sp.start; k < l; k++ {
			if !spanOf(selRow{text: ansi.Strip(m.contentLines[k])}, m.viewport.Width()).bare {
				return r
			}
		}
		r.metaHead = !spanOf(r, m.viewport.Width()).bare
	}
	return r
}

// fullToolHead is the untruncated text of a collapsed tool row whose text
// ends in the truncation ellipsis, or "".
func (m *Model) fullToolHead(id viewmodel.NodeID, text string) string {
	if id.Kind != viewmodel.KindTool || !strings.HasSuffix(strings.TrimRight(text, " "), "…") {
		return ""
	}
	n := m.browseTree[id]
	if n == nil {
		return ""
	}
	switch {
	case len(n.Tools) == 1:
		state, _ := m.transcriptSource()
		return oneLine(toolRowHead(n.Tools[0], state.Workspace().ActiveRoot))
	case len(n.Tools) > 1:
		return toolGroupHead(n.Tools)
	}
	return ""
}

// selectedText is the cleaned text between two content positions.
func (m *Model) selectedText(from, to selPos) string {
	rows := make([]selRow, 0, to.line-from.line+1)
	for l := from.line; l <= to.line; l++ {
		rows = append(rows, m.selRowAt(l))
	}
	return selectionText(rows, selPos{0, from.col}, selPos{to.line - from.line, to.col}, m.viewport.Width())
}

// revalidateSelection runs after every content rebuild. During a drag the
// anchors simply re-resolve; after release a selection whose block is gone,
// or whose covered text is no longer what was copied, is cleared.
func (m *Model) revalidateSelection() {
	if m.sel == nil {
		return
	}
	if !m.selectionEnabled() {
		m.clearSelection()
		return
	}
	from, to, ok := m.selRange()
	if !ok {
		m.clearSelection()
		return
	}
	if !m.sel.dragging && m.selectedText(from, to) != m.sel.copied {
		m.clearSelection()
	}
}

// paintSelection highlights the selected cells of the viewport's visible
// rows. Decoration (gutters, rails, right-aligned meta) is never lit, so
// what is highlighted is what was copied.
func (m *Model) paintSelection(view string) string {
	from, to, ok := m.selRange()
	if !ok {
		return view
	}
	lines := strings.Split(view, "\n")
	top := m.viewport.YOffset()
	for i := range lines {
		l := top + i
		if l < from.line || l > to.line {
			continue
		}
		sp := spanOf(m.selRowAt(l), m.viewport.Width())
		lo, hi := sp.text, sp.end
		if l == from.line {
			lo = max(lo, from.col)
		}
		if l == to.line {
			hi = min(hi, to.col+1)
		}
		if lo < hi {
			lines[i] = lipgloss.StyleRanges(lines[i], lipgloss.NewRange(lo, hi, selectionStyle))
		}
	}
	return strings.Join(lines, "\n")
}
