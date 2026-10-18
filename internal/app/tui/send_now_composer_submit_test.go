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
