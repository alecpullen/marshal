package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acp"
	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/tui"
	"marshal/internal/app/tui/fleet"
	"marshal/internal/app/tui/reconnect"
	"marshal/internal/daemon"
	"marshal/internal/fleetclient"
	"marshal/web/bridge/contract"
)

// fakeAttach is one call the fake daemon saw.
type fakeAttach struct {
	root      string
	sessionID string
}

// fakeRuntime is one runtime the fake daemon started for an attach, and
// everything it must close afterwards.
type fakeRuntime struct {
	embedded *app.Embedded
	emb      *acp.Embedding
	agent    net.Conn
	rt       *app.Runtime
	done     bool
}

func (r *fakeRuntime) close() {
	if r == nil || r.done {
		return
	}
	r.done = true
	if r.agent != nil {
		_ = r.agent.Close()
	}
	if r.emb != nil {
		_ = r.emb.Close(context.Background())
	}
	if r.embedded != nil {
		_ = r.embedded.Close(context.Background())
	}
}

// fakeDaemon is a daemon the tests control: it answers Info from a field and
// serves Attach from a runtime of its own over a pipe, as the real daemon
// relays one.
type fakeDaemon struct {
	t       *testing.T
	dir     string
	home    string
	info    daemon.Info
	infoErr error
	// attachErr makes Attach fail: a daemon that answers the probe but cannot
	// serve a session.
	attachErr error
	// agentAttachErr does the same for AttachAgent, as a refused open does.
	agentAttachErr error
	// nextSessionID, when set, is the session the next serve resumes rather
	// than starting a fresh one: a reconnect lands on the session it left.
	nextSessionID string
	ctx           context.Context
	cancel        context.CancelFunc

	mu            sync.Mutex
	infoCalls     int
	attaches      []fakeAttach
	agentAttaches []string
	current       *fakeRuntime
	started       []*fakeRuntime
	lastRT        *app.Runtime
	// seq numbers the runtimes the fake starts, so each one's clock differs.
	seq int
}

func newFakeDaemon(t *testing.T, dir, home string) *fakeDaemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeDaemon{
		t:    t,
		dir:  dir,
		home: home,
		ctx:  ctx,
		// The local profile is what daemon mode runs on, so a fake that says
		// nothing says local.
		info:   daemon.Info{Profile: "local"},
		cancel: cancel,
	}
	t.Cleanup(func() {
		cancel()
		f.mu.Lock()
		started := append([]*fakeRuntime(nil), f.started...)
		f.mu.Unlock()
		for _, rt := range started {
			rt.close()
		}
	})
	return f
}

func (f *fakeDaemon) Info(ctx context.Context) (daemon.Info, error) {
	f.mu.Lock()
	f.infoCalls++
	f.mu.Unlock()
	if f.infoErr != nil {
		return daemon.Info{}, f.infoErr
	}
	return f.info, nil
}

func (f *fakeDaemon) Attach(ctx context.Context, root, sessionID string) (*daemon.Attachment, error) {
	f.mu.Lock()
	f.attaches = append(f.attaches, fakeAttach{root: root, sessionID: sessionID})
	attachErr := f.attachErr
	f.mu.Unlock()
	return f.serve(attachErr, "")
}

func (f *fakeDaemon) AttachAgent(ctx context.Context, agentID string) (*daemon.Attachment, error) {
	f.mu.Lock()
	f.agentAttaches = append(f.agentAttaches, agentID)
	attachErr := f.agentAttachErr
	f.mu.Unlock()
	return f.serve(attachErr, agentID)
}

// serve builds the runtime an attach runs on and hands back the agent's end of
// it, as the real daemon's child serves a session; agentID marks a fleet
// agent's session.
func (f *fakeDaemon) serve(attachErr error, agentID string) (*daemon.Attachment, error) {
	if attachErr != nil {
		return nil, attachErr
	}
	f.mu.Lock()
	previous := f.current
	f.current = nil
	// A test can name the session the next serve resumes: a reconnect must land
	// on the session it left, and each serve otherwise mints its own from its
	// attach's clock.
	resume := f.nextSessionID
	f.nextSessionID = ""
	// Each attach's runtime gets its own clock: the session ID derives from it,
	// so a shared clock would make a second attach collide with the first
	// session's row.
	f.seq++
	seq := f.seq
	f.mu.Unlock()
	// One runtime at a time, as one project's child serves its sessions:
	// nothing else in these tests keeps two open on the same database.
	previous.close()

	appOpts := append(pipeAppOptions(f.dir, f.home),
		app.WithNow(func() time.Time { return pipeNow.Add(time.Duration(seq) * time.Second) }),
	)
	if resume != "" {
		appOpts = append(appOpts, app.WithExistingSession(resume))
	}
	embedded, err := app.NewEmbedded(f.ctx, appOpts...)
	if err != nil {
		return nil, err
	}
	rt, err := embedded.Start(f.ctx)
	if err != nil {
		_ = embedded.Close(context.Background())
		return nil, err
	}

	host, sess := service.New(embedded.SessionDeps(f.ctx))
	emb, err := acp.NewEmbedding(acp.EmbeddingConfig{Logger: loggerOf(rt), MaxFrameBytes: rpc.MaxFrameBytes})
	if err != nil {
		_ = embedded.Close(context.Background())
		return nil, err
	}
	if err := emb.Adopt(rt, host, sess); err != nil {
		_ = emb.Close(context.Background())
		_ = embedded.Close(context.Background())
		return nil, err
	}

	client, agent := net.Pipe()
	current := &fakeRuntime{embedded: embedded, emb: emb, agent: agent, rt: rt}
	go func() { _ = emb.Serve(f.ctx, agent) }()

	f.mu.Lock()
	f.current = current
	f.started = append(f.started, current)
	f.lastRT = rt
	f.mu.Unlock()

	return &daemon.Attachment{SessionID: rt.SessionID, AgentID: agentID, Conn: client}, nil
}

// drop closes the fake's end of the pipe, as a daemon that goes away does.
func (f *fakeDaemon) drop() {
	f.mu.Lock()
	current := f.current
	f.mu.Unlock()
	if current != nil && current.agent != nil {
		_ = current.agent.Close()
	}
}

// serveSessionNext makes the next serve report the session ID it names: a
// reconnect lands on the session it left rather than opening a new one, and
// each serve otherwise mints its own from its attach's clock.
func (f *fakeDaemon) serveSessionNext(sessionID string) {
	f.mu.Lock()
	f.nextSessionID = sessionID
	f.mu.Unlock()
}

// refuseAttaches makes every later attach fail with err, as a daemon that has
// gone away does: a reconnect's dial cannot land.
func (f *fakeDaemon) refuseAttaches(err error) {
	f.mu.Lock()
	f.attachErr = err
	f.agentAttachErr = err
	f.mu.Unlock()
}

// restarting sends marshal/restarting on the session's stream, as the bridge
// does before it closes it, and leaves the pipe open: the caller drops it.
func (f *fakeDaemon) restarting(t *testing.T, reason string) {
	t.Helper()
	f.mu.Lock()
	current := f.current
	f.mu.Unlock()
	if current == nil || current.agent == nil {
		t.Fatal("the fake has no connection to send marshal/restarting on")
	}
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  acpwire.MethodRestarting,
		"params":  acpwire.RestartingParams{Reason: reason},
	})
	if err != nil {
		t.Fatalf("encode marshal/restarting: %v", err)
	}
	if _, err := current.agent.Write(append(frame, '\n')); err != nil {
		t.Fatalf("send marshal/restarting: %v", err)
	}
}

func (f *fakeDaemon) attachCalls() []fakeAttach {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeAttach(nil), f.attaches...)
}

// agentAttachCalls is the agents AttachAgent was asked for, in order.
func (f *fakeDaemon) agentAttachCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.agentAttaches...)
}

func (f *fakeDaemon) infoCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.infoCalls
}

func (f *fakeDaemon) lastRuntime() *app.Runtime {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastRT
}

// daemonModeRun runs Run in daemon mode over a fake serving dir, and returns
// what the program's model was built from.
func daemonModeRun(t *testing.T, fake *fakeDaemon, dir, home string, opts ...Option) (tui.Model, error) {
	t.Helper()
	var stdout bytes.Buffer
	var model tui.Model
	all := append([]Option{
		WithDaemon("/tmp/marshal-test-daemon.sock"),
		withDaemonClient(fake),
		withWorkingDir(dir),
		WithProgramRunner(func(ctx context.Context, m tea.Model, output io.Writer) ProgramResult {
			model = m.(tui.Model)
			return ProgramResult{}
		}),
	}, opts...)
	err := Run(context.Background(), &stdout, all...)
	return model, err
}

func TestRunUsesTheDaemonWhenItAnswers(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	fake := newFakeDaemon(t, dir, home)

	loaderCalled := false
	model, err := daemonModeRun(t, fake, dir, home,
		WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			loaderCalled = true
			return config.Default(), nil
		})),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	sess, ok := model.Session().(*rpc.Session)
	if !ok {
		t.Fatalf("the daemon's model session is %T, want *rpc.Session", model.Session())
	}
	if want := fake.lastRuntime().SessionID; sess.ID() != want {
		t.Errorf("model session ID = %q, want the daemon's %q", sess.ID(), want)
	}
	if _, ok := model.Host().(*rpc.Host); !ok {
		t.Errorf("the daemon's model host is %T, want *rpc.Host", model.Host())
	}

	calls := fake.attachCalls()
	if len(calls) != 1 {
		t.Fatalf("attaches = %d, want 1: %+v", len(calls), calls)
	}
	if want := filepath.Clean(dir); calls[0].root != want {
		t.Errorf("attach root = %q, want %q", calls[0].root, want)
	}
	if calls[0].sessionID != "" {
		t.Errorf("attach session ID = %q, want empty for a new session", calls[0].sessionID)
	}
	if loaderCalled {
		t.Error("Run started an embedded runtime in daemon mode")
	}
}

func TestRunStaysEmbeddedWithoutADaemon(t *testing.T) {
	t.Run("the daemon errors", func(t *testing.T) {
		fake := newFakeDaemon(t, t.TempDir(), t.TempDir())
		fake.infoErr = errors.New("connect: no such file or directory")
		runEmbeddedBeside(t, fake)
	})

	t.Run("the daemon is another profile", func(t *testing.T) {
		fake := newFakeDaemon(t, t.TempDir(), t.TempDir())
		fake.info = daemon.Info{Profile: "server"}
		runEmbeddedBeside(t, fake)
	})
}

// runEmbeddedBeside runs Run over a daemon that must not serve it, and pins
// that the embedded runtime ran and the fake never attached.
func runEmbeddedBeside(t *testing.T, fake *fakeDaemon) {
	t.Helper()
	dir := t.TempDir()
	home := t.TempDir()

	served := false
	err := Run(context.Background(), io.Discard,
		WithApp(pipeAppOptions(dir, home)...),
		WithDaemon("/tmp/marshal-test-daemon.sock"),
		withDaemonClient(fake),
		withWorkingDir(dir),
		withServed(func(*service.Host, *service.Session) { served = true }),
		WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult { return ProgramResult{} }),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !served {
		t.Error("Run did not run embedded")
	}
	if calls := fake.attachCalls(); len(calls) != 0 {
		t.Errorf("the fake was asked to attach: %+v", calls)
	}
}

// writeProjectConfig gives dir trust-gated project config, which makes the
// project's trust a question until a record is written for it.
func writeProjectConfig(t *testing.T, dir string) {
	t.Helper()
	projectCfg := config.ProjectConfigPath(dir)
	if err := os.MkdirAll(filepath.Dir(projectCfg), 0o755); err != nil {
		t.Fatalf("create the project config directory: %v", err)
	}
	if err := os.WriteFile(projectCfg, []byte("[project]\nname = \"trust-me\"\n"), 0o644); err != nil {
		t.Fatalf("write the project config: %v", err)
	}
}

// TestAnUntrustedProjectRunsOnTheDaemon: the trust question belongs to the
// daemon's session now, so a project whose trust needs asking runs on the
// daemon like any other.
func TestAnUntrustedProjectRunsOnTheDaemon(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	fake := newFakeDaemon(t, dir, home)

	writeProjectConfig(t, dir)

	loaderCalled := false
	model, err := daemonModeRun(t, fake, dir, home,
		WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			loaderCalled = true
			return config.Default(), nil
		})),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := model.Session().(*rpc.Session); !ok {
		t.Fatalf("the daemon's model session is %T, want *rpc.Session", model.Session())
	}
	if loaderCalled {
		t.Error("Run started an embedded runtime for a project that needs trust")
	}
	if calls := fake.attachCalls(); len(calls) != 1 {
		t.Fatalf("attaches = %d, want 1: %+v", len(calls), calls)
	}
}

func TestRunWithoutTheOptionNeverDials(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	fake := newFakeDaemon(t, dir, home)

	served := false
	err := Run(context.Background(), io.Discard,
		WithApp(pipeAppOptions(dir, home)...),
		withDaemonClient(fake),
		withWorkingDir(dir),
		withServed(func(*service.Host, *service.Session) { served = true }),
		WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult { return ProgramResult{} }),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !served {
		t.Error("Run did not run embedded")
	}
	if n := fake.infoCount(); n != 0 {
		t.Errorf("the daemon was probed %d times without WithDaemon", n)
	}
}

func TestAttachWithoutADaemonFails(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	fake.infoErr = errors.New("connect: no such file or directory")

	const socket = "/tmp/marshal-test-no-daemon.sock"
	err := Attach(context.Background(), io.Discard, socket, "",
		withDaemonClient(fake),
		withWorkingDir(dir),
		WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult { return ProgramResult{} }),
	)
	if err == nil {
		t.Fatal("Attach succeeded with no daemon")
	}
	for _, want := range []string{socket, "marshal daemon install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Attach error = %q, want it to name %q", err, want)
		}
	}
}

func TestAttachWithNoIDTakesTheMostRecentSession(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	root := filepath.Clean(dir)
	fake.info = daemon.Info{
		Profile: "local",
		Children: []daemon.ChildInfo{
			{Root: "/some/other/project", Sessions: []daemon.SessionInfo{{ID: "elsewhere"}}},
			{Root: root, Sessions: []daemon.SessionInfo{{ID: "most-recent"}, {ID: "older"}}},
		},
	}

	err := Attach(context.Background(), io.Discard, "/tmp/marshal-test-daemon.sock", "",
		withDaemonClient(fake),
		withWorkingDir(root),
		WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult { return ProgramResult{} }),
	)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	calls := fake.attachCalls()
	if len(calls) != 1 {
		t.Fatalf("attaches = %d, want 1: %+v", len(calls), calls)
	}
	if calls[0].root != root {
		t.Errorf("attach root = %q, want %q", calls[0].root, root)
	}
	if calls[0].sessionID != "most-recent" {
		t.Errorf("attach session ID = %q, want %q", calls[0].sessionID, "most-recent")
	}
}

func TestAttachWithNoSessionFails(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Clean(dir)

	cases := map[string][]daemon.ChildInfo{
		"a child with no session": {
			{Root: root},
		},
		"no child for the root": {
			{Root: "/some/other/project", Sessions: []daemon.SessionInfo{{ID: "elsewhere"}}},
		},
	}
	for name, children := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFakeDaemon(t, dir, t.TempDir())
			fake.info = daemon.Info{Profile: "local", Children: children}

			err := Attach(context.Background(), io.Discard, "/tmp/marshal-test-daemon.sock", "",
				withDaemonClient(fake),
				withWorkingDir(root),
				WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult { return ProgramResult{} }),
			)
			if err == nil {
				t.Fatal("Attach succeeded with no daemon session")
			}
			for _, want := range []string{"no daemon session for", root, "`marshal` starts one"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Attach error = %q, want it to name %q", err, want)
				}
			}
		})
	}
}

// TestAttachServesAnUntrustedProject: attach has no trust gate of its own —
// the daemon session asks its TUI — so a project whose trust needs asking
// still probes the daemon and attaches.
func TestAttachServesAnUntrustedProject(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	writeProjectConfig(t, dir)
	root := filepath.Clean(dir)
	fake.info = daemon.Info{
		Profile:  "local",
		Children: []daemon.ChildInfo{{Root: root, Sessions: []daemon.SessionInfo{{ID: "existing"}}}},
	}

	err := Attach(context.Background(), io.Discard, "/tmp/marshal-test-daemon.sock", "",
		withDaemonClient(fake),
		withWorkingDir(root),
		WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult { return ProgramResult{} }),
	)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if n := fake.infoCount(); n != 1 {
		t.Errorf("attach probed the daemon %d times, want 1", n)
	}
	calls := fake.attachCalls()
	if len(calls) != 1 {
		t.Fatalf("attaches = %d, want 1: %+v", len(calls), calls)
	}
	if calls[0].root != root {
		t.Errorf("attach root = %q, want %q", calls[0].root, root)
	}
	if calls[0].sessionID != "existing" {
		t.Errorf("attach session ID = %q, want %q", calls[0].sessionID, "existing")
	}
}

// TestAttachReturnsTheContextsError: an attach whose context is already done is
// a failure, not a silent success that exits zero with no output.
func TestAttachReturnsTheContextsError(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Attach(ctx, io.Discard, "/tmp/marshal-test-daemon.sock", "",
		withDaemonClient(fake),
		withWorkingDir(dir),
		WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult { return ProgramResult{} }),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Attach error = %v, want context.Canceled", err)
	}
	if n := fake.infoCount(); n != 0 {
		t.Errorf("a cancelled attach probed the daemon %d times, want none", n)
	}
}

// TestAServingFailureNamesTheDaemonAndTheWayOut: a daemon that answers the
// probe but cannot serve a session leaves the user with the socket to look at
// and both ways forward.
func TestAServingFailureNamesTheDaemonAndTheWayOut(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	fake.attachErr = &daemon.AttachError{Status: 503, Message: "the child is not up yet"}

	const socket = "/tmp/marshal-test-serving.sock"
	err := Run(context.Background(), io.Discard,
		WithDaemon(socket),
		withDaemonClient(fake),
		withWorkingDir(dir),
		WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult { return ProgramResult{} }),
	)
	if err == nil {
		t.Fatal("Run succeeded although the daemon could not serve a session")
	}
	for _, want := range []string{socket, "503", "`marshal attach`", "`marshal --embedded`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Run error = %q, want it to name %q", err, want)
		}
	}
}

func TestADetachPrintsTheReattachCommand(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	fake := newFakeDaemon(t, dir, home)

	var stdout bytes.Buffer
	err := Run(context.Background(), &stdout,
		WithDaemon("/tmp/marshal-test-daemon.sock"),
		withDaemonClient(fake),
		withWorkingDir(dir),
		WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult {
			return ProgramResult{Detached: "detached-session"}
		}),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "Detached from session detached-session. `marshal attach detached-session` reattaches."
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}
}

func TestAResumeReattachesToTheChosenSession(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())

	pass := 0
	err := Run(context.Background(), io.Discard,
		WithDaemon("/tmp/marshal-test-daemon.sock"),
		withDaemonClient(fake),
		withWorkingDir(dir),
		WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult {
			pass++
			if pass == 1 {
				return ProgramResult{ResumeSession: "x"}
			}
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := fake.attachCalls()
	if len(calls) != 2 {
		t.Fatalf("attaches = %d, want 2: %+v", len(calls), calls)
	}
	if calls[0].sessionID != "" {
		t.Errorf("first attach session ID = %q, want empty", calls[0].sessionID)
	}
	if calls[1].sessionID != "x" {
		t.Errorf("second attach session ID = %q, want %q", calls[1].sessionID, "x")
	}
}

// TestALostConnectionNamesTheReattach: a connection that closes and does not
// come back reports the loss with the command that reattaches, once the
// reconnect's short window has passed.
func TestALostConnectionNamesTheReattach(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())

	script := &loopScript{t: t}
	script.onSession = func(ctx context.Context, m tui.Model) tui.Model {
		// The daemon goes away, and does not answer again.
		fake.refuseAttaches(errors.New("connect: connection refused"))
		fake.drop()
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Error("the program's context never ended after the daemon closed")
		}
		return m
	}
	err := runOnTheDaemon(t, fake, dir, io.Discard, script,
		loopFleet(filepath.Clean(dir), "sess-row"),
		withReconnect(120*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond))
	if err == nil {
		t.Fatal("Run succeeded after the daemon's connection closed")
	}
	sessionID := fake.lastRuntime().SessionID
	for _, want := range []string{"marshal attach", sessionID, "connection closed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Run error = %q, want it to name %q", err, want)
		}
	}
}

func TestAVersionSkewWritesOneNote(t *testing.T) {
	notesFor := func(t *testing.T, opts ...Option) []string {
		t.Helper()
		dir := t.TempDir()
		fake := newFakeDaemon(t, dir, t.TempDir())
		_, err := daemonModeRun(t, fake, dir, t.TempDir(), opts...)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		rt := fake.lastRuntime()
		if rt == nil {
			t.Fatal("the fake served no runtime")
		}
		var notes []string
		for _, msg := range rt.State.Messages() {
			if strings.Contains(msg.Content, "Restart the daemon after upgrading") {
				notes = append(notes, msg.Content)
			}
		}
		return notes
	}

	t.Run("a different version", func(t *testing.T) {
		notes := notesFor(t, withAgentVersion("other"))
		if len(notes) != 1 {
			t.Fatalf("notes = %d, want exactly one: %v", len(notes), notes)
		}
		for _, want := range []string{"other", "dev"} {
			if !strings.Contains(notes[0], want) {
				t.Errorf("note = %q, want it to name %q", notes[0], want)
			}
		}
	})

	t.Run("the same version", func(t *testing.T) {
		notes := notesFor(t)
		if len(notes) != 0 {
			t.Errorf("notes = %v, want none when the versions agree", notes)
		}
	})
}

func TestTheClientLogIsTheOwnersOnly(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fake := newFakeDaemon(t, dir, home)
	if _, err := daemonModeRun(t, fake, dir, home); err != nil {
		t.Fatalf("Run: %v", err)
	}

	path := filepath.Join(config.DaemonDir(home), "tui.log")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %04o, want 0600", got)
	}
}

const (
	// loopAgentID and loopAgentName are the agent the fleet screen opens in
	// the loop's tests.
	loopAgentID   = "agent-7"
	loopAgentName = "scout"
	// loopDecoyID is the agent the fleet's cursor lands on when the loop gives
	// it no selection to restore: the first row, so a selection is told apart
	// from the default.
	loopDecoyID = "agent-0"
	// loopSocket is the socket the loop's tests pretend a daemon answers on.
	loopSocket = "/tmp/marshal-test-loop.sock"
)

// fleetPass is one run of the loop's fleet program, and what the loop gave it.
type fleetPass struct {
	selected string
	screen   string
}

// loopScript is the program runner the loop's tests inject: it switches on the
// model's type, records what the loop gave each program, and answers from its
// script — one result per pass, the zero result past the script's end.
type loopScript struct {
	t *testing.T
	// onSession runs inside a session pass before it returns its result: a
	// test drops the connection there, or types into the screen. It returns
	// the model the pass ends with, which is where the loop reads the draft
	// from, as runProgram does.
	onSession func(ctx context.Context, m tui.Model) tui.Model
	// onFleet runs inside a fleet pass before it returns its result.
	onFleet func(ctx context.Context, m fleet.Model)
	// onWait runs inside a waiting pass before it returns its result: a test
	// watches the attempts arrive there.
	onWait func(ctx context.Context, m reconnect.Model)

	fleetEnds   []ProgramResult
	sessionEnds []ProgramResult
	waitEnds    []ProgramResult

	fleetPasses []fleetPass
	sessions    []tui.Model
	waits       []reconnect.Model
}

func (s *loopScript) run(ctx context.Context, m tea.Model, _ io.Writer) ProgramResult {
	s.t.Helper()
	switch model := m.(type) {
	case fleet.Model:
		if s.onFleet != nil {
			s.onFleet(ctx, model)
		}
		s.fleetPasses = append(s.fleetPasses, fleetPass{
			selected: fleetSelection(s.t, model),
			screen:   fleetScreen(model),
		})
		return nextResult(&s.fleetEnds)
	case reconnect.Model:
		if s.onWait != nil {
			s.onWait(ctx, model)
		}
		s.waits = append(s.waits, model)
		return nextResult(&s.waitEnds)
	case tui.Model:
		if s.onSession != nil {
			model = s.onSession(ctx, model)
		}
		s.sessions = append(s.sessions, model)
		res := nextResult(&s.sessionEnds)
		// Decision 5: runProgram reads the screen's draft off the model it
		// ended with, so the script does too.
		res.Draft = model.Draft()
		return res
	default:
		s.t.Fatalf("the daemon loop ran a %T", m)
		return ProgramResult{}
	}
}

// nextResult is the script's next result: the zero result past its end.
func nextResult(script *[]ProgramResult) ProgramResult {
	if len(*script) == 0 {
		return ProgramResult{}
	}
	next := (*script)[0]
	*script = (*script)[1:]
	return next
}

// fleetSelection is the row the fleet program's cursor starts on. The start
// selection lands the first time the screen lays its rows out, which a filter
// opened and closed does without moving the cursor or opening anything.
func fleetSelection(t *testing.T, m fleet.Model) string {
	t.Helper()
	for _, key := range []tea.KeyPressMsg{
		{Code: '/', Text: "/"}, // open the filter
		{Code: tea.KeyEsc},     // clear it, and lay the rows out again
	} {
		next, _ := m.Update(key)
		got, ok := next.(fleet.Model)
		if !ok {
			t.Fatalf("the fleet model updated to %T, want fleet.Model", next)
		}
		m = got
	}
	return m.Selected()
}

// fleetScreen is the fleet program's screen at a usual terminal size, so a test
// can read the Events lines the loop wrote to it.
func fleetScreen(m fleet.Model) string {
	next, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	return next.View().Content
}

// loopFleet is the fake fleet the loop's tests inject: it lists the agent the
// loop opens after a decoy row, and the session its targets name, so the
// screen's selection has a row to land on and a different row to start from.
func loopFleet(root, sessionID string) *fakeFleet {
	f := newFakeFleet()
	f.state = acpclient.FleetState{
		Server: "local",
		Conn:   acpclient.FleetConnected,
		Loaded: true,
		Epoch:  1,
		Agents: []contract.AgentStatus{
			{ID: loopDecoyID, Project: root, Name: "decoy", Status: contract.AgentRunning},
			{ID: loopAgentID, Project: root, Name: loopAgentName, Status: contract.AgentRunning},
		},
		Inbox: contract.Inbox{Groups: contract.InboxGroups{
			Running: []string{loopDecoyID},
			Earlier: []string{loopAgentID},
		}},
		Daemon: &contract.DaemonInfo{
			Profile:  "local",
			Children: []contract.DaemonChild{{Root: root, Sessions: []contract.DaemonSession{{ID: sessionID}}}},
		},
	}
	return f
}

// runOnTheDaemon runs Run in daemon mode with the scripted programs.
func runOnTheDaemon(t *testing.T, fake *fakeDaemon, dir string, stdout io.Writer, script *loopScript, client *fakeFleet, opts ...Option) error {
	t.Helper()
	return Run(context.Background(), stdout,
		append([]Option{
			WithDaemon(loopSocket),
			withDaemonClient(fake),
			withWorkingDir(dir),
			withFleetClient(client),
			WithProgramRunner(script.run),
		}, opts...)...,
	)
}

// runTheFleet runs Fleet with the scripted programs, as `marshal fleet` does.
func runTheFleet(t *testing.T, fake *fakeDaemon, dir string, stdout io.Writer, script *loopScript, client *fakeFleet, opts ...Option) error {
	t.Helper()
	return Fleet(context.Background(), stdout, loopSocket,
		append([]Option{
			withDaemonClient(fake),
			withWorkingDir(dir),
			withFleetClient(client),
			WithProgramRunner(script.run),
		}, opts...)...,
	)
}

// TestTheFleetOpensAnAgentAndComesBack: the fleet opens an agent, the session
// screen comes up marked as that agent's, quitting it returns to the fleet with
// the cursor on the agent, and quitting there ends the loop.
func TestTheFleetOpensAnAgentAndComesBack(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	root := filepath.Clean(dir)

	script := &loopScript{
		t: t,
		fleetEnds: []ProgramResult{
			{Open: fleet.Target{AgentID: loopAgentID, AgentName: loopAgentName, Root: root}},
			{}, // the zero target ends the loop
		},
		sessionEnds: []ProgramResult{{}}, // a plain end of the session screen
	}
	if err := runTheFleet(t, fake, dir, io.Discard, script, loopFleet(root, "sess-plain")); err != nil {
		t.Fatalf("Fleet: %v", err)
	}

	if len(script.sessions) != 1 {
		t.Fatalf("session screens = %d, want 1", len(script.sessions))
	}
	if got := script.sessions[0].AgentSession(); got != loopAgentID {
		t.Errorf("the session screen is agent %q's, want %q", got, loopAgentID)
	}
	if calls := fake.agentAttachCalls(); len(calls) != 1 || calls[0] != loopAgentID {
		t.Errorf("agent attaches = %v, want exactly %q", calls, loopAgentID)
	}
	if calls := fake.attachCalls(); len(calls) != 0 {
		t.Errorf("the loop opened project sessions: %+v", calls)
	}
	if len(script.fleetPasses) != 2 {
		t.Fatalf("fleet screens = %d, want 2", len(script.fleetPasses))
	}
	if got := script.fleetPasses[0].selected; got != loopDecoyID {
		t.Errorf("the first fleet screen selected %q, want its first row %q: the loop restored nothing", got, loopDecoyID)
	}
	if got := script.fleetPasses[1].selected; got != loopAgentID {
		t.Errorf("the fleet came back with %q selected, want the agent %q", got, loopAgentID)
	}
}

// TestTheFleetOpensADaemonSession: a session row opens that session of that
// project through the project attach.
func TestTheFleetOpensADaemonSession(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	root := filepath.Clean(dir)

	script := &loopScript{
		t: t,
		fleetEnds: []ProgramResult{
			{Open: fleet.Target{Root: root, SessionID: "sess-row"}},
			{},
		},
		sessionEnds: []ProgramResult{{}},
	}
	if err := runTheFleet(t, fake, dir, io.Discard, script, loopFleet(root, "sess-row")); err != nil {
		t.Fatalf("Fleet: %v", err)
	}

	calls := fake.attachCalls()
	if len(calls) != 1 {
		t.Fatalf("attaches = %d, want 1: %+v", len(calls), calls)
	}
	if calls[0].root != root {
		t.Errorf("attach root = %q, want %q", calls[0].root, root)
	}
	if calls[0].sessionID != "sess-row" {
		t.Errorf("attach session ID = %q, want %q", calls[0].sessionID, "sess-row")
	}
	if n := len(fake.agentAttachCalls()); n != 0 {
		t.Errorf("agent attaches = %d, want none", n)
	}
}

// TestDetachingAnOpenedSessionReturnsToTheFleet: /detach on a session the fleet
// opened goes back to the fleet instead of printing the reattach command.
func TestDetachingAnOpenedSessionReturnsToTheFleet(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	root := filepath.Clean(dir)

	var stdout bytes.Buffer
	script := &loopScript{
		t: t,
		fleetEnds: []ProgramResult{
			{Open: fleet.Target{AgentID: loopAgentID, AgentName: loopAgentName, Root: root}},
			{},
		},
		sessionEnds: []ProgramResult{{Detached: "sess-detached"}},
	}
	if err := runTheFleet(t, fake, dir, &stdout, script, loopFleet(root, "sess-row")); err != nil {
		t.Fatalf("Fleet: %v", err)
	}

	if len(script.fleetPasses) != 2 {
		t.Fatalf("fleet screens = %d, want 2: a detach on an opened session returns to the fleet", len(script.fleetPasses))
	}
	if got := script.fleetPasses[1].selected; got != loopAgentID {
		t.Errorf("the fleet came back with %q selected, want the agent %q", got, loopAgentID)
	}
	if out := stdout.String(); strings.Contains(out, "Detached from session") {
		t.Errorf("stdout = %q, want no detach message once the fleet has run", out)
	}
}

// TestSlashFleetFromAPlainSessionOpensTheFleet: /fleet on the daemon hands the
// loop a session step that ends with ToFleet, and the fleet program runs next
// with its cursor on the session that ran.
func TestSlashFleetFromAPlainSessionOpensTheFleet(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	root := filepath.Clean(dir)
	fleetClient := loopFleet(root, "sess-row")

	script := &loopScript{
		t:           t,
		sessionEnds: []ProgramResult{{ToFleet: true}},
	}
	// The daemon mints the session's ID when it attaches, so the roster the
	// fleet will show is only known inside the session pass: list the session
	// the loop really ran, as the daemon's own fleet does. Doing it here leaves
	// the cursor something to restore onto that is not the roster's first row.
	script.onSession = func(_ context.Context, m tui.Model) tui.Model {
		id := fake.lastRuntime().SessionID
		fleetClient.state.Daemon.Children[0].Sessions = []contract.DaemonSession{{ID: id}}
		return m
	}
	if err := runOnTheDaemon(t, fake, dir, io.Discard, script, fleetClient); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(script.sessions) != 1 {
		t.Fatalf("session screens = %d, want 1", len(script.sessions))
	}
	if len(script.fleetPasses) != 1 {
		t.Fatalf("fleet screens = %d, want the one /fleet asked for", len(script.fleetPasses))
	}
	if n := len(fake.attachCalls()); n != 1 {
		t.Errorf("attaches = %d, want 1", n)
	}
	ran := fake.lastRuntime().SessionID
	if got := script.fleetPasses[0].selected; got != ran {
		t.Errorf("the fleet came up with %q selected, want the session that ran, %q (the first row is %q)", got, ran, loopDecoyID)
	}
}

// TestAFleetProgramsErrorEndsTheLoop: a fleet screen that ends with an error and
// no target ends the loop with that error, as every program's ending does,
// rather than coming back to a fleet nobody asked for.
func TestAFleetProgramsErrorEndsTheLoop(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	root := filepath.Clean(dir)

	boom := errors.New("the fleet screen failed")
	script := &loopScript{t: t, fleetEnds: []ProgramResult{{Err: boom}}}
	err := runTheFleet(t, fake, dir, io.Discard, script, loopFleet(root, "sess-row"))
	if !errors.Is(err, boom) {
		t.Fatalf("Fleet = %v, want the fleet program's error", err)
	}
	if len(script.sessions) != 0 {
		t.Errorf("session screens = %d, want none", len(script.sessions))
	}
	if n := len(fake.attachCalls()) + len(fake.agentAttachCalls()); n != 0 {
		t.Errorf("%d attaches, want none: the zero target opens nothing", n)
	}
}

// TestAResumeFromTheFleetOpensThatSession: a resume wins over the return to the
// fleet even once the fleet has run, so the step after a fleet session is the
// resumed one, not the fleet screen.
func TestAResumeFromTheFleetOpensThatSession(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	root := filepath.Clean(dir)

	stop := errors.New("the resumed session ended")
	script := &loopScript{
		t: t,
		fleetEnds: []ProgramResult{
			{Open: fleet.Target{AgentID: loopAgentID, AgentName: loopAgentName, Root: root}},
			{}, // ends the loop, should it come back to the fleet
		},
		sessionEnds: []ProgramResult{{ResumeSession: "sess-resumed"}, {Err: stop}},
	}
	err := runTheFleet(t, fake, dir, io.Discard, script, loopFleet(root, "sess-row"))
	if !errors.Is(err, stop) {
		t.Fatalf("Fleet = %v, want the resumed session's ending %v", err, stop)
	}

	calls := fake.attachCalls()
	if len(calls) != 1 || calls[0].sessionID != "sess-resumed" {
		t.Fatalf("attaches = %+v, want one to %q", calls, "sess-resumed")
	}
	if len(script.sessions) != 2 {
		t.Fatalf("session screens = %d, want the fleet's own and the resumed one", len(script.sessions))
	}
	if len(script.fleetPasses) != 1 {
		t.Errorf("fleet screens = %d, want 1: a resume does not return to the fleet", len(script.fleetPasses))
	}
}

// TestALostConnectionReturnsToTheFleetWithANotice: once the fleet has run, a
// daemon that goes away is an Events line on the fleet screen, not the end of
// the loop.
func TestALostConnectionReturnsToTheFleetWithANotice(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	root := filepath.Clean(dir)

	script := &loopScript{
		t: t,
		fleetEnds: []ProgramResult{
			{Open: fleet.Target{AgentID: loopAgentID, AgentName: loopAgentName, Root: root}},
			{},
		},
	}
	script.onSession = func(ctx context.Context, m tui.Model) tui.Model {
		// The daemon goes away while the screen waits for work, and never
		// comes back: the reconnect's short window ends the loss.
		fake.refuseAttaches(errors.New("connect: connection refused"))
		fake.drop()
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Error("the screen's context never ended after the daemon closed")
		}
		return m
	}
	if err := runTheFleet(t, fake, dir, io.Discard, script, loopFleet(root, "sess-row"),
		withReconnect(120*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond)); err != nil {
		t.Fatalf("Fleet: %v", err)
	}

	if len(script.fleetPasses) != 2 {
		t.Fatalf("fleet screens = %d, want 2: a lost connection returns to the fleet", len(script.fleetPasses))
	}
	if got := script.fleetPasses[1].screen; !strings.Contains(got, "lost the connection to "+loopAgentName) {
		t.Errorf("the fleet's Events do not name the lost connection:\n%s", got)
	}
}

// TestARefusedOpenReturnsToTheFleet: an agent the daemon refuses to open sends
// the loop back to the fleet with the reason on its Events ring.
func TestARefusedOpenReturnsToTheFleet(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	fake.agentAttachErr = &daemon.AttachError{Status: 409, Message: "the agent has no session to view"}
	root := filepath.Clean(dir)

	script := &loopScript{
		t: t,
		fleetEnds: []ProgramResult{
			{Open: fleet.Target{AgentID: loopAgentID, AgentName: loopAgentName, Root: root}},
			{},
		},
	}
	if err := runTheFleet(t, fake, dir, io.Discard, script, loopFleet(root, "sess-row")); err != nil {
		t.Fatalf("Fleet: %v", err)
	}

	if len(script.sessions) != 0 {
		t.Fatalf("session screens = %d, want none: the open was refused", len(script.sessions))
	}
	if len(script.fleetPasses) != 2 {
		t.Fatalf("fleet screens = %d, want 2: a refused open returns to the fleet", len(script.fleetPasses))
	}
	if got := script.fleetPasses[1].screen; !strings.Contains(got, "could not open "+loopAgentName) {
		t.Errorf("the fleet's Events do not say the open was refused:\n%s", got)
	}
	if n := len(fake.agentAttachCalls()); n != 1 {
		t.Errorf("agent attaches = %d, want the one refused attempt", n)
	}
}

// TestBeforeTheFleetEndingsAreUnchanged: a loop that has not run the fleet
// behaves exactly as it did — the detach message, the plain end, the resume,
// and the lost connection's error.
func TestBeforeTheFleetEndingsAreUnchanged(t *testing.T) {
	t.Run("a detach prints the reattach command and ends the loop", func(t *testing.T) {
		dir := t.TempDir()
		fake := newFakeDaemon(t, dir, t.TempDir())

		var stdout bytes.Buffer
		script := &loopScript{t: t, sessionEnds: []ProgramResult{{Detached: "sess-gone"}}}
		if err := runOnTheDaemon(t, fake, dir, &stdout, script, loopFleet(dir, "sess-row")); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(script.fleetPasses) != 0 {
			t.Errorf("fleet screens = %d, want none before the fleet has run", len(script.fleetPasses))
		}
		if want := "Detached from session sess-gone. `marshal attach sess-gone` reattaches."; !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
		}
	})

	t.Run("an end ends the loop", func(t *testing.T) {
		dir := t.TempDir()
		fake := newFakeDaemon(t, dir, t.TempDir())

		script := &loopScript{t: t}
		if err := runOnTheDaemon(t, fake, dir, io.Discard, script, loopFleet(dir, "sess-row")); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(script.fleetPasses) != 0 {
			t.Errorf("fleet screens = %d, want none before the fleet has run", len(script.fleetPasses))
		}
	})

	t.Run("a resume reattaches to the chosen session", func(t *testing.T) {
		dir := t.TempDir()
		fake := newFakeDaemon(t, dir, t.TempDir())

		script := &loopScript{t: t, sessionEnds: []ProgramResult{{ResumeSession: "chosen"}}}
		if err := runOnTheDaemon(t, fake, dir, io.Discard, script, loopFleet(dir, "sess-row")); err != nil {
			t.Fatalf("Run: %v", err)
		}
		calls := fake.attachCalls()
		if len(calls) != 2 {
			t.Fatalf("attaches = %d, want 2: %+v", len(calls), calls)
		}
		if calls[1].sessionID != "chosen" {
			t.Errorf("the resumed attach names session %q, want %q", calls[1].sessionID, "chosen")
		}
		if len(script.fleetPasses) != 0 {
			t.Errorf("fleet screens = %d, want none before the fleet has run", len(script.fleetPasses))
		}
	})

	t.Run("a lost connection is the loop's error", func(t *testing.T) {
		dir := t.TempDir()
		fake := newFakeDaemon(t, dir, t.TempDir())

		script := &loopScript{t: t}
		script.onSession = func(ctx context.Context, m tui.Model) tui.Model {
			// The daemon goes away and nothing serves the session again: the
			// reconnect's short window ends it, and the loss is the loop's
			// error as it was before the fleet had run.
			fake.refuseAttaches(errors.New("connect: connection refused"))
			fake.drop()
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
				t.Error("the screen's context never ended after the daemon closed")
			}
			return m
		}
		err := runOnTheDaemon(t, fake, dir, io.Discard, script, loopFleet(dir, "sess-row"),
			withReconnect(120*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond))
		if err == nil {
			t.Fatal("Run succeeded after the daemon's connection closed")
		}
		for _, want := range []string{"marshal attach", "connection closed"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Run error = %q, want it to name %q", err, want)
			}
		}
		if len(script.fleetPasses) != 0 {
			t.Errorf("fleet screens = %d, want none before the fleet has run", len(script.fleetPasses))
		}
	})
}

// fleetWiring is one program's wiring: the fleet client and the watch the loop
// handed it (decision 8).
type fleetWiring struct {
	client acpclient.Fleet
	watch  *fleet.Watch
}

// recordFleetWiring is the option that records what each program was given, in
// the order the loop ran them.
func recordFleetWiring(into *[]fleetWiring) Option {
	return withFleetWiring(func(c acpclient.Fleet, w *fleet.Watch) {
		*into = append(*into, fleetWiring{client: c, watch: w})
	})
}

// waitingFleet is loopFleet's state with one agent queued: what a session
// screen counts.
func waitingFleet(root, sessionID string) *fakeFleet {
	f := loopFleet(root, sessionID)
	f.state.Inbox.Queue = []contract.InboxItem{{Kind: contract.InboxItemAgent, AgentID: "a2"}}
	return f
}

// sessionView is the model's screen at a usual terminal size, stripped of its
// styling, so a test reads what the user would.
func sessionView(t *testing.T, m tui.Model) string {
	t.Helper()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	return ansi.Strip(updated.View().Content)
}

// TestADaemonSessionGetsTheFleet: the loop's fleet client starts with the loop,
// so a plain `marshal`'s session screen counts from its first frame, and it has
// no watch, because the fleet has not run: notifications run wherever the fleet
// is open (decision 8).
func TestADaemonSessionGetsTheFleet(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	client := waitingFleet(filepath.Clean(dir), "sess-row")

	var (
		wiring []fleetWiring
		view   string
	)
	script := &loopScript{t: t, sessionEnds: []ProgramResult{{}}}
	script.onSession = func(_ context.Context, m tui.Model) tui.Model {
		view = sessionView(t, m)
		return m
	}

	if err := runOnTheDaemon(t, fake, dir, io.Discard, script, client, recordFleetWiring(&wiring)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(script.sessions) != 1 {
		t.Fatalf("session screens = %d, want 1", len(script.sessions))
	}
	if len(wiring) != 1 {
		t.Fatalf("programs given a fleet = %d, want the one session screen", len(wiring))
	}
	if wiring[0].client != acpclient.Fleet(client) {
		t.Errorf("the session screen's fleet client is %T, want the loop's %T", wiring[0].client, client)
	}
	if wiring[0].watch != nil {
		t.Error("a plain marshal's session screen was given a watch: notifications run wherever the fleet is open")
	}
	if !strings.Contains(view, "needs you 1") {
		t.Errorf("the session screen shows no count of the queued agent:\n%s", view)
	}
	if len(script.fleetPasses) != 0 {
		t.Errorf("fleet screens = %d, want none: the loop never opened the fleet", len(script.fleetPasses))
	}
}

// TestTheWatchStartsWithTheFleet: the watch is created with the first fleet
// program and handed to every program after it, so the session screen the fleet
// opens rings the bell through the same watch (decision 8).
func TestTheWatchStartsWithTheFleet(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	root := filepath.Clean(dir)
	client := loopFleet(root, "sess-row")

	var wiring []fleetWiring
	script := &loopScript{
		t: t,
		fleetEnds: []ProgramResult{
			{Open: fleet.Target{AgentID: loopAgentID, AgentName: loopAgentName, Root: root}},
			{}, // the zero target ends the loop
		},
		sessionEnds: []ProgramResult{{}},
	}
	if err := runTheFleet(t, fake, dir, io.Discard, script, client, recordFleetWiring(&wiring)); err != nil {
		t.Fatalf("Fleet: %v", err)
	}

	// The fleet, the session it opened, then the fleet again.
	if len(wiring) != 3 {
		t.Fatalf("programs given a fleet = %d, want 3", len(wiring))
	}
	if wiring[0].watch == nil {
		t.Fatal("the first fleet program ran with no watch")
	}
	if wiring[2].watch != wiring[0].watch {
		t.Error("the fleet came back with another watch, want the loop's")
	}
	if wiring[1].watch != wiring[0].watch {
		t.Error("the session screen after the fleet has another watch, want the loop's")
	}
	for i, w := range wiring {
		if w.client != acpclient.Fleet(client) {
			t.Errorf("program %d's fleet client is %T, want the loop's %T", i, w.client, client)
		}
	}
}

// TestEmbeddedModeHasNoFleet: embedded mode gets neither the client nor the
// watch (decision 8), so its screen counts nothing even though a fleet would.
func TestEmbeddedModeHasNoFleet(t *testing.T) {
	dir := t.TempDir()
	client := waitingFleet(filepath.Clean(dir), "sess-row")

	var (
		wiring []fleetWiring
		view   string
	)
	err := Run(context.Background(), io.Discard,
		WithApp(pipeAppOptions(dir, t.TempDir())...),
		withFleetClient(client),
		recordFleetWiring(&wiring),
		WithProgramRunner(func(_ context.Context, m tea.Model, _ io.Writer) ProgramResult {
			view = sessionView(t, m.(tui.Model))
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(wiring) != 0 {
		t.Errorf("programs given a fleet = %d, want none in embedded mode", len(wiring))
	}
	if strings.Contains(view, "needs you") {
		t.Errorf("embedded mode's screen counted a fleet:\n%s", view)
	}
}

// TestTheLoopsFleetClientClosesWithIt: the client starts with the loop, not
// with the fleet program, so a loop that only ever shows a session still builds
// one — and closes it when it returns. An injected client is the test's, and is
// not closed.
func TestTheLoopsFleetClientClosesWithIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, home)

	socket := filepath.Join(t.TempDir(), "fleet.sock")
	stream := serveFleetStream(t, socket)

	var wiring []fleetWiring
	script := &loopScript{t: t}
	script.onSession = func(_ context.Context, m tui.Model) tui.Model {
		select {
		case <-stream.opened:
		case <-time.After(10 * time.Second):
			t.Error("the loop's fleet client never reached the bridge")
		}
		return m
	}

	// No client is injected, so the loop builds and owns one.
	if err := Run(context.Background(), io.Discard,
		WithDaemon(socket),
		withDaemonClient(fake),
		withWorkingDir(dir),
		WithProgramRunner(script.run),
		recordFleetWiring(&wiring),
	); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(wiring) != 1 {
		t.Fatalf("programs given a fleet = %d, want the one session screen", len(wiring))
	}
	if _, ok := wiring[0].client.(*fleetclient.Client); !ok {
		t.Errorf("the session screen's fleet client is %T, want the one the loop built", wiring[0].client)
	}
	select {
	case <-stream.closed:
	case <-time.After(10 * time.Second):
		t.Error("the loop did not close the fleet client it built")
	}
}

// notificationRecorder stands in for the system's notifier command: it records
// what would have run and signals the first, so no test spawns osascript or
// notify-send.
type notificationRecorder struct {
	mu     sync.Mutex
	names  []string
	called chan struct{}
	once   sync.Once
}

func newNotificationRecorder() *notificationRecorder {
	return &notificationRecorder{called: make(chan struct{})}
}

func (r *notificationRecorder) run(_ context.Context, name string, _ ...string) error {
	r.mu.Lock()
	r.names = append(r.names, name)
	r.mu.Unlock()
	r.once.Do(func() { close(r.called) })
	return nil
}

func (r *notificationRecorder) commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.names...)
}

// parkedNotifications drives watch through the one transition notificationOf
// reports — an agent parking on an approval — and returns what it passed.
func parkedNotifications(t *testing.T, w *fleet.Watch) []fleet.Notification {
	t.Helper()
	base := acpclient.FleetState{Server: "local", Conn: acpclient.FleetConnected, Loaded: true, Epoch: 1}
	if got := w.Observe(base); len(got) != 0 {
		t.Fatalf("the first state passed %v, want a baseline", got)
	}
	parked := base
	parked.Agents = []contract.AgentStatus{{
		ID:      "a2",
		Name:    "Alpha",
		Status:  contract.AgentAwaitingApproval,
		Pending: &contract.PendingRequest{Kind: contract.PendingApproval},
	}}
	parked.Inbox.Groups.NeedsYou = []string{"a2"}
	return w.Observe(parked)
}

// typeDraft types text into the screen's composer, as the user does, and
// returns the model it leaves and the draft the screen then holds.
func typeDraft(t *testing.T, m tui.Model, text string) (tui.Model, tui.Draft) {
	t.Helper()
	for _, r := range text {
		next, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		typed, ok := next.(tui.Model)
		if !ok {
			t.Fatalf("the screen updated to %T, want tui.Model", next)
		}
		m = typed
	}
	if m.Draft().Empty() {
		t.Fatalf("typing %q left an empty draft", text)
	}
	return m, m.Draft()
}

// clearDraft deletes the composer's text with backspaces, as the user does.
func clearDraft(t *testing.T, m tui.Model, runes int) tui.Model {
	t.Helper()
	for i := 0; i < runes; i++ {
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		typed, ok := next.(tui.Model)
		if !ok {
			t.Fatalf("the screen updated to %T, want tui.Model", next)
		}
		m = typed
	}
	if !m.Draft().Empty() {
		t.Fatalf("the composer still holds a draft after %d backspaces", runes)
	}
	return m
}

// userConfigHome is a home whose user config exists, so a daemon session screen
// opens on its composer rather than the first-run connect panel: the draft
// tests type into it.
func userConfigHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// The config must land in this home, so the ambient overrides cannot send
	// the read elsewhere.
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	path := config.UserConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create the user config directory: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write the user config: %v", err)
	}
	return home
}

// TestALostConnectionReconnectsToTheSameSession: a connection that closes is
// reconnected to the session it left — not to a new one — with the waiting
// screen between the two session screens (decisions 2 and 4).
func TestALostConnectionReconnectsToTheSameSession(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())
	root := filepath.Clean(dir)

	dropped := false
	script := &loopScript{t: t}
	script.onSession = func(ctx context.Context, m tui.Model) tui.Model {
		if dropped {
			return m
		}
		dropped = true
		fake.serveSessionNext(m.Session().ID())
		fake.drop()
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Error("the screen's context never ended after the daemon closed")
		}
		return m
	}
	if err := runOnTheDaemon(t, fake, dir, io.Discard, script, loopFleet(root, "sess-row"),
		withReconnect(5*time.Second, 10*time.Millisecond, 50*time.Millisecond)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(script.sessions) != 2 {
		t.Fatalf("session screens = %d, want 2: the reconnect runs the session again", len(script.sessions))
	}
	if len(script.waits) != 1 {
		t.Fatalf("waiting screens = %d, want the one between them", len(script.waits))
	}
	left := script.sessions[0].Session().ID()
	if got := script.sessions[1].Session().ID(); got != left {
		t.Errorf("the reconnected screen runs session %q, want the session it left, %q", got, left)
	}
	calls := fake.attachCalls()
	if len(calls) != 2 {
		t.Fatalf("attaches = %d, want the first and the reconnect: %+v", len(calls), calls)
	}
	if calls[0].sessionID != "" {
		t.Errorf("the first attach names session %q, want a new one", calls[0].sessionID)
	}
	if calls[1].sessionID != left {
		t.Errorf("the reconnect attached %q, want the session it left, %q", calls[1].sessionID, left)
	}
}

// TestAReconnectedScreenHasTheDraft: the draft the screen ended with comes back
// with the reconnected screen, before any update (decision 5).
func TestAReconnectedScreenHasTheDraft(t *testing.T) {
	home := userConfigHome(t)
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, home)
	root := filepath.Clean(dir)

	var typed tui.Draft
	dropped := false
	script := &loopScript{t: t}
	script.onSession = func(ctx context.Context, m tui.Model) tui.Model {
		if dropped {
			return m
		}
		dropped = true
		var screen tui.Model
		screen, typed = typeDraft(t, m, "keep me")
		fake.serveSessionNext(m.Session().ID())
		fake.drop()
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Error("the screen's context never ended after the daemon closed")
		}
		return screen
	}
	if err := runOnTheDaemon(t, fake, dir, io.Discard, script, loopFleet(root, "sess-row"),
		withReconnect(5*time.Second, 10*time.Millisecond, 50*time.Millisecond)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(script.sessions) != 2 {
		t.Fatalf("session screens = %d, want 2", len(script.sessions))
	}
	// The second screen reports the draft as it stands, before any update.
	got := script.sessions[1].Draft()
	if got.Empty() {
		t.Fatal("the reconnected screen has no draft: the session's draft was not kept")
	}
	if !reflect.DeepEqual(got, typed) {
		t.Error("the reconnected screen's draft is not the one the session kept")
	}
}

// TestAReturnFromTheFleetHasTheDraft: a screen that ends with /fleet and a draft
// leaves the draft for the session, and the screen the fleet opens has it
// (decision 5).
func TestAReturnFromTheFleetHasTheDraft(t *testing.T) {
	home := userConfigHome(t)
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, home)
	root := filepath.Clean(dir)

	var typed tui.Draft
	pass := 0
	script := &loopScript{t: t, sessionEnds: []ProgramResult{{ToFleet: true}, {}}}
	script.onSession = func(_ context.Context, m tui.Model) tui.Model {
		pass++
		screen := m
		if pass == 1 {
			screen, typed = typeDraft(t, m, "keep me")
			script.fleetEnds = []ProgramResult{
				{Open: fleet.Target{Root: root, SessionID: m.Session().ID()}},
				{},
			}
		}
		fake.serveSessionNext(m.Session().ID())
		return screen
	}
	if err := runOnTheDaemon(t, fake, dir, io.Discard, script, loopFleet(root, "sess-row")); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(script.sessions) != 2 {
		t.Fatalf("session screens = %d, want the one the fleet opened again", len(script.sessions))
	}
	if got := script.sessions[1].Draft(); !reflect.DeepEqual(got, typed) {
		t.Error("the screen the fleet opened does not have the draft its session kept")
	}
}

// TestAnEmptyDraftClearsTheEntry: a screen that ends with nothing in its
// composer clears the session's entry, so the next screen opens without it
// (decision 5).
func TestAnEmptyDraftClearsTheEntry(t *testing.T) {
	home := userConfigHome(t)
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, home)
	root := filepath.Clean(dir)

	const text = "keep me"
	pass := 0
	script := &loopScript{t: t, sessionEnds: []ProgramResult{{ToFleet: true}, {ToFleet: true}, {}}}
	script.onSession = func(_ context.Context, m tui.Model) tui.Model {
		pass++
		screen := m
		switch pass {
		case 1:
			screen, _ = typeDraft(t, m, text)
			script.fleetEnds = []ProgramResult{
				{Open: fleet.Target{Root: root, SessionID: m.Session().ID()}},
				{Open: fleet.Target{Root: root, SessionID: m.Session().ID()}},
				{},
			}
		case 2:
			// The screen opened with the draft; the user deletes it before
			// the screen ends.
			if m.Draft().Empty() {
				t.Error("the screen the fleet reopened has no draft")
			}
			screen = clearDraft(t, m, len(text))
		}
		fake.serveSessionNext(m.Session().ID())
		return screen
	}
	if err := runOnTheDaemon(t, fake, dir, io.Discard, script, loopFleet(root, "sess-row")); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(script.sessions) != 3 {
		t.Fatalf("session screens = %d, want 3", len(script.sessions))
	}
	if got := script.sessions[2].Draft(); !got.Empty() {
		t.Error("the screen after an empty draft still opened with one")
	}
}

// TestTheReconnectGivesUpAfterItsWindow: a daemon that never serves again ends
// the wait with the window, and the loop reports the loss as it always did
// (decision 7).
func TestTheReconnectGivesUpAfterItsWindow(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())

	var sessionID string
	dropped := false
	script := &loopScript{t: t}
	script.onSession = func(ctx context.Context, m tui.Model) tui.Model {
		if dropped {
			return m
		}
		dropped = true
		sessionID = m.Session().ID()
		fake.refuseAttaches(errors.New("connect: no such file or directory"))
		fake.drop()
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Error("the screen's context never ended after the daemon closed")
		}
		return m
	}
	err := runOnTheDaemon(t, fake, dir, io.Discard, script,
		loopFleet(filepath.Clean(dir), "sess-row"),
		withReconnect(120*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond))
	if err == nil {
		t.Fatal("Run succeeded although the daemon never served the session again")
	}
	for _, want := range []string{"marshal attach", sessionID} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Run error = %q, want it to name %q", err, want)
		}
	}
	if len(script.waits) != 1 {
		t.Errorf("waiting screens = %d, want 1", len(script.waits))
	}
}

// TestARefusedReconnectStopsAtOnce: a refusal with a 4xx status ends the
// attempts at once, so the fake is asked for the session exactly once more
// (decision 3).
func TestARefusedReconnectStopsAtOnce(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())

	dropped := false
	script := &loopScript{t: t}
	script.onSession = func(ctx context.Context, m tui.Model) tui.Model {
		if dropped {
			return m
		}
		dropped = true
		fake.refuseAttaches(&daemon.AttachError{Status: 409, Message: "the session is gone"})
		fake.drop()
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Error("the screen's context never ended after the daemon closed")
		}
		return m
	}
	// A long window: the refusal, not the window, has to end the attempts.
	err := runOnTheDaemon(t, fake, dir, io.Discard, script,
		loopFleet(filepath.Clean(dir), "sess-row"),
		withReconnect(10*time.Second, 10*time.Millisecond, 50*time.Millisecond))
	if err == nil {
		t.Fatal("Run succeeded after the reconnect was refused")
	}
	if calls := fake.attachCalls(); len(calls) != 2 {
		t.Fatalf("attaches = %d, want the first and exactly one retry: %+v", len(calls), calls)
	}
	if len(script.waits) != 1 {
		t.Errorf("waiting screens = %d, want 1", len(script.waits))
	}
}

// TestGivingUpStopsTheAttempts: when the waiting screen ends because the user
// gave up, no attempt follows it (decision 2).
func TestGivingUpStopsTheAttempts(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())

	dropped := false
	script := &loopScript{t: t, waitEnds: []ProgramResult{{GaveUp: true}}}
	script.onSession = func(ctx context.Context, m tui.Model) tui.Model {
		if dropped {
			return m
		}
		dropped = true
		fake.refuseAttaches(errors.New("connect: connection refused"))
		fake.drop()
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Error("the screen's context never ended after the daemon closed")
		}
		return m
	}
	script.onWait = func(context.Context, reconnect.Model) {
		// Let one retry be asked for before the user gives up.
		deadline := time.Now().Add(5 * time.Second)
		for len(fake.attachCalls()) < 2 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	err := runOnTheDaemon(t, fake, dir, io.Discard, script,
		loopFleet(filepath.Clean(dir), "sess-row"),
		withReconnect(10*time.Second, 100*time.Millisecond, 100*time.Millisecond))
	if err == nil {
		t.Fatal("Run succeeded after the user gave up the wait")
	}
	if len(script.waits) != 1 {
		t.Fatalf("waiting screens = %d, want 1", len(script.waits))
	}
	stopped := len(fake.attachCalls())
	// The window has not ended, so an attempt that was not stopped would
	// arrive within the wait between retries.
	time.Sleep(300 * time.Millisecond)
	if got := len(fake.attachCalls()); got != stopped {
		t.Errorf("attaches = %d after the give-up, want the %d it stopped at", got, stopped)
	}
}

// TestTheRestartReasonReachesTheWaitingScreen: the reason the bridge sends
// before it closes the stream is what the waiting screen says (decision 4).
func TestTheRestartReasonReachesTheWaitingScreen(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())

	dropped := false
	script := &loopScript{t: t, sessionEnds: []ProgramResult{{}, {}}}
	script.onSession = func(ctx context.Context, m tui.Model) tui.Model {
		if dropped {
			return m
		}
		dropped = true
		fake.restarting(t, "upgrade")
		fake.drop()
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Error("the screen's context never ended after the daemon closed")
		}
		return m
	}
	if err := runOnTheDaemon(t, fake, dir, io.Discard, script, loopFleet(filepath.Clean(dir), "sess-row"),
		withReconnect(5*time.Second, 10*time.Millisecond, 50*time.Millisecond)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(script.waits) != 1 {
		t.Fatalf("waiting screens = %d, want 1", len(script.waits))
	}
	screen := ansi.Strip(script.waits[0].View().Content)
	if !strings.Contains(screen, "is restarting: upgrade") {
		t.Errorf("the waiting screen does not say why the connection closed:\n%s", screen)
	}
}

// TestTheVersionNoteIsAddedOnce: two attaches of one session with a differing
// version add one note, not one per attach (decision 6).
func TestTheVersionNoteIsAddedOnce(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())

	dropped := false
	script := &loopScript{t: t, sessionEnds: []ProgramResult{{}, {}}}
	script.onSession = func(ctx context.Context, m tui.Model) tui.Model {
		if dropped {
			return m
		}
		dropped = true
		fake.serveSessionNext(m.Session().ID())
		fake.drop()
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Error("the screen's context never ended after the daemon closed")
		}
		return m
	}
	if err := runOnTheDaemon(t, fake, dir, io.Discard, script, loopFleet(filepath.Clean(dir), "sess-row"),
		withAgentVersion("other"),
		withReconnect(5*time.Second, 10*time.Millisecond, 50*time.Millisecond)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	rt := fake.lastRuntime()
	if rt == nil {
		t.Fatal("the fake served no runtime")
	}
	var notes []string
	for _, msg := range rt.State.Messages() {
		if strings.Contains(msg.Content, "Restart the daemon after upgrading") {
			notes = append(notes, msg.Content)
		}
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %d across two attaches, want one: %v", len(notes), notes)
	}
}

// TestNotifyOffKeepsTheBell: WithNotify(false) builds the watch with a nil
// notifier, so the bell still rings for a transition and nothing else happens;
// on is the default, and runs the system's notifier where there is one.
func TestNotifyOffKeepsTheBell(t *testing.T) {
	systemHasNotifier := runtime.GOOS == "darwin" || runtime.GOOS == "linux"

	// runFleet runs the fleet program and answers its watch and the notifier
	// command runner it was built with.
	runFleet := func(t *testing.T, on bool) (*fleet.Watch, *notificationRecorder) {
		t.Helper()
		dir := t.TempDir()
		fake := newFakeDaemon(t, dir, t.TempDir())
		recorder := newNotificationRecorder()
		var wiring []fleetWiring
		script := &loopScript{t: t}
		opts := []Option{
			recordFleetWiring(&wiring),
			withNotifyRun(recorder.run),
			WithNotify(on),
		}
		if err := runTheFleet(t, fake, dir, io.Discard, script, loopFleet(filepath.Clean(dir), "sess-row"), opts...); err != nil {
			t.Fatalf("Fleet: %v", err)
		}
		if len(wiring) != 1 || wiring[0].watch == nil {
			t.Fatalf("the fleet program's wiring is %+v, want the loop's watch", wiring)
		}
		return wiring[0].watch, recorder
	}

	t.Run("off, the bell still rings", func(t *testing.T) {
		watch, recorder := runFleet(t, false)

		if got := parkedNotifications(t, watch); len(got) == 0 {
			t.Fatal("the watch passed nothing for a parked agent: the bell would not ring")
		}
		if names := recorder.commands(); len(names) != 0 {
			t.Errorf("notifications off ran %v, want nothing but the bell", names)
		}
	})

	t.Run("on by default", func(t *testing.T) {
		watch, recorder := runFleet(t, true)

		if got := parkedNotifications(t, watch); len(got) == 0 {
			t.Fatal("the watch passed nothing for a parked agent: the bell would not ring")
		}
		if !systemHasNotifier {
			// A system without a desktop notifier does nothing at all
			// (internal/app/notify's decision 7), so there is no command to
			// expect.
			return
		}
		select {
		case <-recorder.called:
		case <-time.After(10 * time.Second):
			t.Error("notifications on ran no desktop notification")
		}
	})
}
