package lsp

import (
	"context"
	"log/slog"
	"sync"
)

// Handle is a swappable reference to the active Manager. Tool and index
// adapters hold a Handle so the manager can be replaced (when the session
// enters or leaves a worktree, or its server set changes) without rewiring
// them.
//
// Set swaps in a manager someone else owns, such as one shared between
// sessions. Restart builds its own: it constructs newM outside the write
// lock for latency, so two concurrent Restarts would each construct a
// manager and the loser would discard its newM silently. If Restart ever
// gains concurrent callers, move the NewManager call inside the write lock.
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

// Set points the handle at m, which may be nil, and returns the manager it
// pointed at before. It neither runs m nor stops old: the caller owns both.
func (h *Handle) Set(m *Manager) (old *Manager) {
	h.mu.Lock()
	old = h.m
	h.m = m
	h.mu.Unlock()
	return old
}

// Restart builds a new Manager rooted at root, swaps it in, and returns
// both managers. The caller shuts old down and runs newM.
func (h *Handle) Restart(root string) (newM, old *Manager) {
	newM = NewManager(root, ServersForRoot(h.servers, root), h.log)
	h.mu.Lock()
	old = h.m
	h.m = newM
	h.mu.Unlock()
	return newM, old
}

// Sync adapts a Handle to native.LSPSync: it always talks to the current
// manager's Workspace, so a worktree restart needs no rewiring.
type Sync struct{ h *Handle }

func NewSync(h *Handle) *Sync { return &Sync{h: h} }

func (s *Sync) BeginEdit(paths []string) (func(ctx context.Context) string, bool) {
	if s == nil || s.h == nil || s.h.Get() == nil {
		return nil, false
	}
	return s.h.Get().Workspace().BeginEdit(paths)
}

func (s *Sync) Rescan() {
	if s == nil || s.h == nil || s.h.Get() == nil {
		return
	}
	s.h.Get().Workspace().Rescan()
}
