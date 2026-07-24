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
			return newFrame(k, func() []*field {
				fields := make([]*field, 0, len(routing.AllRoles))
				for _, role := range routing.AllRoles {
					fields = append(fields, rolePresetField(s, k, role))
				}
				return fields
			})
		},
		func(k string) { delete(s.cfg.AgentProfiles, k) },
		entriesOpts{
			yank: func(k string) any {
				p := s.cfg.AgentProfiles[k]
				p.Roles = maps.Clone(p.Roles)
				return yankedMapEntry{key: k, val: p}
			},
			paste: func(_ string, data any) error {
				ye, ok := data.(yankedMapEntry)
				if !ok {
					return fmt.Errorf("nothing yanked")
				}
				existing := map[string]bool{}
				for kk := range s.cfg.AgentProfiles {
					existing[kk] = true
				}
				name := uniqueCopyName(ye.key, existing)
				if s.cfg.AgentProfiles == nil {
					s.cfg.AgentProfiles = map[string]routing.AgentProfile{}
				}
				p := ye.val.(routing.AgentProfile)
				p.Name = name
				p.Roles = maps.Clone(p.Roles)
				s.cfg.AgentProfiles[name] = p
				return nil
			},
		})
	return rootDrillFrame("Profiles", drill)
}

func rolePresetField(s *state, profile string, role routing.AgentRole) *field {
	get := func() string { return s.cfg.AgentProfiles[profile].Roles[role].Preset }
	set := func(v string) {
		p := s.cfg.AgentProfiles[profile]
		if p.Roles == nil {
			p.Roles = map[routing.AgentRole]routing.RoleBinding{}
		}
		if v == "" {
			delete(p.Roles, role)
		} else {
			p.Roles[role] = routing.RoleBinding{Preset: v}
		}
		p.Name = profile
		s.cfg.AgentProfiles[profile] = p
	}
	desc := "preset for the " + roleTitle(role) + " role · unset falls back to the agent default"
	if preset, ok := s.cfg.Models.Presets[get()]; ok {
		desc = "→ " + preset.Provider + "/" + preset.Model + " · d clears"
	}
	return &field{
		id:       "profiles." + profile + "." + string(role),
		title:    roleTitle(role),
		kind:     kindPicker,
		desc:     desc,
		keywords: []string{"role", "preset", "workflow", string(role)},
		getStr:   get,
		del:      func() { set("") },
		pickOptions: func() []picker.Item {
			names := sortedKeys(s.cfg.Models.Presets)
			if len(names) == 0 {
				return []picker.Item{{Label: "Add a preset first in Model Presets", Value: "__none__", Badge: "required"}}
			}
			current := get()
			items := make([]picker.Item, 0, len(names)+1)
			for _, n := range names {
				p := s.cfg.Models.Presets[n]
				badge := ""
				if n == current {
					badge = "● now"
				}
				items = append(items, picker.Item{Label: n, Detail: p.Provider + "/" + p.Model, Badge: badge, Value: n})
			}
			items = append(items, picker.Item{Label: "(unset — use default)", Value: unsetRoleValue, Badge: "clear"})
			return items
		},
		pickOnPick: func(v string) error {
			switch v {
			case "__none__":
				return fmt.Errorf("add a preset first in the Model Presets section")
			case unsetRoleValue:
				set("")
				return nil
			}
			if _, ok := s.cfg.Models.Presets[v]; !ok {
				return fmt.Errorf("unknown preset %q", v)
			}
			set(v)
			return nil
		},
	}
}
