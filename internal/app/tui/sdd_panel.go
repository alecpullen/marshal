package tui

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
)

// sddPanel renders the persistent SDD progress panel: a header with the
// controller state + plan name, one row per task (phase + retry count),
// and a branch-review footer. Mirrors the swarm roster panel's layout.
func sddPanel(p session.SDDProgress, width int) string {
	if !p.Active {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "SDD · %s · %s\n", p.PlanName, strings.ToLower(p.ControllerState))
	fmt.Fprintf(&b, "tasks %d/%d", p.DoneTasks, p.TotalTasks)
	if p.TokensMax > 0 {
		fmt.Fprintf(&b, " · tokens %d/%d", p.TokensUsed, p.TokensMax)
	}
	b.WriteString("\n")
	for _, t := range p.Tasks {
		phase := string(t.Phase)
		if t.Phase == session.SDDPhaseActive {
			phase = activeSubPhase(t)
		}
		line := fmt.Sprintf("  %s  %s", t.Name, phase)
		if t.FixRound > 0 {
			line += fmt.Sprintf(" (retry %d/%d)", t.FixRound, t.MaxFixes)
		}
		b.WriteString(line + "\n")
	}
	if p.BranchReview != "" && p.BranchReview != session.SDDPhasePending {
		b.WriteString(fmt.Sprintf("  branch review: %s\n", p.BranchReview))
	}
	return b.String()
}

func activeSubPhase(t session.SDDTaskStatus) string {
	switch {
	case t.Implementer == session.SDDPhaseActive:
		return "implementer"
	case t.Audit == session.SDDPhaseActive:
		return "audit"
	case t.Review == session.SDDPhaseActive:
		return "review"
	default:
		return "active"
	}
}
