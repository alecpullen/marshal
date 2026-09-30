package tui

import (
	tea "charm.land/bubbletea/v2"
)

// MouseOverride is the session-scoped Ctrl+S state. Mouse capture and native
// click-drag selection are mutually exclusive — the terminal delivers mouse
// events to exactly one of them — so the only truthful way to expose the
// choice is an explicit three-state override resolved against config.
//
// A plain bool cannot express it. With tui.mouse_capture = false, "released"
// and "inherit" are genuinely different states, and toggling a bool flipped
// from false reported "released to the terminal" while the mouse had never
// been captured: a success message for an operation that changed nothing.
type MouseOverride int

const (
	// MouseInherit follows the configured default. A new session starts
	// here, and this is the state a config reload is allowed to move:
	// nothing explicit is being overridden.
	MouseInherit MouseOverride = iota
	// MouseCapture forces capture on regardless of config.
	MouseCapture
	// MouseRelease forces capture off regardless of config.
	MouseRelease
)

// effectiveMouseCapture resolves the override against the configured
// default. Everything that needs the answer — the tea.View mouse mode, the
// footer hint, and the Ctrl+S toggle itself — goes through here, so no two of
// them can disagree about whether the mouse is captured.
func (m Model) effectiveMouseCapture() bool {
	switch m.mouseOverride {
	case MouseCapture:
		return true
	case MouseRelease:
		return false
	default:
		return m.state != nil && m.state.Config.TUI.MouseCapture
	}
}

// mouseMode is the Bubble Tea view mode for the resolved capture state.
func (m Model) mouseMode() tea.MouseMode {
	if m.effectiveMouseCapture() {
		return tea.MouseModeCellMotion
	}
	return tea.MouseModeNone
}

// toggleMouseCapture flips the resolved state to its opposite and pins the
// result as an explicit session override. Toggling from the *effective* state
// is what makes the key truthful from either configured default: with capture
// configured off, the first press captures rather than announcing a release
// that already held.
//
// Feedback is a transient toast, never a transcript message: a UI mode change
// is not conversation content, and Ctrl+S used to append a system message
// every time it toggled.
func (m *Model) toggleMouseCapture() (Model, tea.Cmd) {
	capture := !m.effectiveMouseCapture()
	if capture {
		m.mouseOverride = MouseCapture
	} else {
		m.mouseOverride = MouseRelease
	}
	m.refreshViewport()
	return *m, m.showToast(mouseCaptureToast(capture))
}

// mouseCaptureToast words the feedback from what actually happened: the verb
// describes the state the key just resolved to, and the hint describes how to
// get the other one back.
func mouseCaptureToast(capture bool) string {
	if capture {
		return "Mouse captured · wheel scrolls the transcript · Ctrl+S releases it for native selection"
	}
	return "Mouse released to the terminal · click-drag selects text · Ctrl+S captures it again"
}
