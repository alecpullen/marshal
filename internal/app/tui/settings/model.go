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
	width          int
	height         int
}

func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
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

	// Known issue: changing the Default profile does not recompute the active
	// preset label or repopulate the Provider/Model/Local-only fields in place.
	// The select field callback only receives the new value string, and the
	// current implementation keeps the originally computed fields.
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

	m.fields = append(m.fields, newLabelField("Preset", presetName))

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
			if name := m.activePresetName(); name != "" {
				if p, ok := m.cfg.Models.Presets[name]; ok {
					p.Provider = v
					m.cfg.Models.Presets[name] = p
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
			if name := m.activePresetName(); name != "" {
				if p, ok := m.cfg.Models.Presets[name]; ok {
					p.Model = v
					m.cfg.Models.Presets[name] = p
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
			if name := m.activePresetName(); name != "" {
				if p, ok := m.cfg.Models.Presets[name]; ok {
					p.LocalOnly = v
					m.cfg.Models.Presets[name] = p
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

func (m *Model) activePresetName() string {
	profile, ok := m.cfg.AgentProfiles[m.cfg.Profile.Default]
	if !ok {
		return ""
	}
	name, ok := profile.Roles[routing.RoleImplementer]
	if !ok {
		return ""
	}
	return name
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
	if len(m.fields) == 0 {
		return
	}
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
	if len(m.fields) == 0 {
		return
	}
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
