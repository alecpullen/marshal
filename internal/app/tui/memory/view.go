package memory

import (
	"fmt"
	"strings"
)

const frameInnerWidth = 61

func (m Model) View() string {
	var b strings.Builder
	b.WriteString(frameTitle("Project Memories"))
	if len(m.memories) == 0 {
		b.WriteString(frameLine("No memories yet."))
	}
	for i, mem := range m.memories {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		line := fmt.Sprintf("%s[%s] (%s) %s", cursor, mem.Kind, mem.Confidence, mem.Content)
		b.WriteString(frameLine(line))
	}
	b.WriteString(frameSeparator())
	if m.footer != "" {
		b.WriteString(frameLine(m.footer))
	}
	b.WriteString(frameLine("[↑/k ↓/j] Move  [c] Confirm  [s] Mark Stale  [Esc] Close"))
	b.WriteString(frameBottom())
	return b.String()
}

func frameTitle(title string) string {
	trimmed := truncateRunes(title, frameInnerWidth-1)
	padding := frameInnerWidth - 1 - runeLen(trimmed)
	if padding < 0 {
		padding = 0
	}
	return "┌─ " + trimmed + " " + strings.Repeat("─", padding) + "┐\n"
}

func frameSeparator() string {
	return "├" + strings.Repeat("─", frameInnerWidth+2) + "┤\n"
}

func frameBottom() string {
	return "└" + strings.Repeat("─", frameInnerWidth+2) + "┘\n"
}

func frameLine(content string) string {
	trimmed := truncateRunes(content, frameInnerWidth)
	return "│ " + trimmed + strings.Repeat(" ", frameInnerWidth-runeLen(trimmed)) + " │\n"
}

func truncateRunes(s string, limit int) string {
	if runeLen(s) <= limit {
		return s
	}

	var b strings.Builder
	count := 0
	for _, r := range s {
		if count == limit {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}

func runeLen(s string) int {
	return len([]rune(s))
}
