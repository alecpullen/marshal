package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// pumpChanged blocks until the session signals that its status may have
// changed, then returns a changedMsg carrying the channel it read. A closed
// channel gives a nil message, which ends the pump.
func pumpChanged(ch <-chan struct{}) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return nil
		}
		return changedMsg{ch: ch}
	}
}

// refreshSheetCmd re-reads the sheet values req names, off the UI thread. sess
// fires Changed when they land, so the command returns no message.
func refreshSheetCmd(ctx context.Context, sess acpclient.Session, req acpwire.SheetRefresh) tea.Cmd {
	return func() tea.Msg {
		_ = sess.RefreshSheet(ctx, req)
		return nil
	}
}
