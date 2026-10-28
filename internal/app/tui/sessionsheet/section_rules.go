package sessionsheet

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

// rulesSectionID is the stable identifier of the standing-rules section. The
// panel keys Enter's read-only detail view off it, so it is named here rather
// than repeated as a literal.
const rulesSectionID = "rules"

// RulesSection lists the approval rules added during this session, so a
// standing "allow go test *" is visible rather than invisible.
type RulesSection struct{}

func (RulesSection) ID() string      { return rulesSectionID }
func (RulesSection) Title() string   { return "RULES" }
func (RulesSection) Priority() int   { return 4 }
func (RulesSection) Clippable() bool { return false }

func (RulesSection) Relevant(d Data) bool { return len(d.Rules) > 0 }

func (RulesSection) Render(d Data, width, maxRows int) []string {
	rows := make([]string, 0, len(d.Rules))
	for _, r := range d.Rules {
		rows = append(rows, ansi.Truncate(" allow "+r, width, "…"))
	}
	if maxRows <= 0 || len(rows) <= maxRows {
		return rows
	}
	if maxRows == 1 {
		// One row has no space for an indicator beside a rule; show the rule
		// rather than a count of the ones it hides.
		return rows[:1]
	}
	hidden := len(rows) - (maxRows - 1)
	rows = rows[:maxRows-1]
	// The sheet clips the list, not the rules: say how many are left and how
	// to read them all.
	more := theme.MutedStyle().Render(
		ansi.Truncate(fmt.Sprintf("  … %d more — ↵ details", hidden), width, "…"))
	return append(rows, more)
}

func (RulesSection) OneLine(d Data, width int) string {
	return ansi.Truncate(fmt.Sprintf("⚑ %d session rules · ↵ details", len(d.Rules)), width, "…")
}
