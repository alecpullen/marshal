package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/app/tui/doctorpanel"
	"marshal/internal/app/tui/gitinfo"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/plugins"
	"marshal/internal/app/tui/skills"
	"marshal/internal/pipeline"
	"marshal/internal/worktree"
)

// tuiCommandEffects holds the interactive logic for commands registered
// with TUIOnly in the commands registry. The split is explicit: pure
// (headless-capable) commands carry a registry Handler; interactive
// commands live here, one entry each, next to the dispatch entry point.
var tuiCommandEffects map[string]func(m *Model, args []string) (tea.Model, tea.Cmd)

// newSessionEffect is the shared /new and /clear handler. It asks the
// runtime to build a brand-new session, then re-points every model field
// that depends on the current session and drops per-message UI state that
// belonged to the old one.
func newSessionEffect(m *Model, _ []string) (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	if m.sessionSwapper == nil {
		m.state.AddMessage(session.RoleSystem, "New session is not available in this build.", session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	}

	oldCount := len(m.state.Messages())
	snap, err := m.sessionSwapper.NewSession()
	if err != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Failed to start new session: %v", err), session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	}

	m.state = snap.State
	m.gitInfo = gitinfo.Read(m.state.Workspace().ActiveRoot)
	m.lastGitRead = m.now()
	if snap.Runner != nil {
		m.runner = snap.Runner
	}
	if snap.SwarmRunner != nil {
		m.swarmRunner = snap.SwarmRunner
	}
	if snap.PipelineFactory != nil {
		m.pipelineFactory = snap.PipelineFactory
	}
	if snap.PlanAuthorFactory != nil {
		m.planAuthorFactory = snap.PlanAuthorFactory
	}
	if snap.ToolRegistry != nil {
		m.toolRegistry = snap.ToolRegistry
	}
	if snap.ReviewDispatcher != nil {
		m.reviewDispatcher = snap.ReviewDispatcher
	}

	// Drop per-message UI state that belongs to the old session.
	m.itemExpanded = make(map[itemKey]bool)
	m.viewStack = nil
	m.lastTranscriptHash = 0
	m.detailExpanded = false
	m.activeToolExpanded = false
	m.activeToolStartedAt = time.Time{}
	m.clickRegions = nil

	m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Started new conversation. Cleared %d messages.", oldCount), session.ContentTypePlain)
	m.refreshViewport()
	return m, railBaseRefCmd(m.state.Workspace().ActiveRoot)
}

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
			m.dock.Open(skills.NewPanel(m.homeDir, m.workDir, m.state.Trusted(), m.state, m.skillIndex))
			m.refreshViewport()
			return m, nil
		},
		"plugins": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.dock.Open(plugins.NewPanel(m.homeDir, m.workDir, m.state.Trusted(), m.state))
			m.refreshViewport()
			return m, nil
		},
		"doctor": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			diags := config.Diagnose(m.state.Config, m.state.Layers())
			m.dock.Open(doctorpanel.New(m.state, diags))
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
			parsed, err := parseSDDArgs(args)
			if err != nil {
				m.state.AddMessage(session.RoleSystem, err.Error(), session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			}
			// /sdd new <goal> starts the authoring child.
			if parsed.New {
				return m.startSDDAuthoring(parsed)
			}
			if m.pipelineFactory == nil {
				m.state.AddMessage(session.RoleSystem, "Plan execution is not available (agent failed to initialise).", session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			}
			planPath := parsed.PlanPath
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
			// Set the strategy on the controller.
			if adapter, ok := runner.(*pipeline.ControllerAdapter); ok {
				adapter.Controller().Strategy = parsed.Strategy
			}
			m.pipelineRunner = runner
			m.openRunPreflight("sdd", runner, planPath)
			m.refreshViewport()
			return m, nil
		},
		"run": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			doc := runOutcomeDoc(m.state.SDDProgress(), worktree.CLIGitOps{}, m.state.WorkingDir, m.now(), m.width)
			m.dock.Open(docpanel.New(doc, m.state))
			m.refreshViewport()
			return m, nil
		},
		"review": func(m *Model, args []string) (tea.Model, tea.Cmd) {
			if m.busy {
				return m, nil
			}
			if m.reviewDispatcher == nil {
				m.state.AddMessage(session.RoleSystem, "Review subagent is not available in this build.", session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			}
			if err := m.state.BeginWork(); err != nil {
				m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Cannot start review: %v", err), session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			}
			m.busy = true
			m.turnStartedAt = m.now()
			agentCtx, cancel := context.WithCancel(m.ctx)
			m.agentCancel = cancel
			focus := strings.TrimSpace(strings.Join(args, " "))
			return *m, tea.Batch(runReviewCmd(agentCtx, m.state, m.reviewDispatcher, focus), tickCmd(), spinnerTickCmd())
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
		"options": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.openModelOptions()
			m.refreshViewport()
			return m, nil
		},
		"profiles": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.openSettingsBrowser("profiles")
			m.refreshViewport()
			return m, nil
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
		"new":   newSessionEffect,
		"clear": newSessionEffect,
	}
}
