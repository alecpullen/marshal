package settings

import (
	"fmt"

	"charm.land/huh/v2"

	"marshal/internal/app/config"
)

type providerEntry struct {
	key string
	cfg config.ProviderConfig
}

func (p providerEntry) Title() string {
	return p.key + "  (" + maskKey(p.cfg.APIKey) + ")"
}
func (p providerEntry) Key() string { return p.key }

func newProvidersPane(s *state) sectionPane {
	spec := collectionSpec{
		heading:   "Providers",
		keyPrompt: "New provider name",
		entries: func(s *state) []collectionEntry {
			out := make([]collectionEntry, 0, len(s.cfg.Providers))
			for k, pc := range s.cfg.Providers {
				out = append(out, providerEntry{key: k, cfg: pc})
			}
			return out
		},
		add: func(s *state, key string) error {
			if key == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.Providers[key]; ok {
				return fmt.Errorf("entry already exists")
			}
			if s.cfg.Providers == nil {
				s.cfg.Providers = make(map[string]config.ProviderConfig)
			}
			s.cfg.Providers[key] = config.ProviderConfig{Type: "openai_compatible"}
			return nil
		},
		editForm: func(s *state, key string) (*huh.Form, func()) {
			local := s.cfg.Providers[key] // copy
			local.APIKey = s.cfg.Providers[key].APIKey
			keyInput, keyClear := secretField("API key",
				func() string { return s.cfg.Providers[key].APIKey },
				func(v string) { local.APIKey = v })
			form := newSectionForm(
				huh.NewInput().Key("Type").Title("Type").Value(&local.Type),
				huh.NewInput().Key("Base URL").Title("Base URL").Value(&local.BaseURL),
				huh.NewInput().Key("API key env").Title("API key env").
					Description("env var name resolved at provider construction — preferred over storing the key").
					Value(&local.APIKeyEnv),
				keyInput,
				keyClear,
				huh.NewConfirm().Key("Tool calling").Title("Tool calling").
					Description("provider advertises native tool-calling support").
					Value(&local.ToolCalling),
			)
			return form, func() { s.cfg.Providers[key] = local }
		},
		delete: func(s *state, key string) { delete(s.cfg.Providers, key) },
	}
	return newCollectionPane(s, spec)
}
