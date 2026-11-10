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

	// No test may be reaped by the production linger while it is running.
	prevLinger := projectLinger
	projectLinger = 5 * time.Minute
	t.Cleanup(func() { projectLinger = prevLinger })

	ws := NewWorkspace(filepath.Join(dir, "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "unused", nil, filepath.Join(dir, "state"), Limits{}, "", nil, "marshal-state")
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
	var req strings.Builder
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\nHost: daemon\r\n", method, attachPath)
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
