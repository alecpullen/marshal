package conversation

import (
	"strings"
)

// This file owns what the reader has selected, in the coordinates that survive a
// reflow.
//
// A selection is two LOGICAL positions — a byte offset into one block's text —
// plus the identity of the block they are in. Deliberately not two rows and two
// cells: a row is a position in a LAYOUT, and a layout changes when the terminal
// is resized, when a diff is expanded, when streaming output arrives above. A
// selection built from rows would survive all of that only by accident, and the
// accident is silent — the highlight moves, the copy takes different bytes, and
// nothing reports it.
//
// Positions are created from cells (that is what a pointer event carries), and
// that conversion is where a position is snapped to a grapheme boundary. After
// that point every offset in this file is a boundary, so no code below has to
// consider the case.

// TextPosition identifies a point in one block's text.
type TextPosition struct {
	// Block is the identity of the block the position is in.
	Block BlockID
	// Offset is a byte offset into that block's LOGICAL text, always at a
	// grapheme boundary.
	Offset int
	// Row is the DISPLAY row the position was created from, for a caller that
	// needs to keep the reader's place. It is presentation input and is never
	// used to resolve an offset: that is what Offset is for.
	Row int
	// Cell is the display cell the position was created from, for the same
	// reason.
	Cell int
}

// PositionAt converts a display (row, cell) in a block to a logical position.
//
// A row outside the block clamps to the block's first or last row rather than
// failing: a drag that runs off the bottom is still a selection to the end, and
// a caller should not have to special-case that. The offset is snapped, so the
// returned position is always a place text can be cut.
func PositionAt(block RenderedBlock, row, cell int) TextPosition {
	if len(block.Rows) == 0 {
		return TextPosition{Block: block.BlockID}
	}
	if row < 0 {
		row = 0
	}
	if row > len(block.Rows)-1 {
		row = len(block.Rows) - 1
	}
	if cell < 0 {
		cell = 0
	}
	off := block.OffsetAt(row, cell)
	off = SnapToBoundary(block.Logical, off)
	return TextPosition{Block: block.BlockID, Offset: off, Row: row, Cell: cell}
}

// positionAtOffset converts a logical offset to a position, keeping the row it
// is displayed on.
//
// It exists for the keyboard path: an arrow key moves an OFFSET by a grapheme,
// and the result still needs to know which row it is on so a caller can scroll
// it into view.
//
// Unexported: the keyboard path is owned by the parent package through
// GraphemeLineUp/GraphemeLineDown, which do the row-keeping themselves.
func positionAtOffset(block RenderedBlock, off int) TextPosition {
	off = SnapToBoundary(block.Logical, off)
	if row, ok := block.RowForOffset(off); ok {
		cell, _ := block.CellAt(row, off)
		return TextPosition{Block: block.BlockID, Offset: off, Row: row, Cell: cell}
	}
	return TextPosition{Block: block.BlockID, Offset: off}
}

// Selection is what the reader has selected: an anchor (where the gesture
// began) and a focus (where it is now), both in one block.
//
// Anchor and focus are kept in gesture order rather than sorted, because the
// direction is information a caller may want — a future "extend by word from
// here" needs to know which end is moving. Text() sorts them for extraction.
type Selection struct {
	// Block is the identity of the block the selection is in. A selection is
	// scoped to ONE block: crossing from the conversation into the inspector
	// must not select the whole two-column screen, which is why this is an
	// identity and not a pair of screen coordinates.
	Block BlockID
	// Revision is the block's revision when the selection was made. A caller
	// uses it to detect that the text changed underneath the selection, which
	// is what makes streaming output unable to alter bytes already selected.
	Revision int
	// Anchor is where the gesture started.
	Anchor int
	// Focus is where it is now.
	Focus int
}

// Empty reports whether the selection covers no characters.
func (s Selection) Empty() bool {
	return s.Block == "" || s.Anchor == s.Focus
}

// MatchesRevision reports whether the selection is still valid for a block.
func (s Selection) MatchesRevision(block RenderedBlock) bool {
	return s.Revision == block.Revision
}

// bounds returns the selection's start and end, clamped to a text length.
func (s Selection) bounds(textLen int) (int, int) {
	from, to := s.Anchor, s.Focus
	if from > to {
		from, to = to, from
	}
	if from < 0 {
		from = 0
	}
	if to > textLen {
		to = textLen
	}
	if from > textLen {
		from = textLen
	}
	if to < from {
		to = from
	}
	return from, to
}

// Text extracts what the selection covers, and reports whether it could be
// resolved at all.
//
// It refuses a block other than the one the selection was made in. That is the
// point of carrying the identity: a caller that resolves a selection against the
// wrong block would otherwise receive that block's text, which is a plausible
// string and the wrong answer, and nothing would indicate it.
//
// The text is the block's LOGICAL text sliced between the bounds, so a soft wrap
// contributes its space (via the logical text, which never had a newline) and the
// author's own break contributes a newline. Neither is reconstructed here: the
// logical text IS the projection the reader read.
func (s Selection) Text(block RenderedBlock) (string, bool) {
	if s.Block == "" || s.Block != block.BlockID {
		return "", false
	}
	from, to := s.bounds(len(block.Logical))
	if from >= to {
		return "", true
	}
	return block.Logical[from:to], true
}

// SelectionAt returns the selection between two positions in one block,
// reporting false when they are in different blocks.
//
// Refusing to join two blocks is deliberate: the text between a point in one
// block and a point in another is not a range of any single string, and the
// alternative — concatenating across blocks — would silently splice unrelated
// documents together. A caller that wants that asks for it explicitly.
func SelectionAt(anchor, focus TextPosition, revision int) (Selection, bool) {
	if anchor.Block == "" || anchor.Block != focus.Block {
		return Selection{}, false
	}
	return Selection{
		Block:    anchor.Block,
		Revision: revision,
		Anchor:   anchor.Offset,
		Focus:    focus.Offset,
	}, true
}

// nextWordBoundary returns the offset where the word starting at off ends.
//
// A "word" is a run of non-space characters, so this is the whitespace rule
// rather than a linguistic one: it behaves the same for "foo(int)" as for
// "hello", which is what a reader extending a selection over code expects.
//
// Text with no spaces at all — CJK, a long identifier, a URL — still advances,
// because a word-wise key that does nothing looks broken. It advances one
// GRAPHEME in that case, which is the smallest step that is unambiguously
// progress.
//
// Unexported: no caller outside the package wires a word-wise key yet; the
// exported surface is what the parent package consumes.
func nextWordBoundary(text string, off int) int {
	off = SnapToBoundary(text, off)
	if off >= len(text) {
		return len(text)
	}
	i := off
	// Skip the current run of non-space characters.
	for i < len(text) && !isSpaceAt(text, i) {
		i = NextGrapheme(text, i)
	}
	if i > off {
		return i
	}
	// We began on a space: skip the spaces.
	for i < len(text) && isSpaceAt(text, i) {
		i = NextGrapheme(text, i)
	}
	if i > off {
		return i
	}
	// Neither branch advanced (a zero-width grapheme), so advance one anyway.
	return NextGrapheme(text, off)
}

// prevWordBoundary returns the offset where the word ending at off begins.
//
// Symmetric with nextWordBoundary, including the no-spaces case: it always moves
// back at least one grapheme, so the key cannot appear dead.
func prevWordBoundary(text string, off int) int {
	off = SnapToBoundary(text, off)
	if off <= 0 {
		return 0
	}
	i := PrevGrapheme(text, off)
	// Skip back over the spaces immediately before off.
	for i > 0 && isSpaceAt(text, i) {
		i = PrevGrapheme(text, i)
	}
	// Then back over the word.
	for i > 0 {
		prev := PrevGrapheme(text, i)
		if isSpaceAt(text, prev) {
			break
		}
		i = prev
	}
	return i
}

// isSpaceAt reports whether the grapheme at off is whitespace.
func isSpaceAt(text string, off int) bool {
	if off < 0 || off >= len(text) {
		return false
	}
	// The grapheme is compared, not the byte: a space in a multi-byte
	// encoding is still one grapheme, and comparing the byte is enough because
	// the only space this app wraps at is ASCII.
	return text[off] == ' ' || text[off] == '\t' || text[off] == '\n'
}

// lineBounds returns the offsets bounding the display row containing off, for a
// line-wise extension.
//
// The bounds are the row's own logical range, so a line selection includes the
// author's trailing whitespace on that line (it is inside the range) and stops
// before the wrap's consumed space (which is restored by the row separator when
// text is assembled, not by the range).
func lineBounds(block RenderedBlock, off int) (int, int, bool) {
	off = SnapToBoundary(block.Logical, off)
	row, ok := block.RowForOffset(off)
	if !ok {
		return 0, 0, false
	}
	r := block.Rows[row].Range
	if !r.HasText() {
		return 0, 0, false
	}
	return r.Start, r.End, true
}

// GraphemeLineUp returns the offset one display row above off, keeping the
// display column where it can. It is what an Up/Down arrow does to a caret.
//
// The column is kept rather than the byte offset because the point of pressing
// Up is to stay under the character you were on, and a row's offsets are not
// proportional to its columns.
func GraphemeLineUp(block RenderedBlock, off, _ int) int {
	start, _, ok := lineBounds(block, off)
	if !ok {
		return off
	}
	row, _ := block.RowForOffset(off)
	if row == 0 {
		return start
	}
	// Keep the cell, then snap: the character under the cursor is what the
	// reader is looking at.
	cell, found := block.CellAt(row, off)
	if !found {
		return start
	}
	return PositionAt(block, row-1, cell).Offset
}

// GraphemeLineDown is GraphemeLineUp's counterpart.
func GraphemeLineDown(block RenderedBlock, off, _ int) int {
	_, end, ok := lineBounds(block, off)
	if !ok {
		return off
	}
	row, _ := block.RowForOffset(off)
	if row >= len(block.Rows)-1 {
		return end
	}
	cell, found := block.CellAt(row, off)
	if !found {
		return end
	}
	return PositionAt(block, row+1, cell).Offset
}

// HighlightRange returns the display cells a logical range covers on one row, so
// a caller can style a selection without re-deriving the mapping.
//
// It reports false when the range does not touch this row at all, which is
// different from a zero-width highlight at cell 0: a caller that conflated them
// would draw a caret on every row of the transcript.
func (r RenderedBlock) HighlightRange(rowIndex int, from, to int) (int, int, bool) {
	if rowIndex < 0 || rowIndex >= len(r.Rows) {
		return 0, 0, false
	}
	row := r.Rows[rowIndex]
	if !row.Range.HasText() {
		return 0, 0, false
	}
	// Clip the range to this row.
	if from < row.Range.Start {
		from = row.Range.Start
	}
	if to > row.Range.End {
		to = row.Range.End
	}
	if to < from {
		return 0, 0, false
	}
	startCell, ok := r.CellAt(rowIndex, from)
	if !ok {
		return 0, 0, false
	}
	endCell, ok := r.CellAt(rowIndex, to)
	if !ok {
		// The end is past this row's text: highlight to the end of the row's
		// content, which is where the reader's eye ends.
		endCell = row.Cells
	}
	if endCell < startCell {
		startCell, endCell = endCell, startCell
	}
	return startCell, endCell, true
}

// selectedRows returns the rows a selection touches, so a caller can check that
// every block it plans to freeze is one the selection is actually in.
//
// Unexported: the freeze decision is made from the selection's own bounds; no
// caller currently consumes the row list.
func (s Selection) selectedRows(block RenderedBlock) []int {
	if s.Block != block.BlockID {
		return nil
	}
	from, to := s.bounds(len(block.Logical))
	var rows []int
	for i := range block.Rows {
		row := block.Rows[i]
		if !row.Range.HasText() {
			continue
		}
		if row.Range.End < from || row.Range.Start > to {
			continue
		}
		rows = append(rows, i)
	}
	return rows
}

// EndsAtSoftWrap reports whether an offset sits at the end of a row that the
// renderer wrapped, rather than at the end of a line the author wrote.
//
// It is what lets a copy action remove a trailing space that a reader cannot see
// without also removing the author's own trailing whitespace — which is content,
// and which the row's own range records.
func (r RenderedBlock) EndsAtSoftWrap(off int) bool {
	off = SnapToBoundary(r.Logical, off)
	for _, row := range r.Rows {
		if !row.Range.HasText() {
			continue
		}
		if off == row.Range.End && row.ConsumedSpace && !row.HardBreak {
			return true
		}
	}
	return false
}

// TrimSelectedSpace removes spaces a soft wrap consumed from the END of a
// selection, so dragging to the end of a wrapped line does not put an invisible
// trailing space on the clipboard.
//
// It is applied by a CALLER that has established the selection ends at a soft
// wrap (see EndsAtSoftWrap). It cannot decide that on its own: the author's own
// trailing whitespace is content and must survive, and only the row knows which
// is which. Offsets are NOT modified, so a subsequent extension still starts
// from where the reader actually dragged.
func TrimSelectedSpace(s string) string {
	return strings.TrimRight(s, " ")
}
