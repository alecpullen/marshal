package session

import (
	"context"
	"sync"
	"time"

	"marshal/internal/app/config"
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

type PendingToolCall struct {
	ID      string
	Name    string
	Args    string
	Command string
	Risk    string
	Reason  string
}

type State struct {
	Config     config.Config
	WorkingDir string
	StartedAt  time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu              sync.Mutex
	messages        []Message
	providerErr     error
	pendingApproval *PendingToolCall
	sessionRules    []string
}

func New(cfg config.Config, workingDir string, now time.Time) *State {
	ctx, cancel := context.WithCancel(context.Background())
	return &State{
		Config:     cfg,
		WorkingDir: workingDir,
		StartedAt:  now,
		ctx:        ctx,
		cancel:     cancel,
	}
}

func (s *State) AddMessage(role Role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.messages = append(s.messages, Message{
		Role:      role,
		Content:   content,
		CreatedAt: time.Now(),
	})
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
