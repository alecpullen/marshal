package settings

import (
	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func agentFrame(s *state) *frame {
	getActivePreset := func() routing.ModelPreset {
		name := activePresetNameFor(s.cfg)
		if name == "" {
			return routing.ModelPreset{}
		}
		return s.cfg.Models.Presets[name]
	}

	return newFrame("Agent", func() []*field {
		active := getActivePreset()
		presetTitle := active.Name
		if presetTitle == "" {
			presetTitle = "(none)"
		}
		return []*field{
			func() *field {
				f := enumField("agent.default_profile", "Default profile", sortedKeys(s.cfg.AgentProfiles),
					func() string { return s.cfg.Profile.Default },
					func(v string) { s.cfg.Profile.Default = v })
				f.TomlPath = "profile.default"
				f.Desc = "active agent profile that sets model routing roles"
				return f
			}(),
			{ID: "agent.preset", Title: "Preset", Kind: kindScalar,
				TomlPath: "models.presets.<name>",
				Desc:     "resolved from the default profile's implementer role",
				GetStr:   func() string { return presetTitle }},
			func() *field {
				f := intField("agent.max_tool_iterations", "Max tool iterations",
					func() int { return s.cfg.Agent.MaxToolIterations }, 0,
					func(v int) { s.cfg.Agent.MaxToolIterations = v })
				f.TomlPath = "agent.max_tool_iterations"
				f.Desc = "max tool calls per agent turn · 0 = unlimited (default)"
				return f
			}(),
			func() *field {
				f := intField("agent.max_retries", "Max retries",
					func() int { return s.cfg.Agent.MaxRetries }, 0,
					func(v int) { s.cfg.Agent.MaxRetries = v })
				f.TomlPath = "agent.max_retries"
				f.Desc = "LLM retries before giving up on a turn"
				return f
			}(),
			func() *field {
				f := intField("agent.max_turn_context_tokens", "Max turn context tokens",
					func() int { return s.cfg.Agent.MaxTurnContextTokens }, 0,
					func(v int) { s.cfg.Agent.MaxTurnContextTokens = v })
				f.TomlPath = "agent.max_turn_context_tokens"
				f.Desc = "max context tokens per agent turn (truncates oldest)"
				return f
			}(),
			func() *field {
				f := intField("agent.max_tool_result_chars", "Max tool result chars",
					func() int { return s.cfg.Agent.MaxToolResultChars }, 0,
					func(v int) { s.cfg.Agent.MaxToolResultChars = v })
				f.TomlPath = "agent.max_tool_result_chars"
				f.Desc = "chars per tool result before spilling to file (0 = derive from window)"
				return f
			}(),
			func() *field {
				f := intField("agent.subtask_iterations", "Subtask iterations",
					func() int { return s.cfg.Agent.SubtaskIterations }, 0,
					func(v int) { s.cfg.Agent.SubtaskIterations = v })
				f.TomlPath = "agent.subtask_iterations"
				f.Desc = "max sub-agent iterations per task · 0 = unlimited (default)"
				return f
			}(),
			func() *field {
				f := intField("agent.thinking_budget_margin", "Thinking budget margin",
					func() int { return s.cfg.Agent.ThinkingBudgetMargin }, 0,
					func(v int) { s.cfg.Agent.ThinkingBudgetMargin = v })
				f.TomlPath = "agent.thinking_budget_margin"
				f.Desc = "Anthropic thinking headroom tokens · 0 = auto (max(2048, max/4)), -1 = disabled"
				return f
			}(),
			{ID: "agent.plan_first", Title: "Plan first", Kind: kindToggle,
				TomlPath: "agent.plan_first",
				Desc:     "require a plan step before implementation",
				GetBool:  func() bool { return s.cfg.Agent.PlanFirst },
				SetBool:  func(v bool) { s.cfg.Agent.PlanFirst = v }},
			func() *field {
				f := enumField("agent.approval_mode", "Approval mode",
					[]string{"plan", "default", "edit", "copilot", "auto"},
					func() string { return s.cfg.Agent.ApprovalMode },
					func(v string) { s.cfg.Agent.ApprovalMode = v })
				f.TomlPath = "agent.approval_mode"
				f.Desc = "interaction/approval mode for the agent loop"
				SetFieldWriteGlobal(f, true)
				return f
			}(),
		}
	})
}

// activePresetNameFor resolves the implementer preset of the default profile.
// It remains local because the config package intentionally keeps its
// equivalent helper private.
func activePresetNameFor(cfg config.Config) string {
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		return ""
	}
	return profile.Roles[routing.RoleImplementer].Preset
}
