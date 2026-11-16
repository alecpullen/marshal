package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/acp"
	"marshal/internal/app/config"
	"marshal/internal/launch"
	"marshal/internal/trust"
)

// TestRecordPermanentTrustAnchorsAtRepoRoot pins the --trust fix: the
// record must be keyed on the canonical repository root, not the launch
// subdirectory, so the next root-anchored launch finds it. The stored
// config hash must be taken at the root too — a subdirectory hash is
// always empty (no .marshal/config.toml there), which would force a
// re-prompt on the next launch even with the right key.
func TestRecordPermanentTrustAnchorsAtRepoRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// A .git entry makes root a repository for repo.Root.
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A project config at the root gives the record a real hash.
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".marshal", "config.toml"), []byte("[agent]\nplan_first = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	t.Setenv("MARSHAL_DATA_DIR", dataDir)

	if err := recordPermanentTrustIn(sub); err != nil {
		t.Fatalf("recordPermanentTrustIn: %v", err)
	}

	records, err := trust.NewStore(dataDir).Load()
	if err != nil {
		t.Fatalf("load trust store: %v", err)
	}
	wantKey := trust.Canonicalize(root)
	rec, ok := records[wantKey]
	if !ok {
		keys := make([]string, 0, len(records))
		for k := range records {
			keys = append(keys, k)
		}
		t.Fatalf("no trust record for repo root %q; records: %v", wantKey, keys)
	}
	if !rec.Trusted {
		t.Error("record is not trusted")
	}
	wantHash, err := trust.ConfigHashFor(root)
	if err != nil {
		t.Fatalf("ConfigHashFor(root): %v", err)
	}
	if rec.ConfigHash != wantHash {
		t.Errorf("config hash = %q, want the root config hash %q", rec.ConfigHash, wantHash)
	}
}

// TestRecordPermanentTrustNonGitDirUsesLaunchDir pins the non-git
// fallback: outside a repository the record keys on the launch directory,
// exactly as before.
func TestRecordPermanentTrustNonGitDirUsesLaunchDir(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()
	t.Setenv("MARSHAL_DATA_DIR", dataDir)

	if err := recordPermanentTrustIn(dir); err != nil {
		t.Fatalf("recordPermanentTrustIn: %v", err)
	}

	records, err := trust.NewStore(dataDir).Load()
	if err != nil {
		t.Fatalf("load trust store: %v", err)
	}
	if _, ok := records[trust.Canonicalize(dir)]; !ok {
		t.Fatalf("no trust record for the launch directory %q", trust.Canonicalize(dir))
	}
}

func TestRunDispatchesACPSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	oldACP := acpRunner
	defer func() { acpRunner = oldACP }()
	acpRunner = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
		called = true
		return nil
	}
	if err := run(context.Background(), []string{"acp"}, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !called {
		t.Fatal("acpRunner was not called")
	}
}

func TestACPListenFlagRoutesToListener(t *testing.T) {
	var gotNetwork, gotAddr string
	orig := acpListener
	acpListener = func(ctx context.Context, network, addr string, stderr io.Writer) error {
		gotNetwork, gotAddr = network, addr
		return nil
	}
	t.Cleanup(func() { acpListener = orig })

	args := []string{"acp", "--listen", "unix:///run/marshal/agent.sock"}
	if err := run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotNetwork != "unix" || gotAddr != "/run/marshal/agent.sock" {
		t.Fatalf("got (%q, %q), want (unix, /run/marshal/agent.sock)", gotNetwork, gotAddr)
	}
}

// TestACPViewersFlagRoutesToTheViewersRunner pins P4 plan 3's dispatch: both
// --viewers forms reach the viewers runner with the socket path, and every
// other form is refused before it runs.
func TestACPViewersFlagRoutesToTheViewersRunner(t *testing.T) {
	var gotPath string
	orig := acpViewersRunner
	acpViewersRunner = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, path string) error {
		gotPath = path
		return nil
	}
	t.Cleanup(func() { acpViewersRunner = orig })

	for _, args := range [][]string{
		{"acp", "--viewers", "unix:///x/v.sock"},
		{"acp", "--viewers=unix:///x/v.sock"},
	} {
		gotPath = ""
		if err := run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
			t.Fatalf("%v: run: %v", args, err)
		}
		if gotPath != "/x/v.sock" {
			t.Fatalf("%v: path = %q, want /x/v.sock", args, gotPath)
		}
	}

	refused := [][]string{
		{"acp", "--viewers", "tcp://127.0.0.1:1"},
		{"acp", "--viewers"},
		{"acp", "--viewers", "unix:///x/v.sock", "--viewers=unix:///y/w.sock"},
	}
	for _, args := range refused {
		gotPath = ""
		if err := run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard); err == nil {
			t.Errorf("%v: run succeeded, want a refusal", args)
		}
		if gotPath != "" {
			t.Errorf("%v: the viewers runner ran with %q, want a refusal first", args, gotPath)
		}
	}
}

// acpRouted records which runner the acp branch chose, and with what, so a
// test can assert the dispatch without binding a socket or serving anything.
type acpRouted struct {
	stdio        int    // acpRunner (stdio) calls
	listen       int    // acpListener calls
	network      string // the listener's network
	addr         string // the listener's address
	viewers      int    // acpListenerWithViewers calls
	viewerAt     string // the viewer socket either listener was given
	standalone   int    // acpViewersRunner calls
	standaloneAt string // the socket --viewers alone was given
}

// recordACPRouting replaces every runner the acp branch can reach with a
// recorder, in the pattern of TestACPListenFlagRoutesToListener.
func recordACPRouting(t *testing.T) *acpRouted {
	t.Helper()
	r := &acpRouted{}
	origRunner, origListener, origListenerWithViewers, origViewers := acpRunner, acpListener, acpListenerWithViewers, acpViewersRunner
	acpRunner = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
		r.stdio++
		return nil
	}
	acpListener = func(ctx context.Context, network, addr string, stderr io.Writer) error {
		r.listen++
		r.network, r.addr = network, addr
		return nil
	}
	acpListenerWithViewers = func(ctx context.Context, network, addr, viewers string, stderr io.Writer) error {
		r.viewers++
		r.network, r.addr, r.viewerAt = network, addr, viewers
		return nil
	}
	acpViewersRunner = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, path string) error {
		r.standalone++
		r.standaloneAt = path
		return nil
	}
	t.Cleanup(func() {
		acpRunner, acpListener, acpListenerWithViewers, acpViewersRunner = origRunner, origListener, origListenerWithViewers, origViewers
	})
	return r
}

// TestACPListenWithViewersRoutesToTheRestrictedListener pins F6's spelled-out
// form: --listen with a viewer socket runs the restricted listener, whose
// host socket and viewer socket both arrive intact in either flag spelling.
func TestACPListenWithViewersRoutesToTheRestrictedListener(t *testing.T) {
	for _, args := range [][]string{
		{"acp", "--listen", "unix:///y/a.sock", "--viewers", "unix:///x/v.sock"},
		{"acp", "--listen", "unix:///y/a.sock", "--viewers=unix:///x/v.sock"},
		{"acp", "--listen=unix:///y/a.sock", "--viewers", "unix:///x/v.sock"},
		{"acp", "--listen=unix:///y/a.sock", "--viewers=unix:///x/v.sock"},
	} {
		r := recordACPRouting(t)
		if err := run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
			t.Fatalf("%v: run: %v", args, err)
		}
		if r.viewers != 1 {
			t.Fatalf("%v: restricted listener calls = %d, want 1", args, r.viewers)
		}
		if r.network != "unix" || r.addr != "/y/a.sock" {
			t.Errorf("%v: host socket = (%q, %q), want (unix, /y/a.sock)", args, r.network, r.addr)
		}
		if r.viewerAt != "/x/v.sock" {
			t.Errorf("%v: viewer socket = %q, want /x/v.sock", args, r.viewerAt)
		}
		if r.listen != 0 || r.standalone != 0 || r.stdio != 0 {
			t.Errorf("%v: other runners ran (listen %d, viewers %d, stdio %d)", args, r.listen, r.standalone, r.stdio)
		}
	}
}

// TestACPListenReadsTheViewersVariable pins decision 3: the bridge names the
// container's viewer socket in the environment, which --listen reads when no
// --viewers flag is given.
func TestACPListenReadsTheViewersVariable(t *testing.T) {
	t.Setenv(acp.ViewersEnv, "unix:///x/v.sock")
	r := recordACPRouting(t)

	if err := run(context.Background(), []string{"acp", "--listen", "unix:///y/a.sock"}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	if r.viewers != 1 {
		t.Fatalf("restricted listener calls = %d, want 1", r.viewers)
	}
	if r.network != "unix" || r.addr != "/y/a.sock" {
		t.Errorf("host socket = (%q, %q), want (unix, /y/a.sock)", r.network, r.addr)
	}
	if r.viewerAt != "/x/v.sock" {
		t.Errorf("viewer socket = %q, want /x/v.sock", r.viewerAt)
	}
}

// TestTheViewersVariableIsRemoved pins decision 3's second half: whatever mode
// runs, --listen drops the variable from its own environment before any
// runner starts, so nothing the agent spawns inherits it.
func TestTheViewersVariableIsRemoved(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "stdio", args: []string{"acp"}},
		{name: "listen", args: []string{"acp", "--listen", "unix:///y/a.sock"}},
		{name: "viewers alone", args: []string{"acp", "--viewers", "unix:///x/v.sock"}},
		{name: "listen with viewers", args: []string{"acp", "--listen", "unix:///y/a.sock", "--viewers", "unix:///x/v.sock"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(acp.ViewersEnv, "unix:///x/v.sock")
			recordACPRouting(t)
			if err := run(context.Background(), tc.args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := os.Getenv(acp.ViewersEnv); got != "" {
				t.Fatalf("after %v, %s = %q, want it removed", tc.args, acp.ViewersEnv, got)
			}
		})
	}
}

// TestTheViewersFlagBeatsTheVariable pins the precedence decision 3 states: an
// explicit --viewers wins over what the bridge put in the environment.
func TestTheViewersFlagBeatsTheVariable(t *testing.T) {
	t.Setenv(acp.ViewersEnv, "unix:///env/v.sock")
	r := recordACPRouting(t)

	args := []string{"acp", "--listen", "unix:///y/a.sock", "--viewers", "unix:///flag/v.sock"}
	if err := run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	if r.viewers != 1 {
		t.Fatalf("restricted listener calls = %d, want 1", r.viewers)
	}
	if r.viewerAt != "/flag/v.sock" {
		t.Errorf("viewer socket = %q, want the flag's /flag/v.sock", r.viewerAt)
	}
}

// TestACPViewersAloneIgnoresTheVariable pins that --viewers alone, and stdio
// with neither flag, never consult the environment: the variable belongs to
// --listen.
func TestACPViewersAloneIgnoresTheVariable(t *testing.T) {
	t.Setenv(acp.ViewersEnv, "unix:///env/v.sock")

	t.Run("viewers alone", func(t *testing.T) {
		r := recordACPRouting(t)
		args := []string{"acp", "--viewers", "unix:///flag/v.sock"}
		if err := run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
			t.Fatalf("run: %v", err)
		}
		if r.standalone != 1 || r.standaloneAt != "/flag/v.sock" {
			t.Fatalf("viewers runner calls = %d with %q, want 1 with /flag/v.sock", r.standalone, r.standaloneAt)
		}
		if r.viewers != 0 || r.listen != 0 || r.stdio != 0 {
			t.Errorf("other runners ran (listen %d, restricted %d, stdio %d)", r.listen, r.viewers, r.stdio)
		}
	})

	t.Run("no viewers flag", func(t *testing.T) {
		r := recordACPRouting(t)
		if err := run(context.Background(), []string{"acp"}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
			t.Fatalf("run: %v", err)
		}
		if r.stdio != 1 {
			t.Fatalf("stdio calls = %d, want 1", r.stdio)
		}
		if r.viewers != 0 || r.listen != 0 || r.standalone != 0 {
			t.Errorf("other runners ran (listen %d, restricted %d, viewers %d)", r.listen, r.viewers, r.standalone)
		}
	})
}

// TestACPListenWithATCPViewerIsRefused pins the unix-socket requirement for
// the flag and for the variable's value alike: a viewer socket is a path on
// the host either way, so --listen refuses it before any listener runs.
func TestACPListenWithATCPViewerIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		env     string
		wantErr string
	}{
		{
			name:    "flag",
			args:    []string{"acp", "--listen", "unix:///y/a.sock", "--viewers", "tcp://127.0.0.1:9"},
			wantErr: "--viewers takes a unix socket",
		},
		{
			name:    "variable",
			args:    []string{"acp", "--listen", "unix:///y/a.sock"},
			env:     "tcp://127.0.0.1:9",
			wantErr: acp.ViewersEnv + " takes a unix socket",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(acp.ViewersEnv, tc.env)
			r := recordACPRouting(t)
			err := run(context.Background(), tc.args, strings.NewReader(""), io.Discard, io.Discard)
			if err == nil {
				t.Fatalf("run succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to name %q", err, tc.wantErr)
			}
			if r.viewers != 0 || r.listen != 0 || r.standalone != 0 || r.stdio != 0 {
				t.Errorf("a runner ran for a refused invocation (listen %d, restricted %d, viewers %d, stdio %d)",
					r.listen, r.viewers, r.standalone, r.stdio)
			}
		})
	}
}

// TestRunDispatchesSnapshotsSubcommand pins the dispatch anchor: `marshal
// snapshots` must reach the snapshots runner with its remaining arguments and
// all three streams, exactly like `plugin`. It is asserted through the
// package-level seam so the test never opens the real store.
func TestRunDispatchesSnapshotsSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	oldSnapshots := snapshotsRunner
	defer func() { snapshotsRunner = oldSnapshots }()
	snapshotsRunner = func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		called = true
		if len(args) != 1 || args[0] != "status" {
			t.Errorf("args = %v, want [status]", args)
		}
		fmt.Fprintln(stdout, "snapshots ran")
		return nil
	}
	if err := run(context.Background(), []string{"snapshots", "status"}, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !called {
		t.Fatal("snapshotsRunner was not called")
	}
	if !strings.Contains(stdout.String(), "snapshots ran") {
		t.Errorf("stdout = %q, want the subcommand's output", stdout.String())
	}
}

// TestDaemonDispatches pins the dispatch anchor: `marshal daemon` must reach
// the daemon runner with its remaining arguments and all three streams,
// exactly like `snapshots`. It is asserted through the package-level seam so
// the test never opens a socket or touches a service manager.
func TestDaemonDispatches(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	oldDaemon := daemonRunner
	defer func() { daemonRunner = oldDaemon }()
	daemonRunner = func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		called = true
		if len(args) != 1 || args[0] != "status" {
			t.Errorf("args = %v, want [status]", args)
		}
		fmt.Fprintln(stdout, "daemon ran")
		return nil
	}
	if err := run(context.Background(), []string{"daemon", "status"}, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !called {
		t.Fatal("daemonRunner was not called")
	}
	if !strings.Contains(stdout.String(), "daemon ran") {
		t.Errorf("stdout = %q, want the subcommand's output", stdout.String())
	}
}

func TestRunPrintsVersionAndSkipsApp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	oldApp := appRunner
	defer func() { appRunner = oldApp }()
	appRunner = func(ctx context.Context, out io.Writer, opts ...launch.Option) error {
		called = true
		return nil
	}

	oldVersion, oldCommit, oldDate := version, commit, date
	defer func() { version, commit, date = oldVersion, oldCommit, oldDate }()
	version, commit, date = "0.0.1-alpha", "abc1234", "2026-08-26T00:00:00Z"

	if err := run(context.Background(), []string{"--version"}, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if called {
		t.Fatal("appRunner was called; --version must not start the TUI")
	}
	out := stdout.String()
	for _, want := range []string{"marshal", "0.0.1-alpha", "abc1234", "2026-08-26T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
}

func TestVersionStringFallsBackToDev(t *testing.T) {
	oldVersion, oldCommit, oldDate := version, commit, date
	defer func() { version, commit, date = oldVersion, oldCommit, oldDate }()
	version, commit, date = "", "", ""

	got := versionString()
	if !strings.Contains(got, "dev") {
		t.Errorf("versionString() = %q, want it to mention %q", got, "dev")
	}
}

// isolateHome points HOME, the data dir and XDG's data dir at a fresh
// directory, so the daemon socket a launch asks for is under the test's home
// and never the developer's.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MARSHAL_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	return home
}

// TestPlainMarshalTriesTheDaemon pins P4 D5's default: a plain launch offers
// the daemon socket to launch.Run, which decides between daemon and embedded.
func TestPlainMarshalTriesTheDaemon(t *testing.T) {
	var stdout, stderr bytes.Buffer
	isolateHome(t)
	got := -1
	oldApp := appRunner
	defer func() { appRunner = oldApp }()
	appRunner = func(ctx context.Context, out io.Writer, opts ...launch.Option) error {
		got = len(opts)
		return nil
	}

	if err := run(context.Background(), nil, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got != 1 {
		t.Fatalf("appRunner got %d options, want 1 (the daemon socket)", got)
	}
}

// TestEmbeddedSkipsTheDaemon pins --embedded: the option is never offered, so
// the session stays in this process.
func TestEmbeddedSkipsTheDaemon(t *testing.T) {
	var stdout, stderr bytes.Buffer
	isolateHome(t)
	got := -1
	oldApp := appRunner
	defer func() { appRunner = oldApp }()
	appRunner = func(ctx context.Context, out io.Writer, opts ...launch.Option) error {
		got = len(opts)
		return nil
	}

	if err := run(context.Background(), []string{"--embedded"}, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("appRunner got %d options, want 0 with --embedded", got)
	}
}

// TestAttachRoutesToTheAttachRunner pins `marshal attach [session-id]`: no ID
// means the project's most recently active session, and the socket is the
// daemon's under this home.
func TestAttachRoutesToTheAttachRunner(t *testing.T) {
	home := isolateHome(t)
	wantSocket := config.DaemonSocketPath(home)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"attach"}, want: ""},
		{args: []string{"attach", "abc"}, want: "abc"},
	} {
		var stdout, stderr bytes.Buffer
		called := false
		var gotSocket, gotID string
		oldAttach := attachRunner
		attachRunner = func(ctx context.Context, out io.Writer, socket, sessionID string, opts ...launch.Option) error {
			called = true
			gotSocket, gotID = socket, sessionID
			return nil
		}

		err := run(context.Background(), tc.args, bytes.NewBuffer(nil), &stdout, &stderr)
		attachRunner = oldAttach
		if err != nil {
			t.Fatalf("run(%v) error = %v", tc.args, err)
		}
		if !called {
			t.Fatalf("run(%v) did not reach the attach runner", tc.args)
		}
		if gotID != tc.want {
			t.Errorf("run(%v) session ID = %q, want %q", tc.args, gotID, tc.want)
		}
		if gotSocket != wantSocket {
			t.Errorf("run(%v) socket = %q, want %q", tc.args, gotSocket, wantSocket)
		}
		if !strings.HasPrefix(gotSocket, home) {
			t.Errorf("run(%v) socket = %q, want it under the test's home %q", tc.args, gotSocket, home)
		}
	}
}

// TestFleetRoutesToTheFleetRunner pins `marshal fleet`: the screen reaches the
// fleet runner with the daemon's socket under this home.
func TestFleetRoutesToTheFleetRunner(t *testing.T) {
	home := isolateHome(t)
	wantSocket := config.DaemonSocketPath(home)

	var stdout, stderr bytes.Buffer
	called := false
	var gotSocket string
	oldFleet := fleetRunner
	fleetRunner = func(ctx context.Context, out io.Writer, socket string, opts ...launch.Option) error {
		called = true
		gotSocket = socket
		return nil
	}
	defer func() { fleetRunner = oldFleet }()

	if err := run(context.Background(), []string{"fleet"}, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !called {
		t.Fatal("run() did not reach the fleet runner")
	}
	if gotSocket != wantSocket {
		t.Errorf("run() socket = %q, want %q", gotSocket, wantSocket)
	}
	if !strings.HasPrefix(gotSocket, home) {
		t.Errorf("run() socket = %q, want it under the test's home %q", gotSocket, home)
	}
}

// TestFleetRefusesArgumentsAndEmbedded pins the refusals: fleet takes no
// arguments yet (plan 7 adds --server and login), and --embedded is refused
// wherever it appears.
func TestFleetRefusesArgumentsAndEmbedded(t *testing.T) {
	isolateHome(t)
	oldFleet := fleetRunner
	defer func() { fleetRunner = oldFleet }()
	fleetRunner = func(ctx context.Context, out io.Writer, socket string, opts ...launch.Option) error {
		t.Fatal("the fleet runner was called for a refused invocation")
		return nil
	}

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--embedded", "fleet"}, "--embedded cannot be combined with fleet"},
		{[]string{"fleet", "--embedded"}, "--embedded cannot be combined with fleet"},
		{[]string{"fleet", "--embedded=1"}, "--embedded cannot be combined with fleet"},
		{[]string{"fleet", "login"}, "unexpected argument"},
		{[]string{"fleet", "--server", "x"}, "unexpected argument"},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), tc.args, bytes.NewBuffer(nil), &stdout, &stderr)
		if err == nil {
			t.Fatalf("run(%v) returned no error, want a refusal", tc.args)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("run(%v) error = %q, want it to name %q", tc.args, err, tc.want)
		}
	}
}

// TestAttachRefusesExtraArgumentsAndEmbedded pins the refusals: attach takes at
// most one ID, --embedded is refused wherever it appears, and a flag-shaped ID
// is named as a flag.
func TestAttachRefusesExtraArgumentsAndEmbedded(t *testing.T) {
	isolateHome(t)
	oldAttach := attachRunner
	defer func() { attachRunner = oldAttach }()
	attachRunner = func(ctx context.Context, out io.Writer, socket, sessionID string, opts ...launch.Option) error {
		t.Fatal("attach runner was called for a refused invocation")
		return nil
	}

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"attach", "a", "b"}, "at most one session ID"},
		{[]string{"--embedded", "attach"}, "--embedded cannot be combined"},
		{[]string{"attach", "--embedded"}, "--embedded cannot be combined"},
		{[]string{"attach", "abc", "--embedded"}, "--embedded cannot be combined"},
		{[]string{"attach", "-abc"}, "unexpected flag"},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), tc.args, bytes.NewBuffer(nil), &stdout, &stderr)
		if err == nil {
			t.Fatalf("run(%v) returned no error, want a refusal", tc.args)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("run(%v) error = %q, want it to name %q", tc.args, err, tc.want)
		}
	}
}
