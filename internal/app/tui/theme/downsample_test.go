package theme

import (
	"image/color"
	"testing"
)

// TestDownsamplePreservesPresetIdentity pins P-10: a non-256-color terminal
// must still get the preset the user chose, downsampled — not a wholesale
// substitution of warm-sunset, which made every other theme a no-op.
func TestDownsamplePreservesPresetIdentity(t *testing.T) {
	warm := downsampleTo16(warmSunset256)
	for _, name := range Names() {
		preset, ok := LookupPreset(name)
		if !ok {
			t.Fatalf("preset %q missing", name)
		}
		if preset.AccentPrimary == warmSunset256.AccentPrimary {
			continue // legitimately shares warm-sunset's accent
		}
		got := downsampleTo16(preset)
		if got == warm {
			t.Errorf("preset %q downsampled to warm-sunset wholesale", name)
		}
	}
}

// TestDownsampleKeepsMeaningfulPairsDistinct pins P-11 and P-12: slots that
// convey a distinction must not collapse onto one color. Focused vs unfocused
// panel chrome is AccentPrimary vs AccentSecondary — collapsing that pair does
// not dim focus indication, it deletes it.
func TestDownsampleKeepsMeaningfulPairsDistinct(t *testing.T) {
	check := func(t *testing.T, label string, th Theme) {
		t.Helper()
		for i, pair := range meaningfulPairs {
			a, b := *pair[0](&th), *pair[1](&th)
			if isNoColor(a) || isNoColor(b) {
				continue // monochrome mode collapses everything by design
			}
			if a == b {
				t.Errorf("%s: meaningful pair %d collapsed onto %v", label, i, a)
			}
		}
	}

	for _, name := range Names() {
		preset, ok := LookupPreset(name)
		if !ok {
			t.Fatalf("preset %q missing", name)
		}
		t.Run(name+"/256", func(t *testing.T) { check(t, name, preset) })
		t.Run(name+"/16", func(t *testing.T) { check(t, name, downsampleTo16(preset)) })
	}

	t.Run("warm-sunset-16-base", func(t *testing.T) { check(t, "warmSunset16", warmSunset16) })
}

func isNoColor(c color.Color) bool {
	_, ok := index256(c)
	return !ok
}

func TestNearestANSI16(t *testing.T) {
	cases := []struct {
		in   [3]int
		want int
	}{
		{[3]int{0, 0, 0}, 0},
		{[3]int{255, 255, 255}, 15},
		{[3]int{250, 10, 10}, 1},   // nearer dim red than bright red
		{[3]int{255, 85, 85}, 9},   // exact bright red
		{[3]int{10, 250, 10}, 2},   // nearer dim green
		{[3]int{85, 255, 255}, 14}, // exact bright cyan
	}
	for _, tc := range cases {
		if got := nearestANSI16(tc.in); got != tc.want {
			t.Errorf("nearestANSI16(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestRGBFor256(t *testing.T) {
	if got := rgbFor256(0); got != [3]int{0, 0, 0} {
		t.Errorf("index 0 = %v", got)
	}
	if got := rgbFor256(196); got != [3]int{255, 0, 0} {
		t.Errorf("index 196 (pure red) = %v, want [255 0 0]", got)
	}
	if got := rgbFor256(232); got != [3]int{8, 8, 8} {
		t.Errorf("index 232 (darkest gray) = %v", got)
	}
}
