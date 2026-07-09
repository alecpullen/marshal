package session

import (
	"context"
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
	"marshal/internal/tools/registry"
)

// Snapshotter lets the TUI/commands undo/redo via the shadow-git snapshot
// service without importing internal/snapshot.
type Snapshotter interface {
	Track(ctx context.Context) (string, error)
	Diff(ctx context.Context, hash string) (string, error)
	Restore(ctx context.Context, hash string) error
	Revert(ctx context.Context, fromHash, toHash string) error
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

// PendingQuestion is a clarifying question from the agent awaiting the
// user's free-text answer. The runner blocks on ResponseChan; the TUI sends
// exactly one value ("" means the user declined to answer).
type PendingQuestion struct {
	Question     string
	ResponseChan chan string
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
	activeSkills    map[string]bool
	toolBudget      ToolBudget
	swarmProgress   SwarmProgress
	sandbox         SandboxInfo
	trusted         bool
	turnIndex       int
	snapshotter     Snapshotter
	turnUsage       turnUsage
	title           string
	titleSet        bool

	// F14: append-only message tree.
	leafID     int64
	leafDBID   int64
	nextMsgID  int64
	parentOf   map[int64]int64
	childrenOf map[int64][]int64
	msgByID    map[int64]*Message
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

func New(cfg config.Config, workingDir string, now time.Time, p Persistence) *State {
	ctx, cancel := context.WithCancel(context.Background())
	return &State{
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
		parentOf:      make(map[int64]int64),
		childrenOf:    make(map[int64][]int64),
		msgByID:       make(map[int64]*Message),
		nextMsgID:     1,
	}
}

func (s *State) SessionID() string { return s.sessionID }

func (s *State) Logger() *slog.Logger { return s.logger }

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

func (s *State) persistenceEnabled() bool {
	return s.db != nil && s.sessionID != "" && s.logger != nil
}

// appendMessage is the shared body for AddMessage / AddMessageFinal /
// AddMessageSalvaged. It records the message in the in-memory tree (with
// its parent being the current leaf) and best-effort persists to the DB,
// promoting the in-memory id to the DB-assigned id on success so the tree
// stays consistent with the database.
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

	id := s.nextMsgID
	s.nextMsgID++
	parent := s.leafID
	var parentDBID int64
	if s.persistenceEnabled() {
		parentDBID = s.leafDBID
	} else {
		// In in-memory mode, the leaf id is the in-memory id and is
		// used as the parent for the next message.
		parentDBID = 0
	}
	msg := Message{
		ID:            id,
		ParentID:      parent,
		Role:          role,
		Content:       content,
		ContentType:   contentType,
		Reasoning:     reasoning,
		ThinkDuration: thinkDuration,
		CreatedAt:     time.Now(),
		Final:         final,
		Salvaged:      salvaged,
		SalvageReason: salvageReason,
	}
	s.messages = append(s.messages, msg)
	s.parentOf[id] = parent
	if parent != 0 {
		s.childrenOf[parent] = append(s.childrenOf[parent], id)
	}
	ptr := &s.messages[len(s.messages)-1]
	s.msgByID[id] = ptr
	s.leafID = id
	s.mu.Unlock()

	if s.persistenceEnabled() {
		dbID, err := s.db.SaveMessage(s.sessionID, string(role), content, string(contentType), msg.CreatedAt, reasoning, thinkDuration, final, parentDBID)
		if err != nil {
			s.logger.Error("save message failed", "error", err, "session_id", s.sessionID, "role", role)
			return
		}
		s.mu.Lock()
		ptr.ID = dbID
		s.leafDBID = dbID
		// promote the in-memory map key from transient id to DB id so
		// parentOf / childrenOf stay consistent with persisted ids.
		if _, ok := s.msgByID[id]; ok {
			delete(s.msgByID, id)
			s.msgByID[dbID] = ptr
		}
		// re-record the parent / child relations using the DB id.
		if parent != 0 {
			delete(s.parentOf, id)
			s.parentOf[dbID] = parent
			children := s.childrenOf[parent]
			for i, c := range children {
				if c == id {
					children[i] = dbID
					break
				}
			}
			s.childrenOf[parent] = children
		}
		s.leafID = dbID
		s.mu.Unlock()
	}
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
	s.mu.Unlock()
}

// AppendThinking appends a chunk of reasoning/thinking text to the
// in-progress message.
func (s *State) AppendThinking(delta string) {
	s.mu.Lock()
	s.inProgress.Reasoning += delta
	s.mu.Unlock()
}

// EndStreaming marks the in-progress message inactive. Reasoning captured so
// far is preserved (not cleared) so a subsequent AddMessage call can still
// pick it up.
func (s *State) EndStreaming() {
	s.mu.Lock()
	s.inProgress.Active = false
	s.mu.Unlock()
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
	defer s.mu.Unlock()
	s.pendingApproval = tc
}

func (s *State) PendingApproval() *PendingToolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingApproval
}

func (s *State) SetPendingQuestion(q *PendingQuestion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingQuestion = q
}

func (s *State) PendingQuestion() *PendingQuestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingQuestion
}

func (s *State) SetActiveToolCall(atc ActiveToolCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeToolCall = &atc
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
	defer s.mu.Unlock()
	s.activeToolCall = nil
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
	defer s.mu.Unlock()
	if a.Kind == "" {
		a.Kind = ActivityIdle
	}
	s.activity = a
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
	s.mu.Unlock()

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

func (s *State) DeactivateSkill(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activeSkills, name)
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
