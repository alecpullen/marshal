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

// endInterval is how often an end forwarder compares what an end would stop
// with the last one it sent (P3 R3).
const endInterval = 50 * time.Millisecond

// EndManagerConfig wires one connection's end-flow and session handlers.
type EndManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
	// HostLookup resolves the service host built with a session's service.
	HostLookup func(sessionID string) (*service.Host, bool)
	// Notify sends end_work updates.
	Notify NotifyFunc
	// Marshal reports whether the connection's client declared the marshal
	// capability. Nil means it did not.
	Marshal func() bool
}

// EndManager serves a session's end flow, its close and the host's session
// list over the session's service for one connection, and runs one end
// forwarder per session a marshal client reads.
type EndManager struct {
	lookup     func(sessionID string) (*service.Session, bool)
	hostLookup func(sessionID string) (*service.Host, bool)
	notify     NotifyFunc
	marshal    func() bool

	mu         sync.Mutex
	forwarders map[string]*endForwarder
	closed     bool

	// fences holds this connection's live end forwarders, by session, so a
	// response barrier can confirm their updates are written (P3 R2).
	fences fenceSet
}

// endUpdateEnvelope is the session/update body an end_work update travels in.
// It is a dedicated struct, as the status and work envelopes are:
// SessionUpdateParams' Update is a map[string]any, which would lose the
// update's typed fields.
type endUpdateEnvelope struct {
	SessionID string                `json:"sessionId"`
	Update    acpwire.EndWorkUpdate `json:"update"`
}

// endForwarder tracks one session's end forwarder goroutine. done closes when
// the goroutine returns, so Close can wait for it as the work manager does.
type endForwarder struct {
	cancel context.CancelFunc
	fence  *fence
	done   chan struct{}
}

// NewEndManager builds one connection's end-flow handlers.
func NewEndManager(cfg EndManagerConfig) *EndManager {
	return &EndManager{
		lookup:     cfg.Lookup,
		hostLookup: cfg.HostLookup,
		notify:     cfg.Notify,
		marshal:    cfg.Marshal,
		forwarders: map[string]*endForwarder{},
	}
}

// EndWork handles session/end_work: it returns the work an end of the session
// would stop, and for a marshal client it first makes sure the session's end
// forwarder is running, so the membership arrives as an update before this
// call's response (P3 R2, R3).
func (m *EndManager) EndWork(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodEndWork); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if m.isMarshal() {
		m.ensureForwarder(p.SessionID)
	}
	return sess.ActiveWork(), nil
}

// EndAccept handles session/end_accept: it checks the user's choice against the
// live membership and freezes the session's admission in one step on the
// session's service.
func (m *EndManager) EndAccept(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.EndAcceptCallParams
	if err := decodeParams(params, &p, acpwire.MethodEndAccept); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	return sess.AcceptEnd(ctx, p.Accept)
}

// EndWait handles session/end_wait: it stops and joins the accepted end's work.
// It is a long call, so it takes the cancellable context (decision 3).
func (m *EndManager) EndWait(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodEndWait); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := cancellable(ctx)
	defer cancel()
	if err := sess.WaitForEnd(runCtx); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// ExitAnalysis handles session/exit_analysis: it runs the exit postmortem's
// agent pass with the goal the client named. It is a long call, so it takes the
// cancellable context (decision 3).
func (m *EndManager) ExitAnalysis(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ExitAnalysisParams
	if err := decodeParams(params, &p, acpwire.MethodExitAnalysis); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := cancellable(ctx)
	defer cancel()
	if err := sess.RunExitAnalysis(runCtx, p.Goal); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// Postmortem handles session/postmortem: it writes the session's report and
// answers the path it wrote.
func (m *EndManager) Postmortem(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodPostmortem); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	path, err := sess.Postmortem(ctx)
	if err != nil {
		return nil, err
	}
	return acpwire.PostmortemResult{Path: path}, nil
}

// CloseSession handles session/close for a marshal client: it ends the
// conversation, not the runtime, so the runtime stays registered for the
// embedding or the host's CloseAll (decision 4).
func (m *EndManager) CloseSession(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodSessionClose); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.Close(ctx); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// ListSessions handles session/list for a marshal client: the session it names
// resolves the service host whose database answers the query.
func (m *EndManager) ListSessions(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ListSessionsParams
	if err := decodeParams(params, &p, acpwire.MethodSessionList); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	host, err := m.host(p.SessionID)
	if err != nil {
		return nil, err
	}
	sessions, err := host.ListSessions(ctx, p.Cwd, p.Limit)
	if err != nil {
		return nil, err
	}
	return acpwire.ListSessionsResult{Sessions: sessions}, nil
}

// Close stops every end forwarder and waits for each one to return, so the
// connection's cleanup can stop writing as the work manager does. The
// connection's cleanup calls it.
func (m *EndManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	forwards := make([]*endForwarder, 0, len(m.forwarders))
	for id, f := range m.forwarders {
		forwards = append(forwards, f)
		delete(m.forwarders, id)
	}
	m.mu.Unlock()
	for _, f := range forwards {
		f.cancel()
	}
	for _, f := range forwards {
		<-f.done
	}
}

// fenceSession waits until the session's end forwarder has sent the membership
// as it is now, if it differs from the last one sent (P3 R2).
func (m *EndManager) fenceSession(ctx context.Context, sessionID string) {
	m.fences.wait(ctx, sessionID)
}

// isMarshal reports whether the connection's client declared the marshal
// capability.
func (m *EndManager) isMarshal() bool {
	return m.marshal != nil && m.marshal()
}

// service resolves a session's client-seam service. An empty id is invalid
// params and an unknown session is a server error.
func (m *EndManager) service(sessionID string) (*service.Session, error) {
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
func (m *EndManager) lookupService(sessionID string) (*service.Session, bool) {
	if m.lookup == nil {
		return nil, false
	}
	return m.lookup(sessionID)
}

// host resolves the service host of the session's runtime. An empty id is
// invalid params and an unknown session is a server error.
func (m *EndManager) host(sessionID string) (*service.Host, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, invalidParamsError("sessionId is required")
	}
	host, ok := m.lookupHost(sessionID)
	if !ok || host == nil {
		return nil, serverErrorf("unknown session: %s", sessionID)
	}
	return host, nil
}

// lookupHost resolves a session's service host without reporting why it failed.
func (m *EndManager) lookupHost(sessionID string) (*service.Host, bool) {
	if m.hostLookup == nil {
		return nil, false
	}
	return m.hostLookup(sessionID)
}

// ensureForwarder starts one end forwarder for a session, unless the connection
// is closed or the session already has one. Its fence is registered before its
// goroutine starts, so a barrier that follows cannot miss it.
func (m *EndManager) ensureForwarder(sessionID string) {
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
	f := &endForwarder{cancel: cancel, fence: newFence(), done: make(chan struct{})}
	m.forwarders[sessionID] = f
	m.fences.add(sessionID, f.fence)
	m.mu.Unlock()

	go m.runForwarder(sessionID, f, ctx)
}

// runForwarder keeps the session's end mirror current: every endInterval, and
// whenever its fence is asked, it reads what an end would stop and sends it
// when the encoding differs from the last one it sent. The membership changes
// without a broker event this manager sees, so polling is what keeps the mirror
// current. It stops when the connection closes or the session goes away.
func (m *EndManager) runForwarder(sessionID string, f *endForwarder, ctx context.Context) {
	defer func() {
		f.cancel()
		m.mu.Lock()
		delete(m.forwarders, sessionID)
		m.mu.Unlock()
		m.fences.remove(sessionID, f.fence)
		f.fence.close()
		// done closes last: Close waits on it, so everything above has run when
		// the connection's cleanup goes on to stop writing.
		close(f.done)
	}()

	// sent says a membership has been sent on this connection, so last is the
	// encoding to compare against. A fresh forwarder sends the membership once
	// even when it matches end_work's response: that first update is what
	// installs the client's mirror, and a barrier-fenced end_work depends on it.
	var (
		last []byte
		sent bool
	)
	// send reads the membership, encodes it and sends it when the encoding
	// differs from the last one sent. It reports whether the session still
	// exists.
	send := func() bool {
		// Resolve the service on every pass, so a runtime replaced on load or
		// resume is followed.
		sess, ok := m.lookupService(sessionID)
		if !ok || sess == nil {
			return false
		}
		work := sess.ActiveWork()
		encoded, err := json.Marshal(work)
		if err != nil {
			return true
		}
		if sent && bytes.Equal(encoded, last) {
			return true
		}
		// Nothing is sent under the manager's mutex.
		if m.notify == nil {
			return true
		}
		if err := m.notify(acpwire.MethodSessionUpdate, endUpdateEnvelope{
			SessionID: sessionID,
			Update: acpwire.EndWorkUpdate{
				Kind: acpwire.UpdateEndWork,
				Work: work,
			},
		}); err != nil {
			return true
		}
		last = encoded
		sent = true
		return true
	}

	ticker := time.NewTicker(endInterval)
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

// registerEnds registers the five new end-flow methods on srv, and
// session/close and session/list through marshalOr: a client without the
// capability reaches others[method], unchanged. A method missing from others
// refuses such a client as method-not-found.
func registerEnds(srv *Server, ends *EndManager, others map[string]Handler) {
	srv.Handle(acpwire.MethodEndWork, ends.EndWork)
	srv.Handle(acpwire.MethodEndAccept, ends.EndAccept)
	srv.Handle(acpwire.MethodEndWait, ends.EndWait)
	srv.Handle(acpwire.MethodExitAnalysis, ends.ExitAnalysis)
	srv.Handle(acpwire.MethodPostmortem, ends.Postmortem)
	srv.Handle(acpwire.MethodSessionClose, marshalOr(srv.marshalClient, ends.CloseSession, others[acpwire.MethodSessionClose]))
	srv.Handle(acpwire.MethodSessionList, marshalOr(srv.marshalClient, ends.ListSessions, others[acpwire.MethodSessionList]))
}
