package provider

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"marshal/internal/llm/schema"
)

func newTestOllama(t *testing.T, baseURL string) *OllamaNative {
	t.Helper()
	// ToolCalling=true matches what the factory passes for a tool-capable
	// config; per-model /api/show probing (Task 5) can still override it.
	caps := DefaultCapabilities()
	caps.ToolCalling = true
	p, err := NewOllamaNative(Options{Name: "test-ollama", BaseURL: baseURL, KeepAlive: "30m", Capabilities: &caps})
	if err != nil {
		t.Fatalf("NewOllamaNative: %v", err)
	}
	return p
}

func TestOllamaChatRequestBody(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			// Capability probe (wired in Task 5): advertise tool support.
			_, _ = w.Write([]byte(`{"capabilities":["completion","tools"]}`))
			return
		}
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":2}`))
	}))
	defer server.Close()

	temp := 0.2
	maxTok := 128
	p := newTestOllama(t, server.URL)
	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model: "qwen2.5-coder:7b",
		Messages: []schema.ChatMessage{
			{Role: schema.RoleSystem, Content: "be terse"},
			{Role: schema.RoleUser, Content: "hi"},
		},
		Temperature: &temp,
		MaxTokens:   &maxTok,
		Tools: []schema.ToolDefinition{{
			Name:        "read_file",
			Description: "read a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}

	if got["keep_alive"] != "30m" {
		t.Fatalf("keep_alive = %v, want %q", got["keep_alive"], "30m")
	}
	if got["stream"] != false {
		t.Fatalf("stream = %v, want false", got["stream"])
	}
	opts, ok := got["options"].(map[string]any)
	if !ok {
		t.Fatalf("options missing or wrong type: %v", got["options"])
	}
	if opts["temperature"] != 0.2 {
		t.Fatalf("options.temperature = %v, want 0.2", opts["temperature"])
	}
	if opts["num_predict"] != float64(128) {
		t.Fatalf("options.num_predict = %v, want 128", opts["num_predict"])
	}
	msgs, ok := got["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %v, want 2 entries", got["messages"])
	}
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages[0].role = %v, want system", msgs[0])
	}
	tools, ok := got["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v, want 1 entry", got["tools"])
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "read_file" {
		t.Fatalf("tools[0].function.name = %v, want read_file", fn["name"])
	}
}

func TestOllamaChatNonStreamingEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"hello","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"a.go"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":4}`))
	}))
	defer server.Close()

	p := newTestOllama(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	ev1, ok := recvEvent(t, events)
	if !ok || ev1.Type != schema.ChatEventDelta || ev1.Delta != "hello" {
		t.Fatalf("event 1 = %+v ok=%v, want Delta %q", ev1, ok, "hello")
	}
	ev2, ok := recvEvent(t, events)
	if !ok || ev2.Type != schema.ChatEventDone {
		t.Fatalf("event 2 = %+v ok=%v, want Done", ev2, ok)
	}
	if ev2.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want %q", ev2.FinishReason, "stop")
	}
	if ev2.Usage == nil || ev2.Usage.PromptTokens != 10 || ev2.Usage.CompletionTokens != 4 || ev2.Usage.TotalTokens != 14 {
		t.Fatalf("Usage = %+v, want prompt=10 completion=4 total=14", ev2.Usage)
	}
	if len(ev2.ToolCalls) != 1 || ev2.ToolCalls[0].Name != "read_file" {
		t.Fatalf("ToolCalls = %+v, want one read_file call", ev2.ToolCalls)
	}
	if string(ev2.ToolCalls[0].Args) != `{"path":"a.go"}` {
		t.Fatalf("ToolCalls[0].Args = %s", ev2.ToolCalls[0].Args)
	}
	assertChannelClosed(t, events)
}

func TestOllamaChatStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(
			`{"message":{"role":"assistant","content":"hel"},"done":false}` + "\n" +
				`{"message":{"role":"assistant","content":"lo"},"done":false}` + "\n" +
				`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":7,"eval_count":3}` + "\n",
		))
	}))
	defer server.Close()

	p := newTestOllama(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	ev1, _ := recvEvent(t, events)
	if ev1.Type != schema.ChatEventDelta || ev1.Delta != "hel" {
		t.Fatalf("event 1 = %+v, want Delta %q", ev1, "hel")
	}
	ev2, _ := recvEvent(t, events)
	if ev2.Type != schema.ChatEventDelta || ev2.Delta != "lo" {
		t.Fatalf("event 2 = %+v, want Delta %q", ev2, "lo")
	}
	ev3, _ := recvEvent(t, events)
	if ev3.Type != schema.ChatEventDone || ev3.FinishReason != "stop" {
		t.Fatalf("event 3 = %+v, want Done stop", ev3)
	}
	if ev3.Usage == nil || ev3.Usage.PromptTokens != 7 || ev3.Usage.CompletionTokens != 3 {
		t.Fatalf("Usage = %+v, want prompt=7 completion=3", ev3.Usage)
	}
	assertChannelClosed(t, events)
}

func TestOllamaChatStreamingThinkingAndToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(
			`{"message":{"role":"assistant","thinking":"hmm"},"done":false}` + "\n" +
				`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"b.go"}}}]},"done":false}` + "\n" +
				`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":1}` + "\n",
		))
	}))
	defer server.Close()

	p := newTestOllama(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	ev1, _ := recvEvent(t, events)
	if ev1.Type != schema.ChatEventDelta || ev1.Kind != schema.DeltaThinking || ev1.Delta != "hmm" {
		t.Fatalf("event 1 = %+v, want thinking Delta %q", ev1, "hmm")
	}
	ev2, _ := recvEvent(t, events)
	if ev2.Type != schema.ChatEventDone {
		t.Fatalf("event 2 = %+v, want Done", ev2)
	}
	if len(ev2.ToolCalls) != 1 || ev2.ToolCalls[0].Name != "read_file" {
		t.Fatalf("ToolCalls = %+v, want one read_file call", ev2.ToolCalls)
	}
	assertChannelClosed(t, events)
}

func TestOllamaChatStreamingErrorLine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"error":"model requires more system memory"}` + "\n"))
	}))
	defer server.Close()

	p := newTestOllama(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	ev, _ := recvEvent(t, events)
	if ev.Type != schema.ChatEventError || ev.Err == nil {
		t.Fatalf("event = %+v, want Error", ev)
	}
	assertChannelClosed(t, events)
}

func TestOllamaChatHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model 'nope' not found"}`))
	}))
	defer server.Close()

	p := newTestOllama(t, server.URL)
	_, err := p.Chat(t.Context(), chatReq(false))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v (%T), want *ProviderError", err, err)
	}
	if pe.StatusCode != http.StatusNotFound {
		t.Fatalf("StatusCode = %d, want 404", pe.StatusCode)
	}
}
