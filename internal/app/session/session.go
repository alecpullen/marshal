package session

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/pubsub"
	"marshal/internal/redact"
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
	EventMessageAdded            = "message_added"
	EventThinkingChanged         = "thinking_changed"
	EventActivityChanged         = "activity_changed"
	EventActiveToolChanged       = "active_tool_changed"
	EventAuditAdded              = "audit_added"
	EventPendingApprovalChanged  = "pending_approval_changed"
	EventPendingQuestionChanged  = "pending_question_changed"
	EventPendingSkillGateChanged = "pending_skill_gate_changed"
	EventPendingSudoAuthChanged  = "pending_sudo_auth_changed"
	EventBrowserChanged          = "browser_changed"
	EventStepChanged             = "step_changed"
	EventHoldChanged             = "hold_changed"
	EventEffortChanged           = "effort_changed"
	EventJobExit                 = "job_exit"
	// EventSteeringState carries a steering snapshot (queue depth, active
	// turn, current operation, pending count). EventSteeringReceipt carries
	// one request's settled receipt. Both are additive: a client that does
	// not know them ignores them.
	EventSteeringState   = "steering_state"
	EventSteeringReceipt = "steering_receipt"
)

// Event is the union payload published on the session event broker
// (F21). Only the field relevant to Type is populated; the others remain
// nil. Payloads passed to Publish must be safe copies (no shared pointers
// to internal state that may be mutated by concurrent writers).
//
// PendingApproval and PendingQuestion carry a display copy of the pending
// request. A registered prompt's copy carries the canonical key and responder
// and never a response channel, so a subscriber cannot answer or clear a
// request on its own; only an unregistered legacy fixture still carries
// ResponseChan.
type Event struct {
	Message          *Message
	Thinking         *InProgressMessage
	Activity         *Activity
	ActiveTool       *ActiveToolCall
	Audit            *registry.AuditEvent
	PendingApproval  *PendingToolCall
	PendingQuestion  *PendingQuestion
	PendingSkillGate *PendingSkillGate
	// PendingSudoAuth carries a privileged-command authentication prompt.
	// The snapshot deliberately carries only ResponseChan — never a password
	// — so no subscriber can observe a credential.
	PendingSudoAuth *PendingSudoAuth
	Browser         *BrowserInfo
	// Step carries a step that began or ended (EventStepChanged).
	Step *Step
	// Held carries the new hold state (EventHoldChanged).
	Held bool
	// JobExit carries a background job's exit (EventJobExit).
	JobExit *JobExit
	// Steering carries a steering snapshot (EventSteeringState): queue
	// depth, the active turn, the current operation and the pending count.
	// SteeringReceipt carries one request's settled receipt
	// (EventSteeringReceipt). Both are deep copies, so a subscriber cannot
	// mutate session state through them.
	Steering        *SteeringSnapshot
	SteeringReceipt *SteeringReceipt
	// Effort carries an effort override or application (EventEffortChanged).
	Effort *EffortChange
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
	// Thinking is the resolved reasoning-effort level for the active route
	// ("" = provider default). Mirrored from Route.Preset.Thinking so the
	// TUI can show the effort flag without reaching into routing.
	Thinking string
	// ReasoningCapable mirrors the serving provider's Capabilities().Reasoning
	// bit — the same gate the chat path applies before putting the effort
	// field on the wire (chat.go). False means the preset effort is inert
	// (e.g. native Ollama, whose think toggle is a separate mechanism), so
	// display surfaces must not present it as active.
	ReasoningCapable bool
	Active           bool
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

	mu                sync.Mutex
	hold              bool          // guarded by mu; see hold.go
	holdCh            chan struct{} // closed and cleared on release; guarded by mu
	messages          []Message
	inProgress        InProgressMessage
	notice            Notice
	noticeSet         bool
	pendingApproval   *PendingToolCall
	pendingQuestion   *PendingQuestion
	pendingSkillGate  *PendingSkillGate
	skillGateDisabled bool
	// pendingSudoAuth is the in-flight privileged-command authentication
	// prompt. Single-valued: sudoMu serializes shell.sudo session-wide so a
	// second concurrent call cannot clobber it.
	pendingSudoAuth *PendingSudoAuth
	// pendingAccessCtx and pendingAccessIntent are the original request
	// context and the full intent behind the canonical typed pending
	// approval. They are guarded by accessMu: the pending pointer itself
	// also lives under mu, but only the access gate may settle it.
	pendingAccessCtx    context.Context
	pendingAccessIntent AccessIntent
	// prompts is the canonical registry of live human-request prompts
	// (approval/question/skill/sudo), keyed by PromptKey.ID. Guarded by
	// accessMu: the same gate that arbitrates a user answer against an
	// accepted steering cancellation. See prompts.go.
	prompts map[string]*promptRecord
	// secretPromptCapable reports whether the attached transport can raise a
	// masked secret prompt. Defaults false (fail closed): an ACP/web client
	// has no secret channel, so shell.sudo refuses rather than blocking on a
	// prompt no transport can answer. Set true only by the TUI wiring.
	secretPromptCapable bool
	// sudoUnlock is the opt-in sudo timestamp-grace mode (omit -k). Runtime
	// only, never a config default; sticky until toggled off.
	sudoUnlock bool
	// sudoMu serializes privileged commands session-wide. The pending-auth
	// slot is single-valued, so two concurrent shell.sudo calls would clobber
	// each other; the lock is held across prompt + execute.
	sudoMu           sync.Mutex
	skillGateAllowed map[string]bool
	skillGateDenied  map[string]int
	// activeToolCall is the latest in-flight call; activeTools holds every
	// one by ToolCallID (concurrent calls in envelope mode).
	activeToolCall *ActiveToolCall
	activeTools    map[string]*ActiveToolCall
	// steps is every step created or restored in this session, in sequence
	// order; nextStepSeq is the next sequence number to hand out.
	steps        []Step
	nextStepSeq  int64
	sessionRules []string
	auditLog     []registry.AuditEvent
	thinkingLog  []ThinkingEntry
	lastBackup   []BackupFile
	contextPack  contextpack.Pack
	// requestInspection is the bounded snapshot of the last conversation
	// attempt Marshal submitted to its provider adapter. It is in-memory only
	// (see request_inspection.go): nothing persists it, and it is never logged.
	requestInspection *RequestInspection
	activeRoute       RouteInfo
	turnToolCache     map[string]registry.ToolResult
	toolCacheOrder    []string
	toolCacheBytes    int
	activity          Activity
	plan              []string
	todos             []db.TodoItem
	activeSkills      map[string]bool
	// skillOrder records activation sequence per skill so eviction can pick
	// the least-recently-activated. Kept separate from activeSkills because
	// ActiveSkills() must stay alphabetically sorted for prefix caching —
	// see the comment on ActiveSkills.
	skillOrder map[string]int64
	skillSeq   int64
	// skillBodyAge tracks how many turns have elapsed since a skill's body
	// was last sent in full. Used by appendSkillBodies to degrade to a stub.
	skillBodyAge map[string]int
	loadedTools  map[string]bool
	toolBudget   ToolBudget
	// toolAuditThisTurn is the per-turn accumulator for the cross-turn
	// ledger (Task 3/A1). LogToolCall appends here as well as to
	// auditLog, and appendMessage flushes it to the turn_tool_audit table
	// when an assistant final message persists. Best-effort: failures are
	// logged and do not abort the turn.
	toolAuditThisTurn []db.ToolAuditEntry
	// interruptedTurnNote is a one-shot pending note describing where an
	// interrupted (Esc-cancelled) turn stopped. Set at cancel time,
	// consumed and cleared at the next turn's start (see interrupted.go).
	interruptedTurnNote string
	swarmProgress       SwarmProgress
	sddProgress         SDDProgress
	// sddFallbackAllowed is the controller's pending marshal.agent
	// fallback allowlist, stashed so the pipeline registry factory can
	// narrow the next ScopeFallback dispatch to the declared paths.
	// Empty/nil means no scope is declared; the factory must reject.
	sddFallbackAllowed []string
	sandbox            SandboxInfo
	browser            BrowserInfo
	trusted            bool
	// systemAccess is the per-session system-access modifier (spec §2):
	// full-filesystem read/write via file tools, lifted prompt-level
	// shell-editing ban, and catastrophic-floor guardrails. Runtime-only;
	// never a config default. Sticky until toggled off. Guarded by mu.
	systemAccess bool
	// effortOverride is the session's unsaved reasoning-effort override,
	// guarded by mu.
	effortOverride EffortOverride
	// turnRoute is the route the running turn uses (Thinking = effective
	// effort); turnRouteSet is false between turns. Guarded by mu.
	turnRoute    RouteInfo
	turnRouteSet bool
	// accessMu serializes access-tuple mutations (session mode changes,
	// system-access toggles, config rebases) so a reader never observes a
	// half-applied pair. It guards configuredMode and sessionModeOverride;
	// the effective pair itself still lives under mu. Lock order is
	// accessMu → workMu or accessMu → mu, never the reverse.
	accessMu sync.Mutex
	// configuredMode is the normalized approval mode the current config
	// asked for, i.e. the mode a reload keeps unless the user explicitly
	// changed it. Guarded by accessMu.
	configuredMode string
	// sessionModeOverride is the user's session-scoped mode selection. It
	// outlives an unrelated config reload but is cleared when the configured
	// mode itself changes. Empty when no manual/approved selection exists.
	// Guarded by accessMu.
	sessionModeOverride string
	turnIndex           int
	snapshotter         Snapshotter

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
	// subagentEpoch is the conversation's single opaque runtime epoch for all
	// subagent work, minted once with crypto/rand.Text. Every work-index entry
	// carries it, so a restored entry can be told from one this runtime
	// produced without parsing any id. Guarded by mu.
	subagentEpoch string
	// subagentRevision is the subagent registry's source revision, advanced on
	// every register/update/finish/cancel change. A snapshot publishes it so a
	// subscriber can discard a stale projection. Guarded by mu.
	subagentRevision uint64
	// subagentSettling marks, by agent id, a terminal settlement whose first
	// serialized final-index save attempt has not completed yet. Guarded by mu.
	subagentSettling map[int64]bool
	// subagentSaveErrors records, by agent id, a subagent work-index save
	// failure and the source revision it belonged to, so a save that covers
	// that revision clears exactly the errors it covered. Guarded by mu.
	subagentSaveErrors map[int64]subagentSaveFailure
	// runEvents is the plan-run event log: verify failures, review
	// findings, commits, retries. In-memory only, never persisted.
	runEvents []RunEvent
	// jobExits is the transcript record of background jobs finishing,
	// persisted in session_state and restored on resume (see jobexit.go).
	jobExits []JobExit
	// jobExitSaveMu serializes the snapshot-and-write sequence that stores
	// job exits and guards jobExitsPending. It is deliberately separate from
	// mu: the write spans a database round trip, which must not be held while
	// the rest of the session waits, and two saves that interleaved a
	// snapshot with a write could store an older list over a newer one.
	jobExitSaveMu sync.Mutex
	// jobExitsPending marks a job-exit list that is recorded in memory but
	// could not be written durably, so a later caller can retry it instead of
	// leaving a settled card that never comes back after a resume.
	jobExitsPending bool
	// jobExitsUnpublished holds completions recorded but not yet announced,
	// keyed by execution identity: their event is published only once their
	// durable write succeeds, so no client re-projects a card whose outcome the
	// session cannot restore. A retried settlement replaces its own entry
	// instead of announcing the same completion twice. Guarded by
	// jobExitSaveMu.
	jobExitsUnpublished map[string]JobExit
	// jobExitsPublished is the set of identities whose completion event has
	// been announced, so a duplicate delivery or a retry after success
	// announces nothing. It grows with the session's completions, like
	// jobExits itself. Guarded by jobExitSaveMu.
	jobExitsPublished map[string]bool

	// workView is the session's versioned work-view index: the settled watch
	// results and the acknowledgments that hide their attention. The zero
	// value is the empty version-1 view; a stored view is copied in at
	// startup. Guarded by mu for reads and mutations; the save sequence takes
	// workViewSaveMu and snapshots it under mu. See jobexit.go.
	workView db.WorkViewRecord
	// workViewSaveMu serializes the work-view snapshot-and-write sequence, for
	// the same reason jobExitSaveMu does for job exits: the write spans a
	// database round trip that must not be held under mu, and two saves that
	// interleaved a snapshot with a write could store an older view over a
	// newer one. Lock order is workViewSaveMu → mu, never the reverse.
	workViewSaveMu sync.Mutex
	// workViewDirty marks a work view that changed in memory but could not be
	// written durably, so a later caller can retry it rather than leaving a
	// result the next resume cannot restore. Guarded by workViewSaveMu.
	workViewDirty bool
	// workViewPendingAcks holds acknowledgments validated against the live view
	// but not yet committed durably. A failed save leaves the acknowledgment
	// here — its result's attention stays visible — and the retry re-applies it
	// to the newest snapshot. Guarded by workViewSaveMu.
	workViewPendingAcks []db.WorkAcknowledgment
	// workViewLoadErr records that the stored view could not be loaded (an
	// unknown stored version or corrupt JSON). It is a diagnostic: the stored
	// value is left exactly as it is and never overwritten automatically, so
	// every further save refuses rather than clobbering data the session cannot
	// understand. Guarded by workViewSaveMu.
	workViewLoadErr error

	turnUsage turnUsage
	// turnQuota is the latest subscription quota reading from an
	// OAuth-backed provider; nil when the active provider reports none.
	turnQuota *schema.QuotaInfo
	title     string
	titleSet  bool

	// Task 5: work gate for shutdown sequencing. workMu protects
	// quiescing and is paired with workWG so BeginQuiesce can set the
	// gate before WaitForWork begins polling the waitgroup.
	workMu    sync.Mutex
	workWG    sync.WaitGroup
	quiescing bool

	// Task 5: the registered operation stop group — the target an explicit
	// turn stop reaches. turnStopService is the conversation's command
	// service, installed by app construction; turnStopPendingCause retains a
	// stop requested before the turn it belongs to registered (Ctrl+C landing
	// while a turn is still starting up must not be lost). turnStopMu is a
	// leaf lock: no callback and no Service call ever runs while it is held.
	turnStopMu           sync.Mutex
	turnStop             *TurnStopToken
	turnStopPendingCause string
	turnStopService      TurnStopService
	// subagentStopGroups maps an agent.run child's view id to that child's own
	// stop group, so a targeted stop of one child reaches only that child's
	// attached commands. Guarded by mu, alongside the subagent registry.
	subagentStopGroups map[int64]*TurnStopToken
	// subagentWorkItems is the FIFO of child-creation work items EnterSubagent
	// registered and ExitSubagent has not released yet. Pairing by count keeps
	// every registration released exactly once even when several children start
	// and finish at once: one EnterSubagent, one item, one ExitSubagent.
	// Guarded by mu.
	subagentWorkItems []WorkID

	// end is the conversation's end controller: the registry of active work
	// and the exit phase. It is a value field with a mutex of its own, so a
	// State built as a literal still has a working controller, and it is the
	// OUTERMOST conversation lock (see the conversation-end section below).
	end endController

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
	// so the TUI transcript and status line update without polling. Entries
	// are typed so every queued message has a stable internal ID: the
	// Send now controller tracks which entries one accepted request covers,
	// and a string-only queue could not tell two identical texts apart.
	steeringQueue  []SteeringMessage
	steeringBroker *pubsub.Broker[SteeringEvent]

	// steeringMu guards the Send now controller below. Lock order is
	// accessMu → steeringMu → State.mu, with no inverse acquisition; the
	// controller's snapshot/event publication and its callbacks all run
	// outside these locks.
	steeringMu sync.Mutex
	steering   steeringControl

	// subagentReports is the machine-generated completion queue for
	// background agent.run children. It is deliberately separate from the
	// human steering queue: ClearSteering (turn-cancel, Ctrl+X) and
	// PopSteering (blank-Enter follow-up) must never drop a background
	// child's report. The runner drains it at loop-top alongside steering.
	subagentReports []string

	// watchReports is the machine-generated completion queue for
	// background watch children. It is deliberately separate from the
	// human steering queue: ClearSteering (turn-cancel, Ctrl+X) and
	// PopSteering (blank-Enter follow-up) must never drop a background
	// child's report. The runner drains it at loop-top alongside steering.
	// Entries are coalesced by watch ID (see watch_reports.go) so a watch
	// that fires repeatedly while idle folds into one pending entry.
	watchReports []watchReportEntry

	// watchResumeName/watchResumeRepeat are the one-slot auto-resume
	// latch (see watch_resume.go). Armed by the runner's turn-end residual
	// drain; consumed at each runtime's idle boundary.
	watchResumeName   string
	watchResumeRepeat bool

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

	// spinnerLabelDwell holds the pinned activity label for the turn
	// spinner row, with a minimum dwell time so fast-cycling tool labels
	// don't flicker too fast to read. Lives on State (not the TUI Model)
	// because View() is a value receiver — Model-level state is discarded
	// every frame.
	spinnerLabelPinned     string
	spinnerLabelPinnedAt   time.Time
	spinnerLabelPinnedKind ActivityKind

	// modelCacheDir is the shared data directory holding the on-disk model
	// discovery cache (internal/llm/provider/modelcache). The agent runner
	// reads it to enrich the system-prompt roster with fresh probe results
	// so subagent model options reflect what providers actually serve, not
	// only what is pinned in config presets. Empty means no cache dir is
	// known (tests, or discovery never configured) and the roster omits
	// discovered entries.
	modelCacheDir string

	// homeDir is the user's home directory, recorded so in-session layer
	// reloads (config.LoadSessionLayers) can locate the user config layer
	// without each caller re-deriving it. Empty means unknown (tests).
	homeDir string
}

// SetModelCacheDir records the data directory that holds the on-disk model
// discovery cache. Called once by buildAgentRunnerWithLock at startup and
// config reload.
func (s *State) SetModelCacheDir(dir string) {
	s.mu.Lock()
	s.modelCacheDir = dir
	s.mu.Unlock()
}

// ModelCacheDir returns the recorded model discovery cache directory
// (empty when never set).
func (s *State) ModelCacheDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelCacheDir
}

// SetHomeDir records the user's home directory for in-session layer
// reloads. Called once by buildAgentRunnerWithLock at startup.
func (s *State) SetHomeDir(dir string) {
	s.mu.Lock()
	s.homeDir = dir
	s.mu.Unlock()
}

// HomeDir returns the recorded home directory (empty when never set).
func (s *State) HomeDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.homeDir
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

// SetTurnQuota records the latest subscription quota reading from an
// OAuth-backed provider. A nil argument clears it, so switching to a
// provider that reports no quota stops the footer advertising a stale
// window from the previous one.
func (s *State) SetTurnQuota(q *schema.QuotaInfo) {
	s.mu.Lock()
	s.turnQuota = q
	s.mu.Unlock()
}

// TurnQuota returns the latest quota reading, or nil when the active
// provider has not reported one.
func (s *State) TurnQuota() *schema.QuotaInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnQuota
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

// ClearTitleManual clears the manual-title flag so auto-titling (turn-start
// titling and new-task re-titling) may update the title again. The current
// title text is kept.
func (s *State) ClearTitleManual() {
	s.mu.Lock()
	s.titleSet = false
	s.mu.Unlock()
}

// restoreTitle re-applies the persisted title and its manual flag when a
// session is loaded, so re-titling treats previously manual names as
// manual across restarts. Best-effort: a missing row or DB error leaves the
// in-memory title untouched.
func (s *State) restoreTitle() {
	if s.db == nil {
		return
	}
	row, err := s.db.GetSession(s.sessionID)
	if err != nil || row.Title == "" {
		return
	}
	if row.TitleManual {
		s.SetTitleManual(row.Title)
	} else {
		s.SetTitle(row.Title)
	}
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

// WithClock overrides the state's clock. The spinner-dwell logic reads it
// via PinnedSpinnerLabel, and tests pass a frozen clock so a dwell window
// can be advanced deterministically; the default is time.Now.
func WithClock(now func() time.Time) Option {
	return func(s *State) {
		if now != nil {
			s.now = now
		}
	}
}

func New(cfg config.Config, workingDir string, now time.Time, p Persistence, opts ...Option) *State {
	ctx, cancel := context.WithCancel(context.Background())
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
		skillOrder:             make(map[string]int64),
		skillBodyAge:           make(map[string]int),
		loadedTools:            make(map[string]bool),
		parentOf:               make(map[int64]int64),
		childrenOf:             make(map[int64][]int64),
		msgByID:                make(map[int64]Message),
		dbIDToImID:             make(map[int64]int64),
		nextMsgID:              1,
		nextStepSeq:            1,
		workspace:              Workspace{ProjectRoot: workingDir, ActiveRoot: workingDir},
		subagentMaxConcurrency: defaultSubagentMaxConcurrency,
		subagentEpoch:          rand.Text(),
		workView:               db.WorkViewRecord{Version: 1},
	}
	for _, opt := range opts {
		opt(s)
	}
	// The configured mode is the reload baseline: it is what a later config
	// reload compares against (see RebaseSessionConfig), while
	// State.Config.Agent.ApprovalMode carries the effective mode a reader
	// sees. New conversations start with no override and system access off.
	s.configuredMode = configuredAccessMode(s.Config.Agent.ApprovalMode)
	s.Config.Agent.ApprovalMode = s.configuredMode
	if s.persistenceEnabled() {
		s.loadFromDB()
		s.loadWorkView()
		s.normalizeRestoredAgentWork()
		s.restoreWorkspace()
		s.restoreTitle()
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

// SystemAccess reports whether the session has system access enabled.
func (s *State) SystemAccess() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.systemAccess
}

// SetSystemAccess toggles the per-session system-access modifier.
func (s *State) SetSystemAccess(on bool) {
	// accessMu first so a concurrent mode change or config rebase cannot
	// interleave with this toggle; the flag itself stays under mu.
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.systemAccess = on
}

// AccessState returns the effective access tuple — the mode governing
// approval decisions and the system-access flag — as one value read under a
// single lock, so callers never see a mode from one commit and a flag from
// another.
func (s *State) AccessState() AccessState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return AccessState{Mode: s.Config.Agent.ApprovalMode, System: s.systemAccess}
}

// normalizeAccessMode validates a literal session-mode selection: a mode the
// user (or an approved request) names explicitly. Unlike a configured value,
// a selection is never guessed — blank or unrecognized input is an error that
// must leave the session's authority untouched.
func normalizeAccessMode(mode string) (string, error) {
	switch mode {
	case "plan", "default", "edit", "copilot", "auto":
		return mode, nil
	}
	return "", fmt.Errorf("session: unknown approval mode %q", mode)
}

// configuredAccessMode normalizes a mode read from configuration, matching
// how the rest of Marshal parses [agent].approval_mode: case-insensitive, and
// unknown or blank values fall back to "default" — the safe, read-only mode —
// rather than failing the session.
func configuredAccessMode(mode string) string {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	switch normalized {
	case "plan", "default", "edit", "copilot", "auto":
		return normalized
	}
	return "default"
}

// SetSessionMode applies an explicit mode selection for this session. The
// runner setter arrives as apply because the session package does not know
// about the policy engine; it is infallible, so validation and the quiescing
// gate happen here, before any mutation, and a rejected selection changes
// nothing. On success the mode becomes the session override, so it survives a
// config reload that does not itself change the configured mode (see
// ModeForConfig).
func (s *State) SetSessionMode(mode string, apply func()) error {
	valid, err := normalizeAccessMode(mode)
	if err != nil {
		return err
	}
	if apply == nil {
		return fmt.Errorf("session: set mode %q: no apply function", valid)
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	// The quiescing gate is checked under workMu (never the other way round,
	// which would invert the documented lock order) so a mode change cannot
	// land during shutdown.
	s.workMu.Lock()
	quiescing := s.quiescing
	s.workMu.Unlock()
	if quiescing {
		return ErrSessionQuiescing
	}
	// apply is the runner's setter: call it without State.mu held, then
	// commit the session-visible pair. The override is deliberately not
	// touched — a manual change does not redefine the configured baseline.
	apply()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionModeOverride = valid
	s.Config.Agent.ApprovalMode = valid
	return nil
}

// ModeForConfig reports the mode the session should run with for an incoming
// configuration: the session override when the configured mode has not
// changed, otherwise the configured mode itself. It mutates nothing, so a
// caller can consult it before deciding whether to rebuild anything.
func (s *State) ModeForConfig(cfg config.Config) string {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	if cfg.Agent.ApprovalMode == s.configuredMode && s.sessionModeOverride != "" {
		return s.sessionModeOverride
	}
	return cfg.Agent.ApprovalMode
}

// ConfigSnapshot returns a copy of the installed configuration, read under the
// session mutex. RebaseSessionConfig replaces Config wholesale on every reload
// — including reloads driven from a background goroutine — so a bare read of
// State.Config races the swap. A caller that needs more than one field
// consistently must take one snapshot rather than reading the field per use.
func (s *State) ConfigSnapshot() config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Config
}

// RebaseSessionConfig installs a reloaded configuration and re-derives the
// effective mode against it. An unchanged configured mode keeps the session
// override (an unrelated setting change must not silently revert the user's
// mode); an explicit change to the configured mode clears the override, since
// the user's edit is the newer intent. systemAccess is preserved either way —
// the grant belongs to the conversation, not to the file. Failed reloads must
// not call this at all.
func (s *State) RebaseSessionConfig(cfg config.Config) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	if configured := configuredAccessMode(cfg.Agent.ApprovalMode); configured != s.configuredMode {
		s.sessionModeOverride = ""
		s.configuredMode = configured
	}
	if s.sessionModeOverride != "" {
		cfg.Agent.ApprovalMode = s.sessionModeOverride
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Config = cfg
}

// ── Capability access requests ────────────────────────────────────────
//
// A request for editing and/or system access is a typed pending approval: it
// occupies the same single slot as an ordinary tool approval, so the human is
// never asked two things at once, and it carries Access metadata instead of
// only a description. Settlement is deliberately centralized here: every
// outcome — approval, denial, cancellation, shutdown — funnels through
// finishAccessLocked under accessMu, which is what lets a grant be applied
// before it is acknowledged and a cancellation that is already visible win.

// modeHasEditing reports whether a mode already carries full editing
// authority — the three modes that approve edits without a separate
// selector. A blank or unrecognized mode is read-only, so it never counts.
func modeHasEditing(mode string) bool {
	switch mode {
	case "edit", "copilot", "auto":
		return true
	}
	return false
}

// accessMissingFlags recomputes a request's missing-capability flags from the
// intent and the pair the request carries. Editing is missing only when it
// was asked for and the mode still lacks it (Plan/Default); system access is
// missing only when it was asked for and not yet held. Already being in
// Edit/Copilot/Auto means no editing capability can be missing, regardless of
// the hint.
func accessMissingFlags(req *AccessRequest, intent AccessIntent) {
	req.MissingEditing = intent.Editing && !modeHasEditing(req.Current.Mode)
	req.MissingSystem = intent.System && !req.Current.System
}

// accessRequestLocked returns the displayable form of the request currently
// occupying the pending-approval slot. It re-reads the live pair and
// recomputes the missing flags from the saved intent, so a reader refreshing
// a stale panel learns what is still missing now. It is a value snapshot:
// neither the canonical request nor a previously published copy is mutated.
// Callers must hold accessMu.
func (s *State) accessRequestLocked() (AccessRequest, bool) {
	s.mu.Lock()
	pending := s.pendingApproval
	intent := s.pendingAccessIntent
	if pending == nil || pending.Access == nil {
		s.mu.Unlock()
		return AccessRequest{}, false
	}
	req := *pending.Access
	req.Current = AccessState{Mode: s.Config.Agent.ApprovalMode, System: s.systemAccess}
	s.mu.Unlock()

	accessMissingFlags(&req, intent)
	return req, true
}

// AccessRequest returns the access request waiting for a decision, if any.
func (s *State) AccessRequest() (AccessRequest, bool) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	return s.accessRequestLocked()
}

// finishAccessLocked is the one settlement point for a typed access request.
// The caller holds accessMu and has already committed whatever state the
// result describes; this function only clears the slot, publishes, and
// delivers — it never mutates authority, and it must never touch a transport
// mutex, config, the model layer or background jobs.
//
// A result whose request ID does not name the canonical request does nothing:
// a late or duplicate outcome cannot settle a newer request. Otherwise the
// canonical typed request (and only that one) is cleared, the cleared slot is
// announced, and exactly one decision is delivered on the canonical buffered
// channel, which is then closed so the waiter unblocks. Ordinary approvals
// keep their own Respond path and are untouched.
func (s *State) finishAccessLocked(result AccessResult) {
	s.mu.Lock()
	pending := s.pendingApproval
	if pending == nil || pending.Access == nil || pending.Access.RequestID != result.RequestID {
		s.mu.Unlock()
		return
	}
	s.pendingApproval = nil
	s.pendingAccessCtx = nil
	s.pendingAccessIntent = AccessIntent{}
	s.mu.Unlock()

	// The registry mirror is settled with the same outcome, so a cancellation
	// racing this acknowledgement finds a settled record rather than a second
	// claim on a request that no longer exists.
	s.finishPromptMirrorLocked(result.RequestID, causeForAccessStatus(result.Status))

	s.publishEvent(EventPendingApprovalChanged, Event{PendingApproval: nil})

	// Deliberately bypass PendingToolCall.Respond: its guard makes Respond a
	// no-op for a typed request precisely so that this helper is the only way
	// one can be acknowledged. The decision carries a copy of the result, so
	// its recipient can retain it without aliasing session state.
	decision := UserApprovalDecision{Access: &result}
	decision.Approved = result.Status == AccessGranted
	pending.responded.Do(func() {
		if pending.ResponseChan != nil {
			select {
			case pending.ResponseChan <- decision:
			default:
			}
			close(pending.ResponseChan)
		}
	})
}

// RegisterAccess asks for the capabilities named by intent, publishing a
// request on the pending-approval slot when one is actually needed.
//
// waiting=false means the session already has everything asked for: no
// request ID was generated and no pending event was published. Every refusal
// (a done context, an unusable response channel, an empty intent, a child
// session, a quiescing session, an occupied slot) leaves any existing
// approval exactly as it was — a request never displaces one already shown.
func (s *State) RegisterAccess(ctx context.Context, pending *PendingToolCall, intent AccessIntent) (AccessRequest, bool, error) {
	// Validate everything the caller controls before touching the gate: a
	// malformed request must not even briefly hold the session's access lock.
	if ctx == nil {
		return AccessRequest{}, false, fmt.Errorf("session: register access: no context")
	}
	if err := ctx.Err(); err != nil {
		return AccessRequest{}, false, fmt.Errorf("session: register access: context already done: %w", err)
	}
	// An operation whose steering cancellation was already accepted must not
	// publish a new prompt. The other four Register* methods reject such a
	// context through newPromptRecordLocked; without the same check here an
	// access request would flash on screen only to be canceled immediately.
	if SteeringRequested(ctx) {
		return AccessRequest{}, false, fmt.Errorf("session: register access: %w", errPromptRequested)
	}
	if pending == nil {
		return AccessRequest{}, false, fmt.Errorf("session: register access: no pending call")
	}
	if pending.ResponseChan == nil {
		return AccessRequest{}, false, fmt.Errorf("session: register access: pending call has no response channel")
	}
	// The canonical channel is buffered so settlement can never block on a
	// waiter that has gone away, and it must be empty so a stale decision
	// cannot be mistaken for this request's answer.
	if cap(pending.ResponseChan) < 1 {
		return AccessRequest{}, false, fmt.Errorf("session: register access: response channel is unbuffered")
	}
	if len(pending.ResponseChan) != 0 {
		return AccessRequest{}, false, fmt.Errorf("session: register access: response channel is not empty")
	}
	if !intent.Editing && !intent.System {
		return AccessRequest{}, false, fmt.Errorf("session: register access: intent requests no capability")
	}

	s.accessMu.Lock()
	defer s.accessMu.Unlock()

	if depth := s.SubagentDepth(); depth > 0 {
		return AccessRequest{}, false, fmt.Errorf("session: register access: child session (depth %d) cannot request access", depth)
	}
	s.workMu.Lock()
	quiescing := s.quiescing
	s.workMu.Unlock()
	if quiescing {
		return AccessRequest{}, false, ErrSessionQuiescing
	}

	s.mu.Lock()
	occupied := s.pendingApproval != nil
	s.mu.Unlock()
	if occupied {
		return AccessRequest{}, false, fmt.Errorf("session: register access: a pending approval already occupies the slot")
	}

	// One read of the effective pair decides whether anything is missing at
	// all; the flags are computed from the intent, never trusted from it.
	current := s.AccessState()
	missing := AccessRequest{Current: current}
	accessMissingFlags(&missing, intent)
	if !missing.MissingEditing && !missing.MissingSystem {
		return AccessRequest{
			SessionID: s.sessionID,
			Reason:    intent.Reason,
			Current:   current,
		}, false, nil
	}

	// The request ID is independent of the provider's tool-call ID: the
	// caller may reuse that ID, and two sequential requests must never be
	// confusable.
	req := AccessRequest{
		RequestID:      rand.Text(),
		SessionID:      s.sessionID,
		Actor:          s.accessActorLocked(pending.StepID),
		StepID:         pending.StepID,
		Reason:         intent.Reason,
		Current:        current,
		MissingEditing: missing.MissingEditing,
		MissingSystem:  missing.MissingSystem,
	}
	// The canonical call gets its own allocation: the value returned to the
	// caller and the one published on the event are copies, so a later
	// refresh cannot rewrite metadata someone already displayed.
	canonical := req
	pending.Access = &canonical

	s.mu.Lock()
	s.pendingApproval = pending
	s.pendingAccessCtx = ctx
	s.pendingAccessIntent = intent
	s.mu.Unlock()

	// Mirror the request into the canonical prompt registry. The mirror
	// carries the access request's own opaque ID as its key and never owns
	// settlement: finishAccessLocked remains the one acknowledger, and
	// CancelOperationPrompts reaches it through the existing CancelAccess
	// path. What the mirror buys is a single cancellation-intent boundary:
	// ResolveAccess and Send now both decide under accessMu.
	s.mirrorAccessPromptLocked(ctx, pending, req.RequestID)

	// Published after State.mu is released (subscribers may call back into
	// State). The event copy carries the request metadata but no
	// ResponseChan: a typed request is settled only by the session's private
	// helper, and an event subscriber must not be able to answer one — or to
	// close a channel it does not own.
	eventReq := req
	s.publishEvent(EventPendingApprovalChanged, Event{PendingApproval: &PendingToolCall{
		ID:      pending.ID,
		Name:    pending.Name,
		Args:    pending.Args,
		Command: pending.Command,
		Risk:    pending.Risk,
		Reason:  pending.Reason,
		Diff:    pending.Diff,
		Schema:  pending.Schema,
		StepID:  pending.StepID,
		Access:  &eventReq,
	}})
	return req, true, nil
}

// accessActorLocked resolves the label to show for the requesting step.
// State.Step takes State.mu itself, so this must never be called with State.mu
// held — it is called with only accessMu held.
func (s *State) accessActorLocked(stepID StepID) string {
	if stepID == 0 {
		return "Marshal"
	}
	step, ok := s.Step(stepID)
	if !ok {
		return "Marshal"
	}
	switch {
	case step.Actor.Label != "":
		return step.Actor.Label
	case step.Actor.Role != "":
		return step.Actor.Role
	default:
		return "Marshal"
	}
}

// ResolveAccess settles the outstanding access request with the user's
// choice. prepare validates the choice and returns the state it wants
// committed; it runs under accessMu but must be memory-only.
//
// The result always describes the access pair in force when it returns. A
// success is only reported after the commit has been applied, so an
// acknowledgment can never arrive before the capability does.
func (s *State) ResolveAccess(choice AccessChoice, prepare AccessPrepare) (AccessResult, error) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()

	// The pair and the pending request are read together: a choice is judged
	// against the state the session is actually in when it arrives.
	req, ok := s.accessRequestLocked()
	actual := s.AccessState()
	expire := func(message string) (AccessResult, error) {
		// The current approval (if any) is deliberately left in place: a
		// choice for a request this session does not know must not disturb
		// the one the user is actually looking at.
		err := errors.New("session: resolve access: " + message)
		return AccessResult{
			SessionID: s.sessionID,
			Status:    AccessExpired,
			Effective: actual,
			Message:   err.Error(),
		}, err
	}
	// A request is missing when no typed approval occupies the slot, and
	// foreign when the choice names a different request or session. Either
	// way it is not this session's to settle.
	if !ok {
		return expire("no access request is pending")
	}
	if choice.RequestID != req.RequestID || choice.SessionID != req.SessionID {
		return expire("choice does not name the pending request")
	}
	// Every registration saves the original context privately, so its absence
	// means the request is not one this session published — and a child
	// session can never own the parent's slot.
	if s.pendingAccessCtx == nil {
		return expire("request has no original context")
	}
	if s.SubagentDepth() > 0 {
		return expire("child session cannot settle an access request")
	}

	// The gate is taken before anything is committed, so a cancellation or a
	// shutdown that is already visible wins the race outright.
	if err := s.pendingAccessCtx.Err(); err != nil {
		return s.finishAccessCanceledLocked(req, actual, "request context canceled"), nil
	}
	if s.quiescingLocked() {
		return s.finishAccessCanceledLocked(req, actual, "session is quiescing"), nil
	}
	// Steering cancellation intent recorded by SubmitSendNow is checked at the
	// same gate: an approval cannot slip through the interval between accepting
	// steering and signalling its cancellation.
	if SteeringRequested(s.pendingAccessCtx) {
		return s.finishAccessCanceledLocked(req, actual, ErrSendNow.Error()), nil
	}

	// The chooser answered against the pair it was shown. If the session has
	// moved on, applying the choice would grant a capability the user never
	// saw requested: ask for a refresh, keep the request, and invoke nothing.
	if choice.Current != actual {
		return AccessResult{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Status:    AccessRefresh,
			Effective: actual,
			Message:   "access state changed since the request was shown",
		}, nil
	}

	// A matching denial settles here and revokes nothing: authority only ever
	// moves forward.
	if !choice.Approved {
		result := AccessResult{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Status:    AccessDenied,
			Effective: actual,
			Message:   "access denied",
		}
		s.finishAccessLocked(result)
		return result, nil
	}

	// The pair to commit against is the actual one, and the capabilities that
	// still need granting are recomputed from it.
	target := actual
	needed := req
	needed.Current = actual
	accessMissingFlags(&needed, s.pendingAccessIntent)
	if !needed.MissingEditing && !needed.MissingSystem {
		result := AccessResult{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Status:    AccessAvailable,
			Effective: actual,
			Message:   "requested access is already available",
		}
		s.finishAccessLocked(result)
		return result, nil
	}

	// Validate the choice against what is actually missing before looking for
	// a resolver: only an editing-capable variant may fill an editing gap, and
	// a system-only request must not name a mode at all.
	if needed.MissingEditing {
		if !modeHasEditing(choice.Mode) {
			return s.finishAccessFailedLocked(req, actual, fmt.Errorf("invalid editing mode %q", choice.Mode))
		}
		target.Mode = choice.Mode
	} else if choice.Mode != "" {
		return s.finishAccessFailedLocked(req, actual, fmt.Errorf("unexpected editing mode %q for a system-only request", choice.Mode))
	}
	if needed.MissingSystem {
		target.System = true
	}

	if prepare == nil {
		return s.finishAccessFailedLocked(req, actual, errors.New("no access resolver configured"))
	}
	commit, err := prepare(req, choice)
	if err != nil {
		return s.finishAccessFailedLocked(req, actual, fmt.Errorf("access resolver rejected the request: %w", err))
	}
	// A commit without an applier would report success with nothing applied,
	// and one that targets a different pair than the request implies would
	// grant something the user never saw. Both fail closed.
	if commit.Apply == nil {
		return s.finishAccessFailedLocked(req, actual, errors.New("access resolver returned no apply function"))
	}
	if commit.Effective != target {
		return s.finishAccessFailedLocked(req, actual, fmt.Errorf("resolver proposed %+v, expected %+v", commit.Effective, target))
	}

	// Recheck immediately before applying: this is the line that linearizes
	// the grant. Cancellation or quiescing already visible here wins; either
	// arriving later waits for this application and its acknowledgment.
	if err := s.pendingAccessCtx.Err(); err != nil {
		return s.finishAccessCanceledLocked(req, actual, "request context canceled"), nil
	}
	if s.quiescingLocked() {
		return s.finishAccessCanceledLocked(req, actual, "session is quiescing"), nil
	}
	// The same shared cancellation-intent boundary SubmitSendNow sets under
	// this gate. A grant whose claim reached here first still applies below;
	// only intent already recorded when this line runs prevents it.
	if SteeringRequested(s.pendingAccessCtx) {
		return s.finishAccessCanceledLocked(req, actual, ErrSendNow.Error()), nil
	}

	// Only now is authority moved: apply (the resolver's setter) runs with
	// accessMu held but State.mu released, then the session records exactly
	// the capabilities that were missing and nothing else. The public mode
	// and system setters are deliberately not called — they take accessMu.
	commit.Apply()
	s.mu.Lock()
	if needed.MissingEditing {
		s.sessionModeOverride = target.Mode
		s.Config.Agent.ApprovalMode = target.Mode
	}
	if needed.MissingSystem {
		s.systemAccess = true
	}
	s.mu.Unlock()

	result := AccessResult{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Status:    AccessGranted,
		Effective: target,
		Message:   "access granted",
	}
	s.finishAccessLocked(result)
	return result, nil
}

// finishAccessCanceledLocked settles a request the gate found already
// unwinnable. It reports the actual pair, which is unchanged, and the
// acknowledgment is a cancellation rather than a grant.
func (s *State) finishAccessCanceledLocked(req AccessRequest, actual AccessState, message string) AccessResult {
	result := AccessResult{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Status:    AccessCanceled,
		Effective: actual,
		Message:   message,
	}
	s.finishAccessLocked(result)
	return result
}

// finishAccessFailedLocked settles a request that cannot be applied and
// reports the concrete reason. It is always paired with a non-nil error at
// the call site, so a caller cannot mistake a refusal for a grant.
func (s *State) finishAccessFailedLocked(req AccessRequest, actual AccessState, cause error) (AccessResult, error) {
	result := AccessResult{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Status:    AccessFailed,
		Effective: actual,
		Message:   cause.Error(),
	}
	s.finishAccessLocked(result)
	return result, cause
}

// quiescingLocked reports whether the shutdown gate has closed. Callers must
// not hold workMu: accessMu precedes workMu in the lock order, so taking it
// here (while holding accessMu) is the documented direction.
func (s *State) quiescingLocked() bool {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	return s.quiescing
}

// CancelAccess settles the outstanding request as canceled or failed. It
// accepts only those two statuses: this is the cancellation path, not a
// second way to approve or deny. A request ID that does not name the
// canonical request changes nothing and returns false.
func (s *State) CancelAccess(requestID string, status AccessStatus, message string) bool {
	if status != AccessCanceled && status != AccessFailed {
		return false
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()

	req, ok := s.accessRequestLocked()
	if !ok || requestID == "" || req.RequestID != requestID {
		return false
	}
	actual := s.AccessState()
	result := AccessResult{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Status:    status,
		Effective: actual,
		Message:   message,
	}
	s.finishAccessLocked(result)
	return true
}

// SecretPromptCapable reports whether the attached transport can raise a
// masked secret prompt. False by default.
func (s *State) SecretPromptCapable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secretPromptCapable
}

// SetSecretPromptCapable records whether the transport can raise a masked
// secret prompt. The TUI wiring sets this true; ACP/web leave it false.
func (s *State) SetSecretPromptCapable(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secretPromptCapable = on
}

// SudoUnlockEnabled reports whether the session has opted into sudo's own
// timestamp grace (omit -k) instead of forcing fresh authentication.
func (s *State) SudoUnlockEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sudoUnlock
}

// SetSudoUnlockEnabled toggles the opt-in unlock. Runtime-only, never a
// config default; sticky until toggled off.
func (s *State) SetSudoUnlockEnabled(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sudoUnlock = on
}

// TryLockSudo attempts to take the session-wide privileged-command lock,
// returning false when another shell.sudo call already holds it.
func (s *State) TryLockSudo() bool { return s.sudoMu.TryLock() }

// UnlockSudo releases the privileged-command lock.
func (s *State) UnlockSudo() { s.sudoMu.Unlock() }

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
//
// The child's creation is also registered with the conversation's end gate
// (Task 8), atomically with its admission: a child is work the user must be
// warned about before an end stops it, and the registration is what makes one
// child exactly one item — from creation, through its card, to its exit.
func (s *State) EnterSubagent() error {
	_, err := s.EnterSubagentWork()
	return err
}

// EnterSubagentWork is EnterSubagent plus the child-creation work item it
// registered with the conversation's end gate. The caller holds the item id and
// hands it to RegisterSubagentWithWork, so the child's card names THAT child's
// item — a targeted stop reaches the child the user pointed at, and one child is
// never registered twice. ExitSubagent releases the item.
func (s *State) EnterSubagentWork() (WorkID, error) {
	s.mu.Lock()
	if s.subagentDepth >= subagentMaxDepth {
		s.mu.Unlock()
		return "", ErrSubagentDepthLimit
	}
	if s.subagentConcurr >= s.subagentMaxConcurrency {
		// The "(max N)" substring is asserted by
		// TestEnterSubagentDefaultCapIsThree — keep it.
		max := s.subagentMaxConcurrency
		running := s.subagentConcurr
		s.mu.Unlock()
		return "", fmt.Errorf("%w (max %d): %d already running. Call agent.await with \"any\": true to wait for the first to finish, then retry",
			ErrSubagentConcurrencyLimit, max, running)
	}
	s.subagentConcurr++
	label := s.subagentWorkLabelLocked()
	s.mu.Unlock()

	id, err := s.end.beginChildWork(label)
	if err != nil {
		s.mu.Lock()
		if s.subagentConcurr > 0 {
			s.subagentConcurr--
		}
		s.mu.Unlock()
		return "", err
	}
	s.mu.Lock()
	s.subagentWorkItems = append(s.subagentWorkItems, id)
	s.mu.Unlock()
	return id, nil
}

// subagentWorkLabelLocked renders the label a child's creation registers with:
// the session's own words for what it is doing, else the operation label, so
// the warning panel names the work rather than an opaque id. The state lock must
// be held.
func (s *State) subagentWorkLabelLocked() string {
	if s.activity.Kind != "" && s.activity.Kind != ActivityIdle && s.activity.Label != "" {
		return "subagent: " + s.activity.Label
	}
	return "subagent"
}

// ExitSubagent decrements the in-flight concurrency counter added by a prior
// successful EnterSubagent. Calling ExitSubagent without a matching
// EnterSubagent is a programming error and would emit a negative counter;
// callers must always pair the calls. Depth is unaffected.
//
// It releases the most recently registered child-creation item. A caller that
// holds the item id its EnterSubagentWork returned must use ExitSubagentWork
// instead: with several children in flight, a FIFO pop can release a sibling's
// registration and leave this child's item looking active.
func (s *State) ExitSubagent() {
	s.mu.Lock()
	if s.subagentConcurr > 0 {
		s.subagentConcurr--
	}
	var item WorkID
	if n := len(s.subagentWorkItems); n > 0 {
		item = s.subagentWorkItems[n-1]
		s.subagentWorkItems = s.subagentWorkItems[:n-1]
	}
	s.mu.Unlock()
	// One child, one item: the creation registration is released here, which is
	// the registration that covers the child's whole life. A child whose card
	// registered its own item (a pipeline or review worker, which never enters
	// here) is released by FinishSubagent instead.
	if item != "" {
		s.end.release(item)
	}
}

// ExitSubagentWork is ExitSubagent for a caller that holds the item id its
// EnterSubagentWork returned. The release is keyed by that identity rather than
// by registration order, so concurrent children stay paired exactly: a FIFO pop
// could release a sibling's registration while this child was still running,
// which would hide that child from the work membership an end warns about.
func (s *State) ExitSubagentWork(id WorkID) {
	s.mu.Lock()
	if s.subagentConcurr > 0 {
		s.subagentConcurr--
	}
	released := false
	for i, item := range s.subagentWorkItems {
		if item == id {
			s.subagentWorkItems = append(s.subagentWorkItems[:i], s.subagentWorkItems[i+1:]...)
			released = true
			break
		}
	}
	s.mu.Unlock()
	if released {
		s.end.release(id)
	}
}

// BeginWork registers that the caller is starting a unit of asynchronous
// work (typically a Bubble Tea command). After BeginQuiesce has been
// called, BeginWork returns ErrSessionQuiescing and the caller must not
// start the work. Every successful BeginWork must be paired with a
// matching EndWork call (typically via defer in the command closure).
//
// It is also the conversation end gate's parent-operation registration (Task
// 8): the operation becomes part of the membership an end acceptance checks, so
// an accepted end can see a turn that is blocked in provider resolution or
// waiting on an approval — neither of which reports itself as a command or a
// child — and refuses to start one after an end has been accepted.
func (s *State) BeginWork() error {
	return s.BeginWorkCancelable(nil)
}

// BeginWorkCancelable is BeginWork plus the operation's stop hook: cancel is
// recorded as the way to stop this operation, so an accepted conversation end
// can unblock a turn parked in provider resolution or waiting on an approval
// without cancelling the runtime's whole infrastructure root. A nil cancel is
// exactly BeginWork.
//
// The hook is invoked with no conversation lock held, so a stop pass never
// deadlocks against a turn that is finishing.
func (s *State) BeginWorkCancelable(cancel func()) error {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	if s.quiescing {
		return ErrSessionQuiescing
	}
	id, err := s.end.begin(WorkKindParent, s.derivedWorkLabel(), "", "", cancel)
	if err != nil {
		return err
	}
	s.end.rememberParent(id)
	s.workWG.Add(1)
	return nil
}

// EndWork signals that a unit of work registered via BeginWork has
// completed. Calling EndWork without a matching BeginWork is a
// programming error that would panic via the WaitGroup.
func (s *State) EndWork() {
	s.end.releaseOldestParent()
	s.workWG.Done()
}

// ---------------------------------------------------------------------------
// Conversation end: the shared work gate
//
// Shared contract 7: runtime end acceptance checks the displayed opaque work
// IDs and freezes ordinary admission ATOMICALLY. If any currently active ID
// was not displayed, refresh and stop nothing. Finished IDs need no
// reconfirmation.
//
// Shared contract 8: exit phases are open → stopping work → postmortem →
// closing → closed, where a REPLACEMENT omits postmortem, and only scoped
// exit-analysis work may launch during the postmortem phase.
//
// Both halves are served by one registry that every ordinary unit of
// conversation work registers itself with — a parent operation (BeginWork), a
// delegated child (EnterSubagent / RegisterSubagent) and a managed command (the
// command service's admission seam) — and the membership check that accepts an
// end takes the same lock registration takes. That is what makes acceptance
// atomic: work that registered first can never be missed by an accepted end,
// and work that registers after one can never be admitted. Sampling a count
// and then acting on it would not be: a command admitted between the two would
// either hide from the warning or be destroyed without ever being shown.
//
// Infrastructure deliberately does NOT register. Indexing, LSP, OAuth refresh
// and watch samplers run under the runtime's infrastructure root and are not
// conversation work: warning about them would train the user to ignore the
// warning.
// ---------------------------------------------------------------------------

// ExitPhase is the conversation's end phase. The order is
// open → stopping work → postmortem → closing → closed. A replacement omits
// postmortem: no exit analysis runs when the conversation is replaced.
type ExitPhase string

const (
	// ExitPhaseOpen is a live conversation: ordinary work is admitted.
	ExitPhaseOpen ExitPhase = "open"
	// ExitPhaseStopping means an end was accepted: ordinary admission is
	// frozen and the displayed work is being stopped and joined.
	ExitPhaseStopping ExitPhase = "stopping_work"
	// ExitPhasePostmortem means the stopped work has settled and the only work
	// that may still launch is the scoped exit analysis.
	ExitPhasePostmortem ExitPhase = "postmortem"
	// ExitPhaseClosing means no work may launch at all: terminal teardown is
	// running.
	ExitPhaseClosing ExitPhase = "closing"
	// ExitPhaseClosed means the conversation and its runtime are closed.
	ExitPhaseClosed ExitPhase = "closed"
)

// WorkKind classifies one registered unit of conversation work.
type WorkKind string

const (
	// WorkKindParent is a top-level operation: a TUI turn, a /review dispatch,
	// an SDD run, an ACP prompt.
	WorkKindParent WorkKind = "parent"
	// WorkKindChild is one delegated child: an agent.run subagent, a pipeline
	// role runner, a review worker. A child's creation is registered before its
	// view exists, so an active parent waiting on provider work or an approval
	// is visible as work.
	WorkKindChild WorkKind = "child"
	// WorkKindCommand is one managed command admitted through the
	// conversation's command service.
	WorkKindCommand WorkKind = "command"
)

// maxWorkLabelRunes bounds every label a snapshot carries. A label is display
// data, never an identity: a long command line or a pasted prompt must not
// grow a warning panel without limit, and nothing downstream may parse it.
const maxWorkLabelRunes = 72

// WorkID is the stable, opaque identity of one registered unit of conversation
// work. It is stable for the item's whole life — a command keeps the identity
// the service minted at admission, a child keeps the one its creation
// registered — so an accepted end can compare the set a transport displayed
// with the set that exists without depending on labels, ordering or counts.
type WorkID string

// WorkItem is one registered unit of conversation work. Everything here is
// display data or a targeting handle; identity is ID.
type WorkItem struct {
	ID   WorkID
	Kind WorkKind
	// Label is a bounded, secret-scrubbed, single-line description.
	Label string
	// Group is the WorkID of the operation a command is attached to, so a
	// client can show attached commands UNDER their parent without counting
	// them as separate stoppable targets. It is empty when the command belongs
	// to no registered operation (a child's command whose child item is gone).
	Group WorkID
	// TurnID is the explicit-stop group the work belongs to, when it has one.
	TurnID string
	// SubagentID is the child card's id for a delegated child, so a targeted
	// stop can reach it. Zero for parents and commands.
	SubagentID int64
	// Scope is the exit-analysis epoch this work belongs to; empty for ordinary
	// work. Only scoped work may launch during the postmortem phase.
	Scope string
	// StartedAt is when the work registered.
	StartedAt time.Time
}

// WorkSnapshot is the membership of a conversation's registered work at one
// moment, with the exit phase it was taken in.
type WorkSnapshot struct {
	// Phase is the conversation's exit phase at snapshot time.
	Phase ExitPhase
	// Items is the ordinary membership: parent operations, delegated children
	// and managed commands. Each item keeps its own distinct ID even when it is
	// displayed under a parent, so new work can never hide behind an existing
	// parent's identity.
	Items []WorkItem
	// Analysis is the scoped exit-analysis epoch's membership, if one is open.
	// It is separate on purpose: analysis work is not part of the ordinary work
	// a user is warned about, and it is joined as its own epoch.
	Analysis []WorkItem
}

// IDs returns the ordinary membership's stable IDs, in registration order.
func (s WorkSnapshot) IDs() []WorkID {
	out := make([]WorkID, 0, len(s.Items))
	for _, item := range s.Items {
		out = append(out, item.ID)
	}
	return out
}

// Contains reports whether id is in the ordinary membership.
func (s WorkSnapshot) Contains(id WorkID) bool {
	for _, item := range s.Items {
		if item.ID == id {
			return true
		}
	}
	return false
}

// Targets returns the distinct stoppable targets in the ordinary membership.
// A command displayed under its parent's group is folded into that parent: the
// parent's stop reaches it, and counting both would report a user-facing
// number larger than the number of things that will actually be stopped.
func (s WorkSnapshot) Targets() []WorkID {
	seen := make(map[WorkID]bool, len(s.Items))
	have := make(map[WorkID]bool, len(s.Items))
	for _, item := range s.Items {
		have[item.ID] = true
	}
	out := make([]WorkID, 0, len(s.Items))
	for _, item := range s.Items {
		target := item.ID
		if item.Kind == WorkKindCommand && item.Group != "" && have[item.Group] {
			target = item.Group
		}
		if seen[target] {
			continue
		}
		seen[target] = true
		out = append(out, target)
	}
	return out
}

// ErrConversationEnding reports that an end has been accepted, so ordinary
// conversation work can no longer start. It wraps ErrSessionQuiescing so every
// existing caller that checks that sentinel keeps working, in the direction
// that reads correctly: an accepted end IS a frozen conversation, and the
// reason it is frozen is the end.
var ErrConversationEnding = fmt.Errorf("%w: the conversation is ending", ErrSessionQuiescing)

// ErrNotPostmortemPhase reports that scoped exit-analysis work was requested
// outside the postmortem phase. It cannot reopen a conversation that has been
// accepted for replacement or closure.
var ErrNotPostmortemPhase = errors.New("session: exit analysis is admitted only during the postmortem phase")

// ErrExitAnalysisAlreadyOpen reports that a scoped exit-analysis epoch is
// already open. One epoch at a time keeps its work separable.
var ErrExitAnalysisAlreadyOpen = errors.New("session: an exit-analysis epoch is already open")

// ExitAnalysisScope authorizes one scoped exit-analysis epoch. Work admitted
// under it is registered in its own epoch, and the epoch is closed before
// closure, so a stale scope can never admit work into a conversation that has
// moved on.
type ExitAnalysisScope struct {
	id    string
	epoch int
}

// ID is the scope's identity, and the value stamped on the work it admits.
func (s *ExitAnalysisScope) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

// Epoch is the scope's work epoch.
func (s *ExitAnalysisScope) Epoch() int {
	if s == nil {
		return 0
	}
	return s.epoch
}

// exitAnalysisKey is a private context key so nothing outside this package can
// forge an exit-analysis authorization.
type exitAnalysisKey struct{}

// WithExitAnalysisScope stamps a scoped exit-analysis authorization onto ctx.
// Commands and children launched from that context inherit it, which is what
// keeps the analysis epoch's work separate from ordinary work — and what makes
// the epoch's cleanup complete.
func WithExitAnalysisScope(ctx context.Context, scope *ExitAnalysisScope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if scope == nil {
		return ctx
	}
	return context.WithValue(ctx, exitAnalysisKey{}, scope)
}

// ExitAnalysisScopeFromContext reads the exit-analysis authorization a scoped
// operation stamped onto its context. ok is false for ordinary work.
func ExitAnalysisScopeFromContext(ctx context.Context) (*ExitAnalysisScope, bool) {
	if ctx == nil {
		return nil, false
	}
	scope, ok := ctx.Value(exitAnalysisKey{}).(*ExitAnalysisScope)
	return scope, ok && scope != nil
}

// ExitAnalysisScopeID is the scope's id, or "" when the context carries none.
// The command service reads it at admission to tell ordinary work from scoped
// exit-analysis work without importing anything else.
func ExitAnalysisScopeID(ctx context.Context) string {
	scope, ok := ExitAnalysisScopeFromContext(ctx)
	if !ok {
		return ""
	}
	return scope.ID()
}

// WorkAdmissionRequest is what the command service reports about one command it
// is about to admit. The service calls this while it holds its own admission
// lock, so the answer it gets is the answer that applies to the moment of
// admission.
type WorkAdmissionRequest struct {
	ExecutionID string
	DisplayID   string
	Command     string
	Actor       string
	TurnID      string
	// Scope is the exit-analysis epoch from the launching context, or "" for
	// ordinary work.
	Scope string
}

// WorkLease releases one admitted unit of work from the end controller.
// Release is idempotent and safe to call from any goroutine.
type WorkLease interface {
	Release()
}

// WorkAdmission is the end controller's admission seam. The command service
// holds it and calls AdmitWork for every command, so a frozen conversation
// rejects admission at the service rather than only at the transport.
type WorkAdmission interface {
	// AdmitWork registers one unit of work, atomically with its admission
	// decision: a non-nil error means the work must not start.
	AdmitWork(req WorkAdmissionRequest) (WorkLease, error)
}

// endItem is one registered unit of work inside the controller.
type endItem struct {
	item WorkItem
	// cancel stops this item's owning operation when the item registered one
	// (a runtime turn's work context). It is invoked with no lock held.
	cancel func()
}

// endController is the conversation's end gate: the work registry, the exit
// phase and the scoped analysis epoch, under one mutex.
//
// Lock order: endController.mu is a LEAF with respect to the conversation's
// other locks — the session mutex and the command service's admission mutex may
// both be held when it is taken (mu → end.mu), and nothing is ever acquired
// while it is held. Cancellation and join callbacks therefore always run with
// the lock released, and the stop pass can call back into the service without
// inverting the order.
//
// It is a value field of State so a State built as a literal (tests, embedded
// callers) still has a working gate.
type endController struct {
	mu    sync.Mutex
	phase ExitPhase
	next  int64
	items map[WorkID]*endItem
	order []WorkID

	// parents is the FIFO of registered parent operations, so EndWork can
	// release the one its BeginWork registered. anonymousKids is the stack of
	// child-creation items whose caller used the argument-less EnterSubagent
	// pair, so each of those registrations is released exactly once too.
	parents       []WorkID
	anonymousKids []WorkID
	// childCards maps a subagent view id to its child work item. cardOwned
	// marks the items a CARD registered (a pipeline or review worker, which has
	// no creation pair): only those are released by the card's finish, while a
	// creation-registered child is released by the exit that names its own id —
	// that distinction is what keeps every item released exactly once even when
	// several children start and finish at once.
	childCards map[int64]WorkID
	cardOwned  map[WorkID]bool

	// analysisID is the open exit-analysis epoch's id, "" when none is open;
	// analysisEpoch counts epochs, so a stale scope cannot re-admit work.
	analysisID    string
	analysisEpoch int

	// drained is closed when the ordinary membership becomes empty. It exists
	// so a join waits on a channel instead of polling.
	drained chan struct{}
}

// phaseNow reports the current exit phase.
func (c *endController) phaseNow() ExitPhase {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phaseLocked()
}

func (c *endController) phaseLocked() ExitPhase {
	if c.phase == "" {
		return ExitPhaseOpen
	}
	return c.phase
}

// frozenLocked reports whether ordinary admission is closed. The controller's
// lock must be held.
func (c *endController) frozenLocked() bool {
	return c.phaseLocked() != ExitPhaseOpen
}

// begin registers one unit of work. Ordinary work (scopeID == "") is refused
// once an end has been accepted; scoped exit-analysis work is admitted only
// while the postmortem phase is open and only under the open epoch's id.
func (c *endController) begin(kind WorkKind, label, scopeID, turnID string, cancel func()) (WorkID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	phase := c.phaseLocked()
	if scopeID == "" {
		if phase != ExitPhaseOpen {
			return "", ErrConversationEnding
		}
	} else if phase != ExitPhasePostmortem || scopeID != c.analysisID || c.analysisID == "" {
		return "", ErrNotPostmortemPhase
	}
	return c.reserveLocked(kind, label, scopeID, turnID, cancel), nil
}

// reserveLocked allocates one stable work id and registers its item. The
// controller's lock must be held, so admission and the end-membership check that
// accepts an end are one atomic decision.
func (c *endController) reserveLocked(kind WorkKind, label, scopeID, turnID string, cancel func()) WorkID {
	c.next++
	var id WorkID
	switch kind {
	case WorkKindCommand:
		id = WorkID(fmt.Sprintf("work-command-%d", c.next))
	case WorkKindChild:
		id = WorkID(fmt.Sprintf("work-child-%d", c.next))
	default:
		id = WorkID(fmt.Sprintf("work-parent-%d", c.next))
	}
	if c.items == nil {
		c.items = make(map[WorkID]*endItem)
	}
	if c.drained == nil {
		c.drained = make(chan struct{})
	}
	c.items[id] = &endItem{item: WorkItem{
		ID:        id,
		Kind:      kind,
		Label:     boundWorkLabel(label),
		TurnID:    turnID,
		Scope:     scopeID,
		StartedAt: time.Now(),
	}, cancel: cancel}
	c.order = append(c.order, id)
	return id
}

// release drops one registered item, notifying a waiting join when the
// ordinary membership becomes empty. Unknown ids are ignored: a release that
// lost a race with the terminal teardown must not panic.
func (c *endController) release(id WorkID) {
	c.mu.Lock()
	_, ok := c.items[id]
	delete(c.items, id)
	if ok {
		c.order = removeWorkID(c.order, id)
		c.parents = removeWorkID(c.parents, id)
		c.anonymousKids = removeWorkID(c.anonymousKids, id)
		delete(c.cardOwned, id)
		for view, item := range c.childCards {
			if item == id {
				delete(c.childCards, view)
			}
		}
	}
	c.signalDrainedLocked()
	c.mu.Unlock()
}

// signalDrainedLocked closes the drain channel and clears it when nothing is
// registered any more, so the next registration starts a fresh wait. The
// controller's lock must be held.
func (c *endController) signalDrainedLocked() {
	if len(c.items) != 0 || c.drained == nil {
		return
	}
	close(c.drained)
	c.drained = nil
}

// drainLocked returns the channel to wait on for the ordinary membership to
// empty. done is true when it is already empty. The controller's lock must be
// held.
func (c *endController) drainLocked() (ch chan struct{}, done bool) {
	if len(c.items) == 0 {
		return nil, true
	}
	if c.drained == nil {
		c.drained = make(chan struct{})
	}
	return c.drained, false
}

// removeWorkID drops id from a work-id slice.
func removeWorkID(ids []WorkID, id WorkID) []WorkID {
	for i, want := range ids {
		if want == id {
			return append(ids[:i:i], ids[i+1:]...)
		}
	}
	return ids
}

// SetWorkCancel publishes the stop hook for the operation that most recently
// registered work (BeginWorkCancelable). It exists so a caller that allocates
// its context after registering — the runtime's BeginWork does — can still hand
// an accepted end a way to stop that exact operation, instead of forcing the
// stop pass to cancel the whole runtime root.
//
// It attaches to the newest registered parent whose hook is still unset, so it
// can never repoint an operation that already published one.
func (s *State) SetWorkCancel(cancel func()) {
	if s == nil || cancel == nil {
		return
	}
	s.end.setParentCancel(cancel)
}

// setParentCancel binds cancel to the newest open parent item, newest first, so
// a later registration of the same operation cannot be repointed.
func (c *endController) setParentCancel(cancel func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.parents) - 1; i >= 0; i-- {
		item, ok := c.items[c.parents[i]]
		if !ok {
			continue
		}
		if item.cancel != nil {
			// Already published: this call belongs to an older registration that
			// has not been attached yet, or to a duplicate. Attaching to an older
			// item is still correct — it is work whose hook is genuinely unset.
			continue
		}
		item.cancel = cancel
		return
	}
}

// rememberParent records a parent operation's id so EndWork releases exactly
// one registration per BeginWork.
func (c *endController) rememberParent(id WorkID) {
	c.mu.Lock()
	c.parents = append(c.parents, id)
	c.mu.Unlock()
}

// releaseOldestParent releases the parent registration EndWork corresponds to.
func (c *endController) releaseOldestParent() {
	c.mu.Lock()
	if len(c.parents) == 0 {
		c.mu.Unlock()
		return
	}
	id := c.parents[0]
	c.parents = c.parents[1:]
	c.mu.Unlock()
	c.release(id)
}

// attachTurn stamps the explicit-stop group's turn id onto the newest open
// parent item, so a command admitted for that turn can be grouped under the
// operation it belongs to.
func (c *endController) attachTurn(turnID string) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	c.mu.Lock()
	for i := len(c.parents) - 1; i >= 0; i-- {
		item, ok := c.items[c.parents[i]]
		if !ok {
			continue
		}
		item.item.TurnID = turnID
		break
	}
	c.mu.Unlock()
}

// beginChildWork registers one delegated child's creation, atomically with its
// admission decision: a frozen conversation refuses the child rather than
// admitting work an accepted end could never see. The item covers the child's
// whole life — from creation, through its card appearing, to its exit — so the
// membership holds exactly one item per live child. The caller holds the item's
// id and releases it when the child exits (ExitSubagent), so pairing is exact
// even when several children start and finish at once.
func (c *endController) beginChildWork(label string) (WorkID, error) {
	return c.begin(WorkKindChild, label, "", "", nil)
}

// bindChildCard binds a delegated child's subagent view id to the child's own
// creation item, so a targeted stop can name it, and gives the item the label
// the card shows. The binding is by creation order: the caller that registered
// the creation passes its item id through, and a card whose caller registered no
// creation (a pipeline or review worker) gets a card-owned item that its finish
// releases.
func (c *endController) bindChildCard(id WorkID, subagentID int64, label string) WorkID {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.childCards == nil {
		c.childCards = make(map[int64]WorkID)
	}
	if id != "" {
		if item, present := c.items[id]; present {
			item.item.SubagentID = subagentID
			if label != "" {
				item.item.Label = boundWorkLabel(label)
			}
			c.childCards[subagentID] = id
			return id
		}
	}
	if existing, ok := c.childCards[subagentID]; ok {
		if item, present := c.items[existing]; present {
			item.item.SubagentID = subagentID
			if label != "" {
				item.item.Label = boundWorkLabel(label)
			}
			return existing
		}
	}
	card := c.reserveLocked(WorkKindChild, label, "", "", nil)
	c.items[card].item.SubagentID = subagentID
	c.childCards[subagentID] = card
	if c.cardOwned == nil {
		c.cardOwned = make(map[WorkID]bool)
	}
	c.cardOwned[card] = true
	return card
}

// endChildWork releases the child-creation item the exiting child registered.
// Releasing by identity — not by scanning for "some child item" — is what keeps
// the membership conservation-exact when several children start and finish at
// once: every registration is released exactly once, by the exit that owns it.
func (c *endController) endChildWork(id WorkID) {
	if id == "" {
		return
	}
	c.release(id)
}

// releaseChildCard drops a child item that a card registered (a pipeline or
// review worker, which has no creation pair), when that worker reports finished.
// A creation-registered child is released by its own exit instead: that is the
// registration which covers the child's whole life, not only the part after its
// card appeared.
func (c *endController) releaseChildCard(subagentID int64) {
	c.mu.Lock()
	id, ok := c.childCards[subagentID]
	if !ok {
		c.mu.Unlock()
		return
	}
	owned := c.cardOwned[id]
	c.mu.Unlock()
	if owned {
		c.release(id)
	}
}

// snapshotLocked builds the membership snapshot, in registration order. The
// controller's lock must be held.
func (c *endController) snapshotLocked() WorkSnapshot {
	snap := WorkSnapshot{Phase: c.phaseLocked()}
	for _, id := range c.order {
		item, ok := c.items[id]
		if !ok {
			continue
		}
		if item.item.Scope != "" {
			snap.Analysis = append(snap.Analysis, item.item)
			continue
		}
		snap.Items = append(snap.Items, item.item)
	}
	// Attached commands are grouped under the operation they belong to, matched
	// by the explicit-stop group they share. Each command keeps its own ID in
	// the membership; grouping is display only.
	byTurn := make(map[string]WorkID, len(snap.Items))
	for _, item := range snap.Items {
		if item.Kind != WorkKindCommand && item.TurnID != "" {
			if _, seen := byTurn[item.TurnID]; !seen {
				byTurn[item.TurnID] = item.ID
			}
		}
	}
	for i := range snap.Items {
		if snap.Items[i].Kind != WorkKindCommand || snap.Items[i].Group != "" {
			continue
		}
		if snap.Items[i].TurnID == "" {
			continue
		}
		if group, ok := byTurn[snap.Items[i].TurnID]; ok && group != snap.Items[i].ID {
			snap.Items[i].Group = group
		}
	}
	return snap
}

// snapshot returns the current membership.
func (c *endController) snapshot() WorkSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

// accept freezes ordinary admission. It reports whether THIS call froze the
// conversation: a second acceptance is a no-op that stops nothing again.
// requireDisplayed, when true, rejects the acceptance outright if any active
// ordinary ID is not in displayed — the refresh path, which stops nothing.
func (c *endController) accept(displayed []string, requireDisplayed bool) (snap WorkSnapshot, expanded []WorkID, frozen bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap = c.snapshotLocked()
	if requireDisplayed {
		seen := make(map[WorkID]bool, len(displayed))
		for _, id := range displayed {
			seen[WorkID(id)] = true
		}
		for _, item := range snap.Items {
			if !seen[item.ID] {
				expanded = append(expanded, item.ID)
			}
		}
	}
	// A refusal freezes nothing: the caller has not accepted THIS membership, so
	// it must be able to refresh and ask again with ordinary work still live. Only
	// an acceptance freezes admission, and only once.
	if len(expanded) > 0 {
		return snap, expanded, false
	}
	if c.phaseLocked() == ExitPhaseOpen {
		c.phase = ExitPhaseStopping
		frozen = true
	}
	return snap, expanded, frozen
}

// stopTargets returns the work ordinary admission's stop pass must act on: the
// still-active commands and children, with each item's cancel hook. It is
// taken under the lock so the pass acts on one consistent membership; the
// callbacks themselves run with no lock held.
func (c *endController) stopTargets(scope string) []endStopTarget {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]endStopTarget, 0, len(c.order))
	for _, id := range c.order {
		item, ok := c.items[id]
		if !ok || item.item.Scope != scope {
			continue
		}
		out = append(out, endStopTarget{
			id:         item.item.ID,
			kind:       item.item.Kind,
			subagentID: item.item.SubagentID,
			turnID:     item.item.TurnID,
			cancel:     item.cancel,
		})
	}
	return out
}

// endStopTarget is one still-active unit of work the stop pass must stop.
type endStopTarget struct {
	id         WorkID
	kind       WorkKind
	subagentID int64
	turnID     string
	cancel     func()
}

// outstanding returns the ordinary items still registered, for an honest
// failed-join report.
func (c *endController) outstanding() []WorkItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []WorkItem
	for _, id := range c.order {
		item, ok := c.items[id]
		if !ok || item.item.Scope != "" {
			continue
		}
		out = append(out, item.item)
	}
	return out
}

// outstandingScoped returns the items still registered under one analysis
// epoch.
func (c *endController) outstandingScoped(scope string) []WorkItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []WorkItem
	for _, id := range c.order {
		item, ok := c.items[id]
		if !ok || item.item.Scope != scope {
			continue
		}
		out = append(out, item.item)
	}
	return out
}

// setPhase moves the conversation's exit phase, refusing to move backwards.
func (c *endController) setPhase(phase ExitPhase) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if phaseRank(phase) < phaseRank(c.phaseLocked()) {
		return
	}
	c.phase = phase
}

// phaseRank orders the phases so a late transition can never reopen an
// earlier one.
func phaseRank(phase ExitPhase) int {
	switch phase {
	case ExitPhaseOpen:
		return 0
	case ExitPhaseStopping:
		return 1
	case ExitPhasePostmortem:
		return 2
	case ExitPhaseClosing:
		return 3
	case ExitPhaseClosed:
		return 4
	}
	return 0
}

// openAnalysis opens one scoped exit-analysis epoch. It is admitted only while
// the postmortem phase is open and only one epoch at a time.
func (c *endController) openAnalysis() (*ExitAnalysisScope, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phaseLocked() != ExitPhasePostmortem {
		return nil, ErrNotPostmortemPhase
	}
	if c.analysisID != "" {
		return nil, ErrExitAnalysisAlreadyOpen
	}
	c.analysisEpoch++
	c.analysisID = fmt.Sprintf("analysis-%d", c.analysisEpoch)
	return &ExitAnalysisScope{id: c.analysisID, epoch: c.analysisEpoch}, nil
}

// closeAnalysis closes the open epoch, so its authorization cannot admit work
// into a conversation that has moved past the analysis.
func (c *endController) closeAnalysis(scope *ExitAnalysisScope) {
	if scope == nil {
		return
	}
	c.mu.Lock()
	if c.analysisID == scope.ID() {
		c.analysisID = ""
	}
	c.mu.Unlock()
}

// hasScope reports whether the given scope is the open epoch's.
func (c *endController) hasScope(scope *ExitAnalysisScope) bool {
	if scope == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.analysisID != "" && c.analysisID == scope.ID()
}

// boundWorkLabel renders one display label: single line, secret-scrubbed and
// bounded. Labels are display data only — nothing pairs work by label — so
// bounding them costs no correctness, and scrubbing them keeps a pasted token
// out of a warning panel and out of any later transcript.
func boundWorkLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	if idx := strings.IndexAny(label, "\r\n"); idx >= 0 {
		label = label[:idx]
	}
	label = strings.Join(strings.Fields(label), " ")
	if len(label) > 0 {
		label = redact.Secrets(label)
	}
	return strutil.Truncate(label, maxWorkLabelRunes, true)
}

// derivedWorkLabel is the label a parent operation registers with when the
// caller supplies none: what the session says it is doing, else its title.
// It is deliberately coarse — the label is for the user's warning panel, and
// the operator's own words ("thinking", "shell.run") are more useful there
// than a guess at intent.
func (s *State) derivedWorkLabel() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	activity := s.activity
	title := s.title
	s.mu.Unlock()
	if activity.Kind != "" && activity.Kind != ActivityIdle && activity.Label != "" {
		return activity.Label
	}
	return title
}

// WorkAdmission returns the end controller's admission seam for the
// conversation's command service. A nil State returns nil, so a caller that
// has no conversation to own cannot install a gate that would refuse every
// command.
func (s *State) WorkAdmission() WorkAdmission {
	if s == nil {
		return nil
	}
	return s
}

// AdmitWork registers one managed command, atomically with its admission
// decision: the membership check that accepts an end takes the same lock, so a
// command admitted here is either part of an accepted end's work or was
// admitted after the end was accepted (which this call refuses).
func (s *State) AdmitWork(req WorkAdmissionRequest) (WorkLease, error) {
	id, err := s.end.begin(WorkKindCommand, req.Command, req.Scope, req.TurnID, nil)
	if err != nil {
		return nil, err
	}
	return &workLease{s: s, id: id}, nil
}

// workLease releases one admitted unit of work.
type workLease struct {
	s  *State
	id WorkID
}

// Release is idempotent and safe from any goroutine.
func (l *workLease) Release() {
	if l == nil || l.s == nil {
		return
	}
	l.s.end.release(l.id)
}

// WorkPhase returns the conversation's exit phase.
func (s *State) WorkPhase() ExitPhase {
	if s == nil {
		return ExitPhaseOpen
	}
	return s.end.phaseNow()
}

// SetWorkPhase moves the conversation's exit phase, refusing to move
// backwards.
func (s *State) SetWorkPhase(phase ExitPhase) {
	if s == nil {
		return
	}
	s.end.setPhase(phase)
}

// WorkMembership returns the registered work snapshot: the ordinary membership
// a warning describes and the scoped analysis epoch, if one is open.
func (s *State) WorkMembership() WorkSnapshot {
	if s == nil {
		return WorkSnapshot{Phase: ExitPhaseOpen}
	}
	return s.end.snapshot()
}

// AcceptWorkDisplayed freezes ordinary admission after checking that every
// currently active ordinary ID was displayed. requireDisplayed rejects the
// acceptance, changing nothing and stopping nothing, when the active set has
// expanded; finished IDs need no reconfirmation and are simply absent from
// expanded. frozen reports whether THIS call froze the conversation.
func (s *State) AcceptWorkDisplayed(displayed []string, requireDisplayed bool) (WorkSnapshot, []WorkID, bool) {
	if s == nil {
		return WorkSnapshot{Phase: ExitPhaseOpen}, nil, false
	}
	return s.end.accept(displayed, requireDisplayed)
}

// WorkStopTargets returns the still-active units of work a stop pass must act
// on. scope == "" selects the ordinary membership; a scope id selects one
// exit-analysis epoch.
func (s *State) WorkStopTargets(scope string) []WorkStopTarget {
	if s == nil {
		return nil
	}
	targets := s.end.stopTargets(scope)
	out := make([]WorkStopTarget, 0, len(targets))
	for _, t := range targets {
		out = append(out, WorkStopTarget{
			ID:         t.id,
			Kind:       t.kind,
			SubagentID: t.subagentID,
			TurnID:     t.turnID,
			cancel:     t.cancel,
		})
	}
	return out
}

// WorkStopTarget is one still-active unit of work. Cancel stops the operation
// that registered it (a runtime turn's work context); it is nil for work whose
// owner cancels it itself.
type WorkStopTarget struct {
	ID         WorkID
	Kind       WorkKind
	SubagentID int64
	TurnID     string
	cancel     func()
}

// Cancel stops the operation that registered this target, when it registered a
// cancel hook. It runs with no conversation lock held.
func (t WorkStopTarget) Cancel() {
	if t.cancel != nil {
		t.cancel()
	}
}

// OutstandingWork returns the ordinary items still registered, for an honest
// failed-join report.
func (s *State) OutstandingWork() []WorkItem {
	if s == nil {
		return nil
	}
	return s.end.outstanding()
}

// OutstandingScopedWork returns the items still registered under one
// exit-analysis epoch.
func (s *State) OutstandingScopedWork(scope *ExitAnalysisScope) []WorkItem {
	if s == nil || scope == nil {
		return nil
	}
	return s.end.outstandingScoped(scope.ID())
}

// WaitForWorkDrain waits for the ordinary membership to empty. It returns
// ctx.Err() when the caller's deadline ends first, so a failed join is
// reported and can be retried instead of latching success.
func (s *State) WaitForWorkDrain(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.end.mu.Lock()
	ch, done := s.end.drainLocked()
	s.end.mu.Unlock()
	if done {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitForScopedWorkDrain waits for one exit-analysis epoch's membership to
// empty, so its work is always joined before closure.
func (s *State) WaitForScopedWorkDrain(ctx context.Context, scope *ExitAnalysisScope) error {
	if s == nil || scope == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if len(s.end.outstandingScoped(scope.ID())) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// OpenExitAnalysis opens one scoped exit-analysis epoch. It is admitted only
// during the postmortem phase: the analysis cannot reopen ordinary work, and
// its work is separate from the conversation's ordinary membership.
func (s *State) OpenExitAnalysis() (*ExitAnalysisScope, error) {
	if s == nil {
		return nil, ErrNotPostmortemPhase
	}
	return s.end.openAnalysis()
}

// CloseExitAnalysis closes the open epoch so its authorization can admit
// nothing more.
func (s *State) CloseExitAnalysis(scope *ExitAnalysisScope) {
	if s == nil {
		return
	}
	s.end.closeAnalysis(scope)
}

// ExitAnalysisOpen reports whether scope is still the open epoch.
func (s *State) ExitAnalysisOpen(scope *ExitAnalysisScope) bool {
	if s == nil {
		return false
	}
	return s.end.hasScope(scope)
}

// ---------------------------------------------------------------------------
// Explicit turn stops
//
// An explicit turn stop (Ctrl+C, /stop, a cancelled review/pipeline/SDD turn, a
// targeted child kill) must have a precise target: the commands still attached
// to that one operation. The work-gate registration above is the natural place
// for it — it already brackets a unit of work — so the stop group lives beside
// it and is registered/cleared the same way, before dispatch and on finish.
//
// Order matters (shared contract 3): the command service accepts the stop FIRST
// — marking that operation's attached executions as stopping and refusing that
// turn any further admission — and only then does the caller cancel the agent's
// wait/provider context. Ordinary cleanup cancels and unregisters WITHOUT
// requesting execution stop, so a turn that ends normally never kills work that
// was left running in the background.
// ---------------------------------------------------------------------------

// ErrNoTurnStopService is returned when a session has no command service to
// stop: a session built without commands (a scoped child, a test fixture) has
// nothing to stop, and silently succeeding would tell the caller an execution
// was marked stopped when no service ever heard it.
var ErrNoTurnStopService = errors.New("session: no command service is registered for turn stops")

// TurnStopService is the conversation-owned command service an explicit turn
// stop reaches. It is deliberately narrow and defined here (rather than in
// internal/tools/native) so the session and the TUI can both name it: the
// native JobManager implements it and app construction injects it.
//
// StopAttached is the admission/detachment synchronization point: it atomically
// marks every command still attached to the turn as stopping and remembers the
// turn as stopped, so a stopped turn cannot admit more commands. Implementations
// must not stop executions that detached (or settled) first, and return the
// affected execution ids.
type TurnStopService interface {
	// StopAttached stops and rejects the attached commands of one turn.
	StopAttached(turnID, cause string) ([]string, error)
	// RequestStop requests termination of one execution: the targeted form
	// used when a stop names a single child instead of a turn.
	RequestStop(executionID, cause string) error
}

// turnStopSeq allocates process-unique turn ids for stop groups. It is
// process-wide (not per session) so a group allocated without a session — a
// runner used directly, which must allocate its own — still names a target no
// other operation can collide with.
var turnStopSeq atomic.Uint64

// allocateTurnID mints the identity a stop group uses at the command service.
func allocateTurnID(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "turn"
	}
	return fmt.Sprintf("%s-%d", prefix, turnStopSeq.Add(1))
}

// TurnStopToken is one operation's stop group: the target an explicit turn stop
// reaches, and the thing a stop registration replaces or clears. It names the
// actor whose commands belong to the group, the group's identity at the service,
// and the service call that accepts the stop.
//
// A token outlives its registration. Once its turn is marked stopped, the token
// keeps reporting that stop — and the service keeps refusing that turn's
// admission — so a late start (a synchronous review worker inheriting the
// parent's token, say) cannot resurrect a stopped turn.
type TurnStopToken struct {
	actor  string
	turnID string
	// service is the conversation's command service. Nil for a session built
	// without commands; Stop then reports that rather than pretending otherwise.
	service TurnStopService

	mu        sync.Mutex
	stopped   bool
	stopCause string
	// stopAccepted marks that this token has already asked the service, so a
	// concurrent second Stop waits for the answer instead of asking twice.
	stopAccepted bool
	stopCh       chan struct{}
	// executionIDs are the executions the accepted stop targeted.
	executionIDs []string
	stopErr      error
}

// NewTurnStopToken allocates a stop group for an operation with no session
// registration yet — app construction at an entry point, or a runner used
// directly with no turn in flight. The turn id is minted here so the group can
// be stopped before its first command exists.
func NewTurnStopToken(service TurnStopService, actor string) *TurnStopToken {
	prefix := "turn"
	if actor = strings.TrimSpace(actor); actor != "" {
		prefix = "turn-" + actor
	}
	return &TurnStopToken{service: service, actor: actor, turnID: allocateTurnID(prefix)}
}

// Actor reports who owns the group.
func (t *TurnStopToken) Actor() string {
	if t == nil {
		return ""
	}
	return t.actor
}

// TurnID reports the group's identity at the command service.
func (t *TurnStopToken) TurnID() string {
	if t == nil {
		return ""
	}
	return t.turnID
}

// Stopped reports whether the group's stop was accepted.
func (t *TurnStopToken) Stopped() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

// StopCause returns the cause of the accepted stop, "" when none was.
func (t *TurnStopToken) StopCause() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopCause
}

// StoppedExecutionIDs returns the executions the accepted stop targeted, in the
// order the service reported them.
func (t *TurnStopToken) StoppedExecutionIDs() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.executionIDs...)
}

// HasService reports whether the group can reach a command service at all.
func (t *TurnStopToken) HasService() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.service != nil
}

// Stop accepts an explicit stop for this group: it asks the service to stop and
// reject the group's attached commands FIRST, records the outcome, and only then
// reports success to the caller — which is what cancels the agent context.
// Service acceptance precedes agent cancellation, so a stop the service never
// accepted cannot cancel the turn's agent work.
//
// Concurrent and repeated stops share one service decision.
func (t *TurnStopToken) Stop(cause string) ([]string, error) {
	if t == nil {
		return nil, nil
	}
	if cause = strings.TrimSpace(cause); cause == "" {
		cause = "turn stopped"
	}

	t.mu.Lock()
	if t.stopAccepted {
		// Another caller already asked; wait for its answer instead of issuing a
		// second stop the service would have to deduplicate.
		ch := t.stopCh
		t.mu.Unlock()
		<-ch
	} else {
		t.stopAccepted = true
		t.stopCause = cause
		t.stopCh = make(chan struct{})
		ch := t.stopCh
		t.mu.Unlock()
		ids, err := t.performStop(cause)
		close(ch)
		return ids, err
	}
	t.mu.Lock()
	ids, err := append([]string(nil), t.executionIDs...), t.stopErr
	t.mu.Unlock()
	return ids, err
}

// performStop asks the service for the stop decision and records it. It runs
// exactly once per token, from whichever Stop call won the race, and always
// outside the token's lock: the service may query the session.
func (t *TurnStopToken) performStop(cause string) ([]string, error) {
	t.mu.Lock()
	service, turnID := t.service, t.turnID
	t.mu.Unlock()
	if service == nil || turnID == "" {
		// Nothing to stop. Record the decision so the caller can still cancel its
		// own agent context, and report it rather than pretending a service
		// accepted a stop that does not exist.
		t.mu.Lock()
		t.stopped = true
		t.stopErr = ErrNoTurnStopService
		t.mu.Unlock()
		return nil, ErrNoTurnStopService
	}
	ids, err := service.StopAttached(turnID, cause)
	t.mu.Lock()
	t.stopped = true
	t.executionIDs = append([]string(nil), ids...)
	t.stopErr = err
	t.mu.Unlock()
	return ids, err
}

// StopTarget requests termination of one execution belonging to this group.
// Unlike Stop it does not mark the group's turn stopped: a targeted child stop
// must not refuse admission for the parent's other work.
func (t *TurnStopToken) StopTarget(executionID, cause string) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	service := t.service
	t.mu.Unlock()
	if service == nil {
		return ErrNoTurnStopService
	}
	return service.RequestStop(executionID, cause)
}

// RequestTurnStop is the session's explicit-stop entry point (contract 3). It
// resolves the registered stop group and asks the command service to stop and
// reject that group's attached commands, then reports the service's decision so
// the caller can cancel the agent context — service acceptance precedes agent
// cancellation.
//
// A stop that arrives before any group is registered is retained: the next
// registration adopts the pending cause, so a keystroke landing while a turn is
// still starting cannot be lost.
func (s *State) RequestTurnStop(cause string) ([]string, error) {
	if s == nil {
		return nil, nil
	}
	cause = strings.TrimSpace(cause)
	s.turnStopMu.Lock()
	token := s.turnStop
	if token == nil {
		// Retain the request for the next registration instead of dropping it.
		s.turnStopPendingCause = cause
		s.turnStopMu.Unlock()
		return nil, nil
	}
	s.turnStopMu.Unlock()
	return token.Stop(cause)
}

// BeginTurnStop registers a stop group for one operation and returns its token
// plus any stop cause requested before the registration existed. The actor is
// stamped here, before any work is dispatched, so a stop reaches the operation
// even while it is still blocked in title generation or provider resolution.
//
// Registration replaces any previous group: one operation at a time is the
// session's contract. A replaced token keeps its own stopped state, and the
// service keeps refusing its turn.
func (s *State) BeginTurnStop(actor string) (token *TurnStopToken, pendingCause string) {
	if s == nil {
		return nil, ""
	}
	prefix := "turn"
	if actor = strings.TrimSpace(actor); actor != "" {
		prefix = "turn-" + actor
	}
	if s.sessionID != "" {
		prefix = s.sessionID + "/" + prefix
	}
	token = &TurnStopToken{actor: actor, turnID: allocateTurnID(prefix)}
	s.turnStopMu.Lock()
	token.service = s.turnStopService
	pendingCause = s.turnStopPendingCause
	s.turnStopPendingCause = ""
	s.turnStop = token
	s.turnStopMu.Unlock()
	// Stamp the group's turn onto this operation's work item, so a command
	// admitted for this turn is shown under the operation it belongs to rather
	// than as an unattached stranger.
	s.end.attachTurn(token.turnID)
	// A stop that arrived before this registration names THIS operation: the
	// user asked to stop before the operation announced itself, so the
	// registration adopts it immediately. Consuming it later would let a turn
	// do work the user already asked to stop.
	if pendingCause != "" {
		_, _ = token.Stop(pendingCause)
	}
	return token, pendingCause
}

// EndTurnStop clears the registration ONLY when it is still the same token, so
// a finishing operation cannot unregister the stop group a newer operation
// installed. Ordinary cleanup uses this: it cancels and unregisters without
// requesting execution stop. Returns whether the registration was cleared.
func (s *State) EndTurnStop(token *TurnStopToken) bool {
	if s == nil || token == nil {
		return false
	}
	s.turnStopMu.Lock()
	defer s.turnStopMu.Unlock()
	if s.turnStop != token {
		return false
	}
	s.turnStop = nil
	return true
}

// CurrentTurnStop returns the registered stop group, or nil.
func (s *State) CurrentTurnStop() *TurnStopToken {
	if s == nil {
		return nil
	}
	s.turnStopMu.Lock()
	defer s.turnStopMu.Unlock()
	return s.turnStop
}

// SetTurnStopService installs the conversation's command service. App
// construction calls it once; a nil service (a read-only filtered registry, a
// session with no commands) makes RequestTurnStop report that nothing can be
// stopped rather than silently succeeding.
func (s *State) SetTurnStopService(service TurnStopService) {
	if s == nil {
		return
	}
	s.turnStopMu.Lock()
	s.turnStopService = service
	s.turnStopMu.Unlock()
}

// GetTurnStopService returns the installed command service, or nil.
func (s *State) GetTurnStopService() TurnStopService {
	if s == nil {
		return nil
	}
	s.turnStopMu.Lock()
	defer s.turnStopMu.Unlock()
	return s.turnStopService
}

// StopExecution requests termination of one execution belonging to this
// conversation by its execution id. It is the targeted form (an agent.kill-style
// stop, or a stop of one child's command) and deliberately does NOT mark any
// turn stopped, so its siblings and the parent operation keep admitting work.
func (s *State) StopExecution(executionID, cause string) error {
	if s == nil {
		return ErrNoTurnStopService
	}
	if strings.TrimSpace(executionID) == "" {
		return errors.New("session: execution id is required")
	}
	service := s.GetTurnStopService()
	if service == nil {
		return ErrNoTurnStopService
	}
	return service.RequestStop(executionID, cause)
}

// stopOriginKey carries one tool-call's execution origin on the handler's
// context. It is a private key type so nothing outside this package can
// overwrite the identity a managed command records.
type stopOriginKey struct{}

// ExecutionOrigin identifies the conversation work one managed command belongs
// to: the conversation, the actor that requested it, the turn whose explicit
// stop targets it, and the originating call. It is defined here, in the shared
// leaf both internal/agent and internal/tools/native already import, because a
// tool handler needs to read it while the package that stamps it cannot be
// imported by the package that consumes it (internal/tools/native's in-package
// tests import internal/agent).
type ExecutionOrigin struct {
	ConversationID string
	Actor          string
	TurnID         string
	StepID         string
	ToolCallID     string
	Generation     int
}

// WithExecutionOrigin stamps an execution origin onto a tool handler's context.
func WithExecutionOrigin(ctx context.Context, origin ExecutionOrigin) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, stopOriginKey{}, origin)
}

// ExecutionOriginFromContext reads the execution origin a runner stamped onto a
// tool handler's context. ok is false for a handler called outside a runner
// turn (a direct tool invocation, a test), where there is no turn to stop.
func ExecutionOriginFromContext(ctx context.Context) (ExecutionOrigin, bool) {
	if ctx == nil {
		return ExecutionOrigin{}, false
	}
	origin, ok := ctx.Value(stopOriginKey{}).(ExecutionOrigin)
	return origin, ok
}

// BeginQuiesce sets the quiescing gate so subsequent BeginWork calls
// are rejected. Callers should call BeginQuiesce before WaitForWork to
// ensure no new work can register between the gate and the wait.
//
// The access gate is taken first so the transition linearizes against an
// in-flight grant: a grant that already passed its final quiesce check runs
// to completion (with its acknowledgment) before the gate closes, and one
// that has not yet reached the check sees the closed gate.
func (s *State) BeginQuiesce() {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
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
// pending question, pending privileged-auth prompt, and steering queue for
// a graceful shutdown. It sends a denial on the approval channel
// (non-blocking), an AnswerUnanswered answer per question on the question
// channel (non-blocking), a denial on the sudo-auth channel (non-blocking,
// carrying no password), and publishes cleared-state events. The channels
// are closed on the send side after all values have been sent (or dropped)
// so the waiter can unblock safely.
//
// After this call, PendingApproval(), PendingQuestion() and
// PendingSudoAuth() return nil. SteeringQueue() is empty except for entries an
// accepted Send now request is still responsible for: those survive, and their
// receipts are marked undelivered rather than erased.
//
// The access gate is held for the whole function: settlement of a typed access
// request and the ordinary cleanup happen under it, so a grant cannot be
// applied after shutdown has decided the session is over, and shutdown cannot
// clear a request a registration is still publishing.
func (s *State) ResolvePendingForShutdown() {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()

	// A typed access request is settled first and through the private helper,
	// while its canonical pointer is still in place: the helper is what
	// clears the session's private access state, publishes the cleared slot,
	// and delivers exactly one cancellation on the canonical channel. Respond
	// is a no-op for it, so going through the ordinary path below would
	// strand the waiter.
	s.mu.Lock()
	typed := s.pendingApproval != nil && s.pendingApproval.Access != nil
	var typedReq AccessRequest
	if typed {
		typedReq = *s.pendingApproval.Access
	}
	actual := AccessState{Mode: s.Config.Agent.ApprovalMode, System: s.systemAccess}
	s.mu.Unlock()
	if typed {
		s.finishAccessCanceledLocked(typedReq, actual, "session is shutting down")
	}

	// Shutdown settles every live prompt through the canonical registry. It
	// runs while each record can still compare its own prompt, and it skips a
	// record another claim already owns: that claim completes under its own
	// resolver contract and must not be rolled back. A typed access request
	// was already settled above by its own helper.
	shutdownErr := errors.New("session is shutting down")
	for _, rec := range s.prompts {
		if rec.settled || rec.claimed || rec.access {
			continue
		}
		rec.claimed = true
		rec.cause = shutdownErr
		deliverCanceledPrompt(rec, shutdownErr)
		s.settlePromptLocked(rec, shutdownErr)
	}
	s.prompts = nil

	// Accepted-but-undelivered steering survives shutdown. The user asked
	// explicitly for it to be delivered, and the spec requires it be reported
	// as undelivered rather than erased: only entries no accepted request is
	// still responsible for are dropped. The receipts are marked here, under
	// the same gate, so the final status is observable before the session goes
	// away. Lock order is accessMu → steeringMu → State.mu, which this holds.
	s.steeringMu.Lock()
	protected := s.protectedSteeringIDsLocked()
	var undelivered []SteeringReceipt
	for _, id := range s.steering.order {
		rec := s.steering.requests[id]
		if rec == nil || rec.delivered {
			continue
		}
		rec.receipt.Status = SteeringUndelivered
		rec.receipt.Version++
		undelivered = append(undelivered, cloneSteeringReceipt(rec.receipt))
	}
	s.steeringMu.Unlock()

	s.mu.Lock()
	approval := s.pendingApproval
	s.pendingApproval = nil
	question := s.pendingQuestion
	s.pendingQuestion = nil
	skillGate := s.pendingSkillGate
	s.pendingSkillGate = nil
	sudoAuth := s.pendingSudoAuth
	s.pendingSudoAuth = nil
	if len(protected) == 0 {
		s.steeringQueue = nil
	} else {
		kept := s.steeringQueue[:0]
		for _, e := range s.steeringQueue {
			if protected[e.ID] {
				kept = append(kept, e)
			}
		}
		s.steeringQueue = kept
	}
	remainingSteering := len(s.steeringQueue)
	s.mu.Unlock()

	// Publish cleared-state events regardless of whether there was a
	// pending item — subscribers that never received the "set" event
	// are unaffected, and those that did get the cleared value. The one
	// exception is the approval slot when a typed request was settled above:
	// finishAccessLocked already announced that clear, and republishing it
	// would be a second nil for one transition.
	if !typed {
		s.publishEvent(EventPendingApprovalChanged, Event{PendingApproval: nil})
	}
	s.publishEvent(EventPendingQuestionChanged, Event{PendingQuestion: nil})
	s.publishEvent(EventPendingSkillGateChanged, Event{PendingSkillGate: nil})
	s.publishEvent(EventPendingSudoAuthChanged, Event{PendingSudoAuth: nil})
	broker := func() *pubsub.Broker[SteeringEvent] {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.steeringBroker
	}()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: remainingSteering})
	}
	for i := range undelivered {
		r := undelivered[i]
		s.publishSteeringState(&r)
	}

	// Deny any pending ordinary approval. A registered prompt was already
	// settled through the canonical registry above, so Respond would re-enter
	// the access gate this function holds; only an unregistered legacy fixture
	// delivers on its own channel here. A typed request was settled above too.
	if approval != nil && approval.Access == nil && approval.canonical == nil {
		approval.Respond(UserApprovalDecision{Approved: false})
	}

	// Answer every pending question with "Unanswered". Registered prompts were
	// settled by the registry loop above (see the approval comment).
	if question != nil && question.canonical == nil {
		question.Respond(UnansweredAnswers(question.Questions))
	}

	// Deny any pending skill gate. Registered prompts were settled above.
	if skillGate != nil && skillGate.canonical == nil {
		skillGate.Respond(SkillGateDeny)
	}

	// Deny any pending privileged-auth prompt. The decision carries no
	// password, so nothing secret is buffered after this point.
	if sudoAuth != nil && sudoAuth.canonical == nil {
		sudoAuth.Respond(SudoAuthDecision{OK: false})
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
	// push reports into the old session's garbage queue.
	s.mu.Lock()
	for _, v := range s.subagents {
		if v.Status == SubagentRunning && v.Cancel != nil {
			v.Cancel()
		}
	}
	// Drain the watch report queue so any still-pending reports are
	// persisted as RoleUser messages before the session ends, rather than
	// being lost at process exit. This restores restart durability for
	// reports that never reached a loop-top drain or turn-end residual
	// handling. The DB is still open at Shutdown (Quiesce keeps it open
	// for downstream consumers), so AddMessage can persist safely. The
	// subagent report queue is cleared without persisting because subagent
	// reports are already persisted at push time.
	residuals := s.watchReports
	s.watchReports = nil
	s.subagentReports = nil
	s.mu.Unlock()

	for _, e := range residuals {
		s.AddMessage(RoleUser, e.text, ContentTypeWatchReport)
	}

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

// SpinnerLabelDwell is the minimum time a pinned activity label stays
// in the turn spinner row before a new label can replace it.
const SpinnerLabelDwell = 500 * time.Millisecond

// PinnedSpinnerLabel returns the activity label to display in the turn
// spinner row, applying a minimum dwell time so fast-cycling tool labels
// don't flicker. When a new label arrives while the current one has not
// yet dwelled for SpinnerLabelDwell, the old label is kept. Once the dwell
// expires, the new label is adopted. A kind change (e.g. tool → approval)
// adopts immediately, bypassing the dwell.
func (s *State) PinnedSpinnerLabel(act Activity) string {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	// First label or kind change: adopt immediately.
	if s.spinnerLabelPinned == "" || act.Kind != s.spinnerLabelPinnedKind {
		s.spinnerLabelPinned = act.Label
		s.spinnerLabelPinnedAt = now
		s.spinnerLabelPinnedKind = act.Kind
		return s.spinnerLabelPinned
	}
	// Same kind, same label: keep it.
	if act.Label == s.spinnerLabelPinned {
		return s.spinnerLabelPinned
	}
	// Same kind, different label: only adopt if the current one has
	// dwelled long enough.
	if now.Sub(s.spinnerLabelPinnedAt) >= SpinnerLabelDwell {
		s.spinnerLabelPinned = act.Label
		s.spinnerLabelPinnedAt = now
		return s.spinnerLabelPinned
	}
	// Keep the old label; the new one will show after the dwell.
	return s.spinnerLabelPinned
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

	visible := s.stepVisibility(s.branchIDsLocked())
	for i := range s.auditLog {
		evt := s.auditLog[i]
		if !visible(evt.StepID) {
			continue
		}
		items = append(items, TranscriptItem{
			Timestamp: evt.Timestamp,
			Kind:      KindAudit,
			Audit:     &evt,
		})
	}

	for i := range s.thinkingLog {
		t := s.thinkingLog[i]
		if !visible(t.StepID) {
			continue
		}
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
	if s.skillOrder == nil {
		s.skillOrder = make(map[string]int64)
	}
	if s.skillBodyAge == nil {
		s.skillBodyAge = make(map[string]int)
	}
	// Re-activating an already-active skill must not refresh its age: age
	// drives eviction, and a repeat skill.load is a body re-fetch,
	// not a claim that the skill became more relevant.
	if _, seen := s.skillOrder[name]; !seen {
		s.skillSeq++
		s.skillOrder[name] = s.skillSeq
		s.skillBodyAge[name] = 0
	}
	s.activeSkills[name] = true
}

// DeactivateSkill removes a skill from the active set. It reports whether
// the skill was active. The skill's body stops being written to the wire on
// the next prompt build.
func (s *State) DeactivateSkill(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeSkills[name] {
		return false
	}
	delete(s.activeSkills, name)
	delete(s.skillOrder, name)
	delete(s.skillBodyAge, name)
	return true
}

// ActiveSkillsByAge returns active skill names oldest activation first.
// Eviction consumes this; the prompt does not. ActiveSkills stays sorted.
func (s *State) ActiveSkillsByAge() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.activeSkills))
	for name := range s.activeSkills {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return s.skillOrder[names[i]] < s.skillOrder[names[j]]
	})
	return names
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

// TickSkillBodyAges advances every active skill's body age by one turn.
// Called once per turn by the agent runner.
func (s *State) TickSkillBodyAges() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.activeSkills {
		s.skillBodyAge[name]++
	}
}

// SkillBodyAge reports how many turns have elapsed since a skill's body was
// last sent in full.
func (s *State) SkillBodyAge(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skillBodyAge[name]
}

// ResetSkillBodyAge marks a skill's body as freshly needed, so the next
// prompt build sends its full text again. A repeat skill.load calls this.
func (s *State) ResetSkillBodyAge(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skillBodyAge == nil {
		s.skillBodyAge = make(map[string]int)
	}
	s.skillBodyAge[name] = 0
}

// ResetAllSkillBodyAges re-sends every active skill's full body on the next
// build. Rollover calls this: compaction discards the prior wire, so every
// active skill has to be re-established from scratch.
func (s *State) ResetAllSkillBodyAges() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.activeSkills {
		s.skillBodyAge[name] = 0
	}
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
