// Package dock hosts a single interactive panel docked above the input area.
package dock

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/layout"
)

// Sizing is a panel's height-budget hint to the dock host.
type Sizing int

const (
	// Docked caps the panel at MaxRows (40% of the frame, floor 6), leaving
	// the transcript visible above.
	Docked Sizing = iota
	// FullFrame gives the panel the whole frame minus the status line; the
	// transcript is hidden while the panel is open.
	FullFrame
)

// statusLineRows aliases layout.StatusLineRows. The source of truth
// lives in internal/app/tui/layout; dock cannot import tui (tui imports
// dock) but the duplication is intentional and pinned by
// TestDockStatusLineRowsMatchesLayout in dock_test.go.
const statusLineRows = layout.StatusLineRows

// Panel is anything the dock can host.
type Panel interface {
	Update(msg tea.Msg) tea.Cmd
	View(width, maxHeight int) string
	// Sizing reports the panel's height-budget hint.
	Sizing() Sizing
}

// MaxRows is the dock's height budget: 40% of the frame height, floor 6.
func MaxRows(frameHeight int) int {
	return max(frameHeight*2/5, 6)
}

// Host owns the single dock slot.
type Host struct {
	panel Panel
	rows  int
}

// Open replaces the active panel.
func (h *Host) Open(panel Panel) { h.panel = panel }

// CloseNow clears the active panel and its rendered height.
func (h *Host) CloseNow() { h.panel, h.rows = nil, 0 }

// IsOpen reports whether the dock has an active panel.
func (h *Host) IsOpen() bool { return h.panel != nil }

// Panel returns the active panel, if any.
func (h *Host) Panel() Panel { return h.panel }

// Rows is the height of the last rendered view.
func (h *Host) Rows() int { return h.rows }

// Update delegates a message to the active panel.
func (h *Host) Update(msg tea.Msg) tea.Cmd {
	if h.panel == nil {
		return nil
	}
	return h.panel.Update(msg)
}

// FullFrameOpen reports whether the active panel requested the full frame.
func (h *Host) FullFrameOpen() bool {
	return h.panel != nil && h.panel.Sizing() == FullFrame
}

// View renders the active panel within the height budget its sizing hint
// requests.
func (h *Host) View(width, frameHeight int) string {
	if h.panel == nil {
		h.rows = 0
		return ""
	}

	budget := MaxRows(frameHeight)
	if h.panel.Sizing() == FullFrame {
		budget = max(frameHeight-statusLineRows, 1)
	}
	view := h.panel.View(width, budget)
	h.rows = lipgloss.Height(view)
	return view
}
