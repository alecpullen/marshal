package bridge

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Discard is the operator's "throw this work away" action and the spec
// shows it for every agent. The child can still refuse session/discard:
// the session may be gone (a bridge restart, or a session closed out from
// under it) or no longer isolated (it was merged, or the worktree was
// removed). Neither means the agent record should survive.
//
// A record that cannot be retired leaks its concurrency slot, because
// releaseAgent is the only thing that calls slots.release. Enough leaks
// fill the pool, and Spawn then blocks forever waiting for a slot that
// will never come — the bridge stops accepting work entirely.
func TestDiscardRetiresAnAgentWhenTheChildRefuses(t *testing.T) {
	for name, msg := range map[string]string{
		"session is gone":    `server error: unknown session "s-1"`,
		"no longer isolated": `server error: session "s-1" is not isolated in a worktree`,
	} {
		t.Run(name, func(t *testing.T) {
			f, _, agentOf := agentFleet(t)
			id, err := f.Spawn(ctlContext(t), "/p", SpawnOptions{Prompt: "x"})
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			agentOf(id).handler = func(method string, _ json.RawMessage) (any, *rpcError, bool) {
				if method == "session/discard" {
					return nil, &rpcError{Code: -32000, Message: msg}, true
				}
				return nil, nil, false
			}

			if err := f.Discard(context.Background(), id); err != nil {
				t.Fatalf("Discard: %v", err)
			}
			if _, ok := f.ws.Agent(id); ok {
				t.Fatal("the agent is still listed after discard")
			}
			if n := f.slots.running; n != 0 {
				t.Fatalf("slots.running = %d after discard, want 0: the slot leaked", n)
			}
		})
	}
}

// A persisted agent whose session cannot be restored is the state a
// bridge restart leaves behind: the record survives, the runtime does
// not, and reattaching fails. Discard must still retire it. Otherwise the
// record is permanent and, with it, the concurrency slot it holds.
func TestDiscardRetiresAnAgentWhoseSessionCannotBeRestored(t *testing.T) {
	f, _, _ := agentFleet(t)
	id, err := f.Spawn(ctlContext(t), "/p", SpawnOptions{Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// Detach without destroying: the record survives, the runtime does not.
	f.releaseAgent(id, false)

	// The next runtime cannot restore the session.
	fail := newFakeAgent()
	fail.handler = func(method string, _ json.RawMessage) (any, *rpcError, bool) {
		if method == "session/load" {
			return nil, &rpcError{Code: -32603, Message: "internal error"}, true
		}
		return nil, nil, false
	}
	f.newRuntime = func(a Agent) (*Child, error) { return &Child{Transport: fail}, nil }

	if err := f.Discard(context.Background(), id); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, ok := f.ws.Agent(id); ok {
		t.Fatal("the agent is still listed after discard")
	}
	if n := f.slots.running; n != 0 {
		t.Fatalf("slots.running = %d after discard, want 0: the slot leaked", n)
	}
}

// A full pool must not hang the caller forever. Spawn waits for a slot
// rather than rejecting the work, which is right, but the wait needs a
// bound: an HTTP client that never gets a response cannot tell a busy
// bridge from a dead one.
func TestSpawnFailsWhenThePoolIsFull(t *testing.T) {
	f, _, _ := agentFleet(t)
	f.slots = newSlots(1)
	f.slotWait = 250 * time.Millisecond

	if _, err := f.Spawn(ctlContext(t), "/p", SpawnOptions{Prompt: "first"}); err != nil {
		t.Fatalf("first Spawn: %v", err)
	}
	// The pool is now full. A second spawn must report that rather than
	// block. The context deliberately has no deadline: an HTTP request
	// context does not either, so the bridge itself has to bound the
	// wait or the caller waits forever.
	done := make(chan error, 1)
	go func() {
		_, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "second"})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Spawn succeeded with a full pool")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Spawn blocked with a full pool instead of reporting it")
	}
}
