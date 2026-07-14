package connect

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/picker"
)

func TestNewStartsAtPickTemplate(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	if m.step != stepPickTemplate {
		t.Fatalf("step = %v, want stepPickTemplate", m.step)
	}
	if m.title == "" {
		t.Fatal("title must be set for the pickTemplate step")
	}
}

func TestNewScopedProviderStartsAtPickModel(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), SkipToIntroModel: true, ScopedProvider: "ollama"})
	if m.step != stepPickModel {
		t.Fatalf("step = %v, want stepPickModel", m.step)
	}
}

func TestEscAtPickTemplateEmitsCancelled(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 27})
	if cmd == nil {
		t.Fatal("expected a cmd emitting CancelledMsg")
	}
	msg := cmd()
	if _, ok := msg.(CancelledMsg); !ok {
		t.Fatalf("cmd produced %T, want CancelledMsg", msg)
	}
	_ = updated
}

// ── Step 1: pickTemplate + step 2 credentials tests ──

func TestPickTemplateOllamaSkipsAPIKey(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	updated, _ := m.Update(pickerPicked("ollama"))
	if updated.step != stepProbing {
		t.Fatalf("local template should skip apiKey, got step = %v", updated.step)
	}
	if updated.template.ID != "ollama" {
		t.Fatalf("template = %q, want ollama", updated.template.ID)
	}
}

func TestPickTemplateOpenRouterEntersAPIKey(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	updated, _ := m.Update(pickerPicked("openrouter"))
	if updated.step != stepAPIKey {
		t.Fatalf("remote template should enter apiKey, got step = %v", updated.step)
	}
	if updated.template.KeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("KeyEnv = %q", updated.template.KeyEnv)
	}
}

func TestPickCustomEntersBaseURL(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	updated, _ := m.Update(pickerPicked("__custom__"))
	if updated.step != stepBaseURL {
		t.Fatalf("custom should enter baseURL, got step = %v", updated.step)
	}
}

func TestAPIKeyEnterAdvancesToProbing(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	m, _ = m.Update(pickerPicked("openrouter"))
	m.input.SetValue("sk-test-1234")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepProbing {
		t.Fatalf("apiKey Enter should advance to probing, got step = %v", updated.step)
	}
	if updated.providerCfg.APIKey != "sk-test-1234" {
		t.Fatalf("api key not captured: %q", updated.providerCfg.APIKey)
	}
	if updated.providerCfg.APIKeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("api_key_env not set from template: %q", updated.providerCfg.APIKeyEnv)
	}
}

func TestCustomBaseURLThenKey(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	m, _ = m.Update(pickerPicked("__custom__"))
	m.input.SetValue("https://myhost/v1")
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
	if m.step != stepAPIKey {
		t.Fatalf("after baseURL should be apiKey, got %v", m.step)
	}
	if m.providerCfg.BaseURL != "https://myhost/v1" {
		t.Fatalf("base_url not captured: %q", m.providerCfg.BaseURL)
	}
	m.input.SetValue("sk-x")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepProbing {
		t.Fatalf("after apiKey should be probing, got %v", updated.step)
	}
}

func pickerPicked(value string) tea.Msg { return picker.PickedMsg{Value: value} }
