package conversation

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// tabFixtures are the shapes whose mapping is only correct when a tab's width
// is derived from the column it STARTS at. "a\tb" at column 0 is the case every
// obvious implementation gets right; the rest put the tab at a non-zero start
// column, which is every Markdown table's second field onward
// (CellSeparator is "\t") and every wrapped row's continuation.
var tabFixtures = []struct {
	name string
	text string
}{
	{"tab at column zero", "a\tb"},
	{"tabs after two characters", "ab\tcd\tef"},
	{"tabs after text", "left\tmiddle\tright"},
	{"leading tab", "\tab\tcd"},
	{"tabs between plain words", "w x\ty\tz"},
}

// The headline symptom: a cell maps to an offset, and mapping it back names
// the SAME byte. With a tab anywhere but at column zero, a helper that assumes
// a tab is ts cells wide from wherever it is walked breaks the round trip.
func TestCellAtAndOffsetAtRoundTripWhenTabsDoNotStartAtColumnZero(t *testing.T) {
	for _, f := range tabFixtures {
		for _, width := range []int{40, 60, 100} {
			t.Run(f.name, func(t *testing.T) {
				block := LayoutBlock(Block{ID: "msg:1", Text: f.text}, LayoutOptions{Width: width})
				for i, row := range block.Rows {
					if !row.Range.HasText() || row.Range.Empty() {
						continue
					}
					for off := row.Range.Start; off < row.Range.End; off++ {
						cell, ok := block.CellAt(i, off)
						if !ok {
							t.Fatalf("CellAt(%d) reported nowhere in %q", off, f.text)
						}
						if back := block.OffsetAt(i, cell); back != off {
							t.Fatalf("%q width %d: offset %d -> cell %d -> offset %d, want %d",
								f.text, width, off, cell, back, off)
						}
					}
				}
			})
		}
	}
}

// The concrete symptom from the review: source "a\tb", a cell past the tab
// must map to the offset the cell was DRAWN at. With an indent of 2, the tab
// starts at column 3 and expands to the stop at column 8, so 'b' is drawn at
// column 8: the last cell of the row names its offset, and a cell past it
// resolves to nothing.
func TestCellAtMapsACellPastATabToTheOffsetDrawnThere(t *testing.T) {
	const text = "a\tb"
	indented := LayoutBlock(Block{ID: "msg:1", Text: text}, LayoutOptions{Width: 40, Indent: 2})
	row := indented.Rows[0]

	// Display: "  a     b" — the tab span occupies columns 3..7 (5 cells).
	for _, s := range row.Spans {
		if s.Range.HasText() && indented.Logical[s.Range.Start:s.Range.End] == "\t" {
			if s.Cells != 5 {
				t.Fatalf("the tab after one character was laid out as %d cells, want 5 (starts at column 3, next stop at 8)", s.Cells)
			}
		}
	}
	// 'b' is drawn at column 8, the row's last cell; the mapping must agree
	// with the geometry, not with a column-blind walk.
	if cell, ok := indented.CellAt(0, 2); !ok || cell != 8 {
		t.Fatalf("CellAt(the offset of 'b') = %d, %v; want its drawn column 8", cell, ok)
	}
	if got := indented.OffsetAt(0, row.Cells-1); got != 2 {
		t.Fatalf("OffsetAt(the cell 'b' is drawn at) = %d, want 2 (the offset of 'b')", got)
	}
}

// A Markdown table's field separator is a tab whose start column is the width
// of every cell before it — column 0 for the first separator, past that for
// the rest. A find match in the second column is highlighted with
// HighlightRange, so the two mappings have to agree about where the cells are.
func TestHighlightRangeOnATableRowMatchesTheCellsTheTextIsDrawnAt(t *testing.T) {
	sp := ProjectMarkdown("| a | b | c |\n| --- | --- | --- |\n| 1 | 22 | 333 |", MarkdownOptions{})
	block := RenderedBlock{
		BlockID: "table:1",
		Logical: sp.Text,
		Rows:    Layout(sp, LayoutOptions{Width: 120}),
	}

	// Find the row whose readable text is the body row "1\t22\t333".
	bodyIndex := -1
	for i, row := range block.Rows {
		if row.ContentText() == "1\t22\t333" {
			bodyIndex = i
			break
		}
	}
	if bodyIndex < 0 {
		t.Fatalf("the projected table does not contain the body row as drawn: %q", rowsText(block.Rows))
	}

	// Highlight "22" — in the SECOND column, so everything before it includes
	// a tab that started past column zero.
	sep := strings.Index(block.Logical, "1\t22\t333")
	from := sep + 2
	to := from + 2 // "22"
	startCell, endCell, ok := block.HighlightRange(bodyIndex, from, to)
	if !ok {
		t.Fatalf("HighlightRange refused the range on row %d", bodyIndex)
	}
	if endCell <= startCell {
		t.Fatalf("HighlightRange produced a zero-width highlight (%d, %d); the tab's start column was ignored",
			startCell, endCell)
	}
	// The highlighted cells must actually cover where "22" is drawn.
	if got := block.Rows[bodyIndex].Text()[startCell:endCell]; got != "22" {
		t.Fatalf("the cells %d..%d hold %q, want %q — the highlight is tinted off the text",
			startCell, endCell, got, "22")
	}
}

// A tab wider than a whole row's budget must not wedge the wrapper on the
// one grapheme that cannot fit: the wrap has to advance past it or it loops
// forever. The layout overflows (recorded, not truncated, like any wide
// grapheme), and the reassembly still gets the bytes back.
func TestATabWiderThanTheBudgetDoesNotWedgeTheWrap(t *testing.T) {
	for _, f := range tabFixtures {
		t.Run(f.name, func(t *testing.T) {
			// A six-column terminal: an indent of one leaves five, and every
			// stop-aligned tab in these fixtures is wider than five cells.
			block := LayoutBlock(Block{ID: "msg:1", Text: f.text}, LayoutOptions{Width: 6, Indent: 1})
			seenTabs := 0
			for i, row := range block.Rows {
				if row.Cells > 6 {
					// Overflow is recorded, not truncated.
					t.Logf("row %d overflows to %d cells (a wide grapheme) — recorded, not truncated", i, row.Cells)
				}
				for _, s := range row.Spans {
					if s.Range.HasText() && block.Logical[s.Range.Start:s.Range.End] == "\t" {
						seenTabs++
					}
				}
			}
			if seenTabs == 0 {
				t.Fatalf("%q produced no tab span to check", f.text)
			}
			if got := reassembled(block.Rows); got != f.text {
				t.Fatalf("reassembled %q, want %q — a byte was lost", got, f.text)
			}
			// No empty progress: consecutive rows must each consume content.
			prev := 0
			for i, row := range block.Rows {
				end := row.Range.End
				if end <= prev {
					t.Fatalf("row %d ends at %d, not past the previous row's %d: the wrapper stopped advancing",
						i, end, prev)
				}
				prev = end
			}
		})
	}
}

// A tab span's cell count must be exactly what the layout computed for the
// column it starts at: the layout and the mapping must never disagree about
// how wide the span is.
func TestTabSpanCellsMatchTheLayoutAtTheirStartColumn(t *testing.T) {
	for _, f := range tabFixtures {
		t.Run(f.name, func(t *testing.T) {
			block := LayoutBlock(Block{ID: "msg:1", Text: f.text}, LayoutOptions{Width: 60})
			seenTabs := 0
			for _, row := range block.Rows {
				col := 0
				for _, s := range row.Spans {
					if s.Range.HasText() &&
						block.Logical[s.Range.Start:s.Range.End] == "\t" {
						want := tabStop - col%tabStop
						if s.Cells != want {
							t.Fatalf("tab span at column %d laid out as %d cells, want %d (ts=%d)",
								col, s.Cells, want, tabStop)
						}
						seenTabs++
					}
					col += s.Cells
				}
			}
			if seenTabs == 0 {
				t.Fatalf("%q produced no tab span to check", f.text)
			}
			// And the row's total width agrees with the width function the app
			// measures display text with — indent included, tabs expanded.
			for i, row := range block.Rows {
				if got := ansi.StringWidth(row.Text()); got != row.Cells {
					t.Fatalf("row %d claims %d cells but its text measures %d", i, row.Cells, got)
				}
			}
		})
	}
}
