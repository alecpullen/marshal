package acp

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

const methodFollow = "session/follow"

// followParams is session/follow's params.
type followParams struct {
	SessionID string `json:"sessionId"`
}

// updateFollowedRun is the session/update kind a follower sends when a run
// it follows starts or ends (decision 3).
const updateFollowedRun = "followed_run"

// followedRunUpdate is that update's body.
type followedRunUpdate struct {
	Kind    string             `json:"kind"`  // updateFollowedRun
	State   string             `json:"state"` // "started" or "ended"
	RunID   string             `json:"runId"`
	RunKind acpwire.RunKind    `json:"runKind,omitempty"` // on "started"
	Outcome acpwire.RunOutcome `json:"outcome,omitempty"` // on "ended"
	Error   string             `json:"error,omitempty"`   // a failed run's message
}

// The two states followedRunUpdate reports.
const (
	followedRunStarted = "started"
	followedRunEnded   = "ended"
)

// update is the update's wire body. SessionUpdateParams.Update is a
// map[string]any, so the typed body above is projected onto one; an absent
// optional field is omitted exactly as its JSON tag requires.
func (u followedRunUpdate) update() map[string]any {
	body := map[string]any{"kind": u.Kind, "state": u.State, "runId": u.RunID}
	if u.RunKind != "" {
		body["runKind"] = u.RunKind
	}
	if u.Outcome != "" {
		body["outcome"] = u.Outcome
	}
	if u.Error != "" {
		body["error"] = u.Error
	}
	return body
}

// runFollower reports one session's service runs to its connection
// (decision 2). It lives until stop, the connection's close, or the
// runtime's replacement.
//
// It is deliberately narrow: it makes no policy decision and owns no run. It
// reads the session's status, sends "started" before the run's first event and
// "ended" after its last, and forwards everything in between by the same
// forwarder a turn uses.
type runFollower struct {
	tm        *TurnManager
	sessionID string
	rt        *TurnRuntime
	state     *session.State
	service   *service.Session
	ctx       context.Context
	cancel    context.CancelFunc

	// mu guards the run being followed: its ID, the forwarder its events go
	// through, and that forwarder's own cancellation. An empty runID means no
	// run is followed, which is also how a cancel tells "stop the service's
	// run" apart from "the bridge's own turn holds the slot".
	mu      sync.Mutex
	runID   string
	fwd     *turnForwarder
	runStop context.CancelFunc

	// closing is set only by stop (the connection's teardown, or a replaced
	// runtime): nobody is left to inform, so a run still followed when the
	// follower ends is dropped rather than reported.
	closing atomic.Bool
}

// newRunFollower builds the follower for one session and runtime.
func newRunFollower(tm *TurnManager, sessionID string, rt *TurnRuntime, svc *service.Session) *runFollower {
	parent := rt.State.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &runFollower{
		tm:        tm,
		sessionID: sessionID,
		rt:        rt,
		state:     rt.State,
		service:   svc,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// start subscribes to the runtime's existing broker for the follower's whole
// life, as the child approval collector does: a run's events must not be
// dropped mid-stream.
func (f *runFollower) start() {
	if f.rt.Events == nil {
		return
	}
	events := f.rt.Events.Subscribe(f.ctx, pubsub.WithTerminal[session.Event]())
	go f.run(events)
}

// stop ends the follower and every followed run's forwarder with it.
func (f *runFollower) stop() {
	f.closing.Store(true)
	f.cancel()
}

// run is the follower's one goroutine. It selects over the session's events,
// the status ticker and the run flush ticker, so "started" is written before an
// event is forwarded and "ended" after the flush and telemetry that end a run.
func (f *runFollower) run(events <-chan pubsub.Event[session.Event]) {
	statusTicker := time.NewTicker(statusInterval)
	defer statusTicker.Stop()
	// The stack flushes at the cadence a turn's does: the follower's run
	// dirties the same projection the turn's forwarder does.
	flushInterval, _ := f.tm.stackIntervals()
	flush := time.NewTicker(flushInterval)
	defer flush.Stop()

	for {
		select {
		case <-f.ctx.Done():
			f.shutdownEnded()
			return
		case ev, ok := <-events:
			if !ok {
				f.shutdownEnded()
				return
			}
			// syncRun forwards the held event itself when it ends a run:
			// endRun closes the forwarder, so a forward afterwards would
			// drop the event the loop already took off the channel.
			if !f.syncRun(events, &ev) {
				// An event that arrives while no run is followed is not
				// forwarded: the bridge's own turn forwards its events, and
				// the service reports no run for it (decision 2).
				f.forward(ev)
			}
		case <-statusTicker.C:
			f.syncRun(events, nil)
		case <-flush.C:
			if f.following() && f.state != nil {
				f.tm.flushDirtyStack(f.sessionID, f.state)
			}
		}
	}
}

// syncRun reads the service's run and starts or ends the followed one. A
// different run than the one followed ends the old run first, then starts the
// new one.
//
// held is the event the run loop is holding, nil when the status ticker called.
// An event taken off the channel belongs to the run that is about to be
// closed, so ending a run forwards it here and reports true: the caller must
// not forward it afterwards, because endRun closes the forwarder.
func (f *runFollower) syncRun(events <-chan pubsub.Event[session.Event], held *pubsub.Event[session.Event]) bool {
	status := f.service.Status()
	cur := f.currentRunID()
	if status.Run != nil {
		if status.Run.ID == cur {
			return false
		}
		if cur != "" {
			// The old run's events are published before the slot is released,
			// so the held event is the old run's tail even when the new run
			// has started.
			f.endRun(events, status.LastRun, held)
			f.startRun(*status.Run)
			return true
		}
		f.startRun(*status.Run)
		return false
	}
	if cur != "" {
		f.endRun(events, status.LastRun, held)
		return true
	}
	return false
}

// currentRunID reports the ID of the run being followed, empty when none.
func (f *runFollower) currentRunID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runID
}

// following reports whether a run is followed right now.
func (f *runFollower) following() bool { return f.currentRunID() != "" }

// followCancel reports the followed run's forwarder cancellation, if any. The
// cancel path uses it to decide whether the service has a run to stop.
func (f *runFollower) followRun() (svc *service.Session, following bool) {
	return f.service, f.following()
}

// startRun sends "started" for a new run and builds the forwarder that run's
// events go through. The forwarder's context ends with the run.
func (f *runFollower) startRun(run acpwire.Run) {
	runCtx, runStop := context.WithCancel(f.ctx)
	fwd := f.tm.newTurnForwarder(runCtx, f.sessionID, f.rt, forwarderOptions{
		followed: true,
		failed: func() {
			slog.Default().Warn("acp: followed run's forwarding failed",
				"session", f.sessionID, "run", run.ID)
		},
	})
	f.mu.Lock()
	f.runID, f.fwd, f.runStop = run.ID, fwd, runStop
	f.mu.Unlock()
	f.notifyUpdate(followedRunUpdate{
		Kind:    updateFollowedRun,
		State:   followedRunStarted,
		RunID:   run.ID,
		RunKind: run.Kind,
	})
}

// endRun closes the followed run in the order decision 3 fixes: what is still
// buffered is forwarded, the stack is flushed, the telemetry finishTurn sends
// goes out, and only then "ended" with the outcome the service's last run
// reports.
func (f *runFollower) endRun(events <-chan pubsub.Event[session.Event], last *acpwire.RunResult, held *pubsub.Event[session.Event]) {
	// The event the loop was holding is the oldest of what is left, so it goes
	// before the buffered tail. It must be forwarded here: endRun closes the
	// forwarder below, and a forward after that drops the event.
	if held != nil {
		f.forward(*held)
	}
	// Everything still buffered belongs to the run that has just ended, so it
	// is forwarded before the run is closed.
drain:
	for events != nil {
		select {
		case ev, ok := <-events:
			if !ok {
				break drain
			}
			f.forward(ev)
		default:
			break drain
		}
	}

	if f.state != nil {
		f.tm.flushStack(f.sessionID, f.state, false)
	}
	f.tm.sendTelemetry(f.sessionID, f.rt)

	update := followedRunUpdate{Kind: updateFollowedRun, State: followedRunEnded, Outcome: acpwire.RunDone}
	f.mu.Lock()
	update.RunID = f.runID
	runStop := f.runStop
	f.runID, f.fwd, f.runStop = "", nil, nil
	f.mu.Unlock()
	if last != nil {
		update.Outcome = last.Outcome
		if last.Outcome == acpwire.RunFailed && last.Error != nil {
			update.Error = last.Error.Message
		}
	}
	f.notifyUpdate(update)
	if runStop != nil {
		runStop()
	}
}

// shutdownEnded reports the run a stopping runtime left followed, so a bridge
// showing that row as running can clear it. Nothing is drained or flushed: the
// runtime is going away and the connection may be too. The outcome is the
// service's last run for this id, unnamed when the service never recorded one
// (the follower does not know how the stop ended the work).
func (f *runFollower) shutdownEnded() {
	f.mu.Lock()
	runID, runStop := f.runID, f.runStop
	f.runID, f.fwd, f.runStop = "", nil, nil
	f.mu.Unlock()
	if runID == "" {
		return
	}
	if runStop != nil {
		runStop()
	}
	// A connection that is itself closing has no viewer left to inform.
	if f.closing.Load() {
		return
	}
	update := followedRunUpdate{Kind: updateFollowedRun, State: followedRunEnded, RunID: runID}
	if f.service != nil {
		if last := f.service.Status().LastRun; last != nil && last.ID == runID {
			update.Outcome = last.Outcome
			if last.Outcome == acpwire.RunFailed && last.Error != nil {
				update.Error = last.Error.Message
			}
		}
	}
	f.notifyUpdate(update)
}

// forward dispatches one event through the followed run's forwarder. With no
// run followed the event is dropped.
func (f *runFollower) forward(ev pubsub.Event[session.Event]) {
	f.mu.Lock()
	fwd := f.fwd
	f.mu.Unlock()
	if fwd == nil {
		return
	}
	fwd.forward(ev)
}

// notifyUpdate sends one followed_run update. A notification failure is
// logged, never surfaced: the follower has no caller to fail.
func (f *runFollower) notifyUpdate(u followedRunUpdate) {
	if err := f.tm.notify("session/update", SessionUpdateParams{
		SessionID: f.sessionID,
		Update:    u.update(),
	}); err != nil {
		slog.Default().Warn("acp: followed run update notify failed",
			"session", f.sessionID, "run", u.RunID, "state", u.State, "err", err)
	}
}

// Follow is session/follow's handler. It reports the named session's service
// runs to this connection from now until the connection or the runtime ends.
func (m *TurnManager) Follow(ctx context.Context, params json.RawMessage) (any, error) {
	// A marshal client reads its runs from the session's status: the status
	// forwarder already mirrors the run in flight and the last one, so a
	// second source would report the same run twice.
	if m.isMarshal() {
		return nil, invalidParamsError("a marshal client reads its runs from the session's status")
	}
	var p followParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/follow params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, invalidParamsError("session/follow requires sessionId")
	}
	// Resolve through the lookup every other call on this connection uses: it
	// also starts the session-lifetime child approval collector.
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, serverErrorf("session %s not found", p.SessionID)
	}
	if m.service == nil {
		return nil, serverErrorf("session %s has no run service", p.SessionID)
	}
	svc, ok := m.service(p.SessionID)
	if !ok || svc == nil {
		// Not an unknown session any more, but one nothing can run: the
		// distinction the contract asks for is kept.
		return nil, serverErrorf("session %s has no run service", p.SessionID)
	}
	m.ensureFollower(p.SessionID, rt, svc)
	return map[string]any{}, nil
}

// ensureFollower starts the session's run follower if it is not already running
// for this exact runtime. It is idempotent per (session, runtime), as
// ensureChildApprovals is: Follow may be called more than once on one
// connection, and a replaced runtime (a new State for the same session id)
// stops the old follower and starts a fresh one, so the events it forwards
// belong to the runtime that is live.
func (m *TurnManager) ensureFollower(sessionID string, rt *TurnRuntime, svc *service.Session) {
	if rt == nil || rt.State == nil || rt.Events == nil {
		return
	}
	m.followMu.Lock()
	defer m.followMu.Unlock()
	if existing, ok := m.followers[sessionID]; ok {
		if existing.state == rt.State {
			return
		}
		existing.stop()
		delete(m.followers, sessionID)
	}
	f := newRunFollower(m, sessionID, rt, svc)
	m.followers[sessionID] = f
	f.start()
}

// CloseFollowers stops every run follower this connection started. It is the
// connection-teardown half of the follower's lifetime; the session's own
// shutdown ends the other half.
func (m *TurnManager) CloseFollowers() {
	m.followMu.Lock()
	followers := m.followers
	m.followers = map[string]*runFollower{}
	m.followMu.Unlock()
	for _, f := range followers {
		f.stop()
	}
}

// followerFor returns the session's live follower, nil when this connection
// follows no run for it.
func (m *TurnManager) followerFor(sessionID string) *runFollower {
	m.followMu.Lock()
	defer m.followMu.Unlock()
	return m.followers[sessionID]
}
