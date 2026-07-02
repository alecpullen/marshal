package settings

import (
	"fmt"
	"sort"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

type Model struct {
	cfg            config.Config
	workingDir     string
	projectCfgPath string
	fields         []field
	focused        int
	footer         string
}

func New(cfg config.Config, workingDir, projectCfgPath string) Model {
	m := Model{
		cfg:            cfg,
		workingDir:     workingDir,
		projectCfgPath: projectCfgPath,
	}

	profileNames := make([]string, 0, len(cfg.AgentProfiles))
	for name := range cfg.AgentProfiles {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)
	if len(profileNames) == 0 {
		profileNames = []string{""}
	}

	m.fields = append(m.fields, newSelectField(
		"Default profile",
		profileNames,
		cfg.Profile.Default,
		func(v string) { m.cfg.Profile.Default = v },
	))

	activePreset := activePresetFromConfig(cfg)
	presetName := ""
	if activePreset.Name != "" {
		presetName = activePreset.Name
	}

	m.fields = append(m.fields, newStringField(
		"Preset",
		presetName,
		func(v string) {
			// Preset name is read-only in v1; edits apply to the active preset.
		},
	))

	provider := cfg.Agent.Provider
	model := cfg.Agent.Model
	localOnly := false
	if activePreset.Name != "" {
		provider = activePreset.Provider
		model = activePreset.Model
		localOnly = activePreset.LocalOnly
	}

	m.fields = append(m.fields, newStringField(
		"Provider",
		provider,
		func(v string) {
			if activePreset.Name != "" {
				if p, ok := m.cfg.Models.Presets[activePreset.Name]; ok {
					p.Provider = v
					m.cfg.Models.Presets[activePreset.Name] = p
				}
			} else {
				m.cfg.Agent.Provider = v
			}
		},
	))

	m.fields = append(m.fields, newStringField(
		"Model",
		model,
		func(v string) {
			if activePreset.Name != "" {
				if p, ok := m.cfg.Models.Presets[activePreset.Name]; ok {
					p.Model = v
					m.cfg.Models.Presets[activePreset.Name] = p
				}
			} else {
				m.cfg.Agent.Model = v
			}
		},
	))

	m.fields = append(m.fields, newBoolField(
		"Local only",
		"block remote providers for this preset",
		&localOnly,
		func(v bool) {
			if activePreset.Name != "" {
				if p, ok := m.cfg.Models.Presets[activePreset.Name]; ok {
					p.LocalOnly = v
					m.cfg.Models.Presets[activePreset.Name] = p
				}
			}
		},
	))

	m.fields = append(m.fields, newBoolField(
		"Remote providers allowed",
		"allow remote providers globally",
		&m.cfg.Privacy.RemoteProvidersAllowed,
		nil,
	))

	if len(m.fields) > 0 {
		m.fields[0].Focus()
	}
	return m
}

func activePresetFromConfig(cfg config.Config) routing.ModelPreset {
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		return routing.ModelPreset{}
	}
	name, ok := profile.Roles[routing.RoleImplementer]
	if !ok {
		return routing.ModelPreset{}
	}
	if preset, ok := cfg.Models.Presets[name]; ok {
		return preset
	}
	return routing.ModelPreset{Name: name}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			return m, func() tea.Msg { return CancelledMsg{} }
		case tea.KeyCtrlS:
			return m, m.saveCmd()
		case tea.KeyTab:
			m.nextField()
			return m, nil
		case tea.KeyShiftTab:
			m.prevField()
			return m, nil
		default:
			if m.focused >= 0 && m.focused < len(m.fields) {
				m.fields[m.focused].Update(msg)
			}
			return m, nil
		}
	}
	return m, nil
}

func (m *Model) nextField() {
	if m.focused >= 0 && m.focused < len(m.fields) {
		m.fields[m.focused].Blur()
	}
	m.focused++
	if m.focused >= len(m.fields) {
		m.focused = 0
	}
	m.fields[m.focused].Focus()
}

func (m *Model) prevField() {
	if m.focused >= 0 && m.focused < len(m.fields) {
		m.fields[m.focused].Blur()
	}
	m.focused--
	if m.focused < 0 {
		m.focused = len(m.fields) - 1
	}
	m.fields[m.focused].Focus()
}

func (m *Model) saveCmd() tea.Cmd {
	return func() tea.Msg {
		if err := config.SaveProjectConfig(m.projectCfgPath, m.cfg); err != nil {
			m.footer = fmt.Sprintf("Save failed: %v", err)
			return nil
		}
		loaded, err := config.Load(config.LoadOptions{WorkingDir: m.workingDir})
		if err != nil {
			m.footer = fmt.Sprintf("Reload failed: %v", err)
			return nil
		}
		return SavedMsg{Cfg: loaded}
	}
}

func (m Model) Footer() string {
	return m.footer
}
