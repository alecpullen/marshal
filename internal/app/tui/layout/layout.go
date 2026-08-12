// Package layout owns responsive breakpoints and width policy for the TUI,
// so panels share one definition instead of hand-rolling divergent caps.
package layout

// StatusLineRows is the single row of persistent chrome at the bottom of
// the TUI. The dock package duplicates this value because tui imports
// dock but dock cannot import tui; the duplication is intentional and the
// shared value here is the source of truth. If StatusLineRows ever
// changes, run `go test ./internal/app/tui/layout -v` to confirm the
// dock/dock_test guard still passes.
const StatusLineRows = 1

// WideBreakpoint is the panel-interior width in columns at which
// list-shaped panels split into a list pane and a right-hand detail pane.
const WideBreakpoint = 100

// PanelWidth returns the width a dock panel renders at: it fills the width
// the dock gives it minus the standard 2-cell right margin, preferring a
// 30-cell floor.
//
// The floor is clamped to the dock width. A floor larger than its container
// is not a floor, it is an overflow: the panel would render past the dock
// edge and corrupt the frame around it. Unreachable while the TUI gates
// below minTerminalWidth, but the clamp costs nothing and removes the trap.
func PanelWidth(dockWidth int) int {
	w := max(dockWidth-2, 30)
	if w > dockWidth && dockWidth > 0 {
		w = dockWidth
	}
	return max(w, 1)
}

// TwoColumn reports whether a panel interior of the given width uses the
// list+detail two-column layout.
func TwoColumn(innerWidth int) bool {
	return innerWidth >= WideBreakpoint
}

// SplitPanes divides a panel interior width into list and detail pane
// widths separated by a 2-cell gap. The list pane takes half.
func SplitPanes(innerWidth int) (list, detail int) {
	list = innerWidth / 2
	detail = innerWidth - list - 2
	if detail < 1 {
		detail = 1
	}
	return list, detail
}
