package settings

import (
	"fmt"
	"strings"
)

func (m Model) View() string {
	var b strings.Builder
	b.WriteString("┌── Settings ───────────────────────────────────────────┐\n")
	for _, f := range m.fields {
		line := f.View(50)
		b.WriteString(fmt.Sprintf("│ %s\n", line))
	}
	b.WriteString("├───────────────────────────────────────────────────────┤\n")
	if m.footer != "" {
		b.WriteString(fmt.Sprintf("│ %s\n", m.footer))
	}
	b.WriteString("│ [Ctrl+S] Save  [Esc] Cancel  [Tab] Next field         │\n")
	b.WriteString("└───────────────────────────────────────────────────────┘\n")
	return b.String()
}
