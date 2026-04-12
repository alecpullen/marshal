package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alec/marshal/internal/config"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const baseToml = `
[model.marshal]
provider = "fireworks"
model    = "accounts/fireworks/models/qwen3-235b"
api_key  = "${FIREWORKS_API_KEY}"
base_url = "https://api.fireworks.ai/inference/v1"

[model.executor]
provider = "fireworks"
model    = "accounts/fireworks/models/qwen3-235b"
api_key  = "${FIREWORKS_API_KEY}"
base_url = "https://api.fireworks.ai/inference/v1"

[model.critic]
provider = "fireworks"
model    = "accounts/fireworks/models/qwen3-235b"
api_key  = "${FIREWORKS_API_KEY}"
base_url = "https://api.fireworks.ai/inference/v1"

[model.compactor]
provider = "fireworks"
model    = "accounts/fireworks/models/qwen3-235b"
api_key  = "${FIREWORKS_API_KEY}"
base_url = "https://api.fireworks.ai/inference/v1"

[loop]
max_rounds    = 3
compact_after = 2
clarify       = "ambiguous"

[profiles.dev.model.executor]
provider = "ollama"
model    = "qwen3:8b"
base_url = "http://localhost:11434/v1"
api_key  = "ollama"
`

func TestLoad_Defaults(t *testing.T) {
	cfg, err := config.Load(config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.MaxRounds != 3 {
		t.Errorf("expected default max_rounds=3, got %d", cfg.Loop.MaxRounds)
	}
	if cfg.Loop.Clarify != config.ClarifyAmbiguous {
		t.Errorf("expected default clarify=ambiguous, got %q", cfg.Loop.Clarify)
	}
}

func TestLoad_File(t *testing.T) {
	dir := t.TempDir()
	f := writeFile(t, dir, "marshal.toml", baseToml)

	t.Setenv("FIREWORKS_API_KEY", "test-key-123")

	cfg, err := config.Load(config.Options{ExtraFiles: []string{f}})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Model.Executor.Provider != "fireworks" {
		t.Errorf("expected provider=fireworks, got %q", cfg.Model.Executor.Provider)
	}
	// Env-var expansion
	if cfg.Model.Executor.APIKey != "test-key-123" {
		t.Errorf("expected api_key=test-key-123, got %q", cfg.Model.Executor.APIKey)
	}
}

func TestLoad_Profile(t *testing.T) {
	dir := t.TempDir()
	f := writeFile(t, dir, "marshal.toml", baseToml)

	cfg, err := config.Load(config.Options{
		Profile:    "dev",
		ExtraFiles: []string{f},
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Model.Executor.Provider != "ollama" {
		t.Errorf("expected executor provider=ollama after profile merge, got %q", cfg.Model.Executor.Provider)
	}
	if cfg.Model.Executor.Model != "qwen3:8b" {
		t.Errorf("expected executor model=qwen3:8b after profile merge, got %q", cfg.Model.Executor.Model)
	}
	// Non-overridden roles remain from base
	if cfg.Model.Critic.Provider != "fireworks" {
		t.Errorf("expected critic provider=fireworks (unchanged), got %q", cfg.Model.Critic.Provider)
	}
	if cfg.ActiveProfile() != "dev" {
		t.Errorf("expected active profile=dev, got %q", cfg.ActiveProfile())
	}
}

func TestLoad_ProfileNotFound(t *testing.T) {
	dir := t.TempDir()
	f := writeFile(t, dir, "marshal.toml", baseToml)

	_, err := config.Load(config.Options{
		Profile:    "nonexistent",
		ExtraFiles: []string{f},
	})
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
}

func TestLoad_EnvVarOverride(t *testing.T) {
	dir := t.TempDir()
	f := writeFile(t, dir, "marshal.toml", baseToml)

	t.Setenv("MARSHAL_EXECUTOR_MODEL", "gpt-4o")
	t.Setenv("MARSHAL_EXECUTOR_API_KEY", "sk-override")

	cfg, err := config.Load(config.Options{ExtraFiles: []string{f}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Executor.Model != "gpt-4o" {
		t.Errorf("expected executor model=gpt-4o from env, got %q", cfg.Model.Executor.Model)
	}
	if cfg.Model.Executor.APIKey != "sk-override" {
		t.Errorf("expected executor api_key=sk-override from env, got %q", cfg.Model.Executor.APIKey)
	}
}

func TestRedacted(t *testing.T) {
	dir := t.TempDir()
	f := writeFile(t, dir, "marshal.toml", baseToml)
	t.Setenv("FIREWORKS_API_KEY", "super-secret")

	cfg, err := config.Load(config.Options{ExtraFiles: []string{f}})
	if err != nil {
		t.Fatal(err)
	}

	r := cfg.Redacted()
	if r.Model.Executor.APIKey != "<redacted>" {
		t.Errorf("expected <redacted>, got %q", r.Model.Executor.APIKey)
	}
	// Original must be unchanged
	if cfg.Model.Executor.APIKey != "super-secret" {
		t.Errorf("Redacted() modified original config")
	}
}

func TestMissingFileIsSkipped(t *testing.T) {
	_, err := config.Load(config.Options{
		ExtraFiles: []string{"/nonexistent/path/marshal.toml"},
	})
	if err != nil {
		t.Fatalf("missing file should be skipped, got error: %v", err)
	}
}

func TestValidate(t *testing.T) {
	cfg := &config.Config{}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for empty config")
	}
}
