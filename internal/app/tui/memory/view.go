package memory

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

var memTheme = theme.Load()

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

	var lines []string
	lines = append(lines, "Project Memories")

	if len(m.memories) == 0 {
		lines = append(lines, "No memories yet.")
	}
	for i := m.offset; i < end; i++ {
		mem := m.memories[i]
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		line := fmt.Sprintf("%s[%s] (%s) %s", cursor, mem.Kind, mem.Confidence, mem.Content)
		lines = append(lines, line)
	}
	// Separator line drawn inside the border.
	lines = append(lines, strings.Repeat("─", inner))

	if m.footer != "" {
		lines = append(lines, m.footer)
	}
	lines = append(lines, "[↑/k ↓/j] Move  [c] Confirm  [s] Mark Stale  [Esc] Close")

	// Truncate and pad each line to inner display width.
	contentStyle := lipgloss.NewStyle().Width(inner).Foreground(memTheme.FGDefault)
	for i, l := range lines {
		if lipgloss.Width(l) > inner {
			l = ansi.Cut(l, 0, inner)
		}
		lines[i] = contentStyle.Render(l)
	}

	innerContent := strings.Join(lines, "\n")

	// Wrap the entire content in a rounded border.
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(memTheme.FGMuted).
		Padding(0, 1)

	return style.Render(innerContent)
}
