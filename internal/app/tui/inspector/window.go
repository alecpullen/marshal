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
	"strings"

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
	// aboveShown and belowShown record whether the caller may spend a row on
	// the note disclosing hidden rows above or below. They are decided HERE
	// rather than left to the caller, because the notes come out of the SAME
	// budget as the rows: with a two-row budget and the cursor mid-list there
	// is no arrangement that fits both notes and the cursor's own row, so one
	// note is dropped rather than the frame overflowed.
	aboveShown, belowShown bool
}

// Rows reports how many list rows the window covers.
func (w listWindow) Rows() int { return max(w.End-w.Start, 0) }

// ShowAbove reports whether the above note may be emitted inside the budget.
func (w listWindow) ShowAbove() bool { return w.aboveShown }

// ShowBelow reports whether the below note may be emitted inside the budget.
func (w listWindow) ShowBelow() bool { return w.belowShown }

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
	noteCap := min(max(noteRows, 0), 2)
	rows := w.Available
	if w.Total > rows {
		rows = max(rows-min(noteCap, rows-1), 1)
	}
	w.Start, w.End = windowRange(w.Total, rows, cursor, scroll)

	// The notes the caller renders come out of the SAME budget as the rows, so
	// they are allowed only up to what the window left over. The reservation
	// above cannot express this on its own: it deducts a fixed noteRows, while
	// the notes that will actually appear depend on which sides the window
	// turned out to hide. At a two-row budget with the cursor mid-list, both
	// notes plus the cursor row is three rows for two — so one note is dropped
	// rather than the frame overflowed, because a row past the frame hides the
	// panel's chrome and costs the reader more than the missing disclosure.
	allow := w.Available - w.Rows()
	if w.Start > 0 && allow > 0 {
		w.aboveShown = true
		allow--
	}
	if w.End < w.Total && allow > 0 {
		w.belowShown = true
	}
	return w
}

// windowRange picks the [start,end) rows for a window of rows rows that keeps
// cursor inside it and honours scroll when that does not push the cursor out.
func windowRange(total, rows, cursor, scroll int) (start, end int) {
	start = cursor - rows + 1
	if start < 0 {
		start = 0
	}
	if scroll > 0 && start < scroll {
		// Honour the reader's scroll when it does not push the cursor out.
		if scroll <= cursor {
			start = scroll
		}
	}
	if start+rows > total {
		start = max(total-rows, 0)
	}
	end = min(start+rows, total)
	return start, end
}

// rowBudget writes rows while a row budget lasts, so a panel's emission can
// never exceed the height it was given — nor its rows be wider than the panel
// it is joined into.
//
// The three list tabs each lay out a header, a windowed list, trailing notes
// and a detail body. Budgeting that by hand is exactly where a blank line
// before the body — or a header that wrapped to more rows than its author
// counted — goes uncounted, and the surplus escapes into the column the panel
// is joined into (clipLeftColumn hides it, so the reader loses content with no
// sign that anything was dropped). Writing through this type makes the total
// an invariant rather than an arithmetic hope.
//
// The WIDTH bound is here for the same reason as the height one. Five of the
// chrome rows these tabs draw are composed from author-controlled or fixed text
// that was never clamped — the Changes header carries the base ref, and the
// vanished/loading notes are longer than the product's minimum panel width at
// 30 columns. An unclamped row WRAPS, and a wrapped row is a second display row
// the height budget never counted, inside a slot it budgeted for one: the note
// reflows everything below it and the panel overflows vertically for a reason
// that looks like it has nothing to do with height. Clamping here rather than
// at each call site is what makes that impossible to forget for the next row
// somebody adds.
type rowBudget struct {
	b     strings.Builder
	limit int
	// width is the panel width every row is clamped to, in display cells.
	width int
	used  int
}

// minBudgetWidth is the width the row budget clamps to when the panel has not
// recorded a usable one.
//
// A panel that has measured its HEIGHT but not its width still has to bound its
// rows: the emission is being bounded, so "unmeasured" cannot mean "unbounded"
// — that is how Resize(0, 24) produced rows past the frame. The floor is the
// same 20 columns the Agents and Context tabs already assume when they render
// their chrome (max(m.width, 20)), so the budget and the tabs agree about what
// a degenerate panel is.
const minBudgetWidth = 20

// newRowBudget returns a budget that emits at most limit rows, each clamped to
// width display cells.
func newRowBudget(limit, width int) *rowBudget {
	return &rowBudget{limit: max(limit, 0), width: budgetWidth(width)}
}

// budgetWidth is the width a budgeted panel bounds its rows to: the recorded
// panel width, or minBudgetWidth when nothing usable has been recorded.
//
// It is exposed because the budget is not the only writer of a row: the
// window's "N more rows" notes are COMPOSED before they are handed over, and a
// note composed against an unmeasured width would collapse to a single ellipsis
// cell — the reader would lose the disclosure that the list is windowed. Every
// row of a budgeted panel is therefore measured against this one number.
func budgetWidth(width int) int { return max(width, minBudgetWidth) }

// line writes s as one row, reporting false when the budget is spent.
//
// The row is clamped to the panel width here, so a caller cannot spend a row
// that is wider than the frame — a wrapped row costs rows the height budget has
// already promised elsewhere.
func (r *rowBudget) line(s string) bool {
	if r.used >= r.limit {
		return false
	}
	r.b.WriteString(clampToWidth(s, r.width))
	r.b.WriteString("\n")
	r.used++
	return true
}

// blank writes an empty row.
func (r *rowBudget) blank() bool { return r.line("") }

// left reports how many of the budget's rows are unspent.
func (r *rowBudget) left() int { return max(r.limit-r.used, 0) }

// body writes an already-rendered multi-row block, clipping it to what is
// left. It is the safety net under a sub-render that was handed its own
// height: the detail view budgets itself exactly, and this makes an error in
// that arithmetic cost content rather than a row past the frame.
func (r *rowBudget) body(s string) {
	if s == "" {
		return
	}
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if !r.line(line) {
			return
		}
	}
}

// String returns the rows written so far.
func (r *rowBudget) String() string { return r.b.String() }

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
//
// An UNMEASURED width does NOT pass the line through unbounded. A bound that
// silently disappears is the failure this helper exists to prevent: the callers
// that genuinely mean "nothing has been measured, so render everything"
// (changeRowText, agentRowText, clampLines) each say so AT THE CALL SITE, where
// the exception is visible next to the reason for it. Reaching this function
// therefore means the caller wants a bound, and the only honest bound available
// with no width is the narrowest one that is still a bound.
func clampToWidth(s string, width int) string {
	if width <= 0 {
		return ansi.Truncate(s, 1, "…")
	}
	return ansi.Truncate(s, width, "…")
}
