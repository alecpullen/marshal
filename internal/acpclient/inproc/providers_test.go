package inproc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/credentials"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/schema"
)

func TestProviderTemplatesListTheCatalog(t *testing.T) {
	h, _ := New(Deps{State: newState(t, "s1")})
	got := h.ProviderTemplates()
	want := provider.All()
	if len(got) != len(want) {
		t.Fatalf("templates = %d, want %d", len(got), len(want))
	}
	for i, tpl := range want {
		if !reflect.DeepEqual(got[i], templateWire(tpl)) {
			t.Fatalf("template %d = %+v, want every field of %+v", i, got[i], tpl)
		}
	}

	var codex acpwire.ProviderTemplate
	for _, tpl := range got {
		if tpl.ID == "openai-codex" {
			codex = tpl
		}
	}
	if codex.ID == "" {
		t.Fatal("the catalog does not hold openai-codex")
	}
	data, err := json.Marshal(codex)
	if err != nil {
		t.Fatalf("encode openai-codex: %v", err)
	}
	const wantJSON = `{"id":"openai-codex","label":"OpenAI (ChatGPT subscription)","type":"openai_codex","baseUrl":"https://chatgpt.com/backend-api","toolCalling":true,"structuredOutput":true,"auth":"oauth","keyHint":"Uses your ChatGPT subscription via browser login — no API key required.","models":["gpt-6-astra","gpt-5.6-sol","gpt-5.6-terra","gpt-5.6-luna"]}`
	if string(data) != wantJSON {
		t.Fatalf("openai-codex encodes as\n%s\nwant\n%s", data, wantJSON)
	}
}

func TestProbeProviderListsModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"qwen2.5-coder:7b","owned_by":"ollama"},{"id":"llama3.1:8b","owned_by":"meta"}]}`))
	}))
	defer srv.Close()

	h, _ := New(Deps{State: newState(t, "s1"), Auth: newTestAuth()})
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	models, err := h.ProbeProvider(context.Background(), "testprov", pc)
	if err != nil {
		t.Fatalf("ProbeProvider: %v", err)
	}
	if len(models) != 2 || models[0].ID != "qwen2.5-coder:7b" {
		t.Fatalf("models = %v, want the server's list", models)
	}
}

func TestProbeProviderReportsFailures(t *testing.T) {
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer forbidden.Close()

	h, _ := New(Deps{State: newState(t, "s1"), Auth: newTestAuth()})
	cases := map[string]string{
		"non-200":            forbidden.URL + "/v1",
		"connection refused": "http://127.0.0.1:1/v1",
	}
	for name, baseURL := range cases {
		t.Run(name, func(t *testing.T) {
			pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: baseURL}
			if _, err := h.ProbeProvider(context.Background(), "testprov", pc); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestProbeProviderPreservesModelLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"gpt-4o","owned_by":"openai"}]}`)
	}))
	defer srv.Close()

	h, _ := New(Deps{State: newState(t, "s1"), Auth: newTestAuth()})
	models, err := h.ProbeProvider(context.Background(), "openai", config.ProviderConfig{
		Type: "openai_compatible", BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("ProbeProvider: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ID != "gpt-4o" {
		t.Errorf("ID = %q, want gpt-4o", models[0].ID)
	}
	// /models reports no limits, so discovery leaves them for the preset
	// defaults.
	if models[0].ContextWindow != 0 || models[0].MaxOutputTokens != 0 {
		t.Errorf("limits = %d/%d, want both zero", models[0].ContextWindow, models[0].MaxOutputTokens)
	}
}

// TestProbeProviderUsesTheAdaptersResolver: an openai_codex probe must build its
// token store through the adapter's resolver, so a probe launched from an
// isolated runtime never opens the developer's keychain.
func TestProbeProviderUsesTheAdaptersResolver(t *testing.T) {
	auth := newTestAuth()
	auth.engineErr = fmt.Errorf("injected store failure: %w", credentials.ErrKeyringUnavailable)

	h, _ := New(Deps{State: newState(t, "s1"), Auth: auth})
	pc := config.ProviderConfig{
		Type:     "openai_codex",
		Template: "openai-codex",
		BaseURL:  "https://chatgpt.com/backend-api",
		Auth:     "oauth",
	}
	_, err := h.ProbeProvider(context.Background(), "codex", pc)
	if len(auth.engines) != 1 || auth.engines[0] != "codex" {
		t.Fatalf("engine requests = %v, want one for \"codex\"", auth.engines)
	}
	if err == nil {
		t.Fatal("a failing injected resolver must produce an error")
	}
	if !errors.Is(err, credentials.ErrKeyringUnavailable) {
		t.Fatalf("err = %v, want the injected failure wrapped", err)
	}
}

func TestAuthResolverDefaultsToProduction(t *testing.T) {
	h, _ := New(Deps{State: newState(t, "s1")})
	if got, want := h.AuthResolver(), provider.DefaultAuthResolver(); got != want {
		t.Fatalf("AuthResolver() = %v, want the production default", got)
	}

	injected := newTestAuth()
	h, _ = New(Deps{State: newState(t, "s1"), Auth: injected})
	if got := h.AuthResolver(); got != provider.AuthResolver(injected) {
		t.Fatalf("AuthResolver() = %v, want the injected resolver", got)
	}
}

func TestCanProbeModel(t *testing.T) {
	auth := newTestAuth()
	auth.engineErr = fmt.Errorf("OS keychain unavailable — OAuth tokens cannot be stored: %w", credentials.ErrKeyringUnavailable)
	h, _ := New(Deps{State: newState(t, "s1"), Auth: auth})

	if !h.CanProbeModel(context.Background(), "ollama", config.ProviderConfig{
		Type: "ollama", BaseURL: "http://127.0.0.1:11434",
	}) {
		t.Error("an ollama provider can probe a model")
	}
	if h.CanProbeModel(context.Background(), "openai", config.ProviderConfig{
		Type: "openai_compatible", BaseURL: "http://127.0.0.1:1234/v1",
	}) {
		t.Error("an openai_compatible provider cannot probe a model")
	}
	if h.CanProbeModel(context.Background(), "codex", config.ProviderConfig{
		Type: "openai_codex", BaseURL: "https://chatgpt.com/backend-api", Auth: "oauth",
	}) {
		t.Error("a provider whose client cannot be built cannot probe a model")
	}
}

func TestProbeModelProbesOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/show" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"capabilities":["completion","tools"],"model_info":{"qwen3.context_length":40960}}`)
	}))
	defer srv.Close()

	h, _ := New(Deps{State: newState(t, "s1"), Auth: newTestAuth()})
	caps, err := h.ProbeModel(context.Background(), "ollama", config.ProviderConfig{
		Type: "ollama", BaseURL: srv.URL,
	}, "qwen3:8b")
	if err != nil {
		t.Fatalf("ProbeModel: %v", err)
	}
	if caps.ToolCalling == nil || !*caps.ToolCalling {
		t.Errorf("ToolCalling = %v, want true", caps.ToolCalling)
	}
	if caps.Reasoning == nil || *caps.Reasoning {
		t.Errorf("Reasoning = %v, want false", caps.Reasoning)
	}
	if caps.ContextWindow != 40960 {
		t.Errorf("ContextWindow = %d, want 40960", caps.ContextWindow)
	}

	other, err := h.ProbeModel(context.Background(), "openai", config.ProviderConfig{
		Type: "openai_compatible", BaseURL: "http://127.0.0.1:1234/v1",
	}, "gpt-4o")
	if err != nil {
		t.Fatalf("ProbeModel for a non-prober: %v", err)
	}
	if other.ToolCalling != nil || other.ContextWindow != 0 || other.Reasoning != nil {
		t.Errorf("capabilities = %+v, want the zero value", other)
	}

	auth := newTestAuth()
	auth.engineErr = fmt.Errorf("OS keychain unavailable — OAuth tokens cannot be stored: %w", credentials.ErrKeyringUnavailable)
	broken, _ := New(Deps{State: newState(t, "s1"), Auth: auth})
	codex, err := broken.ProbeModel(context.Background(), "codex", config.ProviderConfig{
		Type: "openai_codex", BaseURL: "https://chatgpt.com/backend-api", Auth: "oauth",
	}, "gpt-6-astra")
	if err == nil {
		t.Fatal("a client that cannot be built must return its error")
	}
	if codex.ToolCalling != nil || codex.ContextWindow != 0 || codex.Reasoning != nil {
		t.Errorf("capabilities = %+v, want the zero value", codex)
	}
}

// cacheState builds a state holding one provider called "local".
func cacheState(t *testing.T) *session.State {
	t.Helper()
	st := newState(t, "s1")
	st.Config.Providers = map[string]config.ProviderConfig{
		"local": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	return st
}

func TestModelCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := cacheState(t)
	h, _ := New(Deps{State: st, Auth: newTestAuth(), ModelCacheDir: dir})
	pc := st.Config.Providers["local"]

	models := []schema.ModelInfo{{ID: "qwen3:8b"}, {ID: "llama3.1:8b"}}
	if err := h.SaveModels(context.Background(), "local", models); err != nil {
		t.Fatalf("SaveModels: %v", err)
	}
	got, ok := h.ListModels(context.Background(), "local")
	if !ok || len(got) != 2 || got[0].ID != "qwen3:8b" {
		t.Fatalf("ListModels = %v, %v, want the saved list", got, ok)
	}
	c := modelcache.Load(dir)
	entry, ok := c.Providers["local"]
	if !ok {
		t.Fatal("the cache holds no entry for local")
	}
	if want := modelcache.HashProvider(pc); entry.ConfigHash != want {
		t.Errorf("ConfigHash = %q, want %q", entry.ConfigHash, want)
	}

	// A new base URL is a different config, so the entry no longer matches.
	moved := pc
	moved.BaseURL = "http://localhost:11434/other/v1"
	st.Config.Providers["local"] = moved
	if _, ok := h.ListModels(context.Background(), "local"); ok {
		t.Fatal("a changed config must not match the cached entry")
	}
	st.Config.Providers["local"] = pc

	if err := h.RenameModels(context.Background(), "local", "renamed"); err != nil {
		t.Fatalf("RenameModels: %v", err)
	}
	c = modelcache.Load(dir)
	if _, ok := c.Providers["local"]; ok {
		t.Error("the rename left the old name in the cache")
	}
	if _, ok := c.Providers["renamed"]; !ok {
		t.Error("the rename did not move the entry")
	}
	if err := h.RenameModels(context.Background(), "missing", "other"); err != nil {
		t.Fatalf("renaming a name the cache does not hold: %v", err)
	}

	if err := h.ForgetModels(context.Background(), "renamed"); err != nil {
		t.Fatalf("ForgetModels: %v", err)
	}
	if _, ok := modelcache.Load(dir).Providers["renamed"]; ok {
		t.Error("ForgetModels left the entry in the cache")
	}
}

func TestModelCacheIgnoresWhatPersistIgnored(t *testing.T) {
	dir := t.TempDir()
	st := cacheState(t)
	h, _ := New(Deps{State: st, Auth: newTestAuth(), ModelCacheDir: dir})

	if err := h.SaveModels(context.Background(), "local", nil); err != nil {
		t.Fatalf("SaveModels with an empty list: %v", err)
	}
	if err := h.SaveModels(context.Background(), "absent", []schema.ModelInfo{{ID: "m"}}); err != nil {
		t.Fatalf("SaveModels for a name the config lacks: %v", err)
	}
	if _, err := os.Stat(modelcache.Path(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a write persisted for nothing: %v", err)
	}

	noCache, _ := New(Deps{State: st, Auth: newTestAuth()})
	if _, ok := noCache.ListModels(context.Background(), "local"); ok {
		t.Error("ListModels without a cache directory found something")
	}
	if err := noCache.SaveModels(context.Background(), "local", []schema.ModelInfo{{ID: "m"}}); err != nil {
		t.Errorf("SaveModels without a cache directory: %v", err)
	}
	if err := noCache.ForgetModels(context.Background(), "local"); err != nil {
		t.Errorf("ForgetModels without a cache directory: %v", err)
	}
	if err := noCache.RenameModels(context.Background(), "local", "renamed"); err != nil {
		t.Errorf("RenameModels without a cache directory: %v", err)
	}
}
