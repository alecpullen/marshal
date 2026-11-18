package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"marshal/web/bridge"
)

// buildScript writes a stand-in webbridge build: a shell script whose
// --version answer, and whose exit status, are what the daemon's check sees.
func buildScript(t *testing.T, body string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "marshal-build-")
	if err != nil {
		t.Fatalf("create the build's directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "webbridge")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write the build: %v", err)
	}
	return path
}

// asOwnBuild makes ownExecutable answer path for the rest of the test, so the
// daemon records a script as its own binary instead of this test binary.
func asOwnBuild(t *testing.T, path string) {
	t.Helper()
	old := ownExecutable
	ownExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { ownExecutable = old })
}

// restartProgress posts a restart request and answers the lines the route
// writes, stopping at the line that ends them.
func restartProgress(t *testing.T, client *http.Client, body string) []bridge.DaemonRestartProgress {
	t.Helper()
	resp, err := client.Post("http://daemon/api/daemon/restart", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/daemon/restart: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/daemon/restart status = %d, want 200", resp.StatusCode)
	}
	var lines []bridge.DaemonRestartProgress
	dec := json.NewDecoder(resp.Body)
	for {
		var line bridge.DaemonRestartProgress
		if err := dec.Decode(&line); err != nil {
			break
		}
		lines = append(lines, line)
		if line.Restarting || line.Error != "" {
			break
		}
	}
	if len(lines) == 0 {
		t.Fatal("POST /api/daemon/restart answered no line")
	}
	return lines
}

// TestARestartReturnsTheReexec: asked to restart now, the local daemon says
// which build it restarts on, stops serving, and answers run with the binary
// main is to execute in its place (decision 2).
func TestARestartReturnsTheReexec(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	script := buildScript(t, "#!/bin/sh\necho webbridge test-new\nexit 0\n")
	asOwnBuild(t, script)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--profile", "local", "--addr", "off", "--socket", socket,
			"--oauth-refresh=false"}, io.Discard, io.Discard)
	}()

	client := socketClient(socket)
	waitForDaemon(t, client)

	lines := restartProgress(t, client, `{"now": true}`)
	last := lines[len(lines)-1]
	if !last.Restarting {
		t.Fatalf("restart answered %+v, want a restarting line", last)
	}
	if last.Version != "test-new" {
		t.Fatalf("restarting on %q, want test-new", last.Version)
	}

	select {
	case err := <-done:
		var reexec *errReexec
		if !errors.As(err, &reexec) {
			t.Fatalf("run returned %v, want an *errReexec", err)
		}
		if reexec.path != script {
			t.Fatalf("re-execute path = %q, want %q", reexec.path, script)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within 10s of the restart")
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket file survived the restart: %v", err)
	}
}

// TestARefusedOwnBuildKeepsTheDaemon: a build that cannot even answer
// --version is not executed, so nothing restarts — the route answers the
// refusal, and the daemon carries on serving it (decision 9).
func TestARefusedOwnBuildKeepsTheDaemon(t *testing.T) {
	isolateWebbridgeEnv(t)
	t.Setenv("MARSHAL_DATA_DIR", t.TempDir())
	socket := localSocketPath(t)
	script := buildScript(t, "#!/bin/sh\nexit 1\n")
	asOwnBuild(t, script)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--profile", "local", "--addr", "off", "--socket", socket,
			"--oauth-refresh=false"}, io.Discard, io.Discard)
	}()

	client := socketClient(socket)
	waitForDaemon(t, client)

	lines := restartProgress(t, client, `{"now": true}`)
	last := lines[len(lines)-1]
	if last.Restarting {
		t.Fatalf("restart answered %+v, want a refusal instead", last)
	}
	if last.Error == "" {
		t.Fatalf("restart answered %+v, want a refusal", last)
	}

	// Still serving, and the refusal is on GET /api/daemon.
	info := waitForDaemon(t, client)
	if info.Restart == nil || info.Restart.Refused == "" {
		t.Fatalf("GET /api/daemon reports restart = %+v, want a refusal", info.Restart)
	}
	select {
	case err := <-done:
		t.Fatalf("run returned %v after a refused build, want it still serving", err)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after its context was cancelled, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of its context being cancelled")
	}
}

// TestTheDaemonReexecutesInPlace (L6): a new build landing on the daemon's own
// path restarts it through an exec of that path — the same process, a later
// start — so the socket and the state it serves survive the swap.
func TestTheDaemonReexecutesInPlace(t *testing.T) {
	// A short directory: the copy's path is executed again, and a unix socket
	// path is length-bounded.
	dir, err := os.MkdirTemp("/tmp", "marshal-reexec-")
	if err != nil {
		t.Fatalf("create the daemon's directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	bin := filepath.Join(dir, "webbridge")
	copySelf(t, bin)
	next := filepath.Join(dir, "webbridge.next")
	copySelf(t, next)

	socket := filepath.Join(dir, "marshal.sock")
	cmd := exec.Command(bin,
		"--profile", "local", "--addr", "off", "--socket", socket,
		"--state-dir", filepath.Join(dir, "state"),
		"--workspace", filepath.Join(dir, "fleet.json"),
		"--marshal-bin", "/usr/bin/false", "--oauth-refresh=false")
	cmd.Env = append(subprocessEnv(t, os.Environ(), dir), testAsBinaryEnv+"=1")
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the daemon: %v", err)
	}
	t.Cleanup(func() { stopDaemon(t, cmd, &logs) })

	client := socketClient(socket)
	first := waitForDaemon(t, client)
	if first.PID != cmd.Process.Pid {
		t.Fatalf("GET /api/daemon reports pid %d, want the subprocess's %d", first.PID, cmd.Process.Pid)
	}

	// A new build lands where the daemon was started from.
	if err := os.Rename(next, bin); err != nil {
		t.Fatalf("replace the daemon's own binary: %v", err)
	}

	lines := restartProgress(t, client, `{}`)
	if last := lines[len(lines)-1]; !last.Restarting {
		t.Fatalf("restart answered %+v, want a restarting line", last)
	}

	// The pid is the same either side of the exec, so a later start is what
	// says the process re-executed rather than a second daemon answering.
	restarted := awaitLaterStart(t, client, first.StartedAt)
	if restarted.PID != cmd.Process.Pid {
		t.Fatalf("the restarted daemon reports pid %d, want the subprocess's %d", restarted.PID, cmd.Process.Pid)
	}
	if restarted.PID != first.PID {
		t.Fatalf("the restarted daemon reports pid %d, want the first daemon's %d", restarted.PID, first.PID)
	}
}

// copySelf writes a copy of this test binary to path, so the copy can be the
// daemon's own executable: TestMain serves webbridge when it is asked to.
func copySelf(t *testing.T, path string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("find this test binary: %v", err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read this test binary: %v", err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// subprocessEnv is this process's environment without any WEBBRIDGE_* setting
// and with every home and config variable pointed into dir: the re-executed
// daemon belongs to this test, so it must neither read the developer's
// ~/.config/marshal nor write into their data directory.
func subprocessEnv(t *testing.T, environ []string, dir string) []string {
	t.Helper()
	pinned := []struct{ key, value string }{
		{"HOME", filepath.Join(dir, "home")},
		{"XDG_CONFIG_HOME", filepath.Join(dir, "config")},
		{"XDG_DATA_HOME", filepath.Join(dir, "data")},
		{"MARSHAL_CONFIG_DIR", filepath.Join(dir, "marshal-config")},
		{"MARSHAL_DATA_DIR", filepath.Join(dir, "marshal-data")},
	}
	for _, pin := range pinned {
		if err := os.MkdirAll(pin.value, 0o700); err != nil {
			t.Fatalf("create the subprocess's %s: %v", pin.key, err)
		}
	}
	out := make([]string, 0, len(environ)+len(pinned))
	for _, kv := range environ {
		if strings.HasPrefix(kv, "WEBBRIDGE_") {
			continue
		}
		key, _, _ := strings.Cut(kv, "=")
		keep := true
		for _, pin := range pinned {
			if key == pin.key {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	for _, pin := range pinned {
		out = append(out, pin.key+"="+pin.value)
	}
	return out
}

// stopDaemon ends the subprocess: SIGTERM, a bounded wait, then a kill, so the
// test never leaves a daemon behind.
func stopDaemon(t *testing.T, cmd *exec.Cmd, logs *bytes.Buffer) {
	t.Helper()
	_ = cmd.Process.Signal(syscall.SIGTERM)
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Errorf("the daemon did not stop cleanly: %v\n%s", err, logs.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-waited
		t.Errorf("the daemon did not stop within 10s of SIGTERM:\n%s", logs.String())
	}
}

// awaitLaterStart polls GET /api/daemon until the daemon reports a start later
// than after, which is how a re-executed daemon is told from the one it
// replaced: the pid is the same either side of an exec.
func awaitLaterStart(t *testing.T, client *http.Client, after int64) bridge.DaemonInfo {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://daemon/api/daemon")
		if err == nil {
			var info bridge.DaemonInfo
			derr := json.NewDecoder(resp.Body).Decode(&info)
			_ = resp.Body.Close()
			if derr == nil && resp.StatusCode == http.StatusOK && info.StartedAt > after {
				return info
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the daemon did not report a later start: it never re-executed")
	return bridge.DaemonInfo{}
}
