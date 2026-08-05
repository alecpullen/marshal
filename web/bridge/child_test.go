package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// testContext returns a context that times out quickly so a broken
// child fails the test instead of hanging it.
func testContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// helperEnv marks a re-exec'd test binary as a fake ACP child. See
// TestHelperProcess.
const helperEnv = "GO_WANT_HELPER_PROCESS"

// TestHelperProcess is not a test: it is the fake ACP child, re-exec'd
// by the real tests via exec.Command(os.Args[0], ...). Behavior is
// selected with HELPER_MODE:
//
//   - "normal": line-oriented echo server. Requests (frames with an
//     "id") get {"jsonrpc":"2.0","id":<id>,"result":<method>} replies;
//     notifications are counted and echoed back once as the
//     "helper/notifyEcho" notification.
//   - "die": replies to the first request, then exits 1.
//   - "notify": emits the "helper/ready" notification immediately, then
//     behaves like "normal".
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	runHelperChild(os.Getenv("HELPER_MODE"))
	os.Exit(0)
}

// helperCommand returns the exec.Cmd that re-execs this test binary as
// the fake child in the given mode.
func helperCommand(mode string) (string, []string, []string) {
	env := []string{helperEnv + "=1", "HELPER_MODE=" + mode}
	return os.Args[0], []string{"-test.run=^TestHelperProcess$", "-test.v"}, env
}

func runHelperChild(mode string) {
	fmt.Fprintln(os.Stderr, "helper ready")
	if mode == "notify" {
		fmt.Println(`{"jsonrpc":"2.0","method":"helper/ready","params":{"pid":1}}`)
	}
	sc := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	notifyCount := 0
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		if len(req.ID) == 0 {
			// Inbound notification: count it and (once) echo the tally.
			notifyCount++
			if notifyCount == 1 {
				_ = enc.Encode(map[string]any{
					"jsonrpc": "2.0",
					"method":  "helper/notifyEcho",
					"params":  map[string]any{"count": notifyCount},
				})
			}
			continue
		}
		fmt.Fprintf(os.Stderr, "helper saw request %s\n", req.Method)
		if mode == "registry" || mode == "registry-die" {
			handleRegistryMode(&req, enc)
			// registry-die kills the process only after session/new, so
			// the post-restart session/resume survives on generation 2.
			if mode == "registry-die" && req.Method == "session/new" {
				os.Exit(1)
			}
			continue
		}
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  req.Method,
		}
		_ = enc.Encode(resp)
		if mode == "die" {
			os.Exit(1)
		}
	}
}

// handleRegistryMode backs the registry tests: session/new returns a
// sessionId, everything else returns an empty object. The trigger
// methods test/ask_permission and test/ask_question make the helper
// issue a child-initiated request and report the bridge's response on
// stderr, where tests can observe it via StderrLog.
func handleRegistryMode(req *struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}, enc *json.Encoder) {
	switch req.Method {
	case "session/new":
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{"sessionId": "s-1"},
		})
	case "test/ask_permission", "test/ask_question":
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{},
		})
		method := "session/request_permission"
		params := map[string]any{"toolCallId": "tc-1", "toolName": "shell.run"}
		if req.Method == "test/ask_question" {
			method = "session/request_question"
			params = map[string]any{
				"sessionId":  "s-1",
				"questionId": "q-1",
				"questions":  []map[string]any{{"question": "proceed?"}},
			}
		}
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      9000,
			"method":  method,
			"params":  params,
		})
	default:
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{},
		})
	}
}

// waitFor polls cond every 10 ms until it is true or the timeout
// elapses, failing the test in the latter case.
func waitFor(t *testing.T, d time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}

func newTestChild(t *testing.T, mode string) *Child {
	t.Helper()
	bin, args, env := helperCommand(mode)
	return &Child{
		MarshalBin: bin,
		Args:       args,
		Env:        env,
	}
}

func TestChildStartStop(t *testing.T) {
	c := newTestChild(t, "normal")
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := testContext(t)
	defer cancel()
	res, err := c.Request(ctx, "session/new", map[string]string{"cwd": "/tmp"})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	var got string
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got != "session/new" {
		t.Fatalf("result = %q, want %q", got, "session/new")
	}

	c.Stop()
	c.Stop() // idempotent: second call must not panic or block
}

func TestChildStopWithoutStart(t *testing.T) {
	c := &Child{}
	c.Stop() // must not panic
}

func TestChildRequestErrorWhenStopped(t *testing.T) {
	c := newTestChild(t, "normal")
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	c.Stop()

	ctx, cancel := testContext(t)
	defer cancel()
	if _, err := c.Request(ctx, "session/new", nil); err == nil {
		t.Fatal("Request after Stop: expected error, got nil")
	}
}

func TestChildNotificationRouting(t *testing.T) {
	type note struct {
		method string
		params json.RawMessage
	}
	var mu sync.Mutex
	var notes []note

	c := newTestChild(t, "notify")
	c.OnNotification = func(method string, params json.RawMessage) {
		mu.Lock()
		notes = append(notes, note{method: method, params: params})
		mu.Unlock()
	}
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	// The "notify" helper emits helper/ready on startup.
	waitFor(t, 5*time.Second, "startup notification", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(notes) >= 1
	})

	// Round-trip one notification through the child; it echoes a
	// helper/notifyEcho notification back.
	if err := c.Notify("session/cancel", map[string]string{"sessionId": "s1"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	waitFor(t, 5*time.Second, "notify echo", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(notes) >= 2
	})

	mu.Lock()
	defer mu.Unlock()
	if notes[0].method != "helper/ready" {
		t.Fatalf("first notification method = %q, want helper/ready", notes[0].method)
	}
	if notes[1].method != "helper/notifyEcho" {
		t.Fatalf("second notification method = %q, want helper/notifyEcho", notes[1].method)
	}
	var params struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(notes[1].params, &params); err != nil {
		t.Fatalf("unmarshal echo params: %v", err)
	}
	if params.Count != 1 {
		t.Fatalf("echo count = %d, want 1", params.Count)
	}
}

func TestChildRestartOnExit(t *testing.T) {
	restarts := make(chan struct{}, 4)
	c := newTestChild(t, "die")
	c.OnRestart = func() {
		restarts <- struct{}{}
	}
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	// The "die" helper replies once, then exits 1 — that reply proves
	// the first generation was up before it died.
	ctx, cancel := testContext(t)
	defer cancel()
	if _, err := c.Request(ctx, "session/new", nil); err != nil {
		t.Fatalf("first Request: %v", err)
	}

	// The supervisor must respawn the child and invoke OnRestart.
	select {
	case <-restarts:
	case <-time.After(5 * time.Second):
		t.Fatal("OnRestart was not invoked after unexpected exit")
	}

	// The respawned generation answers requests again.
	waitFor(t, 5*time.Second, "respawned child to answer", func() bool {
		rctx, rcancel := testContext(t)
		defer rcancel()
		_, err := c.Request(rctx, "ping", nil)
		return err == nil
	})
}

func TestChildConcurrentRequests(t *testing.T) {
	c := newTestChild(t, "normal")
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			method := fmt.Sprintf("method/%d", i)
			ctx, cancel := testContext(t)
			defer cancel()
			res, err := c.Request(ctx, method, map[string]int{"n": i})
			if err != nil {
				errs <- fmt.Errorf("worker %d: %w", i, err)
				return
			}
			var got string
			if err := json.Unmarshal(res, &got); err != nil {
				errs <- fmt.Errorf("worker %d: unmarshal: %w", i, err)
				return
			}
			if got != method {
				errs <- fmt.Errorf("worker %d: result = %q, want %q", i, got, method)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestChildSerializedConcurrentWrites(t *testing.T) {
	c := newTestChild(t, "normal")
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	// Interleave Notify and Request from many goroutines. If stdin
	// writes were not serialized, frames would interleave mid-line and
	// the child's scanner would fail to parse them, hanging requests.
	const workers = 16
	const rounds = 10
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range rounds {
				if err := c.Notify("session/update", map[string]int{"i": i, "j": j}); err != nil {
					errs <- fmt.Errorf("worker %d notify: %w", i, err)
					return
				}
				ctx, cancel := testContext(t)
				_, err := c.Request(ctx, "session/prompt", map[string]int{"i": i, "j": j})
				cancel()
				if err != nil {
					errs <- fmt.Errorf("worker %d request: %w", i, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestChildStderrCapture(t *testing.T) {
	c := newTestChild(t, "normal")
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	// Drive one request so the child writes to its stderr log.
	ctx, cancel := testContext(t)
	defer cancel()
	if _, err := c.Request(ctx, "session/new", nil); err != nil {
		t.Fatalf("Request: %v", err)
	}

	waitFor(t, 5*time.Second, "stderr capture", func() bool {
		return strings.Contains(c.StderrLog(), "helper saw request")
	})
	if !strings.Contains(c.StderrLog(), "helper ready") {
		t.Fatalf("StderrLog() missing startup line; got:\n%s", c.StderrLog())
	}
}
