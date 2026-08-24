// Package liveregion renders a transcript block whose height is bounded.
//
// Every other transcript renderer emits as many rows as its content needs.
// For a block whose content streams — a thinking trace, a running
// subagent's tail — that makes height a function of the text, so it changes
// on every token and the transcript slides under the reader.
//
// A region wraps its body to the content width FIRST and windows the
// resulting display rows SECOND. Row count is therefore a function of
// MaxRows alone. A region grows until it reaches MaxRows and then stays
// there permanently, scrolling its body window instead.
//
// This is a leaf package: it imports only theme and glyph, so both tui and
// its sub-packages can consume it without an import cycle.
package liveregion

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

const (
	// GutterWidth mirrors the tui package's gutter contract (" X "). tui
	// imports liveregion, so the constant cannot be imported back; a guard
	// test in the tui package pins the two together, following the
	// precedent dock sets for layout.StatusLineRows.
	GutterWidth = 3

	// wrapBreakpoints mirrors tui.WrapBreakpoints, for the same reason.
	wrapBreakpoints = "/-:"

	// SubagentRows is the cap for a dispatched agent's region: header +
	// meta + up to 3 body rows + footer.
	SubagentRows = 6
	// ThinkingRows is the cap for a reasoning region: header + up to 3
	// body rows, matching the 3-line tail the thinking box showed before.
	ThinkingRows = 4
)

// Spec describes one bounded region.
type Spec struct {
	Glyph      string      // gutter glyph; the spinner frame while live
	GlyphColor color.Color // gutter glyph colour
	Title      string      // "reviewer" / "thinking"
	Right      string      // right-aligned in the header row: elapsed
	Meta       string      // second row, dim; "" omits the row
	Body       []string    // logical lines; wrapped internally
	Footer     string      // e.g. "ctrl+f to drill in"; "" omits the row
	MaxRows    int         // total cap, header/meta/footer included
	// MinRows is the region's high-water mark: the tallest it has already
	// rendered. Render pads to it so a body that shrinks does not shrink
	// the region.
	//
	// It exists because Render is pure and cannot remember how tall it has
	// been, while the body genuinely shrinks — SubagentActivityTail
	// switches between streamed reasoning and audit summaries, which have
	// different line counts. The Model owns the mark and passes it back.
	MinRows int
	Offset  int  // display rows scrolled back from the tail
	Live    bool // drives the tint
	Width   int  // full frame width
}

func contentWidth(w int) int { return max(w-GutterWidth, 1) }

// chromeRows counts the non-body rows: the header is always present, meta
// and footer only when set.
func chromeRows(s Spec) int {
	n := 1
	if s.Meta != "" {
		n++
	}
	if s.Footer != "" {
		n++
	}
	return n
}

// bodyRows wraps every logical body line to the content width and returns
// the resulting display rows. Wrapping before windowing is the whole point
// of this package.
func bodyRows(s Spec) []string {
	cw := contentWidth(s.Width)
	out := make([]string, 0, len(s.Body))
	for _, line := range s.Body {
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(ansi.Wrap(line, cw, wrapBreakpoints), "\n")...)
	}
	return out
}

// bodyBudget is how many body rows fit under the cap. A cap smaller than
// the chrome still renders the chrome — the caller set a nonsensical cap,
// and dropping the header would lose the region's identity entirely.
func bodyBudget(s Spec) int {
	return max(s.MaxRows-chromeRows(s), 0)
}

// MaxOffset is the largest meaningful Offset for s: how many display rows
// can be scrolled back before the head of the body is reached.
func MaxOffset(s Spec) int {
	budget := bodyBudget(s)
	if budget <= 0 {
		return 0
	}
	return max(len(bodyRows(s))-budget, 0)
}

// Rows reports how many rows Render will emit for s. Callers budget height
// with this rather than counting newlines.
func Rows(s Spec) int {
	n := chromeRows(s) + min(len(bodyRows(s)), bodyBudget(s))
	if s.MinRows > n {
		n = s.MinRows
	}
	// The cap still wins. chromeRows is the floor: a MaxRows smaller than
	// the chrome still renders the chrome, since dropping the header would
	// lose the region's identity entirely.
	return min(n, max(s.MaxRows, chromeRows(s)))
}

// window returns the visible slice of rows: tail-anchored, offset rows back
// from the end, clamped at both ends.
func window(rows []string, budget, offset int) []string {
	if budget <= 0 || len(rows) == 0 {
		return nil
	}
	if len(rows) <= budget {
		return rows
	}
	offset = min(max(offset, 0), len(rows)-budget)
	end := len(rows) - offset
	return rows[end-budget : end]
}

// Render emits exactly Rows(s) rows, each exactly s.Width cells wide.
func Render(s Spec, th theme.Theme) string {
	if s.Width < 1 {
		return ""
	}
	cw := contentWidth(s.Width)

	// Every segment style derives from base, which carries the background
	// when painting. If only an outer wrapper carried it, the inner
	// foreground styles' resets would punch holes in the fill mid-row.
	base := lipgloss.NewStyle()
	if s.Live && th.Tier.PaintsSurface() {
		base = base.Background(th.BGSurface)
	}
	titleStyle := base.Foreground(th.AccentPrimary).Bold(true)
	mutedStyle := base.Foreground(th.FGMuted)
	glyphStyle := base.Foreground(s.GlyphColor)

	g := s.Glyph
	if g == "" {
		g = glyph.Running
	}

	var b strings.Builder
	writeRow := func(content string) {
		b.WriteString(base.Width(s.Width).MaxWidth(s.Width).Render(content))
		b.WriteString("\n")
	}

	// Header: gutter glyph, title left, Right pinned to the right edge.
	writeRow(glyphStyle.Render(" "+g+" ") + headerBody(s, cw, titleStyle, mutedStyle, base))

	rail := mutedStyle.Render(" " + glyph.Rail + " ")

	if s.Meta != "" {
		writeRow(rail + mutedStyle.Render(ansi.Truncate(s.Meta, cw, "…")))
	}

	visible := window(bodyRows(s), bodyBudget(s), s.Offset)
	// Pad to the high-water mark. Padding goes below the content so the
	// body stays top-aligned in its area and the region reads as one with
	// room left, rather than as content pinned oddly to the footer.
	for pad := Rows(s) - chromeRows(s) - len(visible); pad > 0; pad-- {
		visible = append(visible, "")
	}
	for _, line := range visible {
		writeRow(rail + mutedStyle.Render(line))
	}

	if s.Footer != "" {
		writeRow(rail + mutedStyle.Render(ansi.Truncate(s.Footer, cw, "…")))
	}
	return b.String()
}

// headerBody lays the title flush left and Right flush to the content
// width's right edge, so the elapsed timer occupies a fixed column and
// stops reflowing every segment behind it on each tick.
func headerBody(s Spec, cw int, titleStyle, rightStyle, base lipgloss.Style) string {
	rw := ansi.StringWidth(s.Right)
	avail := cw
	if rw > 0 {
		avail = max(cw-rw-1, 1)
	}
	title := ansi.Truncate(s.Title, avail, "…")
	out := titleStyle.Render(title)
	if rw == 0 {
		return out
	}
	gap := max(cw-ansi.StringWidth(title)-rw, 1)
	// The gap is styled with base so the fill stays continuous across it.
	return out + base.Render(strings.Repeat(" ", gap)) + rightStyle.Render(s.Right)
}
