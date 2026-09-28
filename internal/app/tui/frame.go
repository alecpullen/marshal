package tui

import (
	"marshal/internal/app/tui/layout"
)

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

	// The docked panel sits directly above the composer, inside the body.
	dockRows := min(m.dockRows(), max(body.Height-1, 0))
	if dockRows > 0 {
		f.Dock = layout.Rect{
			X:      0,
			Y:      body.Bottom() - dockRows,
			Width:  body.Width,
			Height: dockRows,
		}
	}

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
	activityRows := m.turnSpinnerRows() + m.todoPanelRows() + m.liveStripRows() + m.laneRows()
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
