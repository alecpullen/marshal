package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestProvidersAddAndEditType(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(providersFrame(s))
	ps.SetSize(80, 24)
	// providers root IS the collection frame; add an entry
	ps.Update(kp("a"))
	for _, r := range "local" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	pc, ok := s.cfg.Providers["local"]
	if !ok {
		t.Fatalf("add should create provider, got %v", s.cfg.Providers)
	}
	if pc.Type != "openai_compatible" {
		t.Fatalf("new provider should default to openai_compatible, got %q", pc.Type)
	}
	// drill into it and edit Type
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if ps.depth() != 2 {
		t.Fatalf("enter should drill into the provider, depth=%d", ps.depth())
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // edit first row (Type)
	ps.top().list.input.SetValue("anthropic")
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Providers["local"].Type != "anthropic" {
		t.Fatalf("type edit should apply immediately, got %q", s.cfg.Providers["local"].Type)
	}
}

func TestProviderBaseURLEditInvalidatesDiscovery(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	st := newState(cfg)
	st.discovered["ollama"] = []string{"qwen2.5:7b", "llama3.1:8b"}

	f := providersFrame(st)
	drill := f.list.Rows()[0]
	detail := drill.build()
	for _, r := range detail.list.Rows() {
		if r.id == "providers.ollama.base_url" {
			if err := r.setStr("http://localhost:9999/v1"); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if _, ok := st.discovered["ollama"]; ok {
		t.Fatal("editing base_url should invalidate the discovery cache for ollama")
	}
}

func TestProviderDetailHasTestConnectionRow(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	st := newState(cfg)
	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var found *field
	for _, r := range detail.list.Rows() {
		if r.title == "Test connection" {
			found = r
			break
		}
	}
	if found == nil {
		t.Fatal("provider detail must have a Test connection row")
	}
	if found.kind != kindAction {
		t.Fatalf("Test connection row kind = %v, want kindAction", found.kind)
	}
}

func TestRemoteProviderTestConnectionBlockedByPrivacy(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Type: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1"},
	}
	st := newState(cfg)
	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var tc *field
	for _, r := range detail.list.Rows() {
		if r.title == "Test connection" {
			tc = r
			break
		}
	}
	label := tc.actLabel()
	if !strings.Contains(label, "blocked") {
		t.Fatalf("remote provider with privacy off: label = %q, want 'blocked'", label)
	}
	if cmd := tc.act(); cmd != nil {
		t.Fatal("blocked test connection act() should return nil")
	}
}

func TestLocalProviderTestConnectionNotBlocked(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	st := newState(cfg)
	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var tc *field
	for _, r := range detail.list.Rows() {
		if r.title == "Test connection" {
			tc = r
			break
		}
	}
	if strings.Contains(tc.actLabel(), "blocked") {
		t.Fatal("local provider should not be blocked")
	}
}

func TestHooksAddWithoutPromptAndDelete(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(hooksFrame(s))
	ps.SetSize(80, 24)
	ps.Update(kp("a")) // no key prompt: adds immediately
	if len(s.cfg.Hooks.Entries) != 1 || s.cfg.Hooks.Entries[0].Event != "pre_tool" {
		t.Fatalf("a should append a pre_tool hook, got %v", s.cfg.Hooks.Entries)
	}
	ps.Update(kp("d"))
	if len(s.cfg.Hooks.Entries) != 0 {
		t.Fatalf("d should delete the hook, got %v", s.cfg.Hooks.Entries)
	}
}
