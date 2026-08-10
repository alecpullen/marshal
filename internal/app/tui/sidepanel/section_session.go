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

func (SessionSection) Relevant(d Data) bool { return len(d.Turns) > 0 }

// sessionTotals sums token usage and measures elapsed time since the
// oldest recorded turn. d.Turns is most-recent-first.
func sessionTotals(d Data) (turns, in, out int, elapsed time.Duration) {
	turns = len(d.Turns)
	for _, t := range d.Turns {
		in += t.PromptTokens
		out += t.CompletionTokens
	}
	if turns > 0 {
		elapsed = d.Now.Sub(d.Turns[turns-1].StartedAt)
	}
	return turns, in, out, elapsed
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
	turns, in, out, elapsed := sessionTotals(d)
	rows := []string{
		ansi.Truncate(fmt.Sprintf(" turn %d · %s", turns, shortDuration(elapsed)), width, "…"),
		ansi.Truncate(fmt.Sprintf(" %s in · %s out",
			strutil.CompactTokens(in), strutil.CompactTokens(out)), width, "…"),
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
	turns, in, out, elapsed := sessionTotals(d)
	line := fmt.Sprintf("turn %d · %s · %s/%s",
		turns, shortDuration(elapsed),
		strutil.CompactTokens(in), strutil.CompactTokens(out))
	if label := budgetLabel(d); label != "" {
		line += " ·" + label
	}
	return ansi.Truncate(line, width, "…")
}
