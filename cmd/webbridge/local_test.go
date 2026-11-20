package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
		"WEBBRIDGE_TOKEN_FILE", "WEBBRIDGE_TLS_CERT", "WEBBRIDGE_TLS_KEY",
		"WEBBRIDGE_WEB_LISTEN", "WEBBRIDGE_WEB_GROUP", "WEBBRIDGE_PREVIEW_ADDR",
		"WEBBRIDGE_PREVIEW_URL",
		"WEBBRIDGE_CWD_ROOT", "WEBBRIDGE_STATE_DIR", "WEBBRIDGE_STATE_VOLUME",
		"WEBBRIDGE_WORKSPACE",
		"WEBBRIDGE_EGRESS", "WEBBRIDGE_SECRETS", "WEBBRIDGE_SECRETS_KEY_FILE",
		"WEBBRIDGE_IDLE_SESSION_TIMEOUT", "WEBBRIDGE_OAUTH_REFRESH", "WEBBRIDGE_TUNNELS",
		"WEBBRIDGE_STUDIO_DIR",
		"WEBBRIDGE_RELAY_JOIN", "WEBBRIDGE_JOIN", "WEBBRIDGE_JOIN_NAME",
		"WEBBRIDGE_JOIN_TOKEN_FILE", "WEBBRIDGE_JOIN_CA_FILE",
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
	return waitForDaemonAt(t, client, "daemon")
}

// waitForDaemonAt polls GET /api/daemon at host. The daemon's own socket
// answers any Host, its web listener only a host the daemon publishes (D13),
// so a test waiting on web.sock names the published one.
func waitForDaemonAt(t *testing.T, client *http.Client, host string) bridge.DaemonInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + host + "/api/daemon")
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

// TestTheDaemonsDefaults pins the one daemon's defaults (OD1): each applies
// only when neither its flag nor its variable was given, the egress proxy is
// off, and no working-directory project is registered.
func TestTheDaemonsDefaults(t *testing.T) {
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
		t.Errorf("cwdRoot = %q, want empty: the daemon registers no working-directory project", cfg.cwdRoot)
	}
	if want := 30 * time.Minute; cfg.idleSessionTimeout != want {
		t.Errorf("idleSessionTimeout = %s, want %s", cfg.idleSessionTimeout, want)
	}
	// The refresh helper is off unless asked: nothing else in the daemon's
	// defaults starts a process.
	if cfg.oauthRefresh {
		t.Errorf("oauthRefresh = true, want false: the refresh helper is asked for")
	}
}

// TestTheDaemonRefuses pins the settings the daemon refuses. The retired
// --profile is refused unless it says local, and the retired --relay-join is
// refused outright (TestRelayJoinIsRefused). The four join flags are not
// refused: they are accepted and ignored (TestTheJoinFlagsAreIgnored).
func TestTheDaemonRefuses(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())

	cases := []struct {
		name string
		args []string
		env  map[string]string
		// want is the text the refusal must name. An empty want means the
		// settings are accepted, which is what the retired flag is pinned
		// with.
		want string
	}{
		{"idle timeout negative", []string{"--idle-session-timeout", "-5m"}, nil, "cannot be negative"},
		{"oauth refresh unparsable", []string{"--profile", "local"}, map[string]string{"WEBBRIDGE_OAUTH_REFRESH": "yes"}, "WEBBRIDGE_OAUTH_REFRESH"},
		// --profile is retired: local, or no flag at all, is every bridge.
		{"profile local", []string{"--profile", "local"}, nil, ""},
		{"profile cloud", []string{"--profile", "cloud"}, nil, "retired"},
		{"profile server", []string{"--profile", "server"}, nil, "retired"},
		// The settings the daemon once guarded behind the profile are its
		// own: the egress proxy is off unless asked for, and asking is fine.
		{"egress on", []string{"--egress", "on"}, nil, ""},
		{"egress on from the variable", nil, map[string]string{"WEBBRIDGE_EGRESS": "on"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateWebbridgeEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := parseConfig(tc.args, io.Discard)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("parseConfig(%v) refused the settings: %v", tc.args, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("parseConfig(%v) accepted the setting", tc.args)
			}
			// Every refusal names what to keep: the retired profile, the
			// flag, or the setting. A variable that does not parse names
			// the variable instead.
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %s", err, tc.want)
			}
		})
	}
}

// TestRelayJoinIsRefused pins the one retirement that is a refusal: the relay
// a joining host dialled into has no place in the one daemon, where a remote
// daemon is a host reached over SSH (D6). The refusal says so, by flag and by
// variable alike.
func TestRelayJoinIsRefused(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"flag", []string{"--relay-join", "box=/tmp/box.secret"}, nil},
		{"variable", nil, map[string]string{"WEBBRIDGE_RELAY_JOIN": "box=/tmp/box.secret"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateWebbridgeEnv(t)
			t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := parseConfig(tc.args, io.Discard)
			if err == nil {
				t.Fatalf("parseConfig(%v) accepted the retired --relay-join", tc.args)
			}
			if !strings.Contains(err.Error(), "--relay-join is retired") || !strings.Contains(err.Error(), "SSH") {
				t.Errorf("error %q does not retire --relay-join for a host reached over SSH", err)
			}
		})
	}
}

// TestTheJoinFlagsAreIgnored pins the retirement the other way round: an
// installed unit's four join flags still start the daemon — the files they
// name are never opened — and one warning names them. The warning goes
// through slog, the path the retired --profile takes, so the test reads the
// handler that writes where stderr would hold it.
func TestTheJoinFlagsAreIgnored(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)

	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{
			"--socket", socket,
			"--join", "https://relay.example",
			"--join-name", "box",
			"--join-token-file", "/nonexistent/join.secret",
			"--join-ca-file", "/nonexistent/relay.pem",
		}, io.Discard, io.Discard)
	}()

	// The daemon serves: the flags are ignored rather than refused, and the
	// secret files they name are never read, so a missing one stops nothing.
	waitForDaemon(t, socketClient(socket))

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after its context was cancelled; want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of its context being cancelled")
	}

	want := "--join, --join-name, --join-token-file, --join-ca-file are retired and ignored"
	if got := strings.Count(logged.String(), "retired and ignored"); got != 1 {
		t.Fatalf("warnings = %d, want 1: %s", got, logged.String())
	}
	if !strings.Contains(logged.String(), want) {
		t.Errorf("the warning does not name the four flags as %q: %s", want, logged.String())
	}
	if !strings.Contains(logged.String(), "marshal daemon install") {
		t.Errorf("the warning does not say how to rewrite the unit: %s", logged.String())
	}
}

// TestTheRetiredListenerFlagsAreIgnored: an installed unit's --addr and
// --token-file still start the daemon — neither is read, so a missing token
// file stops nothing — and one warning names them.
func TestTheRetiredListenerFlagsAreIgnored(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)

	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{
			"--addr", "off", "--token-file", "/nonexistent/studio.token",
			"--socket", socket,
		}, io.Discard, io.Discard)
	}()

	// The daemon serves: the flags are ignored rather than refused, and the
	// token file they name is never read, so a missing one stops nothing.
	waitForDaemon(t, socketClient(socket))

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after its context was cancelled; want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of its context being cancelled")
	}

	want := "--addr, --token-file are retired and ignored"
	if got := strings.Count(logged.String(), "retired and ignored"); got != 1 {
		t.Fatalf("warnings = %d, want 1: %s", got, logged.String())
	}
	if !strings.Contains(logged.String(), want) {
		t.Errorf("the warning does not name %q: %s", want, logged.String())
	}
	if !strings.Contains(logged.String(), "marshal daemon install") {
		t.Errorf("the warning does not say how to rewrite the unit: %s", logged.String())
	}
}

// TestTheRetiredProfileWarnsOnce pins the one rule the retired flag still
// carries: local, or no flag at all, is accepted, and only an explicit local
// is worth a warning.
func TestTheRetiredProfileWarnsOnce(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
		want int
	}{
		{"flag", []string{"--profile", "local"}, nil, 1},
		{"variable", nil, map[string]string{"WEBBRIDGE_PROFILE": "local"}, 1},
		{"absent", nil, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateWebbridgeEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			if _, err := parseConfig(tc.args, io.Discard); err != nil {
				t.Fatalf("parseConfig(%v) refused the settings: %v", tc.args, err)
			}
			if got := strings.Count(buf.String(), "level=WARN"); got != tc.want {
				t.Errorf("warnings = %d, want %d (%s)", got, tc.want, buf.String())
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
		{"five minutes", []string{"--idle-session-timeout", "5m"}, 5 * time.Minute},
		{"zero", []string{"--idle-session-timeout", "0"}, 0},
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
// its socket who it is: the socket is peer-checked, and with no
// --web-listen it is the only listener.
func TestTheLocalProfileServesItsSocket(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)

	var stderr strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--socket", socket}, io.Discard, &stderr)
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

	// The peer-checked socket serves the factory's routes as well: the SPA
	// and the local daemon speak one API, and the factory is part of it.
	resp, err := socketClient(socket).Get("http://daemon/api/factory/items")
	if err != nil {
		t.Fatalf("GET /api/factory/items over the socket: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/factory/items status = %d, want 200", resp.StatusCode)
	}
	var items struct {
		Items []bridge.FactoryItem `json:"items"`
	}
	derr := json.NewDecoder(resp.Body).Decode(&items)
	_ = resp.Body.Close()
	if derr != nil {
		t.Fatalf("decode factory items: %v", derr)
	}
	if len(items.Items) != 0 {
		t.Fatalf("factory items = %d on a fresh daemon, want 0", len(items.Items))
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
}

// TestTheDaemonServesTheWebListener: --web-listen unix puts web.sock beside
// the daemon's own socket, serving the API with no bearer check, while a
// socket-only route stays 404 there (D3).
//
// The web listener is guarded: it answers only a host this daemon publishes
// (D13), so the run is given --public-url http://web and the requests below
// are addressed to that name, as a proxy in front of web.sock would address
// it.
func TestTheDaemonServesTheWebListener(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	web := filepath.Join(filepath.Dir(socket), "web.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{
			"--socket", socket,
			"--web-listen", "unix",
			// The web listener answers only a host this daemon publishes
			// (D13); "web" is the name the requests below put in the Host.
			"--public-url", "http://web",
		}, io.Discard, io.Discard)
	}()

	client := socketClient(web)
	// The web listener answers only a host the daemon publishes (D13), so
	// it is polled at the name --public-url gave it.
	info := waitForDaemonAt(t, client, "web")
	if want := "unix:" + web; info.WebListen != want {
		t.Errorf("webListen = %q, want %q", info.WebListen, want)
	}
	if info.WebError != "" {
		t.Errorf("webError = %q, want empty", info.WebError)
	}

	// No Authorization header: the web listener holds no token.
	resp, err := client.Get("http://web/api/agents")
	if err != nil {
		t.Fatalf("GET /api/agents over web.sock: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/agents over web.sock = %d, want 200 with no Authorization", resp.StatusCode)
	}

	// Pinning a project is the socket's own route: the web listener neither
	// opens a child nor changes what the daemon keeps running.
	resp, err = client.Post("http://web/api/projects/pin", "application/json",
		strings.NewReader(`{"root":"/tmp/x","pinned":true}`))
	if err != nil {
		t.Fatalf("POST /api/projects/pin over web.sock: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /api/projects/pin over web.sock = %d, want 404", resp.StatusCode)
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
}

// TestAWebListenerThatCannotBindIsReported: a live process already on
// web.sock leaves the daemon serving its own socket, with the failure
// reported on GET /api/daemon instead of refused at startup (OD1).
func TestAWebListenerThatCannotBindIsReported(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	web := filepath.Join(filepath.Dir(socket), "web.sock")

	// A listener already owns the path, so the daemon's bind is refused.
	live, err := net.Listen("unix", web)
	if err != nil {
		t.Fatalf("occupy %s: %v", web, err)
	}
	t.Cleanup(func() { _ = live.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--socket", socket, "--web-listen", "unix"}, io.Discard, io.Discard)
	}()

	info := waitForDaemon(t, socketClient(socket))
	if info.WebListen != "" {
		t.Errorf("webListen = %q, want empty: the bind failed", info.WebListen)
	}
	if info.WebError == "" {
		t.Fatal("webError is empty; a web listener that cannot bind must be reported")
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
}

// TestPreviewsOpenWithoutAWebListener: previews have a listener of their own,
// so they open even with no --web-listen; 501 would mean no preview port was
// recorded.
func TestPreviewsOpenWithoutAWebListener(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--socket", socket}, io.Discard, io.Discard)
	}()

	client := socketClient(socket)
	waitForDaemon(t, client)

	resp, err := client.Post("http://daemon/api/agents/x/preview/3000", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/agents/x/preview/3000: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		t.Fatal("previews answered 501; the preview listener opened no port")
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
		done <- run(ctx, []string{"--socket", socket,
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

// TestTheDaemonStartsNoHelperUnasked: without --oauth-refresh, the
// daemon runs no helper and reports none.
func TestTheDaemonStartsNoHelperUnasked(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	script, argsFile := helperScript(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--socket", socket,
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

// TestTheDaemonSupervisesTheTunnelsHelper: --tunnels runs `<marshal-bin>
// daemon tunnels --socket <socket>` beside the daemon, and takes it down
// again when the bridge stops.
func TestTheDaemonSupervisesTheTunnelsHelper(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	script, argsFile := helperScript(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--socket", socket,
			"--tunnels", "--marshal-bin", script}, io.Discard, io.Discard)
	}()

	// The helper is the marshal binary with `daemon tunnels`, dialling the
	// socket this daemon serves on.
	want := "daemon tunnels --socket " + socket
	if argv := waitForArgs(t, argsFile); argv != want {
		t.Errorf("helper argv = %q, want %q", argv, want)
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
}

// TestTheDaemonStartsNoTunnelsHelperUnasked: without --tunnels, the
// daemon runs no tunnels helper.
func TestTheDaemonStartsNoTunnelsHelperUnasked(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	script, argsFile := helperScript(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--socket", socket,
			"--marshal-bin", script}, io.Discard, io.Discard)
	}()

	waitForDaemon(t, socketClient(socket))
	// The helper records its arguments as it starts, so half a second is
	// long enough for one that was going to run.
	time.Sleep(500 * time.Millisecond)
	if b, err := os.ReadFile(argsFile); err == nil {
		t.Fatalf("the tunnels helper ran without --tunnels: %q", string(b))
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
}

// TestASecondLocalDaemonRefusesToStart: the socket belongs to the first
// daemon, and the refusal is ErrDaemonRunning — not a second server
// fighting for the same path.
func TestASecondLocalDaemonRefusesToStart(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	args := []string{"--socket", socket}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, args, io.Discard, io.Discard) }()

	client := socketClient(socket)
	first := waitForDaemon(t, client)

	// The second daemon takes its own state directory and workspace.
	// Sharing the first one's would make the factory journal lock, not the
	// socket, the error this test is about — the lock is taken during
	// startup, before the socket is ever opened.
	secondRoot := t.TempDir()
	secondState := filepath.Join(secondRoot, "state")
	secondArgs := []string{
		"--socket", socket,
		"--state-dir", secondState,
		"--workspace", filepath.Join(secondRoot, "workspace.json"),
	}
	if err := run(context.Background(), secondArgs, io.Discard, io.Discard); !errors.Is(err, bridge.ErrDaemonRunning) {
		t.Fatalf("second run error = %v, want bridge.ErrDaemonRunning", err)
	}
	// The refused start released its own journal: a later process can take
	// it, so a failed socket startup leaves nothing locked.
	store, err := bridge.OpenFactoryStore(filepath.Join(secondState, "factory", "v1"))
	if err != nil {
		t.Fatalf("reopen the second daemon's factory store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close the reopened factory store: %v", err)
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

// TestTheDaemonReportsItsStudioDir: --studio-dir points the daemon at the
// Studio's files, GET /api/daemon reports that directory, and the socket
// answers its index at / — the socket and the web listener share one Server,
// so both serve the Studio (D12).
func TestTheDaemonReportsItsStudioDir(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	studio := writeStudioIndex(t, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--socket", socket, "--studio-dir", studio}, io.Discard, io.Discard)
	}()

	client := socketClient(socket)
	info := waitForDaemon(t, client)
	if info.StudioDir != studio {
		t.Errorf("studioDir = %q, want %q", info.StudioDir, studio)
	}

	resp, err := client.Get("http://daemon/")
	if err != nil {
		t.Fatalf("GET / over the socket: %v", err)
	}
	body, rerr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if rerr != nil {
		t.Fatalf("read the Studio's index: %v", rerr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200 from the given Studio directory", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Marshal Studio") {
		t.Fatalf("GET / answered %q, want the Studio's index.html", string(body))
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
}

// TestTheDaemonRunsWithoutStudioFiles: a daemon that finds no Studio files —
// ownExecutable naming a binary in an empty directory — starts anyway,
// reports no studioDir, and answers / with the 503 page that says where it
// looked (D12).
func TestTheDaemonRunsWithoutStudioFiles(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	// The search runs on ownExecutable, which is the seam: a binary in an
	// empty directory has no Studio beside or above it.
	exeDir := t.TempDir()
	oldOwn := ownExecutable
	ownExecutable = func() (string, error) { return filepath.Join(exeDir, "webbridge"), nil }
	t.Cleanup(func() { ownExecutable = oldOwn })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"--socket", socket}, io.Discard, io.Discard) }()

	client := socketClient(socket)
	info := waitForDaemon(t, client)
	if info.StudioDir != "" {
		t.Errorf("studioDir = %q, want empty: no Studio files were found", info.StudioDir)
	}

	resp, err := client.Get("http://daemon/")
	if err != nil {
		t.Fatalf("GET / over the socket: %v", err)
	}
	body, rerr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if rerr != nil {
		t.Fatalf("read the no-files page: %v", rerr)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET / = %d, want 503 with no Studio files", resp.StatusCode)
	}
	// The page names where the search looked, so a user can put the files
	// there or point --studio-dir at them.
	if want := filepath.Join(exeDir, "share", "marshal", "studio"); !strings.Contains(string(body), want) {
		t.Fatalf("GET / answered %q, want the page naming %s", string(body), want)
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
}
