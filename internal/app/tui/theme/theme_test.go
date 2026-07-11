package theme

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLoadReturnsColors(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "")
	th := LoadFor(false, "xterm-256color")
	if th.AccentPrimary == nil || th.FGDefault == nil {
		t.Fatalf("expected non-nil colors, got %#v", th)
	}
	if th.AccentPrimary != lipgloss.Color("209") {
		t.Fatalf("AccentPrimary = %#v, want 209 in 256 mode", th.AccentPrimary)
	}
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
}
