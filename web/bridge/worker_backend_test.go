package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeWorkerBackend is an in-process WorkerBackend: no child process, no
// endpoint, no package-level factory. Each instance owns the sinks it was
// built with, so two fakes in one test observe only their own traffic.
type fakeWorkerBackend struct {
	events   workerEventSink
	requests workerRequestSink

	mu       sync.Mutex
	seq      uint64
	nextID   int
	sessions map[string]*fakeWorkerSession
}

type fakeWorkerSession struct {
	spec      WorkerSessionSpec
	caps      WorkerCapabilities
	stop      string
	uncertain bool
	requests  map[string]*fakeWorkerRequest
}

type fakeWorkerRequest struct {
	method  string
	params  json.RawMessage
	result  json.RawMessage
	settled bool
}

func newFakeWorkerBackend(events workerEventSink, requests workerRequestSink) *fakeWorkerBackend {
	return &fakeWorkerBackend{
		events:   events,
		requests: requests,
		sessions: map[string]*fakeWorkerSession{},
	}
}

// fakeCapsForMode is the fake's capability story: a session's capabilities come
// from its mode, and every call returns a fresh slice so one session can never
// mutate another's.
func fakeCapsForMode(mode string) WorkerCapabilities {
	switch mode {
	case "act":
		return WorkerCapabilities{
			Prompt:             true,
			Cancel:             true,
			PermissionRequests: true,
			WorkspaceEdits:     true,
			Modes:              []string{"act", "ask"},
		}
	case "ask":
		return WorkerCapabilities{
			Prompt:             true,
			Cancel:             true,
			PermissionRequests: true,
			Modes:              []string{"ask"},
		}
	default:
		return WorkerCapabilities{}
	}
}

func (b *fakeWorkerBackend) Start(ctx context.Context, spec WorkerSessionSpec) (string, WorkerCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return "", WorkerCapabilities{}, err
	}
	caps := fakeCapsForMode(spec.Mode)
	b.mu.Lock()
	b.nextID++
	id := fmt.Sprintf("fake-session-%d", b.nextID)
	b.sessions[id] = &fakeWorkerSession{
		spec:     spec,
		caps:     caps,
		stop:     "end_turn",
		requests: map[string]*fakeWorkerRequest{},
	}
	b.mu.Unlock()
	b.emit(id, "session.started", map[string]any{"workerId": spec.WorkerID, "mode": spec.Mode})
	return id, caps, nil
}

func (b *fakeWorkerBackend) Prompt(ctx context.Context, sessionID, text string) (WorkerTurnResult, error) {
	if err := ctx.Err(); err != nil {
		return WorkerTurnResult{}, err
	}
	b.mu.Lock()
	s := b.sessions[sessionID]
	if s == nil {
		b.mu.Unlock()
		return WorkerTurnResult{}, fmt.Errorf("fake worker: unknown session %q", sessionID)
	}
	if !s.caps.Prompt {
		b.mu.Unlock()
		return WorkerTurnResult{}, ErrWorkerUnsupported
	}
	uncertain, stop := s.uncertain, s.stop
	b.mu.Unlock()

	b.emit(sessionID, "turn.started", map[string]any{"prompt": text})
	if uncertain {
		// The turn was dispatched and its outcome never came back. Reporting a
		// zero result with a nil error would read as a finished turn.
		b.emit(sessionID, "turn.uncertain", map[string]any{})
		return WorkerTurnResult{}, ErrWorkerUncertain
	}
	b.emit(sessionID, "turn.finished", map[string]any{"stopReason": stop})
	return WorkerTurnResult{StopReason: stop}, nil
}

func (b *fakeWorkerBackend) Cancel(ctx context.Context, sessionID string) error {
	b.mu.Lock()
	s := b.sessions[sessionID]
	if s == nil {
		b.mu.Unlock()
		return fmt.Errorf("fake worker: unknown session %q", sessionID)
	}
	if !s.caps.Cancel {
		b.mu.Unlock()
		return ErrWorkerUnsupported
	}
	b.mu.Unlock()
	b.emit(sessionID, "turn.cancelled", map[string]any{})
	return nil
}

func (b *fakeWorkerBackend) Close(ctx context.Context, sessionID string) error {
	b.mu.Lock()
	if _, ok := b.sessions[sessionID]; !ok {
		b.mu.Unlock()
		return fmt.Errorf("fake worker: unknown session %q", sessionID)
	}
	delete(b.sessions, sessionID)
	b.mu.Unlock()
	b.emit(sessionID, "session.closed", map[string]any{})
	return nil
}

// request dispatches one agent request through the backend's request sink and
// records the sink's answer as that request's single settlement.
func (b *fakeWorkerBackend) request(ctx context.Context, sessionID, requestID, method string, params json.RawMessage) (json.RawMessage, error) {
	b.mu.Lock()
	s := b.sessions[sessionID]
	if s == nil {
		b.mu.Unlock()
		return nil, fmt.Errorf("fake worker: unknown session %q", sessionID)
	}
	sink, caps := b.requests, s.caps
	b.mu.Unlock()

	if sink == nil || !caps.PermissionRequests {
		return nil, ErrWorkerUnsupported
	}
	if err := b.openRequest(sessionID, requestID, method, params); err != nil {
		return nil, err
	}
	b.emit(sessionID, "request.opened", map[string]any{"requestId": requestID, "method": method})

	answer, err := sink(ctx, sessionID, requestID, method, params)
	if err != nil {
		return nil, err
	}
	if err := b.settleRequest(sessionID, requestID, answer); err != nil {
		return nil, err
	}
	return answer, nil
}

// openRequest registers a request the backend wants answered. A request ID is
// created once: a duplicate is refused rather than overwriting the first.
func (b *fakeWorkerBackend) openRequest(sessionID, requestID, method string, params json.RawMessage) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[sessionID]
	if s == nil {
		return fmt.Errorf("fake worker: unknown session %q", sessionID)
	}
	if requestID == "" {
		return errors.New("fake worker: empty request id")
	}
	if _, dup := s.requests[requestID]; dup {
		return fmt.Errorf("fake worker: duplicate request id %q", requestID)
	}
	s.requests[requestID] = &fakeWorkerRequest{method: method, params: params}
	return nil
}

// settleRequest records the one answer to a request. A second answer, or an
// answer for a request that was never opened, is refused: a request settles
// exactly once, and only in this process.
func (b *fakeWorkerBackend) settleRequest(sessionID, requestID string, result json.RawMessage) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[sessionID]
	if s == nil {
		return fmt.Errorf("fake worker: unknown session %q", sessionID)
	}
	r := s.requests[requestID]
	if r == nil {
		return fmt.Errorf("fake worker: unknown request %q", requestID)
	}
	if r.settled {
		return fmt.Errorf("fake worker: request %q already settled", requestID)
	}
	r.settled = true
	r.result = result
	return nil
}

func (b *fakeWorkerBackend) emit(sessionID, kind string, data any) {
	if b.events == nil {
		return
	}
	raw, err := json.Marshal(data)
	if err != nil {
		panic(fmt.Sprintf("fake worker: marshal %q event: %v", kind, err))
	}
	b.mu.Lock()
	b.seq++
	seq := b.seq
	b.mu.Unlock()
	b.events(sessionID, seq, kind, raw)
}

// workerEvent is one notification a fake sink received.
type workerEvent struct {
	sessionID string
	sequence  uint64
	kind      string
	data      json.RawMessage
}

// eventRecorder is a test-owned sink: it is never a package global, so two
// recording backends in one test cannot share it by accident.
type eventRecorder struct {
	mu     sync.Mutex
	events []workerEvent
}

func (r *eventRecorder) sink(sessionID string, sequence uint64, kind string, data json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, workerEvent{sessionID: sessionID, sequence: sequence, kind: kind, data: data})
}

func (r *eventRecorder) all() []workerEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]workerEvent(nil), r.events...)
}

// TestWorkerCapabilitiesArePerSession pins that capabilities travel with the
// session Start returned, not with the backend: two sessions from one backend
// differ, a capability the session lacks answers ErrWorkerUnsupported, and one
// session's Modes slice never aliases another's.
func TestWorkerCapabilitiesArePerSession(t *testing.T) {
	backend := newFakeWorkerBackend(nil, nil)
	ctx := context.Background()

	actID, actCaps, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w1", Mode: "act"})
	if err != nil {
		t.Fatalf("start act session: %v", err)
	}
	askID, askCaps, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w2", Mode: "ask"})
	if err != nil {
		t.Fatalf("start ask session: %v", err)
	}
	bareID, bareCaps, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w3"})
	if err != nil {
		t.Fatalf("start bare session: %v", err)
	}
	if actID == askID || actID == bareID || askID == bareID {
		t.Fatalf("sessions share an id: %q %q %q", actID, askID, bareID)
	}

	if !actCaps.WorkspaceEdits || !actCaps.Prompt || !actCaps.Cancel {
		t.Errorf("act capabilities = %+v, want prompt, cancel and workspace edits", actCaps)
	}
	if askCaps.WorkspaceEdits {
		t.Errorf("ask capabilities = %+v, want no workspace edits", askCaps)
	}
	if bareCaps.Prompt || bareCaps.Cancel || len(bareCaps.Modes) != 0 {
		t.Errorf("bare capabilities = %+v, want nothing", bareCaps)
	}

	// The slice belongs to the session, not to the backend.
	actCaps.Modes[0] = "mutated"
	if _, again, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w4", Mode: "act"}); err != nil {
		t.Fatalf("start second act session: %v", err)
	} else if again.Modes[0] != "act" {
		t.Errorf("new session Modes = %v, want them unaffected by the first session's slice", again.Modes)
	}

	// A capability the session lacks is unsupported, not a failure.
	if _, err := backend.Prompt(ctx, bareID, "hello"); !errors.Is(err, ErrWorkerUnsupported) {
		t.Errorf("prompt on a session without Prompt = %v, want ErrWorkerUnsupported", err)
	}
	if err := backend.Cancel(ctx, askID); err != nil {
		t.Errorf("cancel on a session with Cancel = %v, want nil", err)
	}
	if err := backend.Cancel(ctx, bareID); !errors.Is(err, ErrWorkerUnsupported) {
		t.Errorf("cancel on a session without Cancel = %v, want ErrWorkerUnsupported", err)
	}
	if _, err := backend.Prompt(ctx, actID, "hello"); err != nil {
		t.Errorf("prompt on the act session = %v, want nil", err)
	}
}

// TestWorkerRequestsSettleOnce pins that a request is answered exactly once: a
// repeat settlement and a duplicate request ID are refused, not accepted.
func TestWorkerRequestsSettleOnce(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []string
	)
	want := json.RawMessage(`{"allow":true}`)
	requests := func(ctx context.Context, sessionID, requestID, method string, params json.RawMessage) (json.RawMessage, error) {
		mu.Lock()
		calls = append(calls, sessionID+"/"+requestID+"/"+method)
		mu.Unlock()
		return want, nil
	}

	backend := newFakeWorkerBackend(nil, requests)
	ctx := context.Background()
	sessionID, caps, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w1", Mode: "act"})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if !caps.PermissionRequests {
		t.Fatalf("act capabilities = %+v, want permission requests", caps)
	}

	got, err := backend.request(ctx, sessionID, "req-1", "session/request_permission", json.RawMessage(`{"tool":"shell"}`))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("request answer = %s, want %s", got, want)
	}
	mu.Lock()
	delivered := append([]string(nil), calls...)
	mu.Unlock()
	if len(delivered) != 1 || delivered[0] != sessionID+"/req-1/session/request_permission" {
		t.Fatalf("request sink calls = %v, want exactly one for req-1", delivered)
	}

	// The request is settled: answering again is refused.
	if err := backend.settleRequest(sessionID, "req-1", json.RawMessage(`{"allow":false}`)); err == nil {
		t.Error("second settlement of req-1 was accepted, want refused")
	} else if !strings.Contains(err.Error(), "already settled") {
		t.Errorf("second settlement error = %v, want an already-settled refusal", err)
	}

	// The ID is taken: re-opening it is refused rather than replacing it.
	if err := backend.openRequest(sessionID, "req-1", "session/request_permission", nil); err == nil {
		t.Error("duplicate request id was accepted, want refused")
	} else if !strings.Contains(err.Error(), "duplicate request id") {
		t.Errorf("duplicate request error = %v, want a duplicate-id refusal", err)
	}

	// An answer for a request that never opened is refused too.
	if err := backend.settleRequest(sessionID, "req-missing", want); err == nil {
		t.Error("settlement of an unknown request was accepted, want refused")
	}

	// A second, distinct request still works: refusal is per request.
	if _, err := backend.request(ctx, sessionID, "req-2", "session/request_permission", nil); err != nil {
		t.Errorf("second request: %v", err)
	}
}

// TestWorkerUnknownOutcomeIsNotSuccess pins that a dispatched turn whose
// outcome the backend never saw comes back as ErrWorkerUncertain with no
// result — never a nil error and never a stop reason a caller could journal.
func TestWorkerUnknownOutcomeIsNotSuccess(t *testing.T) {
	backend := newFakeWorkerBackend(nil, nil)
	ctx := context.Background()
	sessionID, _, err := backend.Start(ctx, WorkerSessionSpec{WorkerID: "w1", Mode: "act"})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	backend.mu.Lock()
	backend.sessions[sessionID].uncertain = true
	backend.mu.Unlock()

	for i := 0; i < 3; i++ {
		result, err := backend.Prompt(ctx, sessionID, "do the thing")
		if err == nil {
			t.Fatalf("prompt %d: err = nil, want ErrWorkerUncertain", i)
		}
		if !errors.Is(err, ErrWorkerUncertain) {
			t.Fatalf("prompt %d: err = %v, want ErrWorkerUncertain", i, err)
		}
		if errors.Is(err, ErrWorkerUnsupported) {
			t.Errorf("prompt %d: uncertain outcome also reads as unsupported", i)
		}
		if result.StopReason != "" {
			t.Errorf("prompt %d: result = %+v, want no stop reason with an uncertain outcome", i, result)
		}
	}

	// The same backend still reports a known outcome as a result.
	backend.mu.Lock()
	backend.sessions[sessionID].uncertain = false
	backend.mu.Unlock()
	result, err := backend.Prompt(ctx, sessionID, "do the thing")
	if err != nil {
		t.Fatalf("known-outcome prompt: %v", err)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("known-outcome result = %+v, want end_turn", result)
	}
}

// TestWorkerSinkIsInstanceScoped pins that a backend emits only into the sink it
// was constructed with: two instances never observe each other's events, and an
// instance built without a sink emits nowhere rather than to a neighbour.
func TestWorkerSinkIsInstanceScoped(t *testing.T) {
	var first, second eventRecorder
	backendA := newFakeWorkerBackend(first.sink, nil)
	backendB := newFakeWorkerBackend(second.sink, nil)
	silent := newFakeWorkerBackend(nil, nil)
	ctx := context.Background()

	aID, _, err := backendA.Start(ctx, WorkerSessionSpec{WorkerID: "a", Mode: "act"})
	if err != nil {
		t.Fatalf("start A session: %v", err)
	}
	bID, _, err := backendB.Start(ctx, WorkerSessionSpec{WorkerID: "b", Mode: "ask"})
	if err != nil {
		t.Fatalf("start B session: %v", err)
	}
	if _, _, err := silent.Start(ctx, WorkerSessionSpec{WorkerID: "s", Mode: "act"}); err != nil {
		t.Fatalf("start silent session: %v", err)
	}
	if _, err := backendA.Prompt(ctx, aID, "one"); err != nil {
		t.Fatalf("prompt A: %v", err)
	}
	if _, err := backendB.Prompt(ctx, bID, "two"); err != nil {
		t.Fatalf("prompt B: %v", err)
	}

	aEvents, bEvents := first.all(), second.all()
	if len(aEvents) == 0 || len(bEvents) == 0 {
		t.Fatalf("recorded events = %d and %d, want both non-empty", len(aEvents), len(bEvents))
	}
	for _, ev := range aEvents {
		if ev.sessionID != aID {
			t.Errorf("sink A saw session %q, want only %q", ev.sessionID, aID)
		}
	}
	for _, ev := range bEvents {
		if ev.sessionID != bID {
			t.Errorf("sink B saw session %q, want only %q", ev.sessionID, bID)
		}
	}

	// Sequence is ordered per backend instance, and each event carries JSON.
	var last uint64
	for _, ev := range aEvents {
		if ev.sequence <= last {
			t.Errorf("event sequence %d after %d, want strictly increasing", ev.sequence, last)
		}
		last = ev.sequence
		if !json.Valid(ev.data) {
			t.Errorf("event %q data = %s, want valid JSON", ev.kind, ev.data)
		}
	}
}
