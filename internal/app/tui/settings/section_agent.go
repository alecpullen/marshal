package settings

import (
	"sort"
	"strconv"

	"charm.land/huh/v2"

	"marshal/internal/llm/routing"
)

type agentBuffers struct {
	provider, model string
	localOnly       bool
	presetOpt       string
	maxToolIter     string
	maxRetries      string
	maxTurnTokens   string
	subtaskIters    string
}

func newAgentPane(s *state) sectionPane {
	b := &agentBuffers{}
	return newScalarPane(func() *huh.Form { return buildAgentForm(s, b) })
}

func buildAgentForm(s *state, b *agentBuffers) *huh.Form {
	profileNames := make([]string, 0, len(s.cfg.AgentProfiles))
	for name := range s.cfg.AgentProfiles {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)

	presetName := activePresetNameFor(s.cfg)
	activePreset := routing.ModelPreset{Name: presetName}
	if presetName != "" {
		if p, ok := s.cfg.Models.Presets[presetName]; ok {
			activePreset = p
		}
	}

	b.provider = activePreset.Provider
	if b.provider == "" {
		b.provider = s.cfg.Agent.Provider
	}
	b.model = activePreset.Model
	if b.model == "" {
		b.model = s.cfg.Agent.Model
	}
	b.localOnly = activePreset.LocalOnly
	b.presetOpt = activePreset.Name
	b.maxToolIter = strconv.Itoa(s.cfg.Agent.MaxToolIterations)
	b.maxRetries = strconv.Itoa(s.cfg.Agent.MaxRetries)
	b.maxTurnTokens = strconv.Itoa(s.cfg.Agent.MaxTurnContextTokens)
	b.subtaskIters = strconv.Itoa(s.cfg.Agent.SubtaskIterations)

	profileOpts := make([]huh.Option[string], 0, len(profileNames)+1)
	for _, name := range profileNames {
		profileOpts = append(profileOpts, huh.NewOption(name, name))
	}
	// Preserve the current value even if the profile map is empty, so huh
	// doesn't clobber cfg.Profile.Default on form init.
	if s.cfg.Profile.Default != "" {
		found := false
		for _, name := range profileNames {
			if name == s.cfg.Profile.Default {
				found = true
				break
			}
		}
		if !found {
			profileOpts = append([]huh.Option[string]{huh.NewOption(s.cfg.Profile.Default, s.cfg.Profile.Default)}, profileOpts...)
		}
	}
	if len(profileOpts) == 0 {
		profileOpts = []huh.Option[string]{huh.NewOption("", "")}
	}

	presetTitle := activePreset.Name
	if presetTitle == "" {
		presetTitle = "(none)"
	}

	fields := []huh.Field{
		huh.NewSelect[string]().
			Key("Default profile").
			Title("Default profile").
			Options(profileOpts...).
			Value(&s.cfg.Profile.Default),

		// Preset is read-only display — a single-option Select keeps the user
		// from editing it while still showing it inline.
		huh.NewSelect[string]().
			Key("Preset").
			Title("Preset").
			Options(huh.NewOption(presetTitle, activePreset.Name)).
			Value(&b.presetOpt),

		huh.NewInput().
			Key("Provider").
			Title("Provider").
			Value(&b.provider).
			Validate(func(val string) error {
				if name := activePresetNameFor(s.cfg); name != "" {
					if p, ok := s.cfg.Models.Presets[name]; ok {
						p.Provider = val
						s.cfg.Models.Presets[name] = p
					}
				} else {
					s.cfg.Agent.Provider = val
				}
				return nil
			}),

		huh.NewInput().
			Key("Model").
			Title("Model").
			Value(&b.model).
			Validate(func(val string) error {
				if name := activePresetNameFor(s.cfg); name != "" {
					if p, ok := s.cfg.Models.Presets[name]; ok {
						p.Model = val
						s.cfg.Models.Presets[name] = p
					}
				} else {
					s.cfg.Agent.Model = val
				}
				return nil
			}),

		huh.NewConfirm().
			Key("Local only").
			Title("Local only").
			Description("block remote providers for this preset").
			Value(&b.localOnly).
			Validate(func(checked bool) error {
				if name := activePresetNameFor(s.cfg); name != "" {
					if p, ok := s.cfg.Models.Presets[name]; ok {
						p.LocalOnly = checked
						s.cfg.Models.Presets[name] = p
					}
				}
				return nil
			}),

		numField("Max tool iterations", &b.maxToolIter, 1, func(v int) { s.cfg.Agent.MaxToolIterations = v }),
		numField("Max retries", &b.maxRetries, 0, func(v int) { s.cfg.Agent.MaxRetries = v }),
		numField("Max turn context tokens", &b.maxTurnTokens, 0, func(v int) { s.cfg.Agent.MaxTurnContextTokens = v }),
		numField("Subtask iterations", &b.subtaskIters, 0, func(v int) { s.cfg.Agent.SubtaskIterations = v }),

		huh.NewConfirm().
			Key("Plan first").
			Title("Plan first").
			Value(&s.cfg.Agent.PlanFirst),
	}

	return newSectionForm(fields...)
}
