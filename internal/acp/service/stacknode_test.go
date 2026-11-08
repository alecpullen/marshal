package service

import (
	"context"
	"testing"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// The detail cases live in the conformance harness now: they read the same
// rows through both arms of the seam (V1). What stays here is the closed
// session's contract, which the client's mirror cannot see.
func TestStackNodeOnAReplacedSessionFails(t *testing.T) {
	h, old := New(Deps{State: newState(t, "s1")})
	h.swap(newState(t, "s2"))

	if _, err := old.StackNode(context.Background(), "step:1", 0); !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Fatalf("StackNode on a closed session = %v, want the %q code", err, acpwire.CodeSessionClosed)
	}
}
