package config

import (
	"testing"

	"marshal/internal/llm/routing"
)

func TestMigrateLegacyAgentModel(t *testing.T) {
	cfg := Default()
	cfg.Profile.Default = "" // clear the built-in default so migration fires
	cfg.Providers = map[string]ProviderConfig{
		"openai": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1"},
	}

	if !MigrateLegacyAgentModel(&cfg, "openai", "gpt-4o") {
		t.Fatal("migration reported no change")
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
	cfg.Profile.Default = "mine"
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"mine": {Name: "mine", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "p"},
		}},
	}

	if MigrateLegacyAgentModel(&cfg, "openai", "gpt-4o") {
		t.Error("migrated a config that already has a default profile")
	}
	if cfg.Profile.Default != "mine" {
		t.Errorf("clobbered the existing default profile: %q", cfg.Profile.Default)
	}
}

// A legacy config that omits [profile] entirely still needs migrating.
// Default() supplies Profile.Default = "local_balanced", so an emptiness
// check never fires — but no profile by that name exists, so without the
// migration the router resolves nothing at all. This is the shape shown in
// docs/09-configuration-examples.md, and it worked before the legacy
// resolution path was deleted.
func TestMigrateWhenDefaultProfileNameDoesNotExist(t *testing.T) {
	cfg := Default()
	cfg.AgentProfiles = nil // nothing defines cfg.Profile.Default

	if cfg.Profile.Default == "" {
		t.Fatal("precondition: Default() is expected to name a default profile")
	}

	if !MigrateLegacyAgentModel(&cfg, "ollama", "qwen3-coder") {
		t.Fatalf("did not migrate a config whose default profile %q does not exist",
			cfg.Profile.Default)
	}
	if cfg.Profile.Default != "single" {
		t.Errorf("Profile.Default = %q, want %q", cfg.Profile.Default, "single")
	}
	profile, ok := cfg.AgentProfiles["single"]
	if !ok {
		t.Fatal("no single-model profile after migration")
	}
	if len(profile.Roles) != len(routing.AllRoles) {
		t.Errorf("bound %d roles, want %d", len(profile.Roles), len(routing.AllRoles))
	}
}

// The inverse: a config that genuinely defines the profile Default() names
// is already valid and must be left alone.
func TestMigrateNoOpWhenDefaultProfileExists(t *testing.T) {
	cfg := Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		cfg.Profile.Default: {Name: cfg.Profile.Default, Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "p"},
		}},
	}
	want := cfg.Profile.Default

	if MigrateLegacyAgentModel(&cfg, "ollama", "qwen3-coder") {
		t.Error("migrated a config whose default profile is defined")
	}
	if cfg.Profile.Default != want {
		t.Errorf("Profile.Default = %q, want %q", cfg.Profile.Default, want)
	}
}

// The deleted legacyRoute admitted a local provider under
// remote_providers_allowed = false (its gate was
// !RemoteAllowed && !isLocalProvider(LegacyProvider)). Presets are gated on
// preset.LocalOnly instead, so the migration must carry that fact across or
// every local-only user is blocked after migrating.
func TestMigrateMarksLocalProviderPresetLocalOnly(t *testing.T) {
	cfg := Default()
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.AgentProfiles = nil
	cfg.Providers = map[string]ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}

	if !MigrateLegacyAgentModel(&cfg, "ollama", "qwen3-coder") {
		t.Fatal("did not migrate")
	}
	preset := cfg.Models.Presets["ollama/qwen3-coder"]
	if !preset.LocalOnly {
		t.Error("preset for a localhost provider must be LocalOnly, or the remote gate blocks it")
	}
}

func TestMigrateDoesNotMarkRemoteProviderLocalOnly(t *testing.T) {
	cfg := Default()
	cfg.AgentProfiles = nil
	cfg.Providers = map[string]ProviderConfig{
		"openai": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1"},
	}

	if !MigrateLegacyAgentModel(&cfg, "openai", "gpt-4o") {
		t.Fatal("did not migrate")
	}
	if cfg.Models.Presets["openai/gpt-4o"].LocalOnly {
		t.Error("a remote provider must not be marked LocalOnly — that would bypass the privacy gate")
	}
}

func TestMigrateUnknownProviderIsNotLocalOnly(t *testing.T) {
	cfg := Default()
	cfg.AgentProfiles = nil
	cfg.Providers = nil // no matching [providers.ghost] block

	if !MigrateLegacyAgentModel(&cfg, "ghost", "m") {
		t.Fatal("did not migrate")
	}
	if cfg.Models.Presets["ghost/m"].LocalOnly {
		t.Error("a provider with no known base URL must not be assumed local")
	}
}

func TestMigrateNoOpWhenNoLegacyFields(t *testing.T) {
	cfg := Default()
	if MigrateLegacyAgentModel(&cfg, "", "") {
		t.Error("migrated a config with nothing to migrate")
	}
}

func TestMigratePartialLegacyIsIgnored(t *testing.T) {
	cfg := Default()
	if MigrateLegacyAgentModel(&cfg, "openai", "") { // model missing
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
	cfg.Providers = map[string]ProviderConfig{
		"openai": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1"},
	}

	if !MigrateLegacyAgentModel(&cfg, "openai", "gpt-4o") {
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
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"mine": {Name: "mine", Provider: "groq", Model: "llama"},
	}

	MigrateLegacyAgentModel(&cfg, "openai", "gpt-4o")

	if _, ok := cfg.Models.Presets["mine"]; !ok {
		t.Error("migration dropped an existing preset")
	}
}
