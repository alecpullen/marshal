// Package dock hosts a single interactive panel docked above the input area.
package dock

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
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

// MessageOwner is the optional interface a Panel implements when it emits
// its own async result messages (an install finishing, a scan returning).
//
// The dock host forwards keypresses and pastes to the active panel
// unconditionally, but every other message keeps flowing to the main model's
// handlers so background work continues. A panel whose result types are
// unexported can therefore never be routed by the model — the message matches
// no case and is silently dropped, leaving the panel's own Update case dead
// code. OwnsMsg closes that gap: the panel claims its own messages by type and
// the host delivers them without the model needing to name them.
type MessageOwner interface {
	OwnsMsg(msg tea.Msg) bool
}

// CloseAware is notified when the host permanently releases a panel.
type CloseAware interface{ OnClose() }

// OwnsMsg reports whether the active panel claims msg as its own async
// result. False when no panel is open or the panel does not implement
// MessageOwner.
func (h *Host) OwnsMsg(msg tea.Msg) bool {
	owner, ok := h.panel.(MessageOwner)
	return ok && owner.OwnsMsg(msg)
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

// Open replaces the active panel. The displaced panel is gone for good, so a
// CloseAware one is notified before the slot takes the new panel; a panel that
// must survive the exchange is exchanged with SwapRetained instead.
func (h *Host) Open(panel Panel) {
	notifyClose(h.panel)
	h.panel = panel
}

// CloseNow clears the active panel and its rendered height. The panel is gone
// for good, so a CloseAware one is notified once; a repeated call with nothing
// open is inert.
func (h *Host) CloseNow() {
	notifyClose(h.panel)
	h.panel, h.rows = nil, 0
}

// SwapRetained replaces the visible panel without closing either panel and
// returns the displaced panel to its owner for later restoration or release.
func (h *Host) SwapRetained(panel Panel) Panel {
	previous := h.panel
	h.panel = panel
	return previous
}

// ReleaseRetained closes a panel previously returned by SwapRetained.
func (h *Host) ReleaseRetained(panel Panel) {
	notifyClose(panel)
}

// notifyClose tells a CloseAware panel that the host will not show it again.
// Panels are interfaces and need not be comparable, so the notification is a
// type assertion and never an identity comparison.
func notifyClose(panel Panel) {
	if c, ok := panel.(CloseAware); ok {
		c.OnClose()
	}
}

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
//
// A FullFrame panel is drawn then closed: it owns every row the status line
// leaves, and a panel that draws an open frame gains a bottom border. A panel
// that already ends on its own bottom border (the settings and agents panels
// render closed) is not charged for that border, so it keeps the whole budget
// instead of leaving a blank row above the status line.
func (h *Host) View(width, frameHeight int) string {
	if h.panel == nil {
		h.rows = 0
		return ""
	}

	full := h.panel.Sizing() == FullFrame
	budget := MaxRows(frameHeight)
	if full {
		budget = max(frameHeight-statusLineRows, 1)
	}
	view := h.panel.View(width, budget)
	if full {
		view = chrome.Close(view, width, chrome.FocusColor(theme.Current()))
		if lipgloss.Height(view) > budget {
			// The panel drew an open frame, so Close added the row that ends
			// it. Re-render one row shorter: the closed frame then fits the
			// budget exactly rather than overflowing it.
			view = chrome.Close(h.panel.View(width, max(budget-1, 1)), width, chrome.FocusColor(theme.Current()))
		}
	}
	h.rows = lipgloss.Height(view)
	return view
}
