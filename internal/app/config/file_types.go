package config

import "marshal/internal/llm/routing"

// sandboxFile is the TOML-mirror nullable form of SandboxConfig, used both
// by configFile (load path) and by SaveProjectConfig (save path) so all
// mirror copies share a single definition — adding/renaming a SandboxConfig
// field requires editing just this struct plus SandboxConfig itself, plus
// the merge block.
type sandboxFile struct {
	Backend           *string  `toml:"backend"`
	MemoryLimitMB     *int     `toml:"memory_limit_mb"`
	CPUSeconds        *int     `toml:"cpu_seconds"`
	MaxProcesses      *int     `toml:"max_processes"`
	FileSizeLimitMB   *int     `toml:"file_size_limit_mb"`
	ContainerRuntime  *string  `toml:"container_runtime"`
	ContainerImage    *string  `toml:"container_image"`
	AllowFallback     *bool    `toml:"allow_fallback"`
	UnsafePassthrough *bool    `toml:"unsafe_passthrough"`
	EnvAllowlist      []string `toml:"env_allowlist"`
	EnvDenylist       []string `toml:"env_denylist"`
}

type fileProject struct {
	Name      *string  `toml:"name"`
	Languages []string `toml:"languages"`
}

type fileCommands struct {
	Test   *string `toml:"test"`
	Format *string `toml:"format"`
	Vet    *string `toml:"vet"`
}

type fileProfile struct {
	Default      *string `toml:"default"`
	ActivePreset *string `toml:"active_preset"`
}

type fileAgent struct {
	Provider                 *string `toml:"provider"`
	Model                    *string `toml:"model"`
	MaxToolIterations        *int    `toml:"max_tool_iterations"`
	MaxRetries               *int    `toml:"max_retries"`
	MaxTurnContextTokens     *int    `toml:"max_turn_context_tokens"`
	ReconnectMaxWaitSeconds  *int    `toml:"reconnect_max_wait_seconds"`
	MaxToolResultChars       *int    `toml:"max_tool_result_chars"`
	MaxStructuredOutputChars *int    `toml:"max_structured_output_chars"`
	PlanFirst                *bool   `toml:"plan_first"`
	SubtaskIterations        *int    `toml:"subtask_iterations"`
	ApprovalMode             *string `toml:"approval_mode"`
	HistoryBudgetTokens      *int    `toml:"history_budget_tokens"`
	ParseRepairFeedback      *bool   `toml:"parse_repair_feedback"`
	MaxTouchedFileBytes      *int    `toml:"max_touched_file_bytes"`
}

type filePrivacy struct {
	RemoteProvidersAllowed *bool `toml:"remote_providers_allowed"`
	RemoteLimitDiscovery   *bool `toml:"remote_limit_discovery"`
	RedactSecrets          *bool `toml:"redact_secrets"`
	IncludeGitignoredFiles *bool `toml:"include_gitignored_files"`
}

type fileIndexing struct {
	UseTreesitter          *bool    `toml:"use_treesitter"`
	UseEmbeddings          *bool    `toml:"use_embeddings"`
	SummariseFiles         *bool    `toml:"summarise_files"`
	Ignore                 []string `toml:"ignore"`
	MaxIndexableFileBytes  *int64   `toml:"max_indexable_file_bytes"`
	MaxSearchableFileBytes *int64   `toml:"max_searchable_file_bytes"`
	Watch                  *bool    `toml:"watch"`
	WatchDebounceMs        *int     `toml:"watch_debounce_ms"`
	EmbeddingPreset        *string  `toml:"embedding_preset"`
}

type fileSkills struct {
	Autoload  []string `toml:"autoload"`
	MaxActive *int     `toml:"max_active"`
}

type fileShell struct {
	DefaultTimeoutSeconds *int          `toml:"default_timeout_seconds"`
	MaxOutputBytes        *int          `toml:"max_output_bytes"`
	MaxBackgroundJobs     *int          `toml:"max_background_jobs"`
	BackgroundRetention   *string       `toml:"background_retention"`
	AllowNetwork          *bool         `toml:"allow_network"`
	AutoApprove           *bool         `toml:"auto_approve"`
	GuardrailDynamicArgv0 *string       `toml:"guardrail_dynamic_argv0"`
	Allow                 *CommandRules `toml:"allow"`
	Confirm               *CommandRules `toml:"confirm"`
	Deny                  *PatternRules `toml:"deny"`
	Sandbox               *sandboxFile  `toml:"sandbox"`
}

type fileTools struct {
	Shell *fileShell `toml:"shell"`
}

type fileWeb struct {
	Enabled        *bool   `toml:"enabled"`
	FetchTimeout   *string `toml:"fetch_timeout"`
	SearchProvider *string `toml:"search_provider"`
	SearchURL      *string `toml:"search_url"`
	SearchKey      *string `toml:"search_key"`
}

type fileDesktop struct {
	Enabled          *bool    `toml:"enabled"`
	Mode             *string  `toml:"mode"`
	Headless         *bool    `toml:"headless"`
	CDPURL           *string  `toml:"cdp_url"`
	URLAllowlist     []string `toml:"url_allowlist"`
	URLDenylist      []string `toml:"url_denylist"`
	DefaultTimeout   *string  `toml:"default_timeout"`
	ScreenshotFormat *string  `toml:"screenshot_format"`
}

type fileSwarmBudget struct {
	MaxFixRounds   *int           `toml:"max_fix_rounds"`
	MaxTotalTokens *int           `toml:"max_total_tokens"`
	ToolIters      map[string]int `toml:"tool_iters"`
}

type fileSwarm struct {
	Budget *fileSwarmBudget `toml:"budget"`
}

type fileSDD struct {
	AutoWorktree    *bool          `toml:"auto_worktree"`
	MaxFixRounds    *int           `toml:"max_fix_rounds"`
	DispatchRetries *int           `toml:"dispatch_retries"`
	PlansDir        *string        `toml:"plans_dir"`
	VerifyTimeoutMS *int           `toml:"verify_timeout_ms"`
	CleanupAtStart  *bool          `toml:"cleanup_at_start"`
	MaxTotalTokens  *int           `toml:"max_total_tokens"`
	Verify          *fileSDDVerify `toml:"verify"`
}

type fileSDDVerify struct {
	Build *string `toml:"build"`
	Test  *string `toml:"test"`
}

type fileMCPServer struct {
	Command *string           `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	Trust   *string           `toml:"trust"`
}

type fileMCP struct {
	Servers                  map[string]fileMCPServer `toml:"servers"`
	Policies                 map[string]string        `toml:"policies"`
	DisclosureThresholdTools *int                     `toml:"disclosure_threshold_tools"`
}

type fileSnapshots struct {
	Enabled       *bool `toml:"enabled"`
	RetentionDays *int  `toml:"retention_days"`
	MaxFileBytes  *int  `toml:"max_file_bytes"`
}

type fileHistory struct {
	Enabled *bool `toml:"enabled"`
}

type fileTUI struct {
	Theme        *string           `toml:"theme"`
	Palette      map[string]string `toml:"palette"`
	Mode         *string           `toml:"mode"`
	MouseCapture *bool             `toml:"mouse_capture"`
	SidePanel    *fileSidePanel    `toml:"side_panel"`
	Suggestions  *string           `toml:"suggestions"`
}

type fileSidePanel struct {
	Enabled  *bool    `toml:"enabled"`
	MinWidth *int     `toml:"min_width"`
	WidthPct *int     `toml:"width_pct"`
	MinCols  *int     `toml:"min_cols"`
	MaxCols  *int     `toml:"max_cols"`
	Hidden   []string `toml:"hidden"`
}

type filePermissions struct {
	Rules []PermissionRule `toml:"rules"`
}

type fileDiagnostics struct {
	Commands map[string]string `toml:"commands"`
}

type fileHookEntry struct {
	Event     *string `toml:"event"`
	Matcher   *string `toml:"matcher"`
	Command   *string `toml:"command"`
	TimeoutMS *int    `toml:"timeout_ms"`
}

type fileHooks struct {
	FailClosed *bool           `toml:"fail_closed"`
	Entries    []fileHookEntry `toml:"entries"`
}

type fileModels struct {
	Presets map[string]routing.ModelPreset `toml:"presets"`
}

type fileAgentEntry struct {
	Context routing.ContextBudget `toml:"context"`
}

type fileRollover struct {
	Enabled                 *bool            `toml:"enabled"`
	Policy                  *string          `toml:"policy"`
	ContextPercentThreshold *int             `toml:"context_percent_threshold"`
	TurnCountThreshold      *int             `toml:"turn_count_threshold"`
	TokenCounter            *string          `toml:"token_counter"`
	DigestModel             *string          `toml:"digest_model"`
	DigestProvider          *string          `toml:"digest_provider"`
	RecallToolEnabled       *string          `toml:"recall_tool_enabled"`
	Retention               *string          `toml:"retention"`
	BlobThresholdBytes      *int             `toml:"blob_threshold_bytes"`
	Calibration             *fileCalibration `toml:"calibration"`
}

type fileCalibration struct {
	Enabled *bool `toml:"enabled"`
}

type fileSession struct {
	Rollover *fileRollover `toml:"rollover"`
}

type fileLSPServer struct {
	Command  *string  `toml:"command"`
	Args     []string `toml:"args"`
	Disabled *bool    `toml:"disabled"`
}

type fileLSP struct {
	Enabled *bool                    `toml:"enabled"`
	Servers map[string]fileLSPServer `toml:"servers"`
}

type fileScratchpad struct {
	MaxEntries          *int `toml:"max_entries"`
	MaxTotalTokens      *int `toml:"max_total_tokens"`
	MaxEntryTokens      *int `toml:"max_entry_tokens"`
	ProjectionMaxTokens *int `toml:"projection_max_tokens"`
}

type configFile struct {
	Project     *fileProject     `toml:"project"`
	Commands    *fileCommands    `toml:"commands"`
	Profile     *fileProfile     `toml:"profile"`
	Agent       *fileAgent       `toml:"agent"`
	Privacy     *filePrivacy     `toml:"privacy"`
	Indexing    *fileIndexing    `toml:"indexing"`
	Tools       *fileTools       `toml:"tools"`
	Web         *fileWeb         `toml:"web"`
	Desktop     *fileDesktop     `toml:"desktop"`
	Swarm       *fileSwarm       `toml:"swarm"`
	SDD         *fileSDD         `toml:"sdd"`
	MCP         *fileMCP         `toml:"mcp"`
	Snapshots   *fileSnapshots   `toml:"snapshots"`
	History     *fileHistory     `toml:"history"`
	TUI         *fileTUI         `toml:"tui"`
	Permissions *filePermissions `toml:"permissions"`
	Diagnostics *fileDiagnostics `toml:"diagnostics"`
	Hooks       *fileHooks       `toml:"hooks"`
	Session     *fileSession     `toml:"session"`
	Skills      *fileSkills      `toml:"skills"`
	// Providers stays a plain map: nil already distinguishes absent/present.
	Providers map[string]ProviderConfig `toml:"providers"`
	Models    *fileModels               `toml:"models"`
	// AgentProfilesRaw is decoded as raw tables so bare-string values
	// (e.g. planner = "reasoning") are preserved as strings rather than
	// failing to decode into RoleBinding. The conversion to typed
	// RoleBinding values happens in merge().
	AgentProfilesRaw map[string]map[string]any `toml:"agent_profiles"`
	// CustomAgents mirrors config.CustomAgents for the file layer.
	CustomAgents map[string]routing.CustomAgent       `toml:"custom_agents"`
	Agents       map[routing.AgentRole]fileAgentEntry `toml:"agents"`
	LSP          *fileLSP                             `toml:"lsp"`
	Scratchpad   *fileScratchpad                      `toml:"scratchpad"`
}
