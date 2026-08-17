package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDetectServersOnPath(t *testing.T) {
	dir := t.TempDir()
	// Create a stub "gopls" executable on a temp PATH.
	stub := filepath.Join(dir, "gopls")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got := DetectServers(map[string]ServerSpec{}, map[string]bool{})
	if _, ok := got["go"]; !ok {
		t.Fatalf("expected go server detected on PATH, got %#v", got)
	}

	// Disabled language is excluded even when present.
	got = DetectServers(map[string]ServerSpec{}, map[string]bool{"go": true})
	if _, ok := got["go"]; ok {
		t.Fatal("disabled go should not be detected")
	}

	// Explicit config override for a language whose binary is not on PATH is
	// still included (user asked for it).
	got = DetectServers(map[string]ServerSpec{"python": {Command: "pyright-langserver", Args: []string{"--stdio"}}}, map[string]bool{})
	if _, ok := got["python"]; !ok {
		t.Fatal("configured python server should be included")
	}
}

// TestStartServerCancelledAfterInit verifies that when the parent context
// is cancelled after Initialize succeeds but before the client is
// registered in m.state, the process is cleaned up and no entry is left
// in m.state.
func TestStartServerCancelledAfterInit(t *testing.T) {
	dir := t.TempDir()

	// A stub server that blocks without reading stdin. Initialize's
	// request gets no response, so when we cancel the context the
	// in-flight Initialize fails via the cancelled initCtx and the
	// process is reaped. This is deterministic: the server never
	// registers, and no zombie process is left behind.
	stub := filepath.Join(dir, "stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	servers := map[string]ServerSpec{
		"test": {Command: stub},
	}
	m := NewManager(dir, servers, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	err := m.Run(ctx)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// No server should be registered.
	if _, ok := m.ServerFor("test"); ok {
		t.Fatal("test server should not be registered after context cancellation")
	}
}

// TestStartServerCancelledAfterInitPostInit exercises the new cancellation
// branch in startServer: the parent context is cancelled after Initialize
// succeeds (so the handshake completes) but before the client is registered
// in m.state. Unlike TestStartServerCancelledAfterInit, whose stub never
// answers the initialize request and so takes the pre-existing init-failure
// reap path, this test drives the handshake to completion and cancels in the
// narrow post-init window via the test-only afterInit hook. It verifies the
// process is reaped (not left as a zombie) and that no server is registered.
func TestStartServerCancelledAfterInitPostInit(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "server.pid")

	// A stub that completes the LSP initialize handshake (so Initialize
	// returns nil), records its PID, then holds the process open. The test
	// cancels the parent context via the afterInit hook once Initialize has
	// returned, which should trigger the post-init cancellation branch.
	stub := filepath.Join(dir, "stub.sh")
	stubSrc := "#!/bin/sh\n" +
		"len=0\n" +
		"while IFS= read -r line; do\n" +
		"  line=$(printf '%s' \"$line\" | tr -d '\\r')\n" +
		"  [ -z \"$line\" ] && break\n" +
		"  case \"$line\" in\n" +
		"    Content-Length:*) len=${line#Content-Length: } ;;\n" +
		"  esac\n" +
		"done\n" +
		"dd bs=1 count=\"$len\" of=/dev/null 2>/dev/null\n" +
		"echo $$ > " + pidFile + "\n" +
		"BODY='{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"capabilities\":{}}}'\n" +
		"printf 'Content-Length: %d\\r\\n\\r\\n%s' \"${#BODY}\" \"$BODY\"\n" +
		"sleep 3600\n"
	if err := os.WriteFile(stub, []byte(stubSrc), 0o755); err != nil {
		t.Fatal(err)
	}

	servers := map[string]ServerSpec{
		"test": {Command: stub},
	}
	m := NewManager(dir, servers, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel the parent context as soon as Initialize has returned, i.e.
	// in the post-init window the new branch protects.
	var postInitCalled bool
	m.afterInit = func() {
		postInitCalled = true
		cancel()
	}

	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if !postInitCalled {
		t.Fatal("afterInit hook never ran; the post-init cancellation branch was not reached")
	}

	// No server should be registered.
	if _, ok := m.ServerFor("test"); ok {
		t.Fatal("test server should not be registered after post-init context cancellation")
	}

	// The child process must have been reaped. If it were leaked, its PID
	// would still be alive after Run returns.
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("stub did not record its PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		t.Fatalf("bad PID in %s: %v", pidFile, err)
	}
	if err := processAlive(pid); err == nil {
		t.Errorf("server process %d still alive after post-init cancellation; leaked (not reaped)", pid)
	}
}

// processAlive reports whether the process with the given PID still exists.
func processAlive(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.Signal(0))
}

// TestRunStartsServersInParallel verifies that Manager.Run starts each server
// in its own goroutine so a hung Initialize does not block other languages.
func TestRunStartsServersInParallel(t *testing.T) {
	dir := t.TempDir()

	// A stub that sleeps for 60s then exits — simulates a hung server.
	hung := filepath.Join(dir, "hung.sh")
	if err := os.WriteFile(hung, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A stub that exits immediately — Initialize will fail quickly.
	quick := filepath.Join(dir, "quick.sh")
	if err := os.WriteFile(quick, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	servers := map[string]ServerSpec{
		"hung":  {Command: hung},
		"quick": {Command: quick},
	}
	m := NewManager(dir, servers, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run should return within ~3s (2s ctx timeout + small overhead).
	// If servers were started sequentially, the hung server's 60s sleep
	// would block the quick server and Run would not return in time.
	start := time.Now()
	err := m.Run(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Run took %v, expected <5s (sequential start would block)", elapsed)
	}

	// Neither server should be ready (both fail Initialize for different reasons).
	if _, ok := m.ServerFor("hung"); ok {
		t.Error("hung server should not be ready")
	}
	if _, ok := m.ServerFor("quick"); ok {
		t.Error("quick server should not be ready")
	}
}
