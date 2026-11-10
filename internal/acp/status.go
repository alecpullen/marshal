package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
)

// statusInterval is how often a forwarder compares the session's status
// with the last one it sent (P3 R3).
const statusInterval = 50 * time.Millisecond

// StatusManagerConfig wires one connection's status handlers.
type StatusManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
	// Notify sends session_status updates.
	Notify NotifyFunc
	// Marshal reports whether the connection's client declared the
	// marshal capability. Nil means it did not.
	Marshal func() bool
}

// StatusManager serves session/status and the notice calls for one
// connection, and runs one status forwarder per session a marshal client
// reads.
type StatusManager struct {
	lookup  func(sessionID string) (*service.Session, bool)
	notify  NotifyFunc
	marshal func() bool

	mu         sync.Mutex
	forwarders map[string]*statusForwarder
	closed     bool

	// fences holds this connection's live status forwarders, by session, so a
	// response barrier can confirm their updates are written (P3 R2).
	fences fenceSet
}

// statusEnvelope is the session/update body a session_status update travels
// in. It is a dedicated struct, not SessionUpdateParams, because the latter's
// Update is a map[string]any: a status carried through a map would not keep
// its typed fields.
type statusEnvelope struct {
	SessionID string               `json:"sessionId"`
	Update    acpwire.StatusUpdate `json:"update"`
}

// statusForwarder tracks one session's status forwarder goroutine.
type statusForwarder struct {
	cancel context.CancelFunc
	fence  *fence
}

// NewStatusManager builds one connection's status handlers.
func NewStatusManager(cfg StatusManagerConfig) *StatusManager {
	return &StatusManager{
		lookup:     cfg.Lookup,
		notify:     cfg.Notify,
		marshal:    cfg.Marshal,
		forwarders: map[string]*statusForwarder{},
	}
}

// Status handles session/status: it returns the session's status, and for a
// marshal client it first makes sure the session's forwarder is running, so
// the status arrives as an update before this call's response (P3 R2, R3).
func (m *StatusManager) Status(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.StatusParams
	if err := decodeParams(params, &p, acpwire.MethodStatus); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if m.isMarshal() {
		m.ensureForwarder(p.SessionID)
	}
	return sess.Status(), nil
}

// NoticeSet handles session/notice_set.
func (m *StatusManager) NoticeSet(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.NoticeSetParams
	if err := decodeParams(params, &p, acpwire.MethodNoticeSet); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	// The service's error travels as it is, so its code reaches the client as
	// error data.
	if err := sess.SetNotice(ctx, p.Notice); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

// NoticeClear handles session/notice_clear.
func (m *StatusManager) NoticeClear(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.NoticeClearParams
	if err := decodeParams(params, &p, acpwire.MethodNoticeClear); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.ClearNotice(ctx, p.Category); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

// NoticeDismiss handles session/notice_dismiss.
func (m *StatusManager) NoticeDismiss(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.StatusParams
	if err := decodeParams(params, &p, acpwire.MethodNoticeDismiss); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.DismissNotice(ctx); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

// Close stops every forwarder. The connection's cleanup calls it.
func (m *StatusManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	cancels := make([]context.CancelFunc, 0, len(m.forwarders))
	for id, f := range m.forwarders {
		cancels = append(cancels, f.cancel)
		delete(m.forwarders, id)
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// fenceSession waits until the session's status forwarder has sent the
// status as it is now, if it differs from the last one sent (P3 R2).
func (m *StatusManager) fenceSession(ctx context.Context, sessionID string) {
	m.fences.wait(ctx, sessionID)
}

// isMarshal reports whether the connection's client declared the marshal
// capability.
func (m *StatusManager) isMarshal() bool {
	return m.marshal != nil && m.marshal()
}

// service resolves a session's client-seam service. An empty id is invalid
// params and an unknown session is a server error.
func (m *StatusManager) service(sessionID string) (*service.Session, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, invalidParamsError("sessionId is required")
	}
	sess, ok := m.lookupService(sessionID)
	if !ok || sess == nil {
		return nil, serverErrorf("unknown session: %s", sessionID)
	}
	return sess, nil
}

// lookupService resolves a session's service without reporting why it failed.
func (m *StatusManager) lookupService(sessionID string) (*service.Session, bool) {
	if m.lookup == nil {
		return nil, false
	}
	return m.lookup(sessionID)
}

// ensureForwarder starts one status forwarder for a session, unless the
// connection is closed or the session already has one. Its fence is registered
// before its goroutine starts, so a barrier that follows cannot miss it.
func (m *StatusManager) ensureForwarder(sessionID string) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if _, ok := m.forwarders[sessionID]; ok {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &statusForwarder{cancel: cancel, fence: newFence()}
	m.forwarders[sessionID] = f
	m.fences.add(sessionID, f.fence)
	m.mu.Unlock()

	go m.runForwarder(sessionID, f, ctx)
}

// runForwarder keeps the session's status current: every statusInterval, and
// whenever its fence is asked, it reads the status and sends it when the
// encoding differs from the last one it sent. Most status fields change
// without a broker event, so polling is what keeps the mirror current. It
// stops when the connection closes or the session goes away.
func (m *StatusManager) runForwarder(sessionID string, f *statusForwarder, ctx context.Context) {
	defer func() {
		m.mu.Lock()
		delete(m.forwarders, sessionID)
		m.mu.Unlock()
		m.fences.remove(sessionID, f.fence)
		f.fence.close()
	}()

	// sent says a status has been sent on this connection, so last is the
	// encoding to compare against.
	var (
		last []byte
		sent bool
	)
	// send reads the status, encodes it and sends it when the encoding differs
	// from the last one sent. It reports whether the session still exists.
	send := func() bool {
		// Resolve the service on every pass, so a runtime replaced on load or
		// resume is followed.
		sess, ok := m.lookupService(sessionID)
		if !ok || sess == nil {
			return false
		}
		status := sess.Status()
		encoded, err := json.Marshal(status)
		if err != nil {
			return true
		}
		if sent && bytes.Equal(encoded, last) {
			return true
		}
		if err := m.notify("session/update", statusEnvelope{
			SessionID: sessionID,
			Update: acpwire.StatusUpdate{
				Kind:   acpwire.UpdateSessionStatus,
				Status: status,
			},
		}); err != nil {
			return true
		}
		last = encoded
		sent = true
		return true
	}

	ticker := time.NewTicker(statusInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		case answer := <-f.fence.requests():
			alive := send()
			close(answer)
			if !alive {
				return
			}
		}
	}
}

// registerStatus registers the four status methods on srv.
func registerStatus(srv *Server, statuses *StatusManager) {
	srv.Handle(acpwire.MethodStatus, statuses.Status)
	srv.Handle(acpwire.MethodNoticeSet, statuses.NoticeSet)
	srv.Handle(acpwire.MethodNoticeClear, statuses.NoticeClear)
	srv.Handle(acpwire.MethodNoticeDismiss, statuses.NoticeDismiss)
}

// registerInitialize registers the initialize handler: registerHandlers' own,
// moved out unchanged so a test can serve the real handshake.
func registerInitialize(srv *Server) {
	srv.Handle("initialize", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p InitializeParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, invalidParamsError("parse initialize params: %v", err)
			}
		}
		if p.ProtocolVersion == 0 {
			return nil, invalidParamsError("protocolVersion is required")
		}

		declared := p.ClientCapabilities != nil &&
			p.ClientCapabilities.Marshal != nil &&
			p.ClientCapabilities.Marshal.ProtocolVersion == acpwire.MarshalProtocolVersion
		// A viewer connection serves marshal clients only (P4 D2): a client
		// that did not declare the capability is refused here, and nothing is
		// recorded, so every later call is refused too.
		if srv.MarshalOnly && !declared {
			return nil, viewerCapabilityRefusal()
		}

		// Record whether this client declared the marshal-only protocol (P3
		// R9). The capability below is advertised to every client, so a client
		// that does not declare it still sees it.
		srv.setMarshalClient(declared)

		return map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"marshal": map[string]any{
					"protocolVersion": acpwire.MarshalProtocolVersion,
				},
				"loadSession": true,
				// Not per session: config/* act on the user-global config.
				"configAccess": map[string]any{},
				// workspace/parse, /patch and /format (no session needed).
				"workspaceFiles": map[string]any{},
				"sessionCapabilities": map[string]any{
					"close":                 map[string]any{},
					"list":                  map[string]any{},
					"resume":                map[string]any{},
					"additionalDirectories": map[string]any{},
					"delete":                map[string]any{},
					"commandDispatch":       map[string]any{},
					"swarmDispatch":         map[string]any{},
					"sddDispatch":           map[string]any{},
					"sessionTelemetry":      map[string]any{},
					"stackView":             map[string]any{},
					"stackNode":             map[string]any{},
					"subagentStacks":        map[string]any{},
					"lastRequest":           map[string]any{},
					"filesView":             map[string]any{},
					"commitDraft":           map[string]any{},
					"stepDiffs":             map[string]any{},
					"runDetail":             map[string]any{},
					"watchAccess":           map[string]any{},
					"memoryAccess":          map[string]any{},
					"holdControl":           map[string]any{},
					"memoryScopes":          map[string]any{},
					"agentsRoster":          map[string]any{},
					"skillsAccess":          map[string]any{},
					"pluginsAccess":         map[string]any{},
					// session/new isolation plus session/diff, /merge,
					// /discard and /worktree_prune.
					"worktreeIsolation": map[string]any{},
					// session/commit for non-isolated sessions.
					"exitPath": map[string]any{},
				},
			},
			"agentInfo": map[string]any{
				"name":    "marshal",
				"title":   "Marshal",
				"version": AgentVersion,
			},
			"authMethods": []any{},
		}, nil
	})
}
