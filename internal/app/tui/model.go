package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/settings"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

// AgentRunner is the one thing the TUI knows about the agent loop: how to
// kick off a turn and get back a terminal error (or nil). It is satisfied
// structurally by *agent.Runner without this package importing
// internal/agent — the TUI stays a rendering layer with no policy/prompt
// logic, per CLAUDE.md's design constraints.
type AgentRunner interface {
	Run(ctx context.Context, goal string) error
	SetForceClass(class string)
}

const (
	minTerminalWidth  = 40
	minTerminalHeight = 10

	doneDisplayDuration = 2 * time.Second
)

type Model struct {
	state                  *session.State
	input                  textarea.Model
	editingCommand         bool
	commandSuggestions     []commands.Command
	commandSuggestionIndex int
	runner                 AgentRunner
	swarmRunner            AgentRunner
	ctx                    context.Context
	busy                   bool
	settingsOpen           bool
	settingsModel          settings.Model
	configReloader         ConfigReloader
	memoryOpen             bool
	memoryModel            memory.Model
	memoryDB               *db.DB
	memoryProject          int64
	cmdRegistry            *commands.Registry
	agentCancel            context.CancelFunc
	forceMode              string // reserved for future status-bar display

	// New Layout State
	width    int
	height   int
	viewport viewport.Model

	// Viewport dirty tracking.
	lastTranscriptHash uint64
	thinkingExpanded   bool

	spinner           Spinner
	spinnerFrame      string
	lastActivityLabel string
	lastActivityDone  time.Time
	lastActivityKind  session.ActivityKind
	now               func() time.Time
}

type Option func(*Model)

type ConfigReloader func(cfg config.Config) error

func WithConfigReloader(fn ConfigReloader) Option {
	return func(m *Model) {
		m.configReloader = fn
	}
}

func WithCommandRegistry(reg *commands.Registry) Option {
	return func(m *Model) {
		m.cmdRegistry = reg
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

// WithSwarmRunner configures the TUI to route /swarm <goal> submissions
// through runner (the swarm orchestrator). ctx follows the same rules as
// WithRunner's: pass the cancellable program context so Ctrl+C and /stop
// cancel an in-flight swarm run.
func WithSwarmRunner(ctx context.Context, runner AgentRunner) Option {
	return func(m *Model) {
		m.ctx = ctx
		m.swarmRunner = runner
	}
}

func projectConfigPath(workingDir string) string {
	return filepath.Join(workingDir, ".marshal", "config.toml")
}

func New(state *session.State, opts ...Option) Model {
	input := textarea.New()
	input.Prompt = ""
	input.ShowLineNumbers = false
	input.Placeholder = "Ask Marshal..."
	input.CharLimit = 4000
	input.MaxHeight = 8
	input.SetHeight(1)
	input.SetWidth(80)

	km := textarea.DefaultKeyMap
	km.InsertNewline.SetKeys("shift+enter")
	input.KeyMap = km
	input.Focus()

	input.FocusedStyle.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	input.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(dimColor)
	input.BlurredStyle.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	input.BlurredStyle.Placeholder = lipgloss.NewStyle().Foreground(dimColor)

	// CursorLine is the style wrapping the active text row. The upstream
	// default adds a dark background ("0") that extends across the full
	// line width (including padding spaces), producing a dark bar behind
	// the cursor line. We override it to have no background so the input
	// area stays on a single clean line.
	input.FocusedStyle.CursorLine = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	input.BlurredStyle.CursorLine = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "7"})

	// EndOfBuffer is the filler row(s) below the last line of text. The
	// upstream default applies a dark foreground ("0") that can leave a
	// faint artifact row when the textarea height is 1.
	input.FocusedStyle.EndOfBuffer = lipgloss.NewStyle()
	input.BlurredStyle.EndOfBuffer = lipgloss.NewStyle()

	// Cursor lives on the embedded cursor.Model. Style renders via
	// .Reverse(true), which swaps fg↔bg — the Foreground set here becomes
	// the visible block fill. The glyph under a block cursor is usually a
	// space, so a Background-only style would leave the block in the
	// terminal's default colour and never show coral at all.
	input.Cursor.Style = lipgloss.NewStyle().Foreground(coralColor)
	input.Cursor.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	m := Model{
		state:          state,
		input:          input,
		editingCommand: false,
		ctx:            context.Background(),
		viewport:       viewport.New(0, 0),
		spinner:        NewSpinner(),
		now:            time.Now,
	}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

func blinkCmd() tea.Cmd {
	return textarea.Blink
}

func (m Model) Init() tea.Cmd {
	return blinkCmd()
}

func (m *Model) resize(width, height int) {
	if width < minTerminalWidth {
		width = minTerminalWidth
	}
	if height < minTerminalHeight {
		height = minTerminalHeight
	}

	m.width = width
	m.height = height

	// Input interior: width minus border (2) and padding (2) leaves the
	// box's inner width (width-4); the "❯ " prompt occupies 2 cells of it.
	m.input.SetWidth(max(width-6, 1))
	m.resizeInputHeight()

	// Transcript viewport lives inside a subtle border frame.
	m.viewport.Width = max(width-2, 1)
	m.viewport.Height = max(height-transcriptFrameRows-m.swarmPanelRows()-m.inputAreaRows()-statusLineRows, 1)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	tc := m.state.PendingApproval()

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		m.settingsModel.SetSize(m.width, m.height)
		m.memoryModel.SetSize(m.width, m.height)
		m.refreshViewport()
		return m, nil
	case agentFinishedMsg:
		m.busy = false
		m.agentCancel = nil
		if msg.err != nil {
			m.state.SetProviderError(msg.err)
		}
		m.state.SetActivity(session.Activity{Kind: session.ActivityIdle})
		if m.lastActivityKind != session.ActivityIdle && m.lastActivityKind != "" {
			m.lastActivityDone = m.now()
			m.lastActivityKind = session.ActivityIdle
		}
		m.updateViewportHeight()
		m.refreshViewport()
		return m, nil
	case agentTickMsg:
		if !m.busy {
			return m, nil
		}
		m.spinnerFrame = m.spinner.Next()
		act := m.state.Activity()
		if act.Kind == session.ActivityIdle && m.lastActivityKind != session.ActivityIdle && m.lastActivityKind != "" {
			m.lastActivityDone = m.now()
		}
		m.lastActivityKind = act.Kind
		if act.Kind != session.ActivityIdle && act.Label != "" {
			m.lastActivityLabel = act.Label
		}
		if m.state.PendingQuestion() != nil && m.input.Placeholder != "Type your answer..." {
			m.input.Placeholder = "Type your answer..."
		}
		m.updateViewportHeight()
		m.refreshViewport()
		return m, tickCmd()
	case settings.SavedMsg:
		m.state.Config = msg.Cfg
		if m.configReloader != nil {
			if err := m.configReloader(msg.Cfg); err != nil {
				m.state.SetProviderError(err)
				m.settingsModel = settings.New(msg.Cfg, m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))
				m.settingsModel.SetSize(m.width, m.height)
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
			if msg.Type == tea.KeyCtrlK {
				m.memoryOpen = false
				return m, nil
			}
			updated, cmd := m.memoryModel.Update(msg)
			m.memoryModel = updated.(memory.Model)
			return m, cmd
		}

		if q := m.state.PendingQuestion(); q != nil {
			switch msg.Type {
			case tea.KeyEnter:
				q.ResponseChan <- strings.TrimSpace(m.input.Value())
				m.state.SetPendingQuestion(nil)
				m.input.Reset()
				m.input.Placeholder = "Ask Marshal..."
				m.resizeInputHeight()
				m.updateViewportHeight()
				m.lastTranscriptHash = 0
				return m, nil
			case tea.KeyEsc:
				q.ResponseChan <- ""
				m.state.SetPendingQuestion(nil)
				m.input.Reset()
				m.input.Placeholder = "Ask Marshal..."
				m.resizeInputHeight()
				m.updateViewportHeight()
				m.lastTranscriptHash = 0
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.resizeInputHeight()
			m.updateViewportHeight()
			return m, cmd
		}

		if tc != nil {
			if m.editingCommand {
				switch msg.Type {
				case tea.KeyEsc:
					m.editingCommand = false
					m.input.Reset()
					m.input.Placeholder = "Ask Marshal..."
					m.resizeInputHeight()
					m.updateViewportHeight()
					m.lastTranscriptHash = 0
					return m, nil
				case tea.KeyEnter:
					value := strings.TrimSpace(m.input.Value())
					if value != "" {
						tc.ResponseChan <- session.UserApprovalDecision{Approved: true, Edited: value}
						m.editingCommand = false
						m.input.Reset()
						m.input.Placeholder = "Ask Marshal..."
						m.resizeInputHeight()
						m.updateViewportHeight()
						m.state.SetPendingApproval(nil)
					}
					m.lastTranscriptHash = 0
					return m, nil
				}
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				m.resizeInputHeight()
				m.updateViewportHeight()
				return m, cmd
			}

			switch msg.Type {
			case tea.KeyEnter:
				tc.ResponseChan <- session.UserApprovalDecision{Approved: true}
				m.state.SetPendingApproval(nil)
				m.lastTranscriptHash = 0
				return m, nil
			case tea.KeyEsc:
				tc.ResponseChan <- session.UserApprovalDecision{Approved: false}
				m.state.SetPendingApproval(nil)
				m.lastTranscriptHash = 0
				return m, nil
			default:
				switch msg.String() {
				case "d":
					tc.ResponseChan <- session.UserApprovalDecision{Approved: false}
					m.state.SetPendingApproval(nil)
					m.lastTranscriptHash = 0
					return m, nil
				case "a":
					m.state.AddSessionRule(tc.Command)
					tc.ResponseChan <- session.UserApprovalDecision{Approved: true}
					m.state.SetPendingApproval(nil)
					m.lastTranscriptHash = 0
					return m, nil
				case "e":
					m.editingCommand = true
					if tc.Name == "shell.run" {
						m.input.SetValue(tc.Command)
						m.input.Placeholder = "Edit command..."
					} else {
						m.input.SetValue(tc.Args)
						m.input.Placeholder = "Edit JSON arguments..."
					}
					m.resizeInputHeight()
					m.updateViewportHeight()
					m.input.Focus()
					m.lastTranscriptHash = 0
					return m, nil
				case "r":
					if m.state.HasBackup() {
						_ = m.state.RollbackBackup()
						m.state.LogToolCall(registry.AuditEvent{
							Timestamp:     time.Now(),
							ToolName:      "rollback",
							ResultSummary: "Rollback applied successfully",
						})
						m.lastTranscriptHash = 0
						m.refreshViewport()
						return m, nil
					}
				}
				return m, nil
			}
		} else {
			// Global hotkeys — input is always focused.
			switch msg.Type {
			case tea.KeyEsc:
				m.cancelTurn()
				return m, nil
			case tea.KeyCtrlO:
				m.settingsModel = settings.New(m.state.Config, m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))
				m.settingsModel.SetSize(m.width, m.height)
				m.settingsOpen = true
				return m, nil
			case tea.KeyCtrlK:
				if m.memoryDB == nil {
					return m, nil
				}
				m.memoryModel = memory.New(m.memoryDB, m.memoryProject)
				m.memoryModel.SetSize(m.width, m.height)
				m.memoryOpen = true
				return m, nil
			case tea.KeyCtrlG:
				m.thinkingExpanded = !m.thinkingExpanded
				m.lastTranscriptHash = 0
				m.refreshViewport()
				return m, nil
			case tea.KeyCtrlR:
				if m.state.HasBackup() {
					_ = m.state.RollbackBackup()
					m.state.LogToolCall(registry.AuditEvent{
						Timestamp:     time.Now(),
						ToolName:      "rollback",
						ResultSummary: "Rollback applied successfully",
					})
					m.refreshViewport()
				}
				return m, nil
			case tea.KeyPgUp, tea.KeyPgDown:
				var vpCmd tea.Cmd
				m.viewport, vpCmd = m.viewport.Update(msg)
				return m, vpCmd
			case tea.KeyCtrlU:
				m.viewport.HalfViewUp()
				return m, nil
			case tea.KeyCtrlD:
				m.viewport.HalfViewDown()
				return m, nil
			case tea.KeyUp:
				if m.moveCommandSuggestion(-1) {
					return m, nil
				}
			case tea.KeyDown:
				if m.moveCommandSuggestion(1) {
					return m, nil
				}
			case tea.KeyTab:
				if m.acceptCommandSuggestion() {
					return m, nil
				}
			case tea.KeyEnter:
				if key.Matches(msg, m.input.KeyMap.InsertNewline) {
					break
				}
				value := strings.TrimSpace(m.input.Value())
				if value == "" {
					return m, nil
				}
				m.input.Reset()
				m.resizeInputHeight()
				m.updateCommandSuggestions()
				m.updateViewportHeight()

				if strings.HasPrefix(value, "/") {
					return m.dispatchCommand(value)
				}

				if m.busy {
					return m, nil
				}
				if m.runner == nil {
					m.state.AddMessage(session.RoleUser, value, session.ContentTypePlain)
					m.refreshViewport()
					return m, nil
				}
				m.busy = true
				agentCtx, cancel := context.WithCancel(m.ctx)
				m.agentCancel = cancel
				return m, tea.Batch(runAgentCmd(agentCtx, m.runner, value), tickCmd())
			}
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	inputHeightChanged := m.resizeInputHeight()
	m.updateCommandSuggestions()

	// Recalculate viewport if input area height changed
	viewportHeightChanged := m.updateViewportHeight()
	if inputHeightChanged || viewportHeightChanged {
		m.lastTranscriptHash = 0
		m.refreshViewport()
	}

	return m, cmd
}

func (m Model) inputAreaRows() int {
	rows := inputBorderRows + activityStripRows
	if q := m.state.PendingQuestion(); q != nil {
		inputInnerWidth := max(m.width-4, 1)
		content := renderQuestionPanel(q, inputInnerWidth)
		rows += len(strings.Split(content, "\n"))
		inputHeight := max(m.input.Height(), 1)
		if inputHeight > m.input.MaxHeight {
			inputHeight = m.input.MaxHeight
		}
		rows += inputHeight
	} else if tc := m.state.PendingApproval(); tc != nil {
		content := ""
		if m.editingCommand {
			content = "❯ " + m.input.View()
		} else {
			inputInnerWidth := max(m.width-4, 1)
			content = renderApprovalPanel(tc, inputInnerWidth)
		}
		rows += len(strings.Split(content, "\n"))
	} else {
		inputHeight := max(m.input.Height(), 1)
		if inputHeight > m.input.MaxHeight {
			inputHeight = m.input.MaxHeight
		}
		rows += inputHeight
	}
	if len(m.commandSuggestions) > 0 {
		rows += commandSuggestionRows
	}
	return rows
}

func (m *Model) resizeInputHeight() bool {
	rows := wrappedInputRows(m.input.Value(), m.input.Width())
	if m.input.MaxHeight > 0 && rows > m.input.MaxHeight {
		rows = m.input.MaxHeight
	}
	if rows < 1 {
		rows = 1
	}
	if rows == m.input.Height() {
		return false
	}
	m.input.SetHeight(rows)
	return true
}

func wrappedInputRows(value string, width int) int {
	if width < 1 || value == "" {
		return 1
	}

	rows := 0
	for _, line := range strings.Split(value, "\n") {
		if line == "" {
			rows++
			continue
		}
		wrapped := ansi.Wrap(line, width, "")
		rows += max(len(strings.Split(wrapped, "\n")), 1)
	}
	return max(rows, 1)
}

func (m Model) swarmPanelRows() int {
	if m.state.SwarmProgress().Active {
		return swarmPanelRows
	}
	return 0
}

func (m *Model) updateViewportHeight() bool {
	newViewportHeight := max(m.height-transcriptFrameRows-m.swarmPanelRows()-m.inputAreaRows()-statusLineRows, 1)
	if newViewportHeight == m.viewport.Height {
		return false
	}
	m.viewport.Height = newViewportHeight
	return true
}

func (m *Model) updateCommandSuggestions() {
	m.commandSuggestions = nil
	m.commandSuggestionIndex = 0
	if m.cmdRegistry == nil {
		return
	}
	value := m.input.Value()
	if !strings.HasPrefix(value, "/") || strings.Contains(strings.TrimPrefix(value, "/"), " ") {
		return
	}
	prefix := strings.ToLower(strings.TrimPrefix(value, "/"))
	for _, cmd := range m.cmdRegistry.List() {
		if prefix == "" || strings.HasPrefix(strings.ToLower(cmd.Name), prefix) {
			m.commandSuggestions = append(m.commandSuggestions, cmd)
			if len(m.commandSuggestions) == 5 {
				break
			}
		}
	}
}

func (m *Model) moveCommandSuggestion(delta int) bool {
	if len(m.commandSuggestions) == 0 {
		return false
	}
	m.commandSuggestionIndex += delta
	if m.commandSuggestionIndex < 0 {
		m.commandSuggestionIndex = len(m.commandSuggestions) - 1
	}
	if m.commandSuggestionIndex >= len(m.commandSuggestions) {
		m.commandSuggestionIndex = 0
	}
	return true
}

func (m *Model) acceptCommandSuggestion() bool {
	if len(m.commandSuggestions) == 0 {
		return false
	}
	cmd := m.commandSuggestions[m.commandSuggestionIndex]
	m.input.SetValue("/" + cmd.Name + " ")
	m.updateCommandSuggestions()
	return true
}

func (m *Model) refreshViewport() {
	items := m.state.Transcript()
	inProgress := m.state.InProgress()
	streamLen := len(inProgress.Reasoning)
	_, activeTool := m.state.ActiveToolCall()
	busy := m.busy || activeTool || streamLen > 0

	hash := transcriptHash(items, streamLen, busy, m.viewport.Width)
	if hash == m.lastTranscriptHash {
		return
	}
	m.lastTranscriptHash = hash

	var b strings.Builder
	if len(items) == 0 {
		b.WriteString(renderWelcomeBanner(m.viewport.Width))
	}
	for _, item := range items {
		b.WriteString(renderTranscriptItem(item, m.thinkingExpanded, m.viewport.Width))
	}

	if inProgress.Active && inProgress.Reasoning != "" {
		b.WriteString(renderThinkingBox(inProgress.Reasoning, m.spinnerFrame, m.viewport.Width))
	}
	if atc, ok := m.state.ActiveToolCall(); ok {
		b.WriteString(renderActiveToolCall(atc, m.spinnerFrame, m.now(), m.viewport.Width))
	}
	if err := m.state.ProviderError(); err != nil {
		b.WriteString(renderProviderError(err, m.viewport.Width))
	}

	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
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

// cancelTurn cancels the in-flight agent turn, if any. Shared by Esc and
// the /stop command.
func (m *Model) cancelTurn() bool {
	if m.agentCancel == nil {
		return false
	}
	m.agentCancel()
	m.agentCancel = nil
	m.state.AddMessage(session.RoleSystem, "Agent turn cancelled.", session.ContentTypePlain)
	m.refreshViewport()
	return true
}

func (m *Model) dispatchCommand(raw string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return m, nil
	}
	name := strings.TrimPrefix(parts[0], "/")
	var args []string
	if len(parts) > 1 {
		args = parts[1:]
	}

	if m.cmdRegistry == nil {
		m.state.AddMessage(session.RoleSystem, "Command registry not available.", session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	}
	cmd, ok := m.cmdRegistry.Lookup(name)
	if !ok {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Unknown command: /%s. Type /help for available commands.", name), session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	}

	msg := cmd.Handler(m.state, args)

	if msg != "" {
		m.state.AddMessage(session.RoleSystem, msg, session.ContentTypePlain)
	}

	switch cmd.Name {
	case "exit", "quit":
		m.state.Shutdown()
		return m, tea.Quit

	case "settings":
		m.settingsModel = settings.New(m.state.Config, m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))
		m.settingsModel.SetSize(m.width, m.height)
		m.settingsOpen = true
		m.refreshViewport()
		return m, nil

	case "memory":
		if m.memoryDB == nil {
			m.state.AddMessage(session.RoleSystem, "Memory browser not available (no database configured).", session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		}
		m.memoryModel = memory.New(m.memoryDB, m.memoryProject)
		m.memoryModel.SetSize(m.width, m.height)
		m.memoryOpen = true
		m.refreshViewport()
		return m, nil

	case "stop":
		if !m.cancelTurn() {
			m.refreshViewport()
		}
		return m, nil

	case "ask":
		if m.runner != nil {
			m.runner.SetForceClass("question")
		}
		m.forceMode = "ask"
		m.refreshViewport()
		return m, nil

	case "edit":
		if m.runner != nil {
			m.runner.SetForceClass("edit")
		}
		m.forceMode = "edit"
		m.refreshViewport()
		return m, nil

	case "auto":
		if m.runner != nil {
			m.runner.SetForceClass("")
		}
		m.forceMode = ""
		m.refreshViewport()
		return m, nil

	case "swarm":
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
		m.busy = true
		agentCtx, cancel := context.WithCancel(m.ctx)
		m.agentCancel = cancel
		return m, tea.Batch(runAgentCmd(agentCtx, m.swarmRunner, goal), tickCmd())

	case "model":
		if len(args) == 0 {
			m.state.AddMessage(session.RoleSystem, "Usage: /model <preset-name>. Available presets are listed in your config.toml.", session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		}
		if m.configReloader != nil {
			presetName := args[0]
			newCfg := m.state.Config
			preset, ok := newCfg.Models.Presets[presetName]
			if !ok {
				m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Unknown preset: %s", presetName), session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			}
			newCfg.Profile.Default = "switched"
			newCfg.AgentProfiles = map[string]routing.AgentProfile{
				"switched": {
					Name: "switched",
					Roles: map[routing.AgentRole]string{
						routing.RoleImplementer: presetName,
						routing.RoleRepoScout:   presetName,
						routing.RoleKnowledge:   presetName,
					},
				},
			}
			if err := m.configReloader(newCfg); err != nil {
				m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Failed to switch model: %v", err), session.ContentTypePlain)
			} else {
				m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Switched to model: %s (%s)", presetName, preset.Model), session.ContentTypePlain)
			}
		}
		m.refreshViewport()
		return m, nil

	default:
		m.refreshViewport()
		return m, nil
	}
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// tailRunes returns the last limit runes of s (the most recent text),
// unlike truncateRunes which keeps the first limit runes.
func tailRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[len(runes)-limit:])
}

func visibleRunes(s string) int {
	return ansi.StringWidth(s)
}

var (
	// Warm Sunset palette (256-color).
	coralColor  = lipgloss.Color("209") // marshal, focused border, prompt
	goldColor   = lipgloss.Color("214") // tool calls
	tealColor   = lipgloss.Color("43")  // success
	orangeColor = lipgloss.Color("172") // warning / risk
	mauveColor  = lipgloss.Color("245") // blurred border
	userColor   = lipgloss.Color("246") // user prompt

	// accentColor is the primary accent (coral). Retained name because it is
	// referenced widely; successColor/warningColor/errorColor are retuned to
	// the warm palette.
	accentColor  = coralColor
	violetColor  = lipgloss.Color("175") // markdown headings (warm magenta)
	dimColor     = lipgloss.Color("244")
	successColor = tealColor
	warningColor = orangeColor
	errorColor   = lipgloss.Color("203")

	mutedStyle      = lipgloss.NewStyle().Foreground(dimColor)
	panelTitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("255")).
			Bold(true)
	thinkingLineStyle = lipgloss.NewStyle().
				Foreground(dimColor).
				Italic(true)
	inputPromptStyle = lipgloss.NewStyle().
				Foreground(coralColor).
				Bold(true)

	codeBorderStyle = lipgloss.NewStyle().
			Foreground(dimColor).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(dimColor)
	toolNameStyle = lipgloss.NewStyle().
			Foreground(goldColor)
	keyHintStyle = lipgloss.NewStyle().
			Foreground(coralColor).
			Bold(true)
	riskLabelStyle = lipgloss.NewStyle().
			Foreground(warningColor).
			Bold(true)
	dimSeparator = " · "

	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(coralColor).
			Padding(0, 1)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))
)

func compactTokenCount(tokens int) string {
	if tokens >= 1000 {
		return fmt.Sprintf("%dk", tokens/1000)
	}
	return fmt.Sprintf("%d", tokens)
}

func transcriptHash(items []session.TranscriptItem, streamLen int, busy bool, width int) uint64 {
	var h uint64
	h = uint64(len(items)) ^ (uint64(streamLen) << 20) ^ (uint64(width) << 40)
	for i, item := range items {
		h ^= uint64(item.Timestamp.UnixNano()) * uint64(i+1)
	}
	if busy {
		h ^= 0xDEADBEEF
	}
	return h
}
