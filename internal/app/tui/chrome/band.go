package chrome

import (
	"image/color"
	"strconv"
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
//
// Styled content closes every segment with a full reset, which would end the
// band at the first segment's end and leave the rest of the line, padding
// included, on the terminal background. The band's background is re-asserted
// after each sequence that clears it (keepBackground).
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
	set := ansi.NewStyle().BackgroundColor(bg).String()
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		line = ansi.Truncate(line, w, "…")
		if gap := w - ansi.StringWidth(line); gap > 0 {
			line += strings.Repeat(" ", gap)
		}
		lines[i] = style.Render(keepBackground(line, set))
	}

	out := strings.Join(lines, "\n")
	if trailing {
		out += "\n"
	}
	return out
}

// keepBackground writes set after every SGR sequence in line that leaves the
// background at the terminal default. A sequence that sets its own background
// is left alone, so an explicit highlight inside the band still wins.
func keepBackground(line, set string) string {
	if !strings.Contains(line, "\x1b[") {
		return line
	}
	var b strings.Builder
	b.Grow(len(line) + len(set)*4)
	var state byte
	for len(line) > 0 {
		seq, _, n, next := ansi.DecodeSequence(line, state, nil)
		b.WriteString(seq)
		if clearsBackground(seq) {
			b.WriteString(set)
		}
		state, line = next, line[n:]
	}
	return b.String()
}

// clearsBackground reports whether seq is an SGR sequence whose last
// background change is a reset: a bare or 0 reset, or 49. The arguments of an
// extended colour (38/48/58;5;n and ;2;r;g;b) are skipped, so a foreground
// colour 49 is not mistaken for a background reset.
func clearsBackground(seq string) bool {
	if len(seq) < 3 || !strings.HasPrefix(seq, "\x1b[") || seq[len(seq)-1] != 'm' {
		return false
	}
	params := strings.Split(seq[2:len(seq)-1], ";")
	cleared := false
	for i := 0; i < len(params); i++ {
		p := params[i]
		if head, _, colon := strings.Cut(p, ":"); colon {
			// The colon form carries its colour arguments in the same field.
			if head == "48" {
				cleared = false
			}
			continue
		}
		switch n, _ := strconv.Atoi(p); {
		case n == 0 || n == 49:
			cleared = true
		case n == 38 || n == 48 || n == 58:
			if n == 48 {
				cleared = false
			}
			if i+1 < len(params) {
				switch params[i+1] {
				case "5":
					i += 2
				case "2":
					i += 4
				}
			}
		case n >= 40 && n <= 47, n >= 100 && n <= 107:
			cleared = false
		}
	}
	return cleared
}
