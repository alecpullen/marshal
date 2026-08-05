package bridge

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseEvent is one parsed SSE frame.
type sseEvent struct {
	id   string
	data string
}

// readSSEEvent reads one event frame (id/data lines up to the blank
// separator). Comment lines (": ...") are skipped and reading
// continues, so heartbeats do not satisfy a test waiting for an event.
func readSSEEvent(t *testing.T, rd *bufio.Reader) sseEvent {
	t.Helper()
	var ev sseEvent
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE line: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if ev.data != "" {
				return ev
			}
		case strings.HasPrefix(line, ":"):
			// heartbeat comment: skip
		case strings.HasPrefix(line, "id: "):
			ev.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

// startSSE issues a GET against the handler and returns the response
// plus a reader positioned at the body. The caller cancels the request
// context when done.
func startSSE(t *testing.T, l *EventLog, target string) (*http.Response, *bufio.Reader, context.CancelFunc) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(l.ServeSSE))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+target, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", target, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	return resp, bufio.NewReader(resp.Body), cancel
}

func TestSSERequiresSessionID(t *testing.T) {
	l := NewEventLog()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec := httptest.NewRecorder()
	l.ServeSSE(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestSSEFreshConnectStreamsLiveEvents(t *testing.T) {
	l := NewEventLog()
	_, rd, _ := startSSE(t, l, "/api/events?sessionId=s1")

	if _, err := l.Append("s1", map[string]any{"n": 1}); err != nil {
		t.Fatalf("append: %v", err)
	}
	ev := readSSEEvent(t, rd)
	if ev.id != "1" || ev.data != `{"n":1}` {
		t.Fatalf("first event = %+v, want id 1 data {\"n\":1}", ev)
	}
}

func TestSSEReplaysAfterLastEventID(t *testing.T) {
	l := NewEventLog()
	for i := 1; i <= 3; i++ {
		if _, err := l.Append("s1", map[string]any{"n": i}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	_, rd, _ := startSSE(t, l, "/api/events?sessionId=s1&lastEventId=1")
	ev := readSSEEvent(t, rd)
	if ev.id != "2" {
		t.Fatalf("first replayed id = %q, want 2", ev.id)
	}
	ev = readSSEEvent(t, rd)
	if ev.id != "3" {
		t.Fatalf("second replayed id = %q, want 3", ev.id)
	}

	// Live events continue after the replayed window.
	if _, err := l.Append("s1", map[string]any{"n": 4}); err != nil {
		t.Fatalf("append 4: %v", err)
	}
	ev = readSSEEvent(t, rd)
	if ev.id != "4" {
		t.Fatalf("live event id = %q, want 4", ev.id)
	}
}

func TestSSEOverflowSendsNudge(t *testing.T) {
	l := NewEventLog()
	for i := 1; i <= ringCap+5; i++ {
		if _, err := l.Append("s1", map[string]any{"n": i}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// lastEventId 2 is evicted (oldest retained is 6): nudge first,
	// then the retained tail.
	_, rd, _ := startSSE(t, l, "/api/events?sessionId=s1&lastEventId=2")
	ev := readSSEEvent(t, rd)
	if ev.id != "" || ev.data != `{"type":"replay_overflow"}` {
		t.Fatalf("nudge = %+v, want no id, replay_overflow data", ev)
	}
	ev = readSSEEvent(t, rd)
	if ev.id != fmt.Sprintf("%d", 6) {
		t.Fatalf("first retained id = %q, want 6", ev.id)
	}
}

func TestSSEHeartbeat(t *testing.T) {
	l := NewEventLog()
	l.HeartbeatInterval = 20 * time.Millisecond
	_, rd, cancel := startSSE(t, l, "/api/events?sessionId=s1")
	defer cancel()

	deadline := time.After(2 * time.Second)
	got := make(chan string, 1)
	go func() {
		line, err := rd.ReadString('\n')
		if err != nil {
			got <- "err: " + err.Error()
			return
		}
		got <- line
	}()
	select {
	case line := <-got:
		if !strings.HasPrefix(line, ":") {
			t.Fatalf("first line = %q, want heartbeat comment", line)
		}
	case <-deadline:
		t.Fatal("no heartbeat within 2s")
	}
}

func TestSSEDisconnectCleansUp(t *testing.T) {
	l := NewEventLog()
	_, _, cancel := startSSE(t, l, "/api/events?sessionId=s1")
	cancel()

	// The handler should notice the cancelled context and unsubscribe.
	waitFor(t, 2*time.Second, "subscriber cleanup", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return len(l.subs) == 0
	})
}
