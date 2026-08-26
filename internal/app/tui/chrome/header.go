package chrome

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

// ruleGlyph is the hairline rule that follows a section title.
const ruleGlyph = "─"

// Header renders a section heading: a bold title, a hairline rule filling
// the remaining width, and an optional dim right-hand label.
//
// It lives in chrome rather than sidepanel because four unrelated surfaces
// need it — the side rail's sections, the pinned todo panel, the agent lane,
// and the /agents panel's drilled-in frames. chrome is the lower layer
// (sidepanel imports chrome, never the reverse), so this is the only home
// that does not force a dock panel to import the side rail.
func Header(title, right string, width int) string {
	th := theme.Current()
	titleStyle := lipgloss.NewStyle().Foreground(th.FGEmphasis)
	if _, isNoColor := th.FGEmphasis.(lipgloss.NoColor); !isNoColor {
		titleStyle = titleStyle.Bold(true)
	}
	ruleStyle := lipgloss.NewStyle().Foreground(th.BorderMuted)
	dim := lipgloss.NewStyle().Foreground(th.FGMuted)

	if width < 1 {
		return ""
	}
	// Empty title: render a full-width rule with no leading space.
	if title == "" {
		return ruleStyle.Render(strings.Repeat(ruleGlyph, width))
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
