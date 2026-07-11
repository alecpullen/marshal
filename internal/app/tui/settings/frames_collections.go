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
	drill := entriesDrillExt("providers", "Providers", "New provider name",
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
						setStr: func(v string) error {
							mut(func(p *config.ProviderConfig) { p.APIKeyEnv = v })
							invalidate()
							return nil
						}},
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
		func(k string) { delete(s.cfg.Providers, k) },
		entriesOpts{
			yank: func(k string) any {
				return yankedMapEntry{key: k, val: s.cfg.Providers[k]}
			},
			paste: func(_ string, data any) error {
				ye, ok := data.(yankedMapEntry)
				if !ok {
					return fmt.Errorf("nothing yanked")
				}
				existing := map[string]bool{}
				for kk := range s.cfg.Providers {
					existing[kk] = true
				}
				name := uniqueCopyName(ye.key, existing)
				if s.cfg.Providers == nil {
					s.cfg.Providers = map[string]config.ProviderConfig{}
				}
				s.cfg.Providers[name] = ye.val.(config.ProviderConfig)
				return nil
			},
		})
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
			fieldID:     wizardFieldID,
			items:       items,
			title:       "Add provider",
			footer:      "pick a template",
			allowCustom: true,
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
				s.wizardCreatedProvider = name
				return nil
			},
		}
	}
}

func providerPickerField(s *state, id string, getProvider func() string, setProvider func(string) error) *field {
	return &field{
		id:     id,
		title:  "Provider",
		kind:   kindPicker,
		desc:   "configured provider for this role",
		getStr: func() string { return getProvider() },
		pickOptions: func() []picker.Item {
			names := sortedKeys(s.cfg.Providers)
			if len(names) == 0 {
				return []picker.Item{{Label: "Add a provider\u2026", Value: "__add_provider__", Badge: "required"}}
			}
			items := make([]picker.Item, 0, len(names))
			current := getProvider()
			for _, n := range names {
				badge := ""
				if n == current {
					badge = "\u25cf now"
				}
				if isLocalhost(s.cfg.Providers[n].BaseURL) {
					if badge != "" {
						badge += " "
					}
					badge += "local"
				}
				items = append(items, picker.Item{Label: n, Value: n, Badge: badge})
			}
			return items
		},
		pickOnPick: func(v string) error {
			if v == "__add_provider__" {
				return fmt.Errorf("add a provider first in the Providers section")
			}
			return setProvider(v)
		},
	}
}

func modelPickerField(s *state, id string, providerName func() string, getModel func() string, setModel func(string) error) *field {
	return &field{
		id:              id,
		title:           "Model",
		kind:            kindPicker,
		desc:            "model id for this role",
		pickAllowCustom: true,
		getStr:          func() string { return getModel() },
		pickOptions: func() []picker.Item {
			pn := providerName()
			current := getModel()
			var items []picker.Item
			if cached, ok := s.discovered[pn]; ok && len(cached) > 0 {
				for _, m := range cached {
					badge := "\u25c9 discovered"
					if m == current {
						badge = "\u25cf now \u25c9 discovered"
					}
					items = append(items, picker.Item{Label: m, Value: m, Badge: badge})
				}
			} else if tpl, ok := provider.Lookup(pn); ok && len(tpl.Models) > 0 {
				for _, m := range tpl.Models {
					badge := "\u25cb catalog"
					if m == current {
						badge = "\u25cf now \u25cb catalog"
					}
					items = append(items, picker.Item{Label: m, Value: m, Badge: badge})
				}
			} else {
				items = []picker.Item{{Label: "Test connection to discover", Value: "__discover__", Badge: "refresh"}}
			}
			return items
		},
		pickOnPick: func(v string) error {
			if v == "__discover__" {
				return fmt.Errorf("test the provider connection first to discover models")
			}
			return setModel(v)
		},
	}
}

func badgeForTemplate(tpl provider.ProviderTemplate) string {
	if tpl.Local {
		return "local"
	}
	return "remote"
}

func uniqueCopyName(base string, existing map[string]bool) string {
	candidate := base + "-copy"
	if !existing[candidate] {
		return candidate
	}
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s-copy-%d", base, i)
		if !existing[c] {
			return c
		}
	}
}

func insertHook(e []config.HookConfig, at int, hc config.HookConfig) []config.HookConfig {
	if at < 0 {
		at = 0
	}
	if at > len(e) {
		at = len(e)
	}
	out := make([]config.HookConfig, 0, len(e)+1)
	out = append(out, e[:at]...)
	out = append(out, hc)
	out = append(out, e[at:]...)
	return out
}

func presetsFrame(s *state) *frame {
	drill := entriesDrillExt("presets", "Model Presets", "New preset name",
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
					providerPickerField(s, "presets."+k+".provider",
						func() string { return s.cfg.Models.Presets[k].Provider },
						func(v string) error { mut(func(p *routing.ModelPreset) { p.Provider = v }); return nil }),
					modelPickerField(s, "presets."+k+".model",
						func() string { return s.cfg.Models.Presets[k].Provider },
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
		func(k string) { delete(s.cfg.Models.Presets, k) },
		entriesOpts{
			yank: func(k string) any {
				return yankedMapEntry{key: k, val: s.cfg.Models.Presets[k]}
			},
			paste: func(_ string, data any) error {
				ye, ok := data.(yankedMapEntry)
				if !ok {
					return fmt.Errorf("nothing yanked")
				}
				existing := map[string]bool{}
				for kk := range s.cfg.Models.Presets {
					existing[kk] = true
				}
				name := uniqueCopyName(ye.key, existing)
				if s.cfg.Models.Presets == nil {
					s.cfg.Models.Presets = map[string]routing.ModelPreset{}
				}
				s.cfg.Models.Presets[name] = ye.val.(routing.ModelPreset)
				return nil
			},
		})
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
	drill := entriesDrillExt("hooks", "Hooks", "",
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
		},
		entriesOpts{
			moveUp: func(k string) {
				i, _ := strconv.Atoi(k)
				if i <= 0 {
					return
				}
				e := s.cfg.Hooks.Entries
				e[i-1], e[i] = e[i], e[i-1]
			},
			moveDown: func(k string) {
				i, _ := strconv.Atoi(k)
				e := s.cfg.Hooks.Entries
				if i >= len(e)-1 {
					return
				}
				e[i+1], e[i] = e[i], e[i+1]
			},
			yank: func(k string) any {
				i, _ := strconv.Atoi(k)
				if i >= len(s.cfg.Hooks.Entries) {
					return nil
				}
				return s.cfg.Hooks.Entries[i]
			},
			paste: func(k string, data any) error {
				hc, ok := data.(config.HookConfig)
				if !ok {
					return fmt.Errorf("nothing yanked")
				}
				i, _ := strconv.Atoi(k)
				s.cfg.Hooks.Entries = insertHook(s.cfg.Hooks.Entries, i+1, hc)
				return nil
			},
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
	drill := entriesDrillExt("permissions", "Permissions", "",
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
		},
		entriesOpts{
			moveUp: func(k string) {
				i, _ := strconv.Atoi(k)
				if i <= 0 {
					return
				}
				r := s.cfg.Permissions.Rules
				r[i-1], r[i] = r[i], r[i-1]
			},
			moveDown: func(k string) {
				i, _ := strconv.Atoi(k)
				r := s.cfg.Permissions.Rules
				if i >= len(r)-1 {
					return
				}
				r[i+1], r[i] = r[i], r[i+1]
			},
			yank: func(k string) any {
				i, _ := strconv.Atoi(k)
				if i >= len(s.cfg.Permissions.Rules) {
					return nil
				}
				return s.cfg.Permissions.Rules[i]
			},
			paste: func(k string, data any) error {
				rr, ok := data.(config.PermissionRule)
				if !ok {
					return fmt.Errorf("nothing yanked")
				}
				i, _ := strconv.Atoi(k)
				s.cfg.Permissions.Rules = insertRule(s.cfg.Permissions.Rules, i+1, rr)
				return nil
			},
		})
	return rootDrillFrame("Permissions", drill)
}

func insertRule(r []config.PermissionRule, at int, rr config.PermissionRule) []config.PermissionRule {
	if at < 0 {
		at = 0
	}
	if at > len(r) {
		at = len(r)
	}
	out := make([]config.PermissionRule, 0, len(r)+1)
	out = append(out, r[:at]...)
	out = append(out, rr)
	out = append(out, r[at:]...)
	return out
}
