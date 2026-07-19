package tui

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
)

const swarmPanelRows = 9

func statusGlyph(status session.SwarmRoleStatus, spinnerFrame string) string {
	switch status {
	case session.SwarmRoleDone:
		return "✔"
	case session.SwarmRoleActive:
		return spinnerFrame
	case session.SwarmRoleFailed:
		return "✘"
	default:
		return "○"
	}
}

// sddPhaseGlyph returns the status symbol for an SDD phase. Mirrors
// statusGlyph but for SDDPhase values.
func sddPhaseGlyph(phase session.SDDPhase, spinnerFrame string) string {
	switch phase {
	case session.SDDPhaseDone, session.SDDPhaseSkipped:
		return "✔"
	case session.SDDPhaseActive:
		return spinnerFrame
	case session.SDDPhaseFailed:
		return "✘"
	default:
		return "○"
	}
}

func renderSwarmPanel(p session.SwarmProgress, spinnerFrame string, width int) string {
	if !p.Active {
		return ""
	}
	inner := max(width-2, 1)

	var b strings.Builder
	b.WriteString(promptPrefixStyle().Render(truncateRunes("Swarm: "+p.Goal, inner)))
	for _, r := range p.Roles {
		b.WriteString("\n")
		line := fmt.Sprintf("%s %s", statusGlyph(r.Status, spinnerFrame), r.Name)
		if r.Detail != "" {
			line += "   " + r.Detail
		}
		b.WriteString(truncateRunes(line, inner))
	}

	if p.TokensMax > 0 || p.TokensUsed > 0 {
		b.WriteString("\n")
		line := fmt.Sprintf("Tokens: %d / %d", p.TokensUsed, p.TokensMax)
		b.WriteString(mutedStyle().Render(truncateRunes(line, inner)))
	}

	return indentBlock(b.String(), "  ")
}
