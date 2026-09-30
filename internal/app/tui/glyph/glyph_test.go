package glyph

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// glyphName pairs a vocabulary constant with its name, so a failure names the
// glyph instead of printing an opaque rune.
type glyphName struct {
	name  string
	glyph string
}

// allGlyphs is the full vocabulary, written out rather than reflected over the
// const block: adding a constant without adding it here is then visible in
// review, which reflection would hide.
var allGlyphs = []glyphName{
	{"Rail", Rail},
	{"User", User},
	{"Running", Running},
	{"Ambient", Ambient},
	{"OK", OK},
	{"Error", Error},
	{"Warning", Warning},
	{"Question", Question},
	{"Thinking", Thinking},
	{"Edit", Edit},
	{"File", File},
	{"Shell", Shell},
	{"Search", Search},
	{"Agent", Agent},
	{"Web", Web},
	{"Brand", Brand},
	{"CustomAgent", CustomAgent},
	{"DisclosureCollapsed", DisclosureCollapsed},
	{"DisclosureExpanded", DisclosureExpanded},
	{"Job", Job},
	{"Watch", Watch},
	{"Copy", Copy},
}

// Every glyph in the vocabulary must be exactly one cell wide, and no two
// glyphs may share a shape.
//
// This is not cosmetic: the transcript lays out a " X " gutter and measures
// the remaining budget for the body, so a double-width rune in the gutter
// pushes every body line one cell right. 🌐 (double-width) was retired for
// exactly this.
//
// SCOPE — read this before trusting the test. It pins width and uniqueness,
// which are mechanical. It does NOT pin font coverage. ⌕ was retired because
// macOS font fallback draws it from a CJK font, and ⌕ measures one cell by
// every width table available, so no assertion here can catch that class of
// bug (verified: ansi.StringWidth reports 1 for ⌕ and 1 for every candidate
// in Miscellaneous Technical). Choosing a glyph from a block with broad
// coverage remains human judgement. What this test does guarantee is that the
// judgement is made explicitly, and that a chosen glyph cannot silently be
// two cells or collide with an existing meaning.
func TestGlyphsAreSingleCellAndDistinct(t *testing.T) {
	seen := make(map[string]string, len(allGlyphs))
	for _, g := range allGlyphs {
		if g.glyph == "" {
			t.Errorf("glyph %s is empty", g.name)
			continue
		}
		if w := ansi.StringWidth(g.glyph); w != 1 {
			t.Errorf("glyph %s = %q has width %d, want 1", g.name, g.glyph, w)
		}
		if prev, dup := seen[g.glyph]; dup {
			t.Errorf("glyph %s = %q duplicates %s: one shape must mean one thing", g.name, g.glyph, prev)
		}
		seen[g.glyph] = g.name
	}
}

// TestCopyGlyphIsDistinctFromEveryOther pins the specific collision that made
// Copy non-obvious: ⧉ (Two Joined Squares) is the conventional "duplicate"
// mark, but it is already Agent, and a copy chip sharing a shape with the
// agent marker would read as an agent affordance.
func TestCopyGlyphIsDistinctFromEveryOther(t *testing.T) {
	for _, g := range allGlyphs {
		if g.name == "Copy" {
			continue
		}
		if Copy == g.glyph {
			t.Fatalf("Copy glyph %q collides with %s", Copy, g.name)
		}
	}
}
