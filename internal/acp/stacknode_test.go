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

// A call's structured execution evidence reaches a client as typed data, not as
// prose: the coaching text a notice may carry stays in Notice, and the wait,
// handoff, stop and outcome facts travel in their own field so a client can
// explain an outcome without parsing a sentence.
func TestStackNodeCallDetailCarriesExecutionEvidence(t *testing.T) {
	s := newSyncStack(t)
	s.st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := s.st.BeginStep(session.Actor{})
	started := time.Unix(1700, 0).UTC()
	code := 3
	s.st.LogToolCall(registry.AuditEvent{
		ToolName: "shell.run", StepID: int64(step), ToolCallID: "c1",
		Args: json.RawMessage(`{"command":"pytest -q","timeout_seconds":10}`),
		Notice: registry.NewCommandExecutionNotice(registry.CommandExecution{
			Version: registry.CommandExecutionVersion,
			// A wait that handed off, then settled nonzero: the evidence has to
			// keep both facts, because the wait explains the handoff and the
			// exit explains the failure.
			ExecutionID: "exec-7", DisplayID: "job-4", ConversationID: "conv-1", Actor: "implementer",
			TurnID: "turn-1", StepID: "1", ToolCallID: "c1", Cwd: "/repo", Generation: 3, PID: 4242,
			StartedAt: started, EndedAt: started.Add(90 * time.Second),
			State: "failed", Outcome: "nonzero_exit", Attachment: registry.ExecutionAttachmentBackground,
			Wait: "10s", DetachReason: "wait expired", DetachedAt: started.Add(10 * time.Second),
			StopCause: "turn stop requested", StopRequestedAt: started.Add(80 * time.Second),
			ExitCode: &code, Truncated: true,
		}),
	})
	key := viewmodel.ToolKey(registry.AuditEvent{StepID: int64(step), ToolCallID: "c1"})
	got, err := callNode(t, s.TurnManager, key)
	if err != nil {
		t.Fatal(err)
	}
	detail := got["detail"].(NodeDetail)
	if len(detail.Calls) != 1 {
		t.Fatalf("calls = %+v", detail.Calls)
	}
	ex := detail.Calls[0].Execution
	if ex == nil {
		t.Fatal("the call detail carries no execution evidence")
	}
	if ex.ExecutionID != "exec-7" || ex.DisplayID != "job-4" || ex.ConversationID != "conv-1" {
		t.Fatalf("identity = %+v", ex)
	}
	if ex.State != "failed" || ex.Outcome != "nonzero_exit" || ex.Attachment != "background" {
		t.Fatalf("outcome = %+v", ex)
	}
	if ex.Wait != "10s" || ex.DetachReason != "wait expired" || ex.DetachedAt == 0 {
		t.Fatalf("handoff evidence = %+v", ex)
	}
	if ex.StopCause != "turn stop requested" || ex.StopRequestedAt == 0 {
		t.Fatalf("stop evidence = %+v", ex)
	}
	if ex.Exit == nil || *ex.Exit != 3 || !ex.Truncated {
		t.Fatalf("exit/truncation = %+v", ex)
	}
	if ex.StartedAt == 0 || ex.EndedAt == 0 || ex.PID != 4242 || ex.Cwd != "/repo" || ex.Generation != 3 {
		t.Fatalf("provenance = %+v", ex)
	}
}

// An unobserved exit must stay absent rather than becoming a zero a client
// renders as success — the start-up-failure case is the one that matters most.
func TestStackNodeExecutionEvidenceOmitsUnobservedExit(t *testing.T) {
	s := newSyncStack(t)
	s.st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := s.st.BeginStep(session.Actor{})
	s.st.LogToolCall(registry.AuditEvent{
		ToolName: "shell.run", StepID: int64(step), ToolCallID: "c2",
		Error: "command did not start",
		Notice: registry.NewCommandExecutionNotice(registry.CommandExecution{
			ExecutionID: "exec-8", DisplayID: "job-5", State: "failed", Outcome: "startup_failure",
			Attachment: registry.ExecutionAttachmentForeground, Wait: "10s",
		}),
	})
	key := viewmodel.ToolKey(registry.AuditEvent{StepID: int64(step), ToolCallID: "c2"})
	got, err := callNode(t, s.TurnManager, key)
	if err != nil {
		t.Fatal(err)
	}
	ex := got["detail"].(NodeDetail).Calls[0].Execution
	if ex == nil || ex.Exit != nil {
		t.Fatalf("execution = %+v, want no exit for a start-up failure", ex)
	}
	raw, err := json.Marshal(ex)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"exit"`) {
		t.Fatalf("an unobserved exit was sent: %s", raw)
	}
	if !strings.Contains(string(raw), `"outcome":"startup_failure"`) {
		t.Fatalf("outcome missing: %s", raw)
	}
}

// A call with no execution record at all must project no evidence object, so
// the addition stays additive for every other tool.
func TestStackNodeOmitsExecutionWithoutRecord(t *testing.T) {
	s := newSyncStack(t)
	s.st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := s.st.BeginStep(session.Actor{})
	s.st.LogToolCall(registry.AuditEvent{ToolName: "file.read", StepID: int64(step), ToolCallID: "c3",
		ResultContent: "contents", Notice: &registry.ToolNotice{Kind: registry.NoticeCappedResults, Text: "capped"}})
	key := viewmodel.ToolKey(registry.AuditEvent{StepID: int64(step), ToolCallID: "c3"})
	got, err := callNode(t, s.TurnManager, key)
	if err != nil {
		t.Fatal(err)
	}
	call := got["detail"].(NodeDetail).Calls[0]
	if call.Execution != nil {
		t.Fatalf("execution = %+v, want none", call.Execution)
	}
	// The coaching text stays where it was: the new field is separate from it.
	if call.Notice == nil || call.Notice.Text != "capped" {
		t.Fatalf("notice = %+v, want the coaching text unchanged", call.Notice)
	}
	raw, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"execution"`) {
		t.Fatalf("an empty execution object was sent: %s", raw)
	}
}

// Display text in the evidence is scrubbed on the way out, the same as every
// other field the projection carries: a stop cause is actor-supplied and a
// control sequence in it must not reach a client's renderer.
func TestStackNodeExecutionEvidenceScrubsDisplayText(t *testing.T) {
	s := newSyncStack(t)
	s.st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := s.st.BeginStep(session.Actor{})
	s.st.LogToolCall(registry.AuditEvent{
		ToolName: "shell.run", StepID: int64(step), ToolCallID: "c4",
		Notice: registry.NewCommandExecutionNotice(registry.CommandExecution{
			ExecutionID: "exec-9", DisplayID: "job-6",
			Cwd: "/repo\x1b]0;pwned\x07", DetachReason: "wait\r\nexpired",
			StopCause: "turn stop\x1b[2J requested",
		}),
	})
	key := viewmodel.ToolKey(registry.AuditEvent{StepID: int64(step), ToolCallID: "c4"})
	got, err := callNode(t, s.TurnManager, key)
	if err != nil {
		t.Fatal(err)
	}
	ex := got["detail"].(NodeDetail).Calls[0].Execution
	if ex == nil {
		t.Fatal("no execution evidence")
	}
	for name, got := range map[string]string{"cwd": ex.Cwd, "detachReason": ex.DetachReason, "stopCause": ex.StopCause} {
		if strings.ContainsAny(got, "\x1b\x07\r") {
			t.Fatalf("%s = %q, still carries a control character", name, got)
		}
	}
	if !strings.Contains(ex.Cwd, "/repo") || !strings.Contains(ex.StopCause, "turn stop") {
		t.Fatalf("scrubbing destroyed the text: cwd=%q stopCause=%q", ex.Cwd, ex.StopCause)
	}
}
