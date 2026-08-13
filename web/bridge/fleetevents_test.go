package bridge

import (
	"encoding/json"
	"testing"
)

func TestLiveStateRemoveProject(t *testing.T) {
	live := newLiveState()
	live.apply(fleetDelta{SessionID: "s1", Kind: "activity", Activity: "read"})
	live.removeProject([]string{"s1"})
	if got := live.get("s1"); got.activity != "" {
		t.Fatalf("removed live state = %+v", got)
	}
}

func TestClassifyNotificationExtractsActivity(t *testing.T) {
	d, ok := classifyNotification("session/update", json.RawMessage(`{"sessionId":"s1","update":{"kind":"tool_call","toolName":"file.read"}}`))
	if !ok || d.Kind != "activity" || d.Activity != "file.read" {
		t.Fatalf("delta = %+v, ok=%v", d, ok)
	}
}

func TestSnapshotDerivesIdleAndPending(t *testing.T) {
	f := testFleet(t)
	id, err := f.Spawn(t.Context(), "/home/u/a", "agent one", "edit")
	if err != nil {
		t.Fatal(err)
	}
	s := f.Snapshot()
	if len(s) != 1 || s[0].ID != id || s[0].Status != "idle" {
		t.Fatalf("snapshot = %+v", s)
	}
	reg, err := f.RegistryForSession(id)
	if err != nil {
		t.Fatal(err)
	}
	reg.permMu.Lock()
	reg.permissions["tc1"] = make(chan Decision, 1)
	reg.permSession["tc1"] = id
	reg.permMu.Unlock()
	if got := f.Snapshot()[0].Status; got != "awaiting-approval" {
		t.Fatalf("status = %q", got)
	}
}
