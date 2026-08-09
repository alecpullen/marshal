package provider

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/llm/provider/limits"
	"marshal/internal/llm/schema"
)

// recvEvent reads the next event from the channel, failing the test if none
// arrives within the timeout. This bounds every test so a channel or
// goroutine bug produces a test failure instead of a hang.
func recvEvent(t *testing.T, events <-chan schema.ChatEvent) (schema.ChatEvent, bool) {
	t.Helper()
	select {
	case ev, ok := <-events:
		return ev, ok
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event from channel")
		return schema.ChatEvent{}, false
	}
}

// assertChannelClosed drains the channel, expecting it to close immediately
// (no further events).
func assertChannelClosed(t *testing.T, events <-chan schema.ChatEvent) {
	t.Helper()
	select {
	case ev, ok := <-events:
		if ok {
			t.Fatalf("expected channel to be closed, got event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel to close")
	}
}

func newTestProvider(t *testing.T, baseURL string) *OpenAICompatible {
	t.Helper()
	p, err := NewOpenAICompatible(Options{
		Name:    "test",
		BaseURL: baseURL,
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	return p
}

func chatReq(stream bool) schema.ChatRequest {
	return schema.ChatRequest{
		Model:    "test-model",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
		Stream:   stream,
	}
}

func TestChatStreamingDeltasAndDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev1, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before first event")
	}
	if ev1.Type != schema.ChatEventDelta || ev1.Delta != "hel" {
		t.Fatalf("event 1 = %+v, want Delta %q", ev1, "hel")
	}

	ev2, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before second event")
	}
	if ev2.Type != schema.ChatEventDelta || ev2.Delta != "lo" {
		t.Fatalf("event 2 = %+v, want Delta %q", ev2, "lo")
	}

	ev3, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before done event")
	}
	if ev3.Type != schema.ChatEventDone {
		t.Fatalf("event 3 = %+v, want Done", ev3)
	}

	assertChannelClosed(t, events)
}

func TestChatStreamingFinishReasonWithoutDoneSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}]}\n\n",
		))
		// Connection closes here with no [DONE] sentinel.
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev1, ok := recvEvent(t, events)
	if !ok || ev1.Type != schema.ChatEventDelta || ev1.Delta != "hi" {
		t.Fatalf("event 1 = %+v ok=%v, want Delta %q", ev1, ok, "hi")
	}

	ev2, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before done event")
	}
	if ev2.Type != schema.ChatEventDone || ev2.FinishReason != "stop" {
		t.Fatalf("event 2 = %+v, want Done with FinishReason=stop", ev2)
	}

	assertChannelClosed(t, events)
}

func TestChatStreamingCleanCloseSynthesizesDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n",
		))
		// Connection closes here with neither [DONE] nor finish_reason.
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev1, ok := recvEvent(t, events)
	if !ok || ev1.Type != schema.ChatEventDelta || ev1.Delta != "hi" {
		t.Fatalf("event 1 = %+v ok=%v, want Delta %q", ev1, ok, "hi")
	}

	ev2, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before synthesized done event")
	}
	if ev2.Type != schema.ChatEventDone {
		t.Fatalf("event 2 = %+v, want synthesized Done", ev2)
	}

	assertChannelClosed(t, events)
}

func TestChatNonStreamingSingleDeltaThenDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev1, ok := recvEvent(t, events)
	if !ok || ev1.Type != schema.ChatEventDelta || ev1.Delta != "hi" {
		t.Fatalf("event 1 = %+v ok=%v, want Delta %q", ev1, ok, "hi")
	}

	ev2, ok := recvEvent(t, events)
	if !ok || ev2.Type != schema.ChatEventDone || ev2.FinishReason != "stop" {
		t.Fatalf("event 2 = %+v ok=%v, want Done with FinishReason=stop", ev2, ok)
	}

	assertChannelClosed(t, events)
}

func TestChatStreamingMalformedJSONProducesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {not json}\n\n"))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before error event")
	}
	if ev.Type != schema.ChatEventError {
		t.Fatalf("event = %+v, want ChatEventError", ev)
	}
	if ev.Err == nil {
		t.Fatal("expected non-nil Err on error event")
	}

	assertChannelClosed(t, events)
}

func TestChatStreamingEmbeddedErrorProducesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"boom\"}}\n\n"))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before error event")
	}
	if ev.Type != schema.ChatEventError {
		t.Fatalf("event = %+v, want ChatEventError", ev)
	}
	if ev.Err == nil || !strings.Contains(ev.Err.Error(), "boom") {
		t.Fatalf("Err = %v, want error containing %q", ev.Err, "boom")
	}

	assertChannelClosed(t, events)
}

func TestChatNonStreamingEmptyChoicesProducesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before error event")
	}
	if ev.Type != schema.ChatEventError {
		t.Fatalf("event = %+v, want ChatEventError", ev)
	}

	assertChannelClosed(t, events)
}

func TestChatReturnsSynchronousProviderErrorOnHTTP500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err == nil {
		t.Fatal("expected error from Chat on HTTP 500, got nil")
	}
	if events != nil {
		t.Fatalf("expected nil channel when Chat returns an error, got %v", events)
	}

	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("errors.As(err, &ProviderError) failed; err = %v", err)
	}
	if providerErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("providerErr.StatusCode = %d, want %d", providerErr.StatusCode, http.StatusInternalServerError)
	}
}

func TestAuthorizationHeaderPresentWhenAPIKeySet(t *testing.T) {
	var gotAuth string
	var sawHeader bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, sawHeader = r.Header["Authorization"]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "test", BaseURL: server.URL, APIKey: "secret-key"})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	if _, err := p.Models(t.Context()); err != nil {
		t.Fatalf("Models returned error: %v", err)
	}

	if !sawHeader {
		t.Fatal("expected Authorization header to be present")
	}
	if gotAuth != "Bearer secret-key" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer secret-key")
	}
}

func TestAuthorizationHeaderAbsentWhenAPIKeyEmpty(t *testing.T) {
	var sawHeader bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header["Authorization"]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	if _, err := p.Models(t.Context()); err != nil {
		t.Fatalf("Models returned error: %v", err)
	}

	if sawHeader {
		t.Fatal("expected Authorization header to be absent when APIKey is empty")
	}
}

func TestModelsParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"llama3","owned_by":"local"}]}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models returned error: %v", err)
	}
	want := []schema.ModelInfo{{ID: "llama3", OwnedBy: "local"}}
	if len(models) != 1 || models[0] != want[0] {
		t.Fatalf("Models() = %+v, want %+v", models, want)
	}
}

func TestModelsNonOKReturnsProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.Models(t.Context())
	if err == nil {
		t.Fatal("expected error from Models on non-200 response")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("errors.As(err, &ProviderError) failed; err = %v", err)
	}
	if providerErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("providerErr.StatusCode = %d, want %d", providerErr.StatusCode, http.StatusUnauthorized)
	}
}

func TestChatStreamingReasoningContentEmitsThinkingDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking...\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev1, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before first event")
	}
	if ev1.Type != schema.ChatEventDelta || ev1.Kind != schema.DeltaThinking || ev1.Delta != "thinking..." {
		t.Fatalf("event 1 = %+v, want thinking delta %q", ev1, "thinking...")
	}

	ev2, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before second event")
	}
	if ev2.Type != schema.ChatEventDelta || ev2.Kind != schema.DeltaAnswer || ev2.Delta != "answer" {
		t.Fatalf("event 2 = %+v, want answer delta %q", ev2, "answer")
	}

	ev3, ok := recvEvent(t, events)
	if !ok || ev3.Type != schema.ChatEventDone {
		t.Fatalf("event 3 = %+v ok=%v, want Done", ev3, ok)
	}

	assertChannelClosed(t, events)
}

func TestInlineThinkParserExtractsThinkingAndContent(t *testing.T) {
	p := inlineThinkParser{}
	c1, th1 := p.feed("<think>one ")
	if c1 != "" || th1 != "" {
		t.Fatalf("partial open tag should emit nothing, got content=%q thinking=%q", c1, th1)
	}
	c2, th2 := p.feed("two</think>answer")
	if th2 != "one two" {
		t.Fatalf("thinking = %q, want %q", th2, "one two")
	}
	if c2 != "answer" {
		t.Fatalf("content = %q, want %q", c2, "answer")
	}
	c3, th3 := p.flush()
	if c3 != "" || th3 != "" {
		t.Fatalf("flush after closed tag should be empty, got content=%q thinking=%q", c3, th3)
	}
}

func TestInlineThinkParserHandlesSplitCloseTag(t *testing.T) {
	p := inlineThinkParser{}
	_, th1 := p.feed("<think>reasoning</thin")
	if th1 != "" {
		t.Fatalf("partial close tag should not emit thinking, got %q", th1)
	}
	c2, th2 := p.feed("k>out")
	if th2 != "reasoning" {
		t.Fatalf("thinking = %q, want %q", th2, "reasoning")
	}
	if c2 != "out" {
		t.Fatalf("content = %q, want %q", c2, "out")
	}
}

func TestInlineThinkParserLeavesOrdinaryProseUntouched(t *testing.T) {
	p := inlineThinkParser{}
	content, thinking := p.feed("I was thinking about the response.")
	content2, thinking2 := p.flush()
	if content+content2 != "I was thinking about the response." || thinking+thinking2 != "" {
		t.Fatalf("ordinary prose parsed as thinking: content=%q thinking=%q", content+content2, thinking+thinking2)
	}
}

func TestChatStreamingInlineThinkTagEmitsThinkingDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"<thi\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"nk>\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"step one\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"</think>the answer\"}}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev1, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before first event")
	}
	if ev1.Type != schema.ChatEventDelta || ev1.Kind != schema.DeltaThinking || ev1.Delta != "step one" {
		t.Fatalf("event 1 = %+v, want thinking delta %q", ev1, "step one")
	}

	ev2, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before second event")
	}
	if ev2.Type != schema.ChatEventDelta || ev2.Kind != schema.DeltaAnswer || ev2.Delta != "the answer" {
		t.Fatalf("event 2 = %+v, want answer delta %q", ev2, "the answer")
	}

	ev3, ok := recvEvent(t, events)
	if !ok || ev3.Type != schema.ChatEventDone {
		t.Fatalf("event 3 = %+v ok=%v, want Done", ev3, ok)
	}

	assertChannelClosed(t, events)
}

func TestBuildChatRequestBodyIncludesResponseFormat(t *testing.T) {
	body, err := buildChatRequestBody(schema.ChatRequest{
		Model:          "test-model",
		Messages:       []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
		ResponseFormat: &schema.ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		t.Fatalf("buildChatRequestBody returned error: %v", err)
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	raw, ok := parsed["response_format"]
	if !ok {
		t.Fatalf("request body missing response_format field")
	}
	if string(raw) != `{"type":"json_object"}` {
		t.Fatalf("response_format = %s, want {\"type\":\"json_object\"}", string(raw))
	}
}

func TestBuildChatRequestBodyOmitsResponseFormatWhenNil(t *testing.T) {
	body, err := buildChatRequestBody(schema.ChatRequest{
		Model:    "test-model",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("buildChatRequestBody returned error: %v", err)
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	if _, ok := parsed["response_format"]; ok {
		t.Fatalf("request body should not contain response_format when nil")
	}
}

func TestBuildChatRequestBodyToolWireShapes(t *testing.T) {
	t.Run("omits tools for baseline request", func(t *testing.T) {
		body, err := buildChatRequestBody(schema.ChatRequest{
			Model:    "test-model",
			Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("buildChatRequestBody returned error: %v", err)
		}

		want := `{"model":"test-model","messages":[{"role":"user","content":"hi"}],"stream":false}`
		if string(body) != want {
			t.Fatalf("body = %s\nwant %s", body, want)
		}
	})

	t.Run("includes max_tokens when set", func(t *testing.T) {
		maxTok := 2048
		body, err := buildChatRequestBody(schema.ChatRequest{
			Model:     "test-model",
			Messages:  []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
			MaxTokens: &maxTok,
		})
		if err != nil {
			t.Fatalf("buildChatRequestBody returned error: %v", err)
		}
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}
		raw, ok := parsed["max_tokens"]
		if !ok {
			t.Fatal("request body missing max_tokens")
		}
		if string(raw) != "2048" {
			t.Fatalf("max_tokens = %s, want 2048", string(raw))
		}
	})

	t.Run("serializes tool definitions", func(t *testing.T) {
		body, err := buildChatRequestBody(schema.ChatRequest{
			Model:    "test-model",
			Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
			Tools: []schema.ToolDefinition{{
				Name:        "file.read",
				Description: "Read a file",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
			}},
		})
		if err != nil {
			t.Fatalf("buildChatRequestBody returned error: %v", err)
		}

		var parsed map[string]json.RawMessage
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}
		got := string(parsed["tools"])
		want := `[{"type":"function","function":{"name":"file.read","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}]`
		if got != want {
			t.Fatalf("tools = %s\nwant  %s", got, want)
		}
	})

	t.Run("serializes assistant calls and tool results", func(t *testing.T) {
		body, err := buildChatRequestBody(schema.ChatRequest{
			Model: "test-model",
			Messages: []schema.ChatMessage{
				{
					Role:    schema.RoleAssistant,
					Content: "Reading now",
					ToolCalls: []schema.ToolCall{{
						ID:   "call_1",
						Name: "file.read",
						Args: json.RawMessage(`{"path":"README.md"}`),
					}},
				},
				{Role: schema.RoleTool, ToolCallID: "call_1", Content: "contents"},
			},
		})
		if err != nil {
			t.Fatalf("buildChatRequestBody returned error: %v", err)
		}

		var parsed struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}
		if len(parsed.Messages) != 2 {
			t.Fatalf("len(messages) = %d, want 2", len(parsed.Messages))
		}
		wantAssistant := `{"role":"assistant","content":"Reading now","tool_calls":[{"id":"call_1","type":"function","function":{"name":"file.read","arguments":"{\"path\":\"README.md\"}"}}]}`
		if string(parsed.Messages[0]) != wantAssistant {
			t.Fatalf("assistant message = %s\nwant             %s", parsed.Messages[0], wantAssistant)
		}
		wantTool := `{"role":"tool","content":"contents","tool_call_id":"call_1"}`
		if string(parsed.Messages[1]) != wantTool {
			t.Fatalf("tool message = %s\nwant         %s", parsed.Messages[1], wantTool)
		}
	})
}

func TestChatStreamingToolCallsOnDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"file.read","arguments":"{\"path\":\"READ"}},{"index":1,"id":"call_2","type":"function","function":{"name":"search.find","arguments":"{\"query\":\"to"}}]}}]}` + "\n\n" +
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ME.md\"}"}},{"index":1,"function":{"arguments":"ol\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev, ok := recvEvent(t, events)
	if !ok || ev.Type != schema.ChatEventDone {
		t.Fatalf("event = %+v ok=%v, want Done", ev, ok)
	}
	if ev.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", ev.FinishReason)
	}
	if len(ev.ToolCalls) != 2 {
		t.Fatalf("len(ToolCalls) = %d, want 2: %+v", len(ev.ToolCalls), ev.ToolCalls)
	}
	if ev.ToolCalls[0].ID != "call_1" || ev.ToolCalls[0].Name != "file.read" || string(ev.ToolCalls[0].Args) != `{"path":"README.md"}` {
		t.Fatalf("ToolCalls[0] = %+v", ev.ToolCalls[0])
	}
	if ev.ToolCalls[1].ID != "call_2" || ev.ToolCalls[1].Name != "search.find" || string(ev.ToolCalls[1].Args) != `{"query":"tool"}` {
		t.Fatalf("ToolCalls[1] = %+v", ev.ToolCalls[1])
	}

	assertChannelClosed(t, events)
}

func TestChatNonStreamingToolCallsOnDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"file.read","arguments":"{\"path\":\"README.md\"}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev, ok := recvEvent(t, events)
	if !ok || ev.Type != schema.ChatEventDone {
		t.Fatalf("event = %+v ok=%v, want Done", ev, ok)
	}
	if ev.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", ev.FinishReason)
	}
	if len(ev.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1: %+v", len(ev.ToolCalls), ev.ToolCalls)
	}
	if ev.ToolCalls[0].ID != "call_1" || ev.ToolCalls[0].Name != "file.read" || string(ev.ToolCalls[0].Args) != `{"path":"README.md"}` {
		t.Fatalf("ToolCalls[0] = %+v", ev.ToolCalls[0])
	}

	assertChannelClosed(t, events)
}

func TestChatStreamingTokenUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20,\"total_tokens\":30}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev1, ok := recvEvent(t, events)
	if !ok || ev1.Type != schema.ChatEventDelta || ev1.Delta != "hel" {
		t.Fatalf("event 1 error: %+v", ev1)
	}

	ev2, ok := recvEvent(t, events)
	if !ok || ev2.Type != schema.ChatEventDelta || ev2.Delta != "lo" {
		t.Fatalf("event 2 error: %+v", ev2)
	}

	ev3, ok := recvEvent(t, events)
	if !ok || ev3.Type != schema.ChatEventDone {
		t.Fatalf("event 3 (done) error: %+v", ev3)
	}

	if ev3.Usage == nil {
		t.Fatal("expected token usage in done event, got nil")
	}
	if ev3.Usage.PromptTokens != 10 || ev3.Usage.CompletionTokens != 20 || ev3.Usage.TotalTokens != 30 {
		t.Errorf("unexpected token usage: %+v", ev3.Usage)
	}

	assertChannelClosed(t, events)
}

func TestChatNonStreamingTokenUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			`{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":18,"total_tokens":30}}`,
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	ev1, ok := recvEvent(t, events)
	if !ok || ev1.Type != schema.ChatEventDelta || ev1.Delta != "hello" {
		t.Fatalf("event 1 error: %+v", ev1)
	}

	ev2, ok := recvEvent(t, events)
	if !ok || ev2.Type != schema.ChatEventDone {
		t.Fatalf("event 2 (done) error: %+v", ev2)
	}

	if ev2.Usage == nil {
		t.Fatal("expected token usage in done event, got nil")
	}
	if ev2.Usage.PromptTokens != 12 || ev2.Usage.CompletionTokens != 18 || ev2.Usage.TotalTokens != 30 {
		t.Errorf("unexpected token usage: %+v", ev2.Usage)
	}

	assertChannelClosed(t, events)
}

func TestResponseFormatWireShapes(t *testing.T) {
	t.Run("json_object serializes without json_schema key", func(t *testing.T) {
		b, err := json.Marshal(&schema.ResponseFormat{Type: "json_object"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(b) != `{"type":"json_object"}` {
			t.Fatalf("wire = %s, want back-compat shape", b)
		}
	})

	t.Run("json_schema serializes the full structured-output shape", func(t *testing.T) {
		rf := &schema.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &schema.JSONSchemaSpec{
				Name:   "action_envelope",
				Strict: true,
				Schema: json.RawMessage(`{"type":"object"}`),
			},
		}
		b, err := json.Marshal(rf)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := `{"type":"json_schema","json_schema":{"name":"action_envelope","strict":true,"schema":{"type":"object"}}}`
		if string(b) != want {
			t.Fatalf("wire = %s\nwant  %s", b, want)
		}
	})
}

func TestActionEnvelopeResponseFormatReachesWire(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		defer r.Body.Close()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"test\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	p := newTestProvider(t, srv.URL)

	actionRF := &schema.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &schema.JSONSchemaSpec{
			Name:   "marshal_action",
			Schema: json.RawMessage(`{"type":"object","properties":{"rationale":{"type":"string"}},"required":["rationale"],"additionalProperties":false}`),
		},
	}

	req := schema.ChatRequest{
		Model:          "test-model",
		Messages:       []schema.ChatMessage{{Role: schema.RoleUser, Content: "test"}},
		Stream:         true,
		ResponseFormat: actionRF,
	}

	events, err := p.Chat(t.Context(), req)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	for range events {
	}

	var body map[string]interface{}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("Failed to parse request body: %v", err)
	}

	rf, ok := body["response_format"].(map[string]interface{})
	if !ok {
		t.Fatalf("response_format missing or wrong type: %v", body["response_format"])
	}

	if rf["type"] != "json_schema" {
		t.Errorf("expected type=json_schema, got %v", rf["type"])
	}

	js, ok := rf["json_schema"].(map[string]interface{})
	if !ok {
		t.Fatalf("json_schema missing or wrong type: %v", rf["json_schema"])
	}

	if js["name"] != "marshal_action" {
		t.Errorf("expected name=marshal_action, got %v", js["name"])
	}

	if strict, exists := js["strict"]; exists {
		t.Errorf("expected strict to be omitted (omitempty on false), got %v", strict)
	}

	inner, ok := js["schema"].(map[string]interface{})
	if !ok {
		t.Fatalf("schema missing or wrong type: %v", js["schema"])
	}

	if inner["type"] != "object" {
		t.Errorf("expected schema.type=object, got %v", inner["type"])
	}

	if inner["additionalProperties"] != false {
		t.Errorf("expected additionalProperties=false, got %v", inner["additionalProperties"])
	}

	required, ok := inner["required"].([]interface{})
	if !ok {
		t.Fatalf("required missing or wrong type: %v", inner["required"])
	}

	if len(required) != 1 || required[0] != "rationale" {
		t.Errorf("expected required=[rationale], got %v", required)
	}
}

func TestChatStreamingTokenUsageWithCacheAndReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":40},"completion_tokens_details":{"reasoning_tokens":20}}}` + "\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var done schema.ChatEvent
	for ev := range events {
		if ev.Type == schema.ChatEventDone {
			done = ev
		}
	}
	if done.Usage == nil {
		t.Fatal("expected token usage in done event, got nil")
	}
	if done.Usage.CacheReadTokens != 40 {
		t.Errorf("CacheReadTokens = %d, want 40", done.Usage.CacheReadTokens)
	}
	if done.Usage.ReasoningTokens != 20 {
		t.Errorf("ReasoningTokens = %d, want 20", done.Usage.ReasoningTokens)
	}
	if done.Usage.CacheWriteTokens != 0 {
		t.Errorf("CacheWriteTokens = %d, want 0 (OpenAI doesn't report cache writes)", done.Usage.CacheWriteTokens)
	}
}

func TestChatNonStreamingTokenUsageWithDeepSeekCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":200,"completion_tokens":80,"total_tokens":280,"prompt_cache_hit_tokens":150,"prompt_cache_miss_tokens":50}}`,
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var done schema.ChatEvent
	for ev := range events {
		if ev.Type == schema.ChatEventDone {
			done = ev
		}
	}
	if done.Usage == nil {
		t.Fatal("expected token usage, got nil")
	}
	if done.Usage.CacheReadTokens != 150 {
		t.Errorf("CacheReadTokens = %d, want 150 (DeepSeek prompt_cache_hit_tokens)", done.Usage.CacheReadTokens)
	}
	if done.Usage.CacheWriteTokens != 50 {
		t.Errorf("CacheWriteTokens = %d, want 50 (DeepSeek prompt_cache_miss_tokens)", done.Usage.CacheWriteTokens)
	}
}

func TestChatTokenUsageNoCacheFieldsZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var done schema.ChatEvent
	for ev := range events {
		if ev.Type == schema.ChatEventDone {
			done = ev
		}
	}
	if done.Usage == nil {
		t.Fatal("expected token usage, got nil")
	}
	if done.Usage.ReasoningTokens != 0 || done.Usage.CacheReadTokens != 0 || done.Usage.CacheWriteTokens != 0 {
		t.Errorf("provider without cache/reasoning fields should be zero: %+v", done.Usage)
	}
}

func TestModelsEnrichesLimitsFromTable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-flash","owned_by":"ollama"}]}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	// Inject a fake limit table.
	tbl := limits.NewTable(map[string]limits.Limit{
		"deepseek-v4-flash": {ContextWindow: 1048576, MaxOutputTokens: 384000},
	})
	p.limitsTable = &tbl

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ContextWindow != 1048576 {
		t.Errorf("ContextWindow = %d, want 1048576", models[0].ContextWindow)
	}
	if models[0].MaxOutputTokens != 384000 {
		t.Errorf("MaxOutputTokens = %d, want 384000", models[0].MaxOutputTokens)
	}
}

func TestModelsPropagatesToolCallingFromLimitsTable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o","owned_by":"openai"}]}`))
	}))
	defer server.Close()

	toolCalling := true
	table := limits.NewTable(map[string]limits.Limit{
		"openrouter/gpt-4o": {ContextWindow: 128000, MaxOutputTokens: 16384, ToolCalling: &toolCalling},
	})
	p, err := NewOpenAICompatible(Options{Name: "openrouter", BaseURL: server.URL, LimitsTable: &table})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models returned error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("Models() returned %d models, want 1", len(models))
	}
	got := models[0]
	if got.ContextWindow != 128000 || got.MaxOutputTokens != 16384 {
		t.Errorf("Models()[0] limits = %+v, want context=128000 maxOutput=16384", got)
	}
	if got.ToolCalling == nil || !*got.ToolCalling {
		t.Errorf("Models()[0].ToolCalling = %v, want true", got.ToolCalling)
	}
}
