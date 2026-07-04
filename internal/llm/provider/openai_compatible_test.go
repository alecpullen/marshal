package provider

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestEmbedParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"model":"embed-model","data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	resp, err := p.Embed(t.Context(), schema.EmbedRequest{Model: "embed-model", Input: []string{"hello"}})
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Fatalf("len(resp.Embeddings) = %d, want 1", len(resp.Embeddings))
	}
	want := []float64{0.1, 0.2}
	got := resp.Embeddings[0]
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("resp.Embeddings[0] = %v, want %v", got, want)
	}
}

func TestEmbedNonOKReturnsProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.Embed(t.Context(), schema.EmbedRequest{Model: "embed-model", Input: []string{"hello"}})
	if err == nil {
		t.Fatal("expected error from Embed on non-200 response")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("errors.As(err, &ProviderError) failed; err = %v", err)
	}
	if providerErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("providerErr.StatusCode = %d, want %d", providerErr.StatusCode, http.StatusBadRequest)
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
