package settings

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// renderPanel draws a rounded-border box with the title embedded in the top
// border. The border uses accent.primary when focused, border.muted when
// not — the primary focus signal of the settings UI.
func renderPanel(title, content string, w, h int, focused bool) string {
	borderColor := settingsTheme.BorderMuted
	titleStyle := lipgloss.NewStyle().Foreground(settingsTheme.FGMuted)
	if focused {
		borderColor = settingsTheme.AccentPrimary
		titleStyle = lipgloss.NewStyle().Bold(true).Foreground(settingsTheme.AccentPrimary)
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
