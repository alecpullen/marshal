// internal/app/tui/inspector/window.go — the shared row window the cursor-driven
// tabs render their lists through.
//
// Three tabs (Changes, Agents, Context) render a LIST plus a cursor, and all
// three had the same defect: they emitted every row. The panel is joined into
// the frame as a second column, and lipgloss.JoinHorizontal pads the shorter
// column to the TALLER one, so a 200-file list inside a 60-row terminal produced
// a 204-row frame and pushed the status line and the composer off the bottom of
// the screen. The Overview tab windowed its document; these three did not.
//
// The helper here is deliberately in ONE place. Three copies of "which rows do I
// show" would drift, and the drift would show up as one tab that loses the
// reader's cursor and another that does not.
package inspector

import (
	"strconv"

	"github.com/charmbracelet/x/ansi"
)

// listWindow is the visible slice of a long list, plus the notes that say what
// was left out.
//
// The zero value is a valid empty window, which is what a panel with no room
// gets.
type listWindow struct {
	// Start and End are the [Start,End) row range to render.
	Start, End int
	// Total is the full row count, so a caller can render the notes without
	// recomputing it.
	Total int
	// Available is the row budget the window was computed against.
	Available int
}

// Rows reports how many list rows the window covers.
func (w listWindow) Rows() int { return max(w.End-w.Start, 0) }

// Above reports how many rows are hidden above the window.
func (w listWindow) Above() int { return max(w.Start, 0) }

// Below reports how many rows are hidden below the window.
func (w listWindow) Below() int { return max(w.Total-w.End, 0) }

// windowList computes the visible slice of a list so that:
//
//   - the cursor row is ALWAYS inside it (a window that loses the cursor makes
//     the down-key read as broken, which is worse than a long list), and
//   - at most avail rows are covered.
//
// The window is biased toward the requested scroll, so a reader who has walked
// the cursor down and then scrolls stays where they put themselves; it moves only
// when the cursor would otherwise fall outside.
//
// noteRows is the number of rows the caller plans to spend DISCLOSING what was
// hidden (one or two lines of "… N more"). They are deducted here rather than
// counted by the caller afterwards, because the budget has to add up BEFORE the
// window is chosen: reserving rows after the fact is how a panel ends up one row
// too tall, which is the whole defect.
func windowList(total, avail, cursor, scroll, noteRows int) listWindow {
	w := listWindow{Total: max(total, 0), Available: max(avail, 0)}
	if w.Total == 0 || w.Available == 0 {
		return w
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= w.Total {
		cursor = w.Total - 1
	}

	// The notes only exist when something is actually hidden, so they are
	// reserved only when they will be shown. A list that fits gets the whole
	// budget for its rows.
	rows := w.Available
	if w.Total > rows {
		rows = max(rows-min(max(noteRows, 0), rows-1), 1)
	}

	start := cursor - rows + 1
	if start < 0 {
		start = 0
	}
	if scroll > 0 && start < scroll {
		// Honour the reader's scroll when it does not push the cursor out.
		if scroll <= cursor {
			start = scroll
		}
	}
	if start+rows > w.Total {
		start = max(w.Total-rows, 0)
	}
	w.Start = start
	w.End = min(start+rows, w.Total)
	return w
}

// windowNote rows report what the window left out.
//
// A windowed list that does not say so reads as a complete list, and a reader
// who cannot see that there are more changed files will conclude there are none.
// The text is truncated to the panel width like every other row, because an
// over-wide row reflows and costs exactly the rows this window exists to save.
func aboveNote(hidden, width int) string {
	if hidden <= 0 {
		return ""
	}
	return clampToWidth("↑ "+pluralise(hidden, "more row"), width)
}

// belowNote is aboveNote's counterpart.
func belowNote(hidden, width int) string {
	if hidden <= 0 {
		return ""
	}
	return clampToWidth("↓ "+pluralise(hidden, "more row"), width)
}

// pluralise renders "3 more rows" / "1 more row".
func pluralise(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// clampToWidth bounds a line to the panel, ellipsising rather than wrapping.
func clampToWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Truncate(s, width, "…")
}
