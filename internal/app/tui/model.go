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
	chatBelowViewportRows       = 3 // input line + help line + rounding slack
	tabHeaderMaxRows            = 4 // cap wrapped tab header to this many rows
	helpMaxRows                 = chatBelowViewportRows - 1
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

	// Viewport content excludes the chat box border.
	m.viewport.Width = max(m.leftWidth-2, 1)
	m.viewport.Height = max(m.chatHeight, 1)

	// Input lives in a padded box with no border. inputStyle uses Width(m.leftWidth)
	// and Padding(0,1), so the textinput content width is leftWidth-4.
	m.input.Width = max(m.leftWidth-4, 1)
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
		m.refreshViewport()
		return m, nil
	case agentTickMsg:
		if !m.busy {
			return m, nil
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
	return style.Render("thinking\n\n"+tail) + "\n\n"
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

func riskText(tc *session.PendingToolCall) string {
	if tc.Reason != "" {
		return tc.Reason
	}
	return tc.Risk
}

func visibleRunes(s string) int {
	var count int
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		count++
	}
	return count
}

// renderMessage formats a chat message so its content wraps within the given
// viewport width instead of being clipped. The role label is printed on the
// first line and continuation lines are indented to align with the content.
func renderMessage(role, content string, width int) string {
	if width < 1 {
		width = 1
	}
	prefix := fmt.Sprintf("  %s: ", role)
	prefixWidth := visibleRunes(prefix)
	contentWidth := width - prefixWidth
	if contentWidth < 1 {
		contentWidth = 1
	}

	wrapped := ansi.Wrap(content, contentWidth, "")
	var b strings.Builder
	first := true
	for _, line := range strings.Split(wrapped, "\n") {
		if first {
			b.WriteString(prefix)
			b.WriteString(line)
			first = false
		} else if line == "" {
			// Preserve paragraph breaks without adding trailing indentation.
			b.WriteString("\n")
			continue
		} else {
			b.WriteString(strings.Repeat(" ", prefixWidth))
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
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

var (
	accentColor       = lipgloss.Color("86")  // Cyan/Teal
	dimColor          = lipgloss.Color("244") // Gray
	thinkingLineStyle = lipgloss.NewStyle().Foreground(dimColor).Italic(true)
	activeTabStyle    = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, true, false).
				BorderForeground(accentColor).
				Foreground(accentColor).
				Padding(0, 1)
	inactiveTabStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, true, false).
				BorderForeground(dimColor).
				Foreground(dimColor).
				Padding(0, 1)
	statusBarAccent = lipgloss.NewStyle().
			Background(accentColor).
			Foreground(lipgloss.Color("0")).
			Padding(0, 1).
			Bold(true)
	statusBarBg = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("252"))
	errorBannerStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("196")).
				Foreground(lipgloss.Color("255")).
				Padding(0, 1)
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

	// 1. Render Left Column Content
	var leftContent string
	if tc != nil && tc.Diff != "" {
		splitWidth := (m.leftWidth - 4) / 2
		if splitWidth < 1 {
			splitWidth = 1
		}
		diffStyle := lipgloss.NewStyle().
			Width(splitWidth).
			Height(m.chatHeight - 2).
			MaxHeight(m.chatHeight - 2).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(dimColor)
		diffView := diffStyle.Render(tc.Diff)

		approvalStyle := lipgloss.NewStyle().
			Width(splitWidth).
			Height(m.chatHeight - 2).
			MaxHeight(m.chatHeight - 2).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accentColor)

		var b strings.Builder
		b.WriteString("┌─ SECURITY APPROVAL REQUIRED ┐\n")
		b.WriteString(fmt.Sprintf("Command: %s\n", tc.Command))
		b.WriteString(fmt.Sprintf("Reason: %s\n", tc.Reason))
		b.WriteString(fmt.Sprintf("Risk: %s\n", tc.Risk))
		b.WriteString("\nOptions:\n")
		b.WriteString("[Enter] Approve\n[d] Deny\n[e] Edit\n[a] Always Allow\n")
		if m.state.HasBackup() {
			b.WriteString("[r] Rollback\n")
		}

		approvalView := approvalStyle.Render(b.String())
		leftContent = lipgloss.JoinHorizontal(lipgloss.Top, diffView, approvalView)
	} else if tc != nil {
		approvalStyle := lipgloss.NewStyle().
			Width(m.leftWidth).
			Height(m.chatHeight).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accentColor)

		cmdLine := truncateRunes(tc.Command, m.leftWidth-12)
		reasonLine := truncateRunes(tc.Reason, m.leftWidth-10)
		riskLine := truncateRunes(riskText(tc), m.leftWidth-8)

		var b strings.Builder
		b.WriteString("SECURITY APPROVAL REQUIRED\n\n")
		b.WriteString(fmt.Sprintf("Command: %s\n", cmdLine))
		b.WriteString(fmt.Sprintf("Reason: %s\n", reasonLine))
		b.WriteString(fmt.Sprintf("Risk: %s\n", riskLine))
		b.WriteString("\n[Enter] Approve  [d] Deny  [e] Edit")
		if tc.Command != "" {
			b.WriteString(fmt.Sprintf("  [a] Always allow \"%s\"", truncateRunes(tc.Command, 20)))
		}
		if m.state.HasBackup() {
			b.WriteString("  [r] Rollback")
		}
		b.WriteString("\n")

		leftContent = approvalStyle.Render(b.String())
	} else {
		leftContent = lipgloss.NewStyle().
			Width(m.leftWidth).
			Height(m.chatHeight).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(dimColor).
			Render(m.viewport.View())
	}

	// Render input box
	inputStyle := lipgloss.NewStyle().Width(m.leftWidth).Padding(0, 1)
	helpStyle := lipgloss.NewStyle().
		Foreground(dimColor).
		MaxWidth(m.leftWidth - 2)

	var helpText string
	if m.inputFocused {
		helpText = "  [Esc] Unfocus  [Tab] Cycle Tabs  [Ctrl+O] Settings  [Ctrl+K] Memories  [Ctrl+R] Rollback"
	} else {
		helpText = "  [Enter] Focus Input  [1-3] Switch Tabs  [Ctrl+O] Settings  [Ctrl+K] Memories  [Ctrl+R] Rollback"
	}

	inputContent := lipgloss.JoinVertical(lipgloss.Left,
		inputStyle.Render(m.input.View()),
		helpStyle.Render(helpText),
	)
	leftColumn := lipgloss.JoinVertical(lipgloss.Left, leftContent, inputContent)

	// 2. Render Right Column Content (Tabs)
	tabNames := []string{"Plan", "Context", "Log"}
	var headers []string
	for i, name := range tabNames {
		style := inactiveTabStyle
		if m.activeTab == i {
			style = activeTabStyle
		}
		headers = append(headers, style.Render(fmt.Sprintf("[%d] %s", i+1, name)))
	}
	tabHeader := lipgloss.JoinHorizontal(lipgloss.Top, headers...)
	tabHeader = lipgloss.NewStyle().
		Width(m.rightWidth - 2).
		MaxHeight(tabHeaderMaxRows).
		Render(tabHeader)

	var sidebarBody string
	sbStyle := lipgloss.NewStyle().
		Width(m.rightWidth - 2).
		Height(m.contentHeight - 3).
		MaxHeight(m.contentHeight - 3)

	switch m.activeTab {
	case 0:
		var sb strings.Builder
		sb.WriteString("Current Plan:\n\n")
		sb.WriteString(" ● Redesign terminal UI layout\n")
		if tc != nil {
			sb.WriteString(fmt.Sprintf("   → Pending approval: %s\n", tc.Command))
		} else if m.busy {
			sb.WriteString("   → Agent is executing tasks...\n")
		} else {
			sb.WriteString("   → Ready for user input.\n")
		}
		sidebarBody = sbStyle.Render(sb.String())
	case 1:
		var sb strings.Builder
		pack := m.state.ContextPack()
		if pack.IsEmpty() {
			sb.WriteString("No context pack built yet.\n")
		} else {
			sb.WriteString(fmt.Sprintf("Pack: %d/%d tokens\n\n", pack.TokenUsage.EstimatedTokens, pack.TokenUsage.MaxTokens))
			for _, section := range pack.Sections {
				sb.WriteString(fmt.Sprintf("📄 %s (%d tk)\n", section.Title, section.EstimatedTokens))
			}
		}
		sidebarBody = sbStyle.Render(sb.String())
	case 2:
		var sb strings.Builder
		auditLog := m.state.AuditLog()
		if len(auditLog) == 0 {
			sb.WriteString("No tool calls yet.\n")
		} else {
			for _, event := range auditLog {
				sb.WriteString(fmt.Sprintf("[%s] %s -> %s\n", event.Timestamp.Format("15:04:05"), event.ToolName, event.ResultSummary))
			}
		}
		sidebarBody = sbStyle.Render(sb.String())
	}

	sidebarContent := lipgloss.JoinVertical(lipgloss.Left, tabHeader, sidebarBody)
	rightColumn := lipgloss.NewStyle().
		Width(m.rightWidth).
		Height(m.contentHeight).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(dimColor).
		Render(sidebarContent)

	// 3. Render Status Bar
	localOnlyText := fmt.Sprintf(" local-only=%t ", !m.state.Config.Privacy.RemoteProvidersAllowed)
	busyStyle := statusBarAccent.Width(9)
	busyText := "  IDLE   "
	if m.busy {
		busyText = " WORKING "
	}
	busyItem := busyStyle.Render(busyText)

	// The plan caps project at 16 runes and cwd at 24 runes. In narrow
	// terminals those fields would force the status bar to wrap, so we
	// allocate the remaining width greedily (project first, then cwd) while
	// respecting the caps and dropping a field entirely when even its label
	// wouldn't fit.
	fixedWidth := visibleRunes(statusBarAccent.Render(" MARSHAL ")) +
		visibleRunes(busyItem) +
		visibleRunes(localOnlyText)
	remaining := m.width - fixedWidth
	const projectOverhead = 10 // " project=" + " "
	const cwdOverhead = 6      // " cwd=" + " "
	projectMax, cwdMax := 0, 0
	if remaining > projectOverhead {
		projectMax = min(16, remaining-projectOverhead)
		remaining -= projectOverhead + projectMax
		if remaining > cwdOverhead {
			cwdMax = min(24, remaining-cwdOverhead)
		}
	}

	statusItems := []string{
		statusBarAccent.Render(" MARSHAL "),
	}
	if projectMax > 0 {
		statusItems = append(statusItems, fmt.Sprintf(" project=%s ", truncateRunes(m.state.Config.Project.Name, projectMax)))
	}
	if cwdMax > 0 {
		statusItems = append(statusItems, fmt.Sprintf(" cwd=%s ", truncateRunes(m.state.WorkingDir, cwdMax)))
	}
	statusItems = append(statusItems, localOnlyText, busyItem)

	statusBarText := lipgloss.JoinHorizontal(lipgloss.Top, statusItems...)
	statusBar := statusBarBg.Width(m.width).MaxWidth(m.width).Render(statusBarText)

	// Assemble layout
	mainLayout := lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, rightColumn)
	result := mainLayout
	if err := m.state.ProviderError(); err != nil {
		banner := errorBannerStyle.Width(m.width).MaxWidth(m.width).Render("Error: " + truncateRunes(err.Error(), m.width-8))
		result = lipgloss.JoinVertical(lipgloss.Left, mainLayout, banner)
	}
	return lipgloss.JoinVertical(lipgloss.Left, result, statusBar)
}

func (m Model) fallbackView() string {
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
