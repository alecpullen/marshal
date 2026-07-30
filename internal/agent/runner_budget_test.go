package agent

import (
	"context"
	"encoding/json"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// TestTruncatedResponsesDoNotStarveToolBudget is the regression test for the
// original complaint: a model whose responses kept hitting the output token
// limit burned the whole tool budget on retries and the turn died with
// "I ran out of tool budget" before doing any work. Truncation retries are
// overhead now, so the work budget survives them.
func TestTruncatedResponsesDoNotStarveToolBudget(t *testing.T) {
	state := newTestState(t)
	// Three truncated turns — each carries a tool call whose arguments were
	// cut off, so nothing runs and the call must be re-issued — then a real
	// answer. With a tool budget of 2 the old accounting would have
	// exhausted it on the truncations alone.
	truncated := []schema.ToolCall{{ID: "c1", Name: "noop", Args: json.RawMessage(`{}`)}}
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"cut off", "cut off", "cut off", "Done."},
		ToolCalls:     [][]schema.ToolCall{truncated, truncated, truncated, nil},
		FinishReasons: []string{"length", "length", "length", "stop"},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 2

	task, err := r.RunTask(context.Background(), "explain something")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty — truncation retries must not exhaust the tool budget", task.SalvagedReason)
	}
	if task.Summary != "Done." {
		t.Fatalf("Summary = %q, want %q", task.Summary, "Done.")
	}
}

// TestTodoListRaisesToolBudget covers the plan-following case. plan_first is
// off by default, so a multi-step task announces its size by writing a todo
// list a few iterations in — the ceiling must grow to match.
func TestTodoListRaisesToolBudget(t *testing.T) {
	state := newTestState(t)
	call := []schema.ToolCall{{ID: "c1", Name: "noop", Args: json.RawMessage(`{}`)}}
	p := &agenttest.ScriptedProvider{
		Responses: []string{"working", "working", "Done."},
		ToolCalls: [][]schema.ToolCall{call, call, nil},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	r.MaxToolIterations = 10

	if err := state.SetTodos([]db.TodoItem{{Content: "one"}, {Content: "two"}, {Content: "three"}}); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}

	if _, err := r.RunTask(context.Background(), "do three things"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	got := state.ToolBudget()
	if want := 10 + 3*planStepIterations; got.Max != want {
		t.Fatalf("ToolBudget.Max = %d, want %d (base + 3 todo steps)", got.Max, want)
	}
}

// TestTruncatedJSONActionsDoNotStarveToolBudget mirrors
// TestTruncatedResponsesDoNotStarveToolBudget for the JSON-action path:
// refusals are charged to overhead, so the work budget survives them.
// Two truncations against a budget of 2 would exhaust it if they were
// charged as work.
func TestTruncatedJSONActionsDoNotStarveToolBudget(t *testing.T) {
	state := newTestState(t)
	truncated := `{"rationale":"r","action":{"type":"tool_call","tool":"noop","args":{}}}`
	p := &agenttest.ScriptedProvider{
		Responses:     []string{truncated, truncated, `{"rationale":"r","action":{"type":"answer","content":"Done."}}`},
		FinishReasons: []string{"length", "length", "stop"},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 2

	task, err := r.RunTask(context.Background(), "explain something")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty — truncation refusals must not exhaust the tool budget", task.SalvagedReason)
	}
	if task.Summary != "Done." {
		t.Fatalf("Summary = %q, want %q", task.Summary, "Done.")
	}
}

// A turn whose every tool call was rejected for bad arguments did no work,
// so it must not spend a slot of the tool budget.
func TestAllInvalidArgsChargesOverheadNotTools(t *testing.T) {
	state := newTestState(t)
	reg, _ := strictRegistry(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"calling", "calling again", "Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "strict.read", Args: json.RawMessage(`{"wrong":"key"}`)}},
			{{ID: "c2", Name: "strict.read", Args: json.RawMessage(`{"path":"a.go"}`)}},
			nil,
		},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	r.MaxToolIterations = 10

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	if _, err := r.RunTask(context.Background(), "read something"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	// Two model turns issued tool calls, but only the second did any work.
	if b := state.ToolBudget(); b.Used != 1 {
		t.Fatalf("ToolBudget.Used = %d, want 1 (the rejected turn is overhead)", b.Used)
	}
	// The rejected turn still counts as a turn consumed overall.
	if got == nil || got.Iterations < 2 {
		t.Fatalf("Iterations = %v, want at least 2 (the rejected turn still happened)", got)
	}
}
