package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"marshal/internal/llm/schema"
)

func TestPromptCacheKeyStableAndOpaque(t *testing.T) {
	a, b := promptCacheKey("sess_123"), promptCacheKey("sess_123")
	if a == "" || a != b {
		t.Fatalf("keys for one session = %q / %q, want equal and non-empty", a, b)
	}
	if promptCacheKey("sess_456") == a {
		t.Fatal("different sessions produced the same key")
	}
	if strings.Contains(a, "sess_123") {
		t.Fatalf("key %q leaks the session id", a)
	}
	if len(a) > 64 {
		t.Fatalf("key length %d, want <= 64", len(a))
	}
	if promptCacheKey("") != "" {
		t.Fatal("empty session must produce no key")
	}
}

func TestSendsPromptCacheKeyDetection(t *testing.T) {
	cases := []struct {
		baseURL, session string
		want             bool
	}{
		{"https://api.openai.com/v1", "s", true},
		{"https://api.openai.com/v1", "", false},
		{"https://api.openai.com.evil.example/v1", "s", false},
		{"https://openrouter.ai/api/v1", "s", false},
		{"http://localhost:1234/v1", "s", false},
	}
	for _, tc := range cases {
		p, err := NewOpenAICompatible(Options{Name: "x", BaseURL: tc.baseURL, SessionID: tc.session})
		if err != nil {
			t.Fatalf("NewOpenAICompatible: %v", err)
		}
		if got := p.sendsPromptCacheKey(); got != tc.want {
			t.Errorf("sendsPromptCacheKey(%q, session=%q) = %v, want %v", tc.baseURL, tc.session, got, tc.want)
		}
	}
}

// rewriteTransport sends every request to target, keeping the path, so a
// provider can be configured with a real hostname but served by httptest.
type rewriteTransport struct{ target *url.URL }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = rt.target.Scheme, rt.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func captureChatBody(t *testing.T, baseURL string, viaRewrite bool) map[string]any {
	t.Helper()
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)
	opts := Options{Name: "x", BaseURL: baseURL, SessionID: "sess_123"}
	if viaRewrite {
		target, _ := url.Parse(server.URL)
		opts.HTTPClient = &http.Client{Transport: rewriteTransport{target: target}}
	} else {
		opts.BaseURL = server.URL
	}
	p, err := NewOpenAICompatible(opts)
	if err != nil {
		t.Fatalf("NewOpenAICompatible: %v", err)
	}
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}
	return body
}

func TestOpenAIChatSendsPromptCacheKey(t *testing.T) {
	body := captureChatBody(t, "https://api.openai.com/v1", true)
	if got := body["prompt_cache_key"]; got != promptCacheKey("sess_123") {
		t.Fatalf("prompt_cache_key = %v, want %q", got, promptCacheKey("sess_123"))
	}
}

// Review focus 4: strict OpenAI-compatible servers reject unknown fields.
func TestOpenAICompatibleOmitsPromptCacheKeyElsewhere(t *testing.T) {
	body := captureChatBody(t, "", false)
	if _, ok := body["prompt_cache_key"]; ok {
		t.Fatalf("prompt_cache_key sent to a non-OpenAI endpoint: %v", body)
	}
}

func TestCodexSendsPromptCacheKey(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSSE))
	}))
	defer server.Close()
	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	caps := schema.ProviderCapabilities{ToolCalling: true, StructuredOutput: true, Reasoning: true}
	p, err := NewOpenAICodex(CodexOptions{
		Name: "codex", BaseURL: server.URL, Engine: engine, Capabilities: &caps,
		StaticModels: []string{"gpt-5.6-luna"}, SessionID: "sess_123",
	})
	if err != nil {
		t.Fatalf("NewOpenAICodex: %v", err)
	}
	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}
	if got := body["prompt_cache_key"]; got != promptCacheKey("sess_123") {
		t.Fatalf("prompt_cache_key = %v, want %q", got, promptCacheKey("sess_123"))
	}
}

// codexKeyServer serves codexSSE, except that a body carrying
// prompt_cache_key (or any body when onlyForKey is false) gets the given status and body when rejectStatus is non-zero.
func codexKeyServer(t *testing.T, rejectStatus int, rejectBody string, onlyForKey bool) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		if _, hasKey := body["prompt_cache_key"]; hasKey || !onlyForKey {
			if rejectStatus != 0 {
				w.WriteHeader(rejectStatus)
				_, _ = w.Write([]byte(rejectBody))
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSSE))
	}))
	t.Cleanup(server.Close)
	return server, &bodies
}

func codexChatOnce(t *testing.T, p *OpenAICodex) error {
	t.Helper()
	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		return err
	}
	for range events {
	}
	return nil
}

func newCodexKeyProvider(t *testing.T, baseURL string) *OpenAICodex {
	t.Helper()
	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	caps := schema.ProviderCapabilities{ToolCalling: true, StructuredOutput: true, Reasoning: true}
	p, err := NewOpenAICodex(CodexOptions{
		Name: "codex", BaseURL: baseURL, Engine: engine, Capabilities: &caps,
		StaticModels: []string{"gpt-5.6-luna"}, SessionID: "sess_123",
	})
	if err != nil {
		t.Fatalf("NewOpenAICodex: %v", err)
	}
	return p
}

// The endpoint's acceptance of prompt_cache_key is not live-verified, so a
// rejection must degrade to an uncached request rather than failing every
// Codex call.
func TestCodexRetriesWithoutPromptCacheKeyWhenRejected(t *testing.T) {
	server, bodies := codexKeyServer(t, http.StatusBadRequest, `{"detail":"Unsupported parameter: prompt_cache_key"}`, true)
	p := newCodexKeyProvider(t, server.URL)

	if err := codexChatOnce(t, p); err != nil {
		t.Fatalf("Chat after a prompt_cache_key rejection: %v", err)
	}
	if len(*bodies) != 2 {
		t.Fatalf("server saw %d requests, want 2 (rejected, then retried)", len(*bodies))
	}
	if _, ok := (*bodies)[0]["prompt_cache_key"]; !ok {
		t.Fatal("first request must carry prompt_cache_key")
	}
	if _, ok := (*bodies)[1]["prompt_cache_key"]; ok {
		t.Fatalf("retry still carries prompt_cache_key: %v", (*bodies)[1])
	}

	if err := codexChatOnce(t, p); err != nil {
		t.Fatalf("second Chat: %v", err)
	}
	if len(*bodies) != 3 {
		t.Fatalf("server saw %d requests after the second Chat, want 3 (no re-probe)", len(*bodies))
	}
	if _, ok := (*bodies)[2]["prompt_cache_key"]; ok {
		t.Fatal("later request sent prompt_cache_key after the endpoint rejected it")
	}
}

func TestCodexDoesNotRetryUnrelated400(t *testing.T) {
	server, bodies := codexKeyServer(t, http.StatusBadRequest, `{"detail":"Missing required parameter: include"}`, false)
	p := newCodexKeyProvider(t, server.URL)

	err := codexChatOnce(t, p)
	if err == nil || !strings.Contains(err.Error(), "Missing required parameter") {
		t.Fatalf("Chat error = %v, want the 400 detail surfaced", err)
	}
	if len(*bodies) != 1 {
		t.Fatalf("server saw %d requests, want 1 (no retry)", len(*bodies))
	}
	if _, ok := (*bodies)[0]["prompt_cache_key"]; !ok {
		t.Fatal("a failed unrelated 400 must not disable the cache key")
	}
}
