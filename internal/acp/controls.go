package acp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
)

// workInterval is how often a work forwarder compares the session's work with
// the last one it sent (P3 R3).
const workInterval = 50 * time.Millisecond

// ControlManagerConfig wires one connection's control handlers.
type ControlManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
	// Notify sends work_snapshot updates.
	Notify NotifyFunc
	// Marshal reports whether the connection's client declared the marshal
	// capability. Nil means it did not.
	Marshal func() bool
}

// ControlManager serves a session's turns, controls and work over its service
// for one connection, and runs one work forwarder per session a marshal client
// reads.
type ControlManager struct {
	lookup  func(sessionID string) (*service.Session, bool)
	notify  NotifyFunc
	marshal func() bool

	mu         sync.Mutex
	forwarders map[string]*controlForwarder
	closed     bool

	// fences holds this connection's live work forwarders, by session, so a
	// response barrier can confirm their updates are written (P3 R2).
	fences fenceSet
}

// workForwarder tracks one session's work forwarder goroutine.
type controlForwarder struct {
	cancel context.CancelFunc
	fence  *fence
}

// NewControlManager builds one connection's control handlers.
func NewControlManager(cfg ControlManagerConfig) *ControlManager {
	return &ControlManager{
		lookup:     cfg.Lookup,
		notify:     cfg.Notify,
		marshal:    cfg.Marshal,
		forwarders: map[string]*controlForwarder{},
	}
}

// Prompt handles session/prompt for a marshal client: it joins the prompt's
// text blocks and runs the turn on the session's service (P3 plan 6b decision
// 1). The service decides what an empty text means, and a watch resume has none,
// so nothing here checks it.
func (m *ControlManager) Prompt(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.PromptCallParams
	if err := decodeParams(params, &p, acpwire.MethodPrompt); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	texts := make([]string, 0, len(p.Prompt))
	for _, block := range p.Prompt {
		texts = append(texts, block.Text)
	}
	// The turn is cancellable: the client's context ending stops the run, as in
	// process (P3 plan 6b decision 7).
	runCtx, cancel := cancellable(ctx)
	defer cancel()
	return sess.Prompt(runCtx, acpwire.PromptParams{
		Text:        strings.Join(texts, "\n\n"),
		Queued:      p.Queued,
		WatchResume: p.WatchResume,
	})
}

// Stop handles session/stop: it stops the run in flight with the caller's
// cause. session/cancel stays a notification for other clients.
func (m *ControlManager) Stop(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.StopParams
	if err := decodeParams(params, &p, acpwire.MethodStop); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.Stop(ctx, p.Cause); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// Review handles session/review.
func (m *ControlManager) Review(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ReviewCallParams
	if err := decodeParams(params, &p, acpwire.MethodReview); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := cancellable(ctx)
	defer cancel()
	if err := sess.Review(runCtx, p.Review); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// AgentRun handles session/agent_run.
func (m *ControlManager) AgentRun(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.AgentRunCallParams
	if err := decodeParams(params, &p, acpwire.MethodAgentRun); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := cancellable(ctx)
	defer cancel()
	if err := sess.RunAgent(runCtx, p.Run); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// Suggest handles session/suggest.
func (m *ControlManager) Suggest(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.SuggestParams
	if err := decodeParams(params, &p, acpwire.MethodSuggest); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := cancellable(ctx)
	defer cancel()
	text, err := sess.Suggest(runCtx, p.LastMessage)
	if err != nil {
		return nil, err
	}
	return acpwire.SuggestResult{Text: text}, nil
}

// VerifyProposal handles session/verify_proposal.
func (m *ControlManager) VerifyProposal(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodVerifyProposal); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := cancellable(ctx)
	defer cancel()
	return sess.VerifyProposal(runCtx)
}

// Close stops every work forwarder. The connection's cleanup calls it.
func (m *ControlManager) Close() {
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

// fenceSession waits until the session's work forwarder has sent the work as
// it is now, if it differs from the last one sent (P3 R2).
func (m *ControlManager) fenceSession(ctx context.Context, sessionID string) {
	m.fences.wait(ctx, sessionID)
}

// isMarshal reports whether the connection's client declared the marshal
// capability.
func (m *ControlManager) isMarshal() bool {
	return m.marshal != nil && m.marshal()
}

// service resolves a session's client-seam service. An empty id is invalid
// params and an unknown session is a server error.
func (m *ControlManager) service(sessionID string) (*service.Session, error) {
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
func (m *ControlManager) lookupService(sessionID string) (*service.Session, bool) {
	if m.lookup == nil {
		return nil, false
	}
	return m.lookup(sessionID)
}

// registerControls registers this plan's control methods on srv, and the seven
// existing ones through marshalOr: a client without the capability reaches
// others[method], unchanged. A method missing from others refuses such a client
// as method-not-found.
func registerControls(srv *Server, controls *ControlManager, others map[string]Handler) {
	srv.Handle(acpwire.MethodStop, controls.Stop)
	srv.Handle(acpwire.MethodReview, controls.Review)
	srv.Handle(acpwire.MethodAgentRun, controls.AgentRun)
	srv.Handle(acpwire.MethodSuggest, controls.Suggest)
	srv.Handle(acpwire.MethodVerifyProposal, controls.VerifyProposal)
	srv.Handle(acpwire.MethodPrompt, marshalOr(srv.marshalClient, controls.Prompt, others[acpwire.MethodPrompt]))
}
