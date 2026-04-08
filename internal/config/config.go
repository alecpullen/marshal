package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type AgentConfig struct {
	Model       string  `toml:"model"`
	BaseURL     string  `toml:"base_url"`
	APIKey      string  `toml:"api_key"`
	Temperature float64 `toml:"temperature"`
	MaxTokens   int     `toml:"max_tokens"`
	JSONOutput  bool    `toml:"json_output"`
}

type LoopConfig struct {
	MaxRounds       int     `toml:"max_rounds"`
	AutoCommit      bool    `toml:"auto_commit"`
	AutoRevert      bool    `toml:"auto_revert"`
	BranchIsolation bool    `toml:"branch_isolation"`
	CompactAfter    int     `toml:"compact_after"`
	TokenBudgetWarn float64 `toml:"token_budget_warning"`
}

type Config struct {
	Executor AgentConfig `toml:"executor"`
	Critic   AgentConfig `toml:"critic"`
	Loop     LoopConfig  `toml:"loop"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	expanded := os.Expand(string(raw), func(key string) string {
		val := os.Getenv(key)
		if val == "" {
			fmt.Printf("warning: $%s is not set\n", key)
		}
		return val
	})
	var cfg Config
	if _, err := toml.Decode(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", expanded, err)
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Executor.Model == "" {
		return fmt.Errorf("executor.model is required")
	}
	if c.Executor.BaseURL == "" {
		return fmt.Errorf("executor.base_url is required")
	}
	if c.Executor.APIKey == "" {
		return fmt.Errorf("executor.api_key / $RUNPOD_API_KEY is required")
	}
	if c.Critic.Model == "" {
		return fmt.Errorf("critic.model is required")
	}
	if c.Critic.BaseURL == "" {
		return fmt.Errorf("critic.base_url is required")
	}
	if c.Critic.APIKey == "" {
		return fmt.Errorf("critic.api_key / $RUNPOD_API_KEY is required")
	}
	return nil
}
