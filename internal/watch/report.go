package watch

import (
	"fmt"
	"strings"

	"marshal/internal/strutil"
)

// Format renders a fired watch Report as the human/model-facing text that
// the runner drains into the model wire. It is pure presentation: the
// manager's OnFire closure in app.go calls it and pushes the formatted
// text to the session's watch-report queue. Persistence happens later, at
// drain time (and at turn-end residual handling) in the runner, not here.
//
// The shape is:
//
//	[watch <name> fired] kind=command interval=5s
//	condition: exit_code 0
//	last sample (tail): ...
//
// plus suffixes per the Report fields: " (auto-removed)", " (fired N
// times)", and " (from subagent <owner>)".
func Format(r Report) string {
	var b []byte
	b = append(b, fmt.Sprintf("[watch %s fired] kind=%s", r.Name, r.Kind)...)
	if r.Interval > 0 {
		b = append(b, fmt.Sprintf(" interval=%s", r.Interval)...)
	}
	if r.Condition != "" {
		b = append(b, "\ncondition: "+r.Condition...)
	}
	if r.Sample != "" {
		b = append(b, "\nlast sample (tail): "+r.Sample...)
	}
	if r.AutoRemoved {
		b = append(b, " (auto-removed)"...)
	}
	if r.FiredCount > 1 {
		b = append(b, fmt.Sprintf(" (fired %d times)", r.FiredCount)...)
	}
	if r.Owner != "" {
		b = append(b, " (from subagent "+r.Owner+")"...)
	}
	return string(b)
}

// ResumeGoal builds the wrapper goal for a watch auto-resume turn: the
// text that starts the TUI auto-run or the ACP server-initiated turn
// when a resume watch fires on an idle session. Shared by both wake
// sites so the runtimes render identical text. lastAssistant is the
// assistant's last pre-idle statement; empty omits the quote sentence
// (the report alone orients the model). repeat adds the stop hint so a
// repeat watch does not wake the session forever.
func ResumeGoal(name, lastAssistant string, repeat bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Watch %q fired while the session was idle — auto-resuming as you requested (resume=true).", name)
	if quote := strings.TrimSpace(lastAssistant); quote != "" {
		fmt.Fprintf(&b, " You last said: \"%s\".", strutil.Truncate(quote, 400, true))
	}
	b.WriteString(" The watch report is delivered in this turn; pick up where you left off.")
	if repeat {
		b.WriteString(" If you are done with this watch, stop it with watch.stop.")
	}
	return b.String()
}
