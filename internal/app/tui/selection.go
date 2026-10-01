package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/conversation"
	"marshal/internal/app/tui/theme"
)

// th is a short alias for the active theme, used only in this file's
// highlight helpers.
func th() theme.Theme { return theme.Current() }

// This file owns the reader's selection on the transcript surface: the state a
// pointer gesture builds, the text it yields, and how it interacts with the
// reading anchor and the copy action.
//
// The pure arithmetic lives in conversation/selection.go. What is here is the
// MODEL's half: which surface owns the selection, what a pointer event does to
// it, and what happens when the transcript is rebuilt underneath it.
//
// Two rules shape the whole file:
//
//   - A selection is a READING state, not a mode. Beginning one does not change
//     what keys do elsewhere, does not touch the agent, and does not survive
//     losing focus. A reader who drags over a phrase and then types has not
//     changed their session.
//   - An offset survives a reflow and a row does not, so nothing here stores a
//     row. Rows are converted to offsets on entry to a gesture and never
//     travelled with.

// surfaceSelection is the transcript's selection state.
type surfaceSelection struct {
	// sel is the selection itself, in logical coordinates.
	sel conversation.Selection
	// dragging is true while the primary button is held. A drag is a
	// CANDIDATE: it becomes a real selection only if it covers something when
	// released, which is what lets a stationary press focus a block without
	// leaving an empty selection behind.
	dragging bool
	// frozen holds the block's text as it was when the drag ENDED, for the
	// blocks the selection touches.
	//
	// This is what stops streaming output from changing bytes the reader has
	// already chosen. It is deliberately populated on RELEASE rather than on
	// every motion: freezing during the drag would pin a half-finished
	// selection to text that is still being written, and the reader would be
	// selecting words that never existed together.
	frozen map[conversation.BlockID]conversation.RenderedBlock
}

// hasSelection reports whether the reader has selected any TEXT.
//
// This is the question a copy action asks, so a zero-width selection reports
// false: a reader who pressed and released without moving has not selected a
// phrase, and `y` must not copy an empty string over their clipboard.
func (m Model) hasSelection() bool {
	return !m.selection.sel.Empty() && m.selection.sel.Block != ""
}

// selectionActive reports whether a selection GESTURE is in progress or has
// produced text.
//
// It is the question a renderer asks — "should I draw a selection?" — and it is
// wider than hasSelection on purpose: during a drag whose ends have not diverged
// yet there is nothing selected but the reader is unmistakably mid-gesture, and a
// renderer that showed nothing would make the first pixels of every drag appear
// to do nothing.
func (m Model) selectionActive() bool {
	return m.selection.dragging || m.hasSelection()
}

// selectionAnchorBlock returns the block a gesture is anchored to, whether or not
// it covers text yet.
func (m Model) selectionAnchorBlock() conversation.BlockID {
	return m.selection.sel.Block
}

// clearSelection drops the selection and its frozen copies.
//
// The frozen copies are dropped with it because they exist only to serve the
// selection: keeping them would hold a copy of a block's text alive for the rest
// of the session, which is exactly the unbounded snapshot the plan warns about.
func (m *Model) clearSelection() {
	m.selection.sel = conversation.Selection{}
	m.selection.dragging = false
	m.selection.frozen = nil
}

// beginSelectionAt starts a drag at a transcript display position.
//
// It reports whether the position was inside a mapped block. A press on chrome
// (the welcome banner, a turn rule) begins nothing, so the reader cannot select
// text that is not there.
func (m *Model) beginSelectionAt(row, cell int) bool {
	span, off, ok := m.mappedBlockAt(row, cell)
	if !ok {
		return false
	}
	m.selection.sel = conversation.Selection{
		Block:    span.id,
		Revision: span.rendered.Revision,
		Anchor:   off.Offset,
		Focus:    off.Offset,
	}
	m.selection.dragging = true
	m.selection.frozen = nil
	return true
}

// extendSelectionTo extends an in-progress drag to a transcript display position.
//
// It does NOTHING when no drag is in progress, so ordinary motion without a held
// button cannot create a selection. And it REFUSES to leave the anchor's block:
// the alternative is concatenating two unrelated documents into one clipboard
// payload, and the reader has no way to see that happened.
func (m *Model) extendSelectionTo(row, cell int) bool {
	if !m.selection.dragging {
		return false
	}
	span, off, ok := m.mappedBlockAt(row, cell)
	if !ok {
		// The pointer left the mapped area. Keep the focus where it was rather
		// than clearing it: dragging up out of the block and back in is one
		// gesture, and dropping the focus would end the selection the moment
		// the pointer crossed a turn separator.
		return false
	}
	if span.id != m.selection.sel.Block {
		// Outside the anchor's block. Extend to the block's NEAREST end, which
		// is the behaviour a reader expects from a drag that ran past the
		// bottom of the text: it selects to the end rather than stopping.
		return m.extendSelectionToBlockEdge(span, row)
	}
	m.selection.sel.Focus = off.Offset
	return true
}

// extendSelectionToBlockEdge extends the selection to the near or far end of the
// anchored block, depending on which side the pointer left it.
func (m *Model) extendSelectionToBlockEdge(span renderedBlockSpan, row int) bool {
	anchorSpan, ok := m.mappedBlockSpan(m.selection.sel.Block)
	if !ok {
		return false
	}
	if row < anchorSpan.blockRow {
		// Above the block: select to its start.
		m.selection.sel.Focus = 0
		return true
	}
	// Below it: select to its end.
	m.selection.sel.Focus = len(anchorSpan.rendered.Logical)
	return true
}

// endSelection finishes a drag.
//
// A drag that covered nothing is DISCARDED rather than kept as a zero-width
// selection: a stationary click is a focus gesture, and leaving an empty
// selection behind would make the next `y` copy nothing at all.
func (m *Model) endSelection() {
	if !m.selection.dragging {
		return
	}
	m.selection.dragging = false
	if m.selection.sel.Empty() {
		m.clearSelection()
		return
	}
	m.freezeSelection()
}

// freezeSelection pins the blocks the selection touches to the text they have
// now.
//
// It is called when a selection is COMPLETED (on release), not during the drag.
// The copy a reader asked for is then the text they saw, even if the answer was
// still streaming.
func (m *Model) freezeSelection() {
	if !m.hasSelection() {
		m.selection.frozen = nil
		return
	}
	span, ok := m.mappedBlockSpan(m.selection.sel.Block)
	if !ok {
		m.selection.frozen = nil
		return
	}
	m.selection.frozen = map[conversation.BlockID]conversation.RenderedBlock{
		span.id: span.rendered,
	}
}

// selectedBlock returns the block a selection resolves against.
//
// It prefers the FROZEN copy, which is what makes the bytes stable across a
// rebuild: the live mapping is replaced on every refresh, so resolving against it
// would return whatever the block says now.
//
// It answers for a GESTURE as well as a finished selection, so a caller can ask
// where a drag is anchored while it is still in progress — which is what the
// extend path needs to know it has left the anchor's block.
func (m Model) selectedBlock() (conversation.RenderedBlock, bool) {
	if m.selection.sel.Block == "" {
		return conversation.RenderedBlock{}, false
	}
	if frozen, ok := m.selection.frozen[m.selection.sel.Block]; ok {
		return frozen, true
	}
	if span, ok := m.mappedBlockSpan(m.selection.sel.Block); ok {
		return span.rendered, true
	}
	return conversation.RenderedBlock{}, false
}

// selectedText returns the text the selection covers.
func (m Model) selectedText() (string, bool) {
	block, ok := m.selectedBlock()
	if !ok {
		return "", false
	}
	return m.selection.sel.Text(block)
}

// copyableText returns the text a copy action should put on the clipboard.
//
// The SELECTION wins over the reading anchor: a reader who dragged over a phrase
// means that phrase, and handing them the whole block would make the careful
// gesture pointless. With no selection the anchor path is unchanged.
func (m Model) copyableText() (string, bool) {
	if m.hasSelection() {
		block, ok := m.selectedBlock()
		if !ok {
			return "", false
		}
		text, ok := m.selection.sel.Text(block)
		if !ok {
			return "", false
		}
		// A selection that ended at a soft wrap ends with the space the wrap
		// consumed, which is not visible on screen. Trimming it here — and only
		// here, where the block is known — is what keeps a pasted phrase from
		// carrying an invisible trailing space.
		if text != "" && block.EndsAtSoftWrap(selectionEnd(m.selection.sel, block)) {
			text = conversation.TrimSelectedSpace(text)
		}
		return text, text != ""
	}
	return m.ancestorCopyText()
}

// selectionEnd returns the selection's later offset, for the soft-wrap check.
func selectionEnd(sel conversation.Selection, block conversation.RenderedBlock) int {
	from, to := sel.Anchor, sel.Focus
	if from > to {
		from, to = to, from
	}
	if to > len(block.Logical) {
		to = len(block.Logical)
	}
	return to
}

// ancestorCopyText is the pre-existing copy path: the reading anchor's block.
//
// It is kept as its own function so the selection logic above cannot silently
// replace it: with no selection, `y` must behave exactly as it did before.
func (m Model) ancestorCopyText() (string, bool) {
	if m.readingAnchor.Block == "" {
		return "", false
	}
	span, ok := m.mappedBlockSpan(m.readingAnchor.Block)
	if !ok {
		return "", false
	}
	return span.rendered.Logical, span.rendered.Logical != ""
}

// copySelectionText puts the reader's selection on the clipboard.
//
// It copies the SELECTION rather than a block's copy TARGET, and the difference
// matters: a block's targets are its whole answer, its code, its path. A
// selection is a phrase inside the readable text, which has no target — so
// routing it through the target resolver would silently copy the entire answer
// instead of the words the reader dragged over.
//
// The payload goes through the same clipboard path as every other copy, so the
// backend, the fallback and the feedback are identical; only the text differs.
func (m *Model) copySelectionText() tea.Cmd {
	text, ok := m.copyableText()
	if !ok || text == "" {
		m.showToast(copyResolveFailure(conversation.SourceAnswer, nil))
		return nil
	}
	return m.beginCopy(conversation.CopyTarget{
		Source: conversation.SourceAnswer,
		Text:   text,
		Label:  selectionCopyLabel(m.selection.sel, text),
	})
}

// selectionCopyLabel states what a selection copy will contain, including how
// much of it there is.
//
// The count is the point: the reader made a precise gesture and the feedback
// should let them check it landed, so a payload of ninety characters reads
// differently from one of four. It counts RUNES rather than bytes, because a
// byte count would be wrong for exactly the multi-byte text this work is careful
// about elsewhere.
func selectionCopyLabel(sel conversation.Selection, text string) string {
	n := utf8.RuneCountInString(text)
	if n == 1 {
		return "Copy selection (1 character)"
	}
	return fmt.Sprintf("Copy selection (%d characters)", n)
}

// selectionStatus renders the selection indicator for the status line, so a
// reader can see that a selection exists without moving the pointer back to it.
//
// It reports "" when there is no selection or none of it resolves, which is what
// the status line uses to decide whether to show the segment at all.
func (m Model) selectionStatus() string {
	if !m.hasSelection() {
		return ""
	}
	text, ok := m.selectedText()
	if !ok {
		return ""
	}
	n := utf8.RuneCountInString(text)
	if n == 1 {
		return "1 selected"
	}
	return fmt.Sprintf("%d selected", n)
}

// selectionKey names a direction a keyboard selection extends in.
type selectionKey int

const (
	selectionKeyLeft selectionKey = iota
	selectionKeyRight
	selectionKeyUp
	selectionKeyDown
)

// beginSelectionAtReadingAnchor starts a keyboard selection at the reader's
// place.
//
// The anchor is the block covering the top of the viewport, which is where the
// reader's eye is. It is the keyboard's counterpart to a pointer press: the same
// state, reached without a mouse, so `v` then arrows then `y` selects text
// exactly as a drag does.
//
// Starting from the anchor's FIRST character rather than an arbitrary column is
// deliberate: a keyboard selection has to start somewhere, and the beginning of
// the block the reader is looking at is the only place that is obviously right.
func (m *Model) beginSelectionAtReadingAnchor() {
	block, _ := m.blockAtViewportTop()
	if block == "" {
		return
	}
	span, ok := m.mappedBlockSpan(block)
	if !ok {
		return
	}
	// Land on the block's first CHARACTER, not the first byte of its range: a
	// range can begin with chrome (an indent) or with a byte the reader cannot
	// see, and a caret placed there would look like nothing happened.
	start := 0
	for _, row := range span.rendered.Rows {
		if !row.Range.HasText() {
			continue
		}
		start = row.Range.Start
		break
	}
	start = conversation.SnapToBoundary(span.rendered.Logical, start)
	m.selection.sel = conversation.Selection{
		Block:    span.id,
		Revision: span.rendered.Revision,
		Anchor:   start,
		Focus:    start,
	}
	m.selection.dragging = true
	m.selection.frozen = nil
}

// extendSelectionByKey moves the selection's focus by one step in a direction.
//
// Left and right move by GRAPHEME, not by byte or rune: a step that stopped
// between a base character and its combining mark would be a position copy
// cannot name, and the reader would see the highlight refuse to move.
//
// Up and down move a ROW and keep the display column, which is what a reader
// expects from an arrow key — the character under the caret stays under it —
// and it is the reason the row/column conversion lives in the mapping rather
// than being re-derived here.
func (m *Model) extendSelectionByKey(dir selectionKey) {
	if m.selection.sel.Block == "" {
		return
	}
	span, ok := m.mappedBlockSpan(m.selection.sel.Block)
	if !ok {
		return
	}
	text := span.rendered.Logical
	focus := m.selection.sel.Focus
	switch dir {
	case selectionKeyLeft:
		focus = conversation.PrevGrapheme(text, focus)
	case selectionKeyRight:
		focus = conversation.NextGrapheme(text, focus)
	case selectionKeyUp:
		focus = conversation.GraphemeLineUp(span.rendered, focus, 0)
	case selectionKeyDown:
		focus = conversation.GraphemeLineDown(span.rendered, focus, 0)
	}
	m.selection.sel.Focus = conversation.SnapToBoundary(text, focus)
	// A keyboard selection is COMPLETE as soon as it covers something: unlike a
	// drag, there is no release to wait for, and the reader expects `y` to work
	// immediately. Freezing here is what makes that text stable while they keep
	// extending it — each step re-freezes the block at its current revision, so
	// the bytes track what is on screen.
	if !m.selection.sel.Empty() {
		m.freezeSelection()
	}
}

// mappedBlockAt resolves a transcript display position to a block and the
// position inside it.
//
// It is the ONE place a pointer event becomes a logical position, so it is the
// one place that has to convert the transcript's row into the block's own row.
// Every other function in this file works in offsets.
func (m Model) mappedBlockAt(row, cell int) (renderedBlockSpan, conversation.TextPosition, bool) {
	span, ok := m.renderedBlockAt(row)
	if !ok {
		return renderedBlockSpan{}, conversation.TextPosition{}, false
	}
	// The block's own row, which is what its mapping is expressed in. Getting
	// this wrong is how a click resolves one block's cell against another
	// block's text.
	//
	// The origin is bodyRow, not blockRow: the mapping's rows describe the BODY,
	// while blockRow is the block's first line — and a block with captured
	// reasoning or a salvage note has lines above the body that the mapping does
	// not cover. Subtracting blockRow put the summary line at body row 0 and
	// shifted every offset in the block by the number of leading lines.
	blockRow := row - span.bodyRow()
	pos := conversation.PositionAt(span.rendered, blockRow, cell-span.prefixCells)
	return span, pos, true
}

// mappedBlockSpan finds the placed mapping for a block.
func (m Model) mappedBlockSpan(id conversation.BlockID) (renderedBlockSpan, bool) {
	for _, s := range m.blockRenderSpans {
		if s.id == id {
			return s, true
		}
	}
	return renderedBlockSpan{}, false
}

// selectionHighlight returns the display cells a selection covers on a given
// transcript row, so the renderer can style it.
//
// It works in TRANSCRIPT rows, not block rows: the caller is a renderer that has
// a row of the assembled transcript. Reporting false for an uncovered row is what
// keeps a caret from being drawn on every line.
//
// It paints from the LIVE mapping, and it refuses when the live block's revision
// no longer matches the one the selection was made at.
//
// The frozen snapshot is the right source for COPY BYTES and the wrong source
// for the HIGHLIGHT, and using it for both was a real defect. The reason is that
// a highlight is a claim about the SCREEN: "these cells, here". The live mapping
// is what supplies the row placement, so a frozen block combined with a live
// `blockRow` mixes two geometries — and after any reflow (a resize, the todo
// panel opening, a dock row appearing) they describe different layouts. A
// selection made at width 80 and re-rendered at width 40 was measured tinting
// forty cells past the selected text, over adjacent text and chrome.
//
// The logical offsets themselves are revision-independent, so painting them
// through the live geometry is exact. What the revision check buys is the case
// where the live text has genuinely CHANGED: the selection's byte offsets then
// name different words, and the honest answer is to paint nothing rather than
// to tint whatever now sits at those offsets. The frozen copy is untouched, so
// the reader's `y` still yields exactly the bytes they dragged over.
func (m Model) selectionHighlight(row int) (startCell, endCell int, ok bool) {
	if !m.hasSelection() {
		return 0, 0, false
	}
	span, hasSpan := m.mappedBlockSpan(m.selection.sel.Block)
	if !hasSpan {
		return 0, 0, false
	}
	if !m.selection.sel.MatchesRevision(span.rendered) {
		// The block's content moved on. The selection still owns its bytes
		// (via the frozen copy) but it can no longer point at cells.
		return 0, 0, false
	}
	// bodyRow, not blockRow: the mapping's rows are the BODY's, and a block with
	// a reasoning summary or a salvage note above it has rows the mapping does
	// not cover.
	blockRow := row - span.bodyRow()
	if blockRow < 0 || blockRow >= len(span.rendered.Rows) {
		return 0, 0, false
	}
	from, to := m.selection.sel.Anchor, m.selection.sel.Focus
	if from > to {
		from, to = to, from
	}
	start, end, ok := span.rendered.HighlightRange(blockRow, from, to)
	if !ok {
		return 0, 0, false
	}
	return start + span.prefixCells, end + span.prefixCells, true
}

// highlightFindMatches paints the search's matches onto assembled transcript
// content.
//
// It runs AFTER the selection, so a match shows through on top of a selected
// region rather than being hidden by it: the reader is searching right now, and
// a highlight they cannot see is worse than one that only overrides the
// selection tint underneath it.
//
// It works on the CONTENT STRING for the same reason highlightSelection does.
// Every block is already rendered and joined, and re-rendering the whole
// transcript to add a background would double the work on every keystroke.
//
// A match in a block with no cell mapping is simply not painted: the search can
// still jump to it, because the jump uses block spans, which cover every block.
// That limit is real and is pinned by a test rather than left to be discovered.
func (m Model) highlightFindMatches(content string) string {
	lines := strings.Split(content, "\n")
	for row := range lines {
		spans := m.findMatchHighlights(row)
		if len(spans) == 0 {
			continue
		}
		// The non-current matches go on first and the current one last, so the
		// cursor's hit is never repainted by a neighbour that happens to
		// overlap it.
		for _, current := range []bool{false, true} {
			for i := len(spans) - 1; i >= 0; i-- {
				if spans[i].current != current {
					continue
				}
				lines[row] = highlightFindCells(lines[row], spans[i].start, spans[i].end, current)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// highlightFindCells wraps cells [start, end) of one rendered line in the match
// background.
//
// The line is ANSI-styled already, so the range is cut by VISIBLE CELLS rather
// than by bytes — ansi.Cut walks escape sequences without counting them, and
// slicing the raw string would land inside a colour code. The middle piece is
// restyled from its stripped form: the underlying foreground is kept from
// fighting the match background, and the CHARACTERS, which are what the reader
// is looking for, are unchanged.
//
// The two match kinds take DIFFERENT colours, and the current one is the one
// with more contrast against the body text. A reader needs to see both which
// lines matched and which hit the cursor is counting from; a single colour
// would leave them unable to tell, and the position in the results ("3/7") is
// not visible while they are reading the line.
func highlightFindCells(line string, start, end int, current bool) string {
	w := ansi.StringWidth(line)
	if start >= w {
		return line
	}
	if end > w {
		end = w
	}
	if end <= start {
		return line
	}
	bg, fg := th().BGFind, th().FGDefault
	if current {
		bg, fg = th().BGFindCurrent, th().FGEmphasis
	}
	before := ansi.Cut(line, 0, start)
	mid := ansi.Cut(line, start, end)
	after := ansi.Cut(line, end, w)
	if theme.IsMonochrome() {
		// Under NO_COLOR every slot is NoColor, so a background-only mark emits
		// no SGR at all and the match is invisible — the reader is told there are
		// results and can see none of them. Inversion is the one non-colour cue
		// available, and it distinguishes the current hit from the others by
		// making it BOLD as well.
		return before + matchMarkFallbackStyle(current).Render(ansi.Strip(mid)) + after
	}
	style := lipgloss.NewStyle().Foreground(fg).Background(bg)
	return before + style.Render(ansi.Strip(mid)) + after
}

// matchMarkFallbackStyle is the monochrome replacement for a match background.
//
// Reverse video is used rather than an underline because a find mark covers a
// PHRASE, and an underline on every match in a long line reads as decoration
// rather than as "this span matched". Reverse is unmistakable and, like every
// other cue in the interface, survives having all colour removed.
func matchMarkFallbackStyle(current bool) lipgloss.Style {
	return lipgloss.NewStyle().Reverse(true).Bold(current)
}

// highlightSelection paints the selection onto assembled transcript content.
//
// It works on the CONTENT STRING rather than on the renderer, because by this
// point every block has already been rendered and joined: re-rendering the whole
// transcript to add a background colour would double the work on every drag
// motion, and a drag emits motion events at pointer rate.
//
// The styling is applied per LINE, and only to the cells the mapping says the
// selection covers, so the highlight and the offsets agree by construction —
// both come from the same HighlightRange.
func (m Model) highlightSelection(content string) string {
	lines := strings.Split(content, "\n")
	for row := range lines {
		start, end, ok := m.selectionHighlight(row)
		if !ok || end <= start {
			continue
		}
		lines[row] = highlightCells(lines[row], start, end)
	}
	return strings.Join(lines, "\n")
}

// highlightCells wraps cells [start, end) of one rendered line in the selection
// background.
//
// The line is ANSI-styled already, so the range is cut by VISIBLE CELLS rather
// than by bytes: ansi.Cut walks escape sequences without counting them, and
// slicing the raw string would land in the middle of a colour code. The three
// pieces are then rejoined with the middle one restyled.
func highlightCells(line string, start, end int) string {
	w := ansi.StringWidth(line)
	if start >= w {
		return line
	}
	if end > w {
		end = w
	}
	if end <= start {
		return line
	}
	before := ansi.Cut(line, 0, start)
	mid := ansi.Cut(line, start, end)
	after := ansi.Cut(line, end, w)
	// The selection's style is applied to the STRIPPED middle, so the
	// background the reader sees is the selection's rather than a fight
	// between it and the colours underneath. The text itself is unchanged:
	// stripping removes escapes, not characters.
	if theme.IsMonochrome() {
		// A background-only tint emits nothing under NO_COLOR, which would leave
		// a reader who just dragged over a phrase with no evidence that anything
		// happened — and `y` about to copy text they cannot see selected.
		// Reverse video is the non-colour equivalent, and it is used for the
		// selection as well as the find marks so the two read as one family.
		return before + matchMarkFallbackStyle(false).Render(ansi.Strip(mid)) + after
	}
	style := lipgloss.NewStyle().
		Foreground(th().FGEmphasis).
		Background(th().BGSelection)
	return before + style.Render(ansi.Strip(mid)) + after
}

// selectionIsLive reports whether the selection should still be drawn for a
// block, which is the same question as "does the block still exist".
//
// A selection whose block vanished (a branch rewind, ClearRunEvents) is dropped
// rather than drawn against a neighbour: a highlight on the wrong block is worse
// than none, because it looks like it worked.
func (m *Model) selectionIsLive() bool {
	if !m.hasSelection() {
		return false
	}
	_, ok := m.mappedBlockSpan(m.selection.sel.Block)
	return ok
}
