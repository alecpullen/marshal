package provider

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
		_, _ = w.Write([]byte(`{"data":[{"type":"model","id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5"},{"type":"model","id":"claude-haiku-4-5","display_name":"Claude Haiku 4.5"}],"has_more":false}`))
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
