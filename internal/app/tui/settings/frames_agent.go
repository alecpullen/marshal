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
			providerRow.title = "Provider (preset: " + active.Name + ")"
			providerRow.desc = "writes into preset " + active.Name + " — shared by every role that uses it"
			modelRow.title = "Model (preset: " + active.Name + ")"
			modelRow.desc = "writes into preset " + active.Name + " — shared by every role that uses it"
		}
		return []*field{
			func() *field {
				f := enumField("agent.default_profile", "Default profile", sortedKeys(s.cfg.AgentProfiles),
					func() string { return s.cfg.Profile.Default },
					func(v string) { s.cfg.Profile.Default = v })
				f.tomlPath = "profile.default"
				f.desc = "active agent profile that sets model routing roles"
				return f
			}(),
			// Read-only: shows which preset the profile resolves to.
			{id: "agent.preset", title: "Preset", kind: kindScalar,
				tomlPath: "models.presets.<name>",
				desc:     "resolved from the default profile's implementer role",
				getStr:   func() string { return presetTitle }},
			func() *field {
				f := providerRow
				f.tomlPath = "agent.provider"
				return f
			}(),
			func() *field {
				f := modelRow
				f.tomlPath = "agent.model"
				return f
			}(),
			{id: "agent.local_only", title: "Local only", kind: kindToggle,
				tomlPath: "models.presets.<name>.local_only",
				desc:     "block remote providers for this preset",
				getBool:  func() bool { return getActive().LocalOnly },
				setBool: func(v bool) {
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
				f.tomlPath = "agent.max_tool_iterations"
				f.desc = "max tool calls per agent turn"
				return f
			}(),
			func() *field {
				f := intField("agent.max_retries", "Max retries",
					func() int { return s.cfg.Agent.MaxRetries }, 0,
					func(v int) { s.cfg.Agent.MaxRetries = v })
				f.tomlPath = "agent.max_retries"
				f.desc = "LLM retries before giving up on a turn"
				return f
			}(),
			func() *field {
				f := intField("agent.max_turn_context_tokens", "Max turn context tokens",
					func() int { return s.cfg.Agent.MaxTurnContextTokens }, 0,
					func(v int) { s.cfg.Agent.MaxTurnContextTokens = v })
				f.tomlPath = "agent.max_turn_context_tokens"
				f.desc = "max context tokens per agent turn (truncates oldest)"
				return f
			}(),
			func() *field {
				f := intField("agent.subtask_iterations", "Subtask iterations",
					func() int { return s.cfg.Agent.SubtaskIterations }, 0,
					func(v int) { s.cfg.Agent.SubtaskIterations = v })
				f.tomlPath = "agent.subtask_iterations"
				f.desc = "max sub-agent iterations per task"
				return f
			}(),
			{id: "agent.plan_first", title: "Plan first", kind: kindToggle,
				tomlPath: "agent.plan_first",
				desc:     "require a plan step before implementation",
				getBool:  func() bool { return s.cfg.Agent.PlanFirst },
				setBool:  func(v bool) { s.cfg.Agent.PlanFirst = v }},
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
