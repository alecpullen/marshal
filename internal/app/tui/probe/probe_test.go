package probe

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/credentials"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/limits"
	"marshal/internal/oauth"
)

func TestIsLocalhost(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://localhost:11434/v1", true},
		{"http://127.0.0.1:11434/v1", true},
		{"http://0.0.0.0:11434/v1", true},
		{"http://[::1]:11434/v1", true},
		{"http://[::1%25lo0]:11434/v1", true},
		{"https://api.openai.com/v1", false},
		{"https://openrouter.ai/api/v1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsLocalhost(c.url); got != c.want {
			t.Errorf("IsLocalhost(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestProviderSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"qwen2.5-coder:7b","owned_by":"ollama"},{"id":"llama3.1:8b","owned_by":"meta"}]}`))
	}))
	defer srv.Close()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	msg := Provider("test.field", "testprov", pc, "", false, 0)().(ResultMsg)

	if msg.Err != nil {
		t.Fatalf("Provider err = %v", msg.Err)
	}
	if msg.Provider != "testprov" || msg.FieldID != "test.field" {
		t.Fatalf("ResultMsg identity = %+v", msg)
	}
	if len(msg.Models) != 2 || msg.Models[0].ID != "qwen2.5-coder:7b" {
		t.Fatalf("ResultMsg models = %v", msg.Models)
	}
}

func TestProviderNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	msg := Provider("test.field", "testprov", pc, "", false, 0)().(ResultMsg)
	if msg.Err == nil {
		t.Fatal("expected error for 403 response")
	}
}

func TestProviderConnectionRefused(t *testing.T) {
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "http://127.0.0.1:1/v1"}
	msg := Provider("test.field", "testprov", pc, "", false, 0)().(ResultMsg)
	if msg.Err == nil {
		t.Fatal("expected error for connection refused")
	}
}

func TestProviderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	old := Timeout
	Timeout = 200 * time.Millisecond
	defer func() { Timeout = old }()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	msg := Provider("test.field", "testprov", pc, "", false, 0)().(ResultMsg)
	if msg.Err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestProviderPreservesModelLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"gpt-4o","owned_by":"openai"}]}`)
	}))
	defer srv.Close()

	msg := Provider("f", "openai", config.ProviderConfig{
		Type: "openai_compatible", BaseURL: srv.URL,
	}, "", false, 0)().(ResultMsg)

	if msg.Err != nil {
		t.Fatalf("probe: %v", msg.Err)
	}
	if len(msg.Models) != 1 {
		t.Fatalf("got %d models, want 1", len(msg.Models))
	}
	if msg.Models[0].ID != "gpt-4o" {
		t.Errorf("ID = %q, want gpt-4o", msg.Models[0].ID)
	}
	// The field must exist and be addressable — its value depends on
	// whether a limits table was wired in, which this test does not do.
	_ = msg.Models[0].ContextWindow
	_ = msg.Models[0].MaxOutputTokens
}

func TestProviderFillsLimitsFromOnDiskCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"gpt-4o","owned_by":"openai"}]}`)
	}))
	defer srv.Close()

	// A warm limits cache must be read even when remote limit discovery is
	// off — reading it makes no network requests.
	dataDir := t.TempDir()
	if err := limits.Save(dataDir, limits.Cache{
		Table:     map[string]limits.Limit{"gpt-4o": {ContextWindow: 128000, MaxOutputTokens: 16384}},
		FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed limits cache: %v", err)
	}

	msg := Provider("f", "openai", config.ProviderConfig{
		Type: "openai_compatible", BaseURL: srv.URL,
	}, dataDir, false, 0)().(ResultMsg)

	if msg.Err != nil {
		t.Fatalf("probe: %v", msg.Err)
	}
	if len(msg.Models) != 1 {
		t.Fatalf("got %d models, want 1", len(msg.Models))
	}
	if msg.Models[0].ContextWindow != 128000 || msg.Models[0].MaxOutputTokens != 16384 {
		t.Errorf("limits = %d/%d, want 128000/16384 from the on-disk cache",
			msg.Models[0].ContextWindow, msg.Models[0].MaxOutputTokens)
	}
}

// ── Explicit-auth probe entry point ──────────────────────────────────

// memAuth is a test AuthResolver: an in-memory token store plus a record of
// which services and provider names were asked for.
type memAuth struct {
	store *credentials.MemStore
	// storeErr, when set, fails OpenStore/CodexOAuthEngine. It stands in for
	// an unavailable OS keychain.
	storeErr error
	services []string
	engines  []string
}

func newMemAuth() *memAuth { return &memAuth{store: credentials.NewMemStore()} }

func (a *memAuth) OpenStore(service string) (credentials.Store, error) {
	a.services = append(a.services, service)
	if a.storeErr != nil {
		return nil, a.storeErr
	}
	return a.store, nil
}

func (a *memAuth) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	a.engines = append(a.engines, name)
	if a.storeErr != nil {
		return nil, a.storeErr
	}
	return provider.NewCodexOAuthEngineWithStore(name, a.store)
}

// TestProviderWithAuthUsesInjectedResolver: an openai_codex probe must build
// its token store through the supplied resolver, so a probe launched from an
// isolated runtime never opens the developer's keychain. The probe fails with
// the injected error and must not fall back to the production adapter.
func TestProviderWithAuthUsesInjectedResolver(t *testing.T) {
	auth := newMemAuth()
	auth.storeErr = fmt.Errorf("injected store failure: %w", credentials.ErrKeyringUnavailable)

	pc := config.ProviderConfig{
		Type:     "openai_codex",
		Template: "openai-codex",
		BaseURL:  "https://chatgpt.com/backend-api",
		Auth:     "oauth",
	}
	msg := ProviderWithAuth("f", "codex", pc, "", false, 0, auth)().(ResultMsg)

	if len(auth.engines) != 1 || auth.engines[0] != "codex" {
		t.Fatalf("engine requests = %v, want one for \"codex\"", auth.engines)
	}
	if msg.Err == nil {
		t.Fatal("a failing injected resolver must produce an error")
	}
	if !errors.Is(msg.Err, credentials.ErrKeyringUnavailable) {
		t.Fatalf("err = %v, want the injected failure wrapped", msg.Err)
	}
}

// TestProviderWithAuthNilResolverFailsClosed: a nil resolver must error rather
// than silently selecting the production keychain.
func TestProviderWithAuthNilResolverFailsClosed(t *testing.T) {
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "http://127.0.0.1:1/v1"}
	msg := ProviderWithAuth("f", "p", pc, "", false, 0, nil)().(ResultMsg)
	if msg.Err == nil {
		t.Fatal("a nil resolver must fail the probe, not fall back to the real keychain")
	}
	if !strings.Contains(msg.Err.Error(), "auth resolver is required") {
		t.Fatalf("err = %v, want an auth-resolver-required error", msg.Err)
	}
}

// TestProviderWrapperSelectsProductionAdapter: the legacy wrapper keeps
// working for callers with no injected dependency (success path through a
// test-owned server).
func TestProviderWrapperSelectsProductionAdapter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"m1","owned_by":"x"}]}`)
	}))
	defer srv.Close()

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL}
	msg := Provider("f", "p", pc, "", false, 0)().(ResultMsg)
	if msg.Err != nil {
		t.Fatalf("provider: %v", msg.Err)
	}
	if len(msg.Models) != 1 || msg.Models[0].ID != "m1" {
		t.Fatalf("models = %v, want the server's list", msg.Models)
	}
}
