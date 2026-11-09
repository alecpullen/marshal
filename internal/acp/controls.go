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

// workForwarder tracks one session's work forwarder goroutine. done closes when
// the goroutine returns, so Close can wait for it as the work manager does.
type controlForwarder struct {
	cancel context.CancelFunc
	fence  *fence
	done   chan struct{}
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
	// The turn is cancellable: the client's context ending stops this call's
	// wait, and the run goes on until session/stop (P4 D4).
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
	// The call's context ending stops the wait, and the run goes on until
	// session/stop (P4 D4).
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
	// The call's context ending stops the wait, and the run goes on until
	// session/stop (P4 D4).
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

// Steer handles session/steer: a delivery of "now" submits a Send now request
// and answers its receipt, and an empty or "queued" delivery queues the text,
// which the service accepts with no turn in flight.
func (m *ControlManager) Steer(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.SteerCallParams
	if err := decodeParams(params, &p, acpwire.MethodSteer); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	switch p.Delivery {
	case acpwire.SteerDeliveryNow:
		return sess.SendNow(ctx, acpwire.SendNowRequest{
			SessionID: p.SessionID,
			TurnID:    p.TurnID,
			RequestID: p.RequestID,
			Text:      p.Text,
		})
	case acpwire.SteerDeliveryQueued, "queued":
		if err := sess.Steer(ctx, p.Text); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	default:
		return nil, invalidParamsError("session/steer: unknown delivery %q: want queued or now", p.Delivery)
	}
}

// SteeringPop handles session/steering_pop.
func (m *ControlManager) SteeringPop(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodSteeringPop); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	text, ok := sess.PopSteering(ctx)
	return acpwire.SteeringPopResult{Text: text, OK: ok}, nil
}

// SteeringClear handles session/steering_clear.
func (m *ControlManager) SteeringClear(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodSteeringClear); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.ClearSteering(ctx); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// SetMode handles session/set_mode: it decodes ACP's own params, whose patch
// fields are pointers so a revocation stays distinguishable from an omission.
func (m *ControlManager) SetMode(ctx context.Context, params json.RawMessage) (any, error) {
	var p SetModeParams
	if err := decodeParams(params, &p, acpwire.MethodSetMode); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	return sess.SetInteraction(ctx, acpwire.InteractionPatch{
		Mode:   p.Mode,
		Auto:   p.Auto,
		System: p.System,
	})
}

// SetEffort handles session/set_effort.
func (m *ControlManager) SetEffort(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.EffortParams
	if err := decodeParams(params, &p, acpwire.MethodSetEffort); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	applies, err := sess.SetEffort(ctx, p.Effort)
	if err != nil {
		return nil, err
	}
	return acpwire.EffortResult{Effort: p.Effort, AppliesFrom: applies}, nil
}

// SetSudoUnlock handles session/set_sudo_unlock.
func (m *ControlManager) SetSudoUnlock(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.SudoUnlockParams
	if err := decodeParams(params, &p, acpwire.MethodSetSudoUnlock); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.SetSudoUnlock(ctx, p.On); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// SubagentCancel handles session/subagent_cancel.
func (m *ControlManager) SubagentCancel(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.SubagentCancelParams
	if err := decodeParams(params, &p, acpwire.MethodSubagentCancel); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	return acpwire.SubagentCancelResult{Cancelled: sess.CancelSubagent(ctx, p.SubagentID)}, nil
}

// SDDClear handles session/sdd_clear: the service calls it ClearFinishedRun.
func (m *ControlManager) SDDClear(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodSDDClear); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.ClearFinishedRun(ctx); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// WatchResumeTake handles session/watch_resume_take.
func (m *ControlManager) WatchResumeTake(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodWatchResumeTake); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	name, repeat, ok := sess.TakeWatchResume(ctx)
	return acpwire.WatchResumeTakeResult{Name: name, Repeat: repeat, OK: ok}, nil
}

// WorkList handles session/work_list: the live view answers the service's work
// and, for a marshal client, starts the forwarder that keeps it current (P3
// R3). The attention and history views are pages, and the service codes a stale
// cursor, so its error travels as it is. A page argument on the live view, or a
// view this does not know, is invalid params, as the work manager answers.
func (m *ControlManager) WorkList(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.WorkListParams
	if err := decodeParams(params, &p, acpwire.MethodWorkList); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	switch p.View {
	case "", "live":
		if p.Query != "" || p.Cursor != "" || p.Limit != 0 {
			return nil, invalidParamsError("session/work_list: the live view does not accept query, cursor or limit")
		}
		snap, exists := sess.Work()
		state := acpwire.WorkState{Exists: exists, Snapshot: snap}
		if m.isMarshal() {
			m.ensureForwarder(p.SessionID)
		}
		return state, nil
	case "attention", "history":
		return sess.ListWork(ctx, acpwire.WorkQuery{
			View:   p.View,
			Query:  p.Query,
			Cursor: p.Cursor,
			Limit:  p.Limit,
		})
	default:
		return nil, invalidParamsError("session/work_list: unknown view %q", p.View)
	}
}

// WorkInspect handles session/work_inspect.
func (m *ControlManager) WorkInspect(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.WorkTargetParams
	if err := decodeParams(params, &p, acpwire.MethodWorkInspect); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	return sess.InspectWork(ctx, p.TaskKey, p.Raw)
}

// WorkStop handles session/work_stop.
func (m *ControlManager) WorkStop(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.WorkTargetParams
	if err := decodeParams(params, &p, acpwire.MethodWorkStop); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	return sess.StopWork(ctx, p.TaskKey, p.Reason)
}

// workUpdateEnvelope is the session/update body a work_snapshot update travels
// in. It is a dedicated struct, as the status and pending envelopes are:
// SessionUpdateParams' Update is a map[string]any, which would lose the
// snapshot's typed fields.
type workUpdateEnvelope struct {
	SessionID string             `json:"sessionId"`
	Update    acpwire.WorkUpdate `json:"update"`
}

// ensureForwarder starts one work forwarder for a session, unless the
// connection is closed or the session already has one. Its fence is registered
// before its goroutine starts, so a barrier that follows cannot miss it.
func (m *ControlManager) ensureForwarder(sessionID string) {
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
	f := &controlForwarder{cancel: cancel, fence: newFence(), done: make(chan struct{})}
	m.forwarders[sessionID] = f
	m.fences.add(sessionID, f.fence)
	m.mu.Unlock()

	go m.runForwarder(sessionID, f, ctx)
}

// runForwarder keeps the session's work mirror current: every workInterval, and
// whenever its fence is asked, it reads the service's work and sends it when the
// encoding differs from the last one it sent. The work changes without a broker
// event this manager sees, so polling is what keeps the mirror current. It stops
// when the connection closes or the session goes away.
func (m *ControlManager) runForwarder(sessionID string, f *controlForwarder, ctx context.Context) {
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

	// sent says a state has been sent on this connection, so last is the
	// encoding to compare against. A fresh forwarder sends the state once even
	// when it matches work_list's response: that first update is what installs
	// the client's mirror, and a barrier-fenced work_list depends on it.
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
		snap, exists := sess.Work()
		state := acpwire.WorkState{Exists: exists, Snapshot: snap}
		encoded, err := json.Marshal(state)
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
		if err := m.notify(acpwire.MethodSessionUpdate, workUpdateEnvelope{
			SessionID: sessionID,
			Update: acpwire.WorkUpdate{
				Kind: acpwire.UpdateWork,
				Work: state,
			},
		}); err != nil {
			return true
		}
		last = encoded
		sent = true
		return true
	}

	ticker := time.NewTicker(workInterval)
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

// Close stops every work forwarder and waits for each one to return, so the
// connection's cleanup can stop writing as the work manager does. The
// connection's cleanup calls it.
func (m *ControlManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	forwards := make([]*controlForwarder, 0, len(m.forwarders))
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

// registerControls registers this plan's control methods on srv, and the
// existing ones through marshalOr: a client without the capability reaches
// others[method], unchanged. A method missing from others refuses such a client
// as method-not-found.
//
// The fourteen methods registered unconditionally are new surface: no client had
// them at the plan's base, so there is no earlier behaviour to preserve and no
// reason to gate them. Only a method that already had a handler for another
// client goes through marshalOr: the ten below, ACP's turn, work and pipeline
// handlers, which a client without the capability keeps.
func registerControls(srv *Server, controls *ControlManager, others map[string]Handler) {
	srv.Handle(acpwire.MethodStop, controls.Stop)
	srv.Handle(acpwire.MethodReview, controls.Review)
	srv.Handle(acpwire.MethodAgentRun, controls.AgentRun)
	srv.Handle(acpwire.MethodSuggest, controls.Suggest)
	srv.Handle(acpwire.MethodVerifyProposal, controls.VerifyProposal)
	srv.Handle(acpwire.MethodSteeringPop, controls.SteeringPop)
	srv.Handle(acpwire.MethodSteeringClear, controls.SteeringClear)
	srv.Handle(acpwire.MethodSetSudoUnlock, controls.SetSudoUnlock)
	srv.Handle(acpwire.MethodSubagentCancel, controls.SubagentCancel)
	srv.Handle(acpwire.MethodSDDClear, controls.SDDClear)
	srv.Handle(acpwire.MethodWatchResumeTake, controls.WatchResumeTake)
	srv.Handle(acpwire.MethodSDDPreflight, controls.SDDPreflight)
	srv.Handle(acpwire.MethodSDDStop, controls.SDDStop)
	srv.Handle(acpwire.MethodSDDAuthor, controls.SDDAuthor)
	srv.Handle(acpwire.MethodPrompt, marshalOr(srv.marshalClient, controls.Prompt, others[acpwire.MethodPrompt]))
	srv.Handle(acpwire.MethodSteer, marshalOr(srv.marshalClient, controls.Steer, others[acpwire.MethodSteer]))
	srv.Handle(acpwire.MethodSetMode, marshalOr(srv.marshalClient, controls.SetMode, others[acpwire.MethodSetMode]))
	srv.Handle(acpwire.MethodSetEffort, marshalOr(srv.marshalClient, controls.SetEffort, others[acpwire.MethodSetEffort]))
	srv.Handle(acpwire.MethodWorkList, marshalOr(srv.marshalClient, controls.WorkList, others[acpwire.MethodWorkList]))
	srv.Handle(acpwire.MethodWorkInspect, marshalOr(srv.marshalClient, controls.WorkInspect, others[acpwire.MethodWorkInspect]))
	srv.Handle(acpwire.MethodWorkStop, marshalOr(srv.marshalClient, controls.WorkStop, others[acpwire.MethodWorkStop]))
	srv.Handle(acpwire.MethodSwarmStart, marshalOr(srv.marshalClient, controls.SwarmStart, others[acpwire.MethodSwarmStart]))
	srv.Handle(acpwire.MethodSDDStart, marshalOr(srv.marshalClient, controls.SDDStart, others[acpwire.MethodSDDStart]))
	srv.Handle(acpwire.MethodSDDAnswer, marshalOr(srv.marshalClient, controls.SDDAnswer, others[acpwire.MethodSDDAnswer]))
}
