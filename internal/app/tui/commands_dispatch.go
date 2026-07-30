package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/plugins"
	"marshal/internal/app/tui/skills"
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
		"skills": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.dock.Open(skills.NewPanel(m.homeDir, m.workDir, m.state.Trusted(), m.state))
			m.refreshViewport()
			return m, nil
		},
		"plugins": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.dock.Open(plugins.NewPanel(m.homeDir, m.workDir, m.state.Trusted(), m.state))
			m.refreshViewport()
			return m, nil
		},
		"stop": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			if !m.cancelTurn() {
				m.refreshViewport()
			}
			return m, nil
		},
		"plan": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("plan")
			m.state.AddMessage(session.RoleSystem, modeSwitchMessage["plan"], session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"default": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("default")
			m.state.AddMessage(session.RoleSystem, modeSwitchMessage["default"], session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"edit": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("edit")
			m.state.AddMessage(session.RoleSystem, modeSwitchMessage["edit"], session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"copilot": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("copilot")
			m.state.AddMessage(session.RoleSystem, modeSwitchMessage["copilot"], session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"auto": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.setMode("auto")
			m.state.AddMessage(session.RoleSystem, modeSwitchMessage["auto"], session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"mode": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			if len(args) > 0 {
				switch strings.ToLower(args[0]) {
				case "plan", "default", "edit", "copilot", "auto":
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
			m.openRunPreflight("swarm", m.swarmRunner, goal)
			m.refreshViewport()
			return m, nil
		},
		"sdd": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			if m.pipelineFactory == nil {
				m.state.AddMessage(session.RoleSystem, "Plan execution is not available (agent failed to initialise).", session.ContentTypePlain)
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
			runner := m.pipelineFactory(planPath)
			if runner == nil {
				m.refreshViewport()
				return m, nil
			}
			m.pipelineRunner = runner
			m.openRunPreflight("sdd", runner, planPath)
			m.refreshViewport()
			return m, nil
		},
		"connect": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.openConnect("/")
			m.refreshViewport()
			return m, nil
		},
		"models": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			if arg := strings.TrimSpace(strings.Join(args, " ")); arg != "" {
				if presetName, ok := m.resolveModelArg(arg); ok {
					m.switchModelPreset(presetName)
					m.refreshViewport()
					return m, nil
				}
			}
			cmd := m.openModels()
			m.refreshViewport()
			return m, cmd
		},
		"agents": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			m.openAgentsRoster(strings.TrimSpace(strings.Join(args, " ")))
			m.refreshViewport()
			return m, nil
		},
		"save": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.state.AddMessage(session.RoleSystem, "Session saved. Marshal auto-saves messages, todos, and workspace state as you work.", session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		},
		"sessions": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.openSessionPicker("")
			m.refreshViewport()
			return m, nil
		},
		"resume": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			id := strings.TrimSpace(strings.Join(args, " "))
			if id == "" {
				m.openSessionPicker("")
				m.refreshViewport()
				return m, nil
			}
			return m.beginResume(id)
		},
	}
}
