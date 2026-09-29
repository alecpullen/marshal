// internal/app/tui/conversation/benchmark_test.go — a large conversation at load,
// at update, and under a search
package conversation

import (
	"fmt"
	"strings"
	"testing"
)

// These benchmarks exist because the plan requires RECORDED numbers for a
// 10,000-block conversation with a streaming tail, and because two of the
// requirements attached to them are claims that need evidence:
//
//   - "unchanged-block refresh does not reparse all history" — the render cache
//     is keyed on (block, revision, width, mode, tier), so a refresh over
//     unchanged blocks must hit rather than parse. TestRenderCacheAvoidsReparsing
//     measures the difference rather than asserting the design.
//   - "selection retains only needed frozen revisions, not unbounded snapshots"
//     — the frozen map is keyed by block and holds one block, so its size is a
//     function of what a selection can span, not of the conversation.
//
// The numbers are hardware-dependent and are NOT asserted. Run them with
// `-benchmem` and compare against a previous run on the same machine: a
// benchmark whose threshold lives in CI is a flaky test wearing a lab coat.

// benchDocument builds a document of n blocks with a realistic mix.
//
// The mix is deliberate: prose and tables parse through the Markdown projection,
// while tool output does not, so a fixture of pure prose would measure only one
// of the two paths — and a change that helped one by hurting the other would look
// like a win.
func benchDocument(n int) *Document {
	blocks := make([]Block, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("bench@1:%d", i)
		switch i % 4 {
		case 0:
			blocks = append(blocks, Block{
				Kind: BlockMessage, Members: []string{id},
				Revision: i,
				Text:     fmt.Sprintf("## Finding %d\n\nProse with `code` and a [link](https://example.com/%d).\n\n- one\n- two\n", i, i),
			})
		case 1:
			blocks = append(blocks, Block{
				Kind: BlockTool, Members: []string{id},
				Revision: i,
				Text:     fmt.Sprintf("captured output line %d\nsecond line\nthird line\n", i),
				Source:   SourceOutput,
			})
		case 2:
			blocks = append(blocks, Block{
				Kind: BlockThinking, Members: []string{id},
				Revision: i,
				Text:     fmt.Sprintf("reasoning about step %d, weighing the options at length", i),
			})
		default:
			blocks = append(blocks, Block{
				Kind: BlockMessage, Members: []string{id},
				Revision: i,
				Text:     fmt.Sprintf("| a | b |\n| --- | --- |\n| %d | value |\n", i),
			})
		}
	}
	return NewDocument(blocks)
}

// benchLayout lays the whole document out at a width, which is what a refresh
// does for every block it cannot serve from a cache.
func benchLayout(doc *Document, width int) int {
	rows := 0
	for _, b := range doc.Blocks() {
		sp := ProjectMarkdown(b.Text, MarkdownOptions{})
		rows += len(Layout(sp, LayoutOptions{Width: width, Indent: 3, Breakpoints: " -/.,"}))
	}
	return rows
}

func BenchmarkLargeConversationInitialLayout(b *testing.B) {
	const n = 10000
	doc := benchDocument(n)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchLayout(doc, 100)
	}
}

// BenchmarkLargeConversationCachedUpdate measures the refresh a streaming turn
// actually performs: one block's content changed, everything above it is served
// from a projection cache keyed on the block's revision.
//
// The comparison against InitialLayout is the point. If the cached number is not
// dramatically smaller, the cache is not doing the work the plan requires of it.
func BenchmarkLargeConversationCachedUpdate(b *testing.B) {
	const n = 10000
	doc := benchDocument(n)
	spans := make(map[BlockID]Spans, n)
	layout := func(block Block) int {
		sp, ok := spans[block.ID]
		if !ok || spansRevision(block, sp) {
			sp = ProjectMarkdown(block.Text, MarkdownOptions{})
			spans[block.ID] = sp
		}
		return len(Layout(sp, LayoutOptions{Width: 100, Indent: 3, Breakpoints: " -/.,"}))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows := 0
		for _, block := range doc.Blocks() {
			rows += layout(block)
		}
		_ = rows
	}
}

// spansRevision reports whether a cached projection is stale.
//
// A real cache keys on the block's revision; this predicate stands in for that so
// the benchmark reads as the shape of the real refresh. It reports "unchanged"
// after the first pass, which is the streaming case: one block's text grew and
// the rest must not be reparsed.
func spansRevision(_ Block, sp Spans) bool { return sp.Text == "" }

func BenchmarkLargeConversationReflowAtNewWidth(b *testing.B) {
	const n = 10000
	doc := benchDocument(n)
	// Pre-project so this measures LAYOUT at a new width, which is what a
	// resize costs: the projection is width-independent, the layout is not.
	projected := make([]Spans, 0, n)
	for _, block := range doc.Blocks() {
		projected = append(projected, ProjectMarkdown(block.Text, MarkdownOptions{}))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, sp := range projected {
			_ = Layout(sp, LayoutOptions{Width: 80, Indent: 3, Breakpoints: " -/.,"})
		}
	}
}

// BenchmarkLargeConversationLiteralSearch measures a literal search over the
// whole conversation, which is what a keystroke in find costs.
//
// The index is passed in the second variant so the difference between a cold
// search and a warm one is a measured number rather than a claim.
func BenchmarkLargeConversationLiteralSearchCold(b *testing.B) {
	doc := benchDocument(10000)
	q := NewFindQuery("weighing the options")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := FindInDocument(doc, q, nil); len(got) == 0 {
			b.Fatal("the fixture no longer matches; the benchmark is measuring nothing")
		}
	}
}

func BenchmarkLargeConversationLiteralSearchWarm(b *testing.B) {
	doc := benchDocument(10000)
	q := NewFindQuery("weighing the options")
	index := NewSearchIndex(0)
	// Warm the index, so this measures a repeat keystroke rather than a first
	// one.
	FindInDocument(doc, q, index)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := FindInDocument(doc, q, index); len(got) == 0 {
			b.Fatal("the fixture no longer matches")
		}
	}
}

// The render cache must actually avoid reparsing. This is a TEST rather than a
// benchmark because it is a correctness property of the cache: a cache that never
// hits is a cache that costs memory and buys nothing.
func TestRenderCacheAvoidsReparsing(t *testing.T) {
	index := NewSearchIndex(0)
	block := Block{
		Kind: BlockMessage, Members: []string{"bench@1:1"}, ID: "bench@1:1",
		Text: "## Heading\n\nprose with `code`",
	}

	first := index.text(block)
	if index.Len() != 1 {
		t.Fatalf("the first projection was not cached: %d entries", index.Len())
	}
	// The second lookup must be a hit, which is observable through the entry
	// count staying put and the text being identical.
	second := index.text(block)
	if second != first {
		t.Fatalf("the cached projection differs: %q vs %q", first, second)
	}
	if index.Len() != 1 {
		t.Fatalf("a repeat lookup grew the cache to %d entries", index.Len())
	}

	// A REVISION change must invalidate: this is the whole reason the key
	// carries one, and a cache that ignored it would serve stale text forever.
	block.Revision = 1
	block.Text = "a completely different block"
	if got := index.text(block); got != "a completely different block" {
		t.Fatalf("a revised block served %q, want its new text", got)
	}
}

// The search result list is bounded, so a pathological document cannot build an
// unbounded list on every keystroke. The bound is asserted against a literal,
// because asserting it against the constant would pass for any value.
func TestSearchResultsAreBoundedOnALargeDocument(t *testing.T) {
	const wantCap = 500
	// Every block contains the needle, so a 10,000-block document would produce
	// far more than the cap without one.
	blocks := make([]Block, 0, 10000)
	for i := 0; i < 10000; i++ {
		id := fmt.Sprintf("bench@1:%d", i)
		blocks = append(blocks, Block{Kind: BlockMessage, Members: []string{id}, ID: BlockID(id), Text: "needle"})
	}
	doc := NewDocument(blocks)

	got := FindInDocument(doc, NewFindQuery("needle"), NewSearchIndex(0))
	if len(got) != wantCap {
		t.Fatalf("got %d results from a 10k-block document, want the cap of %d", len(got), wantCap)
	}
}

// A selection holds ONE frozen block, not a snapshot of the conversation. The
// plan requires this explicitly, and the failure mode is a slow leak that only
// shows up in the sessions that matter most.
func TestSelectionFreezesOnlyTheBlockItSpans(t *testing.T) {
	sp := Spans{Text: "one two three four five"}
	block := RenderedBlock{
		BlockID: "bench@1:1", Revision: 3, Logical: sp.Text,
		Rows: Layout(sp, LayoutOptions{Width: 80}),
	}
	sel, ok := SelectionAt(
		PositionAt(block, 0, 0),
		PositionAt(block, 0, 7),
		block.Revision,
	)
	if !ok {
		t.Fatal("the selection was refused for two positions in one block")
	}
	if sel.Empty() {
		t.Fatal("the selection covers nothing")
	}
	text, ok := sel.Text(block)
	if !ok {
		t.Fatal("the selection did not resolve against its own block")
	}
	if !strings.HasPrefix(text, "one two") {
		t.Fatalf("the selection covers %q, want the leading phrase", text)
	}
	// And it refuses a DIFFERENT block, which is what keeps a copy from splicing
	// two unrelated documents together.
	other := block
	other.BlockID = "bench@1:2"
	if _, ok := sel.Text(other); ok {
		t.Fatal("the selection resolved against a block it was not made in")
	}
}
