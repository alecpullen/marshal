package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
)

// firstRow returns the first rendered row of the guttered input with
// styling stripped and trailing padding removed.
func firstRow(s string) string {
	return strings.TrimRight(stripANSI(strings.Split(s, "\n")[0]), " ")
}

func TestCursorAtEndOfInputUsesCurrentLineLength(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.input.SetValue("first line\nlast")
	m.input.CursorEnd()

	if !m.cursorAtEndOfInput() {
		t.Fatal("cursor at the end of the final line should accept a suggestion")
	}
}

func TestGutteredInputRestoresPlaceholder(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.suggestion = "yes"
	m.suggestionDismissed = false
	m.input.Placeholder = "TEMP"

	_ = m.gutteredInput()

	// The function captures the current placeholder at entry, blanks it
	// while a suggestion ghost overlays the input, and restores it on exit.
	// With the value receiver this defer ran on a copy, so the caller's
	// placeholder was left permanently "" — the bug this guards against.
	if m.input.Placeholder != "TEMP" {
		t.Errorf("placeholder should be restored to its entry value after gutteredInput: got %q, want %q", m.input.Placeholder, "TEMP")
	}
}

// TestGhostSitsAtCursorColumn is the direct regression guard for the
// flicker and the left-to-right jump. The ghost used to be located by
// searching the frame for the cursor's reverse-video escape, which
// bubbles only emits on blink-on frames; a blurred textarea never emits
// it, so it is the deterministic stand-in for a blink-off frame. Both
// cases must put the ghost in the same place, immediately after the
// typed text with no spurious space.
func TestGhostSitsAtCursorColumn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		focused bool
	}{
		{"focused", true},
		{"blurred", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newViewTestModel(t, 80, 24)
			m.suggestion = "yes, go ahead"
			m.suggestionDismissed = false
			m.input.SetValue("yes")
			m.input.CursorEnd()
			if tc.focused {
				m.input.Focus()
			} else {
				m.input.Blur()
			}

			want := glyph.Rail + "❯ yes, go ahead"
			if got := firstRow(m.gutteredInput()); got != want {
				t.Fatalf("input row = %q, want %q", got, want)
			}
		})
	}
}

// TestGhostPreservesRowWidth guards the frame invariant: overlaying a
// ghost must not widen the input row. The old code inserted the ghost,
// pushing the row past the input width.
func TestGhostPreservesRowWidth(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.input.SetValue("yes")
	m.input.CursorEnd()
	before := ansi.StringWidth(strings.Split(m.gutteredInput(), "\n")[0])

	m.suggestion = "yes, go ahead"
	m.suggestionDismissed = false
	after := ansi.StringWidth(strings.Split(m.gutteredInput(), "\n")[0])

	if before != after {
		t.Fatalf("row width with ghost = %d, want %d (unchanged)", after, before)
	}
}

// TestGhostHiddenWhenCursorNotAtEnd guards the fish-style rule that the
// ghost only appears when the cursor is at the end of the input. The ghost
// suffix is computed from the full typed value while ghostPosition returns
// the cursor's column; the two agree only at end-of-input, so drawing
// anywhere else would overwrite typed characters.
func TestGhostHiddenWhenCursorNotAtEnd(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.suggestion = "yes, go ahead"
	m.suggestionDismissed = false
	m.input.SetValue("yes")
	m.input.CursorStart() // cursor at the beginning, not at end-of-input

	if got := m.suggestionGhost(); got != "" {
		t.Fatalf("suggestionGhost = %q, want empty when cursor is not at end of input", got)
	}
	if got := stripANSI(m.gutteredInput()); strings.Contains(got, "go ahead") {
		t.Fatalf("expected no ghost when cursor is not at end of input, got %q", got)
	}
}

func TestGhostPosition(t *testing.T) {
	t.Run("empty_input_is_row_zero_at_prompt_width", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		row, col, ok := m.ghostPosition()
		if !ok {
			t.Fatal("ghostPosition should resolve for an empty input")
		}
		if row != 0 || col != inputPromptWidth {
			t.Fatalf("ghostPosition = (%d, %d), want (0, %d)", row, col, inputPromptWidth)
		}
	})

	t.Run("follows_cursor_onto_wrapped_row", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		// Words, not one long run: textarea's wrap() only breaks at
		// spaces, so an unbroken word never wraps.
		m.input.SetValue(strings.Repeat("word ", m.input.Width()/5+2))
		m.input.CursorEnd()

		row, col, ok := m.ghostPosition()
		if !ok {
			t.Fatal("ghostPosition should resolve for a wrapped single-line value")
		}
		if row == 0 {
			t.Fatal("ghostPosition row = 0, want the cursor's wrapped row")
		}
		if want := inputPromptWidth + m.input.LineInfo().CharOffset; col != want {
			t.Fatalf("ghostPosition col = %d, want %d", col, want)
		}
	})

	t.Run("multi_line_value_has_no_position", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.input.SetValue("one\ntwo")
		m.input.CursorEnd()

		if _, _, ok := m.ghostPosition(); ok {
			t.Fatal("ghostPosition should not resolve when the cursor is off the first logical line")
		}
	})
}

// TestOverlayGhostOutOfRangeRowIsNoop pins the "no fallback position"
// rule: a ghost in the wrong place is worse than no ghost, and the silent
// fallback to row 0 is what produced the jump.
func TestOverlayGhostOutOfRangeRowIsNoop(t *testing.T) {
	view := "❯ yes\n  more"
	for _, row := range []int{-1, 2, 99} {
		if got := overlayGhost(view, row, 2, "GHOST"); got != view {
			t.Fatalf("overlayGhost(row=%d) = %q, want the view unchanged", row, got)
		}
	}
}

// TestOverlayGhostOutOfRangeColumnIsNoop pins the column guard: a ghost
// drawn at or past the end of the line has nothing to overwrite, so the
// view must be returned unchanged rather than widened.
func TestOverlayGhostOutOfRangeColumnIsNoop(t *testing.T) {
	view := "❯ yes\n  more"
	width := ansi.StringWidth("❯ yes") // 6
	for _, col := range []int{-1, width, 999} {
		if got := overlayGhost(view, 0, col, "GHOST"); got != view {
			t.Fatalf("overlayGhost(col=%d) = %q, want the view unchanged", col, got)
		}
	}
}

func TestGutteredInputSuggestion(t *testing.T) {
	t.Run("empty_input_shows_full_suggestion", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.suggestion = "yes"
		m.suggestionDismissed = false
		want := glyph.Rail + "❯ yes"
		if got := firstRow(m.gutteredInput()); got != want {
			t.Fatalf("input row = %q, want %q", got, want)
		}
		if strings.Contains(stripANSI(m.gutteredInput()), "Ask Marshal") {
			t.Fatal("placeholder should be suppressed when a suggestion is active")
		}
	})
	t.Run("prefix_shows_suffix", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.suggestion = "yes"
		m.suggestionDismissed = false
		m.input.SetValue("ye")
		m.input.CursorEnd()
		// Fish-style: the typed "ye" plus the ghosted suffix "s" — the
		// row reads "yes" exactly once, not "yeyes".
		want := glyph.Rail + "❯ yes"
		if got := firstRow(m.gutteredInput()); got != want {
			t.Fatalf("input row = %q, want %q", got, want)
		}
	})
	t.Run("dismissed_hides_ghost", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.suggestion = "yes"
		m.suggestionDismissed = true
		if got := stripANSI(m.gutteredInput()); strings.Contains(got, "yes") {
			t.Fatalf("expected no ghost when dismissed, got %q", got)
		}
	})
	t.Run("busy_hides_ghost", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.suggestion = "yes"
		m.suggestionDismissed = false
		m.busy = true
		if got := stripANSI(m.gutteredInput()); strings.Contains(got, "yes") {
			t.Fatalf("expected no ghost while busy, got %q", got)
		}
	})
	t.Run("non_prefix_input_hides_ghost", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.suggestion = "yes"
		m.suggestionDismissed = false
		m.input.SetValue("no")
		m.input.CursorEnd()
		if got := stripANSI(m.gutteredInput()); strings.Contains(got, "yes") {
			t.Fatalf("expected no ghost when input doesn't prefix suggestion, got %q", got)
		}
	})
	t.Run("multi_line_suggestion_hides_ghost", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		// A ghost is spliced into one rendered row; a newline would add a
		// row and break the input box's rectangle.
		m.suggestion = "yes\nand also"
		m.suggestionDismissed = false
		if got := m.suggestionGhost(); got != "" {
			t.Fatalf("suggestionGhost = %q, want empty for a multi-line suggestion", got)
		}
	})
}
