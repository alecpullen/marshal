package app

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

// embeddingWarningBase returns a config with semantic indexing enabled and
// nothing else configured — the genuine "no embedding preset" state.
func embeddingWarningBase() config.Config {
	cfg := config.Default()
	cfg.Indexing.UseEmbeddings = true
	return cfg
}

func TestEmbeddingStartupWarningSilentWhenEmbeddingsDisabled(t *testing.T) {
	cfg := embeddingWarningBase()
	cfg.Indexing.UseEmbeddings = false
	// Even with an unresolvable preset name set, disabling embeddings must
	// silence the warning entirely.
	cfg.Indexing.EmbeddingPreset = "ollama/nomic-embed-text"
	if msg, ok := embeddingStartupWarning(cfg); ok {
		t.Fatalf("embeddingStartupWarning(cfg) = (%q, true), want no warning when indexing.use_embeddings is false", msg)
	}
}

func TestEmbeddingStartupWarningSilentWhenResolvable(t *testing.T) {
	cfg := embeddingWarningBase()
	// Canonical name with a configured local provider: the router
	// synthesizes the preset, so there is nothing to warn about.
	cfg.Indexing.EmbeddingPreset = "ollama/nomic-embed-text"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
	}
	if msg, ok := embeddingStartupWarning(cfg); ok {
		t.Fatalf("embeddingStartupWarning(cfg) = (%q, true), want no warning when the embedding route resolves", msg)
	}
}

func TestEmbeddingStartupWarningSaysNotConfiguredWhenNothingSet(t *testing.T) {
	cfg := embeddingWarningBase()
	msg, ok := embeddingStartupWarning(cfg)
	if !ok {
		t.Fatal("embeddingStartupWarning(cfg) = no warning, want one when use_embeddings is true and no preset is set")
	}
	if !strings.Contains(msg, "no embedding preset is configured") {
		t.Fatalf("warning = %q, want the classic no-preset wording", msg)
	}
}

// TestEmbeddingStartupWarningNamesUnresolvablePreset pins the regression
// masked for three days in Sep 2026: [indexing] embedding_preset kept the
// name 'ollama/nomic-embed-text' while a stale rewrite removed both
// [providers.ollama] and the [models.presets] entry. ResolveEmbedding
// returned ErrPresetNotFound, but the startup warning claimed "no embedding
// preset is configured" — pointing the user away from the actual problem.
func TestEmbeddingStartupWarningNamesUnresolvablePreset(t *testing.T) {
	cfg := embeddingWarningBase()
	cfg.Indexing.EmbeddingPreset = "ollama/nomic-embed-text"
	msg, ok := embeddingStartupWarning(cfg)
	if !ok {
		t.Fatal("embeddingStartupWarning(cfg) = no warning, want one when the named preset cannot resolve")
	}
	if strings.Contains(msg, "no embedding preset is configured") {
		t.Fatalf("warning = %q, must not claim nothing is configured when a preset name IS set", msg)
	}
	if !strings.Contains(msg, "ollama/nomic-embed-text") {
		t.Fatalf("warning = %q, want it to name the configured preset", msg)
	}
	if !strings.Contains(msg, "ollama") {
		t.Fatalf("warning = %q, want it to point at the missing provider entry", msg)
	}
}

func TestEmbeddingStartupWarningMentionsRemoteGate(t *testing.T) {
	cfg := embeddingWarningBase()
	cfg.Indexing.EmbeddingPreset = "openai/text-embedding-3-small"
	cfg.Providers = map[string]config.ProviderConfig{
		"openai": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"openai/text-embedding-3-small": {Provider: "openai", Model: "text-embedding-3-small"},
	}
	msg, ok := embeddingStartupWarning(cfg)
	if !ok {
		t.Fatal("embeddingStartupWarning(cfg) = no warning, want one when a remote embedding preset is blocked")
	}
	if !strings.Contains(msg, "remote_providers_allowed") {
		t.Fatalf("warning = %q, want it to name the privacy.remote_providers_allowed gate", msg)
	}
}

// TestEmbeddingStartupWarningLegacyProfileBindingNamesPreset covers the
// legacy per-profile embedding role: when the bound preset is gone, the
// warning must surface the router's error (which names the preset) rather
// than the structural not-configured wording.
func TestEmbeddingStartupWarningLegacyProfileBindingNamesPreset(t *testing.T) {
	cfg := embeddingWarningBase()
	cfg.Profile.Default = "single"
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"single": {
			Name: "single",
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleEmbedding: {Preset: "ollama/gone-embedding"},
			},
		},
	}
	msg, ok := embeddingStartupWarning(cfg)
	if !ok {
		t.Fatal("embeddingStartupWarning(cfg) = no warning, want one when the profile-bound embedding preset is missing")
	}
	if strings.Contains(msg, "no embedding preset is configured") {
		t.Fatalf("warning = %q, must not claim nothing is configured when a binding IS set", msg)
	}
	if !strings.Contains(msg, "ollama/gone-embedding") {
		t.Fatalf("warning = %q, want it to name the missing bound preset", msg)
	}
}
