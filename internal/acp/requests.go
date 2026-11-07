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

// requestInterval is how often a request forwarder compares the session's
// open requests with the last ones it sent (P3 R3).
const requestInterval = 50 * time.Millisecond

// RequestManagerConfig wires one connection's request handlers.
type RequestManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
	// Notify sends pending_requests updates.
	Notify NotifyFunc
	// Marshal reports whether the connection's client declared the marshal
	// capability. Nil means it did not.
	Marshal func() bool
}

// RequestManager serves session/pending, the answer calls and
// session/rollback for one connection, and runs one request forwarder per
// session a marshal client reads.
type RequestManager struct {
	lookup  func(sessionID string) (*service.Session, bool)
	notify  NotifyFunc
	marshal func() bool

	mu         sync.Mutex
	forwarders map[string]*requestForwarder
	closed     bool

	// fences holds this connection's live request forwarders, by session, so a
	// response barrier can confirm their updates are written (P3 R2).
	fences fenceSet
}

// pendingEnvelope is the session/update body a pending_requests update travels
// in. Like statusEnvelope it is a dedicated struct: SessionUpdateParams'
// Update is a map[string]any, which would not keep the update's typed fields.
type pendingEnvelope struct {
	SessionID string                `json:"sessionId"`
	Update    acpwire.PendingUpdate `json:"update"`
}

// requestForwarder tracks one session's request forwarder goroutine.
type requestForwarder struct {
	cancel context.CancelFunc
	fence  *fence
}

// NewRequestManager builds one connection's request handlers.
func NewRequestManager(cfg RequestManagerConfig) *RequestManager {
	return &RequestManager{
		lookup:     cfg.Lookup,
		notify:     cfg.Notify,
		marshal:    cfg.Marshal,
		forwarders: map[string]*requestForwarder{},
	}
}

// Pending handles session/pending: it returns the session's open requests and
// the access request the panel should present, and for a marshal client it
// first makes sure the session's forwarder is running, so the state arrives as
// an update before this call's response (P3 R2, R3).
func (m *RequestManager) Pending(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.PendingParams
	if err := decodeParams(params, &p, acpwire.MethodPending); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if m.isMarshal() {
		m.ensureForwarder(p.SessionID)
	}
	return pendingState(ctx, sess), nil
}

// AnswerApproval handles session/answer_approval.
func (m *RequestManager) AnswerApproval(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.AnswerApprovalParams
	if err := decodeParams(params, &p, acpwire.MethodAnswerApproval); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	// The service's error travels as it is, so its code reaches the client as
	// error data.
	if err := sess.AnswerApproval(ctx, p.Key, p.Answer); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// AnswerQuestion handles session/answer_question.
func (m *RequestManager) AnswerQuestion(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.AnswerQuestionParams
	if err := decodeParams(params, &p, acpwire.MethodAnswerQuestion); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.AnswerQuestion(ctx, p.Key, p.Answers); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// AnswerSkillGate handles session/answer_skill_gate.
func (m *RequestManager) AnswerSkillGate(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.AnswerSkillGateParams
	if err := decodeParams(params, &p, acpwire.MethodAnswerSkillGate); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.AnswerSkillGate(ctx, p.Key, p.Choice); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// AnswerSudo handles session/answer_sudo. Neither the password nor the params
// it arrived in ever reach an error or a log line (P3 R8).
func (m *RequestManager) AnswerSudo(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.AnswerSudoParams
	if err := decodeParams(params, &p, acpwire.MethodAnswerSudo); err != nil {
		// The decode failure names neither the password nor the frame it came
		// in: only the field that did not parse.
		p.Answer.Password = nil
		return nil, invalidParamsError("answer_sudo: malformed params")
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	// The service hands the decoded slice to the sudo handler, which zeroes it
	// after use; nothing here may touch it, and nothing here echoes it.
	if err := sess.AnswerSudo(ctx, p.Key, p.Answer); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// AnswerAccess handles session/answer_access.
func (m *RequestManager) AnswerAccess(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.AnswerAccessParams
	if err := decodeParams(params, &p, acpwire.MethodAnswerAccess); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	result, err := sess.AnswerAccess(ctx, p.Choice)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Rollback handles session/rollback.
func (m *RequestManager) Rollback(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.PendingParams
	if err := decodeParams(params, &p, acpwire.MethodRollback); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.Rollback(ctx); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// Close stops every forwarder. The connection's cleanup calls it.
func (m *RequestManager) Close() {
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

// fenceSession waits until the session's request forwarder has sent the open
// requests as they are now, if they differ from the last ones sent (P3 R2).
func (m *RequestManager) fenceSession(ctx context.Context, sessionID string) {
	m.fences.wait(ctx, sessionID)
}

// isMarshal reports whether the connection's client declared the marshal
// capability.
func (m *RequestManager) isMarshal() bool {
	return m.marshal != nil && m.marshal()
}

// service resolves a session's client-seam service. An empty id is invalid
// params and an unknown session is a server error.
func (m *RequestManager) service(sessionID string) (*service.Session, error) {
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
func (m *RequestManager) lookupService(sessionID string) (*service.Session, bool) {
	if m.lookup == nil {
		return nil, false
	}
	return m.lookup(sessionID)
}

// ensureForwarder starts one request forwarder for a session, unless the
// connection is closed or the session already has one. Its fence is registered
// before its goroutine starts, so a barrier that follows cannot miss it.
func (m *RequestManager) ensureForwarder(sessionID string) {
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
	f := &requestForwarder{cancel: cancel, fence: newFence()}
	m.forwarders[sessionID] = f
	m.fences.add(sessionID, f.fence)
	m.mu.Unlock()

	go m.runForwarder(sessionID, f, ctx)
}

// runForwarder keeps the session's open requests current: every
// requestInterval, and whenever its fence is asked, it reads the state and
// sends it when the encoding differs from the last one it sent. Requests open
// and settle without a broker event this manager sees, so polling is what keeps
// the mirror current. It stops when the connection closes or the session goes
// away.
func (m *RequestManager) runForwarder(sessionID string, f *requestForwarder, ctx context.Context) {
	defer func() {
		m.mu.Lock()
		delete(m.forwarders, sessionID)
		m.mu.Unlock()
		m.fences.remove(sessionID, f.fence)
		f.fence.close()
	}()

	// sent says a state has been sent on this connection, so last is the
	// encoding to compare against.
	var (
		last []byte
		sent bool
	)
	// send reads the state, encodes it and sends it when the encoding differs
	// from the last one sent. It reports whether the session still exists.
	send := func() bool {
		// Resolve the service on every pass, so a runtime replaced on load or
		// resume is followed.
		sess, ok := m.lookupService(sessionID)
		if !ok || sess == nil {
			return false
		}
		state := pendingState(ctx, sess)
		encoded, err := json.Marshal(state)
		if err != nil {
			return true
		}
		if sent && bytes.Equal(encoded, last) {
			return true
		}
		if err := m.notify(acpwire.MethodSessionUpdate, pendingEnvelope{
			SessionID: sessionID,
			Update: acpwire.PendingUpdate{
				Kind:    acpwire.UpdatePending,
				Pending: state,
			},
		}); err != nil {
			return true
		}
		last = encoded
		sent = true
		return true
	}

	ticker := time.NewTicker(requestInterval)
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

// pendingState is what one pass reads: it settles a typed access request that
// asks for nothing (ReconcileAccess), then reads the open requests.
func pendingState(ctx context.Context, svc *service.Session) acpwire.PendingState {
	access, ok := svc.ReconcileAccess(ctx)
	state := acpwire.PendingState{Requests: svc.Pending()}
	if ok {
		state.Access = &access
	}
	return state
}

// registerRequests registers the seven request methods on srv.
func registerRequests(srv *Server, requests *RequestManager) {
	srv.Handle(acpwire.MethodPending, requests.Pending)
	srv.Handle(acpwire.MethodAnswerApproval, requests.AnswerApproval)
	srv.Handle(acpwire.MethodAnswerQuestion, requests.AnswerQuestion)
	srv.Handle(acpwire.MethodAnswerSkillGate, requests.AnswerSkillGate)
	srv.Handle(acpwire.MethodAnswerSudo, requests.AnswerSudo)
	srv.Handle(acpwire.MethodAnswerAccess, requests.AnswerAccess)
	srv.Handle(acpwire.MethodRollback, requests.Rollback)
}
