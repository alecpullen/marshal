package config

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestProviderConfigDecodesKeepAliveAndThinkingBudget(t *testing.T) {
	var cfg struct {
		Providers map[string]ProviderConfig `toml:"providers"`
	}
	doc := `
[providers.local]
type = "ollama"
base_url = "http://localhost:11434"
keep_alive = "2h"

[providers.claude]
type = "anthropic"
base_url = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY"
thinking_budget = 4096
reasoning_summary = true
`
	if err := toml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("toml.Unmarshal: %v", err)
	}
	if got := cfg.Providers["local"].KeepAlive; got != "2h" {
		t.Fatalf("KeepAlive = %q, want %q", got, "2h")
	}
	if got := cfg.Providers["claude"].ThinkingBudget; got != 4096 {
		t.Fatalf("ThinkingBudget = %d, want 4096", got)
	}
	if got := cfg.Providers["claude"].ReasoningSummary; !got {
		t.Fatalf("ReasoningSummary = %v, want true", got)
	}
	if got := cfg.Providers["local"].ThinkingBudget; got != 0 {
		t.Fatalf("unset ThinkingBudget = %d, want 0", got)
	}
}

func TestProviderConfigTemplateRoundTrip(t *testing.T) {
	var cfg struct {
		Providers map[string]ProviderConfig `toml:"providers"`
	}
	doc := `
[providers.work]
type = "openai_compatible"
base_url = "https://api.openai.com/v1"
template = "openai"
`
	if err := toml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("toml.Unmarshal: %v", err)
	}
	if got := cfg.Providers["work"].Template; got != "openai" {
		t.Fatalf("Template = %q, want openai", got)
	}
	out, err := toml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("toml.Marshal: %v", err)
	}
	if strings.Contains(string(out), `template = ""`) {
		t.Fatalf("empty template should be omitted, got:\n%s", out)
	}
}
