package settings

import (
	"fmt"

	"charm.land/huh/v2"

	"marshal/internal/app/config"
)

type ruleEntry struct {
	idx  int
	rule config.PermissionRule
}

func (r ruleEntry) Title() string {
	return fmt.Sprintf("%s %s → %s", r.rule.Permission, r.rule.Pattern, r.rule.Action)
}

func (r ruleEntry) Key() string { return fmt.Sprintf("rule-%d", r.idx) }

func newPermissionsPane(s *state) sectionPane {
	spec := collectionSpec{
		heading:   "Permissions",
		keyPrompt: "",
		entries: func(s *state) []collectionEntry {
			out := make([]collectionEntry, 0, len(s.cfg.Permissions.Rules))
			for i, r := range s.cfg.Permissions.Rules {
				out = append(out, ruleEntry{idx: i, rule: r})
			}
			return out
		},
		add: func(s *state, _ string) error {
			s.cfg.Permissions.Rules = append(s.cfg.Permissions.Rules, config.PermissionRule{
				Permission: "shell",
				Pattern:    "*",
				Action:     "confirm",
			})
			return nil
		},
		editForm: func(s *state, key string) (*huh.Form, func()) {
			idx, _ := sliceIndexFromKey(key, "rule-")
			if idx < 0 || idx >= len(s.cfg.Permissions.Rules) {
				idx = 0
			}
			local := s.cfg.Permissions.Rules[idx]
			form := newSectionForm(
				huh.NewInput().Key("Permission").Title("Permission").Value(&local.Permission),
				huh.NewInput().Key("Pattern").Title("Pattern").Value(&local.Pattern),
				huh.NewSelect[string]().Key("Action").Title("Action").
					Options(
						huh.NewOption("allow", "allow"),
						huh.NewOption("confirm", "confirm"),
						huh.NewOption("deny", "deny"),
					).
					Value(&local.Action),
			)
			return form, func() { s.cfg.Permissions.Rules[idx] = local }
		},
		delete: func(s *state, key string) {
			idx, _ := sliceIndexFromKey(key, "rule-")
			if idx < 0 || idx >= len(s.cfg.Permissions.Rules) {
				return
			}
			s.cfg.Permissions.Rules = append(s.cfg.Permissions.Rules[:idx], s.cfg.Permissions.Rules[idx+1:]...)
		},
	}
	return newCollectionPane(s, spec)
}
