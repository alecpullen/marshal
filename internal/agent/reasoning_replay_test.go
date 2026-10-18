package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// reasoningNativeCall builds a native tool call to the fake file.read.
func reasoningNativeCall(id, path string) schema.ToolCall {
	return schema.ToolCall{ID: id, Name: "file.read", Args: json.RawMessage(`{"path":"` + path + `"}`)}
}

// assistantToolCallMessage returns the assistant message carrying tool calls
// from a captured request — the message the previous step appended.
func assistantToolCallMessage(t *testing.T, req schema.ChatRequest) schema.ChatMessage {
	t.Helper()
	for _, m := range req.Messages {
		if m.Role == schema.RoleAssistant && len(m.ToolCalls) > 0 {
			return m
		}
	}
	t.Fatalf("no assistant tool-call message in request: %+v", req.Messages)
	return schema.ChatMessage{}
}

// TestReasoningReplayedOnNextRequest: the thinking text a step produced rides
// onto the assistant message it appended, so the next request replays it.
func TestReasoningReplayedOnNextRequest(t *testing.T) {
	reg := registry.New()
	registerFakeRead(t, reg, false)
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"", "", "Answer."},
		ToolCalls:     [][]schema.ToolCall{{reasoningNativeCall("c1", "a.go")}, {reasoningNativeCall("c2", "b.go")}},
		FinishReasons: []string{"tool_calls", "tool_calls", "stop"},
		Thinking:      []string{"plan A", "", ""},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))

	if _, err := r.RunTask(context.Background(), "read two files"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(p.Requests) < 2 {
		t.Fatalf("got %d requests, want at least 2", len(p.Requests))
	}
	msg := assistantToolCallMessage(t, p.Requests[1])
	if msg.Reasoning == nil || msg.Reasoning.Text != "plan A" {
		t.Fatalf("request 2 assistant reasoning = %+v, want text %q", msg.Reasoning, "plan A")
	}
}

// TestReasoningItemsStampedWithProviderAndModel: opaque items reach the next
// request stamped with the provider and model that produced them, so replay
// can match them.
func TestReasoningItemsStampedWithProviderAndModel(t *testing.T) {
	reg := registry.New()
	registerFakeRead(t, reg, false)
	item := schema.ReasoningItem{
		Kind: schema.ReasoningKindOpenAI,
		Data: json.RawMessage(`{"id":"rs_1","encrypted_content":"abc"}`),
	}
	p := &agenttest.ScriptedProvider{
		Responses:      []string{"", "", "Answer."},
		ToolCalls:      [][]schema.ToolCall{{reasoningNativeCall("c1", "a.go")}, {reasoningNativeCall("c2", "b.go")}},
		FinishReasons:  []string{"tool_calls", "tool_calls", "stop"},
		ReasoningItems: [][]schema.ReasoningItem{{item}},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))

	if _, err := r.RunTask(context.Background(), "read two files"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(p.Requests) < 2 {
		t.Fatalf("got %d requests, want at least 2", len(p.Requests))
	}
	msg := assistantToolCallMessage(t, p.Requests[1])
	if msg.Reasoning == nil || len(msg.Reasoning.Items) != 1 {
		t.Fatalf("request 2 assistant reasoning = %+v, want exactly one item", msg.Reasoning)
	}
	got := msg.Reasoning.Items[0]
	if got.Provider != "scripted" || got.Model != "test-model" {
		t.Fatalf("item stamp = %q/%q, want scripted/test-model", got.Provider, got.Model)
	}
	if got.Kind != schema.ReasoningKindOpenAI || string(got.Data) != string(item.Data) {
		t.Fatalf("item payload = %+v, want %+v", got, item)
	}
}

// TestNoReasoningFromLengthTruncatedStep: a step cut off at the output-token
// limit never has its reasoning replayed.
func TestNoReasoningFromLengthTruncatedStep(t *testing.T) {
	reg := registry.New()
	registerFakeRead(t, reg, false)
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"truncated plan", "", ""},
		ToolCalls:     [][]schema.ToolCall{{reasoningNativeCall("c1", "a.go")}},
		FinishReasons: []string{"length", "tool_calls", "stop"},
		Thinking:      []string{"x", "", ""},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))

	if _, err := r.RunTask(context.Background(), "read a file"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(p.Requests) < 2 {
		t.Fatalf("got %d requests, want at least 2", len(p.Requests))
	}
	msg := assistantToolCallMessage(t, p.Requests[1])
	if msg.Reasoning != nil {
		t.Fatalf("request 2 assistant reasoning = %+v, want nil for a truncated step", msg.Reasoning)
	}
}

// TestEstimateTokensCountsReasoning: reasoning text and opaque item payloads
// travel on the wire, so the prompt-size estimate must count them.
func TestEstimateTokensCountsReasoning(t *testing.T) {
	text := strings.Repeat("r", 400)
	data := json.RawMessage(`{"encrypted_content":"` + strings.Repeat("e", 396) + `"}`)

	without := []schema.ChatMessage{{Role: schema.RoleAssistant, Content: "hi"}}
	with := []schema.ChatMessage{{
		Role:      schema.RoleAssistant,
		Content:   "hi",
		Reasoning: &schema.Reasoning{Text: text, Items: []schema.ReasoningItem{{Kind: schema.ReasoningKindOpenAI, Data: data}}},
	}}

	got := estimateTokens(with)
	want := estimateTokens(without) + (len(text)+len(data))/4
	if got != want {
		t.Fatalf("estimateTokens = %d, want %d (reasoning text + item data counted)", got, want)
	}
}

// TestTrimReplayedReasoningOldestFirstKeepsLatest: trimming clears whole
// assistant messages oldest first, stops once over reports false, and never
// touches the most recent assistant message.
func TestTrimReplayedReasoningOldestFirstKeepsLatest(t *testing.T) {
	msgs := []schema.ChatMessage{
		{Role: schema.RoleAssistant, Content: "a", Reasoning: &schema.Reasoning{Text: "ra"}},
		{Role: schema.RoleTool, Content: "t"},
		{Role: schema.RoleAssistant, Content: "b", Reasoning: &schema.Reasoning{Text: "rb"}},
		{Role: schema.RoleAssistant, Content: "c", Reasoning: &schema.Reasoning{Text: "rc"}},
	}
	// over is true until two assistant reasonings have been cleared.
	over := func(m []schema.ChatMessage) bool {
		n := 0
		for _, msg := range m {
			if msg.Role == schema.RoleAssistant && msg.Reasoning == nil {
				n++
			}
		}
		return n < 2
	}
	out, trimmed := trimReplayedReasoning(msgs, over)
	if trimmed != 2 {
		t.Fatalf("trimmed = %d, want 2", trimmed)
	}
	if out[0].Reasoning != nil {
		t.Fatalf("first assistant reasoning = %+v, want nil (oldest trimmed first)", out[0].Reasoning)
	}
	if out[2].Reasoning != nil {
		t.Fatalf("second assistant reasoning = %+v, want nil (oldest trimmed first)", out[2].Reasoning)
	}
	if out[3].Reasoning == nil || out[3].Reasoning.Text != "rc" {
		t.Fatalf("latest assistant reasoning = %+v, want kept %q", out[3].Reasoning, "rc")
	}
	if msgs[0].Reasoning == nil {
		t.Fatalf("input slice was modified")
	}
}

// TestTrimReplayedReasoningNeverTrimsLatest: even when over stays true
// forever, the latest assistant message keeps its reasoning.
func TestTrimReplayedReasoningNeverTrimsLatest(t *testing.T) {
	msgs := []schema.ChatMessage{
		{Role: schema.RoleAssistant, Content: "a", Reasoning: &schema.Reasoning{Text: "ra"}},
		{Role: schema.RoleAssistant, Content: "b", Reasoning: &schema.Reasoning{Text: "rb"}},
		{Role: schema.RoleTool, Content: "t"},
		{Role: schema.RoleAssistant, Content: "c", Reasoning: &schema.Reasoning{Text: "rc"}},
	}
	out, trimmed := trimReplayedReasoning(msgs, func([]schema.ChatMessage) bool { return true })
	if trimmed != 2 {
		t.Fatalf("trimmed = %d, want 2", trimmed)
	}
	if out[0].Reasoning != nil || out[1].Reasoning != nil {
		t.Fatalf("older reasonings = %+v / %+v, want both nil", out[0].Reasoning, out[1].Reasoning)
	}
	if out[3].Reasoning == nil || out[3].Reasoning.Text != "rc" {
		t.Fatalf("latest assistant reasoning = %+v, want kept %q", out[3].Reasoning, "rc")
	}
}
