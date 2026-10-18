package acp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"marshal/internal/pubsub"
	"marshal/internal/viewmodel"
	"marshal/internal/watch"
)

// watchOwnerStudio tags watches registered through ACP so they are never
// confused with the parent agent's own or a subagent's.
const watchOwnerStudio = "studio"

// watchLiveCheck is how often a forwarder checks that its session still exists.
const watchLiveCheck = time.Second

// WatchManagerACP serves session/watch_list, session/watch_start and
// session/watch_stop, and forwards watch events as session/update
// {kind:"watch"}.
type WatchManagerACP struct {
	lookup func(sessionID string) (*watch.Manager, *pubsub.Broker[watch.Event], bool)
	notify NotifyFunc

	mu         sync.Mutex
	forwarders map[string]context.CancelFunc
}

// NewWatchManagerACP builds the handler set. lookup resolves a session's
// current watch manager and event broker.
func NewWatchManagerACP(lookup func(string) (*watch.Manager, *pubsub.Broker[watch.Event], bool), notify NotifyFunc) *WatchManagerACP {
	return &WatchManagerACP{lookup: lookup, notify: notify, forwarders: map[string]context.CancelFunc{}}
}

// WatchSamplePoint is one sample in a watch's history; At is Unix ms.
type WatchSamplePoint struct {
	At      int64   `json:"at"`
	Value   float64 `json:"value"`
	Tripped bool    `json:"tripped"`
}

// WatchInfoWire is a watch as clients see it. Times are Unix milliseconds.
type WatchInfoWire struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Kind        string             `json:"kind"`
	State       string             `json:"state"`
	Condition   string             `json:"condition,omitempty"`
	Mode        string             `json:"mode"`
	IntervalMs  int64              `json:"intervalMs"`
	Owner       string             `json:"owner,omitempty"`
	FireCount   int                `json:"fireCount"`
	LastSample  string             `json:"lastSample,omitempty"`
	LastError   string             `json:"lastError,omitempty"`
	CreatedAt   int64              `json:"createdAt"`
	LastFiredAt int64              `json:"lastFiredAt,omitempty"`
	Samples     []WatchSamplePoint `json:"samples"`
}

func watchInfoToWire(i watch.Info) WatchInfoWire {
	w := WatchInfoWire{
		ID: i.ID, Name: i.Name, Kind: string(i.Kind), State: string(i.State),
		Condition: i.Condition, Mode: string(i.Mode), IntervalMs: i.Interval.Milliseconds(),
		Owner: i.Owner, FireCount: i.FireCount, LastSample: i.LastSample, LastError: i.LastError,
		CreatedAt: unixMillis(i.CreatedAt), LastFiredAt: unixMillis(i.LastFiredAt),
		Samples: make([]WatchSamplePoint, 0, len(i.Samples)),
	}
	for _, s := range i.Samples {
		w.Samples = append(w.Samples, WatchSamplePoint{At: unixMillis(s.At), Value: s.Value, Tripped: s.Tripped})
	}
	return w
}

// WatchSpecWire is the session/watch_start spec.
type WatchSpecWire struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Command    string `json:"command"`
	JobID      string `json:"jobId"`
	Path       string `json:"path"`
	Condition  string `json:"condition"`
	Mode       string `json:"mode"`
	Notify     *bool  `json:"notify"`
	Resume     bool   `json:"resume"`
	IntervalMs int64  `json:"intervalMs"`
	Owner      string `json:"owner"`
}

func (m *WatchManagerACP) manager(sessionID string) (*watch.Manager, *pubsub.Broker[watch.Event], error) {
	if sessionID == "" {
		return nil, nil, invalidParamsError("sessionId is required")
	}
	wm, broker, ok := m.lookup(sessionID)
	if !ok {
		return nil, nil, serverErrorf("unknown session: %s", sessionID)
	}
	if wm == nil {
		return nil, nil, serverErrorf("session %s has no watch manager", sessionID)
	}
	return wm, broker, nil
}

// List handles session/watch_list.
func (m *WatchManagerACP) List(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionIDParams
	if err := decodeParams(params, &p, "session/watch_list"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	wm, broker, err := m.manager(p.SessionID)
	if err != nil {
		return nil, err
	}
	m.ensureForwarder(p.SessionID, broker)
	infos := wm.List()
	out := make([]WatchInfoWire, 0, len(infos))
	for _, i := range infos {
		out = append(out, watchInfoToWire(i))
	}
	return map[string]any{"watches": out}, nil
}

// Start handles session/watch_start.
func (m *WatchManagerACP) Start(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string        `json:"sessionId"`
		Spec      WatchSpecWire `json:"spec"`
	}
	if err := decodeParams(params, &p, "session/watch_start"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	wm, broker, err := m.manager(p.SessionID)
	if err != nil {
		return nil, err
	}
	m.ensureForwarder(p.SessionID, broker)
	s := p.Spec
	spec := watch.Spec{
		Name: s.Name, Kind: watch.Kind(s.Kind), Command: s.Command, JobID: s.JobID,
		Path: s.Path, Condition: s.Condition, Mode: watch.Mode(s.Mode),
		Notify: s.Notify, Resume: s.Resume,
		Interval: time.Duration(s.IntervalMs) * time.Millisecond, Owner: s.Owner,
	}
	if spec.Owner == "" {
		spec.Owner = watchOwnerStudio
	}
	id, note, err := wm.Start(spec)
	if err != nil {
		return nil, invalidParamsError("%v", err)
	}
	res := map[string]any{"id": id, "name": spec.Name}
	if info, serr := wm.Status(id); serr == nil {
		res["name"] = info.Name
	}
	if note != "" {
		res["note"] = note
	}
	return res, nil
}

// Stop handles session/watch_stop.
func (m *WatchManagerACP) Stop(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
		ID        string `json:"id"`
	}
	if err := decodeParams(params, &p, "session/watch_stop"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	wm, _, err := m.manager(p.SessionID)
	if err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, invalidParamsError("session/watch_stop requires id")
	}
	if _, err := wm.Stop(p.ID); err != nil {
		return nil, serverErrorf("%v", err)
	}
	return map[string]any{}, nil
}

// ensureForwarder subscribes once per session to its watch broker and
// forwards each event as a session/update. The subscription ends when the
// session goes away.
func (m *WatchManagerACP) ensureForwarder(sessionID string, broker *pubsub.Broker[watch.Event]) {
	if broker == nil {
		return
	}
	m.mu.Lock()
	if _, ok := m.forwarders[sessionID]; ok {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.forwarders[sessionID] = cancel
	m.mu.Unlock()

	events := broker.Subscribe(ctx)
	go func() {
		defer func() {
			cancel()
			m.mu.Lock()
			delete(m.forwarders, sessionID)
			m.mu.Unlock()
		}()
		ticker := time.NewTicker(watchLiveCheck)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				e := ev.Payload
				_ = m.notify("session/update", SessionUpdateParams{
					SessionID: sessionID,
					Update: map[string]any{
						"kind": "watch",
						"event": map[string]any{
							"watchId": e.WatchID, "name": e.Name, "kind": string(e.Kind),
							"state": string(e.State), "sample": e.Sample, "owner": e.Owner,
							"mode": string(e.Mode),
						},
					},
				})
			case <-ticker.C:
				if _, _, ok := m.lookup(sessionID); !ok {
					return
				}
			}
		}
	}()
}

// workStopReasonMaxRunes bounds the caller-supplied stop reason so one request
// cannot carry unbounded text into the runtime facade.
const workStopReasonMaxRunes = 1024

// workPageMaxLimit mirrors the facade's page bound (viewmodel.PageWork): a
// paged list request above it is rejected as invalid params before the facade
// is called.
const workPageMaxLimit = 100

// WorkManagerACP serves session/work_list, session/work_inspect,
// session/work_stop and session/work_acknowledge. It is deliberately
// independent from WatchManagerACP: it shares only the session-lookup shape,
// not the watch broker, and it holds the runtime's WorkService directly.
//
// Its lookup resolves a session's runtime as a viewmodel.WorkService directly
// from the SessionManager, without the prompt path's requirement for an
// available Runner: a runner-less session (a session still building its model,
// or a headless one) can still report and manage shared work.
type WorkManagerACP struct {
	lookup func(string) (viewmodel.WorkService, bool)
	notify NotifyFunc

	mu         sync.Mutex
	forwarders map[string]*workForwarder
	closed     bool
}

// workForwarder tracks one session's work-snapshot subscription. Task 22 owns
// starting them; Task 21 only pins the shape and never spawns one.
type workForwarder struct {
	cancel context.CancelFunc
	done   chan struct{}
	epoch  string
}

// NewWorkManagerACP builds the handler set. lookup resolves a session's Work
// facade; notify is the host-level sink for future work_state updates.
func NewWorkManagerACP(lookup func(string) (viewmodel.WorkService, bool), notify NotifyFunc) *WorkManagerACP {
	return &WorkManagerACP{lookup: lookup, notify: notify, forwarders: map[string]*workForwarder{}}
}

// service resolves the Work facade for a session. A missing session id is
// invalid params, an unknown session is a server error, and a lookup that
// reports success with a nil facade fails closed rather than dereferencing it.
func (m *WorkManagerACP) service(sessionID string) (viewmodel.WorkService, error) {
	if sessionID == "" {
		return nil, invalidParamsError("sessionId is required")
	}
	svc, ok := m.lookup(sessionID)
	if !ok {
		return nil, serverErrorf("unknown session: %s", sessionID)
	}
	if svc == nil {
		return nil, serverErrorf("session %s has no work service", sessionID)
	}
	return svc, nil
}

// workFacadeError maps a facade error onto the JSON-RPC surface without ever
// reinterpreting a failure as success. A stale cursor or target is an explicit
// invalid-params error (the caller's page or target no longer applies); an
// unavailable action and every other failure are server errors.
func workFacadeError(err error) error {
	switch {
	case errors.Is(err, viewmodel.ErrWorkStaleCursor), errors.Is(err, viewmodel.ErrWorkStaleTarget):
		return invalidParamsError("%v", err)
	case errors.Is(err, viewmodel.ErrWorkUnsupported):
		return serverErrorf("%v", err)
	default:
		return serverErrorf("%v", err)
	}
}

// List handles session/work_list. A missing or "live" view returns the live
// snapshot; "attention" and "history" delegate to the facade and return one
// page. The live view rejects history arguments rather than silently ignoring
// them.
func (m *WorkManagerACP) List(ctx context.Context, params json.RawMessage) (any, error) {
	var p workListParams
	if err := decodeParams(params, &p, "session/work_list"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	svc, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	switch p.View {
	case "", "live":
		if p.Query != "" || p.Cursor != "" || p.Limit != 0 {
			return nil, invalidParamsError("session/work_list: the live view does not accept query, cursor or limit")
		}
		return workWire[WorkSnapshotWire](svc.WorkSnapshot())
	case "attention", "history":
		if p.Limit < 0 || p.Limit > workPageMaxLimit {
			return nil, invalidParamsError("session/work_list: limit must be between 1 and %d", workPageMaxLimit)
		}
		page, err := svc.ListWork(ctx, viewmodel.WorkQuery{
			ConversationID: p.SessionID,
			View:           p.View,
			Query:          p.Query,
			Cursor:         p.Cursor,
			Limit:          p.Limit,
		})
		if err != nil {
			return nil, workFacadeError(err)
		}
		return workWire[WorkPageWire](page)
	default:
		return nil, invalidParamsError("session/work_list: unknown view %q", p.View)
	}
}

// Inspect handles session/work_inspect. It requires the exact task key and
// returns the bounded detail for that item.
func (m *WorkManagerACP) Inspect(ctx context.Context, params json.RawMessage) (any, error) {
	var p workTargetParams
	if err := decodeParams(params, &p, "session/work_inspect"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	svc, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if p.TaskKey == "" {
		return nil, invalidParamsError("session/work_inspect requires taskKey")
	}
	detail, err := svc.InspectWork(ctx, viewmodel.WorkTarget{ConversationID: p.SessionID, TaskKey: p.TaskKey}, p.Raw)
	if err != nil {
		return nil, workFacadeError(err)
	}
	return workWire[WorkDetailWire](detail)
}

// Stop handles session/work_stop. It requires the exact task key and passes the
// captured conversation plus the bounded reason to the facade; it carries no
// command or permission payload and cannot grant system or edit access.
func (m *WorkManagerACP) Stop(ctx context.Context, params json.RawMessage) (any, error) {
	var p workTargetParams
	if err := decodeParams(params, &p, "session/work_stop"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	svc, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if p.TaskKey == "" {
		return nil, invalidParamsError("session/work_stop requires taskKey")
	}
	if utf8.RuneCountInString(p.Reason) > workStopReasonMaxRunes {
		return nil, invalidParamsError("session/work_stop: reason must be at most %d runes", workStopReasonMaxRunes)
	}
	result, err := svc.StopWork(ctx, viewmodel.WorkTarget{ConversationID: p.SessionID, TaskKey: p.TaskKey}, p.Reason)
	if err != nil {
		return nil, workFacadeError(err)
	}
	return workWire[WorkStopWire](result)
}

// Acknowledge handles session/work_acknowledge. It requires the exact task key
// and the terminal result revision; it reports success only after the facade
// accepts the acknowledgment.
func (m *WorkManagerACP) Acknowledge(ctx context.Context, params json.RawMessage) (any, error) {
	var p workTargetParams
	if err := decodeParams(params, &p, "session/work_acknowledge"); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	svc, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if p.TaskKey == "" {
		return nil, invalidParamsError("session/work_acknowledge requires taskKey")
	}
	if p.ResultRevision == "" {
		return nil, invalidParamsError("session/work_acknowledge requires terminalResultRevision")
	}
	if err := svc.AcknowledgeWork(ctx, viewmodel.WorkAck{
		WorkTarget:     viewmodel.WorkTarget{ConversationID: p.SessionID, TaskKey: p.TaskKey},
		ResultRevision: p.ResultRevision,
	}); err != nil {
		return nil, workFacadeError(err)
	}
	return map[string]any{"acknowledged": true}, nil
}

// Close stops every forwarder and marks the controller closed. It is
// idempotent: a second call is a no-op, and with no forwarders started (Task
// 21) it only sets the flag.
func (m *WorkManagerACP) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	for key, f := range m.forwarders {
		f.cancel()
		delete(m.forwarders, key)
	}
}
