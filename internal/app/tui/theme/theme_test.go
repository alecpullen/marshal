package theme

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLoadReturnsColors(t *testing.T) {
	th := LoadFor(false, "xterm-256color")
	if th.AccentPrimary == nil || th.FGDefault == nil {
		t.Fatalf("expected non-nil colors, got %#v", th)
	}
	if th.AccentPrimary != lipgloss.Color("209") {
		t.Fatalf("AccentPrimary = %#v, want 209 in 256 mode", th.AccentPrimary)
	}
}

func TestLoad(t *testing.T) {
	t.Run("no_color_yields_monochrome", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		t.Setenv("TERM", "xterm-256color")
		th := Load()
		if _, ok := th.AccentPrimary.(lipgloss.NoColor); !ok {
			t.Fatalf("AccentPrimary = %#v in NO_COLOR mode, want NoColor{}", th.AccentPrimary)
		}
		if _, ok := th.BGBase.(lipgloss.NoColor); !ok {
			t.Fatalf("BGBase = %#v in NO_COLOR mode, want NoColor{}", th.BGBase)
		}
		if _, ok := th.FGDefault.(lipgloss.NoColor); !ok {
			t.Fatalf("FGDefault = %#v in NO_COLOR mode, want NoColor{}", th.FGDefault)
		}
	})

	t.Run("no_color_empty_with_256_term_yields_256", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("TERM", "xterm-256color")
		th := Load()
		if th.AccentPrimary != lipgloss.Color("209") {
			t.Fatalf("AccentPrimary = %#v, want 209 in 256 mode", th.AccentPrimary)
		}
	})

	t.Run("no_color_set_without_256_term_yields_16", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("TERM", "xterm")
		th := Load()
		if th.AccentPrimary != lipgloss.Color("5") {
			t.Fatalf("AccentPrimary = %#v, want 5 in 16-color mode", th.AccentPrimary)
		}
	})
}

func TestNoColorYieldsMonochrome(t *testing.T) {
	th := LoadFor(true, "xterm-256color")
	for name, c := range map[string]color.Color{
		"AccentPrimary": th.AccentPrimary,
		"FGMuted":       th.FGMuted,
		"StatusError":   th.StatusError,
	} {
		if _, ok := c.(lipgloss.NoColor); !ok {
			t.Fatalf("%s = %#v in NO_COLOR mode, want NoColor{} (no SGR)", name, c)
		}
	}
}

func Test16ColorFallback(t *testing.T) {
	th := LoadFor(false, "xterm")
	if th.AccentPrimary != lipgloss.Color("5") {
		t.Fatalf("AccentPrimary = %#v, want 16-color magenta 5", th.AccentPrimary)
	}
	if th.StatusSuccess != lipgloss.Color("2") {
		t.Fatalf("StatusSuccess = %#v, want 16-color green 2", th.StatusSuccess)
	}
	if th.BGBase != lipgloss.Color("0") {
		t.Fatalf("BGBase = %#v, want 16-color black 0", th.BGBase)
	}
	if th.BGSurface != lipgloss.Color("8") {
		t.Fatalf("BGSurface = %#v, want 16-color bright black 8", th.BGSurface)
	}
	if th.BGSelection != lipgloss.Color("4") {
		t.Fatalf("BGSelection = %#v, want 16-color blue 4", th.BGSelection)
	}
}
