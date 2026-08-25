package chrome

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// PaintBand renders s as a full-width band filled with bg.
//
// When bg is nil or a lipgloss.NoColor it returns s completely unchanged —
// not merely visually identical, but the same bytes. This is what makes
// tui.depth="flat" provably identical to the unpainted rendering: every
// region renderer can call PaintBand unconditionally and pay nothing for it.
// In particular it does not pad in that case, because padding to width would
// add trailing spaces and change the output even with no SGR emitted.
//
// When bg is a real colour, every line is truncated and then padded to
// exactly w cells. The padding is the point: a band whose lines are shorter
// than the region renders as a ragged stripe against the terminal
// background, which is the artifact recorded in chromeRailWidth's comment in
// view.go and the reason panels painted no background before depth existed.
//
// A trailing newline is preserved without becoming an extra painted row, so
// stacked regions keep their separation.
func PaintBand(s string, w int, bg color.Color) string {
	if bg == nil || w <= 0 {
		return s
	}
	if _, isNoColor := bg.(lipgloss.NoColor); isNoColor {
		return s
	}

	trailing := strings.HasSuffix(s, "\n")
	body := strings.TrimSuffix(s, "\n")

	style := lipgloss.NewStyle().Background(bg)
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		line = ansi.Truncate(line, w, "…")
		if gap := w - ansi.StringWidth(line); gap > 0 {
			line += strings.Repeat(" ", gap)
		}
		lines[i] = style.Render(line)
	}

	out := strings.Join(lines, "\n")
	if trailing {
		out += "\n"
	}
	return out
}
