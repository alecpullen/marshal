package sidepanel

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Shared row layout for the rail body. Sections that compose their own rows
// with ad-hoc format strings drift apart by a column or two — which is how
// CONTEXT's fill-bar percentage ended up one cell out of line with the
// composition percentages directly beneath it — and adjacent sections then
// stop reading as one table.
//
// Everything below is measured in cells with ansi.StringWidth so glyphs and
// styled values participate in the math correctly.

// railIndent is the leading space every rail body row carries, setting the
// body in from the divider gutter.
const railIndent = 1

// railRow lays out one body row: an optional marker glyph, a label that
// gives way under pressure, and a right column flush to the rail's inner
// edge at exactly width cells.
//
// The right column is reserved before the label is sized. That ordering is
// the whole point: sizing the label first and letting the row clip destroys
// the values, which is where a telemetry rail's information actually lives.
//
// label must be plain text — it is the only part that gets truncated, and
// cutting styled text would sever an escape sequence. marker and right may
// carry styling.
// railRow lays out one body row: an optional marker glyph, a label that
// gives way under pressure, and a right column flush to the rail's inner
// edge at exactly width cells.
//
// The right column is reserved before the label is sized. That ordering is
// the whole point: sizing the label first and letting the row clip destroys
// the values, which is where a telemetry rail's information actually lives.
//
// label must be plain text — it is the only part that gets truncated, and
// cutting styled text would sever an escape sequence. marker and right may
// carry styling; both are measured in cells, so their escapes cost nothing.
func railRow(marker, label, right string, width int) string {
	if width <= 0 {
		return ""
	}
	prefix := strings.Repeat(" ", railIndent)
	if marker != "" {
		prefix += marker + " "
	}
	used := ansi.StringWidth(prefix)

	rightW := ansi.StringWidth(right)
	// Reserve the right column and one separating space before sizing the
	// label, so a long label never eats the value.
	budget := width - used - rightW
	if rightW > 0 {
		budget--
	}
	if budget < 1 {
		budget = 1
	}
	if ansi.StringWidth(label) > budget {
		label = ansi.Truncate(label, budget, "…")
	}

	if right == "" {
		// No right column means no edge to align to; trailing padding would
		// only be spaces the frame adds anyway.
		return prefix + label
	}
	gap := width - used - ansi.StringWidth(label) - rightW
	if gap < 1 {
		gap = 1
	}
	return prefix + label + strings.Repeat(" ", gap) + right
}

// railBudget is the label budget railRow will allow for a given right
// column, so callers can shorten a path to exactly what will survive.
func railBudget(marker, right string, width int) int {
	used := railIndent
	if marker != "" {
		used += ansi.StringWidth(marker) + 1
	}
	budget := width - used - ansi.StringWidth(right)
	if right != "" {
		budget--
	}
	return max(budget, 1)
}

// shortenPath trims a path from the left to fit budget cells, cutting at a
// separator so what survives is whole path segments rather than the tail of
// a directory name. "…el/section_context.go" reads as noise;
// "…/sidepanel/section_context.go" reads as a location.
//
// The rail shows file paths in two sections and they must shorten the same
// way, or the same file appears under two different names one block apart.
func shortenPath(path string, budget int) string {
	if budget < 1 {
		return ""
	}
	if ansi.StringWidth(path) <= budget {
		return path
	}
	segs := strings.Split(path, "/")
	for i := 1; i < len(segs); i++ {
		if cand := "…/" + strings.Join(segs[i:], "/"); ansi.StringWidth(cand) <= budget {
			return cand
		}
	}
	// Even the basename alone overflows, so there is no segment boundary
	// left to cut at; trim its head and keep the extension in view.
	base := segs[len(segs)-1]
	return ansi.TruncateLeft(base, ansi.StringWidth(base)-budget+1, "…")
}
