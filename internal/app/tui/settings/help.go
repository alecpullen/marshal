package settings

import (
	"strings"

	"charm.land/lipgloss/v2"
)

func (m Model) helpOverlay(fw, fh int) string {
	lines := []string{
		"Settings keys",
		"",
		"  j/k or ↑/↓     move",
		"  g / G          first / last",
		"  Enter          open · edit · drill in",
		"  Space          toggle on/off",
		"  ←/→            cycle enum values",
		"  a / d          add / delete entry",
		"  y / p          yank / paste (duplicate)",
		"  Shift+↑/↓      reorder entry",
		"  h / Shift+Tab  back to sidebar",
		"  Esc            up one level · discard edit",
		"  /              search all settings",
		"  Ctrl+S         review changes, then save",
		"  ?              close this help",
	}
	if m.sidebarHidden {
		lines = append(lines, "", "  h / l          previous / next section")
	}
	panel := renderPanel("Help", strings.Join(lines, "\n"), max(fw/2, 44), min(fh, len(lines)+4), true)
	return lipgloss.NewStyle().Width(fw).Height(fh).Align(lipgloss.Center, lipgloss.Center).Render(panel)
}
