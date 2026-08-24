package sidepanel

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"
)

// SkillsSection lists the skills active in this session.
//
// Most skills are now auto-loaded by the embedding ranker, which loads them
// quietly (internal/skills/tool.go) — so without this section the only
// evidence that a skill is shaping the agent's behaviour, and consuming
// context, is the behaviour itself.
type SkillsSection struct{}

func (SkillsSection) ID() string      { return "skills" }
func (SkillsSection) Title() string   { return "SKILLS" }
func (SkillsSection) Priority() int   { return 6 }
func (SkillsSection) Clippable() bool { return true }

func (SkillsSection) Relevant(d Data) bool { return len(d.Skills) > 0 }

func (SkillsSection) Render(d Data, width, maxRows int) []string {
	rows := make([]string, 0, len(d.Skills))
	for _, s := range d.Skills {
		rows = append(rows, ansi.Truncate(" "+s, width, "…"))
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return rows
}

func (SkillsSection) OneLine(d Data, width int) string {
	return ansi.Truncate(fmt.Sprintf("◆ %d skills active", len(d.Skills)), width, "…")
}
