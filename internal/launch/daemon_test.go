package launch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp"
	"marshal/internal/acp/service"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/tui"
	"marshal/internal/daemon"
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
	ctx       context.Context
	cancel    context.CancelFunc

	mu        sync.Mutex
	infoCalls int
	attaches  []fakeAttach
	current   *fakeRuntime
	started   []*fakeRuntime
	lastRT    *app.Runtime
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
	if attachErr != nil {
		f.mu.Unlock()
		return nil, attachErr
	}
	previous := f.current
	f.current = nil
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

	return &daemon.Attachment{SessionID: rt.SessionID, Conn: client}, nil
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

func (f *fakeDaemon) attachCalls() []fakeAttach {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeAttach(nil), f.attaches...)
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

func TestRunStaysEmbeddedForAProjectThatNeedsTrust(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	fake := newFakeDaemon(t, dir, home)

	// A project config with no trust record asks the user a question, and a
	// daemon must never answer it (decision 2).
	writeProjectConfig(t, dir)

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
		t.Errorf("the untrusted project was attached: %+v", calls)
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

// TestAttachRefusesAProjectThatNeedsTrust: attach runs no embedded startup, so
// it cannot ask the trust question. A project whose trust needs asking is
// refused rather than served by the daemon unasked.
func TestAttachRefusesAProjectThatNeedsTrust(t *testing.T) {
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
	if err == nil {
		t.Fatal("Attach served a project that needs the trust question")
	}
	for _, want := range []string{root, "needs trust", "`marshal`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Attach error = %q, want it to name %q", err, want)
		}
	}
	if calls := fake.attachCalls(); len(calls) != 0 {
		t.Errorf("the untrusted project was attached: %+v", calls)
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

func TestALostConnectionNamesTheReattach(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, t.TempDir())

	err := Run(context.Background(), io.Discard,
		WithDaemon("/tmp/marshal-test-daemon.sock"),
		withDaemonClient(fake),
		withWorkingDir(dir),
		WithProgramRunner(func(ctx context.Context, _ tea.Model, _ io.Writer) ProgramResult {
			// The daemon goes away while the program waits for work.
			fake.drop()
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
				t.Error("the program's context never ended after the daemon closed")
			}
			return ProgramResult{}
		}),
	)
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
