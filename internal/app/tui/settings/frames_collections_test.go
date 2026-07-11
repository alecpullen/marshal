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
	// Use the wizard to add a provider instead of the old bare 'a' prompt
	req := providersWizard(s)()
	if err := req.onPick("ollama"); err != nil {
		t.Fatalf("wizard onPick(ollama) = %v", err)
	}
	ps.top().list.Refresh()
	pc, ok := s.cfg.Providers["ollama"]
	if !ok {
		t.Fatalf("wizard should create provider, got %v", s.cfg.Providers)
	}
	if pc.Type != "openai_compatible" {
		t.Fatalf("new provider from ollama template should default to openai_compatible, got %q", pc.Type)
	}
	// drill into it and edit Type
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if ps.depth() != 2 {
		t.Fatalf("enter should drill into the provider, depth=%d", ps.depth())
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // edit first row (Type)
	ps.top().list.input.SetValue("anthropic")
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Providers["ollama"].Type != "anthropic" {
		t.Fatalf("type edit should apply immediately, got %q", s.cfg.Providers["ollama"].Type)
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

func TestWizardCreatesProviderFromTemplate(t *testing.T) {
	cfg := config.Default()
	st := newState(cfg)

	req := providersWizard(st)()
	if req == nil {
		t.Fatal("providersWizard should return a pickerRequest")
	}

	var foundOllama bool
	for _, item := range req.items {
		if item.Value == "ollama" {
			foundOllama = true
		}
	}
	if !foundOllama {
		t.Fatal("wizard items should include the ollama template")
	}

	if err := req.onPick("ollama"); err != nil {
		t.Fatalf("wizard onPick(ollama) = %v", err)
	}
	pc, ok := st.cfg.Providers["ollama"]
	if !ok {
		t.Fatal("wizard should have created providers.ollama")
	}
	if pc.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("created provider BaseURL = %q, want http://localhost:11434/v1", pc.BaseURL)
	}
}

func TestWizardCollisionAppendsSuffix(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama":   {Type: "openai_compatible"},
		"ollama-2": {Type: "openai_compatible"},
	}
	st := newState(cfg)

	req := providersWizard(st)()
	if err := req.onPick("ollama"); err != nil {
		t.Fatalf("wizard onPick = %v", err)
	}
	if _, ok := st.cfg.Providers["ollama-3"]; !ok {
		t.Fatal("wizard should have created providers.ollama-3 on collision")
	}
}

func TestProvidersFrameHasAddWizard(t *testing.T) {
	st := newState(config.Default())
	f := providersFrame(st)
	if f.addWizard == nil {
		t.Fatal("providers root frame must have addWizard set")
	}
	req := f.addWizard()
	if req == nil || req.title != "Add provider" {
		t.Fatalf("addWizard request = %+v, want title 'Add provider'", req)
	}
}

func TestProviderNameFieldRenamesKey(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "secret"},
	}
	st := newState(cfg)

	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var nameRow *field
	for _, r := range detail.list.Rows() {
		if r.title == "Name" {
			nameRow = r
			break
		}
	}
	if nameRow == nil {
		t.Fatal("provider detail must have a Name row")
	}
	if nameRow.getStr() != "ollama" {
		t.Fatalf("Name = %q, want ollama", nameRow.getStr())
	}
	if err := nameRow.setStr("my-ollama"); err != nil {
		t.Fatalf("rename err = %v", err)
	}
	if _, ok := st.cfg.Providers["ollama"]; ok {
		t.Fatal("old key should be deleted after rename")
	}
	pc, ok := st.cfg.Providers["my-ollama"]
	if !ok {
		t.Fatal("new key should exist after rename")
	}
	if pc.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("renamed provider BaseURL = %q, want preserved", pc.BaseURL)
	}
}

func TestProviderNameFieldRejectsCollision(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama":     {Type: "openai_compatible"},
		"openrouter": {Type: "openai_compatible"},
	}
	st := newState(cfg)

	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var nameRow *field
	for _, r := range detail.list.Rows() {
		if r.title == "Name" {
			nameRow = r
			break
		}
	}
	if err := nameRow.setStr("openrouter"); err == nil {
		t.Fatal("rename to existing key should error")
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
