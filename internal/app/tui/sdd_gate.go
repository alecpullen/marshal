package tui

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
)

// sddGatePrompt renders the question a pipeline subagent raised. The user
// answers by typing; esc abandons the run.
func sddGatePrompt(g session.SDDGate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "▸ Task %d needs an answer\n", g.TaskN)
	fmt.Fprintf(&b, "%s\n", g.Question)
	b.WriteString("Type your answer and press enter, or press esc to stop the run.")
	return b.String()
}
