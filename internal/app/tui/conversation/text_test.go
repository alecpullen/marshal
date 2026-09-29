package conversation

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// textFixtures are the shapes the width and offset arithmetic has to survive.
//
// They are chosen for the arithmetic rather than for the prose: a path is a
// long unbroken token a wrap has to break somewhere, CJK and the emoji
// sequences are graphemes that occupy two cells, the combining marks are
// graphemes that occupy several bytes in one cell, and the regional
// indicators are the pair implementations most often disagree about.
//
// The empty string is a fixture on purpose. Every one of these functions is
// reached with an empty text the first time a block renders, and off-by-one
// arithmetic on "" is the case that shows up as a panic in production rather
// than as a failure here.
var textFixtures = []struct {
	name string
	text string
}{
	{"ascii", "hello world"},
	{"path", "/Users/alec/projects/marshal/internal/app/tui/conversation/text.go:1-40"},
	{"long unbroken token", strings.Repeat("a", 300)},
	{"cjk", "日本語のテキスト"},
	{"combining mark", "égalité à la carte"},
	{"emoji sequence", "done ✅ 👩🏽‍💻 🏳️‍🌈"},
	{"regional indicators", "🇬🇧 and 🇯🇵"},
	{"trailing whitespace", "line one   \nline two  \n"},
	{"empty", ""},
}

// A mapping is only usable if it agrees with the width function the rest of
// the app measures lines with. Two notions of "how wide is this" would mean a
// row the renderer believes fits and the terminal believes overflows.
func TestCellWidthMatchesTheWidthTheRestOfTheAppMeasures(t *testing.T) {
	for _, f := range textFixtures {
		t.Run(f.name, func(t *testing.T) {
			if got, want := CellWidth(f.text), ansi.StringWidth(f.text); got != want {
				t.Fatalf("CellWidth(%q) = %d, want %d (ansi.StringWidth)", f.text, got, want)
			}
		})
	}
}

// A tab is ONE byte of logical text and up to eight cells of screen. Copy has
// to see the byte, layout has to see the cells, and a mapping that gets this
// wrong misplaces every cell after the first tab on a line.
//
// This is deliberately NOT what ansi.StringWidth reports: it treats "\t" as a
// control character and gives it no width at all, while the terminal advances
// the cursor to the next tab stop. The TUI's own expandTabs exists because of
// that gap, and this function closes it at the mapping layer instead.
func TestCellWidthExpandsTabsToTheNextTabStop(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"a\tb", 9},          // a at column 0, tab to 8, b at column 8
		{"\t", 8},            // tab from column 0 fills eight cells
		{"a\t\tb", 17},       // second tab starts at 8, so it fills eight more
		{"12345678\t", 16},   // tab from a tab stop advances a full stop
		{"a\t\n\tb", 17},     // a newline resets the column, so the second tab re-expands
		{"no tabs here", 12}, // absent tabs change nothing
	}
	for _, c := range cases {
		if got := CellWidth(c.text); got != c.want {
			t.Errorf("CellWidth(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

// A click lands on a cell, but a copy has to cut text at a byte. Snapping to
// the start of the grapheme the offset lands in is what makes the two agree:
// cutting inside a grapheme would put half a combining mark or half a ZWJ
// sequence on the clipboard.
func TestSnapToBoundaryTakesTheStartOfTheGraphemeItLandsIn(t *testing.T) {
	cases := []struct {
		name string
		text string
		off  int
		want int
	}{
		{"at an ascii boundary", "abc", 1, 1},
		{"inside a combining mark", "e\u0301", 1, 0},
		{"inside a combining mark, byte 2", "e\u0301", 2, 0},
		{"at the end of a combining mark", "e\u0301", 3, 3},
		{"inside a wide rune", "a中b", 2, 1},
		{"inside a wide rune, last byte", "a中b", 3, 1},
		{"after a wide rune", "a中b", 4, 4},
		{"inside a ZWJ sequence", "👩🏽‍💻", 4, 0},
		{"past the end", "abc", 99, 3},
		{"before the start", "abc", -5, 0},
		{"empty text", "", 4, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SnapToBoundary(c.text, c.off); got != c.want {
				t.Fatalf("SnapToBoundary(%q, %d) = %d, want %d", c.text, c.off, got, c.want)
			}
		})
	}
}

// Grapheme-wise cursor movement is what Task 12's arrow keys extend a
// selection by. Stepping by a rune would stop between a base character and its
// combining mark, which is a position a copy cannot name.
//
// The input is clamped before stepping, so a position before the start behaves
// as position zero: it advances to the first boundary rather than refusing to
// move. A position past the end is already at the last boundary and cannot
// advance further.
func TestNextAndPrevGraphemeStepWholeClusters(t *testing.T) {
	const text = "a中e\u0301b" // byte offsets: a=0, 中=1..3, e+mark=4..6, b=7
	for _, c := range []struct{ from, want int }{
		{0, 1}, {1, 4}, {4, 7}, {7, 8}, {8, 8}, {99, 8}, {-3, 1},
	} {
		if got := NextGrapheme(text, c.from); got != c.want {
			t.Errorf("NextGrapheme(%q, %d) = %d, want %d", text, c.from, got, c.want)
		}
	}
	// A position past the end is clamped to the end first, so it steps back to
	// the last boundary before it — the same place position 8 reaches.
	for _, c := range []struct{ from, want int }{
		{8, 7}, {7, 4}, {4, 1}, {1, 0}, {0, 0}, {99, 7}, {-3, 0},
	} {
		if got := PrevGrapheme(text, c.from); got != c.want {
			t.Errorf("PrevGrapheme(%q, %d) = %d, want %d", text, c.from, got, c.want)
		}
	}
}

// A wide grapheme owns two cells, and the second of them is not a place text
// can be cut. A click there belongs to the character the reader aimed at.
func TestOffsetForCellClampsWideContinuationCellsToTheirGrapheme(t *testing.T) {
	const text = "a中b" // cells: a=0, 中=1..2, b=3
	for _, c := range []struct{ cell, want int }{
		{0, 0},
		{1, 1},
		{2, 1}, // the continuation cell of 中 clamps to 中's start
		{3, 4},
		{4, 5},  // one past the end is the end
		{99, 5}, // far past the end is still the end
		{-1, 0},
	} {
		if got := OffsetForCell(text, c.cell, 8); got != c.want {
			t.Errorf("OffsetForCell(%q, %d) = %d, want %d", text, c.cell, got, c.want)
		}
	}
}

// An emoji sequence is one grapheme across many bytes: a click anywhere on it
// names its start, and its cells are two however many bytes it took.
func TestOffsetForCellTreatsAnEmojiSequenceAsOneGrapheme(t *testing.T) {
	const seq = "👩🏽‍💻"
	const text = seq + "!" // the sequence is two cells, "!" one
	if got := OffsetForCell(text, 1, 8); got != 0 {
		t.Errorf("OffsetForCell(%q, 1) = %d, want 0", text, got)
	}
	if got := OffsetForCell(text, 2, 8); got != len(seq) {
		t.Errorf("OffsetForCell(%q, 2) = %d, want %d", text, got, len(seq))
	}
	if got := CellWidth(text); got != 3 {
		t.Errorf("CellWidth(%q) = %d, want 3", text, got)
	}
}

// CellForOffset is the inverse a renderer needs: given a logical offset, which
// column does that byte start at. Round-tripping is what lets a selection
// survive a resize, because the geometry is recomputed while the offsets stay
// put.
//
// The property is checked per DISPLAY LINE, not over the whole fixture. A cell
// is a position within one row, so it cannot name a position in a multi-line
// string: column 0 is both the first character and the first character after
// every newline. Testing it across a newline would be asserting something
// untrue about the coordinate, not about the implementation.
func TestCellForOffsetIsTheInverseOfOffsetForCellOnGraphemes(t *testing.T) {
	for _, f := range textFixtures {
		t.Run(f.name, func(t *testing.T) {
			for _, line := range strings.Split(f.text, "\n") {
				for off := 0; off <= len(line); off++ {
					if snapped := SnapToBoundary(line, off); snapped != off {
						continue // a continuation byte is not a position text can be cut at
					}
					cell := CellForOffset(line, off, 8)
					if got := OffsetForCell(line, cell, 8); got != off {
						t.Fatalf("line %q: OffsetForCell(CellForOffset(%d) = %d) = %d, want %d",
							line, off, cell, got, off)
					}
				}
			}
		})
	}
}

// The column a tab lands on depends on the column it started at, so
// CellForOffset has to accumulate the expansion rather than count a tab as one
// byte of one cell.
func TestCellForOffsetCountsExpandedTabCells(t *testing.T) {
	const text = "a\tb"
	for _, c := range []struct{ off, want int }{
		{0, 0},
		{1, 1}, // the tab itself starts at column 1
		{2, 8}, // b is pushed out to the tab stop
		{3, 9},
		{99, 9},
	} {
		if got := CellForOffset(text, c.off, 8); got != c.want {
			t.Errorf("CellForOffset(%q, %d) = %d, want %d", text, c.off, got, c.want)
		}
	}
}
