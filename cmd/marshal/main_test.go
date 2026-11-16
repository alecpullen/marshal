package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"marshal/internal/acp"
	"marshal/internal/app/config"
	"marshal/internal/buildinfo"
	"marshal/internal/credentials"
	"marshal/internal/fleetprofile"
	"marshal/internal/launch"
	"marshal/internal/llm/provider"
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

// TestEveryModeSetsTheAgentVersion pins decision 2's second half: run computes
// the version once and stamps it into acp.AgentVersion before dispatching, so
// every mode's TUI has this build's version — not only `marshal acp`.
func TestEveryModeSetsTheAgentVersion(t *testing.T) {
	const stamped = "1.2.3-test"
	oldVersion, oldAgent, oldUserAgent := version, acp.AgentVersion, provider.UserAgent
	t.Cleanup(func() {
		version, acp.AgentVersion, provider.UserAgent = oldVersion, oldAgent, oldUserAgent
	})

	oldApp, oldAttach, oldFleet := appRunner, attachRunner, fleetRunner
	t.Cleanup(func() { appRunner, attachRunner, fleetRunner = oldApp, oldAttach, oldFleet })
	appRunner = func(ctx context.Context, out io.Writer, opts ...launch.Option) error {
		return nil
	}
	attachRunner = func(ctx context.Context, out io.Writer, socket, sessionID string, opts ...launch.Option) error {
		return nil
	}
	fleetRunner = func(ctx context.Context, out io.Writer, socket string, opts ...launch.Option) error {
		return nil
	}

	version = stamped

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "the app", args: nil},
		{name: "attach", args: []string{"attach"}},
		{name: "fleet", args: []string{"fleet"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateHome(t)
			// Cleared first, so a mode that never sets it cannot pass by
			// inheriting the value another mode left behind.
			acp.AgentVersion = ""
			if err := run(context.Background(), tc.args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
				t.Fatalf("run(%v): %v", tc.args, err)
			}
			if acp.AgentVersion != stamped {
				t.Fatalf("run(%v): acp.AgentVersion = %q, want the stamped %q", tc.args, acp.AgentVersion, stamped)
			}
		})
	}
}

// TestTheUserAgentCarriesTheVersion pins decision 2's User-Agent half: it
// always names this build and its version, so a source build names its commit
// there too instead of the package's marshal/dev default.
func TestTheUserAgentCarriesTheVersion(t *testing.T) {
	oldVersion, oldUserAgent := version, provider.UserAgent
	t.Cleanup(func() { version, provider.UserAgent = oldVersion, oldUserAgent })

	for _, tc := range []struct {
		name  string
		stamp string
	}{
		{name: "stamped", stamp: "1.2.3-alpha"},
		{name: "unstamped", stamp: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version = tc.stamp
			// A value run must overwrite, so a run that never set it fails.
			provider.UserAgent = "sentinel"
			if err := run(context.Background(), []string{"--version"}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
				t.Fatalf("run(--version): %v", err)
			}
			want := "marshal/" + buildinfo.Version(tc.stamp)
			if provider.UserAgent != want {
				t.Fatalf("UserAgent = %q, want %q", provider.UserAgent, want)
			}
		})
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
	// The fleet file is read from the config dir, so the developer's must not
	// answer: an ambient XDG_CONFIG_HOME would point the tests at it.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	return home
}

// isolateFleetConfig isolates the environment, then points the config dir — and
// so fleet.toml — at a fresh directory of its own.
func isolateFleetConfig(t *testing.T) (home, dir string) {
	t.Helper()
	home = isolateHome(t)
	dir = t.TempDir()
	t.Setenv("MARSHAL_CONFIG_DIR", dir)
	return home, dir
}

// writeFleetFile writes fleet.toml with body into dir at mode 0600, and
// answers its path.
func writeFleetFile(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "fleet.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fleet.toml: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod fleet.toml: %v", err)
	}
	return path
}

// recordNotify replaces notifyOption so a test reads the flag a runner was
// handed: launch's options are unexported.
func recordNotify(t *testing.T) *bool {
	t.Helper()
	on := true
	old := notifyOption
	notifyOption = func(notify bool) launch.Option {
		on = notify
		return old(notify)
	}
	t.Cleanup(func() { notifyOption = old })
	return &on
}

// TestPlainMarshalTriesTheDaemon pins P4 D5's default: a plain launch offers
// the daemon socket to launch.Run, which decides between daemon and embedded,
// and notifications from the fleet file (decision 9).
func TestPlainMarshalTriesTheDaemon(t *testing.T) {
	var stdout, stderr bytes.Buffer
	isolateHome(t)
	on := recordNotify(t)
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
	if got != 2 {
		t.Fatalf("appRunner got %d options, want 2 (the daemon socket and notify)", got)
	}
	if !*on {
		t.Error("run() turned notifications off with no fleet file, want them on")
	}
}

// TestEmbeddedSkipsTheDaemon pins --embedded: the daemon option is never
// offered, so the session stays in this process. The fleet file's notify
// still is.
func TestEmbeddedSkipsTheDaemon(t *testing.T) {
	var stdout, stderr bytes.Buffer
	isolateHome(t)
	recordNotify(t)
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
	if got != 1 {
		t.Fatalf("appRunner got %d options, want 1 (notify alone) with --embedded", got)
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

// TestFleetRefusesArgumentsAndEmbedded pins the refusals: --embedded is
// refused wherever it appears, and every fleet form the parser does not know is
// named rather than ignored.
func TestFleetRefusesArgumentsAndEmbedded(t *testing.T) {
	isolateFleetConfig(t)
	oldFleet := fleetRunner
	defer func() { fleetRunner = oldFleet }()
	fleetRunner = func(ctx context.Context, out io.Writer, socket string, opts ...launch.Option) error {
		t.Fatal("the fleet runner was called for a refused invocation")
		return nil
	}
	oldServer := fleetServerRunner
	defer func() { fleetServerRunner = oldServer }()
	fleetServerRunner = func(ctx context.Context, out io.Writer, srv fleetprofile.Server, opts ...launch.Option) error {
		t.Fatal("the server fleet runner was called for a refused invocation")
		return nil
	}

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--embedded", "fleet"}, "--embedded cannot be combined with fleet"},
		{[]string{"fleet", "--embedded"}, "--embedded cannot be combined with fleet"},
		{[]string{"fleet", "--embedded=1"}, "--embedded cannot be combined with fleet"},
		{[]string{"fleet", "login", "--embedded"}, "--embedded cannot be combined with fleet"},
		{[]string{"fleet", "stray"}, "unexpected argument"},
		{[]string{"fleet", "--server"}, "requires a server name"},
		{[]string{"fleet", "--server="}, "requires a server name"},
		{[]string{"fleet", "--server", ""}, "requires a server name"},
		{[]string{"fleet", "--server", "a", "--server", "b"}, "given more than once"},
		{[]string{"fleet", "--frobnicate"}, "unexpected flag"},
		{[]string{"fleet", "--server", "a", "login", "b"}, "cannot be combined with login"},
		{[]string{"fleet", "login"}, "a server name is required"},
		{[]string{"fleet", "login", "a", "b"}, "at most one server name"},
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

// TestFleetServerRoutesToTheServerRunner pins `marshal fleet --server NAME`:
// both spellings reach the server fleet runner with the profile the file names
// for NAME (decision 9).
func TestFleetServerRoutesToTheServerRunner(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	writeFleetFile(t, dir, "[servers.example]\nurl = \"https://fleet.example.com\"\n")

	oldFleet := fleetRunner
	defer func() { fleetRunner = oldFleet }()
	fleetRunner = func(ctx context.Context, out io.Writer, socket string, opts ...launch.Option) error {
		t.Fatal("the local fleet runner was called for --server")
		return nil
	}
	oldServer := fleetServerRunner
	defer func() { fleetServerRunner = oldServer }()

	for _, args := range [][]string{
		{"fleet", "--server", "example"},
		{"fleet", "--server=example"},
	} {
		var stdout, stderr bytes.Buffer
		called := false
		var got fleetprofile.Server
		fleetServerRunner = func(ctx context.Context, out io.Writer, srv fleetprofile.Server, opts ...launch.Option) error {
			called = true
			got = srv
			return nil
		}

		if err := run(context.Background(), args, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
			t.Fatalf("run(%v) error = %v", args, err)
		}
		if !called {
			t.Fatalf("run(%v) did not reach the server fleet runner", args)
		}
		if got.Name != "example" {
			t.Errorf("run(%v) profile name = %q, want %q", args, got.Name, "example")
		}
		if got.URL != "https://fleet.example.com" {
			t.Errorf("run(%v) profile url = %q, want the file's %q", args, got.URL, "https://fleet.example.com")
		}
	}
}

// TestFleetServerNeedsAKnownName pins decision 9's refusal: a name the file
// lacks fails, and the error names the file and the servers it has.
func TestFleetServerNeedsAKnownName(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	path := writeFleetFile(t, dir, "[servers.one]\nurl = \"https://one.example.com\"\n\n[servers.two]\nurl = \"https://two.example.com\"\n")

	oldServer := fleetServerRunner
	defer func() { fleetServerRunner = oldServer }()
	fleetServerRunner = func(ctx context.Context, out io.Writer, srv fleetprofile.Server, opts ...launch.Option) error {
		t.Fatal("the server fleet runner was called for an unknown name")
		return nil
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"fleet", "--server", "nope"}, bytes.NewBuffer(nil), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() returned no error for a name the file lacks")
	}
	for _, want := range []string{path, `no server "nope"`, "one, two"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

// TestFleetServerWithoutAFileGivesAStarter pins decision 9's other half: no
// fleet.toml at all is not an error, and --server answers a starter table for
// the name.
func TestFleetServerWithoutAFileGivesAStarter(t *testing.T) {
	_, dir := isolateFleetConfig(t)

	oldServer := fleetServerRunner
	defer func() { fleetServerRunner = oldServer }()
	fleetServerRunner = func(ctx context.Context, out io.Writer, srv fleetprofile.Server, opts ...launch.Option) error {
		t.Fatal("the server fleet runner was called with no file")
		return nil
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"fleet", "--server", "example"}, bytes.NewBuffer(nil), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() returned no error with no file and an unknown name")
	}
	for _, want := range []string{filepath.Join(dir, "fleet.toml"), "names no servers", "[servers.example]", "url = "} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

// TestFleetRefusesAFileItCannotRead pins decision 3 through decision 9: a
// fleet.toml others can read stops the fleet screen with the sentence that
// says how to fix it, for both the daemon's form and --server's.
func TestFleetRefusesAFileItCannotRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the mode check is unix's")
	}
	_, dir := isolateFleetConfig(t)
	path := writeFleetFile(t, dir, "[servers.example]\nurl = \"https://fleet.example.com\"\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod fleet.toml: %v", err)
	}

	oldFleet := fleetRunner
	defer func() { fleetRunner = oldFleet }()
	fleetRunner = func(ctx context.Context, out io.Writer, socket string, opts ...launch.Option) error {
		t.Fatal("the local fleet runner was called for an unreadable file")
		return nil
	}
	oldServer := fleetServerRunner
	defer func() { fleetServerRunner = oldServer }()
	fleetServerRunner = func(ctx context.Context, out io.Writer, srv fleetprofile.Server, opts ...launch.Option) error {
		t.Fatal("the server fleet runner was called for an unreadable file")
		return nil
	}

	for _, args := range [][]string{
		{"fleet"},
		{"fleet", "--server", "example"},
	} {
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), args, bytes.NewBuffer(nil), &stdout, &stderr)
		if err == nil {
			t.Fatalf("run(%v) returned no error for mode 0644", args)
		}
		want := fmt.Sprintf("fleet.toml at %s is mode 0644: run chmod 600 %s", path, path)
		if !strings.Contains(err.Error(), want) {
			t.Errorf("run(%v) error = %q, want it to carry %q", args, err, want)
		}
	}
}

// TestFleetPassesTheFilesNotify pins decision 9's notify: the local table's
// notify reaches the daemon runner, and a server's reaches the server runner.
func TestFleetPassesTheFilesNotify(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		_, dir := isolateFleetConfig(t)
		writeFleetFile(t, dir, "[local]\nnotify = false\n")
		on := recordNotify(t)

		oldFleet := fleetRunner
		t.Cleanup(func() { fleetRunner = oldFleet })
		called := false
		fleetRunner = func(ctx context.Context, out io.Writer, socket string, opts ...launch.Option) error {
			called = true
			return nil
		}

		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), []string{"fleet"}, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
			t.Fatalf("run() error = %v", err)
		}
		if !called {
			t.Fatal("run() did not reach the fleet runner")
		}
		if *on {
			t.Error("notifications = on, want the file's local.notify = false")
		}
	})

	t.Run("server", func(t *testing.T) {
		_, dir := isolateFleetConfig(t)
		writeFleetFile(t, dir, "[servers.example]\nurl = \"https://fleet.example.com\"\nnotify = false\n")
		on := recordNotify(t)

		oldServer := fleetServerRunner
		t.Cleanup(func() { fleetServerRunner = oldServer })
		called := false
		fleetServerRunner = func(ctx context.Context, out io.Writer, srv fleetprofile.Server, opts ...launch.Option) error {
			called = true
			return nil
		}

		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), []string{"fleet", "--server", "example"}, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
			t.Fatalf("run() error = %v", err)
		}
		if !called {
			t.Fatal("run() did not reach the server fleet runner")
		}
		if *on {
			t.Error("notifications = on, want the profile's notify = false")
		}
	})
}

// TestPlainMarshalKeepsGoingPastABadFleetFile pins decision 9's asymmetry: a
// fleet file that cannot be loaded does not stop a plain session, which keeps
// notifications on and says on stderr why.
func TestPlainMarshalKeepsGoingPastABadFleetFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the mode check is unix's")
	}
	_, dir := isolateFleetConfig(t)
	path := writeFleetFile(t, dir, "[local]\nnotify = false\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod fleet.toml: %v", err)
	}
	on := recordNotify(t)

	oldApp := appRunner
	defer func() { appRunner = oldApp }()
	called := false
	appRunner = func(ctx context.Context, out io.Writer, opts ...launch.Option) error {
		called = true
		return nil
	}

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), nil, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v, want the session to start anyway", err)
	}
	if !called {
		t.Fatal("run() did not reach the app runner past a bad fleet file")
	}
	if !*on {
		t.Error("notifications = off, want them kept on when the file cannot be loaded")
	}
	if !strings.Contains(stderr.String(), path) {
		t.Errorf("stderr = %q, want it to name the file %q", stderr.String(), path)
	}
}

// TestFleetLoginStoresTheToken pins decision 10: a piped token lands in the
// keychain under decision 4's key, trimmed.
func TestFleetLoginStoresTheToken(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	writeFleetFile(t, dir, "[servers.example]\nurl = \"https://fleet.example.com\"\n")

	store := credentials.NewMemStore()
	oldStore := fleetLoginStore
	defer func() { fleetLoginStore = oldStore }()
	fleetLoginStore = func() (credentials.Store, error) { return store, nil }

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("  s3cret-token  \n")
	if err := run(context.Background(), []string{"fleet", "login", "example"}, stdin, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	got, err := store.Get(fleetprofile.KeychainKey("example"))
	if err != nil {
		t.Fatalf("the token was not stored: %v", err)
	}
	if string(got) != "s3cret-token" {
		t.Errorf("stored token = %q, want the piped line trimmed", string(got))
	}
	if !strings.Contains(stderr.String(), "Stored the token for example.") {
		t.Errorf("stderr = %q, want it to report the token was stored", stderr.String())
	}
}

// TestFleetLoginRefusesAnUnknownServer pins decision 10's first check: a name
// the file lacks is refused before anything is read or stored.
func TestFleetLoginRefusesAnUnknownServer(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	writeFleetFile(t, dir, "[servers.one]\nurl = \"https://one.example.com\"\n")

	opened := false
	oldStore := fleetLoginStore
	defer func() { fleetLoginStore = oldStore }()
	fleetLoginStore = func() (credentials.Store, error) {
		opened = true
		return credentials.NewMemStore(), nil
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"fleet", "login", "nope"}, strings.NewReader("tok\n"), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() returned no error for a name the file lacks")
	}
	if !strings.Contains(err.Error(), "one") {
		t.Errorf("error = %q, want it to name the servers the file has", err)
	}
	if opened {
		t.Error("the keychain was opened for an unknown server")
	}
}

// TestFleetLoginRefusesATokenCmdServer pins decision 10: a profile that reads
// its token from token_cmd has no use for a stored one.
func TestFleetLoginRefusesATokenCmdServer(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	writeFleetFile(t, dir, "[servers.example]\nurl = \"https://fleet.example.com\"\ntoken_cmd = \"pass show fleet/example\"\n")

	opened := false
	oldStore := fleetLoginStore
	defer func() { fleetLoginStore = oldStore }()
	fleetLoginStore = func() (credentials.Store, error) {
		opened = true
		return credentials.NewMemStore(), nil
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"fleet", "login", "example"}, strings.NewReader("tok\n"), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() returned no error for a token_cmd profile")
	}
	if !strings.Contains(err.Error(), "token_cmd") {
		t.Errorf("error = %q, want it to name token_cmd", err)
	}
	if opened {
		t.Error("the keychain was opened for a token_cmd profile")
	}
}

// TestFleetLoginRefusesAnEmptyToken pins decision 10's last check: a blank
// line stores nothing.
func TestFleetLoginRefusesAnEmptyToken(t *testing.T) {
	_, dir := isolateFleetConfig(t)
	writeFleetFile(t, dir, "[servers.example]\nurl = \"https://fleet.example.com\"\n")

	opened := false
	oldStore := fleetLoginStore
	defer func() { fleetLoginStore = oldStore }()
	fleetLoginStore = func() (credentials.Store, error) {
		opened = true
		return credentials.NewMemStore(), nil
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"fleet", "login", "example"}, strings.NewReader("   \n"), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() returned no error for an empty token")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want it to say the token is empty", err)
	}
	if opened {
		t.Error("the keychain was opened for an empty token")
	}
}
