package theme

import (
	"fmt"
	"image/color"
	"math"
	"testing"
)

// marshal never paints BGBase (see its doc comment in theme.go), so a
// foreground renders on whatever background the user's terminal provides.
// These bracket the realistic range. BGSurface is appended per preset
// because marshal does paint that one.
var (
	darkTerminalBackgrounds  = [][3]int{{0, 0, 0}, {28, 28, 28}, {48, 48, 48}}
	lightTerminalBackgrounds = [][3]int{{255, 255, 255}, {238, 238, 238}, {218, 218, 218}}
)

const (
	// textContrast is WCAG AA for body text.
	textContrast = 4.5
	// chromeContrast is WCAG AA for non-text UI components: borders and
	// focus rings, which carry no glyphs of their own.
	chromeContrast = 3.0
)

// lightPresets are the presets designed for a light terminal background.
var lightPresets = map[string]bool{
	"warm-sunset-light": true,
	"dracula-light":     true,
	"nord-light":        true,
	"catppuccin-latte":  true,
}

func srgbToLinear(c float64) float64 {
	c /= 255
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// relativeLuminance implements WCAG 2.1 relative luminance.
func relativeLuminance(rgb [3]int) float64 {
	return 0.2126*srgbToLinear(float64(rgb[0])) +
		0.7152*srgbToLinear(float64(rgb[1])) +
		0.0722*srgbToLinear(float64(rgb[2]))
}

// contrastRatio implements the WCAG 2.1 contrast ratio, always >= 1.
func contrastRatio(a, b [3]int) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// slotRGB resolves a palette slot to RGB. Slots that are not plain
// 256-colour indices (NoColor in monochrome mode) report ok=false.
func slotRGB(c color.Color) ([3]int, bool) {
	i, ok := index256(c)
	if !ok {
		return [3]int{}, false
	}
	return rgbFor256(i), true
}

func hexOf(rgb [3]int) string {
	return fmt.Sprintf("#%02X%02X%02X", rgb[0], rgb[1], rgb[2])
}

var contrastSlots = []struct {
	name string
	get  func(Theme) color.Color
	need float64
}{
	{"FGDefault", func(t Theme) color.Color { return t.FGDefault }, textContrast},
	{"FGMuted", func(t Theme) color.Color { return t.FGMuted }, textContrast},
	{"FGEmphasis", func(t Theme) color.Color { return t.FGEmphasis }, textContrast},
	{"UserPrompt", func(t Theme) color.Color { return t.UserPrompt }, textContrast},
	{"StatusError", func(t Theme) color.Color { return t.StatusError }, textContrast},
	{"StatusWarning", func(t Theme) color.Color { return t.StatusWarning }, textContrast},
	{"StatusSuccess", func(t Theme) color.Color { return t.StatusSuccess }, textContrast},
	{"StatusInfo", func(t Theme) color.Color { return t.StatusInfo }, textContrast},
	{"AccentPrimary", func(t Theme) color.Color { return t.AccentPrimary }, textContrast},
	{"AccentTertiary", func(t Theme) color.Color { return t.AccentTertiary }, textContrast},
	{"AccentSecondary", func(t Theme) color.Color { return t.AccentSecondary }, chromeContrast},
	{"BorderMuted", func(t Theme) color.Color { return t.BorderMuted }, chromeContrast},
}

// gamutExempt relaxes slots whose hue is unreachable at 4.5:1 on a light
// background. Searching the whole 256-colour cube, amber tops out at 4.10:1
// and mint at 3.24:1 against #DADADA without turning brown or dark teal.
// These slots are always rendered beside a glyph, so colour is not the sole
// carrier of meaning. See decision D1 in the design doc.
var gamutExempt = map[string]bool{
	"warm-sunset-light/StatusWarning":  true,
	"warm-sunset-light/AccentTertiary": true,
	"warm-sunset-light/AccentPrimary":  true,
	"dracula-light/StatusWarning":      true,
	"nord-light/StatusWarning":         true,
	"nord-light/AccentTertiary":        true,
	"catppuccin-latte/StatusWarning":   true,
	"catppuccin-latte/AccentTertiary":  true,
}

// TestPresetContrast pins F1, F2 and F3: every foreground slot must clear
// its threshold against every background it can be rendered on.
func TestPresetContrast(t *testing.T) {
	for _, name := range Names() {
		th, ok := LookupPreset(name)
		if !ok {
			t.Fatalf("preset %q missing", name)
		}
		backgrounds := darkTerminalBackgrounds
		if lightPresets[name] {
			backgrounds = lightTerminalBackgrounds
		}
		surface, ok := slotRGB(th.BGSurface)
		if !ok {
			t.Fatalf("preset %q: BGSurface is not a 256-colour index", name)
		}
		backgrounds = append(append([][3]int{}, backgrounds...), surface)

		for _, slot := range contrastSlots {
			fg, ok := slotRGB(slot.get(th))
			if !ok {
				continue
			}
			need := slot.need
			if gamutExempt[name+"/"+slot.name] {
				need = chromeContrast
			}
			for _, bg := range backgrounds {
				if got := contrastRatio(fg, bg); got < need {
					t.Errorf("%s: %s %s on %s = %.2f:1, need %.2f:1",
						name, slot.name, hexOf(fg), hexOf(bg), got, need)
				}
			}
		}
	}
}

// TestSelectionLegibility pins F7: FGEmphasis is the mandated selection
// foreground, FGMuted may render as secondary text inside the same band, and
// the band itself must stay visibly distinct from the base background.
func TestSelectionLegibility(t *testing.T) {
	for _, name := range Names() {
		th, ok := LookupPreset(name)
		if !ok {
			t.Fatalf("preset %q missing", name)
		}
		sel, ok1 := slotRGB(th.BGSelection)
		base, ok2 := slotRGB(th.BGBase)
		emph, ok3 := slotRGB(th.FGEmphasis)
		muted, ok4 := slotRGB(th.FGMuted)
		if !ok1 || !ok2 || !ok3 || !ok4 {
			continue
		}
		if got := contrastRatio(emph, sel); got < textContrast {
			t.Errorf("%s: FGEmphasis on BGSelection = %.2f:1, need %.2f:1",
				name, got, textContrast)
		}
		if got := contrastRatio(muted, sel); got < chromeContrast {
			t.Errorf("%s: FGMuted on BGSelection = %.2f:1, need %.2f:1",
				name, got, chromeContrast)
		}
		if got := contrastRatio(sel, base); got < 1.25 {
			t.Errorf("%s: BGSelection vs BGBase = %.2f:1, need 1.25:1 to read as a band",
				name, got)
		}
	}
}
