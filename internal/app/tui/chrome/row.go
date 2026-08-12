package chrome

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// StackThreshold is the label budget below which Row stacks its two columns
// onto separate lines instead of truncating the label. Below roughly this
// many cells a label is no longer identifiable, and the metadata is worth
// more than the line it costs.
const StackThreshold = 24

// Row lays out a two-column list row within inner cells.
//
// The right column is reserved before the label is sized. This is the whole
// point of the helper: composing the row first and letting the panel frame
// clip it destroys the right column, which is where badges and metadata
// live. Rows also then truncate at different points and stop scanning as a
// column.
//
// When the right column cannot fit alongside a usable label, parts are
// dropped in ascending order of value: the detail first and entirely — never
// a mid-token stub, which costs cells and conveys nothing — then the badge.
// When even a badge-only right column would starve the label, the row stacks
// onto two lines so nothing is lost at all.
//
// marker is rendered at the start of the first line and is never truncated;
// pass an equal-width blank for unselected rows so columns stay aligned.
// label, detail and badge may carry ANSI styling. The result may contain one
// newline, and no line exceeds inner cells.
func Row(marker, label, detail, badge string, inner int) string {
	avail := inner - ansi.StringWidth(marker)
	if avail < 1 {
		avail = 1
	}

	// Try progressively cheaper right columns before giving up on one line.
	for _, right := range []string{joinRight(detail, badge), badge} {
		if right == "" {
			continue
		}
		if line, ok := singleLine(marker, label, right, avail); ok {
			return line
		}
	}
	if detail != "" || badge != "" {
		return stacked(marker, label, joinRight(detail, badge), inner)
	}
	line, _ := singleLine(marker, label, "", avail)
	return line
}

// singleLine composes marker + label + gap + right, truncating the label to
// whatever the reserved right column leaves. ok is false when the remaining
// label budget falls below StackThreshold, meaning the caller should try a
// cheaper right column or stack. An empty right column always succeeds:
// there is nothing cheaper to fall back to.
func singleLine(marker, label, right string, avail int) (string, bool) {
	rightW := ansi.StringWidth(right)
	sep := 0
	if rightW > 0 {
		sep = 1
	}
	budget := avail - rightW - sep
	if right != "" && budget < StackThreshold {
		return "", false
	}
	if budget < 1 {
		budget = 1
	}
	shown := label
	if ansi.StringWidth(shown) > budget {
		shown = ansi.Truncate(shown, budget, "…")
	}
	gap := avail - ansi.StringWidth(shown) - rightW
	if gap < sep {
		gap = sep
	}
	return marker + shown + strings.Repeat(" ", gap) + right, true
}

// stacked renders the label on its own line with the right column indented
// beneath it, for widths where one line cannot carry both.
func stacked(marker, label, right string, inner int) string {
	markerW := ansi.StringWidth(marker)
	budget := inner - markerW
	if budget < 1 {
		budget = 1
	}
	shown := label
	if ansi.StringWidth(shown) > budget {
		shown = ansi.Truncate(shown, budget, "…")
	}
	indent := strings.Repeat(" ", markerW+2)
	rightBudget := inner - ansi.StringWidth(indent)
	if rightBudget < 1 {
		rightBudget = 1
	}
	if ansi.StringWidth(right) > rightBudget {
		right = ansi.Truncate(right, rightBudget, "…")
	}
	return marker + shown + "\n" + indent + right
}

// joinRight combines detail and badge with a single space, omitting the
// separator when either is empty.
func joinRight(detail, badge string) string {
	switch {
	case detail == "":
		return badge
	case badge == "":
		return detail
	default:
		return detail + " " + badge
	}
}
