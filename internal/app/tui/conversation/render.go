package conversation

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// This file makes a rendered block describe ITSELF: the logical text it was
// made from, the display rows it occupies, and the mapping between them.
//
// The alternative — rendering to a string and reverse-engineering the string
// when someone clicks — is what this replaces. A rendered string has lost the
// information a selection needs: an ANSI escape is bytes but no cells, a tab is
// one byte and up to eight cells, a wide grapheme is several bytes and two
// cells, and a decoration the renderer inserted (a bullet, a gutter, a table
// border) is text that is on screen and NOT in the document. Substring-matching
// a rendered string back to source gets all four wrong, and gets them wrong
// silently.
//
// So the renderer emits both at once. Everything is derived from one walk of
// the logical text, and the invariants a consumer relies on are stated and
// tested here:
//
//   - Logical text is the block's text. Copy reads it, never the rows, so a
//     soft wrap cannot change what a copy yields.
//   - Display rows carry their logical range, so a drag maps to offsets.
//   - Decoration is marked, so a selection of table cells omits the borders
//     and a copy of code omits the gutter.
//
// The zero value of a decorative span's range is deliberately NOT used: a
// decoration carries Range{-1, -1}, because 0 is a real offset (the start of the
// text) and a decoration claiming it would make "start of block" mean two
// things.

// SpanKind classifies a span so a consumer can style it and so a selection can
// tell content from chrome.
type SpanKind int

const (
	// SpanPlain is unstyled logical text.
	SpanPlain SpanKind = iota
	// SpanEmphasis is text inside emphasis.
	SpanEmphasis
	// SpanStrong is text inside strong emphasis.
	SpanStrong
	// SpanCode is an inline code span.
	SpanCode
	// SpanLink is the visible label of a link. The target is NOT part of the
	// label: a reader copying a link label wants the words, and the URL is
	// preserved by the copy target that copies the original Markdown.
	SpanLink
	// SpanHeading is heading text.
	SpanHeading
	// SpanBullet is a list bullet the renderer inserted. Decoration.
	SpanBullet
	// SpanQuote is a blockquote marker the renderer inserted. Decoration.
	SpanQuote
	// SpanRule is a thematic break the renderer inserted. Decoration.
	SpanRule
	// SpanTableBorder is a table's frame and column separators. Decoration.
	SpanTableBorder
	// SpanTableSep is the separator the renderer inserts BETWEEN table cells,
	// in place of the border, so the row reads as a row of FIELDS. It is
	// generated, and it is in the readable text: a copy of a table row has to
	// carry field boundaries, and a bare "namevalue" is not the row anyone read.
	SpanTableSep
	// SpanIndent is the leading indent on a display row. Frame.
	SpanIndent
	// SpanSoftBreak is the space a soft wrap consumed. It occupies no cell and
	// shows nothing: it exists so the row's logical range can record that the
	// offset is accounted for rather than skipped invisibly.
	SpanSoftBreak
)

// Decorative reports whether this kind is text the RENDERER generated rather
// than text the author wrote.
//
// It is a statement about PROVENANCE, not about what a copy should contain, and
// the two are not the same thing. A list bullet and a table separator are both
// generated, and both belong in a copy: without the bullet a list reads as body
// copy, and without the separator a table row's fields run together into
// "namevalue". What must never reach a copy is the FRAME — an indent, a border,
// a rule — which is what Chrome reports.
//
// Keeping the two separate is what stops "don't copy the renderer's chrome" from
// being implemented as "don't copy anything the renderer made", which silently
// strips the structure out of every list and table a reader copies.
func (k SpanKind) Decorative() bool {
	switch k {
	case SpanBullet, SpanQuote, SpanRule, SpanTableBorder, SpanTableSep, SpanIndent, SpanSoftBreak:
		return true
	}
	return false
}

// Chrome reports whether this kind is frame that must not appear in readable
// text: an indent, a border, a rule, or the invisible residue of a soft wrap.
//
// A bullet, a quote marker and a table separator are deliberately NOT chrome.
// They are how the projection says "this is an item", "this was quoted" and
// "this is where the next field starts", so a copy without them is not a copy of
// what the reader read.
func (k SpanKind) Chrome() bool {
	switch k {
	case SpanRule, SpanTableBorder, SpanIndent, SpanSoftBreak:
		return true
	}
	return false
}

// Range is a half-open logical byte range.
//
// A negative Start marks "no logical text" — a pure decoration — because 0 is
// the start of the text and is therefore a real position.
type Range struct{ Start, End int }

// None is the range of a span that shows no logical text.
func None() Range { return Range{Start: -1, End: -1} }

// HasText reports whether this range refers to logical text at all.
func (r Range) HasText() bool { return r.Start >= 0 && r.End >= r.Start }

// Empty reports whether this range refers to no characters (which is not the
// same as referring to nothing).
func (r Range) Empty() bool { return r.HasText() && r.Start == r.End }

// Len is the number of logical bytes covered.
func (r Range) Len() int {
	if !r.HasText() {
		return 0
	}
	return r.End - r.Start
}

// Run styles a logical range of a Spans' text.
//
// Runs must be ordered and non-overlapping, and must lie within the text. Text
// not covered by a run is SpanPlain. Overlapping runs are rejected at the
// boundary by normalizeRuns rather than resolved arbitrarily: a renderer that
// produced overlaps has a bug, and picking a winner here would hide it.
type Run struct {
	Range Range
	Kind  SpanKind
}

// Spans is a styled projection of one block's logical text.
//
// Text is what a copy of this projection must yield, byte for byte. Runs style
// it. Nothing else is stored: the display form is derived, so there is exactly
// one source of truth for the content.
type Spans struct {
	Text string
	Runs []Run
}

// Span is one run of display text on one row.
type Span struct {
	// Text is DISPLAY text. A tab is expanded to the spaces the terminal would
	// render it as, so Cells is meaningful without knowing the column the span
	// opens at.
	Text string
	// Logical is the text this span CONTRIBUTES to readable text, which is the
	// same as Text for everything except a tab.
	//
	// Both forms are carried because they genuinely differ and both are used: the
	// display form is what a renderer writes (eight spaces of indent line up), and
	// the logical form is what a reader is owed (a copied table row is separated
	// by tabs, not by eight spaces that happen to look like a gap). Deriving one
	// from the other is lossy — expanding is easy and un-expanding is a guess —
	// so the span keeps both rather than picking one and hoping.
	//
	// The soft-wrap residue is the one span whose Logical is empty: it occupies
	// no cells and shows nothing, and its byte is restored by the row's
	// Separator instead. Carrying it in both places would double the space.
	Logical string
	// Kind classifies it.
	Kind SpanKind
	// Range is the logical text this span covers. Frame carries None(), because
	// an indent covers no byte of the author's text.
	Range Range
	// Cells is Text's display width.
	Cells int
}

// Decorative reports whether this span is chrome rather than content.
func (s Span) Decorative() bool { return s.Kind.Decorative() }

// DisplayRow is one rendered row.
type DisplayRow struct {
	// Spans are the row's spans, left to right.
	Spans []Span
	// Range bounds the LOGICAL text visible on this row, excluding the
	// decoration the renderer inserted. A row whose content is empty (a blank
	// line between paragraphs) has Empty() true.
	Range Range
	// HardBreak is true when the author's own newline ends this row, false when
	// the renderer wrapped it. The distinction is the whole reason a soft wrap
	// can never enter copied content while an original break does.
	HardBreak bool
	// ConsumedSpace is true when this row was soft-wrapped at a space, so the
	// space is in the row's logical range but is not displayed. It is what lets
	// text be read back off the rows at all: without it, joining rows would
	// either glue two words together or invent a separator that was not there.
	ConsumedSpace bool
	// Cells is the total display width, including decoration.
	Cells int
}

// Separator is the text a reader sees BETWEEN this row and the one after it.
//
// It is the rule for reading text back off a display form, and it encodes the
// plan's central rendering invariant: a soft wrap contributes a space (or
// nothing, when the wrap fell mid-token), and only the AUTHOR's break
// contributes a newline. A caller extracting a selection joins rows with this,
// which is why it lives here rather than in the caller: the alternative — a
// caller joining with "\n" and later trying to undo the wraps — cannot tell an
// invented newline from a real one.
func (row DisplayRow) Separator() string {
	if row.HardBreak {
		return "\n"
	}
	if row.ConsumedSpace {
		return " "
	}
	return ""
}

// RenderedBlock is a block laid out at a width, with the mapping a consumer
// needs to hit-test and to extract text.
type RenderedBlock struct {
	BlockID  BlockID
	Revision int
	// Width is the budget this was laid out for. A row never exceeds it except
	// for an unbreakable grapheme wider than the budget, which is recorded
	// rather than truncated so the caller can decide.
	Width int
	// Logical is the block's logical text, unchanged by layout.
	Logical string
	// Rows are the display rows.
	Rows []DisplayRow
}

// LogicalText returns the text a copy must yield.
//
// It is the block's own text, so layout cannot affect it: no wrapping, no
// decoration, no trimming. A caller that wants "what is selected" slices this,
// and a caller that wants "what is on row N" reads Rows.
func (r RenderedBlock) LogicalText() string { return r.Logical }

// LayoutOptions are the layout budget and policy for one render.
type LayoutOptions struct {
	// Width is the total cell budget for a row, including Indent.
	Width int
	// Indent is the decoration prefixed to every row, in cells. Continuation
	// rows are indented too, so a wrapped line reads as one item.
	Indent int
	// Breakpoints are the characters a wrap may break after when there is no
	// space. Empty means "break only at spaces, or mid-token as a last
	// resort".
	Breakpoints string
	// TabStop is the column interval a tab advances to. Zero means the
	// standard tab stop.
	TabStop int
}

// rowBudget is the cell budget available to content on a row.
//
// A width at or below the indent leaves NO content cells, and flooring the
// result at 1 would be a lie the layout then breaks: every grapheme would
// overflow (the shredding-into-fragments failure), because "one cell" is a
// budget nothing but a narrow grapheme can honour. The degenerate case
// instead reports 0 — the same signal an unmeasured width uses — so the
// wrapper stops breaking and lets each hard line take the overflow it
// genuinely cannot avoid. One overflowing row is the minimum-overflow
// outcome; a column of overflowing fragments is not.
func (o LayoutOptions) rowBudget() int {
	if o.Width <= 0 {
		return 0 // unmeasured: never wrap
	}
	return o.Width - o.Indent
}

// tabStop returns the effective tab interval.
func (o LayoutOptions) tabStop() int {
	if o.TabStop <= 0 {
		return tabStop
	}
	return o.TabStop
}

// Layout turns styled logical text into display rows.
//
// Wrapping happens AFTER spans exist, which is the ordering the spans make
// possible: the alternative (wrap first, then style by searching) cannot keep
// an inline code span intact across a wrap boundary, because after wrapping
// there is no span to preserve.
//
// A soft wrap consumes the whitespace it breaks at for DISPLAY purposes, but
// the consumed bytes are recorded as a SpanSoftBreak so the row's logical range
// still accounts for them. Copy reads Logical, so nothing is lost either way;
// recording it keeps the ranges honest for a caller that walks rows.
func Layout(sp Spans, opts LayoutOptions) []DisplayRow {
	text := sp.Text
	runs := normalizeRuns(sp.Runs, len(text))
	budget := opts.rowBudget()
	ts := opts.tabStop()

	// Hard lines are the author's own breaks. A trailing newline does not
	// produce a final empty row: it ends the last row.
	//
	// The newline byte belongs to the range of the line it ends, not to the
	// next line. That is what makes the ranges a partition of the text: every
	// byte is accounted for exactly once, which a consumer walking rows for a
	// selection depends on. The newline contributes no span, so it is never
	// displayed.
	var hardRanges []Range
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			hardRanges = append(hardRanges, Range{start, i + 1})
			start = i + 1
		}
	}
	if start < len(text) {
		hardRanges = append(hardRanges, Range{start, len(text)})
	}

	var rows []DisplayRow
	for _, hr := range hardRanges {
		for _, sr := range wrapRange(text, hr, budget, opts.Breakpoints, ts) {
			rows = append(rows, buildRow(text, sr, runs, opts.Indent, ts, hr.End))
		}
	}
	return rows
}

// wrapRange splits one hard line into soft rows of at most budget cells.
//
// It returns ranges that are contiguous over the line: each row starts where
// the previous ended, including the whitespace a break consumed. Keeping the
// ranges contiguous means a caller can map a row index to an offset and back
// without a gaps table, and the whitespace is marked decorative by the row
// builder rather than dropped from the range arithmetic.
func wrapRange(text string, line Range, budget int, breakpoints string, ts int) []Range {
	if budget <= 0 {
		return []Range{line}
	}
	if line.End <= line.Start {
		return []Range{{line.Start, line.Start}}
	}
	var out []Range
	pos := line.Start
	for pos < line.End {
		end := rowEnd(text, pos, line.End, budget, breakpoints, ts)
		out = append(out, Range{pos, end})
		pos = end
	}
	if len(out) == 0 {
		out = append(out, Range{line.Start, line.Start})
	}
	return out
}

// rowEnd finds where the row starting at pos must end.
//
// Two rules, in order: prefer the last break opportunity that fits, otherwise
// fill to the budget and break mid-token. The first rule is what keeps a path
// or a URL whole when it fits; the second is what keeps a 300-character token
// from overflowing a 300-column row into nothing.
//
// A break opportunity is a space, or a breakpoint character the layout was
// given. Either way the break lands AFTER the character, so the character stays
// on the row it ends rather than starting the next one — which is what makes
// "--flag" break before the flag and not after the hyphen that introduces it.
func rowEnd(text string, pos, limit, budget int, breakpoints string, ts int) int {
	col := 0
	lastBreak := -1
	i := pos
	for i < limit {
		g, cells := graphemeAt(text, i, col, ts)
		if col+cells > budget && i > pos {
			if lastBreak > pos {
				return lastBreak
			}
			return i
		}
		col += cells
		i += len(g)
		if isBreakOpportunity(text, i, limit, breakpoints) {
			lastBreak = i
		}
	}
	// The rest of the line fits. The last break opportunity is deliberately
	// NOT used here: breaking a line that fits would split "four five" into
	// two rows at a width that has room for both, and every wrap would then
	// end short of its budget.
	return limit
}

// isBreakOpportunity reports whether a break is allowed at offset i, which is
// just past a character.
func isBreakOpportunity(text string, i, limit int, breakpoints string) bool {
	if i <= 0 || i > limit {
		return false
	}
	prev := text[i-1]
	if prev == ' ' {
		return true
	}
	if breakpoints == "" {
		return false
	}
	return strings.IndexByte(breakpoints, prev) >= 0
}

// graphemeAt returns the grapheme starting at i and the cells it occupies from
// column col.
//
// A tab is the reason this needs the column: its width is not a property of the
// tab, it is the distance to the next stop from wherever the cursor is.
func graphemeAt(text string, i, col, ts int) (string, int) {
	if text[i] == '\t' {
		return "\t", ts - col%ts
	}
	if text[i] == '\n' {
		return "\n", 0
	}
	g, w := ansi.FirstGraphemeCluster(text[i:], ansi.GraphemeWidth)
	if w < 0 {
		w = 0
	}
	return g, w
}

// buildRow turns a logical range into a display row: an indent, the styled
// content, and the break marker.
//
// Content is split into spans at run boundaries and at tabs. A tab is its own
// span even when surrounded by text of the same kind, because its DISPLAY text
// differs from its logical text — it expands to spaces — and a span whose Text
// and Range disagree in length must be alone for the mapping to stay derivable.
func buildRow(text string, r Range, runs []Run, indent int, ts int, hardEnd int) DisplayRow {
	row := DisplayRow{Range: r, HardBreak: r.End >= hardEnd}
	if indent > 0 {
		row.Spans = append(row.Spans, Span{
			Text: strings.Repeat(" ", indent), Logical: strings.Repeat(" ", indent),
			Kind: SpanIndent, Range: None(), Cells: indent,
		})
		row.Cells += indent
	}
	if !r.HasText() || r.Empty() {
		return row
	}

	// The row is walked once to find where its spans END, then sliced. Building
	// each span by appending a grapheme at a time — the obvious way — is
	// quadratic in the span's length, because every append reallocates the
	// growing string: measured on a fenced code block, that single choice was
	// 99% of the allocations in the whole layout, and reflow cost as much as a
	// full render. Slicing the source costs nothing, because a substring of a
	// string is a pointer and a length.
	col := indent
	// segStart is where the current span begins; segKind is what it is. A tab
	// and the residue of a soft wrap always break the span, in both directions:
	// a tab's display text and its logical range have different lengths, and a
	// span whose two lengths disagree cannot be merged with a neighbour without
	// making the mapping underivable.
	cur := newRunCursor(runs, r.Start)
	segStart := r.Start
	segKind := cur.kindAt(r.Start)
	segTab := false
	// spanStartCol is the column the span being built begins at, so its cell
	// count can be taken as the difference rather than accumulated separately.
	spanStartCol := indent

	flush := func(end int) {
		if end <= segStart {
			return
		}
		logical := text[segStart:end]
		display := logical
		if segTab {
			display = expandSpanTabs(logical, spanStartCol, ts)
		}
		row.Spans = append(row.Spans, Span{
			Text: display, Logical: logical, Kind: segKind,
			Range: Range{segStart, end}, Cells: col - spanStartCol,
		})
	}

	i := r.Start
	for i < r.End {
		g, cells := graphemeAt(text, i, col, ts)
		if g == "\n" {
			// The newline is inside the row's range — so the ranges partition
			// the text — but contributes nothing to the display.
			if i > segStart {
				flush(i)
			}
			i += len(g)
			segStart = i
			segKind = cur.kindAt(min(i, max(len(text)-1, 0)))
			spanStartCol = col
			continue
		}
		if g == " " && i == r.End-1 && !row.HardBreak {
			// A space the WRAP consumed is in the range but shows nothing: the
			// terminal does not display a trailing space, and rendering one
			// would make the display text disagree with the cell count. The
			// consumed byte is still accounted for — ConsumedSpace is what puts
			// an equivalent space back when the rows are read as text.
			//
			// Only a SOFT break does this. A row ending at the author's own
			// newline may legitimately end with spaces, and those are the
			// author's bytes: eliding them would silently delete trailing
			// whitespace from the middle of a block's text.
			if i > segStart {
				flush(i)
			}
			row.ConsumedSpace = true
			row.Spans = append(row.Spans, Span{
				Text: "", Logical: "", Kind: SpanSoftBreak,
				Range: Range{i, i + 1}, Cells: 0,
			})
			i++
			segStart = i
			spanStartCol = col
			if i < r.End {
				segKind = cur.kindAt(i)
			}
			segTab = false
			continue
		}

		kind := cur.kindAt(i)
		tab := g == "\t"
		if tab || segTab || kind != segKind {
			if i > segStart {
				flush(i)
			}
			segStart = i
			segKind = kind
			segTab = tab
			spanStartCol = col
		}
		col += cells
		i += len(g)
	}
	flush(r.End)
	row.Cells = col
	return row
}

// expandSpanTabs turns the tabs inside one span into the spaces the terminal
// would render them as, starting from the column the span begins at.
//
// The starting column is needed because a tab's width is the distance to the
// next stop, which depends on where it is — so the same tab is eight spaces at
// the start of a line and one space after seven characters.
func expandSpanTabs(s string, startCol, ts int) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + ts)
	col := startCol
	for i := 0; i < len(s); {
		if s[i] == '\t' {
			pad := ts - col%ts
			b.WriteString(strings.Repeat(" ", pad))
			col += pad
			i++
			continue
		}
		g, w := ansi.FirstGraphemeCluster(s[i:], ansi.GraphemeWidth)
		b.WriteString(g)
		col += w
		i += len(g)
	}
	return b.String()
}

// canExtend reports whether a new grapheme of this kind may join the span.
// Any span carrying logical text can grow; a span whose Range is None cannot,
// because its End has nowhere to advance to.
func canExtend(s Span, kind SpanKind) bool {
	if s.Kind != kind {
		return false
	}
	if s.Kind.Chrome() {
		return false
	}
	return s.Range.HasText()
}

// kindAt returns the kind of the run covering offset i, or SpanPlain.
//
// Runs are normalized (ordered, non-overlapping, in range) before this is
// called, so the first covering run is the only one. The linear scan is
// deliberate here and NOT used on the layout hot path: buildRow walks offsets in
// ascending order and uses runCursor instead, because calling this per grapheme
// measured as 13% of a reflow's CPU.
func kindAt(runs []Run, i int) SpanKind {
	for _, r := range runs {
		if i >= r.Range.Start && i < r.Range.End {
			return r.Kind
		}
	}
	return SpanPlain
}

// runCursor walks runs alongside an ascending offset walk.
//
// Runs are sorted by start and non-overlapping, so an offset walk only ever
// moves forward through them: there is no need to look at a run that has already
// ended, and no need to look at one that has not begun. It is the difference
// between O(bytes × runs) and O(bytes + runs), which matters because the byte
// count is a whole code block and the run count is every styled fragment in it.
type runCursor struct {
	runs []Run
	i    int
}

// newRunCursor positions a cursor at the run covering offset off.
func newRunCursor(runs []Run, off int) runCursor {
	c := runCursor{runs: runs}
	for c.i < len(c.runs) && c.runs[c.i].Range.End <= off {
		c.i++
	}
	return c
}

// kindAt returns the kind at offset off, advancing the cursor as needed.
func (c *runCursor) kindAt(off int) SpanKind {
	for c.i < len(c.runs) && off >= c.runs[c.i].Range.End {
		c.i++
	}
	if c.i >= len(c.runs) {
		return SpanPlain
	}
	if off >= c.runs[c.i].Range.Start {
		return c.runs[c.i].Kind
	}
	// A gap between runs (an offset no run covers) is plain text.
	return SpanPlain
}

// normalizeRuns makes an arbitrary run list usable: out-of-range and empty
// runs are dropped, survivors are clamped to the text, the list is SORTED by
// start (a caller that produced runs while walking an AST backwards must not
// get a projection that silently drops half its input), and overlaps are
// resolved deterministically instead of by drop order.
//
// Malformed runs are DROPPED rather than rejected: this is a render path, and a
// renderer that produced a bad run must still produce readable output rather
// than nothing. Dropping means the affected bytes render as plain text, which is
// the projection that was asked for minus styling — visible, selectable and
// correct, just not decorative.
//
// An overlap keeps the EARLIER run whole and trims the later one's overlapping
// prefix, so the later run only styles the bytes the earlier one left. The
// alternative — dropping the later run outright — threw away styling for
// entirely unclaimed text whenever one run merely grazed another, and the
// affected bytes dropped out of every styled projection with no trace of why.
// Trimming keeps every survivor's kind on the bytes it actually owns, which is
// what kindAt's first-covering-run scan and runCursor's monotone walk both
// rely on.
func normalizeRuns(runs []Run, textLen int) []Run {
	if len(runs) == 0 {
		return nil
	}
	clamped := make([]Run, 0, len(runs))
	for _, r := range runs {
		if !r.Range.HasText() || r.Range.Empty() {
			continue
		}
		if r.Range.Start < 0 {
			continue
		}
		if r.Range.End > textLen {
			r.Range.End = textLen
		}
		if r.Range.End <= r.Range.Start {
			continue
		}
		clamped = append(clamped, r)
	}
	// Lower start wins; ties break by the longer run, then by kind, so the
	// order of equal runs is a function of their content and not of the order
	// they arrived in.
	sortRuns(clamped)
	out := make([]Run, 0, len(clamped))
	for _, r := range clamped {
		if n := len(out); n > 0 {
			prev := &out[n-1]
			if r.Range.Start < prev.Range.End {
				// Overlapping: keep the earlier run, trim the overlap.
				if r.Range.End <= prev.Range.End {
					continue // fully covered by the earlier run
				}
				r.Range.Start = prev.Range.End
			}
			if r.Range.Start == prev.Range.End && r.Kind == prev.Kind {
				prev.Range.End = r.Range.End
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// sortRuns orders runs by start, then by the longer range, then by kind, so
// an equal pair always normalizes the same way regardless of input order.
func sortRuns(runs []Run) {
	// Insertion sort: run lists are short (a projection's styling fragments),
	// and this keeps the sort itself allocation-free.
	for i := 1; i < len(runs); i++ {
		for j := i; j > 0 && lessRun(runs[j], runs[j-1]); j-- {
			runs[j], runs[j-1] = runs[j-1], runs[j]
		}
	}
}

// lessRun is sortRuns' ordering.
func lessRun(a, b Run) bool {
	if a.Range.Start != b.Range.Start {
		return a.Range.Start < b.Range.Start
	}
	if a.Range.End != b.Range.End {
		return a.Range.End > b.Range.End // the longer run first, so it wins any overlap
	}
	return a.Kind < b.Kind
}

// OffsetAt maps a display cell on a row to a logical offset.
//
// Decoration is skipped rather than mapped: a click on a table border names the
// text that follows it, so a drag that starts on a border still selects the cell
// the reader was aiming at. A cell past the row's content names the end of the
// row's logical range, which is a valid place for a cursor.
func (r RenderedBlock) OffsetAt(rowIndex, cell int) int {
	if rowIndex < 0 || rowIndex >= len(r.Rows) {
		return len(r.Logical)
	}
	row := r.Rows[rowIndex]
	col := 0
	// lastTextEnd is the end of the last span that showed anything. A row can
	// end with spans that show nothing — the space a soft wrap consumed — and
	// those share their cell with the visible end of the line: a click at cell
	// N is both "just after the last visible character" and "on the invisible
	// space". Naming the EARLIEST offset at a cell resolves that in favour of
	// the visible text, so the cell the reader clicked on and the offset a copy
	// cuts at agree. Falling through to the row's end instead would report the
	// invisible space's offset, which is one byte past where the reader is
	// looking.
	lastTextEnd := -1
	for _, s := range row.Spans {
		if !s.Range.HasText() {
			// Frame (an indent) occupies cells but names no offset: a click on
			// it belongs to the text after it.
			col += s.Cells
			continue
		}
		// Only a span that actually OCCUPIES cells advances the visible end. A
		// zero-cell span — the invisible residue of a soft wrap — sits at the
		// same column as the end of the text before it, so letting it claim the
		// visible end would report an offset one byte past what a reader can
		// see, which is the bug this whole branch exists to avoid.
		if s.Cells > 0 {
			lastTextEnd = s.Range.End
		}
		if cell < col+s.Cells {
			// One conversion, and it must be from CELLS to LOGICAL BYTES of
			// the span's own logical text. Converting the cell to an offset in
			// the display text and then treating that offset as a cell count
			// again is a double conversion, and it is wrong wherever the two
			// lengths differ — which is every wide grapheme and every tab.
			within := cell - col
			return s.Range.Start + logicalLenForCells(r.Logical[s.Range.Start:s.Range.End], within, tabStop, col)
		}
		col += s.Cells
	}
	if lastTextEnd >= 0 {
		return lastTextEnd
	}
	if !row.Range.HasText() {
		return 0
	}
	return row.Range.End
}

// CellAt maps a logical offset to the cell it is displayed at on its row.
//
// It reports false when the offset is not displayed at all — past the end of
// the block, or inside a region the renderer did not map. A caller must be able
// to tell "here, at column 0" from "nowhere", and returning 0 for both is how a
// selection jumps to the left margin.
func (r RenderedBlock) CellAt(rowIndex, off int) (int, bool) {
	if rowIndex < 0 || rowIndex >= len(r.Rows) {
		return 0, false
	}
	row := r.Rows[rowIndex]
	col := 0
	for _, s := range row.Spans {
		if !s.Range.HasText() {
			col += s.Cells
			continue
		}
		if off >= s.Range.Start && off <= s.Range.End {
			// Walk the span's LOGICAL text, not its display text: for a tab
			// the logical byte is one and the displayed cells are seven, so
			// measuring the display string would report the wrong column and
			// every offset after a tab on that row would be misplaced.
			within := off - s.Range.Start
			return col + cellsForLogicalLen(r.Logical[s.Range.Start:s.Range.End], within, tabStop, col), true
		}
		col += s.Cells
	}
	return 0, false
}

// textAcross extracts the text covering a display range, joining rows by the
// separator each one declares.
//
// This is the primitive a selection copy is built on (Task 12): a drag names a
// start row/cell and an end row/cell, and what lands on the clipboard must be
// the LOGICAL text between them — no soft-wrap newline, no indent, no border,
// and with the whitespace a wrap consumed put back where it belongs.
//
// The range is half-open and normalizes a reversed pair, so a drag backwards
// yields the same text as a drag forwards. Cells are clamped to the block's own
// rows, so a drag that ran off either end stops at the end rather than
// panicking.
//
// Unexported: the selection path resolves text from the LOGICAL offsets the
// mapping yields (Selection.Text), and no caller outside this package needs
// the row/cell form.
func (r RenderedBlock) textAcross(startRow, startCell, endRow, endCell int) string {
	if startRow > endRow || (startRow == endRow && startCell > endCell) {
		startRow, endRow = endRow, startRow
		startCell, endCell = endCell, startCell
	}
	startRow = clampRow(startRow, len(r.Rows))
	endRow = clampRow(endRow, len(r.Rows))
	if len(r.Rows) == 0 {
		return ""
	}
	from := r.OffsetAt(startRow, startCell)
	to := r.OffsetAt(endRow, endCell)
	if to < from {
		to = from
	}
	if from < 0 {
		from = 0
	}
	if to > len(r.Logical) {
		to = len(r.Logical)
	}
	return r.Logical[from:to]
}

// clampRow bounds a row index to the block.
func clampRow(i, n int) int {
	if i < 0 {
		return 0
	}
	if i > n-1 {
		return max(n-1, 0)
	}
	return i
}

// LayoutBlock lays a block's text out at a width.
//
// It is the entry point a consumer uses when all it has is a block, and it is
// deliberately thin: the projection is the block's own text until a Markdown
// projection (which supplies styling runs) is available, so a block that has no
// styled form still gets correct rows and a correct mapping rather than
// falling back to an unmapped string.
func LayoutBlock(b Block, opts LayoutOptions) RenderedBlock {
	return RenderedBlock{
		BlockID:  b.ID,
		Revision: b.Revision,
		Width:    opts.Width,
		Logical:  b.Text,
		Rows:     Layout(Spans{Text: b.Text}, opts),
	}
}

// RowForOffset returns the index of the row displaying a logical offset, and
// whether one does.
func (r RenderedBlock) RowForOffset(off int) (int, bool) {
	for i, row := range r.Rows {
		if !row.Range.HasText() {
			continue
		}
		if off >= row.Range.Start && off <= row.Range.End {
			return i, true
		}
	}
	return 0, false
}

// RowsText returns the display text of a row, decoration included, for a caller
// that needs the literal glyphs (a golden fixture, or a screen assertion).
func (row DisplayRow) Text() string {
	var b strings.Builder
	for _, s := range row.Spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

// ContentText returns the row's LOGICAL text without frame: the readable
// projection of the document itself.
//
// It reads each span's Logical rather than its Text, which is what makes a table
// separator a tab here and eight spaces on screen. Both are correct for their
// purpose and they are not interchangeable.
//
// Structural markers survive — a bullet, a quote marker, a table separator —
// because they are part of what was read. Only the frame is dropped.
func (row DisplayRow) ContentText() string {
	var b strings.Builder
	for _, s := range row.Spans {
		if s.Kind.Chrome() {
			continue
		}
		b.WriteString(s.Logical)
	}
	return b.String()
}

// logicalLenForCells returns the number of LOGICAL bytes a display position
// within a span covers. It is the tab-aware direction of the mapping: eight
// display cells can be one logical byte.
//
// startCol is the column the span BEGINS at, accumulated by the caller while
// walking the row. A tab's width is the distance to the next stop from where
// it lands, so a span at column 13 measures its tab from 13 and not from 0;
// seeding the walk at the span's own column is what makes a tab in a table's
// second field — or anywhere on an indented, wrapped or quoted row — map to
// the cell it is actually drawn at. Callers must pass the accumulated column,
// not 0: the two lengths a span carries are not enough on their own.
func logicalLenForCells(logical string, cells, ts, startCol int) int {
	if cells <= 0 {
		return 0
	}
	col := startCol
	i := 0
	for i < len(logical) {
		g, w := graphemeAt(logical, i, col, ts)
		if col+w > cells+startCol {
			return i
		}
		col += w
		i += len(g)
	}
	return len(logical)
}

// cellsForLogicalLen returns the display cells a number of LOGICAL bytes
// occupies. It is logicalLenForCells' inverse, and both are needed because a
// span's display and logical lengths differ wherever a tab appears.
//
// startCol is the column the span BEGINS at: a tab's expansion is measured to
// the next stop from the column it lands at, not from the span's own start.
// Cells are returned as a WIDTH (a number of cells), so the caller adds them
// to the accumulated column; only the walk inside is column-aware.
func cellsForLogicalLen(logical string, length, ts, startCol int) int {
	if length <= 0 {
		return 0
	}
	if length > len(logical) {
		length = len(logical)
	}
	col := startCol
	i := 0
	for i < length {
		g, w := graphemeAt(logical, i, col, ts)
		col += w
		i += len(g)
	}
	return col - startCol
}
