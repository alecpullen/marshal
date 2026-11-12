package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app"
	"marshal/internal/daemon"
)

// fakeDaemonClient answers `status` without a socket.
type fakeDaemonClient struct {
	info   daemon.Info
	err    error
	onInfo func(context.Context)

	// pinState and pinErr are the scripted answer to Pin; pinCalls, when
	// set, records every call, since the fake is copied into the seam and
	// so cannot count on itself.
	pinState daemon.PinState
	pinErr   error
	pinCalls *[]pinCall
}

// pinCall is one Pin the fake saw.
type pinCall struct {
	root   string
	pinned bool
}

func (c fakeDaemonClient) Info(ctx context.Context) (daemon.Info, error) {
	if c.onInfo != nil {
		c.onInfo(ctx)
	}
	return c.info, c.err
}

func (c fakeDaemonClient) Pin(ctx context.Context, root string, pinned bool) (daemon.PinState, error) {
	if c.pinCalls != nil {
		*c.pinCalls = append(*c.pinCalls, pinCall{root: root, pinned: pinned})
	}
	if c.pinErr != nil {
		return daemon.PinState{}, c.pinErr
	}
	return c.pinState, nil
}

// daemonTestEnv isolates a daemon test: a temporary home and none of the
// location overrides the developer's environment may carry, so PathsFor
// resolves under the temporary home only.
func daemonTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{
		"MARSHAL_CONFIG_DIR",
		"MARSHAL_DATA_DIR",
		"XDG_CONFIG_HOME",
		"XDG_DATA_HOME",
	} {
		t.Setenv(key, "")
	}
	return home
}

// fakeDaemonManager replaces the service-manager seam with a launchd-shaped
// manager whose every command goes to run. No launchctl process is started.
func fakeDaemonManager(t *testing.T, run daemon.Runner) {
	t.Helper()
	old := daemonManagerFor
	t.Cleanup(func() { daemonManagerFor = old })
	daemonManagerFor = func(home string) (*daemon.Manager, error) {
		return &daemon.Manager{
			GOOS:    "darwin",
			Home:    home,
			UID:     501,
			UnitDir: filepath.Join(home, "Library", "LaunchAgents"),
			Run:     run,
		}, nil
	}
}

// fakeDaemonSocket replaces the socket seam. Every path that loads or starts
// the service now waits, briefly, for the daemon to answer, so a test that
// left the real client in place would dial a path nothing listens on.
func fakeDaemonSocket(t *testing.T, c daemonClient) {
	t.Helper()
	oldClient, oldTimeout := daemonClientFor, daemonStartTimeout
	t.Cleanup(func() { daemonClientFor, daemonStartTimeout = oldClient, oldTimeout })
	daemonClientFor = func(string) daemonClient { return c }
	daemonStartTimeout = 50 * time.Millisecond
}

// TestDaemonStartReportsWhetherItAnswers: start says so when the daemon is
// answering, and names its log when it is not, without failing — the service
// manager owns the process, so a slow start is a warning.
func TestDaemonStartReportsWhetherItAnswers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		client  daemonClient
		want    string
		wantLog bool
	}{
		{name: "answering", client: fakeDaemonClient{}, want: "answering"},
		{
			name:    "not answering yet",
			client:  fakeDaemonClient{err: errors.New("dial unix: no such file or directory")},
			want:    "not answering yet",
			wantLog: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := daemonTestEnv(t)
			fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte("state = not running\n"), nil
			})
			fakeDaemonSocket(t, tc.client)

			var stdout, stderr bytes.Buffer
			if err := runDaemon(context.Background(), []string{"start"},
				strings.NewReader(""), &stdout, &stderr); err != nil {
				t.Fatalf("runDaemon start: %v\nstderr: %s", err, stderr.String())
			}
			out := stdout.String()
			if !strings.Contains(out, "started") {
				t.Errorf("start printed %q, want it to say started", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("start printed %q, want it to contain %q", out, tc.want)
			}
			if tc.wantLog && !strings.Contains(out, daemon.PathsFor(home).Log) {
				t.Errorf("start printed %q, want it to name the log %q", out, daemon.PathsFor(home).Log)
			}
		})
	}
}

// TestDaemonInstallPassesExplicitPaths pins decision 10: the unit the manager
// receives is built from resolved absolute paths and carries PATH plus the
// location variables that were set, and only those.
func TestDaemonInstallPassesExplicitPaths(t *testing.T) {
	home := daemonTestEnv(t)
	t.Setenv("PATH", "/usr/bin:/bin")

	webbridge := filepath.Join(t.TempDir(), "webbridge")
	if err := os.WriteFile(webbridge, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	marshal, err := resolvedExecutable()
	if err != nil {
		t.Fatalf("resolvedExecutable: %v", err)
	}
	if !filepath.IsAbs(marshal) {
		t.Fatalf("resolved marshal path %q is not absolute", marshal)
	}

	// install runs the subcommand against the fake manager and returns the
	// rendered unit and what the user saw.
	install := func(t *testing.T) (unit, stdout string) {
		t.Helper()
		var calls []string
		fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil, nil
		})
		// install waits, briefly, for the daemon to answer; nothing is
		// listening here, which is the "not answering yet" path.
		fakeDaemonSocket(t, fakeDaemonClient{err: errors.New("dial unix: no such file or directory")})
		var out, errOut bytes.Buffer
		if err := runDaemon(context.Background(), []string{"install", "--webbridge", webbridge},
			strings.NewReader(""), &out, &errOut); err != nil {
			t.Fatalf("runDaemon install: %v\nstderr: %s", err, errOut.String())
		}
		if len(calls) == 0 {
			t.Fatal("install never reached the service manager")
		}
		joined := strings.Join(calls, "\n")
		if !strings.Contains(joined, "bootstrap") {
			t.Errorf("install did not load the unit; manager calls:\n%s", joined)
		}
		unitPath := filepath.Join(home, "Library", "LaunchAgents", "dev.marshal.daemon.plist")
		body, err := os.ReadFile(unitPath)
		if err != nil {
			t.Fatalf("read the installed unit: %v", err)
		}
		return string(body), out.String()
	}

	t.Run("data dir set", func(t *testing.T) {
		dataDir := t.TempDir()
		t.Setenv("MARSHAL_DATA_DIR", dataDir)
		t.Setenv("SHELL", "/bin/zsh")

		unit, stdout := install(t)
		paths := daemon.PathsFor(home)
		for _, want := range []string{
			webbridge,
			marshal,
			"/usr/bin:/bin",
			paths.Socket,
			paths.StateDir,
			paths.Registry,
			"<string>--addr</string>",
			"<string>off</string>",
			"<string>--marshal-bin</string>",
			// The installed daemon always refreshes (decision 5), so the
			// flag is written into the unit rather than configured.
			"<string>--oauth-refresh</string>",
			"<key>MARSHAL_DATA_DIR</key>",
			"<string>" + dataDir + "</string>",
			"<key>SHELL</key>",
			"<string>/bin/zsh</string>",
		} {
			if !strings.Contains(unit, want) {
				t.Errorf("unit does not carry %q:\n%s", want, unit)
			}
		}
		for _, want := range []string{daemon.PathsFor(home).Socket, filepath.Join(home, "Library", "LaunchAgents", "dev.marshal.daemon.plist")} {
			if !strings.Contains(stdout, want) {
				t.Errorf("install printed %q, want it to name %q", stdout, want)
			}
		}
	})

	t.Run("data dir unset", func(t *testing.T) {
		t.Setenv("SHELL", "")
		unit, _ := install(t)
		paths := daemon.PathsFor(home)
		if want := filepath.Join(home, ".local", "share", "marshal", "daemon", "marshal.sock"); paths.Socket != want {
			t.Fatalf("socket = %q, want %q", paths.Socket, want)
		}
		if !strings.Contains(unit, paths.Socket) {
			t.Errorf("unit does not name the default socket %q:\n%s", paths.Socket, unit)
		}
		if !strings.Contains(unit, "<key>PATH</key>") {
			t.Errorf("unit does not carry PATH:\n%s", unit)
		}
		for _, absent := range []string{"MARSHAL_DATA_DIR", "MARSHAL_CONFIG_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "SHELL"} {
			if strings.Contains(unit, absent) {
				t.Errorf("unit carries %q although it was unset:\n%s", absent, unit)
			}
		}
	})
}

// TestDaemonInstallWritesTheIdleTimeout pins decision 9: [daemon]
// idle_session_timeout is read from the user config alone and written into
// the unit, so a change takes effect at the next install and a project
// config cannot change the daemon's behaviour.
func TestDaemonInstallWritesTheIdleTimeout(t *testing.T) {
	home := daemonTestEnv(t)
	t.Setenv("PATH", "/usr/bin:/bin")

	userConfig := filepath.Join(home, ".config", "marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfig, []byte("[daemon]\nidle_session_timeout = \"10m\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A project config in the working directory carries another value; the
	// unit must not pick it up.
	work := t.TempDir()
	projectConfig := filepath.Join(work, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(projectConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectConfig, []byte("[daemon]\nidle_session_timeout = \"99m\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	webbridge := filepath.Join(t.TempDir(), "webbridge")
	if err := os.WriteFile(webbridge, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, nil
	})
	// install waits, briefly, for the daemon to answer; nothing is
	// listening here, which is the "not answering yet" path.
	fakeDaemonSocket(t, fakeDaemonClient{err: errors.New("dial unix: no such file or directory")})

	var out, errOut bytes.Buffer
	if err := runDaemon(context.Background(), []string{"install", "--webbridge", webbridge},
		strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("runDaemon install: %v\nstderr: %s", err, errOut.String())
	}

	unitPath := filepath.Join(home, "Library", "LaunchAgents", "dev.marshal.daemon.plist")
	body, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read the installed unit: %v", err)
	}
	unit := string(body)
	for _, want := range []string{
		"<string>--idle-session-timeout</string>",
		"<string>10m0s</string>",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit does not carry %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "99m0s") {
		t.Errorf("unit carries the project config's timeout:\n%s", unit)
	}
}

// TestDaemonStatusReportsARunningDaemon pins decision 12's happy path: three
// lines, the daemon's version beside this marshal's, its start time, and its
// projects — and a socket call bounded at two seconds. Under the daemon's line
// it reports each child on a line of its own, with the child's environment
// warning indented beneath it.
func TestDaemonStatusReportsARunningDaemon(t *testing.T) {
	home := daemonTestEnv(t)
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("state = running\npid = 4242\n"), nil
	})

	// The warning is compared verbatim below, so both ends read the one string.
	const warning = "MARSHAL_CONFIG_DIR is not set for this child"

	var gotSocket string
	oldClient := daemonClientFor
	t.Cleanup(func() { daemonClientFor = oldClient })
	daemonClientFor = func(socket string) daemonClient {
		gotSocket = socket
		return fakeDaemonClient{
			info: daemon.Info{
				Version:   "1.4.0",
				Profile:   "local",
				PID:       4242,
				StartedAt: 1700000000000,
				Projects:  []string{"/work/one", "/work/two"},
				Refresh:   &daemon.RefreshInfo{PID: 5151, Restarts: 2},
				Children: []daemon.ChildInfo{
					{
						// Another build, with a warning: the line has to
						// name both versions and the warning follows.
						Root:       "/work/three",
						Version:    "2.0.0",
						Sessions:   []daemon.SessionInfo{{ID: "sess-1", Viewers: 1}},
						EnvWarning: warning,
					},
					{
						// This marshal's own version, no warning, no
						// sessions: no "(this marshal ...)" and no
						// second line.
						Root:    "/work/four",
						Version: "0.9.0",
					},
				},
			},
			onInfo: func(ctx context.Context) {
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Error("the socket call has no deadline")
					return
				}
				if d := time.Until(deadline); d > 3*time.Second || d < time.Second {
					t.Errorf("socket bound = %v, want about 2s", d)
				}
			},
		}
	}

	oldVersion := version
	t.Cleanup(func() { version = oldVersion })
	version = "0.9.0"

	var stdout, stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"status"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runDaemon status: %v\nstderr: %s", err, stderr.String())
	}

	socket := daemon.PathsFor(home).Socket
	if gotSocket != socket {
		t.Errorf("client socket = %q, want %q", gotSocket, socket)
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	// The daemon's three lines, then its refresh helper's, then each
	// child's, the warned child's second one included.
	if len(lines) != 7 {
		t.Fatalf("status printed %d lines, want 7:\n%s", len(lines), stdout.String())
	}
	if !strings.Contains(lines[0], "running") || !strings.Contains(lines[0], "4242") {
		t.Errorf("service line = %q, want the running state and its pid", lines[0])
	}
	if !strings.Contains(lines[1], socket) || !strings.Contains(lines[1], "answering") {
		t.Errorf("socket line = %q, want %q and an answer", lines[1], socket)
	}
	daemonAt := strings.Index(lines[2], "1.4.0")
	marshalAt := strings.Index(lines[2], "0.9.0")
	if daemonAt < 0 || marshalAt < 0 {
		t.Fatalf("version line = %q, want both versions", lines[2])
	}
	if daemonAt > marshalAt {
		t.Errorf("version line = %q, want the daemon's version before this marshal's", lines[2])
	}
	for _, want := range []string{"2023-11-14T22:13:20Z", "/work/one", "/work/two"} {
		if !strings.Contains(lines[2], want) {
			t.Errorf("version line = %q, want it to contain %q", lines[2], want)
		}
	}

	// The refresh helper sits under the daemon's line and above the
	// children's: its pid, and its restarts when it has restarted.
	if want := "refresh: running (pid 5151), restarted 2 times"; lines[3] != want {
		t.Errorf("refresh line = %q, want %q", lines[3], want)
	}

	// The first child runs another build and carries a warning: its line
	// names its own version, this marshal's, and its one session, and the
	// warning follows indented on a line of its own.
	if want := "child /work/three: version 2.0.0, 1 session (this marshal 0.9.0)"; lines[4] != want {
		t.Errorf("child line = %q, want %q", lines[4], want)
	}
	if want := "  environment: " + warning; lines[5] != want {
		t.Errorf("warning line = %q, want %q", lines[5], want)
	}
	// The second child runs this marshal's version with no sessions and no
	// warning: no "(this marshal ...)" aside, and no second line.
	if want := "child /work/four: version 0.9.0, 0 sessions"; lines[6] != want {
		t.Errorf("child line = %q, want %q", lines[6], want)
	}
	for _, line := range lines[4:] {
		if strings.Contains(line, "/work/four") && strings.Contains(line, "this marshal") {
			t.Errorf("child line = %q, want no version aside for a child running this marshal", line)
		}
		if strings.Contains(line, "environment:") && !strings.HasPrefix(line, "  ") {
			t.Errorf("warning line = %q, want it indented under its child", line)
		}
	}
}

// TestDaemonChildLine pins the child line the plan settles: the root, the
// version, and the session count, with this marshal's version beside a child
// that runs another build — and an unstamped child reported as `dev`, as
// daemonVersion reports this marshal.
func TestDaemonChildLine(t *testing.T) {
	cases := []struct {
		name           string
		child          daemon.ChildInfo
		marshalVersion string
		want           string
	}{
		{
			name:           "another build, one session",
			child:          daemon.ChildInfo{Root: "/work/one", Version: "2.0.0", Sessions: []daemon.SessionInfo{{ID: "s"}}},
			marshalVersion: "0.9.0",
			want:           "child /work/one: version 2.0.0, 1 session (this marshal 0.9.0)",
		},
		{
			name:           "the same build, no sessions",
			child:          daemon.ChildInfo{Root: "/work/two", Version: "0.9.0"},
			marshalVersion: "0.9.0",
			want:           "child /work/two: version 0.9.0, 0 sessions",
		},
		{
			name:           "an unstamped child reads as dev",
			child:          daemon.ChildInfo{Root: "/work/three"},
			marshalVersion: "dev",
			want:           "child /work/three: version dev, 0 sessions",
		},
		{
			name: "a pinned child says so",
			child: daemon.ChildInfo{
				Root:     "/work/four",
				Version:  "0.9.0",
				Sessions: []daemon.SessionInfo{{ID: "s"}},
				Pinned:   true,
			},
			marshalVersion: "0.9.0",
			want:           "child /work/four: version 0.9.0, 1 session, pinned",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := daemonChildLine(c.child, c.marshalVersion); got != c.want {
				t.Errorf("daemonChildLine() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestDaemonPinnedLines: a pinned root a child serves is that child's line,
// so it gets no line of its own; a pinned root nothing serves is reported,
// because a pin outlives the child that served it.
func TestDaemonPinnedLines(t *testing.T) {
	info := daemon.Info{
		Pinned: []string{"/work/one", "/work/two"},
		Children: []daemon.ChildInfo{
			{Root: "/work/one", Pinned: true},
		},
	}
	got := daemonPinnedLines(info)
	if len(got) != 1 {
		t.Fatalf("daemonPinnedLines() = %v, want one line for the unserved root", got)
	}
	if want := "pinned /work/two: not running"; got[0] != want {
		t.Errorf("daemonPinnedLines()[0] = %q, want %q", got[0], want)
	}

	// A daemon with no pins at all says nothing.
	if got := daemonPinnedLines(daemon.Info{}); len(got) != 0 {
		t.Errorf("daemonPinnedLines() = %v, want no lines for a daemon with no pins", got)
	}
}

// TestDaemonRefreshLine pins the refresh line's three shapes: no helper at
// all reads `off`, a helper that has not restarted names only its pid, and a
// helper that has restarted adds its count with a real plural.
func TestDaemonRefreshLine(t *testing.T) {
	cases := []struct {
		name    string
		refresh *daemon.RefreshInfo
		want    string
	}{
		{
			name:    "no helper",
			refresh: nil,
			want:    "refresh: off",
		},
		{
			name:    "running, never restarted",
			refresh: &daemon.RefreshInfo{PID: 5151},
			want:    "refresh: running (pid 5151)",
		},
		{
			name:    "running, restarted once",
			refresh: &daemon.RefreshInfo{PID: 5151, Restarts: 1},
			want:    "refresh: running (pid 5151), restarted 1 time",
		},
		{
			name:    "running, restarted thrice",
			refresh: &daemon.RefreshInfo{PID: 5151, Restarts: 3},
			want:    "refresh: running (pid 5151), restarted 3 times",
		},
		{
			name:    "between runs",
			refresh: &daemon.RefreshInfo{},
			want:    "refresh: not running",
		},
		{
			name:    "between runs after a restart",
			refresh: &daemon.RefreshInfo{Restarts: 2},
			want:    "refresh: not running, restarted 2 times",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := daemonRefreshLine(daemon.Info{Refresh: c.refresh}); got != c.want {
				t.Errorf("daemonRefreshLine() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestDaemonStatusFailsWhenTheSocketIsSilent pins decision 12's failure case:
// the service line is still printed, the socket is reported as not answering,
// and only then does status return an error naming the socket.
func TestDaemonStatusFailsWhenTheSocketIsSilent(t *testing.T) {
	home := daemonTestEnv(t)
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("state = running\npid = 77\n"), nil
	})

	oldClient := daemonClientFor
	t.Cleanup(func() { daemonClientFor = oldClient })
	daemonClientFor = func(string) daemonClient {
		return fakeDaemonClient{err: errors.New("dial unix: connect: no such file or directory")}
	}

	var stdout, stderr bytes.Buffer
	err := runDaemon(context.Background(), []string{"status"},
		strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("status succeeded with a silent socket; it must exit non-zero")
	}

	socket := daemon.PathsFor(home).Socket
	if !strings.Contains(err.Error(), "not answering") || !strings.Contains(err.Error(), socket) {
		t.Errorf("error = %q, want it to say the daemon is not answering on %q", err, socket)
	}

	out := stdout.String()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("status printed %d lines, want the service and socket lines:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "running") || !strings.Contains(lines[0], "77") {
		t.Errorf("service line = %q, want it printed before the socket is tried", lines[0])
	}
	if !strings.Contains(lines[1], socket) || !strings.Contains(lines[1], "not answering") {
		t.Errorf("socket line = %q, want %q reported as not answering", lines[1], socket)
	}
}

// TestDaemonPinResolvesTheRepositoryRoot: a pin names a project, so a
// directory inside a repository is pinned at the repository root, and the
// line printed names that root.
func TestDaemonPinResolvesTheRepositoryRoot(t *testing.T) {
	daemonTestEnv(t)

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	var pins []pinCall
	fakeDaemonSocket(t, fakeDaemonClient{pinCalls: &pins})

	var stdout, stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"pin", sub},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runDaemon pin: %v\nstderr: %s", err, stderr.String())
	}
	if len(pins) != 1 {
		t.Fatalf("the daemon was pinned %d times, want once", len(pins))
	}
	if pins[0].root != root {
		t.Errorf("pinned root = %q, want the repository root %q", pins[0].root, root)
	}
	if !pins[0].pinned {
		t.Error("daemon pin asked the daemon to unpin")
	}
	if want := "pinned " + root + "\n"; stdout.String() != want {
		t.Errorf("pin printed %q, want %q", stdout.String(), want)
	}
}

// TestDaemonUnpinDefaultsToTheWorkingDirectory: with no argument, unpin
// unpins the working directory.
func TestDaemonUnpinDefaultsToTheWorkingDirectory(t *testing.T) {
	daemonTestEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)

	var pins []pinCall
	fakeDaemonSocket(t, fakeDaemonClient{pinCalls: &pins})

	var stdout, stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"unpin"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runDaemon unpin: %v\nstderr: %s", err, stderr.String())
	}
	if len(pins) != 1 {
		t.Fatalf("the daemon was unpinned %d times, want once", len(pins))
	}
	if pins[0].root != dir {
		t.Errorf("unpinned root = %q, want the working directory %q", pins[0].root, dir)
	}
	if pins[0].pinned {
		t.Error("daemon unpin asked the daemon to pin")
	}
	if want := "unpinned " + dir + "\n"; stdout.String() != want {
		t.Errorf("unpin printed %q, want %q", stdout.String(), want)
	}
}

// TestDaemonPinRefusesTwoArguments: pin takes one directory, so a second
// argument is refused by name and the daemon is never asked.
func TestDaemonPinRefusesTwoArguments(t *testing.T) {
	daemonTestEnv(t)

	var pins []pinCall
	fakeDaemonSocket(t, fakeDaemonClient{pinCalls: &pins})

	err := runDaemon(context.Background(), []string{"pin", t.TempDir(), "extra"},
		strings.NewReader(""), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("pin with two arguments succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "extra") {
		t.Errorf("error = %q, want it to name %q", err, "extra")
	}
	if len(pins) != 0 {
		t.Errorf("the daemon was pinned %d times, want none", len(pins))
	}
}

// TestDaemonPinReportsASilentDaemon: a daemon that does not answer is an
// error naming its socket, as `status` says it.
func TestDaemonPinReportsASilentDaemon(t *testing.T) {
	home := daemonTestEnv(t)
	// A silent daemon fails the dial, which the client reports as a
	// *url.Error; the fixture carries that shape so the wording under test is
	// the one a real silent socket produces.
	fakeDaemonSocket(t, fakeDaemonClient{pinErr: &url.Error{
		Op:  "Post",
		URL: "http://daemon/api/projects/pin",
		Err: errors.New("dial unix: connect: no such file or directory"),
	}})

	var stdout, stderr bytes.Buffer
	err := runDaemon(context.Background(), []string{"pin", t.TempDir()},
		strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("pin succeeded against a silent daemon, want an error")
	}
	socket := daemon.PathsFor(home).Socket
	if !strings.Contains(err.Error(), "not answering") || !strings.Contains(err.Error(), socket) {
		t.Errorf("error = %q, want it to say the daemon is not answering on %q", err, socket)
	}
	if stdout.Len() != 0 {
		t.Errorf("pin printed %q, want nothing for a silent daemon", stdout.String())
	}
}

// fakeOAuthRefreshRunner replaces the refresh helper's body and counts its
// calls. The real helper blocks until its context ends, so a fake that returns
// at once would let a test finish while pretending it had run the daemon's
// forever-loop; each test decides what its fake does.
func fakeOAuthRefreshRunner(t *testing.T, run func(context.Context, app.OAuthRefreshOptions) error) *int {
	t.Helper()
	old := oauthRefreshRunner
	t.Cleanup(func() { oauthRefreshRunner = old })
	calls := 0
	oauthRefreshRunner = func(ctx context.Context, opts app.OAuthRefreshOptions) error {
		calls++
		return run(ctx, opts)
	}
	return &calls
}

// TestDaemonRefreshRunsTheHelper: `marshal daemon refresh` runs the helper
// once, with the resolved home and a real auth resolver, and installs its own
// signal context — the fake ends when the context the test cancels ends.
func TestDaemonRefreshRunsTheHelper(t *testing.T) {
	home := daemonTestEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got app.OAuthRefreshOptions
	calls := fakeOAuthRefreshRunner(t, func(runCtx context.Context, opts app.OAuthRefreshOptions) error {
		got = opts
		// The helper refreshes until its context ends; the daemon ends it by
		// signalling it, which the test stands in for by cancelling.
		cancel()
		<-runCtx.Done()
		return nil
	})

	var stderr bytes.Buffer
	if err := runDaemon(ctx, []string{"refresh"}, strings.NewReader(""), io.Discard, &stderr); err != nil {
		t.Fatalf("runDaemon refresh: %v\nstderr: %s", err, stderr.String())
	}
	if *calls != 1 {
		t.Fatalf("the refresh helper ran %d times, want once", *calls)
	}
	if got.HomeDir != home {
		t.Errorf("helper home = %q, want the test's home %q", got.HomeDir, home)
	}
	if got.Auth == nil {
		t.Error("helper auth resolver is nil")
	}
	if got.Logger == nil {
		t.Error("helper logger is nil")
	}
}

// TestDaemonRefreshInstallsItsSignalContext is decision 9: main passes a
// background context, so the helper's own signal context is what stops it, and
// its stop runs before the helper returns. The test replaces the signal seam
// rather than signalling the test process.
func TestDaemonRefreshInstallsItsSignalContext(t *testing.T) {
	daemonTestEnv(t)

	parent := context.Background()
	helperCtx, cancelHelper := context.WithCancel(parent)
	defer cancelHelper()

	installs := 0
	stopped := false
	restore := daemonSignalContext
	daemonSignalContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
		installs++
		if ctx != parent {
			t.Error("the signal context was not derived from the caller's context")
		}
		return helperCtx, func() { stopped = true }
	}
	defer func() { daemonSignalContext = restore }()

	calls := fakeOAuthRefreshRunner(t, func(runCtx context.Context, _ app.OAuthRefreshOptions) error {
		if runCtx != helperCtx {
			t.Error("the helper ran on a context the signal seam did not return")
		}
		// The service manager's SIGTERM ends the context the seam returned.
		cancelHelper()
		select {
		case <-runCtx.Done():
		case <-time.After(5 * time.Second):
			t.Error("the helper's context never ended: the signal context was not installed")
		}
		return nil
	})

	if err := runDaemon(parent, []string{"refresh"}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatalf("runDaemon refresh: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("the refresh helper ran %d times, want once", *calls)
	}
	if installs != 1 {
		t.Fatalf("the signal context was installed %d times, want once", installs)
	}
	if !stopped {
		t.Error("the signal context's stop was not called")
	}
}

// TestDaemonRefreshRefusesArguments: refresh takes no arguments, so a stray
// one is refused by name and the helper never runs.
func TestDaemonRefreshRefusesArguments(t *testing.T) {
	daemonTestEnv(t)
	calls := fakeOAuthRefreshRunner(t, func(context.Context, app.OAuthRefreshOptions) error {
		t.Error("the refresh helper ran for an invocation with an argument")
		return nil
	})

	err := runDaemon(context.Background(), []string{"refresh", "extra"},
		strings.NewReader(""), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("refresh with an argument succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "extra") {
		t.Errorf("error = %q, want it to name %q", err, "extra")
	}
	if *calls != 0 {
		t.Errorf("the refresh helper ran %d times, want none", *calls)
	}
}

// TestDaemonRejectsUnknownSubcommands pins the listing: the usage error names
// all five subcommands, for a missing and for an unknown one.
func TestDaemonRejectsUnknownSubcommands(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: nil, want: "subcommand is required"},
		{name: "unknown", args: []string{"frobnicate"}, want: "frobnicate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := runDaemon(context.Background(), c.args, strings.NewReader(""), io.Discard, io.Discard)
			if err == nil {
				t.Fatalf("runDaemon(%v) succeeded, want an error", c.args)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to mention %q", err, c.want)
			}
			for _, sub := range []string{"install", "start", "stop", "status", "pin", "unpin", "uninstall"} {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error = %q, want it to list %q", err, sub)
				}
			}
		})
	}
}
