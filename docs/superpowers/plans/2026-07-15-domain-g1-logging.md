# Domain G1 — ACP/MCP Structured Logging (F-XCUT-176)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Thread a `*slog.Logger` through the public constructors of the ACP and MCP layers so operator-facing events (handler dispatches, outbound requests, MCP connect/list/call, server-skip, shutdown) are emitted on a configurable logger instead of always falling back to `slog.Default()`.

**Architecture:** Each public constructor gains a `*slog.Logger` field, populated via a `WithLogger` functional option (or a new constructor parameter, where the existing constructor has no other options). When the field is nil, `slog.Default()` is used at log-time. Tests inject a buffer-backed logger to assert specific events were emitted. This is purely additive — no existing call site is forced to change.

**Tech Stack:** Go 1.22+; stdlib `log/slog` only.

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
- Preserve existing public function signatures (extend with options, do
  not break callers).

## File Structure

Files modified or created by this plan:

- `internal/acp/server.go` — Task 1 (logger field, option, dispatch logging).
- `internal/acp/server_test.go` — Task 1 (asserts events on a buffer logger).
- `internal/acp/session.go` — Task 2 (logger field, option, lifecycle logging).
- `internal/acp/session_test.go` — Task 2 (new test).
- `internal/tools/mcp/manager.go` — Task 3 (logger field, option, connect/list/call logging).
- `internal/tools/mcp/manager_test.go` — Task 3 (new test).
- `internal/tools/mcp/client.go` — Task 4 (logger field, read-loop / close logging).
- `internal/tools/mcp/client_test.go` — Task 4 (new test).
- `internal/app/app.go` — Thread `app.logger` into the four constructors (one-line change per call site; no new file).

---

### Task 1: `acp.Server` accepts a logger and logs dispatches

**Files:**
- Modify: `internal/acp/server.go` (`Server` struct, `NewServer` options, `Server.dispatch`).
- Modify: `internal/acp/server_test.go` (or new test file).

**Problem:** `Server` has no logger field. The C plan added a single
`slog.Default().Error("acp: handler panicked", ...)` in the new
`recover` block but no logger is threaded through the constructor.
Other dispatches (`Server.Serve`, `Server.Request`) do not log at
all.

**Fix:** Add a `Logger *slog.Logger` field on `Server`. Add a
`WithLogger(*slog.Logger) ServerOption` option. In every log site
in `server.go`, log to `s.Logger` (defaulting to `slog.Default()`
when nil). Log a `Info` on dispatch with method/duration/error.

**Implementation steps:**

- [ ] **Step 1: Add the option and field**

In `internal/acp/server.go`:

```go
type ServerOption func(*Server)

func WithLogger(l *slog.Logger) ServerOption {
    return func(s *Server) { s.Logger = l }
}
```

Add to the `Server` struct:

```go
Logger *slog.Logger // nil → slog.Default()
```

In `NewServer`, after constructing the receiver, apply the
options (typical pattern in the file already exists; reuse it).

- [ ] **Step 2: Replace `slog.Default()` calls and add a dispatch log**

In the panic-recovery site added by the C plan, change
`slog.Default().Error(...)` to `s.Logger.Error(...)` (where
`s.Logger` defaults to `slog.Default()` when nil via a small
helper `func (s *Server) log() *slog.Logger { if s.Logger == nil { return slog.Default() }; return s.Logger }`).

In `Server.dispatch`, wrap the handler call:

```go
start := time.Now()
defer func() {
    s.log().Info("acp dispatch",
        "method", req.Method,
        "id", req.ID,
        "duration_ms", time.Since(start).Milliseconds(),
    )
}()
```

- [ ] **Step 3: Add a test**

```go
func TestServerDispatchLogs(t *testing.T) {
    var buf bytes.Buffer
    s := NewServer(WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))

    req := Request{Method: "ping", ID: jsonrpcID(1), Params: json.RawMessage(`{}`)}
    _, _ = s.dispatch(context.Background(), req, func(ctx context.Context, r Request) (any, error) {
        return map[string]any{"ok": true}, nil
    })

    if !strings.Contains(buf.String(), "method=ping") {
        t.Fatalf("expected dispatch log, got %q", buf.String())
    }
}
```

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/acp -run TestServerDispatchLogs -v
git add internal/acp/server.go internal/acp/server_test.go
git commit -m "feat(acp): thread logger through Server (F-XCUT-176)"
```

---

### Task 2: `acp.SessionManager` accepts a logger and logs lifecycle

**Files:**
- Modify: `internal/acp/session.go` (`SessionManager` struct, `NewSessionManager`, `Close`, `publishReplacement`).
- Modify: `internal/acp/session_test.go`.

**Problem:** `SessionManager.Close` and `publishReplacement` mutate
runtime pointers and may drop prior runtimes; today there is no log
when a replacement occurs or when a `Close` no-ops.

**Fix:** Same option pattern as Task 1. Log at `Info` for
`publishReplacement` (new session ID, prior runtime ID) and at
`Debug` for `Close` (session count, no-op vs teardown).

**Implementation steps:**

- [ ] **Step 1: Add field and option**

In `internal/acp/session.go`:

```go
type SessionManagerOption func(*SessionManager)

func WithSessionManagerLogger(l *slog.Logger) SessionManagerOption {
    return func(m *SessionManager) { m.Logger = l }
}
```

Add `Logger *slog.Logger` to the `SessionManager` struct.

- [ ] **Step 2: Add log calls**

In `publishReplacement`:

```go
m.log().Info("session publishReplacement",
    "session_id", sid, "prior_runtime", priorID, "new_runtime", newID)
```

In `Close`:

```go
m.log().Debug("session close", "count", len(m.sessions))
```

Helper method:

```go
func (m *SessionManager) log() *slog.Logger {
    if m.Logger == nil { return slog.Default() }
    return m.Logger
}
```

- [ ] **Step 3: Test**

```go
func TestSessionManagerLogsReplacement(t *testing.T) {
    var buf bytes.Buffer
    m := NewSessionManager(WithSessionManagerLogger(slog.New(slog.NewTextHandler(&buf, nil))))
    sid, _ := m.Create(ctx, "/tmp")
    m.Publish(sid, fakeRuntime{}) // hmm — use the real API
    // Force a replacement
    m.Publish(sid, fakeRuntime2{})

    if !strings.Contains(buf.String(), "publishReplacement") {
        t.Fatalf("expected replacement log, got %q", buf.String())
    }
}
```

(Adapt the test to the real `SessionManager.Publish` / `publishReplacement` API.)

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/acp -run TestSessionManager -v
git add internal/acp/session.go internal/acp/session_test.go
git commit -m "feat(acp): thread logger through SessionManager (F-XCUT-176)"
```

---

### Task 3: `mcp.Manager` accepts a logger and logs connect/list/call

**Files:**
- Modify: `internal/tools/mcp/manager.go` (`Manager` struct, `NewManager`, `RegisterTools`, `Call`).
- Modify: `internal/tools/mcp/manager_test.go`.

**Problem:** `Manager.RegisterTools` already calls
`slog.Default().Warn("mcp: server skipped", ...)` for a failed
server. `Call` and connect-time errors are silent. There is no way
to inject a logger for tests.

**Fix:** Same option pattern. Add a `Logger *slog.Logger` field and
`WithManagerLogger` option. Replace the `slog.Default()` call.
Add `Info` logs at server connect, list, and call with server name
and duration.

**Implementation steps:**

- [ ] **Step 1: Field + option + helper**

```go
type ManagerOption func(*Manager)
func WithManagerLogger(l *slog.Logger) ManagerOption {
    return func(m *Manager) { m.Logger = l }
}
func (m *Manager) log() *slog.Logger {
    if m.Logger == nil { return slog.Default() }
    return m.Logger
}
```

- [ ] **Step 2: Log call sites**

- In `RegisterTools` loop, on `errors.Join`, also log:
  `m.log().Warn("mcp: server skipped", "name", name, "err", err)`
- In `Call`:
  `m.log().Info("mcp call", "server", name, "tool", tool, "duration_ms", dur)`
- In connect: `m.log().Info("mcp connect", "name", name)`

- [ ] **Step 3: Test**

Construct a `Manager` with a buffer logger, drive `Call` against a
fake `Caller`, assert the buffer contains `"mcp call"`.

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/tools/mcp -v
git add internal/tools/mcp/manager.go internal/tools/mcp/manager_test.go
git commit -m "feat(mcp): thread logger through Manager (F-XCUT-176)"
```

---

### Task 4: `mcp.Client` accepts a logger and logs protocol events

**Files:**
- Modify: `internal/tools/mcp/client.go` (`Client` struct, `NewClient`, `readLoop`, `Close`).
- Modify: `internal/tools/mcp/client_test.go`.

**Problem:** `Client` has no logger. The `readLoop` swallows scanner
errors; close-time errors are silent.

**Fix:** Same option pattern. Add `Logger *slog.Logger` field and
`WithClientLogger` option. Log at `Warn` for scanner errors, `Info`
for clean close, `Debug` for each received response (with id).

**Implementation steps:**

- [ ] **Step 1: Field + option + helper**

```go
type ClientOption func(*Client)
func WithClientLogger(l *slog.Logger) ClientOption {
    return func(c *Client) { c.Logger = l }
}
func (c *Client) log() *slog.Logger {
    if c.Logger == nil { return slog.Default() }
    return c.Logger
}
```

- [ ] **Step 2: Log call sites**

- `readLoop` after the scanner ends: `c.log().Warn("mcp readLoop ended", "err", scanner.Err())`
- `Close` (success path): `c.log().Info("mcp client closed")`
- Each successful `Response` decode: `c.log().Debug("mcp response", "id", id)`

- [ ] **Step 3: Test**

Use a fake `io.ReadCloser` that returns a malformed JSON line, run
`readLoop` for one tick, assert the buffer contains `"mcp readLoop ended"`.

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/tools/mcp -v
git add internal/tools/mcp/client.go internal/tools/mcp/client_test.go
git commit -m "feat(mcp): thread logger through Client (F-XCUT-176)"
```

---

## Self-Review

After the four tasks land:

```bash
CGO_ENABLED=1 go build ./...                 # all packages compile
CGO_ENABLED=1 go test ./internal/acp/... -count=1
CGO_ENABLED=1 go test ./internal/tools/mcp/... -count=1
```

Update `docs/14-codebase-improvement-audit-2026-07-14.md` with a new
resolution section:

```markdown
### Batch 23 (G1 — ACP/MCP structured logging): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-XCUT-176 | RESOLVED | `acp.Server`, `acp.SessionManager`, `mcp.Manager`, `mcp.Client` all accept `*slog.Logger` via `With…` options. Default `slog.Default()`. Dispatch, connect, list, call, replace, close all log. 4 new tests assert specific events on a buffer logger. |
```
