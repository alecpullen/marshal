package session

import (
	"context"
	"sync"
	"time"

	"marshal/internal/app/config"
)

type Role string

const (
	RoleSystem Role = "system"
	RoleUser   Role = "user"
)

type Message struct {
	Role      Role
	Content   string
	CreatedAt time.Time
}

type State struct {
	Config     config.Config
	WorkingDir string
	StartedAt  time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	messages []Message
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
