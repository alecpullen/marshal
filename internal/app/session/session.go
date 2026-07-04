package session

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role          Role
	Content       string
	Reasoning     string
	ThinkDuration time.Duration
	CreatedAt     time.Time
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

type PendingToolCall struct {
	ID           string
	Name         string
	Args         string
	Command      string
	Risk         string
	Reason       string
	Diff         string // Added field for patch rendering
	ResponseChan chan UserApprovalDecision
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
	sessionRules    []string
	auditLog        []registry.AuditEvent
	lastBackup      []BackupFile
	contextPack     contextpack.Pack
	activeRoute     RouteInfo
	turnToolCache   map[string]registry.ToolResult
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
	}
}

func (s *State) persistenceEnabled() bool {
	return s.db != nil && s.sessionID != "" && s.logger != nil
}

func (s *State) AddMessage(role Role, content string) {
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

	msg := Message{
		Role:          role,
		Content:       content,
		Reasoning:     reasoning,
		ThinkDuration: thinkDuration,
		CreatedAt:     time.Now(),
	}
	s.messages = append(s.messages, msg)
	s.mu.Unlock()

	if s.persistenceEnabled() {
		// Best-effort persistence; do not fail the in-memory transcript.
		if err := s.db.SaveMessage(s.sessionID, string(role), content, msg.CreatedAt, reasoning, thinkDuration); err != nil {
			s.logger.Error("save message failed", "error", err, "session_id", s.sessionID, "role", role)
		}
	}
}

func (s *State) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	messages := make([]Message, len(s.messages))
	copy(messages, s.messages)
	return messages
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

	s.AddMessage(RoleSystem, "System notice: The user has rolled back the last patch. All modified files have been reverted to their original state.")
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
