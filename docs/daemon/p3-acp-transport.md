# P3 — the TUI over ACP

Parent: [`design.md`](design.md) §4.5 and §7 (P3). It builds on
[`p2-client-seam.md`](p2-client-seam.md), whose interface and wire types
it implements over ACP. Where this spec and design §4.5 differ, this spec
wins.
Depends on: P2, which closed with plan 9. Plans 1 (the client core), 4a
(the stack over the wire) and 3a (ordering) depended on nothing P2 still
changed and ran alongside its last plans. The rest now wait only for each
other. Plan 9 wrote down the ACP methods the interface marks "new in P3",
grouped by the plan that implements them in "Methods P3 adds" (below).
Status: drafted 2026-11-05, while P2 plan 7a ran, and its open questions
decided the same day (below). The counts were measured at `e671da0d` (P2
plan 7a's plan commit) and re-measured at P2 plan 9, which wrote them
down; no P2 plan changes them now. P3's layering plan re-measures them.
Plans 1 and 4a were written while P2
plan 7b ran ([plan 1](plans/2026-11-05-p3-1-client-core-plan.md),
[plan 4a](plans/2026-11-05-p3-4a-stack-wire-plan.md)), and plan 3a while
P2 plans 8a and 8b ran ([plan 3a](plans/2026-11-06-p3-3a-ordering-plan.md)).
Plans 2 and 6a were written once P2 had closed, to run alongside each
other ([plan 2](plans/2026-11-07-p3-2-layering-plan.md),
[plan 6a](plans/2026-11-07-p3-6a-async-calls-plan.md)), and plans 3b and
4c once those two had merged ([plan 3b](plans/2026-11-07-p3-3b-status-plan.md),
[plan 4c](plans/2026-11-07-p3-4c-projection-memo-plan.md)). The rest are
written one or two at a time, as what they depend on lands.

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

- **The interface** has 110 methods. 39 send an ACP method that exists,
  and 68 are carried by one of the 60 ACP methods that are new in P3
  ("Methods P3 adds", below). Three have no ACP method at all.
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
  no request cancellation: no `$/cancelRequest` or equivalent. 75 methods
  are registered (`host.go`), plus the `session/cancel` notification.
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

## Methods P3 adds

P2 plan 9 measured them: 72 interface methods are carried by 64 ACP
methods that `acp.Server` does not register today, grouped below by the
plan that implements each. The table in `acpclient`
(`acpclient.Methods`) is the source of every row; T4 ends P3 with no row
marked new.

### Plan 3b — status and notices

- `session/status` — `Session.Status`, refreshed by a `session_status`
  update.
- `session/notice_set` — `Session.SetNotice`.
- `session/notice_clear` — `Session.ClearNotice`.
- `session/notice_dismiss` — `Session.DismissNotice`.

### Plan 4b — the transcript read and the sheet

- `session/callers` — `Session.Callers`.
- `session/sheet` — `Session.Sheet`, `Session.RefreshSheet`. Plan 3b
  moved it here: a subagent's sheet needs the subscribe-and-release rule
  plan 4b gives a subagent's stack.

### Plan 5 — agent requests

- `session/request_permission` — `Session.Pending`,
  `Session.AnswerApproval`, `Session.AnswerSkillGate`,
  `Session.AnswerAccess`.
- `session/request_question` — `Session.AnswerQuestion`.
- `session/request_secret` — `Session.AnswerSudo`.
- `session/rollback` — `Session.Rollback`.

### Plan 6b — turns, runs, the end flow and sessions

- `session/review` — `Session.Review`.
- `session/agent_run` — `Session.RunAgent`.
- `session/suggest` — `Session.Suggest`.
- `session/verify_proposal` — `Session.VerifyProposal`.
- `session/sdd_preflight` — `Session.PlanPreflight` (asynchronous, R6).
- `session/sdd_author` — `Session.AuthorPlan`.
- `session/sdd_stop` — `Session.StopSDD`.
- `session/sdd_clear` — `Session.ClearFinishedRun`.
- `session/steering_pop` — `Session.PopSteering`.
- `session/steering_clear` — `Session.ClearSteering`.
- `session/set_sudo_unlock` — `Session.SetSudoUnlock`.
- `session/subagent_cancel` — `Session.CancelSubagent`.
- `session/watch_resume_take` — `Session.TakeWatchResume`.
- `session/end_work` — `Session.ActiveWork`, refreshed by an `end_work`
  update.
- `session/end_accept` — `Session.AcceptEnd`.
- `session/end_wait` — `Session.WaitForEnd`.
- `session/exit_analysis` — `Session.RunExitAnalysis`.
- `session/postmortem` — `Session.Postmortem` (asynchronous, R6).

`session/new` already exists, but `Host.NewSession` becomes an
asynchronous call (R6).

### Plan 7 — notes and commands; config, providers and trust; project data

- `session/note` — `Session.AddNote`.
- `session/command_action` — `Session.CommandAction`.
- `session/tools` — `Session.Tools`.
- `config/provider_templates` — `Host.ProviderTemplates`.
- `config/probe_model` — `Host.CanProbeModel` (asynchronous, R6),
  `Host.ProbeModel`.
- `config/models_list` — `Host.ListModels`.
- `config/models_save` — `Host.SaveModels`.
- `config/models_forget` — `Host.ForgetModels`.
- `config/models_rename` — `Host.RenameModels`.
- `config/login_provider` — `Host.PrepareProviderLogin`,
  `Host.LoginProvider`.
- `config/set_value` — `Host.SaveUserValue`.
- `config/save_providers` — `Host.SaveUserProviders`.
- `config/save_presets` — `Host.SaveUserPresets`.
- `config/prepare_presets` — `Host.PreparePresets`.
- `config/save_provenance` — `Host.SaveProvenance`.
- `trust/prompt` — `Host.TrustPrompt`.
- `trust/decide` — `Host.DecideTrust`.
- `session/mcp_login` — `Session.PrepareMCPLogin`, `Session.LoginMCP`.
- `session/config` — `Session.Config`, `Session.Layers`.
- `session/apply_config` — `Session.ApplyConfig`.
- `session/reload_config` — `Session.ReloadConfig`.
- `session/mcp_reconnect` — `Session.ReconnectMCP`.
- `session/save_project_config` — `Session.SaveProjectConfig`.
- `session/save_verify_commands` — `Session.SaveVerifyCommands`.
- `session/prompt_history` — `Session.RecentPrompts`.
- `session/prompt_history_save` — `Session.SavePrompt`.
- `session/messages` — `Session.Messages`.
- `session/file_index` — `Session.FileIndex`.
- `session/intelligence` — `Session.Intelligence`.
- `session/plans` — `Session.Plans`.
- `session/sdd_discard` — `Session.DiscardCandidate`.
- `session/sdd_scaffold` — `Session.ScaffoldPlan`.
- `session/skills_install` — `Session.InstallSkill`.
- `session/skills_autoload` — `Session.SetSkillAutoload`.
- `session/skill_gate_set` — `Session.SetSkillGate`.
- `session/skill_gate_clear` — `Session.ClearSkillGateDecision`.

`session/command` already exists, but `Session.Command` becomes an
asynchronous call (R6).

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
  of the interface, not the behaviour behind it. Plan 2 refines where it
  lives:
  - **It is `internal/acp/service`,** a package of its own that imports
    neither `app` nor `acp`. `app` builds its `Deps` and its runtime
    methods return its types, and `acp` imports `app`, so the service
    cannot sit in `acp` itself. `acp`'s handlers import it from plan 3b.
  - **`(*app.Runtime).ServiceDeps`** builds the `Deps` from a runtime, as
    `Run` built them: for embedded mode from plan 2, and for each ACP
    session from plan 3b. Only the startup trust prompt is the embedding's
    own.
  - Until plan 8, `inproc` is aliases for the service's types and its
    constructor, so the TUI and its tests are unchanged.
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
  `launch.Run`. In plan 2, `app.Embedded` keeps what the loop needs from
  those options, resolved once: the directories, the host registry, the
  credential resolver, the trust decision and the shutdown sequence.
  `launch` keeps the loop, the program and the TUI. Plan 8 puts the pipe
  between them.

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
  therefore sees the call's effect, as it does in process. How (plan 3a):
  - Only for a client that declared the marshal capability. Every other
    client gets its responses exactly as before (R9).
  - Only for a call whose params name a session (`sessionId`). The
    server runs a response barrier between the handler and the response,
    and the barrier fences that session's forwarders. A call naming no
    session flushes nothing: no host call causes a session update today.
    Plan 3b found that this still holds with `session_status`: no host
    call changes a session's status yet. Plan 7's config calls are the
    first that can, and plan 7 decides how they flush.
  - Each goroutine that forwards a session's events as updates (the
    turn's forwarder, the idle stack loop, the work and watch forwarders)
    serves a fence: it handles the events buffered in its channel when
    asked, which are the ones published before the call returned, flushes
    what it owns, and answers. A fence lives as long as its forwarder's
    loop, and one whose forwarder has gone answers at once.
  - The barrier waits at most two seconds, then logs a warning and
    writes the response, so a stuck forwarder never hangs a call (R11).

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

- **How the status arrives (plan 3b).** `Host.Attach` binds a session
  the agent already holds by sending `session/status`. For a marshal
  client that starts the session's status forwarder on the connection.
  The barrier fences it last, so it sends the current status before the
  response, and then sends it again whenever it differs, checking every
  50ms. One goroutine sends every status update for the session, so they
  arrive in order and need no revision. The response carries the status
  for other clients; a marshal client uses it only when no update came
  first. The sheet's row is plan 4b's.
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
  `tea.Cmd` and acts on a result message. P2 named five, and plan 6a's
  inventory fixed the list at seven:
  - `Command`: its note and any agent goal land when the result arrives;
  - `PlanPreflight`: the cast list opens when the preflight arrives;
  - `Host.NewSession`: the swap happens on its result;
  - `CanProbeModel`: the settings browser shows "detecting…" only once
    the check says it can probe;
  - `ReloadConfig`, at its five call sites (plan 6a): it rebuilds the
    runtime, which starts MCP servers and builds providers;
  - `ReconnectMCP` (plan 6a): the same rebuild;
  - `RefreshSheet` when the sheet opens (plan 6a): it runs git. The sheet
    opens when the refresh returns, so its first frame stays fresh.

  `Postmortem`, the fifth P2 named, already runs in a command.
- **They keep their order.** The model runs the converted calls it makes
  one at a time, in the order it made them. Each starts on the `Update`
  goroutine from the model as it is then, so a call made after `/new`
  goes to the new session. Its result is handled on the `Update`
  goroutine. A quit waits for the call in flight, and drops the ones
  queued behind it. Rendering, scrolling and every synchronous call go on
  meanwhile. The settings browser's `CanProbeModel` runs as the browser's
  own command, as its probes already do, and its stale-result guard drops
  a late answer (plan 6a).
- **The inventory.** Plan 6a measured every call made on the `Update`
  goroutine at `1d34fb9a` and fixed the list above. In embedded mode a
  cheap call costs one in-memory round trip, well under a millisecond, so
  the rest stay synchronous. Making all of them asynchronous for a remote
  daemon is P4's call (G2).

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
    converts to milliseconds (G4);
  - `session/list` encodes `updatedAt` as RFC 3339 and the message count
    in `_meta`, which the client converts (P2 plan 8b);
  - `session/prompt` has no `watchResume`, the watch whose goal the agent
    writes (P2 plan 8a);
  - `session/diff` compares a base with the working tree, and the TUI's
    run outcome needs `base..head`: it gains an optional `head` (P2 plan
    8a);
  - `session/memory_list` lists what the session sees, and the TUI lists
    the project's own rows (P2 plan 8b);
  - `session/plugins_install_scan` returns content counts, not the
    contents the TUI's review shows, and `plugins_install_confirm`
    neither checks the reviewed hash nor rolls back. The handlers move
    onto the adapter's logic (P2 plan 8b);
  - `session/skills_*` find the global store with `skills.ScopeDir`,
    which ignores `MARSHAL_CONFIG_DIR` and `XDG_CONFIG_HOME`, remove a
    skill by name, and load without checking the store. They move onto
    the adapter's logic, which keys a skill by scope and source (P2 plan
    8b).
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
2. **Layering** ([plan](plans/2026-11-07-p3-2-layering-plan.md)), once
   P2 closed, alongside plan 6a. `inproc`'s logic moves to
   `internal/acp/service`, and `inproc` becomes aliases for it, so the
   TUI is unchanged. `app.Embedded` and `(*Runtime).ServiceDeps` take the
   runtime half of `app.Run`, `launch` takes the TUI half, and `app`
   stops importing `tui`. Re-measure the counts above. (The R6 inventory
   moved to plan 6a.)
3. **Status and ordering**, in two plans:
   - **3a. Ordering** ([plan](plans/2026-11-06-p3-3a-ordering-plan.md)),
     alongside P2's close-out. The read-your-writes guarantee in `Server`
     for the update kinds the agent sends today: the response barrier,
     and a fence on each forwarder. It needs only the server and the
     forwarders, which exist and which no P2 plan changes. Proved over the
     pipe with slowed updates, and measured against R10.
   - **3b. Status** ([plan](plans/2026-11-07-p3-3b-status-plan.md)),
     after layering, alongside plan 4c. Each ACP session's service, built
     with `ServiceDeps`; `session/status`, `session_status` and its
     forwarder, added to the barrier; the notice calls; and `rpc.Session`
     with `Attach`, the status mirror and `Changed`. The conformance
     harness starts here: one table of status cases runs against the
     service (which `inproc` aliases) and the client. The sheet moved to
     plan 4b.
4. **Transcript**, in two plans:
   - **4a. The stack over the wire** ([plan](plans/2026-11-05-p3-4a-stack-wire-plan.md)),
     alongside P2's last plans. The stack patch types in `acpwire`, the
     marshal flush interval, `session/stack_unsubscribe`, and the client's
     stack mirror, proved against the real projector over the pipe and
     measured against R10 on a 2,000-call session.
   - **4b. The transcript read and the sheet**, after plans 3 and 4c.
     The session type's `Stack`, `StackNode` and `Callers`; the sheet
     (`Sheet` and `RefreshSheet`), whose subagent reads share the stack's
     subscribe-and-release rule; when the TUI releases a subagent; the
     `busy` decision against the goldens (R4); and R4's cadence, decided
     with plan 4c's numbers.
   - **4c. The projection's cost** ([plan](plans/2026-11-07-p3-4c-projection-memo-plan.md)),
     alongside plan 3b. A memo of each node's wire form and encoding, so
     a projection after a change re-projects and re-encodes only the
     nodes that changed, in the agent's projector and in the service's
     `Stack`. `Build` stays whole: making it incremental needs stable
     transcript identities from `session.State`.
5. **Requests.** The `request_permission` extensions,
   `request_secret`, `request_settled`, the `Pending` mirror and the
   answer calls.
6. **Turns and runs**, in two plans:
   - **6a. The asynchronous calls** ([plan](plans/2026-11-07-p3-6a-async-calls-plan.md)),
     alongside layering. The R6 inventory, and the TUI's conversions of
     the calls it names, over `inproc`: they need only the TUI, which no
     other P3 plan before plan 8 changes. In process they still run on a
     goroutine, so the conversions and their ordering are proved before
     anything crosses a wire.
   - **6b. Turns and runs.** Prompt, stop, steering, interaction and
     effort, the work mirror, pipelines, the end flow and sessions.
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

### Plan 4a — the stack over the wire

The first half of plan 4: the patch protocol as wire types, a marshal client's
cadence, and a client mirror fed by the patches the agent already sends. Plan
4b wires the mirror into `rpc.Session`.

- **`internal/acpwire/stack.go`** gains the stack's names and shapes:
  `MethodStack`, `MethodStackUnsubscribe`, `MethodSessionUpdate`,
  `UpdateStackPatch`; `StackParams`; `StackPatch` (the patch's fields moved
  from `acp`'s unexported type, unchanged); and `SessionUpdate`, the
  `session/update` envelope with its update left raw for its kind's decoder.
  `TestStackPatchDecodesAsTheWireType` decodes a flushed patch's real bytes
  through both types and compares every field with the patch `diff` made, so
  the envelope cannot drift. `acp.StackParams` is now an alias, and `acp`'s
  `notifyStackPatch` still builds the same map envelope: the bytes on the wire
  did not change, and a client without the marshal capability sees exactly
  what it saw before.
- **A marshal client's cadence.** `TurnManagerConfig.Marshal` carries
  `srv.marshalClient`, so the per-connection `TurnManager` knows what the
  client declared at `initialize`. `stackIntervals` then reports 50ms for both
  the turn's flush ticker and the session's idle ticker, or the existing 200ms
  and 500ms for any other client. Each loop reads its interval once, when its
  ticker is created.
- **`session/stack_unsubscribe`** forgets one subagent's projector, so no
  flush projects it again; a later `session/stack` read subscribes again from
  a fresh snapshot. The main transcript cannot be unsubscribed, and an unknown
  session or subagent is not an error. `flushChildStacks` now skips a key
  `m.stacks` no longer holds, so a projector deleted by an unsubscribe is not
  recreated by the flush that was already listing keys.
- **`internal/acpclient/rpc.Stacks`** mirrors every stack a connection reads:
  - `Stack` fetches with `session/stack` on a stack's first read, which
    subscribes the connection, and serves later reads from the mirror without
    I/O. The entry is created in the fetching state before the request goes
    out, so patches arriving meanwhile are queued rather than lost.
  - `Notify` applies `stack_patch` updates by revision: a patch whose base is
    the mirror's revision applies, one at or below it is a duplicate and is
    dropped, and anything else is a gap. A gap marks the entry fetching and
    starts one `session/stack` fetch on its own goroutine, because `Notify`
    runs on the read goroutine and must not block on the connection. The
    fetched snapshot is installed, then every queued patch newer than it is
    applied in order; one that does not chain starts another fetch.
  - A read rebuilds the agent's node order: depth-first from each root through
    `children`, then every node no root reaches in first-arrival order. Nodes
    are shared with the mirror and replaced on upsert, never changed in place.
  - `Release` forgets a subagent's stack and calls
    `session/stack_unsubscribe`; `Changed` returns the session's buffered
    channel, which fires after any install or applied patch for that session's
    main stack or any subagent's.
- **Proved against the real projector.** `internal/acp/stack_wire_test.go`
  serves a marshal-capable `TurnManager` through `startTestAgent`, binds a
  `Stacks` to the same pipe, and compares the mirror's read with the
  projector's own sent nodes, order, roots and revision after each change of a
  scripted turn, after a dirty flush and a band wake, after a lost patch (the
  mirror refetches and converges), and for a subagent before and after
  `Release`.
- **The 2,000-call session.** 200 turns, each a prompt, one step with ten
  `shell.run` calls, and a final reply: **1,201 nodes**, a snapshot of
  **2,534,625 bytes** (2.4 MiB). On the development machine (Apple M5,
  darwin/arm64):
  - applying a ten-node patch through the mirror, decoding included: median
    **1.03 ms** over 20 runs (benchmark 1.09 ms/op); R10 allows 2 ms.
  - a full `session/stack` round trip to an applied mirror over the pipe:
    median **36.2 ms** over 30 runs (benchmark 34.2 ms/op); R10 allows 50 ms.
  - one agent-side flush of the same session after one new tool call:
    **8.4 ms** (no budget; it is the number plan 4b decides the 50ms cadence
    against).
  - The budget tests skip under `-short` and under the race detector, which
    `stackwired_race_test.go` / `stackwired_norace_test.go` report.
- **Left for plan 4b:** `rpc.Session.Stack`, `StackNode` and `Callers` over
  the wire; the `busy` decision checked against the T6 goldens; when the TUI
  releases a subagent's stack; and any revisit of the 50ms cadence in the
  light of the flush cost above. `Stacks` also has no session-close hook yet,
  so each session a connection has signalled keeps one `Changed` channel for
  the connection's lifetime: 4b's session close is where that goes.

### Plan 3a — ordering

The first half of plan 3: R2's read-your-writes guarantee for the update kinds
the agent sends today. Plan 3b adds `session/status`, `session_status`,
`Changed` and the notice calls, and adds `session_status` to the barrier.

- **`internal/acp/fence.go`** holds the mechanism. A `fence` is an unbuffered
  channel of requests and a `done` channel closed once by `close`: `ask` hands
  a request channel over and waits for it to be closed, and returns at once
  when `ctx` ends or the forwarder closes the fence. A `fenceSet` keeps the
  live fences of one manager's forwarders by session, in the order they were
  added; `wait` copies the session's slice under the mutex and asks each fence
  outside it. Its zero value works, so no constructor changed.
- **The barrier** is `Server.SetResponseBarrier`, run by `dispatchRequest`
  after the request leaves the in-flight registry and before its response is
  built. It runs when all of these hold, and for nothing else (R9): a barrier
  is installed; `marshalClient()` reports that this connection declared the
  marshal capability; and the params decode as an object with a non-empty
  string `sessionId`. It runs for a failed call too, because a failed call may
  have published. `dispatchNotification` is unchanged, so no notification runs
  it, and no host call does either: none names a session.
- **The host's barrier** is `responseBarrier(turns, works, watches)`, handed to
  the server in `registerHandlers` once all three managers exist. It fences the
  turn manager's, then the work manager's, then the watch manager's forwarders
  for the session.
- **The four forwarders serve fences**, each registering one for its session
  while its loop runs and removing and closing it when the loop returns:
  - the turn's forwarder (`runTurn`) forwards the events already buffered in
    its terminal subscription, then flushes the stacks the turn dirtied, as its
    ticker does. Its fence is registered after the subscription and removed by
    a defer, so a fence asked during the final drain waits until `finishTurn`
    has written everything;
  - the idle stack loop (`ensureStackIdle`) marks the stack dirty for the
    buffered events, then does exactly what a tick does when no turn is active:
    look the session up (dropping the stacks when it has gone), flush the main
    projector dirty-only, then the subagent projectors and the run detail;
  - the work forwarder (`runWorkForwarder`) forwards the buffered snapshots
    through the same epoch check as its loop;
  - the watch forwarder (`ensureForwarder`) forwards the buffered events
    through the same `send` as its loop.
- **Never a hang (R11).** `responseBarrierTimeout` is 2 s, set on the server by
  `NewServer`. A barrier still running when it expires is reported with
  `acp: response barrier timed out` and the attributes `method` and
  `session_id`, then its context is cancelled (releasing the fence it waits on)
  and the response is written. A barrier that panics is recovered in its own
  goroutine (where `dispatchRequest`'s recover cannot reach), logged as
  `acp: response barrier panicked`, and the call is still answered. The
  barrier's context derives from the handler's request, so a client that
  disconnects also releases it; tests shorten the bound by setting the
  server's own `barrierTimeout`.
- **Proved over the pipe.** `internal/acp/ordering_test.go` serves three
  managers through a server whose every `session/update` frame is slowed by
  50 ms, with a client that records the updates it applies and feeds an
  `rpc.Stacks` mirror:
  - a read after a call already holds the node the call added, and does not
    fetch again (V3);
  - `session/steer` returns only after the client has the `steering_state`
    update it caused, and the work and watch calls likewise for `work_state`
    and `watch`;
  - the barrier counts zero runs for a non-marshal client's call, a call naming
    no session, a call with array params, and a notification, and exactly one
    for a marshal session call (V6);
  - a fence nobody serves costs the response its 50 ms bound, not a hang, and
    is logged with the method and the session.
  `internal/acp/fence_test.go` proves the mechanism directly, including that a
  finished turn's fence is gone.
- **The budget (R10).** On the 2,000-call session (1,201 nodes, a 2,534,638-byte
  snapshot) with the main stack subscribed, over the pipe, on the development
  machine (Apple M5, darwin/arm64, Go 1.26.1):
  - a session call that changes nothing: median **12.5 µs** over 30 calls
    (benchmark 15.9 µs/op); R10 allows 1 ms.
  - a session call that adds one message and publishes its event: median
    **8.47 ms** over 10 calls, no budget. It carries one projection of the
    session, which the idle tick would have paid 50 ms later.
  `TestResponseBarrierBudget` skips under `-short` and under the race detector,
  as the 4a budget tests do.
- **Left for plan 3b:** `session/status` and `session_status`, `Changed`, the
  notice calls, and adding `session_status` to the barrier's conditions; the
  conformance harness. A turn that outlives its connection keeps its fence on
  the old connection's `TurnManager`, so a new connection's barrier does not
  wait for it (P4's reattach revisits this).

### Plan 6a — the asynchronous calls

R6's list, measured rather than assumed. A `go/ast` scan of every non-test file
under `internal/app/tui` for the 110 seam method names, read by hand, found
seven calls made directly on the `Update` goroutine that can block:

- `Session.Command`, from `dispatchCommand`;
- `Host.NewSession`, from `startNewSession`;
- `Session.PlanPreflight`, from the `/sdd` effect;
- `Session.RefreshSheet`, from `openSessionSheet`;
- `Session.ReloadConfig`, at five sites;
- `Session.ReconnectMCP`, from `reconnectMCPServer`;
- `Host.CanProbeModel`, from the settings browser's `startConfirm`.

`Postmortem` was already a command, as were the probes, the sign-ins, the
plugin scan and install, the skill install and remove, the run outcome's diff,
`Callers`, the row detail, the runs and the end flow's waits. Every other
direct call reads or writes in-memory state, a small database row or a small
file, so it stays synchronous and R10 is unchanged.

- **`internal/app/tui/asynccalls.go`** holds the queue. `callOutcome`,
  `callRun` and `callDoneMsg` are the plumbing — the result type is not
  `callResult`, which `spine.go` already uses for a settled tool call's
  rendered result — while `enqueueCall` and `enqueueStep` append and
  `serveCalls` starts the head from `Update`'s epilogue, beside
  `detailQueryCmds`. Four rules:
  - one call runs at a time, in the order the calls were made, so a call made
    after another sees the model the first one left;
  - a call starts from the epilogue, never inside the handler that made it,
    which is what lets a function that returns nothing enqueue without
    changing its signature;
  - a start reads the model when it reaches the head, not when it was queued,
    so a command entered after `/new` runs on the new session;
  - a prompt submitted while the queue holds anything waits as a step and does
    what Enter would have done when it runs: steer when busy, echo when no
    agent is ready, start a prompt otherwise. A slash command still dispatches
    at once; only the calls it makes queue.
- **A quit waits for the call in flight.** `finishQuit` cancels the in-flight
  call's context, drops everything queued behind it, and returns no command;
  the call's result is handled and then finishes the quit, so the session it
  closes is the one the call left. A call that ignores its context is waited
  for, as a frozen `Update` waited for it before.
- **The sheet's open is cancellable.** `openSessionSheet` sets a pending flag
  and enqueues the refresh; the panel is built from the result, so its first
  frame is fresh. A second Ctrl+B while the open is queued clears the flag, and
  the result opens nothing, as a press after the open would have closed it.
- **The settings browser's check is its own command.**
  `startConfirm` returns a `canProbeMsg` command carrying the current
  `capProbeID`, provider, model and the provider config the picker saw, and
  only a matching answer sets `detectingCaps` and starts the probe, so a stale
  answer after Esc is dropped and "detecting…" never appears for a provider
  that cannot report capabilities. The probe reuses that carried config rather
  than re-reading the live one, which an edit in the same panel could have
  changed while the answer was out. It does not join the model's queue, which
  orders nothing else.
- **The guard.** `TestBlockingCallsLeaveTheUpdateGoroutine` walks every
  non-test file under `internal/app/tui` with `go/parser` and fails on a call of
  an R6 name made on a seam receiver — the model's `sess`/`host`, or a local
  copy of one (`sess, ctx := m.sess, m.ctx`) — unless the innermost construct
  around it runs off the `Update` goroutine: a `func() tea.Msg` literal, a
  converted call's run (`func(ctx context.Context) callOutcome`), or a `go`
  statement. Start, step and outcome literals are not exempt, because
  `serveCalls` runs their bodies on the `Update` goroutine; only the innermost
  enclosing literal decides, so an exempt run cannot launder a handler nested
  inside it. It caught one site this plan's inventory had assigned to the wrong
  branch — `settings.ChangedMsg`'s saved branch, which reloaded inline — and
  that branch now enqueues like the others.
  `TestTheGuardCatchesAliasedSeamCallsAndLiteralBodies` parses a fixture per
  case and pins both escapes a return-type-only check left open: the aliased
  receiver and the handler literal nested in a run.
- **Tests changed to settle.** 51 test functions across tasks 2 to 5
  gained a `settleCalls` after the step that made the call, or now pass it the
  command `Update` returned, since a converted call's result lands later.
  `ctrlB` and `runEndCmd` settle inside themselves, because the sheet's opening
  and the replacement session are part of the flows they drive. Three tests
  drive the queue directly so they can assert mid-flight:
  `TestAgentGoalDispatchStartsRun` asserts busy when the goal starts rather than
  after the turn ends, and the new-session and queued-prompt tests hold a call
  open until they release it. Two options tests first drove
  `flushPendingModelOptions` directly to keep the blocking change pump out of
  the test; they now feed a `changedMsg` to `handleChanged` and drop the pump
  command it returns, so the turn-end path that queues the reload is the one
  under test. No assertion was weakened.

### Plan 2 — layering

- **The service is `internal/acp/service`**, and it is `inproc`'s logic moved:
  the same eight exported names (`Deps`, `New`, `Host`, `Session`, `Runner`,
  `PlanRun`, `EndService`, `ReferenceFinder`), the same behaviour, the same
  `inproc:` error texts (some tests pin them). Only the package clause and the
  doc comment changed. Its import rule is the point of the package: it must
  not import `internal/app` or `internal/acp`, because `app` builds its `Deps`
  and returns its types and `acp` imports `app`.
- **`internal/acpclient/inproc` is aliases.** Its eight exported names are
  aliases for the service's, and `New` forwards. The TUI and 34 of its test
  files compile and behave unchanged; plan 8 deletes the package.
- **`(*Runtime).ServiceDeps`** builds what the service needs from the runtime,
  as `Run`'s `Deps` block did: the brokers, the project ID, the reference
  finder, the access and interaction calls, the commands, the credential
  resolver, the model cache, `rt.ConfigReloader`, the layer reloader, the trust
  refresh, the home directory, `rt.ToolNames`, the skill index, the file index,
  `Work` only when the provider is ready, and `bindTurnDeps`' turn calls. It
  sets no trust prompt: that belongs to the embedding.
- **`app.Embedded`** is the runtime half of `app.Run`: `NewEmbedded` applies
  the options once (after the defaults the loop needs: the
  template-rebinding config loader and the deferred trust prompt), resolves
  the credential resolver once, the directories, first run and the project
  host registry. `Start` starts a runtime and does the start-up work (`marshal
  started`, the embedding warning, the OAuth workers, the secret prompt and
  the exit-analysis operation); `SessionDeps` is `ServiceDeps` plus the trust
  prompt and its decision; `Resume`, `TakeTrustReload` and `Finish` are the
  loop's three exit branches; `Close` closes the runtime and then the registry
  it created. The embedding warning and the workers now start before the
  `Deps` are built rather than after; nothing between the two reads the
  transcript or the workers.
- **`internal/launch` is embedded mode's composition root.** `launch.Run` is
  the rest of `app.Run`: the early return, the signal context, `NewEmbedded`,
  the deferred `Close`, and the loop that builds the adapter with
  `inproc.New(embedded.SessionDeps(ctx))`, the TUI options, the program and the
  resume/trust-reload/finish branches. `launch.Option` carries
  `WithProgramRunner` (whose nil is ignored) and `WithApp`, which passes
  `app.Option`s through. `app`'s non-test code now imports neither `tui`, nor
  Bubble Tea, nor `inproc`.
- **Tests moved, not rewritten.** The 24 `TestRun…` tests moved to
  `internal/launch/launch_test.go` with copies of the helpers they use, each
  `app` option wrapped in one `WithApp`. `TestRunResolvesOptionsOnce` stayed in
  `app` as `TestNewEmbeddedResolvesOptionsOnce`, since it counts applications of
  an `app` option. `TestServiceDepsServesTheRuntime` is new. The three
  TUI-over-runtime tests stayed in `app` because they need its unexported
  builders.
- **Two guards.** `TestServiceImportsStayAgentSide` keeps the service off the
  TUI and off `acpclient/rpc`; `TestAppImportsNoClient` keeps `app`'s non-test
  files off `internal/app/tui`, Bubble Tea and `inproc`. `go list -deps
  ./internal/app` prints none of them.
- **The counts did not move** (re-measured on this branch):
  `acpclient.Methods` still has 110 rows, 35 of them an existing ACP method and
  72 new in P3, with 3 carrying none; `acp.Server` still registers 71 methods
  (plus the `session/cancel` notification).
  `internal/acpclient/acpclient.go`, `methods.go`, `internal/acp/host.go` and
  `server.go` are untouched by this plan.
- **Left for plan 3b:** the first ACP handlers that call the service, and each
  ACP session's service built with `ServiceDeps`.

### Plan 3b — status

The first ACP handlers that call the client seam's service, the status the
agent pushes, and the client's mirror of it. Nothing a user sees changed: the
TUI still runs on `inproc`, in process, until plan 8.

- **Each ACP session's service** is the session manager's. `(*SessionManager).
  Service(id)` resolves a session's runtime as `Get` does and builds
  `service.New(rt.ServiceDeps(context.Background()))` on first use, keyed by the
  runtime pointer, under a mutex of its own. It refuses an unknown session, a
  runtime with no `State`, and a runtime that is closing (its `closeOnce` entry
  already exists, read under `closeMu`; the lock order is services, then
  `closeMu`, and `closeRuntimeOnce` never holds `closeMu` while taking the
  services mutex). `closeRuntimeOnce` releases the runtime's service inside its
  `once.Do`, before `m.close`, so a session's service never outlives the
  runtime whose state it reads; a build that races a close is refused or
  released, never leaked.
- **`(*service.Session).Release`** is the new export: it is `close` — the
  session marked closed, its subscriptions cancelled, `Changed` closed — and it
  leaves the state to the runtime that owns it. Releasing twice does nothing.
- **`internal/acp/status.go`** holds the connection's `StatusManager`: one
  `session/status` and three notice handlers over `Lookup` (the session
  manager's `Service`), `Notify` (the connection's sink) and `Marshal` (the
  connection's capability record). A missing `sessionId` is invalid params and
  an unknown session is `unknown session: <id>`; the notice handlers return the
  service's error unchanged, so its code travels as error data.
- **The forwarder polls, one per session per connection.** It starts on the
  first `session/status` from a marshal client, registers its fence before its
  goroutine starts, and wakes every `statusInterval` (50 ms) and on its fence.
  Each pass resolves the session through `Lookup` (so a runtime replaced on load
  or resume is followed), reads `Status()`, encodes it, and sends a
  `session_status` update only when the encoding differs from the last one it
  sent. Most status fields (usage, activity, the notice) change without a broker
  event, so polling is what keeps the mirror current; it never reads the
  service's `Changed`, which has one buffer and a second reader would steal.
  When the session goes it answers a pending fence, then stops and drops the
  fence. `Close` cancels every forwarder and makes later starts do nothing; the
  connection's cleanup calls it.
- **The first value arrives as an update, before the response.** `session/status`
  starts the forwarder before it returns, and `responseBarrier` — now fencing
  turns, works, watches, then statuses — asks it, so the forwarder writes the
  current status before the response is written. The response still carries the
  status for clients without the capability. No revision number is needed:
  every update for a session on a connection comes from that one goroutine, in
  order.
- **`registerInitialize`** is `registerHandlers`' own inline `initialize`
  handler, moved out unchanged so a test can serve the real handshake.
- **`internal/acpclient/rpc/session.go`** adds `Host.Attach` and `Session`:
  `Attach` registers the session before it sends `session/status`, so an update
  that arrives during the call is applied (and the result, which the agent
  computed first, is then ignored); attaching twice sends nothing. `Dial` builds
  the host first and installs its own `Handlers`: notifications go to the host's
  router (which applies `session_status` to the attached session it names and
  drops one for a session that is not attached) and then to the caller's
  `Notify` unchanged, while agent requests reach the caller's `Request` as
  before. `Status` reads the mirror without I/O; applying an update replaces it
  and fires `Changed` (buffer of one). One goroutine waits on the connection's
  `Done` and closes every attached session's `Changed`.
- **The conformance harness** (`internal/acp/conformance_test.go`) opens one
  `seam` two ways: the service itself, and the client over a pipe (`rpc.Dial`
  plus `Host.Attach` to a `StatusManager` built over `service.New`). Four cases
  moved into it from the service's tests — "a fresh state", "the status mirrors
  the state", "the notice calls change the banner" and "the runtime's notice
  reaches the status" — and run as `arm/case`, eight subtests. Both arms agree,
  so no value needed a wire or service change. `Changed`, a closed session and a
  swap stay service-only (decision 7).
- **The budget.** `TestNoticeCallBudget` drives 30 `SetNotice` calls that
  alternate between two notices over the 4a large session (2,000 calls, 1,201
  nodes, a 2.5 MB snapshot); each call includes its barrier and its update. The
  median is **45.7 µs** (39.5 µs on a second run), against R10's 1 ms.
- **The counts moved** (re-measured on this branch): `acpclient.Methods` still
  has 110 rows, now 39 of them an existing ACP method (four more: `Status`,
  `SetNotice`, `ClearNotice`, `DismissNotice`), 68 new in P3 carried by 60
  distinct new methods, and 3 carrying none; `acp.Server` registers 75 methods
  (71 plus the four).
- **Review fixes** (branch review after task 5): `Host.Attach` now returns the
  first call's outcome to a caller that arrives while it is in flight — it
  waits on the entry's `ready` channel and shares its value and error — where
  before a second caller got an empty mirror, and a refused attach settles the
  waiters with its error and leaves no entry, so a later `Attach` retries
  instead of inheriting it. `closeSessions` unregisters what it closes, so on a
  dead connection an id that was attached fails its call like one that was not.
  `service.Session.ClearNotice` refuses a category the wire does not carry
  (`errNoticeCategory`) rather than reporting a success that dismissed no
  banner; `""` still means internal, so a notice set without a category can be
  cleared without one. The forwarder's unused `done` channel is gone. The
  harness now serves each state under the state's own session id and asserts
  the service agrees (only the 4a fixture, which has no id, falls back), and a
  fifth conformance case covers the refused category on both arms.
- **Left for later plans:** the sheet and `RefreshSheet` (plan 4b, which needs
  the subscribe-and-release rule for a subagent's stack), `Changed` for the
  other update kinds (each mirror's own plan), `Host.NewSession` and closing a
  session over the wire (plan 6b), and host calls that change a session's status
  (plan 7). An ACP session's `ChangedFiles` stays 0, because nothing over the
  wire refreshes the sheet cache that counts them.
