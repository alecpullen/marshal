package rpc

import (
	"context"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// NewSession creates a session in the agent that replaces the current one,
// attaches it, and makes it current. A failure leaves the current session as
// it was.
func (h *Host) NewSession(ctx context.Context, name string) (acpclient.Session, error) {
	session, err := h.replaceSession(ctx, name)
	if err != nil {
		return nil, err
	}
	return session, nil
}

// ListSessions lists the stored sessions the resume picker offers. The agent
// serves the call from the current session's project, so it names that session
// (decision 1): before any attach there is none, and the call fails with
// errNoSession instead of being sent.
func (h *Host) ListSessions(ctx context.Context, cwd string, limit int) ([]acpwire.SessionEntry, error) {
	current, err := h.currentID()
	if err != nil {
		return nil, err
	}
	params := acpwire.ListSessionsParams{SessionID: current, Cwd: cwd, Limit: limit}
	var result acpwire.ListSessionsResult
	if err := h.conn.Call(ctx, acpwire.MethodSessionList, params, &result); err != nil {
		return nil, err
	}
	return result.Sessions, nil
}

// replaceSession sends session/new with the current session as replaces,
// attaches the new session, and retires the old one (decision 6).
func (h *Host) replaceSession(ctx context.Context, name string) (*Session, error) {
	current, err := h.currentID()
	if err != nil {
		return nil, err
	}

	params := acpwire.NewSessionParams{Replaces: current, Name: name}
	var result acpwire.NewSessionResult
	if err := h.conn.Call(ctx, acpwire.MethodSessionNew, params, &result); err != nil {
		// Nothing was replaced: the current session and its mirror stand (R7).
		return nil, err
	}

	// The agent has already replaced the session, so a failed attach leaves the
	// old mirror answering an unknown session; the TUI reports the failure the
	// way it reports a failed replacement.
	session, err := h.Attach(ctx, result.SessionID)
	if err != nil {
		return nil, err
	}
	// Attach has already made the new session current.
	if old := h.session(current); old != nil && old != session {
		h.retire(old)
		h.stacks.forget(current)
	}
	return session, nil
}

// retire unregisters s and closes its Changed, once; its reads keep the
// mirror's final values.
func (h *Host) retire(s *Session) {
	h.mu.Lock()
	if h.sessions[s.id] == s {
		delete(h.sessions, s.id)
	}
	h.mu.Unlock()

	s.mu.Lock()
	s.closeLocked()
	s.mu.Unlock()
}

var (
	_ acpclient.Host    = (*Host)(nil)
	_ acpclient.Session = (*Session)(nil)
)
