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
		{"acp", "--viewers", "unix:///x/v.sock", "--listen", "unix:///y/a.sock"},
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

// TestAttachRefusesExtraArgumentsAndEmbedded pins the refusals: attach takes at
// most one ID, and an embedded launch has no daemon session to attach to.
func TestAttachRefusesExtraArgumentsAndEmbedded(t *testing.T) {
	isolateHome(t)
	oldAttach := attachRunner
	defer func() { attachRunner = oldAttach }()
	attachRunner = func(ctx context.Context, out io.Writer, socket, sessionID string, opts ...launch.Option) error {
		t.Fatal("attach runner was called for a refused invocation")
		return nil
	}

	for _, args := range [][]string{{"attach", "a", "b"}, {"--embedded", "attach"}} {
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), args, bytes.NewBuffer(nil), &stdout, &stderr); err == nil {
			t.Fatalf("run(%v) returned no error, want a refusal", args)
		}
	}
}
