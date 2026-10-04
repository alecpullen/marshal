package acp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
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
		case "kind", "rev", "baseRev", "roots", "upsert", "remove":
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

func TestStackSnapshotFlushesThenAnswers(t *testing.T) {
	m, st, notices := newStackTestManager(t)
	takeStack(t, m)
	st.AddMessage(session.RoleUser, "new turn", session.ContentTypePlain)
	snap := takeStack(t, m)
	if len(*notices) != 1 {
		t.Fatalf("notifications = %d", len(*notices))
	}
	p := stackPatchFromNotice(t, (*notices)[0])
	if p.BaseRev != 1 || p.Rev != snap.Rev || snap.Rev != 2 || len(snap.Nodes) != len(p.Upsert) {
		t.Fatalf("patch = %+v, snapshot = %+v", p, snap)
	}
	takeStack(t, m)
	if len(*notices) != 1 {
		t.Fatal("unchanged snapshot notified")
	}
}

func TestStackRequiresSessionID(t *testing.T) {
	m, _, _ := newStackTestManager(t)
	_, err := m.Stack(context.Background(), json.RawMessage(`{}`))
	if err == nil || err.Error() != "acp: session/stack requires sessionId" {
		t.Fatalf("error = %v", err)
	}
}
