package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"sort"

	"marshal/internal/app/config"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/pubsub"
	"marshal/internal/tools/registry"
)

// SteeringEvent is the broker payload published when a message is pushed
// to the steering queue (F16). The session package owns the event type
// per F19 R4 — "event types are defined next to their owning service".
type SteeringEvent struct {
	QueueLen int
	Message  string
}

// Session event types (F21). Defined next to the owning service per F19
// R4. Type strings are opaque identifiers agreed between the session
// publisher and the ACP / external subscribers.
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

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type ContentType string

const (
	ContentTypePlain      ContentType = "plain"
	ContentTypeMarkdown   ContentType = "markdown"
	ContentTypeCode       ContentType = "code"
	ContentTypePlan       ContentType = "plan"
	ContentTypeDiff       ContentType = "diff"
	ContentTypeToolResult ContentType = "tool_result"
)

// ThinkingEntry captures reasoning text that led to a tool call. Unlike
// Message.Reasoning (which is attached to a final answer), ThinkingEntry
// preserves intermediate reasoning that the agent produced before calling a
// tool — reasoning that would otherwise be lost when the next BeginStreaming
// call resets the in-progress buffer.
type ThinkingEntry struct {
	Text      string
	Duration  time.Duration
	StartedAt time.Time
}

type TranscriptKind int

const (
	KindMessage TranscriptKind = iota
	KindThinking
	KindAudit
)

type TranscriptItem struct {
	Timestamp time.Time
	Kind      TranscriptKind
	Message   *Message
	Audit     *registry.AuditEvent
	Thinking  *ThinkingEntry
}

type ActivityKind string

const (
	ActivityIdle     ActivityKind = "idle"
	ActivityThinking ActivityKind = "thinking"
	ActivityTool     ActivityKind = "tool"
	ActivityApproval ActivityKind = "approval"
	ActivityQuestion ActivityKind = "question"
)

type Activity struct {
	Kind      ActivityKind
	Label     string
	StartedAt time.Time
}

type Message struct {
	ID            int64
	ParentID      int64
	Role          Role
	Content       string
	ContentType   ContentType
	Reasoning     string
	ThinkDuration time.Duration
	CreatedAt     time.Time
	Final         bool
	Salvaged      bool
	SalvageReason string
}

// InProgressMessage holds the reasoning text accumulated for the model call
// currently in flight (if any). It is not itself a Message: it becomes the
// Reasoning/ThinkDuration of the next Message added via AddMessage, at which
// point it is cleared for the next call.
type InProgressMessage struct {
	Reasoning string
	StartedAt time.Time
	Active    bool
}

type UserApprovalDecision struct {
	Approved bool
	Edited   string
}

// Question is a single clarifying question presented to the user. Options
// triggers select/multi-select rendering in the TUI; Multi selects between
// NewSelect and NewMultiSelect. AllowOther enables the "Other" affordance
// that lets the user type a free-text answer that's not in the option list.
type Question struct {
	Question   string   `json:"question"`
	Options    []string `json:"options,omitempty"`
	Multi      bool     `json:"multi,omitempty"`
	AllowOther bool     `json:"allow_other,omitempty"`
}

// Answer is the user's response to one Question. When the user hits Esc on
// a question, the TUI records the literal string AnswerUnanswered so the
// agent can see exactly which questions were skipped.
type Answer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// AnswerUnanswered is the sentinel Answer.Answer value recorded when the
// user hits Esc on a question without picking a value. The runner detects
// this sentinel and treats the question as skipped (see runner.go's
// allUnanswered tracking).
const AnswerUnanswered = "Unanswered"

// UnansweredAnswers returns one AnswerUnanswered answer per question, in
// order. Used by shutdown paths and by transports that cannot ask
// interactively (ACP) or pre-fill dismissals (TUI question form).
func UnansweredAnswers(questions []Question) []Answer {
	answers := make([]Answer, len(questions))
	for i, q := range questions {
		answers[i] = Answer{Question: q.Question, Answer: AnswerUnanswered}
	}
	return answers
}

// PendingQuestion carries one or more Questions from the agent awaiting
// user response. The runner blocks on ResponseChan; the TUI sends exactly
// one value, one Answer per Question (in the same order).
type PendingQuestion struct {
	Questions    []Question
	ResponseChan chan []Answer
	responded    sync.Once
}

// Respond sends answers to the response channel exactly once (guarded by
// sync.Once). It is safe to call multiple times; only the first call
// produces a send. The send is non-blocking so unbuffered channels with no
// receiver don't deadlock. The channel is closed after the send so the
// runner can detect that no more responses are coming.
func (p *PendingQuestion) Respond(a []Answer) {
	if p == nil {
		return
	}
	p.responded.Do(func() {
		if p.ResponseChan != nil {
			select {
			case p.ResponseChan <- a:
			default:
			}
			close(p.ResponseChan)
		}
	})
}

type PendingToolCall struct {
	ID           string
	Name         string
	Args         string
	Command      string
	Risk         string
	Reason       string
	Diff         string // Added field for patch rendering
	Schema       string // Details / schema / description of the tool
	ResponseChan chan UserApprovalDecision
	responded    sync.Once
}

// Respond sends a UserApprovalDecision to the response channel exactly once
// (guarded by sync.Once). It is safe to call multiple times; only the first
// call produces a send. The send is non-blocking so unbuffered channels with
// no receiver don't deadlock. The channel is closed after the send so the
// runner can detect that no more responses are coming.
func (p *PendingToolCall) Respond(d UserApprovalDecision) {
	if p == nil {
		return
	}
	p.responded.Do(func() {
		if p.ResponseChan != nil {
			select {
			case p.ResponseChan <- d:
			default:
			}
			close(p.ResponseChan)
		}
	})
}

type ActiveToolCall struct {
	Name      string
	Args      string
	StartedAt time.Time
}

type BackupFile struct {
	Path    string
	Content string
	Mode    os.FileMode
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
	Legacy    bool
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
	Config     config.Config
	WorkingDir string
	StartedAt  time.Time
	db         *db.DB
	sessionID  string
	logger     *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu              sync.Mutex
	messages        []Message
	inProgress      InProgressMessage
	providerErr     error
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
	activity        Activity
	plan            []string
	todos           []db.TodoItem
	activeSkills    map[string]bool
	loadedTools     map[string]bool
	toolBudget      ToolBudget
	swarmProgress   SwarmProgress
	sddProgress     SDDProgress
	sandbox         SandboxInfo
	browser         BrowserInfo
	trusted         bool
	turnIndex       int
	snapshotter     Snapshotter

	runningJobs     int
	subagentDepth   int
	subagentConcurr int
	turnUsage       turnUsage
	title           string
	titleSet        bool

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

	// F16: steering queue (mid-turn user messages). Published to the broker
	// so the TUI transcript and status line update without polling.
	steeringQueue  []string
	steeringBroker *pubsub.Broker[SteeringEvent]

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
	msgByID    map[int64]*Message
	// dbIDToImID maps a persisted DB message id to the in-memory id
	// allocated for it at cold start. Only used during loadFromDB and set
	// to nil when the load completes. Holding s.mu around accesses.
	dbIDToImID map[int64]int64
}

type turnUsage struct {
	used   int
	window int
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

func New(cfg config.Config, workingDir string, now time.Time, p Persistence, opts ...Option) *State {
	ctx, cancel := context.WithCancel(context.Background())
	s := &State{
		Config:        cfg,
		WorkingDir:    workingDir,
		StartedAt:     now,
		db:            p.DB,
		sessionID:     p.SessionID,
		logger:        p.Logger,
		ctx:           ctx,
		cancel:        cancel,
		turnToolCache: make(map[string]registry.ToolResult),
		activity:      Activity{Kind: ActivityIdle},
		activeSkills:  make(map[string]bool),
		loadedTools:   make(map[string]bool),
		parentOf:      make(map[int64]int64),
		childrenOf:    make(map[int64][]int64),
		msgByID:       make(map[int64]*Message),
		dbIDToImID:    make(map[int64]int64),
		nextMsgID:     1,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.persistenceEnabled() {
		s.loadFromDB()
	}
	return s
}

// loadFromDB reconstructs the in-memory message tree for the current
// session from the persisted rows. It is called once from New when
// persistence is enabled. If the DB has no messages for this session
// (or the session is missing), the state stays empty.
//
// Concurrency: takes s.mu for the entire load. Idempotency: refuses to
// run if the in-memory tree is already populated, so calling New twice
// on the same State (e.g. accidental double-init) is a no-op rather than
// a duplicate-load.
func (s *State) loadFromDB() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.msgByID) > 0 {
		// Already loaded (or messages were added in-memory). Avoid
		// clobbering live state.
		return
	}

	leafDBID, err := s.db.GetLeafMessageID(s.sessionID)
	if err != nil {
		s.logger.Error("loadFromDB: get leaf", "error", err, "session_id", s.sessionID)
		s.loadErr = err
		return
	}
	if leafDBID == 0 {
		return
	}

	dbMessages, err := s.db.MessagesOnBranch(s.sessionID, leafDBID)
	if err != nil {
		s.logger.Error("loadFromDB: messages on branch", "error", err, "session_id", s.sessionID, "leaf", leafDBID)
		s.loadErr = err
		return
	}
	if len(dbMessages) == 0 {
		return
	}

	// Walk root -> leaf (MessagesOnBranch returns chronological order).
	// Allocate a stable in-memory id per loaded row starting at nextMsgID,
	// and record parent / child relations using the in-memory ids so the
	// tree mirror is internally consistent.
	for _, dm := range dbMessages {
		imID := s.nextMsgID
		s.nextMsgID++

		// Find this row's in-memory parent id: it was the in-memory id
		// we allocated for dm.ParentID, which equals imID-1 only for
		// a linear branch. Look it up via a dbID->imID translation
		// table built on the fly (cheap for cold-start sizes).
		var imParent int64
		if dm.ParentID > 0 {
			imParent = s.dbIDToImID[dm.ParentID]
		}

		thinkDur := time.Duration(0)
		if dm.ThinkDurationMs > 0 {
			thinkDur = time.Duration(dm.ThinkDurationMs) * time.Millisecond
		}
		msg := Message{
			ID:            imID,
			ParentID:      imParent,
			Role:          Role(dm.Role),
			Content:       dm.Content,
			ContentType:   ContentType(dm.ContentType),
			Reasoning:     dm.Reasoning,
			ThinkDuration: thinkDur,
			CreatedAt:     dm.CreatedAt,
			Final:         dm.Final,
		}
		s.parentOf[imID] = imParent
		if imParent != 0 {
			s.childrenOf[imParent] = append(s.childrenOf[imParent], imID)
		}
		s.dbIDToImID[dm.ID] = imID
		// Stash on the msgByID map so rebuildActiveBranch can find it.
		// We don't append to s.messages here — that is the job of
		// rebuildActiveBranch (single source of truth for ordering).
		s.msgByID[imID] = &msg
	}

	// The last row in dbMessages is the leaf; record both its in-memory
	// id and its DB id so the next AddMessage picks up the right parent.
	leaf := dbMessages[len(dbMessages)-1]
	imLeaf := s.dbIDToImID[leaf.ID]
	s.leafID = imLeaf
	s.leafDBID = leaf.ID
	s.rebuildActiveBranch()
	// The translation map is only needed while loading; drop it so the
	// session doesn't carry a stale dual-id mapping for its lifetime.
	s.dbIDToImID = nil
}

func (s *State) SessionID() string { return s.sessionID }

func (s *State) Logger() *slog.Logger { return s.logger }

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

// PushSteering appends a message typed by the user mid-turn. It is a
// steering message (consumed at the next loop-top) if a turn is running,
// or the next follow-up turn if the turn has already ended. Publishing
// to the broker lets the TUI re-render the queued marker without polling.
func (s *State) PushSteering(msg string) {
	s.mu.Lock()
	s.steeringQueue = append(s.steeringQueue, msg)
	broker := s.steeringBroker
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: queueLen, Message: msg})
	}
}

// SteeringQueue returns a copy of the current queue (does not drain).
func (s *State) SteeringQueue() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.steeringQueue...)
}

// DrainSteering returns and clears the queue atomically. The runner
// calls this at every loop-top to inject queued messages into the live
// model context.
func (s *State) DrainSteering() []string {
	s.mu.Lock()
	out := s.steeringQueue
	s.steeringQueue = nil
	broker := s.steeringBroker
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: queueLen})
	}
	return out
}

// PopSteering returns and removes the oldest queued steering message.
// Used by the TUI's follow-up turn path so each Enter pops one item
// at a time; DrainSteering handles the rest when the next turn starts.
func (s *State) PopSteering() (string, bool) {
	s.mu.Lock()
	if len(s.steeringQueue) == 0 {
		s.mu.Unlock()
		return "", false
	}
	oldest := s.steeringQueue[0]
	s.steeringQueue = s.steeringQueue[1:]
	broker := s.steeringBroker
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: queueLen})
	}
	return oldest, true
}

// ClearSteering drops all queued messages without returning them. Used
// by the TUI's Ctrl+X clear and the interrupt path (cancelTurn).
func (s *State) ClearSteering() {
	s.mu.Lock()
	s.steeringQueue = nil
	broker := s.steeringBroker
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: queueLen})
	}
}

// SetSteeringBroker wires the pub/sub broker so PushSteering publishes.
func (s *State) SetSteeringBroker(b *pubsub.Broker[SteeringEvent]) {
	s.mu.Lock()
	s.steeringBroker = b
	s.mu.Unlock()
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
// hard limits documented in Milestone P (depth 1, concurrency 2). They live
// on the shared session state so any agent.run invocation — even concurrent
// ones from different Go routines — sees the same counters.
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
// increments it; ExitSubagent decrements it. The concurrency guard caps
// parallel sibling subagents the same parent may have running at once.
const (
	subagentMaxDepth       = 1
	subagentMaxConcurrency = 2
)

var (
	ErrSubagentDepthLimit       = fmt.Errorf("session: subagent depth limit exceeded (max %d)", subagentMaxDepth)
	ErrSubagentConcurrencyLimit = fmt.Errorf("session: subagent concurrency limit exceeded (max %d)", subagentMaxConcurrency)
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
	if s.subagentConcurr >= subagentMaxConcurrency {
		return ErrSubagentConcurrencyLimit
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

// SetSubagentConcurrency is a test-only override that lets unit tests
// simulate prior in-flight subagent entries from outside the goroutine
// that would normally hold the lock. Production code uses EnterSubagent.
// Depth is no longer settable here — use WithDepth at construction time.
func (s *State) SetSubagentConcurrency(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subagentConcurr = n
}

func (s *State) persistenceEnabled() bool {
	return s.db != nil && s.sessionID != "" && s.logger != nil
}

// appendMessage is the shared body for AddMessage / AddMessageFinal /
// AddMessageSalvaged. It persists first (best effort) and then inserts the
// message into the in-memory tree exactly once, with its final id, so no
// transient id is ever published to event subscribers or re-keyed later.
// s.mu is held across the DB write: appends are per-session serialized
// anyway, and holding it closes the race where a concurrent Rewind /
// SwitchBranch could orphan a stashed pointer mid-promotion.
func (s *State) appendMessage(role Role, content string, contentType ContentType, final bool, salvaged bool, salvageReason string) {
	s.mu.Lock()
	reasoning := s.inProgress.Reasoning
	var thinkDuration time.Duration
	if reasoning != "" {
		thinkDuration = time.Since(s.inProgress.StartedAt)
		if thinkDuration <= 0 {
			thinkDuration = time.Millisecond
		}
	}
	s.inProgress = InProgressMessage{}

	parent := s.leafID
	createdAt := time.Now()

	var id int64
	persisted := false
	if s.persistenceEnabled() {
		dbID, err := s.db.SaveMessage(s.sessionID, string(role), content, string(contentType), createdAt, reasoning, thinkDuration, final, s.leafDBID)
		if err != nil {
			s.logger.Error("save message failed", "error", err, "session_id", s.sessionID, "role", role)
		} else {
			id = dbID
			s.leafDBID = dbID
			persisted = true
		}
	}
	if !persisted {
		// In-memory mode (or DB write failed): fall back to the transient
		// counter. The message still lands in the tree, it just isn't
		// persisted — same degrade semantics as before.
		id = s.nextMsgID
		s.nextMsgID++
	}

	msg := Message{
		ID:            id,
		ParentID:      parent,
		Role:          role,
		Content:       content,
		ContentType:   contentType,
		Reasoning:     reasoning,
		ThinkDuration: thinkDuration,
		CreatedAt:     createdAt,
		Final:         final,
		Salvaged:      salvaged,
		SalvageReason: salvageReason,
	}
	s.messages = append(s.messages, msg)
	s.parentOf[id] = parent
	if parent != 0 {
		s.childrenOf[parent] = append(s.childrenOf[parent], id)
	}
	s.msgByID[id] = &s.messages[len(s.messages)-1]
	s.leafID = id
	published := msg
	s.mu.Unlock()

	s.publishEvent(EventMessageAdded, Event{Message: &published})
}

func (s *State) AddMessage(role Role, content string, contentType ContentType) {
	s.appendMessage(role, content, contentType, false, false, "")
}

func (s *State) AddMessageFinal(role Role, content string, contentType ContentType) {
	s.appendMessage(role, content, contentType, true, false, "")
}

func (s *State) AddMessageSalvaged(role Role, content string, contentType ContentType, reason string) {
	s.appendMessage(role, content, contentType, true, true, reason)
}

func (s *State) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	messages := make([]Message, len(s.messages))
	copy(messages, s.messages)
	return messages
}

// rebuildActiveBranch reassembles s.messages as the path root -> leafID
// using the in-memory parent/child maps. Caller must hold s.mu.
func (s *State) rebuildActiveBranch() {
	if s.leafID == 0 {
		s.messages = nil
		return
	}
	var path []int64
	for cur := s.leafID; cur != 0; cur = s.parentOf[cur] {
		path = append(path, cur)
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	msgs := make([]Message, 0, len(path))
	for _, id := range path {
		if m, ok := s.msgByID[id]; ok {
			msgs = append(msgs, *m)
		}
	}
	s.messages = msgs
}

// Rewind moves the active leaf to the parent of the message identified by
// turnMsgID (i.e. just before that turn), so the next AddMessage starts a
// new branch. Returns the new leaf id. Does NOT restore files — the caller
// (/rewind) does that via Snapshotter.
func (s *State) Rewind(turnMsgID int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, ok := s.parentOf[turnMsgID]
	if !ok {
		return s.leafID
	}
	s.leafID = parent
	if parent == 0 {
		s.leafDBID = 0
	}
	s.rebuildActiveBranch()
	if s.persistenceEnabled() {
		_ = s.db.SetBranchLeaf(s.sessionID, parent)
	}
	return parent
}

// Branches returns the in-memory branch tips (messages with no children),
// sorted ascending by id.
func (s *State) Branches() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var leaves []int64
	for id := range s.msgByID {
		if len(s.childrenOf[id]) == 0 {
			leaves = append(leaves, id)
		}
	}
	sort.Slice(leaves, func(i, j int) bool { return leaves[i] < leaves[j] })
	return leaves
}

// SwitchBranch sets the active leaf to leafID and rebuilds the
// active-branch view (s.messages) to match.
func (s *State) SwitchBranch(leafID int64) {
	s.mu.Lock()
	s.leafID = leafID
	s.rebuildActiveBranch()
	s.mu.Unlock()

	if s.persistenceEnabled() {
		_ = s.db.SetBranchLeaf(s.sessionID, leafID)
	}
}

// LeafID returns the active-branch leaf id.
func (s *State) LeafID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leafID
}

// ClearMessages removes all messages from the transcript and returns the
// count of messages that were cleared. It does not affect the audit log,
// pending approvals, backups, or context pack.
func (s *State) ClearMessages() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := len(s.messages)
	s.messages = nil
	return count
}

// BeginStreaming starts a new in-progress message, resetting any reasoning
// left over from a previous call. Call this once per model call that may
// stream reasoning content, before consuming its event stream.
func (s *State) BeginStreaming() {
	s.mu.Lock()
	s.inProgress = InProgressMessage{StartedAt: time.Now(), Active: true}
	snap := s.inProgress
	s.mu.Unlock()
	s.publishEvent(EventThinkingChanged, Event{Thinking: &snap})
}

// AppendThinking appends a chunk of reasoning/thinking text to the
// in-progress message.
func (s *State) AppendThinking(delta string) {
	s.mu.Lock()
	s.inProgress.Reasoning += delta
	snap := s.inProgress
	s.mu.Unlock()
	s.publishEvent(EventThinkingChanged, Event{Thinking: &snap})
}

// EndStreaming marks the in-progress message inactive. Reasoning captured so
// far is preserved (not cleared) so a subsequent AddMessage call can still
// pick it up.
func (s *State) EndStreaming() {
	s.mu.Lock()
	s.inProgress.Active = false
	snap := s.inProgress
	s.mu.Unlock()
	s.publishEvent(EventThinkingChanged, Event{Thinking: &snap})
}

func (s *State) LogThinking(entry ThinkingEntry) {
	s.mu.Lock()
	s.thinkingLog = append(s.thinkingLog, entry)
	s.mu.Unlock()
}

// InProgress returns a copy of the current in-progress message.
func (s *State) InProgress() InProgressMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inProgress
}

func (s *State) Shutdown() {
	s.cancel()
}

func (s *State) Done() <-chan struct{} {
	return s.ctx.Done()
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

// SetProviderError records the most recent provider-level failure (HTTP
// error, malformed response, connection failure, etc.) for display in the
// TUI. Passing nil clears it — callers should clear on the next
// successful call. Nothing in this milestone calls this yet; it exists so
// a future agent loop has a place to report provider failures without
// further session.State changes.
func (s *State) SetProviderError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providerErr = err
}

func (s *State) ProviderError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.providerErr
}

func (s *State) SetPendingApproval(tc *PendingToolCall) {
	s.mu.Lock()
	s.pendingApproval = tc
	var snap *PendingToolCall
	if tc != nil {
		copy := *tc
		snap = &copy
	}
	s.mu.Unlock()
	s.publishEvent(EventPendingApprovalChanged, Event{PendingApproval: snap})
}

func (s *State) PendingApproval() *PendingToolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingApproval
}

func (s *State) SetPendingQuestion(q *PendingQuestion) {
	s.mu.Lock()
	s.pendingQuestion = q
	var snap *PendingQuestion
	if q != nil {
		copy := *q
		snap = &copy
	}
	s.mu.Unlock()
	s.publishEvent(EventPendingQuestionChanged, Event{PendingQuestion: snap})
}

func (s *State) PendingQuestion() *PendingQuestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingQuestion
}

func (s *State) SetActiveToolCall(atc ActiveToolCall) {
	s.mu.Lock()
	s.activeToolCall = &atc
	copy := atc
	s.mu.Unlock()
	s.publishEvent(EventActiveToolChanged, Event{ActiveTool: &copy})
}

func (s *State) ActiveToolCall() (ActiveToolCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeToolCall == nil {
		return ActiveToolCall{}, false
	}
	return *s.activeToolCall, true
}

func (s *State) ClearActiveToolCall() {
	s.mu.Lock()
	s.activeToolCall = nil
	s.mu.Unlock()
	s.publishEvent(EventActiveToolChanged, Event{ActiveTool: nil})
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
	s.mu.Unlock()
	if s.persistenceEnabled() {
		if err := s.db.SaveTodos(s.sessionID, s.todos); err != nil {
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
	published := event
	s.mu.Unlock()

	s.publishEvent(EventAuditAdded, Event{Audit: &published})

	if s.persistenceEnabled() {
		if err := s.db.SaveToolCall(s.sessionID, event); err != nil {
			s.logger.Error("save tool call failed", "error", err, "session_id", s.sessionID, "tool", event.ToolName)
		}
	}
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

func (s *State) StoreBackup(backups []BackupFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastBackup = backups
}

func (s *State) Backup() []BackupFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	backups := make([]BackupFile, len(s.lastBackup))
	copy(backups, s.lastBackup)
	return backups
}

func (s *State) ClearBackup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastBackup = nil
}

func (s *State) HasBackup() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lastBackup) > 0
}

func (s *State) RollbackBackup() error {
	s.mu.Lock()
	backups := make([]BackupFile, len(s.lastBackup))
	copy(backups, s.lastBackup)
	s.lastBackup = nil
	s.mu.Unlock()

	if len(backups) == 0 {
		return fmt.Errorf("no backup available")
	}
	for _, bf := range backups {
		path := filepath.Join(s.WorkingDir, bf.Path)
		if err := os.WriteFile(path, []byte(bf.Content), bf.Mode); err != nil {
			return err
		}
	}

	s.AddMessage(RoleSystem, "System notice: The user has rolled back the last patch. All modified files have been reverted to their original state.", ContentTypePlain)
	return nil
}

func (s *State) ClearTurnToolCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnToolCache = make(map[string]registry.ToolResult)
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
	s.turnToolCache[key] = result
}

func (s *State) ActivateSkill(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeSkills[name] = true
}

func (s *State) ActiveSkills() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.activeSkills))
	for name := range s.activeSkills {
		names = append(names, name)
	}
	return names
}

func (s *State) HasActiveSkill(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeSkills[name]
}

// LoadedToolNames returns a sorted copy of the MCP tool names the agent
// has explicitly opted into via tools.select during this session. The
// agent prompt builder uses this to expand the deferred tool list back
// into the prompt once the agent confirms it needs a particular tool.
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

// AddLoadedToolNames records that the agent has opted into the given MCP
// tool names for the remainder of the session. Names are de-duplicated;
// unknown names are accepted without error so callers can pass through
// the full requested set.
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
