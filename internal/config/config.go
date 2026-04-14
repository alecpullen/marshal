package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

type AgentConfig struct {
	Provider      string  `toml:"provider"` // "ollama", "openai", "fireworks", "together"
	Model         string  `toml:"model"`
	BaseURL       string  `toml:"base_url"`
	APIKey        string  `toml:"api_key"`
	Temperature   float64 `toml:"temperature"`
	MaxTokens     int     `toml:"max_tokens"`
	JSONOutput    bool    `toml:"json_output"`
	ContextWindow int     `toml:"context_window"` // Ollama num_ctx, critical for tool use
	EnableTools   bool    `toml:"enable_tools"`   // opt-in tool use for this agent role
	MaxToolCalls  int     `toml:"max_tool_calls"` // max tool calls per round (default 20)
}

// GetProvider returns the provider, defaulting to "openai" if not set.
// This maintains backward compatibility with existing configs.
func (ac AgentConfig) GetProvider() string {
	if ac.Provider == "" {
		return "openai"
	}
	return ac.Provider
}

// OverrideModel returns a copy of ac with the model (and optionally provider) overridden.
func (ac AgentConfig) OverrideModel(model string, provider ...string) AgentConfig {
	copy := ac
	copy.Model = model
	if len(provider) > 0 && provider[0] != "" {
		copy.Provider = provider[0]
	}
	return copy
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

type UIConfig struct {
	Editor string `toml:"editor"` // e.g. "vim", "nvim", "nano". Falls back to $EDITOR then vim.
}

// CustomCommand defines a user-configured command for the TUI.
// Supports shell execution, builtin aliases, and view opening.
type CustomCommand struct {
	Action  string `toml:"action"`  // "shell", "builtin", "view"
	Command string `toml:"command"` // for shell action: the command to execute
	Builtin string `toml:"builtin"` // for builtin action: the builtin command to alias
	View    string `toml:"view"`    // for view action: the view to open
	Help    string `toml:"help"`    // description shown in completions
}

// PipelineConfig controls parallel pipeline execution behaviour.
type PipelineConfig struct {
	MaxParallel int  `toml:"max_parallel"` // max tasks per tier running at once; 0 → 3
	FailFast    bool `toml:"fail_fast"`    // stop on first task failure (default true)
}

// OrchestratorConfig controls Marshal's autonomous decision-making behavior.
type OrchestratorConfig struct {
	Mode                 string  `toml:"mode"`                   // "autonomous" or "interactive" (default: autonomous)
	MaxExplorationSteps  int     `toml:"max_exploration_steps"`  // max steps for autonomous exploration (default: 10)
	AutoConfirmComplex   bool    `toml:"auto_confirm_complex"`   // skip confirmation for complex tasks (default: true)
	AskBeforeDestructive bool    `toml:"ask_before_destructive"` // always ask for destructive operations (default: true)
	MinConfidence        float64 `toml:"min_confidence"`         // minimum confidence before exploring (default: 0.6)
}

// GetMode returns the orchestrator mode, defaulting to "autonomous".
func (o OrchestratorConfig) GetMode() string {
	if o.Mode == "" || o.Mode == "autonomous" {
		return "autonomous"
	}
	return o.Mode
}

// GetMaxExplorationSteps returns the max exploration steps with default of 10.
func (o OrchestratorConfig) GetMaxExplorationSteps() int {
	if o.MaxExplorationSteps <= 0 {
		return 10
	}
	return o.MaxExplorationSteps
}

// GetMinConfidence returns the minimum confidence threshold with default of 0.6.
func (o OrchestratorConfig) GetMinConfidence() float64 {
	if o.MinConfidence <= 0 {
		return 0.6
	}
	return o.MinConfidence
}

// MaxParallelOrDefault returns MaxParallel with a sensible default of 3.
func (p PipelineConfig) MaxParallelOrDefault() int {
	if p.MaxParallel <= 0 {
		return 3
	}
	return p.MaxParallel
}

type Config struct {
	Executor     AgentConfig              `toml:"executor"`
	Critic       AgentConfig              `toml:"critic"`
	Marshal      AgentConfig              `toml:"marshal"` // Optional: Marshal orchestrator config
	Planner      AgentConfig              `toml:"planner"`
	Orchestrator OrchestratorConfig       `toml:"orchestrator"` // Controls autonomous decision-making
	Pipeline     PipelineConfig           `toml:"pipeline"`
	Loop         LoopConfig               `toml:"loop"`
	Session      SessionConfig            `toml:"session"`
	UI           UIConfig                 `toml:"ui"`
	Commands     map[string]CustomCommand `toml:"commands"` // User-defined custom commands
	RepoRoot     string                   // Set at runtime, not from config file
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

// GetMarshalConfig returns the Marshal agent config, falling back to Executor if not set.
// This allows users to configure Marshal independently or share executor settings.
func (c *Config) GetMarshalConfig() AgentConfig {
	// If Marshal has its own config, use it
	if c.Marshal.Model != "" || c.Marshal.BaseURL != "" {
		return c.Marshal
	}
	// Otherwise fall back to Executor settings
	return c.Executor
}

func (c *Config) Validate() error {
	// Validate Executor
	if c.Executor.Model == "" {
		return fmt.Errorf("executor.model is required")
	}
	if c.Executor.BaseURL == "" {
		return fmt.Errorf("executor.base_url is required")
	}
	// API key required except for Ollama (local) and Bedrock (AWS credentials)
	if c.Executor.APIKey == "" && c.Executor.GetProvider() != "ollama" && c.Executor.GetProvider() != "bedrock" {
		return fmt.Errorf("executor.api_key / $FIREWORKS_API_KEY is required (not needed for ollama/bedrock providers)")
	}

	// Validate Critic
	if c.Critic.Model == "" {
		return fmt.Errorf("critic.model is required")
	}
	if c.Critic.BaseURL == "" {
		return fmt.Errorf("critic.base_url is required")
	}
	// API key required except for Ollama (local) and Bedrock (AWS credentials)
	if c.Critic.APIKey == "" && c.Critic.GetProvider() != "ollama" && c.Critic.GetProvider() != "bedrock" {
		return fmt.Errorf("critic.api_key / $FIREWORKS_API_KEY is required (not needed for ollama/bedrock providers)")
	}

	// Marshal is optional — falls back to Executor
	if c.Marshal.Model != "" && c.Marshal.BaseURL == "" {
		return fmt.Errorf("marshal.base_url is required when marshal.model is set")
	}

	// Planner is optional — warn but don't hard-fail (existing users won't have [planner])
	if c.Planner.Model == "" {
		fmt.Fprintf(os.Stderr, "warning: planner.model not configured; `marshal pipeline` will not work\n")
	}

	// Marshal inherits from executor if not explicitly configured
	c.applyMarshalDefaults()

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

	// Validate custom commands
	for name, cmd := range c.Commands {
		if name == "" {
			return fmt.Errorf("custom command name cannot be empty")
		}
		switch cmd.Action {
		case "shell":
			if cmd.Command == "" {
				return fmt.Errorf("command for '%s' cannot be empty when action is 'shell'", name)
			}
		case "builtin":
			if cmd.Builtin == "" {
				return fmt.Errorf("builtin for '%s' cannot be empty when action is 'builtin'", name)
			}
		case "view":
			if cmd.View == "" {
				return fmt.Errorf("view for '%s' cannot be empty when action is 'view'", name)
			}
		default:
			return fmt.Errorf("custom command '%s' has invalid action '%s' (must be 'shell', 'builtin', or 'view')", name, cmd.Action)
		}
	}

	return nil
}

// applyMarshalDefaults fills in Marshal config from Executor if Marshal is not fully configured.
// This allows users to omit [marshal] section and have it inherit from [executor].
func (c *Config) applyMarshalDefaults() {
	if c.Marshal.Provider == "" {
		c.Marshal.Provider = c.Executor.Provider
	}
	if c.Marshal.Model == "" {
		c.Marshal.Model = c.Executor.Model
	}
	if c.Marshal.BaseURL == "" {
		c.Marshal.BaseURL = c.Executor.BaseURL
	}
	if c.Marshal.APIKey == "" {
		c.Marshal.APIKey = c.Executor.APIKey
	}
	if c.Marshal.Temperature == 0 {
		c.Marshal.Temperature = 0.3 // Lower temp for planning
	}
	if c.Marshal.MaxTokens == 0 {
		c.Marshal.MaxTokens = 4096
	}
	if c.Marshal.ContextWindow == 0 {
		c.Marshal.ContextWindow = c.Executor.ContextWindow
	}
}
