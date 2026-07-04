package settings

import (
	"strings"
)

func (m Model) View() string {
	frameWidth := 57
	if m.width > 0 {
		frameWidth = min(60, m.width-4)
	}
	if frameWidth < 30 {
		frameWidth = 30
	}
	inner := frameWidth - 4 // "│ " and " │"

	var b strings.Builder
	b.WriteString(frameTitle("Settings", frameWidth))
	for i, f := range m.fields {
		focused := i == m.focused
		line := f.View(inner)
		if focused {
			line = "> " + line
		} else {
			line = "  " + line
		}
		b.WriteString(frameLine(line, inner))
	}
	b.WriteString(frameSeparator(inner))
	if m.footer != "" {
		b.WriteString(frameLine(m.footer, inner))
	}
	b.WriteString(frameLine("[Ctrl+S] Save  [Esc] Cancel  [Tab/↑/↓] Navigate", inner))
	b.WriteString(frameBottom(frameWidth))
	return b.String()
}

func frameTitle(title string, w int) string {
	inner := w - 4
	t := truncateRunes(title, inner-1)
	pad := inner - 1 - len([]rune(t))
	if pad < 0 {
		pad = 0
	}
	return "┌─ " + t + " " + strings.Repeat("─", pad) + "┐\n"
}

func frameSeparator(inner int) string {
	return "├" + strings.Repeat("─", inner+2) + "┤\n"
}

func frameBottom(w int) string {
	return "└" + strings.Repeat("─", w-2) + "┘\n"
}

func frameLine(content string, inner int) string {
	t := truncateRunes(content, inner)
	pad := inner - len([]rune(t))
	if pad < 0 {
		pad = 0
	}
	return "│ " + t + strings.Repeat(" ", pad) + " │\n"
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
