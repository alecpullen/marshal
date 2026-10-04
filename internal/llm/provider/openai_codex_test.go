package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/credentials"
	"marshal/internal/llm/schema"
	"marshal/internal/oauth"
)

// --- fixtures ---

// codexTestToken builds a synthetic JWT carrying the namespaced account-id
// claim. It is unsigned and structurally valid only — the provider decodes
// the payload without verifying a signature, so this is sufficient to
// exercise the extraction path. No real token material appears here.
func codexTestToken(accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	claims := map[string]any{
		"sub": "user-test",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	if accountID != "" {
		claims[codexAuthClaimPath] = map[string]any{
			codexAccountIDClaim: accountID,
			"chatgpt_plan_type": "plus",
		}
	}
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".signature"
}

// codexTestEngine returns an engine backed by an in-memory store holding a
// valid, non-expired token. The store is returned so tests can inspect it.
func codexTestEngine(t *testing.T, token string) (*oauth.Engine, *credentials.MemStore) {
	t.Helper()
	store := credentials.NewMemStore()
	raw, err := json.Marshal(oauth.StoredTokens{
		AccessToken:  token,
		RefreshToken: "refresh-test",
		ExpiresAt:    time.Now().Add(time.Hour),
		IssuedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("marshal stored tokens: %v", err)
	}
	if err := store.Set("marshal:provider:codex", raw); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	return &oauth.Engine{
		ServerURL:  codexIssuer,
		StorageKey: "marshal:provider:codex",
		Store:      store,
		Flow: oauth.FlowConfig{
			Issuer:       codexIssuer,
			AuthorizeURL: codexAuthorizeURL,
			TokenURL:     codexTokenURL,
			ClientID:     codexClientID,
		},
	}, store
}

// newTestCodex builds a codex provider pointed at a test server.
func newTestCodex(t *testing.T, baseURL string, engine *oauth.Engine) *OpenAICodex {
	t.Helper()
	caps := schema.ProviderCapabilities{
		ToolCalling:      true,
		StructuredOutput: true,
		Reasoning:        true,
	}
	p, err := NewOpenAICodex(CodexOptions{
		Name:         "codex",
		BaseURL:      baseURL,
		Engine:       engine,
		Capabilities: &caps,
		StaticModels: []string{"gpt-5.6-luna"},
	})
	if err != nil {
		t.Fatalf("NewOpenAICodex: %v", err)
	}
	return p
}

// codexSSE is a minimal but realistic codex Responses stream: the event
// sequence the live spike observed (spike §3), ending in response.completed
// with a usage object.
const codexSSE = `event: response.created
data: {"type":"response.created","response":{"status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":0,"delta":"OK"}

event: response.output_text.done
data: {"type":"response.output_text.done","output_index":0,"text":"OK"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}}

event: response.completed
data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":11,"output_tokens":2,"total_tokens":13}}}

`

// codexQuotaHeaders is the header set the live endpoint returns on every 2xx
// response (spike §5).
func codexQuotaHeaders() map[string]string {
	return map[string]string{
		"X-Codex-Plan-Type":                     "plus",
		"X-Codex-Primary-Used-Percent":          "50",
		"X-Codex-Primary-Reset-After-Seconds":   "14845",
		"X-Codex-Primary-Window-Minutes":        "300",
		"X-Codex-Secondary-Used-Percent":        "8",
		"X-Codex-Secondary-Reset-After-Seconds": "601645",
		"X-Codex-Secondary-Window-Minutes":      "10080",
	}
}

// --- account id extraction ---

func TestAccountIDFromAccessToken(t *testing.T) {
	tok := codexTestToken("acct-123")
	if got := accountIDFromAccessToken(tok); got != "acct-123" {
		t.Fatalf("accountIDFromAccessToken = %q, want %q", got, "acct-123")
	}
}

func TestAccountIDFromAccessTokenOpaque(t *testing.T) {
	// An opaque token is not a JWT: the provider must omit the header
	// rather than panic or fail the request.
	for _, tok := range []string{"", "opaque-token", "a.b", "a.b.c.d", "not..base64!"} {
		if got := accountIDFromAccessToken(tok); got != "" {
			t.Errorf("accountIDFromAccessToken(%q) = %q, want empty", tok, got)
		}
	}
}

func TestAccountIDFromAccessTokenMissingClaim(t *testing.T) {
	// A structurally valid JWT with no namespaced claim yields "".
	tok := codexTestToken("")
	if got := accountIDFromAccessToken(tok); got != "" {
		t.Fatalf("accountIDFromAccessToken = %q, want empty", got)
	}
}

// --- request shape ---

func TestCodexChatRequestShape(t *testing.T) {
	var gotBody map[string]any
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSSE))
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p := newTestCodex(t, server.URL, engine)

	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model: "gpt-5.6-luna",
		Messages: []schema.ChatMessage{
			{Role: schema.RoleSystem, Content: "Marshal harness capabilities"},
			{Role: schema.RoleSystem, Content: "Repository instructions"},
			{Role: schema.RoleUser, Content: "hi"},
			{Role: schema.RoleSystem, Content: "Runtime guidance"},
			{Role: schema.RoleAssistant, Content: "working"},
			{Role: schema.RoleSystem, Content: "Follow-up guidance"},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}

	// Headers: the codex-specific set from spike §3.
	if got := gotHeaders.Get("Authorization"); got != "Bearer "+codexTestToken("acct-abc") {
		t.Errorf("Authorization = %q", got)
	}
	if got := gotHeaders.Get(codexOriginatorHeader); got != codexOriginator {
		t.Errorf("originator = %q, want %q", got, codexOriginator)
	}
	if got := gotHeaders.Get(codexAccountIDHeader); got != "acct-abc" {
		t.Errorf("account id header = %q, want %q", got, "acct-abc")
	}
	if got := gotHeaders.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q", got)
	}

	// Body: store=false, stream=true, and the required include list.
	if got := gotBody["instructions"]; got != "Marshal harness capabilities\n\nRepository instructions" {
		t.Errorf("instructions = %v, want complete harness prompt", got)
	}
	input, ok := gotBody["input"].([]any)
	if !ok || len(input) != 4 {
		t.Fatalf("input = %v, want four conversation messages", gotBody["input"])
	}
	for i, role := range []string{"user", "developer", "assistant", "developer"} {
		item := input[i].(map[string]any)
		if item["role"] != role {
			t.Errorf("input[%d].role = %v, want %s", i, item["role"], role)
		}
	}
	for i, text := range []string{"hi", "Runtime guidance", "working", "Follow-up guidance"} {
		item := input[i].(map[string]any)
		parts := item["content"].([]any)
		if parts[0].(map[string]any)["text"] != text {
			t.Errorf("input[%d] lost its content", i)
		}
	}
	if got := gotBody["store"]; got != false {
		t.Errorf("store = %v, want false", got)
	}
	if got := gotBody["stream"]; got != true {
		t.Errorf("stream = %v, want true", got)
	}
	include, ok := gotBody["include"].([]any)
	if !ok || len(include) != 1 || include[0] != codexIncludeReasoning {
		t.Errorf("include = %v, want [%q]", gotBody["include"], codexIncludeReasoning)
	}
}

func TestCodexChatOmitsAccountHeaderForOpaqueToken(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSSE))
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, "opaque-token")
	p := newTestCodex(t, server.URL, engine)

	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}
	if got := gotHeaders.Get(codexAccountIDHeader); got != "" {
		t.Errorf("account id header = %q, want absent for an opaque token", got)
	}
}

// --- streaming ---

func TestCodexChatStreamsDeltasAndDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSSE))
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p := newTestCodex(t, server.URL, engine)

	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	ev, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before first event")
	}
	if ev.Type != schema.ChatEventDelta || ev.Delta != "OK" {
		t.Fatalf("first event = %+v, want Delta %q", ev, "OK")
	}

	done, ok := recvEvent(t, events)
	if !ok {
		t.Fatal("channel closed before done event")
	}
	if done.Type != schema.ChatEventDone {
		t.Fatalf("second event = %+v, want Done", done)
	}
	if done.Usage == nil || done.Usage.PromptTokens != 11 || done.Usage.CompletionTokens != 2 {
		t.Fatalf("usage = %+v, want 11/2", done.Usage)
	}
	assertChannelClosed(t, events)
}

func TestCodexChatAttachesQuotaToDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range codexQuotaHeaders() {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSSE))
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p := newTestCodex(t, server.URL, engine)

	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var done schema.ChatEvent
	for ev := range events {
		if ev.Type == schema.ChatEventDone {
			done = ev
		}
	}
	if done.Quota == nil {
		t.Fatal("Done event has no Quota; the x-codex-* headers were not parsed")
	}
	if done.Quota.PlanType != "plus" {
		t.Errorf("plan type = %q, want plus", done.Quota.PlanType)
	}
	if done.Quota.PrimaryUsedPercent != 50 {
		t.Errorf("primary used = %d, want 50", done.Quota.PrimaryUsedPercent)
	}
	if done.Quota.PrimaryResetAfterSecs != 14845 {
		t.Errorf("primary reset = %d, want 14845", done.Quota.PrimaryResetAfterSecs)
	}
	if done.Quota.PrimaryWindowMinutes != 300 {
		t.Errorf("primary window = %d, want 300", done.Quota.PrimaryWindowMinutes)
	}
	if done.Quota.SecondaryUsedPercent != 8 {
		t.Errorf("secondary used = %d, want 8", done.Quota.SecondaryUsedPercent)
	}
	if done.Quota.SecondaryWindowMinutes != 10080 {
		t.Errorf("secondary window = %d, want 10080", done.Quota.SecondaryWindowMinutes)
	}
}

func TestCodexChatNoQuotaHeadersLeavesQuotaNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSSE))
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p := newTestCodex(t, server.URL, engine)

	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for ev := range events {
		if ev.Type == schema.ChatEventDone && ev.Quota != nil {
			t.Fatalf("Quota = %+v, want nil when no quota headers are present", ev.Quota)
		}
	}
}

// --- error mapping ---

func TestCodexChatMaps400Detail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"The 'gpt-5.2-codex' model is not supported when using Codex with a ChatGPT account."}`))
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p := newTestCodex(t, server.URL, engine)

	_, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.2-codex",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error for HTTP 400")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %T (%v), want *ProviderError", err, err)
	}
	if pe.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", pe.StatusCode)
	}
	// The detail is surfaced without the JSON wrapper.
	if !strings.Contains(pe.Body, "not supported when using Codex") {
		t.Errorf("body = %q, want the detail text", pe.Body)
	}
	if strings.Contains(pe.Body, `{"detail"`) {
		t.Errorf("body = %q, want the raw JSON wrapper stripped", pe.Body)
	}
}

func TestCodexChatMaps401ToAuthRequired(t *testing.T) {
	// The first call 401s; the forced refresh also fails (the refresh
	// endpoint is unreachable), so the auth error surfaces.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(codexErrorCodeHeader, "token_expired")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"token expired"}}`))
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	// Point the token endpoint at a closed server so the forced refresh
	// fails deterministically.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	engine.Flow.TokenURL = deadURL

	p := newTestCodex(t, server.URL, engine)

	_, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error for HTTP 401")
	}
	// The refresh failure surfaces (it is the more actionable error), and
	// it must not be a silent success.
	if !strings.Contains(err.Error(), "token endpoint") && !errors.Is(err, oauth.ErrAuthSentinel) {
		t.Fatalf("error = %v, want a token-endpoint or auth-required error", err)
	}
}

func TestCodexChatRetriesOnceAfter401(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set(codexErrorCodeHeader, "token_expired")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"token expired"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSSE))
	}))
	defer server.Close()

	// The refresh endpoint returns a fresh token, so the retry succeeds.
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  codexTestToken("acct-abc"),
			"refresh_token": "refresh-2",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	defer refresh.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	engine.Flow.TokenURL = refresh.URL

	p := newTestCodex(t, server.URL, engine)

	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat should have retried and succeeded: %v", err)
	}
	var sawDone bool
	for ev := range events {
		if ev.Type == schema.ChatEventDone {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatal("retry did not produce a Done event")
	}
	if calls != 2 {
		t.Fatalf("server saw %d calls, want exactly 2 (original + one retry)", calls)
	}
}

// TestCodexChatDouble401MapsToAuthRequired pins the contract that a 401
// always becomes *oauth.ErrAuthRequired, including when the forced refresh
// succeeds but the retry is rejected too. The other 401 tests either fail
// the refresh first or end in success, so neither exercises mapError's 401
// branch.
func TestCodexChatDouble401MapsToAuthRequired(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set(codexErrorCodeHeader, "invalid_token")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid token"}}`))
	}))
	defer server.Close()

	// The refresh succeeds, so the retry is attempted and 401s again.
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  codexTestToken("acct-abc"),
			"refresh_token": "refresh-2",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	defer refresh.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	engine.Flow.TokenURL = refresh.URL

	p := newTestCodex(t, server.URL, engine)
	_, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error after two 401s")
	}
	if !errors.Is(err, oauth.ErrAuthSentinel) {
		t.Fatalf("error = %T (%v), want *oauth.ErrAuthRequired", err, err)
	}
	var authErr *oauth.ErrAuthRequired
	if !errors.As(err, &authErr) {
		t.Fatalf("error = %v, want errors.As to find *oauth.ErrAuthRequired", err)
	}
	if authErr.ServerName != "codex" {
		t.Errorf("ServerName = %q, want codex", authErr.ServerName)
	}
	if !strings.Contains(authErr.Reason, "invalid_token") {
		t.Errorf("Reason = %q, want the X-Openai-Ide-Error-Code value", authErr.Reason)
	}
	if calls != 2 {
		t.Fatalf("server saw %d calls, want exactly 2 (original + one retry)", calls)
	}
}

func TestCodexChatAuthRequiredWhenNoToken(t *testing.T) {
	store := credentials.NewMemStore()
	engine := &oauth.Engine{
		ServerURL:  codexIssuer,
		StorageKey: "marshal:provider:codex",
		Store:      store,
		Flow: oauth.FlowConfig{
			Issuer:       codexIssuer,
			AuthorizeURL: codexAuthorizeURL,
			TokenURL:     codexTokenURL,
			ClientID:     codexClientID,
		},
	}
	p := newTestCodex(t, "http://127.0.0.1:1", engine)

	_, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error when no token is stored")
	}
	if !errors.Is(err, oauth.ErrAuthSentinel) {
		t.Fatalf("error = %v, want *oauth.ErrAuthRequired", err)
	}
}

// --- constructor ---

func TestNewOpenAICodexRequiresEngine(t *testing.T) {
	_, err := NewOpenAICodex(CodexOptions{Name: "codex", BaseURL: "https://example.com"})
	if err == nil {
		t.Fatal("expected an error when Engine is nil")
	}
}

func TestNewOpenAICodexTrimsTrailingSlash(t *testing.T) {
	engine, _ := codexTestEngine(t, codexTestToken("acct"))
	p, err := NewOpenAICodex(CodexOptions{
		Name:    "codex",
		BaseURL: "https://example.com/backend-api/",
		Engine:  engine,
	})
	if err != nil {
		t.Fatalf("NewOpenAICodex: %v", err)
	}
	if p.baseURL != "https://example.com/backend-api" {
		t.Fatalf("baseURL = %q, want the trailing slash trimmed", p.baseURL)
	}
}

func TestCodexCapabilities(t *testing.T) {
	engine, _ := codexTestEngine(t, codexTestToken("acct"))
	p := newTestCodex(t, "https://example.com", engine)
	caps := p.Capabilities(context.Background())
	if !caps.ToolCalling || !caps.StructuredOutput || !caps.Reasoning {
		t.Fatalf("capabilities = %+v, want tool/structured/reasoning all true", caps)
	}
}
