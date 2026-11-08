package service

import (
	"context"
	"testing"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
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
