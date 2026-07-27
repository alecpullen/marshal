package sidepanel

import (
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/theme"
)

// Body styling helpers. Sections emit otherwise-unstyled strings; these are
// the only way a section body applies color, so the set of things color can
// mean stays small and auditable. Color encodes state — never section
// identity — so the rail is fully legible with NO_COLOR set.

func styleSuccess(s string) string {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusSuccess).Render(s)
}

func styleError(s string) string {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusError).Render(s)
}

func styleWarning(s string) string {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusWarning).Render(s)
}
