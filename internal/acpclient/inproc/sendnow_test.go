package inproc

import (
	"context"
	"reflect"
	"testing"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// steerTurn opens an eligible turn on state with a no-op interrupt handler and
// returns its turn ID.
func steerTurn(t *testing.T, state *session.State) string {
	t.Helper()
	state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
		return session.OperationStop{}, nil
	})
	turn, err := state.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn = %v, want nil", err)
	}
	return turn
}

// TestSendNowRecordsTheReceiptBeforeReturning pins that an accepted Send now
// request is in Status by the time SendNow returns, queue and signal included.
func TestSendNowRecordsTheReceiptBeforeReturning(t *testing.T) {
	state := newState(t, "s1")
	turn := steerTurn(t, state)
	_, sess := New(Deps{State: state})

	got, err := sess.SendNow(context.Background(), acpwire.SendNowRequest{
		SessionID: "s1",
		TurnID:    turn,
		RequestID: "r1",
		Text:      "steer",
	})
	if err != nil {
		t.Fatalf("SendNow = %v, want nil", err)
	}
	if got.Status != acpwire.SteeringAccepted {
		t.Errorf("receipt status = %q, want %q", got.Status, acpwire.SteeringAccepted)
	}
	if got.RequestID != "r1" {
		t.Errorf("receipt request ID = %q, want r1", got.RequestID)
	}

	status := sess.Status()
	if status.Steering.Receipt == nil {
		t.Fatalf("Steering.Receipt = nil, want the accepted receipt")
	}
	if got := status.Steering.Receipt.RequestID; got != "r1" {
		t.Errorf("Steering.Receipt.RequestID = %q, want r1", got)
	}
	if want := []string{"steer"}; !reflect.DeepEqual(status.Steering.Queue, want) {
		t.Errorf("Steering.Queue = %v, want %v", status.Steering.Queue, want)
	}
	waitChanged(t, sess)
}

// TestSendNowReturnsTheStatesRefusal pins that a refusal comes back as the
// state's own error, with nothing recorded.
func TestSendNowReturnsTheStatesRefusal(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	if _, err := sess.SendNow(context.Background(), acpwire.SendNowRequest{
		SessionID: "s1",
		TurnID:    "t1",
		RequestID: "r1",
		Text:      "steer",
	}); err == nil {
		t.Fatalf("SendNow = nil, want the state's refusal with no steering turn open")
	}
	if got := sess.Status().Steering.Receipt; got != nil {
		t.Fatalf("Steering.Receipt = %+v, want nil after a refusal", got)
	}
}

// TestSendNowOnAClosedSessionFails pins that a swap's old session refuses a Send
// now request with the closed-session code.
func TestSendNowOnAClosedSessionFails(t *testing.T) {
	h, old := New(Deps{State: newState(t, "s1")})
	h.Swap(newState(t, "s2"))

	if _, err := old.SendNow(context.Background(), acpwire.SendNowRequest{
		SessionID: "s1",
		TurnID:    "t1",
		RequestID: "r1",
		Text:      "steer",
	}); !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Fatalf("SendNow on a closed session = %v, want the %q code", err, acpwire.CodeSessionClosed)
	}
}
