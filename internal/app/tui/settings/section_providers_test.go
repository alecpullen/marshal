package settings

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

func providersTestConfig() config.Config {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "sk-real-1234", APIKeyEnv: "OLLAMA_KEY", ToolCalling: true},
	}
	return cfg
}

func TestProvidersPaneMasksApiKey(t *testing.T) {
	m := New(providersTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "providers")
	view := stripANSI(m.View())
	if strings.Contains(view, "sk-real-1234") {
		t.Error("raw API key must never render in the providers list")
	}
}

func TestProvidersPaneAddAndEdit(t *testing.T) {
	m := New(providersTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "providers")
	m = keyPress(m, "a", "a", "n", "t", "h", "r", "o", "p", "i", "c", "enter")
	if _, ok := m.state.cfg.Providers["anthropic"]; !ok {
		t.Fatal("add should create the anthropic provider entry")
	}
}

func TestProvidersPaneSubFormMasksKey(t *testing.T) {
	m := New(providersTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "providers")
	m = keyPress(m, "enter") // edit the existing ollama entry
	view := stripANSI(m.View())
	if strings.Contains(view, "sk-real-1234") {
		t.Error("raw API key must never render inside the sub-form")
	}
	if !strings.Contains(view, "••••1234") {
		t.Error("masked key should render in the sub-form description")
	}
}
