package tui

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/strutil"
)

// sddPanel renders the plan-execution panel: the plan and branch, the
// current task and phase, the fix round when one is running, and the most
// recent ledger line.
func sddPanel(p session.SDDProgress, width int) string {
	if !p.Active {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "plan · %s", p.PlanName)
	if p.Branch != "" {
		fmt.Fprintf(&b, " · %s", p.Branch)
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "task %d/%d", p.CurrentTask, p.TotalTasks)
	if p.Phase != "" {
		fmt.Fprintf(&b, " · %s", p.Phase)
	}
	if p.FixRound > 0 {
		fmt.Fprintf(&b, " · fix %d/%d", p.FixRound, p.MaxFixRounds)
	}
	if p.TokensMax > 0 {
		fmt.Fprintf(&b, " · tokens %d/%d", p.TokensUsed, p.TokensMax)
	}
	b.WriteString("\n")

	if p.Detail != "" {
		fmt.Fprintf(&b, "  %s\n", strutil.Truncate(p.Detail, maxInt(width-4, 20), true))
	}
	if p.LastLedger != "" {
		fmt.Fprintf(&b, "  %s\n", strutil.Truncate(p.LastLedger, maxInt(width-4, 20), true))
	}
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
