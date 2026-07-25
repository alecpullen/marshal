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
