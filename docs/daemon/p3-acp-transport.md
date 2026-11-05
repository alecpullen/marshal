# P3 — the TUI over ACP

Parent: [`design.md`](design.md) §4.5 and §7 (P3). It builds on
[`p2-client-seam.md`](p2-client-seam.md), whose interface and wire types
it implements over ACP. Where this spec and design §4.5 differ, this spec
wins.
Depends on: P2. Plans 1 (the client core) and 4a (the stack over the
wire) depend on nothing P2 still changes and run alongside P2's last
plans. The other plans wait for all nine P2 plans: the boundary test's
allowlist is empty, `WithLegacyState` is gone, and P2 plan 9 has written
the list of ACP methods the interface marks "new in P3".
Status: drafted 2026-11-05, while P2 plan 7a ran, and its open questions
decided the same day (below). The counts were measured at `e671da0d` (P2
plan 7a's plan commit), and P2 plans 7a to 9 will change them. P3's
layering plan re-measures them. Plans 1 and 4a were written while P2
plan 7b ran ([plan 1](plans/2026-11-05-p3-1-client-core-plan.md),
[plan 4a](plans/2026-11-05-p3-4a-stack-wire-plan.md)); the rest are
written one at a time once P2 closes.

Decided with the user (2026-11-05):
- **The adapter's logic moves, and is not rewritten.** It becomes the
  per-session service behind `internal/acp`'s handlers (R1).
- **Only calls that can block become asynchronous** in the TUI's
  `Update`. P4 decides about the rest for a remote daemon (R6).
- **One switch, at the end.** Each group is proved over the pipe by the
  conformance suite, and the TUI moves onto the pipe in plan 8. There is
  no early developer flag (Migration order).
- **Today's busy rule wins.** If the agent's rule would change any T6
  golden, `session/stack` gains an optional param carrying the TUI's
  rule, so nothing a user sees changes. Other clients keep the agent's
  rule (R4). This had no recommendation in the draft; it follows the
  goal.
- **Marshal-only updates sit behind the `initialize` capability** (R2).
- **The client core goes first, alongside P2.** It touches only
  `acpclient/rpc`, the ACP server and new `acpwire` types, which no P2
  plan still changes. Layering moves `inproc` and splits `app.Run`, which
  every remaining P2 plan touches, so it waits for P2 to close (Migration
  order).
- **The stack's wire half also goes early** (plan 4a). The mirror, the
  marshal cadence and subagent unsubscribe need only `session/stack` and
  `stack_patch`, which the per-connection `TurnManager` already serves,
  so they run alongside P2 too. The rest of the transcript plan (4b)
  waits for plan 3.

## Goal

After P3, embedded `marshal` is an ACP client of its own agent. The agent
host that `marshal acp` serves runs in the same process, on one end of an
in-memory pipe, and the TUI's `acpclient.Host` and `Session` speak ACP on
the other. Every interface method sends its ACP method. Every read comes
from a client-side mirror kept current by the agent's notifications.
`internal/acpclient/inproc` is deleted. Nothing a user sees changes.

P4 then puts the bridge between the two ends. It adds a transport, not a
new client.

## Where the code is today

- **The interface** has 57 methods (69 once plan 7a lands). 20 send an ACP
  method that exists, and 34 name one that is new in P3: 30 distinct names
  (38 after 7a). Plans 7b, 7c and 8 add more.
- **The adapter is most of the logic.** `inproc` converts runtime values
  to wire values and applies the policy the TUI used to apply. That
  covers the run-error classification, row-action IDs, the sheet's cache,
  the work pump, Send-now receipts, the watch resume, access
  reconciliation and the model cache. C4 requires that it behave exactly
  like the code it replaced. Deleting it must not delete that behaviour.
- **The ACP server** (`internal/acp/server.go`) reads and writes
  newline-delimited JSON-RPC 2.0 on any reader and writer
  (`NewServer`, `:119`). It reads each inbound frame with a 1 MiB cap
  (`:312`). Handlers run on their own goroutines (`handlerWG`), and nothing
  orders a response after the notifications its handler caused. There is
  no request cancellation: no `$/cancelRequest` or equivalent. 70 methods
  are registered (`host.go`).
- **What the agent pushes today:**
  - the ACP chunk kinds (`agent_message_chunk`, `agent_thought_chunk`,
    `tool_call`, …);
  - `stack_patch`, revisioned upserts and removals of `WireNode`s
    (`stack.go:28`), flushed at most every 200ms, or every 500ms when
    idle;
  - `work_state`, `steering_state` and `steering_receipt`.

  Nothing pushes status, pending requests, the end flow's active work or
  the sheet.
- **What the agent asks today:** `session/request_permission` (approve or
  deny, tagged for skill gates and mode access) and
  `session/request_question`. Sudo has no ACP form, and an approval cannot
  say "for the session" or "always".
- **The layering forbids the obvious wiring.** `acp` imports `app`
  (`run.go:12`), and `app` imports `tui` (`app.Run`, `app.go:2577`), so
  `app.Run` cannot start an ACP host. `cmd/marshal` imports both
  (`main.go:20` to `22`).
- **A long transcript does not fit in a frame.** A synthetic session with
  2,000 tool calls (200 turns of ten `shell.run` calls with 1 KB of output)
  projects to 1,001 nodes, about 2.67 MB of JSON. Projecting takes 13ms,
  encoding 3ms and decoding 10ms. Its first `stack_patch`, which upserts
  every node, and its `session/stack` result are both larger than any 1 MiB
  reader allows.
- **The TUI calls the seam synchronously in `Update`.** Besides its reads
  and its 138 notes (one helper, `m.note`), it has about 50 call sites
  for calls, most of them in `Update`. In process they are function
  calls. P2's plans name the ones that must become asynchronous
  over a wire: `Command`, `PlanPreflight`, `Host.NewSession`,
  `Postmortem` and `CanProbeModel`.
- **TUI tests build state, not runtimes.** About 225 `tui.New` calls go
  through helpers (`testModel`, `testModelWith`, …) that build an adapter
  over a `session.State` and a `Deps`. P2 placed every test behind those
  helpers so that P3 changes only the helpers.

## In scope

1. A JSON-RPC client implementing `acpclient.Host` and `Session` over a
   transport, plus the in-memory pipe transport.
2. ACP handlers for every method the interface marks "new in P3", and the
   additive extensions existing methods need.
3. The mirror, the update kinds that feed it, and the ordering guarantee
   that keeps reads consistent after calls.
4. The adapter's logic moved agent side, behind the ACP handlers.
5. Embedded mode over the pipe, with `inproc` deleted.

## Out of scope (follow-ups)

- **G1.** The bridge transport, the RPC passthrough, brokering
  `session/request_secret`, and reattaching to a running turn. P4.
- **G2.** Making every call asynchronous for a remote daemon (R6). P4
  revisits the calls this spec leaves synchronous.
- **G3.** Resuming a session in place. Resume stays a restart of the
  program, as in P2 (R7).
- **G4.** Moving ACP's own `Work*Wire` and `PermissionRequest` types onto
  `acpwire`. They stay, and the client converts (R9).
- **G5.** Fixing the bridge's own 1 MiB reader (Findings). It does not
  wait for P3: P2 plan 7b carries it (decided with the user, 2026-11-05).

## Behaviour

### R1. Packages and layering

- **`internal/acpclient/rpc`** (new) is the client. `rpc.Dial(ctx, t
  Transport, opts) (*Host, error)` runs `initialize` and returns a host
  whose sessions implement `acpclient.Session`. It imports only what
  `acpclient` may import (C1). Plan 1's `Host` holds only the connection,
  the agent's info and `SharedFilesystem`; each group's plan adds its
  methods, and the check that `Host` implements `acpclient.Host` arrives
  with the last.
- **`Transport`** is a framed, two-way message stream: send a frame,
  receive frames. `rpc.NewStream` frames any byte stream with newlines,
  as `marshal acp` speaks on stdio. `rpc.Pipe()` returns the two ends of
  an in-memory connection: the client's `Transport`, and the agent's byte
  stream, which `acp.NewServer` reads and writes unchanged. P4's bridge
  transport (HTTP calls plus the session's SSE stream) implements the
  same interface.
- **The adapter's logic moves into `internal/acp`, and is not
  rewritten.** Each ACP session gets the agent-side service that `inproc`
  is today: the same `Deps`, built by the runtime as `Run` builds them
  now, and the same methods. Handlers decode the params, call the
  service, and encode the result. `inproc`'s tests move with it as the
  service's parity tests. What gets deleted is the in-process transport
  of the interface, not the behaviour behind it.
- **`internal/launch`** (new) is the composition root for embedded
  mode:
  - it starts the agent host on one end of a pipe and dials the client on
    the other;
  - it runs the TUI over the client;
  - it owns what `app.Run` does around the TUI today: the resume restart
    loop, first run, the startup trust prompt, signals and the exit code.

  `app` keeps building runtimes and stops importing `tui`. The runtime
  options `app_test.go` relies on stay in `app`, and `launch` passes them
  through to the agent host's runtime starter. `cmd/marshal` calls
  `launch.Run`.

### R2. Wire protocol

- **Framing** stays newline-delimited JSON-RPC 2.0. The pipe reads frames
  with no fixed cap below 64 MiB. Each end of the pipe sets its cap
  explicitly; neither inherits the 1 MiB default.
- **`initialize`.** The client declares `clientCapabilities.marshal`
  with a protocol version. The agent advertises `agentCapabilities.marshal`
  with its version to every client, and records whether this client
  declared it. It sends the marshal-only update kinds (R3) only to a
  client that did. The client refuses an agent that does not advertise
  the version. It also checks `agentInfo.version`, and refuses a mismatch
  in embedded mode, where both ends are one binary.
- **Errors.** An `*acpwire.Error` travels as a JSON-RPC error with code
  `-32000`, the server's usual `server error: <message>` message, and the
  `*acpwire.Error` itself, encoded whole, as `data`:
  `{"code": <acpwire code>, "message": …, "data": <its ErrorData>}`. The
  client rebuilds the `*acpwire.Error` from `data`, so `acpclient.IsCode`,
  `NoticeOf` and its message work unchanged. Any other error keeps its
  JSON-RPC code, and the client returns its message.
- **Cancellation (new).** When a call's context ends, the client sends
  `$/cancel_request {"id": <id>}` and returns at once. The agent cancels
  that handler's context. This is how Esc cancels an MCP sign-in, and how
  a stopped preflight stops.
  - A handler opts in by deriving its context through the server. The
    others keep the connection's context, as today: some start work that
    outlives the request (`/mcp auth`'s flow, `mcpauth.go:124`), which
    cancelling every request's context would end.
  - The server registers each request before it starts the handler, and
    handles `$/cancel_request` on its read goroutine, so a cancel that
    follows its request is never missed. A cancel for an unknown or
    finished request is ignored.
- **Progress (new).** A call that reports progress (C2) gets
  `$/progress {"id": <id>, "value": …}` notifications before its
  response. The handler sends them through its context on its own
  goroutine, before it returns, so they precede the response on the wire.
  The client hands each value to the call's function, such as a sign-in's
  `show(url)`, on its read goroutine.
- **Read your writes (new).** Before writing a call's response, the agent
  flushes every update the call caused, on the same connection. The
  client applies frames in arrival order, and a call returns only after
  the frames before its response are applied. A read after a call
  therefore sees the call's effect, as it does in process.

### R3. The mirror

- **Each session holds a mirror:** status, the stack of the main
  transcript and of each subagent that is read, pending requests, work,
  the end flow's active work, the command list and the sheet. Reads
  return the mirror's value, without I/O, as C2 requires.
- **`Changed` fires** after the client applies any update for the
  session, coalesced into its buffer of one as today. The TUI's 150ms
  tick stays, for clocks.
- **Where each read comes from:**

  | Read | First value | Kept current by |
  |---|---|---|
  | `Status` | `session/status` (new) | `session_status` (new): the full value whenever it differs from the last one sent, at most every 50ms, and before any call's response |
  | `Stack` | `session/stack` (exists) | `stack_patch` (exists), at the marshal client's flush interval (R4) |
  | `Pending` | the requests the agent sends | each request's arrival, its answer, and `request_settled` (new) |
  | `Work` | `session/work_list`'s live view (exists) | `work_state` (exists) |
  | `ActiveWork` | `session/end_work` (new) | `end_work` (new) |
  | `Commands` | `session/command_list` (exists, with P2's dispatch fields) | `commands_changed` (new), sent when the registry changes |
  | `Sheet` | `session/sheet` (new) | the result of each `RefreshSheet`, and `sheet` (new) |

- **Host reads** (`ProviderTemplates`, `SharedFilesystem`) are fetched at
  dial time. `SharedFilesystem` is true for the pipe.

### R4. The transcript

- The mirror applies `stack_patch` upserts and removals by revision. A
  patch whose base revision is not the mirror's triggers a fresh
  `session/stack`, and the patch is dropped: its content is in the
  fetched snapshot, because the agent answers a read with a revision at
  least as new as every patch it sent before. Patches that arrive while
  the fetch is in flight are queued, and applied after it in order when
  they chain (plan 4a).
- **Order.** A patch carries no node order, so the mirror rebuilds the
  projection's: depth-first from each root through `children`, then the
  nodes no root reaches, such as the band (plan 4a).
- **Cadence.** ACP's 200ms flush is slower than the TUI's 150ms tick, and
  streaming text would visibly lag. A marshal client gets a 50ms flush,
  and the agent coalesces anything faster. The client's cost per patch is
  measured against R10's budget. Both of the agent's loops, the turn's
  and the idle one, use 50ms for a marshal client; other clients keep
  200ms and 500ms. Plan 4a also measures the agent's own flush of a
  2,000-call session, since at 50ms its cost decides whether the
  interval is affordable.
- **`busy` goes.** `Stack(subagentID, busy)` loses the parameter. The
  agent's rule decides a tree's busy flag: the session's turn, or a
  subagent's own run. P2 plan 3d recorded that the two rules differ.
  P3's transcript plan checks every T6 golden against the agent's rule
  before the parameter goes. If any golden changes, the parameter stays
  as an optional `session/stack` param carrying the TUI's rule, and the
  agent's rule serves only the clients that omit it (decided). No golden
  is updated.
- **Subagents.** The first read of a subagent's stack fetches it
  synchronously and subscribes. Later reads come from the mirror. Leaving
  the drill-in unsubscribes with `session/stack_unsubscribe` (new, plan
  4a), which keeps the agent from projecting trees nobody reads. The main
  transcript cannot be unsubscribed.
- `StackNode` and `Callers` are plain calls (`session/stack_node` exists;
  `session/callers` is new).

### R5. Agent requests

- **Approvals and gates** use `session/request_permission`, extended
  additively:
  - `kind` (`approval`, `skill_gate` or `access`), the owner (session or
    subagent ID, C7) and the choices offered;
  - a response may carry the TUI's richer choices: once, for the session
    or always for an approval, and the skill gate's choices.

  A client that sends only approve or deny gets today's behaviour.
- **Questions** use `session/request_question` (exists).
- **Sudo** uses `session/request_secret` (new): an agent-to-client
  request whose response carries the password. Design §6.3 defines it for
  P4's broker. P3 gives it its shape and its rules: never logged, never
  in an update.
- **The mirror's `Pending`** lists the requests the client has received
  and not yet answered, keyed as C7 keys them. Answering sends the
  response. A request the agent settles another way (a cancelled turn, a
  timeout, or in P4 another client's answer) arrives as `request_settled`,
  and an answer after that returns `CodeRequestSettled`.
- `ReconcileAccess`, `AnswerAccess` and `Rollback` are calls (new).

### R6. Calls on the Update goroutine

- **A call stays synchronous in `Update`** only when the agent answers it
  without a provider call, a subprocess or a network round trip, and it
  stays under the budget R10 sets.
- **Every other call becomes asynchronous.** The TUI sends it from a
  `tea.Cmd` and acts on a result message. P2 named five:
  - `Command`: its note and any agent goal land when the result arrives;
  - `PlanPreflight`: the cast list opens when the preflight arrives;
  - `Host.NewSession`: the swap happens on its result;
  - `Postmortem`;
  - `CanProbeModel`: the settings browser shows "detecting…" only once
    the check says it can probe.
- **The inventory.** P3's first plan measures every call made in `Update`
  at its base and fixes this list. In embedded mode a cheap call costs one
  in-memory round trip, well under a millisecond, so the rest stay
  synchronous. Making all of them asynchronous for a remote daemon is P4's
  call (G2).

### R7. Sessions

- `Host.NewSession` sends `session/new`, and `Close` sends
  `session/close`. A swap closes the old session's mirror and its
  `Changed`, as C4 requires.
- Subagents stay addressed through the parent session by ID (C2). The
  client never holds a session for a child.
- **Resume stays a restart.** The TUI reports the session to resume, and
  `launch` restarts the agent host on it. P4's reattach (a client joining
  a live session) is a different operation, and `session/load` already
  exists for it.

### R8. Secrets and logging

- These calls carry a secret:
  - the provider-key write (C10);
  - the probes, whose config may hold a typed key (C3, plan 7a);
  - the answer to `session/request_secret`.
- Neither end logs their params or that response, at any level. If frame
  logging is added, it redacts these methods by name.
- T7 extends to the wire. A sudo password appears in no frame except
  that response, and in no update, notification or log line.

### R9. Compatibility with existing ACP clients

- The web UI (through the bridge) and editors keep working unchanged:
  - existing methods change only by gaining optional params and result
    fields;
  - the marshal-only update kinds go only to clients that declare the
    capability (R2);
  - no default changes.
- **Where the TUI's semantics differ from an existing method,** the
  method gains an optional param that selects the TUI's. The known
  differences, from P2's records:
  - `session/command` refuses during a turn, and the TUI never did;
  - `session/command` mints no action IDs and returns no agent goal;
  - `session/set_mode` names system access `system_access`;
  - `session/steer` delivers Send now as `now`;
  - `session/sdd_start` and `session/sdd_answer` report a gate as
    `stopReason: "gate"`, which the client maps to
    `CodeHumanGateRequired`;
  - `session/work_*` encode times as RFC 3339 strings, which the client
    converts to milliseconds (G4).
- `TestACPClientMethodTableMatchesTheServer` (T4) ends P3 with every row
  marked existing.

### R10. Budgets

Measured on the 2,000-call fixture, on the development machine:
- the client applies one `stack_patch` of ten changed nodes in under 2ms;
- a full `session/stack` round trip, from request to an applied mirror,
  takes under 50ms;
- a synchronous call in `Update` adds under 1ms over the pipe;
- the time from a key press to the next frame does not grow measurably.
  Reads are local, so only calls can move it.

A plan that misses a budget stops and reports. It does not loosen the
budget.

### R11. Embedded mode

- One process, as today. The agent host and the TUI share the process's
  log file and its data directory.
- An agent-side panic still ends the process, as today. A pipe that
  closes unexpectedly ends the TUI with the agent's error, never with a
  hang.
- `marshal acp` and embedded `marshal` run the same agent host code. The
  only difference is the transport.

## Design

| Area | Change |
|---|---|
| `internal/acpclient/rpc` (new) | The client, the mirror, the transport interface and the pipe. |
| `internal/acp` | Handlers for every new method; the per-session service moved from `inproc`; the new update kinds; cancellation, progress and response ordering in `Server`. |
| `internal/acpwire` | Params and results for the new methods; the update payloads. |
| `internal/launch` (new) | Embedded mode's composition root. |
| `internal/app` | Stops importing `tui`. `Run`'s TUI half moves to `launch`. |
| `internal/app/tui` | Asynchronous forms of the R6 calls. The test helpers build a pipe-connected agent over a test state. |
| `internal/acpclient/inproc` | Deleted. |
| `cmd/marshal` | Calls `launch.Run`. |

## Migration order

The TUI keeps running on `inproc` until plan 8, so every plan leaves it
working. Each group's methods are proved over the pipe by the
conformance suite (V1) before anything switches.

1. **Client core** ([plan](plans/2026-11-05-p3-1-client-core-plan.md)),
   alongside P2's last plans. The transport, `rpc.NewStream` and
   `rpc.Pipe`, the connection (calls, notifications, agent requests, error
   mapping, cancellation and progress), and `rpc.Dial` with the
   `initialize` capability. On the server: error data for
   `*acpwire.Error`, `$/cancel_request` for handlers that opt in,
   `$/progress`, and the capability. The pipe's round trip is measured
   against R10.
2. **Layering**, once P2 closes. `launch` takes the TUI half of
   `app.Run`, and `app` stops importing `tui`. `inproc`'s logic moves
   into `internal/acp` as the per-session service, and `inproc` becomes a
   thin wrapper over that service, so the TUI is unchanged. Re-measure the
   counts above and the R6 inventory.
3. **Status and ordering.** `session/status`, `session_status`, the
   read-your-writes guarantee in `Server`, `Changed`, and the notice
   calls. The conformance harness starts here: one test table runs against
   both `inproc` and the client.
4. **Transcript**, in two plans:
   - **4a. The stack over the wire** ([plan](plans/2026-11-05-p3-4a-stack-wire-plan.md)),
     alongside P2's last plans. The stack patch types in `acpwire`, the
     marshal flush interval, `session/stack_unsubscribe`, and the client's
     stack mirror, proved against the real projector over the pipe and
     measured against R10 on a 2,000-call session.
   - **4b. The transcript read**, after plan 3. The session type's
     `Stack`, `StackNode` and `Callers`, when the TUI releases a
     subagent, and the `busy` decision against the goldens (R4).
5. **Requests.** The `request_permission` extensions,
   `request_secret`, `request_settled`, the `Pending` mirror and the
   answer calls.
6. **Turns and runs.** Prompt, stop, steering, interaction and effort,
   the work mirror, pipelines, the end flow and sessions, with the R6
   asynchronous conversions.
7. **The rest.** Notes, commands, config, providers, trust and project
   data: the remaining new methods, probably as two plans.
8. **Switch.** `launch` runs the TUI over the pipe, the test helpers build
   pipe-connected agents, and `inproc` is deleted.

## Tests

- **V1. Conformance.** One table of tests per interface group, moved from
  `inproc`'s tests, runs against every implementation, from plan 3 on.
  Until plan 8 those are `inproc` and the client over a pipe to an agent
  host over a test state. After plan 8, only the client remains. Plan 1
  proves the protocol itself against the real server.
- **V2. TUI suite.** Every TUI test passes over the pipe, with only the
  helpers changed. T6's goldens are unchanged.
- **V3. Ordering.** For each call that changes state, a read right after
  it sees the change, under `-race` and with the agent's handlers
  deliberately slowed.
- **V4. Large transcript.** The 2,000-call fixture round-trips over the
  pipe, its first patch included, within R10's budgets.
- **V5. Secrets.** T7 over the wire (R8).
- **V6. Compatibility.** `internal/acp`'s existing tests and the bridge's
  end-to-end tests pass unchanged, and a client without the capability
  receives none of the new update kinds.
- **V7. Boundary.** Nothing imports `acpclient/inproc`, and the TUI
  imports `acpclient` but not `acpclient/rpc`. Only `launch` dials.
- **V8. Suite.** Every plan keeps `go test ./...` green, apart from the
  known local failures.

## Done when

- Embedded `marshal` runs entirely over ACP on the pipe, and `inproc` is
  deleted.
- Every interface method's ACP method exists: T4 has no row marked new.
- V1 to V8 pass, and the R10 budgets hold.
- A manual run behaves as before. It covers P2's checklist (Done when),
  plus:
  - a long session's transcript stays smooth while streaming;
  - Esc cancels an MCP sign-in;
  - a sudo prompt;
  - `/new` and `/exit` with a postmortem.

## Findings while drafting

- **The bridge cannot read a long session's transcript** (by reading,
  not run). `web/bridge/child.go:444` reads child frames with a 1 MiB cap
  (`maxFrameBytes`). On an oversized frame, `readLoop` simply returns: no
  error is reported, nothing reads the child's later output, and pending
  requests are never answered. The 2,000-call fixture's first
  `stack_patch` and its `session/stack` result are about 2.67 MB. So a web
  client opening a long session could leave the bridge's connection to
  that child stuck. P2 plan 7b fixes it (G5): it raises the cap, and
  treats a scanner error as a child failure.

## Implementation record

### Plan 1 — the client core

- **`internal/acpwire`** gains `initialize.go`: `MarshalProtocolVersion` (1),
  `MethodProgress` (`$/progress`) and `MethodCancelRequest`
  (`$/cancel_request`), plus the `initialize` types (`MarshalCapability`,
  `ClientCapabilities`, `InitializeParams`, `AgentInfo`,
  `AgentCapabilities`, `InitializeResult`) and the two notification
  parameter types (`ProgressParams`, `CancelRequestParams`).
- **`internal/acpclient/rpc`** is the client core:
  - `Transport` is a framed two-way message stream; `NewStream` frames any
    reader and writer with newlines (as `marshal acp` speaks on stdio), and
    `Pipe` returns the client's transport and the agent's byte stream, which
    `acp.NewServer` reads and writes unchanged. Each end sets its own frame
    cap: `MaxFrameBytes` (64 MiB) on the client, `Server.MaxLineBytes` on the
    agent.
  - `Conn` makes calls, hands the agent's notifications and requests to
    `Handlers`, maps `*acpwire.Error` back to itself, sends
    `$/cancel_request` when a call's context ends, receives `$/progress`
    values before the response, and ends every pending call when the
    transport breaks.
  - `Dial` runs `initialize` with
    `{"protocolVersion":1,"clientCapabilities":{"marshal":{"protocolVersion":1}}}`,
    refuses an agent that does not advertise marshal protocol 1 (or another
    version, when `DialOptions.AgentVersion` is set), and returns a `Host`
    carrying the connection and the agent's info.
- **The ACP server** gains error data (`*acpwire.Error` travels as code
  `-32000` with the whole error as `data`), opt-in cancellation
  (`cancellable`, an in-flight registry filled before each handler starts,
  and `$/cancel_request` handled on the read goroutine in arrival order),
  progress (`reportProgress` sends `$/progress` for the handler's request),
  and the capability record `setMarshalClient` / `marshalClient`.
  `initialize` advertises `agentCapabilities.marshal` to every client and
  records whether this one declared it. Handlers that do not opt in see
  exactly the frames they saw before.
- **The budget.** `BenchmarkPipeCallRoundTrip` (one trivial call over the
  pipe to the real server, server logging discarded) measures about
  **9.7 µs** per call (9,689 ns/op) on the development machine (Apple M5,
  darwin/arm64). R10 allows 1 ms.
- **Not yet over the wire.** No `acpclient.Host` method beyond
  `SharedFilesystem` is implemented by `rpc.Host`; each later plan adds its
  group's methods.

