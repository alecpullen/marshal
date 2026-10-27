package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/steerpanel"
)

// TestSendNowComposerSubmitClosesOnlyOnAcceptance pins the composer's accepted
// submit path: the composer closes through the clear callback only after the
// runtime accepted the request, and a rejected submit keeps the composer open
// with its text intact so the user can retry.
func TestSendNowComposerSubmitClosesOnlyOnAcceptance(t *testing.T) {
	// Accepted: the submit reaches the runtime and the composer closes,
	// restoring the surface it displaced.
	f := newSendNowFixture(t, 120, 40, true)
	displaced := &sendNowTestPanel{id: "settings"}
	f.m.dock.Open(displaced)
	p := f.composer()
	f.send(tea.PasteMsg{Content: "steer the turn"})
	if p.Text() != "steer the turn" {
		t.Fatalf("composer text = %q", p.Text())
	}
	f.send(steerpanel.SubmitMsg{ComposerID: p.ID(), SessionID: f.state.SessionID(), TurnID: f.turnID, Text: p.Text()})

	if len(f.submits) != 1 {
		t.Fatalf("an accepted submit recorded %d requests, want 1", len(f.submits))
	}
	got := f.submits[0]
	if got.SessionID != f.state.SessionID() || got.TurnID != f.turnID || got.Text != "steer the turn" {
		t.Fatalf("request = %s/%s %q, want %s/%s %q",
			got.SessionID, got.TurnID, got.Text, f.state.SessionID(), f.turnID, "steer the turn")
	}
	if f.m.steeringComposer != nil {
		t.Fatal("the composer stayed open after an accepted submit")
	}
	if f.m.composerID != "" {
		t.Fatalf("composerID = %q after acceptance, want it cleared", f.m.composerID)
	}
	if f.m.dock.Panel() != dock.Panel(displaced) {
		t.Fatalf("accepted submit left the dock on %T, want the displaced panel", f.m.dock.Panel())
	}
	if q := f.state.SteeringQueue(); len(q) != 1 || q[0] != "steer the turn" {
		t.Fatalf("queue = %v, want the accepted text", q)
	}

	// Rejected — the turn ended under the composer: no request, and the
	// composer stays up with the text the user wrote.
	r := newSendNowFixture(t, 120, 40, true)
	r.m.dock.Open(&sendNowTestPanel{id: "settings"})
	p2 := r.composer()
	r.send(tea.PasteMsg{Content: "keep me"})
	r.state.EndSteeringTurn(r.turnID, "the turn ended")
	r.send(steerpanel.SubmitMsg{ComposerID: p2.ID(), SessionID: r.state.SessionID(), TurnID: r.turnID, Text: p2.Text()})

	if len(r.submits) != 0 {
		t.Fatalf("a submit against an ended turn recorded %d requests", len(r.submits))
	}
	if r.m.steeringComposer == nil {
		t.Fatal("a rejected submit closed the composer")
	}
	if p2.Text() != "keep me" {
		t.Fatalf("a rejected submit lost the composer text: %q", p2.Text())
	}
}

// closeCountingSendNowPanel is a suspended-surface panel that records the
// host's close notifications, standing in for a panel whose workers are
// cancelled when it is gone for good.
type closeCountingSendNowPanel struct {
	sendNowTestPanel
	closes int
}

func (p *closeCountingSendNowPanel) OnClose() { p.closes++ }

var _ dock.CloseAware = (*closeCountingSendNowPanel)(nil)

// TestHostSuspendRestoreDoesNotCloseRetainedPanel pins the Send now suspension
// contract: a panel the composer displaces is retained and restored without a
// close notification, and it is notified exactly once when the host really
// discards it.
func TestHostSuspendRestoreDoesNotCloseRetainedPanel(t *testing.T) {
	f := newSendNowFixture(t, 120, 40, true)
	panel := &closeCountingSendNowPanel{sendNowTestPanel: sendNowTestPanel{id: "installer"}}
	f.m.dock.Open(panel)

	// Send now suspends the panel: the composer takes the dock and the panel is
	// retained, so it is told nothing.
	f.composer()
	if f.m.composerDisplaced != dock.Panel(panel) {
		t.Fatalf("composer displaced %T, want the installer panel", f.m.composerDisplaced)
	}
	if panel.closes != 0 {
		t.Fatalf("opening the composer closed the panel it displaced (%d notifications)", panel.closes)
	}

	// A routing pass runs with the retained panel in the slot and puts the
	// composer back; still nobody is closed and the result lands once.
	f.send(sendNowOwnedMsg{})
	if panel.owned != 1 {
		t.Fatalf("suspended panel claimed %d results, want exactly 1", panel.owned)
	}
	if panel.closes != 0 {
		t.Fatalf("the routing pass closed the suspended panel (%d notifications)", panel.closes)
	}
	if _, ok := f.m.dock.Panel().(*steerpanel.Panel); !ok {
		t.Fatalf("composer was not restored; dock panel = %T", f.m.dock.Panel())
	}

	// Esc restores the panel: it was retained, so coming back notifies nobody.
	f.send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if f.m.dock.Panel() != dock.Panel(panel) {
		t.Fatalf("dock panel = %T, want the retained panel", f.m.dock.Panel())
	}
	if panel.closes != 0 {
		t.Fatalf("restoring the retained panel closed it (%d notifications)", panel.closes)
	}

	// The actual discard: on shutdown the active composer and the panel retained
	// behind it are released once, so a panel that cancels work on close gets
	// its chance before the program stops.
	g := newSendNowFixture(t, 120, 40, true)
	retained := &closeCountingSendNowPanel{sendNowTestPanel: sendNowTestPanel{id: "installer"}}
	g.m.dock.Open(retained)
	g.composer()
	if retained.closes != 0 {
		t.Fatalf("the second composer closed its displaced panel (%d notifications)", retained.closes)
	}
	if cmd := g.m.finishQuit(); cmd == nil {
		t.Fatal("finishQuit returned no command")
	}
	if retained.closes != 1 {
		t.Fatalf("shutdown notified the retained panel %d times, want exactly 1", retained.closes)
	}
}
