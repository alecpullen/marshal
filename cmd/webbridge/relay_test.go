package main

// relay_test.go covers the relay's own flags and secrets, and one run of two
// bridges for real: a bridge that relays, and the bridge that joins it
// (P8 D11-D13).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// relayLog collects a bridge's stderr, so a test can read the address it bound
// once it is serving.
type relayLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *relayLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *relayLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// relayWaitForAddr waits for the bridge's "listening on http://…" line and
// returns the origin it names. The listener is given port 0, so the line is
// how a test learns the port it really took.
func relayWaitForAddr(t *testing.T, log *relayLog) string {
	t.Helper()
	const marker = "listening on http://"
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(log.String(), "\n") {
			i := strings.Index(line, marker)
			if i < 0 {
				continue
			}
			rest := line[i+len(marker):]
			if j := strings.IndexByte(rest, ' '); j >= 0 {
				rest = rest[:j]
			}
			if rest != "" {
				return "http://" + rest
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the bridge never reported a listening address: %q", log.String())
	return ""
}

// relaySecretFile writes a join secret file at mode, returning its path.
func relaySecretFile(t *testing.T, secret string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "join.secret")
	if err := os.WriteFile(path, []byte(secret+"\n"), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// WriteFile's mode is subject to the umask, and the test is about the
	// mode the file ends up with.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return path
}

// relayGet asks url with the relay's bearer token and, when hostToken is not
// empty, the host's own token as the forward carries it.
func relayGet(t *testing.T, client *http.Client, url, relayToken, hostToken string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build %s: %v", url, err)
	}
	req.Header.Set("Authorization", "Bearer "+relayToken)
	if hostToken != "" {
		req.Header.Set("X-Marshal-Host-Token", hostToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

// relayHosts is the relay's host list as GET /api/relay/hosts answers it (D16).
type relayHosts struct {
	Hosts []struct {
		Name      string `json:"name"`
		Connected bool   `json:"connected"`
	} `json:"hosts"`
}

// relayWaitForHost waits until the relay names name as connected, which is
// what a joined bridge's first connections are.
func relayWaitForHost(t *testing.T, client *http.Client, relayURL, relayToken, name string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	last := "no answer"
	for time.Now().Before(deadline) {
		resp := relayGet(t, client, relayURL+"/api/relay/hosts", relayToken, "")
		var hosts relayHosts
		err := json.NewDecoder(resp.Body).Decode(&hosts)
		_ = resp.Body.Close()
		if err != nil {
			last = "decode: " + err.Error()
		} else {
			last = "nothing joined"
			for _, h := range hosts.Hosts {
				if h.Name == name && h.Connected {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the relay never reported %q connected: %s", name, last)
}

// TestRelayJoinPairsParseFromFlagAndEnv pins the two ways a relay is told
// which hosts may join it: the repeatable flag, and the comma-separated
// variable that applies only when the flag is not given.
func TestRelayJoinPairsParseFromFlagAndEnv(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  string
		want []relayJoin
	}{
		{
			name: "flag",
			args: []string{"--relay-join", "alpha=/tmp/a", "--relay-join", "beta=/tmp/b"},
			want: []relayJoin{{name: "alpha", path: "/tmp/a"}, {name: "beta", path: "/tmp/b"}},
		},
		{
			name: "variable",
			env:  "alpha=/tmp/a,beta=/tmp/b",
			want: []relayJoin{{name: "alpha", path: "/tmp/a"}, {name: "beta", path: "/tmp/b"}},
		},
		{
			// The flag decides when it is given: a stale variable must not
			// add a host nothing named on the command line.
			name: "flag wins over the variable",
			args: []string{"--relay-join", "alpha=/tmp/a"},
			env:  "beta=/tmp/b",
			want: []relayJoin{{name: "alpha", path: "/tmp/a"}},
		},
		{name: "neither"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateWebbridgeEnv(t)
			t.Setenv("WEBBRIDGE_RELAY_JOIN", tc.env)
			cfg, err := parseConfig(tc.args, io.Discard)
			if err != nil {
				t.Fatalf("parseConfig(%v): %v", tc.args, err)
			}
			if !reflect.DeepEqual(cfg.relayJoins, tc.want) {
				t.Errorf("relayJoins = %+v, want %+v", cfg.relayJoins, tc.want)
			}
		})
	}
}

// TestASecretOthersCanReadIsRefused pins decision 2: the command reads the
// secret files and refuses one group or other can read, naming the file. The
// bridge is handed values and never opens a secret file itself.
func TestASecretOthersCanReadIsRefused(t *testing.T) {
	shared := relaySecretFile(t, "s3cret", 0o644)
	_, err := readSecretFile(shared)
	if err == nil {
		t.Fatalf("readSecretFile accepted %s, which group and other can read", shared)
	}
	if !strings.Contains(err.Error(), shared) {
		t.Errorf("error %q does not name the file %s", err, shared)
	}

	private := relaySecretFile(t, "s3cret", 0o600)
	secret, err := readSecretFile(private)
	if err != nil {
		t.Fatalf("readSecretFile on a 0600 file: %v", err)
	}
	if secret != "s3cret" {
		t.Errorf("secret = %q, want %q", secret, "s3cret")
	}

	// A relay's own secrets go through the same rule, so a shared file stops
	// the start rather than being handed to the bridge.
	if _, err := readRelaySecrets([]relayJoin{{name: "alpha", path: shared}}); err == nil {
		t.Fatalf("readRelaySecrets accepted %s, which group and other can read", shared)
	}

	// An empty file holds no secret to hand over.
	if err := os.WriteFile(private, []byte("\n"), 0o600); err != nil {
		t.Fatalf("empty %s: %v", private, err)
	}
	if _, err := readSecretFile(private); err == nil {
		t.Fatalf("readSecretFile accepted %s, which holds no secret", private)
	}
}

// TestAJoiningBridgeReachesItsRelay runs two bridges: one that relays under the
// name "box", and one that joins it. The relayed route is the joiner's own
// GET /api/agents, reached through the relay's forward with both tokens (D13,
// D15).
func TestAJoiningBridgeReachesItsRelay(t *testing.T) {
	isolateWebbridgeEnv(t)
	const (
		relayToken = "relay-token"
		hostToken  = "host-token"
		hostName   = "box"
	)
	secretPath := relaySecretFile(t, "join-secret", 0o600)
	root := t.TempDir()
	for _, dir := range []string{"relay-project", "joiner-project"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The relay: a bridge that names the one host that may join it.
	relayStderr := &relayLog{}
	relayDone := make(chan error, 1)
	go func() {
		relayDone <- run(ctx, []string{
			"--addr", "127.0.0.1:0", "--preview-addr", "off", "--egress", "off",
			"--token", relayToken,
			"--relay-join", hostName + "=" + secretPath,
			"--cwd-root", filepath.Join(root, "relay-project"),
			"--workspace", filepath.Join(root, "relay.json"),
			"--state-dir", filepath.Join(root, "relay-state"),
		}, io.Discard, relayStderr)
	}()
	relayURL := relayWaitForAddr(t, relayStderr)

	// The joiner: a bridge that dials out to that relay and serves what the
	// relay reverses onto it.
	joinDone := make(chan error, 1)
	go func() {
		joinDone <- run(ctx, []string{
			"--addr", "127.0.0.1:0", "--preview-addr", "off", "--egress", "off",
			"--token", hostToken,
			"--join", relayURL, "--join-name", hostName, "--join-token-file", secretPath,
			"--cwd-root", filepath.Join(root, "joiner-project"),
			"--workspace", filepath.Join(root, "joiner.json"),
			"--state-dir", filepath.Join(root, "joiner-state"),
		}, io.Discard, io.Discard)
	}()

	client := &http.Client{Timeout: 10 * time.Second}
	relayWaitForHost(t, client, relayURL, relayToken, hostName)

	resp := relayGet(t, client, relayURL+"/api/hosts/"+hostName+"/api/agents", relayToken, hostToken)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read the forwarded answer: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/agents through the relay = %d, want 200: %s", resp.StatusCode, body)
	}
	// The relay marks what it forwards with the host that answered, so this
	// is the joiner's own answer rather than the relay's.
	if got := resp.Header.Get("X-Marshal-Relayed"); got != hostName {
		t.Errorf("X-Marshal-Relayed = %q, want %q", got, hostName)
	}
	var agents []json.RawMessage
	if err := json.Unmarshal(body, &agents); err != nil {
		t.Fatalf("the forwarded answer %q is not the agents list: %v", body, err)
	}

	cancel()
	for name, done := range map[string]chan error{"relay": relayDone, "joiner": joinDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s run returned %v after its context was cancelled; want nil", name, err)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("%s run did not return within 10s of its context being cancelled", name)
		}
	}
}
