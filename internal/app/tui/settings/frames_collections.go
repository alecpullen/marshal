package settings

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/picker"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
)

// rootDrillFrame unwraps a single drill field into a section root frame, so
// sections that are nothing but one collection open directly on the list.
func rootDrillFrame(title string, drill *field) *frame {
	f := drill.build()
	f.title = title
	return f
}

func providersFrame(s *state) *frame {
	drill := entriesDrill("providers", "Providers", "New provider name",
		func() []string { return sortedKeys(s.cfg.Providers) },
		func(k string) string { return k + "  (" + maskKey(s.cfg.Providers[k].APIKey) + ")" },
		func(k string) error {
			if k == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.Providers[k]; ok {
				return fmt.Errorf("entry already exists")
			}
			if s.cfg.Providers == nil {
				s.cfg.Providers = map[string]config.ProviderConfig{}
			}
			s.cfg.Providers[k] = config.ProviderConfig{Type: "openai_compatible"}
			return nil
		},
		func(k string) *frame {
			mut := func(f func(*config.ProviderConfig)) {
				pc := s.cfg.Providers[k]
				f(&pc)
				s.cfg.Providers[k] = pc
			}
			invalidate := func() {
				delete(s.discovered, k)
			}
			return newFrame(k, func() []*field {
				return []*field{
					scalarField("providers."+k+".name", "Name",
						func() string { return k },
						func(v string) error {
							v = strings.TrimSpace(v)
							if v == "" {
								return fmt.Errorf("name cannot be empty")
							}
							if v == k {
								return nil
							}
							if _, ok := s.cfg.Providers[v]; ok {
								return fmt.Errorf("name already exists")
							}
							pc := s.cfg.Providers[k]
							delete(s.cfg.Providers, k)
							s.cfg.Providers[v] = pc
							delete(s.discovered, k)
							k = v
							return nil
						}),
					scalarField("providers."+k+".type", "Type",
						func() string { return s.cfg.Providers[k].Type },
						func(v string) error { mut(func(p *config.ProviderConfig) { p.Type = v }); return nil }),
					scalarField("providers."+k+".base_url", "Base URL",
						func() string { return s.cfg.Providers[k].BaseURL },
						func(v string) error { mut(func(p *config.ProviderConfig) { p.BaseURL = v }); invalidate(); return nil }),
					{id: "providers." + k + ".api_key_env", title: "API key env", kind: kindScalar,
						desc:   "env var name resolved at provider construction — preferred over storing the key",
						getStr: func() string { return s.cfg.Providers[k].APIKeyEnv },
						setStr: func(v string) error { mut(func(p *config.ProviderConfig) { p.APIKeyEnv = v }); invalidate(); return nil }},
					{id: "providers." + k + ".api_key", title: "API key", kind: kindScalar, masked: true,
						desc:     "enter replaces · empty keeps · d clears · prefer the env-var field",
						keywords: []string{"secret", "api key", "token"},
						getStr:   func() string { return s.cfg.Providers[k].APIKey },
						setStr: func(v string) error {
							mut(func(p *config.ProviderConfig) { p.APIKey = v })
							invalidate()
							return nil
						},
						del: func() {
							mut(func(p *config.ProviderConfig) { p.APIKey = "" })
							invalidate()
						}},
					{id: "providers." + k + ".tool_calling", title: "Tool calling", kind: kindToggle,
						desc:    "provider advertises native tool-calling support",
						getBool: func() bool { return s.cfg.Providers[k].ToolCalling },
						setBool: func(v bool) { mut(func(p *config.ProviderConfig) { p.ToolCalling = v }) }},
					testConnectionField(s, k),
				}
			})
		},
		func(k string) { delete(s.cfg.Providers, k) })
	f := rootDrillFrame("Providers", drill)
	f.addWizard = providersWizard(s)
	f.list.addWizard = f.addWizard
	return f
}

func providersWizard(s *state) func() *pickerRequest {
	return func() *pickerRequest {
		all := provider.All()
		items := make([]picker.Item, 0, len(all))
		for _, tpl := range all {
			items = append(items, picker.Item{
				Label:  tpl.Label,
				Detail: tpl.BaseURL,
				Badge:  badgeForTemplate(tpl),
				Value:  tpl.ID,
			})
		}
		return &pickerRequest{
			fieldID: "__wizard__",
			items:   items,
			title:   "Add provider",
			footer:  "pick a template",
			onPick: func(tplID string) error {
				tpl, ok := provider.Lookup(tplID)
				if !ok {
					return fmt.Errorf("unknown template %q", tplID)
				}
				existing := map[string]bool{}
				for k := range s.cfg.Providers {
					existing[k] = true
				}
				name := provider.UniqueName(tpl.ID, existing)
				if s.cfg.Providers == nil {
					s.cfg.Providers = map[string]config.ProviderConfig{}
				}
				s.cfg.Providers[name] = config.ProviderConfig{
					Type:        tpl.Type,
					BaseURL:     tpl.BaseURL,
					APIKeyEnv:   tpl.KeyEnv,
					ToolCalling: tpl.ToolCalling,
				}
				return nil
			},
		}
	}
}

func badgeForTemplate(tpl provider.ProviderTemplate) string {
	if tpl.Local {
		return "local"
	}
	return "remote"
}

func presetsFrame(s *state) *frame {
	drill := entriesDrill("presets", "Model Presets", "New preset name",
		func() []string { return sortedKeys(s.cfg.Models.Presets) },
		func(k string) string {
			p := s.cfg.Models.Presets[k]
			return k + "  (" + p.Provider + "/" + p.Model + ")"
		},
		func(k string) error {
			if k == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.Models.Presets[k]; ok {
				return fmt.Errorf("entry already exists")
			}
			s.cfg.Models.Presets[k] = routing.ModelPreset{Name: k}
			return nil
		},
		func(k string) *frame {
			mut := func(f func(*routing.ModelPreset)) {
				p := s.cfg.Models.Presets[k]
				f(&p)
				s.cfg.Models.Presets[k] = p
			}
			return newFrame(k, func() []*field {
				return []*field{
					scalarField("presets."+k+".provider", "Provider",
						func() string { return s.cfg.Models.Presets[k].Provider },
						func(v string) error { mut(func(p *routing.ModelPreset) { p.Provider = v }); return nil }),
					scalarField("presets."+k+".model", "Model",
						func() string { return s.cfg.Models.Presets[k].Model },
						func(v string) error { mut(func(p *routing.ModelPreset) { p.Model = v }); return nil }),
					intField("presets."+k+".context_window", "Context window",
						func() int { return s.cfg.Models.Presets[k].ContextWindow }, 0,
						func(v int) { mut(func(p *routing.ModelPreset) { p.ContextWindow = v }) }),
					intField("presets."+k+".max_output", "Max output tokens",
						func() int { return s.cfg.Models.Presets[k].MaxOutputTokens }, 0,
						func(v int) { mut(func(p *routing.ModelPreset) { p.MaxOutputTokens = v }) }),
					{id: "presets." + k + ".temperature", title: "Temperature", kind: kindScalar,
						getStr: func() string {
							return strconv.FormatFloat(s.cfg.Models.Presets[k].Temperature, 'f', -1, 64)
						},
						setStr: floatSetter(func(v float64) { mut(func(p *routing.ModelPreset) { p.Temperature = v }) })},
					{id: "presets." + k + ".top_p", title: "Top P", kind: kindScalar,
						getStr: func() string {
							return strconv.FormatFloat(s.cfg.Models.Presets[k].TopP, 'f', -1, 64)
						},
						setStr: floatSetter(func(v float64) { mut(func(p *routing.ModelPreset) { p.TopP = v }) })},
					enumField("presets."+k+".tool_calling", "Tool calling",
						[]string{"native", "simulated", "none"},
						func() string { return s.cfg.Models.Presets[k].ToolCalling },
						func(v string) { mut(func(p *routing.ModelPreset) { p.ToolCalling = v }) }),
					enumField("presets."+k+".reasoning", "Reasoning effort",
						[]string{"low", "medium", "high", "none"},
						func() string { return s.cfg.Models.Presets[k].ReasoningEffort },
						func(v string) { mut(func(p *routing.ModelPreset) { p.ReasoningEffort = v }) }),
					{id: "presets." + k + ".local_only", title: "Local only", kind: kindToggle,
						desc:    "block remote providers for this preset",
						getBool: func() bool { return s.cfg.Models.Presets[k].LocalOnly },
						setBool: func(v bool) { mut(func(p *routing.ModelPreset) { p.LocalOnly = v }) }},
				}
			})
		},
		func(k string) { delete(s.cfg.Models.Presets, k) })
	return rootDrillFrame("Model Presets", drill)
}

// sliceKeys returns "0".."n-1" index keys for slice-backed collections.
func sliceKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strconv.Itoa(i)
	}
	return out
}

func hooksFrame(s *state) *frame {
	drill := entriesDrill("hooks", "Hooks", "",
		func() []string { return sliceKeys(len(s.cfg.Hooks.Entries)) },
		func(k string) string {
			i, _ := strconv.Atoi(k)
			h := s.cfg.Hooks.Entries[i]
			return fmt.Sprintf("%s %s → %s", h.Event, h.Matcher, h.Command)
		},
		func(string) error {
			s.cfg.Hooks.Entries = append(s.cfg.Hooks.Entries, config.HookConfig{Event: "pre_tool"})
			return nil
		},
		func(k string) *frame {
			i, _ := strconv.Atoi(k)
			return newFrame("hook "+k, func() []*field {
				if i >= len(s.cfg.Hooks.Entries) {
					return nil
				}
				h := &s.cfg.Hooks.Entries[i]
				return []*field{
					scalarField("hooks."+k+".event", "Event",
						func() string { return h.Event },
						func(v string) error { h.Event = v; return nil }),
					scalarField("hooks."+k+".matcher", "Matcher",
						func() string { return h.Matcher },
						func(v string) error { h.Matcher = v; return nil }),
					scalarField("hooks."+k+".command", "Command",
						func() string { return h.Command },
						func(v string) error { h.Command = v; return nil }),
					intField("hooks."+k+".timeout_ms", "Timeout (ms)",
						func() int { return h.TimeoutMS }, 0, func(v int) { h.TimeoutMS = v }),
				}
			})
		},
		func(k string) {
			i, _ := strconv.Atoi(k)
			if i < len(s.cfg.Hooks.Entries) {
				s.cfg.Hooks.Entries = append(s.cfg.Hooks.Entries[:i], s.cfg.Hooks.Entries[i+1:]...)
			}
		})
	return rootDrillFrame("Hooks", drill)
}

func testConnectionField(s *state, k string) *field {
	fieldID := "providers." + k + ".test_connection"
	return &field{
		id:    fieldID,
		title: "Test connection",
		kind:  kindAction,
		desc:  "ping the provider and list available models",
		actLabel: func() string {
			if as, ok := s.actionState[fieldID]; ok && as.label != "" {
				return as.label
			}
			pc := s.cfg.Providers[k]
			if !isLocalhost(pc.BaseURL) && !s.cfg.Privacy.RemoteProvidersAllowed {
				return "\u2717 blocked (enable Remote providers in Privacy)"
			}
			return "\u21b5 test"
		},
		act: func() tea.Cmd {
			pc := s.cfg.Providers[k]
			if !isLocalhost(pc.BaseURL) && !s.cfg.Privacy.RemoteProvidersAllowed {
				return nil
			}
			s.actionState[fieldID] = actionState{pending: true, label: "\u2026"}
			return probeProvider(fieldID, k, pc)
		},
	}
}

func permissionsFrame(s *state) *frame {
	drill := entriesDrill("permissions", "Permissions", "",
		func() []string { return sliceKeys(len(s.cfg.Permissions.Rules)) },
		func(k string) string {
			i, _ := strconv.Atoi(k)
			r := s.cfg.Permissions.Rules[i]
			return fmt.Sprintf("%s %s → %s", r.Permission, r.Pattern, r.Action)
		},
		func(string) error {
			s.cfg.Permissions.Rules = append(s.cfg.Permissions.Rules, config.PermissionRule{
				Permission: "shell", Pattern: "*", Action: "confirm",
			})
			return nil
		},
		func(k string) *frame {
			i, _ := strconv.Atoi(k)
			return newFrame("rule "+k, func() []*field {
				if i >= len(s.cfg.Permissions.Rules) {
					return nil
				}
				r := &s.cfg.Permissions.Rules[i]
				return []*field{
					scalarField("permissions."+k+".permission", "Permission",
						func() string { return r.Permission },
						func(v string) error { r.Permission = v; return nil }),
					scalarField("permissions."+k+".pattern", "Pattern",
						func() string { return r.Pattern },
						func(v string) error { r.Pattern = v; return nil }),
					enumField("permissions."+k+".action", "Action",
						[]string{"allow", "confirm", "deny"},
						func() string { return r.Action },
						func(v string) { r.Action = v }),
				}
			})
		},
		func(k string) {
			i, _ := strconv.Atoi(k)
			if i < len(s.cfg.Permissions.Rules) {
				s.cfg.Permissions.Rules = append(s.cfg.Permissions.Rules[:i], s.cfg.Permissions.Rules[i+1:]...)
			}
		})
	return rootDrillFrame("Permissions", drill)
}
