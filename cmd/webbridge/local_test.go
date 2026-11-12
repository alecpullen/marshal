package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"marshal/web/bridge"
)

// isolateWebbridgeEnv blanks the bridge's variables so the host's own
// settings cannot reach the parse under test.
func isolateWebbridgeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"WEBBRIDGE_PROFILE", "WEBBRIDGE_SOCKET", "WEBBRIDGE_ADDR", "WEBBRIDGE_TOKEN",
		"WEBBRIDGE_CWD_ROOT", "WEBBRIDGE_STATE_DIR", "WEBBRIDGE_WORKSPACE",
		"WEBBRIDGE_EGRESS", "WEBBRIDGE_SECRETS", "WEBBRIDGE_SECRETS_KEY_FILE",
		"WEBBRIDGE_IDLE_SESSION_TIMEOUT", "WEBBRIDGE_OAUTH_REFRESH",
	} {
		t.Setenv(k, "")
	}
}

// localSocketPath returns a short socket path in a directory the test
// owns 0700: ListenLocal refuses a directory anyone else can reach, and
// a unix socket path is capped at 104 bytes on macOS, which t.TempDir()
// can exceed.
func localSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "marshal-local-")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "marshal.sock")
}

// socketClient returns a client that dials the local daemon's socket.
func socketClient(path string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		},
	}
}

// waitForDaemon polls GET /api/daemon until the local daemon answers.
func waitForDaemon(t *testing.T, client *http.Client) bridge.DaemonInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://daemon/api/daemon")
		if err != nil {
			last = err
		} else {
			var info bridge.DaemonInfo
			derr := json.NewDecoder(resp.Body).Decode(&info)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && derr == nil {
				return info
			}
			last = fmt.Errorf("status %d, decode %v", resp.StatusCode, derr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the local daemon did not answer: %v", last)
	return bridge.DaemonInfo{}
}

// TestTheLocalProfilesDefaults pins decision 2: each default applies
// only when neither its flag nor its variable was given, and the
// working-directory project is not registered.
func TestTheLocalProfilesDefaults(t *testing.T) {
	isolateWebbridgeEnv(t)
	data := t.TempDir()
	t.Setenv("MARSHAL_DATA_DIR", data)

	cfg, err := parseConfig([]string{"--profile", "local"}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	dir := filepath.Join(data, "daemon")
	if want := filepath.Join(dir, "marshal.sock"); cfg.socket != want {
		t.Errorf("socket = %q, want %q", cfg.socket, want)
	}
	if want := filepath.Join(dir, "state"); cfg.stateDir != want {
		t.Errorf("stateDir = %q, want %q", cfg.stateDir, want)
	}
	if want := filepath.Join(dir, "fleet.json"); cfg.workspace != want {
		t.Errorf("workspace = %q, want %q", cfg.workspace, want)
	}
	if cfg.egress != "off" {
		t.Errorf("egress = %q, want off", cfg.egress)
	}
	if cfg.secrets != "env" {
		t.Errorf("secrets = %q, want env", cfg.secrets)
	}
	if cfg.cwdRoot != "" {
		t.Errorf("cwdRoot = %q, want empty: the local profile registers no working-directory project", cfg.cwdRoot)
	}
	if want := 30 * time.Minute; cfg.idleSessionTimeout != want {
		t.Errorf("idleSessionTimeout = %s, want %s", cfg.idleSessionTimeout, want)
	}
	// The refresh helper is off unless asked: nothing else in the local
	// profile's defaults starts a process.
	if cfg.oauthRefresh {
		t.Errorf("oauthRefresh = true, want false: the refresh helper is asked for")
	}
}

// TestTheLocalProfileRefuses pins decision 3 and the settings that only
// the local profile understands.
func TestTheLocalProfileRefuses(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		env  map[string]string
		// unparsable marks a case whose refusal is the variable's own
		// parse error rather than a profile one.
		unparsable bool
	}{
		{"egress flag", []string{"--profile", "local", "--egress", "on"}, nil, false},
		{"egress env", []string{"--profile", "local"}, map[string]string{"WEBBRIDGE_EGRESS": "on"}, false},
		{"secrets local", []string{"--profile", "local", "--secrets", "local", "--secrets-key-file", keyFile}, nil, false},
		{"socket without the profile", []string{"--socket", "/tmp/x.sock"}, nil, false},
		{"addr off without the profile", []string{"--addr", "off"}, nil, false},
		{"idle timeout without the profile", []string{"--idle-session-timeout", "5m"}, nil, false},
		{"idle timeout negative", []string{"--profile", "local", "--idle-session-timeout", "-5m"}, nil, false},
		{"oauth refresh without the profile", []string{"--oauth-refresh"}, nil, false},
		{"oauth refresh unparsable", []string{"--profile", "local"}, map[string]string{"WEBBRIDGE_OAUTH_REFRESH": "yes"}, true},
		{"profile cloud", []string{"--profile", "cloud"}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateWebbridgeEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := parseConfig(tc.args, io.Discard)
			if err == nil {
				t.Fatalf("parseConfig(%v) accepted the setting", tc.args)
			}
			// Every refusal names what to use instead: the local
			// profile. A variable that does not parse names the
			// variable instead.
			want := "local"
			if tc.unparsable {
				want = "WEBBRIDGE_OAUTH_REFRESH"
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %s", err, want)
			}
		})
	}
}

// TestTheIdleTimeoutParses pins that --idle-session-timeout reaches the
// config as a duration, and that zero is a value rather than "unset":
// it means a session is never ended for being idle.
func TestTheIdleTimeoutParses(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want time.Duration
	}{
		{"five minutes", []string{"--profile", "local", "--idle-session-timeout", "5m"}, 5 * time.Minute},
		{"zero", []string{"--profile", "local", "--idle-session-timeout", "0"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateWebbridgeEnv(t)
			t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
			cfg, err := parseConfig(tc.args, io.Discard)
			if err != nil {
				t.Fatalf("parseConfig(%v): %v", tc.args, err)
			}
			if cfg.idleSessionTimeout != tc.want {
				t.Errorf("idleSessionTimeout = %s, want %s", cfg.idleSessionTimeout, tc.want)
			}
		})
	}
}

// TestTheLocalProfileServesItsSocket runs the daemon for real and asks
// its socket who it is: --addr off means the socket is the only
// listener, peer-checked rather than token-checked.
func TestTheLocalProfileServesItsSocket(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)

	var stderr strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--profile", "local", "--addr", "off", "--socket", socket}, io.Discard, &stderr)
	}()

	info := waitForDaemon(t, socketClient(socket))
	if info.Profile != "local" {
		t.Errorf("profile = %q, want local", info.Profile)
	}
	if info.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", info.PID, os.Getpid())
	}
	if info.Socket != socket {
		t.Errorf("socket = %q, want %q", info.Socket, socket)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after its context was cancelled; want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of its context being cancelled")
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still exists after shutdown: %v", err)
	}
	if strings.Contains(stderr.String(), "generated bearer token") {
		t.Fatalf("the local profile generated a token: %q", stderr.String())
	}
}

// helperScript writes a marshal stand-in for the refresh helper: it
// records the arguments it was given and then stays alive, so a test can
// see both what the bridge ran and whether it is gone afterwards.
func helperScript(t *testing.T) (script, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	script = filepath.Join(dir, "marshal-helper")
	argsFile = filepath.Join(dir, "helper-args")
	body := "#!/bin/sh\nprintf '%s' \"$*\" > \"$HELPER_ARGS_FILE\"\nexec sleep 60\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write helper script: %v", err)
	}
	// The helper inherits the bridge's environment, so this reaches it.
	t.Setenv("HELPER_ARGS_FILE", argsFile)
	return script, argsFile
}

// waitForArgs polls the helper's argument file until it has contents.
func waitForArgs(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && len(b) > 0 {
			return string(b)
		}
		last = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the helper did not record its arguments: %v", last)
	return ""
}

// TestTheLocalProfileSupervisesTheRefreshHelper: --oauth-refresh runs
// `<marshal-bin> daemon refresh` beside the local bridge, reports it on
// GET /api/daemon, and takes it down again when the bridge stops.
func TestTheLocalProfileSupervisesTheRefreshHelper(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	script, argsFile := helperScript(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--profile", "local", "--addr", "off", "--socket", socket,
			"--oauth-refresh", "--marshal-bin", script}, io.Discard, io.Discard)
	}()

	client := socketClient(socket)
	var refresh bridge.DaemonRefresh
	deadline := time.Now().Add(5 * time.Second)
	for {
		info := waitForDaemon(t, client)
		if info.Refresh != nil && info.Refresh.PID > 0 {
			refresh = *info.Refresh
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("GET /api/daemon reported no refresh helper: %+v", info)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The helper is the marshal binary with `daemon refresh`, nothing else.
	if argv := waitForArgs(t, argsFile); argv != "daemon refresh" {
		t.Errorf("helper argv = %q, want %q", argv, "daemon refresh")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after its context was cancelled; want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within 10s of its context being cancelled")
	}
	if err := syscall.Kill(refresh.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("refresh helper %d still running after run returned: %v", refresh.PID, err)
	}
}

// TestTheLocalProfileStartsNoHelperUnasked: without --oauth-refresh, the
// local profile runs no helper and reports none.
func TestTheLocalProfileStartsNoHelperUnasked(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	script, argsFile := helperScript(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--profile", "local", "--addr", "off", "--socket", socket,
			"--marshal-bin", script}, io.Discard, io.Discard)
	}()

	info := waitForDaemon(t, socketClient(socket))
	if info.Refresh != nil {
		t.Errorf("GET /api/daemon reported a refresh helper without --oauth-refresh: %+v", info.Refresh)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of its context being cancelled")
	}
	if _, err := os.Stat(argsFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the helper script ran without --oauth-refresh; want it never run")
	}
}

// TestASecondLocalDaemonRefusesToStart: the socket belongs to the first
// daemon, and the refusal is ErrDaemonRunning — not a second server
// fighting for the same path.
func TestASecondLocalDaemonRefusesToStart(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	args := []string{"--profile", "local", "--addr", "off", "--socket", socket}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, args, io.Discard, io.Discard) }()

	client := socketClient(socket)
	first := waitForDaemon(t, client)

	if err := run(context.Background(), args, io.Discard, io.Discard); !errors.Is(err, bridge.ErrDaemonRunning) {
		t.Fatalf("second run error = %v, want bridge.ErrDaemonRunning", err)
	}
	// The refusal left the first daemon answering.
	if again := waitForDaemon(t, client); again.PID != first.PID {
		t.Fatalf("pid = %d after the second daemon refused, want %d", again.PID, first.PID)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first daemon returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first daemon did not stop within 5s")
	}
}
