package chrome

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/theme"
)

// SelectionStyle reads theme.Current(), so drive it by reloading the theme.
func withTheme(t *testing.T, th theme.Theme) {
	t.Helper()
	prev := theme.Current()
	t.Cleanup(func() { theme.Reload(prev) })
	theme.Reload(th)
}

// At Tier16 the four neutrals are fully spent, so selection must use reverse
// video (SGR 7) rather than a background fill.
func TestSelectionUsesReverseAtTier16(t *testing.T) {
	withTheme(t, theme.Theme{
		Tier:        theme.Tier16,
		FGDefault:   lipgloss.Color("15"),
		FGEmphasis:  lipgloss.Color("15"),
		BGSelection: lipgloss.Color("4"),
	})
	out := SelectionStyle().Render("row")
	if !strings.Contains(out, "\x1b[7m") && !strings.Contains(out, ";7m") {
		t.Errorf("Tier16 selection did not use reverse video: %q", out)
	}
}

func TestSelectionUsesBackgroundAtTier256(t *testing.T) {
	withTheme(t, theme.Theme{
		Tier:        theme.Tier256,
		FGDefault:   lipgloss.Color("252"),
		FGEmphasis:  lipgloss.Color("255"),
		BGSelection: lipgloss.Color("240"),
	})
	out := SelectionStyle().Render("row")
	if !strings.Contains(out, "48;5;240") {
		t.Errorf("Tier256 selection did not paint BGSelection: %q", out)
	}
}

// Monochrome keeps its existing behaviour: no colour, no reverse — the
// SelectionMarker glyph and the bold weight carry the state on their own.
func TestSelectionMonochromeUnchanged(t *testing.T) {
	withTheme(t, theme.Theme{Tier: theme.TierMono, FGDefault: lipgloss.NoColor{}})
	out := SelectionStyle().Render("row")
	if strings.Contains(out, "\x1b[7m") {
		t.Errorf("monochrome selection must not use reverse: %q", out)
	}
}
