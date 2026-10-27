package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// reviewIndicatorMark identifies the generated indicator row so tests can
// separate it from content. It is fixed text that never contains user input.
const reviewIndicatorMark = "PgUp/PgDn"

func isReviewIndicator(row string) bool {
	return strings.Contains(row, reviewIndicatorMark)
}

// TestPromptReviewReachesEveryRow pages a long, blank-line-containing input
// from the start until the review stops advancing, and asserts that every
// unique wrapped segment became visible. A blank logical line must survive as
// content, a wrapped long line must keep every suffix, and no window may
// exceed its height.
func TestPromptReviewReachesEveryRow(t *testing.T) {
	var lines []string
	for i := 0; i < 6; i++ {
		lines = append(lines, fmt.Sprintf("segment-%02d", i))
		lines = append(lines, "") // blank logical line → one empty physical row
	}
	long := strings.Repeat("wrap/", 40)
	lines = append(lines, long) // wraps, never truncates

	const (
		width  = 20
		height = 5
	)
	rows := promptReviewRows(lines, width)
	if len(rows) <= height {
		t.Fatalf("fixture must page: %d rows do not exceed height %d", len(rows), height)
	}
	// Nothing may be truncated: the flattened rows still carry the long line.
	if !strings.Contains(strings.Join(rows, ""), long) {
		t.Fatalf("wrapping lost part of the long line: %q", rows)
	}

	var (
		r    promptReview
		got  []string
		seen = make(map[string]bool, len(rows))
	)
	for {
		win := r.Window(rows, height)
		if len(win) > height {
			t.Fatalf("window returned %d rows for height %d: %q", len(win), height, win)
		}
		for _, row := range win {
			if isReviewIndicator(row) {
				continue
			}
			got = append(got, row)
			seen[row] = true
		}
		before := r.offset
		r.Page(rows, height, 1)
		if r.offset == before {
			break // clamped at the end of the rows
		}
	}
	for _, row := range rows {
		if !seen[row] {
			t.Errorf("wrapped row never became visible: %q", row)
		}
	}
	if len(got) == 0 || got[0] != rows[0] {
		t.Fatalf("first visible row = %q, want %q", got, rows[0])
	}
	if got[len(got)-1] != rows[len(rows)-1] {
		t.Errorf("last visible row = %q, want %q", got[len(got)-1], rows[len(rows)-1])
	}

	// Blank rows are content: at least one must have survived and been shown.
	blank := false
	for _, row := range rows {
		if row == "" {
			blank = true
		}
	}
	if !blank {
		t.Fatalf("fixture produced no blank rows: %q", rows)
	}
}

// TestPromptReviewResizeAndBounds covers reflow between widths, the one-row
// and oversized windows, both ends of the range, and zero content.
func TestPromptReviewResizeAndBounds(t *testing.T) {
	lines := []string{"alpha beta gamma", "delta", "", "epsilon zeta eta theta"}

	t.Run("reflow and clamp", func(t *testing.T) {
		narrow := promptReviewRows(lines, 8)
		wide := promptReviewRows(lines, 40)
		if len(narrow) <= len(wide) {
			t.Fatalf("narrow wrapping should produce more rows: narrow=%d wide=%d", len(narrow), len(wide))
		}
		if len(wide) <= 2 {
			t.Fatalf("fixture must page at height 2: got %d wide rows", len(wide))
		}
		// An offset valid at the narrow width must clamp when the body reflows
		// to fewer rows at the wide width.
		r := promptReview{offset: len(narrow) - 1}
		if got := r.Window(narrow, 2); len(got) > 2 {
			t.Fatalf("narrow window returned %d rows for height 2", len(got))
		}
		win := r.Window(wide, 2)
		if len(win) > 2 {
			t.Fatalf("wide window returned %d rows for height 2", len(win))
		}
		wantMax := len(wide) - 1 // one of the two rows is reserved for the indicator
		if r.offset != wantMax {
			t.Errorf("offset after reflow = %d, want clamped to %d", r.offset, wantMax)
		}
	})

	t.Run("one row shows content", func(t *testing.T) {
		rows := promptReviewRows(lines, 8)
		var r promptReview
		win := r.Window(rows, 1)
		if len(win) != 1 {
			t.Fatalf("one-row window returned %d rows, want 1", len(win))
		}
		if isReviewIndicator(win[0]) {
			t.Errorf("one-row window shows the indicator, want content: %q", win[0])
		}
		if win[0] != rows[0] {
			t.Errorf("one-row window shows %q, want first row %q", win[0], rows[0])
		}
		// A far offset clamps to the last row, still without an indicator.
		r.offset = len(rows) + 50
		win = r.Window(rows, 1)
		if len(win) != 1 || isReviewIndicator(win[0]) {
			t.Fatalf("one-row window at the end = %q, want the last content row", win)
		}
		if win[0] != rows[len(rows)-1] {
			t.Errorf("one-row window at the end shows %q, want %q", win[0], rows[len(rows)-1])
		}
	})

	t.Run("oversized height", func(t *testing.T) {
		rows := promptReviewRows(lines, 8)
		var r promptReview
		for _, height := range []int{len(rows), len(rows) + 25} {
			win := r.Window(rows, height)
			if len(win) != len(rows) {
				t.Fatalf("height %d returned %d rows, want all %d", height, len(win), len(rows))
			}
			for _, row := range win {
				if isReviewIndicator(row) {
					t.Errorf("height %d should need no indicator, got %q", height, row)
				}
			}
		}
	})

	t.Run("both ends", func(t *testing.T) {
		rows := promptReviewRows(lines, 8)
		var r promptReview
		for r.offset < len(rows)-1 {
			before := r.offset
			r.Page(rows, 4, 1)
			if r.offset == before {
				break
			}
		}
		last := func(win []string) string {
			for i := len(win) - 1; i >= 0; i-- {
				if !isReviewIndicator(win[i]) {
					return win[i]
				}
			}
			return ""
		}
		win := r.Window(rows, 4)
		if len(win) > 4 {
			t.Fatalf("bottom window returned %d rows for height 4", len(win))
		}
		if got := last(win); got != rows[len(rows)-1] {
			t.Errorf("bottom window shows last row %q, want %q", got, rows[len(rows)-1])
		}
		for r.offset > 0 {
			r.Page(rows, 4, -1)
		}
		win = r.Window(rows, 4)
		if win[0] != rows[0] {
			t.Errorf("top window does not show the first row: %q", win)
		}
	})

	t.Run("zero content", func(t *testing.T) {
		var r promptReview
		if win := r.Window(nil, 5); len(win) != 0 {
			t.Errorf("zero-content window returned %q, want nothing", win)
		}
		r.Page(nil, 5, 1)
		r.Page(nil, 5, -1)
		if r.offset != 0 {
			t.Errorf("paging zero content moved offset to %d, want 0", r.offset)
		}
		if win := r.Window(nil, 0); win != nil {
			t.Errorf("zero-height window returned %q, want nil", win)
		}
		if win := r.Window(nil, -3); win != nil {
			t.Errorf("negative-height window returned %q, want nil", win)
		}
	})
}

// TestPromptReviewUnicodeAndNoColor checks that wrapping accounts for Unicode
// cell width (wide characters and combining marks) and that the indicator
// stays plain, readable text with colour disabled.
func TestPromptReviewUnicodeAndNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	const (
		width   = 8
		content = "日本語のテキストです"
	)
	rows := promptReviewRows([]string{content}, width)
	if len(rows) < 3 {
		t.Fatalf("wide characters should wrap into several rows at %d cells, got %d: %q", width, len(rows), rows)
	}
	for _, row := range rows {
		if w := ansi.StringWidth(row); w > width {
			t.Errorf("row is %d cells, exceeds width %d: %q", w, width, row)
		}
	}
	// The rows still recover the original text because nothing was truncated
	// or padded mid-run.
	if joined := strings.Join(rows, ""); joined != content {
		t.Errorf("wrapped rows lost content: joined %q, want %q", joined, content)
	}

	// Combining marks are one grapheme, so they must not be split or counted
	// as extra cells.
	combining := "e\u0301cole caf\u0065\u0301"
	for _, row := range promptReviewRows([]string{combining}, 4) {
		if w := ansi.StringWidth(row); w > 4 {
			t.Errorf("combining-mark row is %d cells, exceeds width 4: %q", w, row)
		}
	}

	// The indicator must be plain text: no escape sequences, no user content.
	var r promptReview
	win := r.Window(rows, 2)
	ind := win[len(win)-1]
	if !isReviewIndicator(ind) {
		t.Fatalf("window with omitted rows has no indicator: %q", win)
	}
	if ansi.Strip(ind) != ind {
		t.Errorf("indicator contains escape sequences: %q", ind)
	}
	if !strings.Contains(ind, "rows ") || !strings.Contains(ind, " of ") {
		t.Errorf("indicator is not readable plain text: %q", ind)
	}
	if strings.Contains(ind, content) {
		t.Errorf("indicator leaks user content: %q", ind)
	}
}
