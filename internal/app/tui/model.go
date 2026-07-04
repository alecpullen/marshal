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
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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

const (
	minTerminalWidth  = 40
	minTerminalHeight = 10
	minPanelWidth     = 10

	totalHorizontalBorderGutter = 5 // left border + right border + gutter
	verticalOverhead            = 4 // status bar (1) + right-column border (2) + slack (1)
	chatBelowViewportRows       = 4 // bordered input box (3) + help line (1)

	doneDisplayDuration = 2 * time.Second
)

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

	// Layout geometry computed once per WindowSizeMsg.
	leftWidth     int
	rightWidth    int
	contentHeight int
	chatHeight    int

	// Viewport dirty tracking.
	lastMessageCount int
	lastStreamLen    int
	thinkingExpanded bool

	spinner           Spinner
	spinnerFrame      string
	lastActivityLabel string
	lastActivityDone  time.Time
	lastActivityKind  session.ActivityKind
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
	input.Prompt = ""
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
		spinner:        NewSpinner(),
	}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return textinput.Blink
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

	// 70/30 split over the interior width: subtract the left/right column
	// borders and the one-column gutter between columns.
	availableWidth := width - totalHorizontalBorderGutter
	if availableWidth < minPanelWidth*2 {
		availableWidth = minPanelWidth * 2
	}
	m.leftWidth = int(float64(availableWidth) * 0.70)
	if m.leftWidth < minPanelWidth {
		m.leftWidth = minPanelWidth
	}
	m.rightWidth = availableWidth - m.leftWidth
	if m.rightWidth < minPanelWidth {
		m.rightWidth = minPanelWidth
		m.leftWidth = availableWidth - m.rightWidth
		if m.leftWidth < minPanelWidth {
			m.leftWidth = minPanelWidth
		}
	}

	// Vertical budget: status bar (1 row) + right-column border (2 rows) +
	// one row of rounding/help slack + interior content. The interior content
	// height must leave room for the input/help rows below the chat viewport.
	m.contentHeight = height - verticalOverhead
	if m.contentHeight < 5 {
		m.contentHeight = 5
	}

	// Chat viewport height is the interior content height minus the rows
	// reserved for the input line and wrapped help line(s) below it.
	m.chatHeight = m.contentHeight - chatBelowViewportRows
	if m.chatHeight < 1 {
		m.chatHeight = 1
	}

	// Viewport fills the Chat panel interior, whose border is added by
	// renderPanel on top of these dimensions.
	m.viewport.Width = max(m.leftWidth, 1)
	m.viewport.Height = max(m.chatHeight-1, 1)

	// Input lives in a bordered, padded box. inputStyle uses Width(m.leftWidth),
	// Border (2 cols) and Padding(0,1) (2 cols), so the available interior width
	// is leftWidth-4. The prompt "❯ " occupies 2 cols, leaving leftWidth-6 for
	// the textinput's own content.
	m.input.Width = max(m.leftWidth-6, 1)
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
		if msg.err != nil {
			m.state.SetProviderError(msg.err)
		}
		m.state.SetActivity(session.Activity{Kind: session.ActivityIdle})
		if m.lastActivityKind != session.ActivityIdle && m.lastActivityKind != "" {
			m.lastActivityDone = time.Now()
			m.lastActivityKind = session.ActivityIdle
		}
		m.refreshViewport()
		return m, nil
	case agentTickMsg:
		if !m.busy {
			return m, nil
		}
		m.spinnerFrame = m.spinner.Next()
		act := m.state.Activity()
		if act.Kind == session.ActivityIdle && m.lastActivityKind != session.ActivityIdle && m.lastActivityKind != "" {
			m.lastActivityDone = time.Now()
		}
		m.lastActivityKind = act.Kind
		if act.Kind != session.ActivityIdle && act.Label != "" {
			m.lastActivityLabel = act.Label
		}
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

		if tc != nil {
			if m.editingCommand {
				switch msg.Type {
				case tea.KeyEsc:
					m.editingCommand = false
					m.inputFocused = false
					m.input.Blur()
					m.input.Reset()
					m.input.Placeholder = "Ask Marshal..."
					return m, nil
				case tea.KeyEnter:
					value := strings.TrimSpace(m.input.Value())
					if value != "" {
						tc.ResponseChan <- session.UserApprovalDecision{Approved: true, Edited: value}
						m.editingCommand = false
						m.inputFocused = false
						m.input.Blur()
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
					tc.ResponseChan <- session.UserApprovalDecision{Approved: false}
					m.state.SetPendingApproval(nil)
					return m, nil
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
						m.inputFocused = true
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
							m.refreshViewport()
							return m, nil
						}
					}
				}
				// Ignore all other key inputs when approval prompt is shown and not editing
				return m, nil
			}
		} else {
			// Tab cycling
			if msg.Type == tea.KeyTab {
				m.activeTab = (m.activeTab + 1) % 3
				return m, nil
			}
			if msg.Type == tea.KeyShiftTab {
				m.activeTab = (m.activeTab - 1 + 3) % 3
				return m, nil
			}

			// Global tab switching hotkeys
			if msg.Type == tea.KeyCtrlP {
				m.activeTab = 0
				return m, nil
			}
			if msg.Type == tea.KeyCtrlX {
				m.activeTab = 1
				return m, nil
			}
			if msg.Type == tea.KeyCtrlT {
				m.activeTab = 2
				return m, nil
			}
			if msg.Type == tea.KeyCtrlR {
				if m.state.HasBackup() {
					_ = m.state.RollbackBackup()
					m.state.LogToolCall(registry.AuditEvent{
						Timestamp:     time.Now(),
						ToolName:      "rollback",
						ResultSummary: "Rollback applied successfully",
					})
					m.refreshViewport()
					return m, nil
				}
			}

			if msg.Type == tea.KeyCtrlG {
				m.thinkingExpanded = !m.thinkingExpanded
				m.lastMessageCount = -1 // force refreshViewport to rebuild despite unchanged message/stream state
				m.refreshViewport()
				return m, nil
			}

			if m.inputFocused {
				switch msg.Type {
				case tea.KeyEsc:
					m.inputFocused = false
					m.input.Blur()
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
				case tea.KeyEnter:
					value := strings.TrimSpace(m.input.Value())
					if value == "" || m.busy {
						return m, nil
					}
					m.input.Reset()
					if m.runner == nil {
						m.state.AddMessage(session.RoleUser, value)
						m.refreshViewport()
						return m, nil
					}
					m.busy = true
					return m, tea.Batch(runAgentCmd(m.ctx, m.runner, value), tickCmd())
				}
			} else {
				switch msg.Type {
				case tea.KeyEsc:
					m.state.Shutdown()
					return m, tea.Quit
				case tea.KeyEnter:
					m.inputFocused = true
					return m, m.input.Focus()
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
				case tea.KeyRunes:
					switch msg.String() {
					case "1":
						m.activeTab = 0
						return m, nil
					case "2":
						m.activeTab = 1
						return m, nil
					case "3":
						m.activeTab = 2
						return m, nil
					}
				}
				if msg.String() == "r" {
					if m.state.HasBackup() {
						_ = m.state.RollbackBackup()
						m.state.LogToolCall(registry.AuditEvent{
							Timestamp:     time.Now(),
							ToolName:      "rollback",
							ResultSummary: "Rollback applied successfully",
						})
						m.refreshViewport()
						return m, nil
					}
				}
				var vpCmd tea.Cmd
				m.viewport, vpCmd = m.viewport.Update(msg)
				return m, vpCmd
			}
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) refreshViewport() {
	messages := m.state.Messages()
	inProgress := m.state.InProgress()
	streamLen := len(inProgress.Reasoning)
	if len(messages) == m.lastMessageCount && streamLen == m.lastStreamLen && !m.busy {
		return
	}
	m.lastMessageCount = len(messages)
	m.lastStreamLen = streamLen

	var b strings.Builder
	if len(messages) == 0 {
		b.WriteString("  No messages yet.\n")
	}
	for _, message := range messages {
		if message.Reasoning != "" {
			b.WriteString(renderThinkingSummary(message.Reasoning, message.ThinkDuration, m.thinkingExpanded, m.viewport.Width))
		}
		b.WriteString(renderMessage(string(message.Role), message.Content, m.viewport.Width))
	}
	if inProgress.Active {
		b.WriteString(renderThinkingBox(inProgress.Reasoning, m.viewport.Width))
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

func formatThinkDuration(d time.Duration) string {
	return fmt.Sprintf("%.0fs", d.Seconds())
}

const thinkingBoxTailLines = 6

// renderThinkingBox renders the live "thinking" panel shown while a model
// call's reasoning is still streaming in.
func renderThinkingBox(reasoning string, width int) string {
	boxWidth := width - 2
	if boxWidth < 1 {
		boxWidth = 1
	}
	style := lipgloss.NewStyle().
		Width(boxWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(dimColor).
		Foreground(dimColor).
		Italic(true)
	tail := tailRunes(reasoning, boxWidth*thinkingBoxTailLines)
	// Header is "thinking" (8) + spaces + 34-character subtitle. Keep it on
	// one line by sizing the spacer so the joined result is exactly boxWidth.
	header := lipgloss.JoinHorizontal(
		lipgloss.Top,
		"thinking",
		strings.Repeat(" ", max(boxWidth-42, 1)),
		"streaming · Ctrl+G expands history",
	)
	return style.Render(header+"\n\n"+tail) + "\n\n"
}

// renderThinkingSummary renders a finished message's captured reasoning,
// either collapsed to one line or, when expanded, as a full boxed panel
// matching renderThinkingBox's style.
func renderThinkingSummary(reasoning string, duration time.Duration, expanded bool, width int) string {
	if !expanded {
		return thinkingLineStyle.Render(fmt.Sprintf("  ⚙ thought for %s", formatThinkDuration(duration))) + "\n"
	}
	boxWidth := width - 2
	if boxWidth < 1 {
		boxWidth = 1
	}
	style := lipgloss.NewStyle().
		Width(boxWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(dimColor).
		Foreground(dimColor).
		Italic(true)
	return style.Render(fmt.Sprintf("thinking (%s)\n\n%s", formatThinkDuration(duration), reasoning)) + "\n\n"
}

func renderPanel(title string, meta string, body string, width int, height int) string {
	if width < 4 {
		width = 4
	}
	if height < 2 {
		height = 2
	}
	// width and height are the interior content dimensions of the panel;
	// lipgloss adds the rounded border on top of them, so the panel's total
	// visual size is width+2 by height+2. Header and body must fill the
	// interior exactly.
	innerWidth := max(width, 1)
	truncatedTitle := truncateRunes(title, innerWidth)
	header := panelTitleStyle.Render(truncatedTitle)
	if meta != "" {
		metaWidth := innerWidth - visibleRunes(truncatedTitle)
		if metaWidth > 0 {
			header = lipgloss.JoinHorizontal(
				lipgloss.Top,
				header,
				strings.Repeat(" ", max(metaWidth-visibleRunes(meta), 0)),
				mutedStyle.Render(truncateRunes(meta, metaWidth)),
			)
		}
	}
	contentHeight := max(height-1, 1)
	content := lipgloss.NewStyle().
		Width(innerWidth).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Render(body)
	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(panelBorderColor).
		Render(lipgloss.JoinVertical(lipgloss.Left, header, content))
}

func renderKeyHelp(width int, focused bool) string {
	items := []string{
		"Esc",
		"Tab",
		"Ctrl+O",
		"Ctrl+K",
		"Ctrl+G thinking",
	}
	if !focused {
		items = []string{
			"Enter",
			"1-3",
			"Ctrl+O",
			"Ctrl+K",
			"Ctrl+G thinking",
		}
	}
	text := strings.Join(items, "  ")
	return mutedStyle.MaxWidth(max(width, 1)).Render(ansi.Truncate(text, max(width, 1), "…"))
}

func renderModeStrip(active string, width int) string {
	if active == "" {
		active = "Auto"
	}
	labels := []string{"Ask", "Plan", "Auto", "Swarm"}
	rendered := make([]string, 0, len(labels))
	for _, label := range labels {
		if strings.EqualFold(label, active) {
			rendered = append(rendered, lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(label))
			continue
		}
		rendered = append(rendered, mutedStyle.Render(label))
	}
	return lipgloss.NewStyle().
		Width(max(width, 1)).
		MaxWidth(max(width, 1)).
		Render(strings.Join(rendered, " "))
}

func (m Model) renderStatusBar(width int) string {
	route := m.state.ActiveRoute()
	role := "inactive"
	modelProvider := "no model"
	locality := "remote-ok"
	if route.Active {
		role = string(route.Role)
		modelProvider = fmt.Sprintf("%s @ %s", route.Model, route.Provider)
		if route.LocalOnly {
			locality = "local"
		}
	} else if !m.state.Config.Privacy.RemoteProvidersAllowed {
		locality = "local"
	}

	activity := m.state.Activity()
	var busyText string
	switch activity.Kind {
	case session.ActivityIdle:
		if m.lastActivityLabel != "" && time.Since(m.lastActivityDone) < doneDisplayDuration {
			busyText = fmt.Sprintf("✓ %s", truncateRunes(m.lastActivityLabel, 30))
		} else {
			busyText = "IDLE"
		}
	case session.ActivityThinking:
		busyText = fmt.Sprintf("%s thinking...", m.spinnerFrame)
	case session.ActivityTool, session.ActivityApproval:
		label := activity.Label
		if label == "" {
			label = string(activity.Kind)
		}
		busyText = fmt.Sprintf("%s %s", m.spinnerFrame, truncateRunes(label, 30))
	default:
		busyText = "IDLE"
	}

	parts := []string{
		statusBarBrand.Render("MARSHAL"),
		" Auto ",
		fmt.Sprintf(" %s ", truncateRunes(role, 16)),
		fmt.Sprintf(" %s ", truncateRunes(modelProvider, 28)),
		fmt.Sprintf(" %s ", locality),
		statusBarBusy.Width(9).MaxWidth(9).Render(truncateRunes(busyText, 9)),
	}
	line := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	return statusBarBg.Width(width).MaxWidth(width).Render(truncateRunes(line, width))
}

func riskText(tc *session.PendingToolCall) string {
	if tc.Risk != "" {
		return tc.Risk
	}
	return tc.Reason
}

func visibleRunes(s string) int {
	return ansi.StringWidth(s)
}

// renderMessage formats a chat message so its content wraps within the given
// viewport width instead of being clipped. The role label is printed on the
// first line and continuation lines are indented to align with the content.
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

var (
	panelBorderColor = lipgloss.Color("240")
	panelSoftColor   = lipgloss.Color("236")
	accentColor      = lipgloss.Color("38")
	violetColor      = lipgloss.Color("99")
	dimColor         = lipgloss.Color("244")
	mutedColor       = lipgloss.Color("247")
	successColor     = lipgloss.Color("71")
	warningColor     = lipgloss.Color("178")
	errorColor       = lipgloss.Color("167")

	mutedStyle      = lipgloss.NewStyle().Foreground(dimColor)
	panelTitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("255")).
			Bold(true)
	thinkingLineStyle = lipgloss.NewStyle().
				Foreground(dimColor).
				Italic(true)
	userRoleStyle   = lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	agentRoleStyle  = lipgloss.NewStyle().Foreground(violetColor).Bold(true)
	toolRoleStyle   = lipgloss.NewStyle().Foreground(warningColor).Bold(true)
	outputRoleStyle = lipgloss.NewStyle().Foreground(warningColor).Bold(true)

	activePillStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accentColor).
			Foreground(accentColor).
			Padding(0, 1)
	inactivePillStyle = lipgloss.NewStyle().
				Foreground(dimColor).
				Padding(0, 1)

	statusBarBrand = lipgloss.NewStyle().
			Background(violetColor).
			Foreground(lipgloss.Color("255")).
			Padding(0, 1).
			Bold(true)
	statusBarBg = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(lipgloss.Color("252"))
	statusBarBusy = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(warningColor).
			Padding(0, 1).
			Bold(true)
)

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return m.fallbackView()
	}

	if m.settingsOpen {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.settingsModel.View())
	}
	if m.memoryOpen {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.memoryModel.View())
	}

	tc := m.state.PendingApproval()

	chatPanel := m.renderChatPanel(tc)
	inputPanel := m.renderInputArea()
	leftColumn := lipgloss.JoinVertical(lipgloss.Left, chatPanel, inputPanel)

	rightColumn := m.renderRightInfoPanel(tc)

	mainLayout := lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, rightColumn)

	topBar := lipgloss.JoinHorizontal(
		lipgloss.Top,
		mutedStyle.Width(5).Render("● ● ●"),
		lipgloss.NewStyle().Width(max(m.width-25, 1)).Align(lipgloss.Center).Bold(true).Render("Marshal"),
		renderModeStrip("Auto", 20),
	)

	statusBar := m.renderStatusBar(m.width)
	return lipgloss.JoinVertical(lipgloss.Left, topBar, mainLayout, statusBar)
}

func (m Model) renderChatPanel(tc *session.PendingToolCall) string {
	if err := m.state.ProviderError(); err != nil {
		body := lipgloss.NewStyle().
			Foreground(errorColor).
			Render("! " + truncateRunes(err.Error(), max(m.leftWidth-2, 1)))
		return renderPanel("Provider Error", "fits AltScreen", body, m.leftWidth, m.chatHeight)
	}
	if tc != nil {
		return m.renderApprovalArea(tc)
	}
	return renderPanel("Chat", "live transcript", m.viewport.View(), m.leftWidth, m.chatHeight)
}

func (m Model) renderApprovalArea(tc *session.PendingToolCall) string {
	helpLine := "Enter approve  d deny  e edit  a always allow"
	if m.state.HasBackup() {
		helpLine += "  r rollback"
	}

	if tc.Diff == "" {
		body := strings.Join([]string{
			panelTitleStyle.Foreground(accentColor).Render("Agent wants to run"),
			truncateRunes(tc.Command, max(m.leftWidth, 1)),
			"",
			mutedStyle.Render("Reason"),
			truncateRunes(tc.Reason, max(m.leftWidth, 1)),
			"",
			mutedStyle.Render("Risk"),
			truncateRunes(riskText(tc), max(m.leftWidth, 1)),
			"",
			helpLine,
		}, "\n")
		return renderPanel("Approval", "pending", body, m.leftWidth, m.chatHeight)
	}

	splitWidth := max((m.leftWidth-4)/2, 10)
	diffLines := strings.Split(tc.Diff, "\n")
	maxDiffLines := max(m.chatHeight-1, 1)
	if len(diffLines) > maxDiffLines {
		diffLines = diffLines[:maxDiffLines]
	}
	for i := range diffLines {
		diffLines[i] = truncateRunes(diffLines[i], splitWidth)
	}
	diffBody := strings.Join(diffLines, "\n")
	diffPanel := renderPanel("Diff", "proposed patch", diffBody, splitWidth, m.chatHeight)

	approvalBody := strings.Join([]string{
		panelTitleStyle.Foreground(accentColor).Render("Agent wants to run"),
		truncateRunes(tc.Command, max(splitWidth, 1)),
		"",
		mutedStyle.Render("Reason"),
		truncateRunes(tc.Reason, max(splitWidth, 1)),
		"",
		mutedStyle.Render("Risk"),
		truncateRunes(riskText(tc), max(splitWidth, 1)),
		"",
		helpLine,
	}, "\n")
	approvalPanel := renderPanel("Approval", "security", approvalBody, splitWidth, m.chatHeight)
	return lipgloss.JoinHorizontal(lipgloss.Top, diffPanel, approvalPanel)
}

func (m Model) renderInputArea() string {
	inputStyle := lipgloss.NewStyle().
		Width(m.leftWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(panelBorderColor).
		Padding(0, 1)
	inputLine := lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render("❯ "),
		m.input.View(),
	)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		inputStyle.Render(inputLine),
		renderKeyHelp(m.leftWidth, m.inputFocused),
	)
}

func renderMessage(role, content string, width int) string {
	if width < 1 {
		width = 1
	}
	label := strings.ToLower(role)
	roleStyle := mutedStyle
	switch label {
	case "user":
		roleStyle = userRoleStyle
	case "agent", "assistant":
		roleStyle = agentRoleStyle
	case "tool":
		roleStyle = toolRoleStyle
	case "output":
		roleStyle = outputRoleStyle
	}

	prefixWidth := 10
	contentWidth := max(width-prefixWidth-2, 1)
	wrapped := ansi.Wrap(content, contentWidth, "")
	var b strings.Builder
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		if i == 0 {
			b.WriteString(roleStyle.Width(prefixWidth).Align(lipgloss.Right).Render(label))
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
			continue
		}
		b.WriteString(strings.Repeat(" ", prefixWidth+2))
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// pillHeight is the number of lines activePillStyle occupies once rendered
// (its rounded border adds a top and bottom line around the label).
var pillHeight = lipgloss.Height(activePillStyle.Render("x"))

// padPillHeight vertically pads a rendered (borderless) pill block with
// blank lines so its height matches activePillStyle's bordered block. This
// keeps active and inactive pills the same rendered height — required so
// lipgloss.JoinHorizontal aligns them on a single row — without adding the
// horizontal border characters that a real border would introduce.
func padPillHeight(rendered string) string {
	height := lipgloss.Height(rendered)
	extra := pillHeight - height
	if extra <= 0 {
		return rendered
	}
	blank := strings.Repeat(" ", lipgloss.Width(rendered))
	top := extra / 2
	bottom := extra - top
	lines := make([]string, 0, top+1+bottom)
	for i := 0; i < top; i++ {
		lines = append(lines, blank)
	}
	lines = append(lines, rendered)
	for i := 0; i < bottom; i++ {
		lines = append(lines, blank)
	}
	return strings.Join(lines, "\n")
}

func renderSidebarTabs(width int, active int) string {
	names := []string{"Plan", "Context", "Log"}
	parts := make([]string, 0, len(names))
	for i, name := range names {
		label := fmt.Sprintf("%d %s", i+1, name)
		if active == i {
			parts = append(parts, activePillStyle.Render(label))
			continue
		}
		parts = append(parts, padPillHeight(inactivePillStyle.Render(label)))
	}
	return lipgloss.NewStyle().
		Width(max(width, 1)).
		MaxWidth(max(width, 1)).
		Render(lipgloss.JoinHorizontal(lipgloss.Top, parts...))
}

func (m Model) renderPlanTab(width int, height int, tc *session.PendingToolCall) string {
	plan := m.state.Plan()
	activity := m.state.Activity()

	var b strings.Builder

	if len(plan) == 0 && activity.Kind == session.ActivityIdle {
		b.WriteString(mutedStyle.Render("No active plan. Ask the agent what to do next."))
		b.WriteString("\n\n→  Ready for input")
		return lipgloss.NewStyle().Width(width).MaxWidth(width).Height(height).MaxHeight(height).Render(b.String())
	}

	if len(plan) > 0 {
		b.WriteString(mutedStyle.Render("Current Plan:"))
		b.WriteString("\n")
		for _, item := range plan {
			b.WriteString(fmt.Sprintf(" ● %s\n", truncateRunes(item, max(width-4, 1))))
		}
	}

	if tc != nil {
		b.WriteString(fmt.Sprintf("\n→  Pending approval: %s", truncateRunes(tc.Command, max(width-22, 1))))
	} else if activity.Kind != session.ActivityIdle {
		label := activity.Label
		if activity.Kind == session.ActivityThinking {
			label = "thinking..."
		}
		b.WriteString(fmt.Sprintf("\n→  %s %s", m.spinnerFrame, truncateRunes(label, max(width-6, 1))))
	} else {
		b.WriteString("\n→  Ready for input")
	}

	return lipgloss.NewStyle().Width(width).MaxWidth(width).Height(height).MaxHeight(height).Render(b.String())
}

func (m Model) renderContextTab(width int, height int) string {
	pack := m.state.ContextPack()
	if pack.IsEmpty() {
		return mutedStyle.Width(width).MaxWidth(width).Height(height).Render("No context pack built yet.")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Context Pack\n")
	fmt.Fprintf(&b, "ctx %s / %s\n\n", compactTokenCount(pack.TokenUsage.EstimatedTokens), compactTokenCount(pack.TokenUsage.MaxTokens))
	for i, section := range pack.Sections {
		if i >= 6 {
			break
		}
		title := section.Title
		if title == "" {
			title = section.Source
		}
		// Budget accounts for a leading "N  " index (up to 3 chars), a
		// trailing "  " separator, and a token suffix from
		// compactTokenCount which is normally <=3 chars ("999") but can
		// grow to 4+ for very large packs (e.g. "100k"). MaxWidth below
		// clamps any residual overflow, so this fixed budget is a safe
		// approximation rather than an exact fit.
		fmt.Fprintf(&b, "%d  %s  %s\n", i+1, truncateRunes(title, max(width-8, 1)), compactTokenCount(section.EstimatedTokens))
	}
	return lipgloss.NewStyle().Width(width).MaxWidth(width).Height(height).MaxHeight(height).Render(b.String())
}

func (m Model) renderLogTab(width int, height int) string {
	auditLog := m.state.AuditLog()
	if len(auditLog) == 0 {
		return mutedStyle.Width(width).MaxWidth(width).Height(height).Render("No tool calls yet.")
	}
	var b strings.Builder
	for i, event := range auditLog {
		if i >= 8 {
			break
		}
		fmt.Fprintf(&b, "%s  %s  %s\n",
			event.Timestamp.Format("15:04"),
			truncateRunes(event.ToolName, 10),
			truncateRunes(event.ResultSummary, max(width-20, 1)),
		)
	}
	return lipgloss.NewStyle().Width(width).MaxWidth(width).Height(height).MaxHeight(height).Render(b.String())
}

func compactTokenCount(tokens int) string {
	if tokens >= 1000 {
		return fmt.Sprintf("%dk", tokens/1000)
	}
	return fmt.Sprintf("%d", tokens)
}

func (m Model) renderRightInfoPanel(tc *session.PendingToolCall) string {
	innerWidth := max(m.rightWidth, 1)
	bodyHeight := max(m.contentHeight-4, 1)
	tabs := renderSidebarTabs(innerWidth, m.activeTab)

	var body string
	switch m.activeTab {
	case 0:
		body = m.renderPlanTab(innerWidth, bodyHeight, tc)
	case 1:
		body = m.renderContextTab(innerWidth, bodyHeight)
	default:
		body = m.renderLogTab(innerWidth, bodyHeight)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, tabs, body)
	return renderPanel("", "inspector", content, m.rightWidth, m.contentHeight)
}

func (m Model) fallbackView() string {
	if m.settingsOpen {
		return m.settingsModel.View()
	}
	if m.memoryOpen {
		return m.memoryModel.View()
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		mutedStyle.Render("Marshal — waiting for terminal resize..."),
	)
}
