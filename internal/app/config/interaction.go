package config

import (
	"fmt"

	"marshal/internal/interaction"
	"marshal/internal/llm/routing"
)

// InteractionDefaults returns the configured starting tuple. It validates
// without falling back: a malformed configured tuple is an error, never a
// silent Act.
func (cfg Config) InteractionDefaults() (interaction.Defaults, error) {
	d := interaction.Defaults{Mode: cfg.Agent.Mode, Auto: cfg.Agent.Auto}
	if err := interaction.ValidateDefaults(d); err != nil {
		return interaction.Defaults{}, err
	}
	return d, nil
}

// Clone returns a deep copy of cfg so callers can snapshot it without
// aliasing maps or slices.
func Clone(cfg Config) Config { return deepCopyConfig(cfg) }

// legacyApprovalMode projects the canonical tuple onto the retired five-mode
// spelling for unmigrated callers. It is a compatibility projection only.
func legacyApprovalMode(mode interaction.Mode, auto bool) string {
	switch mode {
	case interaction.ModeAsk:
		return "default"
	case interaction.ModeAct:
		if auto {
			return "auto"
		}
		return "copilot"
	default:
		return "default"
	}
}

// normalizeInteractionFields canonicalizes one layer's interaction fields.
// It returns the canonical mode/auto pointers (nil means absent) plus any
// source-specific notices. The precedence table is:
//
//   - canonical mode wins; a same-layer legacy approval_mode is ignored with
//     a notice and its derived Auto never contributes;
//   - canonical Ask synthesizes Auto=false to clear an inherited Auto;
//   - canonical Act with absent Auto preserves the lower layer's Auto;
//   - legacy-only mode supplies both fields, and a same-layer explicit Auto
//     overrides the derived bool;
//   - absent mode stays absent.
func normalizeInteractionFields(mode *interaction.Mode, auto *bool, legacy *string, source, path string) (*interaction.Mode, *bool, []Diagnostic, error) {
	var notices []Diagnostic
	if mode != nil {
		if legacy != nil {
			notices = append(notices, Diagnostic{
				Severity: SeverityInfo,
				Path:     path + ".approval_mode",
				Message:  "approval_mode is retired and ignored because mode is set",
				Source:   source,
			})
		}
		switch *mode {
		case interaction.ModeAsk:
			if auto != nil && *auto {
				return nil, nil, nil, fmt.Errorf("%s: Ask cannot enable Auto", path)
			}
			f := false
			auto = &f
		case interaction.ModeAct:
			// Preserve the lower layer's Auto when this layer omits it.
		default:
			return nil, nil, nil, fmt.Errorf("%s.mode: invalid interaction mode %q", path, *mode)
		}
		return mode, auto, notices, nil
	}
	if legacy != nil {
		d, note, err := interaction.LegacyDefaults(*legacy)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s.approval_mode: %w", path, err)
		}
		m := d.Mode
		a := d.Auto
		if auto != nil {
			a = *auto
		}
		notices = append(notices, Diagnostic{
			Severity: SeverityInfo,
			Path:     path + ".approval_mode",
			Message:  note,
			Source:   source,
		})
		return &m, &a, notices, nil
	}
	return nil, auto, notices, nil
}

// normalizeInteractionFile canonicalizes the interaction fields in one file
// mirror before merge. It returns a fresh copy; the caller keeps the raw
// mirror for hoisting.
func normalizeInteractionFile(file configFile, source string) (configFile, []Diagnostic, error) {
	out := file
	var notices []Diagnostic

	if file.Agent != nil {
		a := *file.Agent
		mode, auto, ns, err := normalizeInteractionFields(a.Mode, a.Auto, a.ApprovalMode, source, "agent")
		if err != nil {
			return configFile{}, nil, err
		}
		notices = append(notices, ns...)
		a.Mode = mode
		a.Auto = auto
		a.ApprovalMode = nil
		out.Agent = &a
	}
	if file.CustomAgents != nil {
		agents := make(map[string]fileCustomAgent, len(file.CustomAgents))
		for name, ca := range file.CustomAgents {
			mode, auto, ns, err := normalizeInteractionFields(ca.Mode, ca.Auto, ca.ApprovalMode, source, "custom_agents."+name)
			if err != nil {
				return configFile{}, nil, err
			}
			notices = append(notices, ns...)
			ca.Mode = mode
			ca.Auto = auto
			ca.ApprovalMode = nil
			agents[name] = ca
		}
		out.CustomAgents = agents
	}
	return out, notices, nil
}

// canonicalFileForSave drops deprecated interaction spellings and retired
// shell keys from an explicit save. Authored rules are retained; effective
// System/revision/session overrides are never serialized.
func canonicalFileForSave(file configFile, source string) (configFile, error) {
	_ = source
	out := file
	if file.Agent != nil {
		a := *file.Agent
		a.ApprovalMode = nil
		out.Agent = &a
	}
	if file.CustomAgents != nil {
		agents := make(map[string]fileCustomAgent, len(file.CustomAgents))
		for name, ca := range file.CustomAgents {
			ca.ApprovalMode = nil
			agents[name] = ca
		}
		out.CustomAgents = agents
	}
	if file.Tools != nil && file.Tools.Shell != nil {
		s := *file.Tools.Shell
		s.AutoApprove = nil
		s.GuardrailDynamicArgv0 = nil
		tools := *file.Tools
		tools.Shell = &s
		out.Tools = &tools
	}
	return out, nil
}

// validateInteractionTuple rejects a cumulative merge whose tuple is invalid
// (for example auto=true over an inherited Ask).
func validateInteractionTuple(a AgentConfig) error {
	return interaction.ValidateDefaults(interaction.Defaults{Mode: a.Mode, Auto: a.Auto})
}

// runtime converts a file custom agent to its runtime form. The name is the
// map key, which the file mirror does not carry.
func (a fileCustomAgent) runtime(name string) routing.CustomAgent {
	ca := routing.CustomAgent{
		Name:                   name,
		Preset:                 a.Preset,
		SystemPrompt:           a.SystemPrompt,
		ToolDenylist:           a.ToolDenylist,
		Mode:                   a.Mode,
		Auto:                   a.Auto,
		MaxIterations:          a.MaxIterations,
		MaxIterationsUnlimited: a.MaxIterationsUnlimited,
		Context:                a.Context,
		Temperature:            a.Temperature,
		Thinking:               a.Thinking,
	}
	if a.ApprovalMode != nil {
		ca.ApprovalMode = *a.ApprovalMode
	}
	return ca
}

// customAgentFile converts a runtime custom agent to its canonical file
// form. The retired approval_mode is never emitted.
func customAgentFile(a routing.CustomAgent) fileCustomAgent {
	return fileCustomAgent{
		Name:                   a.Name,
		Preset:                 a.Preset,
		SystemPrompt:           a.SystemPrompt,
		ToolDenylist:           a.ToolDenylist,
		Mode:                   a.Mode,
		Auto:                   a.Auto,
		MaxIterations:          a.MaxIterations,
		MaxIterationsUnlimited: a.MaxIterationsUnlimited,
		Context:                a.Context,
		Temperature:            a.Temperature,
		Thinking:               a.Thinking,
	}
}
