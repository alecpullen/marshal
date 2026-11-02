package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/pubsub"
	"marshal/internal/viewmodel"
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

// pumpWorkEvents reads from the model's persistent Work snapshot subscription
// and returns a tea.Cmd that blocks until the next WorkSnapshot event lands,
// then returns a workMsg carrying the subscription ID the pump captured.
// Re-arming reads from the same channel: a terminated pump returns nil and
// never resubscribes itself, so only an explicit rebind starts a new stream.
func pumpWorkEvents(ch <-chan pubsub.Event[viewmodel.WorkSnapshot], subscriptionID uint64) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return workMsg{snapshot: viewmodel.WorkSnapshotOf(ev.Payload), subscriptionID: subscriptionID}
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
