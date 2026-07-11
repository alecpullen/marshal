# ACP Reliability and v1 Lifecycle Design

**Date:** 2026-07-11

## Purpose

This batch repairs Marshal's ACP transport and session boundary. The current server executes request handlers synchronously on its only input loop, so a running `session/prompt` prevents the same loop from reading permission responses or `session/cancel`. Session loading attempts to insert an existing primary key instead of opening persisted state, active runtimes are overwritten without cleanup, and EOF does not close session-owned resources.

The work also corrects the ACP v1 wire contracts directly involved in this boundary: prompt content blocks, standard `session/update` notifications, truthful initialization capabilities, history replay, and the stable `session/close` method.

This design follows the current official ACP v1 documentation:

- [Initialization](https://agentclientprotocol.com/protocol/v1/initialization)
- [Session setup and loading](https://agentclientprotocol.com/protocol/v1/session-setup)
- [Prompt turns](https://agentclientprotocol.com/protocol/v1/prompt-turn)
- [Content blocks](https://agentclientprotocol.com/protocol/v1/content)
- [Cancellation](https://agentclientprotocol.com/protocol/v1/cancellation)

## Dependency

The execution and shutdown safety batch described by `docs/superpowers/specs/2026-07-11-execution-shutdown-safety-design.md` must land first. This ACP batch depends on its runtime-owned work context and these idempotent methods:

```go
func (rt *Runtime) Quiesce(ctx context.Context) error
func (rt *Runtime) Close(ctx context.Context) error
```

ACP must not implement a second, weaker resource-cleanup sequence around the current runtime.

This batch exposes the runtime work gate to external transports through:

```go
func (rt *Runtime) BeginWork(parent context.Context) (workCtx context.Context, finish func(), err error)
```

`BeginWork` registers work before returning, derives a context cancelled by either the caller or the runtime root context, and returns an idempotent `finish` function that releases the work gate. It returns `session.ErrSessionQuiescing` after quiesce begins. ACP must use this method for every runner invocation.

## Goals

1. Keep one dedicated transport reader available while prompt handlers run.
2. Route responses to Marshal-originated JSON-RPC requests without handler starvation.
3. Process `session/cancel` during an active turn and join the cancelled turn.
4. Allow prompts in different sessions to run concurrently while serializing prompts within one session.
5. Load an existing persisted session without inserting or replacing its database row.
6. Replay the active persisted conversation branch through standard ACP notifications before `session/load` returns.
7. Close replaced, explicitly closed, and transport-orphaned runtimes deterministically.
8. Accept ACP v1 text and resource-link prompt blocks and reject unsupported content honestly.
9. Emit standard `session/update` frames rather than Marshal-specific notification methods.
10. Advertise only the ACP capabilities implemented by this batch.

## Non-goals

This batch does not add:

- `session/resume`, `session/list`, or `session/delete`;
- `$/cancel_request` support beyond the required session-specific cancel path;
- image, audio, or embedded-resource prompt handling;
- dynamic client-supplied MCP servers;
- additional workspace roots;
- HTTP, SSE, or WebSocket ACP transports;
- complete ACP tool-call/plan/config-option projection;
- optional ACP elicitation/structured-question support;
- ACP v2 draft behavior;
- fixes from later audit batches such as privacy, symlink confinement, SSRF, or SQLite migrations.

Non-goals are rejected or omitted truthfully. They are not accepted and silently ignored.

## Chosen approach

`Server.Serve` remains the owner of the input stream, but it becomes a frame router rather than a handler executor. It decodes and classifies every frame immediately:

- a response matching a Marshal-originated request is delivered to its waiter;
- an inbound request is dispatched in a tracked goroutine and writes exactly one response;
- an inbound notification is dispatched in a tracked goroutine and never writes a response;
- malformed frames receive the appropriate JSON-RPC parse error when a response is permitted.

Concurrency policy does not live in the transport. `TurnManager` owns one explicit active-turn slot per session. `SessionManager` owns runtime creation, loading, replacement, closing, and shutdown.

This is preferred over a worker pool because cancellation and permission-response traffic must never queue behind long prompt handlers. It is preferred over special-casing only `session/prompt` because any future long-running handler would recreate the same deadlock.

## Components

### Transport frame routing

The wire decoder will distinguish requests, notifications, and responses by structure rather than treating every frame with an ID as a request. A response has an ID, no method, and a result or error. An unmatched response is ignored as a peer/protocol error; Marshal must not answer a response with another response.

`Server` will own:

```go
type outboundResult struct {
	response *Response
	err      error
}

type Server struct {
	// existing scanner/encoder/handler fields
	ctx       context.Context
	cancel    context.CancelFunc
	handlerWG sync.WaitGroup

	outMu      sync.Mutex
	outbound   map[string]chan outboundResult
	outboundID uint64

	shutdownOnce sync.Once
	shutdownErr  error
}
```

The exact private layout may be adjusted to avoid copying mutexes, but these ownership boundaries are required.

`Request` registers its waiter before writing the outbound request. The read loop removes a matched waiter and delivers its result. Context cancellation removes only that request's waiter. Server shutdown atomically removes every remaining waiter and delivers the terminal transport error, so permission handlers never remain blocked after EOF.

All encoder writes remain serialized through one mutex. Handler completion order may differ from request arrival order; response IDs preserve correlation.

The scanner retains the existing one-megabyte maximum frame size. Parent cancellation closes the input when it implements `io.Closer`, which includes the production stdin handle and test pipes. For a non-closable reader, `Serve` may return after cancellation while its blocked scanner goroutine exits only when that reader eventually returns; no handler, runtime, or outbound waiter may remain attached to it.

The server accepts no new frames after shutdown begins. It cancels handler contexts, fails outbound waiters, and waits up to five seconds for handlers. A handler that violates cancellation produces a bounded shutdown error rather than hanging the process.

### JSON-RPC errors and null results

The protocol layer will use typed errors so expected request failures become stable wire codes:

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
```

- `-32602` covers malformed and unsupported method parameters.
- `-32601` covers unknown methods.
- `-32800` covers a request cancelled because its transport/runtime context ended.
- `-32000` covers unknown sessions, active-turn conflicts, and lifecycle failures safe to expose to the client.
- unexpected internal failures remain `-32603`.

A successful handler that returns nil must encode a literal `"result": null`; it must not omit the result field. Error responses omit result and include error. Notifications never receive responses, even when their parameters are invalid.

### ACP initialization

The initialization handler validates the requested protocol version and returns version 1 with truthful capabilities:

```json
{
  "protocolVersion": 1,
  "agentCapabilities": {
    "loadSession": true,
    "sessionCapabilities": {
      "close": {}
    }
  },
  "agentInfo": {
    "name": "marshal",
    "title": "Marshal"
  },
  "authMethods": []
}
```

Marshal does not advertise image, audio, embedded-context, additional-directory, resume, list, delete, HTTP-MCP, or SSE-MCP support.

### Prompt content normalization

The prompt request becomes:

```go
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

type PromptTurnParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}
```

Normalization preserves block order:

- a text block requires non-empty text and contributes it exactly;
- a resource link requires both name and URI and contributes `Resource link: <name> (<uri>)`;
- normalized contributions are separated by two newlines;
- an empty array, an empty normalized goal, missing required fields, or any unadvertised block type returns `-32602`;
- resource links are not fetched by the ACP layer and do not broaden filesystem or network authority.

Supporting resource links means retaining their reference for the agent, not dereferencing an arbitrary URI.

### Standard session updates

Every client-visible turn/replay notification uses this envelope:

```json
{
  "jsonrpc": "2.0",
  "method": "session/update",
  "params": {
    "sessionId": "sess_123",
    "update": {
      "sessionUpdate": "agent_message_chunk",
      "content": {
        "type": "text",
        "text": "message"
      }
    }
  }
}
```

Projection rules are:

- user messages become `user_message_chunk`;
- assistant messages become `agent_message_chunk`;
- system notices become `agent_message_chunk` because they are agent-originated user-visible output;
- streaming/model reasoning becomes `agent_thought_chunk`;
- internal activity, audit, active-tool, pending-approval, and pending-question events do not emit invented methods.

Optional message IDs are omitted. Marshal's in-memory IDs are reconstructed during load and are not yet a stable external identity. Tool-call and plan projection remains a later ACP feature because current session events do not preserve all mandatory ACP identifiers and status fields.

Live turn subscriptions use `pubsub.WithTerminal[session.Event]()` so message updates cannot be dropped by the broker's best-effort buffer. Backpressure may slow the runner when the editor cannot consume required output, but it cannot silently lose completed messages.

Permission events remain internal triggers. A non-nil pending approval invokes `session/request_permission`; the custom `session/pending_approval_changed` notification is removed. A permission request/response transport failure cancels the turn and is returned instead of being ignored.

ACP v1 baseline has no structured-question response method. Until optional elicitation support is implemented, a pending question is immediately resolved with one `session.Answer{Answer: session.AnswerUnanswered}` per question. This prevents `question.ask` from hanging a headless turn and uses the runner's existing best-judgment fallback.

### Per-session turn lifecycle

`TurnManager` will store a structured active turn:

```go
type activeTurn struct {
	cancel context.CancelFunc
	done   chan struct{}
}
```

Registration is atomic under the active-turn mutex. If the session already has an active turn, a second `session/prompt` returns `-32000` with `session already has an active turn`; it never cancels or replaces the first prompt. Prompts for distinct sessions register distinct slots and may run concurrently.

The runtime adapter exposed to `TurnManager` includes the work-gate function:

```go
type TurnRuntime struct {
	SessionID string
	BeginWork func(context.Context) (context.Context, func(), error)
	Run       RunnerFunc
	Events    *pubsub.Broker[session.Event]
}
```

After reserving the slot, `PromptTurn` calls `TurnRuntime.BeginWork`. The runner receives the returned context and always defers the returned `finish` function. Consequently runtime `Close` cancels and joins ACP work just as it does TUI work. Failure to register because quiesce has begun becomes `-32800`.

The turn context derives from the server/runtime session lifetime. On normal completion, the prompt returns `{"stopReason":"end_turn"}`. A client `session/cancel` notification:

1. looks up the active turn;
2. calls its cancel function;
3. waits for `done` or transport shutdown;
4. causes the original prompt to return `{"stopReason":"cancelled"}`.

Cancelling an idle or unknown session is a notification no-op. Turn cleanup deletes the map entry only when it still points to the same `activeTurn`, preventing an old defer from removing a newer slot.

`TurnManager` also exposes a transport-internal join operation:

```go
func (m *TurnManager) CancelAndWait(ctx context.Context, sessionID string) error
```

Session replacement and `session/close` call this operation. The public `session/cancel` handler delegates to it but treats an absent turn as success.

The event-forwarding loop joins the runner goroutine before returning. It drains already-published terminal events, but does not wait indefinitely for new events after the runner ends.

### Runtime create and existing-session load modes

`app.StartRuntime` needs an explicit load mode rather than overloading an ID option:

```go
func WithExistingSession(id string) Option

func (db *DB) GetProjectByRoot(rootPath string) (Project, error)
```

`WithSessionID(id)` retains its current meaning: create a new session using a caller-supplied ID. `WithExistingSession(id)` means:

1. require a non-empty ID;
2. open and migrate the workspace database;
3. resolve the existing workspace project with the read-only `GetProjectByRoot` lookup rather than `GetOrCreateProject`;
4. call `DB.GetSession(id)`;
5. require the stored `ProjectID` to equal the resolved project;
6. skip `DB.CreateSession`;
7. construct `session.State` with the existing ID so its current active branch is hydrated by `loadFromDB`.

Missing projects, missing sessions, and project mismatches return errors without creating or updating project/session/message rows. Schema migration remains the normal startup prerequisite. The mode is usable by ACP and future headless transports; it contains no ACP-specific notification logic.

`session/new` and `session/load` require an absolute `cwd` and an explicit `mcpServers` array. Empty MCP arrays are accepted. Non-empty MCP arrays and non-empty additional-directory arrays return `-32602` because this batch does not implement them.

### Session manager ownership and replay

`SessionManager` owns every active `*app.Runtime`. Lifecycle operations are serialized so two create/load/close operations cannot publish conflicting runtimes under one ID.

Runtime replacement must join the active turn before closing its resources. To avoid coupling `SessionManager` directly to `TurnManager`, the manager accepts a callback after construction:

```go
type TurnCanceller func(context.Context, string) error

func (m *SessionManager) SetTurnCanceller(cancel TurnCanceller)
```

`acp.Run` constructs the session manager, constructs the turn manager from its lookup, and wires `turns.CancelAndWait` into the session manager before registering lifecycle handlers. Production lifecycle operations fail closed if this callback was not configured; unit tests may provide a fake.

Loading proceeds as follows:

1. validate the request;
2. remove any active runtime under the requested ID from visibility;
3. cancel and join its active turn through the configured `TurnCanceller`;
4. close the old runtime completely;
5. call `StartRuntime` with `WithWorkingDir(cwd)` and `WithExistingSession(id)`;
6. install the new runtime;
7. replay `rt.State.Messages()` in active-branch order through direct, synchronous `session/update` writes;
8. return nil so the transport emits literal `result: null`.

If runtime startup fails, no runtime is installed. If replay fails, the newly installed runtime is removed and closed before the load error is returned. A prompt cannot validly target the loaded session until the client receives the load response; the manager nevertheless remains concurrency-safe against an out-of-order client.

The manager exposes lifecycle operations equivalent to:

```go
func (m *SessionManager) Get(id string) (*app.Runtime, bool)
func (m *SessionManager) Close(ctx context.Context, id string) error
func (m *SessionManager) CloseAll(ctx context.Context) error
```

`session/close` removes the runtime from visibility, cancels and joins the session turn, then closes the runtime. Success returns `{}`. Closing an unknown session returns a stable `-32000` error, which ACP explicitly permits.

`CloseAll` atomically removes all runtimes from visibility, invokes the configured `TurnCanceller` for every session, closes each runtime exactly once with the same bounded context, attempts every cancellation and close, and returns `errors.Join` of independent failures.

### Connection shutdown

`acp.Run` owns both the server and session manager. Regardless of whether `Serve` ends through EOF, scanner error, output error, or context cancellation, it:

1. cancels server handlers and outbound requests;
2. waits for handler shutdown within five seconds;
3. calls `SessionManager.CloseAll` with a five-second context;
4. returns joined transport and cleanup errors.

Batch 1 runtime closure ensures active model calls, background jobs, MCP clients, brokers, snapshots, database, and logging are handled in their correct order.

## Data flow

```text
editor session/prompt
  -> dedicated read loop decodes frame
  -> tracked request handler
  -> TurnManager atomically reserves session slot
  -> normalize ContentBlock[]
  -> terminal session-event subscription
  -> runtime Runner.Run
      -> session/update notifications
      -> session/request_permission outbound request
          -> dedicated read loop routes editor response
  -> join runner and drain published updates
  -> prompt response: end_turn or cancelled

editor session/load
  -> validate cwd/session/MCP roots
  -> close active runtime with same id
  -> StartRuntime(WithExistingSession)
  -> hydrate active DB branch
  -> ordered session/update replay
  -> result: null

EOF / transport cancellation
  -> stop frame dispatch
  -> cancel handlers + fail outbound waiters
  -> join handlers
  -> close every session runtime
```

## Error handling

- A malformed JSON frame yields `-32700` with `id: null`.
- A structurally invalid request yields `-32600`.
- Unknown methods yield `-32601`.
- Invalid lifecycle or prompt parameters yield `-32602`.
- A duplicate active prompt yields `-32000` without disturbing the original.
- A missing/cross-project session load yields `-32000` and does not create a row.
- Intentional `session/cancel` returns `stopReason: cancelled` on the original prompt.
- Transport/runtime cancellation of an ordinary request yields `-32800` when the response can still be written.
- Permission failures cancel the runner and surface as a turn error; they are never discarded.
- Replay write failure removes and closes the just-loaded runtime.
- Replacement and connection shutdown continue closing all resources after an individual close failure.
- Repeated manager/server/runtime shutdown is safe and does not panic.

## Testing strategy

### Transport tests

- A real `io.Pipe` conversation proves a permission response is read while `session/prompt` remains active.
- `session/cancel` is read and dispatched during a prompt.
- Requests that finish out of order retain their correct response IDs.
- Notifications never receive responses.
- A nil handler result encodes `result: null`.
- Unmatched responses are ignored rather than answered as unknown requests.
- EOF/context cancellation fails outbound waiters.
- Shutdown joins tracked handlers or returns after its five-second bound.
- Concurrent response/notification/outbound-request writes remain valid newline-delimited JSON under `-race`.

### Turn tests

- Two sessions run concurrently.
- A second prompt for one session receives the active-turn error and leaves the first running.
- Cancel waits for runner exit and the prompt returns `cancelled`.
- Normal completion returns `end_turn`.
- Permission response round-trips without deadlock.
- Permission failure cancels and joins the runner.
- Pending structured questions are answered with `AnswerUnanswered` and do not hang.
- Only standard `session/update` notifications appear on the wire.
- Terminal subscriptions preserve all emitted message updates.

### Runtime and session tests

- `WithExistingSession` loads without inserting a duplicate session row.
- The active persisted branch, reasoning, content type, and leaf continue correctly after load.
- Missing and cross-project session IDs fail without mutation.
- `session/load` emits ordered user/agent chunks before literal `result: null`.
- Replay failure closes and removes the new runtime.
- Replacement closes the old runtime before the new one is visible.
- `session/close` cancels active work and closes once.
- EOF calls `CloseAll`; every runtime is closed once even when one close fails.

### Protocol tests

- Initialize advertises `loadSession` and `sessionCapabilities.close`, and no unsupported capability.
- Text blocks preserve order and content.
- Resource-link blocks retain name and URI without fetching them.
- Empty, malformed, image, audio, embedded-resource, non-empty MCP, and additional-root inputs return `-32602`.
- Success-null and error response shapes are JSON-RPC compliant.

### Verification commands

```bash
go test -count=1 ./internal/acp ./internal/app ./internal/app/session
go test -race -count=1 ./internal/acp ./internal/app ./internal/app/session
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
```

## Documentation changes

- Add an ACP section to the user/developer documentation describing supported v1 methods and capabilities.
- Document that prompt support is text plus resource links only.
- Document `session/load` replay, `session/close`, per-session prompt serialization, and cancellation behavior.
- Mark only the ACP deadlock, session-load, runtime replacement, and EOF-cleanup audit findings resolved.
- Leave optional ACP methods, dynamic MCP, full tool updates, and all unrelated audit findings open.

## Acceptance criteria

1. A permission response and `session/cancel` are processed while their prompt handler is active.
2. At most one prompt runs per session; distinct sessions can run concurrently.
3. Cancellation joins the runner and returns `stopReason: cancelled` from the original prompt.
4. `session/load` never inserts an existing or missing session and replays the active persisted branch before returning `result: null`.
5. Replaced, explicitly closed, and transport-orphaned runtimes are closed exactly once.
6. Prompt input uses ACP v1 content blocks and rejects every unadvertised type.
7. Client-visible turn/replay events use only standard `session/update` envelopes.
8. Initialize advertises only `loadSession` and `sessionCapabilities.close` from the optional capabilities in scope.
9. EOF and context cancellation release every outbound waiter and active handler within the shutdown bound.
10. Targeted race tests, the full test suite, vet, and the CGO build pass after Batch 1 is present.
