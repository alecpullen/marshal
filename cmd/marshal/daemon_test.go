package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/daemon"
	"marshal/internal/fleetprofile"
	"marshal/internal/tunnels"
)

// updateGolden rewrites the checked-in fixtures a currency test compares
// against, so a deliberate document change is one command rather than a
// hand-edited JSON diff.
var updateGolden = flag.Bool("update", false, "rewrite the checked-in fixtures")

// fakeDaemonClient answers `status` without a socket.
type fakeDaemonClient struct {
	info   daemon.Info
	err    error
	onInfo func(context.Context)

	// infos, when set, are the scripted answers to Info, consumed in order:
	// a restart test answers with the daemon's state before the restart and
	// again with a later startedAt once it is back. The last answer repeats,
	// so a poll after the list is spent sees a settled daemon rather than an
	// empty one.
	infos *[]daemon.Info

	// pinState and pinErr are the scripted answer to Pin; pinCalls, when
	// set, records every call, since the fake is copied into the seam and
	// so cannot count on itself.
	pinState daemon.PinState
	pinErr   error
	pinCalls *[]pinCall

	// restartSteps are the scripted lines Restart reports; restartNow, when
	// set, records the now it was called with. Like pinCalls these are
	// pointers because the fake is copied into the seam.
	restartSteps []daemon.RestartProgress
	restartNow   *bool
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
	if c.infos != nil && len(*c.infos) != 0 {
		info := (*c.infos)[0]
		if len(*c.infos) > 1 {
			*c.infos = (*c.infos)[1:]
		}
		return info, c.err
	}
	return c.info, c.err
}

// Restart replays the scripted lines as the client does: each reaches
// progress, and an error line ends the answer with that error.
func (c fakeDaemonClient) Restart(ctx context.Context, now bool, progress func(daemon.RestartProgress)) error {
	if c.restartNow != nil {
		*c.restartNow = now
	}
	for _, step := range c.restartSteps {
		if progress != nil {
			progress(step)
		}
		if step.Error != "" {
			return errors.New(step.Error)
		}
	}
	return nil
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
	fakeDaemonManagerOS(t, "darwin", run)
}

// fakeDaemonManagerOS replaces the service-manager seam for one platform, so
// a test can drive Linux and macOS without a real service manager.
func fakeDaemonManagerOS(t *testing.T, goos string, run daemon.Runner) {
	t.Helper()
	old := daemonManagerFor
	t.Cleanup(func() { daemonManagerFor = old })
	daemonManagerFor = func(home string) (*daemon.Manager, error) {
		m := &daemon.Manager{GOOS: goos, Home: home, UID: 501, Run: run}
		if goos == "darwin" {
			m.UnitDir = filepath.Join(home, "Library", "LaunchAgents")
		} else {
			m.UnitDir = filepath.Join(home, ".config", "systemd", "user")
		}
		return m, nil
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
			// The Studio listener's address is a retired key (one-daemon
			// D10): the installed unit writes no --addr, and the web
			// listener is the only door the daemon opens.
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

// TestDaemonInstallWritesEgress pins that [daemon] egress is read from the
// user config alone and written into the unit, so a change takes effect at
// the next install and a project config cannot turn the proxy on.
func TestDaemonInstallWritesEgress(t *testing.T) {
	home := daemonTestEnv(t)
	t.Setenv("PATH", "/usr/bin:/bin")

	userConfig := filepath.Join(home, ".config", "marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfig, []byte("[daemon]\negress = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A project config in the working directory sets it back off; the unit
	// must not pick that up.
	work := t.TempDir()
	projectConfig := filepath.Join(work, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(projectConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectConfig, []byte("[daemon]\negress = false\n"), 0o644); err != nil {
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
		"<string>--egress</string>",
		"<string>on</string>",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit does not carry %q:\n%s", want, unit)
		}
	}
}

// TestDaemonInstallEnablesLinger pins D1's default: a Linux install reads the
// user's linger bit, sets it when it is off, and says which it did. The bit is
// a warning, not a gate — install has already succeeded by the time it runs.
func TestDaemonInstallEnablesLinger(t *testing.T) {
	daemonTestEnv(t)
	t.Setenv("PATH", "/usr/bin:/bin")

	webbridge := filepath.Join(t.TempDir(), "webbridge")
	if err := os.WriteFile(webbridge, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	const show = "loginctl show-user 501 -p Linger"
	var calls []string
	fakeDaemonManagerOS(t, "linux", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		line := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, line)
		if line == show {
			return []byte("Linger=no\n"), nil
		}
		return nil, nil
	})
	// install waits for the daemon to answer; an answering client means
	// the readiness poll returns at once.
	fakeDaemonSocket(t, fakeDaemonClient{})

	var out, errOut bytes.Buffer
	if err := runDaemon(context.Background(), []string{"install", "--webbridge", webbridge},
		strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("runDaemon install: %v\nstderr: %s", err, errOut.String())
	}

	if !strings.Contains(out.String(), "linger: enabled") {
		t.Errorf("install printed %q, want it to say linger: enabled", out.String())
	}
	if !strings.Contains(strings.Join(calls, "\n"), "loginctl enable-linger 501") {
		t.Errorf("install did not enable linger; manager calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestDaemonInstallLeavesLingerOffWithTheFlag: --no-linger is an opt-out, so
// install neither reads nor sets the bit.
func TestDaemonInstallLeavesLingerOffWithTheFlag(t *testing.T) {
	daemonTestEnv(t)
	t.Setenv("PATH", "/usr/bin:/bin")

	webbridge := filepath.Join(t.TempDir(), "webbridge")
	if err := os.WriteFile(webbridge, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var calls []string
	fakeDaemonManagerOS(t, "linux", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil, nil
	})
	fakeDaemonSocket(t, fakeDaemonClient{})

	var out, errOut bytes.Buffer
	if err := runDaemon(context.Background(), []string{"install", "--webbridge", webbridge, "--no-linger"},
		strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("runDaemon install --no-linger: %v\nstderr: %s", err, errOut.String())
	}

	if !strings.Contains(out.String(), "linger: left off (--no-linger)") {
		t.Errorf("install printed %q, want it to say linger: left off (--no-linger)", out.String())
	}
	for _, call := range calls {
		if strings.Contains(call, "loginctl") {
			t.Errorf("install --no-linger ran %q; it must not touch linger", call)
		}
	}
}

// TestDaemonInstallPrintsLingerAtAFixedPosition: the linger line holds one
// place in install's output — after the unit and socket it names, and before
// the readiness note — so a script that reads the block sees it in the same
// spot whether or not linger needed setting.
func TestDaemonInstallPrintsLingerAtAFixedPosition(t *testing.T) {
	daemonTestEnv(t)
	t.Setenv("PATH", "/usr/bin:/bin")

	webbridge := filepath.Join(t.TempDir(), "webbridge")
	if err := os.WriteFile(webbridge, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	fakeDaemonManagerOS(t, "linux", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		line := strings.Join(append([]string{name}, args...), " ")
		if line == "loginctl show-user 501 -p Linger" {
			return []byte("Linger=yes\n"), nil
		}
		return nil, nil
	})
	// An answering daemon makes the readiness note the single "answering"
	// line rather than a poll that runs for daemonStartTimeout.
	fakeDaemonSocket(t, fakeDaemonClient{})

	var out, errOut bytes.Buffer
	if err := runDaemon(context.Background(), []string{"install", "--webbridge", webbridge},
		strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("runDaemon install: %v\nstderr: %s", err, errOut.String())
	}

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	at := func(prefix string) int {
		for i, line := range lines {
			if strings.HasPrefix(line, prefix) {
				return i
			}
		}
		return -1
	}
	unit, socket, linger, ready := at("unit:"), at("socket:"), at("linger:"), at("answering")
	if unit < 0 || socket < 0 || linger < 0 || ready < 0 {
		t.Fatalf("install printed %d lines %q, want unit:, socket:, linger: and the readiness note:\n%s",
			len(lines), lines, out.String())
	}
	if !(unit < socket && socket < linger && linger < ready) {
		t.Errorf("install order unit@%d socket@%d linger@%d ready@%d, want unit < socket < linger < ready:\n%s",
			unit, socket, linger, ready, out.String())
	}
}

// TestDaemonInstallReportsAnAlreadyEnabledLinger: with the bit already set,
// install says so and issues no enable-linger — the privilege is not asked for
// again when the wanted end state already holds.
func TestDaemonInstallReportsAnAlreadyEnabledLinger(t *testing.T) {
	daemonTestEnv(t)
	t.Setenv("PATH", "/usr/bin:/bin")

	webbridge := filepath.Join(t.TempDir(), "webbridge")
	if err := os.WriteFile(webbridge, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var calls []string
	fakeDaemonManagerOS(t, "linux", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		line := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, line)
		if line == "loginctl show-user 501 -p Linger" {
			return []byte("Linger=yes\n"), nil
		}
		return nil, nil
	})
	fakeDaemonSocket(t, fakeDaemonClient{})

	var out, errOut bytes.Buffer
	if err := runDaemon(context.Background(), []string{"install", "--webbridge", webbridge},
		strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("runDaemon install: %v\nstderr: %s", err, errOut.String())
	}

	if !strings.Contains(out.String(), "linger: already enabled") {
		t.Errorf("install printed %q, want it to say linger: already enabled", out.String())
	}
	for _, call := range calls {
		if strings.Contains(call, "enable-linger") {
			t.Errorf("install ran %q with linger already on; it must not set it again", call)
		}
	}
}

// TestDaemonStatusReportsLinger: status answers whether the install survives
// logout, on the line after the service line and exactly once.
func TestDaemonStatusReportsLinger(t *testing.T) {
	daemonTestEnv(t)

	fakeDaemonManagerOS(t, "linux", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		line := strings.Join(append([]string{name}, args...), " ")
		switch line {
		case "loginctl show-user 501 -p Linger":
			return []byte("Linger=yes\n"), nil
		case "systemctl --user show --property=ActiveState,MainPID,LoadState " + daemon.Unit:
			return []byte("LoadState=loaded\nActiveState=active\nMainPID=4242\n"), nil
		}
		return nil, nil
	})
	fakeDaemonSocket(t, fakeDaemonClient{})

	var stdout, stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"status"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runDaemon status: %v\nstderr: %s", err, stderr.String())
	}

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("status printed %d lines, want the service and linger lines:\n%s", len(lines), stdout.String())
	}
	if !strings.HasPrefix(lines[0], "service:") {
		t.Errorf("line 0 = %q, want the service line", lines[0])
	}
	if lines[1] != "linger: enabled" {
		t.Errorf("line 1 = %q, want \"linger: enabled\"", lines[1])
	}
	if got := strings.Count(stdout.String(), "linger:"); got != 1 {
		t.Errorf("status printed linger %d times, want once:\n%s", got, stdout.String())
	}
}

// TestDaemonUninstallLeavesLingerEnabled pins D1's decision 3: the bit is per
// user, so uninstall reports it and never clears it.
func TestDaemonUninstallLeavesLingerEnabled(t *testing.T) {
	daemonTestEnv(t)

	var calls []string
	fakeDaemonManagerOS(t, "linux", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		line := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, line)
		if line == "loginctl show-user 501 -p Linger" {
			return []byte("Linger=yes\n"), nil
		}
		return nil, nil
	})

	var stdout, stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"uninstall"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runDaemon uninstall: %v\nstderr: %s", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "linger: left enabled (per user, not per unit)") {
		t.Errorf("uninstall printed %q, want it to say the bit was left enabled", stdout.String())
	}
	for _, call := range calls {
		if strings.Contains(call, "disable-linger") {
			t.Errorf("uninstall ran %q; it must not clear a bit it does not own", call)
		}
	}
}

// TestDaemonStatusReportsARunningDaemon pins decision 12's happy path: three
// lines, the daemon's version beside this marshal's, its start time, and its
// projects — and a socket call bounded at two seconds. Under the daemon's line
// it reports each child on a line of its own, the restart that child holds and
// its environment warning indented beneath it.
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
						// Another build, with a warning and a held
						// restart: the line has to name both versions,
						// the restart follows it, and the warning last.
						Root:       "/work/three",
						Version:    "2.0.0",
						Sessions:   []daemon.SessionInfo{{ID: "sess-1", Viewers: 1}},
						EnvWarning: warning,
						Restart: &daemon.DaemonRestart{
							Reasons:    []string{"a new build", "config changed"},
							WaitingFor: "2 sessions",
						},
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
	// The daemon's three lines, then its refresh helper's, then its web and
	// Studio lines, then each child's, the held restart and the warned child's
	// warning included.
	if len(lines) != 10 {
		t.Fatalf("status printed %d lines, want 10:\n%s", len(lines), stdout.String())
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
	// The web listener's line follows it; this daemon reports none.
	if want := "web: off"; lines[4] != want {
		t.Errorf("web line = %q, want %q", lines[4], want)
	}
	// The Studio's line follows the web line's; this daemon found no files,
	// which it says rather than leaving a blank.
	if want := "studio: no files found (reinstall Marshal, or build them: see AGENTS.md)"; lines[5] != want {
		t.Errorf("studio line = %q, want %q", lines[5], want)
	}

	// The first child runs another build with a held restart and carries a
	// warning: its line names its own version, this marshal's, and its one
	// session; the restart follows it, then the warning, both indented.
	if want := "child /work/three: version 2.0.0, 1 session (this marshal 0.9.0)"; lines[6] != want {
		t.Errorf("child line = %q, want %q", lines[6], want)
	}
	if want := "  restart pending: a new build, config changed, waiting for 2 sessions"; lines[7] != want {
		t.Errorf("restart line = %q, want %q under the child's", lines[7], want)
	}
	if want := "  environment: " + warning; lines[8] != want {
		t.Errorf("warning line = %q, want %q", lines[8], want)
	}
	// The second child runs this marshal's version with no sessions, no
	// warning and no restart: no "(this marshal ...)" aside, and no second
	// line.
	if want := "child /work/four: version 0.9.0, 0 sessions"; lines[9] != want {
		t.Errorf("child line = %q, want %q", lines[9], want)
	}
	for _, line := range lines[6:] {
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

// TestDaemonRestartLines pins the restart lines under a child's: nothing for a
// child that is current, the reasons and what they wait for while a restart is
// held, the same reasons without the waiter when nothing holds it, and a
// refusal on a line of its own. A restart with no reasons prints no bare
// `restart pending: ` line, so a refusal alone still says what it is.
func TestDaemonRestartLines(t *testing.T) {
	cases := []struct {
		name    string
		restart *daemon.DaemonRestart
		want    []string
	}{
		{
			name:    "none",
			restart: nil,
			want:    nil,
		},
		{
			name: "waiting",
			restart: &daemon.DaemonRestart{
				Reasons:    []string{"a new build", "config changed"},
				WaitingFor: "2 sessions",
			},
			want: []string{"  restart pending: a new build, config changed, waiting for 2 sessions"},
		},
		{
			name: "not waiting",
			restart: &daemon.DaemonRestart{
				Reasons: []string{"a new build"},
			},
			want: []string{"  restart pending: a new build"},
		},
		{
			name: "refused",
			restart: &daemon.DaemonRestart{
				Reasons: []string{"a new build"},
				Refused: "exit status 2",
			},
			want: []string{
				"  restart pending: a new build",
				"  restart refused: exit status 2",
			},
		},
		{
			name: "refused with no reasons",
			restart: &daemon.DaemonRestart{
				Refused: "exit status 2",
			},
			want: []string{"  restart refused: exit status 2"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := daemonRestartLines(daemon.ChildInfo{Restart: c.restart})
			if len(got) != len(c.want) {
				t.Fatalf("daemonRestartLines() = %q, want %q", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("daemonRestartLines()[%d] = %q, want %q", i, got[i], c.want[i])
				}
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

// TestTheStatusDocumentIsCurrent holds the JSON the tray app reads to the
// example checked in beside the Flutter tests. A field added to the Go
// document, dropped from it, or renamed fails here; regenerate with -update.
func TestTheStatusDocumentIsCurrent(t *testing.T) {
	const path = "../../test/contract/daemon-status.json"

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var status daemon.Status
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	got, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		t.Fatalf("encode status: %v", err)
	}
	got = append(got, '\n')

	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	if string(got) != string(raw) {
		t.Errorf("%s is stale; regenerate with\n  go test ./cmd/marshal -run TestTheStatusDocumentIsCurrent -update", path)
	}
}

// TestDaemonStatusJSONExitsZeroWhenSilent pins the decision the tray app
// depends on: `--json` exits 0 whenever it wrote the document, and the
// document says the socket did not answer.
func TestDaemonStatusJSONExitsZeroWhenSilent(t *testing.T) {
	daemonTestEnv(t)
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("state = running\npid = 77\n"), nil
	})
	fakeDaemonSocket(t, fakeDaemonClient{err: errors.New("dial unix: connect: no such file or directory")})

	var stdout, stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"status", "--json"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("status --json with a silent socket: %v\nstderr: %s", err, stderr.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("decode the status document: %v\n%s", err, stdout.String())
	}
	if _, ok := raw["daemon"]; ok {
		t.Errorf("the document carries a daemon key a silent socket cannot have:\n%s", stdout.String())
	}
	var status daemon.Status
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatalf("decode the status document: %v", err)
	}
	if status.Answering {
		t.Errorf("answering = true with a silent socket:\n%s", stdout.String())
	}
	if status.SchemaVersion != daemon.StatusSchema {
		t.Errorf("schemaVersion = %d, want %d", status.SchemaVersion, daemon.StatusSchema)
	}
	// The service state is present even when the socket is not: the tray
	// reads install/start/stop from it.
	if !status.Service.Running || status.Service.PID != 77 {
		t.Errorf("service = %+v, want the running service and its pid", status.Service)
	}
	if strings.Contains(stdout.String(), "socket:") {
		t.Errorf("--json printed a human line:\n%s", stdout.String())
	}
}

// TestDaemonStatusJSONCarriesTheDaemon: an answering daemon is in the
// document, at the schema the app speaks.
func TestDaemonStatusJSONCarriesTheDaemon(t *testing.T) {
	daemonTestEnv(t)
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("state = running\npid = 4242\n"), nil
	})
	fakeDaemonSocket(t, fakeDaemonClient{info: daemon.Info{
		Version:  "1.4.0",
		Profile:  "local",
		PID:      4242,
		Projects: []string{"/work/one"},
	}})

	var stdout, stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"status", "--json"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("status --json: %v\nstderr: %s", err, stderr.String())
	}

	var status daemon.Status
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatalf("decode the status document: %v\n%s", err, stdout.String())
	}
	if status.SchemaVersion != 1 {
		t.Errorf("schemaVersion = %d, want 1", status.SchemaVersion)
	}
	if !status.Answering {
		t.Error("answering = false for an answering daemon")
	}
	if status.Daemon == nil {
		t.Fatalf("no daemon in the document:\n%s", stdout.String())
	}
	if status.Daemon.Version != "1.4.0" {
		t.Errorf("daemon.version = %q, want %q", status.Daemon.Version, "1.4.0")
	}
	if status.Service.PID != 4242 {
		t.Errorf("service.pid = %d, want 4242", status.Service.PID)
	}
}

// TestDaemonStatusJSONReportsLinger: the document always carries a linger
// object, and on Linux it reports the bit D1's install sets.
func TestDaemonStatusJSONReportsLinger(t *testing.T) {
	daemonTestEnv(t)
	fakeDaemonManagerOS(t, "linux", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		line := strings.Join(append([]string{name}, args...), " ")
		switch line {
		case "loginctl show-user 501 -p Linger":
			return []byte("Linger=yes\n"), nil
		case "systemctl --user show --property=ActiveState,MainPID,LoadState " + daemon.Unit:
			return []byte("LoadState=loaded\nActiveState=active\nMainPID=4242\n"), nil
		}
		return nil, nil
	})
	fakeDaemonSocket(t, fakeDaemonClient{})

	var stdout, stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"status", "--json"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("status --json: %v\nstderr: %s", err, stderr.String())
	}
	var status daemon.Status
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatalf("decode the status document: %v", err)
	}
	if !status.Linger.Supported || !status.Linger.Enabled {
		t.Errorf("linger = %+v, want supported and enabled", status.Linger)
	}
	if strings.Contains(stdout.String(), "linger:") {
		t.Errorf("--json printed a human line:\n%s", stdout.String())
	}
}

// TestDaemonRestartPrintsWhatItWaitsFor: restart reports what the daemon is
// waiting for as it sweeps, prints a repeated waiting line once, then names
// the build it restarts on and reports the version it came back on.
func TestDaemonRestartPrintsWhatItWaitsFor(t *testing.T) {
	daemonTestEnv(t)

	infos := []daemon.Info{
		{Version: "v1", StartedAt: 1700000000000},
		{Version: "v2", StartedAt: 1700000005000},
	}
	fakeDaemonSocket(t, fakeDaemonClient{
		infos: &infos,
		restartSteps: []daemon.RestartProgress{
			{WaitingFor: []string{"a run in /work/one"}},
			{WaitingFor: []string{"a run in /work/one"}},
			{Restarting: true, Version: "v2"},
		},
	})

	var stdout, stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"restart"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runDaemon restart: %v\nstderr: %s", err, stderr.String())
	}

	want := []string{
		"waiting for a run in /work/one",
		"restarting on v2",
		"restarted: version v2",
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("restart printed %q, want %q", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

// TestDaemonRestartNowAsksForNow: --now is the request's only argument, and it
// reaches the daemon; without it the daemon waits for idle.
func TestDaemonRestartNowAsksForNow(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "waits for idle", args: []string{"restart"}, want: false},
		{name: "--now does not wait", args: []string{"restart", "--now"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			daemonTestEnv(t)

			infos := []daemon.Info{
				{Version: "v1", StartedAt: 1700000000000},
				{Version: "v2", StartedAt: 1700000005000},
			}
			var gotNow bool
			fakeDaemonSocket(t, fakeDaemonClient{
				infos:        &infos,
				restartSteps: []daemon.RestartProgress{{Restarting: true, Version: "v2"}},
				restartNow:   &gotNow,
			})

			var stdout, stderr bytes.Buffer
			if err := runDaemon(context.Background(), tc.args,
				strings.NewReader(""), &stdout, &stderr); err != nil {
				t.Fatalf("runDaemon %v: %v\nstderr: %s", tc.args, err, stderr.String())
			}
			if !strings.Contains(stdout.String(), "restarting on v2") {
				t.Fatalf("restart printed %q, want it to have asked the daemon", stdout.String())
			}
			if gotNow != tc.want {
				t.Errorf("restart --now = %v, want %v", gotNow, tc.want)
			}
		})
	}
}

// TestDaemonRestartStartsAnAbsentDaemon: a daemon that does not answer has no
// restart to ask for, so restart starts it, as `start` does, and says so.
func TestDaemonRestartStartsAnAbsentDaemon(t *testing.T) {
	home := daemonTestEnv(t)

	var calls []string
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil, nil
	})
	// Nothing is listening: the socket read and the readiness poll after the
	// start both fail, which is the absent daemon this command starts.
	fakeDaemonSocket(t, fakeDaemonClient{err: errors.New("dial unix: connect: no such file or directory")})

	var stdout, stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"restart"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runDaemon restart: %v\nstderr: %s", err, stderr.String())
	}

	if len(calls) == 0 {
		t.Fatal("restart never reached the service manager")
	}
	if joined := strings.Join(calls, "\n"); !strings.Contains(joined, "bootstrap") {
		t.Errorf("restart did not load the unit; manager calls:\n%s", joined)
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if lines[0] != "started" {
		t.Errorf("first line = %q, want \"started\"", lines[0])
	}
	if log := daemon.PathsFor(home).Log; !strings.Contains(stdout.String(), log) {
		t.Errorf("restart printed %q, want it to name the log %q", stdout.String(), log)
	}
}

// TestDaemonRestartReportsARefusal: the daemon's error line — the new build
// failed — becomes the command's error, and no restart is reported.
func TestDaemonRestartReportsARefusal(t *testing.T) {
	daemonTestEnv(t)

	const refusal = "the new build failed --version"
	infos := []daemon.Info{{Version: "v1", StartedAt: 1700000000000}}
	fakeDaemonSocket(t, fakeDaemonClient{
		infos: &infos,
		restartSteps: []daemon.RestartProgress{
			{WaitingFor: []string{"2 sessions"}},
			{Error: refusal},
		},
	})

	var stdout, stderr bytes.Buffer
	err := runDaemon(context.Background(), []string{"restart"},
		strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("restart succeeded although the daemon refused, want an error")
	}
	if !strings.Contains(err.Error(), "marshal daemon restart:") || !strings.Contains(err.Error(), refusal) {
		t.Errorf("error = %q, want \"marshal daemon restart: %s\"", err, refusal)
	}
	if strings.Contains(stdout.String(), "restarted:") {
		t.Errorf("restart printed %q, want no restarted line for a refusal", stdout.String())
	}
}

// TestDaemonRestartRefusesArguments: restart takes only --now, so a stray
// argument is refused by name and the daemon is never asked.
func TestDaemonRestartRefusesArguments(t *testing.T) {
	daemonTestEnv(t)
	fakeDaemonSocket(t, fakeDaemonClient{onInfo: func(context.Context) {
		t.Error("restart with an argument asked the daemon")
	}})

	err := runDaemon(context.Background(), []string{"restart", "extra"},
		strings.NewReader(""), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("restart with an argument succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "extra") {
		t.Errorf("error = %q, want it to name %q", err, "extra")
	}
}

// TestDaemonStatusShowsTheDaemonsRestart: status reports the daemon's own held
// restart on the line after the daemon's, before the refresh helper's and
// before any child's — pending with its reasons and waiter, and a refusal on a
// line of its own.
func TestDaemonStatusShowsTheDaemonsRestart(t *testing.T) {
	cases := []struct {
		name    string
		restart *daemon.DaemonRestart
		want    []string
	}{
		{
			name: "pending",
			restart: &daemon.DaemonRestart{
				Reasons:    []string{"a new build", "config changed"},
				WaitingFor: "2 sessions",
			},
			want: []string{"daemon restart pending: a new build, config changed, waiting for 2 sessions"},
		},
		{
			name: "refused",
			restart: &daemon.DaemonRestart{
				Reasons: []string{"a new build"},
				Refused: "exit status 2",
			},
			want: []string{
				"daemon restart pending: a new build",
				"daemon restart refused: exit status 2",
			},
		},
		{
			name:    "refused with no reasons",
			restart: &daemon.DaemonRestart{Refused: "exit status 2"},
			want:    []string{"daemon restart refused: exit status 2"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			daemonTestEnv(t)
			fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte("state = running\npid = 4242\n"), nil
			})
			fakeDaemonSocket(t, fakeDaemonClient{info: daemon.Info{
				Version:   "1.4.0",
				PID:       4242,
				StartedAt: 1700000000000,
				Restart:   c.restart,
			}})

			var stdout, stderr bytes.Buffer
			if err := runDaemon(context.Background(), []string{"status"},
				strings.NewReader(""), &stdout, &stderr); err != nil {
				t.Fatalf("runDaemon status: %v\nstderr: %s", err, stderr.String())
			}

			lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			daemonAt := -1
			for i, line := range lines {
				if strings.HasPrefix(line, "daemon: version") {
					daemonAt = i
					break
				}
			}
			if daemonAt < 0 {
				t.Fatalf("status printed no daemon line:\n%s", stdout.String())
			}
			for i, want := range c.want {
				at := daemonAt + 1 + i
				if at >= len(lines) {
					t.Fatalf("status printed %d lines, want %q at %d:\n%s", len(lines), want, at, stdout.String())
				}
				if lines[at] != want {
					t.Errorf("line %d = %q, want %q", at, lines[at], want)
				}
			}
			if next := daemonAt + 1 + len(c.want); next < len(lines) && strings.HasPrefix(lines[next], "daemon restart") {
				t.Errorf("line %d = %q, want no further restart line", next, lines[next])
			}
		})
	}
}

// TestDaemonRejectsUnknownSubcommands pins the listing: the usage error names
// every subcommand, for a missing and for an unknown one.
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
			for _, sub := range []string{"install", "start", "stop", "status", "restart", "pin", "unpin", "uninstall"} {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error = %q, want it to list %q", err, sub)
				}
			}
		})
	}
}

// daemonTestWebbridge writes an executable the unit can name as its web bridge
// and returns its path, so a unit built by daemonUnitSpec resolves one.
func daemonTestWebbridge(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "webbridge")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// daemonWriteUserConfig writes the user config daemonUnitSpec reads, for the
// tests that state [daemon] settings.
func daemonWriteUserConfig(t *testing.T, home, body string) {
	t.Helper()
	path := filepath.Join(home, ".config", "marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// daemonInstallUnit runs `marshal daemon install` against the fake service
// manager and answers the unit it wrote, so a test reads what the daemon will
// run.
func daemonInstallUnit(t *testing.T, home string) string {
	t.Helper()
	t.Setenv("PATH", "/usr/bin:/bin")
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, nil
	})
	// install waits, briefly, for the daemon to answer; nothing is listening
	// here, which is the "not answering yet" path.
	fakeDaemonSocket(t, fakeDaemonClient{err: errors.New("dial unix: no such file or directory")})
	var out, errOut bytes.Buffer
	if err := runDaemon(context.Background(), []string{"install", "--webbridge", daemonTestWebbridge(t)},
		strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("runDaemon install: %v\nstderr: %s", err, errOut.String())
	}
	body, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "dev.marshal.daemon.plist"))
	if err != nil {
		t.Fatalf("read the installed unit: %v", err)
	}
	return string(body)
}

// daemonInstallRun runs `marshal daemon install` with args against the fake
// service manager and answers what it printed, what it wrote to stderr, and its
// error.
func daemonInstallRun(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return daemonInstallRunWebbridge(t, daemonTestWebbridge(t), args...)
}

// daemonInstallRunWebbridge is daemonInstallRun over a named webbridge, for the
// tests that need the Studio's files beside it.
func daemonInstallRunWebbridge(t *testing.T, webbridge string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("PATH", "/usr/bin:/bin")
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, nil
	})
	// install waits, briefly, for the daemon to answer; nothing is listening
	// here, which is the "not answering yet" path.
	fakeDaemonSocket(t, fakeDaemonClient{err: errors.New("dial unix: no such file or directory")})
	var out, errOut bytes.Buffer
	err := runDaemon(context.Background(),
		append([]string{"install", "--webbridge", webbridge}, args...),
		strings.NewReader(""), &out, &errOut)
	return out.String(), errOut.String(), err
}

// daemonReadUserConfig is the user config as the next load reads it.
func daemonReadUserConfig(t *testing.T, home string) config.Config {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{HomeDir: home, SkipProjectConfig: true})
	if err != nil {
		t.Fatalf("read the user config: %v", err)
	}
	return cfg
}

// TestDaemonInstallWritesTheWebListener: the five [daemon] settings reach the
// unit as the arguments that serve them (D3, D10).
func TestDaemonInstallWritesTheWebListener(t *testing.T) {
	home := daemonTestEnv(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	daemonWriteUserConfig(t, home, "[daemon]\n"+
		"web_listen = \"/run/user/501/marshal/web.sock\"\n"+
		"web_group = \"marshal\"\n"+
		"web_url = \"https://box.example.com\"\n"+
		"preview_listen = \"127.0.0.1:7790\"\n"+
		"preview_url = \"https://preview.example.com\"\n")

	spec, _, err := daemonUnitSpec(daemonTestWebbridge(t), home)
	if err != nil {
		t.Fatalf("daemonUnitSpec: %v", err)
	}
	for _, c := range []struct{ name, got, want string }{
		{name: "WebListen", got: spec.WebListen, want: "/run/user/501/marshal/web.sock"},
		{name: "WebGroup", got: spec.WebGroup, want: "marshal"},
		{name: "PublicURL", got: spec.PublicURL, want: "https://box.example.com"},
		{name: "PreviewListen", got: spec.PreviewListen, want: "127.0.0.1:7790"},
		{name: "PreviewURL", got: spec.PreviewURL, want: "https://preview.example.com"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	// And each is written into the unit as the argument that carries it.
	unit := daemonInstallUnit(t, home)
	for _, arg := range [][2]string{
		{"--web-listen", spec.WebListen},
		{"--web-group", spec.WebGroup},
		{"--public-url", spec.PublicURL},
		{"--preview-addr", spec.PreviewListen},
		{"--preview-url", spec.PreviewURL},
	} {
		pair := "<string>" + arg[0] + "</string>\n\t\t<string>" + arg[1] + "</string>"
		if !strings.Contains(unit, pair) {
			t.Errorf("unit does not carry %q after %q:\n%s", arg[1], arg[0], unit)
		}
	}
}

// TestDaemonInstallStudioSetsTheListener: --studio writes daemon.web_listen =
// "unix" — the form that needs no D13 warning — and the unit the daemon runs
// carries it (D3, D10).
func TestDaemonInstallStudioSetsTheListener(t *testing.T) {
	home := daemonTestEnv(t)
	if _, stderr, err := daemonInstallRun(t, "--studio"); err != nil {
		t.Fatalf("install --studio: %v\nstderr: %s", err, stderr)
	}
	cfg := daemonReadUserConfig(t, home)
	if got := cfg.Daemon.WebListen; got != "unix" {
		t.Errorf("daemon.web_listen = %q, want unix", got)
	}
	if got := cfg.Daemon.WebURL; got != "" {
		t.Errorf("daemon.web_url = %q, want it left unset", got)
	}
	unit, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "dev.marshal.daemon.plist"))
	if err != nil {
		t.Fatalf("read the installed unit: %v", err)
	}
	if !strings.Contains(string(unit), "<string>--web-listen</string>\n\t\t<string>unix</string>") {
		t.Errorf("the unit does not carry --web-listen unix:\n%s", unit)
	}
}

// TestDaemonInstallStudioKeepsAnExistingListener: an install restarts the unit,
// so --studio must not move a listener the operator already points a proxy at.
func TestDaemonInstallStudioKeepsAnExistingListener(t *testing.T) {
	home := daemonTestEnv(t)
	daemonWriteUserConfig(t, home, "[daemon]\nweb_listen = \"127.0.0.1:7700\"\n")
	if _, stderr, err := daemonInstallRun(t, "--studio"); err != nil {
		t.Fatalf("install --studio: %v\nstderr: %s", err, stderr)
	}
	if got := daemonReadUserConfig(t, home).Daemon.WebListen; got != "127.0.0.1:7700" {
		t.Errorf("daemon.web_listen = %q, want the existing 127.0.0.1:7700", got)
	}
}

// TestDaemonInstallStudioURLNeedsAListener: an address with nothing listening is
// a meaningless pair, refused before anything is written.
func TestDaemonInstallStudioURLNeedsAListener(t *testing.T) {
	home := daemonTestEnv(t)
	_, _, err := daemonInstallRun(t, "--studio-url", "https://marshal.example.com")
	if err == nil {
		t.Fatal("install --studio-url with no listener did not fail")
	}
	if !strings.Contains(err.Error(), "--studio-url needs a listener") {
		t.Errorf("error = %v, want it to name the missing listener", err)
	}
	cfg := daemonReadUserConfig(t, home)
	if cfg.Daemon.WebListen != "" || cfg.Daemon.WebURL != "" {
		t.Errorf("a refused --studio-url wrote web_listen = %q, web_url = %q",
			cfg.Daemon.WebListen, cfg.Daemon.WebURL)
	}
}

// TestDaemonInstallStudioRefusesAGroupWithUnix: web_group needs a socket path of
// its own (D3), so --studio names that rather than writing a pair the daemon
// would refuse at start.
func TestDaemonInstallStudioRefusesAGroupWithUnix(t *testing.T) {
	home := daemonTestEnv(t)
	daemonWriteUserConfig(t, home, "[daemon]\nweb_group = \"marshal\"\n")
	_, _, err := daemonInstallRun(t, "--studio")
	if err == nil {
		t.Fatal("install --studio with daemon.web_group did not fail")
	}
	if !strings.Contains(err.Error(), "daemon.web_group") {
		t.Errorf("error = %v, want it to name daemon.web_group", err)
	}
	if got := daemonReadUserConfig(t, home).Daemon.WebListen; got != "" {
		t.Errorf("a refused --studio wrote web_listen = %q", got)
	}
}

// TestDaemonInstallStudioURLRefusesANonHTTPAddress: daemonPublicURL's rule is
// the only one this command applies, and a value it refuses is never written.
func TestDaemonInstallStudioURLRefusesANonHTTPAddress(t *testing.T) {
	home := daemonTestEnv(t)
	_, _, err := daemonInstallRun(t, "--studio", "--studio-url", "ftp://marshal.example.com")
	if err == nil {
		t.Fatal("install --studio-url ftp:// did not fail")
	}
	if !strings.Contains(err.Error(), "daemon.web_url") {
		t.Errorf("error = %v, want it to name daemon.web_url", err)
	}
	cfg := daemonReadUserConfig(t, home)
	if cfg.Daemon.WebListen != "" || cfg.Daemon.WebURL != "" {
		t.Errorf("a refused pair was written: web_listen = %q, web_url = %q",
			cfg.Daemon.WebListen, cfg.Daemon.WebURL)
	}
}

// TestDaemonInstallStudioURLWritesTheAddress: the address is a second run, which
// is why --studio-url exists apart from --studio, and the unit carries it as
// --public-url.
func TestDaemonInstallStudioURLWritesTheAddress(t *testing.T) {
	home := daemonTestEnv(t)
	if _, stderr, err := daemonInstallRun(t, "--studio"); err != nil {
		t.Fatalf("install --studio: %v\nstderr: %s", err, stderr)
	}
	if _, stderr, err := daemonInstallRun(t, "--studio-url", "https://marshal.example.com"); err != nil {
		t.Fatalf("install --studio-url: %v\nstderr: %s", err, stderr)
	}
	cfg := daemonReadUserConfig(t, home)
	if got := cfg.Daemon.WebListen; got != "unix" {
		t.Errorf("daemon.web_listen = %q, want unix", got)
	}
	if got := cfg.Daemon.WebURL; got != "https://marshal.example.com" {
		t.Errorf("daemon.web_url = %q, want the address", got)
	}
	unit, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "dev.marshal.daemon.plist"))
	if err != nil {
		t.Fatalf("read the installed unit: %v", err)
	}
	if !strings.Contains(string(unit), "<string>--public-url</string>\n\t\t<string>https://marshal.example.com</string>") {
		t.Errorf("the unit does not carry --public-url:\n%s", unit)
	}
}

// TestDaemonInstallStudioKeepsUnrelatedSections: the write goes through the
// user-config writer, so a provider table set under it survives.
func TestDaemonInstallStudioKeepsUnrelatedSections(t *testing.T) {
	home := daemonTestEnv(t)
	daemonWriteUserConfig(t, home, "[providers.local]\nbase_url = \"http://127.0.0.1:11434/v1\"\n")
	if _, stderr, err := daemonInstallRun(t, "--studio"); err != nil {
		t.Fatalf("install --studio: %v\nstderr: %s", err, stderr)
	}
	cfg := daemonReadUserConfig(t, home)
	p, ok := cfg.Providers["local"]
	if !ok {
		t.Fatalf("[providers.local] is gone: %+v", cfg.Providers)
	}
	if p.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Errorf("base_url = %q, want it kept", p.BaseURL)
	}
}

// TestDaemonInstallAcceptsAPreviewTemplate: daemon.preview_url may be a
// template for a proxy with wildcard hostnames, and the unit writes it as it
// stands — the daemon fills the token in per preview.
func TestDaemonInstallAcceptsAPreviewTemplate(t *testing.T) {
	home := daemonTestEnv(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	daemonWriteUserConfig(t, home, "[daemon]\n"+
		"preview_listen = \"127.0.0.1:7790\"\n"+
		"preview_url = \"https://p{token}.preview.example.com\"\n")

	spec, _, err := daemonUnitSpec(daemonTestWebbridge(t), home)
	if err != nil {
		t.Fatalf("daemonUnitSpec: %v", err)
	}
	if want := "https://p{token}.preview.example.com"; spec.PreviewURL != want {
		t.Errorf("PreviewURL = %q, want %q", spec.PreviewURL, want)
	}

	unit := daemonInstallUnit(t, home)
	pair := "<string>--preview-url</string>\n\t\t<string>https://p{token}.preview.example.com</string>"
	if !strings.Contains(unit, pair) {
		t.Errorf("unit does not carry the template after --preview-url:\n%s", unit)
	}
}

// TestDaemonInstallRefusesABadPreviewTemplate: {token} is one token in the
// host's first label and nowhere else, so install refuses a setting that would
// leave the daemon unable to place it.
func TestDaemonInstallRefusesABadPreviewTemplate(t *testing.T) {
	for _, c := range []struct{ name, url string }{
		{name: "twice", url: "https://p{token}x{token}.preview.example.com"},
		{name: "in the path", url: "https://preview.example.com/{token}"},
		{name: "in a second label", url: "https://preview.{token}.example.com"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := daemonTestEnv(t)
			t.Setenv("PATH", "/usr/bin:/bin")
			daemonWriteUserConfig(t, home, "[daemon]\npreview_url = \""+c.url+"\"\n")

			_, _, err := daemonUnitSpec(daemonTestWebbridge(t), home)
			if err == nil {
				t.Fatalf("daemonUnitSpec accepted preview_url %q", c.url)
			}
			want := "daemon.preview_url: {token} goes once, in the first label of the host, as in https://p{token}.preview.example.com"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to carry %q", err, want)
			}
		})
	}
}

// TestDaemonInstallRefusesAPreviewTemplateThatIsNotAnOrigin: the daemon checks
// its own --preview-url with bridge.CheckPreviewURL at every start, so install
// checks the same value — the template with a plain token in place of {token} —
// and refuses a scheme, userinfo, path, query or fragment now, rather than
// writing a unit the daemon cannot start on. The refusal names the setting as
// the user wrote it, not the substituted value they never wrote.
func TestDaemonInstallRefusesAPreviewTemplateThatIsNotAnOrigin(t *testing.T) {
	for _, c := range []struct{ name, url, detail string }{
		{name: "a path", url: "https://p{token}.preview.example.com/x", detail: "an origin carries no path"},
		{name: "a query", url: "https://p{token}.preview.example.com?a=1", detail: "an origin carries no query"},
		{name: "a fragment", url: "https://p{token}.preview.example.com#x", detail: "an origin carries no fragment"},
		{name: "userinfo", url: "https://u@p{token}.preview.example.com", detail: "an origin carries no userinfo"},
		{name: "another scheme", url: "ftp://p{token}.preview.example.com", detail: "scheme is not http or https"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := daemonTestEnv(t)
			t.Setenv("PATH", "/usr/bin:/bin")
			daemonWriteUserConfig(t, home, "[daemon]\npreview_url = \""+c.url+"\"\n")

			_, _, err := daemonUnitSpec(daemonTestWebbridge(t), home)
			if err == nil {
				t.Fatalf("daemonUnitSpec accepted preview_url %q", c.url)
			}
			if want := "daemon.preview_url \"" + c.url + "\""; !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to name the setting as %q", err, want)
			}
			if !strings.Contains(err.Error(), c.detail) {
				t.Errorf("error = %q, want it to carry %q", err, c.detail)
			}
		})
	}
}

// TestDaemonInstallRefusesANetworkWebListener: the daemon runs host processes
// with the user's authority, so D3 makes it listen on a unix socket or a
// loopback address only, and install refuses anything else rather than
// publishing it and being discovered later.
func TestDaemonInstallRefusesANetworkWebListener(t *testing.T) {
	home := daemonTestEnv(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	daemonWriteUserConfig(t, home, "[daemon]\nweb_listen = \"0.0.0.0:7788\"\n")

	_, _, err := daemonUnitSpec(daemonTestWebbridge(t), home)
	if err == nil {
		t.Fatal("daemonUnitSpec accepted a web listener bound to every interface")
	}
	for _, want := range []string{"daemon.web_listen", "unix socket or a loopback address only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to carry %q", err, want)
		}
	}

	// install never reaches the service manager with such a unit.
	calls := 0
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		return nil, nil
	})
	var out, errOut bytes.Buffer
	if err := runDaemon(context.Background(), []string{"install", "--webbridge", daemonTestWebbridge(t)},
		strings.NewReader(""), &out, &errOut); err == nil {
		t.Fatal("install accepted a web listener bound to every interface")
	}
	if calls != 0 {
		t.Errorf("install reached the service manager %d times, want none", calls)
	}
}

// TestDaemonInstallRefusesAWebGroupWithUnix: a group needs a socket path of
// its own to share, so `unix` — whose socket sits in the private daemon
// directory — and a loopback address are both refused, naming both keys.
func TestDaemonInstallRefusesAWebGroupWithUnix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
	}{
		{name: "unix", config: "web_listen = \"unix\"\nweb_group = \"marshal\"\n"},
		{name: "loopback address", config: "web_listen = \"127.0.0.1:7788\"\nweb_group = \"marshal\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := daemonTestEnv(t)
			daemonWriteUserConfig(t, home, "[daemon]\n"+tc.config)

			_, _, err := daemonUnitSpec(daemonTestWebbridge(t), home)
			if err == nil {
				t.Fatalf("daemonUnitSpec accepted daemon.web_group with %q", tc.config)
			}
			for _, want := range []string{"daemon.web_group", "daemon.web_listen"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to name %q", err, want)
				}
			}
		})
	}
}

// TestDaemonInstallNamesTheRetiredKeys: a retired [daemon] key is read by
// nothing (D10, D6), so install says so — with what to do instead — and writes
// the unit anyway.
func TestDaemonInstallNamesTheRetiredKeys(t *testing.T) {
	home := daemonTestEnv(t)
	daemonWriteUserConfig(t, home, "[daemon]\n"+
		"studio_addr = \"127.0.0.1:7788\"\n"+
		"join_url = \"https://relay.example.com\"\n")

	t.Setenv("PATH", "/usr/bin:/bin")
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, nil
	})
	fakeDaemonSocket(t, fakeDaemonClient{err: errors.New("dial unix: no such file or directory")})

	var out, errOut bytes.Buffer
	if err := runDaemon(context.Background(), []string{"install", "--webbridge", daemonTestWebbridge(t)},
		strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("runDaemon install: %v\nstderr: %s", err, errOut.String())
	}
	for _, want := range []string{
		"marshal daemon install: daemon.studio_addr is retired and ignored; set daemon.web_listen (docs/one-daemon/design.md D10)",
		"marshal daemon install: daemon.join_url is retired and ignored; a remote daemon is a host: marshal fleet add",
	} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("install's stderr = %q, want it to carry %q", errOut.String(), want)
		}
	}
	if !strings.Contains(out.String(), "unit: ") {
		t.Errorf("install printed %q, want it to have written the unit anyway", out.String())
	}
}

// TestDaemonInstallRemovesTheOldTokenFiles: the credentials the retired
// listeners read go with them (D10, D6). A missing file is not an error, so a
// daemon directory that never held one installs as before.
func TestDaemonInstallRemovesTheOldTokenFiles(t *testing.T) {
	home := daemonTestEnv(t)
	dir := daemon.PathsFor(home).Dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"studio.token", "join.token"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	daemonInstallUnit(t, home)
	for _, name := range []string{"studio.token", "join.token"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the install (stat: %v)", path, err)
		}
	}

	// Neither file is there now, and that is not an error.
	daemonInstallUnit(t, home)
}

// TestDaemonStatusReportsTheWebListener pins the four forms of the status's
// web line (D10, D13): off, not listening with its reason, a unix socket, and
// an address — which every account on the machine can reach.
func TestDaemonStatusReportsTheWebListener(t *testing.T) {
	cases := []struct {
		name string
		info daemon.Info
		want string
	}{
		{name: "off", want: "web: off"},
		{
			name: "not listening",
			info: daemon.Info{WebError: "listen tcp 127.0.0.1:7788: bind: address already in use"},
			want: "web: not listening (listen tcp 127.0.0.1:7788: bind: address already in use)",
		},
		{
			name: "unix socket",
			info: daemon.Info{WebListen: "unix:/run/user/501/marshal/web.sock"},
			want: "web: unix:/run/user/501/marshal/web.sock",
		},
		{
			name: "loopback address",
			info: daemon.Info{WebListen: "127.0.0.1:7788"},
			want: "web: 127.0.0.1:7788 (loopback: every account on this machine can reach it)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			daemonTestEnv(t)
			fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte("state = running\npid = 4242\n"), nil
			})
			fakeDaemonSocket(t, fakeDaemonClient{info: tc.info})

			var stdout, stderr bytes.Buffer
			if err := runDaemon(context.Background(), []string{"status"},
				strings.NewReader(""), &stdout, &stderr); err != nil {
				t.Fatalf("runDaemon status: %v\nstderr: %s", err, stderr.String())
			}

			lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			var webLines []string
			refreshAt := -1
			for i, line := range lines {
				if strings.HasPrefix(line, "web:") {
					webLines = append(webLines, line)
				}
				if strings.HasPrefix(line, "refresh:") {
					refreshAt = i
				}
			}
			if len(webLines) != 1 {
				t.Fatalf("status printed %d web lines, want 1:\n%s", len(webLines), stdout.String())
			}
			if webLines[0] != tc.want {
				t.Errorf("web line = %q, want %q", webLines[0], tc.want)
			}
			// It follows the refresh helper's line.
			if refreshAt < 0 || refreshAt+1 >= len(lines) || lines[refreshAt+1] != tc.want {
				t.Errorf("web line does not follow the refresh line:\n%s", stdout.String())
			}
		})
	}
}

// TestDaemonStatusNamesTheStudioDir pins the status's Studio line (D12): the
// directory the daemon serves it from, and — when it serves none — that there
// are no files, with the two ways to get them, rather than an empty path.
func TestDaemonStatusNamesTheStudioDir(t *testing.T) {
	cases := []struct {
		name string
		info daemon.Info
		want string
	}{
		{
			name: "files",
			info: daemon.Info{StudioDir: "/usr/share/marshal/studio"},
			want: "studio: /usr/share/marshal/studio",
		},
		{
			name: "no files",
			want: "studio: no files found (reinstall Marshal, or build them: see AGENTS.md)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			daemonTestEnv(t)
			fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte("state = running\npid = 4242\n"), nil
			})
			fakeDaemonSocket(t, fakeDaemonClient{info: tc.info})

			var stdout, stderr bytes.Buffer
			if err := runDaemon(context.Background(), []string{"status"},
				strings.NewReader(""), &stdout, &stderr); err != nil {
				t.Fatalf("runDaemon status: %v\nstderr: %s", err, stderr.String())
			}

			lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			var studioLines []string
			webAt := -1
			for i, line := range lines {
				if strings.HasPrefix(line, "studio:") {
					studioLines = append(studioLines, line)
				}
				if strings.HasPrefix(line, "web:") {
					webAt = i
				}
			}
			if len(studioLines) != 1 {
				t.Fatalf("status printed %d studio lines, want 1:\n%s", len(studioLines), stdout.String())
			}
			if studioLines[0] != tc.want {
				t.Errorf("studio line = %q, want %q", studioLines[0], tc.want)
			}
			// It follows the web line's.
			if webAt < 0 || webAt+1 >= len(lines) || lines[webAt+1] != tc.want {
				t.Errorf("studio line does not follow the web line:\n%s", stdout.String())
			}
		})
	}
}

// TestDaemonTokenAndJoinTokenAreGone: both subcommands went with the listeners
// they authenticated, so each falls to the unknown-subcommand error rather
// than quietly doing something else.
func TestDaemonTokenAndJoinTokenAreGone(t *testing.T) {
	for _, sub := range []string{"token", "join-token"} {
		t.Run(sub, func(t *testing.T) {
			daemonTestEnv(t)
			var stdout, stderr bytes.Buffer
			err := runDaemon(context.Background(), []string{sub},
				strings.NewReader(""), &stdout, &stderr)
			if err == nil {
				t.Fatalf("marshal daemon %s is still a subcommand", sub)
			}
			for _, want := range []string{"unknown subcommand", sub} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to carry %q", err, want)
				}
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", stdout.String())
			}
		})
	}
}

// fakeTunnelsRunner replaces the tunnels helper's body and answers its call
// count, as fakeOAuthRefreshRunner does for refresh.
func fakeTunnelsRunner(t *testing.T, run func(context.Context, tunnels.Config) error) *int {
	t.Helper()
	old := tunnelsRunner
	t.Cleanup(func() { tunnelsRunner = old })
	calls := 0
	tunnelsRunner = func(ctx context.Context, cfg tunnels.Config) error {
		calls++
		return run(ctx, cfg)
	}
	return &calls
}

// TestDaemonTunnelsRunsTheHelperOnTheSocket: `marshal daemon tunnels` runs the
// helper on the socket it was given, with fleet.toml under the resolved home
// and its tunnel sockets in the hosts directory beside that socket.
func TestDaemonTunnelsRunsTheHelperOnTheSocket(t *testing.T) {
	home := daemonTestEnv(t)

	var got tunnels.Config
	calls := fakeTunnelsRunner(t, func(_ context.Context, cfg tunnels.Config) error {
		got = cfg
		return nil
	})

	socket := filepath.Join(home, "run", "daemon.sock")
	var stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"tunnels", "--socket", socket},
		strings.NewReader(""), io.Discard, &stderr); err != nil {
		t.Fatalf("runDaemon tunnels: %v\nstderr: %s", err, stderr.String())
	}
	if *calls != 1 {
		t.Fatalf("the tunnels helper ran %d times, want once", *calls)
	}
	if want := fleetprofile.Path(home); got.FleetFile != want {
		t.Errorf("FleetFile = %q, want the fleet file under the test home %q", got.FleetFile, want)
	}
	if want := filepath.Join(filepath.Dir(socket), "hosts"); got.Dir != want {
		t.Errorf("Dir = %q, want %q", got.Dir, want)
	}
	if got.Report == nil {
		t.Error("Report is nil: the helper has nowhere to send its hosts")
	}
	if got.Probe == nil {
		t.Error("Probe is nil: the helper cannot tell a live tunnel from a silent one")
	}
	if got.Stderr != &stderr {
		t.Errorf("Stderr = %v, want the command's own stderr", got.Stderr)
	}
}

// TestDaemonTunnelsNeedsTheSocket: the daemon's socket is how the helper
// reports what each tunnel is doing, so without it the helper never runs.
func TestDaemonTunnelsNeedsTheSocket(t *testing.T) {
	daemonTestEnv(t)
	calls := fakeTunnelsRunner(t, func(context.Context, tunnels.Config) error {
		t.Error("the tunnels helper ran without --socket")
		return nil
	})

	err := runDaemon(context.Background(), []string{"tunnels"},
		strings.NewReader(""), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("tunnels without --socket succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "--socket is required") {
		t.Errorf("error = %q, want it to say --socket is required", err)
	}
	if *calls != 0 {
		t.Errorf("the tunnels helper ran %d times, want none", *calls)
	}
}

// TestDaemonStatusListsTheHosts: the text status gives one line per host the
// tunnel helper reports, and a host that is down carries its last error on
// that line.
func TestDaemonStatusListsTheHosts(t *testing.T) {
	daemonTestEnv(t)
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("state = running\npid = 4242\n"), nil
	})
	doc := `{"version":"1.0.0","profile":"local","pid":4242,"startedAt":1700000000000,"hosts":[` +
		`{"name":"box","ssh":"box.example.com","state":"live","socket":"/tmp/hosts/box.sock"},` +
		`{"name":"gpu","ssh":"gpu.example.com","state":"down","socket":"/tmp/hosts/gpu.sock",` +
		`"lastError":"ssh: connect to host gpu.example.com port 22: Connection refused"}]}`
	var info daemon.Info
	if err := json.Unmarshal([]byte(doc), &info); err != nil {
		t.Fatalf("unmarshal the daemon document: %v", err)
	}
	fakeDaemonSocket(t, fakeDaemonClient{info: info})

	var stdout, stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"status"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runDaemon status: %v\nstderr: %s", err, stderr.String())
	}

	want := []string{
		"host box (box.example.com): live",
		"host gpu (gpu.example.com): down (ssh: connect to host gpu.example.com port 22: Connection refused)",
	}
	var hostLines []string
	for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
		if strings.HasPrefix(line, "host ") {
			hostLines = append(hostLines, line)
		}
	}
	if len(hostLines) != len(want) {
		t.Fatalf("status printed %d host lines, want %d:\n%s", len(hostLines), len(want), stdout.String())
	}
	for i, line := range want {
		if hostLines[i] != line {
			t.Errorf("host line %d = %q, want %q", i+1, hostLines[i], line)
		}
	}
}

// TestDaemonStatusNamesAHostsPreviewPort: a live host that serves previews
// says which loopback port they are forwarded to, because that port is the
// only way to reach them. A host with no port, and a host that is down
// whatever it last reported, keep the line they had.
func TestDaemonStatusNamesAHostsPreviewPort(t *testing.T) {
	daemonTestEnv(t)
	fakeDaemonManager(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("state = running\npid = 4242\n"), nil
	})
	doc := `{"version":"1.0.0","profile":"local","pid":4242,"startedAt":1700000000000,"hosts":[` +
		`{"name":"box","ssh":"box.example.com","state":"live","socket":"/tmp/hosts/box.sock",` +
		`"previewPort":4173},` +
		`{"name":"gpu","ssh":"gpu.example.com","state":"live","socket":"/tmp/hosts/gpu.sock"},` +
		`{"name":"old","ssh":"old.example.com","state":"down","socket":"/tmp/hosts/old.sock",` +
		`"previewPort":4173,"lastError":"ssh: connect to host old.example.com port 22: Connection refused"}]}`
	var info daemon.Info
	if err := json.Unmarshal([]byte(doc), &info); err != nil {
		t.Fatalf("unmarshal the daemon document: %v", err)
	}
	fakeDaemonSocket(t, fakeDaemonClient{info: info})

	var stdout, stderr bytes.Buffer
	if err := runDaemon(context.Background(), []string{"status"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("runDaemon status: %v\nstderr: %s", err, stderr.String())
	}

	want := []string{
		"host box (box.example.com): live, previews on localhost:4173",
		"host gpu (gpu.example.com): live",
		"host old (old.example.com): down (ssh: connect to host old.example.com port 22: Connection refused)",
	}
	var hostLines []string
	for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
		if strings.HasPrefix(line, "host ") {
			hostLines = append(hostLines, line)
		}
	}
	if len(hostLines) != len(want) {
		t.Fatalf("status printed %d host lines, want %d:\n%s", len(hostLines), len(want), stdout.String())
	}
	for i, line := range want {
		if hostLines[i] != line {
			t.Errorf("host line %d = %q, want %q", i+1, hostLines[i], line)
		}
	}
}
