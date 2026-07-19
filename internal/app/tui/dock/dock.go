// Package dock hosts a single interactive panel docked above the input area.
package dock

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Panel is anything the dock can host.
type Panel interface {
	Update(msg tea.Msg) tea.Cmd
	View(width, maxHeight int) string
}

// CloseMsg asks the model to close the dock.
type CloseMsg struct{}

// Close is a convenience message constructor for panels.
func Close() tea.Msg { return CloseMsg{} }

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

// View renders the active panel within the dock height budget.
func (h *Host) View(width, frameHeight int) string {
	if h.panel == nil {
		h.rows = 0
		return ""
	}

	view := h.panel.View(width, MaxRows(frameHeight))
	h.rows = lipgloss.Height(view)
	return view
}
