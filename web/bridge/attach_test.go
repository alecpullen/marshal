package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// attachHarness is a local-profile bridge serving SocketHandler over a unix
// socket, with its project children replaced by in-memory fakes. The socket
// paths live in a short temporary directory: a unix socket path is capped at
// 104 bytes on macOS and t.TempDir() under a suite TMPDIR is not short.
type attachHarness struct {
	t      *testing.T
	fleet  *Fleet
	server *Server
	socket string

	mu    sync.Mutex
	fakes map[string]*fakeProjectChild
}

func newAttachHarness(t *testing.T) *attachHarness {
	t.Helper()
	dir, err := os.MkdirTemp("", "p4a")
	if err != nil {
		t.Fatalf("make harness dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// No test may be reaped by the production linger while it is running. The
	// duration is the fleet's, not a package variable: a relay goroutine can
	// still read it while this test's cleanups run.
	ws := NewWorkspace(filepath.Join(dir, "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "unused", nil, filepath.Join(dir, "state"), Limits{}, "", nil, "marshal-state")
	f.lingerDur = 5 * time.Minute
	t.Cleanup(f.Close)

	h := &attachHarness{
		t:      t,
		fleet:  f,
		socket: filepath.Join(dir, "d.sock"),
		fakes:  map[string]*fakeProjectChild{},
	}
	f.newProjectChild = func(root, socket string) (*Child, error) {
		fake := newFakeProjectChild(socket)
		h.mu.Lock()
		h.fakes[root] = fake
		h.mu.Unlock()
		return &Child{Transport: fake}, nil
	}

	h.server = NewServer(f)
	h.server.SetDaemonInfo(DaemonInfo{Version: "v0.0.0", Profile: "local", PID: os.Getpid(), Socket: h.socket})

	ln, err := ListenLocal(h.socket)
	if err != nil {
		t.Fatalf("ListenLocal(%s): %v", h.socket, err)
	}
	hs := &http.Server{Handler: h.server.SocketHandler()}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { _ = hs.Close() })
	return h
}

// fake returns the child built for root, if one has started.
func (h *attachHarness) fake(root string) *fakeProjectChild {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fakes[root]
}

// attachResult is one handshake's response head and the connection under it.
type attachResult struct {
	conn   net.Conn
	reader *bufio.Reader
	status int
	header textproto.MIMEHeader
}

// attach writes one handshake on a fresh connection and reads the response
// head by hand: the upgraded stream starts right behind it.
func (h *attachHarness) attach(method, root, sessionID string, upgrade bool) *attachResult {
	h.t.Helper()
	body := fmt.Sprintf(`{"root":%s,"sessionId":%s}`, strconv.Quote(root), strconv.Quote(sessionID))
	return h.handshake(attachPath, method, body, upgrade)
}

// attachAgent writes one /api/agents/{id}/attach handshake on a fresh
// connection. The path names the agent, so the handshake carries no body.
func (h *attachHarness) attachAgent(method, id string, upgrade bool) *attachResult {
	h.t.Helper()
	return h.handshake("/api/agents/"+id+"/attach", method, "", upgrade)
}

// handshake writes one attach request for path on a fresh connection and reads
// the response head by hand, as both attaches answer it.
func (h *attachHarness) handshake(path, method, body string, upgrade bool) *attachResult {
	h.t.Helper()
	var req strings.Builder
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\nHost: daemon\r\n", method, path)
	if upgrade {
		req.WriteString("Connection: Upgrade\r\nUpgrade: " + attachProtocol + "\r\n")
	}
	fmt.Fprintf(&req, "Content-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)

	conn, err := net.Dial("unix", h.socket)
	if err != nil {
		h.t.Fatalf("dial daemon socket: %v", err)
	}
	h.t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, req.String()); err != nil {
		h.t.Fatalf("write handshake: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	reader := bufio.NewReader(conn)
	tp := textproto.NewReader(reader)
	line, err := tp.ReadLine()
	if err != nil {
		h.t.Fatalf("read status line: %v", err)
	}
	var proto string
	var status int
	if _, err := fmt.Sscanf(line, "%s %d", &proto, &status); err != nil {
		h.t.Fatalf("parse status line %q: %v", line, err)
	}
	header, err := tp.ReadMIMEHeader()
	if err != nil {
		h.t.Fatalf("read response headers: %v", err)
	}
	if status != http.StatusSwitchingProtocols {
		return &attachResult{conn: conn, reader: reader, status: status, header: header}
	}
	// The stream outlives the handshake.
	_ = conn.SetDeadline(time.Time{})
	return &attachResult{conn: conn, reader: reader, status: status, header: header}
}

// refusal returns a refused answer's {"error": …} sentence.
func (a *attachResult) refusal(t *testing.T) string {
	t.Helper()
	n, err := strconv.Atoi(a.header.Get("Content-Length"))
	if err != nil || n <= 0 {
		return ""
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(a.reader, buf); err != nil {
		t.Fatalf("read refusal body: %v", err)
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode refusal body %q: %v", buf, err)
	}
	return out.Error
}

// send writes one JSON-RPC frame on the attached stream.
func (a *attachResult) send(t *testing.T, frame string) {
	t.Helper()
	if _, err := io.WriteString(a.conn, frame+"\n"); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// readLine reads one frame from the attached stream.
func (a *attachResult) readLine(t *testing.T) string {
	t.Helper()
	_ = a.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := a.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read frame from the stream: %v", err)
	}
	return strings.TrimSpace(line)
}

// infoBytes reads GET /api/daemon over the socket.
func (h *attachHarness) infoBytes() []byte {
	h.t.Helper()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", h.socket)
		},
	}}
	resp, err := client.Get("http://daemon/api/daemon")
	if err != nil {
		h.t.Fatalf("GET /api/daemon: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		h.t.Fatalf("read /api/daemon: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("GET /api/daemon = %d: %s", resp.StatusCode, body)
	}
	return body
}

// eventually polls until check passes, so a test never races the relay
// goroutine that counts the viewer.
func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// framesOf returns the frames the fake received for method on conn.
func framesOf(t *testing.T, fake *fakeProjectChild, conn int, method string) []capturedFrame {
	t.Helper()
	var out []capturedFrame
	for _, f := range fake.framesOn(conn) {
		if f.method == method {
			out = append(out, f)
		}
	}
	return out
}

func TestAttachingANewSessionUpgrades(t *testing.T) {
	root := t.TempDir()
	h := newAttachHarness(t)

	res := h.attach(http.MethodPost, root, "", true)
	if res.status != http.StatusSwitchingProtocols {
		t.Fatalf("attach = %d, want 101", res.status)
	}
	if got := res.header.Get("Upgrade"); got != attachProtocol {
		t.Errorf("Upgrade = %q, want %q", got, attachProtocol)
	}
	if got := res.header.Get("Connection"); !strings.EqualFold(got, "upgrade") {
		t.Errorf("Connection = %q, want Upgrade", got)
	}
	sessionID := res.header.Get(sessionHeader)
	if sessionID == "" {
		t.Fatal("101 carried no " + sessionHeader)
	}

	fake := h.fake(root)
	if fake == nil {
		t.Fatal("no project child was built for the root")
	}
	news := framesOf(t, fake, stdioConn, "session/new")
	if len(news) != 1 {
		t.Fatalf("session/new sent %d times on the child's own connection, want 1", len(news))
	}
	params := paramsOf(t, news[0].params)
	if got := params["cwd"]; got != root {
		t.Errorf("session/new cwd = %v, want %s", got, root)
	}

	// A frame written after the 101 reaches a viewer connection on the child,
	// and the child's answer comes back.
	res.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"session/status","params":{"sessionId":%q}}`, sessionID))
	line := res.readLine(t)
	if !strings.Contains(line, `"id":1`) {
		t.Errorf("child answer = %q, want the reply to id 1", line)
	}
	eventually(t, "the viewer frame to reach the child", func() bool {
		return len(framesOf(t, fake, 0, "session/status")) == 1
	})
}

func TestAttachingATrackedSessionCreatesNothing(t *testing.T) {
	root := t.TempDir()
	h := newAttachHarness(t)

	first := h.attach(http.MethodPost, root, "", true)
	if first.status != http.StatusSwitchingProtocols {
		t.Fatalf("first attach = %d, want 101", first.status)
	}
	sessionID := first.header.Get(sessionHeader)

	second := h.attach(http.MethodPost, root, sessionID, true)
	if second.status != http.StatusSwitchingProtocols {
		t.Fatalf("second attach = %d, want 101", second.status)
	}
	if got := second.header.Get(sessionHeader); got != sessionID {
		t.Fatalf("second attach session = %q, want %q", got, sessionID)
	}

	fake := h.fake(root)
	if n := len(framesOf(t, fake, stdioConn, "session/new")); n != 1 {
		t.Errorf("session/new sent %d times, want 1: a tracked session is attached as it is", n)
	}
	if n := len(framesOf(t, fake, stdioConn, "session/resume")); n != 0 {
		t.Errorf("session/resume sent %d times, want 0 for a tracked session", n)
	}

	pc, err := h.fleet.projectChildFor(context.Background(), root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	eventually(t, "both viewers to be counted", func() bool {
		for _, s := range pc.reg.summaries() {
			if s.ID == sessionID {
				return s.Viewers == 2
			}
		}
		return false
	})
}

func TestAttachingAStoredSessionResumesIt(t *testing.T) {
	t.Run("a stored session is resumed and attached", func(t *testing.T) {
		root := t.TempDir()
		h := newAttachHarness(t)

		res := h.attach(http.MethodPost, root, "stored-session", true)
		if res.status != http.StatusSwitchingProtocols {
			t.Fatalf("attach = %d, want 101", res.status)
		}
		if got := res.header.Get(sessionHeader); got != "stored-session" {
			t.Errorf("session = %q, want stored-session", got)
		}
		fake := h.fake(root)
		resumes := framesOf(t, fake, stdioConn, "session/resume")
		if len(resumes) != 1 {
			t.Fatalf("session/resume sent %d times, want 1", len(resumes))
		}
		params := paramsOf(t, resumes[0].params)
		if got := params["sessionId"]; got != "stored-session" {
			t.Errorf("session/resume sessionId = %v", got)
		}
		if got := params["cwd"]; got != root {
			t.Errorf("session/resume cwd = %v, want %s", got, root)
		}
	})

	t.Run("a failed resume is a 404", func(t *testing.T) {
		root := t.TempDir()
		h := newAttachHarness(t)
		h.fleet.newProjectChild = func(root, socket string) (*Child, error) {
			fake := newFakeProjectChild(socket)
			fake.handler = func(method string, _ json.RawMessage) (any, *rpcError, bool) {
				if method == "session/resume" {
					return nil, &rpcError{Code: -32000, Message: "no such session"}, true
				}
				return nil, nil, false
			}
			return &Child{Transport: fake}, nil
		}

		res := h.attach(http.MethodPost, root, "stale", true)
		if res.status != http.StatusNotFound {
			t.Fatalf("attach of an unknown stored session = %d, want 404", res.status)
		}
		if msg := res.refusal(t); !strings.Contains(msg, "unknown session") {
			t.Errorf("refusal = %q, want an unknown-session sentence", msg)
		}
	})
}

// TestAnAttachedSessionAsksItsClientAboutTrust: a project child's registry
// marks every session it opens or resumes, so the child lets the session's
// marshal client answer the trust question (P4 D8).
func TestAnAttachedSessionAsksItsClientAboutTrust(t *testing.T) {
	t.Run("a new session", func(t *testing.T) {
		root := t.TempDir()
		h := newAttachHarness(t)

		res := h.attach(http.MethodPost, root, "", true)
		if res.status != http.StatusSwitchingProtocols {
			t.Fatalf("attach = %d, want 101", res.status)
		}
		fake := h.fake(root)
		if fake == nil {
			t.Fatal("no project child was built for the root")
		}
		news := framesOf(t, fake, stdioConn, "session/new")
		if len(news) != 1 {
			t.Fatalf("session/new sent %d times, want 1", len(news))
		}
		if got := paramsOf(t, news[0].params)["clientTrust"]; got != true {
			t.Errorf("session/new clientTrust = %v, want true", got)
		}
	})

	t.Run("a stored session", func(t *testing.T) {
		root := t.TempDir()
		h := newAttachHarness(t)

		res := h.attach(http.MethodPost, root, "stored-session", true)
		if res.status != http.StatusSwitchingProtocols {
			t.Fatalf("attach = %d, want 101", res.status)
		}
		fake := h.fake(root)
		if fake == nil {
			t.Fatal("no project child was built for the root")
		}
		resumes := framesOf(t, fake, stdioConn, "session/resume")
		if len(resumes) != 1 {
			t.Fatalf("session/resume sent %d times, want 1", len(resumes))
		}
		if got := paramsOf(t, resumes[0].params)["clientTrust"]; got != true {
			t.Errorf("session/resume clientTrust = %v, want true", got)
		}
	})
}

func TestTheAttachRefusals(t *testing.T) {
	t.Run("a wrong method", func(t *testing.T) {
		h := newAttachHarness(t)
		res := h.attach(http.MethodGet, t.TempDir(), "", true)
		if res.status != http.StatusMethodNotAllowed {
			t.Fatalf("GET attach = %d, want 405", res.status)
		}
		if res.refusal(t) == "" {
			t.Error("405 carried no JSON error")
		}
	})

	t.Run("no upgrade headers", func(t *testing.T) {
		h := newAttachHarness(t)
		res := h.attach(http.MethodPost, t.TempDir(), "", false)
		if res.status != http.StatusBadRequest {
			t.Fatalf("attach without an upgrade = %d, want 400", res.status)
		}
		if res.refusal(t) == "" {
			t.Error("400 carried no JSON error")
		}
	})

	t.Run("a relative root", func(t *testing.T) {
		h := newAttachHarness(t)
		res := h.attach(http.MethodPost, "some/project", "", true)
		if res.status != http.StatusBadRequest {
			t.Fatalf("attach with a relative root = %d, want 400", res.status)
		}
		if res.refusal(t) == "" {
			t.Error("400 carried no JSON error")
		}
	})

	t.Run("a root that is a file", func(t *testing.T) {
		h := newAttachHarness(t)
		file := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		res := h.attach(http.MethodPost, file, "", true)
		if res.status != http.StatusBadRequest {
			t.Fatalf("attach with a file root = %d, want 400", res.status)
		}
		if res.refusal(t) == "" {
			t.Error("400 carried no JSON error")
		}
	})

	t.Run("a child that cannot start", func(t *testing.T) {
		h := newAttachHarness(t)
		h.fleet.newProjectChild = func(root, socket string) (*Child, error) {
			return nil, fmt.Errorf("no such binary")
		}
		res := h.attach(http.MethodPost, t.TempDir(), "", true)
		if res.status != http.StatusBadGateway {
			t.Fatalf("attach with a child that cannot start = %d, want 502", res.status)
		}
		msg := res.refusal(t)
		if !strings.Contains(msg, "no such binary") {
			t.Errorf("502 refusal = %q, want the start error", msg)
		}
	})

	t.Run("a server without a fleet", func(t *testing.T) {
		srv := NewServer(nil)
		body := fmt.Sprintf(`{"root":%s}`, strconv.Quote(t.TempDir()))
		req := httptest.NewRequest(http.MethodPost, attachPath, strings.NewReader(body))
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", attachProtocol)
		rec := httptest.NewRecorder()
		srv.SocketHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("attach on a server without a fleet = %d, want 501", rec.Code)
		}
		var out struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Error == "" {
			t.Errorf("501 body = %q, want a JSON error", rec.Body.String())
		}
	})
}

// TestAnAttachToAFleetSessionIsRefused: a live fleet agent's session belongs
// to that agent, so an attach naming it is refused before any viewer
// connection opens (P4 D10 decision 6).
func TestAnAttachToAFleetSessionIsRefused(t *testing.T) {
	root := t.TempDir()
	h := newAttachHarness(t)

	// A client's session starts the child and holds one viewer, so the
	// refusal below is measured against a live relay.
	first := h.attach(http.MethodPost, root, "", true)
	if first.status != http.StatusSwitchingProtocols {
		t.Fatalf("opening attach = %d, want 101", first.status)
	}
	pc, err := h.fleet.projectChildFor(context.Background(), root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	pc.reg.TrackFleet("fleet-1", AgentPath(root))

	fake := h.fake(root)
	if fake == nil {
		t.Fatal("no fake was built for the project child")
	}
	eventually(t, "the first viewer to connect", func() bool { return fake.viewers() == 1 })
	viewers := fake.viewers()

	res := h.attach(http.MethodPost, root, "fleet-1", true)
	if res.status != http.StatusConflict {
		t.Fatalf("attach to a fleet session = %d, want 409", res.status)
	}
	if got, want := res.refusal(t), "session fleet-1 belongs to a fleet agent"; got != want {
		t.Errorf("409 refusal = %q, want %q", got, want)
	}
	if got := fake.viewers(); got != viewers {
		t.Errorf("a refused attach opened a viewer connection: %d before, %d after", viewers, got)
	}
	if !pc.reg.IsFleet("fleet-1") {
		t.Error("the refusal untracked the fleet's session")
	}
}

// TestAnAttachToASpawningFleetSessionIsRefused: Spawn maps a session to its
// agent as soon as session/new returns, before the registry tracks it. An
// attach landing in that window must still be refused, or it would take the
// relay of a session the agent is about to use (P4 D10 decision 6).
func TestAnAttachToASpawningFleetSessionIsRefused(t *testing.T) {
	root := t.TempDir()
	h := newAttachHarness(t)

	// A client's session starts the child and holds one viewer, so the
	// refusal below is measured against a live relay.
	first := h.attach(http.MethodPost, root, "", true)
	if first.status != http.StatusSwitchingProtocols {
		t.Fatalf("opening attach = %d, want 101", first.status)
	}
	fake := h.fake(root)
	if fake == nil {
		t.Fatal("no fake was built for the project child")
	}
	eventually(t, "the first viewer to connect", func() bool { return fake.viewers() == 1 })
	viewers := fake.viewers()

	// The state a Spawn is in between session/new returning and TrackFleet:
	// the fleet knows the session, the registry does not.
	h.fleet.mu.Lock()
	h.fleet.sessionAgent["spawning-1"] = "agent-a"
	h.fleet.mu.Unlock()

	res := h.attach(http.MethodPost, root, "spawning-1", true)
	if res.status != http.StatusConflict {
		t.Fatalf("attach to a spawning fleet session = %d, want 409", res.status)
	}
	if got, want := res.refusal(t), "session spawning-1 belongs to a fleet agent"; got != want {
		t.Errorf("409 refusal = %q, want %q", got, want)
	}
	if got := fake.viewers(); got != viewers {
		t.Errorf("a refused attach opened a viewer connection: %d before, %d after", viewers, got)
	}
}

// TestAnAttachToAPausedAgentsSessionIsRefused: a paused agent keeps its record
// and loses its runtime, and the session that record names is still the
// agent's. The project attach refuses it with the fleet session's sentence,
// the one place a paused session was still attachable (decision 2, P4 H15).
func TestAnAttachToAPausedAgentsSessionIsRefused(t *testing.T) {
	root := t.TempDir()
	h := newAttachHarness(t)

	// A client's session starts the child and holds one viewer, so the
	// refusal below is measured against a live relay.
	first := h.attach(http.MethodPost, root, "", true)
	if first.status != http.StatusSwitchingProtocols {
		t.Fatalf("opening attach = %d, want 101", first.status)
	}
	fake := h.fake(root)
	if fake == nil {
		t.Fatal("no fake was built for the project child")
	}
	eventually(t, "the first viewer to connect", func() bool { return fake.viewers() == 1 })
	viewers := fake.viewers()

	// A paused agent: the record survives and the runtime does not, so
	// neither the registry's fleet flag nor the classifier's map knows the
	// session. Only the record names it.
	if err := h.fleet.ws.PutAgent(Agent{
		ID: "agent-a", Project: root, Name: "reviewer", SessionID: "paused-1",
		OwnerID: "owner-1", Origin: OriginUI,
	}); err != nil {
		t.Fatalf("record agent: %v", err)
	}

	res := h.attach(http.MethodPost, root, "paused-1", true)
	if res.status != http.StatusConflict {
		t.Fatalf("attach to a paused agent's session = %d, want 409", res.status)
	}
	if got, want := res.refusal(t), "session paused-1 belongs to a fleet agent"; got != want {
		t.Errorf("409 refusal = %q, want %q", got, want)
	}
	if got := fake.viewers(); got != viewers {
		t.Errorf("a refused attach opened a viewer connection: %d before, %d after", viewers, got)
	}
}

func TestAFailedDialClosesTheNewSession(t *testing.T) {
	root := t.TempDir()
	h := newAttachHarness(t)

	// A child that starts and speaks stdio but never binds a viewer socket:
	// the handshake succeeds and the dial is what fails.
	agent := newFakeAgent()
	h.fleet.newProjectChild = func(root, socket string) (*Child, error) {
		return &Child{Transport: agent}, nil
	}

	res := h.attach(http.MethodPost, root, "", true)
	if res.status != http.StatusBadGateway {
		t.Fatalf("attach whose viewer dial fails = %d, want 502", res.status)
	}
	if res.refusal(t) == "" {
		t.Error("502 carried no JSON error")
	}

	news := agent.calls("session/new")
	if len(news) != 1 {
		t.Fatalf("session/new sent %d times, want 1", len(news))
	}
	created := paramsOf(t, news[0])["sessionId"]
	closes := agent.calls("session/close")
	if len(closes) != 1 {
		t.Fatalf("session/close sent %d times after a failed dial, want 1", len(closes))
	}
	if got := paramsOf(t, closes[0])["sessionId"]; created != nil && got != created {
		t.Errorf("session/close closed %v, want the session just created", got)
	}

	// Nothing is tracked: the child reports no session.
	children := h.fleet.projectChildren()
	if len(children) != 1 {
		t.Fatalf("the fleet reports %d children, want 1", len(children))
	}
	if len(children[0].Sessions) != 0 {
		t.Errorf("the failed attach left %d tracked sessions, want 0", len(children[0].Sessions))
	}
}

func TestTheTCPHandlerHasNoAttach(t *testing.T) {
	f := testFleet(t)
	srv := NewServer(f, "secret")
	body := fmt.Sprintf(`{"root":%s}`, strconv.Quote(t.TempDir()))
	req := httptest.NewRequest(http.MethodPost, attachPath, strings.NewReader(body))
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", attachProtocol)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("attach on the network listener = %d, want 404", rec.Code)
	}
}

// TestTheTCPHandlerHasNoAgentAttach: the agent attach is a socket-only route
// for the same reason the project attach is, so the network listener answers
// it 404 (P4 D3).
func TestTheTCPHandlerHasNoAgentAttach(t *testing.T) {
	f := testFleet(t)
	srv := NewServer(f, "secret")
	req := httptest.NewRequest(http.MethodPost, "/api/agents/agent-a/attach", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", attachProtocol)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("agent attach on the network listener = %d, want 404", rec.Code)
	}
}

// fleetAgent gives the harness the state Spawn leaves a local-profile agent
// in: a record in the workspace, a live runtime on the project's child, and
// the session tracked as the fleet's (P4 D10). It returns the child.
func (h *attachHarness) fleetAgent(t *testing.T, root, id, name string) *projectChild {
	t.Helper()
	pc, err := h.fleet.projectChildFor(context.Background(), root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	sessionID := id + "-session"
	if err := h.fleet.ws.PutAgent(Agent{
		ID: id, Project: root, Name: name, SessionID: sessionID,
		OwnerID: "owner-1", Origin: OriginUI,
	}); err != nil {
		t.Fatalf("record agent %s: %v", id, err)
	}
	// The session is tracked as a fleet session and mapped to its agent, as
	// Spawn does it.
	pc.reg.TrackFleet(sessionID, AgentPath(root))
	h.fleet.mu.Lock()
	h.fleet.runtimes[id] = &agentRuntime{id: id, root: root, project: pc, reg: pc.reg, sessionID: sessionID}
	h.fleet.sessionAgent[sessionID] = id
	h.fleet.mu.Unlock()
	return pc
}

// TestTheAgentAttachUpgrades: POST /api/agents/{id}/attach on the socket hands
// the client the agent's own session, named in both headers, and counts it as
// a viewer of that session (P5 F5).
func TestTheAgentAttachUpgrades(t *testing.T) {
	root := t.TempDir()
	h := newAttachHarness(t)
	pc := h.fleetAgent(t, root, "agent-a", "reviewer")
	sessionID := "agent-a-session"

	res := h.attachAgent(http.MethodPost, "agent-a", true)
	if res.status != http.StatusSwitchingProtocols {
		t.Fatalf("agent attach = %d, want 101: %s", res.status, res.refusal(t))
	}
	if got := res.header.Get("Upgrade"); got != attachProtocol {
		t.Errorf("Upgrade = %q, want %q", got, attachProtocol)
	}
	if got := res.header.Get(sessionHeader); got != sessionID {
		t.Errorf("%s = %q, want %q", sessionHeader, got, sessionID)
	}
	if got := res.header.Get(agentHeader); got != "agent-a" {
		t.Errorf("%s = %q, want agent-a", agentHeader, got)
	}

	fake := h.fake(root)
	if fake == nil {
		t.Fatal("no fake was built for the project child")
	}
	eventually(t, "the agent's viewer to connect", func() bool { return fake.viewers() == 1 })
	// summaries leaves a fleet agent's session out, so the count is read where
	// the registry keeps it.
	eventually(t, "the viewer to be counted on the agent's session", func() bool {
		pc.reg.mu.Lock()
		defer pc.reg.mu.Unlock()
		info, ok := pc.reg.sessions[sessionID]
		return ok && info.viewers == 1
	})
}

// TestTheAgentAttachRefusals: every way the agent attach refuses, with its
// status and its sentence, and no viewer connection opened by any of them
// (P5 F5 decision 5).
func TestTheAgentAttachRefusals(t *testing.T) {
	live := func(t *testing.T, h *attachHarness, root string) string {
		h.fleetAgent(t, root, "agent-a", "reviewer")
		return "agent-a"
	}
	// A paused agent keeps its record and loses its runtime.
	paused := func(t *testing.T, h *attachHarness, root string) string {
		id := live(t, h, root)
		h.fleet.mu.Lock()
		delete(h.fleet.runtimes, id)
		h.fleet.mu.Unlock()
		return id
	}
	failedStart := func(t *testing.T, h *attachHarness, root string) string {
		id := live(t, h, root)
		h.fleet.mu.Lock()
		h.fleet.runtimes[id] = &agentRuntime{id: id, root: root, spawnErr: fmt.Errorf("no such binary")}
		h.fleet.mu.Unlock()
		return id
	}
	ownChild := func(t *testing.T, h *attachHarness, root string) string {
		id := live(t, h, root)
		h.fleet.mu.Lock()
		h.fleet.runtimes[id].project = nil
		h.fleet.mu.Unlock()
		return id
	}
	noSession := func(t *testing.T, h *attachHarness, root string) string {
		id := live(t, h, root)
		h.fleet.mu.Lock()
		h.fleet.runtimes[id].sessionID = ""
		h.fleet.mu.Unlock()
		return id
	}

	tests := []struct {
		name     string
		method   string
		upgrade  bool
		noFleet  bool
		setup    func(t *testing.T, h *attachHarness, root string) string
		want     int
		sentence string
	}{
		{
			name: "a wrong method", method: http.MethodGet, upgrade: true, setup: live,
			want: http.StatusMethodNotAllowed, sentence: "attach is a POST",
		},
		{
			name: "no upgrade headers", method: http.MethodPost, setup: live,
			want: http.StatusBadRequest, sentence: "attach needs an " + attachProtocol + " upgrade",
		},
		{
			name: "a bridge with no fleet", method: http.MethodPost, upgrade: true, noFleet: true,
			want: http.StatusNotImplemented, sentence: "this bridge has no fleet",
		},
		{
			name: "an agent with no record", method: http.MethodPost, upgrade: true,
			setup: func(t *testing.T, h *attachHarness, root string) string { return "no-such-agent" },
			want:  http.StatusNotFound, sentence: "unknown agent no-such-agent",
		},
		{
			name: "a paused agent", method: http.MethodPost, upgrade: true, setup: paused,
			want: http.StatusConflict, sentence: "agent reviewer is paused: resume it first",
		},
		{
			name: "an agent whose runtime failed to start", method: http.MethodPost, upgrade: true, setup: failedStart,
			want: http.StatusBadGateway, sentence: "no such binary",
		},
		{
			name: "an agent on a child of its own", method: http.MethodPost, upgrade: true, setup: ownChild,
			want:     http.StatusConflict,
			sentence: "agent reviewer runs on a child of its own, which has no viewer socket yet",
		},
		{
			name: "an agent with no session yet", method: http.MethodPost, upgrade: true, setup: noSession,
			want: http.StatusConflict, sentence: "agent reviewer has no session yet",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.noFleet {
				srv := NewServer(nil)
				req := httptest.NewRequest(tc.method, "/api/agents/agent-a/attach", nil)
				if tc.upgrade {
					req.Header.Set("Connection", "Upgrade")
					req.Header.Set("Upgrade", attachProtocol)
				}
				rec := httptest.NewRecorder()
				srv.SocketHandler().ServeHTTP(rec, req)
				if rec.Code != tc.want {
					t.Fatalf("agent attach on a server without a fleet = %d, want %d", rec.Code, tc.want)
				}
				var out struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Error != tc.sentence {
					t.Errorf("refusal = %q, want %q", out.Error, tc.sentence)
				}
				return
			}

			root := t.TempDir()
			h := newAttachHarness(t)
			id := tc.setup(t, h, root)

			// A child exists for every case whose setup resolved one; a refusal
			// must not open a viewer connection on it.
			viewers := 0
			fake := h.fake(root)
			if fake != nil {
				viewers = fake.viewers()
			}

			res := h.attachAgent(tc.method, id, tc.upgrade)
			if res.status != tc.want {
				t.Fatalf("agent attach = %d, want %d", res.status, tc.want)
			}
			if got := res.refusal(t); got != tc.sentence {
				t.Errorf("refusal = %q, want %q", got, tc.sentence)
			}
			if fake != nil {
				if got := fake.viewers(); got != viewers {
					t.Errorf("a refused attach opened a viewer connection: %d before, %d after", viewers, got)
				}
			}
		})
	}
}

// TestAnAgentAttachWhoseDialFailsKeepsTheSession: a dial failure after the
// handshake closes what the attach opened but never closes the agent's session,
// which belongs to the fleet (P5 F5 decision 5). The project attach closes the
// session it just created; this one must not, and the fleet must still own it
// afterwards.
func TestAnAgentAttachWhoseDialFailsKeepsTheSession(t *testing.T) {
	root := t.TempDir()
	h := newAttachHarness(t)

	// An agent whose project child is gone: the runtime is live and the session
	// is the agent's, but nothing listens where the child's viewer socket would
	// be, so the dial is what fails.
	sessionID := "agent-gone-session"
	pc := &projectChild{root: root, socket: filepath.Join(root, "gone.sock")}
	if err := h.fleet.ws.PutAgent(Agent{
		ID: "agent-gone", Project: root, Name: "reviewer", SessionID: sessionID,
		OwnerID: "owner-1", Origin: OriginUI,
	}); err != nil {
		t.Fatalf("record agent: %v", err)
	}
	h.fleet.mu.Lock()
	h.fleet.runtimes["agent-gone"] = &agentRuntime{id: "agent-gone", root: root, project: pc, sessionID: sessionID}
	h.fleet.sessionAgent[sessionID] = "agent-gone"
	h.fleet.mu.Unlock()

	res := h.attachAgent(http.MethodPost, "agent-gone", true)
	if res.status != http.StatusBadGateway {
		t.Fatalf("agent attach whose viewer dial fails = %d, want 502: %s", res.status, res.refusal(t))
	}
	const want = "dial the project child: "
	if got := res.refusal(t); !strings.HasPrefix(got, want) {
		t.Errorf("refusal = %q, want the %q prefix", got, want)
	}

	// The session is the agent's, so it stays open and the agent keeps it.
	h.fleet.mu.Lock()
	owner, tracked := h.fleet.sessionAgent[sessionID]
	h.fleet.mu.Unlock()
	if !tracked || owner != "agent-gone" {
		t.Errorf("the failed attach left the session with owner %q (tracked %v), want agent-gone", owner, tracked)
	}
	a, ok := h.fleet.ws.Agent("agent-gone")
	if !ok {
		t.Fatal("the failed attach dropped the agent's record")
	}
	if a.SessionID != sessionID {
		t.Errorf("agent session = %q, want %q", a.SessionID, sessionID)
	}
}

func TestAgentAttachIDParsesOnlyItsPath(t *testing.T) {
	tests := []struct {
		path string
		want string
		ok   bool
	}{
		{path: "/api/agents/agent-1/attach", want: "agent-1", ok: true},
		{path: "/api/agents/2f3a9c/attach", want: "2f3a9c", ok: true},
		{path: "/api/agents/a1/attach", want: "a1", ok: true},
		{path: "/api/agents//attach"},
		{path: "/api/agents/"},
		{path: "/api/agents/agent-1"},
		{path: "/api/agents/agent-1/attach/"},
		{path: "/api/agents/one/two/attach"},
		{path: "/api/agents/agent-1/detach"},
		{path: "/api/projects/attach"},
		{path: "/api/agents"},
		{path: ""},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			id, ok := agentAttachID(tc.path)
			if ok != tc.ok || id != tc.want {
				t.Errorf("agentAttachID(%q) = %q, %v; want %q, %v", tc.path, id, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestTheDaemonReportsItsChildren(t *testing.T) {
	root := t.TempDir()
	h := newAttachHarness(t)

	// No child yet: children is an empty list, never null.
	var probe struct {
		Children json.RawMessage `json:"children"`
	}
	if err := json.Unmarshal(h.infoBytes(), &probe); err != nil {
		t.Fatalf("decode /api/daemon: %v", err)
	}
	if got := string(probe.Children); got != "[]" {
		t.Fatalf("children with no child = %s, want []", got)
	}

	res := h.attach(http.MethodPost, root, "", true)
	if res.status != http.StatusSwitchingProtocols {
		t.Fatalf("attach = %d, want 101", res.status)
	}
	sessionID := res.header.Get(sessionHeader)

	var info DaemonInfo
	if err := json.Unmarshal(h.infoBytes(), &info); err != nil {
		t.Fatalf("decode /api/daemon: %v", err)
	}
	if len(info.Children) != 1 {
		t.Fatalf("daemon reported %d children, want 1", len(info.Children))
	}
	child := info.Children[0]
	if child.Root != root {
		t.Errorf("child root = %q, want %q", child.Root, root)
	}
	if child.Version != "dev" {
		t.Errorf("child version = %q, want the initialize version dev", child.Version)
	}
	if len(child.Sessions) != 1 {
		t.Fatalf("child reported %d sessions, want 1", len(child.Sessions))
	}
	if child.Sessions[0].ID != sessionID {
		t.Errorf("session id = %q, want %q", child.Sessions[0].ID, sessionID)
	}
	if child.Sessions[0].LastActive == 0 {
		t.Error("session lastActive is zero")
	}
	eventually(t, "the daemon to count the viewer", func() bool {
		var now DaemonInfo
		if err := json.Unmarshal(h.infoBytes(), &now); err != nil || len(now.Children) != 1 || len(now.Children[0].Sessions) != 1 {
			return false
		}
		return now.Children[0].Sessions[0].Viewers == 1
	})
}
