package provider

import (
	"testing"

	"marshal/internal/app/config"
)

func TestResolveAPIKey(t *testing.T) {
	// Literal key wins.
	k, err := ResolveAPIKey(config.ProviderConfig{APIKey: "lit-key"})
	if err != nil || k != "lit-key" {
		t.Fatalf("literal key: got (%q, %v), want (lit-key, nil)", k, err)
	}
	// Env var lookup.
	t.Setenv("TEST_KEY_ENV", "env-val")
	k, err = ResolveAPIKey(config.ProviderConfig{APIKeyEnv: "TEST_KEY_ENV"})
	if err != nil || k != "env-val" {
		t.Fatalf("env key: got (%q, %v), want (env-val, nil)", k, err)
	}
	// Missing env var.
	_, err = ResolveAPIKey(config.ProviderConfig{APIKeyEnv: "MISSING_ENV_VAR"})
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
	// No key (local endpoint).
	k, err = ResolveAPIKey(config.ProviderConfig{})
	if err != nil || k != "" {
		t.Fatalf("no key: got (%q, %v), want (\"\", nil)", k, err)
	}
}
