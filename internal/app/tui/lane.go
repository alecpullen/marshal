package tui

import (
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/theme"
)

// paintLane paints a rendered strip (the now bar) as a full-width band.
func paintLane(s string, leftWidth int) string {
	return chrome.PaintBand(s, leftWidth, theme.Current().ChromeBG())
}
