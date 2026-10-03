package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/schema"
)

// codexCatalogJSON is a trimmed but structurally faithful copy of the live
// catalog captured in docs/codex-spike-findings-2026-09-14.md §4: seven
// models, two of them visibility == "hide", one retiring.
const codexCatalogJSON = `{
  "models": [
    {"slug":"gpt-6-astra","display_name":"GPT-6-Astra","context_window":272000,"max_context_window":872000,"visibility":"list","supported_in_api":true,"priority":1,"default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"low","description":"Fast"},{"effort":"high","description":"Deep"}]},
    {"slug":"gpt-reserve","display_name":"GPT-Reserve","context_window":272000,"max_context_window":872000,"visibility":"hide","supported_in_api":true,"priority":2},
    {"slug":"gpt-5.6-sol","display_name":"GPT-5.6-Sol","context_window":272000,"max_context_window":872000,"visibility":"list","supported_in_api":true,"priority":3},
    {"slug":"gpt-5.6-terra","display_name":"GPT-5.6-Terra","context_window":272000,"max_context_window":872000,"visibility":"list","supported_in_api":true,"priority":4},
    {"slug":"gpt-5.6-luna","display_name":"GPT-5.6-Luna","context_window":272000,"max_context_window":872000,"visibility":"list","supported_in_api":true,"priority":5},
    {"slug":"gpt-5.5","display_name":"GPT-5.5","context_window":272000,"visibility":"list","supported_in_api":true,"priority":6},
    {"slug":"codex-auto-review","display_name":"Codex Auto Review","context_window":272000,"max_context_window":872000,"visibility":"hide","supported_in_api":true,"priority":7}
  ]
}`

func TestFilterCodexModels(t *testing.T) {
	var parsed codexModelsResponse
	if err := json.Unmarshal([]byte(codexCatalogJSON), &parsed); err != nil {
		t.Fatalf("unmarshal catalog: %v", err)
	}
	got := filterCodexModels(parsed.Models)

	want := []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5"}
	if len(got) != len(want) {
		t.Fatalf("got %d models (%v), want %d", len(got), modelIDs(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("models[%d] = %q, want %q", i, got[i].ID, id)
		}
	}
	// Hidden models must be excluded.
	for _, m := range got {
		if m.ID == "gpt-reserve" || m.ID == "codex-auto-review" {
			t.Errorf("hidden model %q was not filtered out", m.ID)
		}
	}
	// The absent slug that caused every 400 in the spike must not appear.
	for _, m := range got {
		if m.ID == "gpt-5.2-codex" {
			t.Error("gpt-5.2-codex is not in the catalog and must not be offered")
		}
	}
	// Context window comes from the catalog.
	if got[0].ContextWindow != 272000 {
		t.Errorf("context window = %d, want 272000", got[0].ContextWindow)
	}
}

func TestFilterCodexModelsExcludesUnsupportedInAPI(t *testing.T) {
	entries := []codexModelEntry{
		{Slug: "listed", Visibility: "list", SupportedInAPI: true},
		{Slug: "not-in-api", Visibility: "list", SupportedInAPI: false},
		{Slug: "hidden", Visibility: "hide", SupportedInAPI: true},
		{Slug: "", Visibility: "list", SupportedInAPI: true},
	}
	got := filterCodexModels(entries)
	if len(got) != 1 || got[0].ID != "listed" {
		t.Fatalf("got %v, want only [listed]", modelIDs(got))
	}
}

func TestFilterCodexModelsSortsByPriority(t *testing.T) {
	entries := []codexModelEntry{
		{Slug: "third", Visibility: "list", SupportedInAPI: true, Priority: 30},
		{Slug: "first", Visibility: "list", SupportedInAPI: true, Priority: 10},
		{Slug: "second", Visibility: "list", SupportedInAPI: true, Priority: 20},
	}
	got := filterCodexModels(entries)
	want := []string{"first", "second", "third"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("models[%d] = %q, want %q", i, got[i].ID, id)
		}
	}
}

func TestFilterCodexModelsFallsBackToMaxContextWindow(t *testing.T) {
	entries := []codexModelEntry{
		{Slug: "m", Visibility: "list", SupportedInAPI: true, ContextWindow: 0, MaxContextWindow: 872000},
	}
	got := filterCodexModels(entries)
	if got[0].ContextWindow != 872000 {
		t.Fatalf("context window = %d, want the max fallback 872000", got[0].ContextWindow)
	}
}

// --- the fallback chain ---

func TestCodexModelsLiveFetch(t *testing.T) {
	// Do not derive the expected version from the implementation constant:
	// the endpoint accepts 0.0.0 but silently hides current models.
	const wantCatalogVersion = "0.160.0"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != codexModelsPath {
			t.Errorf("path = %q, want %q", r.URL.Path, codexModelsPath)
		}
		if got := r.URL.Query().Get("client_version"); got != wantCatalogVersion {
			t.Errorf("client_version = %q, want %q", got, wantCatalogVersion)
		}
		if got := r.Header.Get("Authorization"); got == "" {
			t.Error("missing Authorization header")
		}
		if got := r.Header.Get(codexOriginatorHeader); got != codexOriginator {
			t.Errorf("originator = %q, want %q", got, codexOriginator)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Models-Etag", `"abc123"`)
		_, _ = w.Write([]byte(codexCatalogJSON))
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p := newTestCodex(t, server.URL, engine)

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 5 {
		t.Fatalf("got %v, want the 5 listed models", modelIDs(models))
	}
	if models[0].ID != "gpt-6-astra" {
		t.Errorf("first model = %q, want gpt-6-astra", models[0].ID)
	}
}

func TestCodexModelsFallsBackToStaticWhenUnauthenticated(t *testing.T) {
	// No token stored: the live fetch cannot authenticate, so the static
	// list must be served rather than an error.
	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	// Wipe the stored token.
	if err := engine.Store.Delete("marshal:provider:codex"); err != nil {
		t.Fatalf("delete token: %v", err)
	}
	p := newTestCodex(t, "http://127.0.0.1:1", engine)

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models must not fail when unauthenticated: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.6-luna" {
		t.Fatalf("got %v, want the static fallback [gpt-5.6-luna]", modelIDs(models))
	}
}

func TestCodexModelsFallsBackToStaticOnServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p := newTestCodex(t, server.URL, engine)

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models must not fail on a server error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.6-luna" {
		t.Fatalf("got %v, want the static fallback", modelIDs(models))
	}
}

func TestCodexModelsFallsBackToStaticOnMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models": not-json`))
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p := newTestCodex(t, server.URL, engine)

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models must not fail on malformed JSON: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.6-luna" {
		t.Fatalf("got %v, want the static fallback", modelIDs(models))
	}
}

func TestCodexModelsFallsBackToStaticOnEmptyCatalog(t *testing.T) {
	// A catalog whose every entry is filtered out must not be treated as a
	// successful fetch (which would cache an empty list).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"hidden","visibility":"hide","supported_in_api":true}]}`))
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p := newTestCodex(t, server.URL, engine)

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.6-luna" {
		t.Fatalf("got %v, want the static fallback", modelIDs(models))
	}
}

// --- cache ---

func TestCodexModelsSavesToCache(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(codexCatalogJSON))
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	pc := config.ProviderConfig{Type: "openai_codex", BaseURL: server.URL}
	p, err := NewOpenAICodex(CodexOptions{
		Name:           "codex",
		BaseURL:        server.URL,
		Engine:         engine,
		StaticModels:   []string{"gpt-5.6-luna"},
		DataDir:        dir,
		ProviderConfig: pc,
	})
	if err != nil {
		t.Fatalf("NewOpenAICodex: %v", err)
	}

	if _, err := p.Models(t.Context()); err != nil {
		t.Fatalf("Models: %v", err)
	}

	c := modelcache.Load(dir)
	entry, ok := c.Providers["codex"]
	if !ok {
		t.Fatal("live fetch did not write a cache entry")
	}
	if len(entry.Models) != 5 {
		t.Fatalf("cached %d models, want 5", len(entry.Models))
	}
	if entry.ConfigHash != modelcache.HashProvider(pc) {
		t.Error("cache entry config hash does not match the provider config")
	}
}

func TestCodexModelsUsesCacheWhenLiveFails(t *testing.T) {
	dir := t.TempDir()
	pc := config.ProviderConfig{Type: "openai_codex", BaseURL: "https://chatgpt.com/backend-api"}

	// Seed a fresh cache entry with a distinctive model.
	c := modelcache.Load(dir)
	c.Providers["codex"] = modelcache.Entry{
		ConfigHash: modelcache.HashProvider(pc),
		Models:     []schema.ModelInfo{{ID: "cached-model"}},
		FetchedAt:  time.Now(),
	}
	if err := modelcache.Save(dir, c); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	// The live fetch fails (unreachable server), so the cache must win over
	// the static list.
	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p, err := NewOpenAICodex(CodexOptions{
		Name:           "codex",
		BaseURL:        "http://127.0.0.1:1",
		Engine:         engine,
		StaticModels:   []string{"gpt-5.6-luna"},
		DataDir:        dir,
		ProviderConfig: pc,
	})
	if err != nil {
		t.Fatalf("NewOpenAICodex: %v", err)
	}

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "cached-model" {
		t.Fatalf("got %v, want the cached list", modelIDs(models))
	}
}

func TestCodexModelsIgnoresStaleCache(t *testing.T) {
	dir := t.TempDir()
	pc := config.ProviderConfig{Type: "openai_codex", BaseURL: "https://chatgpt.com/backend-api"}

	c := modelcache.Load(dir)
	c.Providers["codex"] = modelcache.Entry{
		ConfigHash: modelcache.HashProvider(pc),
		Models:     []schema.ModelInfo{{ID: "stale-model"}},
		FetchedAt:  time.Now().Add(-48 * time.Hour),
	}
	if err := modelcache.Save(dir, c); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p, err := NewOpenAICodex(CodexOptions{
		Name:           "codex",
		BaseURL:        "http://127.0.0.1:1",
		Engine:         engine,
		StaticModels:   []string{"gpt-5.6-luna"},
		DataDir:        dir,
		ProviderConfig: pc,
	})
	if err != nil {
		t.Fatalf("NewOpenAICodex: %v", err)
	}

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.6-luna" {
		t.Fatalf("got %v, want the static fallback (stale cache must be ignored)", modelIDs(models))
	}
}

func TestCodexModelsIgnoresCacheWithDifferentConfig(t *testing.T) {
	dir := t.TempDir()
	pc := config.ProviderConfig{Type: "openai_codex", BaseURL: "https://chatgpt.com/backend-api"}

	// Seed the cache under a *different* config hash.
	c := modelcache.Load(dir)
	c.Providers["codex"] = modelcache.Entry{
		ConfigHash: modelcache.HashProvider(config.ProviderConfig{Type: "openai_codex", BaseURL: "https://other.example.com"}),
		Models:     []schema.ModelInfo{{ID: "other-model"}},
		FetchedAt:  time.Now(),
	}
	if err := modelcache.Save(dir, c); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p, err := NewOpenAICodex(CodexOptions{
		Name:           "codex",
		BaseURL:        "http://127.0.0.1:1",
		Engine:         engine,
		StaticModels:   []string{"gpt-5.6-luna"},
		DataDir:        dir,
		ProviderConfig: pc,
	})
	if err != nil {
		t.Fatalf("NewOpenAICodex: %v", err)
	}

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.6-luna" {
		t.Fatalf("got %v, want the static fallback (config hash mismatch)", modelIDs(models))
	}
}

func TestCodexModelsNoDataDirSkipsCache(t *testing.T) {
	// With no data dir the chain is live → static, and nothing is written.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	engine, _ := codexTestEngine(t, codexTestToken("acct-abc"))
	p := newTestCodex(t, server.URL, engine)

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.6-luna" {
		t.Fatalf("got %v, want the static fallback", modelIDs(models))
	}
}

func modelIDs(models []schema.ModelInfo) []string {
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.ID
	}
	return out
}
