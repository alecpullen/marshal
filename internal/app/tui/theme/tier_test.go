package theme

import "testing"

// The zero value must be Tier256. Theme literals are hand-constructed all
// over the TUI tests, and Current() returns a zero Theme before the first
// Reload. If the zero value meant "monochrome" or "16", every one of those
// would silently stop painting.
func TestZeroThemeIsTier256(t *testing.T) {
	var th Theme
	if th.Tier != Tier256 {
		t.Fatalf("zero Theme.Tier = %d, want Tier256 (%d)", th.Tier, Tier256)
	}
	if !th.Tier.PaintsSurface() {
		t.Fatal("zero Theme must paint BGSurface")
	}
}

func TestOnlyTier256PaintsSurface(t *testing.T) {
	if !Tier256.PaintsSurface() {
		t.Error("Tier256 must paint")
	}
	if Tier16.PaintsSurface() {
		t.Error("Tier16 must not paint: BGSurface 8 behind FGMuted 7 deletes the text")
	}
	if TierMono.PaintsSurface() {
		t.Error("TierMono must not paint: there are no colours")
	}
}

func TestLoadForStampsTier(t *testing.T) {
	for _, tc := range []struct {
		name    string
		noColor bool
		term    string
		want    ColorTier
	}{
		{"256color", false, "xterm-256color", Tier256},
		{"16color", false, "xterm", Tier16},
		{"nocolor", true, "xterm-256color", TierMono},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LoadFor(tc.noColor, tc.term).Tier; got != tc.want {
				t.Fatalf("LoadFor(%v, %q).Tier = %d, want %d", tc.noColor, tc.term, got, tc.want)
			}
		})
	}
}

// A palette override must not reset the tier: Apply copies the base Theme
// and edits colour slots, so Tier has to survive the round trip.
func TestOverridesPreserveTier(t *testing.T) {
	base := LoadFor(false, "xterm")
	if base.Tier != Tier16 {
		t.Fatalf("precondition: want Tier16, got %d", base.Tier)
	}
	out := PaletteOverrides{"fg_default": "#ffffff"}.Apply(base)
	if out.Tier != Tier16 {
		t.Fatalf("Apply dropped Tier: got %d, want Tier16", out.Tier)
	}
}
