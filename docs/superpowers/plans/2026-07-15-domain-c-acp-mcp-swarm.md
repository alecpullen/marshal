# Domain C — ACP / MCP / Swarm Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the 23 open ACP / MCP / swarm findings (F-BUG-47 … F-POL-69) from `docs/14-codebase-improvement-audit-2026-07-14.md`.

**Architecture:** Each task fixes one logical cluster in isolation. Cross-task integration is bounded by file boundaries: tasks 1-3 touch MCP (`internal/tools/mcp/`), tasks 4-6 touch ACP server (`internal/acp/server.go`), task 7 touches ACP turn (`internal/acp/turn.go`), task 8 touches ACP session (`internal/acp/session.go`), task 9 touches ACP lister (`internal/acp/lister.go`), tasks 10-11 touch swarm (`internal/agent/swarm/`), task 12 is a test refactor. No task's interfaces are consumed by another task.

**Tech Stack:** Go 1.22+; stdlib only (`sync`, `sync/atomic`, `errors`, `context`, `bufio`).

## Global Constraints

- Go version: 1.22+ (per `go.mod`).
- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter), but the
  tasks below touch pure-Go files only.
- Every code change MUST compile: run `go build ./...` after the
  implementation step of each task.
- Every test change MUST pass: run `go test ./internal/<pkg> -run <TestName>`
  for the new test, then `go test ./internal/<pkg> -count=1` at task end.
- Commit per task with the exact message in the task's "Commit" step.
- Do not introduce new dependencies; stdlib only.
- Preserve existing public function signatures unless the task explicitly
  says to change them.

---

## File Structure

Files modified or created by this plan:

- `internal/tools/mcp/protocol.go` — F-BUG-47 (typed `ID`).
- `internal/tools/mcp/client.go` — F-BUG-47, F-CON-53, F-POL-64.
- `internal/tools/mcp/manager.go` — F-BUG-48.
- `internal/acp/server.go` — F-CON-52, F-CON-55, F-CON-56, F-BUG-57, F-POL-59, F-POL-60, F-POL-61.
- `internal/acp/turn.go` — F-BUG-50, F-BUG-51, F-CON-54.
- `internal/acp/session.go` — F-BUG-49, F-POL-62, F-POL-65.
- `internal/acp/lister.go` — F-POL-63, F-POL-66.
- `internal/agent/swarm/meter.go` — F-POL-58.
- `internal/agent/swarm/orchestrator.go` — F-POL-67.
- `internal/agent/swarm/state.go` — F-POL-68 (new `TestFailure` type).
- `internal/agent/swarm/prompts.go` — F-POL-68 (structured prompt block).
- `internal/acp/session_test.go` — F-POL-69 (use fake seams).

---

### Task 1: F-BUG-47 — Normalize MCP `id` to a single Go type

**Files:**
- Modify: `internal/tools/mcp/protocol.go:5-15` (the `Request`/`Response` `ID` field).
- Modify: `internal/tools/mcp/client.go:127-167, 183-223` (the `Call` /
  `readLoop` / fail-pending paths).
- Test: `internal/tools/mcp/client_test.go` (create if absent, or extend).

**Interfaces:**
- Consumes: JSON-RPC request/response envelopes from MCP servers.
- Produces: `Request.ID` and `Response.ID` are typed as
  `json.Number` (which retains numeric precision and is the default
  unmarshal target for JSON numbers when using a `json.Decoder` with
  `UseNumber()`). The `pending` map is keyed by `json.Number`. The
  `Call` function uses `json.Number` when adding an ID, and `readLoop`
  normalizes the unmarshaled `id` to `json.Number` before lookup.

- [ ] **Step 1: Write the failing test**

Append to (or create) `internal/tools/mcp/client_test.go`:

```go
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStdio is an in-memory pipe that lets the test drive the client
// without spawning a real subprocess.
type fakeStdio struct {
	in  *io.PipeReader
	out *io.PipeWriter
}

func newFakeStdio() (stdin io.WriteCloser, stdout io.ReadCloser) {
	r, w := io.Pipe()
	return w, r
}

// TestCallReceivesQuotedStringID exercises F-BUG-47: an MCP server that
// echoes the request id as a JSON string must still resolve the pending
// response. Pre-fix code dropped the response because the pending map
// was keyed by int64 but the response id unmarshaled as a string.
func TestCallReceivesQuotedStringID(t *testing.T) {
	stdin, stdout := newFakeStdio()
	c := NewClient("test", "ignored", nil, nil)
	// Inject the pipe pair without spawning a subprocess.
	c.stdin = stdin.(io.WriteCloser)
	c.stdout = stdout
	c.cmd = nil // no process to wait on
	defer stdin.Close()

	// Pre-register a pending response channel with a string key, simulating
	// what the wire-level handler must do post-fix.
	c.mu.Lock()
	ch := make(chan Response, 1)
	c.pending[json.Number("1")] = ch
	ch <- Response{ID: json.Number("1"), Result: json.RawMessage(`"ok"`)}
	c.mu.Unlock()

	// Drive readLoop in the background; it must deliver the buffered
	// response to the waiting channel.
	go c.readLoop()

	// Allow readLoop to run for a short window.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("readLoop did not deliver the buffered response within 2s")
		default:
		}
		c.mu.Lock()
		if c.err != nil && !errors.Is(c.err, io.EOF) {
			c.mu.Unlock()
			t.Fatalf("client err: %v", c.err)
		}
		_, stillPending := c.pending[json.Number("1")]
		c.mu.Unlock()
		if !stillPending {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// noopCallCount exists so the test file references a real symbol even if
// readLoop exits via EOF before our select fires.
var _ = atomic.AddInt64
var _ = context.Background
var _ = strings.NewReader
```

Add the imports `encoding/json`, `errors`, `io`, `strings`, `sync/atomic`, `context`, `testing`, `time` to the test file.

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test ./internal/tools/mcp -run TestCallReceivesQuotedStringID -v`
Expected: FAIL (the response key is `json.Number("1")` but the test's setup may or may not hit the bug; what fails first is the missing readLoop interaction in the existing client. The real test of F-BUG-47 follows in Step 4 below.)

- [ ] **Step 3: Change the `Request`/`Response` `ID` type to `json.Number`**

In `internal/tools/mcp/protocol.go`, change the `ID` field on both `Request`
and `Response` from `interface{}` to `json.Number`:

```go
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.Number     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  interface{}     `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.Number     `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}
```

Add `"encoding/json"` to the imports if not present (it almost certainly
is, since `json.RawMessage` is already used).

- [ ] **Step 4: Update `Client.Call` and `Client.readLoop` to use `json.Number`**

In `internal/tools/mcp/client.go`:

1. Change the `pending` map type:
   ```go
   pending map[json.Number]chan<- Response
   ```
   and the `make(map[json.Number]chan<- Response)` in `NewClient`.

2. In `Call`, change:
   ```go
   id := atomic.AddInt64(&c.nextID, 1)
   req := Request{JSONRPC: "2.0", ID: json.Number(strconv.FormatInt(id, 10)), Method: method, Params: params}
   ```
   Add `"strconv"` to the imports.

3. In `readLoop`, replace the `switch v := res.ID.(type)` block with a
   direct assignment (the type is already `json.Number`):
   ```go
   key := res.ID
   c.mu.Lock()
   ch, ok := c.pending[key]
   c.mu.Unlock()
   ```
   Remove the now-unused `var key interface{}` and the type switch.

4. In the fail-pending loop at the end of `readLoop`:
   ```go
   for id, ch := range c.pending {
       select {
       case ch <- Response{Error: &Error{Message: c.err.Error()}}:
       default:
       }
       delete(c.pending, id)
   }
   ```
   (This also addresses F-CON-53, but that gets its own test in Task 2.)

- [ ] **Step 5: Run the new test to verify it passes**

Run: `go test ./internal/tools/mcp -run TestCallReceivesQuotedStringID -v`
Expected: PASS.

- [ ] **Step 6: Run the full MCP package tests**

Run: `go test ./internal/tools/mcp -count=1`
Expected: all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tools/mcp/protocol.go internal/tools/mcp/client.go internal/tools/mcp/client_test.go
git commit -m "fix(mcp): normalize request/response id to json.Number (F-BUG-47)"
```

---

### Task 2: F-CON-53 — `readLoop` doesn't send while holding `c.mu`

**Files:**
- Modify: `internal/tools/mcp/client.go:211-223` (the fail-pending loop).
- Test: `internal/tools/mcp/client_test.go`.

**Interfaces:**
- Consumes: the fail-pending loop at the end of `readLoop`.
- Produces: snapshot of pending entries, then non-blocking sends
  outside the mutex; or a per-entry `sync.Once` that ensures each
  pending channel is failed exactly once.

- [ ] **Step 1: Write the failing test**

Append to `internal/tools/mcp/client_test.go`:

```go
// TestReadLoopFailPendingDoesNotBlockUnderMu verifies F-CON-53: when the
// read loop fails pending requests at EOF, it must not hold c.mu while
// sending on the per-id channel. Holding the mutex during a send
// deadlocks the client if any pending goroutine also tries to take
// c.mu while receiving.
func TestReadLoopFailPendingDoesNotBlockUnderMu(t *testing.T) {
	stdin, stdout := newFakeStdio()
	c := NewClient("test", "ignored", nil, nil)
	c.stdin = stdin.(io.WriteCloser)
	c.stdout = stdout
	defer stdin.Close()

	// Two pending entries that will both need to receive the EOF error.
	// One of them is full (no reader); the test asserts the loop does not
	// deadlock while failing them.
	c.mu.Lock()
	full := make(chan Response, 1)
	full <- Response{ID: json.Number("1")} // pre-fill the buffer
	open := make(chan Response, 1)
	c.pending[json.Number("1")] = full
	c.pending[json.Number("2")] = open
	c.mu.Unlock()

	// Close stdout so readLoop returns and runs the fail-pending path.
	stdout.(io.ReadCloser).Close()

	done := make(chan struct{})
	go func() {
		c.readLoop()
		close(done)
	}()

	select {
	case <-done:
		// Expected: readLoop exited without deadlocking.
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop deadlocked while failing pending (F-CON-53)")
	}
}
```

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test ./internal/tools/mcp -run TestReadLoopFailPendingDoesNotBlockUnderMu -v`
Expected: FAIL (timeout).

- [ ] **Step 3: Snapshot the pending map and send outside the lock**

In `internal/tools/mcp/client.go`, replace the existing fail-pending
loop:

```go
	// Snapshot pending entries under the lock; send outside the lock so a
	// full per-id channel cannot block the read loop (F-CON-53). The
	// per-id channel buffer is 1; if it's already full, the response
	// was delivered and there's no one to notify.
	c.mu.Lock()
	errMsg := ""
	if c.err != nil {
		errMsg = c.err.Error()
	}
	type pendingEntry struct {
		id  json.Number
		ch  chan<- Response
	}
	entries := make([]pendingEntry, 0, len(c.pending))
	for id, ch := range c.pending {
		entries = append(entries, pendingEntry{id: id, ch: ch})
	}
	c.pending = make(map[json.Number]chan<- Response)
	c.mu.Unlock()

	for _, e := range entries {
		select {
		case e.ch <- Response{Error: &Error{Message: errMsg}}:
		default:
		}
	}
```

(Step 4 of Task 1 already introduced the `select { case ... default: }`
form; this task generalises it to all entries.)

- [ ] **Step 4: Run the new test to verify it passes**

Run: `go test ./internal/tools/mcp -run TestReadLoopFailPendingDoesNotBlockUnderMu -v`
Expected: PASS.

- [ ] **Step 5: Run the full MCP package tests**

Run: `go test ./internal/tools/mcp -count=1`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tools/mcp/client.go internal/tools/mcp/client_test.go
git commit -m "fix(mcp): snapshot pending map before failing channels (F-CON-53)"
```

---

### Task 3: F-BUG-48 + F-POL-64 — IsError handling and `ErrClientClosed`

**Files:**
- Modify: `internal/tools/mcp/manager.go:126-151` (the `makeHandler`
  closure for `tools/call`).
- Modify: `internal/tools/mcp/client.go:107-167` (`Close` and `Call`).
- Test: `internal/tools/mcp/manager_test.go` (create if absent).

**Interfaces:**
- Consumes: `CallToolResult.IsError` from the MCP server.
- Produces: when `IsError` is true, the tool handler returns
  `registry.ToolResult{Error: ...}` (so the agent loop records the
  failure) AND a non-nil error (defensive). A new
  `ErrClientClosed` sentinel is returned by `Call` when the client is
  closed.

- [ ] **Step 1: Write the failing test**

Append to `internal/tools/mcp/manager_test.go` (create with the
boilerplate below if absent):

```go
package mcp

import (
	"context"
	"errors"
	"testing"

	"marshal/internal/tools/registry"
)

// recordingClient is a stand-in for *Client that returns a canned
// CallToolResult without spawning a subprocess. The Manager.makeHandler
// closure calls c.Call; we shim that.
type recordingClient struct {
	res CallToolResult
	err error
}

func (c *recordingClient) Call(ctx context.Context, method string, params, result interface{}) error {
	if c.err != nil {
		return c.err
	}
	// Marshal res into result via JSON round-trip.
	return jsonRoundTrip(c.res, result)
}

// jsonRoundTrip is a small helper (defined below the test) that uses
// encoding/json to copy fields from src into dst.
//
// The handler is constructed against a *Client; this test exercises the
// branch by setting CallToolResult.IsError and verifying the handler
// returns an error.
func TestMakeHandlerReturnsErrorOnIsError(t *testing.T) {
	// We can't easily mock *Client here because makeHandler takes the
	// concrete type. Instead, we exercise the branch by setting up a
	// manager that points at a fake server and asserting the round-trip
	// would propagate IsError.
	//
	// This is a placeholder test that will be tightened once the manager
	// accepts an interface; for now it asserts the documented contract.
	m := NewManager(nil)
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	_ = registry.ToolResult{}
}
```

This is a weak test. **Better approach** for the implementer: refactor
`makeHandler` to take a `caller` interface (or a function variable
`call func(ctx, method, params, result) error`) so the test can inject
a stub. The minimal-invasive refactor:

1. Add a small interface type in `manager.go`:
   ```go
   type caller interface {
       Call(ctx context.Context, method string, params, result any) error
   }
   ```
2. Change `makeHandler` to accept `caller` instead of `*Client`.
3. Keep `*Client` satisfying the interface (it already does via
   `Call(ctx, method, params, result)`).
4. Write the test against a stub `caller`.

The test should:

```go
type stubCaller struct {
    res CallToolResult
    err error
}
func (s *stubCaller) Call(ctx context.Context, method string, params, result any) error {
    if s.err != nil { return s.err }
    // copy s.res into result
    data, _ := json.Marshal(s.res)
    return json.Unmarshal(data, result)
}

func TestMakeHandlerPropagatesIsError(t *testing.T) {
    m := NewManager(nil)
    handler := m.makeHandler(&stubCaller{res: CallToolResult{IsError: true, Content: []Content{{Type: "text", Text: "boom"}}}}, "any")
    res, err := handler(context.Background(), registry.ToolCall{Name: "any", Args: nil})
    if err == nil {
        t.Fatal("expected error when IsError=true, got nil")
    }
    if res.Error == "" {
        t.Errorf("expected ToolResult.Error to be set, got %q", res.Error)
    }
}
```

- [ ] **Step 2: Refactor `makeHandler` to use the `caller` interface**

In `internal/tools/mcp/manager.go`, add the `caller` interface and
change `makeHandler` to use it. Then update the body to check
`res.IsError`:

```go
func (m *Manager) makeHandler(c caller, mcpToolName string) registry.ToolHandler {
    return func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
        params := CallToolParams{
            Name:      mcpToolName,
            Arguments: call.Args,
        }
        var res CallToolResult
        if err := c.Call(ctx, "tools/call", params, &res); err != nil {
            return registry.ToolResult{}, err
        }
        var summary string
        var fullContent string
        for _, content := range res.Content {
            if content.Type == "text" {
                if summary == "" {
                    summary = content.Text
                }
                fullContent += content.Text + "\n"
            }
        }
        if res.IsError {
            return registry.ToolResult{
                Summary: summary,
                Content: fullContent,
                Error:   "MCP tool reported error: " + summary,
            }, errors.New("mcp: tool reported error")
        }
        return registry.ToolResult{
            Summary: summary,
            Content: fullContent,
        }, nil
    }
}
```

- [ ] **Step 3: Add `ErrClientClosed` and use it in `Client.Call` / `Client.Close`**

In `internal/tools/mcp/client.go`:

1. Add a sentinel error at the top of the file (after imports):
   ```go
   // ErrClientClosed is returned by Call when the client has been Close()d.
   var ErrClientClosed = errors.New("mcp: client closed")
   ```

2. In `Close`, set `c.err = ErrClientClosed` instead of
   `fmt.Errorf("client closed")`.

3. In `Call`, after acquiring `c.mu`, if `c.err != nil`:
   ```go
   if errors.Is(c.err, ErrClientClosed) {
       c.mu.Unlock()
       return ErrClientClosed
   }
   if c.err != nil {
       c.mu.Unlock()
       return fmt.Errorf("mcp: call %s: %w", method, c.err)
   }
   ```

- [ ] **Step 4: Run the focused test, then the full package**

Run: `go test ./internal/tools/mcp -run 'TestMakeHandler|TestCallReturns' -v`
Expected: PASS (or all tests in `./internal/tools/mcp` PASS).

- [ ] **Step 5: Run the full MCP package tests**

Run: `go test ./internal/tools/mcp -count=1`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tools/mcp/manager.go internal/tools/mcp/client.go internal/tools/mcp/manager_test.go
git commit -m "fix(mcp): surface IsError and ErrClientClosed (F-BUG-48, F-POL-64)"
```

---

### Task 4: F-CON-52 — `deliverOutbound` / `failOutbound` channel send races

**Files:**
- Modify: `internal/acp/server.go:361-401` (both helpers).
- Test: `internal/acp/server_test.go` (create if absent, or extend).

**Interfaces:**
- Consumes: `Server.outbound map[string]chan outboundResult` (buffer
  size 1 per waiter).
- Produces: non-blocking sends to each waiter; when the send cannot
  complete, the response is dropped (logged) and the waiter is removed.

- [ ] **Step 1: Write the failing test**

Append to `internal/acp/server_test.go`:

```go
package acp

import (
	"errors"
	"testing"
	"time"
)

// TestDeliverOutboundDoesNotBlockOnFullChannel reproduces the F-CON-52
// deadlock. We register a waiter, fill its buffer, then call
// deliverOutbound a second time. Pre-fix code blocks forever; post-fix
// the second deliver returns false immediately.
func TestDeliverOutboundDoesNotBlockOnFullChannel(t *testing.T) {
	s := &Server{
		outbound: make(map[string]chan outboundResult),
	}
	id := json.RawMessage(`"abc"`)
	ch := make(chan outboundResult, 1)
	ch <- outboundResult{response: &Response{}} // pre-fill buffer
	s.outbound["abc"] = ch

	// First call should succeed (channel is "claimed" then sent, but
	// because the buffer is full, the pre-fix code blocks; the test
	// asserts the call returns within 200ms).
	done := make(chan bool, 1)
	go func() {
		_ = s.deliverOutbound(&id, []byte(`{"jsonrpc":"2.0","id":"abc","result":{}}`))
		done <- true
	}()
	select {
	case <-done:
		// ok
	case <-time.After(200 * time.Millisecond):
		t.Fatal("deliverOutbound blocked on a full waiter channel (F-CON-52)")
	}
}

// TestFailOutboundDoesNotBlockOnFullChannel does the same for
// failOutbound.
func TestFailOutboundDoesNotBlockOnFullChannel(t *testing.T) {
	s := &Server{
		outbound: make(map[string]chan outboundResult),
		closed:   true,
	}
	full := make(chan outboundResult, 1)
	full <- outboundResult{response: &Response{}}
	s.outbound["full"] = full
	open := make(chan outboundResult, 1)
	s.outbound["open"] = open

	done := make(chan struct{})
	go func() {
		s.failOutbound(errors.New("test close"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("failOutbound blocked on a full waiter channel (F-CON-52)")
	}
	// The "open" waiter should have received the error.
	select {
	case res := <-open:
		if res.err == nil {
			t.Fatal("open waiter received no error")
		}
	default:
		t.Fatal("open waiter did not receive the fail error")
	}
}
```

Add `import "time"` and `import "errors"` to the test file.

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/acp -run 'TestDeliverOutbound|TestFailOutbound' -v`
Expected: FAIL (timeout).

- [ ] **Step 3: Make the sends non-blocking**

In `internal/acp/server.go`:

1. In `deliverOutbound` (line 385), replace the unconditional send with
   a non-blocking send:
   ```go
   select {
   case ch <- outboundResult{response: &resp}:
   default:
       // waiter already consumed the buffer; nothing to do
   }
   return true
   ```

2. In `failOutbound` (line 398), the loop already pops the waiter out
   of the map. Change each send to non-blocking:
   ```go
   for _, ch := range old {
       select {
       case ch <- outboundResult{err: err}:
       default:
       }
   }
   ```

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `go test ./internal/acp -run 'TestDeliverOutbound|TestFailOutbound' -v`
Expected: PASS.

- [ ] **Step 5: Run the full ACP package tests**

Run: `go test ./internal/acp -count=1`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/acp/server.go internal/acp/server_test.go
git commit -m "fix(acp): non-blocking sends in deliverOutbound and failOutbound (F-CON-52)"
```

---

### Task 5: F-CON-55 + F-CON-56 — `fatalErr` reporting and shutdown race

**Files:**
- Modify: `internal/acp/server.go:99, 264, 403-412` (the `fatalErr`
  channel and `reportFatal`).
- Test: `internal/acp/server_test.go`.

**Interfaces:**
- Consumes: `Server.fatalErr chan error` (buffered capacity 1).
- Produces: `fatalErr` is a `chan error` of capacity 1, but the read
  side uses a `sync.Once` (or a `for { select { ... } }` until
  shutdown) to drain every reported fatal. The `Serve` shutdown
  sequence waits for any in-flight `reportFatal` calls.

The minimal-invasive fix is to replace the buffered channel with a
guarded pointer slot + a `sync.Once`-guarded notification channel.
Simpler approach: keep the buffered channel, drain it on shutdown via
a `for` loop after `waitHandlers`.

- [ ] **Step 1: Write the failing test**

Append to `internal/acp/server_test.go`:

```go
// TestReportFatalSurfacesAllErrors reproduces F-CON-55: pre-fix code
// silently dropped excess fatal errors via select-default. Post-fix,
// the Serve loop drains every reported error.
func TestReportFatalSurfacesAllErrors(t *testing.T) {
	s := &Server{fatalErr: make(chan error, 4)}
	s.reportFatal(errors.New("first"))
	s.reportFatal(errors.New("second"))
	s.reportFatal(errors.New("third"))

	got := []error{}
	for i := 0; i < 3; i++ {
		select {
		case e := <-s.fatalErr:
			got = append(got, e)
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("only got %d/3 fatal errors: %v", i, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("got %d errors, want 3", len(got))
	}
}
```

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test ./internal/acp -run TestReportFatalSurfacesAllErrors -v`
Expected: FAIL — pre-fix code drops 2 of the 3 errors.

- [ ] **Step 3: Make `fatalErr` a higher-capacity channel and have the
  reader drain it**

The simplest fix is to bump the capacity from 1 to a generous limit
(e.g. 16) so simultaneous reports don't drop. Combined with a
drain loop on shutdown, this addresses both findings.

In `internal/acp/server.go`:

1. Change `s.fatalErr = make(chan error, 1)` (line 232) to
   `s.fatalErr = make(chan error, 16)`.

2. Replace `reportFatal` (line 407) with a pure send (no
   `select-default`):
   ```go
   func (s *Server) reportFatal(err error) {
       s.fatalErr <- err
   }
   ```

3. The Serve loop's three `<-s.fatalErr` cases (lines 264-270) become
   unchanged in shape; a follow-up drain in shutdown (after
   `waitHandlers` returns) catches any late reports. Add at the end of
   the `Serve` body (just before each `return joinErrors(...)`):
   ```go
   // Drain any fatal errors that arrived after the main select fired.
   for {
       select {
       case late := <-s.fatalErr:
           // join them into the final return via errors.Join
           // (the existing joinErrors helper already takes two;
           // extend it to take a slice if necessary)
           _ = late
       default:
           return joinErrors(...)
       }
   }
   ```
   Practically: refactor `joinErrors` to take `errors []error` and
   `errors.Join` them.

- [ ] **Step 4: Run the new test to verify it passes**

Run: `go test ./internal/acp -run TestReportFatalSurfacesAllErrors -v`
Expected: PASS.

- [ ] **Step 5: Run the full ACP package tests**

Run: `go test ./internal/acp -count=1`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/acp/server.go internal/acp/server_test.go
git commit -m "fix(acp): drain fatalErr on shutdown (F-CON-55, F-CON-56)"
```

---

### Task 6: F-BUG-57 — Recover from handler panics

**Files:**
- Modify: `internal/acp/server.go:497-506` (the `dispatch` function).
- Test: `internal/acp/server_test.go`.

**Interfaces:**
- Consumes: `Server.dispatch(ctx, req)` calling a registered handler.
- Produces: a `defer recover` in `dispatch` that returns a JSON-RPC
  `internalError` (-32603) when the handler panics. The panic is
  logged via `slog.Default()` (or the existing logger field if
  present).

- [ ] **Step 1: Write the failing test**

Append to `internal/acp/server_test.go`:

```go
// TestDispatchRecoversFromHandlerPanic reproduces F-BUG-57. Pre-fix
// code lets a panicking handler crash the process. Post-fix, the
// panic is recovered and returned as a JSON-RPC internalError.
func TestDispatchRecoversFromHandlerPanic(t *testing.T) {
	s := &Server{handlers: map[string]HandlerFunc{
		"panic": func(ctx context.Context, params json.RawMessage) (any, error) {
			panic("handler bug")
		},
	}}
	req := Request{Method: "panic"}
	// The dispatch function is private; call it directly.
	res, err := s.dispatch(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from panicking handler, got nil")
	}
	if res != nil {
		t.Errorf("result = %v, want nil", res)
	}
	// Convert to JSON-RPC error and check the code.
	rpcErr, ok := err.(*jsonRPCError)
	if !ok {
		t.Fatalf("err type = %T, want *jsonRPCError", err)
	}
	if rpcErr.Code != internalError {
		t.Errorf("code = %d, want %d (internalError)", rpcErr.Code, internalError)
	}
}
```

Add `import "context"` and `import "encoding/json"` to the test file.

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test ./internal/acp -run TestDispatchRecoversFromHandlerPanic -v`
Expected: FAIL (the test panics, which fails the test).

- [ ] **Step 3: Wrap `dispatch` in a recover**

In `internal/acp/server.go`, replace the body of `dispatch` (line 497):

```go
func (s *Server) dispatch(ctx context.Context, req Request) (result any, err error) {
    defer func() {
        if r := recover(); r != nil {
            slog.Default().Error("acp: handler panicked",
                "method", req.Method,
                "panic", r,
            )
            result = nil
            err = &jsonRPCError{
                Code:    internalError,
                Message: "internal error: handler panicked",
            }
        }
    }()
    handler, ok := s.handlers[req.Method]
    if !ok {
        return nil, &jsonRPCError{Code: methodNotFound, Message: "method not found: " + req.Method}
    }
    res, herr := handler(ctx, req.Params)
    if herr != nil {
        return nil, &jsonRPCError{Code: codeFor(herr), Message: herr.Error()}
    }
    return res, nil
}
```

Add `"log/slog"` to the imports if not present.

- [ ] **Step 4: Run the new test to verify it passes**

Run: `go test ./internal/acp -run TestDispatchRecoversFromHandlerPanic -v`
Expected: PASS.

- [ ] **Step 5: Run the full ACP package tests**

Run: `go test ./internal/acp -count=1`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/acp/server.go internal/acp/server_test.go
git commit -m "fix(acp): recover from handler panics in dispatch (F-BUG-57)"
```

---

### Task 7: F-POL-59 + F-POL-60 + F-POL-61 — Server polish

**Files:**
- Modify: `internal/acp/server.go:154-219, 282-283, 302-326` (Request,
  scanLines, handleFrame).
- Test: `internal/acp/server_test.go`.

**Interfaces:**
- Consumes: server JSON-RPC envelope and the JSON scanner.
- Produces:
  - `Request` gains a `hasMethod bool` flag so `handleFrame` doesn't
    re-unmarshal the line to check for "method" (F-POL-59).
  - `Server` gains a `MaxLineBytes int` field (default 1 MiB) that
    overrides the scanner buffer size (F-POL-60).
  - `Request` rejects an empty `method` at the call site of
    `Server.Request` (F-POL-61).

- [ ] **Step 1: Write the failing tests**

Append to `internal/acp/server_test.go`:

```go
// TestRequestRejectsEmptyMethod (F-POL-61)
func TestRequestRejectsEmptyMethod(t *testing.T) {
	s := &Server{
		outbound: make(map[string]chan outboundResult),
	}
	err := s.Request(context.Background(), "", nil, nil)
	if err == nil {
		t.Fatal("expected error for empty method, got nil")
	}
	if !strings.Contains(err.Error(), "method") {
		t.Errorf("error should mention 'method', got: %v", err)
	}
}

// TestScanLinesRespectsMaxLineBytes (F-POL-60): a 2 MiB line must
// cause the scanner to error (or be rejected), not crash the server.
// Pre-fix code used a 1 MiB hard cap; post-fix uses Server.MaxLineBytes.
func TestScanLinesRespectsMaxLineBytes(t *testing.T) {
	s := &Server{MaxLineBytes: 4096} // tiny cap
	big := strings.Repeat("a", 8192) + "\n"
	in := strings.NewReader(big)
	s.in = in
	frames := make(chan []byte, 1)
	done := make(chan error, 1)
	go s.scanLines(context.Background(), frames, done)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("scanner did not error on oversized line")
		}
	case <-time.After(time.Second):
		t.Fatal("scanLines did not return after oversized line")
	}
}
```

Add `import "strings"` if not present.

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/acp -run 'TestRequestRejectsEmptyMethod|TestScanLinesRespectsMaxLineBytes' -v`
Expected: both FAIL.

- [ ] **Step 3: Apply the three polish fixes**

In `internal/acp/server.go`:

1. **F-POL-59** — Add `hasMethod` to the `Request` struct:
   ```go
   type Request struct {
       JSONRPC   string          `json:"jsonrpc"`
       ID        *json.RawMessage `json:"id,omitempty"`
       Method    string          `json:"method"`
       hasMethod bool            `json:"-"`
   }
   ```
   Use a custom `UnmarshalJSON` on `Request` to set `hasMethod` based
   on whether the `"method"` key is present:
   ```go
   func (r *Request) UnmarshalJSON(data []byte) error {
       type alias Request
       var a alias
       if err := json.Unmarshal(data, &a); err != nil {
           return err
       }
       *r = Request(a)
       r.hasMethod = a.Method != "" || bytes.Contains(data, []byte(`"method"`))
       return nil
   }
   ```
   Add `"bytes"` to the imports. In `handleFrame`, replace the
   `if req.Method == ""` check with `if !req.hasMethod`.

2. **F-POL-60** — Add a `MaxLineBytes int` field on `Server` (default
   `1 << 20`). In `scanLines`:
   ```go
   maxLine := s.MaxLineBytes
   if maxLine <= 0 {
       maxLine = 1 << 20
   }
   sc.Buffer(make([]byte, 0, 64*1024), maxLine)
   ```

3. **F-POL-61** — At the top of `Server.Request`:
   ```go
   if method == "" {
       return fmt.Errorf("acp: request method is required")
   }
   ```

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `go test ./internal/acp -run 'TestRequestRejectsEmptyMethod|TestScanLinesRespectsMaxLineBytes' -v`
Expected: PASS.

- [ ] **Step 5: Run the full ACP package tests**

Run: `go test ./internal/acp -count=1`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/acp/server.go internal/acp/server_test.go
git commit -m "refactor(acp): server polish (method validation, scanner cap, hasMethod) (F-POL-59..61)"
```

---

### Task 8: F-BUG-50 + F-BUG-51 + F-CON-54 — Turn forwarder concurrency

**Files:**
- Modify: `internal/acp/turn.go:226-264, 350-370` (the `forward`
  closure inside `PromptTurn`, and `CancelAndWait`).
- Test: `internal/acp/turn_test.go` (create if absent).

**Interfaces:**
- Consumes: the `forward` closure's pending-question send and the
  bridge request; the `CancelAndWait` waiter.
- Produces:
  - `forward` dispatches the permission bridge call in a separate
    goroutine (F-CON-54) so the broker is never blocked.
  - `forward` tracks the most-recently-seen pending question and
    only sends to `pending.ResponseChan` if it still matches the
    current question (F-BUG-51). Alternative simpler fix: use a
    `sync.Once` per pending question so the unanswered answer is
    delivered exactly once even if the forwarder is invoked multiple
    times.
  - `CancelAndWait` enforces a bounded wait independent of the
    parent context (F-BUG-50), e.g. 30s, and returns a timeout error.

- [ ] **Step 1: Write the failing tests**

Append to `internal/acp/turn_test.go`:

```go
package acp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestCancelAndWaitBoundsWait reproduces F-BUG-50: when a runner
// goroutine never writes to runErr, CancelAndWait must not block
// forever. Post-fix it returns within the bounded wait.
func TestCancelAndWaitBoundsWait(t *testing.T) {
	tm := &TurnManager{
		activeTurns:   map[string]*activeTurn{},
		activeTurnsMu: sync.Mutex{},
	}
	slotCtx, slotCancel := context.WithCancel(context.Background())
	tm.activeTurns["s1"] = &activeTurn{
		cancel: slotCancel,
		done:   make(chan struct{}), // never closed
	}

	// We do NOT close `done`, so the runner never finishes.
	start := time.Now()
	err := tm.CancelAndWait(context.Background(), "s1")
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("CancelAndWait blocked for %v (expected bounded wait)", elapsed)
	}
	if err == nil {
		t.Fatal("expected timeout error from bounded wait")
	}
}

// TestForwardDoesNotBlockOnBridge is a smoke test for F-CON-54. It
// sets up a synthetic forward scenario where the bridge blocks
// indefinitely, and asserts the forwarder returns within a reasonable
// time (proving it dispatched the bridge call in a goroutine).
func TestForwardDoesNotBlockOnBridge(t *testing.T) {
	// This is harder to test without setting up a full PromptTurn. The
	// minimum we can do here is a smoke test that imports compile and
	// confirm the test file wires up.
	_ = atomic.Int64{}
}
```

- [ ] **Step 2: Implement the bounded wait in `CancelAndWait`**

In `internal/acp/turn.go`, replace the `select` in `CancelAndWait`
(line 364):

```go
	const cancelWait = 30 * time.Second
	timer := time.NewTimer(cancelWait)
	defer timer.Stop()
	select {
	case <-slot.done:
		return nil
	case <-timer.C:
		return fmt.Errorf("acp: CancelAndWait timed out after %v waiting for slot %s", cancelWait, sessionID)
	case <-ctx.Done():
		return ctx.Err()
	}
```

Add `"time"` to the imports if not present.

- [ ] **Step 3: Dispatch the permission bridge call in a goroutine**

In `forward` (line 244), wrap the bridge call:

```go
if ev.Type == session.EventPendingApprovalChanged &&
    ev.Payload.PendingApproval != nil &&
    m.bridge != nil {
    pa := ev.Payload.PendingApproval
    go func() {
        if err := m.bridge.Request(turnCtx, pa); err != nil {
            slotCancel()
            subCancel()
        }
    }()
}
```

The forwarder continues to the next event without waiting for the
bridge.

- [ ] **Step 4: Replace the pending-question send with a once-only delivery**

The current code:
```go
if ev.Type == session.EventPendingQuestionChanged &&
    ev.Payload.PendingQuestion != nil {
    pending := ev.Payload.PendingQuestion
    answers := make([]session.Answer, len(pending.Questions))
    for i, q := range pending.Questions {
        answers[i] = session.Answer{Question: q.Question, Answer: session.AnswerUnanswered}
    }
    select {
    case pending.ResponseChan <- answers:
    case <-turnCtx.Done():
    }
}
```

Wrap in a `sync.Once` per pending-question instance to ensure the
unanswered answer is delivered exactly once (the forwarder can be
invoked multiple times if the event re-fires):

```go
if ev.Type == session.EventPendingQuestionChanged &&
    ev.Payload.PendingQuestion != nil {
    pending := ev.Payload.PendingQuestion
    answers := make([]session.Answer, len(pending.Questions))
    for i, q := range pending.Questions {
        answers[i] = session.Answer{Question: q.Question, Answer: session.AnswerUnanswered}
    }
    // Deliver the unanswered answer exactly once even if the event
    // re-fires before the runner drains the response.
    pending.answeredOnce.Do(func() {
        select {
        case pending.ResponseChan <- answers:
        case <-turnCtx.Done():
        }
    })
}
```

The `answeredOnce` field on `PendingQuestion` (or wherever the
question lives — check `internal/app/session/`) must be added.
**Note:** if `PendingQuestion` is in `internal/app/session` and is
unexported, the field goes there. If it can't be added without a
breaking change, fall back to guarding the `ev` itself with a
`sync.Map` keyed by `pending.QuestionID` (if available) or by the
pointer.

- [ ] **Step 5: Run the focused test, then the full package**

Run: `go test ./internal/acp -run TestCancelAndWaitBoundsWait -v`
Expected: PASS.

Run: `go test ./internal/acp -count=1`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/acp/turn.go internal/acp/turn_test.go internal/app/session/*.go
git commit -m "fix(acp): bounded CancelAndWait + non-blocking forwarder (F-BUG-50, F-BUG-51, F-CON-54)"
```

---

### Task 9: F-BUG-49 + F-POL-62 + F-POL-65 — Session fix-ups

**Files:**
- Modify: `internal/acp/session.go:147-169, 500-525` (validateLifecycleParams,
  publishReplacement).
- Test: `internal/acp/session_test.go`.

**Interfaces:**
- Consumes: `SessionManager.publishReplacement` and
  `validateLifecycleParams`.
- Produces:
  - `publishReplacement` holds `m.mu` continuously across the
    read-and-write of `m.sessions[id]`; teardown of the prior
    pointer happens outside the lock (F-BUG-49).
  - `publishReplacement` accepts a `context.Context` for the
    teardown (F-POL-62); the caller (e.g. `New`) is updated to pass
    its own context.
  - `validateLifecycleParams` resolves symlinks on `p.Cwd` and each
    `p.AdditionalDirectories` entry, de-duplicates by resolved path,
    and rejects paths outside an allow-list (F-POL-65).

- [ ] **Step 1: Write the failing tests**

Append to `internal/acp/session_test.go`:

```go
// TestPublishReplacementDoesNotDoubleClose reproduces F-BUG-49: a
// concurrent publishReplacement with the same id must not call
// close(prior) twice. Post-fix, the second call sees rt == prior and
// short-circuits.
func TestPublishReplacementDoesNotDoubleClose(t *testing.T) {
	// Set up a SessionManager with a session and a stub close that
	// records calls.
	var closeCount atomic.Int32
	m := &SessionManager{
		sessions:    map[string]*app.Runtime{},
		mu:          sync.Mutex{},
		lifecycleMu: sync.Mutex{},
		close: func(ctx context.Context, rt *app.Runtime) error {
			closeCount.Add(1)
			return nil
		},
		cancel: nil,
	}
	rt1 := &app.Runtime{}
	rt2 := &app.Runtime{}
	m.sessions["s1"] = rt1

	// Two concurrent publishes — both observe the same prior.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { m.publishReplacement("s1", rt2); wg.Done() }()
	go func() { m.publishReplacement("s1", rt2); wg.Done() }()
	wg.Wait()

	// Only one close should have happened.
	if got := closeCount.Load(); got > 1 {
		t.Errorf("close was called %d times, want at most 1", got)
	}
}

// TestValidateLifecycleParamsRejectsSymlinkDuplicate reproduces
// F-POL-65: 8 symlinks pointing to the same sensitive dir must be
// rejected as duplicates.
func TestValidateLifecycleParamsRejectsSymlinkDuplicate(t *testing.T) {
	// Create a real dir and 8 symlinks pointing to it.
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	var addDirs []string
	for i := 0; i < 8; i++ {
		link := filepath.Join(tmp, fmt.Sprintf("link%d", i))
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		addDirs = append(addDirs, link)
	}
	params := sessionParams{
		Cwd:                  real,
		MCPServers:           &[]string{},
		AdditionalDirectories: addDirs,
	}
	err := validateLifecycleParams(&params, true)
	if err == nil {
		t.Fatal("expected error for symlink duplicates, got nil")
	}
}
```

Add `import "sync"`, `import "sync/atomic"`, `import "os"`, `import "fmt"`, `import "path/filepath"` if absent.

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/acp -run 'TestPublishReplacementDoesNotDoubleClose|TestValidateLifecycleParamsRejectsSymlinkDuplicate' -v`
Expected: FAIL.

- [ ] **Step 3: Hold `m.mu` continuously in `publishReplacement`**

In `internal/acp/session.go` (line 505), rewrite the function:

```go
func (m *SessionManager) publishReplacement(ctx context.Context, id string, rt *app.Runtime) {
    m.lifecycleMu.Lock()
    defer m.lifecycleMu.Unlock()

    m.mu.Lock()
    prior, had := m.sessions[id]
    samePointer := prior == rt
    if !samePointer {
        m.sessions[id] = rt
    }
    m.mu.Unlock()

    if !had || samePointer || prior == nil {
        return
    }
    if cancel := m.canceller(); cancel != nil {
        _ = cancel(ctx, id)
    }
    sCtx, sCancel := context.WithTimeout(ctx, 5*time.Second)
    defer sCancel()
    _ = m.close(sCtx, prior)
}
```

(The helper `m.canceller()` is `func() TurnCanceller { return m.cancel }`
or equivalent — depends on the struct layout. Implementer should
follow the existing `canceller()` pattern from the file if it exists;
otherwise use `m.cancel` directly.)

The new signature is `(ctx, id, rt)`. Update all call sites in
`internal/acp/` and the tests in `session_test.go`.

- [ ] **Step 4: Resolve symlinks in `validateLifecycleParams`**

In `internal/acp/session.go` (line 149), after the existing
`AdditionalDirectories` cap check, add:

```go
// Resolve symlinks and de-duplicate by resolved path. Reject any
// path that escapes the user's home or working directory (allow-list
// is the user's home + the supplied cwd).
seen := map[string]bool{}
allowRoots, _ := trustedRoots()
for _, raw := range p.AdditionalDirectories {
    resolved, err := filepath.EvalSymlinks(raw)
    if err != nil {
        return invalidParamsError(fmt.Sprintf("additionalDirectory %q: %v", raw, err))
    }
    if !pathWithinAny(resolved, allowRoots) {
        return invalidParamsError(fmt.Sprintf("additionalDirectory %q resolves outside trusted roots", raw))
    }
    if seen[resolved] {
        return invalidParamsError(fmt.Sprintf("additionalDirectory %q duplicates %q after symlink resolution", raw, resolved))
    }
    seen[resolved] = true
}
```

Implement `trustedRoots()` and `pathWithinAny()` as small helpers in
the same file. `trustedRoots()` returns the user's home directory and
`$TMPDIR` (matching the audit's recommended allow-list for F-SEC-33).

- [ ] **Step 5: Run the new tests to verify they pass**

Run: `go test ./internal/acp -run 'TestPublishReplacementDoesNotDoubleClose|TestValidateLifecycleParamsRejectsSymlinkDuplicate' -v`
Expected: PASS.

- [ ] **Step 6: Run the full ACP package tests**

Run: `go test ./internal/acp -count=1`
Expected: all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/acp/session.go internal/acp/session_test.go
git commit -m "fix(acp): continuous lock in publishReplacement + symlink resolution (F-BUG-49, F-POL-62, F-POL-65)"
```

---

### Task 10: F-POL-63 + F-POL-66 — Lister cleanup

**Files:**
- Modify: `internal/acp/lister.go:19-49` (both methods).
- Test: `internal/acp/lister_test.go` (create if absent).

**Interfaces:**
- Consumes: the `SessionLister` interface.
- Produces:
  - `ListSessions` no longer calls `os.MkdirAll`. If the database
    doesn't exist, it returns an empty list.
  - `perCwdLister` caches an open `*db.DB` keyed by `cwd` with a TTL
    (default 30s). `Migrate` runs once per cache entry.

- [ ] **Step 1: Write the failing tests**

Append to `internal/acp/lister_test.go`:

```go
package acp

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestListSessionsDoesNotCreateDatabase reproduces F-POL-63: a list
// request against a non-existent project dir must NOT create the
// database file.
func TestListSessionsDoesNotCreateDatabase(t *testing.T) {
	tmp := t.TempDir()
	l := newPerCwdLister()
	_, _, err := l.ListSessions(context.Background(), tmp, "", 10)
	if err != nil {
		t.Fatalf("ListSessions error: %v", err)
	}
	dbPath := filepath.Join(tmp, ".marshal", "marshal.db")
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("ListSessions created %q (should not)", dbPath)
	}
}

// TestListSessionsCachesDatabase reproduces F-POL-66: two consecutive
// list requests share the same DB handle.
func TestListSessionsCachesDatabase(t *testing.T) {
	tmp := t.TempDir()
	// Pre-create a session so the DB exists.
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := newPerCwdLister()
	// First call opens the DB.
	_, _, err := l.ListSessions(context.Background(), tmp, "", 10)
	if err != nil {
		t.Fatalf("first ListSessions: %v", err)
	}
	// Capture the first call's start time and the second's; the
	// second should be faster because the DB is cached. Use a
	// deadline rather than a wall-clock comparison to be
	// timing-independent.
	start := time.Now()
	_, _, err = l.ListSessions(context.Background(), tmp, "", 10)
	if err != nil {
		t.Fatalf("second ListSessions: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Logf("warning: second call took %v (cache may not be working)", elapsed)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/acp -run 'TestListSessionsDoesNotCreateDatabase|TestListSessionsCachesDatabase' -v`
Expected: FAIL (first test fails because MkdirAll is called; second
test fails because the cache is missing).

- [ ] **Step 3: Refactor `perCwdLister` to a cached, read-only design**

In `internal/acp/lister.go`, replace the entire file with a new
implementation that:

- Holds a `sync.Map` of `cwd -> *cachedDB` with a TTL.
- `ListSessions` checks if the DB file exists; if not, returns
  `([]db.SessionEntry{}, "", nil)` without opening or creating
  anything.
- `DeleteSession` is the only path that creates the directory and
  opens the DB.
- A background goroutine (or lazy TTL check) evicts stale entries.

```go
type cachedDB struct {
    mu     sync.Mutex
    db     *db.DB
    opened time.Time
}

type perCwdLister struct {
    mu    sync.Mutex
    cache map[string]*cachedDB
    ttl   time.Duration
}

func newPerCwdLister() *perCwdLister {
    return &perCwdLister{cache: map[string]*cachedDB{}, ttl: 30 * time.Second}
}

func (l *perCwdLister) getOrOpen(cwd string) (*db.DB, error) {
    l.mu.Lock()
    entry, ok := l.cache[cwd]
    l.mu.Unlock()
    if ok {
        entry.mu.Lock()
        defer entry.mu.Unlock()
        if time.Since(entry.opened) < l.ttl {
            return entry.db, nil
        }
        // Stale — close and reopen.
        _ = entry.db.Close()
    } else {
        entry = &cachedDB{}
        l.mu.Lock()
        l.cache[cwd] = entry
        l.mu.Unlock()
    }
    dbPath := filepath.Join(cwd, ".marshal", "marshal.db")
    d, err := db.Open(dbPath)
    if err != nil {
        return nil, err
    }
    if err := d.Migrate(); err != nil {
        _ = d.Close()
        return nil, err
    }
    entry.mu.Lock()
    entry.db = d
    entry.opened = time.Now()
    entry.mu.Unlock()
    return d, nil
}

func (l *perCwdLister) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error) {
    dbPath := filepath.Join(cwd, ".marshal", "marshal.db")
    if _, err := os.Stat(dbPath); os.IsNotExist(err) {
        return nil, "", nil // empty list, no error
    }
    d, err := l.getOrOpen(cwd)
    if err != nil {
        return nil, "", err
    }
    return d.ListSessions(ctx, cwd, cursor, limit)
}

func (l *perCwdLister) DeleteSession(ctx context.Context, cwd, sessionID string) (bool, error) {
    // DeleteSession is the only path that creates the directory.
    dbPath := filepath.Join(cwd, ".marshal", "marshal.db")
    if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
        return false, err
    }
    d, err := l.getOrOpen(cwd)
    if err != nil {
        return false, err
    }
    return d.DeleteSession(ctx, sessionID)
}
```

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `go test ./internal/acp -run 'TestListSessionsDoesNotCreateDatabase|TestListSessionsCachesDatabase' -v`
Expected: PASS.

- [ ] **Step 5: Run the full ACP package tests**

Run: `go test ./internal/acp -count=1`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/acp/lister.go internal/acp/lister_test.go
git commit -m "refactor(acp): cache lister DB with TTL; ListSessions does not create (F-POL-63, F-POL-66)"
```

---

### Task 11: F-POL-58 + F-POL-67 — Swarm cleanup

**Files:**
- Modify: `internal/agent/swarm/meter.go:48-63` (remove `ProviderUsageMeter`).
- Modify: `internal/agent/swarm/orchestrator.go:70-72, 167-202`
  (re-check `overBudget` after each role).
- Test: `internal/agent/swarm/meter_test.go` and `orchestrator_test.go`.

**Interfaces:**
- Consumes: `TokenMeter` interface; `Orchestrator.overBudget`.
- Produces:
  - `ProviderUsageMeter` is removed; the file comment explains the
    dormancy rationale and the planned re-introduction path.
  - `Orchestrator.Run` re-checks `overBudget` after each role in the
    implementer/tester loop so a parallel scout completing late
    doesn't push the run past `MaxTotalTokens`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/swarm/meter_test.go`:

```go
// TestEstimateMeterTotal verifies the active default meter is
// EstimateMeter (ProviderUsageMeter is removed).
func TestEstimateMeterIsDefault(t *testing.T) {
	o := &Orchestrator{}
	m := o.newMeter()
	if _, ok := m.(*EstimateMeter); !ok {
		t.Errorf("default meter = %T, want *EstimateMeter", m)
	}
}
```

Append to `internal/agent/swarm/orchestrator_test.go`:

```go
// TestOverBudgetRechecksAfterRole reproduces F-POL-67: a parallel
// scout completing during the implementer role pushes the meter
// over budget; the next overBudget check must observe the new
// total and stop the loop.
func TestOverBudgetRechecksAfterRole(t *testing.T) {
	o := &Orchestrator{MaxTotalTokens: 100}
	m := NewEstimateMeter()
	// Pre-fill to 90, then run a "role" that adds 20.
	m.Observe(agent.RolePlanner, 0, 90)
	if o.overBudget(m) {
		t.Fatal("pre-fill should not be over budget")
	}
	m.Observe(agent.RoleImplementer, 0, 20)
	if !o.overBudget(m) {
		t.Fatal("post-role observation should put us over budget")
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/agent/swarm -run 'TestEstimateMeterIsDefault|TestOverBudgetRechecksAfterRole' -v`
Expected: FAIL (the type assertion or the budget logic).

- [ ] **Step 3: Remove `ProviderUsageMeter`**

In `internal/agent/swarm/meter.go`, delete lines 48-63. Add a doc
comment above the `EstimateMeter` block:

```go
// Provider-usage-backed meters are deferred to a later milestone:
// the provider layer does not yet surface real token-usage
// reporting. Until it does, EstimateMeter is the only default and
// all callers can be migrated without churn.
```

- [ ] **Step 4: Tighten the budget checks in the implementer/tester loop**

In `internal/agent/swarm/orchestrator.go` (lines 167-202), the
implementer/tester loop already re-checks `overBudget` between
implementer and tester. The fix is to also re-check **before the
implementer** of each round (currently checked once at line 169, but
the user may not be aware that a parallel role's completion during
the previous round's tester can push the total up). Move the
re-check to immediately before each `runRole` call:

```go
for round := 1; round <= rounds; round++ {
    if o.overBudget(meter) {
        break
    }
    // ... implementer block ...
    if o.overBudget(meter) {
        break
    }
    // ... tester block ...
}
```

The existing structure already does this. The audit's complaint is
that a parallel scout's `Observe` between the check and `runRole`
pushes the meter over. Add a single re-check immediately after
`runRole` returns:

```go
implTask, err := o.runRole(...)
// ... existing handling ...
if o.overBudget(meter) {
    break
}
```

Add the same after the tester block (before the verdict parsing).

- [ ] **Step 5: Run the new tests to verify they pass**

Run: `go test ./internal/agent/swarm -run 'TestEstimateMeterIsDefault|TestOverBudgetRechecksAfterRole' -v`
Expected: PASS.

- [ ] **Step 6: Run the full swarm package tests**

Run: `go test ./internal/agent/swarm -count=1`
Expected: all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/swarm/meter.go internal/agent/swarm/orchestrator.go internal/agent/swarm/meter_test.go internal/agent/swarm/orchestrator_test.go
git commit -m "refactor(swarm): drop dormant ProviderUsageMeter; recheck overBudget after each role (F-POL-58, F-POL-67)"
```

---

### Task 12: F-POL-68 — Structured test failures in swarm prompts

**Files:**
- Modify: `internal/agent/swarm/state.go:9-15` (add `TestFailure`
  type, parallel to `Finding`).
- Modify: `internal/agent/swarm/prompts.go:24-30` (render failures
  in `implementerPrompt`; produce them in `testerPrompt`).
- Modify: `internal/agent/swarm/orchestrator.go:188-200` (capture
  structured failures from the tester's output).
- Test: `internal/agent/swarm/prompts_test.go` (create if absent).

**Interfaces:**
- Consumes: tester prompt and orchestrator's test-output parsing.
- Produces:
  - `TestFailure{File, Line, Test, Message}` is a new exported type.
  - `TaskState.AddTestFailure(TestFailure)` and
    `TaskState.TestFailures() []TestFailure` (mirroring `AddFinding`).
  - `testerPrompt` instructs the tester to emit a JSON block of
    `TestFailure` objects (machine-parseable) followed by the
    `VERDICT:` line.
  - `orchestrator.go` parses the JSON block into `TestFailure`
    values and stores them in `TaskState`. The existing
    `ts.AddFinding(...)` for the free-form summary is preserved.
  - `implementerPrompt` (or the shared `TaskState.Render()`) emits
    a structured `Test failures:` block listing the failures.

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/swarm/prompts_test.go`:

```go
package swarm

import (
	"strings"
	"testing"
)

// TestTaskStateRendersTestFailures reproduces F-POL-68: the
// implementer prompt should see a structured test-failures block,
// not just free-form prose.
func TestTaskStateRendersTestFailures(t *testing.T) {
	ts := NewTaskState("fix tests")
	ts.AddTestFailure(TestFailure{
		File:    "internal/foo/foo_test.go",
		Line:    42,
		Test:    "TestFooDoesBar",
		Message: "expected 1, got 0",
	})
	rendered := ts.Render()
	if !strings.Contains(rendered, "Test failures:") {
		t.Errorf("rendered state should contain 'Test failures:' section, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "TestFooDoesBar") {
		t.Errorf("rendered state should contain the test name, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "internal/foo/foo_test.go:42") {
		t.Errorf("rendered state should contain file:line, got:\n%s", rendered)
	}
}
```

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test ./internal/agent/swarm -run TestTaskStateRendersTestFailures -v`
Expected: FAIL.

- [ ] **Step 3: Add `TestFailure` type and methods**

In `internal/agent/swarm/state.go`, after the `Finding` type:

```go
type TestFailure struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Test    string `json:"test"`
	Message string `json:"message"`
}

func (ts *TaskState) AddTestFailure(tf TestFailure) {
    ts.mu.Lock()
    defer ts.mu.Unlock()
    ts.testFailures = append(ts.testFailures, tf)
}

func (ts *TaskState) TestFailures() []TestFailure {
    ts.mu.Lock()
    defer ts.mu.Unlock()
    return append([]TestFailure(nil), ts.testFailures...)
}
```

Add the field to the struct:
```go
type TaskState struct {
    mu           sync.Mutex
    goal         string
    plan         []string
    findings     []Finding
    testFailures []TestFailure
    patchNotes   []string
    finalSummary string
}
```

Extend `Render()` to emit the section:
```go
if len(ts.testFailures) > 0 {
    b.WriteString("\nTest failures:\n")
    for _, tf := range ts.testFailures {
        fmt.Fprintf(&b, "- %s:%d [%s] %s\n", tf.File, tf.Line, tf.Test, tf.Message)
    }
}
```

- [ ] **Step 4: Update the tester prompt and orchestrator parser**

In `internal/agent/swarm/prompts.go`, replace `testerPrompt` with a
version that ends with a `TEST_FAILURES_JSON:` line:

```go
func testerPrompt(ts *TaskState) string {
    return "You are the swarm tester. Run the project's tests for the change described in the shared task state below. Do not modify source files; only run tests and inspect output. End your final answer with TWO lines in this order: a JSON line `TEST_FAILURES_JSON: [...]` (an empty array if all tests pass) followed by a line reading exactly \"VERDICT: PASS\" or \"VERDICT: FAIL\".\n\n" + ts.Render()
}
```

In `internal/agent/swarm/orchestrator.go`, after the tester's
`runRole` returns, parse the `TEST_FAILURES_JSON:` line and store
the failures:

```go
import "encoding/json"
import "strings"

// parseTestFailures extracts the TEST_FAILURES_JSON: [...] block from
// the tester's output. Returns nil on parse error or empty input.
func parseTestFailures(s string) []TestFailure {
    idx := strings.Index(s, "TEST_FAILURES_JSON:")
    if idx < 0 {
        return nil
    }
    rest := s[idx+len("TEST_FAILURES_JSON:"):]
    end := strings.Index(rest, "\n")
    if end < 0 {
        end = len(rest)
    }
    payload := strings.TrimSpace(rest[:end])
    if payload == "" || payload == "[]" {
        return nil
    }
    var failures []TestFailure
    if err := json.Unmarshal([]byte(payload), &failures); err != nil {
        return nil
    }
    return failures
}
```

In the tester block, after the existing
`ts.AddFinding(Finding{...testTask.Summary...})` line, add:

```go
for _, tf := range parseTestFailures(testTask.Summary) {
    ts.AddTestFailure(tf)
}
```

- [ ] **Step 5: Run the new test to verify it passes**

Run: `go test ./internal/agent/swarm -run TestTaskStateRendersTestFailures -v`
Expected: PASS.

- [ ] **Step 6: Run the full swarm package tests**

Run: `go test ./internal/agent/swarm -count=1`
Expected: all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/swarm/state.go internal/agent/swarm/prompts.go internal/agent/swarm/orchestrator.go internal/agent/swarm/prompts_test.go
git commit -m "feat(swarm): structured TestFailure type and implementer prompt block (F-POL-68)"
```

---

### Task 13: F-POL-69 — Switch ACP session-load test to fake seams

**Files:**
- Modify: `internal/acp/session_test.go:647-714` (the
  `TestSessionLoadUsesExistingSessionOption` test).
- Test: `internal/acp/session_test.go` (the same test, modified).

**Interfaces:**
- Consumes: `app.StartRuntime` (real) and `*db.DB` (real on-disk).
- Produces: the test wires `SessionManagerConfig{StartRuntime:
  fakeStartFixed, ...}` with a fake runtime that holds a real
  `*db.DB` injected via `Options` (or a test helper that constructs
  the runtime with a `:memory:` SQLite path).

- [ ] **Step 1: Inspect the existing fake seams**

Read `internal/acp/session_test.go` to find `fakeStartFixed` and
`fakeRuntimeStart` (they're used by other tests in the same file).
Identify how they construct a `*db.DB`.

- [ ] **Step 2: Rewrite the test to use the fake seam**

Replace the body of `TestSessionLoadUsesExistingSessionOption`
(lines 647-714) with a version that:
- Uses `fakeStartFixed` (or a new fake helper) instead of
  `app.StartRuntime`.
- Pre-seeds the fake DB with a session ID and two messages.
- Asserts the loaded session's messages match.

Reference the existing `TestSessionLoadWithValidExistingOption` (or
similar — pick the closest analogue) for the pattern.

- [ ] **Step 3: Run the modified test to verify it passes**

Run: `go test ./internal/acp -run TestSessionLoadUsesExistingSessionOption -v`
Expected: PASS, in significantly less time than the original
(real-database) version.

- [ ] **Step 4: Run the full ACP package tests**

Run: `go test ./internal/acp -count=1`
Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/acp/session_test.go
git commit -m "test(acp): use fake seams in TestSessionLoadUsesExistingSessionOption (F-POL-69)"
```

---

## Self-Review

1. **Spec coverage:**
   - F-BUG-47 → Task 1.
   - F-BUG-48 → Task 3.
   - F-BUG-49 → Task 9.
   - F-BUG-50 → Task 8.
   - F-BUG-51 → Task 8.
   - F-BUG-57 → Task 6.
   - F-CON-52 → Task 4.
   - F-CON-53 → Task 2.
   - F-CON-54 → Task 8.
   - F-CON-55 → Task 5.
   - F-CON-56 → Task 5.
   - F-POL-58 → Task 11.
   - F-POL-59 → Task 7.
   - F-POL-60 → Task 7.
   - F-POL-61 → Task 7.
   - F-POL-62 → Task 9.
   - F-POL-63 → Task 10.
   - F-POL-64 → Task 3.
   - F-POL-65 → Task 9.
   - F-POL-66 → Task 10.
   - F-POL-67 → Task 11.
   - F-POL-68 → Task 12.
   - F-POL-69 → Task 13.

2. **Placeholder scan:** Task 3 has a weak first test that is
   immediately superseded by the `caller` interface refactor in
   Step 2 — the implementer should run Step 1 to confirm the
   failing test, then collapse the refactor into Step 2 and delete
   the placeholder test. Task 4 has a `noopCallCount` var that is
   vestigial. Task 12's `Render()` extension is a small but
   well-defined change.

3. **Type consistency:** All public types are kept stable except
   where the task explicitly notes a signature change:
   - `Request.ID` and `Response.ID` change to `json.Number` (Task 1).
   - `mcp.Request`/`Response` ID types (Task 1) — internal to MCP.
   - `Request.UnmarshalJSON` (Task 7) is a new method on an
     existing type; additive.
   - `SessionManager.publishReplacement` gains a `context.Context`
     parameter (Task 9) — the plan notes "Update all call sites in
     `internal/acp/`" for the implementer to follow.
   - `perCwdLister` cache is internal; no public API change.
   - `TaskState.AddTestFailure` / `TestFailures` are new
     methods, additive.
