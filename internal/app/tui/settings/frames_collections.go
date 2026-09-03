package settings

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/strutil"
)

// rootDrillFrame unwraps a single drill field into a section root frame, so
// sections that are nothing but one collection open directly on the list.
func rootDrillFrame(title string, drill *field) *frame {
	f := drill.Build()
	f.Title = title
	return f
}

func providersFrame(s *state) *frame {
	drill := entriesDrillExt("providers", "Providers", "New provider name",
		func() []string { return sortedKeys(s.cfg.Providers) },
		func(k string) string {
			pc := s.cfg.Providers[k]
			locality := "remote"
			if probe.IsLocalhost(pc.BaseURL) {
				locality = "local"
			}
			keySource := "no key"
			switch {
			case pc.APIKeyEnv != "":
				keySource = "$" + pc.APIKeyEnv
			case pc.APIKey != "":
				keySource = "key stored"
			}
			return fmt.Sprintf("%s  %s · %s · %s",
				k, strutil.Truncate(pc.BaseURL, 28, true), locality, keySource)
		},
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
				nameField := scalarField("providers."+k+".name", "Name",
					func() string { return k },
					func(v string) error {
						v = strings.TrimSpace(v)
						if v == "" {
							return fmt.Errorf("name cannot be empty")
						}
						if v == k {
							return nil
						}
						if err := config.RenameProvider(&s.cfg, k, v, s.discovered); err != nil {
							return err
						}
						if s.dataDir != "" {
							c := modelcache.Load(s.dataDir)
							if e, ok := c.Providers[k]; ok {
								delete(c.Providers, k)
								c.Providers[v] = e
								if err := modelcache.Save(s.dataDir, c); err != nil {
									return fmt.Errorf("rename model cache: %w", err)
								}
							}
						}
						k = v
						return nil
					})
				nameField.Desc = "unique provider name used in presets and profiles"
				typeField := scalarField("providers."+k+".type", "Type",
					func() string { return s.cfg.Providers[k].Type },
					func(v string) error { mut(func(p *config.ProviderConfig) { p.Type = v }); return nil })
				typeField.Desc = "provider protocol (openai_compatible, anthropic, etc.)"
				urlField := scalarField("providers."+k+".base_url", "Base URL",
					func() string { return s.cfg.Providers[k].BaseURL },
					func(v string) error { mut(func(p *config.ProviderConfig) { p.BaseURL = v }); invalidate(); return nil })
				urlField.Desc = "API endpoint URL for this provider"
				return []*field{
					nameField,
					typeField,
					urlField,
					func() *field {
						f := &field{ID: "providers." + k + ".api_key_env", Title: "API key env", Kind: kindScalar,
							TomlPath: "providers." + k + ".api_key_env",
							Desc:     "env var name resolved at provider construction — preferred over storing the key",
							GetStr:   func() string { return s.cfg.Providers[k].APIKeyEnv },
							SetStr: func(v string) error {
								mut(func(p *config.ProviderConfig) { p.APIKeyEnv = v })
								invalidate()
								return nil
							}}
						SetFieldWriteGlobal(f, true)
						return f
					}(),
					func() *field {
						f := &field{ID: "providers." + k + ".api_key", Title: "API key", Kind: kindScalar, Masked: true,
							TomlPath: "providers." + k + ".api_key",
							Desc:     "enter replaces · empty keeps · d clears · prefer the env-var field",
							Keywords: []string{"secret", "api key", "token"},
							GetStr:   func() string { return s.cfg.Providers[k].APIKey },
							SetStr: func(v string) error {
								mut(func(p *config.ProviderConfig) { p.APIKey = v })
								invalidate()
								return nil
							},
							Del: func() {
								mut(func(p *config.ProviderConfig) { p.APIKey = "" })
								invalidate()
							}}
						SetFieldWriteGlobal(f, true)
						return f
					}(),
					{ID: "providers." + k + ".tool_calling", Title: "Tool calling", Kind: kindToggle,
						Desc:    "provider advertises native tool-calling support",
						GetBool: func() bool { return s.cfg.Providers[k].ToolCalling },
						SetBool: func(v bool) { mut(func(p *config.ProviderConfig) { p.ToolCalling = v }) }},
					{ID: "providers." + k + ".structured_output", Title: "Structured output", Kind: kindToggle,
						Desc:    "provider advertises JSON-schema structured output; enable only if the endpoint enforces format (local Ollama does, ollama.com cloud does not); takes effect on new sessions",
						GetBool: func() bool { return s.cfg.Providers[k].StructuredOutput },
						SetBool: func(v bool) { mut(func(p *config.ProviderConfig) { p.StructuredOutput = v }) }},
					testConnectionField(s, k),
					{ID: "providers." + k + ".options", Title: "Model options", Kind: kindAction,
						Desc: "edit context window / max output / tool calling for each model pair using this provider",
						Act: func() tea.Cmd {
							return func() tea.Msg {
								return OpenModelOptionsForProviderMsg{ProviderName: k}
							}
						}},
				}
			})
		},
		func(k string) { delete(s.cfg.Providers, k) },
		entriesOpts{
			Yank: func(k string) any {
				return yankedMapEntry{Key: k, Val: s.cfg.Providers[k]}
			},
			Paste: func(_ string, data any) error {
				ye, ok := data.(yankedMapEntry)
				if !ok {
					return fmt.Errorf("nothing yanked")
				}
				existing := map[string]bool{}
				for kk := range s.cfg.Providers {
					existing[kk] = true
				}
				name := uniqueCopyName(ye.Key, existing)
				if s.cfg.Providers == nil {
					s.cfg.Providers = map[string]config.ProviderConfig{}
				}
				s.cfg.Providers[name] = ye.Val.(config.ProviderConfig)
				return nil
			},
		})
	f := rootDrillFrame("Providers", drill)
	f.List.OnAddMsg = func() tea.Msg { return OpenConnectMsg{} }
	return f
}

func providerPickerField(s *state, id string, getProvider func() string, setProvider func(string) error) *field {
	return &field{
		ID:     id,
		Title:  "Provider",
		Kind:   kindPicker,
		Desc:   "configured provider for this role",
		GetStr: func() string { return getProvider() },
		PickOptions: func() []picker.Item {
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
				if probe.IsLocalhost(s.cfg.Providers[n].BaseURL) {
					if badge != "" {
						badge += " "
					}
					badge += "local"
				}
				items = append(items, picker.Item{Label: n, Value: n, Badge: badge})
			}
			return items
		},
		PickOnPick: func(v string) error {
			if v == "__add_provider__" {
				s.connectRequested = true
				return nil
			}
			return setProvider(v)
		},
	}
}

func modelPickerField(s *state, id string, providerName func() string, getModel func() string, setModel func(string) error) *field {
	return &field{
		ID:              id,
		Title:           "Model",
		Kind:            kindPicker,
		Desc:            "model id for this role",
		PickAllowCustom: true,
		GetStr:          func() string { return getModel() },
		PickOptions: func() []picker.Item {
			pn := providerName()
			current := getModel()
			var items []picker.Item
			if cached, ok := s.discovered[pn]; ok && len(cached) > 0 {
				for _, mi := range cached {
					badge := "◉ discovered"
					if mi.ID == current {
						badge = "● now ◉ discovered"
					}
					items = append(items, picker.Item{Label: mi.ID, Value: mi.ID, Badge: badge})
				}
			} else if pc, ok := s.cfg.Providers[pn]; ok {
				tplID := pn
				if pc.Template != "" {
					tplID = pc.Template
				}
				if tpl, ok := provider.Lookup(tplID); ok && len(tpl.Models) > 0 {
					for _, mdl := range tpl.Models {
						badge := "\u25cb catalog"
						if mdl == current {
							badge = "\u25cf now \u25cb catalog"
						}
						items = append(items, picker.Item{Label: mdl, Value: mdl, Badge: badge})
					}
				}
			} else {
				items = []picker.Item{{Label: "Discover models via probe", Value: "__discover__", Badge: "refresh"}}
			}
			return items
		},
		PickOnPick: func(v string) error {
			if v == "__discover__" {
				pn := providerName()
				pc, ok := s.cfg.Providers[pn]
				if !ok {
					return fmt.Errorf("provider %q is not configured", pn)
				}
				if !probe.IsLocalhost(pc.BaseURL) && !s.cfg.Privacy.RemoteProvidersAllowed {
					return fmt.Errorf("remote providers are disabled (enable Remote providers in Privacy)")
				}
				s.pendingCmd = probe.Provider("discover."+id, pn, pc, s.dataDir, s.cfg.Privacy.RemoteLimitDiscovery, s.cfg.Agent.ThinkingBudgetMargin)
				return nil
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
				eventField := scalarField("hooks."+k+".event", "Event",
					func() string { return h.Event },
					func(v string) error { h.Event = v; return nil })
				eventField.Desc = "hook event trigger (e.g. pre_tool, post_tool)"
				matcherField := scalarField("hooks."+k+".matcher", "Matcher",
					func() string { return h.Matcher },
					func(v string) error { h.Matcher = v; return nil })
				matcherField.Desc = "pattern to match against the event payload"
				cmdField := scalarField("hooks."+k+".command", "Command",
					func() string { return h.Command },
					func(v string) error { h.Command = v; return nil })
				cmdField.Desc = "shell command to run when the hook fires"
				timeoutField := intField("hooks."+k+".timeout_ms", "Timeout (ms)",
					func() int { return h.TimeoutMS }, 0, func(v int) { h.TimeoutMS = v })
				timeoutField.Desc = "max milliseconds for hook execution"
				return []*field{
					eventField,
					matcherField,
					cmdField,
					timeoutField,
				}
			})
		},
		func(k string) {
			i, _ := strconv.Atoi(k)
			if i < len(s.cfg.Hooks.Entries) {
				s.cfg.Hooks.Entries = append(s.cfg.Hooks.Entries[:i], s.cfg.Hooks.Entries[i+1:]...)
			}
		},
		sliceOpts(&s.cfg.Hooks.Entries))
	return rootDrillFrame("Hooks", drill)
}

func testConnectionField(s *state, k string) *field {
	fieldID := "providers." + k + ".test_connection"
	return &field{
		ID:    fieldID,
		Title: "Test connection",
		Kind:  kindAction,
		Desc:  "ping the provider and list available models",
		ActLabel: func() string {
			if as, ok := s.actionState[fieldID]; ok && as.label != "" {
				return as.label
			}
			pc := s.cfg.Providers[k]
			if !probe.IsLocalhost(pc.BaseURL) && !s.cfg.Privacy.RemoteProvidersAllowed {
				return "\u2717 blocked (enable Remote providers in Privacy)"
			}
			return "\u21b5 test"
		},
		Act: func() tea.Cmd {
			pc := s.cfg.Providers[k]
			if !probe.IsLocalhost(pc.BaseURL) && !s.cfg.Privacy.RemoteProvidersAllowed {
				return nil
			}
			s.actionState[fieldID] = actionState{label: "\u2026"}
			return probe.Provider(fieldID, k, pc, s.dataDir, s.cfg.Privacy.RemoteLimitDiscovery, s.cfg.Agent.ThinkingBudgetMargin)
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
				permField := scalarField("permissions."+k+".permission", "Permission",
					func() string { return r.Permission },
					func(v string) error { r.Permission = v; return nil })
				permField.Desc = "permission name this rule applies to (e.g. shell)"
				patField := scalarField("permissions."+k+".pattern", "Pattern",
					func() string { return r.Pattern },
					func(v string) error { r.Pattern = v; return nil })
				patField.Desc = "glob pattern the rule matches"
				actField := enumField("permissions."+k+".action", "Action",
					[]string{"allow", "confirm", "deny"},
					func() string { return r.Action },
					func(v string) { r.Action = v })
				actField.Desc = "action to take when the rule matches"
				return []*field{
					permField,
					patField,
					actField,
				}
			})
		},
		func(k string) {
			i, _ := strconv.Atoi(k)
			if i < len(s.cfg.Permissions.Rules) {
				s.cfg.Permissions.Rules = append(s.cfg.Permissions.Rules[:i], s.cfg.Permissions.Rules[i+1:]...)
			}
		},
		sliceOpts(&s.cfg.Permissions.Rules))
	return rootDrillFrame("Permissions", drill)
}
