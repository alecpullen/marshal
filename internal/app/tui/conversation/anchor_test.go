package conversation

import "testing"

// makeDoc builds a document from member strings, so a test can describe a
// transcript as a list of names.
func makeDoc(members ...string) *Document {
	blocks := make([]Block, 0, len(members))
	for _, m := range members {
		blocks = append(blocks, Block{Kind: BlockMessage, Members: []string{m}})
	}
	return NewDocument(blocks)
}

// An anchor names a block and an offset inside it. Resolving against an
// unchanged document returns the same place.
func TestAnchorRoundTrips(t *testing.T) {
	doc := makeDoc("msg:1", "msg:2", "msg:3")

	res := Anchor{Block: "msg:2", Offset: 3, Index: 1, Row: 12}.Resolve(doc)
	if !res.Found {
		t.Fatal("anchor did not resolve against its own document")
	}
	if res.Anchor.Block != "msg:2" || res.Anchor.Offset != 3 {
		t.Fatalf("resolved to %+v, want msg:2 +3", res.Anchor)
	}
	if res.Anchor.Index != 1 {
		t.Fatalf("index = %d, want 1", res.Anchor.Index)
	}
	if res.Approximate {
		t.Fatal("a surviving block must not be reported as approximate")
	}
}

// THE reflow requirement: inserting content ABOVE the anchored block must not
// move the reader. The block keeps its identity and its internal offset even
// though its index shifted.
func TestAnchorSurvivesInsertionAbove(t *testing.T) {
	a := Anchor{Block: "msg:2", Offset: 3, Index: 1}

	// A new block arrives before the anchored one — new output streaming in
	// above a scrolled reader.
	after := makeDoc("msg:1", "msg:new", "msg:2", "msg:3")

	res := a.Resolve(after)
	if !res.Found {
		t.Fatal("anchor lost its block after an insertion above it")
	}
	if res.Anchor.Block != "msg:2" || res.Anchor.Offset != 3 {
		t.Fatalf("resolved to %+v, want the same block and offset", res.Anchor)
	}
	if res.Anchor.Index != 2 {
		t.Fatalf("index = %d, want 2 (the block moved down as content arrived above)", res.Anchor.Index)
	}
}

// Appending below the anchor is the common case during streaming and must not
// move the reader either.
func TestAnchorSurvivesAppendBelow(t *testing.T) {
	a := Anchor{Block: "msg:2", Offset: 1, Index: 1}

	after := makeDoc("msg:1", "msg:2", "msg:3", "msg:4")

	res := a.Resolve(after)
	if !res.Found || res.Anchor.Block != "msg:2" || res.Anchor.Offset != 1 || res.Anchor.Index != 1 {
		t.Fatalf("resolved to %+v, want msg:2 +1 at index 1", res.Anchor)
	}
}

// Reflow by itself — the same blocks at a different width — does not change
// any identity or index. Only rows move, and the anchor does not carry a
// resolved row.
func TestAnchorUnaffectedByReflowAtSameContent(t *testing.T) {
	a := Anchor{Block: "msg:2", Offset: 2, Index: 1}

	res := a.Resolve(makeDoc("msg:1", "msg:2", "msg:3"))
	if !res.Found || res.Anchor.Block != "msg:2" || res.Anchor.Index != 1 {
		t.Fatalf("resolved to %+v", res.Anchor)
	}
}

// When the anchored block is gone — a rewind, a branch switch, a cleared run
// event log — the anchor must not silently claim success on a different
// block. It reports the change and names where the reader ended up.
func TestAnchorVanishedBlockReportsNearestSurvivor(t *testing.T) {
	// The reader was on msg:2 of three blocks.
	a := Anchor{Block: "msg:2", Offset: 4, Index: 1}

	// A rewind truncated msg:2 and msg:3 away entirely.
	after := makeDoc("msg:1")

	res := a.Resolve(after)
	if res.Found {
		t.Fatalf("expected the vanished block to be reported, got %+v", res)
	}
	if !res.Approximate {
		t.Fatal("a fallback placement must be flagged approximate")
	}
	if res.Anchor.Block != "msg:1" {
		t.Fatalf("fallback block = %q, want the last surviving block msg:1", res.Anchor.Block)
	}
	if res.Anchor.Offset != 0 {
		t.Fatalf("fallback offset = %d, want 0: a block-relative offset is meaningless on a different block",
			res.Anchor.Offset)
	}
}

// A middle-of-document deletion lands on the block that now occupies the
// reader's old position — never past the end of the document.
func TestAnchorVanishedInMiddleLandsInBounds(t *testing.T) {
	a := Anchor{Block: "msg:3", Offset: 0, Index: 2}

	after := makeDoc("msg:1", "msg:2")
	res := a.Resolve(after)

	if res.Found || !res.Approximate {
		t.Fatalf("expected an approximate resolution, got %+v", res)
	}
	if res.Anchor.Index >= after.Len() {
		t.Fatalf("fallback index %d is past the end of a %d-block document", res.Anchor.Index, after.Len())
	}
	if _, ok := after.Block(res.Anchor.Block); !ok {
		t.Fatalf("fallback names a block that is not in the document: %q", res.Anchor.Block)
	}
}

// With no blocks at all there is nowhere to be, and the result says so rather
// than naming a block that does not exist.
func TestAnchorEmptyDocument(t *testing.T) {
	res := Anchor{Block: "msg:2", Index: 1}.Resolve(makeDoc())

	if !res.Empty {
		t.Fatalf("expected Empty, got %+v", res)
	}
	if res.Anchor.Block != "" {
		t.Fatalf("fallback block = %q, want empty", res.Anchor.Block)
	}
	if res.Found || res.Approximate {
		t.Fatalf("an empty document is neither found nor approximate: %+v", res)
	}
}

// A nil document is treated as empty rather than panicking: the caller may
// legitimately have no document yet.
func TestAnchorNilDocument(t *testing.T) {
	if res := (Anchor{Block: "msg:1"}).Resolve(nil); !res.Empty {
		t.Fatalf("resolving against nil = %+v, want Empty", res)
	}
}

// The anchor must be a comparable value: a reader's position is data a caller
// can store, copy and compare, not a pointer into a structure a rebuild
// invalidates.
func TestAnchorIsComparableValue(t *testing.T) {
	a := Anchor{Block: "msg:1", Offset: 2, Index: 0}
	b := a
	if a != b {
		t.Fatalf("equal anchors compare unequal: %+v vs %+v", a, b)
	}
}

// An anchor naming no block is unresolved rather than silently matching the
// first block.
func TestZeroAnchorDoesNotResolve(t *testing.T) {
	res := Anchor{}.Resolve(makeDoc("msg:1", "msg:2"))
	if res.Found || res.Approximate || res.Empty {
		t.Fatalf("zero anchor produced %+v, want a bare unresolved result", res)
	}
	if res.Anchor.Block != "" {
		t.Fatalf("zero anchor named block %q, want empty", res.Anchor.Block)
	}
}

// Resolving must not mutate the document: the anchor is read-only over it.
func TestResolveDoesNotMutateDocument(t *testing.T) {
	doc := makeDoc("msg:1", "msg:2")
	before := doc.Len()

	_, _ = doc.IndexOf("msg:2")
	_ = Anchor{Block: "msg:2", Offset: 4, Index: 1}.Resolve(doc)
	_ = Anchor{Block: "gone", Index: 9}.Resolve(doc)
	_ = Anchor{}.Resolve(doc)

	if doc.Len() != before {
		t.Fatalf("document changed: %d blocks, want %d", doc.Len(), before)
	}
}

// Resolving twice is idempotent: the second resolution of the same anchor
// against the same document lands in the same place.
func TestResolveIsDeterministic(t *testing.T) {
	doc := makeDoc("msg:1", "msg:2", "msg:3")
	a := Anchor{Block: "msg:2", Offset: 1, Index: 1}

	first := a.Resolve(doc)
	second := a.Resolve(doc)
	if first != second {
		t.Fatalf("resolutions differ:\n%+v\n%+v", first, second)
	}
}
