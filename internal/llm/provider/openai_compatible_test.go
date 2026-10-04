package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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

// A transport failure (server unreachable) must come back as the typed
// *RequestError so the UI can classify it with errors.As instead of
// matching on message text.
func TestChatTransportFailureReturnsRequestError(t *testing.T) {
	p := newTestProvider(t, "http://127.0.0.1:1") // closed port: connection refused
	_, err := p.Chat(context.Background(), chatReq(false))
	if err == nil {
		t.Fatal("Chat() err = nil, want a transport failure")
	}
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("Chat() err = %T %v, want a *RequestError in the chain", err, err)
	}
	if reqErr.Provider != "test" {
		t.Fatalf("RequestError.Provider = %q, want %q", reqErr.Provider, "test")
	}
	// Message text must be unchanged from the pre-type wrap format.
	if !strings.Contains(err.Error(), "provider \"test\": chat request failed:") {
		t.Fatalf("error text changed: %v", err)
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

func TestChatStreamingOpenRouterReasoningFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"reasoning\":\"pondering\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"reasoning_details\":[{\"text\":\"step one\"},{\"text\":\"step two\"}]}}]}\n\n" +
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
	if ev1.Type != schema.ChatEventDelta || ev1.Kind != schema.DeltaThinking || ev1.Delta != "pondering" {
		t.Fatalf("event 1 = %+v, want thinking delta %q", ev1, "pondering")
	}

	ev2, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before second event")
	}
	if ev2.Type != schema.ChatEventDelta || ev2.Kind != schema.DeltaThinking || ev2.Delta != "step onestep two" {
		t.Fatalf("event 2 = %+v, want concatenated reasoning_details %q", ev2, "step onestep two")
	}

	ev3, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before third event")
	}
	if ev3.Type != schema.ChatEventDelta || ev3.Kind != schema.DeltaAnswer || ev3.Delta != "answer" {
		t.Fatalf("event 3 = %+v, want answer delta %q", ev3, "answer")
	}

	ev4, ok := recvEvent(t, events)
	if !ok || ev4.Type != schema.ChatEventDone {
		t.Fatalf("event 4 = %+v ok=%v, want Done", ev4, ok)
	}
	assertChannelClosed(t, events)
}

func TestChatStreamingNoReasoningEmitsNoThinkingDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
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
	if ev1.Type != schema.ChatEventDelta || ev1.Kind != schema.DeltaAnswer || ev1.Delta != "answer" {
		t.Fatalf("event 1 = %+v, want answer delta %q", ev1, "answer")
	}
	ev2, ok := recvEvent(t, events)
	if !ok || ev2.Type != schema.ChatEventDone {
		t.Fatalf("event 2 = %+v ok=%v, want Done", ev2, ok)
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
	}, false)
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

func TestBuildChatRequestBodyReasoningEffort(t *testing.T) {
	newReq := func(thinking string) schema.ChatRequest {
		return schema.ChatRequest{
			Model:    "m",
			Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
			Thinking: thinking,
		}
	}
	// Known and unknown effort values pass through verbatim — the valid set
	// varies by backend and the panel now gates visibility on resolved
	// support, so the wire layer does not second-guess it.
	for _, effort := range []string{"low", "medium", "high", "minimal", "xhigh"} {
		body, err := buildChatRequestBody(newReq(effort), false)
		if err != nil {
			t.Fatalf("buildChatRequestBody(%q): %v", effort, err)
		}
		if !strings.Contains(string(body), `"reasoning_effort":"`+effort+`"`) {
			t.Fatalf("effort %q not passed through: %s", effort, body)
		}
	}
	// Explicit off requests none; default and empty omit the control.
	for _, effort := range []string{"off", "default", ""} {
		body, err := buildChatRequestBody(newReq(effort), false)
		if err != nil {
			t.Fatalf("buildChatRequestBody(%q): %v", effort, err)
		}
		if effort == "off" {
			if !strings.Contains(string(body), `"reasoning_effort":"none"`) {
				t.Fatalf("off must request none: %s", body)
			}
		} else if strings.Contains(string(body), "reasoning_effort") {
			t.Fatalf("effort %q must be omitted: %s", effort, body)
		}
	}
}

func TestBuildChatRequestBodyOmitsResponseFormatWhenNil(t *testing.T) {
	body, err := buildChatRequestBody(schema.ChatRequest{
		Model:    "test-model",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	}, false)
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
		}, false)
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
		}, false)
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
		}, false)
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
		}, false)
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

func TestBuildChatRequestBodyReasoningSummary(t *testing.T) {
	req := schema.ChatRequest{
		Model:    "m",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	}

	body, err := buildChatRequestBody(req, true)
	if err != nil {
		t.Fatalf("buildChatRequestBody: %v", err)
	}
	if !strings.Contains(string(body), `"reasoning":{"summary":"auto"}`) {
		t.Fatalf("reasoning summary not requested: %s", body)
	}

	body, err = buildChatRequestBody(req, false)
	if err != nil {
		t.Fatalf("buildChatRequestBody: %v", err)
	}
	if strings.Contains(string(body), `"reasoning":{`) {
		t.Fatalf("reasoning field must be omitted when the flag is off: %s", body)
	}
}

// A server whose chat template demands the system message first (stock
// qwen3 on llama.cpp / LM Studio) rejects marshal's mid-wire system
// messages with a 500. Chat must retry once with trailing system messages
// demoted to user, transparently.
func TestChatRetriesWithDemotedSystemMessagesOnStrictTemplate(t *testing.T) {
	type seenMsg struct {
		Role string `json:"role"`
	}
	var requests [][]seenMsg
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []seenMsg `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, body.Messages)
		strict := false
		for i, m := range body.Messages {
			if i > 0 && m.Role == "system" {
				strict = true
			}
		}
		if strict {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":500,"message":"Error: Jinja Exception: System message must be at the beginning."}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	req := chatReq(true)
	req.Messages = []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "you are an agent"},
		{Role: schema.RoleUser, Content: "hi"},
		{Role: schema.RoleSystem, Content: "call a tool or answer"},
	}
	events, err := p.Chat(t.Context(), req)
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	ev, ok := recvEvent(t, events)
	if !ok || ev.Type != schema.ChatEventDelta || ev.Delta != "ok" {
		t.Fatalf("first event = %+v ok=%v, want delta %q", ev, ok, "ok")
	}

	if len(requests) != 2 {
		t.Fatalf("server saw %d requests, want 2 (original + demoted retry)", len(requests))
	}
	if requests[0][2].Role != "system" {
		t.Fatalf("first attempt should be sent unmodified, got role %q", requests[0][2].Role)
	}
	if requests[1][2].Role != "user" {
		t.Fatalf("retry should demote trailing system to user, got role %q", requests[1][2].Role)
	}
	if requests[1][0].Role != "system" {
		t.Fatalf("leading system message must stay system, got role %q", requests[1][0].Role)
	}
}

// No trailing system message means no retry — the 500 surfaces as-is.
func TestChatDoesNotRetryStrictTemplateWhenNoTrailingSystem(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":500,"message":"Error: Jinja Exception: System message must be at the beginning."}}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.Chat(t.Context(), chatReq(false))
	if err == nil {
		t.Fatal("Chat() err = nil, want the provider error")
	}
	if calls != 1 {
		t.Fatalf("server saw %d requests, want 1 (no retry without trailing system)", calls)
	}
}

// LM Studio reports prediction-time template failures inside an HTTP-200
// SSE stream, not as a status code. The demote-and-retry path must trigger
// on embedded stream errors too.
func TestChatRetriesOnEmbeddedStrictTemplateError(t *testing.T) {
	type seenMsg struct {
		Role string `json:"role"`
	}
	var requests [][]seenMsg
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []seenMsg `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, body.Messages)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i, m := range body.Messages {
			if i > 0 && m.Role == "system" {
				_, _ = w.Write([]byte(`data: {"error":{"message":"Engine protocol predict request returned 500: {\"error\":{\"message\":\"Error: Jinja Exception: System message must be at the beginning.\"}}"}}` + "\n\n"))
				return
			}
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	req := chatReq(true)
	req.Messages = []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "you are an agent"},
		{Role: schema.RoleUser, Content: "hi"},
		{Role: schema.RoleSystem, Content: "call a tool or answer"},
	}
	events, err := p.Chat(t.Context(), req)
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	ev, ok := recvEvent(t, events)
	if !ok || ev.Type != schema.ChatEventDelta || ev.Delta != "ok" {
		t.Fatalf("first event = %+v ok=%v, want delta %q (embedded-error retry did not fire)", ev, ok, "ok")
	}
	if len(requests) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(requests))
	}
	if requests[1][2].Role != "user" {
		t.Fatalf("retry should demote trailing system to user, got %q", requests[1][2].Role)
	}
}

// OpenCode Zen's Go endpoint requires every chat request to carry a stable
// per-conversation session ID (x-opencode-session) and a self-identifying
// User-Agent; without them it rejects with HTTP 400 MissingSessionID.
func TestChatSendsOpencodeSessionHeaders(t *testing.T) {
	var gotSession, gotUA string
	var sawSession bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// http.Header canonicalizes keys (X-Opencode-Session), so presence
		// must be checked via Get, not direct map indexing.
		gotSession = r.Header.Get("x-opencode-session")
		sawSession = gotSession != ""
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{
		Name:      "opencode-go",
		BaseURL:   server.URL,
		SessionID: "sess_123",
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	// Non-streaming responses emit Delta events then Done; drain to Done.
	for {
		ev, ok := recvEvent(t, events)
		if !ok {
			t.Fatal("channel closed before done event")
		}
		if ev.Type == schema.ChatEventDone {
			break
		}
	}
	if !sawSession || gotSession != "sess_123" {
		t.Fatalf("x-opencode-session = %q (present=%v), want %q", gotSession, sawSession, "sess_123")
	}
	if !strings.HasPrefix(gotUA, "marshal/") {
		t.Fatalf("User-Agent = %q, want a marshal/<version> self-identifying value", gotUA)
	}
}

// The session headers are gated on opencode detection, not on whether a
// session ID happens to be configured: a non-opencode provider with a
// session ID must not leak opencode routing headers (or a custom UA).
func TestChatOmitsOpencodeHeadersForOtherProviders(t *testing.T) {
	var sawSession bool
	var gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawSession = r.Header.Get("x-opencode-session") != ""
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{
		Name:      "test",
		BaseURL:   server.URL,
		SessionID: "sess_123",
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	for {
		ev, ok := recvEvent(t, events)
		if !ok {
			t.Fatal("channel closed before done event")
		}
		if ev.Type == schema.ChatEventDone {
			break
		}
	}
	if sawSession {
		t.Fatal("x-opencode-session sent for a non-opencode provider; want gated")
	}
	if strings.HasPrefix(gotUA, "marshal/") {
		t.Fatalf("User-Agent = %q for a non-opencode provider; want the default client UA", gotUA)
	}
}

// isOpencode must recognize the provider by name (users may point the
// template at a proxy) and by host (the connect flow lets users rename
// the provider), without matching lookalike domains.
func TestIsOpencodeDetection(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    bool
	}{
		{"opencode-go", "https://api.example.com/v1", true},
		{"renamed-zen", "https://opencode.ai/zen/go/v1", true},
		{"renamed-zen", "https://go.opencode.ai/v1", true},
		{"test", "http://localhost:1234/v1", false},
		{"lookalike", "https://opencode.ai.attacker.com/v1", false},
	}
	for _, tc := range cases {
		p, err := NewOpenAICompatible(Options{Name: tc.name, BaseURL: tc.baseURL})
		if err != nil {
			t.Fatalf("NewOpenAICompatible(%q, %q): %v", tc.name, tc.baseURL, err)
		}
		if got := p.isOpencode(); got != tc.want {
			t.Errorf("isOpencode(name=%q, baseURL=%q) = %v, want %v", tc.name, tc.baseURL, got, tc.want)
		}
	}
}

// opencodeEndpointFor implements the Go docs table as anchored prefix
// rules: every current table row plus the fall-through default. The
// provider-prefixed form never matches a rule — the routing layer cuts
// the preset prefix before the provider sees the model — so it stays on
// the status-quo chat path like any other unknown ID.
func TestOpencodeEndpointForRouting(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		// /responses families
		{"grok-4.7", endpointResponses},
		{"grok-4.6", endpointResponses},
		{"gpt-6-luna", endpointResponses},
		{"gpt-5.6-luna", endpointResponses},
		{"muse-spark-1.3-contributor", endpointResponses},
		{"muse-spark-1.2-contributor", endpointResponses},
		// /messages families
		{"minimax-m3", endpointMessages},
		{"minimax-m2.7", endpointMessages},
		{"minimax-m2.5", endpointMessages},
		{"qwen3.8-max", endpointMessages},
		{"qwen3.8-flash", endpointMessages},
		{"qwen3.7-max", endpointMessages},
		{"qwen3.7-plus", endpointMessages},
		{"qwen3.6-plus", endpointMessages},
		// /chat/completions families and the default
		{"glm-5.3", endpointChat},
		{"kimi-k3", endpointChat},
		{"longcat-2.0", endpointChat},
		{"deepseek-v4.1-flash", endpointChat},
		{"mimo-v2.6-pro", endpointChat},
		{"hy4-preview", endpointChat},
		{"space-bunny-free", endpointChat},
		{"test-model", endpointChat},
		{"opencode-go/grok-4.7", endpointChat}, // prefixed form never routes
	}
	for _, tc := range cases {
		if got := opencodeEndpointFor(tc.model); got != tc.want {
			t.Errorf("opencodeEndpointFor(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

// isOpencodeGo gates endpoint routing on the Go product: the /zen/go
// path, not the bare host, because plain Zen routes some of the same
// model IDs differently (minimax-m3 and qwen3.8-max are /chat/completions
// there but /messages here).
func TestIsOpencodeGo(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    bool
	}{
		// Name match covers a provider pointed at a proxy that forwards to Go.
		{"opencode-go", "https://api.example.com/v1", true},
		// Renamed providers are detected via the opencode.ai host + /zen/go path.
		{"renamed-go", "https://opencode.ai/zen/go/v1", true},
		// Plain Zen shares the host but must NOT route.
		{"plain-zen", "https://opencode.ai/zen/v1", false},
		// The go.opencode.ai alias stays headers-only (Phase 1), no routing.
		{"go-alias", "https://go.opencode.ai/v1", false},
		// Lookalike hosts never match.
		{"lookalike", "https://opencode.ai.attacker.com/zen/go/v1", false},
		// A renamed provider at a non-opencode proxy is undetectable.
		{"renamed-go", "https://proxy.example.com/zen/go/v1", false},
		{"test", "http://localhost:1234/v1", false},
	}
	for _, tc := range cases {
		p, err := NewOpenAICompatible(Options{Name: tc.name, BaseURL: tc.baseURL})
		if err != nil {
			t.Fatalf("NewOpenAICompatible(%q, %q): %v", tc.name, tc.baseURL, err)
		}
		if got := p.isOpencodeGo(); got != tc.want {
			t.Errorf("isOpencodeGo(name=%q, baseURL=%q) = %v, want %v", tc.name, tc.baseURL, got, tc.want)
		}
	}
}

// --- OpenCode Go /responses path (direct responsesChat calls; the
// end-to-end routing through Chat lands with the dispatch head) ---

func TestResponsesChatNonStreaming(t *testing.T) {
	var gotPath, gotSession, gotUA string
	var rawBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSession = r.Header.Get("x-opencode-session")
		gotUA = r.Header.Get("User-Agent")
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output": [
				{"type": "reasoning", "summary": [{"type": "summary_text", "text": "pondering"}]},
				{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "hello"}]},
				{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": "{\"city\": \"SF\"}"}
			],
			"status": "completed",
			"usage": {
				"input_tokens": 10, "output_tokens": 5, "total_tokens": 15,
				"input_tokens_details": {"cached_tokens": 4},
				"output_tokens_details": {"reasoning_tokens": 2}
			}
		}`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL, SessionID: "sess_123"})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(false)
	req.Model = "grok-4.7"
	req.Messages = []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "You are helpful"},
		{Role: schema.RoleUser, Content: "hi"},
	}
	req.Tools = []schema.ToolDefinition{{
		Name:        "get_weather",
		Description: "Get weather",
		Parameters:  json.RawMessage(`{"type": "object"}`),
	}}
	events, err := p.responsesChat(t.Context(), req)
	if err != nil {
		t.Fatalf("responsesChat returned error: %v", err)
	}

	ev, ok := recvEvent(t, events)
	if !ok || ev.Type != schema.ChatEventDelta || ev.Kind != schema.DeltaThinking || ev.Delta != "pondering" {
		t.Fatalf("first event = %+v ok=%v, want thinking delta %q", ev, ok, "pondering")
	}
	ev, ok = recvEvent(t, events)
	if !ok || ev.Type != schema.ChatEventDelta || ev.Delta != "hello" {
		t.Fatalf("second event = %+v ok=%v, want answer delta %q", ev, ok, "hello")
	}
	ev, ok = recvEvent(t, events)
	if !ok || ev.Type != schema.ChatEventDone {
		t.Fatalf("third event = %+v ok=%v, want done", ev, ok)
	}
	if ev.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls (function_call items present)", ev.FinishReason)
	}
	if ev.Usage == nil {
		t.Fatal("Usage = nil, want mapped usage")
	}
	if ev.Usage.PromptTokens != 10 || ev.Usage.CompletionTokens != 5 || ev.Usage.TotalTokens != 15 {
		t.Errorf("usage tokens = %+v, want 10/5/15", ev.Usage)
	}
	if ev.Usage.CacheReadTokens != 4 {
		t.Errorf("CacheReadTokens = %d, want 4", ev.Usage.CacheReadTokens)
	}
	if ev.Usage.ReasoningTokens != 2 {
		t.Errorf("ReasoningTokens = %d, want 2", ev.Usage.ReasoningTokens)
	}
	if len(ev.ToolCalls) != 1 || ev.ToolCalls[0].ID != "call_1" || ev.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("ToolCalls = %+v, want one assembled get_weather call", ev.ToolCalls)
	}
	if string(ev.ToolCalls[0].Args) != `{"city": "SF"}` {
		t.Errorf("Args = %s, want the raw arguments JSON", ev.ToolCalls[0].Args)
	}

	if gotPath != "/responses" {
		t.Errorf("request path = %q, want /responses", gotPath)
	}
	if gotSession != "sess_123" {
		t.Errorf("x-opencode-session = %q, want sess_123 (Phase 1 regression)", gotSession)
	}
	if !strings.HasPrefix(gotUA, "marshal/") {
		t.Errorf("User-Agent = %q, want marshal/<version> (Phase 1 regression)", gotUA)
	}
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		t.Fatalf("parse request body: %v", err)
	}
	if body["store"] != false {
		t.Errorf("store = %v, want false (stateless client)", body["store"])
	}
	if got, _ := body["instructions"].(string); got != "You are helpful" {
		t.Errorf("instructions = %q, want the leading system run", got)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v, want one entry", body["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "get_weather" {
		t.Errorf("tool = %+v, want the FLAT responses shape {type, name, ...}", tool)
	}
	if _, nested := tool["function"]; nested {
		t.Error("tool has a nested \"function\" object; the responses API tool shape is flat")
	}
	input, _ := body["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input = %+v, want one user item (system went to instructions)", body["input"])
	}
	item, _ := input[0].(map[string]any)
	if item["role"] != "user" {
		t.Errorf("input[0].role = %v, want user", item["role"])
	}
}

func TestResponsesChatStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"get_weather"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"hel"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"lo"}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","delta":"pondering"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"city\""}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":":\"SF\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"SF\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"output":[{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"SF\"}"}]}}

`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(true)
	req.Model = "grok-4.7"
	events, err := p.responsesChat(t.Context(), req)
	if err != nil {
		t.Fatalf("responsesChat returned error: %v", err)
	}

	var sawThinking bool
	var answer strings.Builder
	for {
		ev, ok := recvEvent(t, events)
		if !ok {
			t.Fatal("channel closed before done event")
		}
		if ev.Type == schema.ChatEventDone {
			if !sawThinking {
				t.Error("no thinking delta seen; want reasoning_summary_text.delta mapped to DeltaThinking")
			}
			if answer.String() != "hello" {
				t.Errorf("answer deltas = %q, want hello", answer.String())
			}
			if ev.FinishReason != "tool_calls" {
				t.Errorf("FinishReason = %q, want tool_calls", ev.FinishReason)
			}
			if ev.Usage == nil || ev.Usage.TotalTokens != 15 {
				t.Errorf("Usage = %+v, want total 15 from response.completed", ev.Usage)
			}
			if len(ev.ToolCalls) != 1 || ev.ToolCalls[0].Name != "get_weather" {
				t.Fatalf("ToolCalls = %+v, want one assembled get_weather call", ev.ToolCalls)
			}
			if string(ev.ToolCalls[0].Args) != `{"city":"SF"}` {
				t.Errorf("Args = %s, want the assembled arguments JSON", ev.ToolCalls[0].Args)
			}
			break
		}
		switch {
		case ev.Type == schema.ChatEventDelta && ev.Kind == schema.DeltaThinking:
			if ev.Delta != "pondering" {
				t.Errorf("thinking delta = %q, want pondering", ev.Delta)
			}
			sawThinking = true
		case ev.Type == schema.ChatEventDelta:
			answer.WriteString(ev.Delta)
		}
	}
}

func TestResponsesMapsIncompleteToLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output": [{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "partial"}]}],
			"status": "incomplete",
			"incomplete_details": {"reason": "max_output_tokens"},
			"usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}
		}`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(false)
	req.Model = "grok-4.7"
	events, err := p.responsesChat(t.Context(), req)
	if err != nil {
		t.Fatalf("responsesChat returned error: %v", err)
	}
	ev, ok := recvEvent(t, events)
	if !ok || ev.Type != schema.ChatEventDelta || ev.Delta != "partial" {
		t.Fatalf("first event = %+v ok=%v, want delta %q", ev, ok, "partial")
	}
	ev, ok = recvEvent(t, events)
	if !ok || ev.Type != schema.ChatEventDone {
		t.Fatalf("second event = %+v ok=%v, want done", ev, ok)
	}
	if ev.FinishReason != "length" {
		t.Errorf("FinishReason = %q, want length (status incomplete)", ev.FinishReason)
	}
}

func TestResponsesReasoningEffortField(t *testing.T) {
	req := chatReq(false)
	req.Model = "grok-4.7"
	req.Thinking = "medium"
	body, err := buildResponsesRequestBody(req, false)
	if err != nil {
		t.Fatalf("buildResponsesRequestBody returned error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if _, hasTopLevel := parsed["reasoning_effort"]; hasTopLevel {
		t.Error("body carries a top-level reasoning_effort field; the responses API uses reasoning.effort")
	}
	reasoning, _ := parsed["reasoning"].(map[string]any)
	if reasoning == nil {
		t.Fatal("reasoning object missing; want reasoning.effort for a thinking request")
	}
	if reasoning["effort"] != "medium" {
		t.Errorf("reasoning.effort = %v, want medium", reasoning["effort"])
	}
	if _, hasSummary := reasoning["summary"]; hasSummary {
		t.Error("reasoning.summary set without the provider reasoning_summary flag; want omitted")
	}

	// The summary flag adds summary:auto alongside the effort.
	body, err = buildResponsesRequestBody(req, true)
	if err != nil {
		t.Fatalf("buildResponsesRequestBody(summary) returned error: %v", err)
	}
	parsed = nil
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	reasoning, _ = parsed["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["summary"] != "auto" {
		t.Errorf("reasoning = %+v, want summary auto when the flag is on", parsed["reasoning"])
	}

	// Explicit off requests none; default and empty omit the reasoning object.
	for _, effort := range []string{"off", "default", ""} {
		req.Thinking = effort
		body, err = buildResponsesRequestBody(req, false)
		if err != nil {
			t.Fatalf("buildResponsesRequestBody(%q) returned error: %v", effort, err)
		}
		parsed = nil
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("parse body: %v", err)
		}
		if effort == "off" {
			reasoning, _ := parsed["reasoning"].(map[string]any)
			if reasoning == nil || reasoning["effort"] != "none" {
				t.Errorf("off must request none: %s", body)
			}
		} else if _, has := parsed["reasoning"]; has {
			t.Errorf("thinking %q: reasoning object present, want omitted", effort)
		}
	}
}

// --- OpenCode Go /messages path (direct messagesChat calls) ---

func TestMessagesChatNonStreaming(t *testing.T) {
	var gotPath, gotSession, gotUA, gotVersion, gotAuth string
	var rawBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSession = r.Header.Get("x-opencode-session")
		gotUA = r.Header.Get("User-Agent")
		gotVersion = r.Header.Get("anthropic-version")
		gotAuth = r.Header.Get("Authorization")
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content": [
				{"type": "text", "text": "hello"},
				{"type": "tool_use", "id": "call_1", "name": "get_weather", "input": {"city": "SF"}}
			],
			"stop_reason": "tool_use",
			"usage": {"input_tokens": 10, "output_tokens": 5, "cache_read_input_tokens": 4, "cache_creation_input_tokens": 2}
		}`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL, APIKey: "sk-test", SessionID: "sess_123"})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(false)
	req.Model = "minimax-m3"
	req.Messages = []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "You are helpful"},
		{Role: schema.RoleUser, Content: "hi"},
	}
	req.Tools = []schema.ToolDefinition{{
		Name:        "get_weather",
		Description: "Get weather",
		Parameters:  json.RawMessage(`{"type": "object"}`),
	}}
	events, err := p.messagesChat(t.Context(), req)
	if err != nil {
		t.Fatalf("messagesChat returned error: %v", err)
	}

	ev, ok := recvEvent(t, events)
	if !ok || ev.Type != schema.ChatEventDelta || ev.Delta != "hello" {
		t.Fatalf("first event = %+v ok=%v, want delta %q", ev, ok, "hello")
	}
	ev, ok = recvEvent(t, events)
	if !ok || ev.Type != schema.ChatEventDone {
		t.Fatalf("second event = %+v ok=%v, want done", ev, ok)
	}
	if ev.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls (stop_reason tool_use)", ev.FinishReason)
	}
	if ev.Usage == nil || ev.Usage.CacheReadTokens != 4 || ev.Usage.CacheWriteTokens != 2 {
		t.Errorf("Usage = %+v, want cache read 4 / write 2", ev.Usage)
	}
	if len(ev.ToolCalls) != 1 || ev.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("ToolCalls = %+v, want one get_weather call", ev.ToolCalls)
	}

	if gotPath != "/messages" {
		t.Errorf("request path = %q, want /messages", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer (not x-api-key)", gotAuth)
	}
	if gotSession != "sess_123" || !strings.HasPrefix(gotUA, "marshal/") {
		t.Errorf("session headers = %q / %q, want Phase 1 behavior on the messages path", gotSession, gotUA)
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", gotVersion)
	}
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		t.Fatalf("parse request body: %v", err)
	}
	if body["max_tokens"] == nil {
		t.Error("max_tokens missing; the Messages API requires it on every call")
	}
	if body["model"] != "minimax-m3" {
		t.Errorf("model = %v, want minimax-m3", body["model"])
	}
	system, _ := body["system"].([]any)
	if len(system) == 0 {
		t.Fatal("system missing; the leading system message must be extracted to the system field")
	}
	sysBlock, _ := system[0].(map[string]any)
	if cc, _ := sysBlock["cache_control"].(map[string]any); cc == nil || cc["type"] != "ephemeral" {
		t.Errorf("system[0].cache_control = %+v, want an ephemeral breakpoint (prompt caching)", sysBlock["cache_control"])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v, want one entry", body["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["input_schema"] == nil {
		t.Error("tool.input_schema missing; want the Anthropic tool shape")
	}
}

func TestMessagesChatStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"get_weather"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"SF\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(true)
	req.Model = "minimax-m3"
	events, err := p.messagesChat(t.Context(), req)
	if err != nil {
		t.Fatalf("messagesChat returned error: %v", err)
	}

	ev, ok := recvEvent(t, events)
	if !ok || ev.Type != schema.ChatEventDone {
		t.Fatalf("event = %+v ok=%v, want done (tool_use-only stream)", ev, ok)
	}
	if ev.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls", ev.FinishReason)
	}
	if len(ev.ToolCalls) != 1 || ev.ToolCalls[0].Name != "get_weather" || string(ev.ToolCalls[0].Args) != `{"city":"SF"}` {
		t.Fatalf("ToolCalls = %+v, want assembled get_weather({\"city\":\"SF\"})", ev.ToolCalls)
	}
	if ev.Usage == nil || ev.Usage.PromptTokens != 10 || ev.Usage.CompletionTokens != 5 {
		t.Errorf("Usage = %+v, want prompt 10 (message_start) / completion 5 (message_delta)", ev.Usage)
	}
}

// --- end-to-end routing through Chat (dispatch head) ---

func TestChatRoutesGrokToResponses(t *testing.T) {
	var gotPath, gotSession string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSession = r.Header.Get("x-opencode-session")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/responses" {
			_, _ = w.Write([]byte(`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"status":"completed"}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL, SessionID: "sess_123"})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(false)
	req.Model = "grok-4.7"
	events, err := p.Chat(t.Context(), req)
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	for {
		ev, ok := recvEvent(t, events)
		if !ok {
			t.Fatal("channel closed before done event")
		}
		if ev.Type == schema.ChatEventDone {
			break
		}
	}
	if gotPath != "/responses" {
		t.Errorf("Chat(grok-4.7) posted to %q, want /responses", gotPath)
	}
	if gotSession != "sess_123" {
		t.Errorf("x-opencode-session = %q, want sess_123 on the responses path", gotSession)
	}
}

func TestChatRoutesMiniMaxToMessages(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/messages" {
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(false)
	req.Model = "minimax-m3"
	events, err := p.Chat(t.Context(), req)
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	for {
		ev, ok := recvEvent(t, events)
		if !ok {
			t.Fatal("channel closed before done event")
		}
		if ev.Type == schema.ChatEventDone {
			break
		}
	}
	if gotPath != "/messages" {
		t.Errorf("Chat(minimax-m3) posted to %q, want /messages", gotPath)
	}
}

func TestChatStreamsResponsesModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"hel"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"lo"}

event: response.completed
data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}

`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(true)
	req.Model = "grok-4.7"
	events, err := p.Chat(t.Context(), req)
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	var answer strings.Builder
	for {
		ev, ok := recvEvent(t, events)
		if !ok {
			t.Fatal("channel closed before done event")
		}
		if ev.Type == schema.ChatEventDone {
			break
		}
		if ev.Type == schema.ChatEventDelta {
			answer.WriteString(ev.Delta)
		}
	}
	if answer.String() != "hello" {
		t.Errorf("streamed answer = %q, want hello", answer.String())
	}
}

func TestChatStreamsMessagesModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(true)
	req.Model = "minimax-m3"
	events, err := p.Chat(t.Context(), req)
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	var answer strings.Builder
	for {
		ev, ok := recvEvent(t, events)
		if !ok {
			t.Fatal("channel closed before done event")
		}
		if ev.Type == schema.ChatEventDone {
			if ev.FinishReason != "stop" {
				t.Errorf("FinishReason = %q, want stop (end_turn)", ev.FinishReason)
			}
			break
		}
		if ev.Type == schema.ChatEventDelta {
			answer.WriteString(ev.Delta)
		}
	}
	if answer.String() != "hello" {
		t.Errorf("streamed answer = %q, want hello", answer.String())
	}
}

// A chat-family model on a Go provider keeps the status-quo path and
// body shape — the dispatch head must not disturb the default.
func TestChatCompletionsUnaffectedForGo(t *testing.T) {
	var gotPath string
	var rawBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(false)
	req.Model = "kimi-k3"
	events, err := p.Chat(t.Context(), req)
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	for {
		ev, ok := recvEvent(t, events)
		if !ok {
			t.Fatal("channel closed before done event")
		}
		if ev.Type == schema.ChatEventDone {
			break
		}
	}
	if gotPath != "/chat/completions" {
		t.Errorf("Chat(kimi-k3) posted to %q, want /chat/completions", gotPath)
	}
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		t.Fatalf("parse request body: %v", err)
	}
	if body["messages"] == nil {
		t.Error("chat-completions body missing the messages array")
	}
	if body["input"] != nil {
		t.Error("chat-completions body carries a responses-style input array")
	}
}

// rewriteHostClient returns an HTTP client that sends every request to
// target instead of the URL's own host, so a provider configured with a
// real base URL (e.g. opencode.ai/zen/v1) can be exercised against an
// httptest server without touching the network.
func rewriteHostClient(t *testing.T, target string) *http.Client {
	t.Helper()
	targetURL, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target %q: %v", target, err)
	}
	return &http.Client{Transport: rewriteHostTransport{target: targetURL}}
}

type rewriteHostTransport struct{ target *url.URL }

func (tr rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = tr.target.Scheme
	req.URL.Host = tr.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

// The dispatch head is gated on the Go product alone: a routing-family
// model on any other OpenAI-compatible provider — the built-in openai
// template, or plain Zen, which shares opencode.ai and routes some of the
// same model IDs differently — must keep posting /chat/completions.
// Widening isOpencodeGo to the host-wide isOpencode() would silently
// reroute grok/gpt/muse-spark/minimax/qwen3 models on every provider, so
// this is the highest-blast-radius invariant in the change.
func TestChatRoutingFamiliesStayOnChatForNonGoProviders(t *testing.T) {
	models := []string{
		"grok-4.7", // /responses family on Go
		"gpt-6-luna",
		"muse-spark-1.3-contributor",
		"minimax-m3", // /messages family on Go
		"qwen3.8-max",
	}
	cases := []struct {
		name    string
		baseURL string // empty = point straight at the httptest server
	}{
		{name: "openai-compatible"},
		{name: "opencode-zen", baseURL: "https://opencode.ai/zen/v1"},
	}
	for _, tc := range cases {
		for _, model := range models {
			t.Run(tc.name+"/"+model, func(t *testing.T) {
				var gotPath string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					gotPath = r.URL.Path
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
				}))
				defer server.Close()

				baseURL := tc.baseURL
				var client *http.Client
				if baseURL == "" {
					baseURL = server.URL
				} else {
					client = rewriteHostClient(t, server.URL)
				}
				p, err := NewOpenAICompatible(Options{Name: tc.name, BaseURL: baseURL, HTTPClient: client})
				if err != nil {
					t.Fatalf("NewOpenAICompatible returned error: %v", err)
				}
				req := chatReq(false)
				req.Model = model
				events, err := p.Chat(t.Context(), req)
				if err != nil {
					t.Fatalf("Chat returned error: %v", err)
				}
				for {
					ev, ok := recvEvent(t, events)
					if !ok {
						t.Fatal("channel closed before done event")
					}
					if ev.Type == schema.ChatEventDone {
						break
					}
				}
				// Suffix rather than equality: the Zen base URL carries a
				// /zen/v1 path prefix. A reroute to /responses or /messages
				// still fails this.
				if !strings.HasSuffix(gotPath, endpointChat) {
					t.Errorf("Chat(%s) on provider %q posted to %q, want a path ending in %q", model, tc.name, gotPath, endpointChat)
				}
			})
		}
	}
}

// A streaming refusal must reach the user as answer text. The
// non-streaming reader concatenates every content part, so it already
// surfaces refusals; dropping the refusal delta would make the same
// response visible non-streamed but read as a generic "empty content"
// error when streamed.
func TestResponsesStreamSurfacesRefusalDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.refusal.delta
data: {"type":"response.refusal.delta","delta":"I can't"}

event: response.refusal.delta
data: {"type":"response.refusal.delta","delta":" help with that"}

event: response.completed
data: {"type":"response.completed","response":{"status":"completed"}}

`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(true)
	req.Model = "grok-4.7"
	events, err := p.responsesChat(t.Context(), req)
	if err != nil {
		t.Fatalf("responsesChat returned error: %v", err)
	}
	var answer strings.Builder
	for {
		ev, ok := recvEvent(t, events)
		if !ok {
			t.Fatal("channel closed before done event")
		}
		if ev.Type == schema.ChatEventError {
			t.Fatalf("stream error: %v (refusal text was dropped)", ev.Err)
		}
		if ev.Type == schema.ChatEventDone {
			break
		}
		if ev.Type == schema.ChatEventDelta {
			answer.WriteString(ev.Delta)
		}
	}
	if answer.String() != "I can't help with that" {
		t.Errorf("streamed refusal = %q, want the refusal text as answer deltas", answer.String())
	}
}

// A proxy in front of the Responses API may append the chat-style [DONE]
// sentinel even though the API itself has none. Skipping it keeps the
// turn alive instead of failing the decode and ending on a stream error.
func TestResponsesStreamToleratesDoneSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"hi"}

data: [DONE]

event: response.completed
data: {"type":"response.completed","response":{"status":"completed"}}

`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(true)
	req.Model = "grok-4.7"
	events, err := p.responsesChat(t.Context(), req)
	if err != nil {
		t.Fatalf("responsesChat returned error: %v", err)
	}
	var answer strings.Builder
	for {
		ev, ok := recvEvent(t, events)
		if !ok {
			t.Fatal("channel closed before done event")
		}
		if ev.Type == schema.ChatEventError {
			t.Fatalf("stream error: %v (sentinel should be skipped, not decoded)", ev.Err)
		}
		if ev.Type == schema.ChatEventDone {
			break
		}
		if ev.Type == schema.ChatEventDelta {
			answer.WriteString(ev.Delta)
		}
	}
	if answer.String() != "hi" {
		t.Errorf("streamed answer = %q, want hi", answer.String())
	}
}

// A truncated (/responses) turn surfaces its incomplete reason in the
// error, so a max_output_tokens or content_filter stop is diagnosable
// instead of reading as a generic empty response.
func TestResponsesEmptyContentSurfacesIncompleteReason(t *testing.T) {
	t.Run("non-streaming", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":[],"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`))
		}))
		defer server.Close()

		p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL})
		if err != nil {
			t.Fatalf("NewOpenAICompatible returned error: %v", err)
		}
		req := chatReq(false)
		req.Model = "grok-4.7"
		events, err := p.responsesChat(t.Context(), req)
		if err != nil {
			t.Fatalf("responsesChat returned error: %v", err)
		}
		ev, ok := recvEvent(t, events)
		if !ok || ev.Type != schema.ChatEventError {
			t.Fatalf("event = %+v ok=%v, want a stream error", ev, ok)
		}
		if ev.Err == nil || !strings.Contains(ev.Err.Error(), "content_filter") {
			t.Errorf("error = %v, want it to mention the incomplete reason content_filter", ev.Err)
		}
	})

	t.Run("streaming", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`event: response.incomplete
data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}

`))
		}))
		defer server.Close()

		p, err := NewOpenAICompatible(Options{Name: "opencode-go", BaseURL: server.URL})
		if err != nil {
			t.Fatalf("NewOpenAICompatible returned error: %v", err)
		}
		req := chatReq(true)
		req.Model = "grok-4.7"
		events, err := p.responsesChat(t.Context(), req)
		if err != nil {
			t.Fatalf("responsesChat returned error: %v", err)
		}
		ev, ok := recvEvent(t, events)
		if !ok || ev.Type != schema.ChatEventError {
			t.Fatalf("event = %+v ok=%v, want a stream error", ev, ok)
		}
		if ev.Err == nil || !strings.Contains(ev.Err.Error(), "max_output_tokens") {
			t.Errorf("error = %v, want it to mention the incomplete reason max_output_tokens", ev.Err)
		}
	})
}

// Replayed assistant tool calls with empty or malformed arguments must
// still carry an arguments string: the Responses API requires the field,
// and omitempty would drop it and 400 on the next request. Matches the
// Anthropic conversion's defense.
func TestResponsesInputDefaultsEmptyToolCallArguments(t *testing.T) {
	msgs := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "hi"},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{
			{ID: "call_empty", Name: "no_args"},
			{ID: "call_bad", Name: "bad_args", Args: json.RawMessage(`{oops`)},
			{ID: "call_ok", Name: "good_args", Args: json.RawMessage(`{"a":1}`)},
		}},
		{Role: schema.RoleTool, ToolCallID: "call_empty", Content: "ok"},
	}
	_, items := buildResponsesInput(msgs)

	got := make(map[string]string)
	for _, item := range items {
		if item.Type == "function_call" {
			got[item.CallID] = item.Arguments
		}
	}
	if len(got) != 3 {
		t.Fatalf("function_call items = %+v, want 3", got)
	}
	if got["call_empty"] != "{}" {
		t.Errorf("empty args = %q, want {} so the required field survives omitempty", got["call_empty"])
	}
	if got["call_bad"] != "{}" {
		t.Errorf("malformed args = %q, want {}", got["call_bad"])
	}
	if got["call_ok"] != `{"a":1}` {
		t.Errorf("valid args = %q, want the original JSON untouched", got["call_ok"])
	}

	// The field must actually reach the wire.
	req := chatReq(false)
	req.Model = "grok-4.7"
	req.Messages = msgs
	body, err := buildResponsesRequestBody(req, false)
	if err != nil {
		t.Fatalf("buildResponsesRequestBody returned error: %v", err)
	}
	var parsed struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	for _, item := range parsed.Input {
		if item["type"] != "function_call" {
			continue
		}
		if args, ok := item["arguments"].(string); !ok || args == "" {
			t.Errorf("function_call item = %+v, want a non-empty arguments string", item)
		}
	}
}

// Known no-op events stay out of the capture's [unrecognized-chunk]
// markers so a real capture file keeps the signal for genuinely unknown
// events (the Anthropic reader sets the same precedent).
func TestResponsesWireCaptureQuietsKnownNoOps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MARSHAL_WIRE_CAPTURE", dir)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.done\",\"text\":\"hi\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"weird_future_event\",\"x\":1}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()

	p, err := NewOpenAICompatible(Options{Name: "wiretest-responses", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible returned error: %v", err)
	}
	req := chatReq(true)
	req.Model = "grok-4.7"
	events, err := p.responsesChat(t.Context(), req)
	if err != nil {
		t.Fatalf("responsesChat returned error: %v", err)
	}
	var answer strings.Builder
	for {
		ev, ok := recvEvent(t, events)
		if !ok {
			t.Fatal("channel closed before done event")
		}
		if ev.Type == schema.ChatEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == schema.ChatEventDone {
			break
		}
		if ev.Type == schema.ChatEventDelta {
			answer.WriteString(ev.Delta)
		}
	}
	if answer.String() != "hi" {
		t.Fatalf("streamed answer = %q, want hi", answer.String())
	}

	matches, err := filepath.Glob(filepath.Join(dir, "wiretest-responses-*.stream"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one capture file, got %v (err=%v)", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if !strings.Contains(string(data), "[unrecognized-chunk] {\"type\":\"weird_future_event\"") {
		t.Fatalf("capture missing marker for the unknown event:\n%s", data)
	}
	for _, quiet := range []string{"response.created", "response.output_text.done"} {
		if strings.Contains(string(data), "[unrecognized-chunk] {\"type\":\""+quiet+"\"") {
			t.Errorf("known no-op event %s wrongly flagged:\n%s", quiet, data)
		}
	}
}
