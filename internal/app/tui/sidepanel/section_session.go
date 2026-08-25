package sidepanel

import (
	"fmt"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/strutil"
)

// SessionSection is the rail's pinned footer: turn count, elapsed time,
// and cumulative token usage. It has no title — the rail introduces it
// with a bare rule so it reads as a footer rather than a fifth section.
type SessionSection struct{}

func (SessionSection) ID() string      { return "session" }
func (SessionSection) Title() string   { return "" }
func (SessionSection) Priority() int   { return 9 }
func (SessionSection) Clippable() bool { return false }

// Relevant reports whether the footer should render. It needs only a session
// — not a completed turn — so the footer is present from the first frame
// rather than popping in after turn 1.
func (SessionSection) Relevant(d Data) bool { return d.State != nil }

// sessionElapsed is session wall-clock: now minus the session's start.
// Deliberately not the age of the oldest turn row (which was wrong whenever
// rows aged out of the window, and undefined at turn 0) and not the sum of
// turn durations (which is active-work time, a different and also useful
// number). Named sessionElapsed to avoid colliding with the swarm section's
// elapsed(start, now) helper.
func sessionElapsed(d Data) time.Duration {
	if d.State == nil {
		return 0
	}
	return d.Now.Sub(d.State.StartedAt)
}

// shortDuration renders a duration compactly: "42s", "1m12s", "2h04m".
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// budgetLabel renders the tool-budget row: "tools 7 · no cap" when Max is 0
// (unlimited), "tools 7/100" when a cap is set. Empty when no tools have run.
func budgetLabel(d Data) string {
	if d.State == nil {
		return ""
	}
	b := d.State.ToolBudget()
	if b.Used <= 0 {
		return ""
	}
	if b.Max > 0 {
		return fmt.Sprintf(" tools %d/%d", b.Used, b.Max)
	}
	return fmt.Sprintf(" tools %d · no cap", b.Used)
}

func (SessionSection) Render(d Data, width, maxRows int) []string {
	rows := []string{
		ansi.Truncate(fmt.Sprintf(" turn %d · %s", d.Totals.Turns, shortDuration(sessionElapsed(d))), width, "…"),
		ansi.Truncate(fmt.Sprintf(" %s in · %s out",
			strutil.CompactTokens(int(d.Totals.PromptTokens)),
			strutil.CompactTokens(int(d.Totals.CompletionTokens))), width, "…"),
	}
	if label := budgetLabel(d); label != "" {
		rows = append(rows, ansi.Truncate(label, width, "…"))
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return rows
}

func (SessionSection) OneLine(d Data, width int) string {
	line := fmt.Sprintf("turn %d · %s · %s/%s",
		d.Totals.Turns, shortDuration(sessionElapsed(d)),
		strutil.CompactTokens(int(d.Totals.PromptTokens)),
		strutil.CompactTokens(int(d.Totals.CompletionTokens)))
	if label := budgetLabel(d); label != "" {
		line += " ·" + label
	}
	return ansi.Truncate(line, width, "…")
}
