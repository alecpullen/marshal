package bridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

func TestLiveStateRemoveProject(t *testing.T) {
	live := newLiveState()
	live.apply(fleetDelta{FleetDelta: contract.FleetDelta{SessionID: "s1", Kind: "activity", Activity: "read"}})
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
	rt, err := f.runtimeForAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	reg := rt.reg
	reg.permMu.Lock()
	reg.permissions["tc1"] = make(chan Decision, 1)
	reg.permSession["tc1"] = rt.sessionID
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
	rt, err := f.runtimeForAgent(id)
	if err != nil {
		t.Fatal(err)
	}

	// Park a permission, exactly as awaitPermission does.
	rt.reg.permMu.Lock()
	rt.reg.permissions["tc-7"] = make(chan Decision, 1)
	rt.reg.permSession["tc-7"] = rt.sessionID
	rt.reg.permMu.Unlock()
	rt.reg.emitEvent(rt.sessionID, map[string]any{
		"type":       "permission_request",
		"toolCallId": "tc-7",
		"params":     json.RawMessage(`{"sessionId":"` + rt.sessionID + `","toolCallId":"tc-7","toolName":"shell.run","command":"rm -rf build"}`),
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
	rt, err := f.runtimeForAgent(id)
	if err != nil {
		t.Fatal(err)
	}

	rt.reg.permMu.Lock()
	rt.reg.permissions["tc-7"] = make(chan Decision, 1)
	rt.reg.permSession["tc-7"] = rt.sessionID
	rt.reg.permMu.Unlock()
	rt.reg.emitEvent(rt.sessionID, map[string]any{
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
	rt, err := f.runtimeForAgent(id)
	if err != nil {
		t.Fatal(err)
	}

	rt.reg.quesMu.Lock()
	rt.reg.questions["q-3"] = make(chan Answers, 1)
	rt.reg.quesSession["q-3"] = rt.sessionID
	rt.reg.quesMu.Unlock()
	rt.reg.emitEvent(rt.sessionID, map[string]any{
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

// telemetryUpdate is session_telemetry as the engine sends it
// (internal/acp/turn.go buildTelemetry): changed files are objects.
const telemetryUpdate = `{"sessionId":"s1","update":{"kind":"session_telemetry",
 "context":{"messages":4,"messageChars":900,"packTokens":2500,"packMaxTokens":10000,"packSections":3},
 "changedFiles":[{"path":"a.go","added":3,"removed":1},{"path":"b.go","added":0,"removed":0}],
 "toolStats":[{"name":"file.read","calls":5,"errors":1,"slowestMs":120}],
 "rules":["no network"],
 "sessionFooter":{"turns":2,"lastTurnTokensUsed":10,"lastTurnTokensWindow":100}}}`

func TestClassifyTelemetryDecodesEngineShape(t *testing.T) {
	d, ok := classifyNotification("session/update", json.RawMessage(telemetryUpdate))
	if !ok || d.Kind != "telemetry" {
		t.Fatalf("telemetry was dropped: %+v ok=%v", d, ok)
	}
	if d.ChangedFiles == nil || d.ContextPct == nil || *d.ChangedFiles != 2 || *d.ContextPct != 25 {
		t.Fatalf("changedFiles=%v contextPct=%v, want 2 and 25", d.ChangedFiles, d.ContextPct)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	stats, _ := m["toolStats"].([]any)
	rules, _ := m["rules"].([]any)
	if len(stats) != 1 || stats[0].(map[string]any)["errors"] != float64(1) || stats[0].(map[string]any)["slowestMs"] != float64(120) {
		t.Fatalf("toolStats = %v", m["toolStats"])
	}
	if len(rules) != 1 || rules[0] != "no network" {
		t.Fatalf("rules = %v", m["rules"])
	}
	if _, ok := m["usage"]; ok {
		t.Fatal("usage must stay off the fleet stream")
	}
}

func TestTelemetryCarriesSteps(t *testing.T) {
	live := newLiveState()
	d, ok := classifyNotification("session/update", json.RawMessage(
		`{"sessionId":"s1","update":{"kind":"session_telemetry","steps":{"done":3}}}`))
	if !ok || d.Kind != "telemetry" {
		t.Fatalf("telemetry was dropped: %+v ok=%v", d, ok)
	}
	if d.StepsDone == nil || *d.StepsDone != 3 {
		t.Fatalf("StepsDone = %v, want 3", d.StepsDone)
	}
	live.apply(d)
	if got := live.get("s1").stepsDone; got != 3 {
		t.Fatalf("live stepsDone = %d, want 3", got)
	}
}

// A missing field is not a zero: a telemetry payload with no steps key must
// not claim the turn has taken none.
func TestTelemetryWithoutStepsOmitsTheCount(t *testing.T) {
	live := newLiveState()
	d, ok := classifyNotification("session/update", json.RawMessage(
		`{"sessionId":"s1","update":{"kind":"session_telemetry","changedFiles":[]}}`))
	if !ok || d.Kind != "telemetry" {
		t.Fatalf("telemetry was dropped: %+v ok=%v", d, ok)
	}
	if d.StepsDone != nil {
		t.Fatalf("StepsDone = %v, want nil for a payload with no steps key", *d.StepsDone)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if v, present := m["stepsDone"]; present {
		t.Fatalf("a keyless telemetry delta carries stepsDone = %v", v)
	}
	live.apply(d)
	if got := live.get("s1").stepsDone; got != 0 {
		t.Fatalf("live stepsDone = %d, want the untouched zero", got)
	}
}

func TestClassifyTelemetryWithNoChangesStillCounts(t *testing.T) {
	d, ok := classifyNotification("session/update", json.RawMessage(
		`{"sessionId":"s1","update":{"kind":"session_telemetry","changedFiles":[],"toolStats":[],"rules":[]}}`))
	if !ok || d.ChangedFiles == nil || *d.ChangedFiles != 0 || string(d.Rules) != "[]" || string(d.ToolStats) != "[]" {
		t.Fatalf("delta = %+v ok=%v", d, ok)
	}
}

func TestContextPct(t *testing.T) {
	for _, c := range []struct{ used, tokens, max, want int }{
		{0, 2500, 10000, 25}, {0, 20000, 10000, 100}, {0, 5, 0, 0}, {40, 1, 100, 40},
	} {
		if got := contextPct(c.used, c.tokens, c.max); got != c.want {
			t.Errorf("contextPct(%d,%d,%d) = %d, want %d", c.used, c.tokens, c.max, got, c.want)
		}
	}
}

func TestTelemetryZerosReachTheWireAndOtherDeltasOmitThem(t *testing.T) {
	d, _ := classifyNotification("session/update", json.RawMessage(
		`{"sessionId":"s1","update":{"kind":"session_telemetry","changedFiles":[],"context":{"packTokens":0,"packMaxTokens":100}}}`))
	out, _ := json.Marshal(d)
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if v, ok := m["contextPct"]; !ok || v != float64(0) {
		t.Fatalf("contextPct = %v, present=%v; a real zero must be sent", v, ok)
	}
	if v, ok := m["changedFiles"]; !ok || v != float64(0) {
		t.Fatalf("changedFiles = %v, present=%v; a real zero must be sent", v, ok)
	}

	a, _ := classifyNotification("session/update", json.RawMessage(`{"sessionId":"s1","update":{"kind":"tool_call","toolName":"x"}}`))
	out, _ = json.Marshal(a)
	m = nil
	_ = json.Unmarshal(out, &m)
	if _, ok := m["contextPct"]; ok {
		t.Fatalf("an activity delta carries contextPct: %s", out)
	}
	if _, ok := m["changedFiles"]; ok {
		t.Fatalf("an activity delta carries changedFiles: %s", out)
	}
}

func TestLiveStateTelemetryZeroesAndActivityKeepsValues(t *testing.T) {
	live := newLiveState()
	tel := func(files, ctx string) fleetDelta {
		d, _ := classifyNotification("session/update", json.RawMessage(
			`{"sessionId":"s1","update":{"kind":"session_telemetry","changedFiles":`+files+`,"context":{"packTokens":`+ctx+`,"packMaxTokens":100}}}`))
		return d
	}
	live.apply(tel(`[{"path":"a"}]`, "50"))
	live.apply(fleetDelta{FleetDelta: contract.FleetDelta{SessionID: "s1", Kind: "activity", Activity: "read"}})
	if got := live.get("s1"); got.changedFiles != 1 || got.contextPct != 50 {
		t.Fatalf("an activity delta disturbed telemetry: %+v", got)
	}
	live.apply(tel(`[]`, "0"))
	if got := live.get("s1"); got.changedFiles != 0 || got.contextPct != 0 {
		t.Fatalf("telemetry zeros were not applied: %+v", got)
	}
}

// TestSnapshotPendingCarriesWhenItWasParked: a parked ask reports when the
// bridge saw it arrive, so a client measures the wait from the ask rather
// than from the agent's last status line.
func TestSnapshotPendingCarriesWhenItWasParked(t *testing.T) {
	f := testFleet(t)
	ctx, cancel := testContext(t)
	defer cancel()
	id, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "agent one", Mode: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := f.runtimeForAgent(id)
	if err != nil {
		t.Fatal(err)
	}

	rt.reg.quesMu.Lock()
	rt.reg.questions["q-3"] = make(chan Answers, 1)
	rt.reg.quesSession["q-3"] = rt.sessionID
	rt.reg.quesMu.Unlock()
	before := time.Now().UnixMilli()
	rt.reg.emitEvent(rt.sessionID, map[string]any{
		"type": "question_request", "questionId": "q-3",
		"params": json.RawMessage(`{"questions":[{"question":"proceed?"}]}`),
	})
	observed := time.Now().UnixMilli()

	p := f.Snapshot()[0].Pending
	if p == nil {
		t.Fatal("Pending is nil for a parked question")
	}
	if p.At == 0 {
		t.Fatal("Pending.At is zero, but the bridge saw the ask arrive")
	}
	if p.At < before || p.At > observed {
		t.Fatalf("Pending.At = %d, want the park time within [%d, %d]", p.At, before, observed)
	}
}

// TestPendingParkTimeOutlivesStatusUpdates: a status delta and an access
// report move UpdatedAt, but the park time belongs to the ask, so a question
// the bridge keeps seeing still says how long it has waited.
func TestPendingParkTimeOutlivesStatusUpdates(t *testing.T) {
	live := newLiveState()
	live.observePending("a1", observedPending{kind: "question", id: "q-1", params: json.RawMessage(`{}`)})
	first := live.get("a1")
	if first.pending == nil || first.pending.at.IsZero() {
		t.Fatalf("the ask was not recorded with a park time: %+v", first.pending)
	}

	// A status delta and an access report: both move UpdatedAt.
	time.Sleep(2 * time.Millisecond)
	live.apply(fleetDelta{FleetDelta: contract.FleetDelta{SessionID: "a1", Kind: "activity", Activity: "read"}})
	live.setAccess("a1", "s1", AccessState{Mode: "edit", Revision: 1})
	mid := live.get("a1")
	if !mid.updatedAt.After(first.updatedAt) {
		t.Fatalf("UpdatedAt did not move: %v then %v", first.updatedAt, mid.updatedAt)
	}
	if mid.pending == nil || !mid.pending.at.Equal(first.pending.at) {
		t.Fatalf("a status update moved the park time: %+v then %+v", first.pending, mid.pending)
	}

	// The same ask seen again is the same wait.
	time.Sleep(2 * time.Millisecond)
	live.observePending("a1", observedPending{kind: "question", id: "q-1", params: json.RawMessage(`{}`)})
	again := live.get("a1")
	if !again.pending.at.Equal(first.pending.at) {
		t.Fatalf("re-observing the ask moved the park time: %v then %v", first.pending.at, again.pending.at)
	}
	if !again.updatedAt.After(mid.updatedAt) {
		t.Fatalf("UpdatedAt did not move on the repeated observation: %v then %v", mid.updatedAt, again.updatedAt)
	}
}

// TestPendingParkTimeIsStampedAnewForANewRequest: the park time belongs to the
// ask, so a different request seen parked later on the same agent begins its
// own wait rather than inheriting the one before it.
func TestPendingParkTimeIsStampedAnewForANewRequest(t *testing.T) {
	live := newLiveState()
	live.observePending("a1", observedPending{kind: "question", id: "q-1", params: json.RawMessage(`{}`)})
	first := live.get("a1")
	if first.pending == nil || first.pending.at.IsZero() {
		t.Fatalf("the ask was not recorded with a park time: %+v", first.pending)
	}

	// A different ask on the same agent: it was parked when it was seen, not
	// when the ask before it was.
	time.Sleep(2 * time.Millisecond)
	live.observePending("a1", observedPending{kind: "question", id: "q-2", params: json.RawMessage(`{}`)})
	next := live.get("a1")
	if next.pending == nil || next.pending.id != "q-2" {
		t.Fatalf("the new ask was not recorded: %+v", next.pending)
	}
	if !next.pending.at.After(first.pending.at) {
		t.Fatalf("the new ask inherited the old park time: %v then %v", first.pending.at, next.pending.at)
	}
}
