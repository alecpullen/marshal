package lsp

import (
	"log/slog"
	"sync"
)

// Handle is a swappable reference to the active Manager. Tool and index
// adapters hold a Handle so the manager can be restarted against a new
// root (when the session enters or leaves a worktree) without rewiring
// them.
//
// Handle has a single consumer at a time (the startRuntime restarter
// goroutine, which serializes on the workspace broker's Subscribe
// channel). Restart therefore builds newM outside the write lock for
// latency: two concurrent Restarts would each construct a manager, and
// the loser would discard its newM silently. If Handle ever fans out
// to multiple consumers, move the NewManager call inside the write lock.
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

// ServerFor returns the LSP client for lang from the current Manager,
// or false if the handle has no manager or no client is registered for
// the language. Equivalent to h.Get().ServerFor(lang) with nil-safety.
func (h *Handle) ServerFor(lang string) (*Client, bool) {
	m := h.Get()
	if m == nil {
		return nil, false
	}
	return m.ServerFor(lang)
}

// Root returns the manager's workspace root, or "" if there is no manager.
func (h *Handle) Root() string {
	m := h.Get()
	if m == nil {
		return ""
	}
	return m.Root()
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
