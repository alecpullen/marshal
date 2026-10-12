package agent

import (
	"context"
	"encoding/json"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func toolsDisabledRunner(t *testing.T, p *agenttest.ScriptedProvider, native bool) *Runner {
	t.Helper()
	reg := registry.New()
	registerFakeRead(t, reg, false)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.NativeTools = native
	r.MaxRetries = 0
	return r
}

var toolsDisabledMsgs = []schema.ChatMessage{
	{Role: schema.RoleSystem, Content: "sys"},
	{Role: schema.RoleUser, Content: "summarize"},
}

// Finalize, handoff and plan calls send the loop's tool definitions with
// tool_choice "none", so their prompt prefix matches the loop's cache.
func TestToolsDisabledCallKeepsDefinitions(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"summary"}}
	r := toolsDisabledRunner(t, p, true)

	res, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil)
	if err != nil || res.Text != "summary" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	req := p.Requests[0]
	if req.ToolChoice != "none" {
		t.Fatalf("ToolChoice = %q, want none", req.ToolChoice)
	}
	want := r.buildToolDefinitions()
	if len(want) == 0 || len(req.Tools) != len(want) {
		t.Fatalf("tools = %d, want the loop's %d definitions", len(req.Tools), len(want))
	}
	for i := range want {
		if req.Tools[i].Name != want[i].Name {
			t.Fatalf("tool %d = %q, want %q", i, req.Tools[i].Name, want[i].Name)
		}
	}
}

// The loop's own calls are unchanged: tools on, no tool_choice.
func TestLoopCallSendsNoToolChoice(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"ok"}}
	r := toolsDisabledRunner(t, p, true)
	if _, err := r.chatWithRetry(context.Background(), p, "test-model", toolsDisabledMsgs, nil); err != nil {
		t.Fatal(err)
	}
	if p.Requests[0].ToolChoice != "" || len(p.Requests[0].Tools) == 0 {
		t.Fatalf("loop call: ToolChoice=%q tools=%d, want \"\" and >0", p.Requests[0].ToolChoice, len(p.Requests[0].Tools))
	}
}

// Review focus 2: a backend that rejects tool_choice gets one tool-less
// retry, and the latch holds for later tools-disabled calls.
func TestToolsDisabledCallFallsBackWhenToolChoiceRejected(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Errs:      []error{&provider.ProviderError{Provider: "scripted", StatusCode: 400, Body: `{"error":{"message":"tool_choice none is not supported"}}`}},
		Responses: []string{"", "summary", "again"},
	}
	r := toolsDisabledRunner(t, p, true)

	res, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil)
	if err != nil || res.Text != "summary" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(p.Requests) != 2 || p.Requests[1].Tools != nil || p.Requests[1].ToolChoice != "" {
		t.Fatalf("want one tool-less retry; requests=%d retry tools=%d choice=%q", len(p.Requests), len(p.Requests[len(p.Requests)-1].Tools), p.Requests[len(p.Requests)-1].ToolChoice)
	}
	if _, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil); err != nil {
		t.Fatal(err)
	}
	if len(p.Requests) != 3 || p.Requests[2].Tools != nil {
		t.Fatalf("latched provider must not be sent tools again; requests=%d", len(p.Requests))
	}
}

// Strict servers do not always name the field they reject. A generic 400
// on a call that sent tools must still fall back rather than fail
// finalize or the handoff summary.
func TestToolsDisabledCallFallsBackOnGenericRejection(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Errs:      []error{&provider.ProviderError{Provider: "scripted", StatusCode: 400, Body: `{"error":"invalid request"}`}},
		Responses: []string{"", "summary"},
	}
	r := toolsDisabledRunner(t, p, true)
	res, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil)
	if err != nil || res.Text != "summary" || len(p.Requests) != 2 || p.Requests[1].Tools != nil {
		t.Fatalf("res=%+v err=%v requests=%d", res, err, len(p.Requests))
	}
}

// A cancelled call is never retried.
func TestToolsDisabledCallDoesNotRetryCancellation(t *testing.T) {
	p := &agenttest.ScriptedProvider{Errs: []error{context.Canceled}, Responses: []string{"", "summary"}}
	r := toolsDisabledRunner(t, p, true)
	if _, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil); err == nil {
		t.Fatal("want the cancellation error back")
	}
	if len(p.Requests) != 1 || r.toolChoiceNoneRejected(p.Name()) {
		t.Fatalf("cancellation must not retry or latch; requests=%d", len(p.Requests))
	}
}

// Review focus 1: a backend that ignores tool_choice (Ollama-native has no
// such field) lets the model call a tool. Retry once without tools.
func TestToolsDisabledCallFallsBackWhenModelCallsTool(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{"", "summary"},
		ToolCalls: [][]schema.ToolCall{{{ID: "c1", Name: "file.read", Args: json.RawMessage(`{"path":"a.go"}`)}}},
	}
	r := toolsDisabledRunner(t, p, true)

	res, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil)
	if err != nil || res.Text != "summary" || len(res.ToolCalls) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(p.Requests) != 2 || p.Requests[1].Tools != nil {
		t.Fatalf("want one tool-less retry; requests=%d", len(p.Requests))
	}
}

// JSON-envelope mode never sends tool definitions; that is unchanged.
func TestToolsDisabledCallInEnvelopeModeSendsNoTools(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"summary"}}
	r := toolsDisabledRunner(t, p, false)
	if _, err := r.chatWithRetryNoNativeTools(context.Background(), p, "test-model", toolsDisabledMsgs, nil); err != nil {
		t.Fatal(err)
	}
	if p.Requests[0].Tools != nil || p.Requests[0].ToolChoice != "" {
		t.Fatalf("envelope mode: tools=%d choice=%q, want none", len(p.Requests[0].Tools), p.Requests[0].ToolChoice)
	}
}
