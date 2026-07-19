package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"marshal/internal/app/session"
)

// renderSDDPanel renders the SDD progress panel. Returns the rendered body
// and its actual row count. Returns ("", 0) when the SDD run is not active.
func renderSDDPanel(p session.SDDProgress, spinnerFrame string, width int) (string, int) {
	if !p.Active {
		return "", 0
	}
	inner := max(width-2, 1)

	var b strings.Builder
	b.WriteString(promptPrefixStyle().Render(truncateRunes("SDD: "+p.PlanName, inner)))
	for i, task := range p.Tasks {
		if i >= 8 {
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
		b.WriteString(mutedStyle().Render(truncateRunes(line, inner)))
	}

	body := indentBlock(b.String(), "  ")
	return body, lipgloss.Height(body)
}
