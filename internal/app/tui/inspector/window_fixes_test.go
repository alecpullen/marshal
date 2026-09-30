package inspector

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestWrapToWidthMeasuresCellsNotRunes is the regression for the review's
// finding that wrapToWidth counted runes.
//
// A rune is not a column. The old implementation used len([]rune(s)), so ten CJK
// ideographs were treated as ten cells and emitted as twenty — a row twice its
// budget, which at this width the terminal wraps, pushing the panel's chrome off
// the bottom of the screen. Measuring in the wrong unit made the function fail at
// its one job for any non-ASCII text.
func TestWrapToWidthMeasuresCellsNotRunes(t *testing.T) {
	// Every non-ASCII character below is 2 cells wide, so a correct
	// implementation fits exactly half as many per row as the rune count would
	// suggest.
	cases := map[string]string{
		"cjk":             strings.Repeat("漢", 20),
		"cjk with spaces": strings.Repeat("漢字 ", 10),
		"emoji":           strings.Repeat("🎉", 10),
		"mixed":           "abc 漢字 def 漢字 ghi",
		"ascii long":      strings.Repeat("a-very-long-unbroken-token ", 6),
		"ascii short":     "a short line",
		"empty":           "",
	}
	for name, s := range cases {
		for _, width := range []int{1, 2, 3, 10, 20, 30, 80} {
			rows := wrapToWidth(s, width)
			if len(rows) == 0 {
				t.Errorf("%s at width %d produced no rows", name, width)
				continue
			}
			for i, row := range rows {
				got := ansi.StringWidth(row)
				if got <= width {
					continue
				}
				// A row may exceed the budget in exactly ONE case: a single
				// grapheme that is itself wider than the whole row. There is no
				// way to render half an ideograph, so the only honest options are
				// "emit it whole" and "drop it", and dropping content is worse.
				// The row must then be that one cluster and nothing else.
				if len([]rune(row)) != 1 {
					t.Errorf("%s at width %d: row %d is %d cells wide: %q",
						name, width, i, got, row)
				}
			}
			// Nothing may be LOST. Wrapping is allowed to move whitespace; it is
			// never allowed to drop content, because a header silently missing
			// words is worse than one that wraps.
			strip := func(v string) string {
				return strings.Join(strings.Fields(v), "")
			}
			joined := strip(strings.Join(rows, " "))
			if want := strip(s); joined != want {
				t.Errorf("%s at width %d: reassembled to %q, want %q", name, width, joined, want)
			}
		}
	}
}

// TestWrapToWidthNeverSplitsAGrapheme pins that a wide cluster stays whole: half
// a grapheme is invalid text, not merely ugly output.
func TestWrapToWidthNeverSplitsAGrapheme(t *testing.T) {
	rows := wrapToWidth(strings.Repeat("漢", 10), 5)
	for _, row := range rows {
		for _, r := range row {
			if r != '漢' {
				t.Fatalf("row %q contains %q, which is not a whole ideograph", row, r)
			}
		}
		if ansi.StringWidth(row)%2 != 0 {
			t.Fatalf("row %q broke an ideograph in half (odd cell count)", row)
		}
	}
}

// renderedRows counts the LINES a render emitted.
//
// It is not strings.Count(s, "\n"): View does not emit a trailing newline, so
// counting newlines under-reports by one and would let an off-by-one overflow
// pass. An empty render is zero rows.
func renderedRows(s string) int {
	if s == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSuffix(s, "\n"), "\n"))
}

// TestWindowListKeepsTheCursorVisible is the core contract of the shared window:
// whatever the scroll and the cursor, the cursor row is inside the window.
//
// A window that loses the cursor makes the down-key read as broken — the reader
// presses it, the cursor moves, and nothing on screen changes — which is the
// failure this helper exists to prevent.
func TestWindowListKeepsTheCursorVisible(t *testing.T) {
	const total = 100
	for _, avail := range []int{1, 2, 5, 20, 99, 100, 200} {
		for _, cursor := range []int{0, 1, 49, 98, 99} {
			for _, scroll := range []int{0, 10, 50, 99, 1000} {
				w := windowList(total, avail, cursor, scroll, 2)
				if w.Rows() > avail {
					t.Errorf("avail=%d cursor=%d scroll=%d: window covers %d rows, over budget",
						avail, cursor, scroll, w.Rows())
				}
				if w.Rows() == 0 {
					t.Errorf("avail=%d cursor=%d scroll=%d: empty window for a non-empty list",
						avail, cursor, scroll)
				}
				if cursor < w.Start || cursor >= w.End {
					t.Errorf("avail=%d cursor=%d scroll=%d: window is [%d,%d) — the cursor is outside it",
						avail, cursor, scroll, w.Start, w.End)
				}
				if w.Start < 0 || w.End > total {
					t.Errorf("avail=%d cursor=%d scroll=%d: window [%d,%d) is outside the list",
						avail, cursor, scroll, w.Start, w.End)
				}
			}
		}
	}
}

// TestWindowListReservesItsNoteRows pins the budget arithmetic: what the caller
// is told it may spend on rows PLUS the notes must fit the budget. Counting the
// notes afterwards is how a windowed panel ends up one row taller than its
// frame, which is the whole defect.
func TestWindowListReservesItsNoteRows(t *testing.T) {
	const total, avail = 100, 10
	for _, cursor := range []int{0, 5, 50, 99} {
		w := windowList(total, avail, cursor, 0, 2)
		notes := 0
		if w.Above() > 0 {
			notes++
		}
		if w.Below() > 0 {
			notes++
		}
		if got := w.Rows() + notes; got > avail {
			t.Errorf("cursor=%d: %d list rows + %d note rows = %d, over the %d budget",
				cursor, w.Rows(), notes, got, avail)
		}
	}
}

// TestWindowListGivesTheWholeBudgetToAShortList pins that a list that fits is not
// needlessly windowed: no note rows are reserved when nothing is hidden.
func TestWindowListGivesTheWholeBudgetToAShortList(t *testing.T) {
	w := windowList(3, 10, 1, 0, 2)
	if w.Rows() != 3 {
		t.Fatalf("a 3-row list in a 10-row budget covered %d rows, want all 3", w.Rows())
	}
	if w.Above() != 0 || w.Below() != 0 {
		t.Errorf("nothing is hidden, yet above=%d below=%d", w.Above(), w.Below())
	}
}

// TestWindowListDegenerateInputs covers the cases that must not panic and must
// not claim content: an empty list, and a panel with no room.
func TestWindowListDegenerateInputs(t *testing.T) {
	if w := windowList(0, 10, 0, 0, 2); w.Rows() != 0 || w.Above() != 0 || w.Below() != 0 {
		t.Errorf("an empty list produced %+v", w)
	}
	if w := windowList(50, 0, 3, 0, 2); w.Rows() != 0 {
		t.Errorf("a zero-row budget produced %d rows", w.Rows())
	}
	// A cursor past the end must be clamped rather than producing a window
	// outside the list.
	w := windowList(10, 5, 999, 0, 2)
	if w.Start < 0 || w.End > 10 || w.Rows() == 0 {
		t.Errorf("an out-of-range cursor produced %+v", w)
	}
	// A negative cursor likewise.
	w = windowList(10, 5, -5, 0, 2)
	if w.Start < 0 || w.End > 10 || w.Rows() == 0 {
		t.Errorf("a negative cursor produced %+v", w)
	}
}

// TestWindowNotesDiscloseWhatIsHidden pins the disclosure rule: a windowed list
// must SAY it is windowed. A reader who cannot see that more rows exist will
// conclude there are none, and a changed-files list that silently stops is
// indistinguishable from a clean tree.
func TestWindowNotesDiscloseWhatIsHidden(t *testing.T) {
	if got := aboveNote(0, 40); got != "" {
		t.Errorf("aboveNote(0) = %q, want nothing hidden", got)
	}
	if got := belowNote(0, 40); got != "" {
		t.Errorf("belowNote(0) = %q, want nothing hidden", got)
	}
	if got := aboveNote(1, 40); got == "" || !strings.Contains(got, "1") {
		t.Errorf("aboveNote(1) = %q, want a row count", got)
	}
	if got := belowNote(42, 40); got == "" || !strings.Contains(got, "42") {
		t.Errorf("belowNote(42) = %q, want the hidden count", got)
	}
	// A note must respect the panel width, or it reflows and costs the rows the
	// window just saved.
	long := belowNote(99999, 8)
	if len([]rune(long)) > 8 {
		t.Errorf("belowNote at width 8 = %q (%d runes), want it clamped", long, len([]rune(long)))
	}
}

// TestDetailViewRespectsItsHeight is the regression for the review's finding
// that Resize(40, 5) emitted 8 rows.
//
// The label and both footer notes were emitted ON TOP of a viewport that was
// already the full height, so a detail body could always overshoot its frame by
// up to three rows. Every height must produce at most that many rows.
func TestDetailViewRespectsItsHeight(t *testing.T) {
	contents := map[string]struct {
		text      string
		truncated bool
	}{
		"long":           {strings.Repeat("a diff line\n", 200), false},
		"long truncated": {strings.Repeat("a diff line\n", 200), true},
		"one line":       {"only one line", false},
		"exactly fit":    {"a\nb\nc", false},
		"empty":          {"", false},
	}
	for name, tc := range contents {
		for height := 0; height <= 12; height++ {
			d := NewDetailView()
			d.SetContent(tc.text, tc.truncated)
			d.Resize(40, height)
			got := renderedRows(d.View("a label"))
			if height > 0 && got > height {
				t.Errorf("%s at height %d emitted %d rows:\n%s", name, height, got, d.View("a label"))
			}
		}
	}
}

// TestDetailViewUnmeasuredRendersEverything pins the other half: before a
// resize the view has no budget, and rendering a window off a height nobody
// measured would replace the body with an ellipsis run.
func TestDetailViewUnmeasuredRendersEverything(t *testing.T) {
	d := NewDetailView()
	text := strings.Repeat("line\n", 40)
	d.SetContent(text, false)
	if got := renderedRows(d.View("label")); got < 40 {
		t.Fatalf("an unmeasured view emitted %d rows for 40 lines of content", got)
	}
}

// TestDetailViewBodyHeightMatchesWhatItEmits pins BodyHeight against reality: a
// caller budgeting a shared column from a number the view does not honour is
// exactly how the panel ends up taller than its frame.
func TestDetailViewBodyHeightMatchesWhatItEmits(t *testing.T) {
	cases := []struct {
		text      string
		truncated bool
		height    int
	}{
		{strings.Repeat("x\n", 100), false, 10},
		{strings.Repeat("x\n", 100), true, 10},
		{strings.Repeat("x\n", 100), false, 3},
		{"short", false, 10},
		{"", false, 10},
		{"a\nb", true, 4},
	}
	for _, tc := range cases {
		d := NewDetailView()
		d.SetContent(tc.text, tc.truncated)
		d.Resize(40, tc.height)
		d.SetLabel("label")
		predicted := d.BodyHeight()
		actual := renderedRows(d.View("label"))
		if predicted != actual {
			t.Errorf("BodyHeight predicted %d rows but View emitted %d (text=%q truncated=%v height=%d)",
				predicted, actual, tc.text, tc.truncated, tc.height)
		}
	}
}

// TestDetailViewFollowAnchorsToTheEndAtRenderTime pins that "following" means
// the last line is on screen, not that an offset recorded at some earlier moment
// still happened to be the end.
//
// The failure it prevents: content arrives, the view is following, and the reader
// sees stale lines with a "scroll for more" note under them — the opposite of
// following.
func TestDetailViewFollowAnchorsToTheEndAtRenderTime(t *testing.T) {
	d := NewDetailView()
	d.SetContent(strings.Repeat("line\n", 100), false)
	d.Resize(40, 10)

	// A resize AFTER the content landed changes the window, and a following view
	// must still end at the last line.
	d.Resize(40, 20)
	view := d.View("label")
	if strings.Contains(view, "scroll for more") {
		t.Fatalf("a following view reported more to scroll to:\n%s", view)
	}
	if !strings.Contains(view, "line") {
		t.Fatalf("a following view showed no content:\n%s", view)
	}
}
