package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/settings"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// AgentRunner is the one thing the TUI knows about the agent loop: how to
// kick off a turn and get back a terminal error (or nil). It is satisfied
// structurally by *agent.Runner without this package importing
// internal/agent — the TUI stays a rendering layer with no policy/prompt
// logic, per CLAUDE.md's design constraints.
type AgentRunner interface {
	Run(ctx context.Context, goal string) error
}

type Model struct {
	state          *session.State
	input          textinput.Model
	editingCommand bool
	runner         AgentRunner
	ctx            context.Context
	busy           bool
	settingsOpen   bool
	settingsModel  settings.Model
	configReloader ConfigReloader
	memoryOpen     bool
	memoryModel    memory.Model
	memoryDB       *db.DB
	memoryProject  int64

	// New Layout State
	width        int
	height       int
	activeTab    int // 0 = Plan, 1 = Context, 2 = Log
	inputFocused bool
	viewport     viewport.Model
}

type Option func(*Model)

type ConfigReloader func(cfg config.Config) error

func WithConfigReloader(fn ConfigReloader) Option {
	return func(m *Model) {
		m.configReloader = fn
	}
}

// WithMemoryStore configures the memory browser overlay (Ctrl+K) with the
// project database it should read from.
func WithMemoryStore(database *db.DB, projectID int64) Option {
	return func(m *Model) {
		m.memoryDB = database
		m.memoryProject = projectID
	}
}

// WithRunner configures the TUI to drive submitted messages through runner
// instead of the Milestone A-G placeholder behavior (append and do
// nothing). ctx is used for every agent turn dispatched from this model —
// callers should pass the same cancellable context the surrounding
// tea.Program itself uses, so Ctrl+C/SIGINT cancels an in-flight turn.
func WithRunner(ctx context.Context, runner AgentRunner) Option {
	return func(m *Model) {
		m.ctx = ctx
		m.runner = runner
	}
}

func projectConfigPath(workingDir string) string {
	return filepath.Join(workingDir, ".marshal", "config.toml")
}

func New(state *session.State, opts ...Option) Model {
	input := textinput.New()
	input.Placeholder = "Ask Marshal..."
	input.Focus()
	input.CharLimit = 4000
	input.Width = 80

	m := Model{
		state:          state,
		input:          input,
		editingCommand: false,
		ctx:            context.Background(),
		inputFocused:   true,
		activeTab:      0,
		viewport:       viewport.New(0, 0),
	}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	tc := m.state.PendingApproval()

	switch msg := msg.(type) {
	case agentFinishedMsg:
		m.busy = false
		if msg.err != nil {
			m.state.SetProviderError(msg.err)
		}
		return m, nil
	case agentTickMsg:
		if !m.busy {
			return m, nil
		}
		return m, tickCmd()
	case settings.SavedMsg:
		m.state.Config = msg.Cfg
		if m.configReloader != nil {
			if err := m.configReloader(msg.Cfg); err != nil {
				m.state.SetProviderError(err)
				m.settingsModel = settings.New(msg.Cfg, m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))
				return m, nil
			}
		}
		m.settingsOpen = false
		return m, nil
	case settings.CancelledMsg:
		m.settingsOpen = false
		return m, nil
	case memory.ClosedMsg:
		m.memoryOpen = false
		return m, nil
	case tea.KeyMsg:
		// Always allow Ctrl+C to quit
		if msg.Type == tea.KeyCtrlC {
			m.state.Shutdown()
			return m, tea.Quit
		}

		if m.settingsOpen {
			if msg.Type == tea.KeyCtrlO {
				m.settingsOpen = false
				return m, nil
			}
			updated, cmd := m.settingsModel.Update(msg)
			m.settingsModel = updated.(settings.Model)
			return m, cmd
		}
		if m.memoryOpen {
			updated, cmd := m.memoryModel.Update(msg)
			m.memoryModel = updated.(memory.Model)
			return m, cmd
		}

		if tc != nil {
			if m.editingCommand {
				switch msg.Type {
				case tea.KeyEsc:
					m.editingCommand = false
					m.input.Reset()
					m.input.Placeholder = "Ask Marshal..."
					return m, nil
				case tea.KeyEnter:
					value := strings.TrimSpace(m.input.Value())
					if value != "" {
						tc.ResponseChan <- session.UserApprovalDecision{Approved: true, Edited: value}
						m.editingCommand = false
						m.input.Reset()
						m.input.Placeholder = "Ask Marshal..."
						m.state.SetPendingApproval(nil)
					}
					return m, nil
				}
			} else {
				switch msg.Type {
				case tea.KeyEnter:
					tc.ResponseChan <- session.UserApprovalDecision{Approved: true}
					m.state.SetPendingApproval(nil)
					return m, nil
				case tea.KeyEsc:
					m.state.Shutdown()
					return m, tea.Quit
				default:
					switch msg.String() {
					case "d":
						tc.ResponseChan <- session.UserApprovalDecision{Approved: false}
						m.state.SetPendingApproval(nil)
						return m, nil
					case "a":
						m.state.AddSessionRule(tc.Command)
						tc.ResponseChan <- session.UserApprovalDecision{Approved: true}
						m.state.SetPendingApproval(nil)
						return m, nil
					case "e":
						m.editingCommand = true
						m.input.SetValue(tc.Command)
						m.input.Placeholder = "Edit command..."
						m.input.Focus()
						return m, nil
					case "r":
						if m.state.HasBackup() {
							_ = m.state.RollbackBackup()
							m.state.LogToolCall(registry.AuditEvent{
								Timestamp:     time.Now(),
								ToolName:      "rollback",
								ResultSummary: "Rollback applied successfully",
							})
							return m, nil
						}
					}
				}
				// Ignore all other key inputs when approval prompt is shown and not editing
				return m, nil
			}
		} else {
			switch msg.Type {
			case tea.KeyEsc:
				m.state.Shutdown()
				return m, tea.Quit
			case tea.KeyCtrlO:
				m.settingsModel = settings.New(m.state.Config, m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))
				m.settingsOpen = true
				return m, nil
			case tea.KeyCtrlK:
				if m.memoryDB == nil {
					return m, nil
				}
				m.memoryModel = memory.New(m.memoryDB, m.memoryProject)
				m.memoryOpen = true
				return m, nil
			case tea.KeyEnter:
				value := strings.TrimSpace(m.input.Value())
				if value == "" || m.busy {
					return m, nil
				}
				m.input.Reset()
				if m.runner == nil {
					m.state.AddMessage(session.RoleUser, value)
					return m, nil
				}
				m.busy = true
				return m, tea.Batch(runAgentCmd(m.ctx, m.runner, value), tickCmd())
			default:
				switch msg.String() {
				case "r":
					if m.state.HasBackup() {
						_ = m.state.RollbackBackup()
						m.state.LogToolCall(registry.AuditEvent{
							Timestamp:     time.Now(),
							ToolName:      "rollback",
							ResultSummary: "Rollback applied successfully",
						})
						return m, nil
					}
				}
			}
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

type agentFinishedMsg struct{ err error }
type agentTickMsg struct{}

func runAgentCmd(ctx context.Context, runner AgentRunner, goal string) tea.Cmd {
	return func() tea.Msg {
		err := runner.Run(ctx, goal)
		return agentFinishedMsg{err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
		return agentTickMsg{}
	})
}

func formatBoxLine(s string, width int) string {
	contentWidth := width - 6 // "│   " (4) + " │" (2)
	runes := []rune(s)
	if len(runes) > contentWidth {
		s = string(runes[:contentWidth-3]) + "..."
		runes = []rune(s)
	}
	padLen := contentWidth - len(runes)
	if padLen < 0 {
		padLen = 0
	}
	return fmt.Sprintf("│   %s%s │\n", string(runes), strings.Repeat(" ", padLen))
}

func (m Model) View() string {
	if m.settingsOpen {
		return m.settingsModel.View()
	}
	if m.memoryOpen {
		return m.memoryModel.View()
	}

	var b strings.Builder
	tc := m.state.PendingApproval()

	fmt.Fprintf(&b, "Marshal\n")
	fmt.Fprintf(&b, "Status: project=%s cwd=%s local-only=%t\n\n",
		m.state.Config.Project.Name,
		m.state.WorkingDir,
		!m.state.Config.Privacy.RemoteProvidersAllowed,
	)
	route := m.state.ActiveRoute()
	if route.Active {
		fmt.Fprintf(&b, "Route: role=%s profile=%s preset=%s provider=%s model=%s local-only=%t\n\n",
			route.Role, route.Profile, route.Preset, route.Provider, route.Model, route.LocalOnly,
		)
	} else {
		fmt.Fprintf(&b, "Route: inactive\n\n")
	}

	fmt.Fprintf(&b, "Transcript\n")
	messages := m.state.Messages()
	if len(messages) == 0 {
		fmt.Fprintf(&b, "  No messages yet.\n")
	}
	for _, message := range messages {
		fmt.Fprintf(&b, "  %s: %s\n", message.Role, message.Content)
	}

	fmt.Fprintf(&b, "\nStreaming Output\n")
	if m.busy {
		fmt.Fprintf(&b, "  Agent is working...\n")
	} else {
		fmt.Fprintf(&b, "  No model output yet.\n")
	}
	fmt.Fprintf(&b, "\nCommand Palette\n")
	fmt.Fprintf(&b, "  No commands available yet.\n")

	fmt.Fprintf(&b, "\nTool Log\n")
	auditLog := m.state.AuditLog()
	if len(auditLog) == 0 {
		fmt.Fprintf(&b, "  No tool calls yet.\n")
	} else {
		for _, event := range auditLog {
			fmt.Fprintf(&b, "  [%s] %s -> %s\n", event.Timestamp.Format("15:04:05"), event.ToolName, event.ResultSummary)
		}
	}

	fmt.Fprintf(&b, "\nContext\n")
	pack := m.state.ContextPack()
	if pack.IsEmpty() {
		fmt.Fprintf(&b, "  No context pack built yet.\n")
	} else {
		fmt.Fprintf(&b, "  Context Pack: %d/%d tokens\n", pack.TokenUsage.EstimatedTokens, pack.TokenUsage.MaxTokens)
		for _, section := range pack.Sections {
			source := section.Source
			if source == "" {
				source = "no source"
			}
			fmt.Fprintf(&b, "  [%s] %s (%s, %d tokens)\n", section.Kind, section.Title, source, section.EstimatedTokens)
		}
	}

	fmt.Fprintf(&b, "\nDiff\n")
	if tc != nil && tc.Diff != "" {
		fmt.Fprintf(&b, "%s\n", tc.Diff)
	} else {
		fmt.Fprintf(&b, "  No patch proposed.\n")
	}

	if err := m.state.ProviderError(); err != nil {
		fmt.Fprintf(&b, "\nProvider Error\n")
		fmt.Fprintf(&b, "  %s\n", err.Error())
	}

	if tc != nil {
		boxWidth := 75
		fmt.Fprintf(&b, "\n┌── SECURITY APPROVAL REQUIRED ───────────────────────────────────────────┐\n")
		fmt.Fprintf(&b, "│ Agent wants to run:                                                     │\n")
		fmt.Fprintf(&b, "%s", formatBoxLine(tc.Command, boxWidth))
		fmt.Fprintf(&b, "│                                                                         │\n")
		fmt.Fprintf(&b, "│ Reason:                                                                 │\n")
		fmt.Fprintf(&b, "%s", formatBoxLine(tc.Reason, boxWidth))
		fmt.Fprintf(&b, "│                                                                         │\n")
		fmt.Fprintf(&b, "%s", formatBoxLine("Risk Level: "+tc.Risk, boxWidth))
		fmt.Fprintf(&b, "└─────────────────────────────────────────────────────────────────────────┘\n")
		if m.editingCommand {
			fmt.Fprintf(&b, "Edit command and press [Enter] to run, [Esc] to cancel\n")
		} else {
			if m.state.HasBackup() {
				fmt.Fprintf(&b, "[Enter] Approve  [d] Deny  [e] Edit  [a] Always Allow in this Session  [r] Rollback Last Patch\n")
			} else {
				fmt.Fprintf(&b, "[Enter] Approve  [d] Deny  [e] Edit  [a] Always Allow in this Session\n")
			}
		}
	}

	if tc == nil || m.editingCommand {
		if m.state.HasBackup() {
			fmt.Fprintf(&b, "[r] Rollback Last Patch\n")
		}
		fmt.Fprintf(&b, "\n%s\n", m.input.View())
	}

	return b.String()
}
