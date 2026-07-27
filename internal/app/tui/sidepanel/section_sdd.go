package sidepanel

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// SDDSection is the full spec-driven-development task list. The live
// strip shows only "sdd 3/7"; this is the list behind that count.
type SDDSection struct{}

func (SDDSection) ID() string      { return "sdd" }
func (SDDSection) Title() string   { return "SDD" }
func (SDDSection) Priority() int   { return 0 }
func (SDDSection) Clippable() bool { return true }

func (SDDSection) Relevant(d Data) bool { return d.SDD.Active }

// taskGlyph maps an SDD task phase to its one-rune cue.
func taskGlyph(phase string) string {
	switch strings.ToLower(phase) {
	case "done", "completed":
		return "✓"
	case "failed":
		return "✘"
	case "active", "in_progress":
		return "◐"
	default:
		return "·"
	}
}

func (SDDSection) Render(d Data, width, maxRows int) []string {
	rows := make([]string, 0, len(d.SDD.Tasks)+1)
	for _, task := range d.SDD.Tasks {
		rows = append(rows, fmt.Sprintf(" %s %s", taskGlyph(string(task.Phase)), task.Name))
	}
	if d.SDD.ControllerState != "" {
		rows = append(rows, " phase  "+strings.ToLower(d.SDD.ControllerState))
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
	line := fmt.Sprintf("▶ sdd %d/%d", d.SDD.DoneTasks, d.SDD.TotalTasks)
	if d.SDD.ControllerState != "" {
		line += " · " + strings.ToLower(d.SDD.ControllerState)
	}
	return ansi.Truncate(line, width, "…")
}
