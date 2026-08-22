package theme

import "charm.land/lipgloss/v2"

// IsMonochromeTheme reports whether th is the monochrome (no color)
// variant. In monochrome mode every slot is lipgloss.NoColor{}, so this is
// checked by asserting the FGDefault slot is a NoColor.
func IsMonochromeTheme(th Theme) bool {
	_, ok := th.FGDefault.(lipgloss.NoColor)
	return ok
}

// IsMonochrome reports whether the currently active theme is the
// monochrome (no color) variant. Shared across TUI packages to avoid
// duplicate detection logic.
func IsMonochrome() bool {
	return IsMonochromeTheme(Current())
}

// MutedStyle returns a lipgloss style using the muted foreground color
// slot (FGMuted). In monochrome mode it returns a plain style with no
// color so no SGR escape is emitted for a terminal that can't render it.
func MutedStyle() lipgloss.Style {
	if IsMonochrome() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(Current().FGMuted)
}
