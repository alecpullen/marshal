package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// reportHosts gives s the tunnel helper's report, through the route the helper
// itself uses (one-daemon D7).
func reportHosts(t *testing.T, s *Server, hosts ...contract.DaemonHost) {
	t.Helper()
	body, err := json.Marshal(contract.DaemonHostsReport{Hosts: hosts})
	if err != nil {
		t.Fatalf("marshal the hosts report: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, daemonHostsPath, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.SocketHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("%s = %d, body = %s, want 204", daemonHostsPath, rec.Code, rec.Body.String())
	}
}

// tunnelRequest is one request as a host read it: what it addressed and the
// headers that crossed the hop.
type tunnelRequest struct {
	method string
	path   string
	query  string
	header http.Header
}

// fakeTunnel is a host's end of a tunnel: an http.Server on a unix socket in a
// short temporary directory, recording what it is sent and answering through h
// (one-daemon D10).
type fakeTunnel struct {
	socket string
	seen   chan tunnelRequest
}

// newFakeTunnel serves h on a unix socket and records every request it is sent.
// The directory is short because a unix socket path is bounded and a test's
// temporary directory can be long.
func newFakeTunnel(t *testing.T, h http.Handler) *fakeTunnel {
	t.Helper()
	dir, err := os.MkdirTemp("", "fwd")
	if err != nil {
		t.Fatalf("make the tunnel's directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "t.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", socket, err)
	}
	f := &fakeTunnel{socket: socket, seen: make(chan tunnelRequest, 8)}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case f.seen <- tunnelRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			header: r.Header.Clone(),
		}:
		default:
		}
		h.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f
}

// okTunnel is a host that answers every forwarded request the same way.
func okTunnel() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

// next is the next request the tunnel was sent.
func (f *fakeTunnel) next(t *testing.T) tunnelRequest {
	t.Helper()
	select {
	case got := <-f.seen:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("the forward did not reach the host's socket")
		return tunnelRequest{}
	}
}

// nothing fails when the tunnel was sent anything: a refused forward never
// leaves this machine's side.
func (f *fakeTunnel) nothing(t *testing.T) {
	t.Helper()
	select {
	case got := <-f.seen:
		t.Fatalf("the host's socket was sent %s %s, want nothing", got.method, got.path)
	case <-time.After(200 * time.Millisecond):
	}
}

// liveHost is the report entry for a tunnel at socket that is answering now.
func liveHost(name, socket string) contract.DaemonHost {
	return contract.DaemonHost{
		Name:   name,
		SSH:    "me@" + name,
		State:  contract.HostLive,
		Socket: socket,
		Since:  time.Now().UnixMilli(),
	}
}

// TestTheHostsRouteListsTheReport: GET /api/hosts answers the hosts the tunnel
// helper last reported, sorted by name (D10).
func TestTheHostsRouteListsTheReport(t *testing.T) {
	s := NewServer(testFleet(t))
	reportHosts(t, s,
		contract.DaemonHost{Name: "workshop", SSH: "me@workshop", State: contract.HostDown, Socket: "/tmp/workshop.sock", LastError: "ssh: connect: refused"},
		liveHost("box", "/tmp/box.sock"),
	)

	rec := doReq(t, s, http.MethodGet, hostsPath, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d, body = %s, want 200", hostsPath, rec.Code, rec.Body.String())
	}
	var got contract.HostList
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(got.Hosts) != 2 {
		t.Fatalf("%s names %d hosts, body = %s, want the two reported", hostsPath, len(got.Hosts), rec.Body.String())
	}
	if got.Hosts[0].Name != "box" || got.Hosts[1].Name != "workshop" {
		t.Fatalf("%s names %q then %q, want box then workshop", hostsPath, got.Hosts[0].Name, got.Hosts[1].Name)
	}
	if got.Hosts[0].State != contract.HostLive || got.Hosts[1].State != contract.HostDown {
		t.Fatalf("%s reports %q and %q, want each host's own state", hostsPath, got.Hosts[0].State, got.Hosts[1].State)
	}
	if got.Hosts[1].LastError != "ssh: connect: refused" {
		t.Fatalf("workshop's lastError = %q, want the helper's", got.Hosts[1].LastError)
	}
}

// TestTheHostsRouteIsEmptyWithoutAReport: a daemon whose helper has said
// nothing lists no host, and says so as an empty list rather than null (D10).
func TestTheHostsRouteIsEmptyWithoutAReport(t *testing.T) {
	s := NewServer(testFleet(t))
	rec := doReq(t, s, http.MethodGet, hostsPath, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d, body = %s, want 200", hostsPath, rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); trimJSON(body) != `{"hosts":[]}` {
		t.Fatalf("%s = %s, want {\"hosts\":[]}", hostsPath, body)
	}
}

// trimJSON is a body without its surrounding whitespace, so an encoder's
// trailing newline does not decide a test.
func trimJSON(body string) string {
	return string(bytes.TrimSpace([]byte(body)))
}

// TestAForwardReachesTheHostsSocket: /api/hosts/box/api/agents?x=1 reaches the
// host's tunnel socket as /api/agents?x=1, and the host's answer comes back
// unchanged (D10).
func TestAForwardReachesTheHostsSocket(t *testing.T) {
	s := NewServer(testFleet(t))
	tunnel := newFakeTunnel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusTeapot, map[string]string{"from": "box"})
	}))
	reportHosts(t, s, liveHost("box", tunnel.socket))

	rec := doReq(t, s, http.MethodGet, hostForwardPrefix+"box/api/agents?x=1", nil, nil)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("the forward = %d, body = %s, want the host's own 418", rec.Code, rec.Body.String())
	}
	if body := trimJSON(rec.Body.String()); body != `{"from":"box"}` {
		t.Fatalf("the forward answered %s, want the host's own body", body)
	}
	seen := tunnel.next(t)
	if seen.method != http.MethodGet || seen.path != "/api/agents" || seen.query != "x=1" {
		t.Fatalf("the host was sent %s %s?%s, want GET /api/agents?x=1", seen.method, seen.path, seen.query)
	}
}

// TestAForwardToAnUnknownHostIs404: a name the helper does not report is no
// host this daemon reaches, and is named as such (D10).
func TestAForwardToAnUnknownHostIs404(t *testing.T) {
	s := NewServer(testFleet(t))
	rec := doReq(t, s, http.MethodGet, hostForwardPrefix+"gamma/api/agents", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a forward to gamma = %d, body = %s, want 404", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if body["error"] != "no host gamma" {
		t.Fatalf("body = %q, want \"no host gamma\"", rec.Body.String())
	}
}

// TestAForwardToAHostThatIsDownIs503: a host whose tunnel is not up cannot
// carry the request, and the answer names its state and why it last failed
// (D10).
func TestAForwardToAHostThatIsDownIs503(t *testing.T) {
	s := NewServer(testFleet(t))
	reportHosts(t, s, contract.DaemonHost{
		Name:      "box",
		SSH:       "me@box",
		State:     contract.HostDown,
		Socket:    "/tmp/box.sock",
		LastError: "ssh: connect to host box port 22: Connection refused",
		Since:     time.Now().UnixMilli(),
	})

	rec := doReq(t, s, http.MethodGet, hostForwardPrefix+"box/api/agents", nil, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a forward to a host that is down = %d, body = %s, want 503", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	want := "box is down: ssh: connect to host box port 22: Connection refused"
	if body["error"] != want {
		t.Fatalf("body = %q, want %q", body["error"], want)
	}
}

// TestAForwardRefusesTheSocketOnlyRoutes: a route only the socket serves is not
// reached through a tunnel either, and the host is sent nothing (D10).
func TestAForwardRefusesTheSocketOnlyRoutes(t *testing.T) {
	s := NewServer(testFleet(t))
	tunnel := newFakeTunnel(t, okTunnel())
	reportHosts(t, s, liveHost("box", tunnel.socket))

	for _, path := range []string{
		attachPath,
		pinPath,
		daemonRestartPath,
		daemonHostsPath,
		marshalEnrollPath,
		agentAttachPrefix + "a1" + agentAttachSuffix,
	} {
		rec := doReq(t, s, http.MethodGet, hostForwardPrefix+"box"+path, nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("a forward to %s = %d, body = %s, want 404", path, rec.Code, rec.Body.String())
		}
	}
	tunnel.nothing(t)
}

// TestAForwardDoesNotChain: a forward carries the host's API and never the
// hosts route, so /api/hosts/x/api/hosts/... is not a route at all (D10).
func TestAForwardDoesNotChain(t *testing.T) {
	s := NewServer(testFleet(t))
	tunnel := newFakeTunnel(t, okTunnel())
	reportHosts(t, s, liveHost("box", tunnel.socket))

	for _, path := range []string{
		hostForwardPrefix + "box/api/hosts/x/api/agents",
		hostForwardPrefix + "box/api/relay/hosts",
		hostForwardPrefix + "box/health",
		hostForwardPrefix + "box/api",
	} {
		rec := doReq(t, s, http.MethodGet, path, nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, body = %s, want 404", path, rec.Code, rec.Body.String())
		}
	}
	tunnel.nothing(t)
}

// TestAForwardDropsTheCallersCredentials: the caller's bearer value and cookie
// stop at this daemon; the host's own socket needs neither (D10).
func TestAForwardDropsTheCallersCredentials(t *testing.T) {
	s := NewServer(testFleet(t))
	tunnel := newFakeTunnel(t, okTunnel())
	reportHosts(t, s, liveHost("box", tunnel.socket))

	rec := doReq(t, s, http.MethodGet, hostForwardPrefix+"box/api/agents", nil, map[string]string{
		"Authorization": "Bearer caller-secret",
		"Cookie":        "session=abc",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("the forward = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	seen := tunnel.next(t)
	if got := seen.header.Get("Authorization"); got != "" {
		t.Fatalf("the hop carried Authorization: %q", got)
	}
	if got := seen.header.Get("Cookie"); got != "" {
		t.Fatalf("the hop carried Cookie: %q", got)
	}
}

// TestAForwardStreams: an event reaches the caller as the host writes it, not
// when the host's handler returns (D10).
func TestAForwardStreams(t *testing.T) {
	s := NewServer(testFleet(t))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	tunnel := newFakeTunnel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		// The handler is still here when the first event reaches the caller, so
		// the first event can only have arrived by having been flushed.
		<-release
		_, _ = io.WriteString(w, "data: two\n\n")
		w.(http.Flusher).Flush()
	}))
	reportHosts(t, s, liveHost("box", tunnel.socket))

	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	req, err := http.NewRequest(http.MethodGet, ts.URL+hostForwardPrefix+"box/api/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("open the forwarded stream: %v", err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the forwarded stream = %d, want 200", res.StatusCode)
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
		t.Fatal("the first event did not arrive before the host wrote the second")
	}
}
