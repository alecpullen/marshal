package tui

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"image/color"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/shlex"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/connect"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/theme"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/permissions"
	"marshal/internal/pubsub"
	"marshal/internal/strutil"
	"marshal/internal/tools/native"
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
	minTerminalWidth  = 80
	minTerminalHeight = 24

	doneDisplayDuration = 2 * time.Second

	// settingsBusyMessage is shown when runtime work makes a settings change
	// unsafe to persist.
	settingsBusyMessage = "Stop the active turn and background jobs before applying settings."

	browserBarRows = 1
)

type Model struct {
	state          *session.State
	input          textarea.Model
	editingCommand bool
	runner         AgentRunner
	swarmRunner    AgentRunner
	sddRunner      AgentRunner
	ctx            context.Context
	busy           bool
	configReloader ConfigReloader
	memoryDB       *db.DB
	memoryProject  int64
	cmdRegistry    *commands.Registry
	agentCancel    context.CancelFunc
	forceMode      string // current interaction mode: "ask", "edit", or "" (auto). Rendered in the help overlay and status line.
	// sddPanelBody and sddPanelCachedRows are computed once per View() call
	// to avoid double-rendering the SDD panel (once for height, once for
	// content). They are reset at the start of each viewString().
	sddPanelBody       string
	sddPanelCachedRows int
	approvalModel      *approvalModel
	questionModel      *questionModel

	// F18: editor completions. cmdPopup is fed by the commands registry
	// (triggered by `/` at position 0) and filePopup is fed by the repo
	// file index (triggered by `@` at a word start). fileIndex holds the
	// repo file paths used to build filePopup, loaded once at startup via
	// WithFileIndex (or lazy on first `@` keystroke — see
	// updateCompletionPopups). lastInputForPopups caches the value seen
	// by updateCompletionPopups so non-key events (mouse, paste, ticks)
	// don't re-evaluate and clobber the popup's index/offset.
	cmdPopup           *completionPopup
	filePopup          *completionPopup
	setPopup           *completionPopup
	fileIndex          []completionItem
	fileIndexLoaded    bool
	lastInputForPopups string
	setReg             *settings.Registry
	// configSavePending means state.Config contains a change that could not
	// be persisted. An otherwise unchanged /set retries the full config.
	configSavePending bool

	// F19 broker pump. jobBroker is the F5 job-event broker; the pump
	// cmd returned from Init (and re-armed from Update on each
	// jobCountMsg) bridges it into jobCountMsg values. jobCount is the
	// cached value the status line renders. When jobBroker is nil
	// (tests, fallback), the status line reads m.state.RunningJobsCount()
	// as the polled fallback.
	jobBroker *pubsub.Broker[native.JobEvent]
	jobEvents <-chan pubsub.Event[native.JobEvent]
	jobCount  int

	// F16: steering broker pump. steeringBroker is the F16 message broker;
	// the pump cmd returned from Init (and re-armed from Update on each
	// steeringMsg) bridges it into steeringMsg values. queuedCount is the
	// cached count the status line and transcript render. When
	// steeringBroker is nil, m.queuedCount is driven by direct
	// state.SteeringQueue() reads.
	steeringBroker *pubsub.Broker[session.SteeringEvent]
	steeringEvents <-chan pubsub.Event[session.SteeringEvent]
	queuedCount    int
	cancelling     bool

	// New Layout State
	rawWidth  int // unclamped terminal dimensions (gate check)
	rawHeight int
	width     int // clamped to ≥ minTerminalWidth/Height (internal geometry)
	height    int
	viewport  viewport.Model

	// Viewport dirty tracking.
	lastTranscriptHash uint64
	thinkingExpanded   bool
	viewportFollow     bool

	// Connect panel (docked; opened by /connect, /models, Ctrl+P).
	connectModel *connect.Model
	discovered   map[string][]string

	// Picker modal (opened by commands like /model, /rewind, /branches, /mode).
	// The picker itself is hosted in m.dock; pickerCommand records which
	// command opened it so PickedMsg can dispatch correctly.
	pickerCommand string
	dock          dock.Host

	spinner           Spinner
	spinnerFrame      string
	lastActivityLabel string
	lastActivityDone  time.Time
	lastActivityKind  session.ActivityKind
	successPulse      bool
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

// WithSDDRunner configures the TUI to route /sdd <plan-file> and
// /mode→SDD submissions to the SDD orchestrator.
func WithSDDRunner(ctx context.Context, runner AgentRunner) Option {
	return func(m *Model) {
		m.ctx = ctx
		m.sddRunner = runner
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
	return config.ProjectConfigPath(workingDir)
}

func relPath(workingDir, path string) string {
	rel, err := filepath.Rel(workingDir, path)
	if err != nil {
		return path
	}
	return rel
}

// applyNewConfig installs cfg as the live session config and invalidates
// anything derived from it.
func (m *Model) applyNewConfig(cfg config.Config) {
	m.state.Config = cfg
	m.setReg = nil
	m.setPopup = nil
	m.lastInputForPopups = ""
	loadTheme(cfg.TUI)
}

// persistAndReload saves cfg to the project config file, asks the runtime
// to reload it, and applies the outcome to TUI state. It returns the save
// error or, when saving succeeded, the reload error (nil on full success).
// On any error the in-memory change is kept (and configSavePending set for
// save failures) so the user can fix the underlying problem and retry
// without losing their edit. Messaging is the caller's job.
func (m *Model) persistAndReload(cfg config.Config) (saveErr, reloadErr error) {
	path := projectConfigPath(m.state.WorkingDir)
	if err := config.SaveProjectConfig(path, cfg); err != nil {
		m.applyNewConfig(cfg)
		m.configSavePending = true
		return err, nil
	}
	m.configSavePending = false
	if m.configReloader != nil {
		// reloadAgentRuntime may install cfg before reporting a cleanup
		// error; invalidate config-derived state before attempting it.
		m.setReg = nil
		if err := m.configReloader(cfg); err != nil {
			// The runtime has already swapped cfg before cleanup can fail.
			// Keep all TUI-derived state aligned with that live config.
			m.applyNewConfig(cfg)
			return nil, err
		}
	}
	m.applyNewConfig(cfg)
	return nil, nil
}

// settingsRegistry returns the cached /set registry, rebuilt after a config
// change invalidates it.
func (m *Model) settingsRegistry() *settings.Registry {
	if m.setReg == nil {
		m.setReg = settings.BuildRegistry(m.state.Config)
	}
	return m.setReg
}

func (m *Model) handleSetCommand(args []string) {
	sys := func(text string) {
		m.state.AddMessage(session.RoleSystem, text, session.ContentTypePlain)
	}

	switch len(args) {
	case 0:
		m.openSettingsBrowser("")
		return
	case 1:
		key := args[0]
		reg := m.settingsRegistry()
		kind, current, options, err := reg.Describe(key)
		if err != nil {
			m.openSettingsBrowser(key)
			return
		}
		line := fmt.Sprintf("%s = %s (%s)", key, current, kind)
		if len(options) > 0 {
			line += " · options: " + strings.Join(options, ", ")
		}
		sys(line)
		return
	default:
		key, value := args[0], strings.Join(args[1:], " ")
		if reason := m.settingsBlockReason(); reason != "" {
			sys(reason)
			return
		}
		reg := m.settingsRegistry()
		change, err := reg.Apply(key, value)
		if err != nil {
			m.setReg = nil
			sys("✗ " + key + ": " + err.Error())
			return
		}
		if !change.Changed {
			if !m.configSavePending {
				sys(fmt.Sprintf("• %s unchanged (%s)", key, change.NewValue))
				return
			}
		}
		saveErr, reloadErr := m.persistAndReload(reg.Config())
		m.refreshOpenSettingsBrowser()
		switch {
		case saveErr != nil:
			sys(fmt.Sprintf("✗ %s applied in session, but save failed: %v", key, saveErr))
		case reloadErr != nil:
			sys(fmt.Sprintf("✗ %s saved, but live reload failed: %v", key, reloadErr))
		case !change.Changed:
			sys(fmt.Sprintf("✓ %s persisted · %s", key, relPath(m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))))
		default:
			sys(fmt.Sprintf("✓ %s: %s → %s · %s", key, change.OldValue, change.NewValue, relPath(m.state.WorkingDir, projectConfigPath(m.state.WorkingDir))))
		}
		return
	}
}

func (m *Model) openSettingsBrowser(query string) {
	browser := settings.NewBrowser(
		m.state.Config,
		projectConfigPath(m.state.WorkingDir),
		query,
	)
	// m.state.Config may already hold an unsaved change left behind by a
	// previous failed save (browser or /set) — applyNewConfig keeps it
	// applied in-memory so the edit isn't lost, which means this fresh
	// panel's baseline is cloned from that already-advanced value and diffs
	// empty on construction. Seed savePending so a repeated commit still
	// retries persistence instead of silently no-op'ing (see
	// BrowserPanel.SetSavePending and flushChanges).
	browser.SetSavePending(m.configSavePending)
	m.dock.Open(browser)
}

// refreshOpenSettingsBrowser rebuilds an open settings browser from the
// current session config so that a preceding /set (or other external config
// change) isn't reverted by the next browser edit/save. The filter query is
// preserved; cursor and drill stack are reset because field closures are bound
// to the old registry's state.
func (m *Model) refreshOpenSettingsBrowser() {
	if browser, ok := m.dock.Panel().(*settings.BrowserPanel); ok {
		m.openSettingsBrowser(browser.FilterValue())
	}
}

func New(state *session.State, opts ...Option) Model {
	loadTheme(state.Config.TUI)

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

	textStyle := lipgloss.NewStyle().Foreground(activeTheme.FGDefault)
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
		Light: activeTheme.FGMuted,
		Dark:  activeTheme.FGDefault,
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
		discovered:     map[string][]string{},
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
	if m.jobBroker != nil && m.jobEvents == nil {
		m.jobEvents = m.jobBroker.Subscribe(m.ctx, pubsub.WithTerminal[native.JobEvent]())
	}
	if m.steeringBroker != nil && m.steeringEvents == nil {
		m.steeringEvents = m.steeringBroker.Subscribe(m.ctx)
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
	if m.jobEvents != nil {
		cmds = append(cmds, pumpJobEvents(m.jobEvents))
	}
	if m.steeringEvents != nil {
		cmds = append(cmds, pumpSteeringEvents(m.steeringEvents))
	}
	return tea.Batch(cmds...)
}

func (m *Model) resize(width, height int) {
	m.rawWidth = width
	m.rawHeight = height
	if width < minTerminalWidth {
		width = minTerminalWidth
	}
	if height < minTerminalHeight {
		height = minTerminalHeight
	}

	m.width = width
	m.height = height

	// Input interior: the ▍ bar (1 cell) + 1 right margin = 2 reserved
	// cells. The textarea's SetWidth sets the text wrap width and
	// internally subtracts promptWidth (2). Reserve 2 so rendered lines
	// stay inside the terminal width.
	m.input.SetWidth(max(width-2, 1))

	// Transcript viewport spans the full terminal width (borderless).
	m.viewport.SetWidth(max(width, 1))
	m.viewport.SetHeight(max(height-transcriptFrameRows-m.swarmPanelRows()-m.sddPanelRows()-m.browserBarRows()-m.dockRows()-m.inputAreaRows()-statusLineRows, 1))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Ctrl+C cancels the in-flight turn, resolves pending state, and quits.
	// Check it before any overlay routing so it can never be captured by a
	// form's keymap.
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+c" {
		return m, m.beginShutdown()
	}

	// WindowSizeMsg must always resize the underlying layout (and the
	// settings/memory overlays) regardless of which overlay is open.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.resize(ws.Width, ws.Height)
		if m.approvalModel != nil {
			m.approvalModel.SetSize(max(m.width-4, 30))
		}
		if m.questionModel != nil {
			m.questionModel.SetSize(max(m.width-4, 30))
		}
		m.refreshViewport()
		return m, nil
	}

	switch msg := msg.(type) {
	case settings.ChangedMsg:
		if msg.BlockedReason != "" {
			m.applyNewConfig(msg.Cfg)
			m.configSavePending = true
			for _, receipt := range msg.Receipts {
				m.state.AddMessage(session.RoleSystem,
					"✗ "+receipt+" · save blocked: "+msg.BlockedReason,
					session.ContentTypePlain)
			}
			m.refreshViewport()
			return m, nil
		}
		saveErr, reloadErr := m.persistAndReload(msg.Cfg)
		if saveErr != nil {
			m.state.AddMessage(session.RoleSystem,
				fmt.Sprintf("✗ save failed: %v", saveErr),
				session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		}
		if reloadErr != nil {
			m.state.AddMessage(session.RoleSystem,
				fmt.Sprintf("✗ settings saved, but live reload failed: %v", reloadErr),
				session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		}
		for _, receipt := range msg.Receipts {
			m.state.AddMessage(session.RoleSystem,
				"✓ "+receipt+" · "+relPath(m.state.WorkingDir, projectConfigPath(m.state.WorkingDir)),
				session.ContentTypePlain)
		}
		m.refreshViewport()
		return m, nil
	case settings.BrowserClosedMsg:
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	case memory.ShowMsg:
		m.dock.CloseNow()
		text := memory.RenderEntry(m.memoryDB, msg.ID)
		m.state.AddMessage(session.RoleSystem, text, session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	case memory.DeletedMsg:
		if msg.Err != nil {
			m.state.AddMessage(session.RoleSystem, "✗ delete failed: "+msg.Err.Error(), session.ContentTypePlain)
		} else {
			m.state.AddMessage(session.RoleSystem, "✓ deleted memory: "+msg.Title, session.ContentTypePlain)
		}
		m.refreshViewport()
		return m, nil
	case memory.ClosedMsg:
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	}

	// Runtime messages always stay with the parent model so background state
	// remains current while a dock panel is open.
	switch msg.(type) {
	case agentFinishedMsg, jobCountMsg, steeringMsg, agentTickMsg, spinnerTickMsg:
		return m.handleRuntimeMessage(msg)
	}

	// Browser-owned picker and probe/action messages must return to the
	// browser before the model's command-picker and connect handlers see
	// them. The browser itself is the dock panel, so it can safely handle
	// every remaining message here.
	if browser, ok := m.dock.Panel().(*settings.BrowserPanel); ok {
		blockedCmd := browser.SetSaveBlocked(m.settingsBlockReason())
		cmd := m.dock.Update(msg)
		if blockedCmd != nil {
			return m, tea.Batch(blockedCmd, cmd)
		}
		return m, cmd
	}

	// Picker messages: the picker itself lives in m.dock and is updated
	// through the dock. We only handle the terminal messages here.
	switch pm := msg.(type) {
	case connect.DoneMsg:
		m.applyConnectDone(pm)
		m.dock.CloseNow()
		m.connectModel = nil
		m.refreshViewport()
		return m, nil
	case connect.CancelledMsg:
		m.dock.CloseNow()
		m.connectModel = nil
		m.refreshViewport()
		return m, nil
	case connect.TickMsg:
		if _, ok := m.dock.Panel().(connect.Panel); ok {
			return m, m.dock.Update(pm)
		}
		return m, nil
	case probe.ResultMsg:
		if _, ok := m.dock.Panel().(connect.Panel); ok {
			cmd := m.dock.Update(pm)
			if pm.Err == nil && pm.Provider != "" {
				m.discovered[pm.Provider] = pm.Models
			}
			return m, cmd
		}
		return m, nil
	case picker.PickedMsg:
		cmdName := m.pickerCommand
		m.dock.CloseNow()
		m.pickerCommand = ""
		switch {
		case cmdName == "" || pm.Value == "":
			m.refreshViewport()
			return m, nil
		case cmdName == "model":
			// preset names may contain spaces; apply directly instead of
			// round-tripping through the arg splitter
			m.switchModelPreset(pm.Value)
			m.refreshViewport()
			return m, nil
		case cmdName == "mode" && pm.Value == "sdd":
			m.openSDDPlanPicker()
			m.refreshViewport()
			return m, nil
		case cmdName == "mode":
			return m.dispatchCommand("/" + pm.Value)
		case cmdName == "sdd-plan":
			// Dock already closed above; dispatch /sdd with the picked path.
			return m.dispatchCommand("/sdd " + pm.Value)
		default:
			return m.dispatchCommand("/" + cmdName + " " + pm.Value)
		}
	case picker.CancelledMsg:
		m.dock.CloseNow()
		m.pickerCommand = ""
		m.refreshViewport()
		return m, nil
	}
	if m.dock.IsOpen() {
		if _, ok := msg.(tea.KeyPressMsg); ok {
			return m, m.dock.Update(msg)
		}
		// non-key messages (ticks, agent events) keep flowing to the
		// normal handlers below so background work continues.
	}

	// F-BUG-147: Block overlay-opening hotkeys (Ctrl+O, Ctrl+K) while a
	// tool decision is pending. These must be intercepted before the
	// approval/question routing below, which would otherwise swallow them.
	if m.state.PendingApproval() != nil || m.state.PendingQuestion() != nil {
		if k, ok := msg.(tea.KeyPressMsg); ok {
			switch k.String() {
			case "ctrl+o":
				m.state.AddMessage(session.RoleSystem,
					"Resolve the pending tool decision before opening settings.",
					session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			case "ctrl+k":
				m.state.AddMessage(session.RoleSystem,
					"Resolve the pending tool decision before opening memory browser.",
					session.ContentTypePlain)
				m.refreshViewport()
				return m, nil
			}
		}
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

	// Bubble Tea v2 emits a KeyReleaseMsg alongside every KeyPressMsg.
	// Nothing in the model cares about release events, and letting them
	// fall through to the default path would re-run
	// updateCompletionPopups() and snap the popup selection back to
	// index 0 after every arrow-key press.
	if _, ok := msg.(tea.KeyReleaseMsg); ok {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if mm, cmd, handled := m.handleKeypress(msg); handled {
			return mm, cmd
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
					if m.state.PendingApproval() == tc {
						tc.Respond(session.UserApprovalDecision{Approved: true, Edited: value})
					}
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
		if m.state.PendingApproval() == tc {
			tc.Respond(session.UserApprovalDecision{Approved: true})
		}
		m.state.SetPendingApproval(nil)
		m.lastTranscriptHash = 0
		return m, nil
	case choiceDeny:
		if m.state.PendingApproval() == tc {
			tc.Respond(session.UserApprovalDecision{Approved: false})
		}
		m.state.SetPendingApproval(nil)
		m.lastTranscriptHash = 0
		return m, nil
	case choiceAlways:
		rule := permissions.Rule{
			Permission: permissions.PermissionForTool(tc.Name),
			Pattern:    permissions.PatternForApproval(tc),
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
			newCfg := m.state.Config
			newCfg.Permissions.Rules = append(newCfg.Permissions.Rules, config.PermissionRule{
				Permission: rule.Permission,
				Pattern:    rule.Pattern,
				Action:     string(rule.Action),
			})
			m.applyNewConfig(newCfg)
			if m.runner != nil {
				m.runner.SetPolicyRules(m.state.Config.Permissions.Rules)
			}
		}
		if m.state.PendingApproval() == tc {
			tc.Respond(session.UserApprovalDecision{Approved: true})
		}
		m.state.SetPendingApproval(nil)
		m.lastTranscriptHash = 0
		return m, nil
	case choiceSessionAllow:
		m.state.AddSessionRule(tc.Command)
		if m.state.PendingApproval() == tc {
			tc.Respond(session.UserApprovalDecision{Approved: true})
		}
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
		return m, m.questionModel.Init()
	}
	qm, cmd := m.questionModel.Update(msg)
	m.questionModel = qm
	if !m.questionModel.IsDone() {
		return m, cmd
	}

	if m.state.PendingQuestion() == q {
		q.Respond(m.questionModel.Answers())
	}
	m.state.SetPendingQuestion(nil)
	m.questionModel = nil
	m.input.Reset()
	m.input.Placeholder = "Ask Marshal..."
	m.updateViewportHeight()
	m.lastTranscriptHash = 0
	return m, nil
}

func (m Model) inputAreaRows() int {
	rows := 0
	if sd := m.state.SDDProgress(); sd.Active {
		rows++ // SDD hint row
	}
	if q := m.state.PendingQuestion(); q != nil {
		content := ""
		if m.questionModel != nil {
			content = m.questionModel.View()
		} else {
			content = renderQuestionPanel(q, max(m.width-4, 1))
		}
		rows += lipgloss.Height(content)
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
		rows += lipgloss.Height(content)
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

func (m Model) browserBarRows() int {
	if m.state.BrowserInfo().SessionOpen {
		return browserBarRows
	}
	return 0
}

// ShouldShowStatusURL returns false when the browser bar is visible, because
// the browser bar already shows the URL and tool name. The right-side status
// segment should omit the URL line to avoid duplication.
func (m Model) ShouldShowStatusURL() bool {
	return !m.state.BrowserInfo().SessionOpen
}

func (m Model) sddPanelRows() int {
	if !m.state.SDDProgress().Active {
		return 0
	}
	// Use the cached value from the last viewString() call. If the cache is
	// stale (e.g. called outside of View), fall back to computing it fresh.
	if m.sddPanelCachedRows > 0 || m.sddPanelBody != "" {
		return m.sddPanelCachedRows
	}
	spinner := m.activeSpinnerFrame(session.ActivityTool)
	_, rows := renderSDDPanel(m.state.SDDProgress(), spinner, m.width)
	return rows
}

// dockRows reports the rows the docked panel occupied at last render, so the
// transcript viewport shrinks while a panel is open.
func (m Model) dockRows() int { return m.dock.Rows() }

func (m *Model) updateViewportHeight() bool {
	newViewportHeight := max(m.height-transcriptFrameRows-m.swarmPanelRows()-m.sddPanelRows()-m.browserBarRows()-m.dockRows()-m.inputAreaRows()-statusLineRows, 1)
	if newViewportHeight == m.viewport.Height() {
		return false
	}
	m.viewport.SetHeight(newViewportHeight)
	return true
}

// updateCompletionPopups inspects the current input value and updates the
// command, file, and /set completion popups. Called from every keystroke.
//
// Triggers (F18 R1, R4):
//   - `/` at position 0 with no space in the typed value → cmdPopup filters
//     against the commands registry.
//   - `@` at a word start (preceded by start-of-input or whitespace) with
//     no whitespace after the `@` → filePopup filters against the repo
//     file index.
//   - `/set ` → setPopup filters setting keys, then selectable enum values.
//
// The popups are mutually exclusive. When the input doesn't match a trigger,
// every popup is dismissed.
func (m *Model) updateCompletionPopups() {
	if m.cmdPopup == nil || m.filePopup == nil {
		return
	}
	value := m.input.Value()
	// Idempotency guard: the default Update path calls this on every
	// non-handled message (mouse, spinner tick, KeyReleaseMsg, paste
	// echo, etc.) and the popup's update() always resets index to 0.
	// Without this, holding the down arrow would visibly snap the
	// selector back to the top between every key repeat. Skip when the
	// input hasn't actually changed.
	if value == m.lastInputForPopups {
		return
	}
	m.lastInputForPopups = value

	if rest, ok := strings.CutPrefix(value, "/set "); ok {
		m.updateSetCompletionPopup(rest)
		m.cmdPopup.dismiss()
		m.filePopup.dismiss()
		return
	}

	cmdTrigger, cmdQuery := m.commandTrigger(value)
	if cmdTrigger {
		if m.cmdPopup.items == nil && m.cmdRegistry != nil {
			// Lazily build the command items from the registry the first
			// time the user starts typing "/". Registry contents can
			// change at runtime; we re-build on every trigger so /help
			// always reflects the current registry. ListAll includes
			// Hidden commands — they are runnable and belong in the popup.
			items := make([]completionItem, 0, len(m.cmdRegistry.ListAll()))
			for _, c := range m.cmdRegistry.ListAll() {
				items = append(items, completionItem{
					Text:        c.Name,
					Description: c.Description,
					Kind:        completionCommand,
				})
			}
			m.cmdPopup.items = items
		}
		if cmdQuery == "" {
			// Bare "/" → show every command, unfiltered, so the user can
			// see what's available before typing.
			m.cmdPopup.showAll()
		} else {
			m.cmdPopup.update(cmdQuery)
		}
		m.filePopup.dismiss()
		if m.setPopup != nil {
			m.setPopup.dismiss()
		}
		return
	}

	fileTrigger, fileQuery := m.fileTrigger(value)
	if fileTrigger {
		m.populateFileIndexIfNeeded()
		if len(m.fileIndex) == 0 {
			m.fileIndex = []completionItem{{
				Text:     "(no indexed files — run /index)",
				Kind:     completionFile,
				Disabled: true,
			}}
		}
		if m.filePopup.items == nil || len(m.filePopup.items) == 0 {
			m.filePopup.items = m.fileIndex
		}
		if fileQuery == "" {
			// Bare "@" → show every file item, unfiltered.
			m.filePopup.showAll()
		} else {
			m.filePopup.update(fileQuery)
		}
		m.cmdPopup.dismiss()
		if m.setPopup != nil {
			m.setPopup.dismiss()
		}
		return
	}

	m.cmdPopup.dismiss()
	m.filePopup.dismiss()
	if m.setPopup != nil {
		m.setPopup.dismiss()
	}
}

func (m *Model) updateSetCompletionPopup(rest string) {
	reg := m.settingsRegistry()
	items := []completionItem{}
	query := rest

	if keyEnd := strings.IndexAny(rest, " \t\n"); keyEnd >= 0 {
		key := rest[:keyEnd]
		query = strings.TrimSpace(rest[keyEnd:])
		if _, _, options, err := reg.Describe(key); err == nil {
			for _, option := range options {
				items = append(items, completionItem{Text: option, Kind: completionSetting})
			}
		}
	} else {
		for _, key := range reg.Keys() {
			_, current, _, _ := reg.Describe(key)
			items = append(items, completionItem{
				Text:        key,
				Description: current,
				Kind:        completionSetting,
			})
		}
	}

	m.setPopup = newCompletionPopup(items)
	if query == "" {
		m.setPopup.showAll()
		return
	}
	m.setPopup.update(query)
}

// commandTrigger returns (true, query) when value is a slash command in
// progress: starts with "/", has no whitespace (so we're still typing
// the command name, not its arguments), and is non-empty after the "/".
func (m *Model) commandTrigger(value string) (bool, string) {
	if !strings.HasPrefix(value, "/") {
		// After a command has been accepted (e.g. "plan "), re-trigger
		// the popup when the user types a space after the command name
		// so argument completions can be shown.
		//
		// NOTE: The re-trigger returns (true, "") which shows ALL items
		// unfiltered. The brief originally specified filtering to only
		// sub-arguments for the accepted command, but the codebase has
		// no concept of per-command sub-arguments — there is no Help
		// field on Command and no metadata for filtering. Implementing
		// sub-argument filtering would require adding a new metadata
		// field to the Command type and populating it for each command.
		// This is a documented plan deviation: the current behaviour
		// (re-show full command list on space) is the correct user-facing
		// behaviour given the data model.
		if m.cmdRegistry != nil {
			for _, c := range m.cmdRegistry.ListAll() {
				prefix := c.Name + " "
				if strings.HasPrefix(value, prefix) {
					return true, ""
				}
			}
		}
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
	index, err := m.memoryDB.GetFileIndex(m.memoryProject, 0)
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
// (/set takes precedence when both somehow show), or nil when none is up.
// Used by the keypress switch to route Up/Down/Tab/Esc.
func (m *Model) activeCompletionPopup() *completionPopup {
	if m.setPopup != nil && m.setPopup.isVisible() {
		return m.setPopup
	}
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
	if m.setPopup != nil {
		m.setPopup.dismiss()
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
	if accepted == "" {
		// Disabled / placeholder items produce no accepted text — just
		// dismiss the popup without mutating the input.
		m.dismissCompletionPopups()
		m.updateViewportHeight()
		return true
	}
	value := m.input.Value()
	newValue := replaceTriggerToken(value, accepted)
	if p == m.setPopup {
		newValue = replaceSetCompletionToken(value, accepted)
	}
	m.input.SetValue(newValue)
	// Move the cursor to the end of the inserted text so the user can
	// keep typing args / a trailing space directly.
	m.input.MoveToEnd()
	m.dismissCompletionPopups()
	m.updateViewportHeight()
	return true
}

func replaceSetCompletionToken(value, replacement string) string {
	if !strings.HasPrefix(value, "/set ") {
		return value
	}
	index := strings.LastIndexAny(value, " \t\n")
	if index < 0 {
		return value
	}
	return value[:index+1] + replacement
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
	// File trigger: find the last "@<...>" at a word start, and consume
	// any immediately-preceding "@"s as part of the trigger. F-SEC-28.
	idx := strings.LastIndex(value, "@")
	if idx < 0 {
		return value
	}
	// Walk back over any preceding "@"s in the same run.
	start := idx
	for start > 0 && value[start-1] == '@' {
		start--
	}
	if start > 0 {
		prev := value[start-1]
		if !isAtFileBoundary(prev) {
			return value
		}
	}
	return value[:start] + replacement
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
		b.WriteString(renderThinkingBox(inProgress.Reasoning, m.activeSpinnerFrame(session.ActivityThinking), m.viewport.Width()))
	}
	if atc, ok := m.state.ActiveToolCall(); ok {
		b.WriteString(renderActiveToolCall(atc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, m.activeSpinnerFrame(session.ActivityTool), m.now(), m.viewport.Width()))
	}
	if err := m.state.ProviderError(); err != nil {
		b.WriteString(renderProviderError(err, m.viewport.Width()))
		b.WriteString("\n")
		b.WriteString(mutedStyle().Render("Run /connect to add a provider, or /models to pick a model."))
		b.WriteString("\n")
	}
	if len(queued) > 0 {
		b.WriteString(renderQueuedMessages(queued, m.viewport.Width()))
	}

	m.viewport.SetContent(b.String())
	if m.viewportFollow {
		m.viewport.GotoBottom()
	}
}

// startAgentRun begins a turn on runner with goal, wiring cancellation and
// the busy/spinner commands. On BeginWork failure it reports and stays idle.
func (m *Model) startAgentRun(runner AgentRunner, goal string) (tea.Model, tea.Cmd) {
	if err := m.state.BeginWork(); err != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Cannot start work: %v", err), session.ContentTypePlain)
		m.busy = false
		m.refreshViewport()
		return *m, nil
	}
	m.busy = true
	agentCtx, cancel := context.WithCancel(m.ctx)
	m.agentCancel = cancel
	return *m, tea.Batch(runAgentCmd(agentCtx, m.state, runner, goal), tickCmd(), spinnerTickCmd())
}

type agentFinishedMsg struct{ err error }
type agentTickMsg struct{}
type spinnerTickMsg struct{}

// runAgentCmd wraps an agent turn into a Bubble Tea command that
// registers session work via BeginWork on construction and releases it
// via EndWork when the command executes. Callers must handle
// ErrSessionQuiescing from BeginWork before creating the command.
func runAgentCmd(ctx context.Context, state *session.State, runner AgentRunner, goal string) tea.Cmd {
	return func() tea.Msg {
		defer state.EndWork()
		err := runner.Run(ctx, goal)
		return agentFinishedMsg{err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
		return agentTickMsg{}
	})
}

func spinnerTickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// spinnerLabel returns the formatted label with a leading spinner glyph, or
// just the label when the spinner frame is empty. This avoids leading-space
// jitter during the 200ms gate window when activeSpinnerFrame returns "".
func spinnerLabel(spinner, label string) string {
	if spinner == "" {
		return label
	}
	return spinner + " " + label
}

// activeSpinnerFrame returns the current spinner frame glyph if the activity
// has been running for at least 200ms, or "" when the activity just started.
// This avoids a flash of the spinner glyph before the user can perceive the
// activity. For ActivityIdle it always returns "".
func (m *Model) activeSpinnerFrame(kind session.ActivityKind) string {
	if kind == session.ActivityIdle {
		return ""
	}
	act := m.state.Activity()
	if m.now().Sub(act.StartedAt) < 200*time.Millisecond {
		return ""
	}
	return m.spinnerFrame
}

// cancelTurn cancels the in-flight agent turn, if any. Shared by Esc and
// the /stop command. The steering queue is NOT cleared here — that happens
// in handleAgentFinished so the finishing goroutine can observe the
// cancellation state and avoid double-processing.
func (m *Model) cancelTurn() bool {
	if m.agentCancel == nil && !m.busy {
		return false
	}
	m.cancelling = true
	if m.agentCancel != nil {
		m.agentCancel()
		m.agentCancel = nil
	}
	m.refreshViewport()
	return true
}

// beginShutdown cancels the in-flight turn, clears pending state, and
// returns tea.Quit. Used by Ctrl+C, /quit, and /exit.
//
// m.busy is intentionally not reset here — tea.Quit is returned immediately
// and the program is exiting, so the agentFinishedMsg path that normally
// clears busy via state.EndWork() will not run.
func (m *Model) beginShutdown() tea.Cmd {
	if m.agentCancel != nil {
		m.agentCancel()
		m.agentCancel = nil
	}
	m.queuedCount = 0
	m.state.ResolvePendingForShutdown()
	m.state.Shutdown()
	return tea.Quit
}

// settingsBlockReason returns a message when runtime work, a pending
// decision, or another dock surface makes a settings write unsafe.
func (m Model) settingsBlockReason() string {
	if m.busy || m.state.RunningJobsCount() > 0 {
		return settingsBusyMessage
	}
	if m.state.PendingApproval() != nil {
		return "Resolve the pending tool approval to save."
	}
	if m.state.PendingQuestion() != nil {
		return "Answer the pending question to save."
	}
	if m.dock.IsOpen() {
		if _, browser := m.dock.Panel().(*settings.BrowserPanel); browser {
			return ""
		}
		return "Close the picker to save."
	}
	return ""
}

// handleAgentFinished handles an agentFinishedMsg, shared by Update and
// handleRuntimeMessage.
func (m Model) handleAgentFinished(msg agentFinishedMsg) (Model, tea.Cmd) {
	if m.cancelling {
		m.state.ClearSteering()
		m.queuedCount = 0
		m.state.AddMessage(session.RoleSystem, "Agent turn cancelled.", session.ContentTypePlain)
		m.cancelling = false
	}
	m.busy = false
	m.agentCancel = nil
	if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
		m.state.SetProviderError(msg.err)
		m.successPulse = false
	} else if msg.err == nil {
		m.successPulse = true
	}
	m.state.SetActivity(session.Activity{Kind: session.ActivityIdle})
	if m.lastActivityKind != session.ActivityIdle && m.lastActivityKind != "" {
		m.lastActivityDone = m.now()
		m.lastActivityKind = session.ActivityIdle
	}
	m.updateViewportHeight()
	m.refreshViewport()
	return m, tickCmd()
}

// handleJobCount handles a jobCountMsg, shared by Update and
// handleRuntimeMessage.
func (m Model) handleJobCount(msg jobCountMsg) (Model, tea.Cmd) {
	m.jobCount = msg.count
	// Re-arm the pump: exactly one in-flight subscription at a time
	// (F19 R2). Return nil if no broker is wired so the cmd chain
	// terminates (this should not happen when the pump is sourced
	// from Init, but keeps Update safe under tests that wire msgs
	// directly).
	if m.jobEvents == nil {
		return m, nil
	}
	return m, pumpJobEvents(m.jobEvents)
}

// handleSteering handles a steeringMsg, shared by Update and
// handleRuntimeMessage.
func (m Model) handleSteering(msg steeringMsg) (Model, tea.Cmd) {
	// F16: cache the queued count so the status line and transcript
	// render without polling, then re-arm the pump. The transcript
	// re-renders via the viewport dirty hash on the next refresh.
	m.queuedCount = msg.queueLen
	if m.steeringEvents == nil {
		m.refreshViewport()
		return m, nil
	}
	m.refreshViewport()
	return m, pumpSteeringEvents(m.steeringEvents)
}

// handleAgentTick handles an agentTickMsg, shared by Update and
// handleRuntimeMessage.
func (m Model) handleAgentTick(msg agentTickMsg) (Model, tea.Cmd) {
	if !m.busy && m.successPulse {
		if m.lastActivityKind == session.ActivityIdle && !m.lastActivityDone.IsZero() &&
			m.now().Sub(m.lastActivityDone) >= doneDisplayDuration {
			m.successPulse = false
		}
	}
	if !m.busy && !m.successPulse {
		return m, nil
	}
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
}

// handleSpinnerTick handles a spinnerTickMsg, shared by Update and
// handleRuntimeMessage.
func (m Model) handleSpinnerTick(msg spinnerTickMsg) (Model, tea.Cmd) {
	if !m.busy {
		return m, nil
	}
	m.spinnerFrame = m.spinner.Next()
	// The spinner tick is at 80ms (smoother than the 150ms layout tick);
	// the activity strip and the in-progress thinking/tool rows read
	// m.spinnerFrame via activeSpinnerFrame, so the viewport must
	// re-render here or the animation stays at the 150ms cadence.
	m.refreshViewport()
	return m, spinnerTickCmd()
}

// handleRuntimeMessage processes agent/steering/tick messages so they
// reach the parent model even when an overlay is open. This keeps
// parent state (busy, job count, steering, activity) current while the
// overlay remains visible.
func (m Model) handleRuntimeMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case agentFinishedMsg:
		return m.handleAgentFinished(msg)
	case jobCountMsg:
		return m.handleJobCount(msg)
	case steeringMsg:
		return m.handleSteering(msg)
	case agentTickMsg:
		return m.handleAgentTick(msg)
	case spinnerTickMsg:
		return m.handleSpinnerTick(msg)
	}
	return m, nil
}

func (m *Model) dispatchCommand(raw string) (tea.Model, tea.Cmd) {
	parts, err := shlex.Split(raw)
	if err != nil {
		m.state.AddMessage(session.RoleSystem, "Invalid command syntax.", session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	}
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

	// Bare picker-backed commands open a modal instead of running the
	// handler; with arguments (or when there is nothing to pick) they fall
	// through to the handler unchanged.
	if len(args) == 0 {
		switch cmd.Name {
		case "rewind":
			if items := m.rewindPickerItems(); len(items) > 0 {
				m.openPicker("rewind", "Rewind to turn", "starts a new branch", items, "")
				m.refreshViewport()
				return m, nil
			}
		case "branches":
			if items := m.branchesPickerItems(); len(items) > 1 {
				m.openPicker("branches", "Switch branch", "", items, "")
				m.refreshViewport()
				return m, nil
			}
		}
	}

	if cmd.Handler != nil {
		if msg := cmd.Handler(m.state, args); msg != "" {
			m.state.AddMessage(session.RoleSystem, msg, session.ContentTypePlain)
		}
	}

	if cmd.TUIOnly {
		effect, ok := tuiCommandEffects[cmd.Name]
		if !ok {
			m.state.AddMessage(session.RoleSystem, "Command /"+cmd.Name+" is not available in this build.", session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		}
		return effect(m, args)
	}

	m.refreshViewport()
	return m, nil
}

// openConnect opens the connect overlay for adding/reconnecting a provider.
func (m *Model) openConnect(_ string) {
	m.connectModel = connect.New(connect.Opts{
		Cfg:        m.state.Config,
		Discovered: m.discovered,
	})
	m.connectModel.SetSize(m.width, m.height)
	m.dock.Open(connect.Panel{Model: m.connectModel})
}

// openModels opens the connect overlay scoped to the first provider for model
// selection. When no providers are configured it falls through to openConnect.
// Returns a tea.Cmd batch that probes all connected (and uncached) providers.
func (m *Model) openModels() tea.Cmd {
	names := m.sortedProviderNames()
	if len(names) == 0 {
		m.openConnect("/")
		return nil
	}
	m.connectModel = connect.New(connect.Opts{
		Cfg:              m.state.Config,
		Discovered:       m.discovered,
		SkipToIntroModel: true,
		ScopedProvider:   names[0],
	})
	m.connectModel.SetSize(m.width, m.height)
	m.dock.Open(connect.Panel{Model: m.connectModel})
	var cmds []tea.Cmd
	for _, n := range names {
		if cached, ok := m.discovered[n]; ok && len(cached) > 0 {
			continue
		}
		pc := m.state.Config.Providers[n]
		if !probe.IsLocalhost(pc.BaseURL) && !m.state.Config.Privacy.RemoteProvidersAllowed {
			continue
		}
		cmds = append(cmds, probe.Provider("models", n, pc))
	}
	return tea.Batch(cmds...)
}

// sortedProviderNames returns provider names sorted alphabetically.
func (m *Model) sortedProviderNames() []string {
	names := make([]string, 0, len(m.state.Config.Providers))
	for k := range m.state.Config.Providers {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// openPicker opens a command modal. The picked value is delivered as
// picker.PickedMsg and re-enters dispatchCommand for pickerCommand, so
// command semantics stay in one place.
func (m *Model) openPicker(cmdName, title, footer string, items []picker.Item, prefilter string) {
	p := picker.New(title, footer, items)
	if prefilter != "" {
		p.SetFilter(prefilter)
	}
	m.dock.Open(p)
	m.pickerCommand = cmdName
}

// setMode applies an interaction mode ("ask", "edit", or "" for auto) for
// the next turn. Shared by the /ask, /edit, /auto, /mode commands and the
// Tab/Shift+Tab mode-cycling hotkeys.
func (m *Model) setMode(mode string) {
	class := mode
	if mode == "ask" {
		class = "question"
	}
	if m.runner != nil {
		m.runner.SetForceClass(class) // "" => auto (classifier runs)
	}
	m.forceMode = mode
}

// modeOrder is the canonical cycle order used by Tab/Shift+Tab.
// "" represents auto (the classifier-driven default).
var modeOrder = []string{"", "ask", "edit"}

// modeSwitchMessage maps each mode value to the exact confirmation
// message used by the /ask, /edit, /auto command handlers, so the
// transcript looks identical whether the user pressed Tab or typed /ask.
var modeSwitchMessage = map[string]string{
	"":     "Switched to Auto mode. Agent will classify each turn automatically.",
	"ask":  "Switched to Ask mode. Agent will answer questions without planning or editing.",
	"edit": "Switched to Edit mode. Agent will plan and execute changes.",
}

// cycleMode advances (forward=true) or reverses the interaction mode,
// wrapping around. It applies the result via setMode and emits the same
// confirmation message the /<mode> commands use.
func (m *Model) cycleMode(forward bool) {
	cur := m.forceMode
	idx := slices.Index(modeOrder, cur)
	if idx < 0 {
		idx = 0
	}
	step := 1
	if !forward {
		step = -1
	}
	next := modeOrder[(idx+step+len(modeOrder))%len(modeOrder)]
	m.setMode(next)
	msg, ok := modeSwitchMessage[next]
	if !ok {
		msg = fmt.Sprintf("Switched to %s mode.", next)
	}
	m.state.AddMessage(session.RoleSystem, msg, session.ContentTypePlain)
	m.refreshViewport()
}

// cycleModel advances (forward=true) or reverses the active model preset,
// wrapping around. Order matches modelPickerItems() (provider then name).
// Session-only: delegates to switchModelPreset.
func (m *Model) cycleModel(forward bool) {
	if m.busy {
		m.state.AddMessage(session.RoleSystem,
			"Busy — switch the model after this turn completes.",
			session.ContentTypePlain)
		m.refreshViewport()
		return
	}
	names := m.sortedPresetNames()
	if len(names) == 0 {
		m.state.AddMessage(session.RoleSystem,
			"No model presets configured. Add one in /settings → Model Presets.",
			session.ContentTypePlain)
		m.refreshViewport()
		return
	}
	cur := m.state.ActiveRoute().Preset
	idx := slices.Index(names, cur)
	if idx < 0 {
		idx = 0 // legacy/unknown route → start at the first preset
	}
	step := 1
	if !forward {
		step = -1
	}
	target := names[(idx+step+len(names))%len(names)]
	m.switchModelPreset(target)
	m.refreshViewport()
}

// openSDDPlanPicker reads the SDD plans directory, globs for *.md files,
// and opens a picker for the user to choose a plan to run.
func (m *Model) openSDDPlanPicker() {
	plansDir := m.state.Config.SDD.PlansDir
	var items []picker.Item
	matches, _ := filepath.Glob(filepath.Join(m.state.WorkingDir, plansDir, "*.md"))
	for _, path := range matches {
		name := filepath.Base(path)
		items = append(items, picker.Item{Label: name, Detail: path, Value: path})
	}
	if len(items) == 0 {
		items = append(items, picker.Item{Label: "No plans found — generate one", Detail: "run the planner first", Value: "generate"})
	}
	p := picker.New("Pick a plan", "SDD workflow", items)
	p.SetAllowCustom(true)
	m.dock.Open(p)
	m.pickerCommand = "sdd-plan"
}

// applyConnectDone persists the provider and model chosen through the
// connect overlay. It writes the provider entry to cfg.Providers, sets
// Agent.Provider/Model, clears the profile default, and saves.
func (m *Model) applyConnectDone(msg connect.DoneMsg) {
	if msg.Provider == "" || msg.Model == "" {
		return
	}
	newCfg := m.state.Config
	if newCfg.Providers != nil {
		copied := make(map[string]config.ProviderConfig, len(newCfg.Providers)+1)
		for k, v := range newCfg.Providers {
			copied[k] = v
		}
		newCfg.Providers = copied
	} else {
		newCfg.Providers = map[string]config.ProviderConfig{}
	}
	if msg.ProviderCfg.Type != "" {
		newCfg.Providers[msg.Provider] = msg.ProviderCfg
	}
	newCfg.Agent.Provider = msg.Provider
	newCfg.Agent.Model = msg.Model
	newCfg.Profile.Default = ""

	saveErr, reloadErr := m.persistAndReload(newCfg)
	switch {
	case saveErr != nil:
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✗ Failed to save model: %v", saveErr), session.ContentTypePlain)
	case reloadErr != nil:
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✗ Failed to switch model: %v", reloadErr), session.ContentTypePlain)
	default:
		m.state.AddMessage(session.RoleSystem,
			fmt.Sprintf("✓ Switched to model: %s (%s)", msg.Model, msg.Provider), session.ContentTypePlain)
	}
}

// switchModelPreset applies a model switch by routing every role of a
// synthetic "switched" profile at the preset. The change is persisted before
// the runtime is asked to reload, matching the /set and settings.ChangedMsg
// contracts.
func (m *Model) switchModelPreset(presetName string) {
	newCfg := m.state.Config
	preset, ok := newCfg.Models.Presets[presetName]
	if !ok {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✗ Unknown preset: %s", presetName), session.ContentTypePlain)
		return
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

	saveErr, reloadErr := m.persistAndReload(newCfg)
	switch {
	case saveErr != nil:
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✗ Failed to save model preset: %v", saveErr), session.ContentTypePlain)
	case reloadErr != nil:
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✗ Failed to switch model: %v", reloadErr), session.ContentTypePlain)
	default:
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✓ Switched to model: %s (%s)", presetName, preset.Model), session.ContentTypePlain)
	}
}

// sortedPresetNames returns model preset names sorted by provider then name,
// matching the order used by modelPickerItems. Shared by the model picker and
// the Alt+M hotkey so they stay in lock-step.
func (m *Model) sortedPresetNames() []string {
	presets := m.state.Config.Models.Presets
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		pi, pj := presets[names[i]], presets[names[j]]
		if pi.Provider != pj.Provider {
			return pi.Provider < pj.Provider
		}
		return names[i] < names[j]
	})
	return names
}

// modelPickerItems builds sorted picker items from configured model presets.
func (m *Model) modelPickerItems() []picker.Item {
	presets := m.state.Config.Models.Presets
	names := m.sortedPresetNames()
	current := m.state.ActiveRoute().Preset
	items := make([]picker.Item, 0, len(names))
	for _, n := range names {
		p := presets[n]
		var badges []string
		if n == current {
			badges = append(badges, "● now")
		}
		if p.LocalOnly {
			badges = append(badges, "local")
		}
		items = append(items, picker.Item{
			Group:  p.Provider,
			Label:  n,
			Detail: p.Provider + "/" + p.Model,
			Badge:  strings.Join(badges, " "),
			Value:  n,
		})
	}
	return items
}

// rewindPickerItems builds picker items from user turns, newest first.
// The most recent turn carries a "● last" badge and is the default cursor target.
func (m *Model) rewindPickerItems() []picker.Item {
	var turns []session.Message
	for _, msg := range m.state.Messages() {
		if msg.Role == session.RoleUser {
			turns = append(turns, msg)
		}
	}
	items := make([]picker.Item, 0, len(turns))
	for i := len(turns) - 1; i >= 0; i-- {
		badge := ""
		if i == len(turns)-1 {
			badge = "● last"
		}
		items = append(items, picker.Item{
			Label:  fmt.Sprintf("turn %d", i+1),
			Detail: strutil.Truncate(strings.ReplaceAll(turns[i].Content, "\n", " "), 50, false),
			Badge:  badge,
			Value:  strconv.Itoa(i + 1),
		})
	}
	return items
}

// branchesPickerItems builds picker items from session branches.
// The current branch carries a "● now" badge; the picker only opens when
// there are at least two branches (a meaningful switching target).
func (m *Model) branchesPickerItems() []picker.Item {
	leaves := m.state.Branches()
	cur := m.state.LeafID()
	items := make([]picker.Item, 0, len(leaves))
	for i, id := range leaves {
		badge := ""
		if id == cur {
			badge = "● now"
		}
		items = append(items, picker.Item{
			Label:  fmt.Sprintf("branch %d", i+1),
			Detail: fmt.Sprintf("leaf %d", id),
			Badge:  badge,
			Value:  strconv.Itoa(i + 1),
		})
	}
	return items
}

// modePickerItems builds picker items for the interaction modes.
// The current mode (or "auto" when forceMode is empty) carries a "● now" badge.
func (m *Model) modePickerItems() []picker.Item {
	current := m.forceMode // "ask", "edit", or "" (auto)
	badge := func(v string) string {
		if v == current || (v == "auto" && current == "") {
			return "● now"
		}
		return ""
	}
	return []picker.Item{
		{Label: "Ask", Detail: "read-only, no planning", Badge: badge("ask"), Value: "ask"},
		{Label: "Edit", Detail: "planning + full tools", Badge: badge("edit"), Value: "edit"},
		{Label: "Auto", Detail: "classify each turn", Badge: badge("auto"), Value: "auto"},
		{Label: "SDD", Detail: "plan-driven multi-task", Badge: badge("sdd"), Value: "sdd"},
	}
}

func visibleRunes(s string) int {
	return ansi.StringWidth(s)
}

var activeTheme theme.Theme

var (
	coralColor  color.Color
	goldColor   color.Color
	tealColor   color.Color
	orangeColor color.Color
	mauveColor  color.Color
	userColor   color.Color

	accentColor  color.Color
	violetColor  color.Color
	dimColor     color.Color
	successColor color.Color
	warningColor color.Color
	errorColor   color.Color

	dimSeparator = " · "
)

func loadTheme(tui config.TUIConfig) {
	activeTheme = theme.LoadWithConfig(tui.Theme, tui.Mode, theme.PaletteOverrides(tui.Palette))

	coralColor = activeTheme.AccentPrimary
	goldColor = activeTheme.AccentTertiary
	tealColor = activeTheme.StatusSuccess
	orangeColor = activeTheme.StatusWarning
	mauveColor = activeTheme.BorderMuted
	userColor = activeTheme.UserPrompt

	accentColor = activeTheme.AccentPrimary
	violetColor = activeTheme.AccentSecondary
	dimColor = activeTheme.FGMuted
	successColor = activeTheme.StatusSuccess
	warningColor = activeTheme.StatusWarning
	errorColor = activeTheme.StatusError

	theme.Reload(activeTheme)
}

// Style helpers — lazy reads from theme.Current() so theme reloads propagate.
func mutedStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(theme.Current().FGMuted) }
func panelTitleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().FGEmphasis).Bold(true)
}
func thinkingLineStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().FGMuted).Italic(true)
}
func codeSurfaceStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(theme.Current().BGSurface).
		Padding(0, 1)
}
func toolNameStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentTertiary)
}
func keyHintStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary).Bold(true)
}
func riskLabelStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusWarning).Bold(true)
}
func warningStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusWarning).Bold(true)
}
func errorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusError).Bold(true)
}
func statusOkStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusSuccess)
}
func statusBusyStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary)
}
func promptPrefixStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary).Bold(true)
}
func toolBulletStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentTertiary)
}
func statusBarStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().FGDefault)
}
func browserGlyphStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentTertiary)
}
func browserPrefixStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentSecondary)
}
func browserBarStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(theme.Current().BGSurface).
		BorderTop(true).
		BorderForeground(theme.Current().BorderMuted)
}
func urlStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(theme.Current().FGDefault) }

func transcriptHash(items []session.TranscriptItem, streamLen int, busy bool, width int, todos []native.TodoItem, queued []string) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "c=%d|w=%d|f=%d|", len(items), width, flags(streamLen, busy, len(todos), len(queued)))
	for _, item := range items {
		fmt.Fprintf(h, "%d|%d|", item.Kind, item.Timestamp.UnixNano())
		if item.Message != nil {
			fmt.Fprintf(h, "%s|%s|%s\x00", item.Message.Role, item.Message.ContentType, item.Message.Content)
		}
	}
	for _, todo := range todos {
		fmt.Fprintf(h, "todo=%s|%s\x00", todo.Content, todo.Status)
	}
	for _, q := range queued {
		fmt.Fprintf(h, "q=%s\x00", q)
	}
	return h.Sum64()
}

// flags packs boolean/len state into a single uint64 for the hash.
func flags(streamLen int, busy bool, nTodos, nQueued int) uint64 {
	var f uint64
	if busy {
		f |= 1
	}
	f |= uint64(streamLen) << 1
	f |= uint64(nTodos) << 21
	f |= uint64(nQueued) << 41
	return f
}

// userConfigDir returns the absolute path to the user-level Marshal
// config directory (typically ~/.config/marshal). The path is
// symlink-resolved and verified to be under the user's home directory
// to defend against an attacker-controlled $HOME pointing at a
// sensitive location (e.g. /root, /etc). See F-SEC-27.
func userConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return ""
	}
	resHome, err := filepath.EvalSymlinks(absHome)
	if err != nil {
		resHome = absHome
	}
	cfgDir := filepath.Join(resHome, ".config", "marshal")
	// If the config dir doesn't exist yet, the parent path is what
	// matters for the trust check.
	resCfg, err := filepath.EvalSymlinks(cfgDir)
	if err != nil {
		resCfg = filepath.Dir(cfgDir)
	}
	rel, err := filepath.Rel(resHome, resCfg)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		slog.Default().Warn("userConfigDir outside home; refusing",
			"home", resHome, "configDir", resCfg)
		return ""
	}
	return cfgDir
}
