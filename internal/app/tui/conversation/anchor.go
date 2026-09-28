package conversation

// Anchor is a reader's place in a document.
//
// It is (block identity, offset inside that block, position among blocks),
// deliberately not a bare row number and deliberately not an index used as
// the identity.
//
// A row number alone is wrong because reflow moves rows: widening the
// terminal, or inserting new output above the reader, changes what row a
// block starts on without changing anything the reader was looking at. So the
// identity of the block is the anchor's core, and the row is carried only as
// an input to the caller's scroll computation.
//
// Index is kept because a block can genuinely vanish — a branch rewind
// rebuilds the message list as a shorter path, a branch switch replaces the
// tail, ClearRunEvents empties the run log — and when it does, the nearest
// surviving place is a question about position, which identity alone cannot
// answer.
type Anchor struct {
	// Block is the identity of the block the reader is in.
	Block BlockID
	// Offset is the position inside that block, in the block's own logical
	// rows, so it survives the block being re-laid-out at a new width.
	Offset int
	// Index is how many blocks preceded this one when the anchor was taken.
	Index int
	// Row is the screen row the reader was on. Zero when the caller does not
	// care. It is presentation input, never identity.
	Row int
}

// Resolution is the outcome of resolving an anchor against a document.
type Resolution struct {
	// Anchor is where the reader should be placed.
	Anchor Anchor
	// Found is true when the anchored block itself survived. False with a
	// non-empty Anchor means the reader was moved to a surviving neighbour.
	Found bool
	// Approximate is true when the anchored block is gone and this is the
	// nearest surviving position instead. The caller is expected to indicate
	// that the scope changed: a reader silently landed on different content
	// has lost their place without being told.
	Approximate bool
	// Empty is true when the document has no blocks at all, so there is
	// nowhere to be.
	Empty bool
}

// Resolve places the anchor in a (possibly rebuilt) document.
//
// Three outcomes, deliberately distinct:
//
//   - the block survived: Found, same offset, recomputed index. This is the
//     reflow case, and it is what keeps a scrolled reader still.
//   - the block is gone: Approximate, pointing at the nearest surviving
//     position, with Offset reset to 0. The offset is deliberately NOT
//     carried over: an offset is a position inside one block's own text, so
//     keeping it while switching blocks would point at an arbitrary place in
//     unrelated content — the exact silent jump the anchor exists to prevent.
//   - the document is empty: Empty, with no block named.
//
// "Nearest surviving position" is the anchor's block index, clamped to the
// new document. That is the correct predecessor rule for the cases where a
// block actually vanishes, because in every one of them the document stops
// being a superset of what it was: a rewind truncates, a branch switch
// replaces the tail, ClearRunEvents drops a run. Under truncation, index i in
// the old document is the block that used to follow the survivors of
// everything before i — so clamping lands on the last thing the reader can
// still see from where they were, and never past the end.
func (a Anchor) Resolve(doc *Document) Resolution {
	if doc == nil || doc.Len() == 0 {
		return Resolution{Empty: true}
	}
	if a.Block == "" {
		// A zero anchor names nothing. Reporting Found would claim a place
		// the caller never took.
		return Resolution{}
	}
	if idx, ok := doc.IndexOf(a.Block); ok {
		return Resolution{
			Anchor: Anchor{Block: a.Block, Offset: a.Offset, Index: idx, Row: a.Row},
			Found:  true,
		}
	}

	idx := a.Index
	if idx > doc.Len()-1 {
		idx = doc.Len() - 1
	}
	if idx < 0 {
		idx = 0
	}
	return Resolution{
		Anchor:      Anchor{Block: doc.Blocks()[idx].ID, Index: idx},
		Approximate: true,
	}
}
