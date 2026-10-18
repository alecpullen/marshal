package agent

import (
	"context"
	"encoding/json"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// newEffortTestRunner wires a native-tools runner resolving to a static
// route whose preset effort is presetThinking, so the tests can watch the
// session override move the turn's effort.
func newEffortTestRunner(t *testing.T, state *session.State, p *agenttest.ScriptedProvider, presetThinking string) *Runner {
	t.Helper()
	reg := registry.New()
	registerFakeRead(t, reg, false)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	r.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "test", Provider: "scripted", Model: "test-model", Thinking: presetThinking},
	}}
	return r
}

func TestEffortOverrideAppliesAtNextStep(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"", "", "Answer."},
		ToolCalls:     [][]schema.ToolCall{{reasoningNativeCall("c1", "a.go")}, {reasoningNativeCall("c2", "b.go")}},
		FinishReasons: []string{"tool_calls", "tool_calls", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{Reasoning: true},
		OnChat: func(idx int, req schema.ChatRequest) {
			if idx == 0 {
				state.SetEffortOverride(session.EffortOverride{Level: "low", Provider: "scripted", Model: "test-model"})
			}
		},
	}
	r := newEffortTestRunner(t, state, p, "high")

	if _, err := r.RunTask(context.Background(), "read two files"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(p.Requests) < 3 {
		t.Fatalf("got %d requests, want at least 3", len(p.Requests))
	}
	if got := p.Requests[0].Thinking; got != "high" {
		t.Fatalf("request 1 effort = %q, want %q", got, "high")
	}
	if got := p.Requests[1].Thinking; got != "low" {
		t.Fatalf("request 2 effort = %q, want %q (override applies at the next step)", got, "low")
	}
	if got := p.Requests[2].Thinking; got != "low" {
		t.Fatalf("request 3 effort = %q, want %q", got, "low")
	}
	if !transcriptContains(state, "Effort → low from step") {
		t.Fatalf("transcript missing the step-change note")
	}
}

func TestEffortOffDeferredToNextTurn(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"", "", "Answer."},
		ToolCalls:     [][]schema.ToolCall{{reasoningNativeCall("c1", "a.go")}, {reasoningNativeCall("c2", "b.go")}},
		FinishReasons: []string{"tool_calls", "tool_calls", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{Reasoning: true},
		OnChat: func(idx int, req schema.ChatRequest) {
			if idx == 0 {
				state.SetEffortOverride(session.EffortOverride{Level: "off", Provider: "scripted", Model: "test-model"})
			}
		},
	}
	r := newEffortTestRunner(t, state, p, "high")

	if _, err := r.RunTask(context.Background(), "read two files"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(p.Requests) < 3 {
		t.Fatalf("got %d requests, want at least 3", len(p.Requests))
	}
	for i, req := range p.Requests {
		if req.Thinking != "high" {
			t.Fatalf("request %d effort = %q, want %q (off-boundary change must defer)", i+1, req.Thinking, "high")
		}
	}
	if !transcriptContains(state, "takes effect from the next turn") {
		t.Fatalf("transcript missing the deferred-effort note")
	}

	// A second turn reads the pending override from its very first request.
	p2 := &agenttest.ScriptedProvider{
		Responses:    []string{"Answer."},
		ProviderCaps: schema.ProviderCapabilities{Reasoning: true},
	}
	r2 := newEffortTestRunner(t, state, p2, "high")
	if _, err := r2.RunTask(context.Background(), "answer now"); err != nil {
		t.Fatalf("RunTask (second turn): %v", err)
	}
	if len(p2.Requests) == 0 {
		t.Fatal("provider was never called on the second turn")
	}
	if got := p2.Requests[0].Thinking; got != "off" {
		t.Fatalf("second-turn first request effort = %q, want %q", got, "off")
	}
}

func TestEffortClearRevertsToPreset(t *testing.T) {
	state := newTestState(t)
	state.SetEffortOverride(session.EffortOverride{Level: "low", Provider: "scripted", Model: "test-model"})
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"", "", "Answer."},
		ToolCalls:     [][]schema.ToolCall{{reasoningNativeCall("c1", "a.go")}, {reasoningNativeCall("c2", "b.go")}},
		FinishReasons: []string{"tool_calls", "tool_calls", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{Reasoning: true},
		OnChat: func(idx int, req schema.ChatRequest) {
			if idx == 0 {
				state.SetEffortOverride(session.EffortOverride{})
			}
		},
	}
	r := newEffortTestRunner(t, state, p, "high")

	if _, err := r.RunTask(context.Background(), "read two files"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(p.Requests) < 2 {
		t.Fatalf("got %d requests, want at least 2", len(p.Requests))
	}
	if got := p.Requests[0].Thinking; got != "low" {
		t.Fatalf("request 1 effort = %q, want %q", got, "low")
	}
	if got := p.Requests[1].Thinking; got != "high" {
		t.Fatalf("request 2 effort = %q, want %q (cleared override reverts to preset)", got, "high")
	}
}

func TestStaleEffortOverrideDropped(t *testing.T) {
	state := newTestState(t)
	state.SetEffortOverride(session.EffortOverride{Level: "low", Provider: "scripted", Model: "other-model"})
	p := &agenttest.ScriptedProvider{
		Responses:    []string{"Answer."},
		ProviderCaps: schema.ProviderCapabilities{Reasoning: true},
	}
	r := newEffortTestRunner(t, state, p, "high")

	if _, err := r.RunTask(context.Background(), "answer now"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(p.Requests) == 0 {
		t.Fatal("provider was never called")
	}
	if got := p.Requests[0].Thinking; got != "high" {
		t.Fatalf("request effort = %q, want %q (stale override must not apply)", got, "high")
	}
	if o := state.EffortOverride(); o.Level != "" {
		t.Fatalf("stale override = %+v, want cleared", o)
	}
	if !transcriptContains(state, "no longer applies") {
		t.Fatalf("transcript missing the stale-override note")
	}
}

func TestThinkingOverrideBeatsSessionOverride(t *testing.T) {
	state := newTestState(t)
	state.SetEffortOverride(session.EffortOverride{Level: "low", Provider: "scripted", Model: "test-model"})
	p := &agenttest.ScriptedProvider{
		Responses:    []string{"Answer."},
		ProviderCaps: schema.ProviderCapabilities{Reasoning: true},
	}
	r := newEffortTestRunner(t, state, p, "high")
	r.ThinkingOverride = "medium"

	if _, err := r.RunTask(context.Background(), "answer now"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(p.Requests) == 0 {
		t.Fatal("provider was never called")
	}
	if got := p.Requests[0].Thinking; got != "medium" {
		t.Fatalf("request effort = %q, want %q (ThinkingOverride wins)", got, "medium")
	}
	if o := state.EffortOverride(); o.Level != "low" {
		t.Fatalf("session override = %+v, want untouched low (ThinkingOverride is not the session override)", o)
	}
}

func TestTurnRouteSetDuringTurnClearedAfter(t *testing.T) {
	state := newTestState(t)
	var duringRunning bool
	var duringThinking string
	p := &agenttest.ScriptedProvider{
		Responses:    []string{"Answer."},
		ProviderCaps: schema.ProviderCapabilities{Reasoning: true},
		OnChat: func(idx int, req schema.ChatRequest) {
			if idx == 0 {
				turn, running := state.TurnRoute()
				duringRunning = running
				duringThinking = turn.Thinking
			}
		},
	}
	r := newEffortTestRunner(t, state, p, "high")

	if _, err := r.RunTask(context.Background(), "answer now"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if !duringRunning {
		t.Fatal("TurnRoute was not set during the turn")
	}
	if duringThinking != "high" {
		t.Fatalf("TurnRoute.Thinking during turn = %q, want %q", duringThinking, "high")
	}
	if _, running := state.TurnRoute(); running {
		t.Fatal("TurnRoute still set after the turn finished")
	}
}

func TestReasoningItemsStillSentAfterEffortChange(t *testing.T) {
	state := newTestState(t)
	item := schema.ReasoningItem{
		Kind: schema.ReasoningKindOpenAI,
		Data: json.RawMessage(`{"id":"rs_1","encrypted_content":"abc"}`),
	}
	p := &agenttest.ScriptedProvider{
		Responses:      []string{"", "", "Answer."},
		ToolCalls:      [][]schema.ToolCall{{reasoningNativeCall("c1", "a.go")}, {reasoningNativeCall("c2", "b.go")}},
		FinishReasons:  []string{"tool_calls", "tool_calls", "stop"},
		ReasoningItems: [][]schema.ReasoningItem{{item}},
		ProviderCaps:   schema.ProviderCapabilities{Reasoning: true},
		OnChat: func(idx int, req schema.ChatRequest) {
			if idx == 0 {
				state.SetEffortOverride(session.EffortOverride{Level: "low", Provider: "scripted", Model: "test-model"})
			}
		},
	}
	r := newEffortTestRunner(t, state, p, "high")

	if _, err := r.RunTask(context.Background(), "read two files"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(p.Requests) < 2 {
		t.Fatalf("got %d requests, want at least 2", len(p.Requests))
	}
	// The effort changed for request 2; the reasoning produced before the
	// change must still be replayed onto the assistant message it belongs to.
	if got := p.Requests[1].Thinking; got != "low" {
		t.Fatalf("request 2 effort = %q, want %q", got, "low")
	}
	msg := assistantToolCallMessage(t, p.Requests[1])
	if msg.Reasoning == nil || len(msg.Reasoning.Items) != 1 {
		t.Fatalf("request 2 assistant reasoning = %+v, want exactly one item", msg.Reasoning)
	}
}
