package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/theme"
	"marshal/internal/strutil"
	"marshal/internal/tools/native"
)

func renderTodos(todos []native.TodoItem, width int) string {
	if len(todos) == 0 {
		return ""
	}
	var b strings.Builder
	for _, t := range todos {
		var glyph string
		var c color.Color
		switch t.Status {
		case native.TodoCompleted:
			glyph = "✓"
			c = theme.Current().StatusSuccess
		case native.TodoInProgress:
			glyph = "▶"
			c = theme.Current().AccentPrimary
		default:
			glyph = "·"
			c = theme.Current().FGMuted
		}
		gutter := gutterPrefix(glyph, c)
		labelStyle := lipgloss.NewStyle().Foreground(theme.Current().FGDefault)
		if t.Status == native.TodoCompleted {
			labelStyle = lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
		}
		if t.Status == native.TodoInProgress {
			labelStyle = lipgloss.NewStyle().Foreground(theme.Current().FGDefault).Bold(true)
		}
		line := labelStyle.Render(strutil.Truncate(t.Content, max(width-6, 1), false))
		b.WriteString(gutter)
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
