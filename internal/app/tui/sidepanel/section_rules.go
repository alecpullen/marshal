package sidepanel

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"
)

// RulesSection lists the approval rules added during this session, so a
// standing "allow go test *" is visible rather than invisible.
type RulesSection struct{}

func (RulesSection) ID() string      { return "rules" }
func (RulesSection) Title() string   { return "RULES" }
func (RulesSection) Priority() int   { return 4 }
func (RulesSection) Clippable() bool { return false }

func (RulesSection) Relevant(d Data) bool { return len(d.Rules) > 0 }

func (RulesSection) Render(d Data, width, maxRows int) []string {
	rows := make([]string, 0, len(d.Rules))
	for _, r := range d.Rules {
		rows = append(rows, ansi.Truncate(" allow "+r, width, "…"))
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return rows
}

func (RulesSection) OneLine(d Data, width int) string {
	return ansi.Truncate(fmt.Sprintf("⚑ %d session rules", len(d.Rules)), width, "…")
}
