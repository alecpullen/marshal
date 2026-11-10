package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// viewerHarness is one agent host serving a viewer socket's connections, with
// one adopted runtime the viewers attach to.
type viewerHarness struct {
	host   *agentHost
	rt     *app.Runtime
	state  *session.State
	served *service.Session
}

// newViewerHarness builds the host and adopts a bare runtime, as
// openEmbedding does for the embedding.
func newViewerHarness(t *testing.T) *viewerHarness {
	t.Helper()
	state := embeddingState(t, "s1")
	broker := pubsub.NewBroker[session.Event]()
	state.SetEventBroker(broker)
	rt := &app.Runtime{
		State:       state,
		SessionID:   "s1",
		EventBroker: broker,
		Config:      config.Default(),
		WorkingDir:  t.TempDir(),
	}
	host, err := newAgentHost(testRunConfig(t))
	if err != nil {
		t.Fatalf("newAgentHost: %v", err)
	}
	servedHost, served := service.New(service.Deps{State: state})
	if err := host.manager.adopt(rt, servedHost, served); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	return &viewerHarness{host: host, rt: rt, state: state, served: served}
}

// pipeNetConn adapts rpc.Pipe's agent end to the net.Conn serveViewer takes.
// The pipe has no addresses and no deadlines.
type pipeNetConn struct {
	io.ReadWriteCloser
}

func (pipeNetConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (pipeNetConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (pipeNetConn) SetDeadline(time.Time) error      { return nil }
func (pipeNetConn) SetReadDeadline(time.Time) error  { return nil }
func (pipeNetConn) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// viewerConn is one served viewer connection: the agent goroutine, the
// client's transport, and the notifications it received.
type viewerConn struct {
	t      *testing.T
	client rpc.Transport
	conn   *rpc.Conn // raw, for calls before or without an initialize
	host   *rpc.Host // set by openViewer's dial
	cancel context.CancelFunc
	done   chan struct{}

	closeOnce sync.Once

	mu     sync.Mutex
	frames []viewerFrame
}

// viewerFrame is one notification the viewer received.
type viewerFrame struct {
	method string
	params json.RawMessage
}

// record keeps one notification.
func (v *viewerConn) record(method string, params json.RawMessage) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.frames = append(v.frames, viewerFrame{method: method, params: append(json.RawMessage(nil), params...)})
}

// sawText reports whether any received frame's params carry s.
func (v *viewerConn) sawText(s string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, f := range v.frames {
		if strings.Contains(string(f.params), s) {
			return true
		}
	}
	return false
}

// close ends the connection: the agent goroutine stops and the client's
// transport closes. It is safe to call more than once.
func (v *viewerConn) close() {
	v.closeOnce.Do(func() {
		v.cancel()
		_ = v.client.Close()
		<-v.done
	})
}

// serve starts the viewer's agent goroutine over a fresh pipe and returns the
// client's transport.
func (h *viewerHarness) serve(t *testing.T) (*viewerConn, rpc.Transport) {
	t.Helper()
	client, agent := rpc.Pipe()
	v := &viewerConn{t: t, client: client, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	go func() {
		defer close(v.done)
		_ = h.host.serveViewer(ctx, pipeNetConn{ReadWriteCloser: agent})
	}()
	t.Cleanup(v.close)
	return v, client
}

// openViewer serves one viewer and dials it as the marshal client the bridge
// is, so the connection is ready to attach.
func (h *viewerHarness) openViewer(t *testing.T) *viewerConn {
	t.Helper()
	v, client := h.serve(t)
	host, err := rpc.Dial(context.Background(), client, rpc.DialOptions{
		Handlers: rpc.Handlers{Notify: v.record},
	})
	if err != nil {
		t.Fatalf("dial the viewer: %v", err)
	}
	v.host = host
	return v
}

// openRawViewer serves one viewer with a raw connection and no initialize, so a
// test can drive the handshake itself.
func (h *viewerHarness) openRawViewer(t *testing.T) *viewerConn {
	t.Helper()
	v, client := h.serve(t)
	v.conn = rpc.NewConn(client, rpc.Handlers{Notify: v.record})
	return v
}

// initialize runs the handshake, declaring the marshal capability when asked.
func (v *viewerConn) initialize(t *testing.T, marshal bool) {
	t.Helper()
	params := map[string]any{"protocolVersion": 1}
	if marshal {
		params["clientCapabilities"] = map[string]any{
			"marshal": map[string]any{"protocolVersion": acpwire.MarshalProtocolVersion},
		}
	}
	var res json.RawMessage
	if err := v.conn.Call(context.Background(), "initialize", params, &res); err != nil {
		t.Fatalf("initialize: %v", err)
	}
}

// attach attaches the viewer's client to id.
func attachViewer(t *testing.T, v *viewerConn, id string) acpclient.Session {
	t.Helper()
	sess, err := v.host.Attach(context.Background(), id)
	if err != nil {
		t.Fatalf("attach %q: %v", id, err)
	}
	return sess
}

// cancellers reports how many entries the host's canceller chain holds.
func (h *viewerHarness) cancellers() int {
	h.host.cancellersMu.Lock()
	defer h.host.cancellersMu.Unlock()
	return len(h.host.cancellers)
}

// TestAViewerRefusesAClientWithoutTheCapability pins decision 3: a viewer
// serves marshal clients only, and a client that did not declare the
// capability is refused at the handshake and on every later call.
func TestAViewerRefusesAClientWithoutTheCapability(t *testing.T) {
	h := newViewerHarness(t)
	v := h.openRawViewer(t)

	var res json.RawMessage
	err := v.conn.Call(context.Background(), "initialize", map[string]any{"protocolVersion": 1}, &res)
	if err == nil || !strings.Contains(err.Error(), "a viewer connection serves marshal clients only") {
		t.Fatalf("initialize without the capability = %v, want the viewer's refusal", err)
	}

	err = v.conn.Call(context.Background(), "session/status", map[string]any{"sessionId": "s1"}, &res)
	if err == nil || !strings.Contains(err.Error(), "a viewer connection serves marshal clients only") {
		t.Fatalf("session/status after a refused initialize = %v, want the viewer's refusal", err)
	}
}

// TestAViewerCannotStartOrLoadASession pins decision 4: a viewer's session/new
// serves only a replacement, and load, resume and delete are refused. The
// manager's sessions are unchanged.
func TestAViewerCannotStartOrLoadASession(t *testing.T) {
	h := newViewerHarness(t)
	before := len(h.host.manager.All())

	v := h.openRawViewer(t)
	v.initialize(t, true)

	for _, method := range []string{"session/new", "session/load", "session/resume", "session/delete"} {
		var res json.RawMessage
		err := v.conn.Call(context.Background(), method, map[string]any{}, &res)
		if err == nil || !strings.Contains(err.Error(), "a viewer connection cannot start or load a session") {
			t.Fatalf("%s = %v, want the viewer's lifecycle refusal", method, err)
		}
	}

	if after := len(h.host.manager.All()); after != before {
		t.Fatalf("the manager holds %d sessions after the refusals, want %d", after, before)
	}
}

// TestTwoViewersEachSeeEveryUpdate pins W2: two viewers attached to one session
// each receive the session's updates, whichever viewer caused them.
func TestTwoViewersEachSeeEveryUpdate(t *testing.T) {
	h := newViewerHarness(t)
	a := h.openViewer(t)
	b := h.openViewer(t)
	sa := attachViewer(t, a, "s1")
	sb := attachViewer(t, b, "s1")

	if err := sa.SetNotice(context.Background(), acpwireNotice("first")); err != nil {
		t.Fatalf("A's SetNotice: %v", err)
	}
	waitFor(t, "both viewers to see A's notice", func() bool {
		return a.sawText("first") && b.sawText("first")
	})

	if err := sb.SetNotice(context.Background(), acpwireNotice("second")); err != nil {
		t.Fatalf("B's SetNotice: %v", err)
	}
	waitFor(t, "A to see B's change", func() bool {
		return a.sawText("second")
	})
}

// TestAViewersUpdatesNeverReachTheSink pins decision 1: a viewer's managers
// notify through its own server, never the host's sink.
func TestAViewersUpdatesNeverReachTheSink(t *testing.T) {
	h := newViewerHarness(t)
	var mu sync.Mutex
	var sinkFrames []string
	h.host.sink.Set(func(method string, params any) error {
		encoded, _ := json.Marshal(params)
		mu.Lock()
		sinkFrames = append(sinkFrames, string(encoded))
		mu.Unlock()
		return nil
	})
	defer h.host.sink.Clear()

	v := h.openViewer(t)
	sess := attachViewer(t, v, "s1")
	if err := sess.SetNotice(context.Background(), acpwireNotice("viewer-only")); err != nil {
		t.Fatalf("SetNotice: %v", err)
	}
	waitFor(t, "the viewer to see its own notice", func() bool {
		return v.sawText("viewer-only")
	})

	mu.Lock()
	defer mu.Unlock()
	for _, frame := range sinkFrames {
		if strings.Contains(frame, "viewer-only") {
			t.Fatalf("the host's sink received the viewer's update: %s", frame)
		}
	}
}

// TestOnlyOneViewersAnswerWins pins W2's single settlement: two viewers answer
// one approval, one answer succeeds and the other reports it as settled.
func TestOnlyOneViewersAnswerWins(t *testing.T) {
	h := newViewerHarness(t)
	key := registerTestApproval(t, h.state)

	a := h.openViewer(t)
	b := h.openViewer(t)
	sa := attachViewer(t, a, "s1")
	sb := attachViewer(t, b, "s1")

	answers := []error{answerApproval(sa, key), answerApproval(sb, key)}
	var won, settled int
	for _, err := range answers {
		switch {
		case err == nil:
			won++
		case acpclient.IsCode(err, acpwire.CodeRequestSettled):
			settled++
		default:
			t.Fatalf("an answer failed unexpectedly: %v", err)
		}
	}
	if won != 1 || settled != 1 {
		t.Fatalf("answers = %d won, %d settled, want exactly one of each", won, settled)
	}
}

// answerApproval answers an approval as one viewer.
func answerApproval(sess acpclient.Session, key string) error {
	return sess.AnswerApproval(context.Background(), key, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce})
}

// TestAViewersSheetIsItsOwn pins W2's subscription rule: a sheet update reaches
// only the viewer that read the sheet.
func TestAViewersSheetIsItsOwn(t *testing.T) {
	h := newViewerHarness(t)
	a := h.openViewer(t)
	b := h.openViewer(t)
	sa := attachViewer(t, a, "s1")
	_ = attachViewer(t, b, "s1")

	// A's read subscribes A's sheet forwarder; B never reads one.
	_ = sa.Sheet(0)

	h.state.AddMessage(session.RoleUser, "a message that moves the sheet", session.ContentTypePlain)
	waitFor(t, "A's sheet update", func() bool {
		return a.sawText(`"kind":"sheet"`)
	})
	if b.sawText(`"kind":"sheet"`) {
		t.Fatal("B received a sheet update, but B never read a sheet")
	}
}

// TestAViewerLeavingLeavesTheOthersServed pins decision 1's independence: one
// viewer hanging up does not stop the other's updates.
func TestAViewerLeavingLeavesTheOthersServed(t *testing.T) {
	h := newViewerHarness(t)
	a := h.openViewer(t)
	b := h.openViewer(t)
	_ = attachViewer(t, a, "s1")
	sb := attachViewer(t, b, "s1")

	a.close()

	if err := sb.SetNotice(context.Background(), acpwireNotice("still-served")); err != nil {
		t.Fatalf("B's SetNotice after A hung up: %v", err)
	}
	waitFor(t, "B's update after A hung up", func() bool {
		return b.sawText("still-served")
	})
}

// TestAViewerStaysOutOfTheCancellerChain pins decision 1's cancellation rule:
// serving viewers adds nothing to the host's chain.
func TestAViewerStaysOutOfTheCancellerChain(t *testing.T) {
	h := newViewerHarness(t)
	before := h.cancellers()

	for i := 0; i < 2; i++ {
		v := h.openViewer(t)
		_ = attachViewer(t, v, "s1")
		v.close()
	}

	if after := h.cancellers(); after != before {
		t.Fatalf("the canceller chain holds %d entries after serving two viewers, want %d", after, before)
	}
}

// TestCloseStopsTheStackLoops pins decision 5: Close drops every session's
// stacks, stopping their idle loops, and a fence for the session then returns
// at once.
func TestCloseStopsTheStackLoops(t *testing.T) {
	state := seamState(t, controlSession)
	broker := pubsub.NewBroker[session.Event]()
	state.SetEventBroker(broker)
	turns := NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: id,
				BeginWork: identityBeginWork,
				Run:       RunnerFunc(func(context.Context, string) error { return nil }),
				Events:    broker,
				State:     state,
			}, id == controlSession
		},
		Notify: func(string, any) error { return nil },
	})

	// A stack read starts the session's idle loop.
	if _, err := turns.Stack(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err != nil {
		t.Fatalf("session/stack: %v", err)
	}
	turns.stacksMu.Lock()
	started := len(turns.stacks)
	turns.stacksMu.Unlock()
	if started == 0 {
		t.Fatal("session/stack started no stack projector")
	}

	turns.Close()

	turns.stacksMu.Lock()
	left := len(turns.stacks)
	turns.stacksMu.Unlock()
	if left != 0 {
		t.Fatalf("Close left %d stack projectors, want none", left)
	}

	// The idle loop's fence went with its stack, so a fence returns at once.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	turns.fences.wait(ctx, controlSession)
}

// TestViewerMaxFrameBytesIsTheClientsCap pins decision 8: the viewer's cap is
// P3's frame cap, with no import of the client in production code.
func TestViewerMaxFrameBytesIsTheClientsCap(t *testing.T) {
	if viewerMaxFrameBytes != rpc.MaxFrameBytes {
		t.Fatalf("viewerMaxFrameBytes = %d, want rpc.MaxFrameBytes (%d)", viewerMaxFrameBytes, rpc.MaxFrameBytes)
	}
}

// shortTempDir is a temporary directory whose path is short enough for a unix
// socket path on macOS (about 104 bytes).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "acp")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// socketViewer is one viewer connected over the real unix socket.
type socketViewer struct {
	conn   net.Conn
	client *rpc.Conn
}

// dialViewerSocket dials path as a marshal client and leaves the connection
// open until the test ends.
func dialViewerSocket(t *testing.T, path string) *socketViewer {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	client := rpc.NewConn(rpc.NewStream(conn, conn, rpc.MaxFrameBytes), rpc.Handlers{})
	t.Cleanup(func() {
		_ = client.Close()
		_ = conn.Close()
	})
	return &socketViewer{conn: conn, client: client}
}

// initializeErr runs the marshal handshake and reports its error.
func (v *socketViewer) initializeErr() error {
	var res acpwire.InitializeResult
	return v.client.Call(context.Background(), "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"marshal": map[string]any{"protocolVersion": acpwire.MarshalProtocolVersion},
		},
	}, &res)
}

// TestViewersAreServedAtOnce pins decision 7: over a real socket, two viewer
// connections initialize concurrently beside stdio. On --listen the second
// would wait in the backlog until the first hung up.
func TestViewersAreServedAtOnce(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "viewers.sock")
	ln, err := listenUnix(path)
	if err != nil {
		t.Fatalf("listenUnix: %v", err)
	}

	// stdio stays connected for the whole test.
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runWithViewersConfig(ctx, stdinR, io.Discard, ln, testRunConfig(t)) }()

	first := dialViewerSocket(t, path)
	second := dialViewerSocket(t, path)

	errs := make(chan error, 2)
	go func() { errs <- first.initializeErr() }()
	go func() { errs <- second.initializeErr() }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("a viewer's initialize: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a viewer's initialize did not answer while the other stayed connected")
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runWithViewersConfig did not return after the context ended")
	}
}

// TestTheViewerSocketIsTheOwnersOnly pins decision 7's socket: 0600 in a 0700
// directory, and a stale file at the path is replaced.
func TestTheViewerSocketIsTheOwnersOnly(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "viewers.sock")

	// A file left at the path by a crashed generation must not stop the bind.
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write the stale file: %v", err)
	}

	ln, err := listenUnix(path)
	if err != nil {
		t.Fatalf("listenUnix: %v", err)
	}
	defer ln.Close()

	sock, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the socket: %v", err)
	}
	if got := sock.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 0600", got)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat the directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o, want 0700", got)
	}
}

// TestStdioEndingClosesTheViewers pins decision 7's shutdown: stdio ending
// closes every viewer, removes the socket, and returns once the sessions are
// closed.
func TestStdioEndingClosesTheViewers(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "viewers.sock")
	ln, err := listenUnix(path)
	if err != nil {
		t.Fatalf("listenUnix: %v", err)
	}

	stdinR, stdinW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runWithViewersConfig(ctx, stdinR, io.Discard, ln, testRunConfig(t)) }()

	viewer := dialViewerSocket(t, path)
	if err := viewer.initializeErr(); err != nil {
		t.Fatalf("the viewer's initialize: %v", err)
	}

	// stdio ends: the bridge's own connection goes away.
	_ = stdinW.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runWithViewersConfig: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithViewersConfig did not return after stdio ended")
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket still exists after the run returned: %v", err)
	}

	var res acpwire.SessionStatus
	err = viewer.client.Call(context.Background(), acpwire.MethodStatus, acpwire.StatusParams{SessionID: "s1"}, &res)
	if err == nil {
		t.Fatal("the viewer was still served after stdio ended")
	}
}
