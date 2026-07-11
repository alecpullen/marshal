// Package chrome provides shared TUI dressing: bordered panels with
// embedded titles, focus-aware border colors, line windowing, and overlay
// compositing. Extracted from the settings TUI so pickers and overlays
// render consistently.
package chrome

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

// Panel draws a rounded-border box with the title embedded in the top
// border. The border uses accent.primary when focused, border.muted when
// not.
func Panel(title, content string, w, h int, focused bool, th theme.Theme) string {
	borderColor := th.BorderMuted
	titleStyle := lipgloss.NewStyle().Foreground(th.FGMuted)
	if focused {
		borderColor = th.AccentPrimary
		titleStyle = lipgloss.NewStyle().Bold(true).Foreground(th.AccentPrimary)
	}
	bs := lipgloss.NewStyle().Foreground(borderColor)
	inner := w - 2
	innerH := h - 2
	if inner < 1 {
		inner = 1
	}
	if innerH < 0 {
		innerH = 0
	}

	// Top border with embedded title: ╭─ Title ────╮
	label := " " + title + " "
	fill := inner - 1 - ansi.StringWidth(label)
	if fill < 0 {
		label = ansi.Truncate(label, inner-1, "…")
		fill = inner - 1 - ansi.StringWidth(label)
	}
	top := bs.Render("╭─") + titleStyle.Render(label) + bs.Render(strings.Repeat("─", max(fill, 0))+"╮")

	lines := strings.Split(content, "\n")
	body := make([]string, 0, innerH)
	for i := 0; i < innerH; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		l = ansi.Truncate(l, inner, "…")
		pad := inner - ansi.StringWidth(l)
		if pad < 0 {
			pad = 0
		}
		body = append(body, bs.Render("│")+l+strings.Repeat(" ", pad)+bs.Render("│"))
	}
	bottom := bs.Render("╰" + strings.Repeat("─", inner) + "╯")
	return top + "\n" + strings.Join(body, "\n") + "\n" + bottom
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
		out = append(out, "")
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

// Overlay splices panel into bg, centered horizontally and vertically.
// Both strings are full rendered views and may contain SGR sequences;
// background lines are cut at column boundaries with ansi.Cut so styling
// survives on both sides of the panel.
func Overlay(bg, panel string, width, height int) string {
	bgLines := strings.Split(bg, "\n")
	pLines := strings.Split(panel, "\n")
	pw := 0
	for _, l := range pLines {
		if w := ansi.StringWidth(l); w > pw {
			pw = w
		}
	}
	if pw > width {
		pw = width
	}
	x := max((width-pw)/2, 0)
	y := max((height-len(pLines))/2, 0)

	n := max(len(bgLines), y+len(pLines))
	out := make([]string, n)
	for i := 0; i < n; i++ {
		line := ""
		if i < len(bgLines) {
			line = bgLines[i]
		}
		pi := i - y
		if pi >= 0 && pi < len(pLines) {
			p := pLines[pi]
			if pad := pw - ansi.StringWidth(p); pad > 0 {
				p += strings.Repeat(" ", pad)
			}
			left := ansi.Cut(line, 0, x)
			if lw := ansi.StringWidth(left); lw < x {
				left += strings.Repeat(" ", x-lw)
			}
			right := ansi.Cut(line, x+pw, width)
			line = left + p + right
		}
		out[i] = line
	}
	return strings.Join(out, "\n")
}
