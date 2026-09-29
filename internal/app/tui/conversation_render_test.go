package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/app/tui/glyph"
)

// visibleWidth measures a rendered line the way the frame does, so an assertion
// about fitting a width agrees with the code that lays the frame out.
func visibleWidth(s string) int { return ansi.StringWidth(s) }

// The mapped layout's indent and the transcript's gutter MUST be the same width.
//
// renderFinalAnswerWithSink substitutes one for the other on a block's first
// line, and that substitution is what keeps a cell in the mapping equal to a
// screen column. If they drifted apart, every offset in a block would be shifted
// by the difference and a click would land next to where the reader aimed — with
// nothing on screen to show it.
func TestMappedIndentMatchesTheTranscriptGutter(t *testing.T) {
	if mappedIndent != gutterWidth {
		t.Fatalf("mappedIndent = %d, gutterWidth = %d; they must match because the "+
			"first line of a mapped block replaces one with the other",
			mappedIndent, gutterWidth)
	}
}

// The first line of a mapped block gets the caller's gutter in place of the
// layout's indent: same width, so the offsets do not move.
func TestMappedFirstLineReplacesTheIndentWithoutMovingOffsets(t *testing.T) {
	gutter := gutterPrefix(glyph.Rail, accentColor)
	indented := strings.Repeat(" ", mappedIndent) + "the answer"

	got := renderMappedFirstLine(indented, gutter)
	if !strings.HasSuffix(got, "the answer") {
		t.Fatalf("the first line's text was altered: %q", got)
	}
	if visibleWidth(got) != visibleWidth(gutter)+len("the answer") {
		t.Fatalf("the line is %d cells, want the gutter plus the text (%d)",
			visibleWidth(got), visibleWidth(gutter)+len("the answer"))
	}
	if strings.HasPrefix(got, " "+gutter) {
		t.Fatalf("the indent was not replaced, it was kept: %q", got)
	}
}

// A mapped answer renders its prose through the projection: the syntax is gone
// from what is displayed, which is the visible half of what this task does.
func TestMappedAnswerRendersProjectedProse(t *testing.T) {
	msg := session.Message{
		Role: session.RoleAssistant, Final: true,
		ContentType: session.ContentTypeMarkdown,
		Content:     "## Heading\n\nA **bold** word and `code`.",
	}
	out := renderFinalAnswer(msg, 80)
	plain := ansi.Strip(out)
	if strings.Contains(plain, "##") || strings.Contains(plain, "**") || strings.Contains(plain, "`") {
		t.Fatalf("markdown syntax reached the screen: %q", plain)
	}
	for _, want := range []string{"Heading", "bold", "code"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("the answer lost %q: %q", want, plain)
		}
	}
}

// The final answer records its mapping, which is what makes the block selectable
// at all. Without it a click on the prose would resolve to nothing.
func TestMappedAnswerPublishesItsMappingToTheSink(t *testing.T) {
	sink := &mappedMessageSink{}
	msg := session.Message{
		Role: session.RoleAssistant, Final: true,
		ContentType: session.ContentTypeMarkdown,
		Content:     "A short answer.",
	}
	renderFinalAnswerWithSink(msg, 80, sink)

	rendered, ok := sink.take()
	if !ok {
		t.Fatal("the answer published no mapping")
	}
	if rendered.Logical != "A short answer." {
		t.Fatalf("the mapping's logical text is %q, want the readable answer", rendered.Logical)
	}
	if len(rendered.Rows) == 0 {
		t.Fatal("the mapping has no rows")
	}
	// Taking clears it, so the next block cannot claim this one's mapping.
	if _, again := sink.take(); again {
		t.Fatal("a taken mapping was handed out twice")
	}
}

// Paths that render through something OTHER than the mapped Markdown renderer
// publish no mapping. A block that published one would claim offsets against
// text that is not on screen, so a click on it would resolve into the wrong
// place.
//
// Note what is deliberately NOT in this list: a FINAL message with any role or
// content type. Those go through the mapped renderer, and that is the pre-
// existing dispatch — "if msg.Final" precedes the role and content-type branches
// in renderMessageWithSink, so a final answer has always been rendered by the
// rich renderer rather than by the plain one. A test asserting otherwise would be
// asserting a change to the transcript's dispatch that this task does not make.
func TestPathsOutsideTheMappedRendererPublishNoMapping(t *testing.T) {
	cases := []struct {
		name string
		msg  session.Message
	}{
		{"non-final assistant prose", session.Message{
			Role: session.RoleAssistant, Content: "still thinking"}},
		{"user prompt", session.Message{
			Role: session.RoleUser, Content: "a prompt"}},
		{"tool result", session.Message{
			Role: session.RoleAssistant, Content: "output",
			ContentType: session.ContentTypeToolResult}},
		{"plan block", session.Message{
			Role: session.RoleAssistant, Content: "1. step",
			ContentType: session.ContentTypePlan}},
		{"skill body (model context, not shown)", session.Message{
			Role: session.RoleSystem, Content: "instructions",
			ContentType: session.ContentTypeSkillBody}},
		{"subagent report (rendered as a card)", session.Message{
			Role: session.RoleAssistant, Content: "report",
			ContentType: session.ContentTypeSubagentReport}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &mappedMessageSink{}
			renderMessageWithSink(c.msg, 80, sink)
			if rendered, ok := sink.take(); ok {
				t.Fatalf("a non-mapped path published a mapping for %q", rendered.Logical)
			}
		})
	}
}

// A rendering sink is per-BUILD. A mapping left over from a previous build would
// be claimed by whatever block is added next, and the reader's click would then
// resolve against a layout that is no longer on screen.
func TestATakenMappingIsNotReusedByTheNextBlock(t *testing.T) {
	sink := &mappedMessageSink{}
	sink.pending = &conversation.RenderedBlock{Logical: "first"}
	if _, ok := sink.take(); !ok {
		t.Fatal("the pending mapping was not handed out")
	}
	if _, ok := sink.take(); ok {
		t.Fatal("the same mapping was handed out to a second block")
	}
}

// A cached rendering must not be served for a different width. This is the
// failure that looks correct until the terminal is resized and then stays wrong:
// the block keeps the previous width's wrapping, and because the text did not
// change nothing re-renders it.
func TestRenderCacheKeysOnWidth(t *testing.T) {
	block := conversation.Block{
		ID: "msg:1", Kind: conversation.BlockMessage,
		Text: "one two three four five six seven eight nine ten",
	}
	wide := renderConversationBlock(block, 60, BlockRenderFull)
	narrow := renderConversationBlock(block, 20, BlockRenderFull)
	if wide == narrow {
		t.Fatal("the same text rendered identically at width 60 and width 20")
	}
	cache := newConversationRenderCache(8)
	key := blockRenderKey{block: block.ID, width: 60}
	cache.put(key, wide)

	if got, ok := cache.get(blockRenderKey{block: block.ID, width: 20}); ok {
		t.Fatalf("the cache served a width-60 rendering for width 20: %q", got)
	}
	if got, ok := cache.get(key); !ok || got != wide {
		t.Fatalf("the cache lost its own entry: %q %v", got, ok)
	}
}

// A block whose content changed is rendered again even at the same width, which
// is what the revision is for. Without it, a streaming answer would freeze at
// the text it had when it was first rendered.
func TestRenderCacheKeysOnRevision(t *testing.T) {
	cache := newConversationRenderCache(8)
	cache.put(blockRenderKey{block: "msg:1", revision: 1, width: 40}, "first")
	if got, ok := cache.get(blockRenderKey{block: "msg:1", revision: 2, width: 40}); ok {
		t.Fatalf("the cache served revision 1's rendering for revision 2: %q", got)
	}
}

// A summary and a full rendering of the same block are different outputs, so
// they cannot share a cache entry.
func TestRenderCacheKeysOnMode(t *testing.T) {
	cache := newConversationRenderCache(8)
	cache.put(blockRenderKey{block: "msg:1", width: 40, mode: BlockRenderFull}, "full")
	if got, ok := cache.get(blockRenderKey{block: "msg:1", width: 40, mode: BlockRenderSummary}); ok {
		t.Fatalf("the cache served a full rendering as a summary: %q", got)
	}
}

// The cache is bounded. An unbounded one leaks across a long session and every
// resize multiplies the entries, which only shows up in the sessions that matter
// most.
func TestRenderCacheIsBoundedAndEvictsOldestFirst(t *testing.T) {
	cache := newConversationRenderCache(4)
	for i := 0; i < 10; i++ {
		cache.put(blockRenderKey{block: conversation.BlockID("msg:" + string(rune('a'+i))), width: 40}, "x")
	}
	if got := cache.Len(); got != 4 {
		t.Fatalf("cache holds %d entries, want the bound of 4", got)
	}
	// The first four inserted are gone; the last four survive.
	if _, ok := cache.get(blockRenderKey{block: "msg:a", width: 40}); ok {
		t.Fatal("the oldest entry survived eviction")
	}
	if _, ok := cache.get(blockRenderKey{block: "msg:j", width: 40}); !ok {
		t.Fatal("the newest entry was evicted")
	}
}

// Reading an entry makes it recent, so a block the reader is looking at is not
// evicted by a stream of new ones arriving behind it.
func TestRenderCacheKeepsRecentlyReadEntries(t *testing.T) {
	cache := newConversationRenderCache(3)
	cache.put(blockRenderKey{block: "a", width: 40}, "a")
	cache.put(blockRenderKey{block: "b", width: 40}, "b")
	cache.put(blockRenderKey{block: "c", width: 40}, "c")
	// Touch "a" so it is no longer the oldest.
	if _, ok := cache.get(blockRenderKey{block: "a", width: 40}); !ok {
		t.Fatal("entry a went missing before eviction")
	}
	cache.put(blockRenderKey{block: "d", width: 40}, "d")

	if _, ok := cache.get(blockRenderKey{block: "a", width: 40}); !ok {
		t.Fatal("a recently read entry was evicted")
	}
	if _, ok := cache.get(blockRenderKey{block: "b", width: 40}); ok {
		t.Fatal("the least recently used entry survived")
	}
}

// The cache is emptied on a session switch. Block identities are scoped per
// session, so entries carried across a switch would serve one conversation's
// rendering for another's block.
func TestRenderCacheResetEmptiesIt(t *testing.T) {
	cache := newConversationRenderCache(8)
	cache.put(blockRenderKey{block: "msg:1", width: 40}, "stale")
	cache.reset()
	if got := cache.Len(); got != 0 {
		t.Fatalf("cache still holds %d entries after reset", got)
	}
	if _, ok := cache.get(blockRenderKey{block: "msg:1", width: 40}); ok {
		t.Fatal("a reset cache served an entry")
	}
}

// Rendering at an unmeasured width produces nothing rather than something the
// caller cannot use. A guessed width would also be cached as if it were good.
func TestRenderAtUnmeasuredWidthProducesNothing(t *testing.T) {
	block := conversation.Block{ID: "msg:1", Text: "some text"}
	if got := renderConversationBlock(block, 0, BlockRenderFull); got != "" {
		t.Fatalf("unmeasured width rendered %q, want nothing", got)
	}
	if got := renderConversationBlock(block, -5, BlockRenderFull); got != "" {
		t.Fatalf("negative width rendered %q, want nothing", got)
	}
}

// A block with no text renders to nothing, so a caller joining blocks does not
// emit a blank line for it.
func TestRenderOfEmptyBlockIsEmpty(t *testing.T) {
	if got := renderConversationBlock(conversation.Block{ID: "msg:1"}, 40, BlockRenderFull); got != "" {
		t.Fatalf("empty block rendered %q", got)
	}
}

// Rendered output carries no trailing newline: the caller joins blocks, and a
// per-block newline is how the transcript acquires a blank line between every
// pair of items.
func TestRenderedBlockHasNoTrailingNewline(t *testing.T) {
	cases := []string{
		"a paragraph",
		"# heading\n\nbody",
		"- one\n- two",
		"```\ncode\n```",
		"a\n\n\nb",
	}
	for _, src := range cases {
		got := renderConversationBlock(conversation.Block{ID: "msg:1", Text: src}, 40, BlockRenderFull)
		if strings.HasSuffix(got, "\n") {
			t.Fatalf("rendering of %q ends with a newline: %q", src, got)
		}
	}
}

// Every displayed line of a rendered block fits the width it was rendered for.
// This is the invariant a mapped renderer can actually be held to, and the one
// that fails visibly (text spilling under the side rail) when it is broken.
func TestRenderedBlockLinesFitTheWidth(t *testing.T) {
	sources := []string{
		"a paragraph that is long enough to need several display lines at a narrow width",
		"| a | b |\n| --- | --- |\n| 1 | 2 |",
		"# a heading that is also quite long\n\nand a body underneath it",
		strings.Repeat("x", 200),
		"- a list item with quite a lot of words in it to force a wrap somewhere",
	}
	for _, width := range []int{20, 40, 79} {
		for _, src := range sources {
			got := renderConversationBlock(conversation.Block{ID: "msg:1", Text: src}, width, BlockRenderFull)
			for i, line := range strings.Split(got, "\n") {
				if w := visibleWidth(line); w > width {
					t.Fatalf("width %d: line %d of %q measures %d cells: %q",
						width, i, src, w, line)
				}
			}
		}
	}
}

// A summary rendering collapses a long block to one line and says so, which is
// what a collapsed group or a one-line transcript row needs.
func TestSummaryRenderingIsOneLine(t *testing.T) {
	src := "a fairly long paragraph that would certainly occupy more than one display line at width forty"
	full := renderConversationBlock(conversation.Block{ID: "msg:1", Text: src}, 40, BlockRenderFull)
	summary := renderConversationBlock(conversation.Block{ID: "msg:1", Text: src}, 40, BlockRenderSummary)

	if strings.Count(full, "\n") == 0 {
		t.Fatal("the fixture does not wrap, so collapsing it proves nothing")
	}
	if strings.Contains(summary, "\n") {
		t.Fatalf("a summary rendering spans several lines: %q", summary)
	}
	if !strings.Contains(summary, "…") {
		t.Fatalf("a truncated summary does not say it was truncated: %q", summary)
	}
	if visibleWidth(summary) > 40 {
		t.Fatalf("the summary is %d cells wide: %q", visibleWidth(summary), summary)
	}
}

// A block short enough not to wrap must not gain an ellipsis: the marker is a
// statement that content was withheld, and claiming that about a complete block
// is a lie the reader has no way to check.
func TestShortBlockSummaryHasNoElisionMarker(t *testing.T) {
	got := renderConversationBlock(conversation.Block{ID: "msg:1", Text: "short"}, 40, BlockRenderSummary)
	if strings.Contains(got, "…") {
		t.Fatalf("a complete block was marked as truncated: %q", got)
	}
	if got != "short" {
		t.Fatalf("summary of a short block = %q, want the text itself", got)
	}
}

// Styling reaches the rendered output: a heading is not emitted as bare text.
// A renderer that dropped the kinds would look plausible in a diff and be wrong
// on screen.
func TestRenderedOutputCarriesStyling(t *testing.T) {
	got := renderConversationBlock(conversation.Block{ID: "msg:1", Text: "## Heading"}, 40, BlockRenderFull)
	if strings.Contains(got, "\x1b[") == false {
		t.Fatalf("a heading rendered with no styling at all: %q", got)
	}
	if strings.Contains(got, "##") {
		t.Fatalf("the heading markers survived into the render: %q", got)
	}
}

// The transcript mapping turns a (row, cell) into an offset in the right block's
// text. This is the join the selection work depends on, so it is asserted through
// the mapping's own entry point rather than through internals.
func TestTranscriptCellResolvesToAnOffsetInTheBlockAtThatRow(t *testing.T) {
	first := conversation.Block{ID: "msg:1", Kind: conversation.BlockMessage, Text: "alpha beta"}
	second := conversation.Block{ID: "msg:2", Kind: conversation.BlockMessage, Text: "gamma delta"}

	r1 := renderConversationBlock(first, 40, BlockRenderFull)
	r2 := renderConversationBlock(second, 40, BlockRenderFull)
	spans := []renderedBlockSpan{
		{id: first.ID, blockRow: 0, rows: lineCount(r1), rendered: layoutFor(first, 40)},
		{id: second.ID, blockRow: lineCount(r1), rows: lineCount(r2), rendered: layoutFor(second, 40)},
	}
	m := Model{blockRenderSpans: spans}

	id, off, ok := m.OffsetAtTranscriptCell(0, 0)
	if !ok {
		t.Fatal("a cell on the first block resolved to nothing")
	}
	if id != first.ID {
		t.Fatalf("row 0 resolved to block %q, want %q", id, first.ID)
	}
	if off != 0 {
		t.Fatalf("cell 0 resolved to offset %d, want 0", off)
	}

	id, off, ok = m.OffsetAtTranscriptCell(spans[1].blockRow, 0)
	if !ok {
		t.Fatal("a cell on the second block resolved to nothing")
	}
	if id != second.ID {
		t.Fatalf("the second block's first row resolved to %q, want %q", id, second.ID)
	}
	if off != 0 {
		t.Fatalf("the second block's cell 0 resolved to offset %d, want 0", off)
	}
}

// A row that no mapped block covers declines rather than guessing. The
// transcript also renders chrome (a welcome banner, a turn rule), and an offset
// computed against some other block's text would put a selection in the wrong
// place with nothing to indicate it.
func TestATranscriptRowWithNoMappedBlockDeclines(t *testing.T) {
	m := Model{blockRenderSpans: []renderedBlockSpan{
		{id: "msg:1", blockRow: 5, rows: 2, rendered: layoutFor(conversation.Block{ID: "msg:1", Text: "x"}, 40)},
	}}
	if id, _, ok := m.OffsetAtTranscriptCell(0, 0); ok {
		t.Fatalf("row 0 resolved to block %q though no block covers it", id)
	}
	if id, _, ok := m.OffsetAtTranscriptCell(99, 0); ok {
		t.Fatalf("row 99 resolved to block %q though no block covers it", id)
	}
	// The covered rows DO resolve, and to the right block.
	if id, _, ok := m.OffsetAtTranscriptCell(5, 0); !ok || id != "msg:1" {
		t.Fatalf("row 5 = (%q, %v), want the block that covers it", id, ok)
	}
}

// The end-to-end property: after a transcript is actually built, reading the
// characters off the rows by cell returns the answer.
//
// This is the one test that exercises the whole chain — refreshViewport renders,
// the sink carries the mapping, addBlock places it, and OffsetAtTranscriptCell
// reads it back — so it fails if any link is wrong. Each unit test above holds one
// link; this holds the chain.
//
// The assertion is deliberately "the characters come back in order" rather than
// "cell N is offset M": the exact column the text starts at is an implementation
// detail of the gutter, and pinning it here would make the test fail on a
// cosmetic change while still passing if the mapping were off by a constant.
func TestReadingAnAnswerOffTheRowsGivesBackTheAnswer(t *testing.T) {
	m := newTestModel(t)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: t.TempDir()})
	const answer = "alpha beta gamma"
	m.state.AddMessageFinal(session.RoleAssistant, answer, session.ContentTypeMarkdown)

	m.refreshViewport()

	span, ok := mappedSpanFor(t, &m, answer)
	if !ok {
		t.Fatalf("the answer published no mapping; blockRenderSpans = %+v", m.blockRenderSpans)
	}
	if span.rows != len(span.rendered.Rows) {
		t.Fatalf("the mapping claims %d rows but carries %d",
			span.rows, len(span.rendered.Rows))
	}
	if span.rendered.Width == 0 {
		t.Fatal("the published mapping was laid out at width 0")
	}
	if span.rendered.Logical != answer {
		t.Fatalf("the mapping's logical text is %q, want %q", span.rendered.Logical, answer)
	}

	// THE cross-check: the row index the mapping claims must be the row the text
	// is actually drawn on. Without this the test is self-consistent — it reads
	// back through the same stored index — and would pass with the mapping
	// pointing at any row at all.
	//
	// The viewport's content is the ground truth, because its lines are exactly
	// what is on screen at a given scroll offset.
	contentLines := strings.Split(m.viewport.GetContent(), "\n")
	if span.blockRow >= len(contentLines) {
		t.Fatalf("the mapping claims row %d but the transcript has %d lines",
			span.blockRow, len(contentLines))
	}
	drawn := ansi.Strip(contentLines[span.blockRow])
	if !strings.Contains(drawn, "alpha") {
		t.Fatalf("the mapping claims row %d, but that row reads %q — the mapping does not "+
			"point at the text it describes", span.blockRow, drawn)
	}

	// Read the row back character by character: for each cell, resolve the
	// character it names, and take the distinct ones in order.
	row := span.rendered.Rows[0]
	var got strings.Builder
	last := -1
	for cell := 0; cell <= row.Cells; cell++ {
		_, off, resolved := m.OffsetAtTranscriptCell(span.blockRow, cell)
		if !resolved || off == last || off >= len(answer) {
			continue
		}
		got.WriteByte(answer[off])
		last = off
	}
	if !strings.HasPrefix(got.String(), "alpha") {
		t.Fatalf("reading the row by cell gave %q, want it to begin with the answer's text",
			got.String())
	}
	// Every character the walk produced must be one the reader can see on that
	// row: a cell that resolves to a byte not displayed there would let a
	// selection reach text that is somewhere else.
	visible := ansi.Strip(contentLines[span.blockRow])
	for i := 0; i < got.Len(); i++ {
		if !strings.ContainsRune(visible, rune(got.String()[i])) {
			t.Fatalf("cell resolution produced %q, which is not on row %d (%q)",
				string(got.String()[i]), span.blockRow, visible)
		}
	}
}

// mappedSpanFor finds the placed mapping whose logical text is exactly want.
func mappedSpanFor(t *testing.T, m *Model, want string) (renderedBlockSpan, bool) {
	t.Helper()
	for _, s := range m.blockRenderSpans {
		if s.rendered.Logical == want {
			return s, true
		}
	}
	return renderedBlockSpan{}, false
}

// A row the mapping does not cover declines, even when the transcript is
// non-empty. The welcome banner and the turn separator are rendered but not
// mapped, and a click on one must not resolve into some other block's text.
func TestClickingRenderedChromeDeclinesRatherThanGuessing(t *testing.T) {
	m := newTestModel(t)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: t.TempDir()})
	// Two turns, so the transcript renders a turn separator above the second
	// one. The separator is chrome: it is drawn but it is not a mapped block.
	m.state.AddMessage(session.RoleUser, "first prompt", session.ContentTypePlain)
	m.state.AddMessageFinal(session.RoleAssistant, "first answer", session.ContentTypeMarkdown)
	m.state.AddMessage(session.RoleUser, "second prompt", session.ContentTypePlain)
	m.state.AddMessageFinal(session.RoleAssistant, "second answer", session.ContentTypeMarkdown)

	m.refreshViewport()
	if m.viewport.TotalLineCount() == 0 {
		t.Fatal("the fixture rendered nothing, so this proves nothing")
	}
	if len(m.blockRenderSpans) == 0 {
		t.Fatal("no block was mapped at all, so a decline proves nothing")
	}

	// Every row that is NOT covered by a mapping must decline.
	covered := map[int]bool{}
	for _, s := range m.blockRenderSpans {
		for r := s.blockRow; r < s.blockRow+s.rows; r++ {
			covered[r] = true
		}
	}
	declined := 0
	for row := 0; row < m.viewport.TotalLineCount(); row++ {
		if covered[row] {
			continue
		}
		if id, off, ok := m.OffsetAtTranscriptCell(row, 0); ok {
			t.Fatalf("row %d is not covered by any mapping but resolved to block %q offset %d",
				row, id, off)
		}
		declined++
	}
	if declined == 0 {
		t.Fatal("no unmapped row existed, so this proves nothing")
	}
}

// layoutFor is the test shorthand for "the mapped rendering of this block".
func layoutFor(b conversation.Block, width int) conversation.RenderedBlock {
	sp := conversation.ProjectMarkdown(b.Text, conversation.MarkdownOptions{})
	return conversation.RenderedBlock{
		BlockID: b.ID,
		Logical: sp.Text,
		Width:   width,
		Rows:    conversation.Layout(sp, conversation.LayoutOptions{Width: width}),
	}
}

// lineCount counts the display lines a rendering occupies.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
