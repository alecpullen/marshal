package theme

// ColorTier records which colour tier a Theme was resolved for.
//
// Renderers that paint a background consult it. BGSurface is only safe to
// paint at Tier256: the 16-ANSI palette spends its four neutrals
// deliberately (0 and 8 for backgrounds, 7 and 15 for foregrounds), so a
// BGSurface ("8") fill behind FGMuted ("7") does not dim that text — it
// deletes it. At TierMono there are no colours to paint with at all.
type ColorTier int

const (
	// Tier256 is the zero value deliberately. Theme literals are
	// hand-constructed throughout the TUI tests, and Current() returns a
	// zero Theme before the first Reload; making any other tier the zero
	// value would silently stop all of them painting.
	Tier256 ColorTier = iota
	Tier16
	TierMono
)

// PaintsSurface reports whether it is safe to paint BGSurface behind text
// in this tier.
func (t ColorTier) PaintsSurface() bool { return t == Tier256 }
