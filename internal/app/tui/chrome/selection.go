package chrome

import (
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/theme"
)

// SelectionMarker and BlankMarker prefix selected and unselected rows. They
// are equal width so a moving cursor does not shift the column beneath it,
// and the marker is a visible glyph so the selected row stays identifiable
// when colour is unavailable — under NO_COLOR the band carries nothing else.
//
// ▸ is the glyph the picker already used for its cursor and matches
// glyph.Running, so this introduces no new Unicode.
const (
	SelectionMarker = "▸ "
	BlankMarker     = "  "
)

// SelectionStyle is the one way to render a selected row: a background band
// paired with an explicit foreground.
//
// Pairing is the point. Setting a background alone leaves the row's existing
// foreground on the band, which is how three panels ended up rendering muted
// text at 1.53:1 on the selection colour.
func SelectionStyle() lipgloss.Style {
	th := theme.Current()
	base := lipgloss.NewStyle().Bold(true)
	if isMonochrome(th) {
		// No colour to pair; the marker and the bold weight carry it.
		return base
	}
	if th.Tier != theme.Tier256 {
		// The 16-ANSI palette's four neutrals are fully spent (see the
		// comment above warmSunset16), so a BGSelection fill would collapse
		// onto a foreground and delete the row rather than highlight it.
		// Reverse video needs no palette budget at all.
		return base.Reverse(true)
	}
	return base.Foreground(th.FGEmphasis).Background(th.BGSelection)
}
