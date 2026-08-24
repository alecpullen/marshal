package liveregion

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

var th256 = theme.LoadFor(false, "xterm-256color")
var th16 = theme.LoadFor(false, "xterm")
var thMono = theme.LoadFor(true, "xterm-256color")

func rowCount(s string) int { return strings.Count(s, "\n") }

func spec(body []string) Spec {
	return Spec{
		Glyph: "*", Title: "reviewer", Right: "2m14s",
		Meta: "opus-4.6 @ anthropic", Body: body, Footer: "ctrl+f to drill in",
		MaxRows: SubagentRows, Live: true, Width: 60,
	}
}

// THE load-bearing test. Row count must rise to the cap and then never
// change again, no matter how much more body arrives. This is the contract
// that stops the transcript sliding; a golden-output test would let it
// regress silently.
func TestRowCountGrowsToCapThenFreezes(t *testing.T) {
	var body []string
	var counts []int
	for i := 0; i < 40; i++ {
		body = append(body, fmt.Sprintf("line %d of streamed reasoning text", i))
		counts = append(counts, rowCount(Render(spec(body), th256)))
	}
	for i := 1; i < len(counts); i++ {
		if counts[i] < counts[i-1] {
			t.Fatalf("row count shrank at step %d: %v", i, counts)
		}
		if counts[i] > SubagentRows {
			t.Fatalf("row count %d exceeded cap %d at step %d", counts[i], SubagentRows, i)
		}
	}
	last := counts[len(counts)-1]
	if last != SubagentRows {
		t.Fatalf("never reached the cap: final %d, want %d", last, SubagentRows)
	}
	// Frozen: everything after the first time it hits the cap is identical.
	for i := range counts {
		if counts[i] == SubagentRows {
			for j := i; j < len(counts); j++ {
				if counts[j] != SubagentRows {
					t.Fatalf("height changed after reaching cap: %v", counts)
				}
			}
			break
		}
	}
}

// Wrapping must be counted in display rows, not logical lines. One very
// long logical line is the case today's renderers get wrong.
func TestLongSingleLineStillRespectsCap(t *testing.T) {
	long := strings.Repeat("wordy ", 200)
	if got := rowCount(Render(spec([]string{long}), th256)); got > SubagentRows {
		t.Fatalf("one long line produced %d rows, cap is %d", got, SubagentRows)
	}
}

func TestRowsMatchesRender(t *testing.T) {
	cases := map[string]Spec{
		"empty body":       {Glyph: "*", Title: "t", MaxRows: 4, Width: 40},
		"short body":       {Glyph: "*", Title: "t", Body: []string{"a"}, MaxRows: 4, Width: 40},
		"over cap":         spec([]string{"a", "b", "c", "d", "e", "f", "g"}),
		"no meta":          {Glyph: "*", Title: "t", Body: []string{"a", "b"}, Footer: "f", MaxRows: 4, Width: 40},
		"no footer":        {Glyph: "*", Title: "t", Meta: "m", Body: []string{"a", "b"}, MaxRows: 4, Width: 40},
		"cap below chrome": {Glyph: "*", Title: "t", Meta: "m", Footer: "f", MaxRows: 1, Width: 40},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if want, got := Rows(s), rowCount(Render(s, th256)); want != got {
				t.Fatalf("Rows()=%d but Render emitted %d rows", want, got)
			}
		})
	}
}

// The timer sits in a fixed column: a longer elapsed string must not change
// the row's visible width, or every segment behind it reflows each tick.
func TestHeaderWidthStableAcrossRightValues(t *testing.T) {
	widths := map[string]int{}
	for _, r := range []string{"9s", "59s", "2m14s", "1h02m"} {
		s := spec([]string{"body"})
		s.Right = r
		header := strings.Split(Render(s, th256), "\n")[0]
		widths[r] = ansi.StringWidth(header)
	}
	var first int
	for r, w := range widths {
		if first == 0 {
			first = w
			continue
		}
		if w != first {
			t.Fatalf("header width varies with Right: %v (%q gave %d, want %d)", widths, r, w, first)
		}
	}
}

func TestOffsetClampsAtBothEnds(t *testing.T) {
	body := make([]string, 30)
	for i := range body {
		body[i] = fmt.Sprintf("row-%02d", i)
	}
	maxOff := MaxOffset(spec(body))
	if maxOff <= 0 {
		t.Fatalf("expected a scrollable region, MaxOffset=%d", maxOff)
	}
	for _, off := range []int{-100, -1, 0, maxOff, maxOff + 1, 10000} {
		s := spec(body)
		s.Offset = off
		out := Render(s, th256) // must not panic
		if rowCount(out) != SubagentRows {
			t.Fatalf("offset %d changed the row count to %d", off, rowCount(out))
		}
	}
	// Offset 0 is tail-anchored: the last body row must be visible.
	s := spec(body)
	if !strings.Contains(Render(s, th256), "row-29") {
		t.Fatal("offset 0 must show the tail")
	}
	// Fully scrolled back shows the head.
	s.Offset = maxOff
	if !strings.Contains(Render(s, th256), "row-00") {
		t.Fatal("max offset must show the head")
	}
}

// Tint gating. 48;5; is the 256-colour background SGR introducer.
func hasBGSGR(s string) bool { return strings.Contains(s, "48;5;") || strings.Contains(s, "48;2;") }

func TestTintOnlyAtTier256(t *testing.T) {
	s := spec([]string{"body"})
	if !hasBGSGR(Render(s, th256)) {
		t.Error("a live region must be tinted at Tier256")
	}
	if hasBGSGR(Render(s, th16)) {
		t.Error("no tint at Tier16: BGSurface 8 behind FGMuted 7 deletes the text")
	}
	if hasBGSGR(Render(s, thMono)) {
		t.Error("no tint in monochrome")
	}
}

func TestFinishedRegionIsNotTinted(t *testing.T) {
	s := spec([]string{"body"})
	s.Live = false
	if hasBGSGR(Render(s, th256)) {
		t.Error("flat = history: a settled region must not be tinted")
	}
}

// Every emitted row must be exactly Width cells so the fill forms a clean
// block rather than a ragged one.
func TestPaintedRowsAreFullWidth(t *testing.T) {
	s := spec([]string{"short", strings.Repeat("long ", 40)})
	for i, line := range strings.Split(strings.TrimRight(Render(s, th256), "\n"), "\n") {
		if got := ansi.StringWidth(line); got != s.Width {
			t.Errorf("row %d width = %d, want %d", i, got, s.Width)
		}
	}
}

func TestZeroWidthDoesNotPanic(t *testing.T) {
	for _, w := range []int{-5, 0, 1, 2, 3, 4} {
		s := spec([]string{"body"})
		s.Width = w
		_ = Render(s, th256)
		_ = Rows(s)
		_ = MaxOffset(s)
	}
}

// The rail must run the region's full height so the block reads as one
// unit. Only the header is exempt — its gutter carries the status glyph,
// which is the top of the rail.
func TestRailRunsFullHeight(t *testing.T) {
	s := spec([]string{"body one", "body two"})
	rows := strings.Split(strings.TrimRight(Render(s, th256), "\n"), "\n")
	if len(rows) < 4 {
		t.Fatalf("want header+meta+body+footer, got %d rows", len(rows))
	}
	for i, row := range rows[1:] {
		if !strings.Contains(ansi.Strip(row), glyph.Rail) {
			t.Errorf("row %d has no rail: %q", i+1, ansi.Strip(row))
		}
	}
}

// THE regression test for the height bug. A body that shrinks must not
// shrink the region once MinRows records how tall it has been.
func TestMinRowsHoldsHeightWhenBodyShrinks(t *testing.T) {
	grown := spec([]string{"a", "b", "c", "d", "e", "f"})
	tall := Rows(grown)
	if tall != SubagentRows {
		t.Fatalf("precondition: want the region at its cap (%d), got %d", SubagentRows, tall)
	}
	// The body collapses to one line — what happens when SubagentActivityTail
	// flips from streamed reasoning to audit summaries.
	shrunk := spec([]string{"a"})
	if got := Rows(shrunk); got >= tall {
		t.Fatalf("precondition: a shrunk body should be shorter without MinRows, got %d", got)
	}
	shrunk.MinRows = tall
	if got := Rows(shrunk); got != tall {
		t.Fatalf("Rows with MinRows=%d = %d, want %d", tall, got, tall)
	}
	if got := strings.Count(Render(shrunk, th256), "\n"); got != tall {
		t.Fatalf("Render emitted %d rows, want %d", got, tall)
	}
}

func TestMinRowsNeverExceedsMaxRows(t *testing.T) {
	s := spec([]string{"a"})
	s.MinRows = 99
	if got := Rows(s); got > s.MaxRows {
		t.Fatalf("MinRows must not push past MaxRows: got %d, cap %d", got, s.MaxRows)
	}
}

func TestMinRowsBelowNaturalIsIgnored(t *testing.T) {
	s := spec([]string{"a", "b", "c", "d", "e", "f"})
	natural := Rows(s)
	s.MinRows = 1
	if got := Rows(s); got != natural {
		t.Fatalf("a MinRows below the natural height must be ignored: %d != %d", got, natural)
	}
}

// Padding rows still carry the rail, or the block develops a gap.
func TestPaddingRowsCarryTheRail(t *testing.T) {
	s := spec([]string{"only one line"})
	s.MinRows = SubagentRows
	rows := strings.Split(strings.TrimRight(Render(s, th256), "\n"), "\n")
	for i, row := range rows[1:] {
		if !strings.Contains(ansi.Strip(row), glyph.Rail) {
			t.Errorf("padded row %d has no rail: %q", i+1, ansi.Strip(row))
		}
	}
}

// Padding must not disturb the full-width invariant the tint depends on.
func TestPaddedRowsAreStillFullWidth(t *testing.T) {
	s := spec([]string{"one"})
	s.MinRows = SubagentRows
	for i, line := range strings.Split(strings.TrimRight(Render(s, th256), "\n"), "\n") {
		if got := ansi.StringWidth(line); got != s.Width {
			t.Errorf("row %d width = %d, want %d", i, got, s.Width)
		}
	}
}

func TestFooterStaysLastWhenPadded(t *testing.T) {
	s := spec([]string{"one"})
	s.MinRows = SubagentRows
	rows := strings.Split(strings.TrimRight(Render(s, th256), "\n"), "\n")
	last := ansi.Strip(rows[len(rows)-1])
	if !strings.Contains(last, "ctrl+f") {
		t.Fatalf("footer must remain the last row, got %q", last)
	}
}
