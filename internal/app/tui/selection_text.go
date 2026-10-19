package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
)

// maxGutterCols bounds how far into a row the gutter scan looks. The deepest
// gutter the transcript draws is a tool body's rail at column 7.
const maxGutterCols = 12

// selPos is a position in transcript content: a content line and a display
// column.
type selPos struct{ line, col int }

// before reports whether p comes before q in reading order.
func (p selPos) before(q selPos) bool {
	return p.line < q.line || (p.line == q.line && p.col < q.col)
}

// selRow is one content line as the copier sees it.
type selRow struct {
	// text is the line with ANSI escapes stripped.
	text string
	// segment groups rows whose leading whitespace is dedented together: one
	// per sub-region, one per block outside its sub-regions, -1 for the blank
	// line between blocks.
	segment int
	// metaHead marks a row that may end in right-aligned meta: the first
	// glyph row of a step block, and the first row of every sub-region.
	metaHead bool
	// inStep marks a row of a step block, where a deeply indented row that
	// ends at the frame edge is the step's meta on a row of its own.
	inStep bool
	// full is the untruncated text of a collapsed tool row, or "".
	full string
}

// rowSpan is the copyable part of a row in display columns.
type rowSpan struct {
	// start is where copied text begins: after the gutter glyph and its
	// space, or 0 on a bare row, whose indentation the dedent removes.
	start int
	// text is the first column of text, where the highlight begins.
	text int
	// end is one past the last copyable column.
	end int
	// bare reports a row with no gutter glyph.
	bare bool
}

// spinnerFrames is the spinner's frame set, hoisted so isGutterGlyph does
// not rebuild it for every row it scans during a drag.
var spinnerFrames = NewSpinner().frames

// isGutterGlyph reports whether s is a glyph the transcript draws in a
// gutter, including the spinner's frames.
func isGutterGlyph(s string) bool {
	switch s {
	case glyph.Rail, glyph.User, glyph.Running, glyph.Ambient, glyph.OK,
		glyph.Error, glyph.Warning, glyph.Question, glyph.Thinking, glyph.Edit,
		glyph.File, glyph.Shell, glyph.Search, glyph.Agent, glyph.Web,
		glyph.Brand, glyph.CustomAgent, glyph.Job, glyph.Watch:
		return true
	}
	return slices.Contains(spinnerFrames, s)
}

// spanOf finds the copyable span of one row rendered at width.
func spanOf(r selRow, width int) rowSpan {
	plain := strings.TrimRight(r.text, " ")
	if plain == "" {
		return rowSpan{}
	}
	runes := []rune(plain)
	i, col := 0, 0
	for i < len(runes) && runes[i] == ' ' {
		i++
		col++
	}
	sp := rowSpan{bare: true}
	if i < len(runes) && col < maxGutterCols && isGutterGlyph(string(runes[i])) &&
		(i+1 == len(runes) || runes[i+1] == ' ') {
		col += ansi.StringWidth(string(runes[i]))
		i++
		if i < len(runes) {
			i++ // the space after the glyph
			col++
		}
		sp.bare = false
		sp.start = col
	}
	sp.text = col
	for _, rr := range runes[i:] {
		if rr != ' ' {
			break
		}
		sp.text++
	}
	full := ansi.StringWidth(plain)
	sp.end = full
	switch {
	case strings.HasSuffix(plain, " "+glyph.DisclosureCollapsed),
		strings.HasSuffix(plain, " "+glyph.DisclosureExpanded):
		sp.end = full - 2
	case full == width && r.inStep && sp.bare && sp.text >= maxGutterCols:
		// The step's meta on a row of its own: decoration only.
		return rowSpan{}
	case full == width && r.metaHead && !strings.HasSuffix(plain, "…"):
		// Right-aligned meta: the segment after the last gap of two or
		// more spaces, when that gap sits inside the text.
		if gap := strings.LastIndex(plain, "  "); gap >= 0 {
			if gapCol := ansi.StringWidth(plain[:gap]); gapCol > sp.text {
				sp.end = gapCol
			}
		}
	}
	if sp.end < sp.text {
		sp.end = sp.text
	}
	return sp
}

// copyPiece is one row's contribution to copied text.
type copyPiece struct {
	text    string
	segment int
	bare    bool
	// dedent marks a piece that starts at its row's copy start, so its
	// leading whitespace is indentation the dedent may remove.
	dedent bool
	// drop marks a row that is decoration only (a rule or a meta row): it
	// contributes no line at all.
	drop bool
	// gap marks a row with nothing on it: layout spacing, not content.
	gap bool
}

// selectionText returns the cleaned text the selection from..to covers,
// inclusive of the cell at to. rows are the transcript's content lines and
// width is the width they were rendered at.
func selectionText(rows []selRow, from, to selPos, width int) string {
	if to.before(from) {
		from, to = to, from
	}
	var pieces []copyPiece
	for l := max(from.line, 0); l <= to.line && l < len(rows); l++ {
		r := rows[l]
		sp := spanOf(r, width)
		lo, hi := sp.start, sp.end
		if l == from.line {
			lo = max(lo, from.col)
		}
		if l == to.line {
			hi = min(hi, to.col+1)
		}
		p := copyPiece{segment: r.segment, bare: sp.bare, gap: strings.TrimSpace(r.text) == ""}
		if sp.end == 0 && !p.gap {
			p.drop = true // a meta row: nothing in it is copyable
		}
		if lo < hi {
			stripped := strings.TrimRight(r.text, " ")
			p.text = strings.TrimRight(ansi.Cut(stripped, lo, hi), " ")
			p.dedent = lo == sp.start
			if r.full != "" && lo <= sp.text && hi >= sp.end {
				p.text, p.dedent = r.full, false
			}
			if t, ok := trimRules(p.text); ok {
				p.text, p.drop = t, t == ""
			}
		} else if !p.drop && !p.gap {
			// The selection's columns do not reach this row's copyable
			// span — a drag endpoint past a short row's end, or a
			// rail-only continuation row. No text in it is copied, but
			// the screen still shows a line break there, so it counts
			// as layout, like a blank row: one break, collapsed when
			// the neighbouring lines are already blank.
			p.gap = true
		}
		pieces = append(pieces, p)
	}
	dedentPieces(pieces)
	var out []string
	for _, p := range pieces {
		if p.drop {
			continue
		}
		// Layout spacing adds nothing after a line that is already blank.
		if p.gap && len(out) > 0 && out[len(out)-1] == "" {
			continue
		}
		out = append(out, p.text)
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// trimRules drops box-drawing rules at either end of a row's text, so a
// task divider copies as its label and a turn separator as nothing. ok is
// false, and s is returned unchanged, for a row with no rule.
func trimRules(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "─") && !strings.HasSuffix(t, "─") {
		return s, false
	}
	return strings.TrimSpace(strings.Trim(t, "─")), true
}

// dedentPieces removes, per segment and per kind (bare or glyph rows), the
// leading whitespace every dedentable piece shares. A bare row's
// indentation is mostly hanging indent; a glyph row's is content, so the two
// kinds are measured separately.
func dedentPieces(pieces []copyPiece) {
	type key struct {
		segment int
		bare    bool
	}
	common := map[key]int{}
	for _, p := range pieces {
		if !p.dedent || strings.TrimSpace(p.text) == "" {
			continue
		}
		k := key{p.segment, p.bare}
		n := len(p.text) - len(strings.TrimLeft(p.text, " "))
		if cur, ok := common[k]; !ok || n < cur {
			common[k] = n
		}
	}
	for i, p := range pieces {
		if !p.dedent {
			continue
		}
		if n, ok := common[key{p.segment, p.bare}]; ok && len(p.text) >= n {
			pieces[i].text = p.text[n:]
		}
	}
}
