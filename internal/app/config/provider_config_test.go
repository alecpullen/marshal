package config

import (
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
	if got := cfg.Providers["local"].ThinkingBudget; got != 0 {
		t.Fatalf("unset ThinkingBudget = %d, want 0", got)
	}
}
