package daemon_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/daemon"
	"marshal/web/bridge"
)

// daemonHarness is one local-profile daemon inside the test process: a real
// bridge on a unix socket, a workspace holding one temporary project, and the
// client that drives it. The project children it starts are this test binary,
// which TestMain runs as `acp --viewers`.
type daemonHarness struct {
	t      *testing.T
	socket string
	root   string // the project every attach names
	fleet  *bridge.Fleet
	srv    *http.Server
	client *daemon.Client
}

func newDaemonHarness(t *testing.T) *daemonHarness {
	t.Helper()
	// A unix socket path is capped near 104 bytes on macOS, so the directory is
	// short rather than t.TempDir()'s.
	dir, err := os.MkdirTemp("", "p4d")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	root := filepath.Join(dir, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create project: %v", err)
	}
	initProject(t, root)

	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("create state dir: %v", err)
	}

	// A project child inherits this process's environment (transport.Open), so
	// the marker set here is what makes TestMain run it as `acp --viewers`.
	t.Setenv(childEnv, "1")

	ws := bridge.NewWorkspace(filepath.Join(dir, "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	if err := ws.AddProject(root); err != nil {
		t.Fatalf("add project: %v", err)
	}

	bin := os.Getenv("MARSHAL_DAEMON_TEST_MARSHAL")
	if bin == "" {
		if bin, err = os.Executable(); err != nil {
			t.Fatalf("find this test binary: %v", err)
		}
	}
	fleet := bridge.NewFleet(ws, bin, nil, stateDir, bridge.Limits{}, "test", nil, "")
	// The local profile runs its fleet as host processes (P4 D4).
	fleet.SetHostProcessesOnly()

	socket := filepath.Join(dir, "daemon.sock")
	api := bridge.NewServer(fleet, "", "")
	api.SetDaemonInfo(bridge.DaemonInfo{
		Version:   "test",
		Profile:   "local",
		PID:       os.Getpid(),
		StartedAt: time.Now().UnixMilli(),
		Socket:    socket,
	})
	ln, err := bridge.ListenLocal(socket)
	if err != nil {
		fleet.Close()
		t.Fatalf("listen on %s: %v", socket, err)
	}
	srv := &http.Server{Handler: api.SocketHandler()}
	go func() {
		_ = srv.Serve(bridge.PeerChecked(ln, os.Getuid(), slog.New(slog.NewTextHandler(io.Discard, nil)))) //nolint:errcheck // the socket closing is how a test ends
	}()

	h := &daemonHarness{
		t:      t,
		socket: socket,
		root:   root,
		fleet:  fleet,
		srv:    srv,
		client: daemon.NewClient(socket),
	}
	t.Cleanup(h.close)

	// The listener is bound, but a client that arrives before the server accepts
	// waits in the backlog; reading the daemon's answer proves the bridge is
	// serving.
	waitFor(t, func() error {
		_, err := h.client.Info(context.Background())
		return err
	})
	return h
}

// close stops the daemon and every child it started.
func (h *daemonHarness) close() {
	_ = h.srv.Close()
	h.fleet.Close()
}

// initProject makes root a git repository with one commit, so a runtime that
// reads the repository finds one.
func initProject(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=test@example.com", "-c", "user.name=Test",
			"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
}

// attachment is one client's attachment: the daemon's upgraded stream, the
// client's connection over it, and the session it attached to.
type attachment struct {
	conn    *daemon.Attachment
	host    *rpc.Host
	session *rpc.Session
}

// attach asks the daemon for root's session (a new one when id is empty) and
// attaches to it, as a client does.
func (h *daemonHarness) attach(id string) *attachment {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := h.client.Attach(ctx, h.root, id)
	if err != nil {
		h.t.Fatalf("attach to %q: %v", id, err)
	}
	host, err := rpc.Dial(ctx, rpc.NewStream(conn.Conn, conn.Conn, rpc.MaxFrameBytes), rpc.DialOptions{})
	if err != nil {
		_ = conn.Conn.Close()
		h.t.Fatalf("dial the attached stream: %v", err)
	}
	session, err := host.Attach(ctx, conn.SessionID)
	if err != nil {
		_ = host.Close()
		h.t.Fatalf("attach session %s: %v", conn.SessionID, err)
	}
	return &attachment{conn: conn, host: host, session: session}
}

// close ends the client's stream, which detaches it from the daemon.
func (a *attachment) close() { _ = a.host.Close() }

// callCtx bounds one call the test makes.
func callCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestAClientAttachesToANewSession(t *testing.T) {
	h := newDaemonHarness(t)
	a := h.attach("")
	defer a.close()

	if got := a.session.ID(); got != a.conn.SessionID {
		t.Fatalf("attached session %q, the daemon named %q", got, a.conn.SessionID)
	}
	// The status is what session/status answered: the mirror is filled, so the
	// session is live on the child.
	if got := a.session.Status().WorkingDir; got != h.root {
		t.Fatalf("the session's working directory is %q, want %q", got, h.root)
	}

	ctx := callCtx(t)
	const note = "read your writes through the relay"
	if err := a.session.AddNote(ctx, acpwire.Note{Text: note, Role: acpwire.RoleUser}); err != nil {
		t.Fatalf("add note: %v", err)
	}
	page, err := a.session.Messages(ctx, acpwire.MessageQuery{})
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if !carries(page, note) {
		t.Fatalf("the note is not in the messages the call returned: %+v", page.Messages)
	}
}

func TestASessionOutlivesItsViewer(t *testing.T) {
	h := newDaemonHarness(t)
	const note = "left behind for the next viewer"

	a := h.attach("")
	id := a.conn.SessionID
	ctx := callCtx(t)
	if err := a.session.AddNote(ctx, acpwire.Note{Text: note, Role: acpwire.RoleUser}); err != nil {
		t.Fatalf("add note: %v", err)
	}
	a.close()

	// The bridge keeps the session after its viewer has gone, and counts no
	// viewer on it.
	waitFor(t, func() error {
		info, err := h.client.Info(ctx)
		if err != nil {
			return err
		}
		viewers, ok := listing(info, id)
		if !ok {
			return fmt.Errorf("session %s is not listed", id)
		}
		if viewers != 0 {
			return fmt.Errorf("session %s has %d viewers", id, viewers)
		}
		return nil
	})

	again := h.attach(id)
	defer again.close()
	if again.conn.SessionID != id {
		t.Fatalf("the second attach named session %q, want %q", again.conn.SessionID, id)
	}
	page, err := again.session.Messages(ctx, acpwire.MessageQuery{})
	if err != nil {
		t.Fatalf("messages after reattaching: %v", err)
	}
	if !carries(page, note) {
		t.Fatalf("the note did not survive the viewer leaving: %+v", page.Messages)
	}
}

func TestTwoViewersShareASession(t *testing.T) {
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	first := h.attach("")
	defer first.close()
	id := first.conn.SessionID
	second := h.attach(id)
	defer second.close()

	for _, step := range []struct {
		at   *attachment
		note string
	}{
		{first, "the first viewer's note"},
		{second, "the second viewer's note"},
	} {
		if err := step.at.session.AddNote(ctx, acpwire.Note{Text: step.note, Role: acpwire.RoleUser}); err != nil {
			t.Fatalf("add %q: %v", step.note, err)
		}
	}

	// Each viewer reads the other's note: one session, two connections.
	for _, viewer := range []*attachment{first, second} {
		page, err := viewer.session.Messages(ctx, acpwire.MessageQuery{})
		if err != nil {
			t.Fatalf("messages: %v", err)
		}
		for _, note := range []string{"the first viewer's note", "the second viewer's note"} {
			if !carries(page, note) {
				t.Fatalf("%q is missing from a viewer's messages: %+v", note, page.Messages)
			}
		}
	}

	// Both connections are counted on the one session.
	waitFor(t, func() error {
		info, err := h.client.Info(ctx)
		if err != nil {
			return err
		}
		viewers, ok := listing(info, id)
		if !ok {
			return fmt.Errorf("session %s is not listed", id)
		}
		if viewers != 2 {
			return fmt.Errorf("session %s has %d viewers, want 2", id, viewers)
		}
		return nil
	})
}

func TestClosingTheSessionUntracksIt(t *testing.T) {
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	a := h.attach("")
	defer a.close()
	id := a.conn.SessionID
	if err := a.session.Close(ctx); err != nil {
		t.Fatalf("close the session: %v", err)
	}

	// The child stays; its session is gone.
	waitFor(t, func() error {
		info, err := h.client.Info(ctx)
		if err != nil {
			return err
		}
		if len(info.Children) != 1 {
			return fmt.Errorf("the daemon lists %d children, want 1", len(info.Children))
		}
		for _, s := range info.Children[0].Sessions {
			if s.ID == id {
				return fmt.Errorf("session %s is still tracked", id)
			}
		}
		return nil
	})
}

// listing finds id among the daemon's children and returns its viewer count.
func listing(info daemon.Info, id string) (int, bool) {
	for _, child := range info.Children {
		for _, s := range child.Sessions {
			if s.ID == id {
				return s.Viewers, true
			}
		}
	}
	return 0, false
}

// carries reports whether any message holds text.
func carries(page acpwire.MessagePage, text string) bool {
	for _, m := range page.Messages {
		if strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}

// waitFor polls cond until it holds, or two minutes pass.
func waitFor(t *testing.T, cond func() error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	var last error
	for time.Now().Before(deadline) {
		if last = cond(); last == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition did not hold: %v", last)
}
