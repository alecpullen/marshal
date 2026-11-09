package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/daemon"
)

// fakeDaemonClient answers `status` without a socket.
type fakeDaemonClient struct {
	info   daemon.Info
	err    error
	onInfo func(context.Context)
}

func (c fakeDaemonClient) Info(ctx context.Context) (daemon.Info, error) {
	if c.onInfo != nil {
		c.onInfo(ctx)
	}
	return c.info, c.err
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
			"<key>MARSHAL_DATA_DIR</key>",
			"<string>" + dataDir + "</string>",
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
		for _, absent := range []string{"MARSHAL_DATA_DIR", "MARSHAL_CONFIG_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
			if strings.Contains(unit, absent) {
				t.Errorf("unit carries %q although it was unset:\n%s", absent, unit)
			}
		}
	})
}

// TestDaemonStatusReportsARunningDaemon pins decision 12's happy path: three
// lines, the daemon's version beside this marshal's, its start time, and its
// projects — and a socket call bounded at two seconds.
func TestDaemonStatusReportsARunningDaemon(t *testing.T) {
	home := daemonTestEnv(t)
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("state = running\npid = 4242\n"), nil
	})

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
	if len(lines) != 3 {
		t.Fatalf("status printed %d lines, want 3:\n%s", len(lines), stdout.String())
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
			for _, sub := range []string{"install", "start", "stop", "status", "uninstall"} {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error = %q, want it to list %q", err, sub)
				}
			}
		})
	}
}
