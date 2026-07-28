package settings

import (
	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func agentFrame(s *state) *frame {
	// Preset-aware setters: write into the active preset when one exists,
	// else into cfg.Agent — same rule as the legacy huh form.
	setProvider := func(v string) error {
		if name := activePresetNameFor(s.cfg); name != "" {
			if p, ok := s.cfg.Models.Presets[name]; ok {
				p.Provider = v
				s.cfg.Models.Presets[name] = p
				return nil
			}
		}
		s.cfg.Agent.Provider = v
		return nil
	}
	setModel := func(v string) error {
		if name := activePresetNameFor(s.cfg); name != "" {
			if p, ok := s.cfg.Models.Presets[name]; ok {
				p.Model = v
				s.cfg.Models.Presets[name] = p
				return nil
			}
		}
		s.cfg.Agent.Model = v
		return nil
	}
	getActive := func() routing.ModelPreset {
		if name := activePresetNameFor(s.cfg); name != "" {
			if p, ok := s.cfg.Models.Presets[name]; ok {
				return p
			}
		}
		return routing.ModelPreset{}
	}

	return newFrame("Agent", func() []*field {
		active := getActive()
		provider := active.Provider
		if provider == "" {
			provider = s.cfg.Agent.Provider
		}
		model := active.Model
		if model == "" {
			model = s.cfg.Agent.Model
		}
		presetTitle := active.Name
		if presetTitle == "" {
			presetTitle = "(none)"
		}
		providerRow := providerPickerField(s, "agent.provider",
			func() string { return provider },
			setProvider)
		modelRow := modelPickerField(s, "agent.model",
			func() string { return provider },
			func() string { return model },
			setModel)
		if active.Name != "" {
			providerRow.Title = "Provider (preset: " + active.Name + ")"
			providerRow.Desc = "writes into preset " + active.Name + " — shared by every role that uses it"
			modelRow.Title = "Model (preset: " + active.Name + ")"
			modelRow.Desc = "writes into preset " + active.Name + " — shared by every role that uses it"
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
			// Read-only: shows which preset the profile resolves to.
			{ID: "agent.preset", Title: "Preset", Kind: kindScalar,
				TomlPath: "models.presets.<name>",
				Desc:     "resolved from the default profile's implementer role",
				GetStr:   func() string { return presetTitle }},
			func() *field {
				f := providerRow
				f.TomlPath = "agent.provider"
				return f
			}(),
			func() *field {
				f := modelRow
				f.TomlPath = "agent.model"
				return f
			}(),
			{ID: "agent.local_only", Title: "Local only", Kind: kindToggle,
				TomlPath: "models.presets.<name>.local_only",
				Desc:     "block remote providers for this preset",
				GetBool:  func() bool { return getActive().LocalOnly },
				SetBool: func(v bool) {
					if name := activePresetNameFor(s.cfg); name != "" {
						if p, ok := s.cfg.Models.Presets[name]; ok {
							p.LocalOnly = v
							s.cfg.Models.Presets[name] = p
						}
					}
				}},
			func() *field {
				f := intField("agent.max_tool_iterations", "Max tool iterations",
					func() int { return s.cfg.Agent.MaxToolIterations }, 1,
					func(v int) { s.cfg.Agent.MaxToolIterations = v })
				f.TomlPath = "agent.max_tool_iterations"
				f.Desc = "max tool calls per agent turn"
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
				f := intField("agent.subtask_iterations", "Subtask iterations",
					func() int { return s.cfg.Agent.SubtaskIterations }, 0,
					func(v int) { s.cfg.Agent.SubtaskIterations = v })
				f.TomlPath = "agent.subtask_iterations"
				f.Desc = "max sub-agent iterations per task"
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
