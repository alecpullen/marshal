package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

// laneSeparator is the rule that opens a lane, marking where the
// transcript ends. Without it the lanes blend into the todo panel and the
// input area directly beneath them.
//
// It reuses renderTurnSeparator's construction — the one `─` rule already
// sanctioned in a codebase that otherwise forbids box-drawing chrome — so
// the two horizontal rules on screen match.
func laneSeparator(width int) string {
	bar := lipgloss.NewStyle().Foreground(dimColor).Render(glyph.Rail)
	w := max(width-1, 1)
	return bar +
		lipgloss.NewStyle().Foreground(theme.Current().BorderMuted).Render(strings.Repeat("─", w)) +
		"\n"
}

// laneItem renders a count-first, pluralized caption part, matching the
// todo panel's "tasks %d/%d" convention: "1 job" / "3 jobs".
func laneItem(n int, singular, plural string) string {
	word := plural
	if n == 1 {
		word = singular
	}
	return fmt.Sprintf("%d %s", n, word)
}

// renderLane renders a lane's chrome: a separator rule row, then a
// count-first header row, then the pre-glyphed item rows. Each rows entry
// is one full display line, pre-glyphed by the caller. Returns "" when
// there are no rows.
//
// The header and rows are built at width-1 so chromeRailWidth prefixes the
// one-cell rail without ellipsizing the rule (the invariant documented at
// agentlane.go:67-69). A trailing newline is preserved so stacked regions
// keep their separation, matching renderJobLane's convention.
func renderLane(header string, rows []string, width int) string {
	if len(rows) == 0 {
		return ""
	}
	return laneSeparator(width) +
		chromeRailWidth(header+"\n"+strings.Join(rows, "\n")+"\n", dimColor, max(width-1, 1))
}

// paintLane paints a lane's rendered content as a full-width band, the
// identical tail both renderers apply today.
func paintLane(s string, leftWidth int) string {
	return chrome.PaintBand(s, leftWidth, theme.Current().ChromeBG())
}
