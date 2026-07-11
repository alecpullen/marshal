package settings

import (
	"fmt"
	"strconv"

	"charm.land/huh/v2"

	"marshal/internal/app/config"
)

type hookEntry struct {
	idx  int
	hook config.HookConfig
}

func (h hookEntry) Title() string {
	return fmt.Sprintf("%s %s → %s", h.hook.Event, h.hook.Matcher, h.hook.Command)
}

func (h hookEntry) Key() string { return fmt.Sprintf("hook-%d", h.idx) }

func newHooksPane(s *state) sectionPane {
	spec := collectionSpec{
		heading:   "Hooks",
		keyPrompt: "",
		entries: func(s *state) []collectionEntry {
			out := make([]collectionEntry, 0, len(s.cfg.Hooks.Entries))
			for i, h := range s.cfg.Hooks.Entries {
				out = append(out, hookEntry{idx: i, hook: h})
			}
			return out
		},
		add: func(s *state, _ string) error {
			s.cfg.Hooks.Entries = append(s.cfg.Hooks.Entries, config.HookConfig{Event: "pre_tool"})
			return nil
		},
		editForm: func(s *state, key string) (*huh.Form, func()) {
			idx, _ := sliceIndexFromKey(key, "hook-")
			if idx < 0 || idx >= len(s.cfg.Hooks.Entries) {
				idx = 0
			}
			local := s.cfg.Hooks.Entries[idx]
			b := &struct{ timeout string }{timeout: strconv.Itoa(local.TimeoutMS)}
			form := newSectionForm(
				huh.NewInput().Key("Event").Title("Event").Value(&local.Event),
				huh.NewInput().Key("Matcher").Title("Matcher").Value(&local.Matcher),
				huh.NewInput().Key("Command").Title("Command").Value(&local.Command),
				numField("Timeout (ms)", &b.timeout, 0, func(v int) { local.TimeoutMS = v }),
			)
			return form, func() { s.cfg.Hooks.Entries[idx] = local }
		},
		delete: func(s *state, key string) {
			idx, _ := sliceIndexFromKey(key, "hook-")
			if idx < 0 || idx >= len(s.cfg.Hooks.Entries) {
				return
			}
			s.cfg.Hooks.Entries = append(s.cfg.Hooks.Entries[:idx], s.cfg.Hooks.Entries[idx+1:]...)
		},
	}
	return newCollectionPane(s, spec)
}
