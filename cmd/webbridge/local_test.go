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
	}{
		{"egress flag", []string{"--profile", "local", "--egress", "on"}, nil},
		{"egress env", []string{"--profile", "local"}, map[string]string{"WEBBRIDGE_EGRESS": "on"}},
		{"secrets local", []string{"--profile", "local", "--secrets", "local", "--secrets-key-file", keyFile}, nil},
		{"socket without the profile", []string{"--socket", "/tmp/x.sock"}, nil},
		{"addr off without the profile", []string{"--addr", "off"}, nil},
		{"profile cloud", []string{"--profile", "cloud"}, nil},
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
			// profile.
			if !strings.Contains(err.Error(), "local") {
				t.Errorf("error %q does not name the local profile", err)
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
