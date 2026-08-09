package config

import (
	"testing"

	"marshal/internal/llm/routing"
)

func TestRoutingConfigSynthesizesProfileFromActivePreset(t *testing.T) {
	cfg := Default()
	cfg.Profile.Default = "single"
	cfg.Profile.ActivePreset = "ollama/qwen2.5-coder:7b"
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen2.5-coder:7b": {Name: "ollama/qwen2.5-coder:7b", Provider: "ollama", Model: "qwen2.5-coder:7b", LocalOnly: true},
		"openai/gpt-4o":           {Name: "openai/gpt-4o", Provider: "openai", Model: "gpt-4o"},
	}
	cfg.Providers = map[string]ProviderConfig{
		"ollama": {BaseURL: "http://localhost:11434/v1"},
		"openai": {BaseURL: "https://api.openai.com/v1"},
	}

	rc := cfg.RoutingConfig()

	// The synthesized profile must exist and bind every role to ActivePreset.
	profile, ok := rc.Profiles["single"]
	if !ok {
		t.Fatal("RoutingConfig must synthesize the 'single' profile when ActivePreset is set")
	}
	for _, role := range routing.AllRoles {
		if profile.Roles[role].Preset != "ollama/qwen2.5-coder:7b" {
			t.Fatalf("role %s bound to %q, want ollama/qwen2.5-coder:7b", role, profile.Roles[role].Preset)
		}
	}
}

func TestRoutingConfigSynthesizesProfileFromSolePreset(t *testing.T) {
	cfg := Default()
	cfg.Profile.Default = "single"
	cfg.Profile.ActivePreset = "" // no ActivePreset; should fall back to sole preset
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen2.5-coder:7b": {Name: "ollama/qwen2.5-coder:7b", Provider: "ollama", Model: "qwen2.5-coder:7b", LocalOnly: true},
	}
	cfg.Providers = map[string]ProviderConfig{
		"ollama": {BaseURL: "http://localhost:11434/v1"},
	}

	rc := cfg.RoutingConfig()

	profile, ok := rc.Profiles["single"]
	if !ok {
		t.Fatal("RoutingConfig must synthesize the 'single' profile from the sole preset")
	}
	if profile.Roles[routing.RoleImplementer].Preset != "ollama/qwen2.5-coder:7b" {
		t.Fatalf("implementer bound to %q, want ollama/qwen2.5-coder:7b", profile.Roles[routing.RoleImplementer].Preset)
	}
}

func TestRoutingConfigPreservesExistingProfile(t *testing.T) {
	cfg := Default()
	cfg.Profile.Default = "mine"
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"mine": {Name: "mine", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "ollama/qwen2.5-coder:7b"},
		}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen2.5-coder:7b": {Name: "ollama/qwen2.5-coder:7b", Provider: "ollama", Model: "qwen2.5-coder:7b", LocalOnly: true},
	}

	rc := cfg.RoutingConfig()

	// The existing profile should be preserved, not overwritten.
	profile, ok := rc.Profiles["mine"]
	if !ok {
		t.Fatal("existing profile should be preserved")
	}
	if len(profile.Roles) != 1 {
		t.Fatalf("existing profile should have 1 role, got %d", len(profile.Roles))
	}
}

func TestRoutingConfigNoSynthesisWithMultiplePresetsAndNoActivePreset(t *testing.T) {
	cfg := Default()
	cfg.Profile.Default = "single"
	cfg.Profile.ActivePreset = "" // ambiguous with 2 presets
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen2.5-coder:7b": {Name: "ollama/qwen2.5-coder:7b", Provider: "ollama", Model: "qwen2.5-coder:7b", LocalOnly: true},
		"openai/gpt-4o":           {Name: "openai/gpt-4o", Provider: "openai", Model: "gpt-4o"},
	}

	rc := cfg.RoutingConfig()

	// No synthesis should happen — no ActivePreset and >1 preset.
	if _, ok := rc.Profiles["single"]; ok {
		t.Fatal("should not synthesize profile when multiple presets exist and no ActivePreset")
	}
}
