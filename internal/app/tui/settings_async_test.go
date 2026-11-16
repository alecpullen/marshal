package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/settings"
)

// settingsCommit drives a docked settings commit to the ChangedMsg the model
// applies. The write runs off the Update goroutine (AUD-10), so the panel
// applies its own SaveDoneMsg first and the ChangedMsg follows one hop later.
func settingsCommit(t *testing.T, m *Model, cmd tea.Cmd) settings.ChangedMsg {
	t.Helper()
	var found []settings.ChangedMsg
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			for _, inner := range msg {
				walk(inner)
			}
		case settings.SaveDoneMsg:
			if p, ok := m.dock.Panel().(*settings.BrowserPanel); ok {
				walk(p.Update(msg))
			}
		case settings.ChangedMsg:
			found = append(found, msg)
		}
	}
	walk(cmd)
	if len(found) != 1 {
		t.Fatalf("the commit produced %d ChangedMsgs, want one", len(found))
	}
	return found[0]
}
