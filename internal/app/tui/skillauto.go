package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
)

// autoSkillsNamesShown is how many skill names the collapsed row lists
// before collapsing the rest into a "+N" count.
const autoSkillsNamesShown = 2

// renderAutoSkills renders the skills auto-loaded at the start of one turn.
//
// The collapsed row names skills rather than counting them, because it has
// to stand on its own: the side rail's SKILLS section is absent on a narrow
// terminal, when the user has hidden the rail, and while drilled into a
// subagent. Naming them here also keeps this renderer pure — deciding by
// rail visibility would both couple it to layout state and rewrite rows the
// user has already read whenever the window is resized.
//
// The rail and this row answer different questions: the rail is what is
// active now, this is what was added here.
func renderAutoSkills(content string, expanded bool, width int) string {
	var names []string
	for _, n := range strings.Split(content, "\n") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return ""
	}
	cw := contentWidth(width)
	var b strings.Builder

	if expanded {
		b.WriteString(gutterPrefix(glyph.Ambient, dimColor))
		b.WriteString(mutedStyle().Render("skills auto-loaded " + glyph.DisclosureExpanded))
		b.WriteString("\n")
		for _, n := range names {
			b.WriteString(continuation())
			b.WriteString(mutedStyle().Render(ansi.Truncate(n, cw, "…")))
			b.WriteString("\n")
		}
		return b.String()
	}

	shown, extra := names, 0
	if len(shown) > autoSkillsNamesShown {
		extra = len(shown) - autoSkillsNamesShown
		shown = shown[:autoSkillsNamesShown]
	}
	line := "skills: " + strings.Join(shown, ", ")
	if extra > 0 {
		line += fmt.Sprintf(" +%d", extra)
	}
	line += " " + glyph.DisclosureCollapsed

	b.WriteString(gutterPrefix(glyph.Ambient, dimColor))
	b.WriteString(mutedStyle().Render(ansi.Truncate(line, cw, "…")))
	b.WriteString("\n")
	return b.String()
}
