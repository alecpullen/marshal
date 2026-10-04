package acp

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"marshal/internal/pubsub"
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
