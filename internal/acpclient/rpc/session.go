package rpc

import (
	"context"
	"encoding/json"
	"sync"

	"marshal/internal/acpwire"
)

// Session is one agent session seen through the connection: a mirror of what
// the agent pushes, and calls that send their ACP methods. Each P3 group plan
// adds its methods.
type Session struct {
	id   string
	conn *Conn

	mu     sync.Mutex
	status acpwire.SessionStatus
	// updated says an update arrived, so Attach's result, which the agent
	// computed before sending it, is stale.
	updated bool
	// pending is the request mirror (requests.go): nil until its first read
	// creates it.
	pending *pendingMirror
	// closed says the connection ended, so changed is closed and nothing
	// may be sent on it.
	closed  bool
	changed chan struct{}

	// ready is closed once Attach's call has settled, and err is its outcome,
	// so a caller that arrives while the call is in flight waits for it
	// instead of returning an empty mirror.
	ready chan struct{}
	err   error
}

// Attach binds a session the agent already holds. It sends session/status,
// which subscribes this connection to the session's status, and returns once
// the mirror holds a value. Attaching a session that is already attached
// returns the same Session and sends nothing; a caller that arrives while the
// first call is still in flight waits for it and gets its outcome, so it never
// sees a mirror the agent has not filled yet, nor a session the agent refused.
func (h *Host) Attach(ctx context.Context, sessionID string) (*Session, error) {
	h.mu.Lock()
	if session, ok := h.sessions[sessionID]; ok {
		h.mu.Unlock()
		return session.await(ctx)
	}
	// The session is registered before the call goes out, so an update that
	// arrives while the call waits is applied instead of dropped.
	session := &Session{
		id:      sessionID,
		conn:    h.conn,
		changed: make(chan struct{}, 1),
		ready:   make(chan struct{}),
	}
	h.sessions[sessionID] = session
	h.mu.Unlock()

	var status acpwire.SessionStatus
	params := acpwire.StatusParams{SessionID: sessionID}
	if err := h.conn.Call(ctx, acpwire.MethodStatus, params, &status); err != nil {
		// The entry goes even when an update arrived first: a later Attach
		// retries and re-subscribes, where keeping the entry would answer
		// every later caller with this error for good.
		h.mu.Lock()
		if h.sessions[sessionID] == session {
			delete(h.sessions, sessionID)
		}
		h.mu.Unlock()
		session.settle(err)
		return nil, err
	}

	session.install(status)
	session.settle(nil)
	return session, nil
}

// await waits for the Attach that registered the session to settle and
// returns its outcome, so a second caller shares the first one's value and
// error rather than reading a mirror no call has filled.
func (s *Session) await(ctx context.Context) (*Session, error) {
	select {
	case <-s.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s, nil
}

// settle records Attach's outcome and releases every caller waiting on it.
func (s *Session) settle(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	close(s.ready)
}

// ID is the agent's session ID.
func (s *Session) ID() string {
	return s.id
}

// Status is the mirrored status: no I/O, and never a read the agent has to
// answer. Its slices are shared with the mirror, so a caller must not change
// them.
func (s *Session) Status() acpwire.SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Changed signals, with a buffer of one, after the mirror's status changes. It
// is closed when the connection ends.
func (s *Session) Changed() <-chan struct{} {
	return s.changed
}

// SetNotice sets the session's banner.
func (s *Session) SetNotice(ctx context.Context, notice acpwire.Notice) error {
	params := acpwire.NoticeSetParams{SessionID: s.id, Notice: notice}
	return s.conn.Call(ctx, acpwire.MethodNoticeSet, params, nil)
}

// ClearNotice clears the session's notice of category.
func (s *Session) ClearNotice(ctx context.Context, category string) error {
	params := acpwire.NoticeClearParams{SessionID: s.id, Category: category}
	return s.conn.Call(ctx, acpwire.MethodNoticeClear, params, nil)
}

// DismissNotice dismisses the session's notice whatever its category.
func (s *Session) DismissNotice(ctx context.Context) error {
	params := acpwire.StatusParams{SessionID: s.id}
	return s.conn.Call(ctx, acpwire.MethodNoticeDismiss, params, nil)
}

// apply replaces the mirrored status with an update's and fires Changed. It
// runs on the connection's read goroutine and never blocks on it.
func (s *Session) apply(status acpwire.SessionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
	s.updated = true
	s.signalLocked()
}

// install sets the mirrored status from session/status's result, unless an
// update already arrived: that update is newer than the result the agent
// computed before sending it (its barrier timed out).
func (s *Session) install(status acpwire.SessionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updated {
		return
	}
	s.status = status
}

// signalLocked taps Changed without blocking. The caller holds s.mu, so a
// signal can never race the close.
func (s *Session) signalLocked() {
	if s.closed {
		return
	}
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// closeLocked closes Changed, once. The caller holds s.mu.
func (s *Session) closeLocked() {
	if s.closed {
		return
	}
	s.closed = true
	close(s.changed)
}

// routeNotify applies a session_status or pending_requests update to the
// attached session it names, and drops one for a session that is not attached.
// Any other kind is ignored. It runs on the read goroutine, before the
// caller's Notify sees the frame.
func (h *Host) routeNotify(method string, params json.RawMessage) {
	if method != acpwire.MethodSessionUpdate {
		return
	}
	var update acpwire.SessionUpdate
	if err := json.Unmarshal(params, &update); err != nil {
		return
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(update.Update, &kind); err != nil {
		return
	}

	h.mu.Lock()
	session := h.sessions[update.SessionID]
	h.mu.Unlock()
	if session == nil {
		return
	}

	switch kind.Kind {
	case acpwire.UpdateSessionStatus:
		var status acpwire.StatusUpdate
		if err := json.Unmarshal(update.Update, &status); err != nil {
			return
		}
		session.apply(status.Status)
	case acpwire.UpdatePending:
		var pending acpwire.PendingUpdate
		if err := json.Unmarshal(update.Update, &pending); err != nil {
			return
		}
		session.applyPending(pending.Pending)
	}
}

// closeSessions closes every attached session's Changed, once the connection
// has ended, and unregisters them: on a dead connection every id then fails
// its call, rather than a known one returning a closed session.
func (h *Host) closeSessions() {
	h.mu.Lock()
	sessions := make([]*Session, 0, len(h.sessions))
	for id, session := range h.sessions {
		sessions = append(sessions, session)
		delete(h.sessions, id)
	}
	h.mu.Unlock()

	for _, session := range sessions {
		session.mu.Lock()
		session.closeLocked()
		session.mu.Unlock()
	}
}
