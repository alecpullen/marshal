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
	"marshal/internal/app/tui/fleet"
	"marshal/internal/app/tui/theme"
)

// fakeFleet is a fleet the test owns: it answers State from a field and never
// signals Changed. Fleet neither starts nor closes an injected client, so it
// has no lifecycle of its own.
type fakeFleet struct {
	state   acpclient.FleetState
	changed chan struct{}
}

func newFakeFleet() *fakeFleet {
	return &fakeFleet{
		state:   acpclient.FleetState{Server: "local", Conn: acpclient.FleetConnected},
		changed: make(chan struct{}, 1),
	}
}

func (f *fakeFleet) State() acpclient.FleetState { return f.state }

func (f *fakeFleet) Changed() <-chan struct{} { return f.changed }

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
