package tui

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
)

// sddGatePrompt renders the human-gate prompt the TUI shows when the
// controller returns ErrHumanGateRequired. The gate kind drives the
// wording; the user resolves by pressing y (resolve) or n (abort).
func sddGatePrompt(g session.SDDGate) string {
	var b strings.Builder
	b.WriteString("▸ SDD human gate\n")
	switch g.Kind {
	case "spec_approval":
		fmt.Fprintf(&b, "Spec ready for approval.\nReason: %s\n", g.Reason)
		b.WriteString("Press y to approve and continue, n to abort.")
	case "final_merge":
		fmt.Fprintf(&b, "Final merge ready.\nReason: %s\n", g.Reason)
		b.WriteString("Press y to merge to target, n to abort.")
	case "escalation":
		fmt.Fprintf(&b, "Escalation on task %s.\nReason: %s\n", g.TaskID, g.Reason)
		b.WriteString("Press y to apply the recommended fix, n to abort.")
	case "branch_correction":
		fmt.Fprintf(&b, "Branch correction needed.\nReason: %s\n", g.Reason)
		b.WriteString("Press y to rebranch, n to abort.")
	default:
		fmt.Fprintf(&b, "Unknown gate kind %q.\nReason: %s\n", g.Kind, g.Reason)
		b.WriteString("Press y to resolve, n to abort.")
	}
	return b.String()
}
