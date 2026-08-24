package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/pubsub"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

// Session event types (F21). Defined next to the owning service per F19
// R4. Type strings are opaque identifiers agreed between the session
// publisher and the ACP / external subscribers.
const (
	// subagentReportCap limits how much of an agent.run result is
	// persisted into turn_tool_audit for cross-turn replay. The cap
	// keeps history bounded while still preserving useful subagent
	// findings.
	subagentReportCap = 4000
)

const (
	EventMessageAdded           = "message_added"
	EventThinkingChanged        = "thinking_changed"
	EventActivityChanged        = "activity_changed"
	EventActiveToolChanged      = "active_tool_changed"
	EventAuditAdded             = "audit_added"
	EventPendingApprovalChanged = "pending_approval_changed"
	EventPendingQuestionChanged = "pending_question_changed"
	EventBrowserChanged         = "browser_changed"
)

// Event is the union payload published on the session event broker
// (F21). Only the field relevant to Type is populated; the others remain
// nil. Payloads passed to Publish must be safe copies (no shared pointers
// to internal state that may be mutated by concurrent writers).
//
// PendingApproval and PendingQuestion carry the full pointer (including
// ResponseChan) for subscribers that need to respond (e.g., the ACP
// permission bridge).
type Event struct {
	Message         *Message
	Thinking        *InProgressMessage
	Activity        *Activity
	ActiveTool      *ActiveToolCall
	Audit           *registry.AuditEvent
	PendingApproval *PendingToolCall
	PendingQuestion *PendingQuestion
	Browser         *BrowserInfo
}

// Snapshotter lets the TUI/commands undo/redo via the shadow-git snapshot
// service without importing internal/snapshot.
type Snapshotter interface {
	Track(ctx context.Context) (string, error)
	Diff(ctx context.Context, hash string) (string, error)
	Restore(ctx context.Context, hash string) error
}

type TranscriptKind int

const (
	KindMessage TranscriptKind = iota
	KindThinking
	KindAudit
	// KindSubagent is a subagent summary card (see SubagentView) rendered in
	// place of that subagent's full tool log. Clicking it drills into the
	// child session's live transcript.
	KindSubagent
	// KindRunEvent is one entry in a plan run's event log (a verify
	// failure, a review finding, a commit). Rendered inline so it
	// interleaves with the subagent cards it explains.
	KindRunEvent
	// KindJobExit is a background shell job finishing. Appended to the end
	// of the const block deliberately: the values are used as map keys and
	// hashed, so inserting in the middle would renumber the existing kinds.
	KindJobExit
)

type TranscriptItem struct {
	Timestamp time.Time
	Kind      TranscriptKind
	Message   *Message
	Audit     *registry.AuditEvent
	Thinking  *ThinkingEntry
	// Subagent is set when Kind == KindSubagent: the summary card for one
	// registered subagent, rendered in place of its full tool log.
	Subagent *SubagentView
	// RunEvent is set when Kind == KindRunEvent.
	RunEvent *RunEvent
	// JobExit is set when Kind == KindJobExit.
	JobExit *JobExit
}

type ActivityKind string

const (
	ActivityIdle     ActivityKind = "idle"
	ActivityThinking ActivityKind = "thinking"
	ActivityTool     ActivityKind = "tool"
	ActivityApproval ActivityKind = "approval"
	ActivityQuestion ActivityKind = "question"
	// ActivityReconnecting marks a wait-for-connectivity pause: the provider
	// connection dropped mid-turn and the agent is backing off before
	// resending the same request. Distinct from ActivityThinking so the UI
	// can tell "the model is slow" apart from "the network is down".
	ActivityReconnecting ActivityKind = "reconnecting"
)

// The tool-result cache spans the session, not one turn: a file the
// agent read two turns ago is answered from memory instead of being
// re-executed, and the cached content goes back on the wire. Bounded so
// a long session cannot grow it without limit.
const (
	maxToolCacheEntries = 64
	maxToolCacheBytes   = 2 << 20 // 2 MiB of cached content
)

type Activity struct {
	Kind      ActivityKind
	Label     string
	StartedAt time.Time
}

type Persistence struct {
	DB        *db.DB
	SessionID string
	Logger    *slog.Logger
}

type RouteInfo struct {
	Role      routing.AgentRole
	Profile   string
	Preset    string
	Provider  string
	Model     string
	LocalOnly bool
	Active    bool
}

type ToolBudget struct {
	Used int
	Max  int
}

// SandboxInfo is a plain (import-cycle-free) snapshot of the active Milestone
// Q sandbox backend's advertised capabilities, set once by app.Run after the
// sandbox is constructed. The TUI reads it to describe isolation honestly
// (e.g. "network: blocked (container)" vs "sandbox: restricted · network not
// isolated") during tool approval/exec rendering. Only the fields the TUI
// actually consumes are captured here — ResourceLimits and FilesystemIsolation
// are retained in the registry.SandboxMeta audit trail if future TUI surfaces
// need them.
type SandboxInfo struct {
	Backend          string
	NetworkIsolation bool
}

type BrowserInfo struct {
	Active      bool
	ToolName    string
	URL         string
	Title       string
	Mode        string
	SessionOpen bool
	UpdatedAt   time.Time
}

type State struct {
	Config config.Config
	// WorkingDir is the project root and never changes for the session's
	// lifetime. Tools that must follow a worktree rebind read
	// Workspace().ActiveRoot instead; see workspace.go.
	WorkingDir string
	StartedAt  time.Time
	now        func() time.Time
	db         *db.DB
	sessionID  string
	logger     *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu              sync.Mutex
	messages        []Message
	inProgress      InProgressMessage
	notice          Notice
	noticeSet       bool
	pendingApproval *PendingToolCall
	pendingQuestion *PendingQuestion
	activeToolCall  *ActiveToolCall
	sessionRules    []string
	auditLog        []registry.AuditEvent
	thinkingLog     []ThinkingEntry
	lastBackup      []BackupFile
	contextPack     contextpack.Pack
	activeRoute     RouteInfo
	turnToolCache   map[string]registry.ToolResult
	toolCacheOrder  []string
	toolCacheBytes  int
	activity        Activity
	plan            []string
	todos           []db.TodoItem
	scratchpad      map[string]db.ScratchpadEntry
	activeSkills    map[string]bool
	loadedTools     map[string]bool
	toolBudget      ToolBudget
	// toolAuditThisTurn is the per-turn accumulator for the cross-turn
	// ledger (Task 3/A1). LogToolCall appends here as well as to
	// auditLog, and appendMessage flushes it to the turn_tool_audit table
	// when an assistant final message persists. Best-effort: failures are
	// logged and do not abort the turn.
	toolAuditThisTurn []db.ToolAuditEntry
	swarmProgress     SwarmProgress
	sddProgress       SDDProgress
	// sddFallbackAllowed is the controller's pending marshal.agent
	// fallback allowlist, stashed so the pipeline registry factory can
	// narrow the next ScopeFallback dispatch to the declared paths.
	// Empty/nil means no scope is declared; the factory must reject.
	sddFallbackAllowed []string
	sandbox            SandboxInfo
	browser            BrowserInfo
	trusted            bool
	turnIndex          int
	snapshotter        Snapshotter

	// workspace is the session's current project/active-root pair and the
	// broker rebinds are published on. Guarded by mu like the rest of this
	// block. See workspace.go.
	workspace       Workspace
	workspaceBroker *pubsub.Broker[WorkspaceEvent]

	runningJobs            int
	subagentDepth          int
	subagentConcurr        int
	subagentMaxConcurrency int
	// subagents is the registry of subagent summary cards (see subagents.go)
	// surfaced in the parent transcript; subagentBroker publishes their
	// lifecycle events so the TUI re-renders without polling.
	subagents      []SubagentView
	subagentBroker *pubsub.Broker[SubagentEvent]
	// subagentDone holds a per-subagent channel that FinishSubagent closes;
	// WaitSubagent blocks on it. Entries are deleted on finish so a
	// finished subagent's Wait returns the stored view instead of
	// selecting on a closed channel.
	subagentDone map[int64]chan struct{}
	// runEvents is the plan-run event log: verify failures, review
	// findings, commits, retries. In-memory only, never persisted.
	runEvents []RunEvent
	// jobExits is the transcript record of background jobs finishing.
	// In-memory only, never persisted.
	jobExits  []JobExit
	turnUsage turnUsage
	title     string
	titleSet  bool

	scratchpadConfig config.ScratchpadConfig

	// Task 5: work gate for shutdown sequencing. workMu protects
	// quiescing and is paired with workWG so BeginQuiesce can set the
	// gate before WaitForWork begins polling the waitgroup.
	workMu    sync.Mutex
	workWG    sync.WaitGroup
	quiescing bool

	// loadErr captures a cold-load error from loadFromDB (Task 3).
	// Exposed via LoadError() so StartRuntime can abort existing-session
	// startup when the persisted transcript cannot be reconstructed.
	loadErr error

	// layers is the merged-config layering snapshot used by /doctor and
	// the status-line indicator. Populated by startRuntime after
	// LoadLayers. The zero value (LayerDefault for every path) lets
	// tests and zero-config sessions still call Layers().
	layers config.Layers

	// F16: steering queue (mid-turn user messages). Published to the broker
	// so the TUI transcript and status line update without polling.
	steeringQueue  []string
	steeringBroker *pubsub.Broker[SteeringEvent]

	// subagentReports is the machine-generated completion queue for
	// background agent.run children. It is deliberately separate from the
	// human steering queue: ClearSteering (turn-cancel, Ctrl+X) and
	// PopSteering (blank-Enter follow-up) must never drop a background
	// child's report. The runner drains it at loop-top alongside steering.
	subagentReports []string

	// F21: session event surface. Publishes message, streaming/thinking,
	// activity, tool lifecycle, audit, approval, and question events to
	// external subscribers (e.g. the ACP transport in a later task).
	// Publishing happens with State.mu released to avoid lock inversions
	// with subscribers; see publishEvent.
	eventBroker *pubsub.Broker[Event]

	// F14: append-only message tree.
	leafID     int64
	leafDBID   int64
	nextMsgID  int64
	parentOf   map[int64]int64
	childrenOf map[int64][]int64
	msgByID    map[int64]Message
	// dbIDToImID maps a persisted DB message id to the in-memory id
	// allocated for it at cold start. Only used during loadFromDB and set
	// to nil when the load completes. Holding s.mu around accesses.
	dbIDToImID map[int64]int64

	// T10: generation boundary for context rollover. BeginGeneration
	// records the current leaf message ID as StartMsgID so history
	// replay can scope correctly. A zero StartMsgID means replay
	// everything.
	generation GenerationInfo

	// sddGate is the current SDD human-gate state surfaced to the TUI.
	// The controller sets it; the TUI reads and clears it.
	sddGate SDDGate
}

// SDDGate is the open question a pipeline subagent raised. The controller
// sets it; the TUI renders it and collects the answer.
//
// TaskTitle and Report exist so the question can be answered in context: a
// bare QUESTION line asks the user to adjudicate a decision whose shape
// they cannot see. Report is the implementer's full report text.
type SDDGate struct {
	TaskN     int
	Question  string
	TaskTitle string
	Report    string
}

// GenerationInfo records where the live rollover generation begins.
// A zero StartMsgID means replay everything.
type GenerationInfo struct {
	ID         string
	Seq        int
	SeedDigest string
	StartMsgID int64
}

// BeginGeneration records the current leaf message ID as StartMsgID
// and stores the generation boundary. A zero StartMsgID means replay
// everything (no messages yet).
func (s *State) BeginGeneration(id string, seq int, seedDigest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var startMsgID int64
	if n := len(s.messages); n > 0 {
		startMsgID = s.messages[n-1].ID
	}
	s.generation = GenerationInfo{
		ID:         id,
		Seq:        seq,
		SeedDigest: seedDigest,
		StartMsgID: startMsgID,
	}
}

// Generation returns the stored generation boundary.
func (s *State) Generation() GenerationInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}

// SetSDDGate sets the current SDD human-gate state. The TUI reads this to
// render the gate prompt.
func (s *State) SetSDDGate(gate SDDGate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sddGate = gate
}

// ClearSDDGate clears the current SDD human-gate state (the human resolved).
func (s *State) ClearSDDGate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sddGate = SDDGate{}
}

// SDDGate returns the current SDD human-gate state (zero value if none).
func (s *State) SDDGate() SDDGate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sddGate
}

// SetSDDFallbackAllowedFiles stashes the controller's pending marshal.agent
// fallback allowlist on the session so the pipeline registry factory can
// apply FallbackWriterView during the next dispatch. The controller must
// call ClearSDDFallbackAllowedFiles once the dispatch returns; an empty
// slice is treated as no scope and the factory rejects the dispatch.
func (s *State) SetSDDFallbackAllowedFiles(allowed []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(allowed) == 0 {
		s.sddFallbackAllowed = nil
		return
	}
	cp := make([]string, len(allowed))
	copy(cp, allowed)
	s.sddFallbackAllowed = cp
}

// SDDFallbackAllowedFiles returns the controller's pending fallback
// allowlist, or nil when none is stashed. The pipeline registry factory
// uses this to narrow ScopeFallback to the declared paths.
func (s *State) SDDFallbackAllowedFiles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sddFallbackAllowed) == 0 {
		return nil
	}
	cp := make([]string, len(s.sddFallbackAllowed))
	copy(cp, s.sddFallbackAllowed)
	return cp
}

// ClearSDDFallbackAllowedFiles drops the stashed fallback allowlist.
func (s *State) ClearSDDFallbackAllowedFiles() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sddFallbackAllowed = nil
}

type turnUsage struct {
	used      int
	window    int
	threshold int
	source    string
}

func (s *State) SetTurnUsage(used int) {
	s.mu.Lock()
	s.turnUsage.used = used
	s.mu.Unlock()
}

func (s *State) SetTurnContextWindow(window int) {
	s.mu.Lock()
	s.turnUsage.window = window
	s.mu.Unlock()
}

// SetTurnBudget records the window and per-turn compaction threshold the
// runner resolved for this turn, plus where the threshold came from
// ("configured", "derived", or "fallback"). Surfaced by /context.
func (s *State) SetTurnBudget(window, threshold int, source string) {
	s.mu.Lock()
	s.turnUsage.window = window
	s.turnUsage.threshold = threshold
	s.turnUsage.source = source
	s.mu.Unlock()
}

func (s *State) TurnBudget() (window, threshold int, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnUsage.window, s.turnUsage.threshold, s.turnUsage.source
}

func (s *State) TurnUsage() (used, window int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnUsage.used, s.turnUsage.window
}

func (s *State) SetTitle(title string) {
	s.mu.Lock()
	s.title = title
	s.mu.Unlock()
}

func (s *State) SetTitleManual(title string) {
	s.mu.Lock()
	s.title = title
	s.titleSet = true
	s.mu.Unlock()
}

// SetTitleIfNotManual sets the title only if it has not been manually set
// (via SetTitleManual or /rename). Returns true if the title was set,
// false if a manual title prevented the update. This atomically combines
// the TitleManuallySet check and SetTitle call to prevent a TOCTOU race
// where a /rename arrives between the check and the set.
func (s *State) SetTitleIfNotManual(title string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.titleSet {
		return false
	}
	s.title = title
	return true
}

func (s *State) Title() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.title
}

func (s *State) TitleManuallySet() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.titleSet
}

// Option configures a State at construction time. Use WithDepth to override
// the default subagent nesting level (depth 0 = top-level agent) when
// building a child session.
type Option func(*State)

// WithDepth sets the subagent nesting depth for a new State. The default
// (zero value) is a top-level agent; a child subagent session passes its
// parent's depth + 1. The depth is fixed at construction and is consulted
// unchanged by EnterSubagent/ExitSubagent.
func WithDepth(d int) Option {
	return func(s *State) {
		s.subagentDepth = d
	}
}

// WithSubagentMaxConcurrency sets the cap on parallel agent.run children
// for this session, sourced from agent.max_concurrent_subagents. Values
// <= 0 keep the built-in default (3); values above 8 clamp to 8.
func WithSubagentMaxConcurrency(n int) Option {
	return func(s *State) {
		switch {
		case n <= 0:
			// keep the default
		case n > 8:
			s.subagentMaxConcurrency = 8
		default:
			s.subagentMaxConcurrency = n
		}
	}
}

// WithClock overrides the function used to stamp scratchpad entry
// timestamps. Tests pass a frozen clock so entries written back-to-back
// share an Updated value and ordering is deterministic; the default is
// time.Now.
func WithClock(now func() time.Time) Option {
	return func(s *State) {
		if now != nil {
			s.now = now
		}
	}
}

func New(cfg config.Config, workingDir string, now time.Time, p Persistence, opts ...Option) *State {
	ctx, cancel := context.WithCancel(context.Background())
	scratchpadCfg := cfg.Scratchpad
	scratchpadCfg.ApplyDefaults()
	s := &State{
		Config:                 cfg,
		WorkingDir:             workingDir,
		StartedAt:              now,
		now:                    time.Now,
		db:                     p.DB,
		sessionID:              p.SessionID,
		logger:                 p.Logger,
		ctx:                    ctx,
		cancel:                 cancel,
		turnToolCache:          make(map[string]registry.ToolResult),
		subagentDone:           make(map[int64]chan struct{}),
		activity:               Activity{Kind: ActivityIdle},
		activeSkills:           make(map[string]bool),
		loadedTools:            make(map[string]bool),
		parentOf:               make(map[int64]int64),
		childrenOf:             make(map[int64][]int64),
		msgByID:                make(map[int64]Message),
		dbIDToImID:             make(map[int64]int64),
		nextMsgID:              1,
		workspace:              Workspace{ProjectRoot: workingDir, ActiveRoot: workingDir},
		scratchpad:             make(map[string]db.ScratchpadEntry),
		scratchpadConfig:       scratchpadCfg,
		subagentMaxConcurrency: defaultSubagentMaxConcurrency,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.persistenceEnabled() {
		s.loadFromDB()
		s.restoreWorkspace()
	}
	return s
}

func (s *State) SessionID() string { return s.sessionID }

// discardLogger is allocated once and reused for all State instances that
// don't have an explicit logger set. Avoids allocating a new slog.Logger
// on every Logger() call.
var discardLogger = slog.New(slog.DiscardHandler)

// Logger returns the session logger, never nil. A State built without one
// (tests, and any caller that skips SetLogger) would otherwise hand back nil
// and panic at the call site — callers legitimately treat logging as always
// available, so the fallback belongs here rather than at each use.
func (s *State) Logger() *slog.Logger {
	if s.logger == nil {
		return discardLogger
	}
	return s.logger
}

func (s *State) SetTrusted(trusted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trusted = trusted
}

func (s *State) Trusted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trusted
}

func (s *State) TurnIndex() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnIndex
}

func (s *State) IncrementTurnIndex() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnIndex++
	return s.turnIndex
}

func (s *State) SetSnapshotter(sp Snapshotter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotter = sp
}

func (s *State) Snapshotter() Snapshotter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotter
}

func (s *State) DB() *db.DB {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db
}

// SetSandboxInfo records the active sandbox backend's capabilities snapshot.
// Called once by app.Run after sandbox.New; the TUI reads it for honest
// isolation rendering.
func (s *State) SetSandboxInfo(info SandboxInfo) {
	s.mu.Lock()
	s.sandbox = info
	s.mu.Unlock()
}

// SandboxInfo returns the recorded capability snapshot (zero-valued if no
// sandbox was set, e.g. in tests).
func (s *State) SandboxInfo() SandboxInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sandbox
}

// SetBrowserInfo records the current browser automation session state.
// Published on the broker so the TUI browser bar and status segment can
// react without polling.
func (s *State) SetBrowserInfo(info BrowserInfo) {
	s.mu.Lock()
	s.browser = info
	s.mu.Unlock()
	bi := info
	s.publishEvent(EventBrowserChanged, Event{Browser: &bi})
}

// BrowserInfo returns the current browser automation session snapshot.
func (s *State) BrowserInfo() BrowserInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.browser
}

// SetRunningJobsCount records how many background shell jobs are currently
// running. Updated by the native toolset's JobManager via OnChange.
func (s *State) SetRunningJobsCount(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runningJobs = n
}

// RunningJobsCount returns the number of background shell jobs currently
// marked as running.
func (s *State) RunningJobsCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runningJobs
}

// SetEventBroker wires the F21 session event broker. Setter calls made
// before this returns have no broker to publish to and are silently
// dropped (see publishEvent).
func (s *State) SetEventBroker(b *pubsub.Broker[Event]) {
	s.mu.Lock()
	s.eventBroker = b
	s.mu.Unlock()
}

// publishEvent reads the broker pointer under State.mu and releases the
// mutex before calling Publish. Holding the mutex across a Publish would
// create a lock inversion: subscribers may run synchronously under their
// own locks and could attempt to call back into State, deadlocking the
// publisher. Payloads must be safe copies; see Event.
func (s *State) publishEvent(typ string, payload Event) {
	s.mu.Lock()
	b := s.eventBroker
	s.mu.Unlock()
	if b != nil {
		b.Publish(typ, payload)
	}
}

// Subagent depth/concurrency bookkeeping. agent.run uses these to enforce the
// hard limits documented in Milestone P (depth 1). They live on the shared
// session state so any agent.run invocation — even concurrent ones from
// different Go routines — sees the same counters.
//
// Depth is a property of the session itself (set at construction via
// WithDepth): a top-level agent has depth 0; a child subagent launched from
// a depth-0 parent has depth 1; nested subsubagents would have depth 2.
// The depth guard rejects EnterSubagent when this session's depth has
// already hit the cap — defense in depth alongside SubtaskScopeView's
// filtering of agent.run out of the child's registry.
//
// Concurrency is a runtime counter on the parent session: the number of
// in-flight agent.run children the parent has launched. EnterSubagent
// increments it; ExitSubagent decrements it. The cap is configurable per
// session via WithSubagentMaxConcurrency (sourced from
// agent.max_concurrent_subagents); the default is 3.
const (
	subagentMaxDepth = 1
	// defaultSubagentMaxConcurrency is the built-in cap on parallel
	// agent.run children; per-session override via
	// WithSubagentMaxConcurrency (sourced from
	// agent.max_concurrent_subagents).
	defaultSubagentMaxConcurrency = 3
)

var (
	ErrSubagentDepthLimit       = fmt.Errorf("session: subagent depth limit exceeded (max %d)", subagentMaxDepth)
	ErrSubagentConcurrencyLimit = errors.New("session: subagent concurrency limit exceeded")
	ErrSessionQuiescing         = errors.New("session is quiescing")
)

// EnterSubagent validates the depth and concurrency guards for spawning a
// new subagent from this session. On success, the in-flight concurrency
// counter is incremented; depth is fixed at construction and never changes
// here. The caller MUST pair every successful EnterSubagent with an
// ExitSubagent (typically via defer) so the counter returns to its prior
// value when the subtask returns.
func (s *State) EnterSubagent() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subagentDepth >= subagentMaxDepth {
		return ErrSubagentDepthLimit
	}
	if s.subagentConcurr >= s.subagentMaxConcurrency {
		return fmt.Errorf("%w (max %d)", ErrSubagentConcurrencyLimit, s.subagentMaxConcurrency)
	}
	s.subagentConcurr++
	return nil
}

// ExitSubagent decrements the in-flight concurrency counter added by a prior
// successful EnterSubagent. Calling ExitSubagent without a matching
// EnterSubagent is a programming error and would emit a negative counter;
// callers must always pair the calls. Depth is unaffected.
func (s *State) ExitSubagent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subagentConcurr > 0 {
		s.subagentConcurr--
	}
}

// BeginWork registers that the caller is starting a unit of asynchronous
// work (typically a Bubble Tea command). After BeginQuiesce has been
// called, BeginWork returns ErrSessionQuiescing and the caller must not
// start the work. Every successful BeginWork must be paired with a
// matching EndWork call (typically via defer in the command closure).
func (s *State) BeginWork() error {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	if s.quiescing {
		return ErrSessionQuiescing
	}
	s.workWG.Add(1)
	return nil
}

// EndWork signals that a unit of work registered via BeginWork has
// completed. Calling EndWork without a matching BeginWork is a
// programming error that would panic via the WaitGroup.
func (s *State) EndWork() {
	s.workWG.Done()
}

// BeginQuiesce sets the quiescing gate so subsequent BeginWork calls
// are rejected. Callers should call BeginQuiesce before WaitForWork to
// ensure no new work can register between the gate and the wait.
func (s *State) BeginQuiesce() {
	s.workMu.Lock()
	s.quiescing = true
	s.workMu.Unlock()
}

// WaitForWork blocks until all in-flight work registered via BeginWork
// has completed via EndWork. It must be called after BeginQuiesce (the
// contract is documented, not enforced at runtime). If the context is
// cancelled before all work completes, WaitForWork returns the context's
// error.
func (s *State) WaitForWork(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.workWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ResolvePendingForShutdown atomically clears the pending approval,
// pending question, and steering queue for a graceful shutdown. It sends
// a denial on the approval channel (non-blocking), an AnswerUnanswered
// answer per question on the question channel (non-blocking), and
// publishes cleared-state events. The channels are closed on the send
// side after all values have been sent (or dropped) so the waiter can
// unblock safely.
//
// After this call, PendingApproval() and PendingQuestion() return nil
// and SteeringQueue() is empty.
func (s *State) ResolvePendingForShutdown() {
	s.mu.Lock()
	approval := s.pendingApproval
	s.pendingApproval = nil
	question := s.pendingQuestion
	s.pendingQuestion = nil
	s.steeringQueue = nil
	s.mu.Unlock()

	// Publish cleared-state events regardless of whether there was a
	// pending item — subscribers that never received the "set" event
	// are unaffected, and those that did get the cleared value.
	s.publishEvent(EventPendingApprovalChanged, Event{PendingApproval: nil})
	s.publishEvent(EventPendingQuestionChanged, Event{PendingQuestion: nil})
	broker := func() *pubsub.Broker[SteeringEvent] {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.steeringBroker
	}()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: 0})
	}

	// Deny any pending approval (Respond handles nil channel and
	// once-only guarantee).
	if approval != nil {
		approval.Respond(UserApprovalDecision{Approved: false})
	}

	// Answer every pending question with "Unanswered" (Respond handles
	// nil channel and once-only guarantee).
	if question != nil {
		question.Respond(UnansweredAnswers(question.Questions))
	}
}

// SubagentDepth returns the session's nesting depth — set once at
// construction via WithDepth. Exposed for diagnostics and tests.
func (s *State) SubagentDepth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subagentDepth
}

// SubagentConcurrency returns the number of agent.run invocations currently
// in flight on this session. Exposed for diagnostics and tests.
func (s *State) SubagentConcurrency() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subagentConcurr
}

// SubagentMaxConcurrency returns the session's configured cap on parallel
// agent.run children. Exposed for tool description generation and tests.
func (s *State) SubagentMaxConcurrency() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subagentMaxConcurrency
}

// SetSubagentConcurrency is a test-only override that lets unit tests
// simulate prior in-flight subagent entries from outside the goroutine
// that would normally hold the lock. Production code uses EnterSubagent.
// Depth is no longer settable here — use WithDepth at construction time.
func (s *State) SetSubagentConcurrency(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subagentConcurr = n
}

func (s *State) Shutdown() {
	// M-5: cancel all running subagents before cancelling the session
	// context so their completion goroutines exit promptly and don't
	// push reports into the old session's garbage queue. Also clear the
	// report queue so any late reports are discarded rather than ending
	// up in a transcript nobody drains.
	s.mu.Lock()
	for _, v := range s.subagents {
		if v.Status == SubagentRunning && v.Cancel != nil {
			v.Cancel()
		}
	}
	s.subagentReports = nil
	s.mu.Unlock()
	s.cancel()
}

func (s *State) Done() <-chan struct{} {
	return s.ctx.Done()
}

// Context returns the session-lifetime context. It is cancelled only by
// Shutdown, so work that must outlive a single turn or tool call —
// background subagents — derives from it rather than from a turn context.
func (s *State) Context() context.Context {
	return s.ctx
}

// LoadError returns any error that occurred during the cold-load of
// persisted state from the database (loadFromDB). A nil return means
// the in-memory state was reconstructed successfully or the session
// had no persisted messages. StartRuntime uses this to detect a broken
// existing session and abort startup.
func (s *State) LoadError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

// SetLayers stores the merged-config layering snapshot. Called once by
// startRuntime after LoadLayers; safe to call again from test code.
func (s *State) SetLayers(l config.Layers) {
	s.mu.Lock()
	s.layers = l
	s.mu.Unlock()
}

// Layers returns the layering snapshot, or the zero value if none was
// stored (e.g. in unit tests that build a State directly).
func (s *State) Layers() config.Layers {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.layers
}

func (s *State) SetContextPack(pack contextpack.Pack) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextPack = pack.Clone()
}

func (s *State) ContextPack() contextpack.Pack {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contextPack.Clone()
}

// UpdateContextPack atomically applies fn to the current pack and stores the
// result. Use it for every read-modify-write pack update: the
// ContextPack/SetContextPack pair is not atomic, and concurrent writers
// (swarm role runners sharing one State, index-completion re-seeds) can
// otherwise lose each other's sections.
func (s *State) UpdateContextPack(fn func(contextpack.Pack) contextpack.Pack) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextPack = fn(s.contextPack.Clone()).Clone()
}

func (s *State) SetActiveRoute(route RouteInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeRoute = route
}

func (s *State) ActiveRoute() RouteInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeRoute
}

func (s *State) SetActivity(a Activity) {
	s.mu.Lock()
	if a.Kind == "" {
		a.Kind = ActivityIdle
	}
	s.activity = a
	s.mu.Unlock()
	s.publishEvent(EventActivityChanged, Event{Activity: &a})
}

func (s *State) Activity() Activity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activity
}

func (s *State) SetToolBudget(b ToolBudget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolBudget = b
}

func (s *State) ToolBudget() ToolBudget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolBudget
}

func (s *State) SetPlan(plan []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plan = make([]string, len(plan))
	copy(s.plan, plan)
}

func (s *State) Plan() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := make([]string, len(s.plan))
	copy(p, s.plan)
	return p
}

func (s *State) SetTodos(todos []db.TodoItem) error {
	s.mu.Lock()
	s.todos = make([]db.TodoItem, len(todos))
	copy(s.todos, todos)
	snapshot := s.todos
	s.mu.Unlock()
	if s.persistenceEnabled() {
		if err := s.db.SaveTodos(s.sessionID, snapshot); err != nil {
			s.logger.Warn("failed to persist todos", "error", err)
		}
	}
	return nil
}

func (s *State) Todos() []db.TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]db.TodoItem, len(s.todos))
	copy(result, s.todos)
	return result
}

// SetScratchpadEntry upserts a single entry by key. Empty keys or content are
// rejected, and entries larger than MaxEntryTokens are refused. After the
// upsert the scratchpad budget is enforced by evicting oldest entries first.
// The entry is persisted to the scratchpad_entries table.
func (s *State) SetScratchpadEntry(key, content, format string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("scratchpad key must not be empty")
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("scratchpad content must not be empty")
	}
	if format == "" {
		format = "text"
	}

	tokens := contextpack.EstimateTokens(content)
	if tokens > s.scratchpadConfig.MaxEntryTokens {
		return fmt.Errorf("scratchpad entry %q is ~%d tokens, exceeds max %d", key, tokens, s.scratchpadConfig.MaxEntryTokens)
	}

	entry := db.ScratchpadEntry{
		Key:       key,
		Content:   content,
		Format:    format,
		Updated:   s.now().UnixMilli(),
		SizeBytes: len(content),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	preSnapshot := s.scratchpadSnapshotLocked()
	s.scratchpad[key] = entry
	s.enforceScratchpadBudgetLocked()
	var evictedKeys []string
	for _, e := range preSnapshot {
		if _, ok := s.scratchpad[e.Key]; !ok {
			evictedKeys = append(evictedKeys, e.Key)
		}
	}
	// The new key is not in preSnapshot. If enforcement self-evicted it,
	// make sure any DB row for this key (including a previous value) is
	// removed and do not re-save it.
	if _, ok := s.scratchpad[key]; !ok && !slices.Contains(evictedKeys, key) {
		evictedKeys = append(evictedKeys, key)
	}

	if s.persistenceEnabled() {
		// Only persist the new value if it survived enforcement.
		if _, ok := s.scratchpad[key]; ok {
			if err := s.db.SaveScratchpadEntry(s.sessionID, entry); err != nil {
				s.logger.Warn("failed to persist scratchpad entry", "error", err)
			}
		}
		for _, evictedKey := range evictedKeys {
			if err := s.db.DeleteScratchpadEntry(s.sessionID, evictedKey); err != nil {
				s.logger.Warn("failed to delete evicted scratchpad entry", "error", err, "key", evictedKey)
			}
		}
	}
	s.Logger().Debug("scratchpad write",
		"key", key,
		"tokens", tokens,
		"total_entries", len(s.scratchpad),
	)
	return nil
}

// DeleteScratchpadEntry removes a single entry by key. Returns nil if the
// key does not exist (idempotent).
func (s *State) DeleteScratchpadEntry(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, existed := s.scratchpad[key]
	delete(s.scratchpad, key)

	if existed {
		s.Logger().Debug("scratchpad delete", "key", key)
	}

	if existed && s.persistenceEnabled() {
		if err := s.db.DeleteScratchpadEntry(s.sessionID, key); err != nil {
			s.logger.Warn("failed to delete scratchpad entry", "error", err, "key", key)
		}
	}
	return nil
}

// Scratchpad returns a defensive copy of all entries sorted newest first.
func (s *State) Scratchpad() []db.ScratchpadEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scratchpadSnapshotLocked()
}

// ScratchpadEntry returns a single entry by key, or ok=false if not found.
func (s *State) ScratchpadEntry(key string) (db.ScratchpadEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.scratchpad[key]
	s.Logger().Debug("scratchpad read", "key", key, "hit", ok)
	return e, ok
}

// ScratchpadConfig returns the applied scratchpad configuration for this
// session. Defaults are populated by New, so the returned value is always
// usable.
func (s *State) ScratchpadConfig() config.ScratchpadConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scratchpadConfig
}

// scratchpadSnapshotLocked returns a defensive copy of all entries sorted
// newest first so callers get deterministic order.
func (s *State) scratchpadSnapshotLocked() []db.ScratchpadEntry {
	out := make([]db.ScratchpadEntry, 0, len(s.scratchpad))
	for _, e := range s.scratchpad {
		out = append(out, e)
	}
	// Sort newest first so callers get deterministic order. Entries come out
	// of a map, so there is no insertion order for a stable sort to preserve:
	// equal Updated values must be broken by key, or callers that truncate
	// the projection (the context pack) drop a different entry each run.
	slices.SortFunc(out, func(a, b db.ScratchpadEntry) int {
		if a.Updated > b.Updated {
			return -1
		}
		if a.Updated < b.Updated {
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

// enforceScratchpadBudgetLocked evicts oldest entries first until the
// scratchpad satisfies both MaxEntries and MaxTotalTokens.
func (s *State) enforceScratchpadBudgetLocked() {
	if len(s.scratchpad) == 0 {
		return
	}

	// Build a list oldest-first by Updated.
	entries := make([]db.ScratchpadEntry, 0, len(s.scratchpad))
	for _, e := range s.scratchpad {
		entries = append(entries, e)
	}
	// Entries come out of a map, so there is no insertion order for a stable
	// sort to preserve: equal Updated values must be broken by key or the
	// eviction victim is random. This is the exact reverse of
	// scratchpadSnapshotLocked's ordering, so "oldest" here always means the
	// last entry a caller would see there.
	slices.SortFunc(entries, func(a, b db.ScratchpadEntry) int {
		if a.Updated < b.Updated {
			return -1
		}
		if a.Updated > b.Updated {
			return 1
		}
		return strings.Compare(b.Key, a.Key)
	})

	totalTokens := 0
	for _, e := range s.scratchpad {
		totalTokens += contextpack.EstimateTokens(e.Content)
	}

	for _, e := range entries {
		if len(s.scratchpad) <= s.scratchpadConfig.MaxEntries && totalTokens <= s.scratchpadConfig.MaxTotalTokens {
			break
		}
		s.Logger().Debug("scratchpad eviction", "key", e.Key)
		delete(s.scratchpad, e.Key)
		totalTokens -= contextpack.EstimateTokens(e.Content)
	}
}

func (s *State) AddSessionRule(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionRules = append(s.sessionRules, prefix)
}

func (s *State) SessionRules() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	rules := make([]string, len(s.sessionRules))
	copy(rules, s.sessionRules)
	return rules
}

func (s *State) LogToolCall(event registry.AuditEvent) {
	s.mu.Lock()
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	s.auditLog = append(s.auditLog, event)
	// Also accumulate into the per-turn ledger buffer. The compaction in
	// summary string and the (ok) marker are filled in here so the
	// ledger stays a one-line summary, while auditLog retains the full
	// event for inspection.
	entry := db.ToolAuditEntry{
		Seq:     len(s.toolAuditThisTurn) + 1,
		Tool:    event.ToolName,
		Summary: ledgerSummaryFor(event),
		Ok:      event.Error == "" && event.Approval != registry.ApprovalDenied,
		Tokens:  estimateAuditTokens(event),
	}
	if event.ToolName == "agent.run" {
		entry.Content = strutil.Truncate(event.ResultContent, subagentReportCap, false)
	}
	s.toolAuditThisTurn = append(s.toolAuditThisTurn, entry)
	published := event
	s.mu.Unlock()

	s.publishEvent(EventAuditAdded, Event{Audit: &published})

	if s.persistenceEnabled() {
		if err := s.db.SaveToolCall(s.sessionID, event); err != nil {
			s.logger.Error("save tool call failed", "error", err, "session_id", s.sessionID, "tool", event.ToolName)
		}
	}
}

// ledgerSummaryFor condenses a full AuditEvent into the one-line form
// used by the cross-turn ledger. It prefers ResultSummary (set by the
// tool handler) and falls back to the tool name when that is empty.
func ledgerSummaryFor(ev registry.AuditEvent) string {
	if ev.ResultSummary != "" {
		return ev.ResultSummary
	}
	return ev.ToolName
}

func estimateAuditTokens(ev registry.AuditEvent) int {
	return estimateTokensForAuditEvent(ev)
}

func (s *State) AuditLog() []registry.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	log := make([]registry.AuditEvent, len(s.auditLog))
	copy(log, s.auditLog)
	return log
}

func (s *State) Transcript() []TranscriptItem {
	s.mu.Lock()
	defer s.mu.Unlock()

	items := make([]TranscriptItem, 0, len(s.messages)+len(s.auditLog)+len(s.thinkingLog))

	for i := range s.messages {
		msg := s.messages[i]
		items = append(items, TranscriptItem{
			Timestamp: msg.CreatedAt,
			Kind:      KindMessage,
			Message:   &msg,
		})
	}

	for i := range s.auditLog {
		evt := s.auditLog[i]
		items = append(items, TranscriptItem{
			Timestamp: evt.Timestamp,
			Kind:      KindAudit,
			Audit:     &evt,
		})
	}

	for i := range s.thinkingLog {
		t := s.thinkingLog[i]
		items = append(items, TranscriptItem{
			Timestamp: t.StartedAt,
			Kind:      KindThinking,
			Thinking:  &t,
		})
	}

	for i := range s.subagents {
		v := s.subagents[i]
		// Refresh the completed-tool-call count from the live child transcript
		// so the running card can show "N tool calls" without a separate poll.
		if v.Child != nil {
			v.ToolCalls = v.Child.CompletedToolCallCount()
			s.subagents[i].ToolCalls = v.ToolCalls
			v.CurrentTool = v.Child.CurrentToolLabel()
			s.subagents[i].CurrentTool = v.CurrentTool
		}
		items = append(items, TranscriptItem{
			Timestamp: v.StartedAt,
			Kind:      KindSubagent,
			Subagent:  &v,
		})
	}

	for i := range s.runEvents {
		ev := s.runEvents[i]
		items = append(items, TranscriptItem{
			Timestamp: ev.At,
			Kind:      KindRunEvent,
			RunEvent:  &ev,
		})
	}

	for i := range s.jobExits {
		e := s.jobExits[i]
		items = append(items, TranscriptItem{
			Timestamp: e.At,
			Kind:      KindJobExit,
			JobExit:   &e,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		ti := items[i].Timestamp
		tj := items[j].Timestamp
		if ti.IsZero() || tj.IsZero() {
			return !ti.IsZero() && tj.IsZero()
		}
		return ti.Before(tj)
	})
	return items
}

// ClearToolCache drops every cached tool result. Called when a tool
// mutates the workspace: a stale read presented as current is worse
// than re-running the read.
func (s *State) ClearToolCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnToolCache = make(map[string]registry.ToolResult)
	s.toolCacheOrder = nil
	s.toolCacheBytes = 0
}

func (s *State) GetTurnToolResult(toolName string, normalizedArgs []byte) (registry.ToolResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := toolName + "|" + string(normalizedArgs)
	result, ok := s.turnToolCache[key]
	return result, ok
}

func (s *State) SetTurnToolResult(toolName string, normalizedArgs []byte, result registry.ToolResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := toolName + "|" + string(normalizedArgs)
	if old, ok := s.turnToolCache[key]; ok {
		s.toolCacheBytes -= len(old.Content)
		s.toolCacheOrder = slices.DeleteFunc(s.toolCacheOrder, func(k string) bool { return k == key })
	}
	s.turnToolCache[key] = result
	s.toolCacheOrder = append(s.toolCacheOrder, key)
	s.toolCacheBytes += len(result.Content)
	s.evictToolCacheLocked()
}

// evictToolCacheLocked drops oldest-first until both bounds hold. The
// newest entry is never evicted, even when it alone exceeds the byte
// bound — the caller just produced it.
func (s *State) evictToolCacheLocked() {
	for len(s.toolCacheOrder) > maxToolCacheEntries ||
		(s.toolCacheBytes > maxToolCacheBytes && len(s.toolCacheOrder) > 1) {
		oldest := s.toolCacheOrder[0]
		s.toolCacheOrder = s.toolCacheOrder[1:]
		if e, ok := s.turnToolCache[oldest]; ok {
			s.toolCacheBytes -= len(e.Content)
			delete(s.turnToolCache, oldest)
		}
	}
}

func (s *State) ActivateSkill(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeSkills[name] = true
}

// ActiveSkills returns the active skill names in sorted order.
//
// The sort is load-bearing, not cosmetic: this slice renders into the system
// prompt (agent/prompts.go, "## Active Skills"), which is messages[0] — the
// provider's cache prefix. Prefix caching requires byte-identical bytes, so
// returning map order meant that with two or more active skills the prompt
// could reorder on any rebuild and silently miss the cache, on a tool that
// measures exactly that cost (db.turn_metrics.cache_read_tokens).
func (s *State) ActiveSkills() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.activeSkills))
	for name := range s.activeSkills {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *State) HasActiveSkill(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeSkills[name]
}

// LoadedToolNames returns a sorted copy of the deferred tool names the
// agent has explicitly opted into via tools.select during this session.
// The agent prompt builder uses this to expand the deferred tool list back
// into the prompt once the agent confirms it needs a particular tool. This
// covers both native deferred tools (config.*, csv.inspect, json.query) and
// MCP tools.
func (s *State) LoadedToolNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.loadedTools))
	for name := range s.loadedTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AddLoadedToolNames records that the agent has opted into the given
// deferred tool names for the remainder of the session. Names are
// de-duplicated; unknown names are accepted without error so callers can
// pass through the full requested set.
func (s *State) AddLoadedToolNames(names []string) {
	if len(names) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		if name == "" {
			continue
		}
		s.loadedTools[name] = true
	}
}
