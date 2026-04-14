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
	ClarifyNever     ClarifyMode = "never"
	ClarifyAmbiguous ClarifyMode = "ambiguous"
	ClarifyAlways    ClarifyMode = "always"
)

// ProviderSubtype distinguishes local-server dialects for OpenAI-compatible endpoints.
type ProviderSubtype string

const (
	SubtypeOpenAI   ProviderSubtype = "openai"
	SubtypeOllama   ProviderSubtype = "ollama"
	SubtypeLlamaCPP ProviderSubtype = "llama_cpp"
	SubtypeVLLM     ProviderSubtype = "vllm"
	SubtypeLMStudio ProviderSubtype = "lmstudio"
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
	// Sampler parameters for local model tuning
	TopP          float64 `toml:"top_p"`
	MinP          float64 `toml:"min_p"`
	RepeatPenalty float64 `toml:"repeat_penalty"`
	Seed          int     `toml:"seed"`
	// ProviderSubtype hints at local-server dialect; auto-detected from BaseURL when empty.
	Subtype ProviderSubtype `toml:"subtype"`
	// SupportsTools explicitly enables/disables tool-use for this model.
	// When set, it overrides the auto-detection logic (useful for local models that support tools).
	SupportsTools *bool `toml:"supports_tools,omitempty"`
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
	EditFormatWholeFile     EditFormat = "wholefile"      // full file in fenced block
	EditFormatSearchReplace EditFormat = "search-replace" // <<<<<<< SEARCH / >>>>>>> REPLACE
	EditFormatUdiff         EditFormat = "udiff"          // unified diff
)

// CriticMode controls how the critic is invoked.
type CriticMode string

const (
	CriticModeSeparate CriticMode = "separate" // separate round-trip to critic model
	CriticModeSelf     CriticMode = "self"     // executor emits self-critique
)

// PermissionMode controls when to ask for user confirmation before edits.
type PermissionMode string

const (
	PermissionNever  PermissionMode = "never"  // Never ask, apply edits immediately (default)
	PermissionAlways PermissionMode = "always" // Always ask before any edit
	PermissionSmart  PermissionMode = "smart"  // Ask only for destructive/major changes
)

// LoopConfig controls task-loop behaviour.
type LoopConfig struct {
	MaxRounds      int            `toml:"max_rounds"`
	CompactAfter   int            `toml:"compact_after"`
	Clarify        ClarifyMode    `toml:"clarify"`
	EditFormat     EditFormat     `toml:"edit_format"`
	LinterIsCritic bool           `toml:"linter_is_critic"` // auto-PASS on clean lint
	CriticMode     CriticMode     `toml:"critic_mode"`      // "separate" or "self"
	LocalProfile   bool           `toml:"local_profile"`    // one-knob local optimization
	Permission     PermissionMode `toml:"permission"`       // when to ask for edit confirmation
	ShowDiff       bool           `toml:"show_diff"`        // show diff after edits (default: true)
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

// AnalyticsConfig controls analytics settings.
type AnalyticsConfig struct {
	Enabled  bool   `toml:"enabled"`
	Provider string `toml:"provider"`
	APIKey   string `toml:"api_key"`
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
	Model   Models       `toml:"model"`
	Loop    LoopConfig   `toml:"loop"`
	Linters LinterConfig `toml:"linters"`
}

// Config is the top-level configuration object.
type Config struct {
	Model     Models             `toml:"model"`
	Loop      LoopConfig         `toml:"loop"`
	Git       GitConfig          `toml:"git"`
	Linters   LinterConfig       `toml:"linters"`
	Tools     ToolsConfig        `toml:"tools"`
	Analytics AnalyticsConfig    `toml:"analytics"`
	Profiles  map[string]Profile `toml:"profiles"`

	// LogFile is the optional path for the structured log sink.
	LogFile string `toml:"log_file"`

	// PreApplyReview enables critic review before file changes are applied.
	// When true, the critic reviews proposed changes before any files are modified.
	PreApplyReview bool `toml:"pre_apply_review"`

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

// ApplyModelDefaults runs DetectSubtype and ApplyDefaults on all configured models.
func (c *Config) ApplyModelDefaults() {
	c.Model.Marshal.DetectSubtype()
	c.Model.Marshal.ApplyDefaults()
	c.Model.Executor.DetectSubtype()
	c.Model.Executor.ApplyDefaults()
	c.Model.Critic.DetectSubtype()
	c.Model.Critic.ApplyDefaults()
	c.Model.Compactor.DetectSubtype()
	c.Model.Compactor.ApplyDefaults()

	// Auto-detect local profile if not explicitly set.
	if !c.Loop.LocalProfile {
		c.Loop.LocalProfile = c.hasLocalSubtype()
	}

	// Apply local-profile defaults when enabled.
	if c.Loop.LocalProfile {
		c.applyLocalProfileDefaults()
	}
}

// hasLocalSubtype returns true if any role points at a local server.
func (c *Config) hasLocalSubtype() bool {
	roles := []ModelConfig{c.Model.Marshal, c.Model.Executor, c.Model.Critic, c.Model.Compactor}
	for _, mc := range roles {
		if mc.DetectSubtype() != SubtypeOpenAI {
			return true
		}
	}
	return false
}

// applyLocalProfileDefaults sets sensible defaults for local models.
func (c *Config) applyLocalProfileDefaults() {
	// Conservative round limits: small models rarely recover across rounds.
	if c.Loop.MaxRounds == 0 || c.Loop.MaxRounds == 3 {
		c.Loop.MaxRounds = 2
	}
	if c.Loop.CompactAfter == 0 || c.Loop.CompactAfter == 2 {
		c.Loop.CompactAfter = 1
	}

	// Linter acts as critic for faster feedback.
	if !c.Loop.LinterIsCritic {
		c.Loop.LinterIsCritic = true
	}

	// Self-critique when feasible (halves round-trips).
	if c.Loop.CriticMode == "" || c.Loop.CriticMode == CriticModeSeparate {
		// Check if executor and critic point at same model.
		execModel := c.Model.Executor.Model
		critModel := c.Model.Critic.Model
		if execModel != "" && execModel == critModel {
			c.Loop.CriticMode = CriticModeSelf
		}
	}

	// Smaller diffs work better with small context windows.
	if c.Loop.EditFormat == "" || c.Loop.EditFormat == EditFormatWholeFile {
		c.Loop.EditFormat = EditFormatSearchReplace
	}
}

// ApplyLocalProfile sets local-optimized defaults when LocalProfile is true
// or when a local subtype is auto-detected. Shows a banner in the returned string.
func (c *Config) ApplyLocalProfile() string {
	// Auto-detect if any role uses a local subtype
	isLocal := c.Model.Executor.DetectSubtype() != SubtypeOpenAI ||
		c.Model.Critic.DetectSubtype() != SubtypeOpenAI ||
		c.Model.Marshal.DetectSubtype() != SubtypeOpenAI ||
		c.Model.Compactor.DetectSubtype() != SubtypeOpenAI

	if !c.Loop.LocalProfile && !isLocal {
		return "" // nothing to do, not a local setup
	}

	// Apply local-profile defaults (only if not explicitly set)
	if c.Loop.MaxRounds == 0 || c.Loop.MaxRounds == 3 { // default was 3
		c.Loop.MaxRounds = 2
	}
	if c.Loop.CompactAfter == 0 || c.Loop.CompactAfter == 2 { // default was 2
		c.Loop.CompactAfter = 1
	}
	if !c.Loop.LinterIsCritic {
		c.Loop.LinterIsCritic = true
	}
	if c.Loop.CriticMode == "" || c.Loop.CriticMode == CriticModeSeparate {
		c.Loop.CriticMode = CriticModeSelf
	}
	if c.Loop.EditFormat == "" || c.Loop.EditFormat == EditFormatWholeFile {
		c.Loop.EditFormat = EditFormatSearchReplace
	}

	return fmt.Sprintf("local profile active (%s executor)\n  max_rounds=%d  critic=%s  edit=%s  pipeline_parallel=1\n  override with loop.local_profile=false",
		c.Model.Executor.DetectSubtype(),
		c.Loop.MaxRounds,
		c.Loop.CriticMode,
		c.Loop.EditFormat,
	)
}

// defaults returns a Config populated with sensible defaults.
func defaults() Config {
	return Config{
		Loop: LoopConfig{
			MaxRounds:      3,
			CompactAfter:   2,
			Clarify:        ClarifyAmbiguous,
			EditFormat:     EditFormatWholeFile,
			LinterIsCritic: false,
			CriticMode:     CriticModeSeparate,
			LocalProfile:   false,
			Permission:     PermissionNever,
			ShowDiff:       true,
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

	// Detect subtypes and apply local-model defaults.
	cfg.ApplyModelDefaults()

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
	if src.TopP != 0 {
		dst.TopP = src.TopP
	}
	if src.MinP != 0 {
		dst.MinP = src.MinP
	}
	if src.RepeatPenalty != 0 {
		dst.RepeatPenalty = src.RepeatPenalty
	}
	if src.Seed != 0 {
		dst.Seed = src.Seed
	}
	if src.Subtype != "" {
		dst.Subtype = src.Subtype
	}
}

// DetectSubtype infers the provider subtype from the BaseURL when not explicitly set.
// Detection rules:
//   - :11434 or /api/generate path -> ollama
//   - :1234 (LM Studio default) -> lmstudio
//   - :8000 + /v1/models lists non-OpenAI -> vllm (heuristic)
//   - localhost with /slots endpoint -> llama_cpp
//   - anything else -> openai
func (m *ModelConfig) DetectSubtype() ProviderSubtype {
	if m.Subtype != "" {
		return m.Subtype
	}
	if m.BaseURL == "" {
		return SubtypeOpenAI
	}
	lower := strings.ToLower(m.BaseURL)

	// Ollama detection
	if strings.Contains(lower, ":11434") || strings.Contains(lower, "/api/generate") {
		return SubtypeOllama
	}

	// LM Studio detection
	if strings.Contains(lower, ":1234") {
		return SubtypeLMStudio
	}

	// vLLM detection (heuristic: port 8000)
	if strings.Contains(lower, ":8000") {
		return SubtypeVLLM
	}

	// llama.cpp server detection (default port 8080 or localhost)
	if strings.Contains(lower, ":8080") || strings.Contains(lower, "localhost") {
		return SubtypeLlamaCPP
	}

	return SubtypeOpenAI
}

// ApplyDefaults sets sensible defaults for local models based on subtype.
// Call after DetectSubtype and after loading config.
func (m *ModelConfig) ApplyDefaults() {
	if m.DetectSubtype() != SubtypeOpenAI {
		// Local model defaults for coding
		if m.Temperature == 0 && m.TopP == 0 && m.MinP == 0 {
			m.Temperature = 0.0
			m.TopP = 0.95
			m.MinP = 0.05
			m.RepeatPenalty = 1.05
		}
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
