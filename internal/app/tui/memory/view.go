package memory

import (
	"fmt"
	"strings"
)

func (m Model) View() string {
	frameWidth := 61
	if m.width > 0 {
		frameWidth = min(61, m.width-4)
	}
	if frameWidth < 30 {
		frameWidth = 30
	}
	inner := frameWidth - 4

	visible := m.visibleCount()
	if visible < 1 {
		visible = 1
	}
	end := m.offset + visible
	if end > len(m.memories) {
		end = len(m.memories)
	}

	var b strings.Builder
	b.WriteString(frameTitle("Project Memories", frameWidth))
	if len(m.memories) == 0 {
		b.WriteString(frameLine("No memories yet.", inner))
	}
	for i := m.offset; i < end; i++ {
		mem := m.memories[i]
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		line := fmt.Sprintf("%s[%s] (%s) %s", cursor, mem.Kind, mem.Confidence, mem.Content)
		b.WriteString(frameLine(line, inner))
	}
	b.WriteString(frameSeparator(inner))
	if m.footer != "" {
		b.WriteString(frameLine(m.footer, inner))
	}
	b.WriteString(frameLine("[↑/k ↓/j] Move  [c] Confirm  [s] Mark Stale  [Esc] Close", inner))
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
