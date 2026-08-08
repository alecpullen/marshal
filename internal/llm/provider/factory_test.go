package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider/limits"
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
	p, err := NewFromConfig("test", pc, "", false)
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
	p, err := NewFromConfig("test", pc, "", false)
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
	p, err := NewFromConfig("test", pc, "", false)
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
	_, err := NewFromConfig("test", pc, "", false)
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
	p, err := NewFromConfig("test", pc, "", false)
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
	p, err := NewFromConfig("test", pc, "", false)
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
	_, err := NewFromConfig("test", pc, "", false)
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
	}, "", false)
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
	}, "", false)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if got := p.(*OllamaNative).keepAlive; got != "-1" {
		t.Fatalf("keepAlive = %q, want -1", got)
	}
}

func TestNewFromConfigAnthropic(t *testing.T) {
	t.Setenv("MARSHAL_TEST_FACTORY_ANTHROPIC_KEY", "sk-test")
	p, err := NewFromConfig("claude", config.ProviderConfig{
		Type:           "anthropic",
		BaseURL:        "https://api.anthropic.com",
		APIKeyEnv:      "MARSHAL_TEST_FACTORY_ANTHROPIC_KEY",
		ThinkingBudget: 4096,
	}, "", false)
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
	if !a.capabilities.ToolCalling {
		t.Fatal("anthropic capabilities must advertise ToolCalling")
	}
}

func TestNewFromConfigAnthropicRequiresKey(t *testing.T) {
	_, err := NewFromConfig("claude", config.ProviderConfig{
		Type:    "anthropic",
		BaseURL: "https://api.anthropic.com",
	}, "", false)
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

	p, err := NewFromConfig("test", config.ProviderConfig{Type: "openai_compatible", BaseURL: server.URL}, dataDir, false)
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
	}, dataDir, false)
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
