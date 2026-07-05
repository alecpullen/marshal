package config

import (
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/llm/routing"
)

func TestSaveProjectConfigRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = "local_balanced"
	cfg.Agent.Provider = "ollama"
	cfg.Agent.Model = "qwen2.5-coder:14b"
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {
			Name:      "coder",
			Provider:  "ollama",
			Model:     "qwen2.5-coder:14b",
			LocalOnly: true,
		},
	}

	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Profile.Default != "local_balanced" {
		t.Fatalf("profile default = %q, want local_balanced", loaded.Profile.Default)
	}
	if loaded.Agent.Provider != "" || loaded.Agent.Model != "" {
		t.Fatalf("agent section should be omitted when preset is active, got %+v", loaded.Agent)
	}
	if loaded.Privacy.RemoteProvidersAllowed {
		t.Fatal("remote_providers_allowed = true, want false")
	}
	preset := loaded.Models.Presets["coder"]
	if preset.Provider != "ollama" || preset.Model != "qwen2.5-coder:14b" || !preset.LocalOnly {
		t.Fatalf("preset coder = %+v", preset)
	}
}

func TestSaveProjectConfigRoundTripLegacyAgent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = ""
	cfg.Agent.Provider = "anthropic"
	cfg.Agent.Model = "claude-sonnet-4"
	cfg.AgentProfiles = nil

	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Agent.Provider != "anthropic" || loaded.Agent.Model != "claude-sonnet-4" {
		t.Fatalf("agent = %+v", loaded.Agent)
	}
}

func TestSaveProjectConfigRoundTripsAgentAndToolSettings(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = "local_balanced"
	cfg.Agent.MaxToolIterations = 8
	cfg.Agent.MaxRetries = 2
	cfg.Tools.Shell.DefaultTimeoutSeconds = 45
	cfg.Tools.Shell.MaxOutputBytes = 98765
	cfg.Tools.Shell.AllowNetwork = true
	cfg.Tools.Shell.AllowSudo = true
	cfg.Tools.Shell.AllowDestructive = true
	cfg.Tools.Shell.AutoApprove = true
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b"},
	}

	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Agent.MaxToolIterations != 8 || loaded.Agent.MaxRetries != 2 {
		t.Fatalf("agent settings = %+v", loaded.Agent)
	}
	shell := loaded.Tools.Shell
	if shell.DefaultTimeoutSeconds != 45 || shell.MaxOutputBytes != 98765 ||
		!shell.AllowNetwork || !shell.AllowSudo || !shell.AllowDestructive || !shell.AutoApprove {
		t.Fatalf("shell settings = %+v", shell)
	}
}

func TestSaveProjectConfigOmitsAgentWhenPresetActive(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = "local_balanced"
	cfg.Agent.Provider = "ollama"
	cfg.Agent.Model = "qwen2.5-coder:14b"
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {
			Name:     "coder",
			Provider: "ollama",
			Model:    "qwen2.5-coder:14b",
		},
	}

	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Agent.Provider != "" || loaded.Agent.Model != "" {
		t.Fatalf("agent section should be omitted when preset is active, got %+v", loaded.Agent)
	}
}

func TestSaveProjectConfigPreservesUnrelatedSections(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`
[commands]
test = "go test ./..."
format = "gofmt -w ."

[indexing]
use_treesitter = true
`), 0644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	cfg := Default()
	cfg.Profile.Default = "local_balanced"
	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Commands.Test != "go test ./..." {
		t.Fatalf("commands.test = %q", loaded.Commands.Test)
	}
	if !loaded.Indexing.UseTreesitter {
		t.Fatal("indexing.use_treesitter was not preserved")
	}
}
