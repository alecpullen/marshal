package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// A hold set while a tool runs lets the agent finish that call, then parks
// it before the next model call until the hold is released.
func TestHoldParksBeforeNextModelCall(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"r","action":{"type":"tool_call","tool":"hold.test","args":{}}}`,
			`{"rationale":"r","action":{"type":"final","summary":"done"}}`,
		},
		ToolCalls: [][]schema.ToolCall{{{ID: "tc1", Name: "hold.test", Args: json.RawMessage(`{}`)}}},
	}
	var modelCalls atomic.Int32
	p.OnChat = func(int, schema.ChatRequest) { modelCalls.Add(1) }
	providerCalls := func(*agenttest.ScriptedProvider) int { return int(modelCalls.Load()) }
	reg := registry.New()
	state := newTestState(t)
	reg.Register(registry.Tool{
		Name: "hold.test", Description: "test tool", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			state.SetHold(true)
			return registry.ToolResult{Summary: "ran"}, nil
		},
	})
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background(), "go") }()

	select {
	case err := <-done:
		t.Fatalf("Run finished while held: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if calls := providerCalls(p); calls != 1 {
		t.Fatalf("model calls while held = %d, want 1", calls)
	}
	state.SetHold(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not resume after release")
	}
	if calls := providerCalls(p); calls != 2 {
		t.Fatalf("model calls after release = %d, want 2", calls)
	}
}

// executeToolCall waits out a hold before touching the tool.
func TestHoldParksToolCall(t *testing.T) {
	reg := registry.New()
	ran := make(chan struct{}, 1)
	reg.Register(registry.Tool{
		Name: "hold.test", Description: "test tool", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			ran <- struct{}{}
			return registry.ToolResult{Summary: "ran"}, nil
		},
	})
	state := newTestState(t)
	runner := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	state.SetHold(true)
	done := make(chan struct{})
	go func() {
		_, _ = runner.executeToolCall(context.Background(), ModelAction{Tool: "hold.test", ToolCallID: "tc1", Args: json.RawMessage(`{}`)})
		close(done)
	}()
	select {
	case <-ran:
		t.Fatal("tool ran while held")
	case <-time.After(100 * time.Millisecond):
	}
	state.SetHold(false)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tool call did not resume after release")
	}
	select {
	case <-ran:
	default:
		t.Fatal("tool never ran")
	}
}

