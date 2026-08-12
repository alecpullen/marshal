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

	t.Run("kitty_term_uses_256_palette", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("TERM", "xterm-kitty")
		th := Load()
		if th.AccentPrimary != lipgloss.Color("209") {
			t.Fatalf("AccentPrimary = %#v, want 209 in kitty terminal", th.AccentPrimary)
		}
	})
}

func TestNoColorYieldsMonochrome(t *testing.T) {
	th := LoadFor(true, "xterm-256color")
	for name, c := range map[string]color.Color{
		"AccentPrimary": th.AccentPrimary,
		"FGMuted":       th.FGMuted,
		"BorderMuted":   th.BorderMuted,
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
	if th.AccentTertiary != lipgloss.Color("3") {
		t.Fatalf("AccentTertiary = %#v, want 16-color yellow 3", th.AccentTertiary)
	}
	if th.UserPrompt != lipgloss.Color("7") {
		t.Fatalf("UserPrompt = %#v, want 16-color white 7", th.UserPrompt)
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
	if th.BorderMuted != lipgloss.Color("7") {
		t.Fatalf("BorderMuted = %#v, want 16-color grey 7", th.BorderMuted)
	}
}

func Test256ColorPaletteHasNewThemeSlots(t *testing.T) {
	th := LoadFor(false, "xterm-256color")
	if th.AccentTertiary != lipgloss.Color("183") {
		t.Fatalf("AccentTertiary = %#v, want 183", th.AccentTertiary)
	}
	if th.UserPrompt != lipgloss.Color("250") {
		t.Fatalf("UserPrompt = %#v, want 250", th.UserPrompt)
	}
	if th.BorderMuted != lipgloss.Color("246") {
		t.Fatalf("BorderMuted = %#v, want 246", th.BorderMuted)
	}
}

func Test16ColorPaletteHasNewThemeSlots(t *testing.T) {
	th := LoadFor(false, "xterm")
	if th.AccentTertiary != lipgloss.Color("3") {
		t.Fatalf("AccentTertiary = %#v, want 3 (yellow)", th.AccentTertiary)
	}
	if th.UserPrompt != lipgloss.Color("7") {
		t.Fatalf("UserPrompt = %#v, want 7 (white)", th.UserPrompt)
	}
	if th.BorderMuted != lipgloss.Color("7") {
		t.Fatalf("BorderMuted = %#v, want 7 (grey)", th.BorderMuted)
	}
}

func TestLoadWithConfigUnknownFallsBackToDefault(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	th := LoadWithConfig("nonexistent", ModeDark, nil)
	if th.AccentPrimary != lipgloss.Color("209") {
		t.Fatalf("AccentPrimary = %#v, want 209 (warm-sunset default)", th.AccentPrimary)
	}
}

func TestLoadWithConfigNoColorWins(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	th := LoadWithConfig("dracula", ModeDark, nil)
	if _, ok := th.AccentPrimary.(lipgloss.NoColor); !ok {
		t.Fatalf("AccentPrimary = %#v, want NoColor{} even with dracula theme", th.AccentPrimary)
	}
}

func TestNamesContainsExpected(t *testing.T) {
	names := Names()
	expected := []string{"catppuccin-mocha", "dracula", "nord", "warm-sunset"}
	for _, exp := range expected {
		found := false
		for _, n := range names {
			if n == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Names() missing expected preset %q", exp)
		}
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("Names() not sorted: %q appears before %q", names[i-1], names[i])
			break
		}
	}
}

func TestLoadWithConfigModeLight(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	th := LoadWithConfig("warm-sunset", ModeLight, nil)
	if th.BGBase != lipgloss.Color("255") {
		t.Fatalf("BGBase = %#v, want 255 (light bg) for light mode", th.BGBase)
	}
	if th.FGDefault != lipgloss.Color("236") {
		t.Fatalf("FGDefault = %#v, want 236 (dark fg) for light mode", th.FGDefault)
	}
}

func TestLoadWithConfigModeDefaultsToDark(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	th := LoadWithConfig("warm-sunset", "", nil)
	if th.BGBase != lipgloss.Color("235") {
		t.Fatalf("BGBase = %#v, want 235 (dark bg) for default empty mode", th.BGBase)
	}
}

func TestLoadWithConfigModeCaseInsensitive(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	th := LoadWithConfig("warm-sunset", "LIGHT", nil)
	if th.BGBase != lipgloss.Color("255") {
		t.Fatalf("BGBase = %#v, want 255 (light bg) for 'LIGHT' mode", th.BGBase)
	}
	th2 := LoadWithConfig("warm-sunset", "Light", nil)
	if th2.BGBase != lipgloss.Color("255") {
		t.Fatalf("BGBase = %#v, want 255 (light bg) for 'Light' mode", th2.BGBase)
	}
}

func TestMonochromeHasNewThemeSlots(t *testing.T) {
	th := LoadFor(true, "xterm-256color")
	if _, ok := th.AccentTertiary.(lipgloss.NoColor); !ok {
		t.Fatalf("AccentTertiary = %#v, want NoColor{} in NO_COLOR mode", th.AccentTertiary)
	}
	if _, ok := th.UserPrompt.(lipgloss.NoColor); !ok {
		t.Fatalf("UserPrompt = %#v, want NoColor{} in NO_COLOR mode", th.UserPrompt)
	}
	if _, ok := th.BorderMuted.(lipgloss.NoColor); !ok {
		t.Fatalf("BorderMuted = %#v, want NoColor{} in NO_COLOR mode", th.BorderMuted)
	}
}
