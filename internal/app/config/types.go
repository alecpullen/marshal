package config

import (
	"time"

	"marshal/internal/llm/routing"
	"marshal/internal/trust"
)

type Config struct {
	Project       ProjectConfig                         `toml:"project"`
	Commands      CommandsConfig                        `toml:"commands"`
	Profile       ProfileConfig                         `toml:"profile"`
	Agent         AgentConfig                           `toml:"agent"`
	Privacy       PrivacyConfig                         `toml:"privacy"`
	Indexing      IndexingConfig                        `toml:"indexing"`
	Providers     map[string]ProviderConfig             `toml:"providers"`
	Models        ModelsConfig                          `toml:"models"`
	AgentProfiles map[string]routing.AgentProfile       `toml:"agent_profiles"`
	CustomAgents  map[string]routing.CustomAgent        `toml:"custom_agents"`
	Agents        map[routing.AgentRole]AgentRoleConfig `toml:"agents"`
	Tools         ToolsConfig                           `toml:"tools"`
	Web           WebConfig                             `toml:"web"`
	Desktop       DesktopConfig                         `toml:"desktop"`
	Swarm         SwarmConfig                           `toml:"swarm"`
	SDD           SDDConfig                             `toml:"sdd"`
	MCP           MCPConfig                             `toml:"mcp"`
	Snapshots     SnapshotsConfig                       `toml:"snapshots"`
	History       HistoryConfig                         `toml:"history"`
	TUI           TUIConfig                             `toml:"tui"`
	Permissions   PermissionsConfig                     `toml:"permissions"`
	Diagnostics   DiagnosticsConfig                     `toml:"diagnostics"`
	Hooks         HooksConfig                           `toml:"hooks"`
	Session       SessionConfig                         `toml:"session"`
	LSP           LSPConfig                             `toml:"lsp"`
}

type ModelsConfig struct {
	Presets map[string]routing.ModelPreset `toml:"presets"`
}

type AgentRoleConfig struct {
	Context routing.ContextBudget `toml:"context"`
}

type ToolsConfig struct {
	Shell ShellToolConfig `toml:"shell"`
}

// SwarmConfig holds run-level swarm settings.
type SwarmConfig struct {
	Budget SwarmBudgetConfig `toml:"budget"`
}

type SwarmBudgetConfig struct {
	MaxFixRounds   int            `toml:"max_fix_rounds"`
	MaxTotalTokens int            `toml:"max_total_tokens"`
	ToolIters      map[string]int `toml:"tool_iters"`
}

// SDDConfig holds run-level settings for the plan-execution pipeline.
type SDDConfig struct {
	AutoWorktree    bool            `toml:"auto_worktree"`
	MaxFixRounds    int             `toml:"max_fix_rounds"`
	PlansDir        string          `toml:"plans_dir"`
	VerifyTimeoutMS int             `toml:"verify_timeout_ms"`
	CleanupAtStart  bool            `toml:"cleanup_at_start"`
	MaxTotalTokens  int             `toml:"max_total_tokens"`
	Verify          SDDVerifyConfig `toml:"verify"`
}

// SDDVerifyConfig is the build and test gate the controller runs itself
// after every task. Empty commands fall back to the Go defaults when the
// repository has a go.mod, and otherwise skip the gate with a visible note.
type SDDVerifyConfig struct {
	Build string `toml:"build"`
	Test  string `toml:"test"`
}

type MCPConfig struct {
	Servers                  map[string]MCPServerConfig `toml:"servers"`
	Policies                 map[string]string          `toml:"policies"`
	DisclosureThresholdTools int                        `toml:"disclosure_threshold_tools"`
}

type MCPServerConfig struct {
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	Trust   string            `toml:"trust"` // "" | "unrestricted" — see F-SEC-06
}

type SnapshotsConfig struct {
	Enabled       bool `toml:"enabled"`
	RetentionDays int  `toml:"retention_days"`
	MaxFileBytes  int  `toml:"max_file_bytes"`
}

type TUIConfig struct {
	Theme     string            `toml:"theme"`
	Palette   map[string]string `toml:"palette"`
	Mode      string            `toml:"mode"`
	SidePanel SidePanelConfig   `toml:"side_panel"`
}

// SidePanelConfig controls the widescreen side rail. MinWidth is the frame
// width at which the rail appears; WidthPct/MinCols/MaxCols size it. Hidden
// lists section IDs to suppress.
type SidePanelConfig struct {
	Enabled  bool     `toml:"enabled"`
	MinWidth int      `toml:"min_width"`
	WidthPct int      `toml:"width_pct"`
	MinCols  int      `toml:"min_cols"`
	MaxCols  int      `toml:"max_cols"`
	Hidden   []string `toml:"hidden"`
}

// HistoryConfig controls project-scoped prompt history (↑/↓ recall in the
// chat input). When Enabled is true (the default), submitted prompts are
// persisted to the project's prompt_history table and recalled on the
// first input line.
type HistoryConfig struct {
	Enabled bool `toml:"enabled"`
}

type PermissionsConfig struct {
	Rules []PermissionRule `toml:"rules"`
}

// PermissionRule maps a tool name to a decision. Permission may be a
// native tool name (e.g. "shell.run", "file.write_patch") or a full
// MCP tool name (e.g. "mcp.github.create_issue"). The rule fires when
// Permission equals the tool's effective permission and either Pattern
// is empty (exact match) or matches the resolved subject.
type PermissionRule struct {
	Permission string `toml:"permission"`
	Pattern    string `toml:"pattern"`
	Action     string `toml:"action"`
}

type DiagnosticsConfig struct {
	Commands map[string]string `toml:"commands"`
}

// HooksConfig holds the lifecycle-hook subsystem settings: the global
// fail-closed policy and the per-event hook entries. Hooks are local
// process execution only (no network, no remote) and are gated on project
// trust when the project-local config defines them.
type HooksConfig struct {
	FailClosed bool         `toml:"fail_closed"`
	Entries    []HookConfig `toml:"entries"`
}

// HookConfig is one lifecycle hook entry. Event names are validated by the
// hook runtime (see internal/hooks), not here, so the config layer only
// parses shape.
type HookConfig struct {
	Event     string `toml:"event"`
	Matcher   string `toml:"matcher"`
	Command   string `toml:"command"`
	TimeoutMS int    `toml:"timeout_ms"`
}

// SessionConfig holds session-level settings including context rollover.
type SessionConfig struct {
	Rollover RolloverConfig `toml:"rollover"`
}

// RolloverConfig controls automatic context rollover behaviour.
type RolloverConfig struct {
	Enabled                 bool              `toml:"enabled"`
	Policy                  string            `toml:"policy"`
	ContextPercentThreshold int               `toml:"context_percent_threshold"`
	TurnCountThreshold      int               `toml:"turn_count_threshold"`
	TokenCounter            string            `toml:"token_counter"`
	DigestModel             string            `toml:"digest_model"`
	DigestProvider          string            `toml:"digest_provider"`
	RecallToolEnabled       string            `toml:"recall_tool_enabled"`
	Retention               string            `toml:"retention"`
	BlobThresholdBytes      int               `toml:"blob_threshold_bytes"`
	Calibration             CalibrationConfig `toml:"calibration"`
}

// LSPConfig holds the LSP server configuration. Enabled defaults to true
// when nil (the zero value). Servers is a map of language identifier to
// per-language LSP server config.
type LSPConfig struct {
	Enabled *bool                      `toml:"enabled"` // nil => true
	Servers map[string]LSPServerConfig `toml:"servers"`
}

// LSPServerConfig configures one LSP server for a language.
type LSPServerConfig struct {
	Command  string   `toml:"command"`
	Args     []string `toml:"args"`
	Disabled bool     `toml:"disabled"`
}

// CalibrationConfig controls EstimatorCounter calibration recording.
// When enabled, each turn that reports provider usage also persists a
// paired estimator-vs-provider token observation to the token_calibration
// table, so the estimator's error can be measured later. Disabled by
// default — it is a measurement tool, not a runtime dependency.
type CalibrationConfig struct {
	Enabled bool `toml:"enabled"`
}

// WebConfig gates outbound network access from agent-side tools (web.fetch
// and web.search). It is disabled by default — Marshal is local-first and
// opt-in for anytying that talks to the public internet. Search is further
// conditional on SearchURL being set; web.fetch is available whenever
// Enabled is true.
type WebConfig struct {
	Enabled        bool          `toml:"enabled"`
	FetchTimeout   time.Duration `toml:"fetch_timeout"`
	SearchProvider string        `toml:"search_provider"`
	SearchURL      string        `toml:"search_url"`
	SearchKey      string        `toml:"search_key"`
}

type DesktopConfig struct {
	Enabled          bool          `toml:"enabled"`
	Mode             string        `toml:"mode"`
	Headless         bool          `toml:"headless"`
	CDPURL           string        `toml:"cdp_url"`
	URLAllowlist     []string      `toml:"url_allowlist"`
	URLDenylist      []string      `toml:"url_denylist"`
	DefaultTimeout   time.Duration `toml:"default_timeout"`
	ScreenshotFormat string        `toml:"screenshot_format"`
}

type ShellToolConfig struct {
	DefaultTimeoutSeconds int           `toml:"default_timeout_seconds"`
	MaxOutputBytes        int           `toml:"max_output_bytes"`
	MaxBackgroundJobs     int           `toml:"max_background_jobs"`
	BackgroundRetention   time.Duration `toml:"background_retention"`
	AllowNetwork          bool          `toml:"allow_network"`
	AutoApprove           bool          `toml:"auto_approve"`
	Allow                 CommandRules  `toml:"allow"`
	Confirm               CommandRules  `toml:"confirm"`
	Deny                  PatternRules  `toml:"deny"`
	GuardrailDynamicArgv0 string        `toml:"guardrail_dynamic_argv0"`
	Sandbox               SandboxConfig `toml:"sandbox"`
}

// SandboxConfig selects and tunes the Milestone Q execution backend for
// shell.run / test.run. The default ("restricted") enables in-process
// hardening (env allowlist scrubbing, resource limits via ulimit/rlimit,
// cwd confinement, process-group kill on timeout). "container" requires a
// Docker/Podman runtime and enforces network isolation, filesystem
// isolation, and hard memory/cpu limits; it falls back to "restricted"
// ONLY when AllowFallback=true AND no runtime is detected — otherwise
// startup fails with a clear error so isolation is not silently waived.
// "passthrough" disables all sandboxing (the pre-Milestone-Q behavior).
type SandboxConfig struct {
	Backend          string `toml:"backend"`
	MemoryLimitMB    int    `toml:"memory_limit_mb"`
	CPUSeconds       int    `toml:"cpu_seconds"`
	MaxProcesses     int    `toml:"max_processes"`
	FileSizeLimitMB  int    `toml:"file_size_limit_mb"`
	ContainerRuntime string `toml:"container_runtime"`
	ContainerImage   string `toml:"container_image"`
	// AllowFallback controls whether a configured container backend that
	// cannot be reached (no docker/podman on the host) may silently
	// downgrade to the restricted backend. Default = false: failing to
	// run the requested sandbox is a hard error, because a downgrade
	// silently drops network/filesystem containment the user expected.
	AllowFallback bool `toml:"allow_fallback"`
	// UnsafePassthrough controls whether backend=passthrough may be
	// selected as the execution backend. Default = false: passthrough
	// is a hard error unless explicitly opted-in, because it disables
	// all process isolation (environment scrubbing, cwd confinement,
	// resource limits) that the restricted and container backends
	// provide. Set to true only in controlled, trusted environments.
	UnsafePassthrough bool     `toml:"unsafe_passthrough"`
	EnvAllowlist      []string `toml:"env_allowlist"`
	EnvDenylist       []string `toml:"env_denylist"`
}

type CommandRules struct {
	Commands []string `toml:"commands"`
}

type PatternRules struct {
	Patterns []string `toml:"patterns"`
}

type ProjectConfig struct {
	Name      string   `toml:"name"`
	Languages []string `toml:"languages"`
}

type CommandsConfig struct {
	Test   string `toml:"test"`
	Format string `toml:"format"`
	Vet    string `toml:"vet"`
}

type ProfileConfig struct {
	Default string `toml:"default"`
}

// AgentConfig names which configured provider and model the agent loop
// (Milestone H) uses. Both fields are intentionally blank in Default():
// Marshal is local-first with no built-in provider assumptions (see
// Providers' Default() comment) — the app runs with the agent loop disabled
// until a user configures both a [providers.<name>] entry and this section.
type AgentConfig struct {
	Provider                 string `toml:"provider"`
	Model                    string `toml:"model"`
	MaxToolIterations        int    `toml:"max_tool_iterations"`
	MaxRetries               int    `toml:"max_retries"`
	MaxTurnContextTokens     int    `toml:"max_turn_context_tokens"`
	MaxStructuredOutputChars int    `toml:"max_structured_output_chars"`
	PlanFirst                bool   `toml:"plan_first"`
	// SubtaskIterations caps tool iterations for an ad-hoc agent.run child.
	// The child gets a fresh Runner with this cap (defaults to 12 when zero).
	// A subtask that exhausts its budget is salvaged: the parent receives
	// whatever partial answer the child produced instead of a hard error.
	SubtaskIterations int `toml:"subtask_iterations"`
	// ApprovalMode is the active interaction/approval mode: "plan",
	// "default", "edit", "copilot", or "auto". Default "default". See
	// docs/superpowers/specs/2026-07-24-approval-modes-design.md.
	ApprovalMode string `toml:"approval_mode"`
}

type PrivacyConfig struct {
	RemoteProvidersAllowed bool `toml:"remote_providers_allowed"`
	RemoteLimitDiscovery   bool `toml:"remote_limit_discovery,omitempty"`
	RedactSecrets          bool `toml:"redact_secrets"`
	IncludeGitignoredFiles bool `toml:"include_gitignored_files"`
}

type IndexingConfig struct {
	UseTreesitter          bool     `toml:"use_treesitter"`
	UseEmbeddings          bool     `toml:"use_embeddings"`
	SummariseFiles         bool     `toml:"summarise_files"`
	Ignore                 []string `toml:"ignore"`
	MaxIndexableFileBytes  int64    `toml:"max_indexable_file_bytes"`
	MaxSearchableFileBytes int64    `toml:"max_searchable_file_bytes"`
	Watch                  *bool    `toml:"watch"`             // nil = auto (on iff embedding configured)
	WatchDebounceMs        int      `toml:"watch_debounce_ms"` // default 1000
}

// ProviderConfig is one [providers.<name>] entry. Only the fields needed
// for the generic OpenAI-compatible provider are present.
type ProviderConfig struct {
	Type        string `toml:"type"` // "openai_compatible" (default when empty) or "ollama"; "ollama" selects the native embedding backend and is not a chat provider type
	BaseURL     string `toml:"base_url"`
	APIKey      string `toml:"api_key"`      // literal key; wins over APIKeyEnv if both set
	APIKeyEnv   string `toml:"api_key_env"`  // env var name to resolve at provider-construction time (NOT resolved here)
	ToolCalling bool   `toml:"tool_calling"` // provider advertises native OpenAI-compatible tool-calling support
}

type LoadOptions struct {
	HomeDir       string
	WorkingDir    string
	TrustResolver trust.Resolver
	Trusted       *bool
	// SkipProjectConfig loads defaults + user config only. Used by the
	// interactive TUI's two-phase load: the trust question moves inline, so
	// the project config must not be merged before the answer.
	SkipProjectConfig bool
}
