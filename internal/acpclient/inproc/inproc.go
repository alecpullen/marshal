// Package inproc is the in-process adapter for the client seam (spec C4).
//
// It implements acpclient.Host and acpclient.Session over the runtime the TUI
// already holds, so the TUI can reach the agent side through the interfaces
// before the pipe exists. P3 deletes this package once the TUI talks to a
// separate process.
//
// It must not import internal/app: TUI tests import this package, and app
// imports tui, so an import back into app would be a cycle.
package inproc

import (
	"sync"
	"sync/atomic"

	"marshal/internal/acpclient"
	"marshal/internal/app/session"
)

// Deps is what the adapter serves. Each P2 plan adds its group's
// dependencies (C4).
type Deps struct {
	State *session.State
}

// Host is the in-process transport: it owns the current session and hands out
// a new one on a swap.
type Host struct {
	mu      sync.Mutex
	current *Session
}

// Session is one conversation over a session.State.
type Session struct {
	state  *session.State
	closed atomic.Bool
}

var (
	_ acpclient.Host    = (*Host)(nil)
	_ acpclient.Session = (*Session)(nil)
)

// New builds the host and the session over d.State.
func New(d Deps) (*Host, *Session) {
	sess := &Session{state: d.State}
	return &Host{current: sess}, sess
}

// Swap closes the current session and returns a new one over state. It serves
// today's swap path; plan 5 replaces it with Host.NewSession and Host.Resume.
func (h *Host) Swap(state *session.State) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current != nil {
		h.current.closed.Store(true)
	}
	sess := &Session{state: state}
	h.current = sess
	return sess
}

// SharedFilesystem reports whether a path the agent names is also a path on
// this machine. The in-process adapter runs in the same process, so it does.
func (h *Host) SharedFilesystem() bool { return true }

// ID is the session's ID. A closed session keeps answering with its final
// value (C4), which holds because its state never changes.
func (s *Session) ID() string { return s.state.SessionID() }
