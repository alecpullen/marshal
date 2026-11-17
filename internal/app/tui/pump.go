package tui

import (
	"context"
	"sync"

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

// bell is the terminal bell: the program's half of a notification, written
// without the renderer (decision 6). The fleet model keeps its own copy, since
// it is unexported there.
const bell = "\a"

// fleetChangedMsg is the fleet's change signal, apart from the session's.
type fleetChangedMsg struct{ ch <-chan struct{} }

// fleetQuitGate releases the fleet's wait when the program quits. The close is
// guarded, so a second quit path cannot panic on an already-closed channel. It
// is held by pointer: Model is a value, and every copy shares one release.
type fleetQuitGate struct {
	once sync.Once
	ch   chan struct{}
}

// newFleetQuitGate is a gate that has not been released.
func newFleetQuitGate() *fleetQuitGate { return &fleetQuitGate{ch: make(chan struct{})} }

// release closes the gate, once. A nil gate is a model built without a fleet,
// which has no wait to release.
func (g *fleetQuitGate) release() {
	if g == nil {
		return
	}
	g.once.Do(func() { close(g.ch) })
}

// pumpFleetChanged blocks until the fleet signals that its state may have
// changed, then returns a fleetChangedMsg carrying the channel it read. It is
// released by done, which the program closes on the way out, so the wait never
// outlives the program. A closed channel gives a nil message, which ends the
// pump: there is nothing more to read.
func pumpFleetChanged(ch, done <-chan struct{}) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-done:
			return nil
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			return fleetChangedMsg{ch: ch}
		}
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
