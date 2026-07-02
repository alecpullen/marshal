package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultConfigValues(t *testing.T) {
	cfg := Default()

	if cfg.Project.Name != "marshal" {
		t.Fatalf("Project.Name = %q, want marshal", cfg.Project.Name)
	}
	if !reflect.DeepEqual(cfg.Project.Languages, []string{"go", "markdown"}) {
		t.Fatalf("Project.Languages = %#v, want go and markdown", cfg.Project.Languages)
	}
	if cfg.Commands.Test != "go test ./..." {
		t.Fatalf("Commands.Test = %q", cfg.Commands.Test)
	}
	if cfg.Commands.Format != "gofmt -w ." {
		t.Fatalf("Commands.Format = %q", cfg.Commands.Format)
	}
	if cfg.Commands.Vet != "go vet ./..." {
		t.Fatalf("Commands.Vet = %q", cfg.Commands.Vet)
	}
	if cfg.Privacy.RemoteProvidersAllowed {
		t.Fatal("RemoteProvidersAllowed = true, want false")
	}
	if !cfg.Privacy.RedactSecrets {
		t.Fatal("RedactSecrets = false, want true")
	}
	if cfg.Privacy.IncludeGitignoredFiles {
		t.Fatal("IncludeGitignoredFiles = true, want false")
	}
}

func TestLoadIgnoresMissingConfigFiles(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Project.Name != "marshal" {
		t.Fatalf("Project.Name = %q, want default marshal", cfg.Project.Name)
	}
}

func TestLoadProjectConfigOverridesGlobalConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, home+"/.config/marshal/config.toml", `
[project]
name = "global"
languages = ["go"]

[commands]
test = "global test"

[privacy]
remote_providers_allowed = true
redact_secrets = false
`)

	writeFile(t, work+"/.marshal/config.toml", `
[project]
name = "project"
languages = ["go", "markdown", "toml"]

[commands]
test = "project test"
format = "project format"

[privacy]
remote_providers_allowed = false
include_gitignored_files = true
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Project.Name != "project" {
		t.Fatalf("Project.Name = %q, want project", cfg.Project.Name)
	}
	if !reflect.DeepEqual(cfg.Project.Languages, []string{"go", "markdown", "toml"}) {
		t.Fatalf("Project.Languages = %#v", cfg.Project.Languages)
	}
	if cfg.Commands.Test != "project test" {
		t.Fatalf("Commands.Test = %q", cfg.Commands.Test)
	}
	if cfg.Commands.Format != "project format" {
		t.Fatalf("Commands.Format = %q", cfg.Commands.Format)
	}
	if cfg.Privacy.RemoteProvidersAllowed {
		t.Fatal("RemoteProvidersAllowed = true, want project override false")
	}
	if cfg.Privacy.RedactSecrets {
		t.Fatal("RedactSecrets = true, want global override false")
	}
	if !cfg.Privacy.IncludeGitignoredFiles {
		t.Fatal("IncludeGitignoredFiles = false, want project override true")
	}
}

func TestLoadMalformedConfigReturnsPath(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	path := work + "/.marshal/config.toml"
	writeFile(t, path, "[project\nname = broken")

	_, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err == nil {
		t.Fatal("Load returned nil error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q does not contain path %q", err.Error(), path)
	}
}

func TestDefaultConfigHasNoProviders(t *testing.T) {
	cfg := Default()

	if len(cfg.Providers) != 0 {
		t.Fatalf("Providers = %#v, want nil or empty", cfg.Providers)
	}
}

func TestLoadParsesProvidersBlock(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, work+"/.marshal/config.toml", `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "local-key"

[providers.openrouter]
type = "openai_compatible"
base_url = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.Providers) != 2 {
		t.Fatalf("Providers = %#v, want 2 entries", cfg.Providers)
	}

	ollama, ok := cfg.Providers["ollama"]
	if !ok {
		t.Fatalf("Providers[ollama] missing, got %#v", cfg.Providers)
	}
	wantOllama := ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "http://localhost:11434/v1",
		APIKey:  "local-key",
	}
	if !reflect.DeepEqual(ollama, wantOllama) {
		t.Fatalf("Providers[ollama] = %#v, want %#v", ollama, wantOllama)
	}

	openrouter, ok := cfg.Providers["openrouter"]
	if !ok {
		t.Fatalf("Providers[openrouter] missing, got %#v", cfg.Providers)
	}
	wantOpenrouter := ProviderConfig{
		Type:      "openai_compatible",
		BaseURL:   "https://openrouter.ai/api/v1",
		APIKeyEnv: "OPENROUTER_API_KEY",
	}
	if !reflect.DeepEqual(openrouter, wantOpenrouter) {
		t.Fatalf("Providers[openrouter] = %#v, want %#v", openrouter, wantOpenrouter)
	}
	if openrouter.APIKeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("Providers[openrouter].APIKeyEnv = %q, want OPENROUTER_API_KEY verbatim", openrouter.APIKeyEnv)
	}
}

func TestLoadProjectProvidersOverwriteGlobalProvidersByKey(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, home+"/.config/marshal/config.toml", `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "global-key"

[providers.lmstudio]
type = "openai_compatible"
base_url = "http://localhost:1234/v1"
api_key = "lmstudio-key"
`)

	writeFile(t, work+"/.marshal/config.toml", `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:9999/v1"
api_key = "project-key"

[providers.openrouter]
type = "openai_compatible"
base_url = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.Providers) != 3 {
		t.Fatalf("Providers = %#v, want 3 entries", cfg.Providers)
	}

	wantOllama := ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "http://localhost:9999/v1",
		APIKey:  "project-key",
	}
	if !reflect.DeepEqual(cfg.Providers["ollama"], wantOllama) {
		t.Fatalf("Providers[ollama] = %#v, want project override %#v", cfg.Providers["ollama"], wantOllama)
	}

	wantLMStudio := ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "http://localhost:1234/v1",
		APIKey:  "lmstudio-key",
	}
	if !reflect.DeepEqual(cfg.Providers["lmstudio"], wantLMStudio) {
		t.Fatalf("Providers[lmstudio] = %#v, want untouched global %#v", cfg.Providers["lmstudio"], wantLMStudio)
	}

	wantOpenrouter := ProviderConfig{
		Type:      "openai_compatible",
		BaseURL:   "https://openrouter.ai/api/v1",
		APIKeyEnv: "OPENROUTER_API_KEY",
	}
	if !reflect.DeepEqual(cfg.Providers["openrouter"], wantOpenrouter) {
		t.Fatalf("Providers[openrouter] = %#v, want %#v", cfg.Providers["openrouter"], wantOpenrouter)
	}
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestLoadToolsRules(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, home+"/.config/marshal/config.toml", `
[tools.shell]
auto_approve = false
allow_destructive = false
[tools.shell.allow]
commands = ["go test"]
[tools.shell.confirm]
commands = ["npm install"]
[tools.shell.deny]
patterns = ["sudo"]
`)

	writeFile(t, work+"/.marshal/config.toml", `
[tools.shell]
auto_approve = true
allow_destructive = true
[tools.shell.allow]
commands = ["go test", "git status"]
[tools.shell.confirm]
commands = ["yarn install"]
[tools.shell.deny]
patterns = ["rm -rf"]
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	s := cfg.Tools.Shell
	if !s.AutoApprove {
		t.Fatal("AutoApprove not merged, want true")
	}
	if !s.AllowDestructive {
		t.Fatal("AllowDestructive not merged, want true")
	}
	if !reflect.DeepEqual(s.Allow.Commands, []string{"go test", "git status"}) {
		t.Errorf("Allow.Commands = %#v", s.Allow.Commands)
	}
	if !reflect.DeepEqual(s.Confirm.Commands, []string{"yarn install"}) {
		t.Errorf("Confirm.Commands = %#v", s.Confirm.Commands)
	}
	if !reflect.DeepEqual(s.Deny.Patterns, []string{"rm -rf"}) {
		t.Errorf("Deny.Patterns = %#v", s.Deny.Patterns)
	}
}

