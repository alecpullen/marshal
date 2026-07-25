package native

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// newConfigToolSet builds a minimal toolSet view for the config tools. It
// copies only the fields the config tools need from the parent toolSet so
// tests can construct one without the full native Options surface.
func newConfigToolSet(t toolSet) (toolSet, error) {
	// Allow tests that set only config (no paths) to proceed; the read
	// tool needs no paths. Write tools check paths at call time.
	return t, nil
}

// configTools returns the full set of config.* tools. Built up across tasks;
// this initial version returns only config.read. Later tasks append the
// section write tools.
func (t *toolSet) configTools() []registry.Tool {
	return []registry.Tool{
		t.configReadTool(),
		t.configAgentSetTool(),
		t.configPrivacySetTool(),
		t.configIndexingSetTool(),
		t.configWebSetTool(),
		t.configDesktopSetTool(),
		t.configSwarmSetTool(),
		t.configSDDSetTool(),
		t.configSnapshotsSetTool(),
		t.configTUISetTool(),
		t.configSessionRolloverSetTool(),
		t.configLSPSetTool(),
		t.configProjectSetTool(),
		t.configCommandsSetTool(),
		t.configProfileSetTool(),
		t.configToolsShellSetTool(),
		t.configToolsShellSandboxSetTool(),
		t.configDiagnosticsSetTool(),
		t.configHooksSetTool(),
	}
}

// configWriteEnvelope is the common shape every section write tool's args
// embed: the scope selector. Section tools define their own struct with the
// nullable fields plus this envelope via anonymous embedding.
type configWriteEnvelope struct {
	Scope string `json:"scope"` // "project" (default) | "global"
}

func (e configWriteEnvelope) resolvedScope() string {
	if e.Scope == "global" {
		return "global"
	}
	return "project"
}

// requestConfigApproval posts a forced approval prompt that bypasses the
// policy mode-transform (so it prompts even in auto mode), reusing the
// mode.request PendingToolCall pattern. Returns whether the user approved.
func (t *toolSet) requestConfigApproval(ctx context.Context, reason string) (bool, error) {
	if t.sessionState == nil {
		return false, fmt.Errorf("session state not available — cannot request approval")
	}
	ch := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{
		ID:           fmt.Sprintf("config_approve_%d", time.Now().UnixNano()),
		Name:         "config.write",
		Reason:       reason,
		Schema:       "Configuration change approval",
		ResponseChan: ch,
	}
	t.sessionState.SetPendingApproval(pending)
	select {
	case decision := <-ch:
		t.sessionState.SetPendingApproval(nil)
		return decision.Approved, nil
	case <-ctx.Done():
		t.sessionState.SetPendingApproval(nil)
		return false, ctx.Err()
	}
}

// commitConfigWrite is the shared write flow used by every section tool.
// mutate applies the section update onto a copy of the current merged config.
// When scope is "global" or destructive is true, a forced approval is
// requested (bypassing auto mode) with the given reason. On approval (or when
// no approval is needed) it persists and hot-reloads.
func (t *toolSet) commitConfigWrite(ctx context.Context, scope, reason string, destructive bool, mutate func(cfg *config.Config)) (registry.ToolResult, error) {
	if t.configReloader == nil {
		return registry.ToolResult{}, fmt.Errorf("config reloader not available")
	}
	needsApproval := scope == "global" || destructive
	if needsApproval {
		approved, err := t.requestConfigApproval(ctx, reason)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if !approved {
			return registry.ToolResult{
				Summary: "denied — configuration change not applied",
				Content: "The user denied the configuration change. Do not retry without asking.",
			}, nil
		}
	}

	next := t.config
	mutate(&next)

	var saveErr error
	if scope == "global" {
		if t.userConfigPath == "" {
			return registry.ToolResult{}, fmt.Errorf("global config path not configured")
		}
		saveErr = config.SaveUserConfigSection(t.userConfigPath, next)
	} else {
		if t.configPath == "" {
			return registry.ToolResult{}, fmt.Errorf("project config path not configured")
		}
		saveErr = config.SaveProjectConfig(t.configPath, next)
	}
	if saveErr != nil {
		return registry.ToolResult{}, fmt.Errorf("save config: %w", saveErr)
	}

	if err := t.configReloader(next); err != nil {
		// Per spec: do not roll back disk; surface the error so the agent
		// can fix the mistake.
		return registry.ToolResult{
			Summary: fmt.Sprintf("saved to disk but reload failed: %v", err),
			Content: fmt.Sprintf("The config was written to disk but the running agent runtime could not be reloaded: %v. Fix the invalid value with another config write.", err),
		}, nil
	}
	t.config = next
	return registry.ToolResult{
		Summary: reason + " — saved and reloaded",
		Content: reason + ". The change has been persisted and the agent runtime reloaded. Call config.read to verify.",
	}, nil
}

func (t *toolSet) configAgentSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.agent.set",
		Description: "Set fields in the [agent] section of the Marshal config (provider, model, max_tool_iterations, max_retries, max_turn_context_tokens, max_structured_output_chars, plan_first, subtask_iterations, approval_mode). Omitted fields are preserved. Use scope=\"global\" to write to the user-global config (requires explicit approval).",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"],"description":"project (default) or global"},"provider":{"type":"string"},"model":{"type":"string"},"max_tool_iterations":{"type":"integer"},"max_retries":{"type":"integer"},"max_turn_context_tokens":{"type":"integer"},"max_structured_output_chars":{"type":"integer"},"plan_first":{"type":"boolean"},"subtask_iterations":{"type":"integer"},"approval_mode":{"type":"string"}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Provider                 *string `json:"provider"`
			Model                    *string `json:"model"`
			MaxToolIterations        *int    `json:"max_tool_iterations"`
			MaxRetries               *int    `json:"max_retries"`
			MaxTurnContextTokens     *int    `json:"max_turn_context_tokens"`
			MaxStructuredOutputChars *int    `json:"max_structured_output_chars"`
			PlanFirst                *bool   `json:"plan_first"`
			SubtaskIterations        *int    `json:"subtask_iterations"`
			ApprovalMode             *string `json:"approval_mode"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.agent.set args: %w", err)
		}
		scope := args.resolvedScope()
		// Build a diff summary for the approval reason.
		reason := fmt.Sprintf("config.agent.set (%s scope): update agent section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.Provider != nil {
				cfg.Agent.Provider = *args.Provider
			}
			if args.Model != nil {
				cfg.Agent.Model = *args.Model
			}
			if args.MaxToolIterations != nil {
				cfg.Agent.MaxToolIterations = *args.MaxToolIterations
			}
			if args.MaxRetries != nil {
				cfg.Agent.MaxRetries = *args.MaxRetries
			}
			if args.MaxTurnContextTokens != nil {
				cfg.Agent.MaxTurnContextTokens = *args.MaxTurnContextTokens
			}
			if args.MaxStructuredOutputChars != nil {
				cfg.Agent.MaxStructuredOutputChars = *args.MaxStructuredOutputChars
			}
			if args.PlanFirst != nil {
				cfg.Agent.PlanFirst = *args.PlanFirst
			}
			if args.SubtaskIterations != nil {
				cfg.Agent.SubtaskIterations = *args.SubtaskIterations
			}
			if args.ApprovalMode != nil {
				cfg.Agent.ApprovalMode = *args.ApprovalMode
			}
		})
	}
	return tool
}

func (t *toolSet) configPrivacySetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.privacy.set",
		Description: "Set fields in the [privacy] section (remote_providers_allowed, redact_secrets, include_gitignored_files). remote_providers_allowed=true escalates to a destructive (forced-approval) change. Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"remote_providers_allowed":{"type":"boolean"},"redact_secrets":{"type":"boolean"},"include_gitignored_files":{"type":"boolean"}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			RemoteProvidersAllowed *bool `json:"remote_providers_allowed"`
			RedactSecrets          *bool `json:"redact_secrets"`
			IncludeGitignoredFiles *bool `json:"include_gitignored_files"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.privacy.set args: %w", err)
		}
		scope := args.resolvedScope()
		destructive := args.RemoteProvidersAllowed != nil && *args.RemoteProvidersAllowed
		reason := fmt.Sprintf("config.privacy.set (%s scope): update privacy section", scope)
		return t.commitConfigWrite(ctx, scope, reason, destructive, func(cfg *config.Config) {
			if args.RemoteProvidersAllowed != nil {
				cfg.Privacy.RemoteProvidersAllowed = *args.RemoteProvidersAllowed
			}
			if args.RedactSecrets != nil {
				cfg.Privacy.RedactSecrets = *args.RedactSecrets
			}
			if args.IncludeGitignoredFiles != nil {
				cfg.Privacy.IncludeGitignoredFiles = *args.IncludeGitignoredFiles
			}
		})
	}
	return tool
}

func (t *toolSet) configIndexingSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.indexing.set",
		Description: "Set fields in the [indexing] section (use_treesitter, use_embeddings, summarise_files, ignore, max_indexable_file_bytes, max_searchable_file_bytes, watch, watch_debounce_ms). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"use_treesitter":{"type":"boolean"},"use_embeddings":{"type":"boolean"},"summarise_files":{"type":"boolean"},"ignore":{"type":"array","items":{"type":"string"}},"max_indexable_file_bytes":{"type":"integer"},"max_searchable_file_bytes":{"type":"integer"},"watch":{"type":"boolean"},"watch_debounce_ms":{"type":"integer"}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			UseTreesitter          *bool    `json:"use_treesitter"`
			UseEmbeddings          *bool    `json:"use_embeddings"`
			SummariseFiles         *bool    `json:"summarise_files"`
			Ignore                 []string `json:"ignore"`
			MaxIndexableFileBytes  *int64   `json:"max_indexable_file_bytes"`
			MaxSearchableFileBytes *int64   `json:"max_searchable_file_bytes"`
			Watch                  *bool    `json:"watch"`
			WatchDebounceMs        *int     `json:"watch_debounce_ms"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.indexing.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.indexing.set (%s scope): update indexing section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.UseTreesitter != nil {
				cfg.Indexing.UseTreesitter = *args.UseTreesitter
			}
			if args.UseEmbeddings != nil {
				cfg.Indexing.UseEmbeddings = *args.UseEmbeddings
			}
			if args.SummariseFiles != nil {
				cfg.Indexing.SummariseFiles = *args.SummariseFiles
			}
			if args.Ignore != nil {
				cfg.Indexing.Ignore = args.Ignore
			}
			if args.MaxIndexableFileBytes != nil {
				cfg.Indexing.MaxIndexableFileBytes = *args.MaxIndexableFileBytes
			}
			if args.MaxSearchableFileBytes != nil {
				cfg.Indexing.MaxSearchableFileBytes = *args.MaxSearchableFileBytes
			}
			if args.Watch != nil {
				cfg.Indexing.Watch = args.Watch
			}
			if args.WatchDebounceMs != nil {
				cfg.Indexing.WatchDebounceMs = *args.WatchDebounceMs
			}
		})
	}
	return tool
}

func (t *toolSet) configWebSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.web.set",
		Description: "Set fields in the [web] section (enabled, fetch_timeout, search_provider, search_url, search_key). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"enabled":{"type":"boolean"},"fetch_timeout":{"type":"string"},"search_provider":{"type":"string"},"search_url":{"type":"string"},"search_key":{"type":"string"}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Enabled        *bool   `json:"enabled"`
			FetchTimeout   *string `json:"fetch_timeout"`
			SearchProvider *string `json:"search_provider"`
			SearchURL      *string `json:"search_url"`
			SearchKey      *string `json:"search_key"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.web.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.web.set (%s scope): update web section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.Enabled != nil {
				cfg.Web.Enabled = *args.Enabled
			}
			if args.FetchTimeout != nil {
				d, err := time.ParseDuration(*args.FetchTimeout)
				if err == nil {
					cfg.Web.FetchTimeout = d
				}
			}
			if args.SearchProvider != nil {
				cfg.Web.SearchProvider = *args.SearchProvider
			}
			if args.SearchURL != nil {
				cfg.Web.SearchURL = *args.SearchURL
			}
			if args.SearchKey != nil {
				cfg.Web.SearchKey = *args.SearchKey
			}
		})
	}
	return tool
}

func (t *toolSet) configDesktopSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.desktop.set",
		Description: "Set fields in the [desktop] section (enabled, mode, headless, cdp_url, url_allowlist, url_denylist, default_timeout, screenshot_format). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"enabled":{"type":"boolean"},"mode":{"type":"string"},"headless":{"type":"boolean"},"cdp_url":{"type":"string"},"url_allowlist":{"type":"array","items":{"type":"string"}},"url_denylist":{"type":"array","items":{"type":"string"}},"default_timeout":{"type":"string"},"screenshot_format":{"type":"string"}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Enabled          *bool    `json:"enabled"`
			Mode             *string  `json:"mode"`
			Headless         *bool    `json:"headless"`
			CDPURL           *string  `json:"cdp_url"`
			URLAllowlist     []string `json:"url_allowlist"`
			URLDenylist      []string `json:"url_denylist"`
			DefaultTimeout   *string  `json:"default_timeout"`
			ScreenshotFormat *string  `json:"screenshot_format"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.desktop.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.desktop.set (%s scope): update desktop section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.Enabled != nil {
				cfg.Desktop.Enabled = *args.Enabled
			}
			if args.Mode != nil {
				cfg.Desktop.Mode = *args.Mode
			}
			if args.Headless != nil {
				cfg.Desktop.Headless = *args.Headless
			}
			if args.CDPURL != nil {
				cfg.Desktop.CDPURL = *args.CDPURL
			}
			if args.URLAllowlist != nil {
				cfg.Desktop.URLAllowlist = args.URLAllowlist
			}
			if args.URLDenylist != nil {
				cfg.Desktop.URLDenylist = args.URLDenylist
			}
			if args.DefaultTimeout != nil {
				d, err := time.ParseDuration(*args.DefaultTimeout)
				if err == nil {
					cfg.Desktop.DefaultTimeout = d
				}
			}
			if args.ScreenshotFormat != nil {
				cfg.Desktop.ScreenshotFormat = *args.ScreenshotFormat
			}
		})
	}
	return tool
}

func (t *toolSet) configSwarmSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.swarm.set",
		Description: "Set fields in the [swarm] section (budget.max_fix_rounds, budget.max_total_tokens, budget.tool_iters). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"budget":{"type":"object","properties":{"max_fix_rounds":{"type":"integer"},"max_total_tokens":{"type":"integer"},"tool_iters":{"type":"object","additionalProperties":{"type":"integer"}}},"additionalProperties":false}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Budget *struct {
				MaxFixRounds   *int           `json:"max_fix_rounds"`
				MaxTotalTokens *int           `json:"max_total_tokens"`
				ToolIters      map[string]int `json:"tool_iters"`
			} `json:"budget"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.swarm.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.swarm.set (%s scope): update swarm section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.Budget != nil {
				if args.Budget.MaxFixRounds != nil {
					cfg.Swarm.Budget.MaxFixRounds = *args.Budget.MaxFixRounds
				}
				if args.Budget.MaxTotalTokens != nil {
					cfg.Swarm.Budget.MaxTotalTokens = *args.Budget.MaxTotalTokens
				}
				if args.Budget.ToolIters != nil {
					if cfg.Swarm.Budget.ToolIters == nil {
						cfg.Swarm.Budget.ToolIters = make(map[string]int)
					}
					for k, v := range args.Budget.ToolIters {
						cfg.Swarm.Budget.ToolIters[k] = v
					}
				}
			}
		})
	}
	return tool
}

func (t *toolSet) configSDDSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.sdd.set",
		Description: "Set fields in the [sdd] section (auto_worktree, max_fix_rounds, plans_dir). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"auto_worktree":{"type":"boolean"},"max_fix_rounds":{"type":"integer"},"plans_dir":{"type":"string"}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			AutoWorktree *bool   `json:"auto_worktree"`
			MaxFixRounds *int    `json:"max_fix_rounds"`
			PlansDir     *string `json:"plans_dir"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.sdd.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.sdd.set (%s scope): update sdd section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.AutoWorktree != nil {
				cfg.SDD.AutoWorktree = *args.AutoWorktree
			}
			if args.MaxFixRounds != nil {
				cfg.SDD.MaxFixRounds = *args.MaxFixRounds
			}
			if args.PlansDir != nil {
				cfg.SDD.PlansDir = *args.PlansDir
			}
		})
	}
	return tool
}

func (t *toolSet) configSnapshotsSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.snapshots.set",
		Description: "Set fields in the [snapshots] section (enabled, retention_days, max_file_bytes). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"enabled":{"type":"boolean"},"retention_days":{"type":"integer"},"max_file_bytes":{"type":"integer"}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Enabled       *bool `json:"enabled"`
			RetentionDays *int  `json:"retention_days"`
			MaxFileBytes  *int  `json:"max_file_bytes"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.snapshots.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.snapshots.set (%s scope): update snapshots section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.Enabled != nil {
				cfg.Snapshots.Enabled = *args.Enabled
			}
			if args.RetentionDays != nil {
				cfg.Snapshots.RetentionDays = *args.RetentionDays
			}
			if args.MaxFileBytes != nil {
				cfg.Snapshots.MaxFileBytes = *args.MaxFileBytes
			}
		})
	}
	return tool
}

func (t *toolSet) configTUISetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.tui.set",
		Description: "Set fields in the [tui] section (theme, palette, mode). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"theme":{"type":"string"},"palette":{"type":"object","additionalProperties":{"type":"string"}},"mode":{"type":"string"}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Theme   *string           `json:"theme"`
			Palette map[string]string `json:"palette"`
			Mode    *string           `json:"mode"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.tui.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.tui.set (%s scope): update tui section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.Theme != nil {
				cfg.TUI.Theme = *args.Theme
			}
			if args.Palette != nil {
				if cfg.TUI.Palette == nil {
					cfg.TUI.Palette = make(map[string]string)
				}
				for k, v := range args.Palette {
					cfg.TUI.Palette[k] = v
				}
			}
			if args.Mode != nil {
				cfg.TUI.Mode = *args.Mode
			}
		})
	}
	return tool
}

func (t *toolSet) configSessionRolloverSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.session.rollover.set",
		Description: "Set fields in the [session.rollover] section (enabled, policy, context_percent_threshold, turn_count_threshold, token_counter, digest_model, digest_provider, recall_tool_enabled, retention, blob_threshold_bytes, calibration.enabled). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"enabled":{"type":"boolean"},"policy":{"type":"string"},"context_percent_threshold":{"type":"integer"},"turn_count_threshold":{"type":"integer"},"token_counter":{"type":"string"},"digest_model":{"type":"string"},"digest_provider":{"type":"string"},"recall_tool_enabled":{"type":"string"},"retention":{"type":"string"},"blob_threshold_bytes":{"type":"integer"},"calibration":{"type":"object","properties":{"enabled":{"type":"boolean"}},"additionalProperties":false}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Enabled                 *bool   `json:"enabled"`
			Policy                  *string `json:"policy"`
			ContextPercentThreshold *int    `json:"context_percent_threshold"`
			TurnCountThreshold      *int    `json:"turn_count_threshold"`
			TokenCounter            *string `json:"token_counter"`
			DigestModel             *string `json:"digest_model"`
			DigestProvider          *string `json:"digest_provider"`
			RecallToolEnabled       *string `json:"recall_tool_enabled"`
			Retention               *string `json:"retention"`
			BlobThresholdBytes      *int    `json:"blob_threshold_bytes"`
			Calibration             *struct {
				Enabled *bool `json:"enabled"`
			} `json:"calibration"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.session.rollover.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.session.rollover.set (%s scope): update session.rollover section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.Enabled != nil {
				cfg.Session.Rollover.Enabled = *args.Enabled
			}
			if args.Policy != nil {
				cfg.Session.Rollover.Policy = *args.Policy
			}
			if args.ContextPercentThreshold != nil {
				cfg.Session.Rollover.ContextPercentThreshold = *args.ContextPercentThreshold
			}
			if args.TurnCountThreshold != nil {
				cfg.Session.Rollover.TurnCountThreshold = *args.TurnCountThreshold
			}
			if args.TokenCounter != nil {
				cfg.Session.Rollover.TokenCounter = *args.TokenCounter
			}
			if args.DigestModel != nil {
				cfg.Session.Rollover.DigestModel = *args.DigestModel
			}
			if args.DigestProvider != nil {
				cfg.Session.Rollover.DigestProvider = *args.DigestProvider
			}
			if args.RecallToolEnabled != nil {
				cfg.Session.Rollover.RecallToolEnabled = *args.RecallToolEnabled
			}
			if args.Retention != nil {
				cfg.Session.Rollover.Retention = *args.Retention
			}
			if args.BlobThresholdBytes != nil {
				cfg.Session.Rollover.BlobThresholdBytes = *args.BlobThresholdBytes
			}
			if args.Calibration != nil && args.Calibration.Enabled != nil {
				cfg.Session.Rollover.Calibration.Enabled = *args.Calibration.Enabled
			}
		})
	}
	return tool
}

func (t *toolSet) configLSPSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.lsp.set",
		Description: "Set fields in the [lsp] section (enabled, servers). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"enabled":{"type":"boolean"},"servers":{"type":"object","additionalProperties":{"type":"object","properties":{"command":{"type":"string"},"args":{"type":"array","items":{"type":"string"}},"disabled":{"type":"boolean"}},"additionalProperties":false}}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Enabled *bool                             `json:"enabled"`
			Servers map[string]config.LSPServerConfig `json:"servers"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.lsp.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.lsp.set (%s scope): update lsp section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.Enabled != nil {
				cfg.LSP.Enabled = args.Enabled
			}
			if args.Servers != nil {
				if cfg.LSP.Servers == nil {
					cfg.LSP.Servers = make(map[string]config.LSPServerConfig)
				}
				for k, v := range args.Servers {
					cfg.LSP.Servers[k] = v
				}
			}
		})
	}
	return tool
}

func (t *toolSet) configProjectSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.project.set",
		Description: "Set fields in the [project] section (name, languages). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"name":{"type":"string"},"languages":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Name      *string  `json:"name"`
			Languages []string `json:"languages"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.project.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.project.set (%s scope): update project section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.Name != nil {
				cfg.Project.Name = *args.Name
			}
			if args.Languages != nil {
				cfg.Project.Languages = args.Languages
			}
		})
	}
	return tool
}

func (t *toolSet) configCommandsSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.commands.set",
		Description: "Set fields in the [commands] section (test, format, vet). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"test":{"type":"string"},"format":{"type":"string"},"vet":{"type":"string"}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Test   *string `json:"test"`
			Format *string `json:"format"`
			Vet    *string `json:"vet"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.commands.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.commands.set (%s scope): update commands section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.Test != nil {
				cfg.Commands.Test = *args.Test
			}
			if args.Format != nil {
				cfg.Commands.Format = *args.Format
			}
			if args.Vet != nil {
				cfg.Commands.Vet = *args.Vet
			}
		})
	}
	return tool
}

func (t *toolSet) configProfileSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.profile.set",
		Description: "Set fields in the [profile] section (default). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"default":{"type":"string"}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Default *string `json:"default"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.profile.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.profile.set (%s scope): update profile section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.Default != nil {
				cfg.Profile.Default = *args.Default
			}
		})
	}
	return tool
}

func (t *toolSet) configToolsShellSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.tools.shell.set",
		Description: "Set fields in the [tools.shell] section (default_timeout_seconds, max_output_bytes, max_background_jobs, background_retention, allow_network, auto_approve, allow, confirm, deny, guardrail_dynamic_argv0). auto_approve=true escalates to a destructive (forced-approval) change. Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"default_timeout_seconds":{"type":"integer"},"max_output_bytes":{"type":"integer"},"max_background_jobs":{"type":"integer"},"background_retention":{"type":"string"},"allow_network":{"type":"boolean"},"auto_approve":{"type":"boolean"},"allow":{"type":"object","properties":{"commands":{"type":"array","items":{"type":"string"}}},"additionalProperties":false},"confirm":{"type":"object","properties":{"commands":{"type":"array","items":{"type":"string"}}},"additionalProperties":false},"deny":{"type":"object","properties":{"patterns":{"type":"array","items":{"type":"string"}}},"additionalProperties":false},"guardrail_dynamic_argv0":{"type":"string"}},"additionalProperties":false}`),
		Risk:        registry.RiskCommand,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			DefaultTimeoutSeconds *int                 `json:"default_timeout_seconds"`
			MaxOutputBytes        *int                 `json:"max_output_bytes"`
			MaxBackgroundJobs     *int                 `json:"max_background_jobs"`
			BackgroundRetention   *string              `json:"background_retention"`
			AllowNetwork          *bool                `json:"allow_network"`
			AutoApprove           *bool                `json:"auto_approve"`
			Allow                 *config.CommandRules `json:"allow"`
			Confirm               *config.CommandRules `json:"confirm"`
			Deny                  *config.PatternRules `json:"deny"`
			GuardrailDynamicArgv0 *string              `json:"guardrail_dynamic_argv0"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.tools.shell.set args: %w", err)
		}
		// Pre-validate background_retention so we can return a parse error
		// before entering the mutate closure (which cannot return errors).
		if args.BackgroundRetention != nil && *args.BackgroundRetention != "" {
			if _, err := time.ParseDuration(*args.BackgroundRetention); err != nil {
				return registry.ToolResult{}, fmt.Errorf("parse background_retention: %w", err)
			}
		}
		scope := args.resolvedScope()
		destructive := args.AutoApprove != nil && *args.AutoApprove
		reason := fmt.Sprintf("config.tools.shell.set (%s scope): update tools.shell section", scope)
		return t.commitConfigWrite(ctx, scope, reason, destructive, func(cfg *config.Config) {
			if args.DefaultTimeoutSeconds != nil {
				cfg.Tools.Shell.DefaultTimeoutSeconds = *args.DefaultTimeoutSeconds
			}
			if args.MaxOutputBytes != nil {
				cfg.Tools.Shell.MaxOutputBytes = *args.MaxOutputBytes
			}
			if args.MaxBackgroundJobs != nil {
				cfg.Tools.Shell.MaxBackgroundJobs = *args.MaxBackgroundJobs
			}
			if args.BackgroundRetention != nil && *args.BackgroundRetention != "" {
				d, _ := time.ParseDuration(*args.BackgroundRetention) // already validated above
				cfg.Tools.Shell.BackgroundRetention = d
			}
			if args.AllowNetwork != nil {
				cfg.Tools.Shell.AllowNetwork = *args.AllowNetwork
			}
			if args.AutoApprove != nil {
				cfg.Tools.Shell.AutoApprove = *args.AutoApprove
			}
			if args.Allow != nil {
				cfg.Tools.Shell.Allow = *args.Allow
			}
			if args.Confirm != nil {
				cfg.Tools.Shell.Confirm = *args.Confirm
			}
			if args.Deny != nil {
				cfg.Tools.Shell.Deny = *args.Deny
			}
			if args.GuardrailDynamicArgv0 != nil {
				cfg.Tools.Shell.GuardrailDynamicArgv0 = *args.GuardrailDynamicArgv0
			}
		})
	}
	return tool
}

func (t *toolSet) configToolsShellSandboxSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.tools.shell.sandbox.set",
		Description: "Set fields in the [tools.shell.sandbox] section (backend, memory_limit_mb, cpu_seconds, max_processes, file_size_limit_mb, container_runtime, container_image, allow_fallback, unsafe_passthrough, env_allowlist, env_denylist). unsafe_passthrough=true escalates to a destructive (forced-approval) change. Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"backend":{"type":"string"},"memory_limit_mb":{"type":"integer"},"cpu_seconds":{"type":"integer"},"max_processes":{"type":"integer"},"file_size_limit_mb":{"type":"integer"},"container_runtime":{"type":"string"},"container_image":{"type":"string"},"allow_fallback":{"type":"boolean"},"unsafe_passthrough":{"type":"boolean"},"env_allowlist":{"type":"array","items":{"type":"string"}},"env_denylist":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}`),
		Risk:        registry.RiskCommand,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Backend           *string  `json:"backend"`
			MemoryLimitMB     *int     `json:"memory_limit_mb"`
			CPUSeconds        *int     `json:"cpu_seconds"`
			MaxProcesses      *int     `json:"max_processes"`
			FileSizeLimitMB   *int     `json:"file_size_limit_mb"`
			ContainerRuntime  *string  `json:"container_runtime"`
			ContainerImage    *string  `json:"container_image"`
			AllowFallback     *bool    `json:"allow_fallback"`
			UnsafePassthrough *bool    `json:"unsafe_passthrough"`
			EnvAllowlist      []string `json:"env_allowlist"`
			EnvDenylist       []string `json:"env_denylist"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.tools.shell.sandbox.set args: %w", err)
		}
		scope := args.resolvedScope()
		destructive := args.UnsafePassthrough != nil && *args.UnsafePassthrough
		reason := fmt.Sprintf("config.tools.shell.sandbox.set (%s scope): update tools.shell.sandbox section", scope)
		return t.commitConfigWrite(ctx, scope, reason, destructive, func(cfg *config.Config) {
			if args.Backend != nil {
				cfg.Tools.Shell.Sandbox.Backend = *args.Backend
			}
			if args.MemoryLimitMB != nil {
				cfg.Tools.Shell.Sandbox.MemoryLimitMB = *args.MemoryLimitMB
			}
			if args.CPUSeconds != nil {
				cfg.Tools.Shell.Sandbox.CPUSeconds = *args.CPUSeconds
			}
			if args.MaxProcesses != nil {
				cfg.Tools.Shell.Sandbox.MaxProcesses = *args.MaxProcesses
			}
			if args.FileSizeLimitMB != nil {
				cfg.Tools.Shell.Sandbox.FileSizeLimitMB = *args.FileSizeLimitMB
			}
			if args.ContainerRuntime != nil {
				cfg.Tools.Shell.Sandbox.ContainerRuntime = *args.ContainerRuntime
			}
			if args.ContainerImage != nil {
				cfg.Tools.Shell.Sandbox.ContainerImage = *args.ContainerImage
			}
			if args.AllowFallback != nil {
				cfg.Tools.Shell.Sandbox.AllowFallback = *args.AllowFallback
			}
			if args.UnsafePassthrough != nil {
				cfg.Tools.Shell.Sandbox.UnsafePassthrough = *args.UnsafePassthrough
			}
			if args.EnvAllowlist != nil {
				cfg.Tools.Shell.Sandbox.EnvAllowlist = args.EnvAllowlist
			}
			if args.EnvDenylist != nil {
				cfg.Tools.Shell.Sandbox.EnvDenylist = args.EnvDenylist
			}
		})
	}
	return tool
}

func (t *toolSet) configDiagnosticsSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.diagnostics.set",
		Description: "Set fields in the [diagnostics] section (commands). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"commands":{"type":"object","additionalProperties":{"type":"string"}}},"additionalProperties":false}`),
		Risk:        registry.RiskCommand,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			Commands map[string]string `json:"commands"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.diagnostics.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.diagnostics.set (%s scope): update diagnostics section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.Commands != nil {
				if cfg.Diagnostics.Commands == nil {
					cfg.Diagnostics.Commands = make(map[string]string)
				}
				for k, v := range args.Commands {
					cfg.Diagnostics.Commands[k] = v
				}
			}
		})
	}
	return tool
}

func (t *toolSet) configHooksSetTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.hooks.set",
		Description: "Set fields in the [hooks] section (fail_closed, entries). Omitted fields are preserved.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","global"]},"fail_closed":{"type":"boolean"},"entries":{"type":"array","items":{"type":"object","properties":{"event":{"type":"string"},"matcher":{"type":"string"},"command":{"type":"string"},"timeout_ms":{"type":"integer"}},"additionalProperties":false}}},"additionalProperties":false}`),
		Risk:        registry.RiskCommand,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			configWriteEnvelope
			FailClosed *bool `json:"fail_closed"`
			Entries    []struct {
				Event     *string `json:"event"`
				Matcher   *string `json:"matcher"`
				Command   *string `json:"command"`
				TimeoutMS *int    `json:"timeout_ms"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return registry.ToolResult{}, fmt.Errorf("decode config.hooks.set args: %w", err)
		}
		scope := args.resolvedScope()
		reason := fmt.Sprintf("config.hooks.set (%s scope): update hooks section", scope)
		return t.commitConfigWrite(ctx, scope, reason, false, func(cfg *config.Config) {
			if args.FailClosed != nil {
				cfg.Hooks.FailClosed = *args.FailClosed
			}
			if args.Entries != nil {
				cfg.Hooks.Entries = nil
				for _, e := range args.Entries {
					entry := config.HookConfig{}
					if e.Event != nil {
						entry.Event = *e.Event
					}
					if e.Matcher != nil {
						entry.Matcher = *e.Matcher
					}
					if e.Command != nil {
						entry.Command = *e.Command
					}
					if e.TimeoutMS != nil {
						entry.TimeoutMS = *e.TimeoutMS
					}
					cfg.Hooks.Entries = append(cfg.Hooks.Entries, entry)
				}
			}
		})
	}
	return tool
}

func (t *toolSet) configReadTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.read",
		Description: "Read the current Marshal configuration (merged project + global). Secret fields (api_key, search_key) are masked to \"***\". Use this before changing settings so you know the current values.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"sections":{"type":"array","items":{"type":"string"},"description":"Optional list of top-level section names to include (e.g. [\"agent\",\"swarm\"]). Omit to return all sections."}},"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
		Cacheable:   true,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			Sections []string `json:"sections"`
		}
		if len(call.Args) > 0 && string(call.Args) != "" && string(call.Args) != "null" {
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("decode config.read args: %w", err)
			}
		}
		masked := config.MaskSecrets(t.config)
		out, err := filteredConfigJSON(masked, args.Sections)
		if err != nil {
			return registry.ToolResult{}, err
		}
		return registry.ToolResult{
			Summary: "current configuration (secrets masked)",
			Content: out,
		}, nil
	}
	return tool
}

// filteredConfigJSON marshals cfg to JSON, optionally keeping only the named
// top-level sections. Empty filter returns the whole config.
func filteredConfigJSON(cfg config.Config, sections []string) (string, error) {
	if len(sections) == 0 {
		b, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal config: %w", err)
		}
		return string(b), nil
	}
	// Marshal to a map, then keep only requested keys.
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		return "", fmt.Errorf("remarshal config: %w", err)
	}
	keep := map[string]any{}
	for _, s := range sections {
		if v, ok := full[s]; ok {
			keep[s] = v
		}
	}
	b, err := json.MarshalIndent(keep, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal filtered config: %w", err)
	}
	return string(b), nil
}
