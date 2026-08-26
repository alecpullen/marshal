package tui

import (
	"context"
	"encoding/json"
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

	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/agents"
	"marshal/internal/app/tui/castlist"
	"marshal/internal/app/tui/changedfiles"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/connect"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/app/tui/doctorpanel"
	"marshal/internal/app/tui/gatepanel"
	"marshal/internal/app/tui/gitinfo"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/modeloptions"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/presetflow"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/sddreview"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/sidepanel"
	"marshal/internal/app/tui/theme"
	"marshal/internal/app/tui/trustpanel"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/jsonextract"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/permissions"
	"marshal/internal/pipeline"
	"marshal/internal/pubsub"
	"marshal/internal/sddauthor"
	"marshal/internal/sddplans"
	"marshal/internal/skills"
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

// SwarmOverrideFactory builds a per-run swarm.RunnerFactory with the given
// role→preset overrides applied. It is provided by app.go (which has
// access to the routedProviderResolver) and wired via
// WithSwarmOverrideFactory. The TUI calls it when the castlist confirms
// with overrides, and the result is installed on the shared orchestrator
// via SetRunnerFactory. RestoreRunner undoes the wrap when the run ends.
type SwarmOverrideFactory func(overrides map[routing.AgentRole]string) swarm.RunnerFactory

// RoleOverrideRunner is an optional interface for runners that accept
// per-run role→preset overrides from the castlist. The overrides apply to
// a copy of the routing config for this run only and are never persisted.
// RestoreRunner undoes the override after the run ends (success, failure,
// or cancel) so a leaked override does not silently apply to every later
// run in the session.
type RoleOverrideRunner interface {
	SetRunnerFactory(factory swarm.RunnerFactory)
	RestoreRunner()
}

// SessionSwapResult carries the rebuilt runtime pieces for a /new or /clear
// swap so the TUI can re-point every model field that depends on the current
// session.
type SessionSwapResult struct {
	State                *session.State
	Runner               AgentRunner
	SwarmRunner          AgentRunner
	PipelineFactory      func(planPath string, overrides map[routing.AgentRole]string) AgentRunner
	SwarmOverrideFactory SwarmOverrideFactory
	PlanAuthorFactory    PlanAuthorFactory
	ToolRegistry         *registry.Registry
	ReviewDispatcher     func(ctx context.Context, focus, model, reviewRange string) error
}

// SessionSwapper is the runtime-facing seam the TUI uses to request a new
// session without importing the app package (avoiding an import cycle).
type SessionSwapper interface {
	NewSession(name string) (SessionSwapResult, error)
}

// SessionSwapperFunc lets app.go pass a closure directly as a SessionSwapper.
type SessionSwapperFunc func(name string) (SessionSwapResult, error)

func (f SessionSwapperFunc) NewSession(name string) (SessionSwapResult, error) { return f(name) }

// CustomAgentRunnerFactory builds a one-shot AgentRunner for a named custom
// agent. It is wired from app.go via WithCustomAgentRunnerFactory and used
// by buildCustomAgentRunner to dispatch Run-now. Returns nil when the agent
// name is unknown or the factory is not available.
type CustomAgentRunnerFactory func(agentName string) (AgentRunner, error)

// SubagentRunnerFactory builds a fresh child *agent.Runner bound to a fresh
// child session. It mirrors agent.SubagentRunnerFactory without importing
// internal/agent, keeping the TUI a rendering layer. Used by the /sdd
// offer-to-fill flow to run a one-shot proposal task.
type SubagentRunnerFactory func(agentName string) (AgentRunner, error)

const (
	minTerminalWidth  = 80
	minTerminalHeight = 24

	successPulseDuration = 2 * time.Second
	// noticeBannerDuration bounds how long a notice banner stays up before
	// auto-dismissing, so a stale failure does not sit under the transcript
	// forever. The store stamps SetAt at write time, so the TTL applies to
	// every notice regardless of which layer produced it. Same-category
	// success and esc dismiss it earlier.
	noticeBannerDuration = 30 * time.Second

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

	// sddScaffoldPlanValue is the picker value emitted by the "Write a starter
	// plan" row. It is a sentinel, never a path: the handler writes a template
	// into the plans directory rather than trying to run it.
	sddScaffoldPlanValue = "__sdd_scaffold_plan__"

	// maxRegionOffset bounds how far back a live region can be scrolled.
	// liveregion.Render clamps for display, so an offset past the head is
	// harmless to render — this only stops the stored value running away so
	// far that scrolling back down takes hundreds of wheel events.
	maxRegionOffset = 40
)

type Model struct {
	state           *session.State
	input           textarea.Model
	editingCommand  bool
	runner          AgentRunner
	swarmRunner     AgentRunner
	pipelineFactory func(planPath string, overrides map[routing.AgentRole]string) AgentRunner
	// swarmOverrideFactory builds a per-run swarm.RunnerFactory with
	// role→preset overrides applied. Nil when the runtime has no provider
	// or when the swarm runner does not support overrides.
	swarmOverrideFactory SwarmOverrideFactory
	// planAuthorFactory builds a scoped SDD plan-authoring runner for one
	// request. Nil when the runtime has no provider.
	planAuthorFactory PlanAuthorFactory
	ctx               context.Context
	// lspCtx is the context for background LSP reference lookups. It is
	// derived from context.Background() rather than m.ctx because m.ctx is
	// the program's cancellable turn context and can be nil or replaced on
	// a provider-build failure; the LSP queries must outlive a turn but be
	// cancelled on shutdown so a lingering lookup never leaks past exit.
	// lspCancel is called from beginShutdown.
	lspCtx         context.Context
	lspCancel      context.CancelFunc
	busy           bool
	configReloader ConfigReloader
	// runnerSource exposes the runtime's current runner after a config
	// reload. Used to recover from a startup provider-build failure, where
	// the TUI was constructed without a runner: the first successful reload
	// rebuilds one, and adoptRunner installs it here.
	runnerSource       func() (context.Context, AgentRunner)
	sessionSwapper     SessionSwapper
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
	dataDir       string
	workDir       string
	skillIndex    *skills.Index
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
	// completionSuppressed is set by Esc and cleared when the input is
	// cleared, on accept, or on submit/reset. This keeps the popup dismissed
	// while editing the same trigger after Esc.
	cmdPopup             *completionPopup
	filePopup            *completionPopup
	setPopup             *completionPopup
	fileIndex            []completionItem
	fileIndexLoaded      bool
	lastInputForPopups   string
	completionSuppressed bool
	// laneCursor is the keyboard-selected row in the agents lane (F6).
	// Up/Down move it while the input is empty and the lane is non-empty;
	// Enter drills into the selected subagent. laneCursorActive is set
	// only once the user explicitly navigates the lane, so a blank Enter
	// keeps its existing steering-drain behavior until then.
	laneCursor       int
	laneCursorActive bool
	// cmdArgMode arms argument completion right after a command is
	// accepted from the popup. While armed and the input still carries
	// the accepted "/<cmd> " prefix, commandTrigger keeps firing so
	// argument completions stay available. Anything that drops the
	// prefix — or submit, history recall, an empty input — disarms it.
	// Ordinary words like "run tests" never trigger anything.
	cmdArgMode   bool
	cmdArgPrefix string
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

	// suggestion is the active ghost-text next-prompt suggestion shown in
	// the input box after a turn completes ("" = none). suggestionDismissed
	// is set by Esc so a dismissed suggestion is not resurrected until the
	// next turn completes. Both are cleared when a new turn starts.
	suggestion          string
	suggestionDismissed bool
	// suggestionProvider, when set, is the Phase 2 LLM fallback seam: a
	// function that derives a suggestion from the last assistant message
	// when the deterministic rules produce none. It runs as a background
	// tea.Cmd so it never blocks the UI. suggestionGen is a generation
	// counter bumped on every keystroke and turn start; a late result from
	// an older generation is discarded.
	suggestionProvider func(ctx context.Context, lastMsg string) (string, error)
	suggestionGen      int

	// F19 broker pump. jobBroker is the F5 job-event broker; the pump
	// cmd returned from Init (and re-armed from Update on each
	// jobCountMsg) bridges it into jobCountMsg values. jobCount is the
	// cached value the status line renders. When jobBroker is nil
	// (tests, fallback), the status line reads m.state.RunningJobsCount()
	// as the polled fallback.
	jobBroker *pubsub.Broker[native.JobEvent]
	jobEvents <-chan pubsub.Event[native.JobEvent]
	jobCount  int
	// jobs is the cached JobInfo snapshot from the latest JobEvent, used by
	// the job lane above the input. Nil/empty when no broker is wired.
	jobs []native.JobInfo

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

	// subagentBroker, when non-nil, delivers SubagentEvents so the TUI
	// re-renders subagent cards as they are registered or change status.
	subagentBroker *pubsub.Broker[session.SubagentEvent]
	subagentEvents <-chan pubsub.Event[session.SubagentEvent]

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
	// railTotals is the session-scoped usage aggregate the rail footer
	// reports. Refreshed alongside railTurns.
	railTotals db.UsageTotals
	// railBaseRef is the commit the changed-files section diffs against,
	// rebased when the workspace changes and after each completed turn
	// (see handleWorkspaceMsg/handleAgentFinished).
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
	// regionOffset holds the per-region body scroll offset for bounded live
	// regions (see internal/app/tui/liveregion), keyed the same way
	// itemExpanded is. Rebuilt-and-pruned on every refreshViewport, so a
	// finished region's entry does not leak.
	regionOffset map[itemKey]int
	// regionRows is the high-water mark for each bounded live region: the
	// tallest it has rendered so far. liveregion.Render is pure and cannot
	// remember, and the body genuinely shrinks (SubagentActivityTail
	// switches between streamed reasoning and audit summaries), so without
	// this the card oscillates. Pruned with regionOffset.
	regionRows map[itemKey]int
	// refFinder resolves blast radius; nil when LSP is unavailable.
	refFinder ReferenceFinder
	// callers caches reference lookups per audit item. A present-but-empty
	// entry is a negative result and must not be re-queried.
	callers      map[itemKey][]string
	callersAsked map[itemKey]bool
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

	// doctorFixProvider is non-empty while the /doctor panel is in key-input
	// sub-mode: the input prompt asks for an API key for this provider.
	// savedInputPlaceholder restores the input placeholder when the sub-mode
	// exits.
	doctorFixProvider     string
	savedInputPlaceholder string

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

	// subagentFactory builds a fresh child *agent.Runner for a one-shot
	// proposal task. Wired from app.go; used by the /sdd offer-to-fill flow
	// to propose build/test commands when none can be detected.
	subagentFactory SubagentRunnerFactory

	// pastes holds condensed paste attachments for the current input.
	// They are expanded into fenced blocks on submit and cleared whenever
	// the input is reset.
	pastes []pasteAttachment

	// reviewDispatcher runs a reviewer subagent for the /review command.
	// Wired from app.go; if nil, /review reports that it is unavailable.
	reviewDispatcher func(ctx context.Context, focus, model, reviewRange string) error

	// pendingModelOptions holds a config candidate saved while the runner
	// or background jobs are active. It is flushed when the model becomes
	// idle and applies via the configured reloader.
	pendingModelOptions *pendingModelOptionsState

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
	// restoreRunner is the cleanup function for a per-run role override
	// applied to a shared runner (swarm). It is called when the run ends
	// (success, failure, or cancel) so a leaked override does not silently
	// apply to every later run in the session. nil when no override is active.
	restoreRunner func()

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
	// kind is "sdd" or "swarm", set by openRunPreflight so the StartMsg
	// handler knows which override path to take.
	kind string
	// verifyBuild/verifyTest hold commands proposed by the offer-to-fill
	// flow. When set, they are persisted to project config on confirm and
	// used for this run.
	verifyBuild string
	verifyTest  string
	// modelOverrides is the castlist's per-run role→preset map. It is
	// applied to a copy of the routing config for this run only and is
	// never written to disk.
	modelOverrides map[routing.AgentRole]string
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

// WithSessionSwapper wires the runtime-facing seam that /new and /clear use
// to swap in a brand-new session. Nil (the default) makes those commands
// report that a new session is unavailable.
func WithSessionSwapper(swapper SessionSwapper) Option {
	return func(m *Model) { m.sessionSwapper = swapper }
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

// WithDataDir sets the application data directory. The model-options panel
// reads the on-disk limits cache from it when resolving per-model
// capability support.
func WithDataDir(dataDir string) Option {
	return func(m *Model) { m.dataDir = dataDir }
}

func WithWorkingDir(workDir string) Option {
	return func(m *Model) {
		m.workDir = workDir
	}
}

// WithSkillIndex hands the runtime skill index to the TUI so panels (the
// skills panel in particular) can load skills the runtime has already
// loaded, rather than re-reading them from disk.
func WithSkillIndex(idx *skills.Index) Option {
	return func(m *Model) {
		m.skillIndex = idx
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
func WithPipelineFactory(ctx context.Context, factory func(planPath string, overrides map[routing.AgentRole]string) AgentRunner) Option {
	return func(m *Model) {
		m.ctx = ctx
		m.pipelineFactory = factory
	}
}

// WithSwarmOverrideFactory wires the function that builds a per-run
// swarm.RunnerFactory with role→preset overrides applied. It is provided
// by app.go (which has access to the routedProviderResolver) and called
// when the castlist confirms with overrides.
func WithSwarmOverrideFactory(factory SwarmOverrideFactory) Option {
	return func(m *Model) { m.swarmOverrideFactory = factory }
}

// PlanAuthorFactory builds a scoped SDD plan-authoring runner for one
// request. It is an alias for sddauthor.Factory so the TUI does not depend
// on the authoring package's construction details.
type PlanAuthorFactory = sddauthor.Factory

// WithPlanAuthorFactory configures the TUI to build a scoped SDD
// plan-authoring runner on demand when /sdd new is submitted.
func WithPlanAuthorFactory(ctx context.Context, factory PlanAuthorFactory) Option {
	return func(m *Model) {
		m.ctx = ctx
		m.planAuthorFactory = factory
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

// WithSubagentBroker wires the broker carrying session.SubagentEvents so
// subagent cards re-render on registration/status changes.
func WithSubagentBroker(ctx context.Context, broker *pubsub.Broker[session.SubagentEvent]) Option {
	return func(m *Model) {
		m.ctx = ctx
		m.subagentBroker = broker
	}
}

// WithCustomAgentRunnerFactory wires a factory that builds a one-shot
// AgentRunner for a named custom agent. Used by the /agents Run-now action.
func WithCustomAgentRunnerFactory(fn CustomAgentRunnerFactory) Option {
	return func(m *Model) {
		m.customAgentFactory = fn
	}
}

// WithSubagentFactory wires a factory that builds a one-shot AgentRunner for
// a proposal task. Used by the /sdd offer-to-fill flow to propose build/test
// commands when none can be detected.
func WithSubagentFactory(fn SubagentRunnerFactory) Option {
	return func(m *Model) {
		m.subagentFactory = fn
	}
}

// WithReviewDispatcher wires the /review command to a dispatcher that runs
// a reviewer subagent. The dispatcher receives the user's optional focus
// argument and runs until the subagent finishes.
func WithReviewDispatcher(fn func(ctx context.Context, focus, model, reviewRange string) error) Option {
	return func(m *Model) {
		m.reviewDispatcher = fn
	}
}

// WithSuggestionProvider wires the Phase 2 LLM fallback for next-prompt
// suggestions. The provider derives a suggestion from the last assistant
// message when the deterministic rules produce none. It runs as a background
// tea.Cmd so it never blocks the UI; stale results are discarded via a
// generation counter. Nil (the default) disables the LLM fallback even when
// [tui] suggestions = "llm".
func WithSuggestionProvider(fn func(ctx context.Context, lastMsg string) (string, error)) Option {
	return func(m *Model) {
		m.suggestionProvider = fn
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
	m.completionSuppressed = false
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
	m.state.ClearNotice(session.NoticeProvider)
	m.state.ClearNotice(session.NoticeConfig)
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
		settings.WithDataDir(m.modelCacheDir),
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

// inputPromptWidth is the number of cells the input textarea's prompt
// function reserves on every display row: "❯ " on row 0 and two spaces on
// wrapped rows, both visible width 2. The suggestion ghost adds it to the
// cursor's column offset within a row to get the column in the rendered
// frame (see ghostPosition in view.go).
const inputPromptWidth = 2

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
	input.SetPromptFunc(inputPromptWidth, func(info textarea.PromptInfo) string {
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

	// The LSP reference-lookup context is independent of m.ctx (the program's
	// cancellable turn context, which can be nil or replaced on a
	// provider-build failure). It is cancelled on shutdown so a lingering
	// lookup never leaks past exit.
	m.lspCtx, m.lspCancel = context.WithCancel(context.Background())

	// bubbles maps wheel-left/right to horizontal panning; step 0 disables
	// it at the viewport level too (the wheel events are already dropped in
	// Update, this covers any other path that forwards mouse messages).
	m.viewport.SetHorizontalStep(0)

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
	if m.subagentBroker != nil && m.subagentEvents == nil {
		m.subagentEvents = m.subagentBroker.Subscribe(m.ctx)
	}

	// Eagerly build inline approval/question forms if the session already
	// has a pending request (parent or subagent), so the first render shows
	// the huh surface instead of the legacy fallback panels.
	if tc, _ := m.pendingApprovalDisplay(); tc != nil {
		m.approvalModel = newApprovalModel(tc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, m.state.HasBackup(), max(m.leftWidth-4, 30))
	}
	if q := m.state.PendingQuestion(); q != nil {
		m.questionModel = newQuestionModel(q, max(m.leftWidth-4, 30))
	}

	m.gitInfo = gitinfo.Read(state.Workspace().ActiveRoot)
	m.lastGitRead = m.now()
	m.railBaseRef = gitinfo.HeadSHA(state.Workspace().ActiveRoot)

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
	if m.subagentEvents != nil {
		cmds = append(cmds, pumpSubagentEvents(m.subagentEvents))
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
	m.viewport.SetHeight(max(height-transcriptFrameRows-m.scrollHintRows()-m.breadcrumbRows()-m.todoPanelRows()-m.runPanelRows()-m.liveStripRows()-m.jobLaneRows()-m.agentLaneRows()-m.dockRows()-m.turnSpinnerRows()-m.inputAreaRows()-statusLineRows, 1))
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
	if rows, err := database.RecentTurnMetrics(m.memoryProject, 24); err == nil {
		m.railTurns = rows
	}
	if totals, err := database.SessionUsage(m.memoryProject, m.state.SessionID()); err == nil {
		m.railTotals = totals
	}
}

// refreshRailChanged reloads the changed-files cache. Shells out to git,
// so it runs on turn boundaries only — never from View.
func (m *Model) refreshRailChanged() {
	if !m.railEnabled() {
		return
	}
	m.railChanged = changedfiles.Read(m.state.Workspace().ActiveRoot, m.railBaseRef)
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
		sidepanel.WorkingSetSection{},
		sidepanel.ToolsSection{},
		sidepanel.RulesSection{},
		sidepanel.RepoSection{},
		sidepanel.SkillsSection{},
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
		Totals:  m.railTotals,
		Changed: m.railChanged,
		Pack:    m.state.ContextPack(),
		Audit:   m.state.AuditLog(),
		Rules:   m.state.SessionRules(),
		Skills:  m.state.ActiveSkills(),
		Swarm:   m.state.SwarmProgress(),
		SDD:     m.state.SDDProgress(),
		Spinner: m.turnSpinnerFrame(),
		Now:     m.now(),
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Clear interruptArmed for any non-keypress message so a stale flag
	// doesn't cause the next Ctrl+C to quit instead of interrupt. Only an
	// immediate second Ctrl+C (no intervening messages) should quit.
	//
	// The two internal tick messages are exempt: while a turn is busy they
	// fire every 80-150ms, so clearing on them would wipe the armed flag
	// almost immediately and the documented "Press Ctrl+C again to quit"
	// second press would re-interrupt instead of quitting.
	if _, ok := msg.(tea.KeyPressMsg); !ok {
		switch msg.(type) {
		case agentTickMsg, spinnerTickMsg:
			// Background churn; do not clear the armed quit.
		default:
			m.interruptArmed = false
		}
	}
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
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if k.String() == "ctrl+c" {
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
		// Any other keypress clears the armed quit, so Ctrl+C never quits
		// on a press the user has mentally separated from the interrupt.
		m.interruptArmed = false
	}

	// WindowSizeMsg must always resize the underlying layout (and the
	// settings/memory overlays) regardless of which overlay is open.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		wasRailEnabled := m.railEnabled()
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
		// A narrow→wide resize newly enables the rail; refreshRailChanged
		// is gated on railEnabled(), so without this the changed section
		// would stay empty until the next turn/workspace event. Fires at
		// most once per disabled→enabled transition.
		if !wasRailEnabled && m.railEnabled() {
			m.refreshRailChanged()
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
	case modeloptions.ChangedMsg:
		cmd := m.handleModelOptionsChanged(msg)
		m.refreshViewport()
		return m, cmd
	case modeloptions.ClosedMsg:
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
	case doctorpanel.ClosedMsg:
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	case doctorpanel.FixMsg:
		m.dock.CloseNow()
		m.doctorFixProvider = msg.Provider
		m.savedInputPlaceholder = m.input.Placeholder
		m.resetInput()
		m.input.Placeholder = "Enter API key for " + msg.Provider
		m.refreshViewport()
		return m, nil
	}

	// Runtime messages always stay with the parent model so background state
	// remains current while a dock panel is open.
	switch msg.(type) {
	case agentFinishedMsg, planAuthorFinishedMsg, jobCountMsg, steeringMsg, agentTickMsg, spinnerTickMsg, workspaceMsg, subagentMsg, railBaseRefMsg, suggestionMsg, callersMsg:
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

	// OpenModelOptionsForProviderMsg from the settings browser: close the
	// browser and open the model-options picker for the named provider.
	if om, ok := msg.(settings.OpenModelOptionsForProviderMsg); ok {
		m.dock.CloseNow()
		m.openModelOptionsForProvider(om.ProviderName)
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
	case presetflow.CapabilityProbedMsg:
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
		case *agents.Panel, connect.Panel, *castlist.Panel:
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
		case cmdName == "model-options":
			// Open the model-options editor for the picked model pair.
			m.dock.Open(modeloptions.New(m.state.Config, pm.Value, m.resolveReasoningSupport(pm.Value)))
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
			if pm.Value == sddScaffoldPlanValue {
				m.scaffoldSDDPlan()
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
		case *agents.Panel, connect.Panel, *castlist.Panel:
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
	case gatepanel.AnswerMsg:
		m.dock.CloseNow()
		m.state.AddMessage(session.RoleUser, pm.Text, session.ContentTypePlain)
		if m.pipelineRunner == nil {
			m.refreshViewport()
			return m, nil
		}
		m.pipelineRunner.AnswerGate(pm.Text)
		goal := m.state.SDDProgress().PlanPath
		return m.startAgentRun(m.pipelineRunner, goal)

	case gatepanel.StopMsg:
		m.dock.CloseNow()
		m.state.ClearSDDGate()
		m.state.AddMessage(session.RoleSystem,
			"Plan run stopped. "+sddResumeHint,
			session.ContentTypePlain)
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
		// Persist proposed verify commands to project config before the run
		// starts. A write failure warns but never blocks the run.
		if run.verifyBuild != "" || run.verifyTest != "" {
			if err := config.SaveVerifyCommands(projectConfigPath(m.state.WorkingDir), run.verifyBuild, run.verifyTest); err != nil {
				m.state.AddMessage(session.RoleSystem,
					fmt.Sprintf("✗ verify commands apply to this run but could not be persisted: %v", err),
					session.ContentTypePlain)
			} else {
				m.state.AddMessage(session.RoleSystem,
					"✓ verify commands persisted to "+relPath(m.state.WorkingDir, projectConfigPath(m.state.WorkingDir)),
					session.ContentTypePlain)
			}
		}
		// Pass the selected strategy to the runner and capture overrides.
		var selectedStrategy string
		if start, ok := msg.(castlist.StartMsg); ok {
			selectedStrategy = start.Strategy
			run.modelOverrides = start.Overrides
		}
		// Apply per-run role overrides to the runner.
		//
		// Swarm: the shared orchestrator's NewRunner is wrapped via
		// SetRunnerFactory and restored when the run ends.
		//
		// SDD: the controller was built with nil overrides at dispatch
		// time (before the user picked any). Rebuild it here with the
		// real overrides so the per-run resolver sees them. The strategy
		// selected in the preflight panel is carried across.
		if run.kind == "sdd" && len(run.modelOverrides) > 0 && m.pipelineFactory != nil {
			newRunner := m.pipelineFactory(run.goal, run.modelOverrides)
			if newRunner != nil {
				if na, ok := newRunner.(*pipeline.ControllerAdapter); ok {
					na.Controller().Strategy = pipeline.Strategy(selectedStrategy)
				}
				run.runner = newRunner
				m.pipelineRunner = newRunner
			}
		} else if adapter, ok := run.runner.(*pipeline.ControllerAdapter); ok {
			// No overrides: just apply the strategy to the existing controller.
			adapter.Controller().Strategy = pipeline.Strategy(selectedStrategy)
		}
		if ror, ok := run.runner.(RoleOverrideRunner); ok && len(run.modelOverrides) > 0 && m.swarmOverrideFactory != nil {
			factory := m.swarmOverrideFactory(run.modelOverrides)
			ror.SetRunnerFactory(factory)
			m.restoreRunner = ror.RestoreRunner
		}
		return m.startAgentRun(run.runner, run.goal)
	case castlist.CancelMsg:
		m.dock.CloseNow()
		m.pendingRun = nil
		// Cancelling preflight must not leave an override installed on
		// the shared orchestrator — it would silently apply to every
		// later run in the session.
		if m.restoreRunner != nil {
			m.restoreRunner()
			m.restoreRunner = nil
		}
		m.refreshViewport()
		return m, nil
	case verifyProposeMsg:
		if pm.err != nil {
			m.state.AddMessage(session.RoleSystem,
				fmt.Sprintf("✗ could not propose verify commands: %v", pm.err), session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		}
		// Stash the proposed commands for this run and re-render the gate
		// row so the user sees exactly what will run. Persistence happens on
		// confirm (castlist.StartMsg), not here.
		if m.pendingRun != nil {
			m.pendingRun.verifyBuild = pm.build
			m.pendingRun.verifyTest = pm.test
		}
		if p, ok := m.dock.Panel().(*castlist.Panel); ok {
			detail := strings.TrimSpace(pm.build + " · " + pm.test)
			if detail == "" {
				detail = "no commands proposed"
			}
			p.SetVerifyRow(strutil.Truncate(detail, 44, true))
		}
		m.state.AddMessage(session.RoleSystem,
			"Proposed verify commands: "+strings.TrimSpace(pm.build+" · "+pm.test)+". Press Enter to run with them (persisted to .marshal/config.toml), or Esc to cancel.",
			session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	case sddreview.AcceptMsg:
		// Accept leaves the candidate artifact in place for a later /sdd.
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	case sddreview.CancelMsg:
		// Discard removes only the generated candidate.
		if panel, ok := m.dock.Panel().(*sddreview.Panel); ok {
			_ = sddauthor.RemoveCandidate(panel.CandidatePath(), m.state.WorkingDir, m.state.Config.SDD.PlansDir)
		}
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	}
	if m.dock.IsOpen() {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.PasteMsg:
			// Offer-to-fill: while the /sdd preflight is open and the verify
			// gate is unknown, `f` dispatches a one-shot proposal task instead
			// of falling through to the dock.
			if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "f" && m.pendingRun != nil && m.pendingRun.verifyBuild == "" && m.pendingRun.verifyTest == "" {
				if p, ok := m.dock.Panel().(*castlist.Panel); ok {
					if p.VerifyGateUnknown() {
						m.state.AddMessage(session.RoleSystem,
							"Proposing verify commands from the repository…", session.ContentTypePlain)
						m.refreshViewport()
						return m, m.startVerifyProposal()
					}
				}
			}
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
	if m.hasPendingApproval() || m.state.PendingQuestion() != nil {
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
	if owner, tc, label := m.pendingApprovalTarget(); tc != nil {
		if updated, cmd, handled := m.scrollTranscript(msg); handled {
			return updated, cmd
		}
		return m.handleApproval(msg, owner, tc, label)
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
		// Vertical-only transcript: sideways trackpad pans and diagonal
		// scrolls must not shift the view horizontally.
		if msg.Button == tea.MouseWheelLeft || msg.Button == tea.MouseWheelRight {
			return m, nil
		}
		// A bounded live region under the cursor scrolls its own body
		// instead of the transcript. This must stay below the horizontal
		// guard: above it, a sideways pan would be consumed here and the
		// vertical-only invariant would be lost.
		if m.scrollLiveRegionAt(msg) {
			return m, nil
		}
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
		if cmd, handled := m.handleAgentLaneClick(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.handleTodoPanelClick(msg); handled {
			return m, cmd
		}
		return m, nil
	case tea.PasteMsg:
		if shouldCondensePaste(msg.Content) {
			m.addPaste(msg.Content)
			m.updateViewportHeight()
			m.refreshViewport()
			return m, nil
		}
		// Small paste: fall through to the textarea as today.
	case tea.KeyPressMsg:
		if mm, cmd, handled := m.handleKeypress(msg); handled {
			return mm, cmd
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// Typing that breaks the suggestion's prefix clears it so a stale ghost
	// cannot resurrect if the user deletes back to empty. Any keystroke also
	// bumps the generation counter so an in-flight LLM fallback result is
	// discarded.
	m.suggestionGen++
	m.clearSuggestionIfPrefixBroken()
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

// pendingApprovalTarget returns the state owning the pending approval the
// user should answer next, preferring the parent session, then the first
// running subagent with a live child state. label names the subagent for
// display; it is empty for the parent.
//
// Subagents() returns a slice in registration order, so the first running
// child with a pending approval is deterministic. With parallel agent.run
// (up to agent.max_concurrent_subagents at once), several children could
// pend at once; answering the first-registered one is acceptable — the
// other surfaces on the next message.
func (m *Model) pendingApprovalTarget() (owner *session.State, tc *session.PendingToolCall, label string) {
	if tc := m.state.PendingApproval(); tc != nil {
		return m.state, tc, ""
	}
	for _, v := range m.state.Subagents() {
		if v.Status != session.SubagentRunning || v.Child == nil {
			continue
		}
		if tc := v.Child.PendingApproval(); tc != nil {
			return v.Child, tc, v.Label
		}
	}
	return nil, nil, ""
}

// hasPendingApproval reports whether any approval is pending — either on
// the parent state or on a running subagent's live child state. Rendering,
// status indicators, and keypress gating must all consult this (not just
// the parent) so a child approval is not silently routed into an invisible
// form.
func (m *Model) hasPendingApproval() bool {
	if m.state.PendingApproval() != nil {
		return true
	}
	for _, v := range m.state.Subagents() {
		if v.Status != session.SubagentRunning || v.Child == nil {
			continue
		}
		if v.Child.PendingApproval() != nil {
			return true
		}
	}
	return false
}

// pendingApprovalDisplay returns the tool call to render for the pending
// approval the user should answer next, mirroring handleApproval's display
// copy: a subagent approval is wrapped in a shallow copy whose reason names
// the subagent, so the rendered panel (and the eager form) attribute the
// request. label is the subagent name, empty for the parent. Returns nil
// when no approval is pending.
func (m *Model) pendingApprovalDisplay() (tc *session.PendingToolCall, label string) {
	_, src, label := m.pendingApprovalTarget()
	if src == nil {
		return nil, ""
	}
	if label == "" {
		return src, ""
	}
	return &session.PendingToolCall{
		ID:           src.ID,
		Name:         src.Name,
		Args:         src.Args,
		Command:      src.Command,
		Risk:         src.Risk,
		Reason:       fmt.Sprintf("subagent %q: %s", label, src.Reason),
		Diff:         src.Diff,
		Schema:       src.Schema,
		ResponseChan: src.ResponseChan,
	}, label
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
//
// owner is the session.State that owns the pending approval (the parent, or
// a running subagent's child state); tc is the pending tool call on that
// state. source names the subagent for display and is empty for the parent.
// The decision is always Responded on the original tc; a shallow display copy
// with a prefixed reason is shown so the dialog names who is asking.
func (m Model) handleApproval(msg tea.Msg, owner *session.State, tc *session.PendingToolCall, source string) (tea.Model, tea.Cmd) {
	// mode.request: show the editing-variant picker instead of the normal
	// approval dialog. The picker opens via the dock; while the dock is
	// open, keypresses are routed to the picker and non-key messages
	// return early here. On PickedMsg the main Update switch handles the
	// response and mode change. This path is parent-only: child runners
	// never request mode elevation, so a child that somehow pends a
	// mode.request falls through to the normal chooser.
	if source == "" && isModeElevationApproval(tc) {
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
				m.resetInput()
				m.input.Placeholder = "Ask Marshal..."
				m.updateViewportHeight()
				m.lastTranscriptHash = 0
				return m, nil
			case "enter":
				value := strings.TrimSpace(m.input.Value())
				if value != "" {
					if owner.PendingApproval() == tc {
						tc.Respond(session.UserApprovalDecision{Approved: true, Edited: value})
					}
					m.editingCommand = false
					m.resetInput()
					m.input.Placeholder = "Ask Marshal..."
					m.updateViewportHeight()
					owner.SetPendingApproval(nil)
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
	// arrives for a pending tool call. For a subagent approval, show a
	// display copy whose reason names the subagent, but always Respond on
	// the original tc. The copy is built field-by-field (not a struct
	// copy) because PendingToolCall embeds sync.Once, which must not be
	// copied.
	displayTC := tc
	if source != "" {
		// Mirror pendingApprovalDisplay so the eager form and the lazily
		// built one attribute the request identically.
		displayTC = &session.PendingToolCall{
			ID:           tc.ID,
			Name:         tc.Name,
			Args:         tc.Args,
			Command:      tc.Command,
			Risk:         tc.Risk,
			Reason:       fmt.Sprintf("subagent %q: %s", source, tc.Reason),
			Diff:         tc.Diff,
			Schema:       tc.Schema,
			ResponseChan: tc.ResponseChan,
		}
	}
	if m.approvalModel == nil {
		m.approvalModel = newApprovalModel(displayTC, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, m.state.HasBackup(), max(m.leftWidth-4, 30))
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
		if owner.PendingApproval() == tc {
			tc.Respond(session.UserApprovalDecision{Approved: true})
		}
		owner.SetPendingApproval(nil)
		m.lastTranscriptHash = 0
		return m, nil
	case choiceDeny:
		if owner.PendingApproval() == tc {
			tc.Respond(session.UserApprovalDecision{Approved: false})
		}
		owner.SetPendingApproval(nil)
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
		if owner.PendingApproval() == tc {
			tc.Respond(session.UserApprovalDecision{Approved: true})
		}
		owner.SetPendingApproval(nil)
		m.lastTranscriptHash = 0
		return m, nil
	case choiceSessionAllow:
		m.state.AddSessionRule(tc.Command)
		if owner.PendingApproval() == tc {
			tc.Respond(session.UserApprovalDecision{Approved: true})
		}
		owner.SetPendingApproval(nil)
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
	m.resetInput()
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
		// Vertical-only transcript: swallow horizontal wheel pans so they
		// never shift the view sideways.
		if msg.Button == tea.MouseWheelLeft || msg.Button == tea.MouseWheelRight {
			return *m, nil, true
		}
		// A bounded live region under the cursor scrolls its own body
		// instead of the transcript. Must stay below the horizontal guard.
		if m.scrollLiveRegionAt(msg) {
			return *m, nil, true
		}
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
		if cmd, handled := m.handleAgentLaneClick(msg); handled {
			return *m, cmd, true
		}
		if cmd, handled := m.handleTodoPanelClick(msg); handled {
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
	} else if tc, _ := m.pendingApprovalDisplay(); tc != nil {
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
	if m.state.PendingQuestion() == nil && !m.hasPendingApproval() {
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
	return max(m.height-transcriptFrameRows-m.scrollHintRows()-m.breadcrumbRows()-statusLineRows-m.todoPanelRows()-m.runPanelRows()-m.liveStripRows()-m.jobLaneRows()-m.agentLaneRows()-m.dockRows()-m.turnSpinnerRows()-m.inputChromeRows()-minTranscriptRows, 1)
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
	todos := m.viewedTodos()
	if m.todosDismissed && todosAllDone(todos) {
		return ""
	}
	out := renderTodoPanelBody(todos, m.todoPanelMode, m.height, m.leftWidth)
	return chrome.PaintBand(out, m.leftWidth, theme.Current().ChromeBG())
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
	out := renderRunPanel(m.state.SDDProgress(), m.turnSpinnerFrame(), m.now(), m.width)
	return chrome.PaintBand(out, m.width, theme.Current().ChromeBG())
}

// runPanelRows reports the rows the run panel occupies. The panel is
// rendered as a top bar (view.go) but its height must be budgeted —
// otherwise the frame grows taller than the terminal and the input area is
// pushed off the bottom of the screen.
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
	newViewportHeight := max(m.height-transcriptFrameRows-m.scrollHintRows()-m.breadcrumbRows()-m.todoPanelRows()-m.runPanelRows()-m.liveStripRows()-m.jobLaneRows()-m.agentLaneRows()-m.dockRows()-m.turnSpinnerRows()-m.inputAreaRows()-statusLineRows, 1)
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
	// Esc dismissed the popup; keep it suppressed until the input is cleared.
	// This prevents editing the same trigger from immediately re-showing it.
	if m.completionSuppressed {
		if value == "" {
			m.completionSuppressed = false
		} else {
			return
		}
	}
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
//
// Argument completion is stateful: only inputs carrying the exact prefix
// of a just-accepted command (e.g. "/plan ") re-trigger the popup. There
// is no per-command argument metadata, so the armed popup shows the
// command list itself. Inputs that merely begin with an English word that
// happens to be a command name ("run the tests") never trigger — the old
// registry-prefix loop did exactly that and was removed.
func (m *Model) commandTrigger(value string) (bool, string) {
	if m.cmdArgMode {
		if strings.HasPrefix(value, m.cmdArgPrefix) {
			return true, ""
		}
		m.cmdArgMode = false
		m.cmdArgPrefix = ""
	}
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

// completionShouldSubmit reports whether Enter on the active popup should
// accept the selection and immediately submit the resulting input (true
// for command popups and setting-value popups), or accept only and keep
// editing (false for file-path popups and setting-key popups).
//
// Setting keys need a value after them ("/set shell.allow_network <value>"),
// so selecting a key with Enter keeps editing. Setting values are complete
// commands ready to submit. File paths are typically part of a larger
// message, so Enter on a file popup keeps editing too.
//
// Must be called before acceptCompletion (which dismisses the popup).
func (m *Model) completionShouldSubmit(p *completionPopup) bool {
	if p == nil || len(p.filtered) == 0 || p.index < 0 || p.index >= len(p.filtered) {
		return false
	}
	chosen := p.filtered[p.index]
	if chosen.Disabled {
		return false
	}
	if chosen.Kind == completionCommand {
		return true
	}
	if chosen.Kind == completionSetting {
		// Distinguish setting-key from setting-value completion by
		// checking whether the part after "/set " already contains
		// a space. "/set shell" → key (no space after the key, accept
		// only). "/set shell.allow_network " → value (space after
		// the key, submit the complete command).
		v := m.input.Value()
		if strings.HasPrefix(v, "/set ") {
			rest := v[len("/set "):]
			return strings.ContainsAny(rest, " \t\n")
		}
		return false
	}
	// File paths keep editing.
	return false
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
	m.completionSuppressed = false
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
	// Arm argument completion for the accepted command: while the input
	// keeps this exact prefix, commandTrigger stays hot (see
	// commandTrigger). File and setting accepts never arm it.
	if p == m.cmdPopup && strings.HasPrefix(newValue, "/") {
		m.cmdArgMode = true
		m.cmdArgPrefix = newValue
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
		item.Message.Role == session.RoleUser &&
		// A subagent report is stored under RoleUser for history replay but
		// is not a turn the user took; treating it as one emits a turn
		// separator above a block that renders nothing.
		item.Message.ContentType != session.ContentTypeSubagentReport
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

// drillIntoLatestRunningSubagent pushes the most recently registered
// running subagent onto the view stack, so inspecting live work does not
// require a mouse. Returns false when no subagent is running.
func (m *Model) drillIntoLatestRunningSubagent() bool {
	subs := m.state.Subagents()
	for i := len(subs) - 1; i >= 0; i-- {
		if subs[i].Status == session.SubagentRunning && subs[i].Child != nil {
			m.drillIntoSubagent(subs[i])
			return true
		}
	}
	return false
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

	todos := m.viewedTodos()
	if sig := todoSignature(todos); sig != m.todosSig {
		m.todosSig = sig
		m.todosDismissed = false
	}
	queued := m.state.SteeringQueue()
	notice, noticeUp := m.state.Notice()
	hash := transcriptHash(items, streamLen, busy, m.viewport.Width(), todos, queued, m.spinnerFrame, atc, notice, noticeUp, m.regionOffset, m.callers, m.regionRows)
	if hash == m.lastTranscriptHash {
		return
	}
	m.lastTranscriptHash = hash

	blocks := make([]string, 0, len(items)+4)
	regions := make([]clickRegion, 0, len(items))
	seenRegions := map[itemKey]bool{}
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
			rv := regionView{offset: m.regionOffset[key], minRows: m.regionRows[key]}
			s := renderTranscriptItem(*entry.Item, expanded, m.spinnerFrame, rv, m.callers[key], m.viewport.Width())
			// Record the tallest this region has been, so a later shrink in
			// the child's activity tail cannot shrink the card.
			if n := strings.Count(s, "\n"); n > m.regionRows[key] {
				if m.regionRows == nil {
					m.regionRows = map[itemKey]int{}
				}
				m.regionRows[key] = n
			}
			var target *clickTarget
			switch entry.Item.Kind {
			case session.KindThinking, session.KindAudit:
				target = &clickTarget{key: key}
			case session.KindMessage:
				if entry.Item.Message != nil &&
					(entry.Item.Message.ContentType == session.ContentTypeNarration ||
						entry.Item.Message.ContentType == session.ContentTypeSkillAuto) {
					target = &clickTarget{key: key}
				}
			case session.KindSubagent:
				if entry.Item.Subagent != nil && entry.Item.Subagent.Child != nil {
					target = &clickTarget{
						key:          key,
						subagent:     entry.Item.Subagent,
						isLiveRegion: entry.Item.Subagent.Status == session.SubagentRunning,
					}
				}
			}
			addBlock(s, target)
			seenRegions[key] = true
		}
	}
	if inProgress.Active && inProgress.Reasoning != "" {
		rv := regionView{offset: m.regionOffset[liveThinkingKey], minRows: m.regionRows[liveThinkingKey]}
		thinkingBlock := renderThinkingBox(
			inProgress.Reasoning,
			m.activeSpinnerFrame(session.ActivityThinking),
			m.now().Sub(inProgress.StartedAt),
			rv,
			m.viewport.Width(),
		)
		addBlock(thinkingBlock, &clickTarget{key: liveThinkingKey, isLiveRegion: true})
		// Record the high-water mark for the thinking region too.
		if n := strings.Count(thinkingBlock, "\n"); n > m.regionRows[liveThinkingKey] {
			if m.regionRows == nil {
				m.regionRows = map[itemKey]int{}
			}
			m.regionRows[liveThinkingKey] = n
		}
		seenRegions[liveThinkingKey] = true
	}
	if act := transcriptState.Activity(); act.Kind == session.ActivityReconnecting && act.Label != "" {
		addBlock(renderReconnectNotice(act.Label, m.activeSpinnerFrame(session.ActivityReconnecting), m.viewport.Width()), nil)
	}
	// Use the same transcriptState that was computed for the drilled-in
	// child (or the parent when not drilling). The previous code read
	// m.state.ActiveToolCall(), which always used the parent session
	// and showed the wrong tool inside a drilled subagent view.
	if atc, ok := transcriptState.ActiveToolCall(); ok {
		// Suppress the parent in-flight agent.run row when a subagent card
		// is already rendering the running child. Completed rows are already
		// deduplicated above; this is the in-flight counterpart.
		suppress := !drilling && atc.Name == "agent.run" && m.state.HasRunningSubagent()
		if !suppress {
			s := renderActiveToolCall(atc, transcriptState.SandboxInfo(), transcriptState.Config.Tools.Shell.AllowNetwork, m.activeSpinnerFrame(session.ActivityTool), m.now(), m.activeToolExpanded, m.viewport.Width())
			addBlock(s, &clickTarget{isActiveTool: true})
		}
	}
	if n, ok := m.state.Notice(); ok {
		addBlock(renderNotice(n, m.viewport.Width()), nil)
	}
	if len(queued) > 0 {
		addBlock(renderQueuedMessages(queued, m.viewport.Width()), nil)
	}

	// Drop offsets for regions that are no longer rendered, so a finished
	// subagent's entry does not leak for the rest of the session.
	for k := range m.regionOffset {
		if !seenRegions[k] {
			delete(m.regionOffset, k)
		}
	}
	// High-water marks follow the same rule: a region that stopped being
	// rendered no longer needs one.
	for k := range m.regionRows {
		if !seenRegions[k] {
			delete(m.regionRows, k)
		}
	}
	// Prune cached callers (and the asked marker) for items no longer in
	// the transcript. This is what makes rollback correct for free: a
	// rewound audit event leaves the transcript, so its blast-radius cache
	// goes with it rather than re-rendering stale callers at moved lines.
	for k := range m.callers {
		if !seenRegions[k] {
			delete(m.callers, k)
			delete(m.callersAsked, k)
		}
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
	var inspection *pipeline.Inspection
	meta := []string{
		"goal: " + strutil.Truncate(goal, 56, true),
		fmt.Sprintf("fix rounds: %d · token budget: %d",
			m.state.Config.Swarm.Budget.MaxFixRounds, m.state.Config.Swarm.Budget.MaxTotalTokens),
	}
	if kind == "sdd" {
		roles = routing.SDDCastRoles
		title = "Start plan run?"

		// Lead with what the run will do to the repository. The model
		// roster matters less than the fact that this is an autonomous,
		// multi-commit run against a branch the user has not seen.
		tasks := "unknown task count"
		for _, c := range sddplans.Discover(m.state.WorkingDir, m.state.Config.SDD.PlansDir) {
			if c.Path != goal {
				continue
			}
			if c.Err == nil {
				tasks = fmt.Sprintf("%d tasks", c.Tasks)
				if c.LedgerErr == nil && c.Done > 0 {
					tasks += fmt.Sprintf(" · resuming at %d", c.Done+1)
				}
			}
		}
		// Use the shared inspection for strategy selection and metadata.
		if adapter, ok := runner.(*pipeline.ControllerAdapter); ok {
			if insp, ierr := adapter.Controller().Inspect(); ierr == nil {
				inspection = insp
			}
		}
		worktree := "off"
		if m.state.Config.SDD.AutoWorktree {
			worktree = "on"
		}
		meta = []string{
			fmt.Sprintf("%s · %s", strutil.Truncate(filepath.Base(goal), 40, true), tasks),
			"one commit per task, on a branch — review and merge it yourself",
			fmt.Sprintf("worktree: %s · fix rounds: %d · verify timeout: %dms",
				worktree, m.state.Config.SDD.MaxFixRounds, m.state.Config.SDD.VerifyTimeoutMS),
		}
		if inspection != nil {
			meta = append(meta, fmt.Sprintf("deterministic ops: %d · fallback ops: %d · est. model calls: %d",
				inspection.Report.Total.DetOps, inspection.Report.Total.AgentOps, inspection.Report.Total.EstCalls))
			for _, tr := range inspection.Report.Tasks {
				meta = append(meta, fmt.Sprintf("task %d: %s · %d det · %d agent · %d call(s)",
					tr.TaskN, tr.Title, tr.DetOps, tr.AgentOps, tr.EstCalls))
			}
		}
		if v := m.state.Config.SDD.Verify; v.Build != "" || v.Test != "" {
			meta = append(meta, "verify: "+strutil.Truncate(strings.TrimSpace(v.Build+" · "+v.Test), 48, true))
		}
	}
	rows := make([]castlist.Row, 0, len(roles))
	for _, entry := range router.Cast(roles) {
		row := castlist.Row{
			Title: strings.ReplaceAll(string(entry.Role), "_", " "),
			Role:  entry.Role,
		}
		if entry.Err != nil {
			row.Err = strutil.Truncate(entry.Err.Error(), 44, true)
		} else {
			row.Detail = entry.Route.Preset.Provider + "/" + entry.Route.Preset.Model
			row.Badge = entry.Route.Preset.Name
		}
		rows = append(rows, row)
	}
	if kind == "sdd" {
		rows = append(rows, verifyGateRow(m.state.Config, m.state.WorkingDir))
	}
	m.pendingRun = &pendingAgentRun{runner: runner, goal: goal, kind: kind}
	panel := castlist.New(title, rows, meta, "agent")
	// Provide the config's model presets so the castlist's in-panel
	// override picker can offer them.
	presetNames := make([]string, 0, len(m.state.Config.Models.Presets))
	for n := range m.state.Config.Models.Presets {
		presetNames = append(presetNames, n)
	}
	sort.Strings(presetNames)
	pickerItems := make([]picker.Item, 0, len(presetNames))
	for _, name := range presetNames {
		preset := m.state.Config.Models.Presets[name]
		pickerItems = append(pickerItems, picker.Item{Label: name, Detail: preset.Provider + "/" + preset.Model, Value: name})
	}
	panel.SetPickerItems(pickerItems)
	if kind == "sdd" {
		// Strategy-aware preflight: select adaptive by default when the plan
		// has executable blocks, and disable adaptive/strict when there are
		// none. Strict is disabled when the inspection is blocked.
		initial := "agent"
		var options []castlist.StrategyOption
		if inspection != nil {
			if inspection.HasMarshalBlocks {
				initial = "adaptive"
			}
			options = []castlist.StrategyOption{
				{Value: "agent"},
				{Value: "adaptive"},
				{Value: "strict"},
			}
			if !inspection.HasMarshalBlocks {
				options[1].DisabledReason = "no executable blocks found"
				options[2].DisabledReason = "no executable blocks found"
			} else if inspection.StrictBlocked {
				options[2].DisabledReason = "fallback or unresolved work remains"
			}
			// An explicit strategy override wins over the automatic default.
			if adapter, ok := runner.(*pipeline.ControllerAdapter); ok {
				if s := adapter.Controller().Strategy; s != pipeline.StrategyAuto && s != "" {
					initial = string(s)
				}
			}
		}
		panel.SetStrategyOptions(options)
		panel.SetStrategy(initial)
	}
	m.dock.Open(panel)
}

// verifyGateRow renders the verify-gate row for the /sdd preflight. It uses
// the same resolver as the gate itself so the UI always shows what will run.
func verifyGateRow(cfg config.Config, repoRoot string) castlist.Row {
	row := castlist.Row{Title: "verify gate"}
	res := pipeline.ResolveVerifyCommands(repoRoot, cfg.SDD.Verify.Build, cfg.SDD.Verify.Test)
	if !res.Known {
		row.Warn = "no build or test command configured — press f to propose one"
		return row
	}
	detail := strings.TrimSpace(res.Build + " · " + res.Test)
	if res.Build != cfg.SDD.Verify.Build || res.Test != cfg.SDD.Verify.Test {
		detail += " (detected)"
	}
	row.Detail = strutil.Truncate(detail, 44, true)
	return row
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
	// Optimistic dismissal: a fresh prompt means the user is trying again, so
	// clear any stale provider notice from an earlier failed turn. If this
	// turn also fails, handleAgentFinished re-sets it.
	m.state.ClearNotice(session.NoticeProvider)
	m.busy = true
	m.turnStartedAt = m.now()
	// A fresh prompt means the user is acting on their own — clear any
	// stale ghost suggestion so it never survives into the new turn, and
	// bump the generation counter so an in-flight LLM fallback result is
	// discarded.
	m.suggestion = ""
	m.suggestionDismissed = false
	m.suggestionGen++
	agentCtx, cancel := context.WithCancel(m.ctx)
	m.agentCancel = cancel
	return *m, tea.Batch(runAgentCmd(agentCtx, m.state, runner, goal), tickCmd(), spinnerTickCmd())
}

// startSDDAuthoring begins an authoring turn for /sdd new. It builds the
// scoped authoring runner, resolves the candidate path, and starts the
// async command. On success the completion handler opens the review panel;
// it never starts an SDD run.
func (m *Model) startSDDAuthoring(parsed parsedSDDArgs) (tea.Model, tea.Cmd) {
	if m.planAuthorFactory == nil {
		m.state.AddMessage(session.RoleSystem, "Plan authoring is not available (agent failed to initialise).", session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	}
	if m.busy {
		return m, nil
	}
	goal := parsed.Goal
	design := goal
	if parsed.FromLastPlan {
		last, ok := lastFinalAssistantPlan(m.state)
		if !ok {
			m.state.AddMessage(session.RoleSystem, "No approved plan found in the last assistant turn. Run /plan first, then /sdd new --from-last-plan.", session.ContentTypePlain)
			m.refreshViewport()
			return m, nil
		}
		design = last
		goal = "Convert the approved Plan mode result into an executable SDD plan."
	}
	if strings.TrimSpace(goal) == "" {
		m.state.AddMessage(session.RoleSystem, "Usage: /sdd new <goal>", session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	}
	planPath, err := sddplans.DraftPath(m.state.WorkingDir, m.state.Config.SDD.PlansDir, goal, m.now())
	if err != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Cannot resolve plan path: %v", err), session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	}
	author, err := m.planAuthorFactory(sddauthor.Request{
		Goal:     goal,
		Design:   design,
		RepoRoot: m.state.WorkingDir,
		PlanPath: planPath,
	})
	if err != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Cannot start plan authoring: %v", err), session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	}
	if err := m.state.BeginWork(); err != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Cannot start work: %v", err), session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	}
	m.busy = true
	m.turnStartedAt = m.now()
	agentCtx, cancel := context.WithCancel(m.ctx)
	m.agentCancel = cancel
	return *m, tea.Batch(runPlanAuthorCmd(agentCtx, m.state, author, sddauthor.Request{
		Goal:     goal,
		Design:   design,
		RepoRoot: m.state.WorkingDir,
		PlanPath: planPath,
	}), tickCmd(), spinnerTickCmd())
}

type agentFinishedMsg struct{ err error }
type agentTickMsg struct{}
type spinnerTickMsg struct{}

// suggestionMsg carries the result of a Phase 2 LLM fallback suggestion
// call. gen is the generation counter captured when the call started; a
// result whose gen is older than the model's current counter is stale and
// discarded.
type suggestionMsg struct {
	suggestion string
	gen        int
}

// runSuggestionFallbackCmd returns a tea.Cmd that invokes the suggestion
// provider for the given last assistant message and reports via
// suggestionMsg. The generation counter is captured at call time so a late
// result can be discarded if the user typed or started a new turn.
func runSuggestionFallbackCmd(provider func(ctx context.Context, lastMsg string) (string, error), lastMsg string, gen int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s, err := provider(ctx, lastMsg)
		if err != nil || strings.TrimSpace(s) == "" {
			return suggestionMsg{suggestion: "", gen: gen}
		}
		return suggestionMsg{suggestion: strings.TrimSpace(s), gen: gen}
	}
}

// verifyProposeMsg carries the result of a one-shot proposal task that
// inspects the repo and proposes build/test commands for the verify gate.
type verifyProposeMsg struct {
	build, test string
	err         error
}

// startVerifyProposal dispatches a one-shot sub-agent that inspects the
// repo and proposes build/test commands, reporting via verifyProposeMsg. It
// runs in the background while the preflight dock stays open; the result is
// surfaced on the verify-gate row for the user to approve or edit.
func (m *Model) startVerifyProposal() tea.Cmd {
	return func() tea.Msg {
		if m.subagentFactory == nil {
			return verifyProposeMsg{err: errors.New("no subagent factory wired")}
		}
		runner, err := m.subagentFactory("")
		if err != nil {
			return verifyProposeMsg{err: fmt.Errorf("build proposal runner: %w", err)}
		}
		if runner == nil {
			return verifyProposeMsg{err: errors.New("proposal runner is nil")}
		}
		prompt := "Inspect this repository and return exactly one JSON object with \"build\" and \"test\" fields: the shell commands (argv, no pipes) to compile and test it, or empty strings if a step does not apply. Only propose commands that will actually work."
		if err := runner.Run(m.ctx, prompt); err != nil {
			return verifyProposeMsg{err: fmt.Errorf("proposal task failed: %w", err)}
		}
		// The runner's final message holds the structured output; read it
		// from the session transcript.
		build, test, err := m.proposalFromSession()
		return verifyProposeMsg{build: build, test: test, err: err}
	}
}

// proposalFromSession extracts the build/test proposal from the last user
// turn's assistant output in the session transcript.
func (m *Model) proposalFromSession() (build, test string, err error) {
	msgs := m.state.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != session.RoleAssistant {
			continue
		}
		raw := msgs[i].Content
		if raw == "" {
			continue
		}
		text, rerr := jsonextract.Extract(raw)
		if rerr != nil {
			continue
		}
		var p struct {
			Build string `json:"build"`
			Test  string `json:"test"`
		}
		if json.Unmarshal([]byte(text), &p) != nil {
			continue
		}
		return p.Build, p.Test, nil
	}
	return "", "", errors.New("proposal task returned no parseable JSON")
}

// runAgentCmd wraps an agent turn into a Bubble Tea command that
// registers session work via BeginWork on construction and releases it
// via EndWork when the command executes. Callers must handle
// ErrSessionQuiescing from BeginWork before creating the command.
func runAgentCmd(ctx context.Context, state *session.State, runner AgentRunner, goal string) tea.Cmd {
	return func() (msg tea.Msg) {
		defer state.EndWork()
		// Recover from a panic in runner.Run so the goroutine does not
		// crash the TUI before handleAgentFinished can restore the
		// shared runner's original factory. The panic is surfaced as a
		// turn error; the restore runs on the normal finish path.
		defer func() {
			if r := recover(); r != nil {
				msg = agentFinishedMsg{err: fmt.Errorf("panic in agent run: %v", r)}
			}
		}()
		err := runner.Run(ctx, goal)
		return agentFinishedMsg{err: err}
	}
}

// runReviewCmd wraps a /review subagent dispatch into a Bubble Tea command.
func runReviewCmd(ctx context.Context, state *session.State, dispatcher func(ctx context.Context, focus, model, reviewRange string) error, focus, model, reviewRange string) tea.Cmd {
	return func() tea.Msg {
		defer state.EndWork()
		err := dispatcher(ctx, focus, model, reviewRange)
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
	if m.lspCancel != nil {
		m.lspCancel()
		m.lspCancel = nil
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
	if m.hasPendingApproval() {
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
	// suggestionCmd is the Phase 2 LLM fallback returned by computeSuggestion
	// on the success path; nil otherwise.
	var suggestionCmd tea.Cmd
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
		m.state.ClearNotice(session.NoticeProvider)
		m.cancelling = false
	}
	m.busy = false
	m.turnStartedAt = time.Time{}
	m.agentCancel = nil
	// Restore the shared runner's original NewRunner so a per-run override
	// does not leak into the next run. This runs on success, failure, and
	// cancel — the defer-style guarantee the plan requires.
	if m.restoreRunner != nil {
		m.restoreRunner()
		m.restoreRunner = nil
	}
	m.refreshRailTurns()
	m.refreshRailChanged()
	if msg.err != nil && !cancelled && !errors.Is(msg.err, context.Canceled) {
		// SDD human gate: open the gate panel and wait for the user's answer.
		if errors.Is(msg.err, pipeline.ErrHumanGateRequired) {
			gate := m.state.SDDGate()
			if gate.Question != "" {
				// One surface: the panel carries the question, its context,
				// and the key hints. Nothing goes to the transcript here.
				m.dock.Open(gatepanel.New(gate))
				m.state.SetActivity(session.Activity{Kind: session.ActivityIdle})
				m.updateViewportHeight()
				m.refreshViewport()
				return m, nil
			}
		}
		m.state.SetNotice(noticeForError(msg.err, "turn"))
		m.state.AddMessage(session.RoleSystem, "✘ Turn failed: "+firstLine(msg.err.Error()), session.ContentTypePlain)
		m.successPulse = false
	} else if msg.err == nil {
		// A completed turn proves the provider is reachable again — clear
		// any stale provider notice from an earlier failed turn.
		m.state.ClearNotice(session.NoticeProvider)
		m.successPulse = true
		m.successPulseAt = m.now()
		// After a successful Plan mode turn, hint that the approved plan can
		// become an executable SDD artifact. Do not auto-run it.
		if m.approvalMode == policy.ModePlan {
			m.state.AddMessage(session.RoleSystem,
				"To turn this approved plan into an executable SDD artifact, run /sdd new --from-last-plan.",
				session.ContentTypePlain)
		}
		// A completed turn is the single point where the final assistant
		// message is in the session state — compute the next-prompt
		// suggestion here. Only on the success path: a failed or cancelled
		// turn must not leave a stale ghost. The returned cmd is the Phase 2
		// LLM fallback (nil when not applicable).
		suggestionCmd = m.computeSuggestion()
	}
	m.state.SetActivity(session.Activity{Kind: session.ActivityIdle})
	m.updateViewportHeight()
	m.refreshViewport()
	flushCmd := m.flushPendingModelOptions()
	// Rebase the changed-files rail onto the active root's HEAD so committed
	// agent work stops inflating the diff; the railBaseRefMsg handler sets the
	// new base and refreshes the cache after the next tick.
	cmds := []tea.Cmd{tickCmd(), flushCmd, railBaseRefCmd(m.state.Workspace().ActiveRoot)}
	if suggestionCmd != nil {
		cmds = append(cmds, suggestionCmd)
	}
	// Issue blast-radius lookups for any edit rows the turn just settled.
	// tea.Batch drops nil entries, so this is a no-op with no finder or no
	// newly-seen resolved symbols.
	cmds = append(cmds, m.callerQueryCmds())
	return m, tea.Batch(cmds...)
}

// clearSuggestionIfPrefixBroken clears the active suggestion when the typed
// input no longer prefixes it (the user typed something that diverges from
// the suggested text). This prevents a stale ghost from resurrecting if the
// user deletes back to an empty input.
func (m *Model) clearSuggestionIfPrefixBroken() {
	if m.suggestion == "" {
		return
	}
	value := m.input.Value()
	if value != "" && !strings.HasPrefix(m.suggestion, value) {
		m.suggestion = ""
		m.suggestionDismissed = false
	}
}

// computeSuggestion derives the next-prompt suggestion from the final
// assistant message of the just-completed turn and stores it on the model.
// It resets the dismissed flag so a fresh turn can surface a new ghost.
// When the deterministic rules produce no suggestion and [tui] suggestions
// is "llm" with a provider wired, it returns a background tea.Cmd for the
// LLM fallback; otherwise it returns nil.
func (m *Model) computeSuggestion() tea.Cmd {
	m.suggestion = ""
	m.suggestionDismissed = false
	if m.state.Config.TUI.Suggestions == "off" {
		return nil
	}
	msgs := m.state.Messages()
	var last session.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == session.RoleAssistant {
			last = msgs[i]
			break
		}
	}
	if last.Role != session.RoleAssistant {
		return nil
	}
	if s, ok := extractSuggestion(last.Content); ok {
		m.suggestion = s
		return nil
	}
	// Phase 2 LLM fallback: only when the mode is "llm" and a provider is
	// wired. The generation counter is bumped so a stale result from an
	// earlier turn is discarded.
	if m.state.Config.TUI.Suggestions == "llm" && m.suggestionProvider != nil {
		m.suggestionGen++
		return runSuggestionFallbackCmd(m.suggestionProvider, last.Content, m.suggestionGen)
	}
	return nil
}

// handlePlanAuthorFinished handles the completion of an authoring turn. On
// success it opens the review panel; on error it reports the error and keeps
// any generated artifact. It never starts an SDD run.
func (m Model) handlePlanAuthorFinished(msg planAuthorFinishedMsg) (Model, tea.Cmd) {
	m.busy = false
	m.turnStartedAt = time.Time{}
	m.agentCancel = nil
	m.state.SetActivity(session.Activity{Kind: session.ActivityIdle})
	if msg.err != nil {
		m.state.SetNotice(noticeForError(msg.err, "plan-author"))
		m.state.AddMessage(session.RoleSystem, "✘ Plan authoring failed: "+firstLine(msg.err.Error()), session.ContentTypePlain)
		m.updateViewportHeight()
		m.refreshViewport()
		return m, nil
	}
	m.state.ClearNotice(session.NoticeProvider)
	m.dock.Open(sddreview.New(msg.result))
	m.updateViewportHeight()
	m.refreshViewport()
	return m, nil
}

// handleJobCount handles a jobCountMsg, shared by Update and
// handleRuntimeMessage.
func (m Model) handleJobCount(msg jobCountMsg) (Model, tea.Cmd) {
	// A job that was running in the previous snapshot and is not running in
	// this one has finished. This is the only place a job's outcome reaches
	// the transcript.
	wasRunning := map[string]native.JobInfo{}
	for _, j := range m.jobs {
		if j.Status == native.StatusRunning {
			wasRunning[j.ID] = j
		}
	}
	for _, j := range msg.jobs {
		if j.Status == native.StatusRunning {
			continue
		}
		prev, ok := wasRunning[j.ID]
		if !ok {
			continue
		}
		code := 0
		if j.ExitCode != nil {
			code = *j.ExitCode
		} else if j.Status != native.StatusCompleted {
			// Killed or timed out with no code recorded: non-zero so the row
			// renders as a failure rather than a clean exit.
			code = -1
		}
		m.state.AddJobExit(session.JobExit{
			ID: j.ID, Command: j.Command, ExitCode: code,
			Duration: time.Since(prev.StartedAt),
		})
	}
	m.jobCount = msg.count
	m.jobs = msg.jobs
	// A job exit row was appended to the transcript above; repaint now so it
	// renders even when the user is idle (no tick, notice, or pulse would
	// otherwise trigger a rebuild). Without this the exit stays invisible
	// until some unrelated event refreshes the viewport.
	m.refreshViewport()
	flushCmd := m.flushPendingModelOptions()
	// Re-arm the pump: exactly one in-flight subscription at a time
	// (F19 R2). Return nil if no broker is wired so the cmd chain
	// terminates (this should not happen when the pump is sourced
	// from Init, but keeps Update safe under tests that wire msgs
	// directly).
	if m.jobEvents == nil {
		return m, flushCmd
	}
	return m, tea.Sequence(pumpJobEvents(m.jobEvents), flushCmd)
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
// waiting for the 5s tick, then re-arm the pump. It also returns a
// railBaseRefCmd so the changed-files rail rebases onto the new root's HEAD
// off the UI thread.
func (m Model) handleWorkspaceMsg(msg workspaceMsg) (Model, tea.Cmd) {
	var baseCmd tea.Cmd
	if msg.activeRoot != "" {
		m.gitInfo = gitinfo.Read(msg.activeRoot)
		m.lastGitRead = m.now()
		baseCmd = railBaseRefCmd(msg.activeRoot)
	}
	if m.workspaceEvents == nil {
		return m, baseCmd
	}
	return m, tea.Batch(pumpWorkspaceEvents(m.workspaceEvents), baseCmd)
}

// handleSubagentMsg handles a subagentMsg: a subagent card was registered
// or changed status, so refresh the transcript viewport to reflect the
// new card state, then re-arm the pump.
func (m Model) handleSubagentMsg(msg subagentMsg) (Model, tea.Cmd) {
	m.refreshViewport()
	if m.subagentEvents == nil {
		return m, nil
	}
	return m, pumpSubagentEvents(m.subagentEvents)
}

// handleRailBaseRef handles a railBaseRefMsg: a freshly-read HEAD SHA for
// the changed-files rail. It rebases the base ref and refreshes the cache.
// refreshRailChanged runs two git diff subprocesses synchronously here; that
// matches the existing turn-boundary behavior and happens at most once per
// workspace change, so it is acceptable on the UI thread.
func (m Model) handleRailBaseRef(msg railBaseRefMsg) (Model, tea.Cmd) {
	// Drop msgs whose dir is no longer the active root: linked worktrees
	// share the object store, so a stale in-flight cmd from a previous
	// workspace/session could otherwise rebase the rail against the wrong
	// tree and produce a misleading diff.
	if msg.dir != m.state.Workspace().ActiveRoot {
		return m, nil
	}
	if msg.ref != "" {
		m.railBaseRef = msg.ref
	}
	m.refreshRailChanged()
	// No explicit refreshViewport here: Bubble Tea re-renders after every
	// Update, and the rail reads m.railChanged directly in View, so the
	// updated cache is picked up on the next frame. refreshViewport only
	// rebuilds the transcript viewport, which this message does not touch.
	return m, nil
}

// handleAgentTick handles an agentTickMsg, shared by Update and
// handleRuntimeMessage.
func (m Model) handleAgentTick(msg agentTickMsg) (Model, tea.Cmd) {
	if now := m.now(); m.state.Workspace().ActiveRoot != "" && now.Sub(m.lastGitRead) >= 5*time.Second {
		m.gitInfo = gitinfo.Read(m.state.Workspace().ActiveRoot)
		m.lastGitRead = now
	}
	if !m.busy && m.successPulse && m.now().Sub(m.successPulseAt) >= successPulseDuration {
		m.successPulse = false
	}
	noticePending := false
	if n, ok := m.state.Notice(); ok {
		if m.now().Sub(n.SetAt) >= noticeBannerDuration {
			m.state.DismissNotice()
			// The banner is rendered into the transcript; dismissing it
			// must repaint the viewport or the stale banner stays on screen
			// until an unrelated transcript change.
			m.refreshViewport()
		} else {
			noticePending = true
		}
	}
	if !m.busy && !m.successPulse && !noticePending {
		// Even when idle, newly-settled edit rows may still need their
		// blast-radius lookups issued; the query cmds no-op when there is
		// nothing left to ask.
		return m, m.callerQueryCmds()
	}
	m.updateViewportHeight()
	m.refreshViewport()
	return m, tea.Batch(tickCmd(), m.callerQueryCmds())
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
	case planAuthorFinishedMsg:
		return m.handlePlanAuthorFinished(msg)
	case jobCountMsg:
		return m.handleJobCount(msg)
	case steeringMsg:
		return m.handleSteering(msg)
	case workspaceMsg:
		return m.handleWorkspaceMsg(msg)
	case subagentMsg:
		return m.handleSubagentMsg(msg)
	case railBaseRefMsg:
		return m.handleRailBaseRef(msg)
	case agentTickMsg:
		return m.handleAgentTick(msg)
	case spinnerTickMsg:
		return m.handleSpinnerTick(msg)
	case suggestionMsg:
		return m.handleSuggestionMsg(msg)
	case callersMsg:
		return m.handleCallers(msg)
	}
	return m, nil
}

// handleSuggestionMsg applies a Phase 2 LLM fallback suggestion result,
// discarding it if a newer generation has superseded it (the user typed or
// started a new turn).
func (m Model) handleSuggestionMsg(msg suggestionMsg) (Model, tea.Cmd) {
	if msg.gen < m.suggestionGen {
		return m, nil
	}
	if msg.suggestion == "" {
		return m, nil
	}
	m.suggestion = msg.suggestion
	m.suggestionDismissed = false
	m.refreshViewport()
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
		cmds = append(cmds, probe.Provider("models", n, pc, m.modelCacheDir, m.state.Config.Privacy.RemoteLimitDiscovery, m.state.Config.Agent.ThinkingBudgetMargin))
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
			"No model presets configured. Use /connect to add a provider or /profiles to assign a discovered model.",
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

// openSDDPlanPicker lists the plans a run can execute, with each plan's
// task count and resume state resolved up front. A plan that will not parse
// is listed with its error rather than hidden, and a partly-finished run is
// pinned to the top as an explicit resume.
func (m *Model) openSDDPlanPicker() {
	candidates := sddplans.Discover(m.state.WorkingDir, m.state.Config.SDD.PlansDir)

	var items []picker.Item
	// Resumable runs first: re-picking the plan is how a stopped run is
	// continued, and that was previously undiscoverable.
	for _, c := range candidates {
		if !c.Resumable() {
			continue
		}
		items = append(items, picker.Item{
			Label:  fmt.Sprintf("Resume %s — task %d/%d", c.Slug, c.Done+1, c.Tasks),
			Detail: fmt.Sprintf("%d/%d done · from the ledger", c.Done, c.Tasks),
			Badge:  "resume",
			Value:  c.Path,
		})
	}
	for _, c := range candidates {
		item := picker.Item{Label: c.Name, Value: c.Path}
		switch {
		case c.Err != nil:
			item.Detail = c.Err.Error()
			item.Badge = "unreadable"
		case c.LedgerErr != nil:
			item.Detail = fmt.Sprintf("%d tasks · ledger unreadable", c.Tasks)
			item.Badge = "unreadable"
		case c.Resumable():
			item.Detail = fmt.Sprintf("%d tasks · %d done", c.Tasks, c.Done)
		default:
			item.Detail = fmt.Sprintf("%d tasks", c.Tasks)
		}
		items = append(items, item)
	}

	if len(candidates) == 0 {
		items = append(items, picker.Item{
			Label:  "Write a starter plan",
			Detail: "creates a template in " + m.state.Config.SDD.PlansDir,
			Value:  sddScaffoldPlanValue,
		})
	}
	items = append(items, picker.Item{
		Label:  "Custom plan path...",
		Detail: "type or paste a path",
		Value:  sddCustomPlanPathValue,
	})

	p := picker.New("Pick a plan to run", "subagent-driven development", items)
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
// connect overlay. It writes the provider entry to cfg.Providers,
// materializes a preset for the provider/model pair, sets the default profile
// to the single-model profile name with the new preset as active, and saves.
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
		// pc is an explicit copy: ProviderConfig is a value struct, and both
		// this map entry and the SaveUserConfigProviderAPIKey call below
		// must observe the same cleared APIKeyEnv.
		pc := msg.ProviderCfg
		// A literal key was just persisted to the user config. Ensure the
		// project snapshot (and therefore the project file) does not retain a
		// stale api_key_env reference from the provider template, which would
		// shadow the saved literal key during layered config merge.
		if pc.APIKey != "" {
			pc.APIKeyEnv = ""
		}
		newCfg.Providers[msg.Provider] = pc
	}
	if msg.EnabledRemote {
		newCfg.Privacy.RemoteProvidersAllowed = true
	}
	// Selecting a model synthesizes a preset plus a profile binding every
	// role to it. The router resolves through profiles only; there is no
	// legacy pair and no implicit "empty default means fall through".
	if newCfg.Models.Presets != nil {
		presets := make(map[string]routing.ModelPreset, len(newCfg.Models.Presets)+1)
		for k, v := range newCfg.Models.Presets {
			presets[k] = v
		}
		newCfg.Models.Presets = presets
	}
	presetName, err := presetflow.Materialize(&newCfg, msg.Provider, msg.Model, msg.ProviderCfg.BaseURL, presetflow.Limits{
		ContextWindow:   msg.ContextWindow,
		MaxOutputTokens: msg.MaxOutputTokens,
		ToolCalling:     msg.ToolCalling,
	})
	if err != nil {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✗ Failed to materialize model preset: %v", err), session.ContentTypePlain)
		return
	}

	newCfg.Profile.Default = singleModelProfileName
	newCfg.Profile.ActivePreset = presetName
	// The "single" profile is synthesized in memory by RoutingConfig when no
	// explicit agent_profiles entry matches the default profile. When
	// ActivePreset is set, RoutingConfig always (re)synthesizes from it,
	// overwriting any stale entry left by a legacy migration. Do not persist
	// it here; that keeps the config file limited to provider + model pair.

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
			config.UserConfigPath(home), msg.Provider, msg.ProviderCfg); err != nil {
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
		// A successful switch proves provider construction works with the
		// new config — clear any stale provider/config notice.
		m.state.ClearNotice(session.NoticeProvider)
		m.state.ClearNotice(session.NoticeConfig)
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
	provider, model, ok := strings.Cut(presetName, "/")
	if !ok || provider == "" || model == "" {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✗ Invalid preset pair: %s", presetName), session.ContentTypePlain)
		return
	}
	if _, ok := newCfg.Providers[provider]; !ok {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("✗ Provider %q is not configured", provider), session.ContentTypePlain)
		return
	}
	// Ensure an override exists if the user explicitly named a preset.
	if _, ok := newCfg.Models.Presets[presetName]; !ok {
		if newCfg.Models.Presets == nil {
			newCfg.Models.Presets = map[string]routing.ModelPreset{}
		}
		newCfg.Models.Presets[presetName] = routing.ModelPreset{
			Name:      presetName,
			Provider:  provider,
			Model:     model,
			LocalOnly: routing.IsLocalProvider(newCfg.Providers[provider].BaseURL),
		}
	}
	preset := newCfg.Models.Presets[presetName]
	newCfg.Profile.Default = singleModelProfileName
	newCfg.Profile.ActivePreset = presetName
	// In-memory profile synthesis in RoutingConfig handles role bindings.
	// When ActivePreset is set, RoutingConfig always (re)synthesizes the
	// "single" profile from it, overwriting any stale entry left by a
	// legacy [agent] model migration.

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
	activeTheme = theme.LoadWithConfig(tui.Theme, tui.Mode, theme.ParseDepth(tui.Depth), theme.PaletteOverrides(tui.Palette))

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
// mutedStyle delegates to the shared theme.MutedStyle so the monochrome
// check lives in one place.
func mutedStyle() lipgloss.Style { return theme.MutedStyle() }
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

func transcriptHash(items []session.TranscriptItem, streamLen int, busy bool, width int, todos []native.TodoItem, queued []string, spinnerFrame string, atc session.ActiveToolCall, notice session.Notice, noticeUp bool, regionOffsets map[itemKey]int, callers map[itemKey][]string, regionRows map[itemKey]int) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "c=%d|w=%d|f=%d|", len(items), width, flags(streamLen, busy, len(todos), len(queued)))
	// The notice banner is rendered into the transcript, so its presence
	// and identity must bust the viewport cache: without this, esc-dismiss
	// and the TTL auto-dismiss repaint nothing (the hash is unchanged) and
	// the banner stays on screen until an unrelated transcript change.
	fmt.Fprintf(h, "notice=%t|%d|%s|%d|%s|%s|", noticeUp, notice.SetAt.UnixNano(), notice.Category.String(), notice.Severity, notice.Message, notice.Hint)
	// Live state: without the spinner frame and the active tool call the
	// early-return in refreshViewport freezes the ▸ row for the whole
	// duration of a long tool call (e.g. agent.run).
	fmt.Fprintf(h, "spin=%s|atc=%s|%s|%d|", spinnerFrame, atc.Name, atc.Args, atc.StartedAt.UnixNano())
	// Bounded live regions scroll independently, and their offsets live on
	// the Model rather than in items — so without this a scroll changes no
	// hashed input, refreshViewport early-returns, and the region visibly
	// does not move. Same class of bug as the notice banner above.
	// Sorted: map iteration order is randomised, and an unstable hash would
	// rebuild the viewport on every call.
	roKeys := make([]itemKey, 0, len(regionOffsets))
	for k := range regionOffsets {
		roKeys = append(roKeys, k)
	}
	sort.Slice(roKeys, func(i, j int) bool {
		if !roKeys[i].ts.Equal(roKeys[j].ts) {
			return roKeys[i].ts.Before(roKeys[j].ts)
		}
		return roKeys[i].kind < roKeys[j].kind
	})
	for _, k := range roKeys {
		fmt.Fprintf(h, "roff=%d|%d|%d|", k.ts.UnixNano(), k.kind, regionOffsets[k])
	}
	// High-water marks render into the transcript (via MinRows) but live on
	// the Model rather than in items, so without this a change to the mark
	// changes no hashed input, refreshViewport early-returns, and the region
	// keeps its stale height. Sorted for the same reason as the offsets.
	rrKeys := make([]itemKey, 0, len(regionRows))
	for k := range regionRows {
		rrKeys = append(rrKeys, k)
	}
	sort.Slice(rrKeys, func(i, j int) bool {
		if !rrKeys[i].ts.Equal(rrKeys[j].ts) {
			return rrKeys[i].ts.Before(rrKeys[j].ts)
		}
		return rrKeys[i].kind < rrKeys[j].kind
	})
	for _, k := range rrKeys {
		fmt.Fprintf(h, "rrows=%d|%d|%d|", k.ts.UnixNano(), k.kind, regionRows[k])
	}
	// Cached blast-radius results render into the transcript but live on the
	// Model rather than in items, so without this an arriving result changes
	// no hashed input, refreshViewport early-returns, and the callers line
	// never appears. Same class of bug as the region offsets above. Sorted:
	// map iteration is randomised, and an unstable hash would rebuild the
	// viewport on every call.
	cKeys := make([]itemKey, 0, len(callers))
	for k := range callers {
		cKeys = append(cKeys, k)
	}
	sort.Slice(cKeys, func(i, j int) bool {
		if !cKeys[i].ts.Equal(cKeys[j].ts) {
			return cKeys[i].ts.Before(cKeys[j].ts)
		}
		return cKeys[i].kind < cKeys[j].kind
	})
	for _, k := range cKeys {
		fmt.Fprintf(h, "callers=%d|%d|%d|", k.ts.UnixNano(), k.kind, len(callers[k]))
	}
	for _, item := range items {
		fmt.Fprintf(h, "%d|%d|", item.Kind, item.Timestamp.UnixNano())
		if item.Message != nil {
			fmt.Fprintf(h, "%s|%s|%s\x00", item.Message.Role, item.Message.ContentType, item.Message.Content)
		}
		if item.Subagent != nil {
			// Subagent cards are live while the child runs: status, tool-call
			// count, and summary must bust the viewport cache or the card
			// freezes at registration time.
			fmt.Fprintf(h, "sub=%d|%v|%s|%d|%d|%s|%s\x00", item.Subagent.ID, item.Subagent.Status, item.Subagent.Label, item.Subagent.ToolCalls, item.Subagent.EndedAt.UnixNano(), item.Subagent.Summary, item.Subagent.CurrentTool)
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
