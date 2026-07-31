package sidepanel

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
)

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
		rows = append(rows, fmt.Sprintf(" %s %d %s", tg, i+1, title))
	}
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
