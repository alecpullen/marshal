package session

import (
	"errors"
	"testing"
)

// registerSettledApproval publishes one approval on the fixture's operation and
// returns its canonical key, so a test can watch settlement of a real record.
func registerSettledApproval(t *testing.T, f *promptFixture) PromptKey {
	t.Helper()
	call := &PendingToolCall{ID: "call-1", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	key, err := f.state.RegisterApproval(f.opCtx, call)
	if err != nil {
		t.Fatalf("RegisterApproval = %v, want nil", err)
	}
	return key
}

// TestPromptSettledStaysOpenWhileThePromptWaits pins that a live record's
// channel is not closed before any settlement.
func TestPromptSettledStaysOpenWhileThePromptWaits(t *testing.T) {
	f := newPromptFixture(t)
	key := registerSettledApproval(t, f)

	settled := f.state.PromptSettled(key)
	if settled == nil {
		t.Fatal("PromptSettled = nil, want the live record's channel")
	}
	select {
	case <-settled:
		t.Fatal("the prompt's channel was closed before any answer or cancellation")
	default:
	}
}

// TestPromptSettledClosesOnAnAnswer pins that the answer path closes it.
func TestPromptSettledClosesOnAnAnswer(t *testing.T) {
	f := newPromptFixture(t)
	key := registerSettledApproval(t, f)
	settled := f.state.PromptSettled(key)

	if ok, err := f.state.ResolveApproval(key, UserApprovalDecision{Approved: true}, nil); !ok || err != nil {
		t.Fatalf("ResolveApproval = (%v, %v), want the answer to win", ok, err)
	}
	select {
	case <-settled:
	default:
		t.Fatal("the prompt's channel stayed open after its answer")
	}
}

// TestPromptSettledClosesOnACancellation pins that a steering cancellation of
// the prompt's operation closes it.
func TestPromptSettledClosesOnACancellation(t *testing.T) {
	f := newPromptFixture(t)
	key := registerSettledApproval(t, f)
	settled := f.state.PromptSettled(key)

	f.state.CancelOperationPrompts(f.ref)

	select {
	case <-settled:
	default:
		t.Fatal("the prompt's channel stayed open after a steering cancellation")
	}
	if !errors.Is(f.state.PromptCause(key), ErrSendNow) {
		t.Fatalf("PromptCause = %v, want ErrSendNow", f.state.PromptCause(key))
	}
}

// TestPromptSettledIsClosedForAnUnknownKey pins that a key naming no live
// record is already settled.
func TestPromptSettledIsClosedForAnUnknownKey(t *testing.T) {
	f := newPromptFixture(t)

	settled := f.state.PromptSettled(PromptKey{ID: "no-such-prompt", Kind: PromptApproval})
	if settled == nil {
		t.Fatal("PromptSettled for an unknown key = nil, want a closed channel")
	}
	select {
	case <-settled:
	default:
		t.Fatal("PromptSettled for an unknown key was not already closed")
	}
}

// TestPromptSettledIsNilForAnUnregisteredPrompt pins that an unregistered
// legacy prompt gets a channel that never fires.
func TestPromptSettledIsNilForAnUnregisteredPrompt(t *testing.T) {
	f := newPromptFixture(t)

	for _, key := range []PromptKey{{}, {Kind: PromptApproval}} {
		if got := f.state.PromptSettled(key); got != nil {
			t.Fatalf("PromptSettled(%+v) = %v, want nil", key, got)
		}
	}
}

// TestPromptSettledFindsALiveAccessRequestByItsID pins the lookup a typed
// access request depends on. It names itself by its request id alone, and its
// mirror record is filed under the approval kind, so a lookup that matched the
// kind would report a live access request as already settled and end its
// request before the client was ever asked.
func TestPromptSettledFindsALiveAccessRequestByItsID(t *testing.T) {
	f := newPromptFixture(t)
	call := &PendingToolCall{ID: "acc-1", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}

	f.state.accessMu.Lock()
	f.state.mirrorAccessPromptLocked(f.opCtx, call, "req-1")
	f.state.accessMu.Unlock()

	settled := f.state.PromptSettled(PromptKey{ID: "req-1"})
	if settled == nil {
		t.Fatal("PromptSettled for a live access request = nil, want its record's channel")
	}
	select {
	case <-settled:
		t.Fatal("a live access request reported itself as already settled")
	default:
	}
}
