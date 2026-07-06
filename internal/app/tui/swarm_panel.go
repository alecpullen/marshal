package tui

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
)

const swarmPanelRows = 8

func statusGlyph(status session.SwarmRoleStatus, spinnerFrame string) string {
	switch status {
	case session.SwarmRoleDone:
		return "✓"
	case session.SwarmRoleActive:
		return spinnerFrame
	case session.SwarmRoleFailed:
		return "✗"
	default:
		return "○"
	}
}

func renderSwarmPanel(p session.SwarmProgress, spinnerFrame string, width int) string {
	if !p.Active {
		return ""
	}
	inner := max(width-4, 1)

	var b strings.Builder
	b.WriteString(promptPrefixStyle.Render(truncateRunes("Swarm: "+p.Goal, inner)))
	for i := 0; i < 5; i++ {
		b.WriteString("\n")
		if i >= len(p.Roles) {
			continue
		}
		r := p.Roles[i]
		line := fmt.Sprintf("%s %s", statusGlyph(r.Status, spinnerFrame), r.Name)
		if r.Detail != "" {
			line += "   " + r.Detail
		}
		b.WriteString(truncateRunes(line, inner))
	}

	return inputBoxStyle.Width(max(width-2, 1)).Render(b.String())
}
