package sidepanel

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

// maxGapRows caps how many blank rows may sit between two sections.
// Uncapped distribution lets sections drift far enough apart on a tall
// terminal that the stack stops reading as a group.
const maxGapRows = 2

// dividerGlyph is the hairline rule down the rail's left edge. It occupies
// one column; a following space brings the reserved gutter to two.
const dividerGlyph = "│"

// gutterCols is how many columns the divider and its trailing space take.
const gutterCols = 2

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// StripANSI removes SGR escapes. Exported for tests that assert on visible
// runes; production code never needs it.
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// Rail is the ordered section stack.
type Rail struct{ sections []Section }

// New builds a rail from sections in render order (top to bottom). Collapse
// order is independent and comes from each section's Priority.
func New(sections ...Section) *Rail { return &Rail{sections: sections} }

// ruleGlyph is the hairline rule that follows a section title.
const ruleGlyph = "─"

// Header renders a section title followed by a rule running to the rail
// edge. When right is non-empty the rule stops short and right is flushed
// to the edge. When there is no room for both, right is dropped and the
// title is truncated.
func Header(title, right string, width int) string {
	th := theme.Current()
	titleStyle := lipgloss.NewStyle().Foreground(th.FGEmphasis).Bold(true)
	ruleStyle := lipgloss.NewStyle().Foreground(th.BorderMuted)
	dim := lipgloss.NewStyle().Foreground(th.FGMuted)

	if width < 1 {
		return ""
	}
	label := ansi.Truncate(title, width, "…")
	labelW := ansi.StringWidth(label)

	// Reserve a space after the title, then whatever right needs.
	rightW := 0
	if right != "" {
		rightW = ansi.StringWidth(right) + 1 // leading space
	}
	ruleW := width - labelW - 1 - rightW

	// Not enough room for a rule alongside right: drop right and retry.
	if ruleW < 1 && right != "" {
		return Header(title, "", width)
	}
	if ruleW < 1 {
		// No room for a rule at all; pad with spaces so width holds.
		return titleStyle.Render(label) +
			strings.Repeat(" ", max(width-labelW, 0))
	}

	out := titleStyle.Render(label) + " " +
		ruleStyle.Render(strings.Repeat(ruleGlyph, ruleW))
	if right != "" {
		out += " " + dim.Render(right)
	}
	return out
}

// View renders the rail at exactly width columns and height rows. Returns
// "" when the rail cannot be drawn or has no relevant sections.
func (r *Rail) View(d Data, width, height int) string {
	if width <= gutterCols || height <= 0 {
		return ""
	}
	inner := width - gutterCols

	live := make([]Section, 0, len(r.sections))
	for _, s := range r.sections {
		if s.Relevant(d) {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return ""
	}

	// Render each section's expanded body once to learn its natural height.
	bodies := make([][]string, len(live))
	costs := make([]SectionCost, len(live))
	for i, s := range live {
		bodies[i] = s.Render(d, inner, height)
		clipped := 0
		if s.Clippable() {
			clipped = min(len(bodies[i])+1, 4) // title + up to 3 body rows
		}
		costs[i] = SectionCost{
			Priority: s.Priority(),
			Natural:  len(bodies[i]) + 1, // +1 for the title row
			Clipped:  clipped,
		}
	}

	states := fit(costs, height)

	// Measure the fitted stack, then deal the leftover out as extra gaps.
	used := 0
	surviving := 0
	for i := range live {
		if states[i] == StateDropped {
			continue
		}
		used += costs[i].height(states[i])
		surviving++
	}
	if surviving > 1 {
		used += surviving - 1 // baseline blank row between sections
	}
	gaps := distributeGaps(surviving, height-used)

	rows := make([]string, 0, height)
	emitted := 0
	for i, s := range live {
		if states[i] == StateDropped {
			continue
		}
		if emitted > 0 {
			rows = appendSep(rows)
			for g := 0; g < gaps[emitted-1]; g++ {
				rows = append(rows, "")
			}
		}
		switch states[i] {
		case StateOneLine:
			rows = append(rows, ansi.Truncate(s.OneLine(d, inner), inner, "…"))
		case StateClipped:
			rows = append(rows, Header(s.Title(), "", inner))
			rows = append(rows, s.Render(d, inner, costs[i].Clipped-1)...)
		default:
			rows = append(rows, Header(s.Title(), "", inner))
			rows = append(rows, bodies[i]...)
		}
		emitted++
	}
	if len(rows) > height {
		rows = rows[:height]
	}
	return frame(rows, inner, height)
}

// appendSep adds a blank separator row before every section but the first.
func appendSep(rows []string) []string {
	if len(rows) == 0 {
		return rows
	}
	return append(rows, "")
}

// frame prefixes every row with the divider, pads each to inner columns,
// and pads the block to height rows so JoinHorizontal aligns cleanly.
// Each output row is gutterCols+inner columns wide — the rail's full width.
func frame(rows []string, inner, height int) string {
	rule := lipgloss.NewStyle().
		Foreground(theme.Current().BorderMuted).
		Render(dividerGlyph) + " "

	out := make([]string, height)
	for i := 0; i < height; i++ {
		body := ""
		if i < len(rows) {
			body = ansi.Truncate(rows[i], inner, "…")
		}
		pad := inner - ansi.StringWidth(body)
		if pad < 0 {
			pad = 0
		}
		out[i] = rule + body + strings.Repeat(" ", pad)
	}
	return strings.Join(out, "\n")
}

// distributeGaps deals leftover rows out as extra blank rows before each
// section after the first, front to back, capped at maxGapRows each.
// Returns sectionCount-1 entries; any remainder stays at the bottom.
func distributeGaps(sectionCount, leftover int) []int {
	if sectionCount < 2 {
		return nil
	}
	gaps := make([]int, sectionCount-1)
	if leftover <= 0 {
		return gaps
	}
	for i := range gaps {
		take := min(leftover, maxGapRows)
		gaps[i] = take
		leftover -= take
		if leftover == 0 {
			break
		}
	}
	return gaps
}
