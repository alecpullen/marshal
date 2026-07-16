// Package huhtheme provides a shared huh.Theme retuned to the marshal Warm
// Sunset palette. It lives in its own package so both the tui package (which
// owns the inline approval/question forms) and the settings sub-package can
// import it without creating an import cycle (tui already imports settings).
package huhtheme

import (
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/theme"
)

// WarmSunset returns a huh.Theme that retunes the Charm theme to the Warm
// Sunset palette used across the marshal transcript: coral for titles and
// selectors, gold for accents, teal for confirmations, and dim grey for
// descriptions and placeholders.
//
// It is shared by the settings overlay and the inline approval/question
// forms so every huh surface matches the existing transcript styling.
func WarmSunset() huh.ThemeFunc {
	return func(isDark bool) *huh.Styles {
		t := huh.ThemeCharm(isDark)
		th := theme.Current()

		coral := th.AccentPrimary
		gold := th.StatusWarning
		teal := th.StatusSuccess
		dim := th.FGMuted
		err := th.StatusError
		normal := th.FGDefault

		// Focused field styling.
		t.Focused.Base = t.Focused.Base.BorderForeground(dim)
		t.Focused.Card = t.Focused.Base
		t.Focused.Title = t.Focused.Title.Foreground(coral).Bold(true)
		t.Focused.NoteTitle = t.Focused.NoteTitle.Foreground(coral).Bold(true)
		t.Focused.Description = t.Focused.Description.Foreground(dim)
		t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(err)
		t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(err)
		t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(coral)
		t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(coral)
		t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(coral)
		t.Focused.Option = t.Focused.Option.Foreground(normal)
		t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(coral)
		t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(teal)
		t.Focused.SelectedPrefix = lipgloss.NewStyle().Foreground(teal).SetString("✓ ")
		t.Focused.UnselectedPrefix = lipgloss.NewStyle().Foreground(dim).SetString("• ")
		t.Focused.UnselectedOption = t.Focused.UnselectedOption.Foreground(normal)
		t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(normal).Background(coral)
		t.Focused.Next = t.Focused.FocusedButton
		t.Focused.BlurredButton = t.Focused.BlurredButton.Foreground(normal).Background(dim)

		// Text input styling.
		t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(gold)
		t.Focused.TextInput.Placeholder = t.Focused.TextInput.Placeholder.Foreground(dim)
		t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(coral)
		t.Focused.TextInput.Text = t.Focused.TextInput.Text.Foreground(normal)

		// Blurred inherits focused then hides the focus indicator.
		t.Blurred = t.Focused
		t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
		t.Blurred.Card = t.Blurred.Base
		t.Blurred.MultiSelectSelector = lipgloss.NewStyle().SetString("  ")
		t.Blurred.NextIndicator = lipgloss.NewStyle()
		t.Blurred.PrevIndicator = lipgloss.NewStyle()
		t.Blurred.TextInput.Prompt = t.Blurred.TextInput.Prompt.Foreground(dim)

		// Group header mirrors the focused title colour.
		t.Group.Title = t.Focused.Title
		t.Group.Description = t.Focused.Description

		return t
	}
}
