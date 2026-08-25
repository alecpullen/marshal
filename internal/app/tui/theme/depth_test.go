package theme

import "testing"

// The zero value must be DepthFlat, for the same reason Tier256 is the zero
// ColorTier (see tier.go): Theme literals are hand-constructed throughout the
// TUI tests and Current() returns a zero Theme before the first Reload. If the
// zero value painted, every one of those would silently start painting.
func TestZeroDepthIsFlat(t *testing.T) {
	var th Theme
	if th.Depth != DepthFlat {
		t.Fatalf("zero Theme.Depth = %d, want DepthFlat (%d)", th.Depth, DepthFlat)
	}
}

func TestParseDepth(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Depth
	}{
		{"flat", DepthFlat},
		{"raised", DepthRaised},
		{"full", DepthFull},
		{"FULL", DepthFull},
		{"  raised  ", DepthRaised},
		{"", DepthFlat},
		{"nonsense", DepthFlat},
	} {
		if got := ParseDepth(tc.in); got != tc.want {
			t.Errorf("ParseDepth(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestDepthStringRoundTrips(t *testing.T) {
	for _, d := range []Depth{DepthFlat, DepthRaised, DepthFull} {
		if got := ParseDepth(d.String()); got != d {
			t.Errorf("ParseDepth(%q) = %d, want %d", d.String(), got, d)
		}
	}
}

func TestDepthNamesMatchParse(t *testing.T) {
	names := DepthNames()
	if len(names) != 3 {
		t.Fatalf("DepthNames() = %v, want 3 entries", names)
	}
	for _, n := range names {
		if ParseDepth(n).String() != n {
			t.Errorf("DepthNames entry %q does not round-trip", n)
		}
	}
}

// The chrome plane paints from DepthRaised up; the transcript plane only at
// DepthFull. Below those, each accessor must yield NoColor so the caller
// emits no SGR at all.
func TestBackgroundAccessorsByDepth(t *testing.T) {
	for _, tc := range []struct {
		depth        Depth
		chromePaints bool
		transcriptOn bool
	}{
		{DepthFlat, false, false},
		{DepthRaised, true, false},
		{DepthFull, true, true},
	} {
		th := warmSunset256
		th.Depth = tc.depth
		if got := !isNoColor(th.ChromeBG()); got != tc.chromePaints {
			t.Errorf("depth %s: ChromeBG paints = %v, want %v", tc.depth, got, tc.chromePaints)
		}
		if got := !isNoColor(th.TranscriptBG()); got != tc.transcriptOn {
			t.Errorf("depth %s: TranscriptBG paints = %v, want %v", tc.depth, got, tc.transcriptOn)
		}
	}
}

// When a plane does paint, it must hand back the real slot value — the
// accessor decides whether, never what.
func TestAccessorsReturnTheRealSlots(t *testing.T) {
	th := warmSunset256
	th.Depth = DepthFull
	if th.ChromeBG() != warmSunset256.BGSurface {
		t.Errorf("ChromeBG() = %#v, want BGSurface %#v", th.ChromeBG(), warmSunset256.BGSurface)
	}
	if th.TranscriptBG() != warmSunset256.BGBase {
		t.Errorf("TranscriptBG() = %#v, want BGBase %#v", th.TranscriptBG(), warmSunset256.BGBase)
	}
}

// Tier16 spends its four neutrals so that 0 and 8 are backgrounds and 7 and
// 15 are foregrounds (see the comment above warmSunset16). Painting BGSurface
// ("8") behind FGMuted ("7") does not dim that text, it deletes it — so depth
// clamps to flat below Tier256, exactly as ColorTier.PaintsSurface required.
func TestDepthClampsBelowTier256(t *testing.T) {
	for _, tier := range []ColorTier{Tier16, TierMono} {
		for _, d := range []Depth{DepthRaised, DepthFull} {
			if got := clampDepth(d, tier); got != DepthFlat {
				t.Errorf("clampDepth(%s, tier %d) = %s, want flat", d, tier, got)
			}
		}
	}
	if got := clampDepth(DepthFull, Tier256); got != DepthFull {
		t.Errorf("clampDepth(full, Tier256) = %s, want full", got)
	}
}

// The raw slots must be untouched by depth — existing call sites read
// BGSurface directly and must keep working at every depth.
func TestRawSlotsSurviveEveryDepth(t *testing.T) {
	for _, d := range []Depth{DepthFlat, DepthRaised, DepthFull} {
		th := warmSunset256
		th.Depth = d
		if th.BGSurface != warmSunset256.BGSurface {
			t.Errorf("depth %s mutated BGSurface", d)
		}
		if th.BGBase != warmSunset256.BGBase {
			t.Errorf("depth %s mutated BGBase", d)
		}
	}
}

func TestLoadWithConfigStampsDepth(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	th := LoadWithConfig("warm-sunset", ModeDark, DepthRaised, nil)
	if th.Depth != DepthRaised {
		t.Fatalf("Depth = %s, want raised", th.Depth)
	}
	if isNoColor(th.ChromeBG()) {
		t.Error("raised at Tier256 must paint chrome")
	}
}

func TestLoadWithConfigClampsBelow256(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm") // 16-colour
	th := LoadWithConfig("warm-sunset", ModeDark, DepthFull, nil)
	if th.Depth != DepthFlat {
		t.Fatalf("Depth = %s at Tier16, want flat (clamped)", th.Depth)
	}
	if !isNoColor(th.ChromeBG()) {
		t.Error("clamped theme must not paint chrome")
	}
}

func TestNoColorClampsDepth(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	th := LoadWithConfig("warm-sunset", ModeDark, DepthFull, nil)
	if th.Depth != DepthFlat {
		t.Fatalf("Depth = %s under NO_COLOR, want flat", th.Depth)
	}
}

// A palette override must not reset the depth, for the same reason it must
// not reset the tier (see TestOverridesPreserveTier).
func TestOverridesPreserveDepth(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	th := LoadWithConfig("warm-sunset", ModeDark, DepthRaised,
		PaletteOverrides{"fg_default": "#ffffff"})
	if th.Depth != DepthRaised {
		t.Fatalf("Apply dropped Depth: got %s, want raised", th.Depth)
	}
}

// The chrome plane must be lighter than the content plane, or the layering
// reads as an arbitrary stripe rather than as depth. The eye needs a real
// step: base and surface one index apart (1.15:1) is not one.
func TestPlanesAreDistinguishable(t *testing.T) {
	base, ok1 := index256(warmSunset256.BGBase)
	surface, ok2 := index256(warmSunset256.BGSurface)
	if !ok1 || !ok2 {
		t.Fatal("expected plain 256-colour indices for both planes")
	}
	if surface <= base {
		t.Fatalf("BGSurface (%d) must be lighter than BGBase (%d)", surface, base)
	}
	got := contrastRatio(rgbFor256(surface), rgbFor256(base))
	if got < 1.5 {
		t.Errorf("plane separation %.2f:1 is below the 1.5:1 legibility floor", got)
	}
}

func TestOverlayBGFollowsDepth(t *testing.T) {
	th := warmSunset256
	th.Depth = DepthFlat
	if !isNoColor(th.OverlayBG()) {
		t.Error("flat must not paint an overlay")
	}
	th.Depth = DepthRaised
	if isNoColor(th.OverlayBG()) {
		t.Error("raised must paint the overlay plane")
	}
	if th.OverlayBG() != th.BGOverlay {
		t.Error("OverlayBG must return the BGOverlay slot verbatim")
	}
}

// The ladder must be strictly ordered by lightness: base < surface < overlay.
// A flat or inverted step means the planes cannot be told apart.
func TestOverlayIsLightestPlane(t *testing.T) {
	base, _ := index256(warmSunset256.BGBase)
	surface, _ := index256(warmSunset256.BGSurface)
	overlay, _ := index256(warmSunset256.BGOverlay)
	if !(base < surface && surface < overlay) {
		t.Fatalf("plane ladder not ordered: base %d, surface %d, overlay %d", base, surface, overlay)
	}
}

// Every preset must define the slot — a nil slot panics when painted.
func TestAllPresetsDefineOverlay(t *testing.T) {
	for name, th := range presets {
		if th.BGOverlay == nil {
			t.Errorf("preset %q has a nil BGOverlay", name)
		}
	}
	if warmSunset16.BGOverlay == nil {
		t.Error("warmSunset16 has a nil BGOverlay")
	}
	if !isNoColor(monochromeTheme().BGOverlay) {
		t.Error("monochrome BGOverlay must be NoColor")
	}
}
