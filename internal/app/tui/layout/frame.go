package layout

// Rect is an immutable rectangle in terminal cells. X/Y are the top-left
// cell; Width/Height are extents. A zero Width or Height means the region
// is not rendered — see Empty.
//
// Rendering and pointer routing both consume these rectangles so a hit
// target can never disagree with the row it was drawn on.
type Rect struct {
	X, Y          int
	Width, Height int
}

// Empty reports whether the rectangle has no area. An empty rectangle is
// the canonical representation of "this region is not on screen".
func (r Rect) Empty() bool { return r.Width <= 0 || r.Height <= 0 }

// Right is the exclusive right edge (X + Width).
func (r Rect) Right() int { return r.X + r.Width }

// Bottom is the exclusive bottom edge (Y + Height).
func (r Rect) Bottom() int { return r.Y + r.Height }

// Contains reports whether the cell (x, y) lies inside the rectangle. The
// right and bottom edges are exclusive, matching the half-open row ranges
// the click regions use.
func (r Rect) Contains(x, y int) bool {
	if r.Empty() {
		return false
	}
	return x >= r.X && x < r.Right() && y >= r.Y && y < r.Bottom()
}

// Row returns the rectangle's row offset for a screen row y, and whether y
// is inside the rectangle at all. It is the bridge from a pointer event to
// a content line: callers add the result to their own scroll offset.
func (r Rect) Row(y int) (int, bool) {
	if r.Empty() || y < r.Y || y >= r.Bottom() {
		return 0, false
	}
	return y - r.Y, true
}

// Frame is the measured layout of one rendered frame. Every rectangle is
// in absolute screen cells, so a pointer handler can test a coordinate
// against the frame without recomputing the row stack.
//
// The zero Frame is valid and describes an empty screen: every region is
// Empty.
type Frame struct {
	// Width and Height are the frame's own bounds (the clamped terminal
	// size the layout was computed for).
	Width, Height int

	// TopBar is the full-width SDD run panel above both columns. Empty
	// when no run panel is rendered.
	TopBar Rect
	// Transcript is the conversation viewport, including the scroll hint
	// and drill-down breadcrumb rows that sit above the content.
	Transcript Rect
	// Inspector is the side rail / inspector column to the right of the
	// transcript. Empty when the inspector is not shown beside the body.
	Inspector Rect
	// Activity is the stacked auxiliary chrome below the transcript:
	// turn spinner, todo panel, live strip, and the consolidated lane.
	// Empty when none of them render.
	Activity Rect
	// Dock is the docked panel above the composer. Empty when no panel is
	// open.
	Dock Rect
	// Composer is the input area, including any decision panel that
	// replaces the textarea.
	Composer Rect
	// Footer is the persistent status line at the bottom of the frame.
	Footer Rect
}

// Body returns the rectangle above the composer and footer, spanning the
// full frame width. It is the region the transcript and inspector split.
func (f Frame) Body() Rect {
	top := f.TopBar.Bottom()
	bottom := f.Composer.Y
	if f.Composer.Empty() {
		bottom = f.Footer.Y
	}
	if bottom <= top {
		return Rect{}
	}
	return Rect{X: 0, Y: top, Width: f.Width, Height: bottom - top}
}
