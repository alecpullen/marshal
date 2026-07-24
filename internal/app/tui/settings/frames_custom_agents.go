package settings

import (
	"fmt"
	"strconv"
	"strings"

	"marshal/internal/app/tui/picker"
	"marshal/internal/llm/routing"
)

// customAgentsFrame is the settings browser section root for custom agents.
// It mirrors the /agents panel's custom-agent drill but lives in the generic
// settings browser for users who prefer the sidebar navigation.
func customAgentsFrame(s *state) *frame {
	drill := entriesDrillExt("custom_agents", "Custom Agents", "New custom agent name",
		func() []string { return sortedKeys(s.cfg.CustomAgents) },
		func(k string) string {
			ca := s.cfg.CustomAgents[k]
			preset := ca.Preset
			if preset == "" {
				preset = "(no preset)"
			}
			return fmt.Sprintf("%s  (%s)", k, preset)
		},
		func(k string) error {
			if k == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.CustomAgents[k]; ok {
				return fmt.Errorf("entry already exists")
			}
			if s.cfg.CustomAgents == nil {
				s.cfg.CustomAgents = map[string]routing.CustomAgent{}
			}
			s.cfg.CustomAgents[k] = routing.CustomAgent{
				Name:   k,
				Preset: "",
			}
			return nil
		},
		func(k string) *frame {
			return newFrame(k, func() []*field {
				fields := CustomAgentFields(s, k)
				out := make([]*field, len(fields))
				for i, f := range fields {
					out[i] = f
				}
				return out
			})
		},
		func(k string) { delete(s.cfg.CustomAgents, k) },
		entriesOpts{
			yank: func(k string) any {
				ca := s.cfg.CustomAgents[k]
				return yankedMapEntry{key: k, val: ca}
			},
			paste: func(_ string, data any) error {
				ye, ok := data.(yankedMapEntry)
				if !ok {
					return fmt.Errorf("nothing yanked")
				}
				existing := map[string]bool{}
				for kk := range s.cfg.CustomAgents {
					existing[kk] = true
				}
				name := uniqueCopyName(ye.key, existing)
				if s.cfg.CustomAgents == nil {
					s.cfg.CustomAgents = map[string]routing.CustomAgent{}
				}
				ca := ye.val.(routing.CustomAgent)
				ca.Name = name
				s.cfg.CustomAgents[name] = ca
				return nil
			},
		})
	return rootDrillFrame("Custom Agents", drill)
}

// CustomAgentFields returns the field list for editing a single custom agent
// by name. It is shared between the settings browser (customAgentsFrame) and
// the /agents roster panel (agents.Panel.customAgentFrame).
func CustomAgentFields(s *State, name string) []*Field {
	cfg := s.cfg
	ca := cfg.CustomAgents[name]

	// Preset picker.
	presetField := NewField("custom_agents."+name+".preset", "Preset", KindPicker)
	SetFieldDesc(presetField, "model preset for this custom agent")
	SetFieldGetStr(presetField, func() string { return ca.Preset })
	SetFieldPickOptions(presetField, func() []picker.Item {
		names := sortedKeys(cfg.Models.Presets)
		items := make([]picker.Item, 0, len(names))
		for _, n := range names {
			pr := cfg.Models.Presets[n]
			items = append(items, picker.Item{Label: n, Detail: pr.Provider + "/" + pr.Model, Value: n})
		}
		return items
	})
	SetFieldPickOnPick(presetField, func(v string) error {
		ca2 := cfg.CustomAgents[name]
		ca2.Preset = v
		cfg.CustomAgents[name] = ca2
		return nil
	})

	// System prompt.
	promptField := NewField("custom_agents."+name+".system_prompt", "System prompt", KindScalar)
	SetFieldDesc(promptField, "custom system prompt addendum")
	SetFieldGetStr(promptField, func() string { return ca.SystemPrompt })
	SetFieldSetStr(promptField, func(v string) error {
		ca2 := cfg.CustomAgents[name]
		ca2.SystemPrompt = v
		cfg.CustomAgents[name] = ca2
		return nil
	})

	// Tool denylist: comma-separated.
	denylistField := NewField("custom_agents."+name+".tool_denylist", "Tool denylist", KindScalar)
	SetFieldDesc(denylistField, "comma-separated tool names to deny")
	SetFieldGetStr(denylistField, func() string {
		return strings.Join(ca.ToolDenylist, ", ")
	})
	SetFieldSetStr(denylistField, func(v string) error {
		var names []string
		if v != "" {
			for _, part := range strings.Split(v, ",") {
				trimmed := strings.TrimSpace(part)
				if trimmed != "" {
					names = append(names, trimmed)
				}
			}
		}
		ca2 := cfg.CustomAgents[name]
		ca2.ToolDenylist = names
		cfg.CustomAgents[name] = ca2
		return nil
	})

	// Approval mode.
	modeField := NewField("custom_agents."+name+".approval_mode", "Approval mode", KindEnum)
	SetFieldDesc(modeField, "approval mode for this custom agent")
	SetFieldGetStr(modeField, func() string {
		if ca.ApprovalMode == "" {
			return "default"
		}
		return ca.ApprovalMode
	})
	SetFieldOptions(modeField, func() []string { return []string{"plan", "default", "edit", "copilot", "auto"} })
	SetFieldSetStr(modeField, func(v string) error {
		ca2 := cfg.CustomAgents[name]
		ca2.ApprovalMode = v
		cfg.CustomAgents[name] = ca2
		return nil
	})

	// Max iterations.
	iterField := NewField("custom_agents."+name+".max_iterations", "Max iterations", KindScalar)
	SetFieldDesc(iterField, "max tool iterations (0 = inherit)")
	SetFieldGetStr(iterField, func() string {
		if ca.MaxIterations == 0 {
			return "0 (inherit)"
		}
		return fmt.Sprintf("%d", ca.MaxIterations)
	})
	SetFieldSetStr(iterField, func(v string) error {
		n := 0
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return fmt.Errorf("must be a number")
		}
		ca2 := cfg.CustomAgents[name]
		ca2.MaxIterations = n
		cfg.CustomAgents[name] = ca2
		return nil
	})

	// Context budget: max_repo_context_tokens.
	ctxField := NewField("custom_agents."+name+".context", "Context (max repo tokens)", KindScalar)
	SetFieldDesc(ctxField, "max repo context tokens (0 = inherit from role)")
	SetFieldGetStr(ctxField, func() string {
		if ca.Context.MaxRepoContextTokens == 0 {
			return "0 (inherit)"
		}
		return strconv.Itoa(ca.Context.MaxRepoContextTokens)
	})
	SetFieldSetStr(ctxField, func(v string) error {
		n := 0
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return fmt.Errorf("must be a number")
		}
		ca2 := cfg.CustomAgents[name]
		ca2.Context.MaxRepoContextTokens = n
		cfg.CustomAgents[name] = ca2
		return nil
	})

	return []*Field{
		presetField,
		promptField,
		denylistField,
		modeField,
		iterField,
		ctxField,
	}
}
