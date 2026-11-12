package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAgent is an in-memory agentTransport for the control-plane tests.
// It records every request, answers from results/handler, hands out
// distinct session ids, and can inject notifications or hang up, so tests
// observe exactly what the bridge sent without a child process.
type fakeAgent struct {
	mu     sync.Mutex
	frames []capturedFrame
	// frameConn runs beside frames and holds the connection each frame
	// arrived on: stdioConn for the child's stdio stream, otherwise the
	// index of a viewer connection (see fakeProjectChild).
	frameConn []int
	// results answers a method with a fixed JSON value; handler, when set,
	// is consulted first and may return an rpc error.
	results map[string]any
	handler func(method string, params json.RawMessage) (any, *rpcError, bool)
	// caps replaces the capabilities initialize advertises.
	caps     map[string]any
	sessions int
	gens     int
	writeMu  sync.Mutex
	out      io.WriteCloser
}

func newFakeAgent() *fakeAgent { return &fakeAgent{results: map[string]any{}} }

// stdioConn is the connection index the child's own stdio stream is recorded
// against; viewer connections are numbered from zero.
const stdioConn = -1

func (a *fakeAgent) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	return a.openAs(stdioConn)
}

// openAs starts one generation whose frames are recorded against conn, so the
// project-child fake can tag each viewer connection's frames.
func (a *fakeAgent) openAs(conn int) (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	a.mu.Lock()
	a.gens++
	a.mu.Unlock()
	a.writeMu.Lock()
	a.out = stdoutW
	a.writeMu.Unlock()
	go a.serveAs(conn, stdinR, stdoutW)
	return stdinW, stdoutR, io.NopCloser(strings.NewReader("")), nil
}

func (a *fakeAgent) serve(r io.Reader, w io.WriteCloser) { a.serveAs(stdioConn, r, w) }

// serveAs is one stream's request loop: it records each frame against conn and
// answers requests from results/handler.
func (a *fakeAgent) serveAs(conn int, r io.Reader, w io.WriteCloser) {
	defer w.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.Method == "" {
			continue
		}
		a.mu.Lock()
		a.frames = append(a.frames, capturedFrame{method: req.Method, params: string(req.Params)})
		a.frameConn = append(a.frameConn, conn)
		a.mu.Unlock()
		if len(req.ID) == 0 {
			continue // notification
		}
		a.reply(w, req.ID, req.Method, req.Params)
	}
}

func (a *fakeAgent) reply(w io.Writer, id json.RawMessage, method string, params json.RawMessage) {
	send := func(frame map[string]any) {
		frame["jsonrpc"], frame["id"] = "2.0", id
		a.writeMu.Lock()
		_ = json.NewEncoder(w).Encode(frame)
		a.writeMu.Unlock()
	}
	a.mu.Lock()
	h := a.handler
	a.mu.Unlock()
	if h != nil {
		if res, rerr, ok := h(method, params); ok {
			if rerr != nil {
				send(map[string]any{"error": rerr})
			} else {
				send(map[string]any{"result": res})
			}
			return
		}
	}
	a.mu.Lock()
	res, ok := a.results[method]
	caps := a.caps
	var sid string
	if method == "session/new" {
		a.sessions++
		sid = "s-" + strconv.Itoa(a.sessions)
	}
	a.mu.Unlock()
	switch {
	case ok:
		send(map[string]any{"result": res})
	case method == "initialize":
		if caps == nil {
			caps = map[string]any{"configAccess": map[string]any{}, "watchAccess": map[string]any{}}
		}
		send(map[string]any{"result": map[string]any{
			"protocolVersion":   1,
			"agentInfo":         map[string]any{"version": "dev"},
			"agentCapabilities": caps,
		}})
	case method == "session/new":
		send(map[string]any{"result": map[string]any{"sessionId": sid}})
	default:
		send(map[string]any{"result": map[string]any{}})
	}
}

// notify injects a notification into the current generation.
func (a *fakeAgent) notify(method string, params any) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if a.out != nil {
		_ = json.NewEncoder(a.out).Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}
}

// die hangs up the current generation, as a crashed agent would.
func (a *fakeAgent) die() {
	a.writeMu.Lock()
	out := a.out
	a.writeMu.Unlock()
	if out != nil {
		_ = out.Close()
	}
}

// calls returns the params of every request seen for method, in order.
func (a *fakeAgent) calls(method string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, f := range a.frames {
		if f.method == method {
			out = append(out, f.params)
		}
	}
	return out
}

func (a *fakeAgent) generations() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gens
}

func (a *fakeAgent) Wait() error                { return nil }
func (a *fakeAgent) Signal(sig os.Signal) error { return nil }

// Kill force-terminates the current generation, as a real SIGKILL does: the
// streams go with the process, so the read loop ends. A no-op here would let
// a generation spawned after Stop's signal outlive the stop and hang it.
func (a *fakeAgent) Kill() error { a.die(); return nil }

func (a *fakeAgent) Detach() error { return nil }

// frameAt is one recorded frame and the connection it arrived on.
type frameAt struct {
	conn  int
	frame capturedFrame
}

// record returns every frame the fake saw, in arrival order, with the
// connection each arrived on.
func (a *fakeAgent) record() []frameAt {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]frameAt, len(a.frames))
	for i, f := range a.frames {
		out[i] = frameAt{conn: a.frameConn[i], frame: f}
	}
	return out
}

// fakeProjectChild is a project child: a fakeAgent whose stdio is the bridge's
// own connection, plus the `--viewers` unix socket the real child would serve.
// Each viewer connection is served on its own goroutine with the fakeAgent's
// request handling, and each frame is recorded against the connection it
// arrived on (stdioConn for stdio, 0, 1, … for viewers).
type fakeProjectChild struct {
	*fakeAgent
	socket string

	mu       sync.Mutex
	ln       net.Listener
	conns    []net.Conn
	nextConn int
	stopped  chan struct{}
	stopOnce sync.Once
	// signals counts the stop signals the bridge sent, so a test can tell one
	// stop from a stop-and-retry.
	signals int
}

func newFakeProjectChild(socket string) *fakeProjectChild {
	return &fakeProjectChild{
		fakeAgent: newFakeAgent(),
		socket:    socket,
		stopped:   make(chan struct{}),
	}
}

// Open serves stdio as fakeAgent does and binds the viewer socket, so a viewer
// can reach the child exactly as `marshal acp --viewers` would let it.
func (p *fakeProjectChild) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	// A previous generation's socket is gone before this one binds: a restart
	// is a new process, which cannot listen on a path that is still bound.
	p.closeSocket()
	ln, err := net.Listen("unix", p.socket)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fake project child: listen %s: %w", p.socket, err)
	}
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	go p.accept(ln)
	return p.fakeAgent.openAs(stdioConn)
}

// accept serves each viewer connection on its own goroutine.
func (p *fakeProjectChild) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		idx := p.nextConn
		p.nextConn++
		p.conns = append(p.conns, conn)
		p.mu.Unlock()
		go p.fakeAgent.serveAs(idx, conn, conn)
	}
}

// closeSocket closes the listener and every viewer connection.
func (p *fakeProjectChild) closeSocket() {
	p.mu.Lock()
	ln, conns := p.ln, p.conns
	p.ln, p.conns = nil, nil
	p.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// die closes the listener and every connection, then hangs up stdio, as a
// crashed agent with its socket gone would.
func (p *fakeProjectChild) die() {
	p.closeSocket()
	p.fakeAgent.die()
}

// Signal records the stop the bridge asked for and takes the streams with
// it, the way a real process does when it dies on SIGTERM. Hanging up stdio
// closes this generation's readDone; without that, a Child whose generation
// was replaced while Stop ran would wait forever on a pipe nothing will ever
// close, because the fake's Kill is a no-op.
func (p *fakeProjectChild) Signal(sig os.Signal) error {
	p.mu.Lock()
	p.signals++
	p.mu.Unlock()
	p.stopOnce.Do(func() { close(p.stopped) })
	p.die()
	return nil
}

// signalsSeen reports how many stop signals the bridge sent the child.
func (p *fakeProjectChild) signalsSeen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.signals
}

// wasStopped reports whether the bridge has stopped the child.
func (p *fakeProjectChild) wasStopped() bool {
	select {
	case <-p.stopped:
		return true
	default:
		return false
	}
}

// waitStopped fails the test rather than hanging when the child is never
// stopped.
func (p *fakeProjectChild) waitStopped(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-p.stopped:
	case <-time.After(d):
		t.Fatalf("the project child was not stopped within %v", d)
	}
}

// viewers reports how many viewer connections the child has accepted.
func (p *fakeProjectChild) viewers() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.nextConn
}

// framesOn returns the frames that arrived on one connection, stdioConn for
// the child's stdio stream.
func (p *fakeProjectChild) framesOn(conn int) []capturedFrame {
	var out []capturedFrame
	for _, rec := range p.record() {
		if rec.conn == conn {
			out = append(out, rec.frame)
		}
	}
	return out
}

// ctlFleet builds a fleet whose control agent is agent. Project agents use
// newAgent when non-nil, else the registry helper child.
func ctlFleet(t *testing.T, agent *fakeAgent) *Fleet {
	t.Helper()
	f := testFleet(t)
	f.audit = NewAuditLog(t.TempDir())
	f.newControl = func() (*Child, error) { return &Child{Transport: agent}, nil }
	return f
}

// agentFleet builds a fleet in which every project agent (and the control
// agent) is a fresh fakeAgent, returned by id for inspection.
func agentFleet(t *testing.T) (*Fleet, *fakeAgent, func(agentID string) *fakeAgent) {
	t.Helper()
	ctl := newFakeAgent()
	f := ctlFleet(t, ctl)
	var mu sync.Mutex
	byID := map[string]*fakeAgent{}
	f.newRuntime = func(a Agent) (*Child, error) {
		fa := newFakeAgent()
		mu.Lock()
		byID[a.ID] = fa
		mu.Unlock()
		return &Child{Transport: fa}, nil
	}
	return f, ctl, func(id string) *fakeAgent {
		mu.Lock()
		defer mu.Unlock()
		return byID[id]
	}
}

func ctlContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}
