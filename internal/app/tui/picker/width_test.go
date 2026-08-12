package picker

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func widestLine(s string) int {
	w := 0
	for _, line := range strings.Split(s, "\n") {
		if n := ansi.StringWidth(line); n > w {
			w = n
		}
	}
	return w
}

func probeModel() *Model {
	return New("Models", "↑↓ select · ↵ accept", []Item{
		{Label: "anthropic/claude-opus-5-20260115-preview",
			Detail: "200k ctx · $15/$75", Badge: "●now"},
		{Label: "openai/gpt-5-turbo-2026-04-01", Detail: "128k ctx · $10/$30"},
		{Label: "ollama/qwen3-coder:30b", Detail: "local"},
	})
}

// TestPickerNeverOverflows covers widths below minTerminalWidth too: panels
// render into docks and rails narrower than the terminal itself.
func TestPickerNeverOverflows(t *testing.T) {
	m := probeModel()
	for _, w := range []int{44, 60, 80, 100, 120, 200} {
		if got := widestLine(m.View(w, 20)); got > w {
			t.Errorf("View(%d): widest line is %d cells", w, got)
		}
	}
}

// TestPickerKeepsCurrentBadge pins F12: at 44 columns the picker previously
// truncated away the ●now badge, so it stopped answering the question it
// exists to answer — which model is active.
func TestPickerKeepsCurrentBadge(t *testing.T) {
	m := probeModel()
	for _, w := range []int{44, 60, 80, 120} {
		if !strings.Contains(m.View(w, 20), "●now") {
			t.Errorf("View(%d) dropped the ●now badge:\n%s", w, m.View(w, 20))
		}
	}
}
