package provider

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/credentials"
	"marshal/internal/llm/provider/limits"
	"marshal/internal/oauth"
)

func TestNewFromConfigUsesLiteralAPIKey(t *testing.T) {
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

	pc := config.ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: server.URL,
		APIKey:  "literal-key",
	}
	p, err := NewFromConfig("test", pc, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig returned error: %v", err)
	}
	if _, err := p.Models(t.Context()); err != nil {
		t.Fatalf("Models returned error: %v", err)
	}

	if !sawHeader {
		t.Fatal("expected Authorization header to be present")
	}
	if gotAuth != "Bearer literal-key" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer literal-key")
	}
}

func TestNewFromConfigResolvesAPIKeyEnv(t *testing.T) {
	t.Setenv("SOME_ENV_VAR", "resolved-value")

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

	pc := config.ProviderConfig{
		Type:      "openai_compatible",
		BaseURL:   server.URL,
		APIKeyEnv: "SOME_ENV_VAR",
	}
	p, err := NewFromConfig("test", pc, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig returned error: %v", err)
	}
	if _, err := p.Models(t.Context()); err != nil {
		t.Fatalf("Models returned error: %v", err)
	}

	if !sawHeader {
		t.Fatal("expected Authorization header to be present")
	}
	if gotAuth != "Bearer resolved-value" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer resolved-value")
	}
}

func TestNewFromConfigPrefersLiteralAPIKeyOverAPIKeyEnv(t *testing.T) {
	t.Setenv("SOME_ENV_VAR", "env-value")

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

	pc := config.ProviderConfig{
		Type:      "openai_compatible",
		BaseURL:   server.URL,
		APIKey:    "literal-key",
		APIKeyEnv: "SOME_ENV_VAR",
	}
	p, err := NewFromConfig("test", pc, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig returned error: %v", err)
	}
	if _, err := p.Models(t.Context()); err != nil {
		t.Fatalf("Models returned error: %v", err)
	}

	if !sawHeader {
		t.Fatal("expected Authorization header to be present")
	}
	if gotAuth != "Bearer literal-key" {
		t.Fatalf("Authorization header = %q, want %q (literal must win over env)", gotAuth, "Bearer literal-key")
	}
}

func TestNewFromConfigErrorsWhenAPIKeyEnvUnset(t *testing.T) {
	pc := config.ProviderConfig{
		Type:      "openai_compatible",
		BaseURL:   "http://example.invalid",
		APIKeyEnv: "DEFINITELY_NOT_SET_ENV_VAR",
	}
	_, err := NewFromConfig("test", pc, "", false, 0)
	if err == nil {
		t.Fatal("expected NewFromConfig to return an error when api_key_env is unset")
	}
	if !strings.Contains(err.Error(), "DEFINITELY_NOT_SET_ENV_VAR") {
		t.Fatalf("error = %q, want it to mention the missing env var name", err.Error())
	}
}

func TestNewFromConfigPropagatesToolCallingCapability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	pc := config.ProviderConfig{
		Type:        "openai_compatible",
		BaseURL:     server.URL,
		APIKey:      "literal-key",
		ToolCalling: true,
	}
	p, err := NewFromConfig("test", pc, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig returned error: %v", err)
	}
	if !p.Capabilities(t.Context()).ToolCalling {
		t.Fatalf("Capabilities().ToolCalling = false, want true")
	}
}

func TestNewFromConfigToolCallingDefaultsToFalse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	pc := config.ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: server.URL,
	}
	p, err := NewFromConfig("test", pc, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig returned error: %v", err)
	}
	if p.Capabilities(t.Context()).ToolCalling {
		t.Fatalf("Capabilities().ToolCalling = true, want false")
	}
}

func TestNewFromConfigErrorsOnUnsupportedProviderType(t *testing.T) {
	pc := config.ProviderConfig{
		Type:    "native_anthropic",
		BaseURL: "https://api.anthropic.com",
	}
	_, err := NewFromConfig("test", pc, "", false, 0)
	if err == nil {
		t.Fatal("expected NewFromConfig to return an error for unsupported provider type")
	}
	if !strings.Contains(err.Error(), "native_anthropic") {
		t.Fatalf("error = %q, want it to mention the unsupported type", err.Error())
	}
}

func TestNewFromConfigOllamaNative(t *testing.T) {
	p, err := NewFromConfig("local", config.ProviderConfig{
		Type:    "ollama",
		BaseURL: "http://localhost:11434",
	}, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	on, ok := p.(*OllamaNative)
	if !ok {
		t.Fatalf("got %T, want *OllamaNative", p)
	}
	if on.keepAlive != defaultOllamaKeepAlive {
		t.Fatalf("keepAlive = %q, want default %q", on.keepAlive, defaultOllamaKeepAlive)
	}
}

func TestNewFromConfigOllamaNativeExplicitKeepAlive(t *testing.T) {
	p, err := NewFromConfig("local", config.ProviderConfig{
		Type:      "ollama",
		BaseURL:   "http://localhost:11434",
		KeepAlive: "-1",
	}, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if got := p.(*OllamaNative).keepAlive; got != "-1" {
		t.Fatalf("keepAlive = %q, want -1", got)
	}
}

// The factory must not let the native ollama backend claim structured
// output by default: ollama.com cloud ignores format/response_format
// constraints entirely (verified live 2026-09-03), so an envelope-mode
// session would get freeform text and fail action parsing. Structured
// output stays an explicit per-provider opt-in, mirroring tool_calling.
func TestNewFromConfigOllamaNativeDisclaimsStructuredOutput(t *testing.T) {
	p, err := NewFromConfig("cloud", config.ProviderConfig{
		Type:    "ollama",
		BaseURL: "https://ollama.com",
	}, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	caps := p.Capabilities(t.Context())
	if caps.JSONMode || caps.StructuredOutput {
		t.Fatalf("JSONMode=%v StructuredOutput=%v, want both false by default for type=ollama", caps.JSONMode, caps.StructuredOutput)
	}
	if caps.ToolCalling {
		t.Fatalf("ToolCalling = true, want false; the config did not enable it")
	}
}

func TestNewFromConfigOllamaNativeOptInStructuredOutput(t *testing.T) {
	p, err := NewFromConfig("local", config.ProviderConfig{
		Type:             "ollama",
		BaseURL:          "http://localhost:11434",
		ToolCalling:      true,
		StructuredOutput: true,
	}, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	caps := p.Capabilities(t.Context())
	if !caps.ToolCalling || !caps.JSONMode || !caps.StructuredOutput {
		t.Fatalf("ToolCalling=%v JSONMode=%v StructuredOutput=%v, want all true after explicit opt-in", caps.ToolCalling, caps.JSONMode, caps.StructuredOutput)
	}
}

func TestNewFromConfigAnthropic(t *testing.T) {
	t.Setenv("MARSHAL_TEST_FACTORY_ANTHROPIC_KEY", "sk-test")
	p, err := NewFromConfig("claude", config.ProviderConfig{
		Type:           "anthropic",
		BaseURL:        "https://api.anthropic.com",
		APIKeyEnv:      "MARSHAL_TEST_FACTORY_ANTHROPIC_KEY",
		ThinkingBudget: 4096,
	}, "", false, 8192)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	a, ok := p.(*Anthropic)
	if !ok {
		t.Fatalf("got %T, want *Anthropic", p)
	}
	if a.thinkingBudget != 4096 {
		t.Fatalf("thinkingBudget = %d, want 4096", a.thinkingBudget)
	}
	if a.thinkingBudgetMargin != 8192 {
		t.Fatalf("thinkingBudgetMargin = %d, want 8192", a.thinkingBudgetMargin)
	}
	if !a.capabilities.ToolCalling {
		t.Fatal("anthropic capabilities must advertise ToolCalling")
	}
}

func TestNewFromConfigAnthropicRequiresKey(t *testing.T) {
	_, err := NewFromConfig("claude", config.ProviderConfig{
		Type:    "anthropic",
		BaseURL: "https://api.anthropic.com",
	}, "", false, 0)
	if err == nil {
		t.Fatal("expected error for missing API key, got nil")
	}
}

func TestNewFromConfigReadsLimitsCacheWithoutRemoteDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o","owned_by":"openai"}]}`))
	}))
	defer server.Close()

	// Remote discovery is off, but a warm on-disk cache must still be read —
	// reading it makes no network requests.
	dataDir := t.TempDir()
	if err := limits.Save(dataDir, limits.Cache{
		Table:     map[string]limits.Limit{"gpt-4o": {ContextWindow: 128000, MaxOutputTokens: 16384}},
		FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed limits cache: %v", err)
	}

	p, err := NewFromConfig("test", config.ProviderConfig{Type: "openai_compatible", BaseURL: server.URL}, dataDir, false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig returned error: %v", err)
	}
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models returned error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ContextWindow != 128000 || models[0].MaxOutputTokens != 16384 {
		t.Fatalf("limits = %d/%d, want 128000/16384 from the on-disk cache",
			models[0].ContextWindow, models[0].MaxOutputTokens)
	}
}

func TestNewFromConfigOllamaReadsLimitsCacheWithoutRemoteDiscovery(t *testing.T) {
	dataDir := t.TempDir()
	if err := limits.Save(dataDir, limits.Cache{
		Table: map[string]limits.Limit{
			"qwen2.5-coder:7b": {ContextWindow: 32768, MaxOutputTokens: 8192},
		},
		FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed limits cache: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen2.5-coder:7b"}]}`))
	}))
	defer server.Close()

	p, err := NewFromConfig("ollama", config.ProviderConfig{
		Type:    "ollama",
		BaseURL: server.URL,
	}, dataDir, false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig returned error: %v", err)
	}
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models returned error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ContextWindow != 32768 || models[0].MaxOutputTokens != 8192 {
		t.Fatalf("limits = %d/%d, want 32768/8192 from the on-disk cache",
			models[0].ContextWindow, models[0].MaxOutputTokens)
	}
}

func TestNewFromConfigAnthropicReportsReasoning(t *testing.T) {
	p, err := NewFromConfig("anthropic", config.ProviderConfig{
		Type:    "anthropic",
		BaseURL: "https://api.anthropic.com",
		APIKey:  "test-key",
	}, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if !p.Capabilities(t.Context()).Reasoning {
		t.Fatal("anthropic provider must report Reasoning capability; NewFromConfig currently strips it")
	}
}

func TestNewFromConfigPropagatesTemperatureLockedCapability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	openaiPC := config.ProviderConfig{Type: "openai_compatible", BaseURL: server.URL, APIKey: "literal-key", TemperatureLocked: true}
	p, err := NewFromConfig("test", openaiPC, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if !p.Capabilities(t.Context()).TemperatureLocked {
		t.Fatal("openai_compatible: Capabilities().TemperatureLocked = false, want true")
	}

	ollamaPC := config.ProviderConfig{Type: "ollama", BaseURL: "http://localhost:11434", TemperatureLocked: true}
	p, err = NewFromConfig("test", ollamaPC, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if !p.Capabilities(t.Context()).TemperatureLocked {
		t.Fatal("ollama: Capabilities().TemperatureLocked = false, want true")
	}

	anthropicPC := config.ProviderConfig{Type: "anthropic", BaseURL: "https://api.anthropic.com", APIKey: "literal-key", TemperatureLocked: true}
	p, err = NewFromConfig("test", anthropicPC, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if !p.Capabilities(t.Context()).TemperatureLocked {
		t.Fatal("anthropic: Capabilities().TemperatureLocked = false, want true")
	}

	// Without the flag the capability stays false (no template/default leak).
	plainPC := config.ProviderConfig{Type: "openai_compatible", BaseURL: server.URL, APIKey: "literal-key"}
	p, err = NewFromConfig("test", plainPC, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if p.Capabilities(t.Context()).TemperatureLocked {
		t.Fatal("openai_compatible: TemperatureLocked = true, want false when unset")
	}
}

// ---- Injected auth resolvers and the codex engine factory ----
//
// Every test below uses in-memory stores and scripted resolvers. The
// production entry point — CodexOAuthEngine / NewCodexOAuthEngine — opens the
// OS keychain and writes into the process-wide codexEngines cache, so no test
// here may call it, seed it, or mutate it. The default adapter's delegation
// (defaultAuthResolver.CodexOAuthEngine → CodexOAuthEngine) is a one-line
// forward, verified by source review instead.

var (
	_ AuthResolver = defaultAuthResolver{}
	_ AuthResolver = (*fakeAuthResolver)(nil)
	_ AuthResolver = denyingAuthResolver{}
)

// recordingStore is an in-memory store that records every key read, so a test
// can prove which resolver's cache a token lookup actually used.
type recordingStore struct {
	*credentials.MemStore

	mu    sync.Mutex
	reads []string
}

func newRecordingStore() *recordingStore {
	return &recordingStore{MemStore: credentials.NewMemStore()}
}

func (s *recordingStore) Get(key string) ([]byte, error) {
	s.mu.Lock()
	s.reads = append(s.reads, key)
	s.mu.Unlock()
	return s.MemStore.Get(key)
}

func (s *recordingStore) readKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reads...)
}

var _ credentials.Store = (*recordingStore)(nil)

// seedCodexTokens stores one valid, non-expired token set under the key an
// engine built for name reads.
func seedCodexTokens(t *testing.T, store credentials.Store, name, access string) {
	t.Helper()
	raw, err := json.Marshal(oauth.StoredTokens{
		AccessToken:  access,
		RefreshToken: "refresh-" + access,
		ExpiresAt:    time.Now().Add(time.Hour),
		IssuedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("marshal stored tokens: %v", err)
	}
	if err := store.Set("marshal:provider:"+name, raw); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
}

// fakeAuthResolver scripts both resolver entry points and records which were
// used. Nothing is scripted by default: an unscripted call fails loudly
// rather than falling through to the production cache.
type fakeAuthResolver struct {
	mu         sync.Mutex
	engine     *oauth.Engine
	engineErr  error
	store      credentials.Store
	storeErr   error
	codexCalls []string
	storeCalls []string
}

func (f *fakeAuthResolver) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	f.mu.Lock()
	f.codexCalls = append(f.codexCalls, name)
	f.mu.Unlock()
	if f.engineErr != nil {
		return nil, f.engineErr
	}
	if f.engine == nil {
		return nil, errors.New("fake auth resolver: no codex engine scripted")
	}
	return f.engine, nil
}

func (f *fakeAuthResolver) OpenStore(service string) (credentials.Store, error) {
	f.mu.Lock()
	f.storeCalls = append(f.storeCalls, service)
	f.mu.Unlock()
	if f.storeErr != nil {
		return nil, f.storeErr
	}
	if f.store == nil {
		return nil, errors.New("fake auth resolver: no store scripted")
	}
	return f.store, nil
}

func (f *fakeAuthResolver) codex() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.codexCalls...)
}

func (f *fakeAuthResolver) opens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.storeCalls...)
}

// denyingAuthResolver fails the test if any auth entry point is reached. It
// guards the non-OAuth branches of the factory.
type denyingAuthResolver struct{ t *testing.T }

func (d denyingAuthResolver) OpenStore(service string) (credentials.Store, error) {
	d.t.Fatalf("OpenStore(%q) reached for a non-OAuth provider", service)
	return nil, nil
}

func (d denyingAuthResolver) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	d.t.Fatalf("CodexOAuthEngine(%q) reached for a non-OAuth provider", name)
	return nil, nil
}

func mustAuthResolver(t *testing.T, open func(string) (credentials.Store, error)) AuthResolver {
	t.Helper()
	r, err := NewAuthResolver(open)
	if err != nil {
		t.Fatalf("NewAuthResolver: %v", err)
	}
	return r
}

func mustCodexEngine(t *testing.T, r AuthResolver, name string) *oauth.Engine {
	t.Helper()
	e, err := r.CodexOAuthEngine(name)
	if err != nil {
		t.Fatalf("CodexOAuthEngine(%q): %v", name, err)
	}
	return e
}

func TestNewCodexOAuthEngineWithStorePreservesPinnedFlow(t *testing.T) {
	store := credentials.NewMemStore()
	engine, err := NewCodexOAuthEngineWithStore("codex", store)
	if err != nil {
		t.Fatalf("NewCodexOAuthEngineWithStore: %v", err)
	}
	if engine.Store != credentials.Store(store) {
		t.Fatal("engine.Store is not the store passed in")
	}
	if engine.StorageKey != "marshal:provider:codex" {
		t.Fatalf("StorageKey = %q, want %q", engine.StorageKey, "marshal:provider:codex")
	}
	if engine.ServerURL != codexIssuer || engine.ClientName != "marshal" {
		t.Fatalf("ServerURL = %q ClientName = %q, want %q/%q", engine.ServerURL, engine.ClientName, codexIssuer, "marshal")
	}
	// Startup only ever reads stored tokens; the interactive flow is driven
	// from the UI layer.
	if engine.Open != nil {
		t.Fatal("Open must stay nil on a factory-built codex engine")
	}
	if !slices.Equal(engine.Scopes, codexScopes) {
		t.Fatalf("Scopes = %v, want %v", engine.Scopes, codexScopes)
	}
	flow := engine.Flow
	if flow.Issuer != codexIssuer || flow.AuthorizeURL != codexAuthorizeURL || flow.TokenURL != codexTokenURL {
		t.Fatalf("flow endpoints = %q/%q/%q, want %q/%q/%q",
			flow.Issuer, flow.AuthorizeURL, flow.TokenURL, codexIssuer, codexAuthorizeURL, codexTokenURL)
	}
	if flow.ClientID != codexClientID {
		t.Fatalf("flow ClientID = %q, want %q", flow.ClientID, codexClientID)
	}
	if !slices.Equal(flow.RedirectPorts, []int{codexRedirectPort, codexRedirectFallbackPort}) {
		t.Fatalf("flow RedirectPorts = %v, want [%d %d]", flow.RedirectPorts, codexRedirectPort, codexRedirectFallbackPort)
	}
	if flow.RedirectPath != "/auth/callback" {
		t.Fatalf("flow RedirectPath = %q, want %q", flow.RedirectPath, "/auth/callback")
	}
	if !maps.Equal(flow.ExtraAuthorizeParams, codexExtraAuthorizeParams) {
		t.Fatalf("flow ExtraAuthorizeParams = %v, want %v", flow.ExtraAuthorizeParams, codexExtraAuthorizeParams)
	}
}

func TestNewCodexOAuthEngineWithStoreReadsTokensFromThatStore(t *testing.T) {
	store := credentials.NewMemStore()
	seedCodexTokens(t, store, "codex", "access-from-injected-store")

	engine, err := NewCodexOAuthEngineWithStore("codex", store)
	if err != nil {
		t.Fatalf("NewCodexOAuthEngineWithStore: %v", err)
	}
	// A pinned flow plus a non-expired token means no network and no browser.
	token, err := engine.TokenSource(t.Context())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if token != "access-from-injected-store" {
		t.Fatalf("token = %q, want the token seeded in the injected store", token)
	}
}

func TestNewCodexOAuthEngineWithStoreRejectsNilStore(t *testing.T) {
	engine, err := NewCodexOAuthEngineWithStore("codex", nil)
	if err == nil {
		t.Fatal("expected a nil store to be rejected")
	}
	if engine != nil {
		t.Fatalf("engine = %v, want nil", engine)
	}
	if !strings.Contains(err.Error(), "token store is required") {
		t.Fatalf("error = %q, want it to mention the missing token store", err.Error())
	}
}

func TestNewCodexOAuthEngineWithStoreLeavesProductionCacheUntouched(t *testing.T) {
	const name = "marshal-test-injected-engine-never-cached"
	if _, err := NewCodexOAuthEngineWithStore(name, credentials.NewMemStore()); err != nil {
		t.Fatalf("NewCodexOAuthEngineWithStore: %v", err)
	}

	codexEnginesMu.Lock()
	_, cached := codexEngines[name]
	codexEnginesMu.Unlock()
	if cached {
		t.Fatal("NewCodexOAuthEngineWithStore wrote into the production engine cache")
	}
}

func TestNewAuthResolverRejectsNilStoreFactory(t *testing.T) {
	resolver, err := NewAuthResolver(nil)
	if err == nil {
		t.Fatal("expected NewAuthResolver(nil) to fail")
	}
	if resolver != nil {
		t.Fatalf("resolver = %v, want nil", resolver)
	}
	if !strings.Contains(err.Error(), "store factory is required") {
		t.Fatalf("error = %q, want it to mention the missing store factory", err.Error())
	}
}

func TestAuthResolverOpenStoreCachesPerService(t *testing.T) {
	var mu sync.Mutex
	var opened []string
	byService := map[string]credentials.Store{}
	resolver := mustAuthResolver(t, func(service string) (credentials.Store, error) {
		mu.Lock()
		defer mu.Unlock()
		opened = append(opened, service)
		store := credentials.NewMemStore()
		byService[service] = store
		return store, nil
	})

	first, err := resolver.OpenStore("marshal")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	second, err := resolver.OpenStore("marshal")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	other, err := resolver.OpenStore("other")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	if first != second {
		t.Fatal("repeated OpenStore for one service returned different stores")
	}
	if first != credentials.Store(byService["marshal"]) {
		t.Fatal("OpenStore did not hand back the store its factory produced")
	}
	if other == first {
		t.Fatal("two services shared one store")
	}
	if len(opened) != 2 || opened[0] != "marshal" || opened[1] != "other" {
		t.Fatalf("factory opened %v, want exactly [marshal other]", opened)
	}
}

func TestAuthResolverEngineIdentityIsStableWithinOneResolver(t *testing.T) {
	var mu sync.Mutex
	opens := 0
	resolver := mustAuthResolver(t, func(string) (credentials.Store, error) {
		mu.Lock()
		defer mu.Unlock()
		opens++
		return credentials.NewMemStore(), nil
	})

	first := mustCodexEngine(t, resolver, "codex")
	second := mustCodexEngine(t, resolver, "codex")
	if first != second {
		t.Fatal("repeated CodexOAuthEngine lookups returned different engines")
	}

	// The engine path and the plain store path agree on one store per
	// resolver, which is what keeps a provider and its refresh worker on a
	// single mutex.
	store, err := resolver.OpenStore("marshal")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if store != first.Store {
		t.Fatal("OpenStore(\"marshal\") and the codex engine use different stores")
	}

	// A second provider name gets its own engine over the shared store.
	other := mustCodexEngine(t, resolver, "codex-alt")
	if other == first {
		t.Fatal("two provider names shared one engine")
	}
	if other.StorageKey != "marshal:provider:codex-alt" {
		t.Fatalf("StorageKey = %q, want %q", other.StorageKey, "marshal:provider:codex-alt")
	}
	if other.Store != store {
		t.Fatal("both codex engines must share the resolver's store")
	}

	mu.Lock()
	got := opens
	mu.Unlock()
	if got != 1 {
		t.Fatalf("store factory called %d times, want 1", got)
	}
}

func TestAuthResolverKeepsEnginesAndTokensPrivatePerResolver(t *testing.T) {
	storeA := newRecordingStore()
	storeB := newRecordingStore()
	seedCodexTokens(t, storeA, "codex", "access-a")
	seedCodexTokens(t, storeB, "codex", "access-b")

	resolverA := mustAuthResolver(t, func(string) (credentials.Store, error) { return storeA, nil })
	resolverB := mustAuthResolver(t, func(string) (credentials.Store, error) { return storeB, nil })

	engineA := mustCodexEngine(t, resolverA, "codex")
	engineB := mustCodexEngine(t, resolverB, "codex")
	if engineA == engineB {
		t.Fatal("two resolvers shared one engine instance")
	}
	if engineA.Store != credentials.Store(storeA) || engineB.Store != credentials.Store(storeB) {
		t.Fatal("an engine is not backed by its own resolver's store")
	}

	tokenA, err := engineA.TokenSource(t.Context())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if tokenA != "access-a" {
		t.Fatalf("token = %q, want %q", tokenA, "access-a")
	}
	if got := storeB.readKeys(); len(got) != 0 {
		t.Fatalf("resolver A's token read reached resolver B's store: %v", got)
	}
	if got := storeA.readKeys(); len(got) != 1 || got[0] != "marshal:provider:codex" {
		t.Fatalf("resolver A read %v, want exactly [marshal:provider:codex]", got)
	}

	tokenB, err := engineB.TokenSource(t.Context())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if tokenB != "access-b" {
		t.Fatalf("token = %q, want %q", tokenB, "access-b")
	}
	if got := storeB.readKeys(); len(got) != 1 || got[0] != "marshal:provider:codex" {
		t.Fatalf("resolver B read %v, want exactly [marshal:provider:codex]", got)
	}
	if got := storeA.readKeys(); len(got) != 1 {
		t.Fatalf("resolver A's store saw %d reads, want 1", len(got))
	}
}

func TestAuthResolverConcurrentOpenStoreOpensOnce(t *testing.T) {
	var opens atomic.Int64
	release := make(chan struct{})
	resolver := mustAuthResolver(t, func(string) (credentials.Store, error) {
		opens.Add(1)
		<-release // hold the resolver's lock so callers pile up behind it
		return credentials.NewMemStore(), nil
	})

	const n = 12
	stores := make([]credentials.Store, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			stores[i], errs[i] = resolver.OpenStore("marshal")
		}(i)
	}
	close(start)
	time.Sleep(20 * time.Millisecond) // let the goroutines contend
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: OpenStore: %v", i, err)
		}
		if stores[i] != stores[0] {
			t.Fatalf("goroutine %d got a different store", i)
		}
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("store factory opened %d stores, want 1", got)
	}
}

func TestAuthResolverConcurrentCodexLookupsShareOneEngine(t *testing.T) {
	var opens atomic.Int64
	release := make(chan struct{})
	resolver := mustAuthResolver(t, func(string) (credentials.Store, error) {
		opens.Add(1)
		<-release
		return credentials.NewMemStore(), nil
	})

	const n = 12
	engines := make([]*oauth.Engine, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			engines[i], errs[i] = resolver.CodexOAuthEngine("codex")
		}(i)
	}
	close(start)
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: CodexOAuthEngine: %v", i, err)
		}
		if engines[i] != engines[0] {
			t.Fatalf("goroutine %d got a different engine", i)
		}
	}
	if engines[0] == nil {
		t.Fatal("no engine returned")
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("store factory opened %d stores, want 1", got)
	}
}

func TestAuthResolverStoreFactoryErrorsAreNotCached(t *testing.T) {
	errBoom := errors.New("keyring: no backend")
	newFactory := func() (*atomic.Int64, func(string) (credentials.Store, error)) {
		var calls atomic.Int64
		return &calls, func(string) (credentials.Store, error) {
			if calls.Add(1) == 1 {
				return nil, errBoom
			}
			return credentials.NewMemStore(), nil
		}
	}

	// OpenStore: the first failure is returned, and the retry runs the
	// factory again instead of treating the failure as a cached success.
	calls, open := newFactory()
	openResolver := mustAuthResolver(t, open)
	if _, err := openResolver.OpenStore("marshal"); !errors.Is(err, errBoom) {
		t.Fatalf("OpenStore error = %v, want it to wrap %v", err, errBoom)
	}
	store, err := openResolver.OpenStore("marshal")
	if err != nil {
		t.Fatalf("second OpenStore: %v", err)
	}
	if store == nil {
		t.Fatal("second OpenStore returned a nil store with a nil error")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("store factory called %d times, want 2", got)
	}

	// CodexOAuthEngine: same contract on the engine path.
	calls, open = newFactory()
	engineResolver := mustAuthResolver(t, open)
	if _, err := engineResolver.CodexOAuthEngine("codex"); !errors.Is(err, errBoom) {
		t.Fatalf("CodexOAuthEngine error = %v, want it to wrap %v", err, errBoom)
	}
	engine := mustCodexEngine(t, engineResolver, "codex")
	if engine == nil {
		t.Fatal("retry returned a nil engine")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("store factory called %d times, want 2", got)
	}
}

func TestAuthResolverNilStoreFactoryResultIsKeyringUnavailable(t *testing.T) {
	var opens atomic.Int64
	resolver := mustAuthResolver(t, func(string) (credentials.Store, error) {
		opens.Add(1)
		return nil, nil
	})

	store, err := resolver.OpenStore("marshal")
	if err == nil {
		t.Fatal("a nil store from the factory must be an error")
	}
	if store != nil {
		t.Fatalf("store = %v, want nil", store)
	}
	if !errors.Is(err, credentials.ErrKeyringUnavailable) {
		t.Fatalf("error = %v, want it to wrap ErrKeyringUnavailable", err)
	}
	if _, err := resolver.CodexOAuthEngine("codex"); !errors.Is(err, credentials.ErrKeyringUnavailable) {
		t.Fatalf("CodexOAuthEngine error = %v, want it to wrap ErrKeyringUnavailable", err)
	}
	if got := opens.Load(); got != 2 {
		t.Fatalf("store factory called %d times, want 2 (the failure must not be cached)", got)
	}
}

func TestNewFromConfigWithAuthRejectsNilResolver(t *testing.T) {
	_, err := NewFromConfigWithAuth("codex", config.ProviderConfig{
		Type:    "openai_codex",
		BaseURL: "https://chatgpt.com/backend-api",
	}, "", false, 0, "", nil)
	if err == nil {
		t.Fatal("expected a nil auth resolver to be rejected")
	}
	if !strings.Contains(err.Error(), "auth resolver is required") {
		t.Fatalf("error = %q, want it to mention the missing auth resolver", err.Error())
	}
}

func TestNewFromConfigWithAuthUsesInjectedCodexEngine(t *testing.T) {
	store := newRecordingStore()
	seedCodexTokens(t, store, "codex", "injected-access")
	engine, err := NewCodexOAuthEngineWithStore("codex", store)
	if err != nil {
		t.Fatalf("NewCodexOAuthEngineWithStore: %v", err)
	}
	fake := &fakeAuthResolver{engine: engine}

	p, err := NewFromConfigWithAuth("codex", config.ProviderConfig{
		Type:    "openai_codex",
		BaseURL: "https://chatgpt.com/backend-api",
	}, "", false, 0, "", fake)
	if err != nil {
		t.Fatalf("NewFromConfigWithAuth: %v", err)
	}
	oc, ok := p.(*OpenAICodex)
	if !ok {
		t.Fatalf("got %T, want *OpenAICodex", p)
	}
	if oc.engine != engine {
		t.Fatal("the provider did not take the engine the resolver returned")
	}
	if got := fake.codex(); len(got) != 1 || got[0] != "codex" {
		t.Fatalf("resolver CodexOAuthEngine calls = %v, want [codex]", got)
	}
	if got := fake.opens(); len(got) != 0 {
		t.Fatalf("resolver OpenStore calls = %v, want none for a codex provider", got)
	}
	token, err := oc.engine.TokenSource(t.Context())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if token != "injected-access" {
		t.Fatalf("token = %q, want the token from the injected store", token)
	}
}

func TestNewFromConfigWithAuthPropagatesResolverError(t *testing.T) {
	pc := config.ProviderConfig{Type: "openai_codex", BaseURL: "https://chatgpt.com/backend-api"}

	errBoom := errors.New("resolver exploded")
	fake := &fakeAuthResolver{engineErr: errBoom}
	_, err := NewFromConfigWithAuth("codex", pc, "", false, 0, "", fake)
	if err == nil {
		t.Fatal("expected the resolver error to fail provider construction")
	}
	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want it to wrap %v", err, errBoom)
	}
	if !strings.Contains(err.Error(), `provider "codex"`) {
		t.Fatalf("error = %q, want it to name the provider", err.Error())
	}

	// An unscripted resolver must not fall through to the production engine:
	// construction has to fail rather than quietly claim the shared cache.
	if _, err := NewFromConfigWithAuth("codex", pc, "", false, 0, "", &fakeAuthResolver{}); err == nil {
		t.Fatal("expected construction to fail when the resolver yields no engine")
	}
}

func TestNewFromConfigWithAuthLeavesNonOAuthTypesAuthFree(t *testing.T) {
	deny := denyingAuthResolver{t: t}

	p, err := NewFromConfigWithAuth("plain", config.ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "https://example.invalid",
		APIKey:  "literal-key",
	}, "", false, 0, "sess-1", deny)
	if err != nil {
		t.Fatalf("openai_compatible: %v", err)
	}
	oc, ok := p.(*OpenAICompatible)
	if !ok {
		t.Fatalf("openai_compatible: got %T", p)
	}
	if oc.SessionID() != "sess-1" {
		t.Fatalf("SessionID = %q, want %q", oc.SessionID(), "sess-1")
	}

	p, err = NewFromConfigWithAuth("local", config.ProviderConfig{
		Type:    "ollama",
		BaseURL: "http://localhost:11434",
	}, "", false, 0, "", deny)
	if err != nil {
		t.Fatalf("ollama: %v", err)
	}
	if _, ok := p.(*OllamaNative); !ok {
		t.Fatalf("ollama: got %T", p)
	}

	p, err = NewFromConfigWithAuth("claude", config.ProviderConfig{
		Type:           "anthropic",
		BaseURL:        "https://api.anthropic.com",
		APIKey:         "literal-key",
		ThinkingBudget: 4096,
	}, "", false, 0, "", deny)
	if err != nil {
		t.Fatalf("anthropic: %v", err)
	}
	if _, ok := p.(*Anthropic); !ok {
		t.Fatalf("anthropic: got %T", p)
	}
}

func TestNewFromConfigWrappersKeepNonOAuthBehavior(t *testing.T) {
	pc := config.ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "https://example.invalid",
		APIKey:  "literal-key",
	}

	// NewFromConfig keeps its signature and produces a provider with no
	// session id.
	p, err := NewFromConfig("plain", pc, "", false, 0)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if got := p.(*OpenAICompatible).SessionID(); got != "" {
		t.Fatalf("SessionID = %q, want empty", got)
	}

	// NewFromConfigWithSession still threads the session id through to the
	// OpenAI-compatible backend.
	p, err = NewFromConfigWithSession("plain", pc, "", false, 0, "sess-42")
	if err != nil {
		t.Fatalf("NewFromConfigWithSession: %v", err)
	}
	if got := p.(*OpenAICompatible).SessionID(); got != "sess-42" {
		t.Fatalf("SessionID = %q, want %q", got, "sess-42")
	}
}
