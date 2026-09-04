package watch

import "fmt"

// Format renders a fired watch Report as the human/model-facing text that
// the runner drains into the model wire. It is pure presentation: the
// manager's OnFire closure in app.go calls it and does the two-channel
// delivery (persist + queue).
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
