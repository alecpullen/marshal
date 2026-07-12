package theme

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLookupPresetKnownNames(t *testing.T) {
	for _, name := range []string{"dracula", "nord", "catppuccin-mocha", "Catppuccin Mocha"} {
		th, ok := LookupPreset(name)
		if !ok {
			t.Errorf("LookupPreset(%q) returned ok=false, want true", name)
		}
		if th.AccentPrimary == nil {
			t.Errorf("LookupPreset(%q).AccentPrimary is nil", name)
		}
	}
}

func TestLookupPresetUnknownReturnsFalse(t *testing.T) {
	th, ok := LookupPreset("")
	if ok {
		t.Error("LookupPreset(\"\") returned ok=true, want false")
	}
	if th != (Theme{}) {
		t.Errorf("LookupPreset(\"\") returned non-zero Theme: %#v", th)
	}

	th, ok = LookupPreset("unknown")
	if ok {
		t.Error("LookupPreset(\"unknown\") returned ok=true, want false")
	}
	if th != (Theme{}) {
		t.Errorf("LookupPreset(\"unknown\") returned non-zero Theme: %#v", th)
	}
}

func TestOverridesApplySlot(t *testing.T) {
	base, _ := LookupPreset("dracula")
	ov := PaletteOverrides{
		"accent_primary": "#FF00FF",
		"fg_default":     "99",
	}
	got := ov.Apply(base)

	// Overridden slots reflect the override value.
	wantAccent := parseHexMust("#FF00FF")
	if got.AccentPrimary != wantAccent {
		t.Errorf("AccentPrimary = %#v, want %#v", got.AccentPrimary, wantAccent)
	}
	wantFG := parseHexMust("99")
	if got.FGDefault != wantFG {
		t.Errorf("FGDefault = %#v, want %#v", got.FGDefault, wantFG)
	}

	// Non-overridden slots keep the preset value.
	if got.FGMuted != base.FGMuted {
		t.Errorf("FGMuted changed from %#v to %#v", base.FGMuted, got.FGMuted)
	}
	if got.AccentSecondary != base.AccentSecondary {
		t.Errorf("AccentSecondary changed from %#v to %#v", base.AccentSecondary, got.AccentSecondary)
	}
}

func TestParseHexForms(t *testing.T) {
	c, err := parseHex("#FF0000")
	if err != nil {
		t.Fatalf("#FF0000: unexpected error: %v", err)
	}
	if c != lipgloss.Color("#FF0000") {
		t.Errorf("#FF0000 = %#v, want lipgloss.Color(\"#FF0000\")", c)
	}

	c, err = parseHex("00FF00")
	if err != nil {
		t.Fatalf("00FF00: unexpected error: %v", err)
	}
	if c != lipgloss.Color("#00FF00") {
		t.Errorf("00FF00 = %#v, want lipgloss.Color(\"#00FF00\")", c)
	}

	c, err = parseHex("#ABC")
	if err != nil {
		t.Fatalf("#ABC: unexpected error: %v", err)
	}
	if c != lipgloss.Color("#AABBCC") {
		t.Errorf("#ABC = %#v, want lipgloss.Color(\"#AABBCC\")", c)
	}

	c, err = parseHex("123")
	if err != nil {
		t.Fatalf("123: unexpected error: %v", err)
	}
	if c != lipgloss.Color("123") {
		t.Errorf("123 = %#v, want lipgloss.Color(\"123\")", c)
	}

	c, err = parseHex("0")
	if err != nil {
		t.Fatalf("0: unexpected error: %v", err)
	}
	if c != lipgloss.Color("0") {
		t.Errorf("0 = %#v, want lipgloss.Color(\"0\")", c)
	}
}

func TestParseHexInvalidErrors(t *testing.T) {
	invalid := []string{
		"#GGG",
		"#12345",
		"-1",
		"256",
		"abcxyz",
	}
	for _, s := range invalid {
		_, err := parseHex(s)
		if err == nil {
			t.Errorf("parseHex(%q): expected error, got nil", s)
		}
	}
}

// parseHexMust is a test helper that panics on error.
func parseHexMust(s string) color.Color {
	c, err := parseHex(s)
	if err != nil {
		panic(err)
	}
	return c
}
