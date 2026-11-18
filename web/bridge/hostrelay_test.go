package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// hostRelayServer is a token-protected bridge that is also a relay naming
// alpha and beta, each with a secret of its own.
func hostRelayServer(t *testing.T) (*Server, *HostRelay) {
	t.Helper()
	s := NewServer(testFleet(t), "secret")
	r, err := NewHostRelay(map[string]string{"alpha": "alpha-secret", "beta": "beta-secret"})
	if err != nil {
		t.Fatalf("NewHostRelay: %v", err)
	}
	s.SetHostRelay(r)
	return s, r
}

// relayServer is that bridge on a real socket: a join is answered with a 101
// and a hijack, which needs a connection the server can hand over.
func relayServer(t *testing.T) (*Server, *HostRelay, *httptest.Server) {
	t.Helper()
	s, r := hostRelayServer(t)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, r, ts
}

// join is one raw join the tests send: the fields the route reads, so a test
// can leave one wrong and see which answer it earns.
type join struct {
	method     string
	name       string
	instance   string
	secret     string
	upgrade    string
	connection string
}

// joinOf is the well-formed join for name, carrying secret.
func joinOf(name, secret string) join {
	return join{
		method:     http.MethodPost,
		name:       name,
		instance:   "i-" + name,
		secret:     secret,
		upgrade:    joinUpgrade,
		connection: "Upgrade",
	}
}

// joinConn is a raw client's end of one join: the connection, the reader the
// answer was read through — so a test reads the bytes after the answer in
// order — and the answer itself.
type joinConn struct {
	conn   net.Conn
	r      *bufio.Reader
	status int
	body   string
}

// dialJoin opens a raw connection to addr and sends j, answering the connection
// and the response. A caller that gets no 101 closes the connection through the
// cleanup.
func dialJoin(t *testing.T, addr string, j join) *joinConn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	req, err := http.NewRequest(j.method, "http://"+addr+relayJoinPath, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if j.upgrade != "" {
		req.Header.Set("Upgrade", j.upgrade)
	}
	if j.connection != "" {
		req.Header.Set("Connection", j.connection)
	}
	if j.name != "" {
		req.Header.Set(joinNameHeader, j.name)
	}
	if j.instance != "" {
		req.Header.Set(joinInstanceHeader, j.instance)
	}
	if j.secret != "" {
		req.Header.Set("Authorization", "Bearer "+j.secret)
	}
	if err := req.Write(conn); err != nil {
		t.Fatalf("write join: %v", err)
	}

	r := bufio.NewReader(conn)
	res, err := http.ReadResponse(r, req)
	if err != nil {
		t.Fatalf("read join answer: %v", err)
	}
	out := &joinConn{conn: conn, r: r, status: res.StatusCode}
	if res.StatusCode != http.StatusSwitchingProtocols {
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("read join body: %v", err)
		}
		out.body = string(body)
	}
	_ = conn.SetDeadline(time.Time{})
	return out
}

// heldInstance is the instance a name's entry currently holds.
func heldInstance(h *relayHost) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.instance
}

// queuedCount is how many fresh connections a name holds.
func queuedCount(h *relayHost) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.queue)
}

// The relay queues a join after it has written the 101, so a client that has
// read its answer may still be ahead of the table; a test that reads the table
// waits for it with the package's waitFor.

// TestAJoinNeedsTheNamesOwnSecret: the table holds a secret per name, and the
// secret that opens one name opens only that one (D11, D13).
func TestAJoinNeedsTheNamesOwnSecret(t *testing.T) {
	_, relay, ts := relayServer(t)
	addr := ts.Listener.Addr().String()

	if got := dialJoin(t, addr, joinOf("beta", "alpha-secret")); got.status != http.StatusUnauthorized {
		t.Fatalf("beta with alpha's secret = %d, body = %s, want 401", got.status, got.body)
	}
	if got := heldInstance(relay.hostFor("beta")); got != "" {
		t.Fatalf("a refused join left beta held by %q", got)
	}

	if got := dialJoin(t, addr, joinOf("beta", "beta-secret")); got.status != http.StatusSwitchingProtocols {
		t.Fatalf("beta with its own secret = %d, body = %s, want 101", got.status, got.body)
	}
	waitFor(t, 2*time.Second, "beta's join to reach the table", func() bool {
		return heldInstance(relay.hostFor("beta")) == "i-beta"
	})
	if got := heldInstance(relay.hostFor("alpha")); got != "" {
		t.Fatalf("beta's join took alpha, which is held by %q", got)
	}
}

// TestAnUnknownNameAndAWrongSecretAnswerAlike: the join route is not an oracle
// for which names a relay holds, so both failures are one 401 (D13).
func TestAnUnknownNameAndAWrongSecretAnswerAlike(t *testing.T) {
	_, _, ts := relayServer(t)
	addr := ts.Listener.Addr().String()

	unknown := dialJoin(t, addr, joinOf("gamma", "alpha-secret"))
	wrong := dialJoin(t, addr, joinOf("alpha", "gamma-secret"))
	if unknown.status != http.StatusUnauthorized || wrong.status != http.StatusUnauthorized {
		t.Fatalf("unknown name = %d, wrong secret = %d, want both 401", unknown.status, wrong.status)
	}
	if unknown.body != wrong.body {
		t.Fatalf("unknown name answers %q, a wrong secret answers %q", unknown.body, wrong.body)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(unknown.body), &body); err != nil {
		t.Fatalf("decode %q: %v", unknown.body, err)
	}
	if body["error"] != "unauthorized" {
		t.Fatalf("body = %q, want the bridge's one unauthorized body", unknown.body)
	}
}

// TestAJoinMustBeTheUpgrade: the upgrade is what reverses the connection, so a
// request without it is a 400 rather than a join (D13).
func TestAJoinMustBeTheUpgrade(t *testing.T) {
	_, relay, ts := relayServer(t)
	addr := ts.Listener.Addr().String()

	plain := joinOf("alpha", "alpha-secret")
	plain.upgrade, plain.connection = "", ""
	if got := dialJoin(t, addr, plain); got.status != http.StatusBadRequest {
		t.Fatalf("a join with no upgrade = %d, body = %s, want 400", got.status, got.body)
	}

	other := joinOf("alpha", "alpha-secret")
	other.upgrade = "websocket"
	if got := dialJoin(t, addr, other); got.status != http.StatusBadRequest {
		t.Fatalf("a join upgrading to %q = %d, want 400", other.upgrade, got.status)
	}

	get := joinOf("alpha", "alpha-secret")
	get.method = http.MethodGet
	if got := dialJoin(t, addr, get); got.status != http.StatusMethodNotAllowed {
		t.Fatalf("a join by GET = %d, want 405", got.status)
	}

	if got := heldInstance(relay.hostFor("alpha")); got != "" {
		t.Fatalf("a refused join reached the table: alpha is held by %q", got)
	}
}

// TestAJoinIsTakenOver: after the 101 the stream has reversed. The relay holds
// the connection and writes a request down it; the client that dialled reads
// that request (D13).
func TestAJoinIsTakenOver(t *testing.T) {
	_, relay, ts := relayServer(t)
	addr := ts.Listener.Addr().String()

	client := dialJoin(t, addr, joinOf("alpha", "alpha-secret"))
	if client.status != http.StatusSwitchingProtocols {
		t.Fatalf("join = %d, body = %s, want 101", client.status, client.body)
	}

	conn, err := relay.hostFor("alpha").take(context.Background())
	if err != nil {
		t.Fatalf("take the joined connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	req, err := http.NewRequest(http.MethodGet, "http://alpha.relay.invalid/api/agents", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := req.Write(conn); err != nil {
		t.Fatalf("write down the joined connection: %v", err)
	}

	_ = client.conn.SetDeadline(time.Now().Add(5 * time.Second))
	got, err := http.ReadRequest(client.r)
	if err != nil {
		t.Fatalf("read the relay's request at the client: %v", err)
	}
	if got.Method != http.MethodGet || got.URL.Path != "/api/agents" {
		t.Fatalf("the client read %s %s, want the relay's GET /api/agents", got.Method, got.URL.Path)
	}
}

// TestTheNewestInstanceWins: a join from a new instance takes the name, and the
// older instance's queued connections close before the new one is taken (D14).
func TestTheNewestInstanceWins(t *testing.T) {
	_, relay, ts := relayServer(t)
	addr := ts.Listener.Addr().String()

	first := dialJoin(t, addr, joinOf("alpha", "alpha-secret"))
	if first.status != http.StatusSwitchingProtocols {
		t.Fatalf("the instance's join = %d, body = %s, want 101", first.status, first.body)
	}
	second := dialJoin(t, addr, joinOf("alpha", "alpha-secret"))
	waitFor(t, 2*time.Second, "both of alpha's joins to queue", func() bool {
		return queuedCount(relay.hostFor("alpha")) == 2
	})

	newer := joinOf("alpha", "alpha-secret")
	newer.instance = "i-alpha-restarted"
	fresh := dialJoin(t, addr, newer)
	if fresh.status != http.StatusSwitchingProtocols {
		t.Fatalf("the restarted instance's join = %d, body = %s, want 101", fresh.status, fresh.body)
	}

	waitFor(t, 2*time.Second, "the newest instance to take alpha", func() bool {
		return heldInstance(relay.hostFor("alpha")) == "i-alpha-restarted" &&
			queuedCount(relay.hostFor("alpha")) == 1
	})
	for i, older := range []*joinConn{first, second} {
		_ = older.conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := older.r.ReadByte(); !errors.Is(err, io.EOF) {
			t.Fatalf("older connection %d read %v, want the relay to have closed it", i, err)
		}
	}
}

// TestTheSocketHasNoJoinRoute: a join over the owner's own socket means
// nothing, so only the network listener answers it (decision 8).
func TestTheSocketHasNoJoinRoute(t *testing.T) {
	s, _ := hostRelayServer(t)
	if got := viaHandler(t, s.SocketHandler(), http.MethodPost, relayJoinPath); got.Code != http.StatusNotFound {
		t.Fatalf("the socket's %s = %d, body = %s, want 404", relayJoinPath, got.Code, got.Body.String())
	}
}

// TestABridgeWithoutARelayHasNoJoinRoute: a bridge that relays nothing is not a
// relay, and answers the join route as it answers any path it does not serve.
func TestABridgeWithoutARelayHasNoJoinRoute(t *testing.T) {
	s := NewServer(testFleet(t), "")
	if got := viaHandler(t, s, http.MethodPost, relayJoinPath); got.Code != http.StatusNotFound {
		t.Fatalf("%s on a bridge with no relay = %d, body = %s, want 404", relayJoinPath, got.Code, got.Body.String())
	}
}

// forwardRelayToken is the relay's own bearer token in the forward tests: the
// value hostRelayServer gives the bridge.
const forwardRelayToken = "secret"

// forwardJoin joins name to the relay at addr as a minimal joiner: it dials the
// join, reads the 101, and serves h on the reversed connection (D13). The
// production joiner does this for a whole bridge; this one is the fewest lines
// a forward test needs, so the forward is not tested through that file.
func forwardJoin(t *testing.T, addr, name, secret string, h http.Handler) {
	t.Helper()
	client := dialJoin(t, addr, joinOf(name, secret))
	if client.status != http.StatusSwitchingProtocols {
		t.Fatalf("%s's join = %d, body = %s, want 101", name, client.status, client.body)
	}
	srv := &http.Server{Handler: h}
	// The connection is the joiner's listener: newRelayConn drains the reader
	// the 101 was read through, as the production joiner does (decision 4).
	l := &forwardListener{conn: newRelayConn(client.conn, client.r)}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = client.conn.Close()
		_ = srv.Close()
	})
}

// forwardListener hands http.Server the one joined connection, then reports
// that there are no more: a joiner dials, it does not listen (D13).
type forwardListener struct {
	conn net.Conn

	mu    sync.Mutex
	taken bool
}

// Accept answers the joined connection once.
func (l *forwardListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.taken {
		return nil, errors.New("forward: one joined connection")
	}
	l.taken = true
	return l.conn, nil
}

// Close closes no socket: the relay dialled the connection, not this listener.
func (l *forwardListener) Close() error { return nil }

// Addr names no socket, because there is none on this side.
func (l *forwardListener) Addr() net.Addr { return forwardAddr{} }

// forwardAddr is the address a joined listener has: none.
type forwardAddr struct{}

func (forwardAddr) Network() string { return "relay" }
func (forwardAddr) String() string  { return "joined" }

// forwardSeen is one request as the host read it: the headers the relay put on
// the hop, and the path and query it addressed.
type forwardSeen struct {
	header http.Header
	path   string
	query  string
}

// forwardRecording is a host that records the requests it is served, so a test
// can read what crossed the hop, and answers each with a 200.
type forwardRecording struct {
	seen chan forwardSeen
}

func (h *forwardRecording) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case h.seen <- forwardSeen{header: r.Header.Clone(), path: r.URL.Path, query: r.URL.RawQuery}:
	default:
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// TestAForwardReachesTheHostWithItsToken: a forward goes down the name's
// joined connection and the host answers it, because the hop carries the host's
// own token (D15).
func TestAForwardReachesTheHostWithItsToken(t *testing.T) {
	s, _, ts := relayServer(t)
	host := NewServer(testFleet(t), "alpha-token")
	forwardJoin(t, ts.Listener.Addr().String(), "alpha", "alpha-secret", host)

	rec := doReq(t, s, http.MethodGet, relayForwardPrefix+"alpha/api/config", nil, map[string]string{
		"Authorization": "Bearer " + forwardRelayToken,
		hostTokenHeader: "alpha-token",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("the forward = %d, body = %s, want the host's 200", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(relayedHeader); got != "alpha" {
		t.Fatalf("%s = %q, want alpha", relayedHeader, got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the host's answer %q: %v", rec.Body.String(), err)
	}
	if _, ok := body["projects"]; !ok {
		t.Fatalf("the forward answered %q, want the host's /api/config", rec.Body.String())
	}
	if held := heldInstance(s.relay.hostFor("alpha")); held != "i-alpha" {
		t.Fatalf("alpha is held by %q, want the joined instance", held)
	}
}

// TestTheHostsTokenDecides: the host's own bearer check decides a forwarded
// request, and its answer is marked as relayed so a caller can tell it from the
// relay's own 401 (D15).
func TestTheHostsTokenDecides(t *testing.T) {
	s, _, ts := relayServer(t)
	host := NewServer(testFleet(t), "alpha-token")
	forwardJoin(t, ts.Listener.Addr().String(), "alpha", "alpha-secret", host)

	rec := doReq(t, s, http.MethodGet, relayForwardPrefix+"alpha/api/config", nil, map[string]string{
		"Authorization": "Bearer " + forwardRelayToken,
		hostTokenHeader: "not-alpha's-token",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a forward with a wrong host token = %d, body = %s, want the host's 401", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(relayedHeader); got != "alpha" {
		t.Fatalf("%s = %q, want the host's 401 marked as alpha's", relayedHeader, got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if body["error"] != "unauthorized" {
		t.Fatalf("body = %q, want the host's unauthorized", rec.Body.String())
	}
}

// TestAForwardWithoutAHostTokenIsTheRelays401: with no host token there is
// nothing the relay may act on, so it answers itself and forwards nothing (D15).
func TestAForwardWithoutAHostTokenIsTheRelays401(t *testing.T) {
	s, _ := hostRelayServer(t)
	rec := doReq(t, s, http.MethodGet, relayForwardPrefix+"alpha/api/config", nil, map[string]string{
		"Authorization": "Bearer " + forwardRelayToken,
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a forward with no host token = %d, body = %s, want 401", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(relayedHeader); got != "" {
		t.Fatalf("%s = %q on the relay's own 401, want none", relayedHeader, got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if body["error"] != "unauthorized" {
		t.Fatalf("body = %q, want the relay's own unauthorized", rec.Body.String())
	}
}

// TestTheRelaysOwnTokenIsStillRequired: a forward is an /api route, so the
// relay's bearer token is checked before any of this (D15).
func TestTheRelaysOwnTokenIsStillRequired(t *testing.T) {
	s, _, ts := relayServer(t)
	host := NewServer(testFleet(t), "alpha-token")
	forwardJoin(t, ts.Listener.Addr().String(), "alpha", "alpha-secret", host)

	for _, auth := range []string{"", "Bearer wrong"} {
		rec := doReq(t, s, http.MethodGet, relayForwardPrefix+"alpha/api/config", nil, map[string]string{
			"Authorization": auth,
			hostTokenHeader: "alpha-token",
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("a forward with Authorization %q = %d, body = %s, want 401", auth, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get(relayedHeader); got != "" {
			t.Fatalf("%s = %q, want none: the relay refused before forwarding", relayedHeader, got)
		}
	}
}

// TestTheHopCarriesNoCookieOrHostTokenHeader: the hop carries the host's token
// and nothing of the caller's credentials, and keeps the caller's query (D15).
func TestTheHopCarriesNoCookieOrHostTokenHeader(t *testing.T) {
	s, _, ts := relayServer(t)
	host := &forwardRecording{seen: make(chan forwardSeen, 1)}
	forwardJoin(t, ts.Listener.Addr().String(), "alpha", "alpha-secret", host)

	rec := doReq(t, s, http.MethodGet, relayForwardPrefix+"alpha/api/config?limit=5", nil, map[string]string{
		"Authorization": "Bearer " + forwardRelayToken,
		hostTokenHeader: "alpha-token",
		"Cookie":        "session=abc",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("the forward = %d, body = %s, want the host's 200", rec.Code, rec.Body.String())
	}
	select {
	case seen := <-host.seen:
		if got := seen.header.Get(hostTokenHeader); got != "" {
			t.Fatalf("the hop carried %s: %q", hostTokenHeader, got)
		}
		if got := seen.header.Get("Cookie"); got != "" {
			t.Fatalf("the hop carried Cookie: %q", got)
		}
		if got := seen.header.Get("Authorization"); got != "Bearer alpha-token" {
			t.Fatalf("the hop carried Authorization %q, want the host's own token", got)
		}
		if got := seen.header.Get("X-Forwarded-For"); got != "" {
			t.Fatalf("the hop carried X-Forwarded-For: %q", got)
		}
		if seen.path != "/api/config" || seen.query != "limit=5" {
			t.Fatalf("the hop addressed %s?%s, want /api/config?limit=5", seen.path, seen.query)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the forward did not reach the host")
	}
}

// TestAForwardOutsideTheAPIIs404: the relay forwards the host's /api and
// nothing else (D15).
func TestAForwardOutsideTheAPIIs404(t *testing.T) {
	s, _ := hostRelayServer(t)
	for _, path := range []string{
		relayForwardPrefix + "alpha",
		relayForwardPrefix + "alpha/",
		relayForwardPrefix + "alpha/health",
		relayForwardPrefix + "alpha/api",
		relayForwardPrefix + "alpha/apix/config",
	} {
		rec := doReq(t, s, http.MethodGet, path, nil, map[string]string{
			"Authorization": "Bearer " + forwardRelayToken,
			hostTokenHeader: "alpha-token",
		})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, body = %s, want 404", path, rec.Code, rec.Body.String())
		}
	}
}

// TestARelayRouteIsNotForwarded: relays never chain, so a path under api/hosts/
// or api/relay/ is not a forward (D15).
func TestARelayRouteIsNotForwarded(t *testing.T) {
	s, _ := hostRelayServer(t)
	for _, path := range []string{
		relayForwardPrefix + "alpha/api/relay/hosts",
		relayForwardPrefix + "alpha/api/relay/join",
		relayForwardPrefix + "alpha/api/hosts/beta/api/config",
	} {
		rec := doReq(t, s, http.MethodGet, path, nil, map[string]string{
			"Authorization": "Bearer " + forwardRelayToken,
			hostTokenHeader: "alpha-token",
		})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, body = %s, want 404", path, rec.Code, rec.Body.String())
		}
	}
}

// TestAnUnknownHostIs404: a name the table does not hold is no relayed host,
// and is named as such.
func TestAnUnknownHostIs404(t *testing.T) {
	s, _ := hostRelayServer(t)
	rec := doReq(t, s, http.MethodGet, relayForwardPrefix+"gamma/api/config", nil, map[string]string{
		"Authorization": "Bearer " + forwardRelayToken,
		hostTokenHeader: "gamma-token",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a forward to gamma = %d, body = %s, want 404", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if body["error"] != "no relayed host gamma" {
		t.Fatalf("body = %q, want the relay's own words", rec.Body.String())
	}
}

// TestADisconnectedHostIs503AfterTheWait: a name with no joined connection is
// waited for, then answered 503 with D14's words (decision 7).
func TestADisconnectedHostIs503AfterTheWait(t *testing.T) {
	s, relay := hostRelayServer(t)
	relay.wait = 100 * time.Millisecond

	start := time.Now()
	rec := doReq(t, s, http.MethodGet, relayForwardPrefix+"alpha/api/config", nil, map[string]string{
		"Authorization": "Bearer " + forwardRelayToken,
		hostTokenHeader: "alpha-token",
	})
	elapsed := time.Since(start)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a forward to a disconnected host = %d, body = %s, want 503", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if body["error"] != "alpha is not connected" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "alpha is not connected")
	}
	if elapsed > time.Second {
		t.Fatalf("the answer took %s, want it within 1s", elapsed)
	}
}

// TestAnEventStreamFlushesThroughTheRelay: an event reaches the caller as the
// host writes it, not when the host's handler returns (D15).
func TestAnEventStreamFlushesThroughTheRelay(t *testing.T) {
	_, _, ts := relayServer(t)
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })
	host := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		// The handler is still here when the first event reaches the caller.
		<-released
	})
	forwardJoin(t, ts.Listener.Addr().String(), "alpha", "alpha-secret", host)

	req, err := http.NewRequest(http.MethodGet, ts.URL+relayForwardPrefix+"alpha/api/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+forwardRelayToken)
	req.Header.Set(hostTokenHeader, "alpha-token")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("open the relayed stream: %v", err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the relayed stream = %d, want 200", res.StatusCode)
	}

	lines := make(chan string, 1)
	go func() {
		line, err := bufio.NewReader(res.Body).ReadString('\n')
		if err != nil {
			return
		}
		lines <- line
	}()
	select {
	case got := <-lines:
		if got != "data: one\n" {
			t.Fatalf("the first event read %q, want %q", got, "data: one\n")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first event did not arrive before the host's handler returned")
	}
}

// TestTheRelayListsItsHosts: GET /api/relay/hosts names every host the relay
// holds, sorted, connected or not (D16).
func TestTheRelayListsItsHosts(t *testing.T) {
	s, relay, ts := relayServer(t)
	host := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	forwardJoin(t, ts.Listener.Addr().String(), "alpha", "alpha-secret", host)
	waitFor(t, 2*time.Second, "alpha's join to reach the table", func() bool {
		return heldInstance(relay.hostFor("alpha")) == "i-alpha"
	})

	rec := doReq(t, s, http.MethodGet, relayHostsPath, nil, map[string]string{
		"Authorization": "Bearer " + forwardRelayToken,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d, body = %s, want 200", relayHostsPath, rec.Code, rec.Body.String())
	}
	var got contract.RelayHosts
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(got.Hosts) != 2 {
		t.Fatalf("the relay lists %d hosts, body = %s, want the two it names", len(got.Hosts), rec.Body.String())
	}
	if got.Hosts[0].Name != "alpha" || got.Hosts[1].Name != "beta" {
		t.Fatalf("the relay lists %q then %q, want alpha then beta", got.Hosts[0].Name, got.Hosts[1].Name)
	}
	joined := got.Hosts[0]
	if !joined.Connected || joined.Since == 0 || joined.LastContact == 0 {
		t.Fatalf("alpha's entry = %+v, want a joined host with since and lastContact", joined)
	}
	absent := got.Hosts[1]
	if absent.Connected || absent.Since != 0 || absent.LastContact != 0 {
		t.Fatalf("beta's entry = %+v, want a host that never joined", absent)
	}

	// A bridge that relays nothing does not register the route, so it answers
	// 404 as an older bridge does (D16).
	plain := NewServer(testFleet(t), "secret")
	if got := viaHandler(t, plain.SocketHandler(), http.MethodGet, relayHostsPath); got.Code != http.StatusNotFound {
		t.Fatalf("%s on a bridge with no relay = %d, body = %s, want 404", relayHostsPath, got.Code, got.Body.String())
	}
}
