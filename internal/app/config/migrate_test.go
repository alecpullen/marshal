package config

import (
	"testing"

	"marshal/internal/llm/routing"
)

func TestMigrateLegacyAgentModel(t *testing.T) {
	cfg := Default()
	cfg.Profile.Default = "" // clear the built-in default so migration fires
	cfg.Agent.Provider = "openai"
	cfg.Agent.Model = "gpt-4o"
	cfg.Providers = map[string]ProviderConfig{
		"openai": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1"},
	}

	if !MigrateLegacyAgentModel(&cfg) {
		t.Fatal("migration reported no change")
	}
	if cfg.Agent.Provider != "" || cfg.Agent.Model != "" {
		t.Errorf("legacy fields not cleared: %q/%q", cfg.Agent.Provider, cfg.Agent.Model)
	}
	if cfg.Profile.Default == "" {
		t.Fatal("no default profile after migration")
	}
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		t.Fatalf("default profile %q does not exist", cfg.Profile.Default)
	}
	if len(profile.Roles) != len(routing.AllRoles) {
		t.Errorf("bound %d roles, want %d", len(profile.Roles), len(routing.AllRoles))
	}
	presetName := profile.Roles[routing.RoleImplementer].Preset
	preset, ok := cfg.Models.Presets[presetName]
	if !ok {
		t.Fatalf("preset %q missing", presetName)
	}
	if preset.Provider != "openai" || preset.Model != "gpt-4o" {
		t.Errorf("preset = %s/%s, want openai/gpt-4o", preset.Provider, preset.Model)
	}
}

func TestMigrateNoOpWhenProfileAlreadySet(t *testing.T) {
	cfg := Default()
	cfg.Agent.Provider = "openai"
	cfg.Agent.Model = "gpt-4o"
	cfg.Profile.Default = "mine"
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"mine": {Name: "mine", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "p"},
		}},
	}

	if MigrateLegacyAgentModel(&cfg) {
		t.Error("migrated a config that already has a default profile")
	}
	if cfg.Profile.Default != "mine" {
		t.Errorf("clobbered the existing default profile: %q", cfg.Profile.Default)
	}
}

func TestMigrateNoOpWhenNoLegacyFields(t *testing.T) {
	cfg := Default()
	if MigrateLegacyAgentModel(&cfg) {
		t.Error("migrated a config with nothing to migrate")
	}
}

func TestMigratePartialLegacyIsIgnored(t *testing.T) {
	cfg := Default()
	cfg.Agent.Provider = "openai" // model missing
	if MigrateLegacyAgentModel(&cfg) {
		t.Error("migrated a half-configured legacy pair")
	}
}

// TestMigrateProfileNameContract asserts that the migration produces a profile
// named "single", which must match the constant singleModelProfileName in
// internal/app/tui/model.go. The two packages do not import each other, so
// this test serves as an explicit contract assertion: if either side renames
// the profile name, this test will fail and the developer must update both.
func TestMigrateProfileNameContract(t *testing.T) {
	cfg := Default()
	cfg.Profile.Default = "" // clear the built-in default so migration fires
	cfg.Agent.Provider = "openai"
	cfg.Agent.Model = "gpt-4o"
	cfg.Providers = map[string]ProviderConfig{
		"openai": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1"},
	}

	if !MigrateLegacyAgentModel(&cfg) {
		t.Fatal("migration reported no change")
	}

	// The migration always creates a profile named "single". This literal
	// must match internal/app/tui/model.go's singleModelProfileName = "single".
	const wantProfileName = "single"
	if cfg.Profile.Default != wantProfileName {
		t.Errorf("Profile.Default = %q, want %q", cfg.Profile.Default, wantProfileName)
	}
	if _, ok := cfg.AgentProfiles[wantProfileName]; !ok {
		t.Errorf("AgentProfiles[%q] does not exist", wantProfileName)
	}
}

func TestMigratePreservesExistingPresets(t *testing.T) {
	cfg := Default()
	cfg.Profile.Default = "" // clear the built-in default so migration fires
	cfg.Agent.Provider = "openai"
	cfg.Agent.Model = "gpt-4o"
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"mine": {Name: "mine", Provider: "groq", Model: "llama"},
	}

	MigrateLegacyAgentModel(&cfg)

	if _, ok := cfg.Models.Presets["mine"]; !ok {
		t.Error("migration dropped an existing preset")
	}
}
