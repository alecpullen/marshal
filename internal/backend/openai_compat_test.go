package backend_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alec/marshal/internal/backend"
)

// sseServer builds a test HTTP server that returns the given SSE lines.
func sseServer(t *testing.T, statusCode int, lines []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if statusCode != http.StatusOK {
			w.WriteHeader(statusCode)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
	}))
}

// buildSSELine encodes a stream event delta as a "data: {...}" SSE line.
func buildSSELine(content, finishReason string) string {
	type delta struct {
		Content string `json:"content,omitempty"`
	}
	type choice struct {
		Delta        delta  `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	}
	type event struct {
		Choices []choice `json:"choices"`
	}
	e := event{Choices: []choice{{
		Delta:        delta{Content: content},
		FinishReason: finishReason,
	}}}
	b, _ := json.Marshal(e)
	return "data: " + string(b)
}

func newTestBackend(t *testing.T, srv *httptest.Server) backend.Backend {
	t.Helper()
	return backend.NewOpenAICompat(backend.OpenAICompatConfig{
		BaseURL:     srv.URL,
		APIKey:      "test-key",
		ModelName:   "test-model",
		SupTools:    true,
		SupJSONMode: true,
		HTTPClient:  srv.Client(),
	}, nil)
}

// --- Stream tests ------------------------------------------------------------

func TestStream_TokensArrive(t *testing.T) {
	lines := []string{
		buildSSELine("Hello", ""),
		buildSSELine(", ", ""),
		buildSSELine("world", ""),
		buildSSELine("", "stop"),
		"data: [DONE]",
	}
	srv := sseServer(t, http.StatusOK, lines)
	defer srv.Close()

	b := newTestBackend(t, srv)
	ch, err := b.Stream(context.Background(), backend.Request{
		Messages: []backend.Message{{Role: backend.MessageRoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var got strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("unexpected error: %v", chunk.Err)
		}
		got.WriteString(chunk.Content)
	}
	if got.String() != "Hello, world" {
		t.Errorf("expected %q, got %q", "Hello, world", got.String())
	}
}

func TestStream_FinishReason(t *testing.T) {
	lines := []string{
		buildSSELine("hi", ""),
		buildSSELine("", "stop"),
		"data: [DONE]",
	}
	srv := sseServer(t, http.StatusOK, lines)
	defer srv.Close()

	b := newTestBackend(t, srv)
	ch, err := b.Stream(context.Background(), backend.Request{
		Messages: []backend.Message{{Role: backend.MessageRoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var finishReason string
	for chunk := range ch {
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
	}
	if finishReason != "stop" {
		t.Errorf("expected finish_reason=stop, got %q", finishReason)
	}
}

func TestStream_ContextCancel(t *testing.T) {
	// Server that streams slowly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 100; i++ {
			fmt.Fprintln(w, buildSSELine(fmt.Sprintf("token%d", i), ""))
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	b := newTestBackend(t, srv)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := b.Stream(ctx, backend.Request{
		Messages: []backend.Message{{Role: backend.MessageRoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read one chunk then cancel.
	<-ch
	cancel()

	// Drain; should close without hanging.
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed cleanly
			}
		case <-timeout:
			t.Fatal("channel did not close after context cancel")
		}
	}
}

// --- Complete tests ----------------------------------------------------------

func TestComplete_Success(t *testing.T) {
	type oaiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := oaiResp{}
		resp.Choices = append(resp.Choices, struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{FinishReason: "stop"})
		resp.Choices[0].Message.Content = "pong"
		resp.Usage.PromptTokens = 5
		resp.Usage.CompletionTokens = 1
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	b := newTestBackend(t, srv)
	resp, err := b.Complete(context.Background(), backend.Request{
		Messages: []backend.Message{{Role: backend.MessageRoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "pong" {
		t.Errorf("expected content=pong, got %q", resp.Content)
	}
	if resp.Usage.PromptTokens != 5 {
		t.Errorf("expected prompt_tokens=5, got %d", resp.Usage.PromptTokens)
	}
}

// --- Retry tests -------------------------------------------------------------

func TestRetry_429ThenSuccess(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// Third call succeeds with streaming.
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, buildSSELine("ok", "stop"))
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer srv.Close()

	b := newTestBackend(t, srv)
	ch, err := b.Stream(context.Background(), backend.Request{
		Messages: []backend.Message{{Role: backend.MessageRoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	for range ch {
	}

	if calls.Load() != 3 {
		t.Errorf("expected 3 calls (2 retries + 1 success), got %d", calls.Load())
	}
}

func TestRetry_401NoRetry(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key","type":"auth_error"}}`))
	}))
	defer srv.Close()

	b := newTestBackend(t, srv)
	_, err := b.Stream(context.Background(), backend.Request{
		Messages: []backend.Message{{Role: backend.MessageRoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 call (no retry on 401), got %d", calls.Load())
	}
}

// --- Interface / config tests ------------------------------------------------

func TestBackendMeta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	b := backend.NewOpenAICompat(backend.OpenAICompatConfig{
		BaseURL:     srv.URL,
		ModelName:   "my-model",
		SupTools:    true,
		SupJSONMode: false,
		HTTPClient:  srv.Client(),
	}, nil)

	if b.Model() != "my-model" {
		t.Errorf("Model()=%q", b.Model())
	}
	if !b.SupportsTools() {
		t.Error("expected SupportsTools=true")
	}
	if b.SupportsJSONMode() {
		t.Error("expected SupportsJSONMode=false")
	}
}
