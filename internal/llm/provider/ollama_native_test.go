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

	"marshal/internal/llm/provider/limits"
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
	numCtx := 32768
	p := newTestOllama(t, server.URL)
	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model: "qwen2.5-coder:7b",
		Messages: []schema.ChatMessage{
			{Role: schema.RoleSystem, Content: "be terse"},
			{Role: schema.RoleUser, Content: "hi"},
		},
		Temperature:   &temp,
		MaxTokens:     &maxTok,
		ContextWindow: &numCtx,
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
	if opts["num_ctx"] != float64(32768) {
		t.Fatalf("options.num_ctx = %v, want 32768", opts["num_ctx"])
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

func TestOllamaCapabilityProbeStripsTools(t *testing.T) {
	var chatBodies []map[string]any
	var showCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			showCalls++
			// Model without "tools" in its capabilities array.
			_, _ = w.Write([]byte(`{"capabilities":["completion"]}`))
		case "/api/chat":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode chat body: %v", err)
			}
			chatBodies = append(chatBodies, body)
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	p := newTestOllama(t, server.URL)
	req := chatReq(false)
	req.Tools = []schema.ToolDefinition{{Name: "read_file", Parameters: json.RawMessage(`{}`)}}

	for i := 0; i < 2; i++ {
		events, err := p.Chat(t.Context(), req)
		if err != nil {
			t.Fatalf("Chat %d: %v", i, err)
		}
		for range events {
		}
	}

	if len(chatBodies) != 2 {
		t.Fatalf("got %d chat requests, want 2", len(chatBodies))
	}
	for i, body := range chatBodies {
		if _, present := body["tools"]; present {
			t.Fatalf("chat request %d still contains tools after caps probe", i)
		}
	}
	if showCalls != 1 {
		t.Fatalf("/api/show called %d times, want 1 (result must be cached)", showCalls)
	}
}

func TestOllamaCapabilityProbeKeepsToolsWhenSupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			_, _ = w.Write([]byte(`{"capabilities":["completion","tools"]}`))
		case "/api/chat":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, present := body["tools"]; !present {
				t.Fatal("tools stripped despite model advertising tools capability")
			}
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true}`))
		}
	}))
	defer server.Close()

	p := newTestOllama(t, server.URL)
	req := chatReq(false)
	req.Tools = []schema.ToolDefinition{{Name: "read_file", Parameters: json.RawMessage(`{}`)}}
	events, err := p.Chat(t.Context(), req)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}
}

func TestOllamaModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen2.5-coder:7b","model":"qwen2.5-coder:7b","size":4683071232,"details":{"family":"qwen2","parameter_size":"7.6B"}},{"name":"llama3.1:8b","model":"llama3.1:8b","size":4661211424,"details":{"family":"llama","parameter_size":"8.0B"}}]}`))
	}))
	defer server.Close()

	p := newTestOllama(t, server.URL)
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	if models[0].ID != "qwen2.5-coder:7b" || models[0].OwnedBy != "ollama" {
		t.Fatalf("models[0] = %+v", models[0])
	}
	if models[1].ID != "llama3.1:8b" {
		t.Fatalf("models[1] = %+v", models[1])
	}
}

func TestOllamaCapabilityProbeFallbackOnOldServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			// Old Ollama: no capabilities field at all.
			_, _ = w.Write([]byte(`{"modelfile":"FROM llama3.1"}`))
		case "/api/chat":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			// ProviderConfig.ToolCalling=true (set below) is the fallback.
			if _, present := body["tools"]; !present {
				t.Fatal("tools stripped; expected config fallback to keep them")
			}
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true}`))
		}
	}))
	defer server.Close()

	caps := DefaultCapabilities()
	caps.ToolCalling = true
	p, err := NewOllamaNative(Options{Name: "test-ollama", BaseURL: server.URL, Capabilities: &caps})
	if err != nil {
		t.Fatalf("NewOllamaNative: %v", err)
	}
	req := chatReq(false)
	req.Tools = []schema.ToolDefinition{{Name: "read_file", Parameters: json.RawMessage(`{}`)}}
	events, err := p.Chat(t.Context(), req)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}
}

func TestOllamaModelsPropagatesToolCallingFromLimitsTable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen2.5-coder:7b"}]}`))
	}))
	defer server.Close()

	toolCalling := true
	table := limits.NewTable(map[string]limits.Limit{
		"qwen2.5-coder:7b": {ContextWindow: 32768, MaxOutputTokens: 8192, ToolCalling: &toolCalling},
	})
	caps := DefaultCapabilities()
	caps.ToolCalling = true
	p, err := NewOllamaNative(Options{Name: "test-ollama", BaseURL: server.URL, Capabilities: &caps, LimitsTable: &table})
	if err != nil {
		t.Fatalf("NewOllamaNative: %v", err)
	}

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	got := models[0]
	if got.ContextWindow != 32768 || got.MaxOutputTokens != 8192 {
		t.Errorf("limits = %+v, want context=32768 maxOutput=8192", got)
	}
	if got.ToolCalling == nil || !*got.ToolCalling {
		t.Errorf("ToolCalling = %v, want true", got.ToolCalling)
	}
}

func TestOllamaProbeCapabilitiesParsesToolsAndContextLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/show" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"capabilities":["completion","tools"],"model_info":{"general.architecture":"qwen3","qwen3.context_length":40960}}`))
	}))
	defer server.Close()

	p := newTestOllama(t, server.URL)
	got := p.ProbeCapabilities(t.Context(), "qwen3-coder:30b")
	if got.ToolCalling == nil || !*got.ToolCalling {
		t.Errorf("ToolCalling = %v, want true", got.ToolCalling)
	}
	if got.ContextWindow != 40960 {
		t.Errorf("ContextWindow = %d, want 40960", got.ContextWindow)
	}
}

func TestOllamaProbeCapabilitiesCachesPerModel(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"capabilities":["tools"],"model_info":{"llama.context_length":128000}}`))
	}))
	defer server.Close()

	p := newTestOllama(t, server.URL)
	p.ProbeCapabilities(t.Context(), "llama3.1:8b")
	p.ProbeCapabilities(t.Context(), "llama3.1:8b")
	if calls != 1 {
		t.Errorf("/api/show called %d times, want 1 (cached)", calls)
	}
}

func TestOllamaProbeCapabilitiesOldServerReportsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"modelfile":"FROM llama3.1"}`))
	}))
	defer server.Close()

	p := newTestOllama(t, server.URL)
	got := p.ProbeCapabilities(t.Context(), "llama3.1:8b")
	if got.ToolCalling != nil {
		t.Errorf("ToolCalling = %v, want nil for old server with no capabilities field", got.ToolCalling)
	}
	if got.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0", got.ContextWindow)
	}
}

func TestOllamaImplementsCapabilityProber(t *testing.T) {
	var _ CapabilityProber = (*OllamaNative)(nil)
}

func TestOllamaStreamingToolCallNotDuplicated(t *testing.T) {
	// Ollama sends cumulative tool call state per chunk, not deltas.
	// Chunk 1 has tool call A with partial args; chunk 2 (done) has
	// tool call A with complete args. The final ToolCalls should have
	// exactly one call with the complete args, not two calls.
	body := strings.Join([]string{
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_weather","arguments":{"city":"S"}}}]},"done":false}`,
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_weather","arguments":{"city":"SF"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`,
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	p, err := NewOllamaNative(Options{Name: "test-ollama", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOllamaNative: %v", err)
	}
	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "test-model",
		Stream:   true,
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "weather?"}},
		Tools: []schema.ToolDefinition{{
			Name:       "get_weather",
			Parameters: json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var toolCalls []schema.ToolCall
	for ev := range events {
		if ev.Type == schema.ChatEventDone {
			toolCalls = ev.ToolCalls
		}
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d: %+v", len(toolCalls), toolCalls)
	}
	if toolCalls[0].Name != "get_weather" {
		t.Errorf("tool call name = %q, want get_weather", toolCalls[0].Name)
	}
	wantArgs := `{"city":"SF"}`
	if string(toolCalls[0].Args) != wantArgs {
		t.Errorf("tool call args = %q, want %q", string(toolCalls[0].Args), wantArgs)
	}
}

func TestOllamaWireCaptureMarksEmptyChunks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MARSHAL_WIRE_CAPTURE", dir)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, "{\"message\":{\"role\":\"assistant\",\"content\":\"hi\"},\"done\":false}\n")
		fmt.Fprint(w, "{\"message\":{\"role\":\"assistant\"},\"done\":false}\n")
		fmt.Fprint(w, "{\"message\":{\"role\":\"assistant\"},\"done\":true,\"done_reason\":\"stop\"}\n")
	}))
	defer server.Close()

	p, err := NewOllamaNative(Options{Name: "wiretest-ollama", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOllamaNative: %v", err)
	}
	events, err := p.Chat(context.Background(), schema.ChatRequest{
		Model:    "m",
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

	matches, err := filepath.Glob(filepath.Join(dir, "wiretest-ollama-*.stream"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one capture file, got %v (err=%v)", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if !strings.Contains(string(data), "[unrecognized-chunk] {\"message\":{\"role\":\"assistant\"},\"done\":false}") {
		t.Fatalf("capture missing marker for empty chunk:\n%s", data)
	}
	if strings.Contains(string(data), "[unrecognized-chunk] {\"message\":{\"role\":\"assistant\"},\"done\":true") {
		t.Fatalf("terminal done chunk wrongly flagged:\n%s", data)
	}
}
