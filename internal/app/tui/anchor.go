package tui

import (
	"marshal/internal/app/tui/conversation"
)

// blockSpan is the content-line range one rendered block occupies in the
// transcript viewport. It is recorded for every block, clickable or not.
type blockSpan struct {
	id        conversation.BlockID
	startLine int
	endLine   int
}

// renderedBlockSpan is one block's mapped rendering, placed in the transcript.
//
// It records BOTH coordinate systems for the same block, because the two are
// needed by different questions and neither can be derived from the other:
//
//   - blockRow is the block's first DISPLAY row index in the transcript, which
//     is what a pointer event's row is compared against. blockSpans measures the
//     same block in transcript lines for scrolling, and a block that renders to
//     three lines holds three entries there — so the two agree only by accident.
//   - rendered carries the block's own rows and their logical ranges, which is
//     what turns a cell inside the block into an offset in the block's text.
//
// Keeping them together is what makes a click resolve in one step: find the
// block by row, then ask that block where the cell is. Splitting them across two
// slices keyed differently is how an offset ends up computed against the wrong
// block's text.
type renderedBlockSpan struct {
	id       conversation.BlockID
	blockRow int
	rows     int
	rendered conversation.RenderedBlock
}

// renderedBlockAt returns the block covering a transcript display row.
func (m Model) renderedBlockAt(row int) (renderedBlockSpan, bool) {
	for _, s := range m.blockRenderSpans {
		if row >= s.blockRow && row < s.blockRow+s.rows {
			return s, true
		}
	}
	return renderedBlockSpan{}, false
}

// OffsetAtTranscriptCell turns a transcript (row, cell) into a logical offset in
// the block at that row.
//
// It reports false when no rendered block covers the row: the transcript also
// renders chrome (the welcome banner, a turn rule, a subagent card) that is not
// a mapped block, and a call there must decline rather than guess an offset in
// some other block's text.
func (m Model) OffsetAtTranscriptCell(row, cell int) (conversation.BlockID, int, bool) {
	s, ok := m.renderedBlockAt(row)
	if !ok {
		return "", 0, false
	}
	return s.id, s.rendered.OffsetAt(row-s.blockRow, cell), true
}

// captureReadingAnchor records where the reader is, before the transcript is
// rebuilt.
//
// It is a no-op while following: a following reader is pinned to the bottom by
// definition, holds no position worth preserving, and capturing one would make
// the restore path fight the pin.
//
// The anchor is (block identity, offset inside the block, ordinal position).
// It is deliberately NOT the viewport's absolute row offset: a reflow moves
// every row after the change — widening the terminal, or new output arriving
// above — so a row number silently points at different content. Identity
// survives all of that; the ordinal is carried only so that a block which
// genuinely vanishes can still be resolved to a nearby surviving position.
func (m *Model) captureReadingAnchor() {
	if m.viewportFollow {
		m.readingAnchor = conversation.Anchor{}
		return
	}
	block, offset := m.blockAtViewportTop()
	if block == "" {
		// Nothing identifiable at the top of the window (an empty transcript,
		// or the welcome banner). Keep whatever anchor we had: dropping it
		// would lose the reader's place on the next restore.
		return
	}
	m.readingAnchor = conversation.Anchor{
		Block:  block,
		Offset: offset,
		Index:  m.blockIndex(block),
	}
}

// restoreReadingAnchor re-scrolls the viewport to the reader's block after a
// rebuild.
//
// It is a no-op while following, and when there is no anchor to restore. When
// the anchored block is gone, the resolution reports Approximate and names the
// nearest surviving position; the reader is placed there rather than at the
// top or bottom, both of which are further from what they were looking at.
func (m *Model) restoreReadingAnchor() {
	if m.viewportFollow || m.readingAnchor.Block == "" {
		return
	}
	doc := m.conversationDocument()
	res := m.readingAnchor.Resolve(doc)
	if res.Empty || res.Anchor.Block == "" {
		return
	}
	offset, ok := m.blockStartRow(res.Anchor.Block)
	if !ok {
		return
	}
	target := offset + res.Anchor.Offset
	m.viewport.SetYOffset(clampOffset(target, m.viewport.Height(), m.viewport.TotalLineCount()))
	// Record where the reader actually ended up, so the next rebuild resolves
	// from a resolved position rather than from a stale one accumulating
	// error across many reflows.
	m.readingAnchor = conversation.Anchor{
		Block:  res.Anchor.Block,
		Offset: res.Anchor.Offset,
		Index:  res.Anchor.Index,
	}
}

// clampOffset keeps a scroll target inside the scrollable range.
func clampOffset(target, height, total int) int {
	if target < 0 {
		return 0
	}
	if maxOffset := total - height; target > maxOffset {
		return max(0, maxOffset)
	}
	return target
}

// blockAtViewportTop returns the identity of the block whose rendered rows
// cover the top of the viewport, and the row offset inside it.
//
// "Top of the viewport" rather than "the selected block" on purpose: there is
// no selection yet (that arrives with the selection work), and the top row is
// what a reader's eye is on when they scroll.
//
// It reads blockSpans rather than clickRegions because anchoring must work for
// every block, including the ones that are not clickable (a plain message, a
// run event). A click region exists only where a click does something.
func (m Model) blockAtViewportTop() (conversation.BlockID, int) {
	line := m.viewport.YOffset()
	for _, s := range m.blockSpans {
		if s.startLine <= line && line < s.endLine {
			return s.id, line - s.startLine
		}
	}
	return "", 0
}

// blockIndex returns a block's ordinal position among the document's blocks,
// or 0 when it is not present.
func (m Model) blockIndex(id conversation.BlockID) int {
	if idx, ok := m.conversationDocument().IndexOf(id); ok {
		return idx
	}
	return 0
}

// blockStartRow returns the rendered row a block starts on.
func (m Model) blockStartRow(id conversation.BlockID) (int, bool) {
	for _, s := range m.blockSpans {
		if s.id == id {
			return s.startLine, true
		}
	}
	return 0, false
}

// saveDrillAnchor stashes the current reading position and follow state before
// the transcript is replaced by a child's, and pushes them onto the per-level
// stack.
func (m *Model) saveDrillAnchor() {
	m.viewStackAnchors = append(m.viewStackAnchors, m.readingAnchor)
	// anchorFollow holds the PARENT's follow flag, which the child's view
	// temporarily overrides. It is a single field rather than a stack because
	// drill depth is ≤1 today (nested agent.run is forbidden) — the stack of
	// anchors below is kept general anyway so depth could grow.
	m.anchorFollow = m.viewportFollow
}

// restoreDrillAnchor pops the saved reading position and follow state for the
// level being returned to.
func (m *Model) restoreDrillAnchor() {
	if n := len(m.viewStackAnchors); n > 0 {
		m.readingAnchor = m.viewStackAnchors[n-1]
		m.viewStackAnchors = m.viewStackAnchors[:n-1]
	}
	m.viewportFollow = m.anchorFollow
}
