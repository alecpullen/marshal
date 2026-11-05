# P3 plan 1: Client core — implementation plan

Date: 2026-11-05. Status: ready for review.
Base: `main` at `5acdd89c` (P2 plan 7b's plan commit, while plan 7b executes). Every anchor below was checked there.
- The JSON literals in Contracts were produced by encoding declarations with exactly the tags given. JSON key order is not significant.
- **This plan runs in parallel with P2 plan 7b** (decided with the user, 2026-11-05). It touches no file plan 7b touches, apart from `AGENTS.md`, where the two edit different sections.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R1 (packages), R2 (wire protocol), R10 (budgets), R11 (embedded mode), Migration order item 1, and tests V6 to V8. Its "Decided with the user" list records the reordering that puts this plan first.
Previous plan: none in P3. The P2 seam this builds towards is [p2-client-seam.md](../p2-client-seam.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** a JSON-RPC client that can talk to the ACP server over an in-memory pipe, and the protocol pieces both ends need.
- `internal/acpclient/rpc` (new): the transport, the pipe, the connection, and `Dial` with the `initialize` handshake.
- `internal/acpwire` gains the `initialize`, `$/progress` and `$/cancel_request` types.
- The ACP server gains error data for `*acpwire.Error`, request cancellation, progress notifications, and the marshal capability.

**Non-goals:**
- **No interface method goes over the wire yet.** `rpc.Host` implements none of `acpclient.Host`'s methods beyond `SharedFilesystem`. Each later plan adds its group's methods.
- **No mirror, no update kinds and no read-your-writes ordering.** P3 plan 3 onwards.
- **No layering.** `launch`, the per-session service and `inproc`'s move wait for P2 to close (P3 plan 2).
- **No existing handler changes behaviour.** No handler opts into cancellation, none reports progress, and none returns an `*acpwire.Error` today, so existing clients see the same frames. The one addition they can see is `agentCapabilities.marshal` in `initialize`'s result.
- **No TUI change** beyond one line in a boundary test. **No change to `docs/daemon/p2-client-seam.md`**: plan 7b edits it.
- **No bridge change.** No changelog entry.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-1-suite`. Create the directory first: `mkdir -p /private/tmp/p3-1-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 7b uses `/private/tmp/p2-7b-suite`. Never share a directory between the two runs.
- **The base is green.** At `341f1941`, all 117 packages passed, `web/bridge` included, and `5acdd89c` adds only documents. `web/bridge`'s `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` has hung about one run in three since `90488446`. The hang is in its fake agent (`fakeagent_test.go`), not in product code, so treat a hang there as known.
- **Two suites may run at once.** Plan 7b executes alongside this plan. Load makes the flakes listed under Task 5 likelier. Rerun a failure alone before treating it as new.
- **The server today** (`internal/acp/server.go`):
  - reads frames with a `bufio.Scanner` capped by the exported `MaxLineBytes` field, 1 MiB when unset (`scanLines`, `:308`);
  - runs each request's handler on its own goroutine with the connection's context (`handleFrame`, `:333`; `dispatchRequest`, `:483`);
  - turns every handler error into a JSON-RPC error with no data. `dispatch` (`:594`) wraps it with `codeFor`, and `wireError` (`:450`) makes the message opaque, except for `serverError` (`-32000`), whose message is `server error: <message>`;
  - sends its own requests with string IDs `marshal-out-N` and waits for the client's response (`Request`, `:175`).
- **Handler contexts outlive their requests.** `/mcp auth` starts its sign-in on a goroutine derived from the request's context and returns at once (`mcpauth.go:124`). So cancelling every request's context when its handler returns would end that flow. This is why cancellation is opt-in (decision 4).
- **No handler returns an `*acpwire.Error` today.** `grep -rn 'acpwire.Error' internal/acp --include='*.go'` finds none outside tests, so the new error data appears only on errors later plans add.
- **`initialize`** is registered per connection in `registerHandlers` (`host.go:298`). It reads `InitializeParams` (`run.go:19`), which carries only `protocolVersion`, and answers with `agentInfo.version` set to `AgentVersion` (`host.go:349`). The bridge reads `agentCapabilities` as a map of names (`web/bridge/control.go:180`), so a new key is harmless.
- **`io.Pipe` writes block until the other end reads.** Both ends of the pipe must keep reading for as long as the connection lives. The server's scanner goroutine does; the client's read goroutine must too.

**Decisions this plan makes beyond the spec** (all are recorded in the spec):

1. **The client core comes first and runs alongside P2** (decided with the user, 2026-11-05). It touches only `acpclient/rpc`, the ACP server and `acpwire`'s new types, which no P2 plan still changes. Layering moves `inproc` and splits `app.Run`, so it waits for P2 to close and becomes P3 plan 2.
2. **The transport is a frame stream; the pipe's agent end is a byte stream.** `Transport` sends and receives whole frames. `NewStream` frames any reader and writer with newlines, as `marshal acp` speaks on stdio. `Pipe` returns the client's `Transport` and the agent's `io.ReadWriteCloser`, which `acp.NewServer` reads and writes unchanged. The client's end caps frames at `MaxFrameBytes` (64 MiB). The agent's end sets `Server.MaxLineBytes` itself (R2: neither inherits the default).
3. **An `*acpwire.Error` travels as itself.** The JSON-RPC error's code is `-32000`, its message is the server's usual `server error: <message>`, and its `data` is the `*acpwire.Error` encoded whole: code, message and data. The client rebuilds it from `data`, so `acpclient.IsCode`, `NoticeOf` and `err.Error()` read exactly as in process. This replaces R2's "data `{"code": …, …}`, the rest of the data being its `ErrorData`": the error's own data is nested, not flattened.
4. **Cancellation is opt-in per handler.**
   - The server registers each request before it starts the handler, and handles `$/cancel_request` on its read goroutine, in arrival order. So a cancel that follows its request is never missed.
   - A handler that can be cancelled derives its context with `cancellable(ctx)`. The derived context ends on a cancel, as well as when the connection ends.
   - Every other handler keeps the connection's context, as today. A cancel for an unknown or finished request is ignored.
   - The handler's context carries its request as a value, which registers nothing with the connection's context, so a long connection leaks nothing per request.
5. **Progress goes through the handler's context.** `reportProgress(ctx, value)` sends `$/progress` for the context's request through the server's writer. The handler sends it on its own goroutine before it returns, so every value precedes the response on the wire. On a context with no request (a notification's), it does nothing.
6. **The client applies frames in arrival order.** One goroutine reads every frame:
   - a notification goes to `Handlers.Notify`, and a `$/progress` value to its call's progress function, both synchronously;
   - a response goes to its waiting call.

   So a call returns only after every frame before its response has been handled: R2's client half. Neither function may block on the connection. An agent request runs `Handlers.Request` on its own goroutine, so a slow answer never stops the reads.
7. **A call whose context ends sends `$/cancel_request` and returns at once.** It does not wait for the agent. A response that arrives later is dropped.
8. **The connection ends on any read or write failure.** An oversized frame, a closed pipe or an EOF ends it: every pending call fails with an error that wraps `ErrClosed` and the cause, `Done` closes, and `Err` reports why. R11's "never with a hang" rests on this.
9. **`initialize` declares and advertises the marshal protocol.**
   - The client sends `clientCapabilities.marshal` with `MarshalProtocolVersion` (1).
   - The agent advertises `agentCapabilities.marshal` with the same version to every client, and records on the server whether this client declared it. Later plans gate the marshal-only update kinds on that record (R2).
   - `Dial` refuses an agent that does not advertise the version. When `DialOptions.AgentVersion` is set, it also refuses an agent that reports another version: embedded mode passes its own, because both ends are one binary.
10. **`rpc.Host` starts small.** It holds the connection, the agent's info and `SharedFilesystem`. Each group's plan adds its methods. The compile-time check that it implements `acpclient.Host` arrives with the last.
11. **The conformance harness (V1) moves to P3 plan 3.** It runs one test table against `inproc` and the client, which needs the per-session service that layering moves agent side. This plan's tests prove the protocol against the real server instead.
12. **The pipe's round trip is measured now.** A benchmark of one trivial call over the pipe to the real server checks R10's "a synchronous call adds under 1ms" before any group depends on it.

**Execution:**
- Branch first: `git switch -c p3-1-client-core` from `main`.
- If `git status` shows `docs/daemon/p3-acp-transport.md` or this plan uncommitted (the spec is untracked), commit them first as explicit paths, with the message `p3-1-client-core: record the P3 spec and plan 1`. The main checkout holds other untracked files (`scripts/lmstudio-bench/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p3-1-client-core: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Wire types (`internal/acpwire`)

A new file, `initialize.go`:

```go
// MarshalProtocolVersion is the marshal-only protocol both ends declare in
// initialize (P3 R2).
const MarshalProtocolVersion = 1

// The protocol's own methods (P3 R2).
const (
	MethodProgress      = "$/progress"
	MethodCancelRequest = "$/cancel_request"
)

// MarshalCapability declares the marshal-only protocol in initialize.
type MarshalCapability struct {
	ProtocolVersion int `json:"protocolVersion"`
}

// ClientCapabilities is the part of initialize's clientCapabilities the
// marshal client sends.
type ClientCapabilities struct {
	Marshal *MarshalCapability `json:"marshal,omitempty"`
}

// InitializeParams is initialize's params as the marshal client sends them.
type InitializeParams struct {
	ProtocolVersion    int                 `json:"protocolVersion"`
	ClientCapabilities *ClientCapabilities `json:"clientCapabilities,omitempty"`
}

// AgentInfo names the agent in initialize's result. Version is empty for a
// build from source.
type AgentInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// AgentCapabilities is the part of initialize's agentCapabilities the
// marshal client reads.
type AgentCapabilities struct {
	Marshal *MarshalCapability `json:"marshal,omitempty"`
}

// InitializeResult is the part of initialize's result the marshal client
// reads.
type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentInfo         AgentInfo         `json:"agentInfo"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
}

// ProgressParams is $/progress's params: one progress value for the request
// whose id it names.
type ProgressParams struct {
	ID    json.RawMessage `json:"id"`
	Value json.RawMessage `json:"value"`
}

// CancelRequestParams is $/cancel_request's params.
type CancelRequestParams struct {
	ID json.RawMessage `json:"id"`
}
```

Encodings:
- the client's `initialize` params: `{"protocolVersion":1,"clientCapabilities":{"marshal":{"protocolVersion":1}}}`;
- a cancel: `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"id":7}}`;
- a progress value: `{"jsonrpc":"2.0","method":"$/progress","params":{"id":7,"value":{"url":"https://example.test/auth"}}}`;
- an `*acpwire.Error{Code: CodeBusy, Message: "A turn is running."}` as a response: `{"jsonrpc":"2.0","id":7,"error":{"code":-32000,"message":"server error: A turn is running.","data":{"code":"busy","message":"A turn is running."}}}`.

### Client (`internal/acpclient/rpc`, new)

```go
// MaxFrameBytes is the largest frame the pipe's client end reads (P3 R2).
// The agent's end sets the same cap on its server.
const MaxFrameBytes = 64 << 20

// ErrClosed is wrapped by every error a call returns because the connection
// ended.
var ErrClosed = errors.New("rpc: connection closed")

// Transport is a framed, two-way message stream (P3 R1).
type Transport interface {
	// Send writes one frame: one complete JSON value, without a newline.
	Send(frame []byte) error
	// Recv returns the next frame. It returns io.EOF once the other end has
	// closed cleanly, and another error when the stream breaks or a frame is
	// larger than the transport's cap.
	Recv() ([]byte, error)
	// Close ends both directions. A Recv blocked on the other end returns.
	Close() error
}

// NewStream frames a byte stream with newlines, as `marshal acp` speaks on
// stdio. maxFrame bounds one received frame. Close closes r and w when they
// are io.Closers.
func NewStream(r io.Reader, w io.Writer, maxFrame int) Transport

// Pipe returns the two ends of an in-memory connection: the client's
// transport, capped at MaxFrameBytes, and the agent's byte stream, which
// acp.NewServer reads and writes. Closing either end ends both directions.
func Pipe() (Transport, io.ReadWriteCloser)

// Handlers are what a Conn does with the frames the agent starts.
type Handlers struct {
	// Notify receives every notification except $/progress, in arrival
	// order, on the connection's read goroutine. It must not block on the
	// connection. Nil drops them.
	Notify func(method string, params json.RawMessage)
	// Request answers an agent-to-client request, on its own goroutine. Its
	// context ends when the connection does. Nil answers every request with
	// method not found.
	Request func(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// Conn is one JSON-RPC connection over a Transport.
type Conn struct{ /* unexported */ }

// NewConn starts reading t and returns the connection.
func NewConn(t Transport, h Handlers) *Conn

// Call sends a request and waits for its response, decoding the result
// into result unless result is nil. A nil params sends no params.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error

// CallWithProgress is Call, handing each $/progress value for the request
// to progress, on the read goroutine, before Call returns.
func (c *Conn) CallWithProgress(ctx context.Context, method string, params, result any, progress func(value json.RawMessage)) error

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error

// Close ends the connection and closes its transport.
func (c *Conn) Close() error

// Done is closed when the connection has ended.
func (c *Conn) Done() <-chan struct{}

// Err is nil while the connection lives. Afterwards it is ErrClosed after
// Close, or an error that wraps ErrClosed and the cause.
func (c *Conn) Err() error

// Error is a JSON-RPC error that carries no acpwire code.
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string // the message

// DialOptions configure Dial.
type DialOptions struct {
	Handlers Handlers
	// SharedFilesystem is what Host.SharedFilesystem reports: true for the
	// pipe.
	SharedFilesystem bool
	// AgentVersion, when not empty, is the version the agent must report.
	AgentVersion string
}

// Dial starts a connection over t, runs initialize, and returns the host. On
// any failure it closes the connection.
func Dial(ctx context.Context, t Transport, opts DialOptions) (*Host, error)

// Host is the client's side of the agent host. Each P3 group plan adds its
// methods.
type Host struct{ /* unexported: the Conn, the agent info, the flag */ }

func (h *Host) SharedFilesystem() bool
func (h *Host) Agent() acpwire.AgentInfo
func (h *Host) Close() error
func (h *Host) Done() <-chan struct{}
func (h *Host) Err() error
```

Unexported names the steps use: the connection's `pending` map of waiting calls by ID; `send`, which writes one frame under a mutex; `readLoop`; and `fail(cause error)`, which ends the connection once.

### Server (`internal/acp`)

- `Error` (`protocol.go:60`) and `jsonRPCError` (`:67`) gain `Data json.RawMessage`. `Error`'s tag is `json:"data,omitempty"`.
- `Server` gains:
  - an in-flight registry: a map from a request ID's compact JSON to its `inflight` entry, guarded by `stateMu`;
  - a `marshal` flag, read with `marshalClient() bool` and set with `setMarshalClient(bool)`.
- `inflight` is unexported. It holds the request's raw ID, its server, and a channel closed once when the request is cancelled.
- `cancellable(ctx context.Context) (context.Context, context.CancelFunc)`: a context derived from `ctx`. It also ends when `ctx`'s request is cancelled, at once if it already was. It needs no request: on a context without one, it is `context.WithCancel`. The caller must call the cancel function.
- `reportProgress(ctx context.Context, value any) error`: sends `$/progress` for `ctx`'s request. It does nothing and returns nil when `ctx` carries no request.
- `InitializeParams` (`run.go:19`) gains `ClientCapabilities *acpwire.ClientCapabilities` with the tag `json:"clientCapabilities,omitempty"`.

### Pinned strings

- `ErrClosed`'s message: `rpc: connection closed`. A wrapped cause reads `rpc: connection closed: <cause>`.
- `Dial`, an agent without the capability: `rpc: the agent does not speak marshal protocol 1`.
- `Dial`, a version mismatch: `rpc: agent version "<got>", want "<want>"`.
- `Dial`, a failed `initialize`: `rpc: initialize: <err>`.
- A frame larger than a stream's cap: `rpc: frame larger than <n> bytes`, wrapping `bufio.ErrTooLong`.
- The client's answer to an agent request with no handler: JSON-RPC code `-32601`, message `method not found: <method>`.

## Task 1: Wire types and the transport

**Goal:** the `initialize` and protocol types exist, and frames cross a stream and the pipe in both directions.

**Files:**
- `internal/acpwire/initialize.go` (new);
- `internal/acpclient/rpc/transport.go` (new), `internal/acpclient/rpc/transport_test.go` (new), `internal/acpclient/rpc/main_test.go` (new).

**Steps:**

1. **Wire types.** Add the declarations in Contracts. `TestWireFieldsFollowTheRules` must pass with them.
2. **`NewStream`.**
   - `Recv` reads one newline-terminated line with a `bufio.Scanner` sized to `maxFrame`. It skips empty lines, as the server's `scanLines` does, and returns a copy of each frame: the scanner reuses its buffer.
   - It returns `io.EOF` at a clean end. A line over the cap is the pinned error, wrapping `bufio.ErrTooLong`. Any other read error is returned as is.
   - `Send` writes the frame and its newline in a single `Write`, so frames never interleave on an `io.Pipe`.
   - `Close` closes the reader and the writer when each is an `io.Closer`, and is safe to call twice.
3. **`Pipe`.** Two `io.Pipe`s, one per direction.
   - The client's end is `NewStream` over the agent-to-client reader and the client-to-agent writer, capped at `MaxFrameBytes`.
   - The agent's end reads the client-to-agent pipe, writes the agent-to-client pipe, and its `Close` closes both. So when the server stops, the client's `Recv` returns `io.EOF`.
4. **`main_test.go`** calls `testenv.Run`, as `internal/acpclient/inproc/main_test.go` does.
5. **Tests** (`transport_test.go`):
   - `TestPipeCarriesFramesBothWays`: a frame sent from each end arrives whole at the other, and a 3 MiB frame from the agent's end arrives whole at the client's.
   - `TestStreamRefusesAnOversizedFrame`: over a stream capped at 1 KiB, a 2 KiB line makes `Recv` return an error for which `errors.Is(err, bufio.ErrTooLong)` holds.
   - `TestPipeCloseEndsBothDirections`: closing the agent's end makes the client's `Recv` return `io.EOF`, and closing the client's end makes a read on the agent's end return EOF.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpwire/ ./internal/acpclient/rpc/
TMPDIR=/private/tmp/p3-1-suite go test ./internal/acpwire/ ./internal/acpclient/rpc/ -count=1 -race
```

Expected: both pass, `TestWireFieldsFollowTheRules` included.

## Task 2: The connection

**Goal:** a `Conn` makes calls, hands the agent's notifications and requests to its handlers, maps errors, cancels and reports progress, and ends cleanly.

**Files:** `internal/acpclient/rpc/conn.go` (new), `internal/acpclient/rpc/conn_test.go` (new).

**Steps:**

1. **IDs and frames.**
   - The connection numbers its requests from 1 with JSON integers.
   - Every frame it sends carries `"jsonrpc":"2.0"`, and a call whose params are nil sends no `params`.
   - The read goroutine classifies each frame as the bridge's `readLoop` does (`web/bridge/child.go:442`): a method with an ID is an agent request, a method without one is a notification, and an ID without a method is a response. A frame that does not parse, or a response to no waiting call, is dropped.
2. **Calls.** `Call` registers a waiter under the new ID, sends the request, and waits for the response, the context's end, or the connection's end.
   - A result decodes into `result` unless `result` is nil.
   - **Errors.** A response error with code `-32000` whose `data` decodes as an `acpwire.Error` with a code returns that `*acpwire.Error`. Any other response error returns an `*Error` with its code and message.
   - **Cancellation.** When the context ends first, remove the waiter, send `$/cancel_request` with the call's ID, ignoring a send error, and return the context's error.
   - **Connection end.** The connection's end returns its error (decision 8). A call made after the end returns that error at once, without sending.
   - `CallWithProgress` registers its progress function with the waiter. The read goroutine hands it each `$/progress` value whose ID matches, in arrival order. A value for a call that is no longer waiting is dropped.
3. **Notifications.** Every notification except `$/progress` goes to `Handlers.Notify`, synchronously, on the read goroutine (decision 6).
4. **Agent requests.** `Handlers.Request` runs on a new goroutine, with a context that ends when the connection does. Its answer is written back with the request's raw ID:
   - a result as `result`, `null` when nil;
   - an `*acpwire.Error`, found with `errors.As`, as code `-32000`, its own message, and its encoding as `data`, the server's form (decision 3);
   - any other error as code `-32603` with the error's text;
   - with no handler, the pinned `method not found` error.
5. **The end.** `fail(cause)` runs once:
   - it records the error, wrapping `ErrClosed` and the cause, or plain `ErrClosed` after `Close`;
   - it closes the transport, fails every waiting call with the error, cancels the agent requests' context, and closes `Done`.

   The read goroutine calls it on any `Recv` error, and `send` calls it on any `Send` error. `Close` calls it with no cause and returns nil.
6. **Tests** (`conn_test.go`). Each drives a fake agent over `Pipe`: the test reads the agent's end with a scanner and writes raw frames to it.
   - `TestCallRoundTrip`: two calls get IDs 1 and 2, a nil params sends no `params`, and each result decodes.
   - `TestCallReturnsAcpwireErrors`: the encoded busy error in Contracts makes `acpclient.IsCode(err, acpwire.CodeBusy)` true, with `err.Error()` equal to `A turn is running.`. An error whose `data` carries a notice keeps it for `acpclient.NoticeOf`. A `-32602` error with no data returns an `*Error` with that code and message.
   - `TestCallSendsCancelWhenTheContextEnds`: the agent never answers, and cancelling the context returns `context.Canceled` within a second. The agent's end then reads the pinned cancel frame with the call's ID. A late response to that ID is dropped, and the next call still succeeds.
   - `TestProgressArrivesBeforeTheResult`: the agent sends two `$/progress` values and then the response. The progress function sees both, in order, before `CallWithProgress` returns. A value for another ID is not delivered.
   - `TestNotificationsBeforeAResponseAreHandledFirst`: the agent sends a notification and then the response. `Handlers.Notify` has recorded it by the time `Call` returns.
   - `TestAgentRequestsAreAnswered`, for a request with the string ID `marshal-out-1`:
     - a handler's result comes back with that ID;
     - an `*acpwire.Error` comes back as `-32000` with its data;
     - with no handler, the answer is `method not found`;
     - a handler that blocks does not stop a call made meanwhile from completing.
   - `TestConnectionEndFailsPendingCalls`: closing the agent's end fails a waiting call with an error for which `errors.Is(err, ErrClosed)` holds. `Done` is closed, and a later call fails at once.
   - `TestCloseReportsErrClosed`: after `Close`, `Err()` is `ErrClosed`.
   - `TestAnOversizedFrameEndsTheConnection`: over `NewStream` capped at 1 KiB, a 2 KiB frame ends the connection, and `Err()` wraps both `ErrClosed` and `bufio.ErrTooLong`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpclient/rpc/
TMPDIR=/private/tmp/p3-1-suite go test ./internal/acpclient/rpc/ -count=1 -race
```

Expected: all pass under `-race`. Mutation check: make the read goroutine hand responses to their waiters before it handles an earlier notification, by sending the notification to a goroutine. `TestNotificationsBeforeAResponseAreHandledFirst` then fails, at least under `-count=20`. Restore it.

## Task 3: The server's halves

**Goal:** the ACP server sends `*acpwire.Error` with its data, cancels a request that opted in, and sends progress before a response.

**Files:** `internal/acp/server.go`, `internal/acp/protocol.go`, `internal/acp/server_test.go`, `internal/acp/client_core_test.go` (new).

**Steps:**

1. **Error data.**
   - In `dispatch` (`server.go:594`), when `errors.As` finds an `*acpwire.Error` in the handler's error, return a `jsonRPCError` with code `serverError`, the wire error's own message, and its JSON encoding as `Data`. Every other error is wrapped exactly as today.
   - `dispatchRequest` (`:483`) copies the `Data` of the `jsonRPCError` it finds with `errors.As` into the response's `Error`. The message stays `wireError`'s, so it reads `server error: <message>`.
2. **The in-flight registry.**
   - In `handleFrame` (`:333`), for a request with an ID and before starting its goroutine, register an `inflight` entry under the ID's compact JSON, from `json.Compact`.
   - Pass the handler a context carrying the entry as a value over the connection's context, with `context.WithValue`, so nothing registers with the connection's context.
   - `dispatchRequest` removes the entry when the handler returns, before it writes the response.
   - A notification gets no entry.
3. **`$/cancel_request`.** `handleFrame` handles it itself, synchronously, before the registered-handler lookup:
   - it decodes the ID, finds the entry, and closes its channel once;
   - an unknown ID, a malformed frame or a repeat cancel is ignored;
   - nothing is written back: the method is a notification. An ID-bearing `$/cancel_request` gets `invalidRequest`, as other notification-only methods do.
4. **`cancellable`.** Derive a context with `context.WithCancel` from the given one. When it carries an entry, start one goroutine that cancels the derived context when the entry's channel closes, and exits when the derived context ends. The caller's cancel function ends it, so nothing outlives the handler. An already cancelled entry cancels the derived context at once.
5. **`reportProgress`.** It encodes `{id, value}` as `acpwire.ProgressParams` and sends `$/progress` with the server's `Notify`, which holds `outMu`. The handler sends its progress before it returns, and the response is written after, so the order holds on the wire.
6. **Tests** (`server_test.go`, in the style of `TestDispatchRequestSanitizesWireErrorMessage`, `:1009`):
   - `TestDispatchRequestCarriesAcpwireErrorData`: a handler returning `&acpwire.Error{Code: acpwire.CodeBusy, Message: "A turn is running."}` writes the pinned response frame.
   - `TestDispatchRequestKeepsOtherErrorsOpaque`: a plain error still writes `internal error` with no `data`.
7. **Tests** (`client_core_test.go`). Each serves `NewServer` on the agent's end of an `rpc.Pipe`, with `MaxLineBytes` set to `rpc.MaxFrameBytes`, and calls it through `rpc.NewConn`:
   - `TestClientSeesServerAcpwireErrors`: through `Conn.Call`, the busy error satisfies `acpclient.IsCode`, with today's message.
   - `TestCancelEndsACancellableHandler`: the handler waits on `cancellable(ctx)`'s context and records when it ends. Cancelling the call ends it within two seconds.
   - `TestCancelLeavesOtherHandlersRunning`: a handler that does not opt in sees no cancellation, and the client's late response is dropped. This guards `/mcp auth`'s flow.
   - `TestACancelBeforeTheHandlerOptsInIsKept`: the handler sleeps 100ms and only then calls `cancellable`. The cancel, sent at once, has already ended the derived context.
   - `TestProgressPrecedesTheResponse`: a handler reports two values and returns. `CallWithProgress` sees both before it returns.
   - `TestThePipeCarriesLargeFramesToTheServerAndBack`: a 3 MiB param reaches the handler whole, and a 3 MiB result reaches the client whole.
   - `TestServerRequestsReachTheClient`: `Server.Request` from a handler goroutine gets the client's `Handlers.Request` answer.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/
TMPDIR=/private/tmp/p3-1-suite go test ./internal/acp/ -count=1 -race -run 'TestServer|TestDispatch|TestClient|TestCancel|TestACancel|TestProgress|TestThePipe|TestScanLines|TestWireError'
TMPDIR=/private/tmp/p3-1-suite go test ./internal/acp/ -count=1
```

Expected: both pass; every existing `internal/acp` test still passes (V6).

Mutation checks, restoring each:
- register the entry inside the handler goroutine instead of `handleFrame`, and `TestACancelBeforeTheHandlerOptsInIsKept` fails under `-count=20`;
- drop step 1's `errors.As`, and `TestDispatchRequestCarriesAcpwireErrorData` fails.

## Task 4: The handshake and `Dial`

**Goal:** `rpc.Dial` connects to the real agent host over the pipe, and both ends agree on the marshal protocol.

**Files:**
- `internal/acp/host.go`, `internal/acp/run.go`, `internal/acp/server.go`, `internal/acp/client_core_test.go`;
- `internal/acpclient/rpc/dial.go` (new), `internal/acpclient/rpc/dial_test.go` (new), `internal/acpclient/rpc/imports_test.go` (new);
- `internal/app/tui/boundary_test.go`.

**Steps:**

1. **The agent.**
   - `InitializeParams` gains `ClientCapabilities` (Contracts).
   - The `initialize` handler (`host.go:298`) calls `srv.setMarshalClient` with whether the client declared the marshal capability at `MarshalProtocolVersion`.
   - Its result's `agentCapabilities` gains `"marshal": {"protocolVersion": 1}`, from `acpwire.MarshalProtocolVersion`, for every client.
   - Nothing else in the result changes.
2. **`Dial`.**
   - Start a `Conn` over the transport with the options' handlers, and call `initialize` with the pinned params into an `acpwire.InitializeResult`.
   - On a call error, close and return `rpc: initialize: <err>`.
   - Without `agentCapabilities.marshal` at `MarshalProtocolVersion`, close and return the pinned refusal.
   - When `AgentVersion` is set and differs from `agentInfo.version`, close and return the pinned mismatch.
   - Otherwise return a `Host` holding the connection, the agent info and the options' `SharedFilesystem`. `Host.Close`, `Done` and `Err` are the connection's.
3. **Imports.** `imports_test.go` keeps `rpc`'s non-test files to the imports `acpclient` allows (C1). Copy `acpclient`'s `allowedOnTheWireSide` rule and add `marshal/internal/acpclient` itself.
4. **The TUI boundary.** `TestSeamImportAllowedClassifies` (`internal/app/tui/boundary_test.go:259`) lists `marshal/internal/acpclient/rpc` among its denied imports (V7). The rule already denies it: `seamImportAllowed` matches `acpclient` exactly.
5. **Tests** (`dial_test.go`, a fake agent over `Pipe` as in Task 2):
   - `TestDialSendsTheMarshalCapability`: the agent reads the pinned params, and answers with the capability and version `1.2.3`. `Host.Agent().Version` is `1.2.3`, and `SharedFilesystem` reports the option.
   - `TestDialRefusesAnAgentWithoutTheCapability`: the pinned error, and the transport is closed.
   - `TestDialRefusesAnotherVersion`: with `AgentVersion` set to `1.2.4`, the pinned mismatch.
6. **Tests** (`client_core_test.go`). `TestDialAgainstTheAgentHost` serves the real handlers on the pipe, as `TestACPClientMethodTableMatchesTheServer` builds them (`acpclient_methods_test.go:20`), with `MaxLineBytes` set:
   - `rpc.Dial` with `AgentVersion: AgentVersion` succeeds, the agent's name is `marshal`, and `srv.marshalClient()` is true;
   - a raw `initialize` through a plain `Conn`, with only `protocolVersion`, still gets `agentCapabilities.marshal`, and leaves `marshalClient()` false.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/ ./internal/acpclient/rpc/
TMPDIR=/private/tmp/p3-1-suite go test ./internal/acpclient/rpc/ -count=1 -race
TMPDIR=/private/tmp/p3-1-suite go test ./internal/acp/ -count=1
TMPDIR=/private/tmp/p3-1-suite go test ./internal/app/tui/ -run 'TestSeamImportAllowedClassifies|TestTUIStaysOnTheClientSeam' -count=1
TMPDIR=/private/tmp/p3-1-suite go test ./web/bridge/ -run 'Control|Initialize' -count=1
```

Expected: all pass. The existing `initialize` tests (`run_test.go:82`) and the bridge's control tests pass with the added capability. Mutation check: make the handler ignore the client's capability, and `TestDialAgainstTheAgentHost` fails on `marshalClient()`. Restore it.

## Task 5: Budget, docs and verification

**Goal:** the pipe meets R10's call budget, the docs record plan 1, and the full suite is green.

**Files:**
- `internal/acp/client_core_test.go`;
- `docs/daemon/p3-acp-transport.md`, `AGENTS.md`, `docs/ARCHITECTURE.md`.

**Steps:**

1. **The benchmark.** `BenchmarkPipeCallRoundTrip` in `client_core_test.go` serves one handler that returns a small result. The server logs to a discard logger, through `WithLogger`, so logging is not measured. Each iteration makes one `Conn.Call`.
2. **The spec.** Add an "Implementation record" section to `p3-acp-transport.md`, after "Findings while drafting", with a plan 1 entry. It lists:
   - the `acpwire` types;
   - `rpc`'s transport, pipe, connection and `Dial`;
   - the server's error data, opt-in cancellation, progress and capability;
   - the measured round trip, in microseconds, with the machine named "development machine";
   - that no interface method goes over the wire yet.
3. **`AGENTS.md`.** In "Where things live", the `internal/acpclient/` line names `rpc/` too: "the TUI's Host/Session interfaces; inproc/ is the in-process adapter (deleted in P3); rpc/ is the JSON-RPC client that replaces it". Plan 7b edits another section of this file. If its merge has landed, keep both edits.
4. **`docs/ARCHITECTURE.md`.** The "Client seam" row (`:82`) adds `internal/acpclient/rpc/` and "the JSON-RPC client P3 is building".
5. **Main may have moved.** If P2 plan 7b merged while this branch ran, merge `main` into the branch before the full verification. No conflict is expected beyond `AGENTS.md`.
6. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd ./web
go vet ./...
TMPDIR=/private/tmp/p3-1-suite go test ./internal/acp/ -run '^$' -bench BenchmarkPipeCallRoundTrip -benchtime 2000x
TMPDIR=/private/tmp/p3-1-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p3-1-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- the benchmark reports under 1,000,000 ns/op. If it does not, stop and report: R10 says a plan that misses a budget does not loosen it;
- the binary builds;
- the test line prints nothing, or only `web/bridge`'s known hang (Assumptions). Treat any other failure as new unless it also fails at `341f1941`, or it is one of the load flakes below and passes on its own with `-count=3`.

**Baseline.** At `341f1941`, all 117 packages passed, and this plan adds one (`internal/acpclient/rpc`). Earlier full runs saw these fail under load and pass alone:
- `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`);
- `TestRunManagedCancellationKillsTree` and `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`);
- `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

No manual check: nothing a user runs touches the new code yet.

## Integration notes

- **P2 plan 7b** runs alongside. The two plans share only `AGENTS.md`, in different sections. Whichever merges second merges `main` first and reruns the suite.
- **P3 plan 2 (layering)** starts once P2 closes. It moves `inproc`'s logic into `internal/acp` and splits `app.Run` into `launch`. `launch` serves the agent host on `Pipe`'s agent end, sets `MaxLineBytes` to `rpc.MaxFrameBytes`, and dials with `AgentVersion` set to the binary's own. It watches `Host.Done` so that a closed pipe ends the TUI with `Host.Err` (R11).
- **P3 plan 3 onwards:**
  - each group adds its methods to `rpc.Host` and a session type;
  - its handlers opt into `cancellable` where a call can be cancelled (the sign-ins, the preflight) and use `reportProgress` for `show(url)`;
  - they return `*acpwire.Error` for coded failures;
  - they gate update kinds on `marshalClient()`.

  Plan 3 starts the conformance harness (V1).
- **R8 (secrets):** the connection logs nothing. A later frame log must redact the secret-carrying methods by name.

## Self-review

- **Every task verifiable alone.** Task 1 adds types and a transport. Task 2 is the client, proved against a fake agent. Task 3 is the server, proved against the client. Task 4 joins them in the handshake. Task 5 measures and records. Each keeps the existing suites green, because no existing handler opts in, reports progress or returns an `*acpwire.Error`.
- **Cold executor.** Each step names the function, the frame shapes, the order of operations and the failure paths, and each test names its setup and the behaviour it pins.
- **Contracts.** The wire types and their encodings, `rpc`'s declarations, the server's additions and every error string are declared there.
- **Anchors.** Every path, symbol and line was checked at `5acdd89c`.
- **Spec.** R1, R2 and the Migration order are updated, with the reordering recorded in "Decided with the user".
- **No placeholders.**
