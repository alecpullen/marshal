package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// flashFor is how long a transient status message stays up.
const flashFor = 3 * time.Second

// flashClearMsg wakes the view when a flash expires.
type flashClearMsg struct{}

// setFlash shows a short message in the status line's right cluster for a few
// seconds. Notices are for errors that need dismissing; this is for "Detail:
// outline" and "Copied 12 lines".
func (m *Model) setFlash(text string) tea.Cmd {
	m.flash = text
	m.flashUntil = m.now().Add(flashFor)
	return tea.Tick(flashFor, func(time.Time) tea.Msg { return flashClearMsg{} })
}

func (m Model) flashActive() bool {
	return m.flash != "" && m.now().Before(m.flashUntil)
}
