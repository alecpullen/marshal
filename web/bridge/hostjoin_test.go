package bridge

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// joinerAnswer is the handler a test's joiner serves: anything that reaches it
// answers 200 "joined", so a request the relay sent down a joined connection can
// be told apart from one that never left.
func joinerAnswer() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "joined")
	})
}

// joinerFor builds a joiner onto ts for name with secret, serving h, with the
// backoff shortened so that a test watching it fail does not wait seconds.
func joinerFor(t *testing.T, ts *httptest.Server, name, secret string, h http.Handler) *Joiner {
	t.Helper()
	j, err := NewJoiner(JoinConfig{URL: ts.URL, Name: name, Secret: secret}, h)
	if err != nil {
		t.Fatalf("NewJoiner: %v", err)
	}
	j.backoffMin = 5 * time.Millisecond
	j.backoffMax = 20 * time.Millisecond
	return j
}

// joinerRun starts j and stops it when the test ends, failing if Run answers an
// error or never answers at all.
func joinerRun(t *testing.T, j *Joiner) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- j.Run(ctx) }()
	t.Cleanup(func() {
		// Closing the listener is what stops Run, which is the shutdown the
		// bridge's own process performs (D13).
		_ = j.listener.Close()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("Run did not stop")
		}
	})
}

// joinerAsk takes one of the relay's queued connections for name and sends a
// request down it, which is what makes the joiner count it consumed (decision
// 5). It answers the status the joiner's handler gave. The taken connection is
// closed when the test ends, so a caller may keep it open.
func joinerAsk(t *testing.T, relay *HostRelay, name string) int {
	t.Helper()
	conn, err := relay.hostFor(name).take(context.Background())
	if err != nil {
		t.Fatalf("take a joined connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	req, err := http.NewRequest(http.MethodGet, "http://"+name+".relay.invalid/api/agents", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := req.Write(conn); err != nil {
		t.Fatalf("write the request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	res, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

// joinerSpares is the joined connections j holds now.
func joinerSpares(j *Joiner) []*joinedConn {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*joinedConn, 0, len(j.conns))
	for c := range j.conns {
		out = append(out, c)
	}
	return out
}

// joinerKeepNoSpares stops j dialling replacements. The process it stands for
// has restarted, and a test that keeps it running must not let two live
// instances take a name from each other, which no restart leaves behind.
func joinerKeepNoSpares(j *Joiner) {
	j.mu.Lock()
	j.spare = 0
	j.mu.Unlock()
}

// joinerConnClosed reports whether the relay has ended a joined connection. A
// spare carries no traffic, so its socket is readable only when it ends; the
// read goes to the socket rather than through the wrapper, so that it cannot
// count as the connection being consumed.
func joinerConnClosed(c *joinedConn) bool {
	_ = c.Conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	defer func() { _ = c.Conn.SetReadDeadline(time.Time{}) }()
	_, err := c.Conn.Read(make([]byte, 1))
	var netErr net.Error
	return err != nil && (!errors.As(err, &netErr) || !netErr.Timeout())
}

// TestAJoinerKeepsTwoSpareConnections is D14: a joiner holds two idle joined
// connections, and no more until the relay sends a request down one.
func TestAJoinerKeepsTwoSpareConnections(t *testing.T) {
	_, relay, ts := relayServer(t)
	j := joinerFor(t, ts, "alpha", "alpha-secret", joinerAnswer())
	joinerRun(t, j)

	waitFor(t, 5*time.Second, "the two spare connections to join", func() bool {
		return queuedCount(relay.hostFor("alpha")) == 2
	})
	// It stays at two: a spare is replaced when the relay takes one and not when
	// it joins, so a joiner that dials on acceptance grows to its cap instead.
	time.Sleep(200 * time.Millisecond)
	if got := queuedCount(relay.hostFor("alpha")); got != 2 {
		t.Fatalf("%d spare connections joined, want 2", got)
	}
	if got := j.Report(); !got.Connected {
		t.Fatalf("report = %+v, want it connected", got)
	}
}

// TestTheSparesOutliveAnIdleWait is what the plan 3 run by hand found: a spare
// connection is idle until the relay sends a request down it, which may be
// minutes later. A joiner whose server carries a header deadline closes every
// spare when the deadline expires and redials for ever, and the relay's queue
// fills with connections it has already been sent a FIN on — so a forward
// takes a corpse and answers 502.
func TestTheSparesOutliveAnIdleWait(t *testing.T) {
	_, relay, ts := relayServer(t)
	j := joinerFor(t, ts, "alpha", "alpha-secret", joinerAnswer())
	joinerRun(t, j)

	waitFor(t, 5*time.Second, "the two spare connections to join", func() bool {
		return queuedCount(relay.hostFor("alpha")) == 2
	})
	before := joinerSpares(j)
	time.Sleep(1500 * time.Millisecond)
	after := joinerSpares(j)
	if len(after) != len(before) {
		t.Fatalf("%d joined connections held after an idle wait, want the same %d", len(after), len(before))
	}
	for _, c := range after {
		found := false
		for _, was := range before {
			if c == was {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("a joined connection was replaced while idle; nothing was sent down either one")
		}
	}
	if got := queuedCount(relay.hostFor("alpha")); got != 2 {
		t.Fatalf("%d spare connections queued after an idle wait, want 2", got)
	}
	if got := j.openCount(); got != 2 {
		t.Fatalf("%d joined connections open after an idle wait, want 2", got)
	}
	// The deadline itself is the bug: no interval is long enough, because a
	// spare's first request has no bound.
	if got := j.srv.ReadHeaderTimeout; got != 0 {
		t.Fatalf("the joined server's ReadHeaderTimeout = %s, want none: it closes every idle spare", got)
	}
}

// TestAConsumedConnectionIsReplaced is decision 5: the connection that carries
// the relay's first request is replaced, while it is still open.
func TestAConsumedConnectionIsReplaced(t *testing.T) {
	_, relay, ts := relayServer(t)
	j := joinerFor(t, ts, "alpha", "alpha-secret", joinerAnswer())
	joinerRun(t, j)

	waitFor(t, 5*time.Second, "the two spare connections to join", func() bool {
		return queuedCount(relay.hostFor("alpha")) == 2
	})
	if status := joinerAsk(t, relay, "alpha"); status != http.StatusOK {
		t.Fatalf("a request over a joined connection = %d, want 200", status)
	}
	// The request consumed one connection, so the joiner owes one replacement:
	// the queue is back to two while the taken connection is still open.
	waitFor(t, 5*time.Second, "the consumed connection's replacement", func() bool {
		return queuedCount(relay.hostFor("alpha")) == 2
	})
	if got := j.openCount(); got != 3 {
		t.Fatalf("open connections = %d, want the two spares and the one in use", got)
	}
}

// TestThePoolStopsAtItsCap is D14's cap, set to 3 here: a consumed connection is
// replaced while the open count is under the cap, and the pool then stops.
func TestThePoolStopsAtItsCap(t *testing.T) {
	_, relay, ts := relayServer(t)
	j := joinerFor(t, ts, "alpha", "alpha-secret", joinerAnswer())
	j.cap = 3
	joinerRun(t, j)

	waitFor(t, 5*time.Second, "the two spare connections to join", func() bool {
		return queuedCount(relay.hostFor("alpha")) == 2
	})
	// Take and use each spare in turn, which is what dials a replacement. The
	// third connection is the last this pool may open.
	for i := 0; i < 3; i++ {
		if got := queuedCount(relay.hostFor("alpha")); got == 0 {
			t.Fatalf("after %d consumed connections the joiner has none spare", i)
		}
		if status := joinerAsk(t, relay, "alpha"); status != http.StatusOK {
			t.Fatalf("request %d over a joined connection = %d, want 200", i, status)
		}
		time.Sleep(100 * time.Millisecond)
		if got := j.openCount(); got > 3 {
			t.Fatalf("%d joined connections open, want no more than 3", got)
		}
	}
	if got := j.openCount(); got != 3 {
		t.Fatalf("open connections = %d, want the pool to have stopped at its cap of 3", got)
	}
	if got := queuedCount(relay.hostFor("alpha")); got != 0 {
		t.Fatalf("%d spare connections, want none: the pool is at its cap", got)
	}
}

// TestARefusedJoinBacksOffAndReports is D14 and D17: a 401 is the join's last
// error, the joiner holds nothing, and it waits the maximum before trying again.
func TestARefusedJoinBacksOffAndReports(t *testing.T) {
	s, _ := hostRelayServer(t)
	var joins atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == relayJoinPath {
			joins.Add(1)
		}
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	j := joinerFor(t, ts, "alpha", "wrong-secret", joinerAnswer())
	joinerRun(t, j)

	waitFor(t, 5*time.Second, "the refusal to be reported", func() bool {
		return j.Report().LastError == "the relay refused the join for alpha"
	})
	if report := j.Report(); report.Connected {
		t.Fatalf("report = %+v, want it not connected", report)
	}
	// It waits the maximum, which is milliseconds here, and asks again.
	first := joins.Load()
	waitFor(t, 5*time.Second, "a second join attempt", func() bool {
		return joins.Load() > first
	})
}

// TestARelayedConnectionRefusesTheAttachAndTheStudioRoutes is D15: the agent
// attach and the Studio's own surface answer 404 to a relayed request, and
// everything else is what the network listener answers.
func TestARelayedConnectionRefusesTheAttachAndTheStudioRoutes(t *testing.T) {
	s := daemonTestServer(t)
	relayed := s.RelayedHandler()

	for _, path := range []string{
		"/api/agents/a-1/attach",
		"/api/studio/code",
		"/api/studio/session",
		"/api/studio/hello",
	} {
		if rec := viaHandler(t, relayed, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Fatalf("relayed %s = %d, body = %s; want 404", path, rec.Code, rec.Body.String())
		}
	}
	// The identity probe answers 200 on the listener it belongs to, so the 404
	// above is this handler's rather than the server's.
	if rec := viaHandler(t, s, http.MethodGet, "/api/studio/hello"); rec.Code != http.StatusOK {
		t.Fatalf("the listener's identity probe = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	// Everything else is ServeHTTP's, bearer check included.
	if rec := viaHandler(t, relayed, http.MethodGet, "/api/agents"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a relayed /api/agents without a token = %d, body = %s; want the network listener's 401", rec.Code, rec.Body.String())
	}
}

// TestTheDaemonDocumentReportsTheJoin is D17: GET /api/daemon carries the join
// the bridge holds.
func TestTheDaemonDocumentReportsTheJoin(t *testing.T) {
	s := daemonTestServer(t)
	_, _, ts := relayServer(t)
	j := joinerFor(t, ts, "alpha", "alpha-secret", joinerAnswer())
	s.SetJoiner(j)
	joinerRun(t, j)

	waitFor(t, 5*time.Second, "the join to report a connection", func() bool {
		return j.Report().Connected
	})
	rec := viaHandler(t, s.SocketHandler(), http.MethodGet, "/api/daemon")
	if rec.Code != http.StatusOK {
		t.Fatalf("socket /api/daemon = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got DaemonInfo
	decodeBody(t, rec, &got)
	if got.Join == nil {
		t.Fatalf("daemon info = %+v, want the join reported", got)
	}
	if got.Join.URL != ts.URL || got.Join.Name != "alpha" || !got.Join.Connected || got.Join.LastError != "" {
		t.Fatalf("join = %+v, want %s as alpha, connected", *got.Join, ts.URL)
	}
}

// TestARestartedJoinerTakesItsNameBack is D14: a second process with the same
// name and secret takes the name, and the first one's spare connections close.
func TestARestartedJoinerTakesItsNameBack(t *testing.T) {
	_, relay, ts := relayServer(t)
	first := joinerFor(t, ts, "alpha", "alpha-secret", joinerAnswer())
	joinerRun(t, first)
	waitFor(t, 5*time.Second, "the first joiner's spare connections", func() bool {
		return queuedCount(relay.hostFor("alpha")) == 2
	})
	spares := joinerSpares(first)
	if len(spares) != 2 {
		t.Fatalf("the first joiner holds %d connections, want 2", len(spares))
	}
	joinerKeepNoSpares(first)

	second := joinerFor(t, ts, "alpha", "alpha-secret", joinerAnswer())
	joinerRun(t, second)

	waitFor(t, 5*time.Second, "the restarted joiner to take the name", func() bool {
		return heldInstance(relay.hostFor("alpha")) == second.instance
	})
	for _, c := range spares {
		waitFor(t, 5*time.Second, "the superseded connection to close", func() bool {
			return joinerConnClosed(c)
		})
	}
	// The restarted instance holds the name with spares of its own.
	waitFor(t, 5*time.Second, "the restarted joiner's spare connections", func() bool {
		return queuedCount(relay.hostFor("alpha")) == 2
	})
}

// TestAStalledJoinDoesNotWedge is the branch review's finding: the pool is one
// goroutine, so a relay that accepts the connection and then says nothing must
// fail that exchange within its bound. Without one, the pool sits in the read
// for ever — no retry, no report, and Run never stops, so the bridge cannot
// shut down either.
func TestAStalledJoinDoesNotWedge(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Accepted and never answered: a relay that stalls after the
			// TCP handshake.
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			_ = conn.Close()
		}
	})

	j, err := NewJoiner(JoinConfig{
		URL:    "http://" + ln.Addr().String(),
		Name:   "alpha",
		Secret: "alpha-secret",
	}, joinerAnswer())
	if err != nil {
		t.Fatalf("NewJoiner: %v", err)
	}
	j.exchange = 150 * time.Millisecond
	j.backoffMin = 10 * time.Millisecond
	j.backoffMax = 20 * time.Millisecond
	joinerRun(t, j)

	// The exchange times out, the report says why, and the pool keeps trying
	// instead of holding the connection for ever. joinerRun's cleanup is what
	// proves Run still returns.
	waitFor(t, 5*time.Second, "the stalled join to be reported", func() bool {
		return j.Report().LastError != ""
	})
}
