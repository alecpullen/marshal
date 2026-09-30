package conversation

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// rowsText joins a rendered block's rows for assertions that care about the
// whole display form.
func rowsText(rows []DisplayRow) string {
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, r.Text())
	}
	return strings.Join(parts, "\n")
}

// contentText joins only the non-decorative parts, which is the readable
// projection.
func contentText(rows []DisplayRow) string {
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, r.ContentText())
	}
	return strings.Join(parts, "\n")
}

// reassembled joins a display form the way a reader would read it off the
// screen, using each row's own declared separator.
//
// This is the plan's central rendering invariant expressed as an assertion — a
// soft wrap contributes a space or nothing, only the AUTHOR's break contributes
// a newline. Joining every row with "\n" (contentText) would hide a violation of
// it, because it puts a newline everywhere regardless.
func reassembled(rows []DisplayRow) string {
	var b strings.Builder
	for i, r := range rows {
		b.WriteString(r.ContentText())
		if i < len(rows)-1 {
			b.WriteString(r.Separator())
		}
	}
	return b.String()
}

// The first invariant everything else depends on: layout cannot change the
// bytes a copy yields. A soft wrap, an indent and an expanded tab are all
// display concerns.
func TestLayoutNeverChangesTheLogicalText(t *testing.T) {
	for _, f := range textFixtures {
		t.Run(f.name, func(t *testing.T) {
			block := LayoutBlock(Block{ID: "msg:1", Text: f.text}, LayoutOptions{
				Width: 40, Indent: 3, Breakpoints: "/-:",
			})
			if block.LogicalText() != f.text {
				t.Fatalf("logical text changed:\n got %q\nwant %q", block.LogicalText(), f.text)
			}
		})
	}
}

// A row never exceeds its budget, for any fixture at any width. This is the
// property a golden fixture can only spot-check; asserting it across widths is
// what catches the arithmetic.
func TestRowsNeverExceedTheWidthBudget(t *testing.T) {
	for _, f := range textFixtures {
		if f.name == "empty" {
			continue
		}
		for _, width := range []int{8, 20, 40, 79, 120} {
			t.Run(f.name, func(t *testing.T) {
				block := LayoutBlock(Block{ID: "msg:1", Text: f.text}, LayoutOptions{
					Width: width, Indent: 3, Breakpoints: "/-:",
				})
				for i, row := range block.Rows {
					if row.Cells > width {
						t.Fatalf("width %d: row %d is %d cells: %q",
							width, i, row.Cells, row.Text())
					}
					if got := ansi.StringWidth(row.Text()); got > width {
						t.Fatalf("width %d: row %d measures %d cells by ansi.StringWidth: %q",
							width, i, got, row.Text())
					}
				}
			})
		}
	}
}

// A tab's width is column-relative, so the wrap that decides where a row ends
// and the builder that draws it must count columns from the SAME place — the
// indent the content starts at. Measuring the wrap from column 0 while the
// builder walked from the indent made every hard line whose tab landed near a
// stop render WIDER than the wrap had decided, which is why the fixtures above
// could not catch it: none of them contains a tab.
//
// The indent is not incidental. Every transcript block is laid out with
// Indent 3, and the Markdown projection makes a table's cell separator a tab,
// so a one-row table is the smallest production shape that overflows.
func TestTabbedRowsNeverExceedTheWidthBudget(t *testing.T) {
	for _, f := range tabFixtures {
		t.Run(f.name, func(t *testing.T) {
			// Both the package stop and a custom one. The custom stop is not
			// decoration: LayoutBlock stores the stop it laid out with, and the
			// mapping measures a tab against it, so a wrap that agreed with the
			// builder at 8 and disagreed at 4 would overflow only here.
			for _, ts := range []int{0, 4} {
				// The stop the layout will actually use; zero means the package
				// default, which is the value every fixture above must be
				// checked against.
				eff := effectiveTabStop(ts)
				// 8 and 10 are here because the guard this replaces —
				// "width < indent + stop", which is the width of a tab at
				// column ZERO, the widest a tab can ever be — discarded exactly
				// these cases: the "leading tab" fixture at indent 1 and stop 8
				// opens its tab at column 1, where the tab is one cell short of
				// the stop and the width leaves exactly room for it. It fits,
				// and the conservative guard threw the case away.
				for _, indent := range []int{0, 1, 3} {
					for _, width := range []int{8, 10, 12, 16, 20, 40, 79} {
						startCol, need, hasTab := firstTabWidth(f.text, indent, eff)
						if hasTab && need > width-indent {
							// CONSERVATIVE, not exact: the tab's in-line start
							// column is measured against the whole prefix, but
							// the wrap RESETS the column at a break, so a tab
							// can also open the NEXT row at column indent and
							// cost less there than it does in line. The guard
							// skips only a case that MIGHT not be honourable;
							// over the shipped fixture matrix it provably skips
							// nothing (TestFirstTabWidthSkipsOnlyWhatCannotFit
							// pins its arithmetic). Cases with a tab that may
							// not fit in line are excluded here because the
							// recorded minimum-overflow outcome — not a budget
							// violation — is the expected shape;
							// TestATabWiderThanTheBudgetDoesNotWedgeTheWrap
							// covers that shape.
							t.Logf("stop %d indent %d width %d: the in-line tab opens at column %d and needs %d content cells, which may not fit; skipping",
								eff, indent, width, startCol, need)
							continue
						}
						block := LayoutBlock(Block{ID: "msg:1", Text: f.text}, LayoutOptions{
							Width: width, Indent: indent, TabStop: ts,
						})
						if got := effectiveTabStop(block.TabStop); got != eff {
							t.Fatalf("stop %d indent %d width %d: the block lost the stop it was laid out with: %d, want %d",
								eff, indent, width, got, eff)
						}
						for i, row := range block.Rows {
							if row.Cells > width {
								t.Fatalf("stop %d indent %d width %d: row %d is %d cells: %q",
									eff, indent, width, i, row.Cells, row.Text())
							}
							if got := ansi.StringWidth(row.Text()); got > width {
								t.Fatalf("stop %d indent %d width %d: row %d measures %d cells: %q",
									eff, indent, width, i, got, row.Text())
							}
						}
						// The wrap may not achieve the budget by losing a byte.
						if got := reassembled(block.Rows); got != f.text {
							t.Fatalf("stop %d indent %d width %d: reassembled %q, want %q",
								eff, indent, width, got, f.text)
						}
					}
				}
			}
		})
	}
}

// firstTabWidth measures a fixture's first tab: its IN-LINE start column (the
// prefix before it on its own hard line, measured from the indent exactly as
// rowEnd measures the walk) and the content cells it needs there at this stop.
//
// The guard this feeds is CONSERVATIVE, not exact: it measures the tab where it
// opens in line, but rowEnd also resets the column at a break, so the tab can
// open a LATER row at column indent and cost less there. Over the shipped
// fixture matrix the guard skips nothing (verified by enumeration), so no
// coverage is lost — it exists to justify, in one place, the shapes the sweep
// would otherwise have to argue case by case.
func firstTabWidth(text string, indent, ts int) (startCol, need int, ok bool) {
	i := strings.IndexByte(text, '\t')
	if i < 0 {
		return 0, 0, false
	}
	// The text before the tab on its own hard line decides the column the tab
	// opens at.
	lineStart := strings.LastIndexByte(text[:i], '\n') + 1
	startCol = indent + ansi.StringWidth(text[lineStart:i])
	return startCol, ts - startCol%ts, true
}

// The skip guard must not skip what the layout can honour: a tab's width is
// measured from the column it STARTS at, so indent+stop is NOT the threshold.
// Both sides of it are pinned here, with the two shapes that differ by one
// cell — the "leading tab" fixture at indent 1, whose tab opens at column 1 and
// is one cell short of the stop against the width a stop leaves there (it fits,
// and the conservative `width < indent+stop` guard threw it away), and a tab
// that opens at column 0 where it really is the full stop and does not fit at
// one cell less.
func TestFirstTabWidthSkipsOnlyWhatCannotFit(t *testing.T) {
	for _, tc := range []struct {
		name                string
		text                string
		indent, width, ts   int
		wantStart, wantNeed int
		wantSkip            bool
	}{
		{"leading tab at indent 1 fits a width of 8", "\tab\tcd", 1, 8, 8, 1, 7, false},
		{"tab at column 0 needs the whole stop", "\tab\tcd", 0, 8, 8, 0, 8, false},
		{"in-line tab wider than width 7 at column 0", "\tab\tcd", 0, 7, 8, 0, 8, true},
		{"in-line need 6 exceeds width-indent 5", "1234567\tword", 3, 8, 8, 10, 6, true},
		{"no tab is never skipped", "plain words", 3, 8, 8, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			startCol, need, hasTab := firstTabWidth(tc.text, tc.indent, tc.ts)
			if wantHas := strings.IndexByte(tc.text, '\t') >= 0; hasTab != wantHas {
				t.Fatalf("hasTab = %v for %q, want %v", hasTab, tc.text, wantHas)
			}
			if startCol != tc.wantStart || need != tc.wantNeed {
				t.Fatalf("firstTabWidth(%q, %d, %d) = start %d / need %d, want %d / %d",
					tc.text, tc.indent, tc.ts, startCol, need, tc.wantStart, tc.wantNeed)
			}
			if got := hasTab && need > tc.width-tc.indent; got != tc.wantSkip {
				t.Fatalf("skip = %v, want %v (start %d, need %d, width %d, indent %d)",
					got, tc.wantSkip, startCol, need, tc.width, tc.indent)
			}
		})
	}
}

// The reviewer's two reproductions, pinned exactly. Both are a row that ends at
// the tab the wrap should have broken before: measuring the tab from column 0
// made it look one cell wide for the wrap and it was drawn seven or eight cells
// wide, so row 0 came out at 20 cells against a budget of 10 and of 16.
func TestAWrapThatFallsAtATabStillFitsTheBudget(t *testing.T) {
	for _, tc := range []struct{ width, indent int }{{10, 1}, {16, 3}} {
		const text = "1234567\tword"
		block := LayoutBlock(Block{ID: "msg:1", Text: text}, LayoutOptions{
			Width: tc.width, Indent: tc.indent,
		})
		for i, row := range block.Rows {
			if row.Cells > tc.width {
				t.Fatalf("width %d indent %d: row %d is %d cells: %q",
					tc.width, tc.indent, i, row.Cells, row.Text())
			}
		}
		// The tab is measured from the indent, so the row ends ON it (or, at
		// width 10, before it): "word" is pushed to a row of its own, which is
		// the opposite of the reported failure, where the tab expanded to fill
		// the budget the wrap had already spent and "word" stayed on row 0.
		if got := block.Rows[0].ContentText(); strings.Contains(got, "word") {
			t.Fatalf("width %d indent %d: first row is %q, want the text before the tab",
				tc.width, tc.indent, got)
		}
		if got := reassembled(block.Rows); got != text {
			t.Fatalf("width %d indent %d: reassembled %q, want %q", tc.width, tc.indent, got, text)
		}
	}
}

// The production shape the overflow was reached through: a Markdown table whose
// cell separator is a tab, laid out at the transcript's indent. At width 20 the
// reviewer measured 21 cells on the body row.
func TestAMarkdownTableWithATabSeparatorFitsTheTranscriptWidth(t *testing.T) {
	sp := ProjectMarkdown("| marshal | stars |\n| --- | --- |\n| go | 1234 |", MarkdownOptions{})
	for _, width := range []int{20, 40, 80} {
		// Through LayoutBlock, not a hand-built literal: LayoutBlock is what
		// carries the tab stop the rows were laid out with onto the block the
		// mapping reads, and a literal would leave that field at zero and
		// assert a propagation that never happened. It is also the entry point
		// the documented consumer path names, so this is the shape a caller
		// actually gets.
		block := LayoutBlock(Block{ID: "table:1", Text: sp.Text}, LayoutOptions{
			Width: width, Indent: 3, Breakpoints: "",
		})
		if block.TabStop != 0 {
			t.Fatalf("width %d: the default stop did not propagate to the block: %d", width, block.TabStop)
		}
		if len(block.Rows) == 0 {
			t.Fatalf("width %d: the table projected to no rows: %q", width, sp.Text)
		}
		for i, row := range block.Rows {
			if row.Cells > width {
				t.Fatalf("width %d: row %d is %d cells: %q", width, i, row.Cells, row.Text())
			}
			if got := ansi.StringWidth(row.Text()); got > width {
				t.Fatalf("width %d: row %d measures %d cells: %q", width, i, got, row.Text())
			}
		}
	}
}

// normalizeRuns must not swallow a whole run when one merely GRAZES another:
// the un-overlapped remainder is real text that would otherwise lose its
// styling and be left an orphan gap. Pinning the deterministic choice — an
// overlap that reaches past the earlier run keeps the earlier one whole and
// styles the unclaimed remainder; an overlap fully covered by an earlier run
// is dropped outright, because none of its bytes are unstyled anywhere.
func TestNormalizeRunsKeepsTheUnoverlappedTailOfAnOverlappingRun(t *testing.T) {
	got := normalizeRuns([]Run{
		{Range: Range{0, 3}, Kind: SpanEmphasis}, // "abc"
		{Range: Range{1, 8}, Kind: SpanCode},     // overlaps, but reaches past it
	}, 10)
	if len(got) != 2 {
		t.Fatalf("a grazing overlap swallowed a whole run: %+v", got)
	}
	if got[0] != (Run{Range: Range{0, 3}, Kind: SpanEmphasis}) {
		t.Fatalf("the first run was not kept: %+v", got[0])
	}
	// The second run survives WITHOUT the two bytes that were already
	// claimed: everything else would style one byte twice or drop its text.
	if got[1].Kind != SpanCode || got[1].Range.Start != 3 || got[1].Range.End != 8 {
		t.Fatalf("the overlapping run's survivor = %+v, want code over bytes 3..8", got[1])
	}

	// A run fully covered by an earlier one HAS no unclaimed bytes, so it
	// contributes nothing.
	if fully := normalizeRuns([]Run{
		{Range: Range{0, 8}, Kind: SpanEmphasis},
		{Range: Range{2, 5}, Kind: SpanCode},
	}, 10); len(fully) != 1 {
		t.Fatalf("a fully covered run was not dropped: %+v", fully)
	}
}

// normalizeRuns must be robust to an UNSORTED input, not silently reject it:
// the runs a real renderer produces are ordered, but a caller that hands over
// a reversed slice (a fold that appended in the wrong direction) must still
// get a projection it can lay out rather than one run and a dropped tail.
func TestNormalizeRunsSortsAnUnsortedInputDeterministically(t *testing.T) {
	got := normalizeRuns([]Run{
		{Range: Range{5, 8}, Kind: SpanStrong},
		{Range: Range{0, 3}, Kind: SpanEmphasis},
	}, 10)
	if len(got) != 2 {
		t.Fatalf("unsorted runs were dropped rather than ordered: %+v", got)
	}
	if got[0].Range.Start > got[1].Range.Start {
		t.Fatalf("the runs came back unsorted: %+v", got)
	}
	if got[0] != (Run{Range: Range{0, 3}, Kind: SpanEmphasis}) ||
		got[1] != (Run{Range: Range{5, 8}, Kind: SpanStrong}) {
		t.Fatalf("the runs were reordered wrongly: %+v", got)
	}
}

// A hard break is the author's; a soft break is the renderer's. Copy uses the
// author's breaks and must never invent one, which is why the distinction is
// recorded on the row rather than inferred later from the display text.
func TestHardBreaksComeFromTheAuthorAndSoftBreaksDoNot(t *testing.T) {
	block := LayoutBlock(Block{ID: "msg:1", Text: "one two three four five\nsecond line"}, LayoutOptions{
		Width: 14, Breakpoints: " ",
	})
	// One hard break per author line: the first author line's row and the
	// second (which is also the last row).
	var hard []int
	for i, row := range block.Rows {
		if row.HardBreak {
			hard = append(hard, i)
		}
	}
	if len(hard) != 2 {
		t.Fatalf("want 2 hard-broken rows (one per author line), got %d: %v", len(hard), hard)
	}
	if hard[0] == 0 {
		t.Fatalf("line one wraps at width 14, so row 0 must be a soft break: %q", rowsText(block.Rows))
	}
	// The row the break is ON ends the author's first line, and the row after
	// it starts the second — the break is the author's, placed where they put it.
	if got := block.Rows[hard[0]].Text(); !strings.HasSuffix(got, "five") {
		t.Fatalf("the first hard break should end the author's first line, got %q", got)
	}
	if !strings.HasPrefix(block.Rows[hard[1]].Text(), "second line") {
		t.Fatalf("the last row should be the second author line, got %q", block.Rows[hard[1]].Text())
	}
}

// The plan's central rendering invariant: what comes back off the rows is the
// text that went in, with the author's newlines and no others.
//
// The trailing newline of a fixture is excluded from the comparison rather than
// from the property: it is inside the last row's range (so the ranges still
// partition the text) but produces no display span, because a terminal shows
// nothing for it. Copy reads Logical, so the byte is not lost either way.
func TestReassembledRowsReproduceTheTextWithOnlyTheAuthorsNewlines(t *testing.T) {
	for _, f := range textFixtures {
		t.Run(f.name, func(t *testing.T) {
			for _, width := range []int{10, 24, 80} {
				block := LayoutBlock(Block{ID: "msg:1", Text: f.text}, LayoutOptions{
					Width: width, Indent: 2, Breakpoints: "/-:",
				})
				want := strings.TrimSuffix(f.text, "\n")
				if got := reassembled(block.Rows); got != want {
					t.Fatalf("width %d:\n got %q\nwant %q", width, got, want)
				}
			}
		})
	}
}

// Every logical byte is accounted for by exactly one row. Gaps would make a
// drag skip text; overlaps would make it select text twice.
func TestRowsCoverTheLogicalTextExactlyOnce(t *testing.T) {
	for _, f := range textFixtures {
		t.Run(f.name, func(t *testing.T) {
			block := LayoutBlock(Block{ID: "msg:1", Text: f.text}, LayoutOptions{
				Width: 24, Indent: 2, Breakpoints: "/-:",
			})
			seen := make([]int, len(f.text))
			for _, row := range block.Rows {
				if !row.Range.HasText() {
					continue
				}
				for i := row.Range.Start; i < row.Range.End; i++ {
					if i < 0 || i >= len(seen) {
						t.Fatalf("row range %+v is outside the text (%d bytes)", row.Range, len(f.text))
					}
					seen[i]++
				}
			}
			for i, n := range seen {
				if n != 1 {
					t.Fatalf("byte %d (%.10q) is covered %d times", i, f.text[i:], n)
				}
			}
		})
	}
}

// Decoration is on screen but not in the document: a selection of table cells
// must omit the borders, and a quote's marker must not become part of a copy.
func TestDecorationIsMarkedAndExcludedFromTheReadableProjection(t *testing.T) {
	rows := Layout(Spans{
		Text: "A1|B1",
		Runs: []Run{
			{Range: Range{0, 2}, Kind: SpanPlain},       // A1
			{Range: Range{2, 3}, Kind: SpanTableBorder}, // the separator
			{Range: Range{3, 5}, Kind: SpanPlain},       // B1
		},
	}, LayoutOptions{Width: 40})

	if got := contentText(rows); got != "A1B1" {
		t.Fatalf("readable projection = %q, want %q", got, "A1B1")
	}
	if got := rowsText(rows); got != "A1|B1" {
		t.Fatalf("display text = %q, want %q", got, "A1|B1")
	}
	if rows[0].Cells != 5 {
		t.Fatalf("row cells = %d, want 5", rows[0].Cells)
	}
}

// A decoration carries no logical range. Zero is a real offset, so a decoration
// claiming it would make "the start of the block" refer to two different places.
func TestDecorationCarriesNoLogicalRange(t *testing.T) {
	rows := Layout(Spans{
		Text: "quoted",
		Runs: []Run{{Range: Range{0, 6}, Kind: SpanPlain}},
	}, LayoutOptions{Width: 40, Indent: 3})

	if len(rows) != 1 || len(rows[0].Spans) < 2 {
		t.Fatalf("want an indent span plus content, got %+v", rows)
	}
	indent := rows[0].Spans[0]
	if indent.Kind != SpanIndent {
		t.Fatalf("first span kind = %v, want SpanIndent", indent.Kind)
	}
	if indent.Range.HasText() {
		t.Fatalf("indent span claims logical range %+v; decoration must claim none", indent.Range)
	}
	if got := rows[0].Range; got.Start != 0 || got.End != 6 {
		t.Fatalf("row logical range = %+v, want {0 6}", got)
	}
}

// An expanded tab is display only. A copy of indented code keeps the author's
// tab; a row shows the spaces the terminal would.
func TestTabsExpandForDisplayAndSurviveInLogicalText(t *testing.T) {
	const text = "a\tb"
	block := LayoutBlock(Block{ID: "msg:1", Text: text}, LayoutOptions{Width: 40})

	if block.LogicalText() != text {
		t.Fatalf("logical text = %q, want %q", block.LogicalText(), text)
	}
	if got := rowsText([]DisplayRow{block.Rows[0]}); got != "a       b" {
		t.Fatalf("display text = %q, want %q", got, "a       b")
	}
	if got := block.Rows[0].Cells; got != 9 {
		t.Fatalf("row cells = %d, want 9", got)
	}
	// The tab is its own span, because its display text and its logical range
	// have different lengths.
	var tab *Span
	for i := range block.Rows[0].Spans {
		if block.Rows[0].Spans[i].Range.Len() == 1 && block.Rows[0].Spans[i].Cells == 7 {
			tab = &block.Rows[0].Spans[i]
		}
	}
	if tab == nil {
		t.Fatalf("no span carries the expanded tab: %+v", block.Rows[0].Spans)
	}
	if got := block.Logical[tab.Range.Start:tab.Range.End]; got != "\t" {
		t.Fatalf("the tab span's logical text = %q, want a tab", got)
	}
}

// A click maps to an offset, and the offset it names must be a grapheme
// boundary. This is the property that keeps half an emoji off the clipboard.
func TestEveryCellOnARowMapsToAGraphemeBoundary(t *testing.T) {
	for _, f := range textFixtures {
		t.Run(f.name, func(t *testing.T) {
			block := LayoutBlock(Block{ID: "msg:1", Text: f.text}, LayoutOptions{
				Width: 16, Indent: 2, Breakpoints: "/-:",
			})
			for i, row := range block.Rows {
				for cell := -2; cell <= row.Cells+2; cell++ {
					off := block.OffsetAt(i, cell)
					if off < 0 || off > len(f.text) {
						t.Fatalf("row %d cell %d -> offset %d, outside the text", i, cell, off)
					}
					if got := SnapToBoundary(f.text, off); got != off {
						t.Fatalf("row %d cell %d -> offset %d is not a grapheme boundary (snaps to %d) in %q",
							i, cell, off, got, f.text)
					}
				}
			}
		})
	}
}

// CellAt and OffsetAt are inverses at every offset a reader can point at.
//
// Two families of offset are excluded, and neither is a weakening: the
// coordinate genuinely cannot name them.
//
//   - An offset INSIDE a multi-byte grapheme is not a place text can be cut, so
//     no cell maps to it.
//   - An offset at a HARD break — the "\n" itself and the position just after
//     it — is the boundary between two rows. A cell is a position within one
//     row, so it cannot name a byte that is only visible as the reason the row
//     ended.
func TestCellAtRoundTripsThroughOffsetAt(t *testing.T) {
	for _, f := range textFixtures {
		t.Run(f.name, func(t *testing.T) {
			block := LayoutBlock(Block{ID: "msg:1", Text: f.text}, LayoutOptions{
				Width: 20, Indent: 3, Breakpoints: "/-:",
			})
			for i, row := range block.Rows {
				if !row.Range.HasText() || row.Range.Empty() {
					continue
				}
				for off := row.Range.Start; off < row.Range.End; off++ {
					if got := SnapToBoundary(f.text, off); got != off {
						continue // a continuation byte is not a place text can be cut
					}
					if f.text[off] == '\n' {
						continue // the break itself belongs to no single row
					}
					cell, ok := block.CellAt(i, off)
					if !ok {
						continue
					}
					if back := block.OffsetAt(i, cell); back != off {
						t.Fatalf("row %d: offset %d (%q) -> cell %d -> offset %d in %q",
							i, off, f.text[off], cell, back, f.text)
					}
				}
			}
		})
	}
}

// A cell past the last visible character resolves to where that text ends —
// the offset just after it — and never falls through to a byte the reader
// cannot see at all.
//
// The distinction matters because a soft wrap puts an invisible byte (the
// consumed space) at exactly the offset where the visible text ends. Returning
// the row's END instead would report that space's successor, which is one byte
// further right than anything on screen: a drag to the right edge of a wrapped
// line would select a character from the next row.
func TestACellPastTheLastVisibleCharacterResolvesToWhereThatTextEnds(t *testing.T) {
	// Wrapped: row 0 is "hello" plus an invisible consumed space.
	wrapped := LayoutBlock(Block{ID: "msg:1", Text: "hello world"}, LayoutOptions{Width: 8})
	row := wrapped.Rows[0]
	got := wrapped.OffsetAt(0, row.Cells)
	if got != row.Range.End-1 {
		t.Fatalf("a click at the row's right edge resolved to %d, want the offset of the "+
			"consumed space %d (the row range is %+v)", got, row.Range.End-1, row.Range)
	}
	if wrapped.Logical[got] != ' ' {
		t.Fatalf("offset %d is %q, want the consumed space", got, wrapped.Logical[got])
	}
	if got >= row.Range.End {
		t.Fatalf("offset %d is past the row's range %+v", got, row.Range)
	}

	// Unwrapped: there is no consumed space, so the offset is simply row end.
	plain := LayoutBlock(Block{ID: "msg:2", Text: "hello world"}, LayoutOptions{Width: 40})
	prow := plain.Rows[0]
	if got := plain.OffsetAt(0, prow.Cells+5); got != prow.Range.End {
		t.Fatalf("a click past an unwrapped row resolved to %d, want the row end %d",
			got, prow.Range.End)
	}
}

// A cell past the end of a row names the end of that row, so a drag that runs
// off the right edge selects to the end of the line rather than nothing.
func TestACellPastTheEndOfARowNamesTheEndOfItsRange(t *testing.T) {
	block := LayoutBlock(Block{ID: "msg:1", Text: "alpha beta"}, LayoutOptions{Width: 40})
	row := block.Rows[0]
	if got := block.OffsetAt(0, 500); got != row.Range.End {
		t.Fatalf("OffsetAt(0, 500) = %d, want the row end %d", got, row.Range.End)
	}
	if got := block.OffsetAt(99, 0); got != len(block.Logical) {
		t.Fatalf("OffsetAt on a nonexistent row = %d, want %d", got, len(block.Logical))
	}
}

// A 300-character token has no break opportunity in it, so the wrap has to
// break mid-token rather than overflow. The fixture exists because the failure
// mode is a row wider than the terminal, which is unreachable on screen.
func TestAnUnbreakableTokenWrapsMidToken(t *testing.T) {
	text := strings.Repeat("a", 300)
	block := LayoutBlock(Block{ID: "msg:1", Text: text}, LayoutOptions{Width: 20})
	if len(block.Rows) < 15 {
		t.Fatalf("300 chars at width 20 should need many rows, got %d", len(block.Rows))
	}
	for i, row := range block.Rows {
		if row.Cells > 20 {
			t.Fatalf("row %d is %d cells", i, row.Cells)
		}
	}
	// A soft wrap must not insert a newline, so the token comes back whole.
	if got := reassembled(block.Rows); got != text {
		t.Fatalf("reassembled content is %d bytes, want %d", len(got), len(text))
	}
}

// A path or flag breaks at its own separators, so the pieces stay recognisable
// and a copy of the whole still reconstructs exactly.
func TestBreakpointsKeepReferencesReconstructible(t *testing.T) {
	const text = "/Users/alec/projects/marshal/internal/app/tui/wrap.go"
	block := LayoutBlock(Block{ID: "msg:1", Text: text}, LayoutOptions{
		Width: 24, Breakpoints: "/-:",
	})
	if len(block.Rows) < 2 {
		t.Fatalf("the path should wrap at width 24, got %d row(s)", len(block.Rows))
	}
	if got := reassembled(block.Rows); got != text {
		t.Fatalf("reassembled path:\n got %q\nwant %q", got, text)
	}
	// A break after "/" keeps the separator on the row it ends, so the next
	// row starts with the path segment rather than with a stray slash.
	for i, row := range block.Rows[:len(block.Rows)-1] {
		if !row.Range.HasText() || row.Range.Empty() {
			continue
		}
		last := text[row.Range.End-1]
		if last != '/' {
			t.Fatalf("row %d breaks after %q, not after a breakpoint", i, string(last))
		}
	}
}

// A wrap that consumes a word boundary must be able to put that boundary back.
// Without it, reading text off the rows either glues two words together
// ("helloworld") or has to invent a separator, and an invented one cannot be
// told apart from a space the author wrote.
func TestASoftWrapAtASpaceDeclaresTheSeparatorItConsumed(t *testing.T) {
	block := LayoutBlock(Block{ID: "msg:1", Text: "hello world"}, LayoutOptions{Width: 8})
	if len(block.Rows) != 2 {
		t.Fatalf("want the line to wrap into 2 rows at width 8, got %d: %q",
			len(block.Rows), rowsText(block.Rows))
	}
	if !block.Rows[0].ConsumedSpace {
		t.Fatalf("the first row wrapped at a space and must say so: %+v", block.Rows[0])
	}
	if got := block.Rows[0].Separator(); got != " " {
		t.Fatalf("separator = %q, want a space", got)
	}
	// A wrap that fell mid-token consumed nothing, so no separator is invented.
	midToken := LayoutBlock(Block{ID: "msg:2", Text: strings.Repeat("a", 20)}, LayoutOptions{Width: 8})
	if midToken.Rows[0].ConsumedSpace {
		t.Fatalf("a mid-token wrap must not claim a consumed space: %+v", midToken.Rows[0])
	}
	if got := midToken.Rows[0].Separator(); got != "" {
		t.Fatalf("separator after a mid-token wrap = %q, want none", got)
	}
}

// Trailing whitespace before the AUTHOR's newline belongs to the author. It is
// not a wrap artefact, so it must still be displayed: eliding it would silently
// delete bytes from the middle of a block.
func TestTrailingWhitespaceBeforeAHardBreakIsKept(t *testing.T) {
	block := LayoutBlock(Block{ID: "msg:1", Text: "keep   \nnext"}, LayoutOptions{Width: 40})
	if got := block.Rows[0].ContentText(); got != "keep   " {
		t.Fatalf("the author's trailing spaces were dropped: %q", got)
	}
	if block.Rows[0].ConsumedSpace {
		t.Fatalf("a hard-broken row must not claim a consumed wrap space")
	}
	if got := reassembled(block.Rows); got != "keep   \nnext" {
		t.Fatalf("reassembled %q, want the trailing spaces kept", got)
	}
}

// textAcross is what a drag-selection copy is built on: it must return the
// LOGICAL text between two display positions, with no soft-wrap newline and no
// decoration.
func TestTextAcrossReturnsLogicalTextBetweenDisplayPositions(t *testing.T) {
	block := LayoutBlock(Block{ID: "msg:1", Text: "hello world"}, LayoutOptions{
		Width: 8, Indent: 2,
	})
	// Rows: "  hello" (cells 2..6 = hello), "  world". The range is half-open,
	// so the end cell names the offset just past the last character selected.
	got := block.textAcross(0, 2, 0, 7)
	if got != "hello" {
		t.Fatalf("textAcross over the first row = %q, want %q", got, "hello")
	}
	// Across the wrap: the consumed space is restored, not turned into a
	// newline and not dropped.
	got = block.textAcross(0, 2, 1, 8)
	if got != "hello world" {
		t.Fatalf("textAcross across the wrap = %q, want %q", got, "hello world")
	}
	// A reversed drag yields the same text as a forward one.
	if back := block.textAcross(1, 8, 0, 2); back != got {
		t.Fatalf("a backwards drag gave %q, want %q", back, got)
	}
	// The end cell is exclusive: stopping at the start of "world" leaves it out.
	if got := block.textAcross(0, 2, 1, 2); got != "hello " {
		t.Fatalf("textAcross up to the start of the next row = %q, want %q", got, "hello ")
	}
	// A drag that runs off both ends is clamped, not a panic.
	if got := block.textAcross(-5, -5, 99, 99); got != "hello world" {
		t.Fatalf("an out-of-range drag gave %q, want the whole text", got)
	}
}

// A drag across an indented row must not pick up the indent: it is decoration
// the renderer added, and a copy that included it would paste stray spaces.
func TestTextAcrossSkipsDecoration(t *testing.T) {
	block := LayoutBlock(Block{ID: "msg:1", Text: "code"}, LayoutOptions{Width: 40, Indent: 4})
	if got := block.textAcross(0, 0, 0, 8); got != "code" {
		t.Fatalf("a drag over the indent and the text gave %q, want %q", got, "code")
	}
}

// An empty block lays out as no rows rather than as one empty row, so a caller
// does not render a blank line for content that is not there.
func TestEmptyTextLaysOutAsNoRows(t *testing.T) {
	block := LayoutBlock(Block{ID: "msg:1", Text: ""}, LayoutOptions{Width: 40, Indent: 3})
	if len(block.Rows) != 0 {
		t.Fatalf("empty text produced %d rows: %+v", len(block.Rows), block.Rows)
	}
	if got := block.LogicalText(); got != "" {
		t.Fatalf("logical text = %q, want empty", got)
	}
}

// A blank line between paragraphs is a row with no content, not a missing row:
// dropping it would glue two paragraphs together in a copy.
//
// Its logical range covers exactly the author's newline. An earlier version kept
// such a row's range empty, which read well and was wrong: the newline is a real
// byte of the block, and leaving it out of every range would mean the ranges no
// longer partition the text — the property the drag-selection mapping is built
// on.
func TestABlankLineIsARowShowingNothingThatStillCoversItsNewline(t *testing.T) {
	block := LayoutBlock(Block{ID: "msg:1", Text: "para one\n\npara two"}, LayoutOptions{Width: 40})
	if len(block.Rows) != 3 {
		t.Fatalf("want 3 rows (two paragraphs and the blank between), got %d: %q",
			len(block.Rows), rowsText(block.Rows))
	}
	blank := block.Rows[1]
	if got := blank.ContentText(); got != "" {
		t.Fatalf("the blank row shows %q, want nothing", got)
	}
	if got := blank.Range; got.Len() != 1 || block.Logical[got.Start:got.End] != "\n" {
		t.Fatalf("the blank row's range is %+v, want exactly the author's newline", got)
	}
	if !blank.HardBreak {
		t.Fatalf("the blank row ends at an author newline, so it is a hard break")
	}
	if got := reassembled(block.Rows); got != "para one\n\npara two" {
		t.Fatalf("reassembled %q, want the paragraphs kept apart", got)
	}
}

// An unmeasured width (0) must not wrap everything into single characters: a
// panel renders before it is measured, and shredding its text then would show a
// column of letters on the first frame.
func TestAnUnmeasuredWidthDoesNotWrap(t *testing.T) {
	const text = "a line that is longer than any zero-width budget could hold"
	block := LayoutBlock(Block{ID: "msg:1", Text: text}, LayoutOptions{Width: 0})
	if len(block.Rows) != 1 {
		t.Fatalf("unmeasured width produced %d rows, want 1", len(block.Rows))
	}
	if got := contentText(block.Rows); got != text {
		t.Fatalf("content = %q, want the text unchanged", got)
	}
}

// A layout whose frame consumes its whole width cannot honour a content
// budget: there are no content cells left, so every grapheme overflows.
// Flooring the budget at 1 pretended one cell still fit and produced the
// visible failure this task guards against — every row shredding into a
// column of single-cell fragments that ALSO overflowed. The honest choice,
// pinned here, is the minimum-overflow one: stop wrapping, one row per hard
// line, overflowing only as far as the author's own text forces.
func TestADegenerateBudgetDoesNotShredTheText(t *testing.T) {
	const text = "alpha beta gamma"
	block := LayoutBlock(Block{ID: "msg:1", Text: text}, LayoutOptions{Width: 4, Indent: 4})
	if len(block.Rows) != 1 {
		t.Fatalf("degenerate layout produced %d rows, want one per hard line: %q",
			len(block.Rows), rowsText(block.Rows))
	}
	if got := reassembled(block.Rows); got != text {
		t.Fatalf("reassembled %q, want %q", got, text)
	}

	// One content cell left is NOT degenerate: the budget is honoured, every
	// row fits the width, and the wrap is still a wrap rather than a shred.
	wrapped := LayoutBlock(Block{ID: "msg:1", Text: text}, LayoutOptions{Width: 5, Indent: 4})
	if len(wrapped.Rows) < 4 {
		t.Fatalf("a single-cell budget stopped wrapping: %q", rowsText(wrapped.Rows))
	}
	for i, row := range wrapped.Rows {
		if row.Cells > 5 {
			t.Fatalf("row %d is %d cells, past the width of 5", i, row.Cells)
		}
	}
	if got := reassembled(wrapped.Rows); got != text {
		t.Fatalf("reassembled %q, want %q", got, text)
	}
}

// A row that would be broken inside a grapheme must not be: the break lands at
// a grapheme boundary, so a wide character is never split across two rows.
func TestWrappingNeverSplitsAGrapheme(t *testing.T) {
	const text = "日本語日本語日本語"
	for _, width := range []int{5, 6, 7, 8} {
		block := LayoutBlock(Block{ID: "msg:1", Text: text}, LayoutOptions{Width: width})
		for i, row := range block.Rows {
			if row.Range.Empty() {
				continue
			}
			start := SnapToBoundary(text, row.Range.Start)
			end := SnapToBoundary(text, row.Range.End)
			if start != row.Range.Start || end != row.Range.End {
				t.Fatalf("width %d row %d breaks inside a grapheme: %+v", width, i, row.Range)
			}
		}
		if got := reassembled(block.Rows); got != text {
			t.Fatalf("width %d reassembled %q, want %q", width, got, text)
		}
	}
}
