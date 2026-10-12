package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/viewmodel"
)

// compactDuration is "42s" or "6m40s": the form the turn receipt and the task
// panel use, where formatElapsed's "6m 40s" costs a column.
func compactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// renderReceipt is the turn's closing line:
// "✓ done · 6m40s · 4 tasks · 11 steps · 19 tools · ±3 files · 212k tok".
func renderReceipt(r *viewmodel.ReceiptInfo, width int) string {
	th := theme.Current()
	g, gc, word := glyph.OK, th.StatusSuccess, "done"
	if r.Salvaged {
		g, gc, word = glyph.Warning, th.StatusWarning, "salvaged"
	}
	parts := []string{word}
	if r.Duration > 0 {
		parts = append(parts, compactDuration(r.Duration))
	}
	if r.Tasks > 0 {
		parts = append(parts, pluralCount(r.Tasks, "task", "tasks"))
	}
	parts = append(parts, pluralCount(r.Steps, "step", "steps"), pluralCount(r.Tools, "tool", "tools"))
	if r.Files > 0 {
		parts = append(parts, fmt.Sprintf("±%d files", r.Files))
		if r.Files == 1 {
			parts[len(parts)-1] = "±1 file"
		}
	}
	if r.Usage != "" {
		parts = append(parts, r.Usage)
	}
	text := strings.Join(parts, " · ")
	text = ansi.Truncate(text, max(width-gutterWidth, 1), "…")
	return gutterPrefix(g, gc) + mutedStyle().Render(text) + "\n"
}

// pluralCount is "1 step" / "3 steps".
func pluralCount(n int, one, many string) string {
	return fmt.Sprintf("%d %s", n, plural(n, one, many))
}
