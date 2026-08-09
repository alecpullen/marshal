// Package chrome provides shared TUI dressing: gutter-framed panels with
// embedded titles, focus-aware gutter colors, and line windowing.
// Extracted from the settings TUI so pickers and docked panels render consistently.
package chrome

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

func isMonochrome(th theme.Theme) bool {
	_, ok := th.FGDefault.(lipgloss.NoColor)
	return ok
}

// Panel draws a gutter-framed panel: a ▍ column down the left with the
// title as a bold header line. The gutter uses accent.secondary when
// focused, fg.muted when not. Output is at most h rows and is not
// padded to h. In monochrome mode the ▍ glyph alone marks the panel.
func Panel(title, content string, w, h int, focused bool, th theme.Theme) string {
	return PanelWithHints(title, "", content, w, h, focused, th)
}

// PanelWithHints is Panel with dim key hints right-aligned on the
// header line (e.g. "↵ edit · Esc back"). Hints are dropped when the
// header has no room for them.
func PanelWithHints(title, hints, content string, w, h int, focused bool, th theme.Theme) string {
	mono := isMonochrome(th)
	gutterColor := th.FGMuted
	titleStyle := lipgloss.NewStyle().Foreground(th.FGMuted)
	if focused {
		gutterColor = th.AccentSecondary
		if mono {
			titleStyle = lipgloss.NewStyle().Foreground(th.AccentSecondary)
		} else {
			titleStyle = lipgloss.NewStyle().Bold(true).Foreground(th.AccentSecondary)
		}
	}
	gutter := lipgloss.NewStyle().Foreground(gutterColor).Render(glyph.Rail) + " "
	inner := w - 2
	if inner < 1 {
		inner = 1
	}

	out := make([]string, 0, h)
	if title != "" {
		label := ansi.Truncate(title, inner, "…")
		head := titleStyle.Render(label)
		if hints != "" {
			gap := inner - ansi.StringWidth(label) - ansi.StringWidth(hints)
			if gap >= 2 {
				head += strings.Repeat(" ", gap) + lipgloss.NewStyle().Foreground(th.FGMuted).Render(hints)
			}
		}
		out = append(out, gutter+head)
	}
	budget := h - len(out)
	if budget < 0 {
		budget = 0
	}
	lines := strings.Split(content, "\n")
	if len(lines) > budget {
		lines = lines[:budget]
	}
	for _, l := range lines {
		out = append(out, gutter+ansi.Truncate(l, inner, "…"))
	}
	return strings.Join(out, "\n")
}

// ClipLines windows lines to at most height rows, keeping focusLine
// visible, with ↑/↓ more indicators occupying the first/last row when
// clipped.
func ClipLines(lines []string, focusLine, height int, th theme.Theme) string {
	if height <= 0 || len(lines) <= height {
		return strings.Join(lines, "\n")
	}
	more := lipgloss.NewStyle().Foreground(th.FGMuted)
	inner := height - 2
	if inner < 1 {
		inner = 1
	}
	start := focusLine - inner/2
	if start < 0 {
		start = 0
	}
	if start+inner > len(lines) {
		start = len(lines) - inner
	}
	out := make([]string, 0, height)
	if start > 0 {
		out = append(out, more.Render("  ↑ more"))
	} else {
		// No top indicator needed; reclaim the row for content.
		inner++
	}
	out = append(out, lines[start:start+inner]...)
	if start+inner < len(lines) {
		out = append(out, more.Render("  ↓ more"))
	}
	if len(out) > height {
		out = out[:height]
	}
	return strings.Join(out, "\n")
}
