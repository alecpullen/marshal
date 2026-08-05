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
	"marshal/internal/app/tui/agents"
	"marshal/internal/app/tui/castlist"
	"marshal/internal/app/tui/changedfiles"
	"marshal/internal/app/tui/connect"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/app/tui/gitinfo"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/sidepanel"
	"marshal/internal/app/tui/theme"
	"marshal/internal/app/tui/trustpanel"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/permissions"
	"marshal/internal/pipeline"
	"marshal/internal/pubsub"
	"marshal/internal/strutil"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/trust"
)

// AgentRunner is the one thing the TUI knows about the agent loop: how to
// kick off a turn and get back a terminal error (or nil). It is satisfied
// structurally by *agent.Runner without this package importing
// internal/agent — the TUI stays a rendering layer with no policy/prompt
// logic, per AGENTS.md's design constraints.
type AgentRunner interface {
	Run(ctx context.Context, goal string) error
	SetForceClass(class string)
	SetPolicyRules(rules []config.PermissionRule)
	SetApprovalMode(mode policy.ApprovalMode)
	// AnswerGate delivers the human's typed answer to a pipeline
	// subagent's question. Only the pipeline runner (ControllerAdapter)
	// acts on it; the regular and swarm runners are no-ops.
	AnswerGate(answer string)
}

// CustomAgentRunnerFactory builds a one-shot AgentRunner for a named custom
// agent. It is wired from app.go via WithCustomAgentRunnerFactory and used
// by buildCustomAgentRunner to dispatch Run-now. Returns nil when the agent
// name is unknown or the factory is not available.
type CustomAgentRunnerFactory func(agentName string) (AgentRunner, error)

const (
	minTerminalWidth  = 80
	minTerminalHeight = 24

	successPulseDuration = 2 * time.Second

	// settingsBusyMessage is shown when runtime work makes a settings change
	// unsafe to persist.
	settingsBusyMessage = "Stop the active turn and background jobs before applying settings."

	// singleModelProfileName is the profile /connect and /models write into when
	// the user picks one model for everything.
	singleModelProfileName = "single"

	// sddCustomPlanPathValue is the picker value emitted by the "Custom plan
	// path..." item in the SDD plan picker. It is not a valid filesystem path,
	// so it cannot collide with an auto-detected plan.
	sddCustomPlanPathValue = "__sdd_custom_path__"
)

type Model struct {
	state           *session.State
	input           textarea.Model
	editingCommand  bool
	runner          AgentRunner
	swarmRunner     AgentRunner
	pipelineFactory func(planPath string) AgentRunner
	ctx             context.Context
	busy            bool
	configReloader  ConfigReloader
	// runnerSource exposes the runtime's current runner after a config
	// reload. Used to recover from a startup provider-build failure, where
	// the TUI was constructed without a runner: the first successful reload
	// rebuilds one, and adoptRunner installs it here.
	runnerSource       func() (context.Context, AgentRunner)
	openConnectOnStart bool
	trustPromptDir     string
	trustDecide        func(trust.Decision)
	// trustRefresh, when set, advances the permanent-trust config hash after
	// an interactive project-config save succeeds: the user approved the new
	// config from a trusted session, so the next launch must not re-prompt.
	trustRefresh  func(workingDir string)
	memoryDB      *db.DB
	memoryProject int64
	homeDir       string
	workDir       string
	cmdRegistry   *commands.Registry
	agentCancel   context.CancelFunc
	approvalMode  policy.ApprovalMode // current interaction mode: plan/default/edit/copilot/auto
	approvalModel *approvalModel
	questionModel *questionModel

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
	// Prompt history (project-scoped), newest first. histIdx == -1 means
	// "not browsing history, editing own draft"; draft stashes in-progress
	// text while browsing.
	history []string
	histIdx int
	draft   string
	setReg  *settings.Registry
	// configSavePending means state.Config contains a change that could not
	// be persisted. An otherwise unchanged /set retries the full config.
	configSavePending bool

	// diagnosticCount caches the number of semantic config diagnostics
	// found by Diagnose. Refreshed at construction and after every
	// successful persist. Zero means no known issues.
	diagnosticCount int

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

	// workspaceBroker, when non-nil, delivers WorkspaceEvents so the
	// status line's git info follows the session's active root without
	// waiting for the 5s tick.
	workspaceBroker *pubsub.Broker[session.WorkspaceEvent]
	workspaceEvents <-chan pubsub.Event[session.WorkspaceEvent]

	// gitInfo holds the cached branch + worktree for state.WorkingDir,
	// refreshed on a throttled tick and on every WindowSizeMsg. lastGitRead
	// bounds the throttle (see handleAgentTick).
	gitInfo     gitinfo.Info
	lastGitRead time.Time

	// New Layout State
	rawWidth  int // unclamped terminal dimensions (gate check)
	rawHeight int
	width     int // clamped to ≥ minTerminalWidth/Height (internal geometry)
	height    int
	// leftWidth is the width of the left column — everything except the
	// side rail. When the rail is absent it equals width. The status line
	// is the one component that keeps the full frame width.
	leftWidth int
	// railWidth is the side rail's width, 0 when the rail is not shown.
	railWidth int
	// rail is the side panel's section stack.
	rail *sidepanel.Rail
	// railHidden is the session-only Ctrl+B override. Not persisted.
	railHidden bool
	// mouseReleased is the session-only Ctrl+S override that hands the mouse
	// back to the terminal so click-drag text selection works without the
	// terminal's modifier key. Not persisted; [tui].mouse_capture is the
	// durable setting. Capture and native selection are mutually exclusive —
	// the terminal cannot deliver events to both — so this is a toggle rather
	// than something the two features can share.
	mouseReleased bool
	// railRepoStats is refreshed on turn boundaries, never during render.
	railRepoStats sidepanel.RepoStats
	// railTurns is the recent turn-metrics cache, refreshed when a turn
	// completes. Never queried during render.
	railTurns []db.TurnMetricsRow
	// railBaseRef is the commit the changed-files section diffs against,
	// captured once at session start.
	railBaseRef string
	// railChanged is the changed-files cache, refreshed on turn boundaries.
	railChanged []sidepanel.ChangedFile
	viewport    viewport.Model

	// Viewport dirty tracking.
	lastTranscriptHash uint64
	detailExpanded     bool
	// itemExpanded holds per-item expand/collapse overrides set by clicking
	// a transcript block. An item with no entry here follows detailExpanded.
	// Cleared whenever ctrl+g flips the global default (see keypress.go).
	itemExpanded map[itemKey]bool
	// activeToolExpanded is a click override for the single in-flight tool
	// call block (renderActiveToolCall) — it has no stable itemKey since it
	// isn't logged to the audit log until it completes. Reset to false
	// whenever a new tool starts; see refreshViewport.
	activeToolExpanded bool
	// activeToolStartedAt tracks the in-flight tool call's StartedAt so
	// refreshViewport can detect "a new tool started" and reset
	// activeToolExpanded. Zero when no tool is active.
	activeToolStartedAt time.Time
	// clickRegions maps content-line ranges in the transcript viewport to
	// the transcript block occupying them, rebuilt every time refreshViewport
	// rebuilds blocks. See click.go.
	clickRegions []clickRegion
	// viewStack is the subagent drill-down stack: when non-empty, the
	// transcript viewport renders the top subagent's child session instead
	// of the orchestrator's. Pushed by clicking a subagent card (see
	// click.go), popped by Esc (see keypress.go). Depth is ≤1 today
	// (nested agent.run is forbidden) but the stack is kept general.
	viewStack []session.SubagentView
	// rollbackArmed is the first half of Ctrl+R's arm-then-confirm. Cleared
	// by any other keypress; see handleKeypress.
	rollbackArmed bool
	// interruptArmed is set when Ctrl+C has interrupted a turn, so a second
	// press quits. Cleared by any other keypress.
	interruptArmed bool
	viewportFollow bool

	// Connect panel (docked; opened by /connect, /models, Ctrl+P).
	connectModel *connect.Model
	discovered   map[string][]schema.ModelInfo
	// modelCacheDir, when set, enables on-disk caching of the discovered
	// list via internal/llm/provider/modelcache. Without it discovery is
	// in-memory only.
	modelCacheDir string

	// Picker modal (opened by commands like /rewind, /branches, /mode).
	// The picker itself is hosted in m.dock; pickerCommand records which
	// command opened it so PickedMsg can dispatch correctly.
	pickerCommand string
	// resumeSession is non-empty when the user asked to resume a different
	// session; the app.Run program runner reads it from the final model.
	resumeSession string
	dock          dock.Host

	spinner      Spinner
	spinnerFrame string
	// turnStartedAt is when the current agent turn began, used for the
	// pinned turn spinner's elapsed clock. Zero while idle. Distinct from
	// session.Activity.StartedAt, which is per-phase and resets between
	// phases — the spinner must survive those gaps.
	turnStartedAt  time.Time
	successPulse   bool
	successPulseAt time.Time
	now            func() time.Time

	// Pinned todo panel (Ctrl+T cycles expanded → collapsed → hidden).
	// todosDismissed hides the all-done summary from the next turn
	// onward; todosSig detects the agent rewriting the list, which
	// un-dismisses it.
	todoPanelMode  todoPanelMode
	todosDismissed bool
	todosSig       string

	// connectReturnToSettings and connectReturnFilter track whether the
	// connect wizard was opened from the settings browser, so completing
	// or canceling the wizard returns to the browser with the same filter.
	connectReturnToSettings bool
	connectReturnFilter     string

	// customAgentFactory builds a one-shot AgentRunner for a named custom
	// agent. Wired from app.go; used by buildCustomAgentRunner for Run-now.
	customAgentFactory CustomAgentRunnerFactory

	// toolRegistry is the live tool registry, used by the agents roster
	// panel for tool denylist validation. Wired from app.go.
	toolRegistry *registry.Registry

	// pendingRun holds the pre-flight state while the cast list panel is
	// open. It is set by openRunPreflight and consumed by the castlist
	// StartMsg/CancelMsg handlers.
	pendingRun *pendingAgentRun

	// pipelineRunner is the active plan-execution runner, stored between the
	// /sdd command dispatch and the preflight confirmation (or the human gate
	// answer). Set by the sdd command handler; consumed by the Enter key handler.
	pipelineRunner AgentRunner

	// pendingSDDGate is set when the controller returns ErrHumanGateRequired.
	// While true, the TUI renders the gate prompt and routes y/n keypresses
	// to resolve or abort the gate.
	pendingSDDGate bool

	// configLayers is a pointer to the runtime's Layers snapshot, shared so
	// the model can read provenance after each successful persist.
	configLayers **config.Layers
	// layerReloader re-runs LoadLayers to refresh the provenance snapshots.
	// The bool reports whether the reload succeeded; when false the caller
	// must not write through *m.configLayers.
	layerReloader func() (config.Layers, bool)
}

// pendingAgentRun captures the runner and goal for a run that is waiting
// for the user to confirm via the cast list panel.
type pendingAgentRun struct {
	runner AgentRunner
	goal   string
}

type Option func(*Model)

type ConfigReloader func(cfg config.Config) error

func WithConfigReloader(fn ConfigReloader) Option {
	return func(m *Model) {
		m.configReloader = fn
	}
}

// WithRunnerSource lets the model fetch the runtime's current runner after
// a successful config reload. It matters when the TUI started without a
// runner (the initial provider build failed): the reload rebuilds one, and
// adoptRunner wires it in so the session becomes usable without a restart.
func WithRunnerSource(fn func() (context.Context, AgentRunner)) Option {
	return func(m *Model) {
		m.runnerSource = fn
	}
}

// WithOpenConnectOnStart opens the connect panel as the TUI starts. Used
// for first run: provider setup is the same flow on the first run and on
// the hundredth.
func WithOpenConnectOnStart() Option {
	return func(m *Model) { m.openConnectOnStart = true }
}

// WithTrustPrompt opens the modal folder-trust panel at startup. decide is
// called with the user's answer; trusting quits the program so the app can
// reload with the project config in force.
func WithTrustPrompt(workingDir string, decide func(trust.Decision)) Option {
	return func(m *Model) {
		m.trustPromptDir = workingDir
		m.trustDecide = decide
	}
}

// WithTrustRefresh registers a hook invoked after an interactive
// project-config save succeeds, so permanent trust can track config edits
// the user made from a trusted session instead of re-prompting next launch.
func WithTrustRefresh(refresh func(workingDir string)) Option {
	return func(m *Model) { m.trustRefresh = refresh }
}

func WithCommandRegistry(reg *commands.Registry) Option {
	return func(m *Model) {
		m.cmdRegistry = reg
	}
}

func WithHomeDir(homeDir string) Option {
	return func(m *Model) {
		m.homeDir = homeDir
	}
}

func WithWorkingDir(workDir string) Option {
	return func(m *Model) {
		m.workDir = workDir
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

// WithPipelineFactory configures the TUI to build a plan-execution runner
// on demand when /sdd <plan-file> is submitted. The factory is called per
// run, not at startup.
func WithPipelineFactory(ctx context.Context, factory func(planPath string) AgentRunner) Option {
	return func(m *Model) {
		m.ctx = ctx
		m.pipelineFactory = factory
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

// WithWorkspaceBroker wires the broker carrying session.WorkspaceEvents.
// When broker is nil, git info still follows the active root on the 5s
// agent tick (used by tests that don't construct a broker).
func WithWorkspaceBroker(ctx context.Context, broker *pubsub.Broker[session.WorkspaceEvent]) Option {
	return func(m *Model) {
		m.ctx = ctx
		m.workspaceBroker = broker
	}
}

// WithCustomAgentRunnerFactory wires a factory that builds a one-shot
// AgentRunner for a named custom agent. Used by the /agents Run-now action.
func WithCustomAgentRunnerFactory(fn CustomAgentRunnerFactory) Option {
	return func(m *Model) {
		m.customAgentFactory = fn
	}
}

// WithToolRegistry wires the live tool registry into the model so the
// agents roster panel can validate tool denylist entries.
func WithToolRegistry(reg *registry.Registry) Option {
	return func(m *Model) {
		m.toolRegistry = reg
	}
}

// WithConfigLayers supplies the layered config snapshots used for settings
// provenance. The pointer is shared; the model replaces what it points to
// after each successful persist so provenance tracks saves.
func WithConfigLayers(layers **config.Layers) Option {
	return func(m *Model) { m.configLayers = layers }
}

// WithLayerReloader supplies a function that re-runs LoadLayers to refresh
// the provenance snapshots after a successful persist. The bool reports
// whether the reload succeeded; when false the model does not overwrite
// the shared Layers pointer.
func WithLayerReloader(fn func() (config.Layers, bool)) Option {
	return func(m *Model) { m.layerReloader = fn }
}

// WithModelCache enables persistent model discovery caching under dataDir.
// Without it the session behaves as before: discovery is in-memory only.
func WithModelCache(dataDir string) Option {
	return func(m *Model) { m.modelCacheDir = dataDir }
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
// anything derived from it. The footer mode is re-seeded from the new
// config so a /settings or /set approval-mode change is reflected in the
// status line (the policy engine is rebuilt from the same value by the
// runtime reload).
func (m *Model) applyNewConfig(cfg config.Config) {
	m.state.Config = cfg
	m.approvalMode = policy.ParseApprovalMode(cfg.Agent.ApprovalMode)
	m.setReg = nil
	m.setPopup = nil
	m.lastInputForPopups = ""
	loadTheme(cfg.TUI)
}

// refreshDiagnostics runs config.Diagnose against the current session config
// and caches the number of diagnostics found. Real per-layer provenance
// comes from state.Layers(); when unset (e.g. tests that don't
// construct via app.Run), the zero value means every Diagnostic.Source
// reads as "default".
func (m *Model) refreshDiagnostics() {
	ds := config.Diagnose(m.state.Config, m.state.Layers())
	m.diagnosticCount = len(ds)
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
	if m.trustRefresh != nil {
		m.trustRefresh(m.state.WorkingDir)
	}
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
		m.afterRuntimeReload()
	}
	m.applyNewConfig(cfg)
	m.refreshDiagnostics()
	return nil, nil
}

// afterRuntimeReload runs the recovery steps that become valid once the
// runtime has successfully rebuilt itself from a new config: the stale
// provider-error banner no longer applies (a rebuilt runner proves
// provider construction worked), and a runner rebuilt after a startup
// failure is adopted so the session becomes usable without a restart.
func (m *Model) afterRuntimeReload() {
	m.state.SetProviderError(nil)
	m.adoptRunner()
}

// adoptRunner installs the runtime's rebuilt runner when the TUI started
// without one (e.g. the initial provider build failed). An existing runner
// is never replaced: reload mutates it in place via CopyFrom.
func (m *Model) adoptRunner() {
	if m.runner != nil || m.runnerSource == nil {
		return
	}
	ctx, runner := m.runnerSource()
	if runner == nil {
		return
	}
	m.ctx = ctx
	m.runner = runner
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
		// Global-target branch: if the field is marked writeGlobal, write
		// the single value to the user config instead of the project config.
		if f, ok := reg.Lookup(key); ok && settings.FieldWriteGlobal(f) && settings.FieldTomlPath(f) != "" {
			tomlPath := settings.FieldTomlPath(f)
			value, ok := config.LookupPath(reg.Config(), tomlPath)
			if !ok {
				sys(fmt.Sprintf("✗ %s: cannot look up value at %s", key, tomlPath))
				return
			}
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				// Fall back to persistAndReload if home is unavailable.
				saveErr, reloadErr := m.persistAndReload(reg.Config())
				m.reportSetResult(key, change, saveErr, reloadErr)
				return
			}
			userPath := config.UserConfigPath(home)
			if err := config.SaveUserConfigValue(userPath, tomlPath, value); err != nil {
				m.applyNewConfig(reg.Config())
				m.configSavePending = true
				sys(fmt.Sprintf("✗ %s applied in session, but user config save failed: %v", key, err))
				return
			}
			m.configSavePending = false
			if m.configReloader != nil {
				m.setReg = nil
				if err := m.configReloader(reg.Config()); err != nil {
					m.applyNewConfig(reg.Config())
					sys(fmt.Sprintf("✗ %s saved, but live reload failed: %v", key, err))
					return
				}
				m.afterRuntimeReload()
			}
			m.applyNewConfig(reg.Config())
			if m.configLayers != nil && m.layerReloader != nil {
				if layers, ok := m.layerReloader(); ok {
					*m.configLayers = &layers
				}
			}
			m.refreshOpenSettingsBrowser()
			if !change.Changed {
				sys(fmt.Sprintf("✓ %s persisted · %s", key, relPath(home, config.UserConfigPath(home))))
			} else {
				sys(fmt.Sprintf("✓ %s: %s → %s · %s", key, change.OldValue, change.NewValue, relPath(home, config.UserConfigPath(home))))
			}
			return
		}

		saveErr, reloadErr := m.persistAndReload(reg.Config())
		m.reportSetResult(key, change, saveErr, reloadErr)
		return
	}
}

// reportSetResult emits the system message for a /set command outcome.
// Shared by the project-save and global-target branches.
func (m *Model) reportSetResult(key string, change settings.Change, saveErr, reloadErr error) {
	sys := func(text string) {
		m.state.AddMessage(session.RoleSystem, text, session.ContentTypePlain)
	}
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
}

// provenanceForPath renders the one-line provenance summary for a TOML
// path, or "" when layers are unavailable.
func (m *Model) provenanceForPath(tomlPath string) string {
	if m.configLayers == nil || *m.configLayers == nil {
		return ""
	}
	p := (*m.configLayers).ProvenanceOf(tomlPath)
	if p.SetBy == config.LayerDefault && p.Overrides == config.LayerDefault {
		return "set by: built-in default"
	}
	s := "set by: " + p.SetBy.String()
	if p.Overrides != config.LayerDefault {
		s += " · overrides: " + p.Overrides.String()
	}
	return s
}

func (m *Model) openSettingsBrowser(query string) {
	browser := settings.NewBrowser(
		m.state.Config,
		projectConfigPath(m.state.WorkingDir),
		query,
		settings.WithProvenance(m.provenanceForPath),
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

func (m *Model) openAgentsRoster(arg string) {
	dispatch := func(agentName, goal string) tea.Cmd {
		if agentName == "" || m.busy {
			return nil
		}
		// Build a custom-agent runner and start the run.
		// buildCustomAgentRunner surfaces its own error messages.
		runner := m.buildCustomAgentRunner(agentName)
		if runner == nil {
			m.refreshViewport()
			return nil
		}
		_, cmd := m.startAgentRun(runner, goal)
		return cmd
	}
	m.dock.Open(agents.NewRosterPanelWithRegistry(
		m.state.Config,
		projectConfigPath(m.state.WorkingDir),
		arg,
		dispatch,
		m.toolRegistry,
	))
}

// buildCustomAgentRunner builds an AgentRunner for the named custom agent.
// Returns nil when the runner or factory is not available. When the factory
// returns an error, the error is surfaced to the user and nil is returned.
// The swarm fallback is only used when the factory is not wired (nil).
func (m *Model) buildCustomAgentRunner(name string) AgentRunner {
	if m.customAgentFactory != nil {
		runner, err := m.customAgentFactory(name)
		if err != nil {
			m.state.AddMessage(session.RoleSystem,
				fmt.Sprintf("Cannot run custom agent %q: %v", name, err),
				session.ContentTypePlain)
			m.refreshViewport()
			return nil
		}
		if runner != nil {
			return runner
		}
	}
	// Fallback: use the swarm runner only when the factory is not wired.
	if m.customAgentFactory == nil && m.swarmRunner != nil {
		return m.swarmRunner
	}
	return nil
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
	// lipgloss.NewStyle().Foreground(accentColor).Bold(true) instead.)
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
	styles.Cursor.Color = accentColor
	styles.Cursor.Blink = true
	input.SetStyles(styles)

	m := Model{
		state:          state,
		input:          input,
		editingCommand: false,
		// Seed from config, not a hardcoded ModeDefault. app.Run wires the
		// policy engine from this same value, so hardcoding here made the
		// status line claim "default" while the engine was auto-approving
		// every edit.
		approvalMode:   policy.ParseApprovalMode(state.Config.Agent.ApprovalMode),
		ctx:            context.Background(),
		viewport:       viewport.New(),
		spinner:        NewSpinner(),
		now:            time.Now,
		viewportFollow: true,
		discovered:     map[string][]schema.ModelInfo{},
		itemExpanded:   map[itemKey]bool{},
	}
	for _, opt := range opts {
		opt(&m)
	}

	// Seed the in-session discovered map from the on-disk cache so the
	// model picker opens instantly with the last-seen lists when the
	// config hash still matches.
	if m.modelCacheDir != "" {
		c := modelcache.Load(m.modelCacheDir)
		now := time.Now()
		for name, pc := range state.Config.Providers {
			if models, ok := c.Lookup(name, pc, modelcache.DefaultTTL, now); ok {
				m.discovered[name] = models
			}
		}
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
	if m.workspaceBroker != nil && m.workspaceEvents == nil {
		m.workspaceEvents = m.workspaceBroker.Subscribe(m.ctx)
	}

	// Eagerly build inline approval/question forms if the session already
	// has a pending request, so the first render shows the huh surface
	// instead of the legacy fallback panels.
	if tc := m.state.PendingApproval(); tc != nil {
		m.approvalModel = newApprovalModel(tc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, m.state.HasBackup(), max(m.leftWidth-4, 30))
	}
	if q := m.state.PendingQuestion(); q != nil {
		m.questionModel = newQuestionModel(q, max(m.leftWidth-4, 30))
	}

	m.gitInfo = gitinfo.Read(state.Workspace().ActiveRoot)
	m.lastGitRead = m.now()
	m.railBaseRef = gitinfo.HeadSHA(state.WorkingDir)

	m.histIdx = -1
	if state.Config.History.Enabled {
		if database := state.DB(); database != nil && m.memoryProject != 0 {
			if prompts, err := database.RecentPrompts(m.memoryProject, promptHistoryLimit); err == nil {
				m.history = prompts
			}
		}
	}
	m.rebuildRail()

	if database := state.DB(); database != nil {
		if projectID := m.memoryProject; projectID != 0 {
			files, ferr := database.CountFiles(projectID)
			syms, serr := database.CountSymbols(projectID)
			if ferr == nil && serr == nil {
				m.railRepoStats = sidepanel.RepoStats{Files: files, Symbols: syms}
			}
		}
	}

	if m.trustDecide != nil && m.trustPromptDir != "" {
		m.dock.Open(trustpanel.New(m.trustPromptDir, m.trustDecide))
	}

	if m.openConnectOnStart && !m.dock.IsOpen() {
		// A pending modal (trust prompt) owns the dock; connect opens after
		// it resolves. On the trust-grant path the app reloads and lands
		// here again with trust settled, so nothing is lost.
		m.openConnect("/")
	}

	m.refreshDiagnostics()

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
	if m.workspaceEvents != nil {
		cmds = append(cmds, pumpWorkspaceEvents(m.workspaceEvents))
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

	cfg := m.state.Config.TUI.SidePanel
	if m.railHidden {
		cfg.Enabled = false
	}
	m.leftWidth, m.railWidth = sidepanel.Geometry(width, minTerminalWidth, cfg)

	// Input interior: the ▍ bar (1 cell) + 1 right margin = 2 reserved
	// cells. The textarea's SetWidth sets the text wrap width and
	// internally subtracts promptWidth (2). Reserve 2 so rendered lines
	// stay inside the left column.
	m.input.SetWidth(max(m.leftWidth-2, 1))

	// Transcript viewport spans the left column (borderless).
	m.viewport.SetWidth(max(m.leftWidth, 1))
	m.input.MaxHeight = m.maxInputHeight()
	m.viewport.SetHeight(max(height-transcriptFrameRows-m.scrollHintRows()-m.breadcrumbRows()-m.todoPanelRows()-m.runPanelRows()-m.liveStripRows()-m.dockRows()-m.turnSpinnerRows()-m.inputAreaRows()-statusLineRows, 1))
}

// railEnabled reports whether the side rail is being rendered.
func (m Model) railEnabled() bool { return m.railWidth > 0 }

// refreshRailTurns reloads the turn-metrics cache the side panel reads.
// Called on turn completion, never from View.
func (m *Model) refreshRailTurns() {
	database := m.state.DB()
	if database == nil || !m.railEnabled() {
		return
	}
	rows, err := database.RecentTurnMetrics(m.memoryProject, 24)
	if err != nil {
		return
	}
	m.railTurns = rows
}

// refreshRailChanged reloads the changed-files cache. Shells out to git,
// so it runs on turn boundaries only — never from View.
func (m *Model) refreshRailChanged() {
	if !m.railEnabled() {
		return
	}
	m.railChanged = changedfiles.Read(m.state.WorkingDir, m.railBaseRef)
}

// rebuildRail constructs the side rail from the full section list, filtering
// out any section whose ID appears in the config's hidden list. Both the
// constructor and tests call this so there is a single code path.
func (m *Model) rebuildRail() {
	all := []sidepanel.Section{
		sidepanel.SwarmSection{},
		sidepanel.SDDSection{},
		sidepanel.ContextSection{},
		sidepanel.ChangedSection{},
		sidepanel.ToolsSection{},
		sidepanel.RulesSection{},
		sidepanel.RepoSection{},
		sidepanel.SessionSection{},
	}
	hidden := map[string]bool{}
	for _, id := range m.state.Config.TUI.SidePanel.Hidden {
		hidden[id] = true
	}
	visible := make([]sidepanel.Section, 0, len(all))
	for _, s := range all {
		if !hidden[s.ID()] {
			visible = append(visible, s)
		}
	}
	m.rail = sidepanel.New(visible...)
}

// railData assembles the side panel's render snapshot. Everything here is
// either already in memory or cached on turn boundaries — this runs once
// per frame and must never query the DB or shell out.
func (m Model) railData() sidepanel.Data {
	return sidepanel.Data{
		State:   m.state,
		Git:     m.gitInfo,
		Repo:    m.railRepoStats,
		Turns:   m.railTurns,
		Changed: m.railChanged,
		Pack:    m.state.ContextPack(),
		Audit:   m.state.AuditLog(),
		Rules:   m.state.SessionRules(),
		Swarm:   m.state.SwarmProgress(),
		SDD:     m.state.SDDProgress(),
		Spinner: m.turnSpinnerFrame(),
		Now:     m.now(),
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Ctrl+C interrupts an in-flight turn on the first press and quits on the
	// second. Checked before any overlay routing so it can never be captured
	// by a form's keymap.
	//
	// It used to quit outright on the first press. Ctrl+C is the universal
	// "stop what you're doing" reflex, so the moment a user most wants to
	// interrupt a slow or misbehaving turn was exactly the moment they lost
	// the session — and with AltScreen there is no scrollback to read
	// afterwards. Esc already had the interrupt semantics; the better-known
	// key just did the more destructive thing.
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+c" {
		if m.busy && !m.interruptArmed {
			m.interruptArmed = true
			m.cancelTurn()
			m.state.AddMessage(session.RoleSystem,
				"Turn interrupted. Press Ctrl+C again to quit.",
				session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		}
		return m, m.beginShutdown()
	}
	// Any other keypress clears the armed quit, so Ctrl+C never quits on a
	// press the user has mentally separated from the interrupt.
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() != "ctrl+c" {
		m.interruptArmed = false
	}

	// WindowSizeMsg must always resize the underlying layout (and the
	// settings/memory overlays) regardless of which overlay is open.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.resize(ws.Width, ws.Height)
		if m.approvalModel != nil {
			m.approvalModel.SetSize(max(m.leftWidth-4, 30))
		}
		if m.questionModel != nil {
			m.questionModel.SetSize(max(m.leftWidth-4, 30))
		}
		// Refresh git state on focus/resize: a fresh view should reflect
		// current branch even if it changed in another tool.
		m.gitInfo = gitinfo.Read(m.state.Workspace().ActiveRoot)
		m.lastGitRead = m.now()
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
		if msg.Saved {
			// The panel already wrote the target file; reload only.
			if m.configReloader != nil {
				m.setReg = nil
				if err := m.configReloader(msg.Cfg); err != nil {
					m.state.AddMessage(session.RoleSystem,
						fmt.Sprintf("✗ settings saved, but live reload failed: %v", err),
						session.ContentTypePlain)
					m.refreshViewport()
					return m, nil
				}
				m.afterRuntimeReload()
			}
			m.applyNewConfig(msg.Cfg)
			m.configSavePending = false
			if !msg.GlobalTarget && m.trustRefresh != nil {
				m.trustRefresh(m.state.WorkingDir)
			}
			if m.configLayers != nil && m.layerReloader != nil {
				if layers, ok := m.layerReloader(); ok {
					*m.configLayers = &layers
				}
			}
			cfgPath := projectConfigPath(m.state.WorkingDir)
			base := m.state.WorkingDir
			if msg.GlobalTarget {
				if home, err := os.UserHomeDir(); err == nil && home != "" {
					cfgPath = config.UserConfigPath(home)
					base = home
				}
			}
			for _, receipt := range msg.Receipts {
				m.state.AddMessage(session.RoleSystem,
					"✓ "+receipt+" · "+relPath(base, cfgPath),
					session.ContentTypePlain)
			}
			m.refreshViewport()
			return m, nil
		}
		// Global-target save failure: apply in memory, mark pending, emit
		// error, but NEVER fall through to persistAndReload (which writes
		// into the project config, violating the provenance constraint).
		if msg.GlobalTarget && !msg.Saved {
			m.applyNewConfig(msg.Cfg)
			m.configSavePending = true
			userPath := ""
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				userPath = config.UserConfigPath(home)
			}
			m.state.AddMessage(session.RoleSystem,
				fmt.Sprintf("✗ save failed: %v · %s", msg.SaveErr, relPath(m.state.WorkingDir, userPath)),
				session.ContentTypePlain)
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
		if m.configLayers != nil && m.layerReloader != nil {
			if layers, ok := m.layerReloader(); ok {
				*m.configLayers = &layers
			}
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
	case trustpanel.ClosedMsg:
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	case docpanel.ClosedMsg:
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	case docpanel.ActionMsg:
		m.dock.CloseNow()
		if msg.Result.Doc != nil {
			m.openDocPanel(msg.Result.Doc)
			return m, nil
		}
		if msg.Result.Text != "" {
			m.state.AddMessage(session.RoleSystem, msg.Result.Text, session.ContentTypePlain)
		}
		m.refreshViewport()
		return m, nil
	}

	// Runtime messages always stay with the parent model so background state
	// remains current while a dock panel is open.
	switch msg.(type) {
	case agentFinishedMsg, jobCountMsg, steeringMsg, agentTickMsg, spinnerTickMsg:
		return m.handleRuntimeMessage(msg)
	}

	// OpenConnectMsg from the settings browser: capture the current filter,
	// close the browser, and open the connect wizard.
	if _, ok := msg.(settings.OpenConnectMsg); ok {
		if bp, ok := m.dock.Panel().(*settings.BrowserPanel); ok {
			m.connectReturnFilter = bp.FilterValue()
		}
		m.connectReturnToSettings = true
		m.dock.CloseNow()
		m.openConnect("/")
		m.refreshViewport()
		return m, nil
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
	case connect.RefreshMsg:
		for _, name := range pm.Providers {
			delete(m.discovered, name)
			m.evictDiscovered(name)
		}
		return m, m.probeProviders(pm.Providers)
	case connect.DoneMsg:
		m.applyConnectDone(pm)
		m.dock.CloseNow()
		m.connectModel = nil
		if m.connectReturnToSettings {
			m.connectReturnToSettings = false
			m.openSettingsBrowser(m.connectReturnFilter)
			m.connectReturnFilter = ""
		}
		m.refreshViewport()
		return m, nil
	case connect.CancelledMsg:
		m.dock.CloseNow()
		m.connectModel = nil
		if m.connectReturnToSettings {
			m.connectReturnToSettings = false
			m.openSettingsBrowser(m.connectReturnFilter)
			m.connectReturnFilter = ""
		}
		m.refreshViewport()
		return m, nil
	case connect.TickMsg:
		if _, ok := m.dock.Panel().(connect.Panel); ok {
			return m, m.dock.Update(pm)
		}
		return m, nil
	case probe.ResultMsg:
		if pm.Err == nil && pm.Provider != "" {
			m.discovered[pm.Provider] = pm.Models
			m.persistDiscovered(pm.Provider, pm.Models)
		}
		if _, ok := m.dock.Panel().(connect.Panel); ok {
			return m, m.dock.Update(pm)
		}
		return m, nil
	case picker.PickedMsg:
		// Forward to panel overlays that host their own picker (agents roster,
		// connect wizard). Their internal handlers will emit their own done
		// messages; we must not close the dock or treat this as a top-level
		// picker pick here.
		switch m.dock.Panel().(type) {
		case *agents.Panel, connect.Panel:
			return m, m.dock.Update(pm)
		}
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
			if pm.Value == sddCustomPlanPathValue {
				m.openSDDCustomPlanPathPicker()
				m.refreshViewport()
				return m, nil
			}
			// Dock already closed above; dispatch /sdd with the picked path.
			return m.dispatchCommand("/sdd " + pm.Value)
		case cmdName == "sessions":
			if pm.Value == "" {
				m.refreshViewport()
				return m, nil
			}
			return m.beginResume(pm.Value)
		case cmdName == "mode-elevation":
			chosen := pm.Value
			if tc := m.state.PendingApproval(); tc != nil {
				tc.Respond(session.UserApprovalDecision{Approved: true, Edited: chosen})
			}
			m.state.SetPendingApproval(nil)
			m.setMode(chosen)
			newCfg := m.state.Config
			newCfg.Agent.ApprovalMode = chosen
			saveErr, reloadErr := m.persistAndReload(newCfg)
			if saveErr != nil {
				m.state.AddMessage(session.RoleSystem,
					fmt.Sprintf("✗ mode elevation saved in session, but config save failed: %v", saveErr),
					session.ContentTypePlain)
			} else if reloadErr != nil {
				m.state.AddMessage(session.RoleSystem,
					fmt.Sprintf("✗ mode elevation saved, but live reload failed: %v", reloadErr),
					session.ContentTypePlain)
			}
			msg, ok := modeSwitchMessage[chosen]
			if !ok {
				msg = fmt.Sprintf("Switched to %s mode.", chosen)
			}
			m.state.AddMessage(session.RoleSystem, msg, session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		default:
			return m.dispatchCommand("/" + cmdName + " " + pm.Value)
		}
	case picker.CancelledMsg:
		// Forward to panel overlays that host their own picker.
		switch m.dock.Panel().(type) {
		case *agents.Panel, connect.Panel:
			return m, m.dock.Update(pm)
		}
		cmdName := m.pickerCommand
		m.dock.CloseNow()
		m.pickerCommand = ""
		if cmdName == "mode-elevation" {
			if tc := m.state.PendingApproval(); tc != nil {
				tc.Respond(session.UserApprovalDecision{Approved: false})
			}
			m.state.SetPendingApproval(nil)
		}
		m.refreshViewport()
		return m, nil
	case castlist.StartMsg:
		m.dock.CloseNow()
		run := m.pendingRun
		m.pendingRun = nil
		if run == nil || m.busy {
			m.refreshViewport()
			return m, nil
		}
		return m.startAgentRun(run.runner, run.goal)
	case castlist.CancelMsg:
		m.dock.CloseNow()
		m.pendingRun = nil
		m.refreshViewport()
		return m, nil
	}
	if m.dock.IsOpen() {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.PasteMsg:
			return m, m.dock.Update(msg)
		default:
			// A panel's own async results (install finished, scan returned)
			// are typically unexported, so no case above can name them and
			// they would otherwise fall through and be dropped — leaving the
			// panel's Update case dead code. Let the panel claim them.
			if m.dock.OwnsMsg(msg) {
				return m, m.dock.Update(msg)
			}
		}
		// Other non-key messages (ticks, agent events) keep flowing to the
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
	//
	// Scroll gestures are handled first so the user can review transcript
	// history before approving a destructive command.
	if tc := m.state.PendingApproval(); tc != nil {
		if updated, cmd, handled := m.scrollTranscript(msg); handled {
			return updated, cmd
		}
		return m.handleApproval(msg, tc)
	}

	// Inline question prompt: when a clarifying question is pending, route
	// messages to the question form. Scroll gestures are handled first so the
	// transcript remains navigable while the question is visible.
	if q := m.state.PendingQuestion(); q != nil {
		if updated, cmd, handled := m.scrollTranscript(msg); handled {
			return updated, cmd
		}
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
	case tea.MouseWheelMsg:
		// Only delivered when tui.mouse_capture is on (see View). AltScreen
		// leaves no terminal scrollback, so without this the wheel does
		// nothing at all.
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		switch msg.Button {
		case tea.MouseWheelUp:
			m.viewportFollow = false
		case tea.MouseWheelDown:
			if m.viewport.AtBottom() {
				m.viewportFollow = true
			}
		}
		return m, vpCmd
	case tea.MouseClickMsg:
		if cmd, handled := m.handleTranscriptClick(msg); handled {
			return m, cmd
		}
		return m, nil
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

// isModeElevationApproval reports whether a pending approval is a mode
// elevation request: its decision UI is the dock picker, so the inline
// approval chooser/panel must not render for it.
func isModeElevationApproval(tc *session.PendingToolCall) bool {
	return tc.Name == "mode.request" || strings.HasPrefix(tc.Reason, "mode-elevation:")
}

// handleApproval routes messages to the inline approval chooser (or the
// edit-command textarea sub-mode) while a tool-call approval is pending. It
// is called before the main keypress switch so huh's internal navigation
// messages (nextFieldMsg/nextGroupMsg) round-trip back to the form.
func (m Model) handleApproval(msg tea.Msg, tc *session.PendingToolCall) (tea.Model, tea.Cmd) {
	// mode.request: show the editing-variant picker instead of the normal
	// approval dialog. The picker opens via the dock; while the dock is
	// open, keypresses are routed to the picker and non-key messages
	// return early here. On PickedMsg the main Update switch handles the
	// response and mode change.
	if isModeElevationApproval(tc) {
		if m.dock.IsOpen() {
			// Picker already open; don't interfere.
			return m, nil
		}
		items := []picker.Item{
			{Label: "Edit", Detail: "plan + confirm each", Value: "edit"},
			{Label: "Copilot", Detail: "auto-approve, may ask", Value: "copilot"},
			{Label: "Auto", Detail: "fully autonomous", Value: "auto"},
		}
		m.openPicker("mode-elevation", "Elevate to editing mode", "choose an editing mode", items, "")
		m.refreshViewport()
		return m, nil
	}

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
		m.approvalModel = newApprovalModel(tc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, m.state.HasBackup(), max(m.leftWidth-4, 30))
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
			// The error is load-bearing: a partial rollback leaves a mixed
			// working tree, and reporting success would hide that from both
			// the user and the audit trail.
			ev := registry.AuditEvent{
				Timestamp:     time.Now(),
				ToolName:      "rollback",
				ResultSummary: "Rollback applied successfully",
			}
			if err := m.state.RollbackBackup(); err != nil {
				ev.Error = err.Error()
				ev.ResultSummary = "Rollback failed"
			}
			m.state.LogToolCall(ev)
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
		m.questionModel = newQuestionModel(q, max(m.leftWidth-4, 30))
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

// scrollTranscript routes viewport scroll gestures to the transcript
// viewport so the user can review history while an approval or question
// panel is open. It returns (model, cmd, true) when the message was handled.
func (m *Model) scrollTranscript(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		switch msg.Button {
		case tea.MouseWheelUp:
			m.viewportFollow = false
		case tea.MouseWheelDown:
			if m.viewport.AtBottom() {
				m.viewportFollow = true
			}
		}
		return *m, vpCmd, true
	case tea.MouseClickMsg:
		if cmd, handled := m.handleTranscriptClick(msg); handled {
			return *m, cmd, true
		}
		return *m, nil, false
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "pgup":
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			m.viewportFollow = false
			return *m, vpCmd, true
		case "pgdown":
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			if m.viewport.AtBottom() {
				m.viewportFollow = true
			}
			return *m, vpCmd, true
		case "ctrl+u":
			m.viewport.HalfPageUp()
			m.viewportFollow = false
			return *m, nil, true
		case "ctrl+d":
			m.viewport.HalfPageDown()
			if m.viewport.AtBottom() {
				m.viewportFollow = true
			}
			return *m, nil, true
		case "end":
			m.viewport.GotoBottom()
			m.viewportFollow = true
			return *m, nil, true
		}
	}
	return *m, nil, false
}

// inputChromeRows counts the rows the input area reserves for everything
// except the textarea itself: the SDD hint, question/approval panels, and
// the completion popup. Splitting this out of inputAreaRows breaks the
// circularity in the MaxHeight budget (available rows must not depend on
// the textarea's own height).
func (m Model) inputChromeRows() int {
	rows := 0
	if sd := m.state.SDDProgress(); sd.Active {
		rows++ // SDD hint row
	}
	if q := m.state.PendingQuestion(); q != nil {
		content := ""
		if m.questionModel != nil {
			content = m.questionModel.View()
		} else {
			content = renderQuestionPanel(q, max(m.leftWidth-4, 1))
		}
		rows += lipgloss.Height(content)
	} else if tc := m.state.PendingApproval(); tc != nil {
		// Mirror renderInputArea's switch exactly so the budget never
		// disagrees with what is on screen.
		var content string
		switch {
		case isModeElevationApproval(tc):
			// The dock picker owns the decision; nothing renders here.
		case m.editingCommand:
			// The ❯ prompt is rendered inside the textarea by SetPromptFunc,
			// so m.input.View() already includes it — do not prepend it again.
			content = m.input.View()
		case m.approvalModel != nil:
			content = m.approvalModel.View()
		default:
			content = renderApprovalPanel(tc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, max(m.leftWidth-4, 1))
		}
		rows += lipgloss.Height(content)
	}
	if p := m.activeCompletionPopup(); p != nil {
		// Cap the popup at completionPopupMax visible match rows plus the
		// panel title row (matches the renderer in view.go).
		rows += min(len(p.matches()), completionPopupMax) + completionPanelChromeRows
	}
	return rows
}

func (m Model) inputAreaRows() int {
	rows := m.inputChromeRows()
	if m.state.PendingQuestion() == nil && m.state.PendingApproval() == nil {
		// DynamicHeight clamps Height() to [MinHeight, MaxHeight], so the
		// only guard needed is the max(..., 1) floor.
		rows += max(m.input.Height(), 1)
	}
	return rows
}

// maxInputHeight is the row budget for the chat textarea: whatever the
// terminal leaves after the transcript frame, status line, auxiliary
// panels, input chrome, and the transcript floor. Always at least 1 so the
// input never becomes untypable on short terminals.
func (m Model) maxInputHeight() int {
	return max(m.height-transcriptFrameRows-m.scrollHintRows()-m.breadcrumbRows()-statusLineRows-m.todoPanelRows()-m.runPanelRows()-m.liveStripRows()-m.dockRows()-m.turnSpinnerRows()-m.inputChromeRows()-minTranscriptRows, 1)
}

// scrollHintRows reports the rows the "↑ scrolled — End to follow" hint
// occupies above the transcript: 1 while the user has scrolled off the
// bottom of an overflowing transcript, 0 otherwise. It is a row of the left
// column like any other, so it must be in the budget — an unbudgeted row
// makes the column taller than the terminal and pushes the input area off
// the bottom of the screen.
func (m Model) scrollHintRows() int {
	if !m.viewportFollow && m.viewport.TotalLineCount() > m.viewport.Height() {
		return 1
	}
	return 0
}

// turnSpinnerRows reports the rows reserved for the pinned turn spinner
// above the input. The row exists only while a turn is running: reserving
// it while idle rendered as a blank line directly above the todo panel.
// During an SDD run the row collapses to zero: the run panel owns the only
// spinner. Mirrors renderTurnSpinner's render condition exactly.
func (m Model) turnSpinnerRows() int {
	if m.state.SDDProgress().Active {
		return 0
	}
	if !m.busy || m.turnStartedAt.IsZero() {
		return 0
	}
	return 1
}

// liveStripRows reports the rows the live strip occupies: 1 while a
// swarm/SDD run or browser session is live, 0 otherwise.
func (m Model) liveStripRows() int {
	if m.renderLiveStrip() == "" {
		return 0
	}
	return 1
}

// renderTodoPanel renders the pinned todo panel for the current frame.
// The all-done summary is suppressed once the user has started another
// turn (spec: the summary "clears on the next user turn").
func (m Model) renderTodoPanel() string {
	todos := m.state.Todos()
	if m.todosDismissed && todosAllDone(todos) {
		return ""
	}
	return renderTodoPanelBody(todos, m.todoPanelMode, m.height, m.leftWidth)
}

// todoPanelRows reports the rows the pinned todo panel occupies.
func (m Model) todoPanelRows() int {
	body := m.renderTodoPanel()
	if body == "" {
		return 0
	}
	return lipgloss.Height(body)
}

// renderRunPanel renders the consolidated SDD run panel for the current
// frame. The panel owns the only spinner on screen during a run; the
// glyph comes from turnSpinnerFrame so it shares the 200ms flash gate.
func (m Model) renderRunPanel() string {
	return renderRunPanel(m.state.SDDProgress(), m.state.SDDGate(), m.turnSpinnerFrame(), m.now(), m.height, m.leftWidth)
}

// runPanelRows reports the rows the run panel occupies. The panel is
// rendered in the left column stack (view.go) but its height must be
// budgeted — otherwise the frame grows taller than the terminal and the
// input area is pushed off the bottom of the screen.
func (m Model) runPanelRows() int {
	panel := m.renderRunPanel()
	if panel == "" {
		return 0
	}
	return lipgloss.Height(panel)
}

// stripShowsBrowser reports whether the live strip is currently rendering
// the browser session (rather than a swarm or SDD run).
func (m Model) stripShowsBrowser() bool {
	return m.state.BrowserInfo().SessionOpen &&
		!m.state.SwarmProgress().Active &&
		!m.state.SDDProgress().Active
}

// ShouldShowStatusURL returns false when the live strip already shows the
// browser URL and tool name. The right-side status segment omits the URL
// then to avoid duplication.
func (m Model) ShouldShowStatusURL() bool {
	return !m.stripShowsBrowser()
}

// dockRows reports the rows the docked panel occupied at last render, so the
// transcript viewport shrinks while a panel is open.
func (m Model) dockRows() int { return m.dock.Rows() }

func (m *Model) updateViewportHeight() bool {
	m.input.MaxHeight = m.maxInputHeight()
	newViewportHeight := max(m.height-transcriptFrameRows-m.scrollHintRows()-m.breadcrumbRows()-m.todoPanelRows()-m.runPanelRows()-m.liveStripRows()-m.dockRows()-m.turnSpinnerRows()-m.inputAreaRows()-statusLineRows, 1)
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

// isUserTurn reports whether a transcript item is a user prompt, the boundary
// the turn separator marks.
func isUserTurn(item session.TranscriptItem) bool {
	return item.Kind == session.KindMessage &&
		item.Message != nil &&
		item.Message.Role == session.RoleUser
}

// drilledInto returns the subagent whose transcript is currently drilled
// into (top of viewStack), or false when the viewport shows the
// orchestrator's own transcript.
func (m *Model) drilledInto() (session.SubagentView, bool) {
	if len(m.viewStack) == 0 {
		return session.SubagentView{}, false
	}
	return m.viewStack[len(m.viewStack)-1], true
}

// drillIntoSubagent pushes a subagent onto the view stack so its child
// session's transcript replaces the orchestrator's in the viewport.
// Cards without a child session (status-only) are not drillable.
func (m *Model) drillIntoSubagent(v session.SubagentView) {
	if v.Child == nil {
		return
	}
	m.viewStack = append(m.viewStack, v)
	m.lastTranscriptHash = 0
	m.viewportFollow = true
}

// popDrill pops one level of subagent drill-down, returning false when the
// viewport was already showing the orchestrator's transcript.
func (m *Model) popDrill() bool {
	if len(m.viewStack) == 0 {
		return false
	}
	m.viewStack = m.viewStack[:len(m.viewStack)-1]
	m.lastTranscriptHash = 0
	return true
}

// breadcrumbRows reports the rows the drill-down breadcrumb occupies above
// the transcript: 1 while drilled into a subagent, 0 otherwise. It is a
// row of the left column like any other and must be in the height budget
// (see scrollHintRows).
func (m Model) breadcrumbRows() int {
	if len(m.viewStack) > 0 {
		return 1
	}
	return 0
}

func (m *Model) refreshViewport() {
	m.updateViewportHeight()
	// While drilled into a subagent, render the child session's transcript
	// (and its live blocks) in place of the orchestrator's. The parent
	// transcript is left untouched so popping back restores it as-is.
	transcriptState := m.state
	drilled, drilling := m.drilledInto()
	if drilling {
		if drilled.Child != nil {
			transcriptState = drilled.Child
		} else {
			drilling = false
		}
	}
	items := transcriptState.Transcript()
	if !drilling {
		// The completed agent.run audit event duplicates the subagent card
		// (its full result content is the verbose subagent log); the card
		// replaces it in the parent view. While drilled in, the child's own
		// audit events render normally.
		filtered := items[:0]
		for _, item := range items {
			if item.Kind == session.KindAudit && item.Audit != nil && item.Audit.ToolName == "agent.run" {
				continue
			}
			filtered = append(filtered, item)
		}
		items = filtered
	}
	inProgress := transcriptState.InProgress()
	streamLen := len(inProgress.Reasoning)
	atc, activeTool := transcriptState.ActiveToolCall()
	if activeTool {
		if atc.StartedAt != m.activeToolStartedAt {
			m.activeToolStartedAt = atc.StartedAt
			m.activeToolExpanded = false
		}
	} else {
		m.activeToolStartedAt = time.Time{}
	}
	busy := m.busy || activeTool || streamLen > 0

	todos := m.state.Todos()
	if sig := todoSignature(todos); sig != m.todosSig {
		m.todosSig = sig
		m.todosDismissed = false
	}
	queued := m.state.SteeringQueue()
	hash := transcriptHash(items, streamLen, busy, m.viewport.Width(), todos, queued, m.spinnerFrame, atc)
	if hash == m.lastTranscriptHash {
		return
	}
	m.lastTranscriptHash = hash

	blocks := make([]string, 0, len(items)+4)
	regions := make([]clickRegion, 0, len(items))
	lineCursor := 0
	// addBlock appends s to blocks (if non-empty) and, when target is
	// non-nil, records the content-line range it occupies so a later click
	// can find it (see click.go). strings.Count is exact regardless of a
	// block's internal formatting, because it counts the same "\n"
	// characters strings.Join below will actually lay out on screen.
	addBlock := func(s string, target *clickTarget) {
		if s == "" {
			return
		}
		blocks = append(blocks, s)
		n := strings.Count(s, "\n")
		if target != nil {
			regions = append(regions, clickRegion{startLine: lineCursor, endLine: lineCursor + n, target: *target})
		}
		lineCursor += n + 1 // +1 for the blank separator strings.Join inserts
	}

	if len(items) == 0 {
		addBlock(renderWelcomeBanner(m.viewport.Width()), nil)
	}
	firstTurn := true
	for _, entry := range groupTranscript(items) {
		// A separator precedes every user turn but the first, so the rule
		// always reads as "a new turn starts here" rather than as a header.
		if entry.Group == nil && isUserTurn(*entry.Item) {
			if !firstTurn {
				addBlock(renderTurnSeparator(m.viewport.Width()), nil)
			}
			firstTurn = false
		}
		if entry.Group != nil {
			key := itemKeyForGroup(entry.Group)
			expanded := m.isExpanded(key)
			s := renderToolGroup(entry.Group, expanded, m.viewport.Width())
			addBlock(s, &clickTarget{key: key})
		} else {
			key := itemKeyFor(entry.Item)
			expanded := m.isExpanded(key)
			s := renderTranscriptItem(*entry.Item, expanded, m.viewport.Width())
			var target *clickTarget
			switch entry.Item.Kind {
			case session.KindThinking, session.KindAudit:
				target = &clickTarget{key: key}
			case session.KindSubagent:
				if entry.Item.Subagent != nil && entry.Item.Subagent.Child != nil {
					target = &clickTarget{subagent: entry.Item.Subagent}
				}
			}
			addBlock(s, target)
		}
	}
	if inProgress.Active && inProgress.Reasoning != "" {
		addBlock(renderThinkingBox(inProgress.Reasoning, m.activeSpinnerFrame(session.ActivityThinking), m.viewport.Width()), nil)
	}
	if atc, ok := m.state.ActiveToolCall(); ok {
		s := renderActiveToolCall(atc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, m.activeSpinnerFrame(session.ActivityTool), m.now(), m.activeToolExpanded, m.viewport.Width())
		addBlock(s, &clickTarget{isActiveTool: true})
	}
	if err := m.state.ProviderError(); err != nil {
		addBlock(renderProviderError(err, m.viewport.Width())+
			mutedStyle().Render("Run /connect to add a provider, or /models to pick a model.")+"\n", nil)
	}
	if len(queued) > 0 {
		addBlock(renderQueuedMessages(queued, m.viewport.Width()), nil)
	}

	m.clickRegions = regions
	// Every block ends with exactly one newline; separation between blocks
	// is the caller's job — one blank line, none within a block.
	m.viewport.SetContent(strings.Join(blocks, "\n"))
	if m.viewportFollow {
		m.viewport.GotoBottom()
	}
}

// openRunPreflight opens the cast list panel for the given kind ("sdd" or
// "swarm") and waits for the user to confirm before starting the run.
func (m *Model) openRunPreflight(kind string, runner AgentRunner, goal string) {
	router := routing.NewStaticRouter(m.state.Config.RoutingConfig())
	roles := routing.SwarmCastRoles
	title := "Start swarm run?"
	meta := []string{
		"goal: " + strutil.Truncate(goal, 56, true),
		fmt.Sprintf("fix rounds: %d · token budget: %d",
			m.state.Config.Swarm.Budget.MaxFixRounds, m.state.Config.Swarm.Budget.MaxTotalTokens),
	}
	if kind == "sdd" {
		roles = routing.SDDCastRoles
		title = "Start SDD run?"
		worktree := "off"
		if m.state.Config.SDD.AutoWorktree {
			worktree = "on"
		}
		meta = []string{
			"plan: " + strutil.Truncate(goal, 56, true),
			fmt.Sprintf("fix rounds: %d · worktree: %s", m.state.Config.SDD.MaxFixRounds, worktree),
			fmt.Sprintf("verify timeout: %dms", m.state.Config.SDD.VerifyTimeoutMS),
		}
	}
	rows := make([]castlist.Row, 0, len(roles))
	for _, entry := range router.Cast(roles) {
		row := castlist.Row{Title: strings.ReplaceAll(string(entry.Role), "_", " ")}
		if entry.Err != nil {
			row.Err = strutil.Truncate(entry.Err.Error(), 44, true)
		} else {
			row.Detail = entry.Route.Preset.Provider + "/" + entry.Route.Preset.Model
			row.Badge = entry.Route.Preset.Name
		}
		rows = append(rows, row)
	}
	m.pendingRun = &pendingAgentRun{runner: runner, goal: goal}
	m.dock.Open(castlist.New(title, rows, meta))
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
	m.turnStartedAt = m.now()
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
// activity. For ActivityIdle it always returns "". During an SDD run it
// always returns "": the run panel owns the only spinner on screen, so
// transcript activity rows render their static glyph with elapsed time.
func (m *Model) activeSpinnerFrame(kind session.ActivityKind) string {
	if kind == session.ActivityIdle {
		return ""
	}
	if m.state.SDDProgress().Active {
		return ""
	}
	act := m.state.Activity()
	if m.now().Sub(act.StartedAt) < 200*time.Millisecond {
		return ""
	}
	return m.spinnerFrame
}

// turnSpinnerFrame returns the current spinner glyph for the pinned turn
// spinner, or "" when idle or within the first 200ms of the turn. The 200ms
// gate mirrors activeSpinnerFrame: it avoids a glyph flash on turns too fast
// for the user to perceive. Keyed on turnStartedAt rather than
// Activity.StartedAt so it does not reset at every phase boundary.
func (m Model) turnSpinnerFrame() string {
	if !m.busy || m.turnStartedAt.IsZero() {
		return ""
	}
	if m.now().Sub(m.turnStartedAt) < 200*time.Millisecond {
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
	// A cancelled turn's error is an artifact of the cancellation, not a
	// provider fault. It does not reliably wrap context.Canceled: a stream
	// aborted mid-flight surfaces as a wrapped transport error ("provider
	// %q: chat request failed: ..."), which used to pin a bogus error banner
	// under the transcript until the next successful turn.
	cancelled := m.cancelling
	if cancelled {
		m.state.ClearSteering()
		m.queuedCount = 0
		m.state.AddMessage(session.RoleSystem, "Agent turn cancelled.", session.ContentTypePlain)
		m.state.SetProviderError(nil)
		m.cancelling = false
	}
	m.busy = false
	m.turnStartedAt = time.Time{}
	m.agentCancel = nil
	m.refreshRailTurns()
	m.refreshRailChanged()
	if msg.err != nil && !cancelled && !errors.Is(msg.err, context.Canceled) {
		// SDD human gate: render the prompt and wait for user resolution.
		if errors.Is(msg.err, pipeline.ErrHumanGateRequired) {
			gate := m.state.SDDGate()
			if gate.Question != "" {
				m.state.AddMessage(session.RoleSystem, sddGatePrompt(gate), session.ContentTypePlain)
				m.pendingSDDGate = true
				m.state.SetActivity(session.Activity{Kind: session.ActivityIdle})
				m.updateViewportHeight()
				m.refreshViewport()
				return m, nil
			}
		}
		m.state.SetProviderError(msg.err)
		m.successPulse = false
	} else if msg.err == nil {
		// A completed turn proves the provider is reachable again — clear
		// any stale error banner from an earlier failed turn.
		m.state.SetProviderError(nil)
		m.successPulse = true
		m.successPulseAt = m.now()
	}
	m.state.SetActivity(session.Activity{Kind: session.ActivityIdle})
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

// handleWorkspaceMsg handles a workspaceMsg: the session's active root
// changed, so re-read git info for the new root immediately rather than
// waiting for the 5s tick, then re-arm the pump.
func (m Model) handleWorkspaceMsg(msg workspaceMsg) (Model, tea.Cmd) {
	if msg.activeRoot != "" {
		m.gitInfo = gitinfo.Read(msg.activeRoot)
		m.lastGitRead = m.now()
	}
	if m.workspaceEvents == nil {
		return m, nil
	}
	return m, pumpWorkspaceEvents(m.workspaceEvents)
}

// handleAgentTick handles an agentTickMsg, shared by Update and
// handleRuntimeMessage.
func (m Model) handleAgentTick(msg agentTickMsg) (Model, tea.Cmd) {
	if now := m.now(); m.state.WorkingDir != "" && now.Sub(m.lastGitRead) >= 5*time.Second {
		m.gitInfo = gitinfo.Read(m.state.Workspace().ActiveRoot)
		m.lastGitRead = now
	}
	if !m.busy && m.successPulse && m.now().Sub(m.successPulseAt) >= successPulseDuration {
		m.successPulse = false
	}
	if !m.busy && !m.successPulse {
		return m, nil
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
	case workspaceMsg:
		return m.handleWorkspaceMsg(msg)
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

		}
	}

	// Plugin-defined prompt commands replay the enter-key path: their body
	// becomes a user turn (steering when the agent is busy), with any
	// arguments appended verbatim to preserve quoting/whitespace.
	if cmd.PromptBody != "" {
		goal := cmd.PromptBody
		if len(parts) > 1 {
			rest := raw[len(parts[0]):]
			rest = strings.TrimSpace(rest)
			if rest != "" {
				goal += "\n\n" + rest
			}
		}
		if m.busy {
			m.state.PushSteering(goal)
			m.refreshViewport()
			return m, nil
		}
		if m.runner == nil {
			m.state.AddMessage(session.RoleSystem, "Agent runner is not available.", session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		}
		return m.startAgentRun(m.runner, goal)
	}

	if cmd.Handler != nil {
		res := cmd.Handler(m.state, args)
		if res.Doc != nil {
			m.openDocPanel(res.Doc)
		} else if res.Text != "" {
			m.state.AddMessage(session.RoleSystem, res.Text, session.ContentTypePlain)
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
		CfgPath:    projectConfigPath(m.state.WorkingDir),
		DataDir:    m.modelCacheDir,
	})
	m.connectModel.SetSize(m.leftWidth, m.height)
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
		AllProviders:     true,
		ScopedProvider:   names[0],
		DataDir:          m.modelCacheDir,
	})
	m.connectModel.SetSize(m.leftWidth, m.height)
	m.dock.Open(connect.Panel{Model: m.connectModel})
	return m.probeProviders(names)
}

// probeProviders dispatches a probe for each named provider that has no
// cached entry and whose endpoint is permitted by the remote-provider
// gate. Used by openModels and by the manual refresh key.
func (m *Model) probeProviders(names []string) tea.Cmd {
	var cmds []tea.Cmd
	for _, n := range names {
		if cached, ok := m.discovered[n]; ok && len(cached) > 0 {
			continue
		}
		pc := m.state.Config.Providers[n]
		if !probe.IsLocalhost(pc.BaseURL) && !m.state.Config.Privacy.RemoteProvidersAllowed {
			continue
		}
		cmds = append(cmds, probe.Provider("models", n, pc, m.modelCacheDir, m.state.Config.Privacy.RemoteLimitDiscovery))
	}
	return tea.Batch(cmds...)
}

// persistDiscovered writes one provider's models through to the on-disk
// cache. Failures are silent: the cache is an optimization, and a session
// that cannot write it behaves exactly as it did before caching existed.
// A failed probe must not evict a good cached entry, so this is only
// called on the success path of probe.ResultMsg.
func (m *Model) persistDiscovered(name string, models []schema.ModelInfo) {
	if m.modelCacheDir == "" || len(models) == 0 {
		return
	}
	pc, ok := m.state.Config.Providers[name]
	if !ok {
		return
	}
	c := modelcache.Load(m.modelCacheDir)
	c.Providers[name] = modelcache.Entry{
		ConfigHash: modelcache.HashProvider(pc),
		Models:     models,
		FetchedAt:  time.Now(),
	}
	_ = modelcache.Save(m.modelCacheDir, c)
}

// evictDiscovered removes one provider's entry from the on-disk cache.
// Used by the manual refresh key — the in-session map is already cleared
// by the caller. Failures are silent for the same reason as persist.
func (m *Model) evictDiscovered(name string) {
	if m.modelCacheDir == "" {
		return
	}
	c := modelcache.Load(m.modelCacheDir)
	if _, ok := c.Providers[name]; !ok {
		return
	}
	delete(c.Providers, name)
	_ = modelcache.Save(m.modelCacheDir, c)
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

// openDocPanel opens a structured command result as a docked doc panel.
func (m *Model) openDocPanel(doc *commands.Doc) {
	m.dock.Open(docpanel.New(*doc, m.state))
	m.refreshViewport()
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

// ResumeSession returns the session ID the user chose to resume, or "" if
// the program ended normally. It is read by the app.Run program runner.
func (m Model) ResumeSession() string { return m.resumeSession }

// beginResume starts a controlled shutdown so app.Run can restart this
// process with the requested existing session.
func (m *Model) beginResume(id string) (tea.Model, tea.Cmd) {
	id = strings.TrimSpace(id)
	if id == "" {
		m.refreshViewport()
		return m, nil
	}
	m.resumeSession = id
	m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Resuming session %s...", id), session.ContentTypePlain)
	return m, m.beginShutdown()
}

// openSessionPicker opens a picker listing previous sessions for this
// project. The picked session ID is passed to beginResume.
func (m *Model) openSessionPicker(prefilter string) {
	database := m.state.DB()
	if database == nil {
		m.state.AddMessage(session.RoleSystem, "Session list is not available (no database).", session.ContentTypePlain)
		return
	}
	sessions, _, err := database.ListSessions(context.Background(), m.state.WorkingDir, "", 100)
	if err != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Could not list sessions: %v", err), session.ContentTypePlain)
		return
	}
	items := make([]picker.Item, 0, len(sessions))
	for _, s := range sessions {
		label := s.Title
		if label == "" {
			label = s.SessionID
		}
		detail := s.UpdatedAt.Format("2006-01-02 15:04") + fmt.Sprintf(" · %d messages", s.MessageCount)
		badge := ""
		if s.SessionID == m.state.SessionID() {
			badge = "● now"
		}
		items = append(items, picker.Item{
			Label:  label,
			Detail: detail,
			Badge:  badge,
			Value:  s.SessionID,
		})
	}
	if len(items) == 0 {
		m.state.AddMessage(session.RoleSystem, "No previous sessions found for this project.", session.ContentTypePlain)
		return
	}
	m.openPicker("sessions", "Resume session", "pick a previous conversation", items, prefilter)
}

// setMode applies an interaction mode for the next turn. Shared by the
// /plan, /default, /edit, /copilot, /auto, /mode commands and the
// Tab/Shift+Tab mode-cycling hotkeys. The change is session-scoped (never
// persisted to disk here), but the in-memory session config is kept in
// sync so the /settings browser — built from m.state.Config — shows the
// current mode instead of a stale one.
func (m *Model) setMode(mode string) {
	class := "edit"
	if mode == "plan" || mode == "default" {
		class = "question"
	}
	if m.runner != nil {
		m.runner.SetForceClass(class)
		m.runner.SetApprovalMode(policy.ApprovalMode(mode))
	}
	m.approvalMode = policy.ApprovalMode(mode)
	m.state.Config.Agent.ApprovalMode = mode
}

// modeOrder is the canonical cycle order used by Tab/Shift+Tab.
var modeOrder = []policy.ApprovalMode{policy.ModePlan, policy.ModeDefault, policy.ModeEdit, policy.ModeCopilot, policy.ModeAuto}

// modeSwitchMessage maps each mode value to the exact confirmation
// message used by the /plan, /default, /edit, /copilot, /auto command
// handlers, so the transcript looks identical whether the user pressed
// Tab or typed /plan.
var modeSwitchMessage = map[string]string{
	"plan":    "Switched to Plan mode. Agent will produce a numbered plan, then stop.",
	"default": "Switched to Default mode. Agent is read-only; it will request elevation to edit.",
	"edit":    "Switched to Edit mode. Each file change requires approval.",
	"copilot": "Switched to Copilot mode. Changes auto-approve; agent may ask on ambiguity.",
	"auto":    "Switched to Auto mode. Fully autonomous; no questions asked.",
}

// cycleMode advances (forward=true) or reverses the interaction mode,
// wrapping around. It applies the result via setMode and emits the same
// confirmation message the /<mode> commands use.
func (m *Model) cycleMode(forward bool) {
	cur := string(m.approvalMode)
	idx := slices.IndexFunc(modeOrder, func(m policy.ApprovalMode) bool { return string(m) == cur })
	if idx < 0 {
		idx = 0
	}
	step := 1
	if !forward {
		step = -1
	}
	next := string(modeOrder[(idx+step+len(modeOrder))%len(modeOrder)])
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
	items = append(items, picker.Item{Label: "Custom plan path...", Detail: "type or paste a path", Value: sddCustomPlanPathValue})
	p := picker.New("Pick a plan", "SDD workflow", items)
	p.SetAllowCustom(true)
	m.dock.Open(p)
	m.pickerCommand = "sdd-plan"
}

// openSDDCustomPlanPathPicker opens a dedicated picker for typing or pasting
// a plan path that was not auto-detected in the SDD plans directory.
func (m *Model) openSDDCustomPlanPathPicker() {
	p := picker.New("Custom plan path", "type or paste a path below", []picker.Item{})
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
	if msg.EnabledRemote {
		newCfg.Privacy.RemoteProvidersAllowed = true
	}
	// Selecting a model synthesizes a preset plus a profile binding every
	// role to it. The router resolves through profiles only; there is no
	// legacy pair and no implicit "empty default means fall through".
	presetName := msg.Provider + "/" + msg.Model
	if newCfg.Models.Presets == nil {
		newCfg.Models.Presets = map[string]routing.ModelPreset{}
	} else {
		presets := make(map[string]routing.ModelPreset, len(newCfg.Models.Presets)+1)
		for k, v := range newCfg.Models.Presets {
			presets[k] = v
		}
		newCfg.Models.Presets = presets
	}
	preset := routing.ModelPreset{
		Name:            presetName,
		Provider:        msg.Provider,
		Model:           msg.Model,
		ContextWindow:   msg.ContextWindow,
		MaxOutputTokens: msg.MaxOutputTokens,
	}
	// Re-picking a model whose limits came back unknown (zero) must not
	// erase figures saved earlier — zero means "unknown", not "reset".
	if existing, ok := newCfg.Models.Presets[presetName]; ok {
		if preset.ContextWindow == 0 {
			preset.ContextWindow = existing.ContextWindow
		}
		if preset.MaxOutputTokens == 0 {
			preset.MaxOutputTokens = existing.MaxOutputTokens
		}
	}
	newCfg.Models.Presets[presetName] = preset

	if newCfg.AgentProfiles == nil {
		newCfg.AgentProfiles = map[string]routing.AgentProfile{}
	} else {
		profiles := make(map[string]routing.AgentProfile, len(newCfg.AgentProfiles)+1)
		for k, v := range newCfg.AgentProfiles {
			profiles[k] = v
		}
		newCfg.AgentProfiles = profiles
	}
	newCfg.AgentProfiles[singleModelProfileName] = routing.SingleModelProfile(singleModelProfileName, presetName)
	newCfg.Profile.Default = singleModelProfileName
	newCfg.Agent.Provider = ""
	newCfg.Agent.Model = ""

	// Credentials never go to project config. Write the key to the user
	// config first; if that fails, configure nothing rather than leaving a
	// provider entry pointing at a key that was never saved. The in-memory
	// config keeps the key so the reloaded runtime can use it;
	// SaveProjectConfig strips it from what reaches the project file.
	if msg.ProviderCfg.APIKey != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			m.state.AddMessage(session.RoleSystem,
				fmt.Sprintf("✗ Failed to locate home directory: %v", err), session.ContentTypePlain)
			return
		}
		if err := config.SaveUserConfigProviderAPIKey(
			config.UserConfigPath(home), msg.Provider, msg.ProviderCfg.APIKey); err != nil {
			m.state.AddMessage(session.RoleSystem,
				fmt.Sprintf("✗ Failed to save API key: %v", err), session.ContentTypePlain)
			return
		}
	}

	saveErr, reloadErr := m.persistAndReload(newCfg)
	switch {
	case saveErr != nil:
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✗ Failed to save model: %v", saveErr), session.ContentTypePlain)
	case reloadErr != nil:
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✗ Failed to switch model: %v", reloadErr), session.ContentTypePlain)
	default:
		// The banner itself tells the user to run /connect; a successful
		// switch must clear it even when no runtime reloader is wired.
		m.state.SetProviderError(nil)
		m.state.AddMessage(session.RoleSystem,
			fmt.Sprintf("✓ Switched to model: %s (%s)", msg.Model, msg.Provider), session.ContentTypePlain)
	}
}

// switchModelPreset applies a model switch by binding every role to the given
// preset through a single-model profile. The change is persisted before
// the runtime is asked to reload, matching the /set and settings.ChangedMsg
// contracts.
func (m *Model) switchModelPreset(presetName string) {
	newCfg := m.state.Config
	preset, ok := newCfg.Models.Presets[presetName]
	if !ok {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✗ Unknown preset: %s", presetName), session.ContentTypePlain)
		return
	}
	if newCfg.AgentProfiles == nil {
		newCfg.AgentProfiles = map[string]routing.AgentProfile{}
	} else {
		profiles := make(map[string]routing.AgentProfile, len(newCfg.AgentProfiles)+1)
		for k, v := range newCfg.AgentProfiles {
			profiles[k] = v
		}
		newCfg.AgentProfiles = profiles
	}
	newCfg.AgentProfiles[singleModelProfileName] = routing.SingleModelProfile(singleModelProfileName, presetName)
	newCfg.Profile.Default = singleModelProfileName

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

// resolveModelArg maps a /models argument to a preset name: first an exact
// preset-name match, then a bare model ID (e.g. "glm-5.2") against each
// preset's Model field. Model-ID matches scan presets in sorted order so
// the result is deterministic when several presets share a model.
func (m *Model) resolveModelArg(arg string) (string, bool) {
	presets := m.state.Config.Models.Presets
	if _, ok := presets[arg]; ok {
		return arg, true
	}
	for _, name := range m.sortedPresetNames() {
		if presets[name].Model == arg {
			return name, true
		}
	}
	return "", false
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

// modePickerItems builds picker items for the interaction modes.
// The current mode carries a "● now" badge.
func (m *Model) modePickerItems() []picker.Item {
	current := string(m.approvalMode)
	badge := func(v string) string {
		if v == current {
			return "● now"
		}
		return ""
	}
	return []picker.Item{
		{Label: "Plan", Detail: "read-only, forced plan", Badge: badge("plan"), Value: "plan"},
		{Label: "Default", Detail: "read-only, request elevation", Badge: badge("default"), Value: "default"},
		{Label: "Edit", Detail: "plan + confirm each", Badge: badge("edit"), Value: "edit"},
		{Label: "Copilot", Detail: "auto-approve, may ask", Badge: badge("copilot"), Value: "copilot"},
		{Label: "Auto", Detail: "fully autonomous", Badge: badge("auto"), Value: "auto"},
		{Label: "SDD", Detail: "plan-driven multi-task", Badge: badge("sdd"), Value: "sdd"},
	}
}

func visibleRunes(s string) int {
	return ansi.StringWidth(s)
}

var activeTheme theme.Theme

// Package-level shortcuts for the active theme's slots, named for what they
// mean rather than what they look like.
//
// There used to be two parallel sets here — an appearance-named one (coral,
// gold, teal, orange, mauve) alongside this semantic one — aliasing the same
// slots, so coralColor and accentColor were literally the same value. That let
// call sites pick colors by appearance, which is how a palette drifts, and it
// reintroduced one layer down exactly what the theme package's doc forbids.
var (
	accentColor   color.Color // AccentPrimary
	violetColor   color.Color // AccentSecondary
	tertiaryColor color.Color // AccentTertiary
	borderColor   color.Color // BorderMuted
	userColor     color.Color // UserPrompt
	dimColor      color.Color // FGMuted
	successColor  color.Color // StatusSuccess
	warningColor  color.Color // StatusWarning
	errorColor    color.Color // StatusError

	dimSeparator = " · "
)

func loadTheme(tui config.TUIConfig) {
	activeTheme = theme.LoadWithConfig(tui.Theme, tui.Mode, theme.PaletteOverrides(tui.Palette))

	accentColor = activeTheme.AccentPrimary
	violetColor = activeTheme.AccentSecondary
	tertiaryColor = activeTheme.AccentTertiary
	borderColor = activeTheme.BorderMuted
	userColor = activeTheme.UserPrompt
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

// thinkingLineStyle is muted only. Italic used to be layered on top, but
// terminals render SGR 3 as reverse video or drop it entirely, and muted
// italic was the least legible combination in the UI. The ⚙ glyph already
// marks the line as reasoning.
func thinkingLineStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
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
func urlStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(theme.Current().FGDefault) }

func transcriptHash(items []session.TranscriptItem, streamLen int, busy bool, width int, todos []native.TodoItem, queued []string, spinnerFrame string, atc session.ActiveToolCall) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "c=%d|w=%d|f=%d|", len(items), width, flags(streamLen, busy, len(todos), len(queued)))
	// Live state: without the spinner frame and the active tool call the
	// early-return in refreshViewport freezes the ▸ row for the whole
	// duration of a long tool call (e.g. agent.run).
	fmt.Fprintf(h, "spin=%s|atc=%s|%s|%d|", spinnerFrame, atc.Name, atc.Args, atc.StartedAt.UnixNano())
	for _, item := range items {
		fmt.Fprintf(h, "%d|%d|", item.Kind, item.Timestamp.UnixNano())
		if item.Message != nil {
			fmt.Fprintf(h, "%s|%s|%s\x00", item.Message.Role, item.Message.ContentType, item.Message.Content)
		}
		if item.Subagent != nil {
			// Subagent cards are live while the child runs: status, tool-call
			// count, and summary must bust the viewport cache or the card
			// freezes at registration time.
			fmt.Fprintf(h, "sub=%d|%v|%s|%d|%d|%s\x00", item.Subagent.ID, item.Subagent.Status, item.Subagent.Label, item.Subagent.ToolCalls, item.Subagent.EndedAt.UnixNano(), item.Subagent.Summary)
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
