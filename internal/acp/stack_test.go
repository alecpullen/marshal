package acp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

type stackNotice struct {
	method string
	params SessionUpdateParams
}

func newStackTestManager(t *testing.T) (*TurnManager, *session.State, *[]stackNotice) {
	t.Helper()
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	notices := []stackNotice{}
	m := NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) { return &TurnRuntime{State: st}, id == "s1" },
		Notify: func(method string, params any) error {
			notices = append(notices, stackNotice{method, params.(SessionUpdateParams)})
			return nil
		},
	})
	return m, st, &notices
}

func takeStack(t *testing.T, m *TurnManager) StackSnapshot {
	t.Helper()
	v, err := m.Stack(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	return v.(StackSnapshot)
}

func stackPatchFromNotice(t *testing.T, n stackNotice) stackPatch {
	t.Helper()
	if n.method != "session/update" {
		t.Fatalf("method = %s", n.method)
	}
	data, err := json.Marshal(n.params)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SessionID != "s1" {
		t.Fatalf("sessionId = %q", envelope.SessionID)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Update, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"kind", "rev", "baseRev", "roots"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing JSON key %s: %s", key, data)
		}
	}
	for key := range fields {
		switch key {
		case "kind", "rev", "baseRev", "roots", "upsert", "remove", "subagentId":
		default:
			t.Fatalf("unexpected JSON key %s", key)
		}
	}
	var p stackPatch
	if err := json.Unmarshal(envelope.Update, &p); err != nil {
		t.Fatal(err)
	}
	if p.Kind != "stack_patch" {
		t.Fatalf("kind = %q", p.Kind)
	}
	return p
}

func TestStackSnapshotForKnownSession(t *testing.T) {
	m, st, notices := newStackTestManager(t)
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	snap := takeStack(t, m)
	if snap.SessionID != "s1" || snap.Rev != 1 || len(snap.Roots) != 1 || !strings.HasPrefix(snap.Roots[0], "turn:") {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(*notices) != 0 {
		t.Fatalf("notifications = %v", *notices)
	}
}

// The band travels as an extra node with a fixed ID. It is not in roots, so a
// transcript walk ignores it, but it must reach the client the same way.
func TestStackSnapshotCarriesTheBand(t *testing.T) {
	m, st, _ := newStackTestManager(t)
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	snap := takeStack(t, m)
	var band *viewmodel.WireNode
	for i := range snap.Nodes {
		if snap.Nodes[i].Kind == "band" {
			band = &snap.Nodes[i]
		}
	}
	if band == nil || band.ID != "band" || band.Band == nil {
		t.Fatalf("no band node in %+v", snap.Nodes)
	}
	for _, r := range snap.Roots {
		if r == "band" {
			t.Fatal("the band must not be a root: it is not part of the transcript")
		}
	}
}

func TestStackUnknownSession(t *testing.T) {
	m, _, _ := newStackTestManager(t)
	for _, id := range []string{"missing", "s1"} {
		if id == "s1" {
			m.lookup = func(string) (*TurnRuntime, bool) { return &TurnRuntime{}, true }
		}
		_, err := m.Stack(context.Background(), json.RawMessage(`{"sessionId":"`+id+`"}`))
		if err == nil || !strings.Contains(err.Error(), "unknown session") {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestStackFlushEmitsPatchAfterChange(t *testing.T) {
	m, st, notices := newStackTestManager(t)
	st.AddMessage(session.RoleUser, "first turn", session.ContentTypePlain)
	takeStack(t, m)
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.flushStack("s1", st, false)
	if len(*notices) != 1 {
		t.Fatalf("notifications = %d", len(*notices))
	}
	p := stackPatchFromNotice(t, (*notices)[0])
	if p.BaseRev != 1 || p.Rev != 2 || len(p.Roots) != 2 {
		t.Fatalf("patch = %+v", p)
	}
	var turn, message bool
	for _, n := range p.Upsert {
		turn = turn || n.Kind == "turn"
		message = message || (n.Message != nil && n.Message.Content == "hello")
	}
	if !turn || !message {
		t.Fatalf("upsert = %+v", p.Upsert)
	}
	data, _ := json.Marshal((*notices)[0].params)
	if strings.Contains(string(data), `"remove"`) {
		t.Fatalf("empty remove must be omitted: %s", data)
	}
}

func TestStackFlushWithoutChangeIsSilent(t *testing.T) {
	m, st, notices := newStackTestManager(t)
	takeStack(t, m)
	m.flushStack("s1", st, false)
	m.flushStack("s1", st, false)
	if len(*notices) != 0 {
		t.Fatalf("notifications = %v", *notices)
	}
}

func TestStackFlushBeforeActivationIsSilent(t *testing.T) {
	m, st, notices := newStackTestManager(t)
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.markStackDirty("s1")
	m.flushStack("s1", st, false)
	if len(*notices) != 0 {
		t.Fatalf("notifications = %v", *notices)
	}
}

func TestStackRemoveListsVanishedNodes(t *testing.T) {
	m, st, notices := newStackTestManager(t)
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	st.SetActiveToolCall(session.ActiveToolCall{Name: "file.read", ToolCallID: "c1", StartedAt: time.Now()})
	snap := takeStack(t, m)
	var runningID string
	for _, n := range snap.Nodes {
		if n.Tool != nil && n.Tool.Running != nil {
			runningID = n.ID
		}
	}
	if runningID == "" {
		t.Fatal("no running row in snapshot")
	}
	st.ClearActiveToolCallID("c1")
	m.flushStack("s1", st, false)
	if len(*notices) != 1 {
		t.Fatalf("notifications = %d", len(*notices))
	}
	p := stackPatchFromNotice(t, (*notices)[0])
	for _, id := range p.Remove {
		if id == runningID {
			return
		}
	}
	t.Fatalf("remove = %v, want %s", p.Remove, runningID)
}

func TestStackSnapshotJSONEmptyArrays(t *testing.T) {
	data, err := json.Marshal(StackSnapshot{SessionID: "s1", Rev: 1, Roots: []string{}, Nodes: []viewmodel.WireNode{}})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"sessionId":"s1","rev":1,"roots":[],"nodes":[]}` {
		t.Fatalf("snapshot JSON = %s", data)
	}
}

// The stack patch is the transport a live client actually sees, so the new
// detail must survive it: a client that receives the card through a patch can
// still ask for the node's typed execution evidence and get the same record a
// snapshot returns.
func TestStackPatchAndDetailAgreeOnExecutionEvidence(t *testing.T) {
	m, st, notices := newStackTestManager(t)
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	// Activate the session first, so the call below arrives as a patch rather
	// than being folded into the initial snapshot.
	takeStack(t, m)

	step := st.BeginStep(session.Actor{})
	st.LogToolCall(registry.AuditEvent{
		ToolName: "shell.run", StepID: int64(step), ToolCallID: "c1",
		Args: json.RawMessage(`{"command":"sleep 600","timeout_seconds":10}`),
		Notice: registry.NewCommandExecutionNotice(registry.CommandExecution{
			ExecutionID: "exec-patch", DisplayID: "job-1", ConversationID: "conv-1",
			State: "running", Attachment: registry.ExecutionAttachmentBackground,
			Wait: "10s", DetachReason: "wait expired",
		}),
	})
	m.flushStack("s1", st, false)
	if len(*notices) == 0 {
		t.Fatal("no patch was emitted")
	}
	patch := stackPatchFromNotice(t, (*notices)[len(*notices)-1])
	if len(patch.Upsert) == 0 {
		t.Fatalf("patch = %+v, want upserts", patch)
	}

	// The patch itself carries the card; the detail endpoint carries the
	// evidence for the same node.
	var toolID string
	for _, n := range patch.Upsert {
		if n.Tool != nil && n.Tool.Job != nil {
			toolID = n.ID
			if n.Tool.Job.ExecutionID != "exec-patch" {
				t.Fatalf("patch card = %+v, want the execution identity", n.Tool.Job)
			}
			if n.Tool.Job.Wait != "10s" || n.Tool.Job.DetachReason != "wait expired" {
				t.Fatalf("patch card = %+v, want the wait/handoff evidence", n.Tool.Job)
			}
		}
	}
	if toolID == "" {
		t.Fatalf("no job card in the patch: %+v", patch.Upsert)
	}

	got, err := callNodeID(t, m, toolID)
	if err != nil {
		t.Fatal(err)
	}
	ex := got["detail"].(NodeDetail).Calls[0].Execution
	if ex == nil || ex.ExecutionID != "exec-patch" {
		t.Fatalf("detail execution = %+v, want the same identity the patch carried", ex)
	}
	if ex.Attachment != "background" || ex.Wait != "10s" {
		t.Fatalf("detail execution = %+v", ex)
	}
}

// callNodeID is callNode for an already-resolved node ID rather than a tool key.
func callNodeID(t *testing.T, m *TurnManager, nodeID string) (map[string]any, error) {
	t.Helper()
	params, _ := json.Marshal(StackNodeParams{SessionID: "s1", NodeID: nodeID})
	v, err := m.StackNode(context.Background(), params)
	if err != nil {
		return nil, err
	}
	return v.(map[string]any), nil
}

func TestStackSnapshotFlushesThenAnswers(t *testing.T) {
	m, st, notices := newStackTestManager(t)
	first := takeStack(t, m)
	st.AddMessage(session.RoleUser, "new turn", session.ContentTypePlain)
	snap := takeStack(t, m)
	if len(*notices) != 1 {
		t.Fatalf("notifications = %d", len(*notices))
	}
	p := stackPatchFromNotice(t, (*notices)[0])
	if p.BaseRev != 1 || p.Rev != snap.Rev || snap.Rev != 2 {
		t.Fatalf("patch = %+v, snapshot = %+v", p, snap)
	}
	// The patch carries what changed; the snapshot is the union of that and
	// what the client already held. (The band rides along unchanged at this
	// rev, so an upsert-only comparison would be wrong.)
	have := map[string]bool{}
	for _, n := range first.Nodes {
		have[n.ID] = true
	}
	for _, n := range p.Upsert {
		have[n.ID] = true
	}
	for _, id := range p.Remove {
		delete(have, id)
	}
	if len(have) != len(snap.Nodes) {
		t.Fatalf("patch and snapshot disagree: patch reaches %d nodes, snapshot has %d (%+v)",
			len(have), len(snap.Nodes), snap.Nodes)
	}
	for _, n := range snap.Nodes {
		if !have[n.ID] {
			t.Fatalf("node %s is in the snapshot but neither held nor patched", n.ID)
		}
	}
	takeStack(t, m)
	if len(*notices) != 1 {
		t.Fatal("unchanged snapshot notified")
	}
}

// runStackTestTurn holds the runner open until a live patch arrives, so a
// finish-only flush cannot satisfy the during-turn assertion.
func runStackTestTurn(t *testing.T) ([]stackNotice, bool) {
	t.Helper()
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	broker := pubsub.NewBroker[session.Event]()
	st.SetEventBroker(broker)
	livePatch := make(chan struct{})
	var liveOnce sync.Once
	var mu sync.Mutex
	var notices []stackNotice
	rt := &TurnRuntime{SessionID: "s1", State: st, Events: broker, BeginWork: identityBeginWork}
	rt.Run = RunnerFunc(func(ctx context.Context, prompt string) error {
		st.AddMessage(session.RoleUser, prompt, session.ContentTypePlain)
		step := st.BeginStep(session.Actor{})
		st.AddMessage(session.RoleAssistant, "Reading the config loader.", session.ContentTypeNarration)
		st.LogToolCall(registry.AuditEvent{ToolName: "file.read", StepID: step, ToolCallID: "c1", ResultContent: "config"})
		select {
		case <-livePatch:
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
		st.EndStep(step)
		st.AddMessageFinal(session.RoleAssistant, "done", session.ContentTypePlain)
		return nil
	})
	m := NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) { return rt, id == "s1" },
		Notify: func(method string, params any) error {
			p := params.(SessionUpdateParams)
			mu.Lock()
			notices = append(notices, stackNotice{method, p})
			mu.Unlock()
			if p.Update["kind"] == "stack_patch" {
				for _, n := range p.Update["upsert"].([]viewmodel.WireNode) {
					if n.Live {
						liveOnce.Do(func() { close(livePatch) })
					}
				}
			}
			return nil
		},
	})
	takeStack(t, m)
	if _, err := m.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"s1","prompt":[{"type":"text","text":"hello"}]}`)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	select {
	case <-livePatch:
		return append([]stackNotice(nil), notices...), true
	default:
		return append([]stackNotice(nil), notices...), false
	}
}

func TestTurnFlushesStackPatches(t *testing.T) {
	notices, live := runStackTestTurn(t)
	if !live {
		t.Fatal("no stack patch arrived while the runner was live")
	}
	var patchSeen bool
	for _, n := range notices {
		switch n.params.Update["kind"] {
		case "stack_patch":
			stackPatchFromNotice(t, n)
			patchSeen = true
		case "session_telemetry":
			if !patchSeen {
				t.Fatal("telemetry arrived before a stack patch")
			}
			return
		}
	}
	t.Fatal("no session telemetry update")
}

func TestFinishTurnFlushSettlesLiveRows(t *testing.T) {
	notices, _ := runStackTestTurn(t)
	var last *stackPatch
	var telemetry bool
	for _, n := range notices {
		if n.params.Update["kind"] == "stack_patch" {
			p := stackPatchFromNotice(t, n)
			last = &p
		}
		if n.params.Update["kind"] == "session_telemetry" {
			telemetry = true
			break
		}
	}
	if !telemetry || last == nil {
		t.Fatal("no final stack patch before telemetry")
	}
	var receipt bool
	for _, n := range last.Upsert {
		if n.Live {
			t.Fatalf("final patch upserts live node %s", n.ID)
		}
		receipt = receipt || strings.HasPrefix(n.ID, "receipt:")
	}
	if !receipt {
		t.Fatalf("final patch has no receipt: %+v", last)
	}
}

func TestStackAfterTelemetryBeforeSlotReleaseStaysSettled(t *testing.T) {
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	broker := pubsub.NewBroker[session.Event]()
	st.SetEventBroker(broker)
	telemetry := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	defer func() {
		close(release)
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("prompt did not finish")
		}
	}()
	rt := &TurnRuntime{State: st, Events: broker, BeginWork: identityBeginWork}
	rt.Run = RunnerFunc(func(context.Context, string) error {
		st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
		step := st.BeginStep(session.Actor{})
		st.AddMessage(session.RoleAssistant, "working", session.ContentTypeNarration)
		st.EndStep(step)
		st.AddMessageFinal(session.RoleAssistant, "done", session.ContentTypePlain)
		return nil
	})
	m := NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) { return rt, id == "s1" },
		Notify: func(_ string, params any) error {
			if params.(SessionUpdateParams).Update["kind"] == "session_telemetry" {
				close(telemetry)
				<-release
			}
			return nil
		},
	})
	takeStack(t, m)
	go func() {
		_, err := m.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"s1","prompt":[{"type":"text","text":"hello"}]}`))
		done <- err
	}()
	select {
	case <-telemetry:
	case <-time.After(3 * time.Second):
		t.Fatal("no telemetry")
	}
	if !m.HasActiveTurn("s1") {
		t.Fatal("slot released before telemetry returned")
	}
	if _, err := m.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"s1","prompt":[{"type":"text","text":"duplicate"}]}`)); err == nil {
		t.Fatal("duplicate prompt accepted while slot reserved")
	}
	snap := takeStack(t, m)
	var receipt bool
	for _, n := range snap.Nodes {
		if n.Live {
			t.Errorf("settled snapshot revived live node %s", n.ID)
		}
		receipt = receipt || n.Receipt != nil
	}
	if !receipt {
		t.Error("settled snapshot lost receipt")
	}
}

func TestStackRequiresSessionID(t *testing.T) {
	m, _, _ := newStackTestManager(t)
	_, err := m.Stack(context.Background(), json.RawMessage(`{}`))
	if err == nil || err.Error() != "acp: session/stack requires sessionId" {
		t.Fatalf("error = %v", err)
	}
}

func TestStackSubagentSnapshotAndPatch(t *testing.T) {
	s := newSyncStack(t)
	s.st.AddMessage(session.RoleUser, "parent", session.ContentTypePlain)
	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	child.AddMessage(session.RoleUser, "child prompt", session.ContentTypePlain)
	v := s.st.RegisterSubagent("worker", child)

	params, _ := json.Marshal(StackParams{SessionID: "s1", SubagentID: v.ID})
	got, err := s.Stack(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	snap := got.(StackSnapshot)
	found := false
	for _, n := range snap.Nodes {
		found = found || (n.Message != nil && n.Message.Content == "child prompt")
	}
	if !found || snap.SubagentID != v.ID {
		t.Fatalf("snapshot = %+v", snap)
	}

	child.AddMessage(session.RoleUser, "second", session.ContentTypePlain)
	s.flushChildStacks("s1", s.st)
	ns := s.notices()
	if len(ns) != 1 {
		t.Fatalf("notices = %d", len(ns))
	}
	if patch := stackPatchFromNotice(t, ns[0]); patch.SubagentID != v.ID {
		t.Fatalf("patch subagentId = %d, want %d", patch.SubagentID, v.ID)
	}
}

func TestStackSubagentErrors(t *testing.T) {
	s := newSyncStack(t)
	s.st.RegisterSubagent("detached", nil)
	var id int64
	s.st.RegisterSubagent("x", nil)
	for _, sv := range s.st.Subagents() {
		id = sv.ID
	}
	for _, c := range []struct {
		id   int64
		want string
	}{{999999, "unknown subagent"}, {id, "no separate transcript"}} {
		params, _ := json.Marshal(StackParams{SessionID: "s1", SubagentID: c.id})
		if _, err := s.Stack(context.Background(), params); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("subagent %d: error = %v, want %q", c.id, err, c.want)
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStackIdleFlushWithoutTurn(t *testing.T) {
	s := newSyncStack(t)
	s.st.AddMessage(session.RoleUser, "first", session.ContentTypePlain)
	if _, err := s.Stack(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.dropStacks("s1") })
	s.st.AddMessage(session.RoleUser, "second", session.ContentTypePlain)
	s.broker.Publish(session.EventActivityChanged, session.Event{})
	waitFor(t, "idle stack_patch", func() bool { return len(s.notices()) > 0 })
	p := stackPatchFromNotice(t, s.notices()[0])
	if p.Kind != "stack_patch" {
		t.Fatalf("patch = %+v", p)
	}
}

func TestStackIdleStopsWhenSessionGone(t *testing.T) {
	s := newSyncStack(t)
	if _, err := s.Stack(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.gone = true
	s.mu.Unlock()
	waitFor(t, "projectors removed", func() bool {
		s.stacksMu.Lock()
		defer s.stacksMu.Unlock()
		return len(s.stacks) == 0
	})
}

func TestStackIdleStartsFromSubagentActivation(t *testing.T) {
	s := newSyncStack(t)
	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	child.AddMessage(session.RoleUser, "one", session.ContentTypePlain)
	v := s.st.RegisterSubagent("worker", child)
	params, _ := json.Marshal(StackParams{SessionID: "s1", SubagentID: v.ID})
	if _, err := s.Stack(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.dropStacks("s1") })
	child.AddMessage(session.RoleUser, "two", session.ContentTypePlain)
	waitFor(t, "idle child patch", func() bool { return len(s.notices()) > 0 })
	if p := stackPatchFromNotice(t, s.notices()[0]); p.SubagentID != v.ID {
		t.Fatalf("patch = %+v", p)
	}
}

// The band's stale tag can appear on elapsed time alone, with no session
// event to mark the projector dirty; a dirty-only flush must still run once
// the band's wake time passes.
func TestStackDirtyFlushRunsWhenBandWakeIsDue(t *testing.T) {
	m, st, notices := newStackTestManager(t)
	takeStack(t, m)
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain) // no broker: nothing marks dirty
	m.flushDirtyStack("s1", st)
	if len(*notices) != 0 {
		t.Fatalf("not dirty and no wake due: notifications = %d", len(*notices))
	}
	p := m.stackFor("s1")
	p.mu.Lock()
	p.wakeAt = time.Now().Add(-time.Second)
	p.mu.Unlock()
	m.flushDirtyStack("s1", st)
	if len(*notices) != 1 {
		t.Fatalf("wake due: notifications = %d, want 1", len(*notices))
	}
}
