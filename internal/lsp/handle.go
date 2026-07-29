package lsp

import (
	"log/slog"
	"sync"
)

// Handle is a swappable reference to the active Manager. Tool and index
// adapters hold a Handle so the manager can be restarted against a new
// root (when the session enters or leaves a worktree) without rewiring
// them.
type Handle struct {
	mu      sync.RWMutex
	m       *Manager
	servers map[string]ServerSpec
	log     *slog.Logger
}

// NewHandle wraps m. servers and log are retained so Restart can build
// replacement managers with the same configuration.
func NewHandle(m *Manager, servers map[string]ServerSpec, log *slog.Logger) *Handle {
	return &Handle{m: m, servers: servers, log: log}
}

// Get returns the current Manager.
func (h *Handle) Get() *Manager {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.m
}

// Restart builds a new Manager rooted at root, swaps it in, and returns
// both managers. The caller shuts old down and runs newM.
func (h *Handle) Restart(root string) (newM, old *Manager) {
	newM = NewManager(root, h.servers, h.log)
	h.mu.Lock()
	old = h.m
	h.m = newM
	h.mu.Unlock()
	return newM, old
}
