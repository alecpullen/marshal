package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// toastDuration is how long transient UI feedback stays on screen. It is
// short on purpose: the toast answers "what did that key just do?" and then
// gets out of the way, unlike a session notice (which is a warning or error
// and is expected to persist until dismissed).
const toastDuration = 3 * time.Second

// toastExpiredMsg retires one toast. It carries the generation of the toast
// it was scheduled for so a superseded toast's timer cannot clear a newer
// one: pressing Ctrl+S twice quickly used to leave the second toast up for
// the remainder of the first toast's lifetime, or clear it a moment later.
type toastExpiredMsg struct{ gen int }

// showToast replaces the current transient feedback and schedules its
// expiry. The returned command is the timer.
func (m *Model) showToast(text string) tea.Cmd {
	m.toastGen++
	gen := m.toastGen
	m.toast = text
	m.toastUntil = m.now().Add(toastDuration)
	return tea.Tick(toastDuration, func(time.Time) tea.Msg { return toastExpiredMsg{gen: gen} })
}

// clearToast drops the transient feedback, e.g. when a session-scoped
// override is reset for a new session.
func (m *Model) clearToast() {
	m.toastGen++
	m.toast = ""
	m.toastUntil = time.Time{}
}

// toastText returns the feedback to render, or "" when nothing is up.
//
// Expiry is checked against the deadline as well as honoured through
// toastExpiredMsg: a dropped timer must not leave a stale line on the status
// bar forever, and the deadline cannot disagree with the timer because both
// are stamped from the same duration.
func (m Model) toastText() string {
	if m.toast == "" {
		return ""
	}
	if !m.toastUntil.IsZero() && !m.now().Before(m.toastUntil) {
		return ""
	}
	return m.toast
}

// handleToastExpired retires the toast the timer was scheduled for. A timer
// whose generation has been superseded is ignored.
func (m Model) handleToastExpired(msg toastExpiredMsg) (Model, tea.Cmd) {
	if msg.gen != m.toastGen {
		return m, nil
	}
	m.clearToast()
	m.refreshViewport()
	return m, nil
}
