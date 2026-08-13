package bridge

import (
	"encoding/json"
	"strings"
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
	id, err := f.Spawn(t.Context(), "/home/u/a", SpawnOptions{Name: "agent one", Mode: "edit"})
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

// The dashboard resolves approvals without opening a chat, so the
// snapshot must carry the pending request's payload, not just a status.
func TestSnapshotCarriesPendingApprovalPayload(t *testing.T) {
	f := testFleet(t)
	ctx, cancel := testContext(t)
	defer cancel()
	id, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "agent one", Mode: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := f.liveRuntimeForSession(id)
	if err != nil {
		t.Fatal(err)
	}

	// Park a permission, exactly as awaitPermission does.
	rt.reg.permMu.Lock()
	rt.reg.permissions["tc-7"] = make(chan Decision, 1)
	rt.reg.permSession["tc-7"] = id
	rt.reg.permMu.Unlock()
	rt.reg.emitEvent(id, map[string]any{
		"type":       "permission_request",
		"toolCallId": "tc-7",
		"params":     json.RawMessage(`{"sessionId":"` + id + `","toolCallId":"tc-7","toolName":"shell.run","command":"rm -rf build"}`),
	})

	snap := f.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d", len(snap))
	}
	if snap[0].Status != "awaiting-approval" {
		t.Fatalf("Status = %q", snap[0].Status)
	}
	p := snap[0].Pending
	if p == nil {
		t.Fatal("Pending is nil; the dashboard cannot render a decision without it")
	}
	if p.Kind != "approval" {
		t.Errorf("Pending.Kind = %q, want \"approval\"", p.Kind)
	}
	if p.ID != "tc-7" {
		t.Errorf("Pending.ID = %q, want \"tc-7\"", p.ID)
	}
	if !strings.Contains(string(p.Params), "shell.run") {
		t.Errorf("Pending.Params lost the request detail: %s", p.Params)
	}
}

// A resolved request must stop being advertised, even though the observed
// payload is still cached: Registry.Pending is the authority.
func TestSnapshotDropsPendingPayloadOnceResolved(t *testing.T) {
	f := testFleet(t)
	ctx, cancel := testContext(t)
	defer cancel()
	id, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "agent one", Mode: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := f.liveRuntimeForSession(id)
	if err != nil {
		t.Fatal(err)
	}

	rt.reg.permMu.Lock()
	rt.reg.permissions["tc-7"] = make(chan Decision, 1)
	rt.reg.permSession["tc-7"] = id
	rt.reg.permMu.Unlock()
	rt.reg.emitEvent(id, map[string]any{
		"type": "permission_request", "toolCallId": "tc-7",
		"params": json.RawMessage(`{"toolName":"shell.run"}`),
	})
	if f.Snapshot()[0].Pending == nil {
		t.Fatal("precondition: pending payload should be present")
	}

	// Resolve it the way the HTTP layer does.
	if err := rt.reg.ResolvePermission("tc-7", Decision{Approved: true}); err != nil {
		t.Fatalf("ResolvePermission: %v", err)
	}
	rt.reg.permMu.Lock()
	delete(rt.reg.permissions, "tc-7")
	delete(rt.reg.permSession, "tc-7")
	rt.reg.permMu.Unlock()

	snap := f.Snapshot()
	if snap[0].Pending != nil {
		t.Errorf("Pending must be dropped once nothing is outstanding, got %+v", snap[0].Pending)
	}
	if snap[0].Status == "awaiting-approval" {
		t.Error("Status must leave awaiting-approval once resolved")
	}
}

func TestSnapshotCarriesPendingQuestionPayload(t *testing.T) {
	f := testFleet(t)
	ctx, cancel := testContext(t)
	defer cancel()
	id, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "agent one", Mode: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := f.liveRuntimeForSession(id)
	if err != nil {
		t.Fatal(err)
	}

	rt.reg.quesMu.Lock()
	rt.reg.questions["q-3"] = make(chan Answers, 1)
	rt.reg.quesSession["q-3"] = id
	rt.reg.quesMu.Unlock()
	rt.reg.emitEvent(id, map[string]any{
		"type": "question_request", "questionId": "q-3",
		"params": json.RawMessage(`{"questions":[{"question":"proceed?"}]}`),
	})

	p := f.Snapshot()[0].Pending
	if p == nil {
		t.Fatal("Pending is nil for a parked question")
	}
	if p.Kind != "question" || p.ID != "q-3" {
		t.Errorf("Pending = %+v, want kind question id q-3", p)
	}
}
