package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/memory"
)

// tuiCommandEffects holds the interactive logic for commands registered
// with TUIOnly in the commands registry. The split is explicit: pure
// (headless-capable) commands carry a registry Handler; interactive
// commands live here, one entry each, next to the dispatch entry point.
var tuiCommandEffects map[string]func(m *Model, args []string) (tea.Model, tea.Cmd)

func init() {
	tuiCommandEffects = map[string]func(m *Model, args []string) (tea.Model, tea.Cmd){
		"exit": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.state.AddMessage(session.RoleSystem, "Goodbye!", session.ContentTypePlain)
			return m, m.beginShutdown()
		},
		"quit": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.state.AddMessage(session.RoleSystem, "Goodbye!", session.ContentTypePlain)
			return m, m.beginShutdown()
		},
		"set": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			m.handleSetCommand(args)
			m.refreshViewport()
			return m, nil
		},
		"settings": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			m.openSettingsBrowser(strings.Join(args, " "))
			m.refreshViewport()
			return m, nil
		},
		"memory": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			if m.memoryDB == nil {
				m.state.AddMessage(session.RoleSystem, "Memory browser not available (no database configured).", session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			}
			m.dock.Open(memory.NewPanel(m.memoryDB, m.memoryProject))
			m.refreshViewport()
			return m, nil
		},
		"stop": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			if !m.cancelTurn() {
				m.refreshViewport()
			}
			return m, nil
		},
		"ask": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("ask")
			m.state.AddMessage(session.RoleSystem, "Switched to Ask mode. Agent will answer questions without planning or editing.", session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"edit": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("edit")
			m.state.AddMessage(session.RoleSystem, "Switched to Edit mode. Agent will plan and execute changes.", session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"auto": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("")
			m.state.AddMessage(session.RoleSystem, "Switched to Auto mode. Agent will classify each turn automatically.", session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"mode": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			if len(args) > 0 {
				switch strings.ToLower(args[0]) {
				case "ask", "edit", "auto":
					return m.dispatchCommand("/" + strings.ToLower(args[0]))
				case "sdd":
					m.openSDDPlanPicker()
					m.refreshViewport()
					return m, nil
				}
			}
			m.openPicker("mode", "Interaction mode", "", m.modePickerItems(), "")
			m.refreshViewport()
			return m, nil
		},
		"swarm": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			goal := strings.TrimSpace(strings.Join(args, " "))
			if goal == "" {
				m.state.AddMessage(session.RoleSystem, "Usage: /swarm <goal>", session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			}
			if m.swarmRunner == nil {
				m.state.AddMessage(session.RoleSystem, "Swarm is not available (agent failed to initialise).", session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			}
			if m.busy {
				return m, nil
			}
			return m.startAgentRun(m.swarmRunner, goal)
		},
		"sdd": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			if m.sddRunner == nil {
				m.state.AddMessage(session.RoleSystem, "SDD is not available (agent failed to initialise).", session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			}
			planPath := strings.TrimSpace(strings.Join(args, " "))
			if planPath == "" {
				m.openSDDPlanPicker()
				m.refreshViewport()
				return m, nil
			}
			if m.busy {
				return m, nil
			}
			return m.startAgentRun(m.sddRunner, planPath)
		},
		"connect": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.openConnect("/")
			m.refreshViewport()
			return m, nil
		},
		"models": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			cmd := m.openModels()
			m.refreshViewport()
			return m, cmd
		},
		"model": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			presets := m.state.Config.Models.Presets
			if len(presets) == 0 {
				m.state.AddMessage(session.RoleSystem, "No model presets configured. Add one in /settings → Model Presets.", session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			}
			if len(args) > 0 {
				if _, ok := presets[args[0]]; ok {
					m.switchModelPreset(args[0])
					m.refreshViewport()
					return m, nil
				}
			}
			// bare, or an argument that doesn't resolve: open the picker,
			// pre-filtered with whatever was typed
			m.openPicker("model", "Switch model", "session only — /settings to persist",
				m.modelPickerItems(), strings.Join(args, " "))
			m.refreshViewport()
			return m, nil
		},
	}
}
