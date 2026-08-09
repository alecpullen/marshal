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

func TestRoutingConfigPreservesEmbeddingRoleWhenSynthesizing(t *testing.T) {
	// Regression test: the user binds an embedding preset on the active
	// "single" profile via config or /settings, but RoutingConfig replaced
	// the whole profile with a freshly synthesized one whenever
	// ActivePreset was set, wiping the embedding binding. Since
	// synthesizeSingleModelProfile excludes the embedding role (a chat
	// preset cannot embed), ResolveEmbedding then failed and app.go
	// warned "no embedding role is configured" despite the user's binding.
	cfg := Default()
	cfg.Profile.Default = "single"
	cfg.Profile.ActivePreset = "ollama/qwen2.5-coder:7b"
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen2.5-coder:7b": {Name: "ollama/qwen2.5-coder:7b", Provider: "ollama", Model: "qwen2.5-coder:7b", LocalOnly: true},
		"ollama/nomic-embed-text": {Name: "ollama/nomic-embed-text", Provider: "ollama", Model: "nomic-embed-text", LocalOnly: true},
	}
	cfg.Providers = map[string]ProviderConfig{
		"ollama": {BaseURL: "http://localhost:11434/v1"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"single": {Name: "single", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleEmbedding: {Preset: "ollama/nomic-embed-text"},
		}},
	}

	rc := cfg.RoutingConfig()

	router := routing.NewStaticRouter(rc)
	route, err := router.ResolveEmbedding()
	if err != nil {
		t.Fatalf("ResolveEmbedding: %v (embedding binding on the active profile was clobbered by synthesis)", err)
	}
	if route.Preset.Name != "ollama/nomic-embed-text" {
		t.Fatalf("ResolveEmbedding resolved to %q, want ollama/nomic-embed-text", route.Preset.Name)
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

func TestRoutingConfigDoesNotMutateAgentProfiles(t *testing.T) {
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

	// Before calling RoutingConfig, "single" must not exist.
	if _, ok := cfg.AgentProfiles["single"]; ok {
		t.Fatal("precondition: AgentProfiles should not contain 'single'")
	}

	_ = cfg.RoutingConfig()

	// After calling RoutingConfig, the caller's AgentProfiles must still
	// not contain "single" — synthesis must not mutate the receiver.
	if _, ok := cfg.AgentProfiles["single"]; ok {
		t.Fatal("RoutingConfig must not mutate the caller's AgentProfiles map")
	}
}

func TestRoutingConfigInvariantResolveEdit(t *testing.T) {
	// Every config the TUI persists must satisfy Resolve("edit") without
	// error. This invariant test simulates a connect → switch cycle to
	// verify the routing path is never broken by stale state.
	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{
		"ollama": {BaseURL: "http://localhost:11434/v1"},
		"openai": {BaseURL: "https://api.openai.com/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen2.5-coder:7b": {Name: "ollama/qwen2.5-coder:7b", Provider: "ollama", Model: "qwen2.5-coder:7b", LocalOnly: true},
		"openai/gpt-4o":           {Name: "openai/gpt-4o", Provider: "openai", Model: "gpt-4o", LocalOnly: false},
	}
	cfg.Privacy.RemoteProvidersAllowed = true

	// Simulate connect to preset1.
	cfg.Profile.Default = "single"
	cfg.Profile.ActivePreset = "ollama/qwen2.5-coder:7b"
	rc1 := cfg.RoutingConfig()
	router1 := routing.NewStaticRouter(rc1)
	if _, err := router1.Resolve("edit"); err != nil {
		t.Fatalf("after connect to preset1, Resolve(edit): %v", err)
	}

	// Simulate switch to preset2. The caller changes ActivePreset but
	// does NOT touch AgentProfiles (synthesis handles it). Without
	// maps.Clone, the first RoutingConfig call would have polluted
	// AgentProfiles["single"] with preset1, and the second call would
	// skip synthesis, leaving the stale binding.
	cfg.Profile.ActivePreset = "openai/gpt-4o"
	rc2 := cfg.RoutingConfig()
	router2 := routing.NewStaticRouter(rc2)
	route, err := router2.Resolve("edit")
	if err != nil {
		t.Fatalf("after switch to preset2, Resolve(edit): %v", err)
	}
	if route.Preset.Name != "openai/gpt-4o" {
		t.Fatalf("after switch to preset2, route.Preset.Name = %q, want openai/gpt-4o", route.Preset.Name)
	}
}

func TestRoutingConfigStaleActivePresetNotRevived(t *testing.T) {
	// When ActivePreset points at a preset that no longer exists and
	// whose provider is not configured, synthesis must not revive it.
	cfg := Default()
	cfg.Profile.Default = "single"
	cfg.Profile.ActivePreset = "ollama/deleted-model"
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"openai/gpt-4o": {Name: "openai/gpt-4o", Provider: "openai", Model: "gpt-4o"},
	}
	cfg.Providers = map[string]ProviderConfig{
		"openai": {BaseURL: "https://api.openai.com/v1"},
	}
	// "ollama" provider is NOT configured, so the stale ActivePreset
	// should not be synthesized.

	rc := cfg.RoutingConfig()

	if _, ok := rc.Profiles["single"]; ok {
		t.Fatal("synthesis must not revive a stale ActivePreset whose provider is unconfigured")
	}
}
