package settings

import (
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
		return []*field{
			enumField("agent.default_profile", "Default profile", sortedKeys(s.cfg.AgentProfiles),
				func() string { return s.cfg.Profile.Default },
				func(v string) { s.cfg.Profile.Default = v }),
			// Read-only: shows which preset the profile resolves to.
			{id: "agent.preset", title: "Preset", kind: kindScalar,
				desc:   "resolved from the default profile's implementer role",
				getStr: func() string { return presetTitle }},
			scalarField("agent.provider", "Provider", func() string { return provider }, setProvider),
			scalarField("agent.model", "Model", func() string { return model }, setModel),
			{id: "agent.local_only", title: "Local only", kind: kindToggle,
				desc:    "block remote providers for this preset",
				getBool: func() bool { return getActive().LocalOnly },
				setBool: func(v bool) {
					if name := activePresetNameFor(s.cfg); name != "" {
						if p, ok := s.cfg.Models.Presets[name]; ok {
							p.LocalOnly = v
							s.cfg.Models.Presets[name] = p
						}
					}
				}},
			intField2("agent.max_tool_iterations", "Max tool iterations",
				func() int { return s.cfg.Agent.MaxToolIterations }, 1,
				func(v int) { s.cfg.Agent.MaxToolIterations = v }),
			intField2("agent.max_retries", "Max retries",
				func() int { return s.cfg.Agent.MaxRetries }, 0,
				func(v int) { s.cfg.Agent.MaxRetries = v }),
			intField2("agent.max_turn_context_tokens", "Max turn context tokens",
				func() int { return s.cfg.Agent.MaxTurnContextTokens }, 0,
				func(v int) { s.cfg.Agent.MaxTurnContextTokens = v }),
			intField2("agent.subtask_iterations", "Subtask iterations",
				func() int { return s.cfg.Agent.SubtaskIterations }, 0,
				func(v int) { s.cfg.Agent.SubtaskIterations = v }),
			{id: "agent.plan_first", title: "Plan first", kind: kindToggle,
				getBool: func() bool { return s.cfg.Agent.PlanFirst },
				setBool: func(v bool) { s.cfg.Agent.PlanFirst = v }},
		}
	})
}
