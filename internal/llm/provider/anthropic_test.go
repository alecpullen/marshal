package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/llm/schema"
)

func newTestAnthropic(t *testing.T, baseURL string) *Anthropic {
	t.Helper()
	p, err := NewAnthropic(Options{Name: "test-anthropic", BaseURL: baseURL, APIKey: "sk-test"})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	return p
}

func TestAnthropicChatHeadersAndBody(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "sk-test" {
			t.Fatalf("x-api-key = %q, want sk-test", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version = %q, want 2023-06-01", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization header must not be set, got %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	defer server.Close()

	p := newTestAnthropic(t, server.URL)
	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model: "claude-sonnet-4-5",
		Messages: []schema.ChatMessage{
			{Role: schema.RoleSystem, Content: "be terse"},
			{Role: schema.RoleUser, Content: "hi"},
		},
		Tools: []schema.ToolDefinition{{
			Name:        "read_file",
			Description: "read a file",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}

	if got["max_tokens"] != float64(8192) {
		t.Fatalf("max_tokens = %v, want default 8192", got["max_tokens"])
	}
	if _, ok := got["num_ctx"]; ok {
		t.Fatal("anthropic request should not include num_ctx")
	}
	system, ok := got["system"].([]any)
	if !ok || len(system) != 1 {
		t.Fatalf("system = %v, want 1 block", got["system"])
	}
	sysBlock := system[0].(map[string]any)
	if sysBlock["text"] != "be terse" {
		t.Fatalf("system text = %v", sysBlock["text"])
	}
	cc, ok := sysBlock["cache_control"].(map[string]any)
	if !ok || cc["type"] != "ephemeral" {
		t.Fatalf("system block missing cache_control ephemeral: %v", sysBlock)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages = %v, want one user message (system extracted)", got["messages"])
	}
	tools := got["tools"].([]any)
	tool0 := tools[0].(map[string]any)
	if tool0["name"] != "read_file" {
		t.Fatalf("tools[0].name = %v", tool0["name"])
	}
	if _, ok := tool0["input_schema"]; !ok {
		t.Fatalf("tools[0] missing input_schema: %v", tool0)
	}
	if cc, ok := tool0["cache_control"].(map[string]any); !ok || cc["type"] != "ephemeral" {
		t.Fatalf("last tool missing cache_control ephemeral: %v", tool0)
	}
}

func TestAnthropicToolUseRoundTripConversion(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	p := newTestAnthropic(t, server.URL)
	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model: "claude-sonnet-4-5",
		Messages: []schema.ChatMessage{
			{Role: schema.RoleUser, Content: "read a.go"},
			{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "toolu_1", Name: "read_file", Args: json.RawMessage(`{"path":"a.go"}`)}}},
			{Role: schema.RoleTool, ToolCallID: "toolu_1", Content: "package a"},
			{Role: schema.RoleTool, ToolCallID: "toolu_2", Content: "second result"},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}

	msgs := got["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v, want 3 (user, assistant, merged user tool_results)", msgs)
	}
	assistant := msgs[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("msgs[1].role = %v", assistant["role"])
	}
	aBlocks := assistant["content"].([]any)
	if len(aBlocks) != 1 || aBlocks[0].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("assistant content = %v, want one tool_use block", aBlocks)
	}
	toolUse := aBlocks[0].(map[string]any)
	if toolUse["id"] != "toolu_1" || toolUse["name"] != "read_file" {
		t.Fatalf("tool_use block = %v", toolUse)
	}
	toolResults := msgs[2].(map[string]any)["content"].([]any)
	if len(toolResults) != 2 {
		t.Fatalf("consecutive tool results must merge into one user message, got %v", toolResults)
	}
	tr0 := toolResults[0].(map[string]any)
	if tr0["type"] != "tool_result" || tr0["tool_use_id"] != "toolu_1" || tr0["content"] != "package a" {
		t.Fatalf("tool_result block = %v", tr0)
	}
}

func TestAnthropicChatNonStreamingEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"toolu_9","name":"read_file","input":{"path":"c.go"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":8,"cache_creation_input_tokens":2}}`))
	}))
	defer server.Close()

	p := newTestAnthropic(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	ev1, _ := recvEvent(t, events)
	if ev1.Type != schema.ChatEventDelta || ev1.Delta != "hello" {
		t.Fatalf("event 1 = %+v, want Delta %q", ev1, "hello")
	}
	ev2, _ := recvEvent(t, events)
	if ev2.Type != schema.ChatEventDone {
		t.Fatalf("event 2 = %+v, want Done", ev2)
	}
	if ev2.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls (mapped from tool_use)", ev2.FinishReason)
	}
	if ev2.Usage == nil {
		t.Fatal("Usage is nil")
	}
	// PromptTokens = input + cache_read + cache_creation (Anthropic excludes
	// cached tokens from input_tokens; schema semantics include them).
	if ev2.Usage.PromptTokens != 20 || ev2.Usage.CompletionTokens != 5 || ev2.Usage.TotalTokens != 25 {
		t.Fatalf("Usage = %+v, want prompt=20 completion=5 total=25", ev2.Usage)
	}
	if ev2.Usage.CacheReadTokens != 8 || ev2.Usage.CacheWriteTokens != 2 {
		t.Fatalf("cache tokens = %+v, want read=8 write=2", ev2.Usage)
	}
	if len(ev2.ToolCalls) != 1 || ev2.ToolCalls[0].ID != "toolu_9" || string(ev2.ToolCalls[0].Args) != `{"path":"c.go"}` {
		t.Fatalf("ToolCalls = %+v", ev2.ToolCalls)
	}
	assertChannelClosed(t, events)
}

func TestAnthropicStreamingTextAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: message_start\n" +
				`data: {"type":"message_start","message":{"usage":{"input_tokens":12,"cache_read_input_tokens":10,"cache_creation_input_tokens":2}}}` + "\n\n" +
				"event: content_block_start\n" +
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
				"event: content_block_delta\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}` + "\n\n" +
				"event: content_block_delta\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}` + "\n\n" +
				"event: content_block_stop\n" +
				`data: {"type":"content_block_stop","index":0}` + "\n\n" +
				"event: message_delta\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}` + "\n\n" +
				"event: message_stop\n" +
				`data: {"type":"message_stop"}` + "\n\n",
		))
	}))
	defer server.Close()

	p := newTestAnthropic(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	ev1, _ := recvEvent(t, events)
	if ev1.Type != schema.ChatEventDelta || ev1.Delta != "hel" {
		t.Fatalf("event 1 = %+v, want Delta hel", ev1)
	}
	ev2, _ := recvEvent(t, events)
	if ev2.Type != schema.ChatEventDelta || ev2.Delta != "lo" {
		t.Fatalf("event 2 = %+v, want Delta lo", ev2)
	}
	ev3, _ := recvEvent(t, events)
	if ev3.Type != schema.ChatEventDone || ev3.FinishReason != "stop" {
		t.Fatalf("event 3 = %+v, want Done stop", ev3)
	}
	if ev3.Usage == nil || ev3.Usage.PromptTokens != 24 || ev3.Usage.CompletionTokens != 4 {
		t.Fatalf("Usage = %+v, want prompt=24 (12+10+2) completion=4", ev3.Usage)
	}
	if ev3.Usage.CacheReadTokens != 10 || ev3.Usage.CacheWriteTokens != 2 {
		t.Fatalf("cache tokens = %+v, want read=10 write=2", ev3.Usage)
	}
	assertChannelClosed(t, events)
}

func TestAnthropicStreamingToolUseAccumulation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: message_start\n" +
				`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
				"event: content_block_start\n" +
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file"}}` + "\n\n" +
				"event: content_block_delta\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}` + "\n\n" +
				"event: content_block_delta\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"d.go\"}"}}` + "\n\n" +
				"event: content_block_stop\n" +
				`data: {"type":"content_block_stop","index":0}` + "\n\n" +
				"event: message_delta\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}` + "\n\n" +
				"event: message_stop\n" +
				`data: {"type":"message_stop"}` + "\n\n",
		))
	}))
	defer server.Close()

	p := newTestAnthropic(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	ev, _ := recvEvent(t, events)
	if ev.Type != schema.ChatEventDone {
		t.Fatalf("event = %+v, want Done", ev)
	}
	if ev.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", ev.FinishReason)
	}
	if len(ev.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want 1", ev.ToolCalls)
	}
	call := ev.ToolCalls[0]
	if call.ID != "toolu_1" || call.Name != "read_file" {
		t.Fatalf("call = %+v", call)
	}
	if string(call.Args) != `{"path":"d.go"}` {
		t.Fatalf("Args = %s, want accumulated JSON", call.Args)
	}
	assertChannelClosed(t, events)
}

func TestAnthropicStreamingThinkingDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: message_start\n" +
				`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}` + "\n\n" +
				"event: content_block_start\n" +
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}` + "\n\n" +
				"event: content_block_delta\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"pondering"}}` + "\n\n" +
				"event: content_block_stop\n" +
				`data: {"type":"content_block_stop","index":0}` + "\n\n" +
				"event: content_block_start\n" +
				`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}` + "\n\n" +
				"event: content_block_delta\n" +
				`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}` + "\n\n" +
				"event: message_delta\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}` + "\n\n" +
				"event: message_stop\n" +
				`data: {"type":"message_stop"}` + "\n\n",
		))
	}))
	defer server.Close()

	p := newTestAnthropic(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	ev1, _ := recvEvent(t, events)
	if ev1.Type != schema.ChatEventDelta || ev1.Kind != schema.DeltaThinking || ev1.Delta != "pondering" {
		t.Fatalf("event 1 = %+v, want thinking Delta", ev1)
	}
	ev2, _ := recvEvent(t, events)
	if ev2.Type != schema.ChatEventDelta || ev2.Kind != schema.DeltaAnswer || ev2.Delta != "answer" {
		t.Fatalf("event 2 = %+v, want answer Delta", ev2)
	}
	ev3, _ := recvEvent(t, events)
	if ev3.Type != schema.ChatEventDone {
		t.Fatalf("event 3 = %+v, want Done", ev3)
	}
	assertChannelClosed(t, events)
}

func TestAnthropicModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "sk-test" {
			t.Fatalf("x-api-key = %q, want sk-test", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"type":"model","id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5","max_input_tokens":200000,"max_tokens":64000},{"type":"model","id":"claude-haiku-4-5","display_name":"Claude Haiku 4.5"}],"has_more":false}`))
	}))
	defer server.Close()

	p := newTestAnthropic(t, server.URL)
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	if models[0].ID != "claude-sonnet-4-5" || models[0].OwnedBy != "anthropic" {
		t.Fatalf("models[0] = %+v", models[0])
	}
	// Anthropic reports limits in its own model list, so they may clamp a
	// preset. A model that omits them must not be treated as authoritative.
	if !models[0].ProviderReported || models[0].ContextWindow != 200000 || models[0].MaxOutputTokens != 64000 {
		t.Fatalf("models[0] = %+v, want provider-reported 200000/64000", models[0])
	}
	if models[1].ProviderReported {
		t.Fatalf("models[1] = %+v, want ProviderReported=false when the list omits limits", models[1])
	}
}

func TestAnthropicChatHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer server.Close()

	p := newTestAnthropic(t, server.URL)
	_, err := p.Chat(t.Context(), chatReq(false))
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v (%T), want *ProviderError", err, err)
	}
	if pe.StatusCode != http.StatusUnauthorized {
		t.Fatalf("StatusCode = %d, want 401", pe.StatusCode)
	}
}

func TestAnthropicThinkingBudgetMargin(t *testing.T) {
	parse := func(t *testing.T, p *Anthropic, req schema.ChatRequest) (budget, maxTokens int, present bool) {
		t.Helper()
		body, err := p.buildChatRequestBody(req)
		if err != nil {
			t.Fatalf("buildChatRequestBody: %v", err)
		}
		var parsed struct {
			MaxTokens int `json:"max_tokens"`
			Thinking  *struct {
				BudgetTokens int `json:"budget_tokens"`
			} `json:"thinking"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("parse body: %v", err)
		}
		if parsed.Thinking == nil {
			return 0, parsed.MaxTokens, false
		}
		return parsed.Thinking.BudgetTokens, parsed.MaxTokens, true
	}

	base := schema.ChatRequest{
		Model:    "claude-sonnet-4",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	}

	// Explicit margin: budget 4096, margin 8192. With maxTokens unset the
	// default (8192) is > budget so no bump applies, but with maxTokens set
	// to the budget it should become budget + margin.
	p := &Anthropic{name: "test", thinkingBudget: 4096, thinkingBudgetMargin: 8192}
	req := base
	req.MaxTokens = ptrInt(4096)
	budget, maxTokens, present := parse(t, p, req)
	if !present || budget != 4096 {
		t.Fatalf("thinking present=%v budget=%d, want present budget 4096", present, budget)
	}
	if maxTokens != 4096+8192 {
		t.Errorf("explicit margin maxTokens = %d, want %d", maxTokens, 4096+8192)
	}

	// margin=0 (auto): max(2048, maxTokens/4). budget 4096, maxTokens 4096 ->
	// max(2048, 1024) = 2048, so maxTokens = 4096+2048 = 6144.
	p2 := &Anthropic{name: "test", thinkingBudget: 4096, thinkingBudgetMargin: 0}
	req.MaxTokens = ptrInt(4096)
	b, maxTokens, ok := parse(t, p2, req)
	if !ok || b != 4096 {
		t.Fatalf("auto thinking present=%v budget=%d, want present 4096", ok, b)
	}
	if maxTokens != 4096+2048 {
		t.Errorf("auto margin maxTokens = %d, want %d", maxTokens, 4096+2048)
	}

	// margin=-1 (disabled): maxTokens must not be adjusted.
	p3 := &Anthropic{name: "test", thinkingBudget: 4096, thinkingBudgetMargin: -1}
	req = base
	req.MaxTokens = ptrInt(2048) // less than budget
	b, maxTokens, ok = parse(t, p3, req)
	if !ok || b != 4096 {
		t.Fatalf("disabled thinking present=%v budget=%d, want present 4096", ok, b)
	}
	if maxTokens != 2048 {
		t.Errorf("disabled margin maxTokens = %d, want 2048 (unadjusted)", maxTokens)
	}
}

func ptrInt(v int) *int { return &v }

func TestAnthropicThinkingBudgetMapping(t *testing.T) {
	base := schema.ChatRequest{
		Model:    "claude-sonnet-4-5",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	}
	// Provider-level thinking_budget is 4096; the preset effort overrides it.
	p := &Anthropic{name: "test", thinkingBudget: 4096}

	parseThinking := func(t *testing.T, req schema.ChatRequest) (budget int, present bool) {
		t.Helper()
		body, err := p.buildChatRequestBody(req)
		if err != nil {
			t.Fatalf("buildChatRequestBody: %v", err)
		}
		var parsed struct {
			Thinking *struct {
				Type         string `json:"type"`
				BudgetTokens int    `json:"budget_tokens"`
			} `json:"thinking"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("parse body: %v", err)
		}
		if parsed.Thinking == nil {
			return 0, false
		}
		return parsed.Thinking.BudgetTokens, true
	}

	// "medium" -> 4096 even though provider budget is also 4096.
	if b, ok := parseThinking(t, base); !ok || b != 4096 {
		t.Fatalf("default (provider budget) thinking = (%d, %v), want (4096, true)", b, ok)
	}
	req := base
	req.Thinking = "medium"
	if b, ok := parseThinking(t, req); !ok || b != 4096 {
		t.Fatalf("medium thinking = (%d, %v), want (4096, true)", b, ok)
	}
	req.Thinking = "high"
	if b, ok := parseThinking(t, req); !ok || b != 16384 {
		t.Fatalf("high thinking = (%d, %v), want (16384, true)", b, ok)
	}
	// "off" explicitly disables thinking even with a provider budget configured.
	req.Thinking = "off"
	body, err := p.buildChatRequestBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"thinking":{"type":"disabled"}`) {
		t.Fatalf("off must explicitly disable thinking: %s", body)
	}
}

func TestAnthropicStreamingEmptyContentReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: message_start\n" +
				`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}` + "\n\n" +
				"event: message_delta\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}` + "\n\n" +
				"event: message_stop\n" +
				`data: {"type":"message_stop"}` + "\n\n",
		))
	}))
	defer server.Close()

	p := newTestAnthropic(t, server.URL)
	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "claude-sonnet-4-5",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var gotErr error
	for ev := range events {
		if ev.Type == schema.ChatEventError {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected error for empty streaming content, got nil")
	}
	if !strings.Contains(gotErr.Error(), "empty content") {
		t.Fatalf("error should mention 'empty content', got: %v", gotErr)
	}
}

func TestAnthropicEmptyContentReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":0}}`))
	}))
	defer server.Close()

	p := newTestAnthropic(t, server.URL)
	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "claude-sonnet-4-5",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var gotErr error
	for ev := range events {
		if ev.Type == schema.ChatEventError {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected error for empty content, got nil")
	}
	if !strings.Contains(gotErr.Error(), "empty content") {
		t.Fatalf("error should mention 'empty content', got: %v", gotErr)
	}
}

func TestAnthropicWireCaptureMarksUnknownEventTypes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MARSHAL_WIRE_CAPTURE", dir)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"weird_future_event\",\"x\":1}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	p, err := NewAnthropic(Options{Name: "wiretest-anthropic", BaseURL: server.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	events, err := p.Chat(context.Background(), schema.ChatRequest{
		Model:    "claude-test",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var sb strings.Builder
	for ev := range events {
		if ev.Type == schema.ChatEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		sb.WriteString(ev.Delta)
	}
	if sb.String() != "hi" {
		t.Fatalf("streamed content = %q, want %q", sb.String(), "hi")
	}

	matches, err := filepath.Glob(filepath.Join(dir, "wiretest-anthropic-*.stream"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one capture file, got %v (err=%v)", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if !strings.Contains(string(data), "weird_future_event") {
		t.Fatalf("capture missing raw event:\n%s", data)
	}
	if !strings.Contains(string(data), "[unrecognized-chunk]") {
		t.Fatalf("capture missing marker for unknown event type:\n%s", data)
	}
	if strings.Contains(string(data), "[unrecognized-chunk] {\"type\":\"content_block_stop\"") {
		t.Fatalf("known no-op event wrongly flagged:\n%s", data)
	}
}

func decodeAnthropicBody(t *testing.T, req schema.ChatRequest) anthropicRequest {
	t.Helper()
	raw, err := buildAnthropicRequestBody(req, 0, 0)
	if err != nil {
		t.Fatalf("buildAnthropicRequestBody: %v", err)
	}
	var body anthropicRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func countBreakpoints(body anthropicRequest) int {
	n := 0
	for _, b := range body.System {
		if b.CacheControl != nil {
			n++
		}
	}
	for _, tool := range body.Tools {
		if tool.CacheControl != nil {
			n++
		}
	}
	for _, m := range body.Messages {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				n++
			}
		}
	}
	return n
}

func toolLoopMessages(tail ...schema.ChatMessage) []schema.ChatMessage {
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "harness prompt"},
		{Role: schema.RoleSystem, Content: "repo instructions"},
		{Role: schema.RoleUser, Content: "fix it"},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "read", Args: json.RawMessage(`{"p":"a"}`)}}},
		{Role: schema.RoleTool, ToolCallID: "c1", Content: "file body"},
	}
	return append(msgs, tail...)
}

// Only the leading system run becomes the system field. A later system
// message (nudge, hint, finalize directive) stays where it was, wrapped as
// a system reminder, so it neither rewrites the cached system prefix
// mid-turn nor loses its position.
func TestAnthropicKeepsMidConversationSystemInPlace(t *testing.T) {
	body := decodeAnthropicBody(t, schema.ChatRequest{
		Model:    "claude-test",
		Messages: toolLoopMessages(schema.ChatMessage{Role: schema.RoleSystem, Content: "Call a tool or give a final answer."}),
	})

	if len(body.System) != 2 || body.System[0].Text != "harness prompt" || body.System[1].Text != "repo instructions" {
		t.Fatalf("system = %+v, want exactly the two leading system messages", body.System)
	}
	if body.System[1].CacheControl == nil {
		t.Error("the last leading system block must keep its breakpoint")
	}
	last := body.Messages[len(body.Messages)-1]
	if last.Role != "user" || len(last.Content) != 2 {
		t.Fatalf("last message = %+v, want one user message holding the tool result and the reminder", last)
	}
	want := "<system-reminder>\nCall a tool or give a final answer.\n</system-reminder>"
	if last.Content[1].Type != "text" || last.Content[1].Text != want {
		t.Fatalf("reminder block = %+v, want text %q", last.Content[1], want)
	}
}

// The rolling breakpoint goes on the last stable block, never on the
// volatile working-state tail, which changes every request.
func TestAnthropicRollingBreakpointSkipsVolatileTail(t *testing.T) {
	body := decodeAnthropicBody(t, schema.ChatRequest{
		Model: "claude-test",
		Tools: []schema.ToolDefinition{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Messages: toolLoopMessages(schema.ChatMessage{
			Role: schema.RoleUser, Content: "Current working state:\n\n## Current Todos\n\n- [~] read", Volatile: true,
		}),
	})

	last := body.Messages[len(body.Messages)-1]
	if len(last.Content) != 2 || last.Content[0].Type != "tool_result" {
		t.Fatalf("last message = %+v, want [tool_result, working-state text]", last)
	}
	if last.Content[0].CacheControl == nil {
		t.Error("tool_result (last stable block) must carry the rolling breakpoint")
	}
	if last.Content[1].CacheControl != nil {
		t.Error("the volatile tail must not carry a breakpoint")
	}
	if got := countBreakpoints(body); got != 3 {
		t.Errorf("breakpoints = %d, want 3 (tools, system, rolling); the API allows at most 4", got)
	}
}

func TestAnthropicRollingBreakpointOnLastBlockWithoutTail(t *testing.T) {
	body := decodeAnthropicBody(t, schema.ChatRequest{Model: "claude-test", Messages: toolLoopMessages()})
	last := body.Messages[len(body.Messages)-1]
	if last.Content[len(last.Content)-1].CacheControl == nil {
		t.Fatalf("last block must carry the rolling breakpoint: %+v", last)
	}
}

// Review focus 3: odd transcripts must neither panic nor mark a volatile block.
func TestAnthropicBreakpointEdgeCases(t *testing.T) {
	// An empty assistant message before any user message contributes no
	// block; the stable-block tracker must not index out of range.
	body := decodeAnthropicBody(t, schema.ChatRequest{Model: "claude-test", Messages: []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleAssistant},
		{Role: schema.RoleUser, Content: "hi"},
	}})
	if body.Messages[0].Content[0].CacheControl == nil {
		t.Errorf("the user block is the last stable block: %+v", body.Messages)
	}

	// Only a volatile message after the system prompt: no message breakpoint.
	body = decodeAnthropicBody(t, schema.ChatRequest{Model: "claude-test", Messages: []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "state", Volatile: true},
	}})
	if body.Messages[0].Content[0].CacheControl != nil {
		t.Error("a volatile-only transcript must not get a message breakpoint")
	}
}

// The anchor's last block carries the fourth breakpoint, so a turn's first
// request reads the previous turn's history instead of rewriting it.
func TestAnthropicAnchorBreakpoint(t *testing.T) {
	body := decodeAnthropicBody(t, schema.ChatRequest{
		Model: "claude-test",
		Tools: []schema.ToolDefinition{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Messages: []schema.ChatMessage{
			{Role: schema.RoleSystem, Content: "harness prompt"},
			{Role: schema.RoleUser, Content: "Project context pack:\n\n## Repo Card\n\nProject: marshal"},
			{Role: schema.RoleUser, Content: "first question"},
			{Role: schema.RoleAssistant, Content: "Answer one.", CacheAnchor: true},
			{Role: schema.RoleUser, Content: "second question"},
			{Role: schema.RoleUser, Content: "Current working state (reference only)", Volatile: true},
		},
	})

	// Wire: user[pack, q1], assistant[ans1], user[q2, tail]
	if got := body.Messages[1].Content[0]; got.CacheControl == nil {
		t.Errorf("anchor block (assistant answer) must carry a breakpoint: %+v", got)
	}
	last := body.Messages[2]
	if last.Content[0].CacheControl == nil {
		t.Error("the rolling breakpoint must stay on the last stable block")
	}
	if last.Content[1].CacheControl != nil {
		t.Error("the volatile tail must not carry a breakpoint")
	}
	if got := countBreakpoints(body); got != 4 {
		t.Errorf("breakpoints = %d, want 4 (tools, system, anchor, rolling)", got)
	}
}

// Review focus 5: an anchor on a leading system message or on the rolling
// block adds nothing.
func TestAnthropicAnchorNeverExceedsFour(t *testing.T) {
	onSystem := decodeAnthropicBody(t, schema.ChatRequest{Model: "claude-test", Messages: []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys", CacheAnchor: true},
		{Role: schema.RoleUser, Content: "hi"},
	}})
	if got := countBreakpoints(onSystem); got != 2 {
		t.Errorf("anchor on the leading system message: breakpoints = %d, want 2 (system, rolling)", got)
	}

	onRolling := decodeAnthropicBody(t, schema.ChatRequest{Model: "claude-test", Messages: []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "hi", CacheAnchor: true},
	}})
	if got := countBreakpoints(onRolling); got != 2 {
		t.Errorf("anchor on the rolling block: breakpoints = %d, want 2", got)
	}

	onVolatile := decodeAnthropicBody(t, schema.ChatRequest{Model: "claude-test", Messages: []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "q"},
		{Role: schema.RoleUser, Content: "state", Volatile: true, CacheAnchor: true},
	}})
	if onVolatile.Messages[0].Content[1].CacheControl != nil {
		t.Error("a volatile message must never carry a breakpoint, anchor or not")
	}
}
