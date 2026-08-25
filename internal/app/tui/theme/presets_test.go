package theme

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLookupPresetKnownNames(t *testing.T) {
	for _, name := range []string{"warm-sunset", "dracula", "nord", "catppuccin-mocha", "Catppuccin Mocha"} {
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

	wantAccent := parseHexMust("#FF00FF")
	if got.AccentPrimary != wantAccent {
		t.Errorf("AccentPrimary = %#v, want %#v", got.AccentPrimary, wantAccent)
	}
	wantFG := parseHexMust("99")
	if got.FGDefault != wantFG {
		t.Errorf("FGDefault = %#v, want %#v", got.FGDefault, wantFG)
	}

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
		"+1",
		"-0",
		"  123  ",
		" 123",
		"123 ",
		"",
	}
	for _, s := range invalid {
		_, err := parseHex(s)
		if err == nil {
			t.Errorf("parseHex(%q): expected error, got nil", s)
		}
	}
}

func TestLookupPresetLightVariants(t *testing.T) {
	for _, name := range []string{"warm-sunset-light", "dracula-light", "nord-light", "catppuccin-latte"} {
		th, ok := LookupPreset(name)
		if !ok {
			t.Errorf("LookupPreset(%q) returned ok=false, want true", name)
		}
		if th.AccentPrimary == nil {
			t.Errorf("LookupPreset(%q).AccentPrimary is nil", name)
		}
	}
}

func TestLightVariantsAreDistinctFromDark(t *testing.T) {
	pairs := []struct {
		dark  string
		light string
	}{
		{"warm-sunset", "warm-sunset-light"},
		{"dracula", "dracula-light"},
		{"nord", "nord-light"},
		{"catppuccin-mocha", "catppuccin-latte"},
	}
	for _, p := range pairs {
		dark, darkOK := LookupPreset(p.dark)
		light, lightOK := LookupPreset(p.light)
		if !darkOK {
			t.Errorf("LookupPreset(%q) returned ok=false, want true", p.dark)
		}
		if !lightOK {
			t.Errorf("LookupPreset(%q) returned ok=false, want true", p.light)
		}
		if darkOK && lightOK && dark.BGBase == light.BGBase {
			t.Errorf("BGBase for %q (%#v) and %q (%#v) are equal, want distinct", p.dark, dark.BGBase, p.light, light.BGBase)
		}
	}
}

func TestOverridesApplyToLightVariant(t *testing.T) {
	base, ok := LookupPreset("catppuccin-latte")
	if !ok {
		t.Fatal("LookupPreset(\"catppuccin-latte\") returned ok=false")
	}
	ov := PaletteOverrides{
		"accent_primary": "#00FF00",
		"fg_default":     "99",
	}
	got := ov.Apply(base)

	wantAccent := parseHexMust("#00FF00")
	if got.AccentPrimary != wantAccent {
		t.Errorf("AccentPrimary = %#v, want %#v", got.AccentPrimary, wantAccent)
	}
	wantFG := parseHexMust("99")
	if got.FGDefault != wantFG {
		t.Errorf("FGDefault = %#v, want %#v", got.FGDefault, wantFG)
	}
	if got.FGMuted != base.FGMuted {
		t.Errorf("FGMuted changed from %#v to %#v", base.FGMuted, got.FGMuted)
	}
	if got.AccentSecondary != base.AccentSecondary {
		t.Errorf("AccentSecondary changed from %#v to %#v", base.AccentSecondary, got.AccentSecondary)
	}
}

func TestModeIsPartOfPresetName(t *testing.T) {
	lightNames := []string{"warm-sunset-light", "dracula-light", "nord-light", "catppuccin-latte"}
	for _, name := range lightNames {
		_, ok := LookupPreset(name)
		if !ok {
			t.Errorf("LookupPreset(%q) returned ok=false, want true", name)
		}
	}
	darkNames := []string{"warm-sunset", "dracula", "nord", "catppuccin-mocha", "Catppuccin Mocha"}
	for _, name := range darkNames {
		_, ok := LookupPreset(name)
		if !ok {
			t.Errorf("LookupPreset(%q) returned ok=false, want true", name)
		}
	}
}

// AccentSecondary and AccentTertiary must be distinguishable — they mark
// different things (panel focus vs tool-call names) and sat one green-channel
// step apart in warm-sunset, both pale violets.
func TestAccentsAreDistinct(t *testing.T) {
	for name, th := range presets {
		s, ok1 := index256(th.AccentSecondary)
		v, ok2 := index256(th.AccentTertiary)
		if !ok1 || !ok2 {
			continue
		}
		if s == v {
			t.Errorf("preset %q: AccentSecondary and AccentTertiary are the same index %d", name, s)
		}
		sr, vr := rgbFor256(s), rgbFor256(v)
		if sr[0] == vr[0] && sr[2] == vr[2] {
			t.Errorf("preset %q: accents differ only in the green channel (%v vs %v)", name, sr, vr)
		}
	}
}

func parseHexMust(s string) color.Color {
	c, err := parseHex(s)
	if err != nil {
		panic(err)
	}
	return c
}
