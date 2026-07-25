package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"marshal/internal/app/config"
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
