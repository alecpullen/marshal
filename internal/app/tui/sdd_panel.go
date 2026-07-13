package tui

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
)

const sddPanelRows = 10

// renderSDDPanel renders the SDD progress panel. Returns empty string when
// the SDD run is not active.
func renderSDDPanel(p session.SDDProgress, spinnerFrame string, width int) string {
	if !p.Active {
		return ""
	}
	inner := max(width-2, 1)

	var b strings.Builder
	b.WriteString(promptPrefixStyle.Render(truncateRunes("SDD: "+p.PlanName, inner)))
	for i, task := range p.Tasks {
		if i >= sddPanelRows-2 {
			break
		}
		b.WriteString("\n")
		implGlyph := sddPhaseGlyph(task.Implementer, spinnerFrame)
		revGlyph := sddPhaseGlyph(task.Reviewer, spinnerFrame)
		line := fmt.Sprintf("%s  %s %s  %s %s",
			task.Name, implGlyph, "impl", revGlyph, "rev")
		if task.FixRound > 0 {
			line += fmt.Sprintf("  ⟳ fix %d/%d", task.FixRound, task.MaxFixes)
		}
		if task.Detail != "" {
			line += "  " + task.Detail
		}
		b.WriteString(truncateRunes(line, inner))
	}
	// Branch review row.
	b.WriteString("\n")
	brGlyph := sddPhaseGlyph(p.BranchReview, spinnerFrame)
	b.WriteString(truncateRunes(fmt.Sprintf("Branch review: %s", brGlyph), inner))

	if p.TokensMax > 0 || p.TokensUsed > 0 {
		b.WriteString("\n")
		line := fmt.Sprintf("Tokens: %d / %d", p.TokensUsed, p.TokensMax)
		b.WriteString(mutedStyle.Render(truncateRunes(line, inner)))
	}

	return indentBlock(b.String(), "  ")
}
