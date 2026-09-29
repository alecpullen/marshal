// internal/app/tui/conversation/search_test.go — find over the readable projection
package conversation

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// The tests here hold the search to four promises that are easy to get wrong in
// ways nothing else would notice: a match is found whatever its case, its
// offsets name the ORIGINAL text rather than the folded form, text the renderer
// inserted is not searched, and content that never reaches the reader is not
// searched either.

// searchBlock builds a one-member block, the shape the adapter produces for a
// single transcript item.
//
// The identity is stamped as well as declared as a member, because a
// NewDocument would derive it: a helper that only worked once the block had
// been through a document could not be used to exercise anything that reads a
// block directly.
func searchBlock(id, text string) Block {
	return Block{ID: BlockID(id), Kind: BlockMessage, Members: []string{id}, Text: text}
}

// projectionOf is the readable text a block is searched in. It is the same
// projection the renderer lays out, which is what makes a match's offsets
// usable for a highlight.
func projectionOf(t *testing.T, src string) string {
	t.Helper()
	return ProjectMarkdown(src, MarkdownOptions{}).Text
}

func TestFindQueryIsCaseFoldedAndReportsEmptiness(t *testing.T) {
	if NewFindQuery("").Empty() != true {
		t.Fatal("the empty query is not reported empty")
	}
	q := NewFindQuery("Milk")
	if q.Empty() {
		t.Fatal("a non-empty query is reported empty")
	}
	if q.String() != "Milk" {
		t.Fatalf("the query does not echo what was typed: %q", q.String())
	}
}

func TestFindReportsNoMatchAsNothing(t *testing.T) {
	doc := NewDocument([]Block{searchBlock("msg@1:1", "the quick brown fox")})
	if got := FindInDocument(doc, NewFindQuery("zebra"), nil); len(got) != 0 {
		t.Fatalf("a query absent from the document produced %d matches: %+v", len(got), got)
	}
}

func TestFindMatchesWhateverTheCase(t *testing.T) {
	doc := NewDocument([]Block{searchBlock("msg@1:1", "the Quick brown Fox")})
	got := FindInDocument(doc, NewFindQuery("quick"), nil)
	if len(got) != 1 {
		t.Fatalf("want one case-insensitive match, got %d", len(got))
	}
	if got[0].Range != (Range{4, 9}) {
		t.Fatalf("match range is %+v, want {4, 9} (the offsets of \"Quick\")", got[0].Range)
	}
}

func TestFindFoldsCaseWithoutMovingOffsetsOffTheOriginalBytes(t *testing.T) {
	// É and è are two bytes each, so a search that reported offsets into the
	// FOLDED string would name the wrong bytes here even though the folded and
	// original byte counts happen to agree. Reading the original back is what
	// proves the mapping.
	const src = "CAFÉ crème\n"
	projected := projectionOf(t, src)
	doc := NewDocument([]Block{searchBlock("msg@1:1", src)})

	upper := FindInDocument(doc, NewFindQuery("café"), nil)
	if len(upper) != 1 {
		t.Fatalf("want one match for the upper-case run, got %d", len(upper))
	}
	if got := projected[upper[0].Range.Start:upper[0].Range.End]; got != "CAFÉ" {
		t.Fatalf("the match names %q, want %q", got, "CAFÉ")
	}
	if upper[0].Range != (Range{0, 5}) {
		t.Fatalf("match range is %+v, want {0, 5}", upper[0].Range)
	}

	lower := FindInDocument(doc, NewFindQuery("CRÈME"), nil)
	if len(lower) != 1 {
		t.Fatalf("want one match for the lower-case run, got %d", len(lower))
	}
	if got := projected[lower[0].Range.Start:lower[0].Range.End]; got != "crème" {
		t.Fatalf("the match names %q, want %q", got, "crème")
	}
}

func TestFindKeepsOffsetsExactWhenAFoldChangesByteWidth(t *testing.T) {
	// "İ" is two bytes and folds to one byte. A search that reported offsets
	// into the FOLDED string would name the wrong bytes from this point on, and
	// the mismatch is invisible in English text — which is how it survives to
	// production. Reading the original back is what proves the mapping.
	const src = "İSTANBUL cafe\n"
	projected := projectionOf(t, src)
	doc := NewDocument([]Block{searchBlock("msg@1:1", src)})

	got := FindInDocument(doc, NewFindQuery("istanbul"), nil)
	if len(got) != 1 {
		t.Fatalf("want one simple-case-folded match, got %d", len(got))
	}
	// Simple folding folds İ to i, so the match spans the whole word and its
	// range must name "İSTANBUL" in the ORIGINAL bytes (nine bytes, not eight).
	if s := projected[got[0].Range.Start:got[0].Range.End]; s != "İSTANBUL" {
		t.Fatalf("the match names %q, want %q", s, "İSTANBUL")
	}
	if got[0].Range != (Range{0, 9}) {
		t.Fatalf("match range is %+v, want {0, 9} (nine bytes, not the folded eight)", got[0].Range)
	}

	// A match AFTER the wide rune is the one that catches an accumulated drift:
	// every offset past İ would be one byte short.
	after := FindInDocument(doc, NewFindQuery("cafe"), nil)
	if len(after) != 1 {
		t.Fatalf("want one match after the wide rune, got %d", len(after))
	}
	if s := projected[after[0].Range.Start:after[0].Range.End]; s != "cafe" {
		t.Fatalf("the trailing match names %q, want %q", s, "cafe")
	}
}

func TestFindReportsEveryNonOverlappingOccurrence(t *testing.T) {
	doc := NewDocument([]Block{searchBlock("msg@1:1", "aaa")})
	got := FindInDocument(doc, NewFindQuery("aa"), nil)
	if len(got) != 1 {
		t.Fatalf("want one non-overlapping match in \"aaa\", got %d", len(got))
	}
	if got[0].Range != (Range{0, 2}) {
		t.Fatalf("match range is %+v, want {0, 2}", got[0].Range)
	}

	doc = NewDocument([]Block{searchBlock("msg@1:1", "aXaXa")})
	got = FindInDocument(doc, NewFindQuery("a"), nil)
	if len(got) != 3 {
		t.Fatalf("want three matches, got %d", len(got))
	}
	for i, want := range []int{0, 2, 4} {
		if got[i].Range != (Range{want, want + 1}) {
			t.Fatalf("match %d is %+v, want {%d, %d}", i, got[i].Range, want, want+1)
		}
	}
}

func TestFindDoesNotSearchTextTheRendererInserted(t *testing.T) {
	// A list bullet is the renderer's, not the author's. A search for it would
	// report a hit on every item of every list, on a character nobody typed.
	const src = "- one\n- two\n"
	projected := projectionOf(t, src)
	if !strings.Contains(projected, "•") {
		t.Fatalf("the fixture no longer projects a bullet: %q", projected)
	}
	doc := NewDocument([]Block{searchBlock("msg@1:1", src)})

	if got := FindInDocument(doc, NewFindQuery("•"), nil); len(got) != 0 {
		t.Fatalf("the bullet was searched: %+v", got)
	}
	got := FindInDocument(doc, NewFindQuery("two"), nil)
	if len(got) != 1 {
		t.Fatalf("want the item's own text to be searched, got %d matches", len(got))
	}
	if text := projected[got[0].Range.Start:got[0].Range.End]; text != "two" {
		t.Fatalf("the match names %q, want %q", text, "two")
	}
}

func TestFindDoesNotMatchAcrossDecoration(t *testing.T) {
	// Table cells are separated by a generated tab. A match that spanned the
	// separator would name a run of characters the reader never saw as a run.
	const src = "| ab | cd |\n| --- | --- |\n| ab | cd |\n"
	projected := projectionOf(t, src)
	if !strings.Contains(projected, "\t") {
		t.Fatalf("the fixture no longer projects a cell separator: %q", projected)
	}
	doc := NewDocument([]Block{searchBlock("msg@1:1", src)})

	// "ab\tcd" appears literally in the projection but the separator inside it
	// is the renderer's, so it is not a phrase anyone read.
	if got := FindInDocument(doc, NewFindQuery("ab\tcd"), nil); len(got) != 0 {
		t.Fatalf("a query spanning the cell separator matched: %+v", got)
	}
	if got := FindInDocument(doc, NewFindQuery("abcd"), nil); len(got) != 0 {
		t.Fatalf("a query spanning across the separator matched: %+v", got)
	}
	if got := FindInDocument(doc, NewFindQuery("cd"), nil); len(got) == 0 {
		t.Fatal("the cell's own text was not found")
	}
}

func TestFindSkipsContentThatNeverReachesTheReader(t *testing.T) {
	// A skill body reaches the model and is not drawn. Searching it would offer
	// the reader a jump to a line that is not on screen — and would leak
	// instructions the transcript deliberately hides.
	hidden := Block{
		Kind:    BlockMessage,
		Members: []string{"msg@1:9"},
		Text:    "load-bearing secret instruction",
		Hidden:  true,
	}
	visible := searchBlock("msg@1:1", "nothing to see here")
	doc := NewDocument([]Block{hidden, visible})

	if got := FindInDocument(doc, NewFindQuery("secret"), nil); len(got) != 0 {
		t.Fatalf("hidden content was searched: %+v", got)
	}
	if got := FindInDocument(doc, NewFindQuery("nothing"), nil); len(got) != 1 {
		t.Fatalf("visible content was not searched alongside it: %+v", got)
	}
}

func TestFindReachesIntoACollapsedGroupsMembers(t *testing.T) {
	// A collapsed group renders one summary line, but its members' output is
	// still part of the conversation. A match inside one names the member (so
	// the caller can open it) and names the group (so the caller can scroll to
	// it), and those are two different identities.
	group := Block{
		Kind:    BlockToolGroup,
		Members: []string{"audit@1:1", "audit@1:2"},
		Children: []Block{
			{Kind: BlockTool, Members: []string{"audit@1:1"}, Text: "read main.go"},
			{Kind: BlockTool, Members: []string{"audit@1:2"}, Text: "read search.go"},
		},
	}
	doc := NewDocument([]Block{group})

	got := FindInDocument(doc, NewFindQuery("search.go"), nil)
	if len(got) != 1 {
		t.Fatalf("want one match inside the collapsed run, got %d", len(got))
	}
	if got[0].Block != BlockID("audit@1:2") {
		t.Fatalf("the match names block %q, want the member it was found in", got[0].Block)
	}
	if got[0].Scroll != BlockID("group:audit@1:1") {
		t.Fatalf("the match scrolls to %q, want the enclosing group", got[0].Scroll)
	}
	if _, ok := doc.Block(got[0].Scroll); !ok {
		t.Fatal("the scroll target is not a block the document indexes")
	}
	if _, ok := doc.BlockForMember(string(got[0].Block)); !ok {
		t.Fatal("the match names a member the document cannot resolve")
	}
}

func TestFindInATopLevelBlockScrollsToThatBlock(t *testing.T) {
	doc := NewDocument([]Block{searchBlock("msg@1:1", "hello world")})
	got := FindInDocument(doc, NewFindQuery("world"), nil)
	if len(got) != 1 {
		t.Fatalf("want one match, got %d", len(got))
	}
	if got[0].Block != got[0].Scroll {
		t.Fatalf("a top-level match scrolls to %q, not to itself (%q)", got[0].Scroll, got[0].Block)
	}
}

func TestFindAnEmptyQueryFindsNothing(t *testing.T) {
	doc := NewDocument([]Block{searchBlock("msg@1:1", "hello world")})
	if got := FindInDocument(doc, NewFindQuery(""), nil); len(got) != 0 {
		t.Fatalf("the empty query matched %d times", len(got))
	}
}

func TestFindCapsItsResults(t *testing.T) {
	// An unbounded result list is a slow leak on a pathological document (a
	// pasted log of one repeated word), so the cap is part of the contract.
	//
	// The expected value is written as a LITERAL rather than as maxFindMatches.
	// Asserting "the result is the cap" against the constant would pass for any
	// value of the constant — including one large enough to be no cap at all —
	// which is the shape of a test that measures itself instead of the code.
	const wantCap = 500
	doc := NewDocument([]Block{searchBlock("msg@1:1", strings.Repeat("x ", wantCap+50))})
	got := FindInDocument(doc, NewFindQuery("x"), nil)
	if len(got) != wantCap {
		t.Fatalf("want the result list capped at %d, got %d", wantCap, len(got))
	}
	// The cap must not be reachable only from inside one block: a document of
	// many short blocks has to be capped too, which is a different code path
	// (the outer break) from the per-block loop.
	many := make([]Block, 0, wantCap+50)
	for i := 0; i < wantCap+50; i++ {
		many = append(many, searchBlock("msg@1:"+strconv.Itoa(i), "x"))
	}
	if got := FindInDocument(NewDocument(many), NewFindQuery("x"), nil); len(got) != wantCap {
		t.Fatalf("a document of many blocks produced %d matches, want %d", len(got), wantCap)
	}
}

func TestFindSearchesToolOutputAsWritten(t *testing.T) {
	// A tool's output is captured text, not Markdown. A block of it containing
	// something that LOOKS like a list marker must be searched as the tool wrote
	// it: projecting it would turn "- item" into "• item" and report a match on
	// a character the tool never printed — and would report offsets into text
	// that is not what the transcript draws.
	out := Block{
		ID:      "audit@1:1",
		Kind:    BlockTool,
		Members: []string{"audit@1:1"},
		Text:    "- item\n",
		Source:  SourceOutput,
	}
	doc := NewDocument([]Block{out})

	if got := FindInDocument(doc, NewFindQuery("•"), nil); len(got) != 0 {
		t.Fatalf("tool output was projected as Markdown: %+v", got)
	}
	got := FindInDocument(doc, NewFindQuery("- item"), nil)
	if len(got) != 1 {
		t.Fatalf("the tool's own text was not found as written: %+v", got)
	}
	if out.Text[got[0].Range.Start:got[0].Range.End] != "- item" {
		t.Fatalf("the match names %q, want the tool's own bytes",
			out.Text[got[0].Range.Start:got[0].Range.End])
	}
}

func TestFindSearchesACollapsedGroupsMembersAsWritten(t *testing.T) {
	// The same rule reaches the members of a collapsed run: their text is tool
	// output too, and a group that was projected would report a match on a
	// bullet none of the tools printed.
	//
	// The fixture is a list marker because that is the ONE construct whose
	// projected form shares no bytes with its source: "- item" becomes
	// "• item". A fixture like "**bold**" would not distinguish the two paths
	// at all, since the source contains "bold" either way and the test would
	// pass whichever branch ran.
	group := Block{
		Kind:    BlockToolGroup,
		Members: []string{"audit@1:1"},
		Children: []Block{{
			ID:      "audit@1:1",
			Kind:    BlockTool,
			Members: []string{"audit@1:1"},
			Text:    "- item\n",
			Source:  SourceOutput,
		}},
	}
	doc := NewDocument([]Block{group})
	if got := FindInDocument(doc, NewFindQuery("•"), nil); len(got) != 0 {
		t.Fatalf("a group member's output was projected: %+v", got)
	}
	got := FindInDocument(doc, NewFindQuery("- item"), nil)
	if len(got) != 1 {
		t.Fatalf("the member's own bytes were not found: %+v", got)
	}
	if got[0].Block != BlockID("audit@1:1") || got[0].Scroll != BlockID("group:audit@1:1") {
		t.Fatalf("the match names block %q / scroll %q", got[0].Block, got[0].Scroll)
	}
}

func TestFindReportsWhetherItSearchedTruncatedText(t *testing.T) {
	// A search over a capped tool result covers a prefix, and saying so is the
	// difference between "not found" and "not in the part I could read".
	capped := Block{
		ID: "audit@1:1", Kind: BlockTool, Members: []string{"audit@1:1"},
		Text: "… [truncated]", Source: SourceOutput, Truncated: true,
	}
	if !DocumentTruncated(NewDocument([]Block{capped})) {
		t.Fatal("a capped block was not reported as truncated")
	}
	plain := searchBlock("msg@1:1", "complete text")
	if DocumentTruncated(NewDocument([]Block{plain})) {
		t.Fatal("an uncapped document was reported as truncated")
	}
	if DocumentTruncated(nil) {
		t.Fatal("a nil document was reported as truncated")
	}
}

func TestFindSearchesEveryBlockInOrder(t *testing.T) {
	doc := NewDocument([]Block{
		searchBlock("msg@1:1", "alpha"),
		searchBlock("msg@1:2", "beta"),
		searchBlock("msg@1:3", "alpha again"),
	})
	got := FindInDocument(doc, NewFindQuery("alpha"), nil)
	if len(got) != 2 {
		t.Fatalf("want two matches, got %d", len(got))
	}
	if got[0].Block != BlockID("msg@1:1") || got[1].Block != BlockID("msg@1:3") {
		t.Fatalf("matches are out of order: %q then %q", got[0].Block, got[1].Block)
	}
}

func TestSearchIndexReprojectsOnlyChangedBlocks(t *testing.T) {
	idx := NewSearchIndex(0)
	block := searchBlock("msg@1:1", "first revision")

	first := idx.text(block)
	if idx.Len() != 1 {
		t.Fatalf("the index holds %d entries after one block, want 1", idx.Len())
	}
	if again := idx.text(block); again != first {
		t.Fatalf("the cached projection changed: %q then %q", first, again)
	}
	if idx.Len() != 1 {
		t.Fatalf("a repeat lookup grew the index to %d entries", idx.Len())
	}

	// The revision is the whole reason the cache can be trusted: a block whose
	// content changed must be reprojected, or a search would keep reporting
	// offsets into text that is no longer there.
	block.Text = "second revision"
	block.Revision = 1
	if got := idx.text(block); got != "second revision" {
		t.Fatalf("a revised block served %q, want its new text", got)
	}
}

func TestSearchIndexIsBounded(t *testing.T) {
	idx := NewSearchIndex(2)
	for i := 0; i < 5; i++ {
		b := searchBlock("msg@1:"+string(rune('a'+i)), "text")
		b.Revision = i
		idx.text(b)
	}
	if idx.Len() > 2 {
		t.Fatalf("the index grew to %d entries past its bound of 2", idx.Len())
	}
}

func TestSearchIndexResetEmptiesIt(t *testing.T) {
	idx := NewSearchIndex(0)
	idx.text(searchBlock("msg@1:1", "text"))
	idx.Reset()
	if idx.Len() != 0 {
		t.Fatalf("the index holds %d entries after reset", idx.Len())
	}
}

func TestFindSurvivesANilIndex(t *testing.T) {
	// The index is an optimization. A caller that has none must get the same
	// answers, not a panic — the search is the contract and the cache is not.
	doc := NewDocument([]Block{searchBlock("msg@1:1", "hello world")})
	if got := FindInDocument(doc, NewFindQuery("world"), nil); len(got) != 1 {
		t.Fatalf("a nil index changed the result: %+v", got)
	}
}

func TestFindHandlesAnEmptyDocument(t *testing.T) {
	if got := FindInDocument(NewDocument(nil), NewFindQuery("x"), nil); len(got) != 0 {
		t.Fatalf("an empty document produced %d matches", len(got))
	}
	if got := FindInDocument(nil, NewFindQuery("x"), nil); len(got) != 0 {
		t.Fatalf("a nil document produced %d matches", len(got))
	}
}

func TestFindOffsetsAreAlwaysOnRuneBoundaries(t *testing.T) {
	// A highlight is applied by CUTTING the rendered line at these offsets. An
	// offset inside a multi-byte character would cut it in half and put invalid
	// UTF-8 on screen.
	// Each query must appear exactly once, so the assertion below is about the
	// offsets rather than about how often the character was typed. The last word
	// is deliberately one that contains no γ, or the single-rune query would
	// have two hits and the assertion would be counting rather than checking.
	const src = "αβγ café κάπα\n"
	projected := projectionOf(t, src)
	doc := NewDocument([]Block{searchBlock("msg@1:1", src)})
	for _, q := range []string{"γ", "café", "κάπα"} {
		got := FindInDocument(doc, NewFindQuery(q), nil)
		if len(got) != 1 {
			t.Fatalf("query %q produced %d matches", q, len(got))
		}
		r := got[0].Range
		if r.Start < 0 || r.End > len(projected) || r.Start > r.End {
			t.Fatalf("query %q produced an out-of-range match %+v", q, r)
		}
		if !utf8.RuneStart(projected[r.Start]) {
			t.Fatalf("query %q starts inside a character (byte %d)", q, r.Start)
		}
		if r.End < len(projected) && !utf8.RuneStart(projected[r.End]) {
			t.Fatalf("query %q ends inside a character (byte %d)", q, r.End)
		}
	}
}
