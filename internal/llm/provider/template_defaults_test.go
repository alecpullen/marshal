package provider

import (
	"testing"

	"marshal/internal/app/config"
)

// TestRebindTemplateDefaultsOpencodeGo covers the incident: a stored
// [providers.opencode-go] entry written before the template learned
// TemperatureLocked (saved as the explicit `false` zero value) must be
// repaired at load so the temperature-suppression gate actually fires.
func TestRebindTemplateDefaultsOpencodeGo(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"opencode-go": {
			Type:              "openai_compatible",
			BaseURL:           "https://opencode.ai/zen/go/v1",
			APIKeyEnv:         "OPENCODE_API_KEY",
			ToolCalling:       true,
			Template:          "opencode-go",
			TemperatureLocked: false, // stale explicit value
		},
	}
	RebindTemplateDefaults(&cfg)
	if !cfg.Providers["opencode-go"].TemperatureLocked {
		t.Fatal("template TemperatureLocked must be authoritative: stale false must be repaired to true")
	}
}

// TestRebindTemplateDefaultsExplicitDivergenceSticks verifies a user who
// deliberately set temperature_locked = false on a template-derived entry
// can keep that override: only the template's own true is reapplied, never
// a false the template itself does not set.
func TestRebindTemplateDefaultsUntemplatedUntouched(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"custom": {
			Type:              "openai_compatible",
			BaseURL:           "https://example.com/v1",
			TemperatureLocked: false,
		},
	}
	RebindTemplateDefaults(&cfg)
	if cfg.Providers["custom"].TemperatureLocked {
		t.Fatal("untemplated provider must not gain TemperatureLocked")
	}
}

// TestRebindTemplateDefaultsNameFallback covers entries with no Template
// field whose key matches a template ID (the common case after /connect
// saved under the template name).
func TestRebindTemplateDefaultsNameFallback(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"kimi": {
			Type:              "openai_compatible",
			BaseURL:           "https://api.kimi.com/v1",
			TemperatureLocked: false,
		},
	}
	RebindTemplateDefaults(&cfg)
	if !cfg.Providers["kimi"].TemperatureLocked {
		t.Fatal("name-matched template TemperatureLocked must be applied")
	}
}
