package modeloptions

import (
	"fmt"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/listpanel"
	"marshal/internal/app/tui/theme"
	"marshal/internal/llm/routing"
)

// ChangedMsg is emitted when the panel commits a new value. The included
// Config is a copy of the original with the active preset mutated; the
// receiver decides whether to persist and reload it.
type ChangedMsg struct {
	Config     config.Config
	PresetName string
	FieldID    string
	OldValue   string
	NewValue   string
}

// ClosedMsg is emitted when the user closes the panel at its root.
type ClosedMsg struct{}

// descriptor describes a single editable scalar row in the panel.
type descriptor struct {
	id          string
	title       string
	description string
	get         func(routing.ModelPreset) int
	set         func(*routing.ModelPreset, int)
}

// Panel is a focused, docked editor for the active model preset's context
// window and max output tokens. It owns a private copy of the config and
// emits ChangedMsg with a fresh candidate copy whenever a value commits.
type Panel struct {
	cfg        config.Config
	presetName string
	fields     *listpanel.FieldList

	changed bool
}

var _ dock.Panel = (*Panel)(nil)

// New creates a model-options panel for the named preset in the provided
// config. Changes mutate an internal copy of cfg; callers receive a fresh
// copy in ChangedMsg.Config.
func New(cfg config.Config, presetName string) *Panel {
	cfg = cloneConfig(cfg)
	p := &Panel{
		cfg:        cfg,
		presetName: presetName,
	}
	p.fields = listpanel.NewFieldList(p.buildFields)
	return p
}

func (p *Panel) buildFields() []*listpanel.Field {
	ds := p.descriptors()
	fields := make([]*listpanel.Field, len(ds))
	for i, d := range ds {
		d := d
		fields[i] = listpanel.ScalarField(d.id, d.title,
			func() string {
				preset := p.cfg.Models.Presets[p.presetName]
				return strconv.Itoa(d.get(preset))
			},
			func(s string) error {
				v, err := strconv.Atoi(s)
				if err != nil {
					return fmt.Errorf("must be a number")
				}
				if v < 0 {
					return fmt.Errorf("must be 0 or positive")
				}
				preset := p.cfg.Models.Presets[p.presetName]
				old := strconv.Itoa(d.get(preset))
				d.set(&preset, v)
				p.cfg.Models.Presets[p.presetName] = preset
				p.changed = true
				_ = old
				return nil
			},
		)
		fields[i].Desc = d.description
		fields[i].Display = func() (string, string) {
			preset := p.cfg.Models.Presets[p.presetName]
			v := d.get(preset)
			if v == 0 {
				return "automatic", ""
			}
			return strconv.Itoa(v), ""
		}
	}
	return fields
}

func (p *Panel) descriptors() []descriptor {
	return []descriptor{
		{
			id:          "context_window",
			title:       "Context window",
			description: "Maximum context size in tokens; 0 uses automatic resolution.",
			get:         func(m routing.ModelPreset) int { return m.ContextWindow },
			set:         func(m *routing.ModelPreset, v int) { m.ContextWindow = v },
		},
		{
			id:          "max_output_tokens",
			title:       "Max output tokens",
			description: "Maximum output tokens per turn; 0 uses automatic resolution.",
			get:         func(m routing.ModelPreset) int { return m.MaxOutputTokens },
			set:         func(m *routing.ModelPreset, v int) { m.MaxOutputTokens = v },
		},
	}
}

// Config returns the panel's current working config copy.
func (p *Panel) Config() config.Config { return cloneConfig(p.cfg) }

// PresetName returns the edited preset name.
func (p *Panel) PresetName() string { return p.presetName }

// Update handles keypresses and forwards them to the field list.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.Code == tea.KeyEscape && !p.fields.Editing() {
			return func() tea.Msg { return ClosedMsg{} }
		}
	case tea.PasteMsg:
		// FieldList handles paste-to-open only when the active row is
		// editable, so forward unconditionally.
	}

	wasCommitted := p.fields.Committed()
	cmd := p.fields.Update(msg)
	if !wasCommitted && p.fields.Committed() && p.changed {
		p.changed = false
		row := p.fields.CursorRow()
		if row != nil {
			preset := p.cfg.Models.Presets[p.presetName]
			ds := p.descriptors()
			for _, d := range ds {
				if d.id == row.ID {
					newVal := strconv.Itoa(d.get(preset))
					oldVal := "automatic"
					if d.get(preset) != 0 {
						oldVal = newVal
					}
					return tea.Batch(cmd, func() tea.Msg {
						return ChangedMsg{
							Config:     cloneConfig(p.cfg),
							PresetName: p.presetName,
							FieldID:    d.id,
							OldValue:   oldVal,
							NewValue:   newVal,
						}
					})
				}
			}
		}
	}
	return cmd
}

// View renders the panel inside the dock's dimensions.
func (p *Panel) View(width, maxHeight int) string {
	p.fields.SetSize(width-3, max(maxHeight-2, 1))
	th := theme.Current()
	title := fmt.Sprintf("Options: %s", p.presetName)
	return chrome.PanelWithHints(title, "↵ edit · Esc close", p.fields.View(), width, maxHeight, true, th)
}

// Sizing reports that the panel wants a docked portion of the frame.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

func cloneConfig(cfg config.Config) config.Config {
	if cfg.Models.Presets == nil {
		cfg.Models.Presets = map[string]routing.ModelPreset{}
		return cfg
	}
	presets := make(map[string]routing.ModelPreset, len(cfg.Models.Presets))
	for k, v := range cfg.Models.Presets {
		presets[k] = v
	}
	cfg.Models.Presets = presets
	return cfg
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
