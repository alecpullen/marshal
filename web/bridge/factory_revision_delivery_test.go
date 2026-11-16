package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests drive the delivery slice with a scripted runtime seam and fake
// active-work observation: nothing here opens a provider connection or a real
// worker session. The wire-level tests go through a real Registry on a scripted
// transport, so the params session/steer and session/steering_status actually
// send are asserted rather than assumed.

// revisionSteerCall is one Send now submission the fake runtime recorded.
type revisionSteerCall struct {
	session   string
	turnID    string
	requestID string
	text      string
}

// revisionRuntimeFake is the scripted runtime seam: the test states the active
// turn, what Send now answers and what receipts were observed.
type revisionRuntimeFake struct {
	mu        sync.Mutex
	session   string
	status    RevisionSteeringState
	pending   []RevisionSteeringState
	statusErr error
	steers    []revisionSteerCall
	steerErr  error
	receipt   RevisionSteeringReceipt
	receipts  map[string]RevisionSteeringReceipt
	pauses    int
	pauseErr  error
}

func newRevisionRuntimeFake() *revisionRuntimeFake {
	return &revisionRuntimeFake{
		session:  "s-1",
		status:   RevisionSteeringState{TurnID: "turn-1", Active: true, Supported: true},
		receipts: map[string]RevisionSteeringReceipt{},
	}
}

func (r *revisionRuntimeFake) RevisionSession(context.Context, FactoryItem) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session, nil
}

func (r *revisionRuntimeFake) RevisionSteeringStatus(context.Context, FactoryItem) (RevisionSteeringState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.statusErr != nil {
		return RevisionSteeringState{}, r.statusErr
	}
	if len(r.pending) > 0 {
		next := r.pending[0]
		r.pending = r.pending[1:]
		return next, nil
	}
	return r.status, nil
}

func (r *revisionRuntimeFake) RevisionSendNow(_ context.Context, _ FactoryItem, turnID, requestID, text string) (RevisionSteeringReceipt, error) {
	r.mu.Lock()
	r.steers = append(r.steers, revisionSteerCall{session: r.session, turnID: turnID, requestID: requestID, text: text})
	err := r.steerErr
	rec := r.receipt
	r.mu.Unlock()
	if err != nil {
		return RevisionSteeringReceipt{}, err
	}
	if rec.RequestID == "" {
		rec = RevisionSteeringReceipt{Version: 1, RequestID: requestID, TurnID: turnID, Status: "accepted"}
	}
	return rec, nil
}

func (r *revisionRuntimeFake) RevisionPause(context.Context, FactoryItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pauseErr != nil {
		return r.pauseErr
	}
	r.pauses++
	return nil
}

func (r *revisionRuntimeFake) RevisionReceipt(_ FactoryItem, requestID string) (RevisionSteeringReceipt, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.receipts[requestID]
	return rec, ok
}

func (r *revisionRuntimeFake) steerCalls() []revisionSteerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]revisionSteerCall(nil), r.steers...)
}

func (r *revisionRuntimeFake) pauseCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pauses
}

// observeReceipt states the receipt observed for one request without any send.
func (r *revisionRuntimeFake) observeReceipt(rec RevisionSteeringReceipt) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.receipts[rec.RequestID] = rec
}

// revisionSeamEffects is the scripted effects adapter: observation and dispatch
// from the revision fake, the runtime seam from whichever runtime the test
// installed.
type revisionSeamEffects struct {
	*revisionFakeEffects
	seam factoryRevisionRuntime
}

func (e *revisionSeamEffects) RevisionSession(ctx context.Context, item FactoryItem) (string, error) {
	return e.seam.RevisionSession(ctx, item)
}

func (e *revisionSeamEffects) RevisionSteeringStatus(ctx context.Context, item FactoryItem) (RevisionSteeringState, error) {
	return e.seam.RevisionSteeringStatus(ctx, item)
}

func (e *revisionSeamEffects) RevisionSendNow(ctx context.Context, item FactoryItem, turnID, requestID, text string) (RevisionSteeringReceipt, error) {
	return e.seam.RevisionSendNow(ctx, item, turnID, requestID, text)
}

func (e *revisionSeamEffects) RevisionPause(ctx context.Context, item FactoryItem) error {
	return e.seam.RevisionPause(ctx, item)
}

func (e *revisionSeamEffects) RevisionReceipt(item FactoryItem, requestID string) (RevisionSteeringReceipt, bool) {
	return e.seam.RevisionReceipt(item, requestID)
}

// revisionRegistrySeam is the same seam over a real Registry, so a test asserts
// the wire rather than a stub.
type revisionRegistrySeam struct {
	reg     *Registry
	session string
}

func (s *revisionRegistrySeam) RevisionSession(context.Context, FactoryItem) (string, error) {
	return s.session, nil
}

func (s *revisionRegistrySeam) RevisionSteeringStatus(ctx context.Context, _ FactoryItem) (RevisionSteeringState, error) {
	return s.reg.RevisionSteeringStatus(ctx, s.session)
}

func (s *revisionRegistrySeam) RevisionSendNow(ctx context.Context, _ FactoryItem, turnID, requestID, text string) (RevisionSteeringReceipt, error) {
	return s.reg.RevisionSendNow(ctx, s.session, turnID, requestID, text)
}

func (s *revisionRegistrySeam) RevisionPause(ctx context.Context, _ FactoryItem) error {
	return s.reg.Cancel(ctx, s.session)
}

func (s *revisionRegistrySeam) RevisionReceipt(_ FactoryItem, requestID string) (RevisionSteeringReceipt, bool) {
	return s.reg.revisionReceipt(s.session, requestID)
}

// revisionWire is a scripted child transport for the steering wire: it answers
// the methods a test states, records every frame, and can push a notification.
type revisionWire struct {
	mu      sync.Mutex
	seen    []capturedFrame
	results map[string]json.RawMessage
	out     io.WriteCloser
	ready   chan struct{}
}

func newRevisionWire() *revisionWire {
	return &revisionWire{results: map[string]json.RawMessage{}, ready: make(chan struct{})}
}

func (w *revisionWire) setResult(method, body string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.results[method] = json.RawMessage(body)
}

func (w *revisionWire) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	w.mu.Lock()
	w.out = stdoutW
	w.mu.Unlock()
	close(w.ready)
	go w.serve(stdinR, stdoutW)
	return stdinW, stdoutR, io.NopCloser(strings.NewReader("")), nil
}

func (w *revisionWire) serve(r io.Reader, out io.WriteCloser) {
	defer out.Close()
	sc := bufio.NewScanner(r)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		w.mu.Lock()
		w.seen = append(w.seen, capturedFrame{method: req.Method, params: string(req.Params)})
		result, ok := w.results[req.Method]
		w.mu.Unlock()
		if len(req.ID) == 0 || string(req.ID) == "null" {
			// A notification (session/cancel) takes no reply.
			continue
		}
		if !ok {
			result = json.RawMessage(`{}`)
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}

// notify pushes one child notification from the scripted transport.
func (w *revisionWire) notify(method string, params any) {
	<-w.ready
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = json.NewEncoder(w.out).Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (w *revisionWire) frames() []capturedFrame {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]capturedFrame(nil), w.seen...)
}

// only returns the frames for one method.
func (w *revisionWire) only(method string) []capturedFrame {
	var out []capturedFrame
	for _, f := range w.frames() {
		if f.method == method {
			out = append(out, f)
		}
	}
	return out
}

func (w *revisionWire) Wait() error                { return nil }
func (w *revisionWire) Signal(sig os.Signal) error { return nil }
func (w *revisionWire) Kill() error                { return nil }
func (w *revisionWire) Detach() error              { return nil }

// newRevisionWireRegistry wires a Registry onto the scripted wire with one
// tracked session.
func newRevisionWireRegistry(t *testing.T, w *revisionWire) *Registry {
	t.Helper()
	c := &Child{Transport: w}
	r := NewRegistry(c)
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(c.Stop)
	r.track("s-1", AgentPath("/tmp/work"))
	return r
}

// newRevisionDeliveryFixture is the journal fixture with a scripted runtime seam
// installed, so the delivery pass runs without a fleet.
func newRevisionDeliveryFixture(t *testing.T) (*revisionFixture, *revisionFakeEffects, *revisionRuntimeFake) {
	t.Helper()
	f, fx := newRevisionEffectsFixture(t)
	rt := newRevisionRuntimeFake()
	f.ctl.effects = &revisionSeamEffects{revisionFakeEffects: fx, seam: rt}
	return f, fx, rt
}

// revisionDelegation is the project delegation setting a pause request reads.
func revisionDelegation(enabled bool) func(string) (ProjectSettings, bool) {
	return func(string) (ProjectSettings, bool) {
		if !enabled {
			return ProjectSettings{}, true
		}
		return ProjectSettings{PlannerDelegationEnabled: true, DesignatedMCPClientID: "planner-1"}, true
	}
}

// revisionLiveEffect seeds the running work an assessment can name as affected,
// with the worker that owns the checkout.
func revisionLiveEffect(t *testing.T, f *revisionFixture) FactoryItem {
	t.Helper()
	return revisionState(t, f, "live", func(dst *FactoryItem) error {
		dst.Workers = []FactoryWorker{{AgentID: "a-1", SessionID: "s-1", Role: "implementer", SpawnEffectID: dst.ID + "-1"}}
		dst.Effects = append(dst.Effects, FactoryEffect{
			ID: dst.ID + "-2", Kind: "implement", WorkerRole: "implementer",
			Status: "dispatched", ApprovalRevision: 1,
		})
		return nil
	})
}

// revisionAssessWith writes one assessment onto the proposal as a planner would,
// then returns the item.
func revisionAssessWith(t *testing.T, f *revisionFixture, classification, recommendation, strategy, affectedEffectID string, delivery bool) FactoryItem {
	t.Helper()
	return revisionState(t, f, "assessed", func(dst *FactoryItem) error {
		p := &dst.Proposals[0]
		a := FactoryRevisionAssessment{
			Classification: classification, Reason: "the candidate conflicts with the running work",
			Recommendation: recommendation, Strategy: strategy, AssessorID: "planner-fixture",
			CandidateHash: p.CandidateHash, BaseRevision: p.BaseRevision, AffectedEffectID: affectedEffectID,
		}
		p.Assessment = &a
		p.AssessmentState = "assessed"
		p.Routing = recommendation
		p.Status = "awaiting_authorization"
		if delivery {
			p.Delivery = &FactoryRevisionDelivery{Strategy: strategy, State: "pending", CandidateHash: p.CandidateHash}
		}
		p.Version++
		return nil
	})
}

// TestFactoryRevisionDelegatedAssessmentRequestsPause checks a planner acting
// under project delegation journals the pause intent, tied to the affected
// effect and the turn, before the existing cancel path is called.
func TestFactoryRevisionDelegatedAssessmentRequestsPause(t *testing.T) {
	f, fx, rt := newRevisionDeliveryFixture(t)
	live := revisionLiveEffect(t, f)
	effectID := live.Effects[len(live.Effects)-1].ID
	fx.obs = revisionIdleObservation(live)
	f.ctl.projectSettings = revisionDelegation(true)

	planner := &fakeRevisionPlanner{available: true, store: f.store, itemID: f.item.ID}
	planner.assess = func(req RevisionAssessmentRequest) (FactoryRevisionAssessment, error) {
		a := judgementFor(req, "scope_change", "amend", "interrupt")
		a.AffectedEffectID = effectID
		return a, nil
	}
	f.ctl.setRevisionPlanner(planner)

	if _, err := f.propose("rev-1", "plan v2\n", "spec v2"); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.assessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := rt.pauseCount(); got != 0 {
		t.Fatalf("recording an assessment paused work on its own: %d", got)
	}
	if err := f.ctl.advanceRevisionPauses(context.Background()); err != nil {
		t.Fatal(err)
	}
	item := f.current()
	p := item.Proposals[0]
	if p.PauseState != "stopping" {
		t.Fatalf("pause state = %q, want the request sent and unconfirmed", p.PauseState)
	}
	if want := factoryRevisionPauseRequestID(item.ID, p.ID); p.PauseRequestID != want {
		t.Fatalf("pause request = %q, want %q", p.PauseRequestID, want)
	}
	// The intent is journalled with the effect it affects and the turn it was
	// observed in, before the cancellation.
	requested := false
	for _, h := range item.History {
		if h.Kind == "revision_pause_requested" {
			requested = true
			if h.EffectID != effectID {
				t.Fatalf("the pause intent does not name the affected effect: %+v", h)
			}
			if !strings.Contains(h.Message, "turn-1") || !strings.Contains(h.Message, p.PauseRequestID) {
				t.Fatalf("the pause intent does not name the request and turn: %+v", h)
			}
		}
	}
	if !requested {
		t.Fatalf("no pause intent was journalled: %+v", item.History)
	}
	for _, h := range item.History {
		if h.Kind == "revision_pause_requested" {
			break
		}
	}
	if got := rt.pauseCount(); got != 1 {
		t.Fatalf("cancel requests = %d, want exactly one", got)
	}
}

// TestFactoryRevisionPauseNeedsUserWhenDelegationOff checks a pause is never
// taken on the planner's own account without delegation: the recommendation is
// recorded, and only an explicit user choice asks for the pause.
func TestFactoryRevisionPauseNeedsUserWhenDelegationOff(t *testing.T) {
	f, fx, rt := newRevisionDeliveryFixture(t)
	live := revisionLiveEffect(t, f)
	effectID := live.Effects[len(live.Effects)-1].ID
	fx.obs = revisionIdleObservation(live)
	f.ctl.projectSettings = revisionDelegation(false)

	if _, err := f.propose("rev-1", "plan v2\n", "spec v2"); err != nil {
		t.Fatal(err)
	}
	assessed := revisionAssessWith(t, f, "scope_change", "amend", "interrupt", effectID, false)
	if assessed.Proposals[0].Assessment.Strategy != "interrupt" {
		t.Fatalf("the recommendation was not recorded: %+v", assessed.Proposals[0].Assessment)
	}
	if err := f.ctl.advanceRevisionPauses(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.current()
	if got.Proposals[0].PauseState != "" {
		t.Fatalf("a planner without delegation paused work: %+v", got.Proposals[0])
	}
	if rt.pauseCount() != 0 {
		t.Fatalf("a planner without delegation sent %d cancel requests", rt.pauseCount())
	}

	// The user's explicit request is the delivery choice naming the affected
	// work's strategy.
	if _, err := f.command(FactoryCommand{
		RequestID: "pause-1", Kind: "revision_delivery_choice",
		Revision: &FactoryRevisionCommand{
			ProposalID: got.Proposals[0].ID, ProposalVersion: got.Proposals[0].Version,
			CandidateHash: got.Proposals[0].CandidateHash, Strategy: "interrupt",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.advanceRevisionPauses(context.Background()); err != nil {
		t.Fatal(err)
	}
	asked := f.current()
	if asked.Proposals[0].PauseState != "stopping" {
		t.Fatalf("the user's explicit pause was not requested: %+v", asked.Proposals[0])
	}
	if rt.pauseCount() != 1 {
		t.Fatalf("cancel requests = %d, want exactly one", rt.pauseCount())
	}
}

// TestFactoryRevisionPauseIsNotFactoryStop checks a pause keeps the worker and
// its checkout, is distinguishable from the factory's terminal stop, and holds
// the scheduler until it is resolved.
func TestFactoryRevisionPauseIsNotFactoryStop(t *testing.T) {
	f, fx, rt := newRevisionDeliveryFixture(t)
	live := revisionLiveEffect(t, f)
	effectID := live.Effects[len(live.Effects)-1].ID
	fx.obs = revisionIdleObservation(live)
	f.ctl.projectSettings = revisionDelegation(true)
	if _, err := f.propose("rev-1", "plan v2\n", "spec v2"); err != nil {
		t.Fatal(err)
	}
	revisionAssessWith(t, f, "scope_change", "amend", "interrupt", effectID, false)
	if err := f.ctl.advanceRevisionPauses(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rt.pauseCount() != 1 {
		t.Fatalf("cancel requests = %d", rt.pauseCount())
	}

	// The matching canceled effect settles as paused for this proposal once the
	// turn and the active work are observed idle.
	settled := revisionState(t, f, "cancelled", func(dst *FactoryItem) error {
		e := &dst.Effects[len(dst.Effects)-1]
		e.Status = "completed"
		e.Outcome = &FactoryOutcome{EffectID: e.ID, Confirmed: true, StopReason: "cancelled", Error: "worker turn ended with \"cancelled\""}
		dst.State = "needs_input"
		dst.Decision = &FactoryDecision{ID: "decision-paused", Kind: "worker_failed", Reason: "cancelled by the proposal's pause", EffectID: e.ID}
		return nil
	})
	fx.obs = revisionIdleObservation(settled)
	if err := f.ctl.advanceRevisionPauses(context.Background()); err != nil {
		t.Fatal(err)
	}
	item := f.current()
	if item.Proposals[0].PauseState != "paused" {
		t.Fatalf("pause state = %q, want paused", item.Proposals[0].PauseState)
	}
	// It is not the factory's terminal stop: no stopping state, no park effect,
	// and the worker keeps its checkout.
	if item.State == "stopping" || item.State == "stopped" || item.State == "closed" {
		t.Fatalf("a pause became the factory's stop: %s", item.State)
	}
	if len(item.Workers) != 1 || item.Workers[0].Parked {
		t.Fatalf("the paused worker lost its checkout: %+v", item.Workers)
	}
	for _, e := range item.Effects {
		if e.Kind == "park" {
			t.Fatalf("a pause parked the worker: %+v", item.Effects)
		}
	}
	if !factoryRevisionWorkPaused(item) {
		t.Fatal("the paused item is not held")
	}

	// The scheduler dispatches nothing for a paused item, even with work
	// prepared to go.
	pending := revisionState(t, f, "prepared", func(dst *FactoryItem) error {
		dst.Effects = append(dst.Effects, FactoryEffect{ID: dst.ID + "-3", Kind: "review", Status: "prepared", WorkerRole: "reviewer", ApprovalRevision: 1})
		return nil
	})
	fx.obs = revisionIdleObservation(pending)
	f.ctl.admitAfterRecovery()
	if err := f.ctl.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fx.executedEffects()) != 0 {
		t.Fatalf("the scheduler dispatched for a paused item: %v", fx.executedEffects())
	}
}

// TestFactoryRevisionPauseDoesNotAssumeIdle checks a cancellation request, a
// detached command or an unobserved runtime never becomes a confirmed pause.
func TestFactoryRevisionPauseDoesNotAssumeIdle(t *testing.T) {
	f, fx, rt := newRevisionDeliveryFixture(t)
	live := revisionLiveEffect(t, f)
	effectID := live.Effects[len(live.Effects)-1].ID
	fx.obs = revisionIdleObservation(live)
	f.ctl.projectSettings = revisionDelegation(true)
	if _, err := f.propose("rev-1", "plan v2\n", "spec v2"); err != nil {
		t.Fatal(err)
	}
	revisionAssessWith(t, f, "scope_change", "amend", "interrupt", effectID, false)
	if err := f.ctl.advanceRevisionPauses(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current().Proposals[0].PauseState; got != "stopping" {
		t.Fatalf("pause state = %q, want stopping", got)
	}
	if rt.pauseCount() != 1 {
		t.Fatalf("cancel requests = %d", rt.pauseCount())
	}

	// A detached command whose state is unknown is not idle.
	detached := revisionIdleObservation(live)
	detached.ActiveWorkKnown = false
	detached.WorkIdle = false
	fx.obs = detached
	if err := f.ctl.advanceRevisionPauses(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current().Proposals[0].PauseState; got != "stopping" {
		t.Fatalf("an unknown active work confirmed the pause: %q", got)
	}

	// A busy turn is not idle.
	busy := revisionIdleObservation(live)
	busy.TurnIdle = false
	fx.obs = busy
	if err := f.ctl.advanceRevisionPauses(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current().Proposals[0].PauseState; got != "stopping" {
		t.Fatalf("a running turn confirmed the pause: %q", got)
	}

	// The runtime itself being unobservable is not idle either.
	fx.obs, fx.obsErr = revisionIdleObservation(live), errors.New("runtime unavailable")
	if err := f.ctl.advanceRevisionPauses(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current().Proposals[0].PauseState; got != "stopping" {
		t.Fatalf("an unobserved runtime confirmed the pause: %q", got)
	}

	// Only the full conjunction, with the affected effect settled, confirms it.
	settled := revisionState(t, f, "settled", func(dst *FactoryItem) error {
		e := &dst.Effects[len(dst.Effects)-1]
		e.Status = "completed"
		e.Outcome = &FactoryOutcome{EffectID: e.ID, Confirmed: true, StopReason: "cancelled"}
		return nil
	})
	fx.obs, fx.obsErr = revisionIdleObservation(settled), nil
	if err := f.ctl.advanceRevisionPauses(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current().Proposals[0].PauseState; got != "paused" {
		t.Fatalf("the observed idle state did not confirm the pause: %q", got)
	}
}

// TestFactoryRevisionPausedWorkerExplicitResume checks a pause survives the
// proposal's retirement and is lifted only by the user's explicit resume.
func TestFactoryRevisionPausedWorkerExplicitResume(t *testing.T) {
	f, _, _ := newRevisionDeliveryFixture(t)
	live := revisionLiveEffect(t, f)
	effectID := live.Effects[len(live.Effects)-1].ID
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	revisionAssessWith(t, f, "scope_change", "amend", "interrupt", effectID, false)
	paused := revisionState(t, f, "paused", func(dst *FactoryItem) error {
		p := &dst.Proposals[0]
		p.PauseState = "paused"
		p.PauseRequestID = factoryRevisionPauseRequestID(dst.ID, p.ID)
		p.Version++
		return nil
	})
	if !factoryRevisionWorkPaused(paused) {
		t.Fatal("the seeded pause does not hold the item")
	}
	if proposed.Proposals[0].ID != paused.Proposals[0].ID {
		t.Fatalf("proposal identity moved: %+v", paused.Proposals)
	}

	// Superseding the proposal does not silently restart work.
	retired, err := f.command(FactoryCommand{
		RequestID: "supersede-1", Kind: "revision_supersede", Revision: revisionAt(paused, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if retired.Proposals[0].Status != "superseded" {
		t.Fatalf("proposal status = %q", retired.Proposals[0].Status)
	}
	if retired.Proposals[0].PauseState != "paused" {
		t.Fatalf("superseding silently resumed the paused worker: %+v", retired.Proposals[0])
	}

	// The user's explicit resume is the delivery choice naming the paused
	// proposal; it needs no new command kind.
	resumed, err := f.command(FactoryCommand{
		RequestID: "resume-1", Kind: "revision_delivery_choice",
		Revision: &FactoryRevisionCommand{
			ProposalID: retired.Proposals[0].ID, ProposalVersion: retired.Proposals[0].Version,
			CandidateHash: retired.Proposals[0].CandidateHash,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := resumed.Proposals[0]
	if p.PauseState != "" || p.PauseRequestID != "" {
		t.Fatalf("the explicit resume did not clear the pause: %+v", p)
	}
	if !revisionHistoryHas(resumed, "revision_resumed", p.ID) {
		t.Fatalf("the resume was not journalled: %+v", resumed.History)
	}
	if factoryRevisionWorkPaused(resumed) {
		t.Fatal("the resumed item is still held")
	}
	if p.Status != "superseded" {
		t.Fatalf("the resume rewrote the proposal's retirement: %q", p.Status)
	}
}

// TestFactoryRevisionSteeringUsesExplicitTurnAndRequest checks the wire: Send
// now names the session, the active turn, the journaled request and the
// delivery, and the reply is stored as evidence bound to the candidate.
func TestFactoryRevisionSteeringUsesExplicitTurnAndRequest(t *testing.T) {
	wire := newRevisionWire()
	wire.setResult("session/steering_status", `{"version":3,"turnId":"turn-1","active":true,"supported":true,"held":false,"pendingCount":0}`)
	reg := newRevisionWireRegistry(t, wire)

	f, fx, _ := newRevisionDeliveryFixture(t)
	f.ctl.effects = &revisionSeamEffects{revisionFakeEffects: fx, seam: &revisionRegistrySeam{reg: reg, session: "s-1"}}
	live := revisionLiveEffect(t, f)
	fx.obs = revisionIdleObservation(live)

	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	candidate := proposed.Proposals[0]
	if _, err := f.command(authorizeCommand(candidate, "amend", "interrupt")); err != nil {
		t.Fatal(err)
	}
	requestID := factoryRevisionSteerRequestID(f.item.ID, candidate.ID)
	wire.setResult("session/steer", `{"version":1,"requestId":"`+requestID+`","turnId":"turn-1","status":"accepted"}`)

	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	item := f.current()
	d := item.Proposals[0].Delivery
	if d.State != "accepted" {
		t.Fatalf("delivery state = %q, want accepted", d.State)
	}
	if d.SessionID != "s-1" || d.TurnID != "turn-1" || d.RequestID != requestID {
		t.Fatalf("the intent did not record the session, turn and request: %+v", d)
	}
	if d.CandidateHash != candidate.CandidateHash {
		t.Fatalf("the intent is not bound to the candidate: %+v", d)
	}
	if len(item.Approvals) != 1 {
		t.Fatalf("a receipt activated the revision: %+v", item.Approvals)
	}

	// The status read came first, then exactly one Send now with the explicit
	// turn, request and delivery.
	if got := len(wire.only("session/steering_status")); got != 2 {
		t.Fatalf("steering_status calls = %d, want the intent read and the re-read", got)
	}
	steers := wire.only("session/steer")
	if len(steers) != 1 {
		t.Fatalf("session/steer frames = %+v", steers)
	}
	params := steers[0].params
	for _, want := range []string{`"delivery":"now"`, `"turnId":"turn-1"`, `"requestId":"` + requestID + `"`, `"sessionId":"s-1"`, candidate.CandidateHash} {
		if !strings.Contains(params, want) {
			t.Fatalf("session/steer params %s missing %s", params, want)
		}
	}
}

// TestFactoryRevisionReceiptMustMatchIntent checks a receipt for another request
// or another turn is not this proposal's evidence.
func TestFactoryRevisionReceiptMustMatchIntent(t *testing.T) {
	wire := newRevisionWire()
	wire.setResult("session/steering_status", `{"version":3,"turnId":"turn-1","active":true,"supported":true}`)
	reg := newRevisionWireRegistry(t, wire)

	f, fx, _ := newRevisionDeliveryFixture(t)
	f.ctl.effects = &revisionSeamEffects{revisionFakeEffects: fx, seam: &revisionRegistrySeam{reg: reg, session: "s-1"}}
	fx.obs = revisionIdleObservation(revisionLiveEffect(t, f))
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	candidate := proposed.Proposals[0]
	if _, err := f.command(authorizeCommand(candidate, "amend", "interrupt")); err != nil {
		t.Fatal(err)
	}
	requestID := factoryRevisionSteerRequestID(f.item.ID, candidate.ID)
	wire.setResult("session/steer", `{"version":1,"requestId":"`+requestID+`","turnId":"turn-1","status":"accepted"}`)
	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A receipt for another request is stored under its own request and never
	// read as this proposal's delivery.
	wire.notify("session/update", map[string]any{
		"sessionId": "s-1",
		"update":    map[string]any{"kind": "steering_receipt", "version": 9, "requestId": "rev-steer-other", "turnId": "turn-1", "status": "delivered"},
	})
	waitFor(t, 2*time.Second, "the unrelated receipt to be observed", func() bool {
		_, ok := reg.revisionReceipt("s-1", "rev-steer-other")
		return ok
	})
	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current().Proposals[0].Delivery.State; got != "accepted" {
		t.Fatalf("an unrelated receipt moved the delivery to %q", got)
	}

	// A receipt for this request but another turn is not evidence either.
	wire.notify("session/update", map[string]any{
		"sessionId": "s-1",
		"update":    map[string]any{"kind": "steering_receipt", "version": 4, "requestId": requestID, "turnId": "turn-9", "status": "delivered"},
	})
	waitFor(t, 2*time.Second, "the mismatched-turn receipt to be observed", func() bool {
		rec, ok := reg.revisionReceipt("s-1", requestID)
		return ok && rec.TurnID == "turn-9"
	})
	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current().Proposals[0].Delivery.State; got != "accepted" {
		t.Fatalf("a receipt for another turn moved the delivery to %q", got)
	}

	// The matching receipt is the evidence, and it is only ever "delivered to
	// the runtime".
	wire.notify("session/update", map[string]any{
		"sessionId": "s-1",
		"update":    map[string]any{"kind": "steering_receipt", "version": 5, "requestId": requestID, "turnId": "turn-1", "status": "delivered"},
	})
	waitFor(t, 2*time.Second, "the matching receipt to be observed", func() bool {
		rec, ok := reg.revisionReceipt("s-1", requestID)
		return ok && rec.Status == "delivered"
	})
	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	item := f.current()
	if got := item.Proposals[0].Delivery.State; got != "delivered" {
		t.Fatalf("delivery state = %q, want delivered", got)
	}
	if len(item.Approvals) != 1 {
		t.Fatalf("a delivered receipt activated the revision on its own: %+v", item.Approvals)
	}
}

// TestFactoryRevisionQueuedIsNotDelivered checks ordinary queued steering
// produces no receipt and never becomes delivery evidence.
func TestFactoryRevisionQueuedIsNotDelivered(t *testing.T) {
	wire := newRevisionWire()
	reg := newRevisionWireRegistry(t, wire)
	ctx, cancel := testContext(t)
	defer cancel()

	if err := reg.Steer(ctx, "s-1", "keep going"); err != nil {
		t.Fatal(err)
	}
	queued := wire.only("session/steer")
	if len(queued) != 1 || queued[0].params != `{"sessionId":"s-1","text":"keep going"}` {
		t.Fatalf("queued steer params = %+v", queued)
	}
	if _, ok := reg.revisionReceipt("s-1", "rev-steer-anything"); ok {
		t.Fatal("a queued steer produced a receipt")
	}

	f, fx, _ := newRevisionDeliveryFixture(t)
	f.ctl.effects = &revisionSeamEffects{revisionFakeEffects: fx, seam: &revisionRegistrySeam{reg: reg, session: "s-1"}}
	fx.obs = revisionIdleObservation(revisionLiveEffect(t, f))
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "wait_safe")); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := f.current().Proposals[0].Delivery
	if d.State != "waiting_safe_boundary" {
		t.Fatalf("wait_safe delivery state = %q", d.State)
	}
	if d.RequestID != "" || d.State == "delivered" {
		t.Fatalf("wait_safe claimed a delivery receipt: %+v", d)
	}
	if len(wire.only("session/steer")) != 1 {
		t.Fatalf("a wait_safe delivery sent a steer: %+v", wire.frames())
	}
}

// TestFactoryRevisionSendNowOldEvidenceStaysOld checks a successful Send now
// keeps the old effect and round attributed to the revision it started under,
// and that no idle-looking observation is substituted for proof.
func TestFactoryRevisionSendNowOldEvidenceStaysOld(t *testing.T) {
	f, fx, rt := newRevisionDeliveryFixture(t)
	fx.obs = revisionIdleObservation(revisionImplementingItem(t, f))
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "interrupt"))
	if err != nil {
		t.Fatal(err)
	}
	oldEffect := authorized.Effects[0]
	oldRound := authorized.Rounds[0]
	rt.receipt = RevisionSteeringReceipt{
		Version: 1, TurnID: "turn-1", Status: "delivered",
		RequestID: factoryRevisionSteerRequestID(f.item.ID, authorized.Proposals[0].ID),
	}
	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	delivered := f.current()
	if got := delivered.Proposals[0].Delivery.State; got != "delivered" {
		t.Fatalf("delivery state = %q, want delivered", got)
	}
	if len(delivered.Approvals) != 1 {
		t.Fatalf("Send now appended an approval: %+v", delivered.Approvals)
	}
	if delivered.Effects[0].ApprovalRevision != oldEffect.ApprovalRevision || delivered.Rounds[0].ApprovalRevision != oldRound.ApprovalRevision {
		t.Fatalf("Send now rewrote the old evidence: effect=%+v round=%+v", delivered.Effects[0], delivered.Rounds[0])
	}

	// The delivered receipt is not an idle observation: a busy turn keeps the
	// old approval active.
	busy := revisionIdleObservation(delivered)
	busy.TurnIdle = false
	fx.obs = busy
	if err := f.ctl.activateRevisionAtSafeBoundary(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current(); len(got.Approvals) != 1 {
		t.Fatalf("a busy turn activated the revision: %+v", got.Approvals)
	}

	// Once the turn is proven idle the approval moves, and the old effect and
	// round keep the revision they ran under.
	fx.obs = revisionIdleObservation(delivered)
	if err := f.ctl.activateRevisionAtSafeBoundary(context.Background()); err != nil {
		t.Fatal(err)
	}
	activated := f.current()
	if len(activated.Approvals) != 2 || activated.Approvals[1].Revision != 2 {
		t.Fatalf("approvals = %+v, want a second approval", activated.Approvals)
	}
	if activated.Effects[0].ApprovalRevision != 1 || activated.Rounds[0].ApprovalRevision != 1 {
		t.Fatalf("the old evidence was reassigned: effect=%+v round=%+v", activated.Effects[0], activated.Rounds[0])
	}
	if activated.Proposals[0].Delivery.State != "delivered" {
		t.Fatalf("activation rewrote the receipt: %+v", activated.Proposals[0].Delivery)
	}
}

// TestFactoryRevisionInterruptDoesNotAssumeIdle checks an interrupt waits for
// independent idle and active-work proof before activation and next work.
func TestFactoryRevisionInterruptDoesNotAssumeIdle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*FactoryObservation)
	}{
		{"turn busy", func(o *FactoryObservation) { o.TurnIdle = false }},
		{"active work busy", func(o *FactoryObservation) { o.WorkIdle = false }},
		{"runtime unknown", func(o *FactoryObservation) { o.RuntimeKnown = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, fx, rt := newRevisionDeliveryFixture(t)
			implementing := revisionImplementingItem(t, f)
			fx.obs = revisionIdleObservation(implementing)
			proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
			if err != nil {
				t.Fatal(err)
			}
			authorized, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "interrupt"))
			if err != nil {
				t.Fatal(err)
			}
			rt.receipt = RevisionSteeringReceipt{
				Version: 1, TurnID: "turn-1", Status: "accepted",
				RequestID: factoryRevisionSteerRequestID(f.item.ID, authorized.Proposals[0].ID),
			}
			if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
				t.Fatal(err)
			}
			obs := revisionIdleObservation(authorized)
			tc.mutate(&obs)
			fx.obs = obs
			if err := f.ctl.activateRevisionAtSafeBoundary(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := f.current(); len(got.Approvals) != 1 {
				t.Fatalf("%s activated the revision: %+v", tc.name, got.Approvals)
			}
		})
	}
}

// TestFactoryRevisionDetachedWorkBlocksActivation checks a detached command
// whose state is unknown blocks activation and the next work.
func TestFactoryRevisionDetachedWorkBlocksActivation(t *testing.T) {
	f, fx, rt := newRevisionDeliveryFixture(t)
	implementing := revisionImplementingItem(t, f)
	fx.obs = revisionIdleObservation(implementing)
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "interrupt"))
	if err != nil {
		t.Fatal(err)
	}
	rt.receipt = RevisionSteeringReceipt{
		Version: 1, TurnID: "turn-1", Status: "delivered",
		RequestID: factoryRevisionSteerRequestID(f.item.ID, authorized.Proposals[0].ID),
	}
	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The turn is idle but a detached command's state is unknown.
	detached := revisionIdleObservation(authorized)
	detached.ActiveWorkKnown = false
	fx.obs = detached
	if err := f.ctl.activateRevisionAtSafeBoundary(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current(); len(got.Approvals) != 1 {
		t.Fatalf("detached work did not block activation: %+v", got.Approvals)
	}
	// The scheduler starts nothing under the new revision while the checkout
	// is not proven idle under the existing rules: whatever it dispatches still
	// carries the approval that is active.
	f.ctl.admitAfterRecovery()
	if err := f.ctl.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := f.current()
	if len(after.Approvals) != 1 {
		t.Fatalf("detached work did not hold the revision: %+v", after.Approvals)
	}
	for _, e := range after.Effects {
		if e.ApprovalRevision != 1 {
			t.Fatalf("the new revision reached the work: %+v", e)
		}
	}
}

// TestFactoryRevisionLostReceiptIsUncertain checks a sent request whose receipt
// was never observed stays uncertain and is never resent.
func TestFactoryRevisionLostReceiptIsUncertain(t *testing.T) {
	// The runtime accepts the call and answers with no receipt at all.
	wire := newRevisionWire()
	wire.setResult("session/steering_status", `{"version":1,"turnId":"turn-1","active":true,"supported":true}`)
	wire.setResult("session/steer", `{}`)
	reg := newRevisionWireRegistry(t, wire)

	f, fx, _ := newRevisionDeliveryFixture(t)
	f.ctl.effects = &revisionSeamEffects{revisionFakeEffects: fx, seam: &revisionRegistrySeam{reg: reg, session: "s-1"}}
	fx.obs = revisionIdleObservation(revisionLiveEffect(t, f))
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "interrupt")); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current().Proposals[0].Delivery.State; got != "uncertain" {
		t.Fatalf("delivery state = %q, want uncertain", got)
	}
	// A second pass neither resends nor invents a delivery.
	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(wire.only("session/steer")); got != 1 {
		t.Fatalf("session/steer frames = %d, want exactly one", got)
	}
	if got := f.current().Proposals[0].Delivery.State; got != "uncertain" {
		t.Fatalf("a repeat pass changed the outcome to %q", got)
	}

	// A journaled intent whose outcome was never recorded — a restart — is
	// uncertain too, and is not sent again.
	restarted := revisionState(t, f, "dispatching", func(dst *FactoryItem) error {
		p := &dst.Proposals[0]
		p.Delivery.State = "dispatching"
		p.Version++
		return nil
	})
	if restarted.Proposals[0].Delivery.State != "dispatching" {
		t.Fatalf("seed delivery = %+v", restarted.Proposals[0].Delivery)
	}
	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current().Proposals[0].Delivery.State; got != "uncertain" {
		t.Fatalf("a restart without evidence reported %q", got)
	}
	if got := len(wire.only("session/steer")); got != 1 {
		t.Fatalf("a restart resent the request: %d frames", got)
	}
}

// TestFactoryRevisionHoldDoesNotSend checks the user's hold dispatches nothing
// and implies no fallback.
func TestFactoryRevisionHoldDoesNotSend(t *testing.T) {
	wire := newRevisionWire()
	wire.setResult("session/steering_status", `{"version":1,"turnId":"turn-1","active":true,"supported":true}`)
	reg := newRevisionWireRegistry(t, wire)
	f, fx, _ := newRevisionDeliveryFixture(t)
	f.ctl.effects = &revisionSeamEffects{revisionFakeEffects: fx, seam: &revisionRegistrySeam{reg: reg, session: "s-1"}}
	live := revisionLiveEffect(t, f)
	fx.obs = revisionIdleObservation(live)
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "hold")); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if frames := wire.frames(); len(frames) != 0 {
		t.Fatalf("a held delivery reached the runtime: %+v", frames)
	}
	item := f.current()
	if got := item.Proposals[0].Delivery.State; got != "pending" {
		t.Fatalf("hold delivery state = %q, want pending", got)
	}
	// A held delivery does not activate the revision either.
	fx.obs = revisionIdleObservation(item)
	if err := f.ctl.activateRevisionAtSafeBoundary(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current(); len(got.Approvals) != 1 {
		t.Fatalf("a held revision activated: %+v", got.Approvals)
	}
}

// TestFactoryRevisionWaitSafeNextPromptContainsNewPlan checks wait_safe makes no
// delivery claim, activates at the safe boundary, and that the next normal
// factory prompt carries the new plan.
func TestFactoryRevisionWaitSafeNextPromptContainsNewPlan(t *testing.T) {
	wire := newRevisionWire()
	reg := newRevisionWireRegistry(t, wire)
	f, fx, _ := newRevisionDeliveryFixture(t)
	f.ctl.effects = &revisionSeamEffects{revisionFakeEffects: fx, seam: &revisionRegistrySeam{reg: reg, session: "s-1"}}
	implementing := revisionImplementingItem(t, f)
	fx.obs = revisionIdleObservation(implementing)
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "wait_safe")); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.deliverAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := f.current().Proposals[0].Delivery
	if d.State != "waiting_safe_boundary" || d.RequestID != "" {
		t.Fatalf("wait_safe delivery = %+v", d)
	}
	if frames := wire.frames(); len(frames) != 0 {
		t.Fatalf("wait_safe reached the runtime: %+v", frames)
	}

	// The old effect settles and the checkout is observed idle: the revision
	// activates without any delivery evidence being invented.
	settled := revisionState(t, f, "settled", func(dst *FactoryItem) error {
		e := &dst.Effects[len(dst.Effects)-1]
		e.Status = "completed"
		e.Outcome = &FactoryOutcome{EffectID: e.ID, Confirmed: true, HeadSHA: "head", TreeSHA: "tree"}
		return nil
	})
	fx.obs = revisionIdleObservation(settled)
	if err := f.ctl.activateRevisionAtSafeBoundary(context.Background()); err != nil {
		t.Fatal(err)
	}
	activated := f.current()
	if len(activated.Approvals) != 2 || activated.Approvals[1].Plan != "plan v2\n" {
		t.Fatalf("wait_safe did not activate the candidate: %+v", activated.Approvals)
	}
	prompt, err := BuildFactoryPrompt(activated, FactoryEffect{ID: activated.ID + "-4", Kind: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "plan v2\n") {
		t.Fatalf("the next factory prompt does not carry the new plan: %s", prompt)
	}
	if strings.Contains(prompt, " plan\n") {
		t.Fatalf("the next factory prompt still carries the old plan: %s", prompt)
	}
}

// TestFactoryRevisionDirectSteeringRemainsAvailable checks the existing queued
// steer is untouched and that Send now stays a separate, explicit request.
func TestFactoryRevisionDirectSteeringRemainsAvailable(t *testing.T) {
	wire := newRevisionWire()
	wire.setResult("session/steering_status", `{"version":1,"turnId":"turn-1","active":true,"supported":true}`)
	wire.setResult("session/steer", `{"version":1,"requestId":"direct-1","turnId":"turn-1","status":"accepted"}`)
	reg := newRevisionWireRegistry(t, wire)
	ctx, cancel := testContext(t)
	defer cancel()

	if err := reg.Steer(ctx, "s-1", "prefer the smaller diff"); err != nil {
		t.Fatal(err)
	}
	queued := wire.only("session/steer")
	if len(queued) != 1 || queued[0].params != `{"sessionId":"s-1","text":"prefer the smaller diff"}` {
		t.Fatalf("direct steer params = %+v", queued)
	}
	if _, ok := reg.revisionReceipt("s-1", "direct-1"); ok {
		t.Fatal("a queued steer was recorded as a delivery receipt")
	}

	// Send now is the explicit request, with its own identity and delivery.
	state, err := reg.RevisionSteeringStatus(ctx, "s-1")
	if err != nil || !state.Active || state.TurnID != "turn-1" {
		t.Fatalf("steering status = %+v, %v", state, err)
	}
	receipt, err := reg.RevisionSendNow(ctx, "s-1", state.TurnID, "direct-2", "approved revision")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RequestID != "direct-1" || receipt.Status != "accepted" {
		t.Fatalf("send now receipt = %+v", receipt)
	}
	sent := wire.only("session/steer")
	if len(sent) != 2 {
		t.Fatalf("session/steer frames = %+v", sent)
	}
	params := sent[1].params
	for _, want := range []string{`"delivery":"now"`, `"turnId":"turn-1"`, `"requestId":"direct-2"`} {
		if !strings.Contains(params, want) {
			t.Fatalf("send now params %s missing %s", params, want)
		}
	}
}
