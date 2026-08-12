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

// minHueSeparation is the angular distance below which two saturated colours
// stop being reliably distinguishable at terminal font weights, especially
// with red-green colour vision deficiency.
const minHueSeparation = 20.0

// hueSat returns the HSL hue in degrees and the saturation of an RGB triple.
func hueSat(rgb [3]int) (hue, sat float64) {
	r := float64(rgb[0]) / 255
	g := float64(rgb[1]) / 255
	b := float64(rgb[2]) / 255
	maxc := math.Max(r, math.Max(g, b))
	minc := math.Min(r, math.Min(g, b))
	if maxc == minc {
		return 0, 0
	}
	l := (maxc + minc) / 2
	d := maxc - minc
	if l > 0.5 {
		sat = d / (2 - maxc - minc)
	} else {
		sat = d / (maxc + minc)
	}
	switch maxc {
	case r:
		hue = math.Mod((g-b)/d, 6)
	case g:
		hue = (b-r)/d + 2
	default:
		hue = (r-g)/d + 4
	}
	hue *= 60
	if hue < 0 {
		hue += 360
	}
	return hue, sat
}

// hueDistance returns the shortest angular distance between two hues.
func hueDistance(a, b float64) float64 {
	d := math.Abs(a - b)
	if d > 180 {
		d = 360 - d
	}
	return d
}

// signalSlots are the four semantic status colours. They answer
// good/bad/warning/info and frequently appear adjacent in one view, so they
// must stay separable by hue.
//
// The accents are deliberately excluded. They mark structure and focus,
// which position, weight and gutter glyphs also carry; and requiring them to
// clear 20° from every status colour over-constrains the light-mode gamut
// past what the 256-colour cube can satisfy at AA contrast. The one accent
// relationship that does matter is pinned separately below.
var signalSlots = []struct {
	name string
	get  func(Theme) color.Color
}{
	{"StatusError", func(t Theme) color.Color { return t.StatusError }},
	{"StatusWarning", func(t Theme) color.Color { return t.StatusWarning }},
	{"StatusSuccess", func(t Theme) color.Color { return t.StatusSuccess }},
	{"StatusInfo", func(t Theme) color.Color { return t.StatusInfo }},
}

// TestSignalHueSeparation pins F4: warm-sunset previously packed five signal
// colours into a 40° arc, leaving a warning and a tool-call name 4° apart.
func TestSignalHueSeparation(t *testing.T) {
	for _, name := range Names() {
		th, ok := LookupPreset(name)
		if !ok {
			t.Fatalf("preset %q missing", name)
		}
		for i := 0; i < len(signalSlots); i++ {
			for j := i + 1; j < len(signalSlots); j++ {
				a, ok1 := slotRGB(signalSlots[i].get(th))
				b, ok2 := slotRGB(signalSlots[j].get(th))
				if !ok1 || !ok2 {
					continue
				}
				ha, sa := hueSat(a)
				hb, sb := hueSat(b)
				if sa < 0.15 || sb < 0.15 {
					continue // a grey carries no hue to separate
				}
				if d := hueDistance(ha, hb); d < minHueSeparation {
					t.Errorf("%s: %s %s (%.0f°) and %s %s (%.0f°) are %.0f° apart, need %.0f°",
						name, signalSlots[i].name, hexOf(a), ha,
						signalSlots[j].name, hexOf(b), hb, d, minHueSeparation)
				}
			}
		}
	}
}

// TestErrorDistinctFromFocusAccent pins F4's motivating case: AccentPrimary
// decides focused panel chrome, so an error sharing its hue makes a focused
// border and a failure indistinguishable. warm-sunset had them 15° apart.
func TestErrorDistinctFromFocusAccent(t *testing.T) {
	for _, name := range Names() {
		th, ok := LookupPreset(name)
		if !ok {
			t.Fatalf("preset %q missing", name)
		}
		ap, ok1 := slotRGB(th.AccentPrimary)
		se, ok2 := slotRGB(th.StatusError)
		if !ok1 || !ok2 {
			continue
		}
		ha, sa := hueSat(ap)
		hb, sb := hueSat(se)
		if sa < 0.15 || sb < 0.15 {
			continue
		}
		if d := hueDistance(ha, hb); d < minHueSeparation {
			t.Errorf("%s: AccentPrimary %s (%.0f°) and StatusError %s (%.0f°) are %.0f° apart, need %.0f°",
				name, hexOf(ap), ha, hexOf(se), hb, d, minHueSeparation)
		}
	}
}

// TestNoForegroundBackgroundCollapse pins F5: in the 16-colour set a
// foreground that lands on the same ANSI index as a background it renders
// against is not merely low-contrast, it is invisible. warmSunset16 assigned
// ANSI 8 to FGMuted, BorderMuted and BGSurface, so muted text inside a code
// block was drawn in exactly its own background colour.
//
// Covers the hand-tuned tables and the downsampled fallback, since
// sixteenFor picks between them.
func TestNoForegroundBackgroundCollapse(t *testing.T) {
	foregrounds := []struct {
		name string
		get  func(Theme) color.Color
	}{
		{"FGDefault", func(t Theme) color.Color { return t.FGDefault }},
		{"FGMuted", func(t Theme) color.Color { return t.FGMuted }},
		{"FGEmphasis", func(t Theme) color.Color { return t.FGEmphasis }},
		{"BorderMuted", func(t Theme) color.Color { return t.BorderMuted }},
	}
	backgrounds := []struct {
		name string
		get  func(Theme) color.Color
	}{
		{"BGBase", func(t Theme) color.Color { return t.BGBase }},
		{"BGSurface", func(t Theme) color.Color { return t.BGSurface }},
		{"BGSelection", func(t Theme) color.Color { return t.BGSelection }},
	}
	for _, name := range Names() {
		preset, ok := LookupPreset(name)
		if !ok {
			t.Fatalf("preset %q missing", name)
		}
		th := sixteenFor(name, preset)
		for _, f := range foregrounds {
			for _, b := range backgrounds {
				if f.get(th) == b.get(th) {
					t.Errorf("%s (16-colour): %s and %s are both %v — the foreground is invisible",
						name, f.name, b.name, f.get(th))
				}
			}
		}
	}
}
