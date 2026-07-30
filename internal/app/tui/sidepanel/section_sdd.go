package sidepanel

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
)

// SDDSection is the spec-driven-development task list. The live
// strip shows only "sdd 3/7"; this is the list behind that count.
type SDDSection struct{}

func (SDDSection) ID() string      { return "sdd" }
func (SDDSection) Title() string   { return "SDD" }
func (SDDSection) Priority() int   { return 0 }
func (SDDSection) Clippable() bool { return true }

func (SDDSection) Relevant(d Data) bool { return d.SDD.Active }

func (SDDSection) Render(d Data, width, maxRows int) []string {
	rows := make([]string, 0, 2)
	rows = append(rows, fmt.Sprintf(" plan · %s", d.SDD.PlanName))
	if d.SDD.Branch != "" {
		rows = append(rows, fmt.Sprintf(" branch · %s", d.SDD.Branch))
	}
	rows = append(rows, fmt.Sprintf(" task %d/%d · %s", d.SDD.CurrentTask, d.SDD.TotalTasks, d.SDD.Phase))
	if d.SDD.FixRound > 0 {
		rows = append(rows, fmt.Sprintf(" fix %d/%d", d.SDD.FixRound, d.SDD.MaxFixRounds))
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
