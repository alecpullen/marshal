package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"

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

type SessionConfig struct {
	DBPath string `toml:"db_path"`
}

type Config struct {
	Executor AgentConfig   `toml:"executor"`
	Critic   AgentConfig   `toml:"critic"`
	Loop     LoopConfig    `toml:"loop"`
	Session  SessionConfig `toml:"session"`
}

// loadDotEnv parses a .env file into a map. Shell env takes precedence —
// values already set in the environment are not overwritten.
func loadDotEnv(path string) map[string]string {
	env := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return env // no .env file is fine
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		var key, val string
		if rest, ok := strings.CutPrefix(line, "set "); ok {
			// fish shell: set KEY VALUE
			parts := strings.SplitN(strings.TrimSpace(rest), " ", 2)
			if len(parts) != 2 {
				continue
			}
			key, val = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		} else {
			// standard: KEY=VALUE
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key, val = strings.TrimSpace(k), strings.TrimSpace(v)
		}

		// Strip optional surrounding quotes
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		env[key] = val
	}
	return env
}

func Load(path string) (*Config, error) {
	dotenv := loadDotEnv(".env")

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	expanded := os.Expand(string(raw), func(key string) string {
		// Shell env takes precedence over .env
		if val := os.Getenv(key); val != "" {
			return val
		}
		if val, ok := dotenv[key]; ok {
			return val
		}
		fmt.Printf("warning: $%s is not set\n", key)
		return ""
	})
	var cfg Config
	if _, err := toml.Decode(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
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
		return fmt.Errorf("executor.api_key / $FIREWORKS_API_KEY is required")
	}
	if c.Critic.Model == "" {
		return fmt.Errorf("critic.model is required")
	}
	if c.Critic.BaseURL == "" {
		return fmt.Errorf("critic.base_url is required")
	}
	if c.Critic.APIKey == "" {
		return fmt.Errorf("critic.api_key / $FIREWORKS_API_KEY is required")
	}

	// Loop validation
	if c.Loop.MaxRounds < 1 {
		return fmt.Errorf("loop.max_rounds must be >= 1")
	}
	if c.Loop.CompactAfter < 1 {
		return fmt.Errorf("loop.compact_after must be >= 1")
	}
	if c.Loop.CompactAfter > c.Loop.MaxRounds {
		return fmt.Errorf("loop.compact_after (%d) cannot exceed loop.max_rounds (%d)",
			c.Loop.CompactAfter, c.Loop.MaxRounds)
	}

	return nil
}
