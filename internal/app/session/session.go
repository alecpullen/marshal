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
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role      Role
	Content   string
	CreatedAt time.Time
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

type State struct {
	Config     config.Config
	WorkingDir string
	StartedAt  time.Time
	DB         *db.DB
	SessionID  string
	Logger     *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu              sync.Mutex
	messages        []Message
	providerErr     error
	pendingApproval *PendingToolCall
	sessionRules    []string
	auditLog        []registry.AuditEvent
	lastBackup      []BackupFile
}

func New(cfg config.Config, workingDir string, now time.Time, database *db.DB, sessionID string, logger *slog.Logger) *State {
	ctx, cancel := context.WithCancel(context.Background())
	return &State{
		Config:     cfg,
		WorkingDir: workingDir,
		StartedAt:  now,
		DB:         database,
		SessionID:  sessionID,
		Logger:     logger,
		ctx:        ctx,
		cancel:     cancel,
	}
}

func (s *State) AddMessage(role Role, content string) {
	msg := Message{
		Role:      role,
		Content:   content,
		CreatedAt: time.Now(),
	}

	s.mu.Lock()
	s.messages = append(s.messages, msg)
	s.mu.Unlock()

	if s.DB != nil && s.SessionID != "" && s.Logger != nil {
		// Best-effort persistence; do not fail the in-memory transcript.
		if err := s.DB.SaveMessage(s.SessionID, string(role), content, msg.CreatedAt); err != nil {
			s.Logger.Error("save message failed", "error", err, "session_id", s.SessionID, "role", role)
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

	if s.DB != nil && s.SessionID != "" && s.Logger != nil {
		if err := s.DB.SaveToolCall(s.SessionID, event); err != nil {
			s.Logger.Error("save tool call failed", "error", err, "session_id", s.SessionID, "tool", event.ToolName)
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
