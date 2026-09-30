package conversation

import (
	"github.com/charmbracelet/x/ansi"
)

// This file owns the one conversion the rest of the interaction work depends
// on: between a LOGICAL position in text (a byte offset, which is what a copy
// cuts and what a model produced) and a DISPLAY position (a cell column, which
// is what a click lands on and what a row is made of).
//
// The two are not proportional, and every bug in this area comes from assuming
// they are. One grapheme can be many bytes and two cells ("👩🏽‍💻" is 15 bytes
// and two cells); one byte can be half a grapheme (a combining mark's second
// byte) and is not a position text can be cut at; and a tab is one byte that
// the terminal renders as up to eight cells. So:
//
//   - offsets in and out of this file are always snapped to a grapheme
//     boundary, because a boundary is the only place a string can be cut;
//   - columns are always measured in cells through cellWidth, never counted in
//     runes or bytes;
//   - tabs are expanded for layout and preserved in the logical text, so a
//     copy of indented code gets the author's tab back.
//
// ansi.StringWidth is the terminal-agreement point: the rest of the TUI
// measures lines with it, so anything here that disagreed with it would produce
// rows the renderer believes fit and the terminal believes overflow.

// tabStop is the column interval a terminal advances "\t" to. It matches the
// transcript renderer's own constant; a mapping that used a different stop
// would place the cursor in a different column than the text it names.
const tabStop = 8

// cellWidth returns the number of display cells a string occupies, accounting
// for wide graphemes, combining marks and tab expansion.
//
// It differs from ansi.StringWidth in exactly one respect, deliberately: a tab
// is measured by where it lands rather than as a zero-width control character.
// Since a newline resets the column, the expansion is accumulated per line.
//
// Unexported: the rest of the TUI measures lines with ansi.StringWidth, and
// nothing outside this package consumes a second width function.
func cellWidth(s string) int {
	if s == "" {
		return 0
	}
	width := 0
	col := 0
	for _, seg := range splitLines(s) {
		col = 0
		rest := seg
		for rest != "" {
			if rest[0] == '\t' {
				pad := tabStop - col%tabStop
				width += pad
				col += pad
				rest = rest[1:]
				continue
			}
			cluster, w := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
			width += w
			col += w
			rest = rest[len(cluster):]
		}
	}
	return width
}

// splitLines splits on "\n", keeping a trailing newline's effect (an empty
// final segment) because a newline resets the column even at the end of input.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// SnapToBoundary moves an offset to the start of the grapheme it lands in,
// clamped to [0, len(s)].
//
// Every offset that enters the mapping API passes through it. A byte offset
// produced by arithmetic — a click cell converted to a position, a resize
// rescaling an anchor — is not guaranteed to be a boundary, and a selection
// end that is not a boundary would put half a grapheme on the clipboard.
//
// Snapping DOWN (to the grapheme's start) rather than up is the choice that
// makes a click on the second cell of a wide character select that character
// instead of the one after it, and makes the snapped offset of a point inside
// a combining sequence name the base character.
func SnapToBoundary(s string, off int) int {
	if off <= 0 {
		return 0
	}
	if off >= len(s) {
		return len(s)
	}
	if isBoundary(s, off) {
		return off
	}
	pos := 0
	for pos < len(s) {
		cluster, _ := ansi.FirstGraphemeCluster(s[pos:], ansi.GraphemeWidth)
		next := pos + len(cluster)
		if off < next {
			return pos
		}
		pos = next
	}
	return len(s)
}

// isBoundary reports whether off is a grapheme boundary in s. Zero and
// len(s) always are.
func isBoundary(s string, off int) bool {
	if off <= 0 || off >= len(s) {
		return true
	}
	pos := 0
	for pos < len(s) {
		if pos == off {
			return true
		}
		cluster, _ := ansi.FirstGraphemeCluster(s[pos:], ansi.GraphemeWidth)
		pos += len(cluster)
	}
	return pos == off
}

// NextGrapheme returns the offset of the boundary after off, or len(s) when
// there is none.
//
// An offset before the start is treated as the start, so a cursor at the
// beginning advances rather than refusing to move.
func NextGrapheme(s string, off int) int {
	off = clampOffset(s, off)
	if off >= len(s) {
		return len(s)
	}
	cluster, _ := ansi.FirstGraphemeCluster(s[off:], ansi.GraphemeWidth)
	return off + len(cluster)
}

// PrevGrapheme returns the offset of the boundary before off, or 0 when there
// is none.
func PrevGrapheme(s string, off int) int {
	off = clampOffset(s, off)
	if off <= 0 {
		return 0
	}
	pos := 0
	prev := 0
	for pos < len(s) {
		cluster, _ := ansi.FirstGraphemeCluster(s[pos:], ansi.GraphemeWidth)
		next := pos + len(cluster)
		if next > off {
			break
		}
		prev = pos
		pos = next
	}
	if pos == off {
		return prev
	}
	return pos
}

// clampOffset bounds an offset to the string, without snapping. Snapping is
// separate on purpose: callers that need a boundary say so, and a caller that
// only needs "inside the string" should not pay for a grapheme walk.
func clampOffset(s string, off int) int {
	if off < 0 {
		return 0
	}
	if off > len(s) {
		return len(s)
	}
	return off
}

// offsetForCell converts a display column to the logical offset of the
// grapheme that owns it. It is the click path: a mouse event carries a cell,
// and the mapping has to name text.
//
// A cell in the middle of a wide grapheme belongs to that grapheme, so it
// resolves to the grapheme's START — a click on the right half of a CJK
// character aims at that character, not at the next one. A cell past the end
// of the line resolves to the end of the line, which is a valid place to put a
// cursor (select to end of line) and never a panic.
//
// The offset is always a grapheme boundary within the FIRST display line
// containing that column; a cell is a position in a row, so an offset returned
// for a multi-line string names a position on its first line.
//
// Unexported: the click path on a LAID-OUT row goes through
// RenderedBlock.OffsetAt, which has the row's spans; this free-standing form
// has no callers outside the package and is kept only for the
// offsetForCell/cellForOffset round-trip tests.
//
// Unlike cellWidth, the walk is per display line and the caller does not pass
// a starting column; mapping a LAID-OUT row whose tabs begin mid-row should
// prefer RenderedBlock.OffsetAt/CellAt, which read the tab span's own
// laid-out geometry and can only be exact through it.
func offsetForCell(s string, cell int, stop int) int {
	if stop <= 0 {
		stop = tabStop
	}
	if cell <= 0 {
		return 0
	}
	// A newline is the end of a row: a cell beyond it belongs to the next
	// line, so the search stops at the break rather than continuing past it.
	line := s
	if i := indexByte(s, '\n'); i >= 0 {
		line = s[:i]
	}
	col := 0
	pos := 0
	for pos < len(line) {
		if line[pos] == '\t' {
			pad := stop - col%stop
			if cell < col+pad {
				return pos
			}
			col += pad
			pos++
			continue
		}
		cluster, w := ansi.FirstGraphemeCluster(line[pos:], ansi.GraphemeWidth)
		if w == 0 {
			// A zero-width cluster (a stray combining mark at the start of a
			// line) occupies no cell, so no click can land on it. Stepping
			// past it keeps the walk monotone; without this the loop would
			// not advance.
			pos += len(cluster)
			continue
		}
		if cell < col+w {
			return pos
		}
		col += w
		pos += len(cluster)
	}
	return len(line)
}

// cellForOffset converts a logical offset to the display column its grapheme
// starts at, within the offset's own display line.
//
// It is the render path and the anchor path: a mapped renderer needs to know
// which column a match begins in so it can style it, and a restored anchor
// needs to know where the reader's byte now sits after a resize. It is the
// inverse of offsetForCell and the two round-trip.
//
// Unexported: the render path on a LAID-OUT row goes through
// RenderedBlock.CellAt; this free-standing pairing with offsetForCell has no
// callers outside the package.
//
// The offset is snapped first, so a caller may pass an arbitrary byte position
// and receive the column of the character it lands in.
func cellForOffset(s string, off int, stop int) int {
	if stop <= 0 {
		stop = tabStop
	}
	off = SnapToBoundary(s, off)
	// Only the line the offset is on contributes columns.
	start := lastLineStart(s, off)
	seg := s[start:off]
	col := 0
	for i := 0; i < len(seg); {
		if seg[i] == '\t' {
			col += stop - col%stop
			i++
			continue
		}
		cluster, w := ansi.FirstGraphemeCluster(seg[i:], ansi.GraphemeWidth)
		col += w
		i += len(cluster)
	}
	return col
}

// lastLineStart returns the index just after the last newline at or before
// off, so column measurement restarts per display line.
func lastLineStart(s string, off int) int {
	start := 0
	for i := 0; i < off && i < len(s); i++ {
		if s[i] == '\n' {
			start = i + 1
		}
	}
	return start
}

// indexByte returns the index of b in s, or -1. It exists so this file does
// not need to import strings for a single call.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
