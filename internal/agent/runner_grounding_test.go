package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// A prose-only response with zero tool calls, for an edit-class task, with
// no tool call yet this turn, must NOT be accepted as a completed answer —
// the model fabricated changes it never made in the live ACP investigation
// this test guards against (minimax-m3, turns 4-5 of a continued session).
func TestNativeEditTaskRequiresToolCallBeforeCompletion(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly, Handler: nopHandler}); err != nil {
		t.Fatalf("register: %v", err)
	}
	p := &agenttest.ScriptedProvider{
		Responses: []string{"All done, I fixed the bug.", "Verified via test.", "All done, confirmed."},
		ToolCalls: [][]schema.ToolCall{
			nil, // fabricated completion attempt: no tool call, must be rejected
			{{ID: "c1", Name: "file.read", Args: json.RawMessage(`{"path":"foo.go"}`)}},
			nil, // now a real tool call has happened this turn: prose completion is accepted
		},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))

	task, err := r.RunTask(context.Background(), "fix the off-by-one bug in foo.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("Status = %v, want Completed", task.Status)
	}
	if task.Summary != "All done, confirmed." {
		t.Fatalf("Summary = %q, want the THIRD response (first was rejected, second had the real tool call)", task.Summary)
	}
	if p.Calls != 3 {
		t.Fatalf("provider Calls = %d, want 3 (reject, tool-call, accept)", p.Calls)
	}
	foundNudge := false
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, "have not made any tool calls") {
			foundNudge = true
		}
	}
	if !foundNudge {
		t.Fatal("expected a grounding-nudge system message in the transcript")
	}
}

// A prose-only response for a QUESTION-class task must complete immediately
// with zero tool calls — the grounding check must not fire for tasks that
// legitimately need no tool use.
func TestNativeQuestionTaskCompletesWithoutToolCall(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Subtotal sums Price*Quantity across all items."},
		ToolCalls: [][]schema.ToolCall{nil},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "what does Subtotal do?")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("Status = %v, want Completed", task.Status)
	}
	if p.Calls != 1 {
		t.Fatalf("provider Calls = %d, want 1 (no nudge for a question-class task)", p.Calls)
	}
}

// If the model STILL returns no tool call after one nudge, the turn must
// complete anyway (bounded to one nudge, never a new infinite loop) — but
// the answer must be flagged as unverified rather than accepted as a clean,
// trusted completion. Live testing against minimax-m3 showed a model that
// ignores the nudge tends to produce a MORE confident fabrication on retry,
// not a more honest one, so a second or third nudge is not the fix; making
// the failure visible is.
func TestNativeGroundingNudgeFiresAtMostOnceThenSalvages(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Done.", "Still done, no tools needed."},
		ToolCalls: [][]schema.ToolCall{nil, nil},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))

	task, err := r.RunTask(context.Background(), "fix the bug")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("Status = %v, want Completed", task.Status)
	}
	if p.Calls != 2 {
		t.Fatalf("provider Calls = %d, want 2 (one nudge, then accept regardless)", p.Calls)
	}
	if task.Summary != "Still done, no tools needed." {
		t.Fatalf("Summary = %q, want the second response", task.Summary)
	}
	if task.SalvagedReason != string(reasonUnverified) {
		t.Fatalf("SalvagedReason = %q, want %q", task.SalvagedReason, reasonUnverified)
	}
	var finalMsg *session.Message
	msgs := state.Messages()
	for i := range msgs {
		if msgs[i].Role == session.RoleAssistant && msgs[i].Final {
			finalMsg = &msgs[i]
		}
	}
	if finalMsg == nil {
		t.Fatal("expected a final assistant message in the transcript")
	}
	if !finalMsg.Salvaged {
		t.Fatalf("final message Salvaged = %v, want true (unverified completions must not look like clean answers)", finalMsg.Salvaged)
	}
	if finalMsg.SalvageReason != string(reasonUnverified) {
		t.Fatalf("final message SalvageReason = %q, want %q", finalMsg.SalvageReason, reasonUnverified)
	}
}

// A grounded completion (at least one real tool call happened this turn)
// must NOT be salvaged, even if the model's own final response has zero
// tool calls — salvaging is only for the case where NO tool call happened
// anywhere in the turn.
func TestNativeGroundedCompletionIsNotSalvaged(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly, Handler: nopHandler}); err != nil {
		t.Fatalf("register: %v", err)
	}
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Verified via read.", "All done, confirmed."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "file.read", Args: json.RawMessage(`{"path":"foo.go"}`)}},
			nil,
		},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))

	task, err := r.RunTask(context.Background(), "fix the bug")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty (a real tool call happened this turn)", task.SalvagedReason)
	}
	for _, m := range state.Messages() {
		if m.Role == session.RoleAssistant && m.Final && m.Salvaged {
			t.Fatalf("final message was salvaged unexpectedly: %+v", m)
		}
	}
}
