package settings

import (
	"fmt"
	"maps"

	"marshal/internal/app/tui/picker"
	"marshal/internal/llm/routing"
)

const unsetRoleValue = "__unset__"

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
	routing.RoleEmbedding:         "Embeddings",
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

// profileFields builds the profile drill-in: the two fallback sources and
// the embedding slot up top, then a header, then every dispatchable role.
func profileFields(s *state, profile string) []*field {
	fields := []*field{
		baseModelField(s, profile),
		fastModelField(s, profile),
		roleModelField(s, profile, routing.RoleEmbedding),
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
	if role == routing.RoleEmbedding {
		// ResolveEmbedding has no fallback: unset means the feature is off,
		// not that a default kicks in.
		desc = "model used to embed code for semantic search · unset disables embeddings"
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
			if role == routing.RoleEmbedding {
				return "", "off"
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

// rolePickerItems and applyRolePick are completed in Task 9; this task keeps
// the preset-only behavior so the frame stays usable between commits.
func rolePickerItems(s *state, profile string, role routing.AgentRole) []picker.Item {
	names := sortedKeys(s.cfg.Models.Presets)
	if len(names) == 0 {
		return []picker.Item{{Label: "Add a preset first in Model Presets", Value: "__none__", Badge: "required"}}
	}
	current := getRoleBinding(s, profile, role)
	items := make([]picker.Item, 0, len(names)+1)
	for _, n := range names {
		p := s.cfg.Models.Presets[n]
		badge := ""
		if n == current {
			badge = "● now"
		}
		items = append(items, picker.Item{Label: n, Detail: p.Provider + "/" + p.Model, Badge: badge, Value: n})
	}
	return append(items, picker.Item{Label: unsetLabel(s, profile, role), Value: unsetRoleValue, Badge: "clear"})
}

// unsetLabel names what the role would fall back to, so "unset" reads as a
// choice rather than a hole.
func unsetLabel(s *state, profile string, role routing.AgentRole) string {
	if role == routing.RoleEmbedding {
		return "(unset — disables embeddings)"
	}
	if routing.FastRoles[role] && getRoleBinding(s, profile, routing.RoleFast) != "" {
		return "(unset — inherit from Fast model)"
	}
	return "(unset — inherit from Base model)"
}

func applyRolePick(s *state, profile string, role routing.AgentRole, v string) error {
	switch v {
	case "__none__":
		return fmt.Errorf("add a preset first in the Model Presets section")
	case unsetRoleValue:
		setRoleBinding(s, profile, role, "")
		return nil
	}
	if _, ok := s.cfg.Models.Presets[v]; !ok {
		return fmt.Errorf("unknown preset %q", v)
	}
	setRoleBinding(s, profile, role, v)
	return nil
}
