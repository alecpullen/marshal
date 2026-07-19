package connect

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
)

func TestPanelSatisfiesDock(t *testing.T) {
	var _ dock.Panel = Panel{}
}

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
	updated, _ := m.Update(pickerPicked("custom"))
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
	m, _ = m.Update(pickerPicked("custom"))
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

func TestProbeSuccessAdvancesToPickModel(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]string{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(probe.ResultMsg{Provider: m.providerName, Models: []string{"qwen2.5-coder:7b", "llama3.1:8b"}})
	if updated.step != stepPickModel {
		t.Fatalf("success should advance to pickModel, got %v", updated.step)
	}
	if len(updated.models) != 2 {
		t.Fatalf("models not stored: %v", updated.models)
	}
	if got := updated.discovered[updated.providerName]; len(got) != 2 {
		t.Fatalf("discovered cache not populated: %v", got)
	}
}

func TestProbeFailureStaysWithRetrySkip(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]string{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(probe.ResultMsg{Provider: m.providerName, Err: errors.New("connection refused")})
	if updated.step != stepProbing {
		t.Fatalf("failure should stay probing, got %v", updated.step)
	}
	if updated.err == "" {
		t.Fatal("expected inline error text set")
	}
}

func TestRetryReRunsProbe(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]string{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Err: errors.New("boom")})
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 114})
	if updated.step != stepProbing {
		t.Fatalf("retry should stay probing, got %v", updated.step)
	}
	if cmd == nil {
		t.Fatal("retry should re-arm the probe cmd")
	}
}

func TestSkipProbeUsesCatalogAndAdvances(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]string{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(tea.KeyPressMsg{Code: 115})
	if updated.step != stepPickModel {
		t.Fatalf("skip should advance to pickModel, got %v", updated.step)
	}
	if len(updated.models) == 0 && len(updated.template.Models) > 0 {
		t.Fatal("skip should seed models from template catalog")
	}
}

func TestPickModelEmitsDone(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]string{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Models: []string{"qwen2.5-coder:7b"}})
	_, cmd := m.Update(pickerPicked("qwen2.5-coder:7b"))
	if cmd == nil {
		t.Fatal("pickModel should emit a DoneMsg cmd")
	}
	msg := cmd()
	dm, ok := msg.(DoneMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want DoneMsg", msg)
	}
	if dm.Model != "qwen2.5-coder:7b" {
		t.Fatalf("DoneMsg.Model = %q", dm.Model)
	}
}

func TestPickTemplateKeyForwardedToPicker(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	if m.picker == nil {
		t.Fatal("expected picker at stepPickTemplate")
	}
	// Send Enter; without the fix this returns (m, nil) because handleKey had
	// no case for stepPickTemplate. With the fix the key is forwarded to the
	// picker, which returns a cmd that produces picker.PickedMsg.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 13})
	if cmd == nil {
		t.Fatal("Enter on pickTemplate should forward to picker and return a cmd")
	}
	msg := cmd()
	if _, ok := msg.(picker.PickedMsg); !ok {
		t.Fatalf("expected picker.PickedMsg, got %T", msg)
	}
}

func TestPickModelKeyForwardedToPicker(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), SkipToIntroModel: true, ScopedProvider: "ollama"})
	if m.picker == nil {
		t.Fatal("expected picker at stepPickModel")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 13})
	if cmd == nil {
		t.Fatal("Enter on pickModel should forward to picker and return a cmd")
	}
	msg := cmd()
	if _, ok := msg.(picker.PickedMsg); !ok {
		t.Fatalf("expected picker.PickedMsg, got %T", msg)
	}
}

func pickerPicked(value string) tea.Msg { return picker.PickedMsg{Value: value} }
