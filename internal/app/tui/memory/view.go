package memory

import (
	"fmt"
	"strings"
)

func (m Model) View() string {
	var b strings.Builder
	b.WriteString("┌── Project Memories ─────────────────────────────────────────────┐\n")
	if len(m.memories) == 0 {
		b.WriteString("│ No memories yet.                                                 │\n")
	}
	for i, mem := range m.memories {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		line := fmt.Sprintf("%s[%s] (%s) %s", cursor, mem.Kind, mem.Confidence, mem.Content)
		b.WriteString(fmt.Sprintf("│ %s\n", line))
	}
	b.WriteString("├───────────────────────────────────────────────────────────────┤\n")
	if m.footer != "" {
		b.WriteString(fmt.Sprintf("│ %s\n", m.footer))
	}
	b.WriteString("│ [↑/k ↓/j] Move  [c] Confirm  [s] Mark Stale  [Esc] Close         │\n")
	b.WriteString("└───────────────────────────────────────────────────────────────┘\n")
	return b.String()
}
