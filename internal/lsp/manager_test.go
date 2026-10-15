package lsp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
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
// is cancelled while Initialize is in flight, startProcess reports the
// failure, reaps the process, and registers nothing with the manager.
func TestStartServerCancelledAfterInit(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "server.pid")

	// A stub server that blocks without reading stdin. Initialize's
	// request gets no response, so when we cancel the context the
	// in-flight Initialize fails via the cancelled initCtx and the
	// process is reaped. This is deterministic: the server never
	// registers, and no zombie process is left behind.
	stub := filepath.Join(dir, "stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho $$ > "+pidFile+"\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	servers := map[string]ServerSpec{
		"test": {Command: stub},
	}
	m := NewManager(dir, servers, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Wait until the stub has recorded its PID, then cancel: the
		// process is then provably running while Initialize is still in
		// flight, which is the window this test targets.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(pidFile); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()

	client, stop, err := m.startProcess(ctx, "test", servers["test"])
	if err == nil && stop == nil {
		t.Fatal("startProcess must return an error or a non-nil stop after cancellation")
	}
	if stop != nil {
		stop()
	}
	if client != nil {
		t.Fatal("startProcess returned a client for a cancelled start")
	}

	// No server should be registered.
	if _, ok := m.ServerFor("test"); ok {
		t.Fatal("test server should not be registered after context cancellation")
	}

	// The child process must have been reaped. If it were leaked, its PID
	// would still be alive after startProcess returns.
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("stub did not record its PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		t.Fatalf("bad PID in %s: %v", pidFile, err)
	}
	if err := processAlive(pid); err == nil {
		t.Errorf("server process %d still alive after context cancellation; leaked (not reaped)", pid)
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

	client, stop, err := m.startProcess(ctx, "test", servers["test"])
	if err == nil && stop == nil {
		t.Fatal("startProcess must return an error or a non-nil stop after post-init cancellation")
	}
	if stop != nil {
		stop()
	}
	if client != nil {
		t.Fatal("startProcess returned a client for a cancelled start")
	}

	if !postInitCalled {
		t.Fatal("afterInit hook never ran; the post-init cancellation branch was not reached")
	}

	// No server should be registered.
	if _, ok := m.ServerFor("test"); ok {
		t.Fatal("test server should not be registered after post-init context cancellation")
	}

	// The child process must have been reaped. If it were leaked, its PID
	// would still be alive after startProcess returns.
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

func TestServersForRootKeepsOnlyRepoLanguages(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "i.ts"), []byte(""), 0o644)
	got := ServersForRoot(map[string]ServerSpec{"go": {Command: "gopls"}, "typescript": {Command: "tls"}}, dir)
	if _, ok := got["go"]; !ok {
		t.Fatal("go server dropped")
	}
	if _, ok := got["typescript"]; ok {
		t.Fatal("typescript kept though only node_modules has .ts")
	}
}

func TestServersForRootMapsJavaScriptToTypeScript(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.js"), []byte(""), 0o644)
	if _, ok := ServersForRoot(map[string]ServerSpec{"typescript": {Command: "tls"}}, dir)["typescript"]; !ok {
		t.Fatal("a .js repo must start the typescript server")
	}
}

func TestWaitReadyBecomesTrueOnceStarted(t *testing.T) {
	m := NewManager(t.TempDir(), map[string]ServerSpec{"go": {}}, nil)
	m.start = func(ctx context.Context, lang string, spec ServerSpec) (*Client, func(), error) {
		c, _ := newScripted(t)
		time.Sleep(50 * time.Millisecond)
		return c, func() { c.Close() }, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	if !m.WaitReady(ctx, "go", 2*time.Second) {
		t.Fatal("WaitReady never became true")
	}
	if m.WaitReady(ctx, "rust", 100*time.Millisecond) {
		t.Fatal("an unconfigured language must not be ready")
	}
}

// Review focus 4.
func TestManagerDisablesAfterTwoFailures(t *testing.T) {
	m := NewManager(t.TempDir(), map[string]ServerSpec{"go": {Command: "gopls"}}, nil)
	var calls int32
	m.start = func(ctx context.Context, lang string, spec ServerSpec) (*Client, func(), error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil, errors.New("initialize: boom")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for m.Disabled("go") == "" {
		if time.Now().After(deadline) {
			t.Fatal("language never disabled")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("start called %d times, want 2 (one restart)", n)
	}
	start := time.Now()
	if m.WaitReady(ctx, "go", 5*time.Second) || time.Since(start) > time.Second {
		t.Fatal("WaitReady on a disabled language must return false promptly")
	}
	if !strings.Contains(m.Disabled("go"), "boom") {
		t.Fatalf("Disabled reason = %q", m.Disabled("go"))
	}
}

func TestManagerRestartsOnceAfterExit(t *testing.T) {
	m := NewManager(t.TempDir(), map[string]ServerSpec{"go": {}}, nil)
	var calls int32
	m.start = func(ctx context.Context, lang string, spec ServerSpec) (*Client, func(), error) {
		n := atomic.AddInt32(&calls, 1)
		c, s := newScripted(t)
		if n == 1 {
			go func() { time.Sleep(50 * time.Millisecond); s.kill() }()
		}
		return c, func() { c.Close() }, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("server not restarted after exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !m.WaitReady(ctx, "go", 2*time.Second) || m.Disabled("go") != "" {
		t.Fatal("restarted server must be ready, not disabled")
	}
}
