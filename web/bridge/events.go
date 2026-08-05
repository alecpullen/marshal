package bridge

import (
	"encoding/json"
	"sync"
	"time"
)

// ringCap is the per-session event ring capacity. Events older than the
// last ringCap are evicted; clients reconnecting with a Last-Event-ID
// older than the oldest retained event get a replay_overflow nudge.
const ringCap = 500

// subBuf is the per-subscriber channel buffer. A subscriber that does
// not drain fast enough loses events (lagged) and receives a
// replay_overflow nudge; the ACP read loop is never blocked.
const subBuf = 64

// overflowNudgePayload is the synthetic event delivered to lagging
// subscribers and to SSE clients whose Last-Event-ID has fallen out of
// the ring. It tells the SPA to refetch state via the load endpoint.
var overflowNudgePayload = json.RawMessage(`{"type":"replay_overflow"}`)

// Event is one entry in a session's event log. ID is monotonically
// increasing per session, starting at 1. Synthetic events (the
// replay_overflow nudge) have ID 0 and are not stored in the ring.
type Event struct {
	ID        int64
	SessionID string
	Data      json.RawMessage
}

// subscriber is one live event consumer for a session.
type subscriber struct {
	sessionID string
	ch        chan Event
	lagged    bool
}

// sessionLog is the per-session ring buffer and id counter.
type sessionLog struct {
	nextID int64 // id assigned to the next appended event
	head   int   // ring index of the oldest event
	count  int
	buf    [ringCap]Event
}

// EventLog is the bridge's event bus: a per-session ring of recent
// events plus a subscriber registry for live fan-out. All ACP
// notifications and bridge-originated events for a session are appended
// here and broadcast to that session's subscribers.
//
// Events appended with an empty session id — permission requests carry
// no sessionId on the ACP wire — are stored in the "" ring and
// broadcast to EVERY session's subscribers, so the SPA receives
// cross-session permission prompts on whichever SSE stream it holds.
// They are not addressable via ServeSSE replay (which requires a
// non-empty sessionId); only the live broadcast reaches clients.
type EventLog struct {
	// HeartbeatInterval is the SSE keepalive interval used by ServeSSE.
	// Defaults to 25s; tests may shorten it.
	HeartbeatInterval time.Duration

	mu       sync.Mutex
	sessions map[string]*sessionLog
	subs     map[*subscriber]struct{}
}

// NewEventLog returns an EventLog with the default heartbeat interval.
func NewEventLog() *EventLog {
	return &EventLog{
		HeartbeatInterval: 25 * time.Second,
		sessions:          make(map[string]*sessionLog),
		subs:              make(map[*subscriber]struct{}),
	}
}

// Append stores payload in the session's ring and broadcasts it to the
// session's subscribers. It never blocks: lagging subscribers drop the
// event and are nudged. Returns the assigned event id.
func (l *EventLog) Append(sessionID string, payload any) (int64, error) {
	data, err := marshalPayload(payload)
	if err != nil {
		return 0, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	sl := l.sessions[sessionID]
	if sl == nil {
		sl = &sessionLog{nextID: 1}
		l.sessions[sessionID] = sl
	}
	ev := Event{ID: sl.nextID, SessionID: sessionID, Data: data}
	sl.nextID++
	if sl.count < ringCap {
		sl.buf[(sl.head+sl.count)%ringCap] = ev
		sl.count++
	} else {
		sl.buf[sl.head] = ev
		sl.head = (sl.head + 1) % ringCap
	}
	for s := range l.subs {
		if sessionID != "" && s.sessionID != sessionID {
			continue
		}
		s.deliver(ev)
	}
	return ev.ID, nil
}

// Replay returns the retained events with ID > afterID for a session.
// overflowed is true when afterID is older than the oldest retained
// event, i.e. events the client missed have been evicted. An afterID
// <= 0 means "no replay" (fresh connect) and returns nothing.
func (l *EventLog) Replay(sessionID string, afterID int64) ([]Event, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if afterID <= 0 {
		return nil, false
	}
	sl := l.sessions[sessionID]
	if sl == nil || sl.count == 0 {
		// No retained history but the client claims a position: the log
		// was lost (bridge restart) or the client is badly stale, so
		// signal overflow to force a state refetch rather than leave it
		// silently stuck.
		return nil, true
	}
	if afterID >= sl.nextID-1 {
		return nil, false
	}
	oldest := sl.nextID - int64(sl.count)
	var out []Event
	for i := 0; i < sl.count; i++ {
		ev := sl.buf[(sl.head+i)%ringCap]
		if ev.ID > afterID {
			out = append(out, ev)
		}
	}
	return out, afterID < oldest
}

// Tail returns the retained events for a session in id order, oldest
// first. Unlike Replay it is a fresh read with no cursor semantics and
// no overflow signal: the caller gets whatever the ring still holds.
// The HTTP load endpoint uses it to hand the SPA enough history to
// render current state without an SSE reconnect.
func (l *EventLog) Tail(sessionID string) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	sl := l.sessions[sessionID]
	if sl == nil || sl.count == 0 {
		return nil
	}
	out := make([]Event, 0, sl.count)
	for i := 0; i < sl.count; i++ {
		out = append(out, sl.buf[(sl.head+i)%ringCap])
	}
	return out
}

// Subscribe registers a live consumer for a session and returns its
// channel plus an unsubscribe function. The returned func is idempotent
// and closes the channel.
func (l *EventLog) Subscribe(sessionID string) (<-chan Event, func()) {
	s := &subscriber{sessionID: sessionID, ch: make(chan Event, subBuf)}
	l.mu.Lock()
	l.subs[s] = struct{}{}
	l.mu.Unlock()
	var once sync.Once
	unsub := func() {
		once.Do(func() {
			l.mu.Lock()
			delete(l.subs, s)
			close(s.ch)
			l.mu.Unlock()
		})
	}
	return s.ch, unsub
}

// deliver performs a non-blocking send to one subscriber. When the
// subscriber's buffer is full the event is dropped and the subscriber
// is marked lagged; the next delivery attempt first tries to push a
// replay_overflow nudge so the client knows to refetch state.
func (s *subscriber) deliver(ev Event) {
	if s.lagged {
		nudge := Event{ID: 0, SessionID: s.sessionID, Data: overflowNudgePayload}
		select {
		case s.ch <- nudge:
			s.lagged = false
		default:
		}
	}
	select {
	case s.ch <- ev:
	default:
		s.lagged = true
	}
}

func marshalPayload(payload any) (json.RawMessage, error) {
	if raw, ok := payload.(json.RawMessage); ok {
		return raw, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Attach wires an EventLog onto a Child and Registry: every inbound ACP
// notification is appended to the notifying session's log (as a
// {method, params} envelope), and every registry-emitted event
// (bridge_restarted, permission_request, question_request, turn_end)
// is appended via the registry's OnEvent hook. Existing callbacks are
// chained, not replaced.
//
// Attach must run before child.Start (or at least before any traffic):
// the hook fields are read unsynchronized by the child's read loop, so
// installing them concurrently with a running child is a data race.
// NewRegistry has the same constraint — build the whole stack, then
// Start.
func Attach(l *EventLog, child *Child, reg *Registry) {
	prevNotify := child.OnNotification
	child.OnNotification = func(method string, params json.RawMessage) {
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(params, &p)
		envelope, err := json.Marshal(map[string]any{"method": method, "params": params})
		if err == nil {
			_, _ = l.Append(p.SessionID, json.RawMessage(envelope))
		}
		if prevNotify != nil {
			prevNotify(method, params)
		}
	}
	prevOnEvent := reg.OnEvent
	reg.OnEvent = func(sessionID string, payload any) {
		_, _ = l.Append(sessionID, payload)
		if prevOnEvent != nil {
			prevOnEvent(sessionID, payload)
		}
	}
}
