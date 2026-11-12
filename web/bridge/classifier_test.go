package bridge

import (
	"encoding/json"
	"testing"
	"time"
)

// classifierFixture wires a Child over a fake agent, its registry, and a
// fleet whose classifier keys two of its sessions to two agents.
func classifierFixture(t *testing.T) (*Fleet, *fakeAgent, *Registry) {
	t.Helper()
	f := testFleet(t)
	fa := newFakeAgent()
	child := &Child{Transport: fa}
	reg := NewRegistry(child)
	t.Cleanup(func() {
		reg.mu.Lock()
		for _, info := range reg.sessions {
			if info.cancel != nil {
				info.cancel()
			}
		}
		reg.mu.Unlock()
	})
	f.installClassifier(child, reg, func(id string) (string, bool) {
		switch id {
		case "s-1":
			return "agent-a", true
		case "s-2":
			return "agent-b", true
		}
		return "", false
	})
	// Install before Start: the child reads its hook fields unsynchronized.
	if err := child.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(child.Stop)
	return f, fa, reg
}

// activityDeltas returns the activity deltas on the fleet stream, keyed by the
// id they were emitted under.
func activityDeltas(f *Fleet) map[string]string {
	out := map[string]string{}
	for _, e := range f.fleetLog.Tail(fleetStreamKey) {
		var d fleetDelta
		if json.Unmarshal(e.Data, &d) == nil && d.Kind == "activity" {
			out[d.SessionID] = d.Activity
		}
	}
	return out
}

// pendingDeltas returns the pending deltas on the fleet stream, keyed by the id
// they were emitted under.
func pendingDeltas(f *Fleet) map[string]string {
	out := map[string]string{}
	for _, e := range f.fleetLog.Tail(fleetStreamKey) {
		var ev fleetDelta
		if json.Unmarshal(e.Data, &ev) == nil && ev.Kind == "pending" {
			out[ev.SessionID] = string(ev.PendingKind)
		}
	}
	return out
}

// TestTheClassifierKeysEachSessionToItsAgent: a notification reaches the live
// state and the fleet log under its own agent's id, and a session in no map
// emits nothing.
func TestTheClassifierKeysEachSessionToItsAgent(t *testing.T) {
	f, fa, _ := classifierFixture(t)

	for _, sid := range []string{"s-1", "s-2", "s-3"} {
		fa.notify("session/update", map[string]any{
			"sessionId": sid,
			"update":    map[string]any{"kind": "tool_call", "toolName": "file.read"},
		})
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		got := activityDeltas(f)
		if got["agent-a"] == "file.read" && got["agent-b"] == "file.read" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("activity deltas = %v, want one under agent-a and one under agent-b", got)
		}
		time.Sleep(5 * time.Millisecond)
	}

	got := activityDeltas(f)
	if len(got) != 2 {
		t.Fatalf("activity deltas = %v, want exactly two: s-3 is no agent's", got)
	}
	for _, raw := range []string{"s-1", "s-2", "s-3"} {
		if _, ok := got[raw]; ok {
			t.Errorf("a delta was keyed by the raw session id %q: %v", raw, got)
		}
	}
	if live := f.live.get("agent-a"); live.activity != "file.read" {
		t.Errorf("agent-a's live activity = %q, want file.read", live.activity)
	}
}

// TestTheClassifierKeysRegistryEventsToTheirAgent: a permission request for one
// session marks only that session's agent as pending.
func TestTheClassifierKeysRegistryEventsToTheirAgent(t *testing.T) {
	f, _, reg := classifierFixture(t)

	reg.emitEvent("s-1", map[string]any{
		"type":       "permission_request",
		"toolCallId": "tc-1",
		"params":     json.RawMessage(`{"sessionId":"s-1","toolCallId":"tc-1"}`),
	})

	got := pendingDeltas(f)
	if len(got) != 1 || got["agent-a"] != "approval" {
		t.Fatalf("pending deltas = %v, want one approval under agent-a", got)
	}
	if _, ok := got["agent-b"]; ok {
		t.Errorf("agent-b was marked pending by another session's request: %v", got)
	}

	// A session no agent owns surfaces nothing, whatever the request.
	reg.emitEvent("s-9", map[string]any{
		"type":       "permission_request",
		"toolCallId": "tc-9",
		"params":     json.RawMessage(`{"sessionId":"s-9","toolCallId":"tc-9"}`),
	})
	if got := pendingDeltas(f); len(got) != 1 {
		t.Errorf("pending deltas = %v, want still one: s-9 is no agent's", got)
	}
}
