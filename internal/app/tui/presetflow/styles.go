package presetflow

import (
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/theme"
)

func titleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(theme.Current().FGDefault)
}
func mutedStyle() lipgloss.Style { return theme.MutedStyle() }
func hintStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(theme.Current().StatusInfo) }
func errStyle() lipgloss.Style   { return lipgloss.NewStyle().Foreground(theme.Current().StatusError) }
