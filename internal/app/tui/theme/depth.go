package theme

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// Depth is how many background planes marshal paints for itself.
//
// DepthFlat is the zero value deliberately, mirroring Tier256 in tier.go:
// Theme literals are hand-constructed throughout the TUI tests and Current()
// returns a zero Theme before the first Reload. A zero value that painted
// would silently change all of them.
//
// The ladder has two levels:
//
//	DepthFlat — nothing painted; the terminal's own background shows
//	            through everywhere, including any transparency or image.
//	DepthFull — the transcript paints BGBase, so marshal owns every
//	            cell it draws inside the transcript. Chrome paints no
//	            tint at either level: the console is an outline, so
//	            depth no longer reaches the input area or the panels.
type Depth int

const (
	DepthFlat Depth = iota
	DepthFull
)

// ParseDepth maps a config string to a Depth. Empty and unrecognised values
// resolve to DepthFlat: an unknown depth must never silently start painting.
// Comparison is case-insensitive and surrounding space is ignored, matching
// how LoadWithConfig treats the mode string.
func ParseDepth(s string) Depth {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "raised":
		// Retired in P-F, when the input band — the only thing it painted —
		// went. Old configs keep loading and mean "flat".
		return DepthFlat
	case "full":
		return DepthFull
	default:
		return DepthFlat
	}
}

// String returns the config spelling of d.
func (d Depth) String() string {
	switch d {
	case DepthFull:
		return "full"
	default:
		return "flat"
	}
}

// DepthNames returns the config spellings in ladder order, for the settings
// enum field. Order is meaningful — it is the order the user cycles through.
func DepthNames() []string {
	return []string{DepthFlat.String(), DepthFull.String()}
}

// clampDepth reduces d to what the colour tier can actually render.
//
// Below Tier256 there is no depth. The 16-ANSI palette spends its four
// neutrals deliberately — 0 and 8 for backgrounds, 7 and 15 for foregrounds
// (see the comment above warmSunset16) — so a painted BGSurface behind
// FGMuted deletes that text rather than dimming it. At TierMono there are no
// colours at all. This is the guard ColorTier.PaintsSurface used to provide,
// moved to load time so it is applied once instead of at every call site.
func clampDepth(d Depth, tier ColorTier) Depth {
	if tier != Tier256 {
		return DepthFlat
	}
	return d
}

// TranscriptBG is the background for the transcript body. Only DepthFull
// paints it — at flat the terminal's own background (and any transparency or
// image) shows through the largest region on screen.
func (t Theme) TranscriptBG() color.Color {
	if t.Depth < DepthFull {
		return lipgloss.NoColor{}
	}
	return t.BGBase
}
