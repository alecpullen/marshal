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

// syncStack is a stack test manager whose notices are safe to read while the
// idle flusher runs.
type syncStack struct {
	*TurnManager
	st     *session.State
	broker *pubsub.Broker[session.Event]
	mu     sync.Mutex
	got    []stackNotice
	gone   bool
}

func newSyncStack(t *testing.T) *syncStack {
	t.Helper()
	s := &syncStack{
		st:     session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}),
		broker: pubsub.NewBroker[session.Event](),
	}
	s.TurnManager = NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) {
			s.mu.Lock()
			defer s.mu.Unlock()
			return &TurnRuntime{State: s.st, Events: s.broker}, id == "s1" && !s.gone
		},
		Notify: func(method string, params any) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.got = append(s.got, stackNotice{method, params.(SessionUpdateParams)})
			return nil
		},
	})
	return s
}

func (s *syncStack) notices() []stackNotice {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stackNotice(nil), s.got...)
}

func callNode(t *testing.T, m *TurnManager, nodeID string) (map[string]any, error) {
	t.Helper()
	params, _ := json.Marshal(StackNodeParams{SessionID: "s1", NodeID: nodeID})
	v, err := m.StackNode(context.Background(), params)
	if err != nil {
		return nil, err
	}
	return v.(map[string]any), nil
}

func TestStackNodeToolReturnsFullOutput(t *testing.T) {
	s := newSyncStack(t)
	s.st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := s.st.BeginStep(session.Actor{})
	big := strings.Repeat("x", viewmodel.WireTextCap*3)
	s.st.LogToolCall(registry.AuditEvent{ToolName: "shell.run", StepID: int64(step), ToolCallID: "c1",
		Args: json.RawMessage(`{"command":"ls"}`), ResultContent: big})
	key := viewmodel.ToolKey(registry.AuditEvent{StepID: int64(step), ToolCallID: "c1"})
	got, err := callNode(t, s.TurnManager, key)
	if err != nil {
		t.Fatal(err)
	}
	detail := got["detail"].(NodeDetail)
	if len(detail.Calls) != 1 || detail.Calls[0].Output != big {
		t.Fatalf("calls = %+v", detail.Calls)
	}
	if !strings.Contains(detail.Calls[0].Args, "\n") {
		t.Fatalf("args not indented: %q", detail.Calls[0].Args)
	}
	if got["node"].(viewmodel.WireNode).Parent == "" {
		t.Fatal("node lost its parent")
	}
}

func TestStackNodeStepReturnsNarrationAndThinking(t *testing.T) {
	s := newSyncStack(t)
	s.st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := s.st.BeginStep(session.Actor{})
	s.st.AddNarration(step, "Looking around.")
	s.st.LogThinking(session.ThinkingEntry{Text: "hmm", StartedAt: time.Now(), StepID: step})
	s.st.LogToolCall(registry.AuditEvent{ToolName: "file.read", StepID: int64(step), ToolCallID: "c1"})
	got, err := callNode(t, s.TurnManager, "step:"+itoa(int64(step)))
	if err != nil {
		t.Fatal(err)
	}
	detail := got["detail"].(NodeDetail)
	if len(detail.Narration) != 1 || detail.Narration[0] != "Looking around." || len(detail.Thinking) != 1 || detail.Thinking[0].Text != "hmm" {
		t.Fatalf("detail = %+v", detail)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestStackNodeUnknownNodeAndSession(t *testing.T) {
	s := newSyncStack(t)
	if _, err := callNode(t, s.TurnManager, "nope"); err == nil || !strings.Contains(err.Error(), "unknown node") {
		t.Fatalf("error = %v", err)
	}
	params, _ := json.Marshal(StackNodeParams{SessionID: "zzz", NodeID: "x"})
	if _, err := s.StackNode(context.Background(), params); err == nil || !strings.Contains(err.Error(), "unknown session") {
		t.Fatalf("error = %v", err)
	}
}
