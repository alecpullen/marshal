package sidepanel

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

// formatElapsed formats a duration for the finished summary line, matching
// the run panel's format: "23m 41s" or "12s".
func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
}

// SDDSection mirrors the run panel when the side rail is open: the summary
// (task count, phase, detail) plus the plan checklist with the same status
// derivation. It is a mirror, not a replacement — the main column's panel
// stays.
type SDDSection struct{}

func (SDDSection) ID() string      { return "sdd" }
func (SDDSection) Title() string   { return "SDD" }
func (SDDSection) Priority() int   { return 0 }
func (SDDSection) Clippable() bool { return true }

func (SDDSection) Relevant(d Data) bool { return d.SDD.Active || d.SDD.Finished }

func (SDDSection) Render(d Data, width, maxRows int) []string {
	if d.SDD.Finished {
		elapsed := d.SDD.EndedAt.Sub(d.SDD.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		var g, verb string
		if d.SDD.Succeeded {
			g = glyph.OK
			verb = "sdd done"
		} else {
			g = glyph.Error
			verb = "sdd stopped"
		}
		base := fmt.Sprintf(" %s %s — %d/%d tasks · %s", g, verb, d.SDD.DoneTasks, d.SDD.TotalTasks, formatElapsed(elapsed))
		var reason, hint string
		if !d.SDD.Succeeded && d.SDD.Error != "" {
			reason = " · " + d.SDD.Error
		}
		// The rail is space-constrained: it points at the outcome panel for
		// both outcomes and leaves the resume instruction to the outcome doc.
		hint = " — /run for details"
		line := base + reason + hint
		if ansi.StringWidth(line) > width && hint != "" {
			line = base + reason
		}
		if ansi.StringWidth(line) > width && reason != "" {
			line = base
		}
		line = ansi.Truncate(line, width, "…")
		return []string{line}
	}
	g := d.Spinner
	if g == "" {
		g = glyph.Running
	}
	summary := fmt.Sprintf("task %d/%d", d.SDD.CurrentTask, d.SDD.TotalTasks)
	if d.SDD.Phase != "" {
		summary += " · " + d.SDD.Phase
	}
	rows := []string{fmt.Sprintf(" %s %s", g, summary)}
	if d.SDD.Detail != "" {
		rows = append(rows, "   "+d.SDD.Detail)
	}
	if d.SDD.FixRound > 0 {
		rows = append(rows, fmt.Sprintf("   fix %d/%d", d.SDD.FixRound, d.SDD.MaxFixRounds))
	}
	tasks := make([]string, 0, len(d.SDD.Tasks))
	for i, title := range d.SDD.Tasks {
		var tg string
		switch {
		case i < d.SDD.DoneTasks:
			tg = glyph.OK
		case i == d.SDD.CurrentTask-1:
			tg = glyph.Running
		default:
			tg = glyph.Ambient
		}
		tasks = append(tasks, fmt.Sprintf(" %s %d %s", tg, i+1, title))
	}
	// Window the checklist around the current task rather than truncating
	// from the top: on a long plan the active task is the one row that must
	// never be clipped away. This is the same policy (and the same helper)
	// the main run panel's checklist uses. The summary rows above are held
	// out of the window so they always survive.
	if budget := maxRows - len(rows); maxRows > 0 && budget > 0 && len(tasks) > budget {
		focus := min(max(d.SDD.CurrentTask-1, 0), len(tasks)-1)
		tasks = strings.Split(chrome.ClipLines(tasks, focus, budget, theme.Current()), "\n")
	}
	rows = append(rows, tasks...)
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], width, "…")
	}
	return rows
}

func (SDDSection) OneLine(d Data, width int) string {
	line := fmt.Sprintf(glyph.Running+" sdd %d/%d", d.SDD.DoneTasks, d.SDD.TotalTasks)
	if d.SDD.Phase != "" {
		line += " · " + d.SDD.Phase
	}
	return ansi.Truncate(line, width, "…")
}
