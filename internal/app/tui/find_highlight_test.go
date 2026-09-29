// internal/app/tui/find_highlight_test.go — marking matches on the transcript
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
)

// These tests hold the highlight to the one rule that makes it trustworthy: the
// cells it paints come from the SAME mapping the offsets do. A highlight derived
// any other way drifts from the offsets by exactly the amount the two disagree,
// silently, and a reader marks the wrong characters while everything looks fine.

// findNeedle is the phrase the highlight fixture searches for. It is a phrase
// rather than a word so that a highlight which marked the whole row, or the
// first character, would not accidentally contain it.
const findNeedle = "distinctive needle"

// findHighlightModel returns a scrollable model with ONE FINAL answer containing
// the needle, and a search open on it.
//
// The answer is FINAL, and that is not incidental. Only a final answer is
// rendered through the mapped path that carries a cell mapping; narration, a
// user prompt and a system notice are drawn by renderers that produce no
// mapping at all. So the highlight has a real, bounded scope, and a fixture
// that used an ordinary message would be asserting a tint that cannot exist.
//
// That limitation is pinned separately, by
// TestFindHighlightIsHonestAboutBlocksItCannotTint.
func findHighlightModel(t *testing.T, query string) Model {
	t.Helper()
	m := findScrollableModel(t)
	m.state.AddMessageFinal(session.RoleAssistant,
		"prose before the "+findNeedle+" and prose after it",
		session.ContentTypeMarkdown)
	m.refreshViewport()
	m.openFind(query)
	if len(m.find.matches) == 0 {
		t.Fatalf("precondition: %q matched nothing", query)
	}
	return m
}

// The highlight must mark the matched characters and nothing else.
//
// The assertion is made against the RENDERED LINE, by cutting it at the cells
// the mapping names and checking the text underneath is the query. Reading the
// mapping back would only prove the mapping agrees with itself.
func TestFindHighlightMarksExactlyTheMatchedCells(t *testing.T) {
	m := findHighlightModel(t, findNeedle)
	match := m.find.matches[0]

	span, ok := m.mappedBlockSpan(match.Block)
	if !ok {
		t.Fatalf("no rendered mapping for the match's block %q", match.Block)
	}
	blockRow, ok := span.rendered.RowForOffset(match.Range.Start)
	if !ok {
		t.Fatalf("the match offset %d is on no row of the block", match.Range.Start)
	}
	transcriptRow := span.blockRow + blockRow

	start, end, ok := m.findHighlight(transcriptRow)
	if !ok {
		t.Fatal("the match's own row reports no highlight")
	}

	// The cells must be the SAME ones HighlightRange names for the same range.
	wantStart, wantEnd, wantOK := span.rendered.HighlightRange(blockRow, match.Range.Start, match.Range.End)
	if !wantOK {
		t.Fatal("precondition: HighlightRange reports nothing for the match's own range")
	}
	if start != wantStart || end != wantEnd {
		t.Fatalf("the highlight is cells [%d,%d), want [%d,%d)", start, end, wantStart, wantEnd)
	}
	if end <= start {
		t.Fatalf("the highlight covers %d cells", end-start)
	}

	// And the cells must name the matched TEXT on the drawn line, which is what
	// catches a mapping that is self-consistent but off by the gutter.
	lines := strings.Split(m.viewport.GetContent(), "\n")
	if transcriptRow >= len(lines) {
		t.Fatalf("the transcript has %d lines, no row %d", len(lines), transcriptRow)
	}
	got := ansi.Strip(ansi.Cut(lines[transcriptRow], start, end))
	if !strings.Contains(got, "distinctive") {
		t.Fatalf("cells [%d,%d) of the drawn line read %q, want the matched text", start, end, got)
	}
}

// A row that is not in the match's block must not be marked. Without this a
// marker would be drawn on every line of the transcript, which is the failure a
// zero-width highlight produces when a caller conflates "no highlight" with
// "highlight at cell 0".
func TestFindHighlightMarksOnlyTheMatchsRow(t *testing.T) {
	m := findHighlightModel(t, findNeedle)
	match := m.find.matches[0]
	span, _ := m.mappedBlockSpan(match.Block)

	marked := 0
	for row := 0; row < m.viewport.TotalLineCount(); row++ {
		if _, _, ok := m.findHighlight(row); ok {
			marked++
		}
	}
	if marked == 0 {
		t.Fatal("no row is marked at all")
	}
	if marked > span.rows {
		t.Fatalf("%d rows are marked, but the match's block occupies only %d", marked, span.rows)
	}
	// A row well outside the block must report nothing.
	for _, row := range []int{span.blockRow - 1, span.blockRow + span.rows} {
		if _, _, ok := m.findHighlight(row); ok {
			t.Fatalf("row %d is marked, but the match's block spans rows [%d,%d)",
				row, span.blockRow, span.blockRow+span.rows)
		}
	}
}

// Every match is reported on its row, and exactly one of them is the CURRENT
// one. A renderer given only the current match could never show a reader how
// many other places a query matched.
func TestFindMatchHighlightsReportEveryMatchAndMarkTheCurrent(t *testing.T) {
	m := findHighlightModel(t, findNeedle)
	match := m.find.matches[0]
	span, _ := m.mappedBlockSpan(match.Block)
	blockRow, _ := span.rendered.RowForOffset(match.Range.Start)
	row := span.blockRow + blockRow

	spans := m.findMatchHighlights(row)
	if len(spans) == 0 {
		t.Fatal("the row reports no matches at all")
	}
	current := 0
	for _, s := range spans {
		if s.current {
			current++
		}
	}
	if current != 1 {
		t.Fatalf("%d of %d spans are marked current, want exactly 1", current, len(spans))
	}
	// The current one must be the one the cursor names.
	marked := m.find.matches[0]
	start, _, _ := m.findHighlight(row)
	var found bool
	for _, s := range spans {
		if s.current && s.start == start && s.end == endOf(m, marked, row) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the current span does not match the cursor's match at cells [%d,...)", start)
	}
}

// endOf returns the end cell the mapping gives the cursor's match on a row.
func endOf(m Model, match conversation.FindMatch, transcriptRow int) int {
	span, ok := m.mappedBlockSpan(match.Block)
	if !ok {
		return -1
	}
	_, end, ok := span.rendered.HighlightRange(transcriptRow-span.blockRow, match.Range.Start, match.Range.End)
	if !ok {
		return -1
	}
	return end
}

// A match in a block that has no cell mapping is not tinted, and find must SAY
// so rather than reporting a highlight at some invented range.
//
// The scope is real: only a final answer is rendered through the mapped path, so
// a query that matches a user prompt, a narration line or a system notice can be
// jumped to (the jump uses the block spans, which cover every block) but cannot
// be tinted. Silently marking nothing would look like the search had failed;
// this asserts the two are distinguished.
func TestFindHighlightIsHonestAboutBlocksItCannotTint(t *testing.T) {
	m := findScrollableModel(t)
	m.state.AddMessage(session.RoleUser, "a prompt mentioning "+findNeedle, session.ContentTypePlain)
	m.refreshViewport()
	m.openFind(findNeedle)
	if len(m.find.matches) == 0 {
		t.Fatalf("precondition: %q matched nothing in the prompt", findNeedle)
	}
	match := m.find.matches[0]

	// The prompt is a real block the reader can jump to...
	row, ok := m.blockStartRow(match.Scroll)
	if !ok {
		t.Fatal("the prompt is not a scrollable block, so find cannot reach the match at all")
	}
	if !m.gotoFindMatch() {
		t.Fatal("the jump failed on a match in a prompt")
	}
	// The viewport clamps near the end of the transcript, so the expected row is
	// the same clamped target the jump computes — asserting the raw row would
	// fail for the LAST block even though the jump did exactly the right thing.
	want := clampOffset(row, m.viewport.Height(), m.viewport.TotalLineCount())
	if got := m.viewport.YOffset(); got != want {
		t.Fatalf("the jump scrolled to row %d, want %d", got, want)
	}
	// And the match's own row must be visible on screen, which is what the
	// reader actually needs — the offset alone could be clamped short of it.
	if row < want || row >= want+m.viewport.Height() {
		t.Fatalf("the match's row %d is not in the visible window [%d,%d)",
			row, want, want+m.viewport.Height())
	}

	// ...but it has no cell mapping, so there is nothing to tint and the
	// accessor must report that rather than a range.
	if _, ok := m.mappedBlockSpan(match.Block); ok {
		t.Skip("this renderer now produces a mapping for prompts; the highlight can cover it")
	}
	tinted := 0
	for r := row; r < row+3; r++ {
		if _, _, ok := m.findHighlight(r); ok {
			tinted++
		}
	}
	if tinted != 0 {
		t.Fatalf("%d rows in an unmapped block report a highlight", tinted)
	}
}

// The same block, once it IS a final answer, is tinted — which is what makes
// the test above a statement about the mapping rather than about find refusing
// to mark things.
func TestFindHighlightCoversAFinalAnswerButNotAPrompt(t *testing.T) {
	m := findScrollableModel(t)
	m.state.AddMessageFinal(session.RoleAssistant, "an answer mentioning "+findNeedle, session.ContentTypeMarkdown)
	m.state.AddMessage(session.RoleUser, "a prompt mentioning "+findNeedle, session.ContentTypePlain)
	m.refreshViewport()
	m.openFind(findNeedle)
	if len(m.find.matches) < 2 {
		t.Fatalf("precondition: got %d matches, want one in each block", len(m.find.matches))
	}

	var answer, prompt conversation.FindMatch
	for _, match := range m.find.matches {
		if _, ok := m.mappedBlockSpan(match.Block); ok {
			answer = match
		} else {
			prompt = match
		}
	}
	if answer.Block == "" || prompt.Block == "" {
		t.Fatalf("precondition: the fixture did not produce one mapped and one unmapped match: %+v", m.find.matches)
	}

	// The answer is tinted.
	span, _ := m.mappedBlockSpan(answer.Block)
	blockRow, _ := span.rendered.RowForOffset(answer.Range.Start)
	if _, _, ok := m.findHighlight(span.blockRow + blockRow); !ok {
		t.Fatal("a match in a final answer is not tinted")
	}
	// The prompt is not.
	promptRow, _ := m.blockStartRow(prompt.Scroll)
	if _, _, ok := m.findHighlight(promptRow); ok {
		t.Fatal("a match in an unmapped block is tinted")
	}
}

// A closed search marks nothing. A reader who pressed Esc must not still see
// highlights they can no longer navigate.
func TestFindHighlightIsEmptyWhenFindIsClosed(t *testing.T) {
	m := findHighlightModel(t, findNeedle)
	match := m.find.matches[0]
	span, _ := m.mappedBlockSpan(match.Block)
	blockRow, _ := span.rendered.RowForOffset(match.Range.Start)
	row := span.blockRow + blockRow
	if _, _, ok := m.findHighlight(row); !ok {
		t.Fatal("precondition: the open search marks nothing")
	}

	m.closeFind()

	if _, _, ok := m.findHighlight(row); ok {
		t.Fatal("a closed search still marks the transcript")
	}
	if spans := m.findMatchHighlights(row); len(spans) != 0 {
		t.Fatalf("a closed search still reports %d match spans", len(spans))
	}
}

// The highlight must survive a rebuild: the mapping is rebuilt on every refresh
// and the offsets are re-derived, so a highlight that only worked until the
// first reflow would be useless in a streaming conversation.
func TestFindHighlightSurvivesARebuild(t *testing.T) {
	m := findHighlightModel(t, findNeedle)
	match := m.find.matches[0]
	span, _ := m.mappedBlockSpan(match.Block)
	blockRow, _ := span.rendered.RowForOffset(match.Range.Start)
	row := span.blockRow + blockRow
	start, end, ok := m.findHighlight(row)
	if !ok {
		t.Fatal("precondition: the open search marks nothing")
	}

	m.state.AddMessage(session.RoleAssistant, "new output that changes nothing about the hit", session.ContentTypeMarkdown)
	m.refreshViewport()
	m.refreshFind()

	// The row may have moved; find the match's new row and check it is marked
	// with the same WIDTH, which is what would break if the two mappings drifted.
	match = m.currentFindMatchOrFail(t)
	span, ok = m.mappedBlockSpan(match.Block)
	if !ok {
		t.Fatal("the match's block lost its mapping after a rebuild")
	}
	newBlockRow, ok := span.rendered.RowForOffset(match.Range.Start)
	if !ok {
		t.Fatal("the match's offset is on no row after a rebuild")
	}
	newStart, newEnd, ok := m.findHighlight(span.blockRow + newBlockRow)
	if !ok {
		t.Fatal("the rebuilt row is not marked")
	}
	if newEnd-newStart != end-start {
		t.Fatalf("the highlight is now %d cells wide, was %d", newEnd-newStart, end-start)
	}
}

// The match must actually reach the SCREEN.
//
// Everything else here tests an accessor, and an accessor that is never called
// paints nothing: the whole highlight can be absent from the rendered transcript
// while every one of those tests passes. This test reads the viewport, which is
// what the reader sees.
func TestFindHighlightReachesTheRenderedTranscript(t *testing.T) {
	m := findScrollableModel(t)
	m.state.AddMessageFinal(session.RoleAssistant,
		"prose before the "+findNeedle+" and prose after it", session.ContentTypeMarkdown)
	m.refreshViewport()

	// A reader parked on the match, so it is inside the visible window.
	m.openFind(findNeedle)
	match := m.find.matches[0]
	span, ok := m.mappedBlockSpan(match.Block)
	if !ok {
		t.Fatal("no cell mapping for the match's block")
	}
	blockRow, _ := span.rendered.RowForOffset(match.Range.Start)
	row := span.blockRow + blockRow
	m.viewportFollow = false
	m.viewport.SetYOffset(clampOffset(row, m.viewport.Height(), m.viewport.TotalLineCount()))
	m.refreshViewport()

	lines := strings.Split(m.viewport.GetContent(), "\n")
	if row >= len(lines) {
		t.Fatalf("the transcript has %d lines, no row %d", len(lines), row)
	}
	before := lines[row]

	// The matched cells must carry a background that the plain line does not.
	start, end, ok := m.findHighlight(row)
	if !ok {
		t.Fatal("the accessor reports no highlight for the match's own row")
	}
	bg := backgroundEscape(ansi.Cut(before, start, end))
	if bg == "" {
		t.Fatalf("the matched cells carry no background: %q", ansi.Cut(before, start, end))
	}

	// The characters must be unchanged, and the marking must be the ONLY
	// difference. Comparing against the same row rendered with the search
	// CLOSED is what makes this an assertion about the highlight: comparing a
	// string with itself, or re-deriving the expectation from the same mapping
	// the highlight used, would pass whatever the renderer did.
	withHighlight := ansi.Strip(before)
	m.closeFind()
	m.refreshViewport()
	closed := strings.Split(m.viewport.GetContent(), "\n")
	if row >= len(closed) {
		t.Fatalf("the transcript lost row %d when the search closed", row)
	}
	plain := closed[row]
	if got := ansi.Strip(plain); got != withHighlight {
		t.Fatalf("the highlight changed the text:\n highlighted %q\n plain       %q", withHighlight, got)
	}
	// With the search closed the same cells carry no match background at all.
	if got := backgroundEscape(ansi.Cut(plain, start, end)); got != "" {
		t.Fatalf("the same cells still carry the match background %q with the search closed: %q",
			got, ansi.Cut(plain, start, end))
	}
}

// backgroundEscape returns the BACKGROUND colour a styled fragment carries, or
// "" when it carries none.
//
// It is extracted rather than hardcoded because the colour a match uses is a
// theme decision: asserting the literal code would fail the moment the palette
// changed, for a reason that has nothing to do with whether the highlight works.
//
// Two encodings have to be understood, which is why this cannot be a substring
// search for "48". lipgloss emits the SHORT form for the colours this theme
// uses — "\x1b[97;43m", foreground 97 and background 43 in one sequence, with no
// literal "48" anywhere — and the LONG form ("48;5;n") for colours that need it.
// Treating the short form as "no background" is exactly the mistake that makes a
// test pass while the highlight is invisible.
func backgroundEscape(s string) string {
	for _, seq := range strings.Split(s, "\x1b[")[1:] {
		end := strings.Index(seq, "m")
		if end < 0 {
			continue
		}
		params := strings.Split(seq[:end], ";")
		for i, p := range params {
			if p == "48" {
				return "48;" + strings.Join(params[i+1:], ";")
			}
		}
		// The short form: exactly two bare parameters are foreground and
		// background. A parameter list of any other length carries an extended
		// colour, which is handled above.
		if len(params) == 2 && params[0] != "" && params[1] != "" &&
			params[0] != "0" && params[0] != "39" && params[0] != "49" {
			return params[1]
		}
	}
	return ""
}

// The current match and the other matches must be DISTINGUISHABLE on screen.
//
// A reader needs both: which lines matched, and which hit "next" is counting
// from. One colour for both leaves them unable to tell, and the "3/7" in the
// status line is not on the line they are reading.
func TestFindPaintsTheCurrentMatchDifferently(t *testing.T) {
	m := findScrollableModel(t)
	// Two hits in one block, so both are on screen with the same styling
	// underneath them and only the match marking differs.
	m.state.AddMessageFinal(session.RoleAssistant,
		"one "+findNeedle+" and then another "+findNeedle+" later on",
		session.ContentTypeMarkdown)
	m.refreshViewport()
	m.openFind(findNeedle)
	if len(m.find.matches) < 2 {
		t.Fatalf("precondition: got %d matches, want 2", len(m.find.matches))
	}
	if m.find.matches[0].Block != m.find.matches[1].Block {
		t.Fatal("precondition: the two matches are not in the same block")
	}

	// Both spans on their row, with the cursor on the first.
	span, _ := m.mappedBlockSpan(m.find.matches[0].Block)
	row := -1
	for r := 0; r < len(span.rendered.Rows); r++ {
		spans := m.findMatchHighlights(span.blockRow + r)
		if len(spans) >= 2 {
			row = span.blockRow + r
		}
	}
	if row < 0 {
		t.Skip("the two matches do not share a display row at this width")
	}
	m.viewportFollow = false
	m.viewport.SetYOffset(clampOffset(row, m.viewport.Height(), m.viewport.TotalLineCount()))
	m.refreshViewport()
	lines := strings.Split(m.viewport.GetContent(), "\n")

	spans := m.findMatchHighlights(row)
	if len(spans) < 2 {
		t.Fatalf("only %d match spans on the row", len(spans))
	}
	currents := 0
	for _, sp := range spans {
		if sp.current {
			currents++
		}
	}
	if currents != 1 {
		t.Fatalf("%d of %d spans on the row are marked current, want exactly 1: %+v", currents, len(spans), spans)
	}

	// The two kinds must be PAINTED differently. This is asserted against the
	// painting function rather than against a cut of the rendered row, because
	// a cut carries the block's own styling too: a code span inside the block
	// has a background of its own, and reading it back as "the match's colour"
	// is how a comparison of two identical values looks like a passing test.
	plainLine := "some text here"
	other := highlightFindCells(plainLine, 0, 4, false)
	cur := highlightFindCells(plainLine, 0, 4, true)
	if other == cur {
		t.Fatalf("the current match and another match paint identically (%q)", other)
	}
	if bg := backgroundEscape(cur); bg == "" {
		t.Fatalf("the current match paints no background: %q", cur)
	}
	if bg := backgroundEscape(other); bg == "" {
		t.Fatalf("another match paints no background: %q", other)
	}
	// And the current match's own background must be the one that appears in the
	// rendered row, so the painting function and the renderer agree on which
	// colour belongs to the cursor's hit.
	styled := lines[row]
	currentSpan := spans[0]
	if !currentSpan.current {
		currentSpan = spans[1]
	}
	seg := ansi.Cut(styled, currentSpan.start, currentSpan.end)
	if !strings.Contains(seg, backgroundEscape(cur)) {
		t.Fatalf("the cursor's match is rendered as %q, want the current-match background %q",
			seg, backgroundEscape(cur))
	}
}

// A closed search paints nothing, even with results still in hand.
//
// The guard is the `open` check, not the emptiness of the result list, and this
// pins it directly: closeFind clears the results, so a test that only pressed
// Esc would pass with the guard removed. A future caller that keeps results
// around for a "re-open last search" affordance would then tint the transcript
// with nothing on screen to navigate.
func TestFindHighlightRequiresTheOpenFlagNotMerelyResults(t *testing.T) {
	m := findHighlightModel(t, findNeedle)
	match := m.find.matches[0]
	span, _ := m.mappedBlockSpan(match.Block)
	blockRow, _ := span.rendered.RowForOffset(match.Range.Start)
	row := span.blockRow + blockRow
	kept := m.find.matches

	m.closeFind()
	if m.find.open {
		t.Fatal("precondition: find is still open")
	}
	// Put the results back with the search closed, which is the state the guard
	// exists for.
	m.find.matches = kept

	if _, _, ok := m.findHighlight(row); ok {
		t.Fatal("a closed search marks the transcript when results are still in hand")
	}
	if spans := m.findMatchHighlights(row); len(spans) != 0 {
		t.Fatalf("a closed search reports %d match spans with results in hand", len(spans))
	}
}

// The status line must say which surface owns the keys while find is open, and
// it must say it in the left chrome rather than as a transient notice: a reader
// typing a query needs to see the count update, not a message that fades.
func TestFindStatusAppearsInTheStatusLine(t *testing.T) {
	m := findHighlightModel(t, findNeedle)

	segs := m.statusLeftSegments()
	var joined string
	for _, s := range segs {
		joined += " " + ansi.Strip(s.text)
	}
	if !strings.Contains(joined, "find") {
		t.Fatalf("the status line does not mention find: %q", joined)
	}
	if !strings.Contains(joined, findNeedle) {
		t.Fatalf("the status line does not name the query: %q", joined)
	}

	m.closeFind()
	joined = ""
	for _, s := range m.statusLeftSegments() {
		joined += " " + ansi.Strip(s.text)
	}
	if strings.Contains(joined, "find ") {
		t.Fatalf("the status line still mentions find after it was closed: %q", joined)
	}
}

// A selection must report itself too. It is the same kind of reading state, and
// a reader who dragged a phrase needs to know the app registered it — otherwise
// the only cue is the highlight, which is invisible under NO_COLOR.
func TestSelectionStatusAppearsInTheStatusLine(t *testing.T) {
	m := findScrollableModel(t)
	// A keyboard selection begins at the block under the top of the viewport,
	// so the reader must be parked on one that HAS a cell mapping — a prompt is
	// drawn by a renderer that produces none, and selecting in it is not
	// possible at all. Choosing the row by looking for a mapped block is what
	// makes this fixture independent of where the filler happened to wrap.
	span, ok := m.mappedBlockSpanForTest()
	if !ok {
		t.Fatal("precondition: the fixture has no mapped block to select in")
	}
	m.viewportFollow = false
	m.viewport.SetYOffset(span.blockRow)
	m.beginSelectionAtReadingAnchor()
	for i := 0; i < 5; i++ {
		m.extendSelectionByKey(selectionKeyRight)
	}
	if !m.hasSelection() {
		t.Fatal("precondition: no selection was made")
	}

	var joined string
	for _, s := range m.statusLeftSegments() {
		joined += " " + ansi.Strip(s.text)
	}
	if !strings.Contains(joined, "selected") {
		t.Fatalf("the status line does not report the selection: %q", joined)
	}
}

// mappedBlockSpanForTest returns the first rendered block that carries a cell
// mapping, which is the only kind of block a selection can be made in.
func (m Model) mappedBlockSpanForTest() (renderedBlockSpan, bool) {
	for _, s := range m.blockRenderSpans {
		if s.id != "" && len(s.rendered.Rows) > 0 {
			return s, true
		}
	}
	return renderedBlockSpan{}, false
}

// The find segment must survive being narrow: it is the only indication of what
// the keys are doing, so it outranks the identity segments that describe the
// session rather than the interaction.
func TestFindStatusOutranksSessionIdentity(t *testing.T) {
	m := findHighlightModel(t, findNeedle)
	segs := m.statusLeftSegments()

	var findPriority, dirPriority int = -1, -1
	for _, s := range segs {
		plain := ansi.Strip(s.text)
		if strings.Contains(plain, "find") {
			findPriority = s.priority
		}
		if plain == m.state.WorkingDir || strings.Contains(plain, "⎇") {
			dirPriority = s.priority
		}
	}
	if findPriority < 0 {
		t.Fatal("no find segment in the status line")
	}
	if dirPriority >= 0 && findPriority > dirPriority {
		t.Fatalf("the find segment is priority %d, below the session identity's %d, so it is shed first",
			findPriority, dirPriority)
	}
}
