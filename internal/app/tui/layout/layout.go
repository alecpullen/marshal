// Package layout owns responsive breakpoints and width policy for the TUI,
// so panels share one definition instead of hand-rolling divergent caps.
package layout

// WideBreakpoint is the panel-interior width in columns at which
// list-shaped panels split into a list pane and a right-hand detail pane.
const WideBreakpoint = 100

// PanelWidth returns the width a dock panel renders at: it fills the width
// the dock gives it minus the standard 2-cell right margin, floored at 30.
func PanelWidth(dockWidth int) int {
	return max(dockWidth-2, 30)
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
