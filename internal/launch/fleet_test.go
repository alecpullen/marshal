package launch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/app/config"
	"marshal/internal/app/tui"
	"marshal/internal/app/tui/fleet"
	"marshal/internal/app/tui/theme"
	"marshal/internal/credentials"
	"marshal/internal/fleetprofile"
	"marshal/web/bridge/contract"
)

// fakeFleet is a fleet the test owns: it answers State from a field and never
// signals Changed. Fleet neither starts nor closes an injected client, so it
// has no lifecycle of its own. The seam is embedded, so an action this test
// does not set up fails loudly instead of quietly answering nothing.
type fakeFleet struct {
	acpclient.Fleet

	state   acpclient.FleetState
	changed chan struct{}

	// finishes records the finishing actions this test drove. The launch
	// tests drive the fleet's lifecycle, so each answers an empty result.
	finishes []string
}

func newFakeFleet() *fakeFleet {
	return &fakeFleet{
		state:   acpclient.FleetState{Server: "local", Conn: acpclient.FleetConnected},
		changed: make(chan struct{}, 1),
	}
}

func (f *fakeFleet) State() acpclient.FleetState { return f.state }

func (f *fakeFleet) Changed() <-chan struct{} { return f.changed }

func (f *fakeFleet) Diff(_ context.Context, agentID, path string) (contract.DiffResult, error) {
	f.finishes = append(f.finishes, "Diff "+agentID+" "+path)
	return contract.DiffResult{}, nil
}

func (f *fakeFleet) CommitDraft(_ context.Context, agentID string) (contract.CommitDraft, error) {
	f.finishes = append(f.finishes, "CommitDraft "+agentID)
	return contract.CommitDraft{}, nil
}

func (f *fakeFleet) Verify(_ context.Context, agentID string) (contract.GateRecord, error) {
	f.finishes = append(f.finishes, "Verify "+agentID)
	return contract.GateRecord{}, nil
}

func (f *fakeFleet) Merge(_ context.Context, agentID string, req contract.MergeRequest) (contract.MergeResult, error) {
	f.finishes = append(f.finishes, "Merge "+agentID)
	return contract.MergeResult{}, nil
}

func (f *fakeFleet) Exit(_ context.Context, agentID string, req contract.ExitRequest) (contract.ExitResult, error) {
	f.finishes = append(f.finishes, "Exit "+agentID)
	return contract.ExitResult{}, nil
}

func (f *fakeFleet) Discard(_ context.Context, agentID string) error {
	f.finishes = append(f.finishes, "Discard "+agentID)
	return nil
}

// noProgram fails the test if the fleet program runs.
func noProgram(t *testing.T) Option {
	t.Helper()
	return WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult {
		t.Error("the fleet program ran although Fleet should have refused")
		return ProgramResult{}
	})
}

func TestFleetWithoutADaemonFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fake := newFakeDaemon(t, t.TempDir(), home)
	fake.infoErr = errors.New("connect: no such file or directory")

	const socket = "/tmp/marshal-test-fleet-no-daemon.sock"
	err := Fleet(context.Background(), io.Discard, socket,
		withDaemonClient(fake),
		withFleetClient(newFakeFleet()),
		noProgram(t),
	)
	if err == nil {
		t.Fatal("Fleet succeeded with no daemon")
	}
	for _, want := range []string{socket, "marshal daemon install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Fleet error = %q, want it to name %q", err, want)
		}
	}
}

func TestFleetRefusesAServerProfileDaemon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fake := newFakeDaemon(t, t.TempDir(), home)
	fake.info.Profile = "server"

	const socket = "/tmp/marshal-test-fleet-server.sock"
	err := Fleet(context.Background(), io.Discard, socket,
		withDaemonClient(fake),
		withFleetClient(newFakeFleet()),
		noProgram(t),
	)
	if err == nil {
		t.Fatal("Fleet ran on a daemon that is not the local profile")
	}
	for _, want := range []string{socket, "server", "the fleet screen needs the local daemon"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Fleet error = %q, want it to name %q", err, want)
		}
	}
}

func TestFleetRunsTheFleetProgram(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fake := newFakeDaemon(t, t.TempDir(), home)

	var got tea.Model
	err := Fleet(context.Background(), io.Discard, "/tmp/marshal-test-fleet.sock",
		withDaemonClient(fake),
		withFleetClient(newFakeFleet()),
		WithProgramRunner(func(_ context.Context, m tea.Model, _ io.Writer) ProgramResult {
			got = m
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Fleet: %v", err)
	}
	if _, ok := got.(fleet.Model); !ok {
		t.Fatalf("the fleet program's model is %T, want fleet.Model", got)
	}
	if n := fake.infoCount(); n != 1 {
		t.Errorf("Fleet probed the daemon %d times, want 1", n)
	}
	if calls := fake.attachCalls(); len(calls) != 0 {
		t.Errorf("the fleet screen attached a session: %+v", calls)
	}
}

// TestFleetUsesTheUsersTheme pins decision 13: the screen's theme comes from
// the user's [tui] config, read with no project layer.
// TestFleetRunsTheRealClientOverTheSocket checks the production path: with
// no client injected, Fleet builds one over the socket, runs the program with
// it, and closes it when the program returns.
func TestFleetRunsTheRealClientOverTheSocket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fake := newFakeDaemon(t, t.TempDir(), home)

	socket := filepath.Join(t.TempDir(), "fleet.sock")
	var got tea.Model
	err := Fleet(context.Background(), io.Discard, socket,
		withDaemonClient(fake),
		WithProgramRunner(func(_ context.Context, m tea.Model, _ io.Writer) ProgramResult {
			got = m
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Fleet: %v", err)
	}
	if _, ok := got.(fleet.Model); !ok {
		t.Fatalf("the fleet program's model is %T, want fleet.Model", got)
	}
}

// TestFleetSurvivesAUserConfigItCannotRead checks decision 13's failure path:
// a user config that does not parse is logged, and the defaults stand in.
func TestFleetSurvivesAUserConfigItCannotRead(t *testing.T) {
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
	// A theme the file would have applied, and a table that does not close.
	if err := os.WriteFile(path, []byte("[tui\ntheme = \"nord\"\n"), 0o644); err != nil {
		t.Fatalf("write the user config: %v", err)
	}

	fake := newFakeDaemon(t, t.TempDir(), home)

	var got fleet.Model
	err := Fleet(context.Background(), io.Discard, filepath.Join(t.TempDir(), "fleet.sock"),
		withDaemonClient(fake),
		WithProgramRunner(func(_ context.Context, m tea.Model, _ io.Writer) ProgramResult {
			got, _ = m.(fleet.Model)
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Fleet: %v", err)
	}

	// The defaults stand in, so the broken file's theme is not applied.
	cfg := config.Default().TUI
	want := theme.LoadWithConfig(cfg.Theme, cfg.Mode, theme.ParseDepth(cfg.Depth), theme.PaletteOverrides(cfg.Palette))
	if got.Theme().AccentPrimary != want.AccentPrimary {
		t.Errorf("the model's accent is %v, want the default's %v", got.Theme().AccentPrimary, want.AccentPrimary)
	}
	nord, _ := theme.LookupPreset("nord")
	if got.Theme().AccentPrimary == nord.AccentPrimary {
		t.Error("the model applied the theme of a config it could not read")
	}
}

func TestFleetUsesTheUsersTheme(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// The config must land in this home, so the ambient overrides cannot send
	// the read elsewhere.
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	cfgPath := config.UserConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("create the user config directory: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("[tui]\ntheme = \"nord\"\n"), 0o644); err != nil {
		t.Fatalf("write the user config: %v", err)
	}

	fake := newFakeDaemon(t, t.TempDir(), home)

	var got fleet.Model
	err := Fleet(context.Background(), io.Discard, "/tmp/marshal-test-fleet-theme.sock",
		withDaemonClient(fake),
		withFleetClient(newFakeFleet()),
		WithProgramRunner(func(_ context.Context, m tea.Model, _ io.Writer) ProgramResult {
			got, _ = m.(fleet.Model)
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Fleet: %v", err)
	}

	// NO_COLOR and TERM decide which palette resolves, so the expectation is
	// resolved the same way the model resolved it rather than hard-coded.
	want := theme.LoadWithConfig("nord", "", theme.DepthFlat, nil)
	if got.Theme().AccentPrimary != want.AccentPrimary {
		t.Errorf("the model's accent is %v, want nord's %v", got.Theme().AccentPrimary, want.AccentPrimary)
	}
	warm, _ := theme.LookupPreset("warm-sunset")
	if got.Theme().AccentPrimary == warm.AccentPrimary && want.AccentPrimary != warm.AccentPrimary {
		t.Errorf("the model kept the default theme's accent %v, want nord's %v", got.Theme().AccentPrimary, want.AccentPrimary)
	}
}

// fleetStream is just enough of the bridge's HTTP surface for the fleet client
// the loop builds: the snapshot's reads, and a stream it holds open until the
// client hangs up.
type fleetStream struct {
	opened chan struct{} // closed when the stream request arrives
	closed chan struct{} // closed when the client hangs up
}

// serveFleetStream serves fleetStream on the unix socket at path.
func serveFleetStream(t *testing.T, socket string) *fleetStream {
	t.Helper()
	s := &fleetStream{opened: make(chan struct{}), closed: make(chan struct{})}
	var openedOnce, closedOnce sync.Once

	answer := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		openedOnce.Do(func() { close(s.opened) })
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// The stream is the client's request: it ends when the client hangs
		// up, which is what closing the client does.
		<-r.Context().Done()
		closedOnce.Do(func() { close(s.closed) })
	})
	mux.HandleFunc("/api/agents", answer("[]"))
	mux.HandleFunc("/api/inbox", answer("{}"))
	mux.HandleFunc("/api/projects", answer("[]"))
	mux.HandleFunc("/api/budgets", answer("{}"))
	mux.HandleFunc("/api/daemon", answer("{}"))

	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", socket, err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return s
}

// TestTheLoopClosesTheFleetClientItCreated pins decision 7's ownership: the
// client the loop builds over the socket is closed when the loop returns.
func TestTheLoopClosesTheFleetClientItCreated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, home)

	socket := filepath.Join(t.TempDir(), "fleet.sock")
	stream := serveFleetStream(t, socket)

	// No client is injected, so the loop builds and owns one. The program
	// waits for it to be up before quitting, so the stream is demonstrably
	// open when the loop closes it.
	script := &loopScript{t: t}
	script.onFleet = func(context.Context, fleet.Model) {
		select {
		case <-stream.opened:
		case <-time.After(10 * time.Second):
			t.Error("the loop's fleet client never reached the bridge")
		}
	}

	if err := Fleet(context.Background(), io.Discard, socket,
		withDaemonClient(fake),
		withWorkingDir(dir),
		WithProgramRunner(script.run),
	); err != nil {
		t.Fatalf("Fleet: %v", err)
	}

	select {
	case <-stream.closed:
	case <-time.After(10 * time.Second):
		t.Error("the loop did not close the fleet client it built")
	}
}

// serverProfile is the server profile the server fleet's tests run on: a
// loopback http URL — no TLS for one — and a token command, so no test needs a
// keychain or a server that answers.
func serverProfile() fleetprofile.Server {
	return fleetprofile.Server{Name: "studio", URL: "http://127.0.0.1:1", TokenCmd: "cat ~/.marshal/studio.token"}
}

// countingToken is the token_cmd runner the server fleet's tests inject: it
// answers a token, or fails, and counts the times the command ran.
type countingToken struct {
	mu   sync.Mutex
	ran  int
	fail error
}

func (c *countingToken) run(context.Context, string, io.Writer) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ran++
	if c.fail != nil {
		return nil, c.fail
	}
	return []byte("tok\n"), nil
}

func (c *countingToken) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ran
}

// runServerFleet runs ServerFleet over the fake daemon's sessions with the
// scripted programs, as runOnTheDaemon does for the daemon's loop. The token
// command is never a real one: an option that supplies its own runner wins over
// the default below.
func runServerFleet(t *testing.T, fake *fakeDaemon, dir string, script *loopScript, opts ...Option) error {
	t.Helper()
	all := append([]Option{
		withDaemonClient(fake),
		withWorkingDir(dir),
		withTokenRunner((&countingToken{}).run),
		WithProgramRunner(script.run),
	}, opts...)
	return ServerFleet(context.Background(), io.Discard, serverProfile(), all...)
}

// loopFleetScript is the script the server fleet's tests run: one fleet pass
// that opens the loop's agent, and one session that ends the loop.
func loopFleetScript(t *testing.T, dir string) *loopScript {
	t.Helper()
	script := &loopScript{t: t}
	script.fleetEnds = []ProgramResult{
		{Open: fleet.Target{AgentID: loopAgentID, AgentName: loopAgentName, Root: dir}},
	}
	return script
}

// TestAServerFleetResolvesTheTokenOnce pins decision 8's one resolution: the
// token is read before the loop starts and kept, so the fleet, a session and
// the fleet again cost one token command between them.
func TestAServerFleetResolvesTheTokenOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, home)
	token := &countingToken{}

	script := loopFleetScript(t, dir)
	script.fleetEnds = append(script.fleetEnds, ProgramResult{})
	script.sessionEnds = []ProgramResult{{ToFleet: true}}

	if err := runServerFleet(t, fake, dir, script, withTokenRunner(token.run)); err != nil {
		t.Fatalf("ServerFleet: %v", err)
	}
	if len(script.fleetPasses) != 2 || len(script.sessions) != 1 {
		t.Fatalf("the loop ran %d fleet programs and %d sessions, want 2 and 1",
			len(script.fleetPasses), len(script.sessions))
	}
	if got := token.count(); got != 1 {
		t.Fatalf("the token command ran %d times, want once", got)
	}
}

// TestAServerFleetFailsWithoutAToken pins the other half of decision 8: a
// profile that cannot produce a token fails before any program runs, and the
// error says which server.
func TestAServerFleetFailsWithoutAToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	token := &countingToken{fail: errors.New("no such file or directory")}
	err := ServerFleet(context.Background(), io.Discard, serverProfile(),
		withTokenRunner(token.run),
		WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult {
			t.Error("a program ran although the profile has no token")
			return ProgramResult{}
		}),
	)
	if err == nil {
		t.Fatal("ServerFleet succeeded without a token")
	}
	if !strings.Contains(err.Error(), serverProfile().Name) {
		t.Errorf("ServerFleet error = %q, want it to name %q", err, serverProfile().Name)
	}
}

// TestAServerFleetReadsTheTokenFromTheKeychain pins decision 4's other half: a
// profile without token_cmd takes its token from the store withTokenStore
// injects, opened once for the whole loop, and runs no shell.
func TestAServerFleetReadsTheTokenFromTheKeychain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, home)

	profile := fleetprofile.Server{Name: "studio", URL: "http://127.0.0.1:1"}
	store := credentials.NewMemStore()
	if err := store.Set(fleetprofile.KeychainKey(profile.Name), []byte("a-keychain-token")); err != nil {
		t.Fatalf("set the token: %v", err)
	}

	opens := 0
	err := ServerFleet(context.Background(), io.Discard, profile,
		withDaemonClient(fake),
		withWorkingDir(dir),
		withTokenStore(func() (credentials.Store, error) {
			opens++
			return store, nil
		}),
		withTokenRunner(func(context.Context, string, io.Writer) ([]byte, error) {
			t.Error("the token command ran for a profile that has none")
			return nil, nil
		}),
		WithProgramRunner(loopFleetScript(t, dir).run),
	)
	if err != nil {
		t.Fatalf("ServerFleet: %v", err)
	}
	if opens != 1 {
		t.Fatalf("the keychain was opened %d times, want once", opens)
	}
}

// TestAServerFleetDialsWithoutASharedFilesystem pins decision 8's filesystem:
// a server's agent does not read this machine's files, so the session is not
// dialled as one that shares it.
func TestAServerFleetDialsWithoutASharedFilesystem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, home)

	var shared []bool
	script := loopFleetScript(t, dir)
	script.onSession = func(_ context.Context, m tui.Model) tui.Model {
		shared = append(shared, m.Host().SharedFilesystem())
		return m
	}

	if err := runServerFleet(t, fake, dir, script); err != nil {
		t.Fatalf("ServerFleet: %v", err)
	}
	if calls := fake.agentAttachCalls(); len(calls) != 1 || calls[0] != loopAgentID {
		t.Fatalf("agent attaches = %+v, want the fleet's agent %q", calls, loopAgentID)
	}
	if len(shared) != 1 {
		t.Fatalf("the loop opened %d sessions, want one", len(shared))
	}
	if shared[0] {
		t.Fatal("the server's session was dialled with a shared filesystem")
	}
}

// TestAServerFleetNeverOpensTheConnectPanel pins decision 8's panel: a server's
// config is not this machine's, so its session screen offers no connect panel
// even on a machine that has no user config yet. The daemon's screen does, and
// says so here, so the check is known to see the panel.
func TestAServerFleetNeverOpensTheConnectPanel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// The config must land in this home, so the ambient overrides cannot send
	// the read elsewhere.
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if _, err := os.Stat(config.UserConfigPath(home)); !os.IsNotExist(err) {
		t.Fatalf("this machine has a user config at %s; the test needs none", config.UserConfigPath(home))
	}

	dir := t.TempDir()
	server := loopFleetScript(t, dir)
	var onServer string
	server.onSession = func(_ context.Context, m tui.Model) tui.Model {
		onServer = sessionView(t, m)
		return m
	}
	if err := runServerFleet(t, newFakeDaemon(t, dir, home), dir, server); err != nil {
		t.Fatalf("ServerFleet: %v", err)
	}
	if len(server.sessions) != 1 {
		t.Fatalf("the server's loop ran %d sessions, want one", len(server.sessions))
	}
	if onServer == "" {
		t.Fatal("the server's session screen rendered nothing")
	}
	if strings.Contains(onServer, connectPanelTitle) {
		t.Fatalf("the server's session screen opened the connect panel:\n%s", onServer)
	}

	// The daemon's screen, for comparison: the same machine, still with no user
	// config, so it does offer the panel. Its own project dir, because each
	// project's runtime owns its database.
	daemonDir := t.TempDir()
	daemon := &loopScript{t: t}
	daemon.fleetEnds = []ProgramResult{{Open: fleet.Target{Root: daemonDir, SessionID: "s1"}}}
	var onDaemon string
	daemon.onSession = func(_ context.Context, m tui.Model) tui.Model {
		onDaemon = sessionView(t, m)
		return m
	}
	if err := Fleet(context.Background(), io.Discard, filepath.Join(t.TempDir(), "fleet.sock"),
		withDaemonClient(newFakeDaemon(t, daemonDir, home)),
		withWorkingDir(daemonDir),
		withFleetClient(newFakeFleet()),
		WithProgramRunner(daemon.run),
	); err != nil {
		t.Fatalf("Fleet: %v", err)
	}
	if len(daemon.sessions) != 1 {
		t.Fatalf("the daemon's loop ran %d sessions, want one", len(daemon.sessions))
	}
	if !strings.Contains(onDaemon, connectPanelTitle) {
		t.Fatalf("the daemon's session screen has no connect panel, so this test cannot see one:\n%s", onDaemon)
	}
}

// connectPanelTitle is the connect panel's title, which its first screen
// carries (internal/app/tui/connect).
const connectPanelTitle = "Connect a provider"

// TestAServerFleetsVersionNoteNamesTheServer pins decision 8's note: a server's
// agent from another build is named as the server's, with no advice to restart
// a daemon that is not here.
func TestAServerFleetsVersionNoteNamesTheServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	dir := t.TempDir()
	fake := newFakeDaemon(t, dir, home)
	if err := runServerFleet(t, fake, dir, loopFleetScript(t, dir), withAgentVersion("other")); err != nil {
		t.Fatalf("ServerFleet: %v", err)
	}

	rt := fake.lastRuntime()
	if rt == nil {
		t.Fatal("the fake served no runtime")
	}
	var notes []string
	for _, msg := range rt.State.Messages() {
		if strings.Contains(msg.Content, "the server's agent") {
			notes = append(notes, msg.Content)
		}
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %d, want exactly one: %v", len(notes), notes)
	}
	for _, want := range []string{"other", "dev", "the server's agent"} {
		if !strings.Contains(notes[0], want) {
			t.Errorf("note = %q, want it to name %q", notes[0], want)
		}
	}
	if strings.Contains(notes[0], "Restart the daemon") {
		t.Errorf("note = %q, want no daemon restart advice on a server", notes[0])
	}
}

// TestAServerFleetNamesItsClientAfterTheProfile pins decision 8's name: the
// fleet client the loop builds is the profile's, so the footer and a rejected
// token name the server the screen is showing.
func TestAServerFleetNamesItsClientAfterTheProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	var wired acpclient.Fleet
	err := ServerFleet(context.Background(), io.Discard, serverProfile(),
		withTokenRunner((&countingToken{}).run),
		withFleetWiring(func(client acpclient.Fleet, _ *fleet.Watch) { wired = client }),
		WithProgramRunner(func(context.Context, tea.Model, io.Writer) ProgramResult {
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("ServerFleet: %v", err)
	}
	if wired == nil {
		t.Fatal("the loop wired no fleet client")
	}
	if got := wired.State().Server; got != serverProfile().Name {
		t.Fatalf("the client's state names %q, want the profile's %q", got, serverProfile().Name)
	}
}
