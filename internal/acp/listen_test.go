package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// dialAndInitialize opens a connection, sends initialize, and returns the
// decoded result.
func dialAndInitialize(t *testing.T, addr string) map[string]any {
	t.Helper()
	conn, err := net.Dial("unix", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var frame struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	return frame.Result
}

func testRunConfig(t *testing.T) runConfig {
	t.Helper()
	return runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return nil, fmt.Errorf("no runtime in tests")
		},
		shutdown: time.Second,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestListenAndServeCutsIdleConnections(t *testing.T) {
	// Unix socket paths are limited to ~104 bytes on macOS, so use a short
	// directory rather than t.TempDir() (whose path is far longer).
	dir, err := os.MkdirTemp("", "acp")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)
	addr := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	cfg := testRunConfig(t)
	cfg.idleTimeout = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- listenAndServeWithConfig(ctx, ln, cfg)
	}()

	// A client that connects and then goes silent must not hold the
	// accept loop hostage: the idle deadline closes its connection.
	silent, err := net.Dial("unix", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer silent.Close()
	_ = silent.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1)
	if _, err := silent.Read(buf); err == nil {
		t.Fatal("silent connection stayed open past the idle deadline")
	}

	// The accept loop must be free again: a fresh, chatty client is
	// served immediately.
	if got := dialAndInitialize(t, addr); got["protocolVersion"] == nil {
		t.Fatalf("post-idle connection: no protocolVersion in %v", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("listenAndServeWithConfig did not return after cancel")
	}
}

func TestListenAndServeAcceptsSuccessiveConnections(t *testing.T) {
	// Unix socket paths are limited to ~104 bytes on macOS, so use a short
	// directory rather than t.TempDir() (whose path is far longer).
	dir, err := os.MkdirTemp("", "acp")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)
	addr := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- listenAndServeWithConfig(ctx, ln, testRunConfig(t))
	}()

	// First connection: initialize, then hang up.
	if got := dialAndInitialize(t, addr); got["protocolVersion"] == nil {
		t.Fatalf("first connection: no protocolVersion in %v", got)
	}

	// The host must survive the hangup and accept a second connection.
	if got := dialAndInitialize(t, addr); got["protocolVersion"] == nil {
		t.Fatalf("second connection: no protocolVersion in %v", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("listenAndServeWithConfig did not return after cancel")
	}
}

// servedPair is one host serving a control socket and a viewer socket beside
// it, as a container's agent does (decisions 4 and 5).
type servedPair struct {
	control string
	viewers string

	done   chan error
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

// stop ends the run and returns its error. It is safe to call more than once.
func (p *servedPair) stop() error {
	p.once.Do(func() {
		p.cancel()
		select {
		case p.err = <-p.done:
		case <-time.After(5 * time.Second):
			p.err = errors.New("listenAndServeWithConfig did not return after cancel")
		}
	})
	return p.err
}

// serveControlAndViewers binds both sockets in a short directory and serves
// them over one host, the viewers on the container allowlist. The run ends
// with the test.
func serveControlAndViewers(t *testing.T, cfg runConfig) *servedPair {
	t.Helper()
	dir := shortTempDir(t)
	control := filepath.Join(dir, "agent.sock")
	viewers := filepath.Join(dir, "viewers.sock")

	ln, err := net.Listen("unix", control)
	if err != nil {
		t.Fatalf("listen on %s: %v", control, err)
	}
	viewerLn, err := listenUnix(viewers)
	if err != nil {
		t.Fatalf("listenUnix on %s: %v", viewers, err)
	}
	cfg.viewers = viewerLn
	cfg.restrictViewers = true

	ctx, cancel := context.WithCancel(context.Background())
	pair := &servedPair{control: control, viewers: viewers, done: make(chan error, 1), cancel: cancel}
	go func() { pair.done <- listenAndServeWithConfig(ctx, ln, cfg) }()
	t.Cleanup(func() {
		if err := pair.stop(); err != nil {
			t.Errorf("listenAndServeWithConfig: %v", err)
		}
	})
	return pair
}

// TestARestrictedViewerIsRefusedAHostCall pins decisions 2 and 4: a marshal
// client on the listener's viewer socket is refused a host call with the
// sentence, and the control connection's same call is not refused.
func TestARestrictedViewerIsRefusedAHostCall(t *testing.T) {
	pair := serveControlAndViewers(t, testRunConfig(t))
	params := json.RawMessage(`{"sessionId":"s1","path":"project.name","value":"x"}`)

	viewer := dialViewerSocket(t, pair.viewers)
	if err := viewer.initializeErr(); err != nil {
		t.Fatalf("the viewer's initialize: %v", err)
	}
	var res json.RawMessage
	err := viewer.client.Call(context.Background(), acpwire.MethodSetValue, params, &res)
	if want := "config/set_value is not available for a containerized agent"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("config/set_value on the viewer = %v, want %q", err, want)
	}

	// The control connection is nobody's viewer: the same call reaches the
	// handler and fails or succeeds on its own terms, never with the refusal.
	control := dialViewerSocket(t, pair.control)
	if err := control.initializeErr(); err != nil {
		t.Fatalf("the control connection's initialize: %v", err)
	}
	err = control.client.Call(context.Background(), acpwire.MethodSetValue, params, &res)
	if err != nil && strings.Contains(err.Error(), "not available for a containerized agent") {
		t.Fatalf("config/set_value on the control connection = %v, want no container refusal", err)
	}
}

// TestARestrictedViewerReadsTheSession pins decision 5's single host: a session
// the control connection opens is served to a viewer on the allowlist.
func TestARestrictedViewerReadsTheSession(t *testing.T) {
	var mu sync.Mutex
	var state *session.State

	cfg := testRunConfig(t)
	cfg.startRuntime = func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
		st := embeddingState(t, "s1")
		broker := pubsub.NewBroker[session.Event]()
		st.SetEventBroker(broker)
		mu.Lock()
		state = st
		mu.Unlock()
		return &app.Runtime{
			State:       st,
			SessionID:   "s1",
			EventBroker: broker,
			Config:      config.Default(),
			WorkingDir:  t.TempDir(),
		}, nil
	}
	pair := serveControlAndViewers(t, cfg)

	control := dialViewerSocket(t, pair.control)
	if err := control.initializeErr(); err != nil {
		t.Fatalf("the control connection's initialize: %v", err)
	}
	var opened SessionResponse
	newParams := map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}}
	if err := control.client.Call(context.Background(), "session/new", newParams, &opened); err != nil {
		t.Fatalf("session/new on the control connection: %v", err)
	}
	if opened.SessionID != "s1" {
		t.Fatalf("session/new answered %q, want s1", opened.SessionID)
	}

	mu.Lock()
	st := state
	mu.Unlock()
	if st == nil {
		t.Fatal("session/new started no runtime the viewer could read")
	}
	st.AddMessage(session.RoleUser, "hello from the control side", session.ContentTypePlain)

	viewer := dialViewerSocket(t, pair.viewers)
	if err := viewer.initializeErr(); err != nil {
		t.Fatalf("the viewer's initialize: %v", err)
	}

	callCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var status acpwire.SessionStatus
	if err := viewer.client.Call(callCtx, acpwire.MethodStatus, acpwire.StatusParams{SessionID: "s1"}, &status); err != nil {
		t.Fatalf("the viewer's session/status: %v", err)
	}
	if status.WorkingDir == "" {
		t.Fatalf("the viewer's session/status = %+v, want the running session's working directory", status)
	}

	var messages json.RawMessage
	if err := viewer.client.Call(callCtx, acpwire.MethodMessages, acpwire.MessagesParams{SessionID: "s1"}, &messages); err != nil {
		t.Fatalf("the viewer's session/messages: %v", err)
	}
	if !strings.Contains(string(messages), "hello from the control side") {
		t.Fatalf("the viewer read no message: %s", messages)
	}
}

// TestTheControlConnectionStillCutsIdle pins decision 5's control loop: the
// idle deadline still cuts a silent control connection while a viewer
// connected beside it is kept.
func TestTheControlConnectionStillCutsIdle(t *testing.T) {
	cfg := testRunConfig(t)
	cfg.idleTimeout = 100 * time.Millisecond
	pair := serveControlAndViewers(t, cfg)

	viewer := dialViewerSocket(t, pair.viewers)
	if err := viewer.initializeErr(); err != nil {
		t.Fatalf("the viewer's initialize: %v", err)
	}

	// A silent control client must not hold the accept loop hostage.
	silent, err := net.Dial("unix", pair.control)
	if err != nil {
		t.Fatalf("dial %s: %v", pair.control, err)
	}
	defer silent.Close()
	_ = silent.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := silent.Read(make([]byte, 1)); err == nil {
		t.Fatal("silent control connection stayed open past the idle deadline")
	}

	// The accept loop must be free again: a fresh, chatty client is served.
	if got := dialAndInitialize(t, pair.control); got["protocolVersion"] == nil {
		t.Fatalf("post-idle control connection: no protocolVersion in %v", got)
	}

	// The viewer had no idle deadline: it is still served well past the
	// window the control connection was cut in.
	if err := viewer.initializeErr(); err != nil {
		t.Fatalf("the viewer after the control connection's idle window: %v", err)
	}
}

// TestViewersEndWithTheListener pins decision 5's shutdown: cancelling the
// context ends the viewer connections, closes the host, and removes both
// sockets.
func TestViewersEndWithTheListener(t *testing.T) {
	pair := serveControlAndViewers(t, testRunConfig(t))

	viewer := dialViewerSocket(t, pair.viewers)
	if err := viewer.initializeErr(); err != nil {
		t.Fatalf("the viewer's initialize: %v", err)
	}

	if err := pair.stop(); err != nil {
		t.Fatalf("listenAndServeWithConfig: %v", err)
	}

	for _, path := range []string{pair.control, pair.viewers} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the socket %s still exists after the run returned: %v", path, err)
		}
	}

	callCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var status json.RawMessage
	if err := viewer.client.Call(callCtx, acpwire.MethodStatus, acpwire.StatusParams{SessionID: "s1"}, &status); err == nil {
		t.Fatal("the viewer was still served after the run returned")
	}
}
