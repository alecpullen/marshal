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
	"testing"
	"time"
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
