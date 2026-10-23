package bridge

import (
	"encoding/json"
	"testing"
)

// TestInteractionContractBridgeReconnectAndCapabilityGrant pins that a
// capability grant reaches the live projection with every field, and that a
// reconnect replaying the same committed tuple leaves it unchanged. The bridge
// owns these JSON types; it never imports the agent's Go types.
func TestInteractionContractBridgeReconnectAndCapabilityGrant(t *testing.T) {
	live := newLiveState()
	grant := json.RawMessage(`{"sessionId":"s1","update":{"kind":"mode_changed","mode":"act","auto":false,"system_access":true,"access_revision":3}}`)
	d, ok := classifyNotification("session/update", grant)
	if !ok || d.Kind != "mode" || d.Access == nil {
		t.Fatalf("grant delta = %+v ok=%v", d, ok)
	}
	want := AccessState{Mode: "act", Auto: false, System: true, Revision: 3}
	if *d.Access != want {
		t.Fatalf("grant access = %+v, want %+v", *d.Access, want)
	}
	live.apply(d)
	if got := live.get("s1"); got.access == nil || *got.access != want {
		t.Fatalf("live access = %+v, want %+v", got.access, want)
	}

	// A reconnect replays the same committed tuple; the projection must not
	// drift or lose the explicit false.
	replay, ok := classifyNotification("session/update", grant)
	if !ok || replay.Access == nil {
		t.Fatalf("replay delta = %+v ok=%v", replay, ok)
	}
	live.apply(replay)
	if got := live.get("s1"); got.access == nil || *got.access != want {
		t.Fatalf("live access after reconnect = %+v, want %+v", got.access, want)
	}
}
