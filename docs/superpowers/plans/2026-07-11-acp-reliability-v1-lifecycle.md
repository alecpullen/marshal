# ACP Reliability and v1 Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Marshal's ACP transport nonblocking and lifecycle-safe while implementing the ACP v1 prompt, load/replay, cancellation, session-update, and session-close contracts in scope.

**Architecture:** A dedicated server read loop classifies frames and dispatches tracked handlers while continuing to route responses for Marshal-originated permission requests. `TurnManager` serializes prompts per session and registers them with the runtime work gate; `SessionManager` owns load, replay, replacement, close, and EOF cleanup. Wire projections use standard ACP v1 content blocks and `session/update` envelopes.

**Tech Stack:** Go 1.24, JSON-RPC 2.0 over newline-delimited stdio, ACP v1, Bubble Tea-independent app runtime, SQLite, existing typed pub/sub.

**Spec:** `docs/superpowers/specs/2026-07-11-acp-reliability-v1-lifecycle-design.md`

## Prerequisites (verify before Task 1)

Batch 1 (`docs/superpowers/plans/2026-07-11-execution-shutdown-safety.md`) must be merged into `main` before this plan begins. Task 1 onward depend on its runtime lifecycle; running them out of order will fail at compile time.

```bash
# All of these must be present in the working tree:
rg -n 'func \(rt \*Runtime\) Quiesce\(' internal/app/runtime.go
rg -n 'func \(rt \*Runtime\) Close\(ctx context\.Context\) error' internal/app/runtime.go
rg -n 'func \(s \*State\) (BeginWork|EndWork|BeginQuiesce|WaitForWork|ResolvePendingForShutdown)' internal/app/session/session.go
rg -n 'var ErrSessionQuiescing' internal/app/session/session.go
```

Expected: the four `rg` queries return a total of eight matches — one for `Quiesce`, one for `Close`, five for the `State` lifecycle methods (`BeginWork`, `EndWork`, `BeginQuiesce`, `WaitForWork`, `ResolvePendingForShutdown`), and one for `ErrSessionQuiescing`. If any line is missing, Batch 1 has not been merged.

Also verify the merge commit and rebase this branch onto it:

```bash
git log --oneline --merges --first-parent main | head -1
git rebase main
```

If any prerequisite symbol is missing, stop and finish Batch 1 first.

## Public surface changed by this plan

The plan adds the exported names below. Use this list as a cross-check at every commit boundary; a name missing here means it is either already present or it belongs to a different batch.

```go
// internal/acp
const (
    parseError       = -32700
    invalidRequest   = -32600
    invalidParams    = -32602
    requestCancelled = -32800
    serverError      = -32000
)

type ContentBlock struct{ /* type, text, uri, name, mimeType, title, description, size */ }
type SessionUpdateParams struct{ SessionID string; Update map[string]any }
type PromptTurnParams struct{ SessionID string; Prompt []ContentBlock }

func normalizePrompt([]ContentBlock) (string, error)
func invalidParamsError(format string, args ...any) error
func serverErrorf(format string, args ...any) error

type TurnRuntime struct {
    SessionID string
    BeginWork func(context.Context) (context.Context, func(), error)
    Run       RunnerFunc
    Events    *pubsub.Broker[session.Event]
}
func (m *TurnManager) CancelAndWait(ctx context.Context, sessionID string) error

type RuntimeCloser func(context.Context, *app.Runtime) error
type TurnCanceller func(context.Context, string) error
type SessionManagerConfig struct {
    StartRuntime RuntimeStarter
    CloseRuntime RuntimeCloser
    Notify       NotifyFunc
    Options      []app.Option
}
func (m *SessionManager) SetTurnCanceller(cancel TurnCanceller)
func (m *SessionManager) Get(id string) (*app.Runtime, bool)
func (m *SessionManager) Close(ctx context.Context, id string) error
func (m *SessionManager) CloseSession(ctx context.Context, params json.RawMessage) (any, error)
func (m *SessionManager) CloseAll(ctx context.Context) error

// internal/db
func (db *DB) GetProjectByRoot(rootPath string) (Project, error)

// internal/app
func WithExistingSession(id string) Option
func (rt *Runtime) BeginWork(parent context.Context) (context.Context, func(), error)

// internal/app/session
func (s *State) LoadError() error
```

Marshal-specific ACP notification methods (`session/message_added`, `session/thinking_changed`, `session/activity_changed`, `session/active_tool_changed`, `session/audit_added`, `session/pending_approval_changed`, `session/pending_question_changed`) and the `Prompt string` field on `PromptTurnParams` are removed by this plan.

## Global Constraints

- Batch 1 (`docs/superpowers/plans/2026-07-11-execution-shutdown-safety.md`) must be implemented first. Before Task 1, the repository must contain `Runtime.Quiesce`, idempotent `Runtime.Close`, `session.State.BeginWork/EndWork/BeginQuiesce/WaitForWork`, and `session.ErrSessionQuiescing`.
- Follow TDD: write the named failing tests, observe the specified failure, implement the minimal production change, rerun focused tests, and commit at every task boundary.
- The transport read loop must never execute a long-running handler inline.
- At most one prompt may run per session. A duplicate returns `-32000` and must not cancel the first prompt. Different sessions may run concurrently.
- ACP runner work must register through `Runtime.BeginWork`; do not create a second work counter in `internal/acp`.
- `session/cancel` is a notification. It receives no response; the original prompt returns `{"stopReason":"cancelled"}` after the runner exits.
- `session/load` replays the active branch through `session/update` and returns literal JSON `result: null` only after replay completes.
- Prompt input supports only ACP `text` and `resource_link` blocks. Never fetch a resource-link URI in the ACP layer.
- Non-empty client MCP server lists and additional-directory lists return `-32602`; do not silently ignore them.
- Initialize advertises only `loadSession` and `sessionCapabilities.close` among optional features in scope.
- Client-visible turn and replay output uses `session/update`. Remove Marshal-specific ACP notification methods.
- All shutdown paths are bounded at five seconds, attempt every cleanup, and use `errors.Join` for independent failures.
- Do not add `session/resume`, `session/list`, `session/delete`, `$/cancel_request`, rich media, embedded resources, dynamic MCP, additional roots, full ACP tool/plan projection, or ACP v2 behavior.
- Preserve unrelated user changes and the uncommitted Batch 1 plan/audit. Stage only paths named by each task.

## File Structure

```text
internal/acp/
  protocol.go           — JSON-RPC codes/errors, response encoding, ACP content/update wire types
  protocol_test.go      — null results, error codes, content normalization
  server.go             — dedicated frame router, tracked handlers, outbound waiter shutdown
  server_test.go        — concurrent dispatch, response routing, EOF/cancellation
  turn.go               — per-session turn slots, runtime work registration, standard updates
  turn_test.go          — prompt concurrency, cancel/join, updates, questions, permissions
  session.go            — runtime ownership, lifecycle validation, load/replay/close
  session_test.go       — replacement ordering, replay, close-all, validation
  run.go                — initialize capabilities and production component wiring
  run_test.go           — initialization and whole-connection cleanup
  integration_test.go   — pipe-level permission and cancellation regressions

internal/app/
  app.go                — WithExistingSession option
  runtime.go            — read-only existing-session bootstrap and Runtime.BeginWork
  app_test.go           — existing load and work-context integration tests

internal/app/session/
  session.go            — expose cold-load failure to runtime bootstrap
  session_test.go       — persistent load-error regression

internal/db/
  projects.go           — read-only GetProjectByRoot
  projects_test.go      — lookup/non-mutation tests

docs/
  10-acp.md             — supported ACP v1 surface and limitations
  13-project-audit-2026-07-11.md — Batch 2 resolution status only
```

---

### Task 1: Define strict JSON-RPC and ACP prompt/update primitives

This task introduces reusable wire contracts without changing turn dispatch yet, so the package remains green at the checkpoint.

**Files:**
- Modify: `internal/acp/protocol.go`
- Modify: `internal/acp/protocol_test.go`

**Interfaces:**

```go
const (
	parseError       = -32700
	invalidRequest   = -32600
	methodNotFound   = -32601
	invalidParams    = -32602
	internalError    = -32603
	requestCancelled = -32800
	serverError      = -32000
)

type ContentBlock struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	URI         string `json:"uri,omitempty"`
	Name        string `json:"name,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type SessionUpdateParams struct {
	SessionID string         `json:"sessionId"`
	Update    map[string]any `json:"update"`
}

func normalizePrompt(blocks []ContentBlock) (string, error)
func invalidParamsError(format string, args ...any) error
func serverErrorf(format string, args ...any) error
```

- [ ] **Step 1: Add failing response-shape tests**

Add `TestResponseMarshalSuccessNilIncludesResultNull`:

```go
func TestResponseMarshalSuccessNilIncludesResultNull(t *testing.T) {
	id := json.RawMessage(`1`)
	b, err := json.Marshal(Response{JSONRPC: "2.0", ID: &id, Result: nil})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"jsonrpc":"2.0","id":1,"result":null}` {
		t.Fatalf("response = %s", b)
	}
}
```

Add `TestResponseMarshalErrorOmitsResult`, requiring error code/message and no `result` key. Add `TestCodeForWrappedJSONRPCError`, wrapping an invalid-params error with `fmt.Errorf("decode: %w", err)` and requiring `codeFor` to return `-32602`.

Run:

```bash
go test ./internal/acp -run 'Test(ResponseMarshal|CodeForWrapped)'
```

Expected: FAIL because success nil currently omits `result`, and `codeFor` does not unwrap.

- [ ] **Step 2: Implement response marshaling and typed error helpers**

Give `Response` a `MarshalJSON` method. The success branch uses a struct with a non-omitempty `Result any` field; the error branch uses a struct with no result field. Change `codeFor` to use `errors.As`:

```go
func codeFor(err error) int {
	var rpcErr *jsonRPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code
	}
	if errors.Is(err, context.Canceled) {
		return requestCancelled
	}
	return internalError
}

func invalidParamsError(format string, args ...any) error {
	return &jsonRPCError{Code: invalidParams, Message: fmt.Sprintf(format, args...)}
}

func serverErrorf(format string, args ...any) error {
	return &jsonRPCError{Code: serverError, Message: fmt.Sprintf(format, args...)}
}
```

- [ ] **Step 3: Add failing prompt normalization tests**

Add table-driven `TestNormalizePrompt` cases:

- two text blocks produce `first\n\nsecond`;
- text/resource-link/text produces `inspect\n\nResource link: README (file:///repo/README.md)\n\ndone`;
- nil blocks, empty text, missing resource name, missing URI, `image`, `audio`, and `resource` each return an error whose code is `invalidParams`;
- a resource link does not invoke any filesystem or HTTP seam because `normalizePrompt` has none.

Run:

```bash
go test ./internal/acp -run TestNormalizePrompt
```

Expected: FAIL to compile because `ContentBlock` and `normalizePrompt` do not exist.

- [ ] **Step 4: Implement normalization and update types**

Implement `normalizePrompt` with ordered `parts` and `strings.Join(parts, "\n\n")`. Reject whitespace-only text/name/URI with `invalidParamsError`. For a resource link append exactly:

```go
fmt.Sprintf("Resource link: %s (%s)", block.Name, block.URI)
```

Add `SessionUpdateParams`. Do not modify `PromptTurnParams` yet; Task 4 switches the turn handler and its existing fixtures atomically.

- [ ] **Step 5: Verify and commit**

Run:

```bash
go test -count=1 ./internal/acp
```

Expected: PASS.

```bash
git add internal/acp/protocol.go internal/acp/protocol_test.go
git commit -m "feat(acp): add strict v1 wire primitives"
```

---

### Task 2: Make `Server` a dedicated asynchronous frame router

The reader must remain available for outbound permission responses and cancellation while handlers run.

**Files:**
- Rewrite: `internal/acp/server.go`
- Modify: `internal/acp/server_test.go`

**Interfaces:**

```go
var handlerShutdownTimeout = 5 * time.Second

type outboundResult struct {
	response *Response
	err      error
}

func (s *Server) dispatchRequest(ctx context.Context, req Request)
func (s *Server) dispatchNotification(ctx context.Context, req Request)
func (s *Server) deliverOutbound(id *json.RawMessage, line []byte) bool
func (s *Server) failOutbound(err error)
func (s *Server) reportFatal(err error)
func (s *Server) waitHandlers(ctx context.Context) error
```

- [ ] **Step 1: Add a failing nonblocking-reader regression**

Add `TestServerReadsOutboundResponseWhileInboundHandlerRuns`. Use `io.Pipe`, register a `session/prompt` handler that calls `srv.Request`, start `Serve`, write the prompt request, read the outbound request ID from a locked output-frame collector, write its matching success response, and require the prompt response within two seconds.

The handler body is:

```go
srv.Handle("session/prompt", func(ctx context.Context, params json.RawMessage) (any, error) {
	var decision struct {
		Approved bool `json:"approved"`
	}
	if err := srv.Request(ctx, "session/request_permission", map[string]any{"sessionId": "s1"}, &decision); err != nil {
		return nil, err
	}
	return map[string]any{"approved": decision.Approved}, nil
})
```

Run:

```bash
go test ./internal/acp -run TestServerReadsOutboundResponseWhileInboundHandlerRuns -count=1 -timeout=5s
```

Expected: FAIL by timeout because the synchronous reader is blocked in the prompt handler.

- [ ] **Step 2: Add frame-classification and handler-order tests**

Add:

- `TestServerResponsesCorrelateWhenHandlersFinishOutOfOrder`: request 1 blocks, request 2 completes, then request 1 completes; decoded response IDs must be 2 then 1.
- `TestServerIgnoresUnmatchedResponse`: an ID/result frame with no pending outbound waiter produces no response.
- `TestServerNotificationRunsWithoutResponse`: retain and strengthen the existing notification test by waiting for handler execution.
- `TestServerEOFReleasesOutboundWaiter`: close the pipe while `Request` waits and require an error containing `connection closed`.
- `TestServerCancellationJoinsHandlers`: handler waits on context; cancel Serve and prove handler returned before Serve returns.
- `TestServerShutdownHonorsBound`: temporarily set `handlerShutdownTimeout` to 50 milliseconds, use a handler that deliberately ignores context, and require Serve to return a deadline error without waiting for the handler's release gate.
- `TestServerResponseWriteFailureStopsServe`: use a writer returning a sentinel error and require `errors.Is(Serve(...), sentinel)`.

Run:

```bash
go test ./internal/acp -run 'TestServer(ResponsesCorrelate|IgnoresUnmatched|NotificationRuns|EOFReleases|CancellationJoins)'
```

Expected: at least the out-of-order and EOF tests FAIL against synchronous dispatch and the current closed-channel behavior.

- [ ] **Step 3: Separate outbound state from encoder locking**

Add a `stateMu` for `outbound`, server context, and closed state. Keep `outMu` only for atomic encoder writes. `Request` must:

1. reject a closed server;
2. allocate/register `chan outboundResult` under `stateMu`;
3. encode under `outMu`;
4. remove the waiter if encoding fails or caller context ends;
5. decode the delivered response or return its terminal error.

`failOutbound` swaps the map to an empty map while locked, then delivers the error to every buffered waiter outside the lock.

- [ ] **Step 4: Implement asynchronous frame routing**

`Serve` creates `serveCtx, cancel := context.WithCancel(ctx)` and runs scanning in a goroutine that copies each scanned line into a frames channel. Every scanner send selects between the frames channel and `serveCtx.Done()` so it cannot remain blocked after Serve exits. The Serve select loop handles parent cancellation, scanner completion, fatal write errors, and frames.

For each valid frame:

- if `Method == ""` and ID is present, call `deliverOutbound` and otherwise ignore it;
- if ID is nil, increment `handlerWG` before launching `dispatchNotification`;
- if ID is present and Method is non-empty, increment `handlerWG` before launching `dispatchRequest`.

`dispatchRequest` calls the handler and writes exactly one success/error response. If response encoding fails, it sends that error once to a buffered fatal-error channel through `reportFatal`; Serve cancels the connection and returns the error. `dispatchNotification` calls the handler and discards its result/error. Both defer `handlerWG.Done()`.

On EOF/cancellation: cancel `serveCtx`, close the input if it implements `io.Closer`, call `failOutbound(errors.New("acp: connection closed"))`, and use a five-second context with `waitHandlers`.

- [ ] **Step 5: Preserve parse and scanner behavior**

Retain the one-megabyte scanner limit. Malformed JSON writes `-32700` with `id: null`. A decoded frame with empty method and no response structure writes `-32600`. Scanner errors are joined with handler-shutdown errors. Clean EOF returns only cleanup errors; parent cancellation returns `ctx.Err()` joined with cleanup errors.

- [ ] **Step 6: Verify under race detection and commit**

Run:

```bash
go test -race -count=1 ./internal/acp -run 'TestServer'
```

Expected: PASS with no data race or timeout.

```bash
git add internal/acp/server.go internal/acp/server_test.go
git commit -m "fix(acp): keep transport reader live during handlers"
```

---

### Task 3: Add read-only runtime loading and transport-visible work registration

This task creates the app/database APIs ACP needs without adding ACP-specific behavior to the runtime.

**Files:**
- Modify: `internal/db/projects.go`
- Modify: `internal/db/projects_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/runtime.go`
- Modify: `internal/app/app_test.go`
- Modify: `internal/app/session/session.go`
- Modify: `internal/app/session/session_test.go`

**Interfaces:**

```go
func (db *DB) GetProjectByRoot(rootPath string) (Project, error)
func WithExistingSession(id string) Option
func (rt *Runtime) BeginWork(parent context.Context) (context.Context, func(), error)
func (s *State) LoadError() error
```

- [ ] **Step 1: Add failing read-only project lookup tests**

Add `TestGetProjectByRoot` and `TestGetProjectByRootMissingDoesNotCreate`. The missing test calls the lookup, requires a `project not found` error, then queries `SELECT COUNT(*) FROM projects` and requires zero.

Run:

```bash
go test ./internal/db -run TestGetProjectByRoot
```

Expected: FAIL to compile because the method does not exist.

- [ ] **Step 2: Implement `GetProjectByRoot`**

Use the same row mapping/time parsing as `GetProject`, with this query:

```sql
SELECT id, root_path, name, created_at, updated_at
FROM projects
WHERE root_path = ?
```

Return `fmt.Errorf("project not found: %s", rootPath)` on `sql.ErrNoRows`.

- [ ] **Step 3: Add failing existing-session runtime tests**

In `app_test.go`, add:

- `TestStartRuntimeLoadsExistingSessionWithoutDuplicateInsert`: bootstrap a workspace/runtime, persist user and assistant messages, close it, record counts for projects/sessions/messages, call `StartRuntime(..., WithExistingSession(id))`, require the same active messages/leaf, and require counts unchanged.
- `TestStartRuntimeExistingSessionMissingDoesNotCreate`: create the project/database but request a missing ID; require an error and unchanged session count.
- `TestStartRuntimeExistingSessionRejectsProjectMismatch`: create two project rows in the database, attach the session to the other project, and require a mismatch error.
- `TestStartRuntimeRejectsConflictingSessionModes`: supply both `WithSessionID("new")` and `WithExistingSession("old")` and require a validation error before insertion.
- session-package `TestStateLoadErrorReportsColdLoadFailure`: create persistent state against a closed DB and require a non-nil load error instead of an indistinguishable empty transcript.

Run:

```bash
go test ./internal/app -run 'TestStartRuntime(LoadsExisting|ExistingSession|RejectsConflicting)'
```

Expected: FAIL to compile because `WithExistingSession` does not exist.

- [ ] **Step 4: Implement explicit existing-session mode**

Add `existingSessionID string` to app options and:

```go
func WithExistingSession(id string) Option {
	return func(opts *options) {
		opts.existingSessionID = id
	}
}
```

Change `loadFromDB` to return its lookup/query error while retaining current logging. Store it in a private `loadErr` field during `New`, and expose a mutex-protected `LoadError`. In `StartRuntime`, after DB migration, branch before `GetOrCreateProject`:

- existing mode calls `GetProjectByRoot(workingDir)`, `GetSession(id)`, verifies `stored.ProjectID == project.ID`, sets `projectID`, `sessionID`, and the state's start time from the stored session, skips `CreateSession`, and aborts startup if the constructed state reports `LoadError()`;
- new mode retains the current get/create and insert behavior;
- both IDs set simultaneously return `app: WithSessionID and WithExistingSession are mutually exclusive`.

Every error path closes the DB/log resources already opened. Do not call `GetOrCreateProject` in existing mode.

- [ ] **Step 5: Add failing `Runtime.BeginWork` tests**

Add:

- `TestRuntimeBeginWorkCancelledByRuntimeQuiesce`: register work, start `Quiesce` in a goroutine, require the returned work context to cancel while quiesce remains blocked, call `finish`, and then require quiesce completion.
- `TestRuntimeBeginWorkFinishIsIdempotent`: call finish twice and require no negative counter/panic.
- `TestRuntimeBeginWorkRejectsAfterQuiesce`: require `errors.Is(err, session.ErrSessionQuiescing)`.
- `TestRuntimeBeginWorkCancelledByParent`: cancel the parent and require the work context to end without quiescing the runtime.

Run:

```bash
go test ./internal/app -run TestRuntimeBeginWork
```

Expected: FAIL to compile because `BeginWork` does not exist.

- [ ] **Step 6: Implement `Runtime.BeginWork`**

Call `rt.State.BeginWork()` before deriving a child. Create `workCtx, cancel := context.WithCancel(parent)`, use `context.AfterFunc(rt.workCtx, cancel)`, and return an idempotent finisher:

```go
var finishOnce sync.Once
finish := func() {
	finishOnce.Do(func() {
		stopRuntimeCancel()
		cancel()
		rt.State.EndWork()
	})
}
```

If `parent`, `State`, or the runtime root context is nil, return a concrete error and do not increment work. If runtime cancellation races registration, cancel the child immediately; the caller still owns `finish`.

- [ ] **Step 7: Verify and commit**

Run:

```bash
go test -race -count=1 ./internal/db ./internal/app ./internal/app/session
```

Expected: PASS.

```bash
git add internal/db/projects.go internal/db/projects_test.go internal/app/app.go internal/app/runtime.go internal/app/app_test.go internal/app/session/session.go internal/app/session/session_test.go
git commit -m "feat(app): load existing runtimes safely"
```

---

### Task 4: Rebuild `TurnManager` around per-session slots and standard updates

This task switches prompt input atomically, so update every old string-prompt fixture in the same checkpoint.

**Files:**
- Rewrite: `internal/acp/turn.go`
- Rewrite: `internal/acp/turn_test.go`
- Modify: `internal/acp/permissions_test.go`

**Interfaces:**

```go
type PromptTurnParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

type TurnRuntime struct {
	SessionID string
	BeginWork func(context.Context) (context.Context, func(), error)
	Run       RunnerFunc
	Events    *pubsub.Broker[session.Event]
}

type activeTurn struct {
	cancel          context.CancelFunc
	done            chan struct{}
	clientCancelled atomic.Bool
}

func (m *TurnManager) CancelAndWait(ctx context.Context, sessionID string) error
```

- [ ] **Step 1: Convert existing fixtures to ACP content arrays**

Replace JSON prompt strings in `turn_test.go` and `permissions_test.go` with:

```json
{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}
```

Every fake `TurnRuntime` gets this identity work gate unless a test overrides it:

```go
BeginWork: func(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
},
```

- [ ] **Step 2: Add failing concurrency and cancellation tests**

Add:

- `TestPromptTurnRejectsConcurrentPromptForSameSession`: first runner blocks; second returns an error with code `serverError`; first remains un-cancelled.
- `TestPromptTurnsDifferentSessionsRunConcurrently`: two runners both signal started before either is released.
- `TestCancelAndWaitJoinsRunner`: runner waits on context then performs gated cleanup; prove cancel does not return before cleanup gate opens.
- `TestSessionCancelMakesPromptReturnCancelled`: run prompt, invoke `Cancel`, and require the prompt result stop reason `cancelled` with no error.
- `TestPromptTurnRegistersRuntimeWork`: fake BeginWork and finish counters each equal one.
- `TestPromptTurnQuiescingReturnsRequestCancelled`: BeginWork returns `session.ErrSessionQuiescing`; require code `requestCancelled` and no runner call.

Run:

```bash
go test ./internal/acp -run 'Test(PromptTurnRejects|PromptTurnsDifferent|CancelAndWait|SessionCancel|PromptTurnRegisters|PromptTurnQuiescing)'
```

Expected: FAIL against overwrite-and-cancel active-turn behavior and missing work registration.

- [ ] **Step 3: Implement slot reservation and work registration**

Parse/normalize before running. Create `slotCtx, slotCancel := context.WithCancel(ctx)` and construct the slot with `cancel: slotCancel` before making it visible. Reserve it only when the session key is absent. After reservation, call `BeginWork(slotCtx)`; the returned context is the runner's `turnCtx`. This prevents `CancelAndWait` from observing a slot with no cancel function. Ensure cleanup:

```go
defer func() {
	slotCancel()
	m.activeTurnsMu.Lock()
	if m.activeTurns[p.SessionID] == slot {
		delete(m.activeTurns, p.SessionID)
	}
	m.activeTurnsMu.Unlock()
	close(slot.done)
}()
```

If reservation loses to an existing slot, call `slotCancel` and return the duplicate-turn server error without calling `BeginWork`. The runner goroutine defers `finish()`. `CancelAndWait` sets `slot.clientCancelled` before cancelling. Map a cancelled runner to `PromptTurnResult{StopReason: "cancelled"}` only when that flag is true; cancellation from server/runtime shutdown returns the request-cancelled error.

- [ ] **Step 4: Add failing standard-update projection tests**

Add:

- `TestPromptTurnProjectsMessagesAsSessionUpdate`: publish user, assistant, and system messages; every method is `session/update`, with update kinds `user_message_chunk`, `agent_message_chunk`, `agent_message_chunk`.
- `TestPromptTurnProjectsThinkingDelta`: publish thinking `abc`, then `abcdef`; emitted thought texts are `abc` and `def`, not cumulative duplicates.
- `TestPromptTurnSuppressesInternalCustomEvents`: activity, audit, active-tool, and pending-clear events produce no wire update.
- `TestPromptTurnUsesTerminalSubscription`: publish more than the default broker buffer before runner return and require every message notification.

Run:

```bash
go test ./internal/acp -run 'TestPromptTurn(Projects|Suppresses|UsesTerminal)'
```

Expected: FAIL because current methods are Marshal-specific and the subscription is best-effort.

- [ ] **Step 5: Implement standard projections**

Replace `sessionEventToNotification` / `eventToParams` with:

```go
func messageUpdate(msg session.Message) map[string]any
func eventToSessionUpdate(ev pubsub.Event[session.Event], lastThinking *string) (map[string]any, bool)
```

`messageUpdate` maps user messages to `user_message_chunk` and assistant/system messages to `agent_message_chunk`; all contain `content: {type:"text", text:...}`. `eventToSessionUpdate` delegates message events to that helper. Thinking updates emit only the suffix when the new reasoning has the previous value as a prefix; otherwise emit the full non-empty value. Ignore unsupported/internal events at the wire layer.

The thinking-delta state is a `string` field on the per-turn goroutine, not on the manager. Declare it inside `PromptTurn` so each new turn starts empty and no shared state survives across prompts. Emit `agent_thought_chunk` content of the form `{"type":"text","text": <delta>}` exactly as the spec requires — do not invent a `reasoning` field on the wire.

Subscribe with:

```go
sub := rt.Events.Subscribe(subCtx, pubsub.WithTerminal[session.Event]())
```

Call `Notify("session/update", SessionUpdateParams{SessionID: p.SessionID, Update: update})` and treat a notify error as fatal to the turn.

- [ ] **Step 6: Make permissions and questions nonblocking and explicit**

For non-nil pending approval, call the bridge synchronously in the forwarding loop. If it fails, cancel and join the runner, then return the error.

For non-nil pending question, build one unanswered item per question and deliver it with a turn-context select:

```go
answers := make([]session.Answer, len(pending.Questions))
for i, q := range pending.Questions {
	answers[i] = session.Answer{Question: q.Question, Answer: session.AnswerUnanswered}
}
select {
case pending.ResponseChan <- answers:
case <-turnCtx.Done():
}
```

Add `TestPromptTurnPermissionFailureCancelsRunner` and `TestPromptTurnAnswersUnsupportedQuestionsAsUnanswered`.

- [ ] **Step 7: Verify and commit**

Run:

```bash
go test -race -count=1 ./internal/acp -run 'Test(Prompt|Cancel|Permission)'
```

Expected: PASS.

```bash
git add internal/acp/turn.go internal/acp/turn_test.go internal/acp/permissions_test.go
git commit -m "fix(acp): serialize and cancel turns per session"
```

---

### Task 5: Make `SessionManager` own validation, load/replay, replacement, and close

Use injection only for runtime start/close and notification tests; production continues to own concrete `*app.Runtime` values.

**Files:**
- Rewrite: `internal/acp/session.go`
- Rewrite: `internal/acp/session_test.go`

**Interfaces:**

```go
type RuntimeCloser func(context.Context, *app.Runtime) error
type TurnCanceller func(context.Context, string) error

type SessionManagerConfig struct {
	StartRuntime RuntimeStarter
	CloseRuntime RuntimeCloser
	Notify       NotifyFunc
	Options      []app.Option
}

func (m *SessionManager) SetTurnCanceller(cancel TurnCanceller)
func (m *SessionManager) Get(id string) (*app.Runtime, bool)
func (m *SessionManager) Close(ctx context.Context, id string) error
func (m *SessionManager) CloseSession(ctx context.Context, params json.RawMessage) (any, error)
func (m *SessionManager) CloseAll(ctx context.Context) error
```

- [ ] **Step 1: Add failing lifecycle parameter tests**

Change `sessionParams` to include:

```go
type sessionParams struct {
	Cwd                   string             `json:"cwd"`
	SessionID             string             `json:"sessionId"`
	MCPServers            *[]json.RawMessage `json:"mcpServers"`
	AdditionalDirectories []string           `json:"additionalDirectories,omitempty"`
}
```

Add table-driven `TestSessionLifecycleValidation` requiring `invalidParams` for relative/missing cwd, omitted MCP array, non-empty MCP array, non-empty additional directories, and missing load ID. Absolute cwd plus `"mcpServers":[]` passes validation.

Run:

```bash
go test ./internal/acp -run TestSessionLifecycleValidation
```

Expected: FAIL because current handlers accept missing cwd/MCP and ignore unsupported fields.

- [ ] **Step 2: Implement validation and explicit close seam**

Default `CloseRuntime` when nil:

```go
func(ctx context.Context, rt *app.Runtime) error {
	if rt == nil {
		return nil
	}
	return rt.Close(ctx)
}
```

Require a non-nil `Notify` for load replay and a configured `TurnCanceller` before Load/Close/CloseAll. Use `lifecycleMu` to serialize lifecycle mutations and a separate `mu sync.RWMutex` for map lookup.

- [ ] **Step 3: Add failing replacement and close tests**

Add:

- `TestSessionLoadClosesOldBeforeStartingReplacement`: preload old runtime, record events from canceller/closer/starter, require `cancel old`, `close old`, `start new` order.
- `TestSessionCloseRemovesCancelsAndCloses`: require Get false before the blocking closer is released, then `{}` success after close.
- `TestSessionCloseUnknownReturnsServerError`.
- `TestSessionCloseAllAttemptsEveryRuntimeAndJoinsErrors`: three runtimes; canceller and closer failures are all observable with `errors.Is`, every runtime attempted once, map empty.
- `TestSessionCloseAllIsIdempotent`: second call does not close again.

Run:

```bash
go test ./internal/acp -run 'TestSession(LoadCloses|CloseRemoves|CloseUnknown|CloseAll)'
```

Expected: FAIL because replacement overwrites without close and no close APIs exist.

- [ ] **Step 4: Implement ownership transitions**

Load removes the old pointer under map lock, calls `TurnCanceller`, and closes old. It always attempts both cancellation and close. If either fails, it returns their joined error and does not start a replacement. Only successful old-runtime cleanup is followed by StartRuntime with copied base options plus `app.WithWorkingDir(p.Cwd)` and `app.WithExistingSession(p.SessionID)`.

The manager does not call `GetProjectByRoot` or `GetSession` itself. Project/session lookup and the `stored.ProjectID == project.ID` mismatch check live inside `app.StartRuntime`'s existing-session branch (Task 3). The manager's responsibility ends at passing `WithExistingSession(p.SessionID)`; if startup fails, `Load` returns that error and installs nothing.

Create calls StartRuntime with `WithWorkingDir`, then checks for an unlikely generated-ID collision. If one exists, cancel/close the old runtime before publishing the new pointer.

Close removes first, then cancels and closes. `CloseSession` parses/validates `sessionId`, calls Close, and returns `map[string]any{}`. CloseAll swaps `sessions` for a new empty map, sorts IDs for deterministic tests/logging, then attempts cancel and close for each using the caller context.

- [ ] **Step 5: Add failing replay tests**

Add:

- `TestSessionLoadReplaysActiveBranchBeforeReturning`: fake runtime state has user, assistant, and system messages; Notify records `SessionUpdateParams`; require ordered standard update kinds and `Load` result nil.
- `TestSessionLoadReplayFailureRemovesAndClosesRuntime`: Notify fails on message two; require Get false and closer called once.
- `TestSessionLoadUsesExistingSessionOption`: integration version uses the real Task 3 StartRuntime and proves session/project/message counts unchanged.

Run:

```bash
go test ./internal/acp -run 'TestSessionLoad(Replays|ReplayFailure|UsesExisting)'
```

Expected: FAIL because current Load neither replays nor cleans up failure.

- [ ] **Step 6: Implement replay with the shared projection**

Consume the focused helper produced by Task 4:

```go
func messageUpdate(msg session.Message) map[string]any
```

For each `rt.State.Messages()` item, synchronously call:

```go
m.notify("session/update", SessionUpdateParams{
	SessionID: p.SessionID,
	Update:    messageUpdate(msg),
})
```

Only install the loaded runtime before replay so lifecycle calls can find it. On any notification error, remove it only if the pointer still matches, close it, and return the joined replay/close error.

- [ ] **Step 7: Verify and commit**

Run:

```bash
go test -race -count=1 ./internal/acp ./internal/app
```

Expected: PASS.

```bash
git add internal/acp/session.go internal/acp/session_test.go
git commit -m "feat(acp): load replay and close owned sessions"
```

---

### Task 6: Wire truthful initialization and bounded connection cleanup

Production wiring must use all ownership/cancellation seams rather than leaving test-only components disconnected.

**Files:**
- Modify: `internal/acp/run.go`
- Modify: `internal/acp/run_test.go`

**Interfaces:**

```go
const connectionShutdownTimeout = 5 * time.Second

type runConfig struct {
	startRuntime RuntimeStarter
	closeRuntime RuntimeCloser
	shutdown     time.Duration
}

func runWithConfig(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, cfg runConfig) error
```

`runWithConfig` normalizes a non-positive `cfg.shutdown` to `connectionShutdownTimeout`.

- [ ] **Step 1: Add failing initialize capability tests**

Send initialize with protocol version 1 and assert the exact semantic shape:

- `protocolVersion: 1`;
- `agentCapabilities.loadSession: true`;
- `agentCapabilities.sessionCapabilities.close` is an object;
- no image/audio/embeddedContext/resume/list/delete/additionalDirectories/MCP capability;
- `agentInfo.name == "marshal"`, `agentInfo.title == "Marshal"`;
- `authMethods` is an empty array.

Add a version negotiation case sending version 2 and requiring response version 1.

Run:

```bash
go test ./internal/acp -run TestRunInitializeCapabilities
```

Expected: FAIL because current initialize omits capabilities and uses the wrong agent shape.

- [ ] **Step 2: Wire manager/turn construction in dependency order**

Add:

```go
type InitializeParams struct {
	ProtocolVersion int `json:"protocolVersion"`
}
```

The initialize handler rejects missing/zero versions as invalid parameters, responds with version 1 when the peer requests any other version, uses `map[string]any{"close": map[string]any{}}` for the close capability, and uses `[]any{}` so `authMethods` encodes as an empty array rather than null.

`runWithConfig` constructs:

1. server;
2. session manager with StartRuntime, CloseRuntime, and `srv.Notify`;
3. turn manager whose Lookup maps runtime fields and sets `BeginWork: rt.BeginWork`;
4. `manager.SetTurnCanceller(turns.CancelAndWait)`;
5. initialize/new/load/close/prompt/cancel handlers.

Register `session/close` with `manager.CloseSession`. Keep `session/cancel` as a notification-compatible handler.

- [ ] **Step 3: Add failing EOF and cleanup-error tests**

Using `runWithConfig` and a fake starter/closer, add:

- `TestRunEOFClosesAllSessionsExactlyOnce`: use an input pipe, create two sessions and wait for both responses, then close the writer to produce EOF and require both close calls once.
- `TestRunContextCancelClosesSessions`: pipe remains open; cancel context and require Run plus closer completion.
- `TestRunReturnsJoinedServeAndCleanupErrors`: inject a close sentinel and a reader/scanner sentinel; require `errors.Is` for both.

Run:

```bash
go test ./internal/acp -run 'TestRun(EOF|ContextCancel|ReturnsJoined)'
```

Expected: FAIL because current Run returns directly from Serve and never closes the manager.

- [ ] **Step 4: Implement bounded final cleanup**

Production `Run` delegates to `runWithConfig` with `app.StartRuntime`, nil/default closer, and five seconds. After `Serve` returns:

```go
closeCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdown)
closeErr := manager.CloseAll(closeCtx)
cancel()
return errors.Join(serveErr, closeErr)
```

Do not discard cleanup errors. The server has already cancelled/joined request handlers before manager closure.

- [ ] **Step 5: Verify and commit**

Run:

```bash
go test -race -count=1 ./internal/acp -run 'TestRun'
```

Expected: PASS.

```bash
git add internal/acp/run.go internal/acp/run_test.go
git commit -m "fix(acp): advertise and clean up session lifecycle"
```

---

### Task 7: Add pipe-level permission, cancellation, replay, and wire regressions

These tests exercise component integration through actual newline-delimited JSON rather than direct method calls.

**Files:**
- Create: `internal/acp/integration_test.go`
- Modify: `internal/acp/server_test.go`

**Test harness interface:**

```go
type wireHarness struct {
	inputWriter *io.PipeWriter
	frames      <-chan map[string]any
	done        <-chan error
}

func newWireHarness(t *testing.T, configure func(*Server)) *wireHarness
func (h *wireHarness) send(t *testing.T, frame any)
func (h *wireHarness) next(t *testing.T) map[string]any
func (h *wireHarness) close(t *testing.T)
```

- [ ] **Step 1: Implement the deterministic wire harness**

Use `io.Pipe` for input and another pipe for output. A scanner goroutine decodes each output line into `map[string]any` and sends it to a buffered channel. `next` has a two-second timeout. `close` closes input and waits for Serve, failing on non-nil errors.

- [ ] **Step 2: Add the permission deadlock regression**

Wire a real `TurnManager` and `PermissionBridge` to the server. The fake runner publishes a pending approval and blocks on its response channel. Build the runtime from a real `*pubsub.Broker[session.Event]` and a `session.State` that exposes `PendingApproval()` and `ResolveApproval(...)`; do not hand-roll an event stream. The harness does not boot `app.Runtime` — pass a fake `RuntimeStarter` that returns a struct satisfying the `*app.Runtime` fields the turn manager reads (`Runner.Run`, `EventBroker`, `SessionID`). Test flow:

1. send `session/prompt` with a text block;
2. read outbound `session/request_permission` and capture its ID;
3. send an approval response using that ID;
4. require the runner receives approval;
5. require the original prompt response returns `end_turn`.

Name: `TestACPWirePermissionResponseDuringPrompt`.

- [ ] **Step 3: Add the in-flight cancellation regression**

The fake runner blocks on context. Send prompt, wait for runner start, send `session/cancel` without an ID, require no cancel response frame, and require the original prompt response contains `stopReason: cancelled` only after the runner's cleanup marker.

Name: `TestACPWireCancelDuringPrompt`.

- [ ] **Step 4: Add session concurrency and duplicate tests**

- `TestACPWireDifferentSessionsPromptConcurrently`: both fake sessions start before either releases; responses correlate to their IDs.
- `TestACPWireDuplicatePromptRejectedWithoutCancellingFirst`: second same-session response has code `-32000`; first remains running and later returns normally.

- [ ] **Step 5: Add replay/null/custom-method tests**

`TestACPWireLoadReplaysBeforeNullResult` sends a load request through a manager backed by a hydrated state. Require all preceding frames use method `session/update`, roles map correctly, the final response has the original ID and a present null result, and no frame method starts with `session/message_`, `session/activity_`, `session/audit_`, or `session/pending_`.

- [ ] **Step 6: Stress response writes and run race tests**

Add `TestACPWireConcurrentFramesRemainValidJSON`: launch 20 different-session handlers that notify and complete in varied order while five outbound requests receive responses. The output decoder must read only valid single-line JSON and every expected ID once.

Run:

```bash
go test -race -count=10 ./internal/acp -run 'TestACPWire'
```

Expected: PASS without timeout, malformed frame, duplicate response, or race.

- [ ] **Step 7: Commit**

```bash
git add internal/acp/integration_test.go internal/acp/server_test.go
git commit -m "test(acp): cover concurrent lifecycle wire flows"
```

---

### Task 8: Document the supported ACP surface and run release gates

**Files:**
- Create: `docs/10-acp.md`
- Modify: `README.md`
- Modify: `CLAUDE.md`
- Modify: `docs/13-project-audit-2026-07-11.md`

- [ ] **Step 1: Write the ACP support matrix**

Document these exact statuses in `docs/10-acp.md`:

- supported: initialize v1, session/new, session/load with active-branch replay, session/prompt, session/cancel, session/close, session/update message/thought chunks, request_permission;
- prompt blocks: text and resource_link;
- concurrency: one prompt per session, multiple sessions concurrently;
- unsupported: dynamic MCP arrays, additional directories, rich media/embedded resources, resume/list/delete, generic request cancellation, full tool/plan updates, ACP v2;
- shutdown: EOF/cancellation closes all active runtimes through the Batch 1 lifecycle.

Link the official ACP v1 initialization, session setup, prompt, content, and cancellation pages already recorded in the design spec.

- [ ] **Step 2: Update top-level documentation**

Add a concise ACP link/status to README and CLAUDE. Do not claim complete ACP v1 conformance; say “ACP v1 conversation lifecycle” and link the limitations matrix.

- [ ] **Step 3: Update only resolved audit findings**

Mark resolved:

- release blocker 2: synchronous ACP permission/cancel deadlock;
- release blocker 3: `session/load` duplicate insert/empty load;
- MCP/runtime finding: replacing an active ACP runtime without closing it;
- MCP/runtime finding: no session-manager shutdown on EOF.

Add newly verified protocol corrections (content arrays, standard updates, truthful capabilities, session close) to the resolution note. Leave ACP resume/list, dynamic MCP, optional tool projections, and every unrelated finding open.

- [ ] **Step 4: Format exact changed Go files**

Run:

```bash
gofmt -w \
  internal/acp/protocol.go internal/acp/protocol_test.go \
  internal/acp/server.go internal/acp/server_test.go \
  internal/acp/turn.go internal/acp/turn_test.go \
  internal/acp/permissions_test.go \
  internal/acp/session.go internal/acp/session_test.go \
  internal/acp/run.go internal/acp/run_test.go internal/acp/integration_test.go \
  internal/db/projects.go internal/db/projects_test.go \
  internal/app/app.go internal/app/runtime.go internal/app/app_test.go \
  internal/app/session/session.go internal/app/session/session_test.go
```

Expected: exit 0 with no unrelated paths touched.

- [ ] **Step 5: Run focused and race suites**

Run:

```bash
go test -count=1 ./internal/acp ./internal/app ./internal/app/session ./internal/db
go test -race -count=1 ./internal/acp ./internal/app ./internal/app/session ./internal/db
```

Expected: PASS.

- [ ] **Step 6: Run full repository gates**

Run:

```bash
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
```

Expected: all commands exit 0.

- [ ] **Step 7: Run mechanical acceptance scans**

Run:

```bash
rg -n 'session/(message_added|thinking_changed|activity_changed|active_tool_changed|audit_added|pending_)' internal/acp
rg -n 'Prompt[[:space:]]+string' internal/acp
rg -n 'WithSessionID\(p\.SessionID\)' internal/acp
rg -n '_ = m\.bridge\.Request|_ = .*\.Close\(' internal/acp
git status --short
```

Expected: the first four searches return no production matches; status lists only intended Batch 2 documentation/code changes and any explicitly preserved pre-existing files.

- [ ] **Step 8: Commit documentation**

```bash
git add docs/10-acp.md README.md CLAUDE.md docs/13-project-audit-2026-07-11.md
git commit -m "docs: describe reliable ACP v1 lifecycle support"
```

---

## Completion Checklist

- [ ] Batch 1 lifecycle APIs exist and are used by ACP runner work.
- [ ] Permission responses and cancellation remain readable during prompt execution.
- [ ] Same-session prompts serialize without implicit replacement; different sessions run concurrently.
- [ ] Cancel joins the runner and returns `cancelled` from the original prompt.
- [ ] Existing-session startup performs no project/session/message upsert and hydrates the active branch.
- [ ] Load replay completes before literal `result: null`.
- [ ] Replaced, closed, and orphaned runtimes close exactly once.
- [ ] Text/resource-link content is accepted; every unadvertised content/root/MCP type is rejected.
- [ ] Only standard `session/update` methods expose turn/replay output.
- [ ] Initialize advertises exactly the implemented optional lifecycle capabilities.
- [ ] EOF/context cancellation releases outbound waiters, handlers, turns, and runtimes within five seconds.
- [ ] Focused tests, race tests, full tests, vet, and CGO build pass.
- [ ] Only Batch 2 audit findings are marked resolved.
