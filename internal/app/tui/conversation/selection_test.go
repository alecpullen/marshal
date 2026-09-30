package conversation

import (
	"strings"
	"testing"
)

// The fixtures here are chosen for the arithmetic, not the prose: a wrapped
// block exercises the multi-row path, a wide-character block exercises grapheme
// snapping, and a block with a hard break exercises "the author's newline is
// content".
var selectionFixtures = []struct {
	name  string
	text  string
	width int
}{
	{"one row", "alpha beta gamma", 80},
	{"wrapped", "alpha beta gamma delta epsilon zeta eta theta iota kappa", 20},
	{"hard break", "first line\nsecond line", 80},
	{"wide characters", "日本語のテキストです", 12},
	{"emoji", "done ✅ 👩🏽‍💻 ok", 80},
	{"tabs", "a\tb\tc", 80},
	{"empty", "", 40},
}

// A selection names WHERE it starts and ends, and the text between them.
//
// The starting point of the whole design: a selection is (block, offset) pairs,
// not row numbers. A row number is meaningless across a reflow, so a selection
// built from one would silently point at different text after a resize.
func TestSelectionExtendsFromAnchorToFocus(t *testing.T) {
	block := layoutText("hello world", 80)
	sel := Selection{Block: block.BlockID, Anchor: 0, Focus: 5}
	got, ok := sel.Text(block)
	if !ok {
		t.Fatal("a selection inside a block produced no text")
	}
	if got != "hello" {
		t.Fatalf("selection = %q, want %q", got, "hello")
	}
}

// A backwards drag selects the same text as a forward one. A reader who drags
// right-to-left is selecting the same region, and producing a different string —
// or nothing — would be a bug they would blame on the terminal.
func TestABackwardsSelectionIsTheSameAsAForwardsOne(t *testing.T) {
	block := layoutText("hello world", 80)
	forward := Selection{Block: block.BlockID, Anchor: 0, Focus: 5}
	backward := Selection{Block: block.BlockID, Anchor: 5, Focus: 0}
	a, _ := forward.Text(block)
	b, _ := backward.Text(block)
	if a != b {
		t.Fatalf("forward gave %q, backward gave %q", a, b)
	}
	if a != "hello" {
		t.Fatalf("selection = %q, want %q", a, "hello")
	}
}

// A selection of nothing (anchor == focus) is empty text but still a valid
// selection: the reader has a caret, and "clear it" and "there is nothing here"
// are different states.
func TestAnEmptySelectionIsValidAndEmpty(t *testing.T) {
	block := layoutText("hello", 80)
	sel := Selection{Block: block.BlockID, Anchor: 3, Focus: 3}
	got, ok := sel.Text(block)
	if !ok {
		t.Fatal("an empty selection was rejected")
	}
	if got != "" {
		t.Fatalf("empty selection gave %q", got)
	}
	if sel.Empty() != true {
		t.Fatal("Empty() should report true for a zero-width selection")
	}
}

// A selection resolves against the block it was made in, by IDENTITY. Pointing
// it at a different block must fail rather than return that block's text: the
// caller asked for what was selected, and answering with a different document's
// characters is worse than answering nothing.
func TestSelectionRefusesToResolveAgainstAnotherBlock(t *testing.T) {
	a := layoutText("alpha", 80)
	b := layoutText("bravo", 80)
	b.BlockID = "msg:2"
	a.BlockID = "msg:1"

	sel := Selection{Block: a.BlockID, Anchor: 0, Focus: 5}
	if got, ok := sel.Text(b); ok {
		t.Fatalf("a selection in %q resolved against %q and returned %q", a.BlockID, b.BlockID, got)
	}
	if _, ok := sel.Text(a); !ok {
		t.Fatal("a selection in the right block failed to resolve")
	}
}

// A selection across several rows includes the whitespace between them that the
// wrap consumed — but does NOT include a newline the author did not write.
//
// This is the plan's central rendering invariant reaching the selection: a soft
// wrap is not content, so dragging down a paragraph must not paste a broken
// line, while dragging across a real paragraph break must.
func TestASelectionAcrossASoftWrapJoinsWithASpaceNotANewline(t *testing.T) {
	block := layoutText("alpha beta gamma delta", 12)
	if len(block.Rows) < 2 {
		t.Fatalf("the fixture did not wrap: %q", rowTexts(block))
	}
	sel := Selection{Block: block.BlockID, Anchor: 0, Focus: len(block.Logical)}
	got, ok := sel.Text(block)
	if !ok {
		t.Fatal("a whole-block selection failed")
	}
	if got != "alpha beta gamma delta" {
		t.Fatalf("selected %q, want the text with its spaces intact", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("a soft wrap put a newline in the selection: %q", got)
	}
}

// ...and the author's own break IS content, so it survives.
func TestASelectionAcrossAHardBreakKeepsTheNewline(t *testing.T) {
	block := layoutText("first line\nsecond line", 80)
	sel := Selection{Block: block.BlockID, Anchor: 0, Focus: len(block.Logical)}
	got, ok := sel.Text(block)
	if !ok {
		t.Fatal("a whole-block selection failed")
	}
	if got != "first line\nsecond line" {
		t.Fatalf("selected %q, want the author's newline kept", got)
	}
}

// A selection's bounds are clamped to the block, so a drag that ran off either
// end selects to the end rather than panicking or returning an error.
func TestSelectionClampsToTheBlock(t *testing.T) {
	block := layoutText("hello", 80)
	for _, sel := range []Selection{
		{Block: block.BlockID, Anchor: -100, Focus: 999},
		{Block: block.BlockID, Anchor: 999, Focus: -100},
	} {
		got, ok := sel.Text(block)
		if !ok {
			t.Fatalf("an out-of-range selection %+v was rejected", sel)
		}
		if got != "hello" {
			t.Fatalf("an out-of-range selection gave %q, want the whole text", got)
		}
	}
}

// A selection is built from a POSITION, and a position's offsets must be
// grapheme boundaries. Snapping happens where the position is created, so a
// selection can never cut a grapheme in half however the position was derived.
func TestSelectionEndsAreGraphemeBoundaries(t *testing.T) {
	block := layoutText("a中b", 80)
	// Cell 2 is the continuation cell of the wide character.
	pos := PositionAt(block, 0, 2)
	if got := SnapToBoundary(block.Logical, pos.Offset); got != pos.Offset {
		t.Fatalf("a position at cell 2 has offset %d, which is not a boundary", pos.Offset)
	}
	sel := Selection{Block: block.BlockID, Anchor: 0, Focus: pos.Offset}
	got, ok := sel.Text(block)
	if !ok {
		t.Fatal("selection failed")
	}
	if got != "a" {
		t.Fatalf("selecting through the wide character's continuation cell gave %q, want %q",
			got, "a")
	}
}

// A position carries the row it came from, so a caller can keep the reader's
// place. It must be the row the cell is actually ON, not a row the caller
// guessed: a drag that extends downward depends on knowing where each end sits.
func TestPositionCarriesTheRowItCameFrom(t *testing.T) {
	block := layoutText("alpha beta gamma delta", 12)
	if len(block.Rows) < 2 {
		t.Fatalf("the fixture did not wrap: %q", rowTexts(block))
	}
	if pos := PositionAt(block, 0, 0); pos.Row != 0 {
		t.Fatalf("a position at row 0 reports row %d", pos.Row)
	}
	if pos := PositionAt(block, 1, 0); pos.Row != 1 {
		t.Fatalf("a position at row 1 reports row %d", pos.Row)
	}
	// A row past the end clamps to the LAST row, not the first: a drag that
	// runs off the bottom of the terminal is a selection to the end of the
	// block, and clamping it to the top would select everything instead.
	last := len(block.Rows) - 1
	if pos := PositionAt(block, 99, 0); pos.Row != last {
		t.Fatalf("a row past the end reports row %d, want the last row (%d)", pos.Row, last)
	}
	// A row before the start clamps to the first.
	if pos := PositionAt(block, -5, 0); pos.Row != 0 {
		t.Fatalf("a row before the start reports row %d, want 0", pos.Row)
	}
}

// A word-wise step walks to the next WORD EDGE: forward from inside a word
// reaches the end of that word, and forward from a space reaches the start of
// the next word.
//
// The properties asserted here are the ones a key handler depends on, rather
// than the exact intermediate offsets, which are a design choice:
//
//   - every step lands on a grapheme boundary (so a selection can cut there);
//   - repeated steps always make progress and terminate at the end (a key that
//     stops moving is indistinguishable from a broken one);
//   - the ends are absorbing (no step runs off the block);
//   - forward and backward are monotone toward their respective ends.
func TestWordStepsWalkWordEdgesAndAlwaysTerminate(t *testing.T) {
	const text = "alpha beta gamma"
	// Forward from the start of "beta" reaches the end of "beta".
	from := strings.Index(text, "beta")
	if got := nextWordBoundary(text, from); got != from+len("beta") {
		t.Fatalf("nextWordBoundary from %d = %d, want the end of the word (%d)",
			from, got, from+len("beta"))
	}
	// Forward from the space before "beta" reaches the START of "beta".
	if got := nextWordBoundary(text, from-1); got != from {
		t.Fatalf("nextWordBoundary from a space = %d, want the next word's start (%d)",
			got, from)
	}

	// Forward from the start advances strictly and terminates at the end.
	off := 0
	for i := 0; i < 50; i++ {
		next := nextWordBoundary(text, off)
		if next <= off {
			t.Fatalf("nextWordBoundary made no progress at %d (stayed at %d)", off, next)
		}
		if snapped := SnapToBoundary(text, next); snapped != next {
			t.Fatalf("nextWordBoundary returned %d, which is not a grapheme boundary", next)
		}
		off = next
		if off == len(text) {
			break
		}
	}
	if off != len(text) {
		t.Fatalf("repeated NextWordBoundary did not reach the end: stopped at %d of %d",
			off, len(text))
	}
	if got := nextWordBoundary(text, len(text)); got != len(text) {
		t.Fatalf("nextWordBoundary at the end = %d, want the end (absorbing)", got)
	}

	// Backward goes the same way, in reverse.
	off = len(text)
	for i := 0; i < 50; i++ {
		prev := prevWordBoundary(text, off)
		if prev >= off {
			t.Fatalf("prevWordBoundary made no progress at %d (stayed at %d)", off, prev)
		}
		if snapped := SnapToBoundary(text, prev); snapped != prev {
			t.Fatalf("prevWordBoundary returned %d, which is not a grapheme boundary", prev)
		}
		off = prev
		if off == 0 {
			break
		}
	}
	if off != 0 {
		t.Fatalf("repeated PrevWordBoundary did not reach the start: stopped at %d", off)
	}
	if got := prevWordBoundary(text, 0); got != 0 {
		t.Fatalf("prevWordBoundary at the start = %d, want 0 (absorbing)", got)
	}
}

// A word extension over CJK text, which has no spaces, must still make progress
// rather than returning the offset it was given: a key that does nothing at all
// is indistinguishable from a broken one.
func TestWordExtensionTerminatesOnTextWithoutSpaces(t *testing.T) {
	const text = "日本語のテキスト"
	got := nextWordBoundary(text, 0)
	if got <= 0 {
		t.Fatalf("nextWordBoundary made no progress on unspaced text: %d", got)
	}
	if got > len(text) {
		t.Fatalf("nextWordBoundary ran past the end: %d > %d", got, len(text))
	}
	if snapped := SnapToBoundary(text, got); snapped != got {
		t.Fatalf("the boundary %d splits a grapheme", got)
	}
}

// A selection carries the block's REVISION it was made against, so a caller can
// tell "this text changed under the selection" from "this is the same text".
// Freezing the revision is what stops streaming output from changing bytes a
// reader already selected.
func TestSelectionRecordsTheRevisionItWasMadeAgainst(t *testing.T) {
	block := layoutText("hello", 80)
	block.Revision = 7
	sel := Selection{Block: block.BlockID, Revision: block.Revision, Anchor: 0, Focus: 5}
	if !sel.MatchesRevision(block) {
		t.Fatal("a selection made against revision 7 does not match revision 7")
	}
	block.Revision = 8
	if sel.MatchesRevision(block) {
		t.Fatal("a selection made against revision 7 claims to match revision 8")
	}
}

// The whole-block selection of an empty block is empty and valid, not an error:
// a reader can select nothing, and reporting failure would make a caller treat
// "nothing selected yet" as "something went wrong".
func TestSelectingAnEmptyBlockIsEmptyNotInvalid(t *testing.T) {
	block := layoutText("", 40)
	sel := Selection{Block: block.BlockID, Anchor: 0, Focus: 0}
	got, ok := sel.Text(block)
	if !ok {
		t.Fatal("a selection in an empty block was rejected")
	}
	if got != "" {
		t.Fatalf("an empty block yielded %q", got)
	}
}

// A wrap consumes a space, and a reader who drags to the end of a wrapped line
// has selected up to the visible text. Copying the invisible space after it would
// put a trailing space on the clipboard that they cannot see — but only a wrap
// may be trimmed, because the author's own trailing whitespace IS content.
func TestOnlyASoftWrapEndingIsEligibleForTrailingSpaceTrimming(t *testing.T) {
	wrapped := layoutText("hello world", 8)
	// The end of row 0 is the consumed space.
	softEnd := wrapped.Rows[0].Range.End
	if !wrapped.EndsAtSoftWrap(softEnd) {
		t.Fatalf("offset %d is the end of a wrapped row but is not reported as one", softEnd)
	}
	// The end of the whole block is not a wrap ending.
	if wrapped.EndsAtSoftWrap(len(wrapped.Logical)) {
		t.Fatal("the end of the block was reported as a soft-wrap ending")
	}

	// A hard-broken row's end is the author's, so it is NOT a wrap ending even
	// though the line may end with spaces.
	authored := layoutText("keep   \nnext", 40)
	authoredEnd := authored.Rows[0].Range.End
	if authored.EndsAtSoftWrap(authoredEnd) {
		t.Fatal("the author's own line ending was reported as a soft wrap")
	}
	// The trimmer itself is unconditional: deciding WHEN to call it is the
	// caller's job, and it is the part that knows where the selection ended.
	if got := TrimSelectedSpace("keep   "); got != "keep" {
		t.Fatalf("TrimSelectedSpace(%q) = %q", "keep   ", got)
	}
}

// rowTexts is a test helper for a failure message.
func rowTexts(block RenderedBlock) []string {
	out := make([]string, 0, len(block.Rows))
	for _, r := range block.Rows {
		out = append(out, r.Text())
	}
	return out
}

// layoutText builds a laid-out block for selection tests.
func layoutText(text string, width int) RenderedBlock {
	sp := Spans{Text: text}
	return RenderedBlock{
		BlockID: "msg:1",
		Width:   width,
		Logical: text,
		Rows:    Layout(sp, LayoutOptions{Width: width, Breakpoints: "/-:"}),
	}
}
