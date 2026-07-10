package settings

import (
	"fmt"
	"sort"
	"strconv"

	"charm.land/huh/v2"

	"marshal/internal/llm/routing"
)

type presetEntry struct {
	key    string
	preset routing.ModelPreset
}

func (p presetEntry) Title() string {
	return p.key + "  (" + p.preset.Provider + "/" + p.preset.Model + ")"
}
func (p presetEntry) Key() string { return p.key }

func newPresetsPane(s *state) sectionPane {
	spec := collectionSpec{
		heading:   "Model Presets",
		keyPrompt: "New preset name",
		entries: func(s *state) []collectionEntry {
			keys := make([]string, 0, len(s.cfg.Models.Presets))
			for k := range s.cfg.Models.Presets {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			out := make([]collectionEntry, 0, len(keys))
			for _, k := range keys {
				out = append(out, presetEntry{key: k, preset: s.cfg.Models.Presets[k]})
			}
			return out
		},
		add: func(s *state, key string) error {
			if key == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.Models.Presets[key]; ok {
				return fmt.Errorf("entry already exists")
			}
			s.cfg.Models.Presets[key] = routing.ModelPreset{Name: key}
			return nil
		},
		editForm: func(s *state, key string) (*huh.Form, func()) {
			local := s.cfg.Models.Presets[key]
			b := struct {
				ctx, maxOut, temp, topP string
			}{
				ctx:    strconv.Itoa(local.ContextWindow),
				maxOut: strconv.Itoa(local.MaxOutputTokens),
				temp:   strconv.FormatFloat(local.Temperature, 'f', -1, 64),
				topP:   strconv.FormatFloat(local.TopP, 'f', -1, 64),
			}
			form := newSectionForm(
				huh.NewInput().Key("Provider").Title("Provider").Value(&local.Provider),
				huh.NewInput().Key("Model").Title("Model").Value(&local.Model),
				numField("Context window", &b.ctx, 0, func(v int) { local.ContextWindow = v }),
				numField("Max output tokens", &b.maxOut, 0, func(v int) { local.MaxOutputTokens = v }),
				floatField("Temperature", &b.temp, func(v float64) { local.Temperature = v }),
				floatField("Top P", &b.topP, func(v float64) { local.TopP = v }),
				huh.NewSelect[string]().Key("Tool calling").Title("Tool calling").
					Options(huh.NewOption("native", "native"), huh.NewOption("simulated", "simulated"), huh.NewOption("none", "none")).
					Value(&local.ToolCalling),
				huh.NewSelect[string]().Key("Reasoning effort").Title("Reasoning effort").
					Options(huh.NewOption("low", "low"), huh.NewOption("medium", "medium"), huh.NewOption("high", "high"), huh.NewOption("none", "none")).
					Value(&local.ReasoningEffort),
				huh.NewConfirm().Key("Local only").Title("Local only").
					Description("block remote providers for this preset").
					Value(&local.LocalOnly),
			)
			return form, func() { s.cfg.Models.Presets[key] = local }
		},
		delete: func(s *state, key string) { delete(s.cfg.Models.Presets, key) },
	}
	return newCollectionPane(s, spec)
}
