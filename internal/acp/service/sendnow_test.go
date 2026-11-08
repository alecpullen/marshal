package service

import (
	"context"
	"testing"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// TestSendNowOnAClosedSessionFails pins that a swap's old session refuses a Send
// now request with the closed-session code.
func TestSendNowOnAClosedSessionFails(t *testing.T) {
	h, old := New(Deps{State: newState(t, "s1")})
	h.swap(newState(t, "s2"))

	if _, err := old.SendNow(context.Background(), acpwire.SendNowRequest{
		SessionID: "s1",
		TurnID:    "t1",
		RequestID: "r1",
		Text:      "steer",
	}); !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Fatalf("SendNow on a closed session = %v, want the %q code", err, acpwire.CodeSessionClosed)
	}
}

// TestSendNowSignalsTheReader pins the in-process signal an accepted Send now
// request leaves behind. The conformance harness runs the same case over both
// arms, where the pipe arm proves only the pushed update, so the signal itself
// is pinned here: losing it would leave an in-process reader stale with every
// shared case still green.
func TestSendNowSignalsTheReader(t *testing.T) {
	state := newState(t, "s1")
	state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
		return session.OperationStop{}, nil
	})
	turn, err := state.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn = %v, want nil", err)
	}
	_, sess := New(Deps{State: state})

	if _, err := sess.SendNow(context.Background(), acpwire.SendNowRequest{
		SessionID: "s1",
		TurnID:    turn,
		RequestID: "r1",
		Text:      "steer",
	}); err != nil {
		t.Fatalf("SendNow = %v, want nil", err)
	}
	waitChanged(t, sess)
}
