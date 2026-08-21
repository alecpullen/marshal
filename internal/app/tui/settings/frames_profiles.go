package settings

import (
	"fmt"
	"maps"
	"strings"

	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/strutil"
)

const unsetRoleValue = "__unset__"

const (
	// modelValuePrefix marks a picker value carrying a raw provider+model
	// rather than a preset name: "model:<provider>/<model id>". Split on the
	// FIRST slash only — model ids legitimately contain slashes
	// ("moonshotai/kimi-k2-instruct").
	modelValuePrefix = "model:"
	// refreshModelsValue re-probes every configured provider.
	refreshModelsValue = "__refresh_models__"
)

// findPresetFor returns the preset already saved for providerName+modelID,
// if any — re-picking a model whose limits came back unknown must not
// erase figures confirmed earlier.
func findPresetFor(s *state, providerName, modelID string) (routing.ModelPreset, bool) {
	for _, p := range s.cfg.Models.Presets {
		if p.Provider == providerName && p.Model == modelID {
			return p, true
		}
	}
	return routing.ModelPreset{}, false
}

// probeAllProviders queues a probe for every configured provider not yet
// probed during this browser's lifetime. Remote providers are skipped when
// the privacy gate blocks them, matching the preset model picker.
func probeAllProviders(s *state, force bool) {
	for _, name := range sortedKeys(s.cfg.Providers) {
		pc := s.cfg.Providers[name]
		if !probe.IsLocalhost(pc.BaseURL) && !s.cfg.Privacy.RemoteProvidersAllowed {
			continue
		}
		if force {
			delete(s.probed, name)
		}
		if !s.markProbed(name) {
			continue
		}
		s.queueCmd(probe.Provider("discover.profiles."+name, name, pc, s.dataDir, s.cfg.Privacy.RemoteLimitDiscovery, s.cfg.Agent.ThinkingBudgetMargin))
	}
}

var roleTitles = map[routing.AgentRole]string{
	routing.RoleRouter:            "Router",
	routing.RoleKnowledge:         "Knowledge",
	routing.RoleSummarizer:        "Summarizer",
	routing.RoleRepoScout:         "Repo scout",
	routing.RoleTester:            "Tester",
	routing.RolePlanner:           "Planner",
	routing.RoleImplementer:       "Implementer",
	routing.RoleReviewer:          "Reviewer",
	routing.RoleSecurityReviewer:  "Security reviewer",
	routing.RoleSubtask:           "Subtask",
	routing.RoleTitle:             "Title generator",
	routing.RoleSDDImplementer:    "SDD implementer",
	routing.RoleSDDReviewer:       "SDD reviewer",
	routing.RoleSDDBranchReviewer: "SDD branch reviewer",
}

func roleTitle(role routing.AgentRole) string {
	if t, ok := roleTitles[role]; ok {
		return t
	}
	return string(role)
}

func profilesFrame(s *state) *frame {
	drill := entriesDrillExt("profiles", "Profiles", "New profile name",
		func() []string { return sortedKeys(s.cfg.AgentProfiles) },
		func(k string) string {
			n := len(s.cfg.AgentProfiles[k].Roles)
			noun := "roles"
			if n == 1 {
				noun = "role"
			}
			return fmt.Sprintf("%s  (%d %s)", k, n, noun)
		},
		func(k string) error {
			if k == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.AgentProfiles[k]; ok {
				return fmt.Errorf("entry already exists")
			}
			if s.cfg.AgentProfiles == nil {
				s.cfg.AgentProfiles = map[string]routing.AgentProfile{}
			}
			s.cfg.AgentProfiles[k] = routing.AgentProfile{
				Name:  k,
				Roles: map[routing.AgentRole]routing.RoleBinding{},
			}
			return nil
		},
		func(k string) *frame {
			return newFrame(k, func() []*field { return profileFields(s, k) })
		},
		func(k string) { delete(s.cfg.AgentProfiles, k) },
		entriesOpts{
			Yank: func(k string) any {
				p := s.cfg.AgentProfiles[k]
				p.Roles = maps.Clone(p.Roles)
				return yankedMapEntry{Key: k, Val: p}
			},
			Paste: func(_ string, data any) error {
				ye, ok := data.(yankedMapEntry)
				if !ok {
					return fmt.Errorf("nothing yanked")
				}
				existing := map[string]bool{}
				for kk := range s.cfg.AgentProfiles {
					existing[kk] = true
				}
				name := uniqueCopyName(ye.Key, existing)
				if s.cfg.AgentProfiles == nil {
					s.cfg.AgentProfiles = map[string]routing.AgentProfile{}
				}
				p := ye.Val.(routing.AgentProfile)
				p.Name = name
				p.Roles = maps.Clone(p.Roles)
				s.cfg.AgentProfiles[name] = p
				return nil
			},
		})
	return rootDrillFrame("Profiles", drill)
}

// presetModel renders a preset name as its provider's model id, falling back
// to the preset name when the preset is missing.
func presetModel(s *state, presetName string) string {
	if p, ok := s.cfg.Models.Presets[presetName]; ok && p.Model != "" {
		return p.Model
	}
	return presetName
}

// bindingLabel renders a RoleBinding for display: a custom agent by name,
// otherwise the bound preset's model id.
func bindingLabel(s *state, b routing.RoleBinding) string {
	if b.CustomAgent != "" {
		return b.CustomAgent
	}
	return presetModel(s, b.Preset)
}

// getRoleBinding/setRoleBinding are the single read/write pair for a
// profile role, so every field in this file mutates config the same way.
func getRoleBinding(s *state, profile string, role routing.AgentRole) string {
	return s.cfg.AgentProfiles[profile].Roles[role].Preset
}

func setRoleBinding(s *state, profile string, role routing.AgentRole, preset string) {
	p := s.cfg.AgentProfiles[profile]
	if p.Roles == nil {
		p.Roles = map[routing.AgentRole]routing.RoleBinding{}
	}
	if preset == "" {
		delete(p.Roles, role)
	} else {
		p.Roles[role] = routing.RoleBinding{Preset: preset}
	}
	p.Name = profile
	s.cfg.AgentProfiles[profile] = p
}

// profileFields builds the profile drill-in: the two fallback sources (Base
// and Fast model), then a header, then every dispatchable role. The
// embedding preset moved to [indexing] embedding_preset (see the Indexing
// frame); the router still consults a legacy per-profile embedding role
// binding as a fallback, but it is no longer surfaced here.
func profileFields(s *state, profile string) []*field {
	fields := []*field{
		baseModelField(s, profile),
		fastModelField(s, profile),
		{ID: "profiles." + profile + ".__roles_header", Title: "roles", Kind: kindHeader, Desc: "dispatchable roles — each inherits its model from the slots above"},
	}
	return append(fields, profileRoleFields(s, profile)...)
}

// profileRoleFields lists every dispatchable role except implementer, which
// is surfaced as Base model instead.
func profileRoleFields(s *state, profile string) []*field {
	fields := make([]*field, 0, len(routing.AllRoles))
	for _, role := range routing.AllRoles {
		if role == routing.RoleImplementer {
			continue
		}
		fields = append(fields, roleModelField(s, profile, role))
	}
	return fields
}

func baseModelField(s *state, profile string) *field {
	f := roleModelField(s, profile, routing.RoleImplementer)
	f.Title = "Base model"
	f.Desc = "every role falls back to this model unless it overrides"
	f.Keywords = append(f.Keywords, "base", "default", "implementer")
	f.Display = func() (string, string) {
		preset := getRoleBinding(s, profile, routing.RoleImplementer)
		if preset == "" {
			return "", "required"
		}
		if p, ok := s.cfg.Models.Presets[preset]; ok {
			return p.Model, p.Provider
		}
		return preset, "missing preset"
	}
	return f
}

func fastModelField(s *state, profile string) *field {
	f := roleModelField(s, profile, routing.RoleFast)
	f.Title = "Fast model"
	f.Desc = "optional · router, title, summarizer and repo scout fall back here"
	f.Keywords = append(f.Keywords, "fast", "cheap", "small")
	f.Display = func() (string, string) {
		preset := getRoleBinding(s, profile, routing.RoleFast)
		if preset == "" {
			return "", "optional"
		}
		if p, ok := s.cfg.Models.Presets[preset]; ok {
			return p.Model, p.Provider
		}
		return preset, "missing preset"
	}
	return f
}

// roleModelField builds one role row. GetStr reports the stored binding (so
// an inherited row never reads as modified by the registry or configdiff)
// while Display renders the effective model resolved through the same
// inheritance chain the router uses.
func roleModelField(s *state, profile string, role routing.AgentRole) *field {
	get := func() string { return getRoleBinding(s, profile, role) }
	set := func(v string) { setRoleBinding(s, profile, role, v) }

	desc := "model for the " + roleTitle(role) + " role · unset inherits from Base model"
	if routing.FastRoles[role] {
		desc = "model for the " + roleTitle(role) + " role · unset inherits from Fast model, then Base model"
	}

	return &field{
		ID:       "profiles." + profile + "." + string(role),
		Title:    roleTitle(role),
		Kind:     kindPicker,
		Desc:     desc,
		Keywords: []string{"role", "preset", "model", "workflow", string(role)},
		GetStr:   get,
		Del:      func() { set("") },
		Display: func() (string, string) {
			if own := get(); own != "" {
				return presetModel(s, own), "● override"
			}
			binding, from, ok := s.cfg.AgentProfiles[profile].EffectiveBinding(role)
			if !ok {
				return "", "unset"
			}
			badge := "← base"
			if from == routing.RoleFast {
				badge = "← fast"
			}
			return bindingLabel(s, binding), badge
		},
		PickOptions: func() []picker.Item { return rolePickerItems(s, profile, role) },
		PickOnPick:  func(v string) error { return applyRolePick(s, profile, role, v) },
	}
}

func rolePickerItems(s *state, profile string, role routing.AgentRole) []picker.Item {
	probeAllProviders(s, false)

	current := getRoleBinding(s, profile, role)
	var items []picker.Item

	// picker.Item.Group renders a real group header in the unfiltered view
	// and disappears under a filter — no synthetic unselectable rows.
	for _, n := range sortedKeys(s.cfg.Models.Presets) {
		p := s.cfg.Models.Presets[n]
		badge := ""
		if n == current {
			badge = "● now"
		}
		items = append(items, picker.Item{
			Label: n, Detail: p.Provider + "/" + p.Model,
			Badge: badge, Group: "presets", Value: n,
		})
	}

	for _, providerName := range sortedKeys(s.cfg.Providers) {
		for _, mi := range s.discovered[providerName] {
			items = append(items, picker.Item{
				Label:  mi.ID,
				Detail: limitDetail(mi),
				Group:  providerName + " (discovered)",
				Value:  modelValuePrefix + providerName + "/" + mi.ID,
			})
		}
	}

	items = append(items, picker.Item{Label: unsetLabel(s, profile, role), Value: unsetRoleValue, Badge: "clear"})
	items = append(items, picker.Item{Label: "Refresh discovered models", Value: refreshModelsValue, Badge: "refresh"})
	return items
}

// limitDetail renders a discovered model's limits, or "" when unknown —
// never the number 0, which would read as a real cap.
func limitDetail(mi schema.ModelInfo) string {
	switch {
	case mi.ContextWindow == 0:
		return ""
	case mi.MaxOutputTokens == 0:
		return strutil.CompactTokens(mi.ContextWindow) + " ctx"
	default:
		return strutil.CompactTokens(mi.ContextWindow) + " ctx · " + strutil.CompactTokens(mi.MaxOutputTokens) + " out"
	}
}

// unsetLabel names what the role would fall back to, so "unset" reads as a
// choice rather than a hole.
func unsetLabel(s *state, profile string, role routing.AgentRole) string {
	if routing.FastRoles[role] && getRoleBinding(s, profile, routing.RoleFast) != "" {
		return "(unset — inherit from Fast model)"
	}
	return "(unset — inherit from Base model)"
}

func applyRolePick(s *state, profile string, role routing.AgentRole, v string) error {
	switch {
	case v == unsetRoleValue:
		setRoleBinding(s, profile, role, "")
		return nil
	case v == refreshModelsValue:
		probeAllProviders(s, true)
		return nil
	case strings.HasPrefix(v, modelValuePrefix):
		rest := strings.TrimPrefix(v, modelValuePrefix)
		providerName, modelID, ok := strings.Cut(rest, "/")
		if !ok || providerName == "" || modelID == "" {
			return fmt.Errorf("malformed model selection %q", v)
		}
		s.requestMaterialization(profile, role, providerName, modelID)
		return nil
	}
	if _, ok := s.cfg.Models.Presets[v]; !ok {
		return fmt.Errorf("unknown preset %q", v)
	}
	setRoleBinding(s, profile, role, v)
	return nil
}
