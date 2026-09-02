package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/llm/schema"
)

// startCaptureTestServer serves one normal content chunk, one chunk the
// decoder recognizes only for usage, one chunk with nothing recognized,
// and [DONE].
func startCaptureTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2},\"choices\":[]}\n\n")
		fmt.Fprint(w, "data: {\"meta\":true}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	return server
}

func drainEvents(t *testing.T, events <-chan schema.ChatEvent) string {
	t.Helper()
	var sb strings.Builder
	for ev := range events {
		if ev.Type == schema.ChatEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		sb.WriteString(ev.Delta)
	}
	return sb.String()
}

func readCaptureFile(t *testing.T, dir, providerName, suffix string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, providerName+"-*."+suffix))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one %s capture file for %q, got %v (err=%v)", suffix, providerName, matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	return string(data)
}

func TestWireCaptureTeesStreamAndMarksUnrecognizedChunks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MARSHAL_WIRE_CAPTURE", dir)

	server := startCaptureTestServer(t)
	p, err := NewOpenAICompatible(Options{Name: "wiretest", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible: %v", err)
	}
	events, err := p.Chat(context.Background(), schema.ChatRequest{
		Model:    "m",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := drainEvents(t, events); got != "hi" {
		t.Fatalf("streamed content = %q, want %q", got, "hi")
	}

	capture := readCaptureFile(t, dir, "wiretest", "stream")
	if !strings.Contains(capture, "\"content\":\"hi\"") {
		t.Fatalf("capture missing raw stream bytes:\n%s", capture)
	}
	if !strings.Contains(capture, "[unrecognized-chunk] {\"meta\":true}") {
		t.Fatalf("capture missing unrecognized-chunk marker:\n%s", capture)
	}
	// The usage-only chunk is recognized: it must not be flagged.
	if strings.Contains(capture, "[unrecognized-chunk] {\"usage\"") {
		t.Fatalf("usage-only chunk wrongly flagged:\n%s", capture)
	}
}

func TestWireCaptureRequestBodiesRecordThinkingEffort(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(wireCaptureRequestsEnvVar, dir)

	server := startCaptureTestServer(t)
	p, err := NewOpenAICompatible(Options{Name: "wiretest", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible: %v", err)
	}
	events, err := p.Chat(context.Background(), schema.ChatRequest{
		Model:    "m",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
		Stream:   true,
		Thinking: "high",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := drainEvents(t, events); got != "hi" {
		t.Fatalf("streamed content = %q, want %q", got, "hi")
	}

	reqBody := readCaptureFile(t, dir, "wiretest", "request.json")
	if !strings.Contains(reqBody, `"reasoning_effort":"high"`) {
		t.Fatalf("request capture missing reasoning_effort:\n%s", reqBody)
	}
}

func TestWireCaptureRequestsDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	// Deliberately leave MARSHAL_WIRE_CAPTURE_REQUESTS unset.
	t.Setenv(wireCaptureRequestsEnvVar, "")

	server := startCaptureTestServer(t)
	p, err := NewOpenAICompatible(Options{Name: "wiretest", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible: %v", err)
	}
	events, err := p.Chat(context.Background(), schema.ChatRequest{
		Model:    "m",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
		Stream:   true,
		Thinking: "high",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drainEvents(t, events)

	matches, err := filepath.Glob(filepath.Join(dir, "*.request.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("request capture wrote files with the env var disabled: %v", matches)
	}
}

func TestWireCaptureDisabledWhenEnvUnset(t *testing.T) {
	t.Setenv("MARSHAL_WIRE_CAPTURE", "")

	server := startCaptureTestServer(t)
	p, err := NewOpenAICompatible(Options{Name: "wiretest", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible: %v", err)
	}
	events, err := p.Chat(context.Background(), schema.ChatRequest{
		Model:    "m",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drainEvents(t, events)
	// Nothing to assert on disk — the temp dir is not the capture dir —
	// the request succeeding with capture "enabled-but-empty" is the test.
}

func TestWireCaptureNeverFailsRequest(t *testing.T) {
	// Point the capture dir through an existing file so MkdirAll fails.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARSHAL_WIRE_CAPTURE", filepath.Join(blocker, "sub"))

	server := startCaptureTestServer(t)
	p, err := NewOpenAICompatible(Options{Name: "wiretest", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAICompatible: %v", err)
	}
	events, err := p.Chat(context.Background(), schema.ChatRequest{
		Model:    "m",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := drainEvents(t, events); got != "hi" {
		t.Fatalf("streamed content = %q, want %q", got, "hi")
	}
}

func TestWireCaptureNilAnnotateIsSafe(t *testing.T) {
	var w *wireCapture
	w.annotate("[unrecognized-chunk]", "raw") // must not panic

	body := io.NopCloser(strings.NewReader("data"))
	if got := w.wrap(body); got != io.ReadCloser(body) {
		t.Fatal("nil capture must pass the body through unchanged")
	}
}

func TestSanitizeCaptureName(t *testing.T) {
	if got := sanitizeCaptureName("a/b c:d"); got != "a_b_c_d" {
		t.Fatalf("sanitizeCaptureName = %q, want %q", got, "a_b_c_d")
	}
	if got := sanitizeCaptureName("openai_compatible"); got != "openai_compatible" {
		t.Fatalf("sanitizeCaptureName = %q, want unchanged", got)
	}
}
