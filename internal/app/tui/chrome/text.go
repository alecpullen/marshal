package chrome

import (
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/theme"
)

// TertiaryStyle renders the third tier of text: key hints, footers, and
// counts — content that should recede without becoming unreadable.
//
// The tiers are FGDefault for body, FGMuted for secondary, and FGMuted plus
// dim for tertiary. Encoding the third tier as a fourth, darker grey is what
// pushed FGMuted below AA contrast in the first place; SGR 2 expresses
// "recede" without spending contrast, and it survives NO_COLOR because it is
// an attribute rather than a colour.
func TertiaryStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().FGMuted).Faint(true)
}
