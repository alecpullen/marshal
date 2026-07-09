package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/settings"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/permissions"
	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
	"marshal/internal/tools/patch"
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
	SetPolicyRules(rules []config.PermissionRule)
}

const (
	minTerminalWidth  = 40
	minTerminalHeight = 10

	doneDisplayDuration = 2 * time.Second
)

type Model struct {
	state          *session.State
	input          textarea.Model
	editingCommand bool
	runner         AgentRunner
	swarmRunner    AgentRunner
	ctx            context.Context
	busy           bool
	settingsOpen   bool
	settingsModel  settings.Model
	configReloader ConfigReloader
	memoryOpen     bool
	memoryModel    memory.Model
	memoryDB       *db.DB
	memoryProject  int64
	cmdRegistry    *commands.Registry
	agentCancel    context.CancelFunc
	forceMode      string // reserved for future status-bar display
	approvalModel  *approvalModel
	questionModel  *questionModel

	// F18: editor completions. cmdPopup is fed by the commands registry
	// (triggered by `/` at position 0) and filePopup is fed by the repo
	// file index (triggered by `@` at a word start). fileIndex holds the
	// repo file paths used to build filePopup, loaded once at startup via
	// WithFileIndex (or lazy on first `@` keystroke — see
	// updateCompletionPopups).
	cmdPopup        *completionPopup
	filePopup       *completionPopup
	fileIndex       []completionItem
	fileIndexLoaded bool

	// F19 broker pump. jobBroker is the F5 job-event broker; the pump
	// cmd returned from Init (and re-armed from Update on each
	// jobCountMsg) bridges it into jobCountMsg values. jobCount is the
	// cached value the status line renders. When jobBroker is nil
	// (tests, fallback), the status line reads m.state.RunningJobsCount()
	// as the polled fallback.
	jobBroker *pubsub.Broker[native.JobEvent]
	jobCount  int

	// F16: steering broker pump. steeringBroker is the F16 message broker;
	// the pump cmd returned from Init (and re-armed from Update on each
	// steeringMsg) bridges it into steeringMsg values. queuedCount is the
	// cached count the status line and transcript render. When
	// steeringBroker is nil, m.queuedCount is driven by direct
	// state.SteeringQueue() reads.
	steeringBroker *pubsub.Broker[session.SteeringEvent]
	queuedCount    int

	// New Layout State
	width    int
	height   int
	viewport viewport.Model

	// Viewport dirty tracking.
	lastTranscriptHash uint64
	thinkingExpanded   bool
	viewportFollow     bool

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

// WithFileIndex seeds the F18 @file completion popup with a snapshot of
// the repo's file paths. Eager seeding is preferred when the model is
// constructed in a context that already holds a db.DB (avoids a
// per-keystroke DB hit on the first `@`); if the model is constructed
// without it, the popup falls back to a lazy load on the first `@`
// keystroke (see updateCompletionPopups).
func WithFileIndex(paths []string) Option {
	return func(m *Model) {
		m.fileIndex = buildFileIndexItems(paths)
		m.fileIndexLoaded = true
	}
}

func buildFileIndexItems(paths []string) []completionItem {
	items := make([]completionItem, 0, len(paths))
	for _, p := range paths {
		if containsRunnerWhitespace(p) {
			continue
		}
		items = append(items, completionItem{Text: p, Kind: completionFile})
	}
	return items
}

func containsRunnerWhitespace(s string) bool {
	return strings.ContainsAny(s, " \t\n\r\f")
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

// WithJobBroker wires the F19 pub/sub broker for F5 job-state changes.
// The model subscribes via pumpJobEvents from Init and re-arms the pump
// on each jobCountMsg. When broker is nil the model falls back to the
// polled m.state.RunningJobsCount() for tests that don't construct a
// broker.
func WithJobBroker(ctx context.Context, broker *pubsub.Broker[native.JobEvent]) Option {
	return func(m *Model) {
		m.ctx = ctx
		m.jobBroker = broker
	}
}

// WithSteeringBroker wires the F19 pub/sub broker for the F16 steering
// queue. The model subscribes via pumpSteeringEvents from Init and
// re-arms the pump on each steeringMsg. When broker is nil the status
// line and transcript still read the queue from session.State directly
// (used by tests that don't construct a broker).
func WithSteeringBroker(ctx context.Context, broker *pubsub.Broker[session.SteeringEvent]) Option {
	return func(m *Model) {
		m.ctx = ctx
		m.steeringBroker = broker
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
	input.MinHeight = 1
	input.DynamicHeight = true
	input.SetHeight(1)
	input.SetWidth(80)

	// The prompt is rendered inside the textarea on every display line via
	// SetPromptFunc. promptWidth=2 reserves two cells on each line: line 0
	// shows "❯ " (width 2) and continuation/wrapped lines show "  " (two
	// spaces, width 2), so wrapped text aligns under the first line's text
	// column. "❯" is rune-width 1, so "❯ " and "  " are both visible width 2.
	input.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "❯ "
		}
		return "  "
	})
	// Re-apply width so the prompt's reserved cells are subtracted from
	// the text wrap width.
	input.SetWidth(80)

	km := textarea.DefaultKeyMap()
	km.InsertNewline.SetKeys("shift+enter")
	input.KeyMap = km
	input.Focus()

	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styles := textarea.DefaultDarkStyles()
	styles.Focused.Text = textStyle
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(dimColor)
	styles.Blurred.Text = textStyle
	styles.Blurred.Placeholder = lipgloss.NewStyle().Foreground(dimColor)

	// The prompt string from SetPromptFunc is re-rendered through
	// computedPrompt() (the Prompt style). Set it to a plain style so the
	// prompt keeps its default color — the ❯ glyph is visually distinct on
	// its own. (If you want the ❯ coral, set these to
	// lipgloss.NewStyle().Foreground(coralColor).Bold(true) instead.)
	styles.Focused.Prompt = lipgloss.NewStyle()
	styles.Blurred.Prompt = lipgloss.NewStyle()

	// CursorLine is the style wrapping the active text row. The upstream
	// default adds a dark background ("0") that extends across the full
	// line width (including padding spaces), producing a dark bar behind
	// the cursor line. We override it to have no background so the input
	// area stays on a single clean line.
	styles.Focused.CursorLine = textStyle
	styles.Blurred.CursorLine = lipgloss.NewStyle().Foreground(compat.AdaptiveColor{
		Light: lipgloss.Color("245"),
		Dark:  lipgloss.Color("7"),
	})

	// EndOfBuffer is the filler row(s) below the last line of text. The
	// upstream default applies a dark foreground ("0") that can leave a
	// faint artifact row when the textarea height is 1.
	styles.Focused.EndOfBuffer = lipgloss.NewStyle()
	styles.Blurred.EndOfBuffer = lipgloss.NewStyle()

	// The textarea keeps its virtual cursor (the v2 default); the coral
	// colour fills the rendered cursor block.
	styles.Cursor.Color = coralColor
	styles.Cursor.Blink = true
	input.SetStyles(styles)

	m := Model{
		state:          state,
		input:          input,
		editingCommand: false,
		ctx:            context.Background(),
		viewport:       viewport.New(),
		spinner:        NewSpinner(),
		now:            time.Now,
		viewportFollow: true,
	}
	for _, opt := range opts {
		opt(&m)
	}

	// F18: build the completion popups eagerly from whatever source data
	// was wired by options. The cmd popup is built empty and the
	// registry is queried lazily inside updateCompletionPopups (avoids
	// duplicating the registry snapshot and lets the registry be
	// registered in any order).
	if m.cmdPopup == nil {
		m.cmdPopup = newCompletionPopup(nil)
	}
	if m.filePopup == nil {
		m.filePopup = newCompletionPopup(m.fileIndex)
	}

	// Eagerly build inline approval/question forms if the session already
	// has a pending request, so the first render shows the huh surface
	// instead of the legacy fallback panels.
	if tc := m.state.PendingApproval(); tc != nil {
		m.approvalModel = newApprovalModel(tc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, m.state.HasBackup(), max(m.width-4, 30))
	}
	if q := m.state.PendingQuestion(); q != nil {
		m.questionModel = newQuestionModel(q, max(m.width-4, 30))
	}

	return m
}

func blinkCmd() tea.Cmd {
	return textarea.Blink
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{blinkCmd()}
	if m.jobBroker != nil {
		cmds = append(cmds, pumpJobEvents(m.ctx, m.jobBroker))
	}
	if m.steeringBroker != nil {
		cmds = append(cmds, pumpSteeringEvents(m.ctx, m.steeringBroker))
	}
	return tea.Batch(cmds...)
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

	// Input interior: the box border (1 each side) + padding (1 each side)
	// = 4 horizontal frame cells. The textarea's SetWidth sets the text
	// wrap width and internally subtracts promptWidth (2). Reserve 4 (box
	// frame) + 2 (safety margin from box width clipping) + 2 (prompt,
	// handled inside SetWidth) = 8 so rendered lines stay inside the box
	// content area and avoid boundary-case trailing-space wrap artifacts.
	m.input.SetWidth(max(width-8, 1))

	// Transcript viewport lives inside a subtle border frame.
	m.viewport.SetWidth(max(width-2, 1))
	m.viewport.SetHeight(max(height-transcriptFrameRows-m.swarmPanelRows()-m.inputAreaRows()-statusLineRows, 1))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Ctrl+C always quits, even while a settings/memory overlay or huh form
	// is open. Check it before any overlay routing so it can never be
	// captured by a form's keymap.
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+c" {
		m.state.Shutdown()
		return m, tea.Quit
	}

	// WindowSizeMsg must always resize the underlying layout (and the
	// settings/memory overlays) regardless of which overlay is open.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.resize(ws.Width, ws.Height)
		m.settingsModel.SetSize(m.width, m.height)
		m.memoryModel.SetSize(m.width, m.height)
		if m.approvalModel != nil {
			m.approvalModel.SetSize(max(m.width-4, 30))
		}
		if m.questionModel != nil {
			m.questionModel.SetSize(max(m.width-4, 30))
		}
		m.refreshViewport()
		return m, nil
	}

	// SavedMsg/CancelledMsg close the settings overlay; handle them before
	// the settings-open guard so they aren't fed back into the form.
	switch msg := msg.(type) {
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
	}

	// When the settings overlay is open, route every remaining message
	// (keypresses AND huh's internal navigation messages such as
	// nextFieldMsg/prevFieldMsg) to the settings form. huh drives field
	// advancement via command-produced messages that round-trip through
	// Update, so the form must see them all — not just KeyPressMsg.
	if m.settingsOpen {
		// Ctrl+O toggles the overlay closed.
		if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+o" {
			m.settingsOpen = false
			return m, nil
		}
		var cmd tea.Cmd
		m.settingsModel, cmd = m.settingsModel.Update(msg)
		return m, cmd
	}
	if m.memoryOpen {
		if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+k" {
			m.memoryOpen = false
			return m, nil
		}
		var cmd tea.Cmd
		m.memoryModel, cmd = m.memoryModel.Update(msg)
		return m, cmd
	}

	// Inline approval chooser: when a tool call is pending, route every
	// message (keypresses AND huh's internal nextField/nextGroup messages)
	// to the approval form so selection navigation round-trips correctly.
	// The edit sub-mode captures the edited command/args in the main
	// textarea before the decision is sent.
	if tc := m.state.PendingApproval(); tc != nil {
		return m.handleApproval(msg, tc)
	}

	// Inline question prompt: when a clarifying question is pending, route
	// messages to the question form.
	if q := m.state.PendingQuestion(); q != nil {
		return m.handleQuestion(msg, q)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Handled above; kept for exhaustiveness but unreachable.
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
	case jobCountMsg:
		m.jobCount = msg.count
		// Re-arm the pump: exactly one in-flight subscription at a time
		// (F19 R2). Return nil if no broker is wired so the cmd chain
		// terminates (this should not happen when the pump is sourced
		// from Init, but keeps Update safe under tests that wire msgs
		// directly).
		if m.jobBroker == nil {
			return m, nil
		}
		return m, pumpJobEvents(m.ctx, m.jobBroker)
	case steeringMsg:
		// F16: cache the queued count so the status line and transcript
		// render without polling, then re-arm the pump. The transcript
		// re-renders via the viewport dirty hash on the next refresh.
		m.queuedCount = msg.queueLen
		if m.steeringBroker == nil {
			m.refreshViewport()
			return m, nil
		}
		m.refreshViewport()
		return m, pumpSteeringEvents(m.ctx, m.steeringBroker)
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
	case tea.KeyPressMsg:
		// Global hotkeys — input is always focused. (Approval and question
		// pending states are routed above, before this switch.)
		switch msg.String() {
		case "esc":
			// F18: dismiss the active completion popup first. Only if
			// nothing is up do we fall through to cancelling the in-flight
			// turn.
			if m.activeCompletionPopup() != nil {
				m.activeCompletionPopup().dismiss()
				return m, nil
			}
			m.cancelTurn()
			return m, nil
		case "ctrl+o":
			m.settingsModel = settings.New(m.state.Config, m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))
			m.settingsModel.SetSize(m.width, m.height)
			m.settingsOpen = true
			return m, nil
		case "ctrl+k":
			if m.memoryDB == nil {
				return m, nil
			}
			m.memoryModel = memory.New(m.memoryDB, m.memoryProject)
			m.memoryModel.SetSize(m.width, m.height)
			m.memoryOpen = true
			return m, nil
		case "ctrl+g":
			m.thinkingExpanded = !m.thinkingExpanded
			m.lastTranscriptHash = 0
			m.refreshViewport()
			return m, nil
		case "ctrl+r":
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
		case "pgup", "pgdown":
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			if msg.String() == "pgup" {
				m.viewportFollow = false
			}
			if msg.String() == "pgdown" && m.viewport.AtBottom() {
				m.viewportFollow = true
			}
			return m, vpCmd
		case "ctrl+u":
			m.viewport.HalfPageUp()
			m.viewportFollow = false
			return m, nil
		case "ctrl+d":
			m.viewport.HalfPageDown()
			if m.viewport.AtBottom() {
				m.viewportFollow = true
			}
			return m, nil
		case "end":
			m.viewport.GotoBottom()
			m.viewportFollow = true
			return m, nil
		case "up":
			if m.activeCompletionPopup() != nil {
				m.activeCompletionPopup().moveUp()
				return m, nil
			}
		case "down":
			if m.activeCompletionPopup() != nil {
				m.activeCompletionPopup().moveDown()
				return m, nil
			}
		case "tab":
			if m.acceptCompletion() {
				return m, nil
			}
		case "ctrl+x":
			// F16 R3: clear the steering queue while the agent is
			// working. Out-of-band so /clear semantics don't collide.
			if m.busy {
				m.state.ClearSteering()
				m.queuedCount = 0
				m.refreshViewport()
				return m, nil
			}
		case "enter":
			// F18: if a popup is visible, accept it (replaces the trigger
			// token) and keep editing — Enter on a popup is a selection,
			// not a submit. Esc is the way to dismiss without accepting.
			if m.acceptCompletion() {
				return m, nil
			}
			value := strings.TrimSpace(m.input.Value())
			// F16 R2 follow-up turn: when the agent has finished and the
			// user presses Enter with no new input, pop the oldest queued
			// steering message and submit it as the next turn. This must
			// happen BEFORE the empty-input short-circuit so a blank
			// prompt still drains the queue.
			if value == "" && !m.busy {
				if followUp, ok := m.popOldestSteering(); ok {
					value = followUp
				} else {
					return m, nil
				}
			}
			if value == "" {
				return m, nil
			}
			m.input.Reset()
			m.dismissCompletionPopups()
			m.updateViewportHeight()
			m.viewportFollow = true

			if strings.HasPrefix(value, "/") {
				return m.dispatchCommand(value)
			}

			if m.busy {
				// F16: turn is running — enqueue as a steering message
				// instead of dropping the input.
				m.state.PushSteering(value)
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

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.updateCompletionPopups()

	// The textarea updates its own height (DynamicHeight); recalculate the
	// viewport height and refresh if it changed.
	viewportHeightChanged := m.updateViewportHeight()
	if viewportHeightChanged {
		m.lastTranscriptHash = 0
		m.refreshViewport()
	}

	return m, cmd
}

// handleApproval routes messages to the inline approval chooser (or the
// edit-command textarea sub-mode) while a tool-call approval is pending. It
// is called before the main keypress switch so huh's internal navigation
// messages (nextFieldMsg/nextGroupMsg) round-trip back to the form.
func (m Model) handleApproval(msg tea.Msg, tc *session.PendingToolCall) (tea.Model, tea.Cmd) {
	// Edit sub-mode: the main textarea captures the edited command/args.
	if m.editingCommand {
		if k, ok := msg.(tea.KeyPressMsg); ok {
			switch k.String() {
			case "esc":
				m.editingCommand = false
				m.input.Reset()
				m.input.Placeholder = "Ask Marshal..."
				m.updateViewportHeight()
				m.lastTranscriptHash = 0
				return m, nil
			case "enter":
				value := strings.TrimSpace(m.input.Value())
				if value != "" {
					tc.ResponseChan <- session.UserApprovalDecision{Approved: true, Edited: value}
					m.editingCommand = false
					m.input.Reset()
					m.input.Placeholder = "Ask Marshal..."
					m.updateViewportHeight()
					m.state.SetPendingApproval(nil)
					m.approvalModel = nil
				}
				m.lastTranscriptHash = 0
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.updateViewportHeight()
		return m, cmd
	}

	// Lazily build the inline approval chooser the first time a message
	// arrives for a pending tool call.
	if m.approvalModel == nil {
		m.approvalModel = newApprovalModel(tc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, m.state.HasBackup(), max(m.width-4, 30))
	}
	am, cmd := m.approvalModel.Update(msg)
	m.approvalModel = am
	if !m.approvalModel.IsDone() {
		return m, cmd
	}

	choice := m.approvalModel.Choice()
	m.approvalModel = nil
	switch choice {
	case choiceApprove:
		tc.ResponseChan <- session.UserApprovalDecision{Approved: true}
		m.state.SetPendingApproval(nil)
		m.lastTranscriptHash = 0
		return m, nil
	case choiceDeny:
		tc.ResponseChan <- session.UserApprovalDecision{Approved: false}
		m.state.SetPendingApproval(nil)
		m.lastTranscriptHash = 0
		return m, nil
	case choiceAlways:
		rule := permissions.Rule{
			Permission: permissions.PermissionForTool(tc.Name),
			Pattern:    patternForApproval(tc),
			Action:     permissions.ActionAllow,
		}
		userConfigPath := filepath.Join(userConfigDir(), "config.toml")
		if err := config.SaveUserConfigRule(userConfigPath, config.PermissionRule{
			Permission: rule.Permission,
			Pattern:    rule.Pattern,
			Action:     string(rule.Action),
		}); err != nil {
			m.state.AddSessionRule(tc.Command)
		} else {
			m.state.Config.Permissions.Rules = append(m.state.Config.Permissions.Rules, config.PermissionRule{
				Permission: rule.Permission,
				Pattern:    rule.Pattern,
				Action:     string(rule.Action),
			})
			if m.runner != nil {
				m.runner.SetPolicyRules(m.state.Config.Permissions.Rules)
			}
		}
		tc.ResponseChan <- session.UserApprovalDecision{Approved: true}
		m.state.SetPendingApproval(nil)
		m.lastTranscriptHash = 0
		return m, nil
	case choiceSessionAllow:
		m.state.AddSessionRule(tc.Command)
		tc.ResponseChan <- session.UserApprovalDecision{Approved: true}
		m.state.SetPendingApproval(nil)
		m.lastTranscriptHash = 0
		return m, nil
	case choiceEdit:
		m.editingCommand = true
		if tc.Name == "shell.run" {
			m.input.SetValue(tc.Command)
			m.input.Placeholder = "Edit command..."
		} else {
			m.input.SetValue(tc.Args)
			m.input.Placeholder = "Edit JSON arguments..."
		}
		m.updateViewportHeight()
		m.input.Focus()
		m.lastTranscriptHash = 0
		return m, nil
	case choiceRollback:
		if m.state.HasBackup() {
			_ = m.state.RollbackBackup()
			m.state.LogToolCall(registry.AuditEvent{
				Timestamp:     time.Now(),
				ToolName:      "rollback",
				ResultSummary: "Rollback applied successfully",
			})
			m.lastTranscriptHash = 0
			m.refreshViewport()
			// Keep the approval open so the user can then approve/deny the
			// original tool.
			return m, nil
		}
		return m, nil
	default:
		return m, nil
	}
}

// handleQuestion routes messages to the inline question form while a
// clarifying question is pending. On completion it sends the answers (or
// every question marked "Unanswered" on abort) to the runner's response
// channel.
func (m Model) handleQuestion(msg tea.Msg, q *session.PendingQuestion) (tea.Model, tea.Cmd) {
	if m.questionModel == nil {
		m.questionModel = newQuestionModel(q, max(m.width-4, 30))
	}
	qm, cmd := m.questionModel.Update(msg)
	m.questionModel = qm
	if !m.questionModel.IsDone() {
		return m, cmd
	}

	q.ResponseChan <- m.questionModel.Answers()
	m.state.SetPendingQuestion(nil)
	m.questionModel = nil
	m.input.Reset()
	m.input.Placeholder = "Ask Marshal..."
	m.updateViewportHeight()
	m.lastTranscriptHash = 0
	return m, nil
}

func (m Model) inputAreaRows() int {
	rows := inputBorderRows
	if m.state.Activity().Kind != session.ActivityIdle {
		rows += activityStripRows
	}
	if q := m.state.PendingQuestion(); q != nil {
		content := ""
		if m.questionModel != nil {
			content = m.questionModel.View()
		} else {
			content = renderQuestionPanel(q, max(m.width-4, 1))
		}
		rows += len(strings.Split(content, "\n"))
	} else if tc := m.state.PendingApproval(); tc != nil {
		content := ""
		if m.editingCommand {
			// The ❯ prompt is rendered inside the textarea by SetPromptFunc,
			// so m.input.View() already includes it — do not prepend it again.
			content = m.input.View()
		} else if m.approvalModel != nil {
			content = m.approvalModel.View()
		} else {
			content = renderApprovalPanel(tc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, max(m.width-4, 1))
		}
		rows += len(strings.Split(content, "\n"))
	} else {
		// DynamicHeight clamps Height() to [MinHeight, MaxHeight], so the
		// only guard needed is the max(..., 1) floor.
		rows += max(m.input.Height(), 1)
	}
	if p := m.activeCompletionPopup(); p != nil {
		// Cap the popup at 8 visible rows (matches the renderer in view.go).
		rows += min(len(p.matches()), 8)
	}
	return rows
}

func (m Model) swarmPanelRows() int {
	if m.state.SwarmProgress().Active {
		return swarmPanelRows
	}
	return 0
}

func (m *Model) updateViewportHeight() bool {
	newViewportHeight := max(m.height-transcriptFrameRows-m.swarmPanelRows()-m.inputAreaRows()-statusLineRows, 1)
	if newViewportHeight == m.viewport.Height() {
		return false
	}
	m.viewport.SetHeight(newViewportHeight)
	return true
}

// updateCompletionPopups inspects the current input value and updates the
// cmdPopup and filePopup state. Called from every keystroke.
//
// Triggers (F18 R1, R4):
//   - `/` at position 0 with no space in the typed value → cmdPopup filters
//     against the commands registry.
//   - `@` at a word start (preceded by start-of-input or whitespace) with
//     no whitespace after the `@` → filePopup filters against the repo
//     file index.
//
// The popups are mutually exclusive: the file popup takes precedence when
// both could match (e.g. "/@" would show commands; "@" at start shows
// files). When the input doesn't match either trigger, both popups are
// dismissed.
func (m *Model) updateCompletionPopups() {
	if m.cmdPopup == nil || m.filePopup == nil {
		return
	}
	value := m.input.Value()

	cmdTrigger, cmdQuery := m.commandTrigger(value)
	if cmdTrigger {
		if m.cmdPopup.items == nil && m.cmdRegistry != nil {
			// Lazily build the command items from the registry the first
			// time the user starts typing "/". Registry contents can
			// change at runtime; we re-build on every trigger so /help
			// always reflects the current registry.
			items := make([]completionItem, 0, len(m.cmdRegistry.List()))
			for _, c := range m.cmdRegistry.List() {
				text := "/" + c.Name
				items = append(items, completionItem{
					Text:        text,
					Description: c.Description,
					Kind:        completionCommand,
				})
			}
			m.cmdPopup.items = items
		}
		if cmdQuery == "" {
			// Bare "/" → show every command, unfiltered, so the user can
			// see what's available before typing.
			m.cmdPopup.filtered = append([]completionItem(nil), m.cmdPopup.items...)
			m.cmdPopup.index = 0
			m.cmdPopup.acceptedText = ""
			m.cmdPopup.visible = len(m.cmdPopup.filtered) > 0
		} else {
			m.cmdPopup.update(cmdQuery)
		}
		m.filePopup.dismiss()
		return
	}

	fileTrigger, fileQuery := m.fileTrigger(value)
	if fileTrigger {
		m.populateFileIndexIfNeeded()
		if m.filePopup.items == nil {
			m.filePopup.items = m.fileIndex
		}
		m.filePopup.update(fileQuery)
		m.cmdPopup.dismiss()
		return
	}

	m.cmdPopup.dismiss()
	m.filePopup.dismiss()
}

// commandTrigger returns (true, query) when value is a slash command in
// progress: starts with "/", has no whitespace (so we're still typing
// the command name, not its arguments), and is non-empty after the "/".
func (m *Model) commandTrigger(value string) (bool, string) {
	if !strings.HasPrefix(value, "/") {
		return false, ""
	}
	// "/plan " is committed — no longer a trigger.
	if strings.Contains(value, " ") || strings.Contains(value, "\n") {
		return false, ""
	}
	rest := strings.TrimPrefix(value, "/")
	if rest == "" {
		// A bare "/" shows the full command list (query "").
		return true, ""
	}
	return true, rest
}

// fileTrigger returns (true, query) when value contains an @-reference
// at a word start (preceded by start-of-input or whitespace) and the
// current word after "@" has no whitespace.
func (m *Model) fileTrigger(value string) (bool, string) {
	idx := strings.LastIndex(value, "@")
	if idx < 0 {
		return false, ""
	}
	// "@" must be at a runner-compatible trigger boundary.
	if idx > 0 {
		prev := value[idx-1]
		if !isAtFileBoundary(prev) {
			return false, ""
		}
	}
	// No whitespace between the "@" and end of value (otherwise the
	// user has already moved past the trigger and onto the next word).
	after := value[idx+1:]
	if containsRunnerWhitespace(after) {
		return false, ""
	}
	return true, after
}

func isAtFileBoundary(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	}
	return false
}

// populateFileIndexIfNeeded lazy-loads the repo file index on the first
// @-keystroke. Skipped when the model was constructed with WithFileIndex
// (eager seed) or when there is no DB / project id wired.
func (m *Model) populateFileIndexIfNeeded() {
	if m.fileIndexLoaded {
		return
	}
	if m.memoryDB == nil || m.memoryProject == 0 {
		// No way to populate; mark loaded so we don't keep retrying.
		m.fileIndexLoaded = true
		return
	}
	index, err := m.memoryDB.GetFileIndex(m.memoryProject)
	if err != nil {
		m.fileIndexLoaded = true
		return
	}
	paths := make([]string, 0, len(index))
	for _, f := range index {
		paths = append(paths, f.Path)
	}
	m.fileIndex = buildFileIndexItems(paths)
	m.fileIndexLoaded = true
}

// activeCompletionPopup returns whichever popup is currently visible
// (cmd takes precedence when both somehow show), or nil when none is up.
// Used by the keypress switch to route Up/Down/Tab/Esc.
func (m *Model) activeCompletionPopup() *completionPopup {
	if m.cmdPopup != nil && m.cmdPopup.isVisible() {
		return m.cmdPopup
	}
	if m.filePopup != nil && m.filePopup.isVisible() {
		return m.filePopup
	}
	return nil
}

func (m *Model) dismissCompletionPopups() {
	if m.cmdPopup != nil {
		m.cmdPopup.dismiss()
	}
	if m.filePopup != nil {
		m.filePopup.dismiss()
	}
}

// acceptCompletion accepts the active popup's current selection, replacing
// the trigger token in the input with the popup's acceptedText. Returns
// true when a popup was visible (and is now dismissed).
func (m *Model) acceptCompletion() bool {
	p := m.activeCompletionPopup()
	if p == nil {
		return false
	}
	p.accept()
	accepted := p.acceptedText
	value := m.input.Value()
	newValue := replaceTriggerToken(value, accepted)
	m.input.SetValue(newValue)
	// Move the cursor to the end of the inserted text so the user can
	// keep typing args / a trailing space directly.
	m.input.MoveToEnd()
	m.dismissCompletionPopups()
	m.updateViewportHeight()
	return true
}

// replaceTriggerToken finds the most recent trigger token in value
// (either the leading "/<cmd...>" word or the last "<sep>@<path...>"
// word at a word boundary) and replaces it with replacement. The cursor
// stays in the same word after replacement.
func replaceTriggerToken(value, replacement string) string {
	if value == "" {
		return replacement
	}
	// Command trigger: value starts with "/".
	if strings.HasPrefix(value, "/") && !strings.ContainsAny(value, " \t\n") {
		return replacement
	}
	// File trigger: find the last "@<...>" at a word start.
	idx := strings.LastIndex(value, "@")
	if idx < 0 {
		return value
	}
	if idx > 0 {
		prev := value[idx-1]
		if !isAtFileBoundary(prev) {
			return value
		}
	}
	return value[:idx] + replacement
}

// popOldestSteering returns and removes the oldest queued steering
// message. Used by the F16 R2 follow-up path so each Enter pops one
// item at a time; the runner's loop-top drain handles any remainder.
func (m *Model) popOldestSteering() (string, bool) {
	msg, ok := m.state.PopSteering()
	if ok {
		// Mirror the cached count; the broker is the source of truth in
		// production, but tests construct models without a broker.
		m.queuedCount = len(m.state.SteeringQueue())
	}
	return msg, ok
}

func (m *Model) refreshViewport() {
	m.updateViewportHeight()
	items := m.state.Transcript()
	inProgress := m.state.InProgress()
	streamLen := len(inProgress.Reasoning)
	_, activeTool := m.state.ActiveToolCall()
	busy := m.busy || activeTool || streamLen > 0

	todos := m.state.Todos()
	queued := m.state.SteeringQueue()
	hash := transcriptHash(items, streamLen, busy, m.viewport.Width(), todos, queued)
	if hash == m.lastTranscriptHash {
		return
	}
	m.lastTranscriptHash = hash

	var b strings.Builder
	if todoPanel := renderTodos(todos, m.viewport.Width()); todoPanel != "" {
		b.WriteString(todoPanel)
		b.WriteString("\n")
	}
	if len(items) == 0 {
		b.WriteString(renderWelcomeBanner(m.viewport.Width()))
	}
	for _, item := range items {
		b.WriteString(renderTranscriptItem(item, m.thinkingExpanded, m.viewport.Width()))
	}

	if inProgress.Active && inProgress.Reasoning != "" {
		b.WriteString(renderThinkingBox(inProgress.Reasoning, m.spinnerFrame, m.viewport.Width()))
	}
	if atc, ok := m.state.ActiveToolCall(); ok {
		b.WriteString(renderActiveToolCall(atc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, m.spinnerFrame, m.now(), m.viewport.Width()))
	}
	if err := m.state.ProviderError(); err != nil {
		b.WriteString(renderProviderError(err, m.viewport.Width()))
	}
	if len(queued) > 0 {
		b.WriteString(renderQueuedMessages(queued, m.viewport.Width()))
	}

	m.viewport.SetContent(b.String())
	if m.viewportFollow {
		m.viewport.GotoBottom()
	}
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
// the /stop command. F16 R5: also drops any messages queued before the
// interrupt — they were enqueued for the turn we just killed, and
// keeping them would re-fire on the next follow-up.
func (m *Model) cancelTurn() bool {
	if m.agentCancel == nil {
		return false
	}
	m.agentCancel()
	m.agentCancel = nil
	m.state.ClearSteering()
	m.queuedCount = 0
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

func transcriptHash(items []session.TranscriptItem, streamLen int, busy bool, width int, todos []native.TodoItem, queued []string) uint64 {
	var h uint64
	h = uint64(len(items)) ^ (uint64(streamLen) << 20) ^ (uint64(width) << 40) ^ (uint64(len(todos)) << 50) ^ (uint64(len(queued)) << 60)
	for i, item := range items {
		h ^= uint64(item.Timestamp.UnixNano()) * uint64(i+1)
	}
	for i, todo := range todos {
		h ^= uint64(len(todo.Content)+len(todo.Status)) * uint64(i+7)
	}
	for i, q := range queued {
		h ^= uint64(len(q)) * uint64(i+13)
	}
	if busy {
		h ^= 0xDEADBEEF
	}
	return h
}

func permissionForTool(toolName string) string {
	switch toolName {
	case "shell.run", "test.run":
		return "shell"
	case "file.write_patch":
		return "file.write_patch"
	default:
		return toolName
	}
}

func patternForApproval(tc *session.PendingToolCall) string {
	if tc.Name == "shell.run" || tc.Name == "test.run" {
		words := strings.Fields(tc.Command)
		if len(words) > 0 {
			return words[0] + " *"
		}
		return "*"
	}
	if tc.Name == "file.write_patch" {
		patches, err := patch.Parse(tc.Args)
		if err != nil || len(patches) == 0 {
			return "**"
		}
		dir := commonDir(patches)
		if dir == "" || dir == "." {
			return "**"
		}
		return dir + "/**"
	}
	return "*"
}

func commonDir(patches []patch.FilePatch) string {
	if len(patches) == 0 {
		return ""
	}
	var dirs []string
	for _, p := range patches {
		dirs = append(dirs, filepath.Dir(p.Path))
	}
	if len(dirs) == 1 {
		return dirs[0]
	}
	common := dirs[0]
	for _, d := range dirs[1:] {
		common = longestCommonPrefix(common, d, string(filepath.Separator))
		if common == "" {
			return ""
		}
	}
	return common
}

func longestCommonPrefix(a, b, sep string) string {
	partsA := strings.Split(a, sep)
	partsB := strings.Split(b, sep)
	var common []string
	for i := 0; i < len(partsA) && i < len(partsB); i++ {
		if partsA[i] == partsB[i] {
			common = append(common, partsA[i])
		} else {
			break
		}
	}
	return strings.Join(common, sep)
}

func userConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "marshal")
}
