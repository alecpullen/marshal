package theme

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ansi16RGB is the conventional RGB rendering of the 16 ANSI colors. Terminals
// retheme these freely — that is the point of using them — but the canonical
// values are what nearest-color matching has to work from.
var ansi16RGB = [16][3]int{
	{0, 0, 0}, {170, 0, 0}, {0, 170, 0}, {170, 85, 0},
	{0, 0, 170}, {170, 0, 170}, {0, 170, 170}, {170, 170, 170},
	{85, 85, 85}, {255, 85, 85}, {85, 255, 85}, {255, 255, 85},
	{85, 85, 255}, {255, 85, 255}, {85, 255, 255}, {255, 255, 255},
}

// cubeSteps are the six channel levels of the xterm 6x6x6 color cube.
var cubeSteps = [6]int{0, 95, 135, 175, 215, 255}

// rgbFor256 returns the conventional RGB for an xterm-256 palette index.
func rgbFor256(i int) [3]int {
	switch {
	case i < 16:
		return ansi16RGB[i]
	case i < 232:
		n := i - 16
		return [3]int{cubeSteps[n/36%6], cubeSteps[n/6%6], cubeSteps[n%6]}
	default:
		v := 8 + 10*(i-232)
		return [3]int{v, v, v}
	}
}

// nearestANSI16 returns the index of the ANSI-16 color closest to c.
func nearestANSI16(c [3]int) int {
	best, bestDist := 0, 1<<62
	for i, a := range ansi16RGB {
		dr, dg, db := c[0]-a[0], c[1]-a[1], c[2]-a[2]
		if d := dr*dr + dg*dg + db*db; d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

// index256 recovers the palette index from a lipgloss 256-color value.
// ok is false for colors that are not simple palette indices (true color,
// NoColor, or anything else).
func index256(c color.Color) (int, bool) {
	// lipgloss.Color returns ansi.BasicColor below 16 and lipgloss.ANSIColor
	// from 16 to 255; hex and NoColor values are neither.
	switch v := c.(type) {
	case ansi.BasicColor:
		return int(v), true
	case lipgloss.ANSIColor:
		return int(v), true
	default:
		return 0, false
	}
}

// meaningfulPairs are slot pairs the interface uses to convey a distinction.
// If a pair collapses to one color, that distinction is not merely dimmer —
// it is gone. Focused vs unfocused panel chrome is decided by
// AccentPrimary/AccentSecondary, so collapsing that pair erases focus.
var meaningfulPairs = [][2]func(*Theme) *color.Color{
	{func(t *Theme) *color.Color { return &t.AccentPrimary },
		func(t *Theme) *color.Color { return &t.AccentSecondary }},
	{func(t *Theme) *color.Color { return &t.StatusSuccess },
		func(t *Theme) *color.Color { return &t.StatusInfo }},
	{func(t *Theme) *color.Color { return &t.StatusError },
		func(t *Theme) *color.Color { return &t.StatusWarning }},
	{func(t *Theme) *color.Color { return &t.FGDefault },
		func(t *Theme) *color.Color { return &t.FGMuted }},
}

// backgroundPairs are foreground/background pairs that must not collapse.
// Nearest-colour matching can legitimately land a foreground on the same
// ANSI slot as a background, which makes that text invisible rather than
// merely low-contrast.
//
// separatePairs nudges the *second* element of a pair, so the background is
// listed second here: the foreground carries the semantic weight and should
// keep its colour.
var backgroundPairs = [][2]func(*Theme) *color.Color{
	{func(t *Theme) *color.Color { return &t.FGMuted },
		func(t *Theme) *color.Color { return &t.BGSurface }},
	{func(t *Theme) *color.Color { return &t.FGMuted },
		func(t *Theme) *color.Color { return &t.BGSelection }},
	{func(t *Theme) *color.Color { return &t.FGDefault },
		func(t *Theme) *color.Color { return &t.BGSurface }},
	{func(t *Theme) *color.Color { return &t.FGEmphasis },
		func(t *Theme) *color.Color { return &t.BGSelection }},
	{func(t *Theme) *color.Color { return &t.BorderMuted },
		func(t *Theme) *color.Color { return &t.BGBase }},
	{func(t *Theme) *color.Color { return &t.BorderMuted },
		func(t *Theme) *color.Color { return &t.BGSurface }},
	{func(t *Theme) *color.Color { return &t.BorderMuted },
		func(t *Theme) *color.Color { return &t.BGSelection }},
	{func(t *Theme) *color.Color { return &t.FGDefault },
		func(t *Theme) *color.Color { return &t.BGOverlay }},
}

// downsampleTo16 maps a 256-color theme onto the 16-ANSI set by nearest
// color, preserving the preset's identity as far as 16 colors allow.
//
// Previously any non-default preset was replaced wholesale by warm-sunset's
// 16-color table when the terminal did not advertise 256 colors, so choosing
// "nord" silently produced warm sunset. Downsampling keeps the chosen preset.
//
// After mapping, pairs that carry a distinction are separated: nearest-color
// matching can legitimately collapse two adjacent hues onto one ANSI slot, and
// a collapsed pair costs the user a signal rather than just some fidelity.
func downsampleTo16(t Theme) Theme {
	out := t
	for _, slot := range allSlots(&out) {
		if i, ok := index256(*slot); ok {
			*slot = lipgloss.Color(itoa(nearestANSI16(rgbFor256(i))))
		}
	}
	separatePairs(&out)
	return out
}

// maxSeparationPasses bounds separatePairs' fixed-point iteration. A nudge
// can move a shared slot onto another pair's anchor, so a single pass does
// not guarantee every pair is distinct; re-checking until a pass makes no
// change reaches a fixed point. The bound guards against oscillation between
// two configurations that each break a different pair.
const maxSeparationPasses = 16

// separatePairs nudges any pair that collapsed onto one colour to the
// next-nearest distinct ANSI slot. The second element of each pair is the
// one moved: meaningfulPairs lists the less-critical foreground second, and
// backgroundPairs lists the background second. It iterates to a bounded fixed
// point so a later nudge cannot leave an earlier pair collapsed.
func separatePairs(t *Theme) {
	pairs := make([][2]func(*Theme) *color.Color, 0, len(meaningfulPairs)+len(backgroundPairs))
	pairs = append(pairs, meaningfulPairs...)
	pairs = append(pairs, backgroundPairs...)
	for pass := 0; pass < maxSeparationPasses; pass++ {
		changed := false
		for _, pair := range pairs {
			a, b := pair[0](t), pair[1](t)
			ai, aok := index256(*a)
			bi, bok := index256(*b)
			if !aok || !bok || ai != bi {
				continue
			}
			// Prefer the bright/dim counterpart of the same hue: it stays in the
			// same color family, so the palette still reads as one system.
			alt := ai ^ 8
			if alt > 15 || alt == ai {
				alt = (ai + 1) % 16
			}
			*b = lipgloss.Color(itoa(alt))
			changed = true
		}
		if !changed {
			return
		}
	}
}

// allSlots returns pointers to every color slot on a Theme.
func allSlots(t *Theme) []*color.Color {
	return []*color.Color{
		&t.FGDefault, &t.FGMuted, &t.BorderMuted, &t.FGEmphasis,
		&t.BGBase, &t.BGSurface, &t.BGOverlay, &t.BGSelection,
		&t.AccentPrimary, &t.AccentSecondary, &t.AccentTertiary,
		&t.UserPrompt,
		&t.StatusError, &t.StatusWarning, &t.StatusSuccess, &t.StatusInfo,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [3]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
