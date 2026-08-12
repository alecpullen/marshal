package tui

import (
	"strings"
	"testing"
)

func TestGutteredInputSuggestion(t *testing.T) {
	t.Run("empty_input_shows_full_suggestion", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.suggestion = "yes"
		m.suggestionDismissed = false
		view := m.gutteredInput()
		if !strings.Contains(view, "yes") {
			t.Fatalf("expected ghost 'yes' in guttered input, got %q", view)
		}
		if strings.Contains(view, "Ask Marshal") {
			t.Fatalf("placeholder should be suppressed when a suggestion is active, got %q", view)
		}
	})
	t.Run("prefix_shows_suffix", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.suggestion = "yes"
		m.suggestionDismissed = false
		m.input.SetValue("ye")
		m.input.CursorEnd()
		view := m.gutteredInput()
		// Fish-style: only the untyped suffix "s" is shown after the cursor.
		if !strings.Contains(view, "s") {
			t.Fatalf("expected ghost suffix 's' (typed 'ye' + suffix 's') in guttered input, got %q", view)
		}
		if strings.Contains(view, "yes") {
			t.Fatalf("expected only the suffix, not the full suggestion, got %q", view)
		}
	})
	t.Run("dismissed_hides_ghost", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.suggestion = "yes"
		m.suggestionDismissed = true
		view := m.gutteredInput()
		if strings.Contains(view, "yes") {
			t.Fatalf("expected no ghost when dismissed, got %q", view)
		}
	})
	t.Run("busy_hides_ghost", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.suggestion = "yes"
		m.suggestionDismissed = false
		m.busy = true
		view := m.gutteredInput()
		if strings.Contains(view, "yes") {
			t.Fatalf("expected no ghost while busy, got %q", view)
		}
	})
	t.Run("non_prefix_input_hides_ghost", func(t *testing.T) {
		m := newViewTestModel(t, 80, 24)
		m.suggestion = "yes"
		m.suggestionDismissed = false
		m.input.SetValue("no")
		m.input.CursorEnd()
		view := m.gutteredInput()
		if strings.Contains(view, "yes") {
			t.Fatalf("expected no ghost when input doesn't prefix suggestion, got %q", view)
		}
	})
}
