// Package config loads and validates marshal's TOML configuration.
//
// Precedence (highest to lowest):
//  1. CLI flags (applied by the caller after Load)
//  2. Environment variables (MARSHAL_ prefix)
//  3. ./marshal.toml
//  4. ~/.config/marshal/config.toml
//
// Profile merging: if --profile <name> is set, the [profiles.<name>] subtree
// is deep-merged on top of the base config after file loading.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Role names for the four-model roster.
const (
	RoleMarshal   = "marshal"
	RoleExecutor  = "executor"
	RoleCritic    = "critic"
	RoleCompactor = "compactor"
)

// ClarifyMode controls when the marshal model asks clarifying questions.
type ClarifyMode string

const (
	ClarifyNever    ClarifyMode = "never"
	ClarifyAmbiguous ClarifyMode = "ambiguous"
	ClarifyAlways   ClarifyMode = "always"
)

// ModelConfig holds per-role model settings.
type ModelConfig struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	APIKey   string `toml:"api_key"`
	BaseURL  string `toml:"base_url"`
	// Optional overrides
	MaxTokens   int     `toml:"max_tokens"`
	Temperature float64 `toml:"temperature"`
}

// Redacted returns a copy with the API key masked.
func (m ModelConfig) Redacted() ModelConfig {
	c := m
	if c.APIKey != "" {
		c.APIKey = "<redacted>"
	}
	return c
}

// GitConfig controls git integration.
type GitConfig struct {
	// Enabled gates all git operations (branch creation, commits, merges).
	// When false the executor still writes files to disk and the critic still
	// reviews them, but no branches are created and no commits are made.
	// Default: false — safe for development without a clean git working tree.
	Enabled bool `toml:"enabled"`
}

// EditFormat selects how the executor expresses file changes.
type EditFormat string

const (
	EditFormatWholeFile    EditFormat = "wholefile"     // full file in fenced block
	EditFormatSearchReplace EditFormat = "search-replace" // <<<<<<< SEARCH / >>>>>>> REPLACE
	EditFormatUdiff        EditFormat = "udiff"          // unified diff
)

// LoopConfig controls task-loop behaviour.
type LoopConfig struct {
	MaxRounds    int         `toml:"max_rounds"`
	CompactAfter int         `toml:"compact_after"`
	Clarify      ClarifyMode `toml:"clarify"`
	EditFormat   EditFormat  `toml:"edit_format"`
}

// LinterConfig maps file-extension groups to linter commands.
type LinterConfig struct {
	Go     string `toml:"go"`
	Python string `toml:"python"`
	JS     string `toml:"js"`
	TS     string `toml:"ts"`
}

// ToolsConfig controls the tool-use executor sandbox.
type ToolsConfig struct {
	RunCommandAllowlist []string `toml:"run_command_allowlist"`
}

// Models is the four-role model roster.
type Models struct {
	Marshal   ModelConfig `toml:"marshal"`
	Executor  ModelConfig `toml:"executor"`
	Critic    ModelConfig `toml:"critic"`
	Compactor ModelConfig `toml:"compactor"`
}

// Profile is a named overlay merged on top of the base config.
type Profile struct {
	Model   Models      `toml:"model"`
	Loop    LoopConfig  `toml:"loop"`
	Linters LinterConfig `toml:"linters"`
}

// Config is the top-level configuration object.
type Config struct {
	Model    Models                `toml:"model"`
	Loop     LoopConfig            `toml:"loop"`
	Git      GitConfig             `toml:"git"`
	Linters  LinterConfig          `toml:"linters"`
	Tools    ToolsConfig           `toml:"tools"`
	Profiles map[string]Profile    `toml:"profiles"`

	// LogFile is the optional path for the structured log sink.
	LogFile string `toml:"log_file"`

	// resolved profile name (set by Load, not from TOML)
	activeProfile string
}

// ModelFor returns the ModelConfig for the given role name.
func (c *Config) ModelFor(role string) (ModelConfig, error) {
	switch role {
	case RoleMarshal:
		return c.Model.Marshal, nil
	case RoleExecutor:
		return c.Model.Executor, nil
	case RoleCritic:
		return c.Model.Critic, nil
	case RoleCompactor:
		return c.Model.Compactor, nil
	default:
		return ModelConfig{}, fmt.Errorf("unknown role %q", role)
	}
}

// ActiveProfile returns the name of the currently active profile (empty = base).
func (c *Config) ActiveProfile() string { return c.activeProfile }

// defaults returns a Config populated with sensible defaults.
func defaults() Config {
	return Config{
		Loop: LoopConfig{
			MaxRounds:    3,
			CompactAfter: 2,
			Clarify:      ClarifyAmbiguous,
			EditFormat:   EditFormatWholeFile,
		},
		Linters: LinterConfig{
			Go:     "golangci-lint run",
			Python: "flake8",
			JS:     "eslint",
			TS:     "eslint",
		},
	}
}

// Options controls Load behaviour.
type Options struct {
	// Profile selects a named profile to merge over the base config.
	Profile string
	// ExtraFiles are additional TOML files to load (in order, after the
	// standard search path). Useful for tests.
	ExtraFiles []string
}

// Load reads configuration from the standard search path, expands environment
// variables, and optionally merges a profile.
func Load(opts Options) (*Config, error) {
	cfg := defaults()

	// Standard search path: user-global then project-local (project-local wins).
	paths := standardPaths()
	paths = append(paths, opts.ExtraFiles...)

	for _, p := range paths {
		if err := mergeFile(&cfg, p); err != nil {
			return nil, fmt.Errorf("loading %s: %w", p, err)
		}
	}

	// Apply profile overlay.
	if opts.Profile != "" {
		p, ok := cfg.Profiles[opts.Profile]
		if !ok {
			return nil, fmt.Errorf("profile %q not found in config", opts.Profile)
		}
		mergeProfile(&cfg, p)
		cfg.activeProfile = opts.Profile
	}

	// Expand ${ENV_VAR} placeholders throughout model API keys.
	expandEnv(&cfg)

	// Apply MARSHAL_* environment variable overrides.
	applyEnvOverrides(&cfg)

	return &cfg, nil
}

// standardPaths returns the ordered list of config file paths to search.
func standardPaths() []string {
	var paths []string

	// ~/.config/marshal/config.toml
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "marshal", "config.toml"))
	}

	// ./marshal.toml
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, "marshal.toml"))
	}

	return paths
}

// mergeFile decodes a TOML file into cfg, skipping missing files silently.
func mergeFile(cfg *Config, path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = toml.NewDecoder(f).Decode(cfg)
	return err
}

// mergeProfile overlays non-zero profile fields onto cfg.
func mergeProfile(cfg *Config, p Profile) {
	mergeModelConfig(&cfg.Model.Marshal, p.Model.Marshal)
	mergeModelConfig(&cfg.Model.Executor, p.Model.Executor)
	mergeModelConfig(&cfg.Model.Critic, p.Model.Critic)
	mergeModelConfig(&cfg.Model.Compactor, p.Model.Compactor)

	if p.Loop.MaxRounds != 0 {
		cfg.Loop.MaxRounds = p.Loop.MaxRounds
	}
	if p.Loop.CompactAfter != 0 {
		cfg.Loop.CompactAfter = p.Loop.CompactAfter
	}
	if p.Loop.Clarify != "" {
		cfg.Loop.Clarify = p.Loop.Clarify
	}
	if p.Linters.Go != "" {
		cfg.Linters.Go = p.Linters.Go
	}
	if p.Linters.Python != "" {
		cfg.Linters.Python = p.Linters.Python
	}
	if p.Linters.JS != "" {
		cfg.Linters.JS = p.Linters.JS
	}
	if p.Linters.TS != "" {
		cfg.Linters.TS = p.Linters.TS
	}
}

func mergeModelConfig(dst *ModelConfig, src ModelConfig) {
	if src.Provider != "" {
		dst.Provider = src.Provider
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.APIKey != "" {
		dst.APIKey = src.APIKey
	}
	if src.BaseURL != "" {
		dst.BaseURL = src.BaseURL
	}
	if src.MaxTokens != 0 {
		dst.MaxTokens = src.MaxTokens
	}
	if src.Temperature != 0 {
		dst.Temperature = src.Temperature
	}
}

// envVarRe matches ${VAR_NAME} placeholders.
var envVarRe = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

// expandStr replaces ${VAR} placeholders with environment variable values.
func expandStr(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(m string) string {
		name := m[2 : len(m)-1]
		return os.Getenv(name)
	})
}

func expandModelConfig(m *ModelConfig) {
	m.APIKey = expandStr(m.APIKey)
	m.BaseURL = expandStr(m.BaseURL)
	m.Model = expandStr(m.Model)
}

func expandEnv(cfg *Config) {
	expandModelConfig(&cfg.Model.Marshal)
	expandModelConfig(&cfg.Model.Executor)
	expandModelConfig(&cfg.Model.Critic)
	expandModelConfig(&cfg.Model.Compactor)
	cfg.LogFile = expandStr(cfg.LogFile)
}

// applyEnvOverrides applies MARSHAL_<ROLE>_<FIELD> environment variables.
// Supported: MARSHAL_EXECUTOR_API_KEY, MARSHAL_EXECUTOR_MODEL, etc.
func applyEnvOverrides(cfg *Config) {
	roles := map[string]*ModelConfig{
		"MARSHAL":   &cfg.Model.Marshal,
		"EXECUTOR":  &cfg.Model.Executor,
		"CRITIC":    &cfg.Model.Critic,
		"COMPACTOR": &cfg.Model.Compactor,
	}
	for envRole, mc := range roles {
		prefix := "MARSHAL_" + envRole + "_"
		if v := os.Getenv(prefix + "API_KEY"); v != "" {
			mc.APIKey = v
		}
		if v := os.Getenv(prefix + "MODEL"); v != "" {
			mc.Model = v
		}
		if v := os.Getenv(prefix + "BASE_URL"); v != "" {
			mc.BaseURL = v
		}
		if v := os.Getenv(prefix + "PROVIDER"); v != "" {
			mc.Provider = v
		}
	}

	if v := os.Getenv("MARSHAL_LOG_FILE"); v != "" {
		cfg.LogFile = v
	}
}

// Redacted returns a copy of Config with all API keys masked.
// Safe to print / log.
func (c *Config) Redacted() Config {
	out := *c
	out.Model.Marshal = c.Model.Marshal.Redacted()
	out.Model.Executor = c.Model.Executor.Redacted()
	out.Model.Critic = c.Model.Critic.Redacted()
	out.Model.Compactor = c.Model.Compactor.Redacted()
	// Redact profiles too.
	redactedProfiles := make(map[string]Profile, len(c.Profiles))
	for name, p := range c.Profiles {
		p.Model.Marshal = p.Model.Marshal.Redacted()
		p.Model.Executor = p.Model.Executor.Redacted()
		p.Model.Critic = p.Model.Critic.Redacted()
		p.Model.Compactor = p.Model.Compactor.Redacted()
		redactedProfiles[name] = p
	}
	out.Profiles = redactedProfiles
	return out
}

// Validate checks that required fields are populated.
func (c *Config) Validate() error {
	var errs []string
	roles := map[string]ModelConfig{
		RoleMarshal:   c.Model.Marshal,
		RoleExecutor:  c.Model.Executor,
		RoleCritic:    c.Model.Critic,
		RoleCompactor: c.Model.Compactor,
	}
	for role, mc := range roles {
		if mc.Model == "" {
			errs = append(errs, fmt.Sprintf("model.%s.model is required", role))
		}
	}
	if c.Loop.MaxRounds < 1 {
		errs = append(errs, "loop.max_rounds must be >= 1")
	}
	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
