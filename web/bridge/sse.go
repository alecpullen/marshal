package bridge

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ServeSSE streams one session's events as Server-Sent Events on
// GET /api/events?sessionId=.... On connect, retained events after the
// client's Last-Event-ID (header, or lastEventId query parameter) are
// replayed first; when that position has fallen out of the ring a
// synthetic replay_overflow event is sent so the SPA refetches state
// via the load endpoint. Live events follow. Idle connections receive
// comment heartbeats every HeartbeatInterval (default 25s).
func (l *EventLog) ServeSSE(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "bridge: sessionId query parameter is required", http.StatusBadRequest)
		return
	}
	l.ServeSSEKey(w, r, sessionID)
}

// ServeSSEKey streams one log key, including replay and live events.
func (l *EventLog) ServeSSEKey(w http.ResponseWriter, r *http.Request, key string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "bridge: streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Subscribe before replaying so events appended during the replay
	// land on the channel; IDs at or below the replayed window are
	// dropped as duplicates when drained.
	ch, unsub := l.Subscribe(key)
	defer unsub()

	afterID := lastEventID(r)
	replay, overflowed := l.Replay(key, afterID)
	if overflowed {
		writeSSE(w, Event{ID: 0, SessionID: key, Data: overflowNudgePayload})
	}
	lastID := afterID
	for _, ev := range replay {
		writeSSE(w, ev)
		lastID = ev.ID
	}
	flusher.Flush()

	interval := l.HeartbeatInterval
	if interval <= 0 {
		interval = 25 * time.Second
	}
	hb := time.NewTicker(interval)
	defer hb.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			lastID = writeLive(w, ev, lastID)
			// Drain any buffered events before flushing, so a burst
			// costs one network flush instead of one per frame.
			for {
				select {
				case ev, ok := <-ch:
					if !ok {
						flusher.Flush()
						return
					}
					lastID = writeLive(w, ev, lastID)
				default:
					goto flushed
				}
			}
		flushed:
			flusher.Flush()
		case <-hb.C:
			fmt.Fprint(w, ": hb\n\n")
			flusher.Flush()
		}
	}
}

// lastEventID extracts the reconnect position from the Last-Event-ID
// header or the lastEventId query parameter. Zero means fresh connect.
func lastEventID(r *http.Request) int64 {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("lastEventId")
	}
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

// writeLive writes one live event unless it duplicates the replayed
// window, and returns the updated last-seen id. Synthetic events
// (ID 0) do not advance the position.
func writeLive(w io.Writer, ev Event, lastID int64) int64 {
	if ev.ID != 0 && ev.ID <= lastID {
		return lastID // duplicate from the replay window
	}
	writeSSE(w, ev)
	if ev.ID != 0 {
		return ev.ID
	}
	return lastID
}

// writeSSE writes one event frame. Synthetic events (ID 0) omit the id
// line so a native EventSource client's lastEventId is not clobbered.
func writeSSE(w io.Writer, ev Event) {
	if ev.ID != 0 {
		fmt.Fprintf(w, "id: %d\n", ev.ID)
	}
	for _, line := range bytes.Split(ev.Data, []byte("\n")) {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}
