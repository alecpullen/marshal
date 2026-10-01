package tui

import (
	"reflect"

	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
)

// dockMeasurement is the dock slot's measured height, shared by POINTER
// between the canonical model and the throwaway copy View renders on.
//
// The height can only be learned by rendering the panel — the dock host
// measures what the panel emits — and View has a value receiver, so a
// measurement stored directly on the model would be written to a copy and
// discarded. The frame's invariant ("rendering and pointer routing cannot
// disagree about a row") needs the canonical model to know the dock's height:
// without it dockRows() answers 0 for every open panel, the frame's Dock
// rectangle stays empty, and computeFrame measures a Transcript rectangle that
// extends over the rows where the dock is actually drawn — so a click there
// resolves to a transcript line that is not on screen.
//
// Sharing one struct through a pointer is what lets View record the true height
// for free (it already renders the panel) while Update reads it. The other
// fields record WHICH panel at which geometry the measurement describes, so
// Update re-measures only when one of those changed.
type dockMeasurement struct {
	rows  int
	valid bool
	// panel is the slot's occupant when the measurement was taken, width and
	// height the geometry it was taken at. A mismatch means the cached rows
	// describe a different panel or a different frame and must not be used for
	// pointer routing.
	panel  dock.Panel
	width  int
	height int
}

// samePanel reports whether two dock panels are the same object.
//
// It is guarded rather than a bare ==, because interface equality panics at
// runtime when the dynamic type is uncomparable (a panel holding a map or a
// func). A panel that cannot be compared reports "changed", which costs one
// extra measurement instead of a crash — the safe direction, since the extra
// measurement is exactly what an unknown panel needs.
func samePanel(a, b dock.Panel) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	if av.Type() != bv.Type() || !av.Comparable() {
		return false
	}
	return av.Interface() == bv.Interface()
}

// syncDock brings the canonical dock slot and its measurement up to date with
// the model's placement state.
//
// It is called from the Update paths that can change either — never from View,
// whose value receiver would discard the result. Callers:
//
//   - Update's entry, so a placement set by the message just handled is applied
//     before the NEXT message is routed against m.dock.IsOpen().
//   - resize, because a resize is what moves a side-placed inspector into the
//     dock and back, and the changed geometry invalidates the measurement.
//   - updateViewportHeight, the funnel nearly every other state change passes
//     through.
func (m *Model) syncDock() {
	m.syncDockPlacement()
	m.measureDock()
}

// syncDockPlacement applies the inspector's dock ownership to the canonical
// dock slot.
//
// The claim is idempotent: installInDock opens the slot only when it is empty
// and releases it only when the inspector's own adapter is the occupant, so a
// panel that already owns the slot — a settings browser, a cast list, the trust
// prompt — is never evicted. Calling it once per message is therefore safe as
// well as cheap.
func (m *Model) syncDockPlacement() {
	if m.inspector != nil {
		m.inspector.installInDock(&m.dock)
	}
}

// ensureDockMeasurement allocates the shared measurement if a model was built
// without one. New allocates it; a Model literal in a test does not.
func (m *Model) ensureDockMeasurement() {
	if m.dockMeas == nil {
		m.dockMeas = &dockMeasurement{}
	}
}

// recordDockRows records the height the dock was just rendered at.
//
// View calls it after rendering the panel, which is the one moment the true
// height is known. The write goes through the shared pointer, so it reaches the
// canonical model even though View holds a copy.
func (m Model) recordDockRows() {
	if m.dockMeas == nil {
		return
	}
	panel := m.dock.Panel()
	m.dockMeas.rows = m.dock.Rows()
	m.dockMeas.valid = true
	m.dockMeas.panel = panel
	m.dockMeas.width = m.leftWidth
	m.dockMeas.height = m.height
}

// measureDock renders the dock panel when the cached measurement no longer
// describes it, so the frame computed from it matches what will be drawn.
//
// The re-measure is deliberately narrow: rendering a panel is the expensive
// part of a frame (the inspector styles a whole tab body), and doing it on every
// message would pay that cost for a number that changes only when the slot or
// the geometry does. A content change inside the panel is covered for free by
// View's recordDockRows.
func (m *Model) measureDock() {
	m.ensureDockMeasurement()
	panel := m.dock.Panel()
	if m.dockMeas.valid && m.dockMeas.width == m.leftWidth && m.dockMeas.height == m.height &&
		samePanel(m.dockMeas.panel, panel) {
		return
	}
	if panel == nil {
		// The slot is free, so the panel's rows are gone with it. Recording it
		// here rather than trusting CloseNow matters because the release paths
		// are many (a panel's own CloseMsg, an eviction by another panel) and
		// the frame must not keep a stale dock height after any of them.
		m.dockMeas.rows, m.dockMeas.valid, m.dockMeas.panel = 0, true, nil
		m.dockMeas.width, m.dockMeas.height = m.leftWidth, m.height
		return
	}
	m.dock.View(m.leftWidth, m.height)
	m.recordDockRows()
}

// dockRect is the measured rectangle of the docked panel, empty when no panel
// is open. computeFrame reads it so the Dock rectangle and the Transcript
// rectangle never overlap.
func (m Model) dockRect(body layout.Rect) layout.Rect {
	rows := min(m.dockRows(), max(body.Height-1, 0))
	if rows <= 0 {
		return layout.Rect{}
	}
	return layout.Rect{
		X:      0,
		Y:      body.Bottom() - rows,
		Width:  body.Width,
		Height: rows,
	}
}

// dockHeight is the dock's rendered height. It is the number the frame's Dock
// rectangle is built from, exposed so a test can compare the rectangle against
// what was actually drawn.
func (m Model) dockHeight() int {
	if m.dockMeas != nil && m.dockMeas.valid {
		return m.dockMeas.rows
	}
	return m.dock.Rows()
}

// computeFrame measures the current frame and stores it on the model. It is
// the single place that decides where every region of the screen lives, so
// rendering and pointer routing cannot disagree about a row.
//
// It must be called before any pointer event is handled and before the
// frame is rendered: View has a value receiver, so a measurement taken
// there would be discarded and the next click would route against a stale
// layout.
//
// The measurement is derived from the same *Rows() helpers the renderers
// use, so a region is non-empty exactly when its renderer emits something.
func (m *Model) computeFrame() {
	f := layout.Frame{Width: m.width, Height: m.height}
	if m.width == 0 || m.height == 0 {
		m.frame = f
		return
	}

	// The SDD run panel is a full-width top bar above both columns.
	if rows := m.runPanelRows(); rows > 0 {
		f.TopBar = layout.Rect{X: 0, Y: 0, Width: m.width, Height: rows}
	}

	// The footer is the last row of the frame; the composer sits directly
	// above it. Both are full-width and always present.
	f.Footer = layout.Rect{X: 0, Y: max(m.height-statusLineRows, 0), Width: m.width, Height: statusLineRows}
	composerRows := max(m.inputAreaRows(), 1)
	f.Composer = layout.Rect{
		X:      0,
		Y:      max(f.Footer.Y-composerRows, f.TopBar.Bottom()),
		Width:  m.width,
		Height: composerRows,
	}

	// The body is everything between the top bar and the composer. The
	// transcript and the inspector split it horizontally; the auxiliary
	// activity chrome stacks at the bottom of the transcript column.
	body := f.Body()
	if body.Empty() {
		m.frame = f
		return
	}

	transcriptWidth := m.leftWidth
	if transcriptWidth <= 0 || transcriptWidth > body.Width {
		transcriptWidth = body.Width
	}
	if m.railEnabled() {
		railWidth := min(m.railWidth, max(body.Width-transcriptWidth, 0))
		if railWidth > 0 {
			f.Inspector = layout.Rect{
				X:      transcriptWidth,
				Y:      body.Y,
				Width:  railWidth,
				Height: body.Height,
			}
		}
	}

	// The docked panel sits directly above the composer, inside the body. Its
	// height comes from the measurement the renderer recorded (see
	// dockMeasurement), so the rectangle describes the rows the panel actually
	// occupies rather than a number only the render path knows.
	f.Dock = m.dockRect(body)

	// The transcript column runs from the top of the body down to the dock.
	transcriptBottom := body.Bottom()
	if !f.Dock.Empty() {
		transcriptBottom = f.Dock.Y
	}
	transcriptHeight := max(transcriptBottom-body.Y, 0)
	if transcriptHeight > 0 {
		f.Transcript = layout.Rect{
			X:      0,
			Y:      body.Y,
			Width:  transcriptWidth,
			Height: transcriptHeight,
		}
	}

	// The activity chrome (spinner, todos, live strip, lane) stacks at the
	// bottom of the transcript column, directly above the dock.
	activityRows := m.turnSpinnerRows() + m.notebookActivityRows() + m.todoPanelRows() + m.liveStripRows() + m.laneRows()
	activityRows = min(activityRows, transcriptHeight)
	if activityRows > 0 {
		f.Activity = layout.Rect{
			X:      0,
			Y:      transcriptBottom - activityRows,
			Width:  transcriptWidth,
			Height: activityRows,
		}
	}

	m.frame = f
}

// frameRect returns the measured rectangle for a region, computing the
// frame first when it has not been measured yet. Tests and pointer
// handlers use it so they never observe a zero frame.
func (m *Model) frameRect() layout.Frame {
	if m.frame.Width == 0 && m.width > 0 {
		m.computeFrame()
	}
	return m.frame
}
