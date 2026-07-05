package agent

import (
	"context"
	"testing"

	"marshal/internal/llm/schema"
)

func TestFinalizeProducesFlaggedCompletion(t *testing.T) {
	state := newTestState(t)
	prov := &scriptedProvider{responses: []string{
		`{"rationale":"done","action":{"type":"final","content":"Here is my best answer."}}`,
	}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonExhausted)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Status != TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	if got.SalvagedReason != "exhausted" {
		t.Fatalf("SalvagedReason = %q, want exhausted", got.SalvagedReason)
	}
	last := state.Messages()[len(state.Messages())-1]
	if !last.Salvaged || last.Content != "Here is my best answer." {
		t.Fatalf("final message = %+v, want salvaged answer", last)
	}
}

func TestFinalizeSynthesizesWhenModelIgnoresDirective(t *testing.T) {
	state := newTestState(t)
	prov := &scriptedProvider{responses: []string{
		`{"rationale":"one more read","action":{"type":"tool_call","tool":"file.read","args":{"path":"x.go"}}}`,
	}}
	r := NewRunner(prov, nil, nil, state, "test-model")

	task := NewTask("do the thing", r.Now())
	task.Plan = []string{"Read x.go", "Patch it"}
	msgs := []schema.ChatMessage{{Role: schema.RoleUser, Content: "do the thing"}}

	got, err := r.finalize(context.Background(), prov, "test-model", msgs, task, reasonStalled)
	if err != nil {
		t.Fatalf("finalize err = %v, want nil", err)
	}
	if got.Status != TaskStatusCompleted || got.SalvagedReason != "stalled" {
		t.Fatalf("task = %+v, want completed+stalled", got)
	}
	last := state.Messages()[len(state.Messages())-1]
	if !last.Salvaged || last.Content == "" {
		t.Fatalf("expected non-empty synthesized salvage message, got %+v", last)
	}
}
