// Package chrome provides shared TUI dressing: gutter-framed panels with
// embedded titles, focus-aware gutter colors, and line windowing.
// Extracted from the settings TUI so pickers and docked panels render consistently.
package chrome

import (
	"strings"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/theme"
)

func isMonochrome(th theme.Theme) bool {
	return theme.IsMonochromeTheme(th)
}

// Panel draws a framed panel: the title in the top border, the content inside
// "│ … │", and no bottom border — a panel is an open frame that continues into
// whatever the dock draws under it. The border uses accent.secondary when
// focused, fg.muted when not. Output is at most h rows tall and is not padded
// to h: the title row is the top border.
//
// w is the panel's content width, as layout.PanelWidth leaves it. The frame's
// two border columns take the 2-cell margin that width reserves, so the frame
// spans the dock exactly and the content keeps its w-2 cells.
func Panel(title, content string, w, h int, focused bool, th theme.Theme) string {
	return PanelWithHints(title, "", content, w, h, focused, th)
}

// PanelWithHints is Panel with dim key hints right-aligned in the top border
// (e.g. "↵ edit · Esc back"). Hints are dropped when the border has no room
// for them.
func PanelWithHints(title, hints, content string, w, h int, focused bool, th theme.Theme) string {
	return Frame{Title: title, Hints: hints, Focused: focused}.Render(content, w+2, h, th)
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
