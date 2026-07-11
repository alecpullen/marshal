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
	// Slow huh-driven test (~5s) because provider sub-form rebuilds
	// incur heavy huh state churn.
	if testing.Short() {
		t.Skip("slow huh-driven test")
	}

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

// TestProvidersPaneClearKeyEmptiesSecret asserts that toggling the "Clear"
// confirm inside the provider sub-form writes an empty string back to the
// working config. The huh validator on the Clear field sets the local
// copy's key to ""; the form's onSubmit callback then writes the local
// copy back into the working config.
func TestProvidersPaneClearKeyEmptiesSecret(t *testing.T) {
	if testing.Short() {
		t.Skip("slow huh-driven test")
	}
	m := New(providersTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "providers")
	m = keyPress(m, "enter") // edit the existing ollama entry
	// Fields in order: Type, Base URL, API key env, API key, Clear, Tool calling.
	m = keyPress(m, "tab", "tab", "tab", "tab") // reach Clear
	m = keyPress(m, "space")                    // toggle Clear on
	// Advance to the last field (Tool calling) and submit. The form's
	// onSubmit writes the local copy (APIKey="") back into the working
	// config.
	m = keyPress(m, "enter", "enter")
	if got := m.state.cfg.Providers["ollama"].APIKey; got != "" {
		t.Fatalf("Clear should empty the API key after submit, got %q", got)
	}
}
