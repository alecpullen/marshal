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
[plan 4c](plans/2026-11-07-p3-4c-projection-memo-plan.md)), and plans 4b
and 5 once those two had merged ([plan 4b](plans/2026-11-07-p3-4b-transcript-plan.md),
[plan 5](plans/2026-11-07-p3-5-requests-plan.md)), and plans 6b and 7a
once those two had merged ([plan 6b](plans/2026-11-08-p3-6b-controls-plan.md),
[plan 7a](plans/2026-11-08-p3-7a-settings-plan.md)), and plans 6c and 7b
once those two had merged ([plan 6c](plans/2026-11-09-p3-6c-ending-plan.md),
[plan 7b](plans/2026-11-09-p3-7b-browsers-plan.md)), and plan 8, the switch,
once those two had merged ([plan 8](plans/2026-11-09-p3-8-switch-plan.md)).

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

Decided with the user (2026-11-07):
- **A marshal client's requests are a pushed mirror, and its answers are
  calls** (R5). This replaces the draft's agent-to-client requests for that
  client: the TUI needs each answer's outcome (`AnswerAccess` returns a
  result, and an always answer is followed by a read of the config), which a
  JSON-RPC response cannot carry back, and an agent request handled on its
  own goroutine could race the update that settles it. The mirror reuses
  plan 3b's forwarder and barrier, and P4's bridge passes it through without
  brokering anything. `session/request_secret` and `request_settled` are
  dropped; clients without the capability keep today's
  `session/request_permission` and `session/request_question`.

Decided with the user (2026-11-08):
- **A marshal client's turns and controls run on the session's service.**
  For a client that declared the marshal capability, `session/prompt`,
  `session/steer`, `session/set_mode`, `session/set_effort` and
  `session/work_list`, `work_inspect` and `work_stop` reach the service, the
  logic moved from `inproc`, and every other client reaches today's
  handlers. This replaces R9's optional params for these methods: ACP's turn
  differs from the service's in too many ways to patch (the queued text put
  back, the codes, the stop's cause, the watch resume, the work's guard), and
  the service's own tests then prove the wire. A marshal connection binds no
  watch-resume hook, because the TUI resumes a watch itself (plan 6b).
- **Plans 6 and 7 are four plans, run as two pairs:** 6b (turns and
  controls) alongside 7a (config, providers and trust), then 6c (pipelines,
  the end flow and sessions) alongside 7b (notes, commands, browsers and
  project data). In each pair one plan works on the turn's path and the
  other on plain calls, so their conflicts are the shared registration and
  harness lines.

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

- **The interface** has 111 methods. 109 send an ACP method that exists, so
  every one of the 22 methods "Methods P3 adds" (below) is registered, and
  two have no ACP method at all.
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
  orders a response after the notifications its handler caused. A
  long-running handler runs in a `cancellable` context, so the client's
  `$/cancel_request` ends the run in flight. 139 methods are registered
  (`host.go`), plus the `session/cancel` notification.
- **What the agent pushes today:**
  - the ACP chunk kinds (`agent_message_chunk`, `agent_thought_chunk`,
    `tool_call`, …);
  - `stack_patch`, revisioned upserts and removals of `WireNode`s
    (`stack.go:28`), flushed at most every 200ms, or every 500ms when
    idle;
  - `work_state`, `steering_state` and `steering_receipt`.

  A marshal client also receives `session_status`, `sheet`,
  `pending_requests`, `session_config`, `work_snapshot` and `end_work`, the
  last carrying what an end would stop (plan 6c).
- **What the agent asks today:** a client that has not declared the marshal
  capability is asked with `session/request_permission` (approve or deny,
  tagged for skill gates and mode access) and `session/request_question`.
  A marshal client is asked nothing: it reads `session/pending`, and answers
  through the answer calls.
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
- `session/sheet` — `Session.Sheet`, `Session.RefreshSheet`, refreshed by
  a `sheet` update. Plan 3b moved it here: a subagent's sheet needs the
  subscribe-and-release rule plan 4b gives a subagent's stack.

Plan 4b also adds `session/stack_busy`, which carries no interface method
of its own: `Session.Stack` sends it when the TUI's busy flag changes
(R4). `Session.ReleaseSubagent`, new in the interface, sends
`session/stack_unsubscribe`, which exists.

### Plan 5 — agent requests

These replace the draft's `session/request_permission` extension,
`session/request_secret` and `request_settled` (decided 2026-11-07, R5).

- `session/pending` — `Session.Pending` and `Session.ReconcileAccess`,
  refreshed by a `pending_requests` update.
- `session/answer_approval` — `Session.AnswerApproval`.
- `session/answer_question` — `Session.AnswerQuestion`.
- `session/answer_skill_gate` — `Session.AnswerSkillGate`.
- `session/answer_sudo` — `Session.AnswerSudo`.
- `session/answer_access` — `Session.AnswerAccess`.
- `session/rollback` — `Session.Rollback`.

### Plan 6b — turns and controls

- `session/stop` — `Session.Stop`, which `session/cancel` carried before:
  that method is a notification, so it can carry no answer for the barrier
  to follow, and no cause. It stays for other clients.
- `session/review` — `Session.Review`.
- `session/agent_run` — `Session.RunAgent`.
- `session/suggest` — `Session.Suggest`.
- `session/verify_proposal` — `Session.VerifyProposal`.
- `session/sdd_clear` — `Session.ClearFinishedRun`.
- `session/steering_pop` — `Session.PopSteering`.
- `session/steering_clear` — `Session.ClearSteering`.
- `session/set_sudo_unlock` — `Session.SetSudoUnlock`.
- `session/subagent_cancel` — `Session.CancelSubagent`.
- `session/watch_resume_take` — `Session.TakeWatchResume`.

Plan 6b also adds the `work_snapshot` update, which carries the service's
work to a marshal client (R3), and serves a marshal client's
`session/prompt`, `steer`, `set_mode`, `set_effort` and `work_*` on the
service (decided with the user, 2026-11-08).

### Plan 6c — pipelines, the end flow and sessions

- `session/sdd_preflight` — `Session.PlanPreflight` (asynchronous, R6).
- `session/sdd_author` — `Session.AuthorPlan`.
- `session/sdd_stop` — `Session.StopSDD`.
- `session/end_work` — `Session.ActiveWork`, refreshed by an `end_work`
  update.
- `session/end_accept` — `Session.AcceptEnd`.
- `session/end_wait` — `Session.WaitForEnd`.
- `session/exit_analysis` — `Session.RunExitAnalysis`.
- `session/postmortem` — `Session.Postmortem` (asynchronous, R6).

`session/new` already exists, and `Host.NewSession` is an asynchronous
call (R6, plan 6a). Plan 6c gives `session/new` an optional `replaces`, which
swaps the session inside its runtime as the in-process host does (R7), and
adds the `end_work` update, which carries what an end would stop to a marshal
client (R3). For a marshal client, `session/swarm_start`, `sdd_start`,
`sdd_answer` and `close` are served by the session's service and
`session/list` by its service host, as plan 6b serves the turn's methods.

### Plan 7a — config, providers and trust

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
- `session/config` — `Session.Config`, `Session.Layers` and
  `Session.Tools`, refreshed by a `session_config` update. Plan 7a folded
  `session/tools` into it: `Tools` takes no context, so it needs a mirror,
  and it changes with the runtime the config rebuilds.
- `session/apply_config` — `Session.ApplyConfig`.
- `session/reload_config` — `Session.ReloadConfig`.
- `session/mcp_reconnect` — `Session.ReconnectMCP`.
- `session/save_project_config` — `Session.SaveProjectConfig`.
- `session/save_verify_commands` — `Session.SaveVerifyCommands`.

For a marshal client, `config/probe_provider` and `config/set_provider_key`
are served by the session's service host too.

### Plan 7b — notes, commands, browsers and project data

- `session/note` — `Session.AddNote`.
- `session/command_action` — `Session.CommandAction`.
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

`session/command` already exists, and `Session.Command` is an asynchronous
call (R6, plan 6a). For a marshal client, `session/command`, `command_list`,
`memory_list`, `memory_delete`, the five `plugins_*` methods the interface
carries, `skills_list`, `skills_load`, `skills_remove`, `diff` and `file` are
served by the session's service, as plan 6b serves the turn's methods. Plan 7b
drops `commands_changed`: the runtime's command registry never changes after
startup, so the client reads the list once (R3).

### Plan 8 — the switch

- `session/sync` carries no interface method, as `session/stack_busy` does
  not. `rpc.Session.Sync` sends it: the agent marks the session's stacks
  dirty, and the response barrier then fences every forwarder, so the
  response follows every update the session's state calls for (R4). The test
  agent's reads use it to catch up with a state a test changed directly (V2).

## Out of scope (follow-ups)

- **G1.** The bridge transport, the RPC passthrough (which carries the
  answer calls, `session/answer_sudo` included, R5), and reattaching to a
  running turn. P4.
- **G2.** Making every call asynchronous for a remote daemon (R6). P4
  revisits the calls this spec leaves synchronous.
- **G3.** Resuming a session in place. Resume stays a restart of the
  program, as in P2 (R7).
- **G4.** Moving ACP's own `Work*Wire` and `PermissionRequest` types onto
  `acpwire`. They stay for clients without the capability. A marshal client
  reads the service's own values instead: its requests (plan 5) and its work
  (plan 6b).
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
- **How the pipe is served (plan 8).** `acp.Embedding` is `marshal acp`'s
  agent host serving the runtime the embedding started, so both modes run the
  same handlers (R11):
  - `Adopt` registers the runtime under its session ID with the service pair
    `launch` built from `SessionDeps`, so the trust prompt and its decision
    are the embedding's;
  - `Serve` serves one connection with the pipe's frame cap;
  - `Close` releases the session's service and closes no runtime: the
    embedding closes its own, after the program, as it does today.

  A `session/new` without `replaces` is refused, because embedded mode starts
  no runtime but its own. `launch` builds one embedding for each pass of its
  loop, dials it over `rpc.Pipe`, attaches the runtime's session and runs the
  TUI over the client.

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
  - **A marshal client's error keeps its sentence (plan 6c).** The server
    writes any other error as its code's fixed text (F-SEC-37), so a run that
    failed with a plain error would reach the TUI as "internal error". For a
    client that declared the capability, the server sends such an error as an
    `*acpwire.Error` with no code and the error's own message; a coded error,
    a JSON-RPC error and a cancellation travel as before. The client rebuilds
    an `*acpwire.Error` from `data` whenever it carries a code or a message,
    so `err.Error()` is the sentence the TUI shows in process. Every other
    client keeps the fixed text.
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
    first that can, and plan 7a decides how they flush: every host call but
    `SharedFilesystem` names the client's current session, the one its last
    attach bound. That runtime's service host serves the call, as the
    in-process host acts on its current session, and the barrier fences
    that session as it fences any session call.
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
  the end flow's active work, the command list, the sheet, and the config
  with its layers and tools. Reads return the mirror's value, without I/O,
  as C2 requires.
- **`Changed` fires** after the client applies any update for the
  session, coalesced into its buffer of one as today. The TUI's 150ms
  tick stays, for clocks.
- **Where each read comes from:**

  | Read | First value | Kept current by |
  |---|---|---|
  | `Status` | `session/status` (new) | `session_status` (new): the full value whenever it differs from the last one sent, at most every 50ms, and before any call's response |
  | `Stack` | `session/stack` (exists) | `stack_patch` (exists), at the marshal client's flush interval (R4) |
  | `Pending` | `session/pending` (new) | `pending_requests` (new): the full value whenever it differs from the last one sent, at most every 50ms, and before any call's response (plan 5) |
  | `Work` | `session/work_list`'s live view (exists): for a marshal client the service's `WorkState` | `work_snapshot` (new, marshal only): the service's work whenever it differs from the last one sent, at most every 50ms, and before any call's response (plan 6b). Other clients keep `work_state` |
  | `Config`, `Layers`, `Tools` | `session/config` (new) | `session_config` (new): the config and layers with every secret masked, and the tools, whenever they differ from the last ones sent, at most every 50ms, and before any call's response (plan 7a) |
  | `ActiveWork` | `session/end_work` (new) | `end_work` (new, marshal only): the whole membership whenever it differs from the last one sent, at most every 50ms, and before any call's response (plan 6c) |
  | `Commands` | `session/command_list` (exists): for a marshal client the service's list | nothing: the runtime's registry never changes after startup, so the first read is kept (plan 7b) |
  | `Sheet` | `session/sheet` (new) | `sheet` (new): each sheet the client has read, whenever it differs from the last one sent, at most every 50ms, and before any call's response (plan 4b) |

- **How the status arrives (plan 3b).** `Host.Attach` binds a session
  the agent already holds by sending `session/status`. For a marshal
  client that starts the session's status forwarder on the connection.
  The barrier fences it last, so it sends the current status before the
  response, and then sends it again whenever it differs, checking every
  50ms. One goroutine sends every status update for the session, so they
  arrive in order and need no revision. The response carries the status
  for other clients; a marshal client uses it only when no update came
  first.
- **How the sheet arrives (plan 4b).** The first read of a sheet (the
  session's, or a subagent's) sends `session/sheet`, which subscribes the
  connection to that sheet; `RefreshSheet` sends it with the refresh flags.
  The session's sheet forwarder checks every subscribed sheet every 50ms,
  and when the barrier asks, and sends a `sheet` update for each one whose
  encoding changed. Most of a sheet is computed from the state on every
  read (the tool and file aggregates, the rules, the budget), so polling is
  what keeps it current, as for the status. A subagent's sheet is released
  with its stack (R4).
- **How the requests arrive (plan 5).** The first read of `Pending` (or
  of `ReconcileAccess`) sends `session/pending`, which for a marshal client
  starts the session's request forwarder; later reads come from the
  mirror, as for the stack and the sheet. Every 50ms, and when the
  barrier asks, the forwarder settles a typed access request that no
  longer asks for anything (as the TUI's per-update `ReconcileAccess`
  did), reads the open requests, and sends them whole as a
  `pending_requests` update when the encoding changed. The
  update carries `Pending`'s value and the access request `ReconcileAccess`
  presents, so both reads come from the mirror (R5).
- **How the work, the config and the end flow's work arrive (plans 6b, 7a
  and 6c).** Each comes as the requests do: the first read sends its method
  (`session/work_list`'s live view, `session/config`, `session/end_work`),
  which for a marshal client starts the session's forwarder for it, polling
  every 50ms and when the barrier asks, and sending the whole value when its
  encoding changed. The work is the service's, guarded as in process, and the
  config is its masked reads, so no update carries a secret (R8).
- **Host reads.** `SharedFilesystem` is set at dial time, and is true for
  the pipe. `ProviderTemplates` and `TrustPrompt` never change while the
  host runs, so each is fetched on its first read after an attach, with
  the current session, and kept (plan 7a).

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
  - **Decided (plan 4b): 50ms stays.** After plan 4c, one flush of the
    2,000-call session costs 4.3ms. A streaming turn on that session
    spends about 9% of one core on the agent's goroutine, and the idle
    loop projects only when an event marked the stack dirty. The client
    applies each patch in about 1ms, where in process the TUI spent about
    4ms of its `Update` goroutine on every refresh.
- **Every change reaches the mirror (plan 8).** The idle loop projects only
  when a session event marked the stack dirty. In process the TUI projected the
  state on every refresh, so a change that publishes nothing still showed.
  - **Eight state calls change what the projection reads and publish
    nothing:** `Rewind`, `SwitchBranch`, `ClearMessages`, `LogThinking`,
    `SetTodos`, `AddRunEvent`, `ClearRunEvents` and `SetWorkspace`. Each now
    publishes `transcript_changed`, which no ACP update carries. Over the pipe
    a `/rewind` would otherwise leave the old transcript on screen.
  - **A running call's output stays silent.** `AppendActiveToolCallOutput`
    would flood the broker. Instead, for a marshal client, the idle loop
    flushes on every tick, dirty or not, while the client's busy flag for the
    session is set or a subagent runs. That is the TUI's own rule: it
    re-projected on every tick then.
  - **A fence still flushes only what is dirty,** so no call's budget moves.
- **`busy` stays (plan 4b).** The agent's rule (the session's turn, or a
  subagent's own run) would change two T6 goldens: the `live` and `tray`
  scenes set the TUI's busy flag with nothing running
  (`transcript_golden_test.go`'s `buildLive` and `buildTray`), and an
  agent reading its own turn would build both trees as idle. So, as
  decided, the parameter stays:
  - A new `session/stack_busy` sets the busy flag every stack of that
    session is projected with for this connection, the subagents'
    included, as the TUI's rule does, and flushes them before it answers.
    The agent's rule serves a connection that never sets it.
  - The client's `Stack(subagentID, busy)` sends `session/stack_busy`
    when `busy` differs from the flag it last sent, and returns once the
    patch it caused is applied. So `Stack` blocks only at a flip, twice a
    turn, for one flush (R10).
- **Subagents.** The first read of a subagent's stack fetches it
  synchronously and subscribes. Later reads come from the mirror. Leaving
  the drill-in unsubscribes with `session/stack_unsubscribe` (new, plan
  4a), which keeps the agent from projecting trees nobody reads. The main
  transcript cannot be unsubscribed. Plan 4b gives the interface
  `ReleaseSubagent`, which the TUI calls when it pops a drill-in, and the
  unsubscribe releases that subagent's sheet with its stack.
- **Reads need no runner (plan 4b).** `session/stack`, `session/stack_node`
  and the idle loop resolve a session without requiring its agent runner,
  so a session whose provider failed to build still shows its transcript,
  as it does in process. They also project at the state's clock, as the
  service does. (They still find only the session's own subagents, where
  the service searches the whole tree: children never dispatch subagents,
  so no drill-in reaches a grandchild.)
- `StackNode` and `Callers` are plain calls (`session/stack_node` exists;
  `session/callers` is new).

### R5. Agent requests

Decided with the user, 2026-11-07 (it replaces the draft's extended
`session/request_permission`, `session/request_secret` and
`request_settled`):
- **A marshal client's open requests are a mirror.** The agent's request
  forwarder (R3) sends the whole `PendingRequests` value, keyed as C7 keys
  them, whenever it changes. It reads the service's `Pending`, so every
  request any part of the session raises reaches the client: the turn's,
  a subagent's, a pipeline's, a sudo prompt, a typed access request.
- **Answers are calls:** `session/answer_approval`,
  `session/answer_question`, `session/answer_skill_gate`,
  `session/answer_sudo` and `session/answer_access`, each handled by the
  service's own answer. The response carries the answer's error or the
  access result, and comes after the update that removes the request
  (R2), so the next read shows it gone. A key that names nothing open
  returns `CodeRequestSettled`, which in P4 is what a second viewer's late
  answer gets.
- **Sudo.** The prompt is in the open requests, which carry no password.
  The password travels only in `session/answer_sudo`'s params, and the
  client zeroes the caller's copy once the call returns, as the in-process
  handler did. Never logged, never in an update (R8).
- **`ReconcileAccess` reads the mirror.** The forwarder does what the
  TUI's per-update call did, settling a satisfied access request before it
  reads, and the update carries the access request to present.
- **The turn no longer asks a marshal client.** For a marshal client the
  turn's forwarder and the child-approval collector do not send
  `session/request_permission` or `session/request_question`, and do not
  deny for want of a bridge: the requests are the mirror's to show.
  Clients without the capability get exactly today's requests (R9).
- `Rollback` is a call (`session/rollback`, new).

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
- **`AddNote` stays synchronous (plan 7b).** It changes the transcript, so it
  pays one flush of the stack before it answers (R10), but an asynchronous
  note could land after a later call's effect: the agent runs each request on
  its own goroutine, so only a returned call is ordered. In process the TUI
  paid the same projection on its next refresh.

### R7. Sessions

- `Host.NewSession` sends `session/new`, and `Close` sends
  `session/close`. A swap closes the old session's mirror and its
  `Changed`, as C4 requires. How (plan 6c):
  - **A replacement is `session/new` with `replaces`,** naming the current
    session. The agent asks that session's service host for `NewSession`,
    which builds the new state inside the same runtime, as in process, and
    then files the runtime under the new session's ID. Without `replaces`,
    `session/new` starts a runtime, as before, for every client.
  - **A marshal client's `session/close` ends the conversation,** as the
    in-process `Close` does: the run cancelled, the requests settled, the
    state shut down. The runtime stays registered for the embedding to close
    on exit (plan 8), or the host's `CloseAll`. Closing the runtime from the
    TUI's `Update` would stop its MCP servers on that goroutine.
  - The client retires the replaced or closed session: it is unregistered and
    its `Changed` closed, its reads keep their final values, and its calls
    reach the agent, which refuses them.
- Subagents stay addressed through the parent session by ID (C2). The
  client never holds a session for a child.
- **Resume stays a restart.** The TUI reports the session to resume, and
  `launch` restarts the agent host on it. P4's reattach (a client joining
  a live session) is a different operation, and `session/load` already
  exists for it.

### R8. Secrets and logging

- These calls carry a secret:
  - the provider-key write (C10);
  - the probes, whose config may hold a typed key (C3, P2 plan 7a);
  - the user-config writes, whose providers or value may hold a typed key
    (plan 7a);
  - `session/answer_sudo`'s params (R5).
- Neither end logs their params or that response, at any level. If frame
  logging is added, it redacts these methods by name.
- T7 extends to the wire. A sudo password appears in no frame except
  the `session/answer_sudo` request that carries it, and in no update,
  notification or log line. A provider key likewise appears only in the
  call that writes or probes with it: the config mirror carries its mask
  (plan 7a).

### R9. Compatibility with existing ACP clients

- The web UI (through the bridge) and editors keep working unchanged:
  - existing methods change only by gaining optional params and result
    fields;
  - the marshal-only update kinds go only to clients that declare the
    capability (R2);
  - no default changes.
- **Where the TUI's semantics differ from an existing method,** the
  method gains an optional param that selects the TUI's, or, where the
  differences are too many to patch, a marshal client's call is served by
  the session's service and every other client keeps today's handler
  (decided with the user, 2026-11-08, for the turn's methods). The known
  differences, from P2's records:
  - `session/command` refuses during a turn, and the TUI never did;
  - `session/command` mints no action IDs and returns no agent goal, and
    `session/command_list` rewrites a headless command's kind and refuses a
    session with no registry. A marshal client's calls are served by the
    service (plan 7b);
  - `session/set_mode` names system access `system_access`, which the
    client sends (plan 6b);
  - `session/steer` delivers Send now as `now`, and refuses a queued text
    with no turn in flight, where the service queues it: a marshal client's
    call is served by the service (plan 6b);
  - `session/prompt` runs ACP's own turn slot, codes nothing as an
    `*acpwire.Error`, puts no queued text back and auto-resumes watches
    itself; `session/set_effort` wraps the setter's error as invalid params.
    A marshal client's calls are served by the service, and a marshal
    connection binds no watch-resume hook (plan 6b);
  - `session/cancel` is a notification, with no answer and no cause; a
    marshal client stops a run with `session/stop` (plan 6b);
  - `session/swarm_start` takes no role overrides, `session/sdd_start` no
    strategy, overrides or verify commands, and both `sdd_start` and
    `sdd_answer` report a gate as `stopReason: "gate"`. A marshal client's
    calls are served by the service, whose gate is `CodeHumanGateRequired`
    (plan 6c);
  - `session/work_*` encode times as RFC 3339 strings, code a stale
    cursor as invalid params, and serve work while the provider is not
    built. A marshal client's calls are served by the service, which
    answers in milliseconds and codes the cursor, and its live view is the
    `work_snapshot` mirror (plan 6b; G4);
  - `session/list` encodes `updatedAt` as RFC 3339 and the message count
    in `_meta`, and needs the host's session lister. A marshal client's
    call names its current session, whose service host serves it (plan 6c);
  - `session/close` closes the whole runtime. A marshal client's call is
    served by the service and ends the conversation only (R7, plan 6c);
  - `session/new` starts a runtime. With `replaces` it swaps the named
    session inside its runtime instead, for any client (R7, plan 6c);
  - every handler error that is not coded travels as fixed text. For a
    marshal client it carries its own message (R2, plan 6c);
  - `session/prompt` has no `watchResume`, the watch whose goal the agent
    writes (P2 plan 8a), and no `queued`: both are optional params, and a
    marshal client sends its text as one text block (plan 6b);
  - `config/probe_provider` answers a failed probe as a result, and
    `config/set_provider_key` takes no mask check; both read the process's
    home directory. A marshal client's calls name its session, and are
    served by that session's service host (plan 7a);
  - `session/diff` compares a base with the working tree, and the TUI's
    run outcome needs `base..head`. A marshal client's call is served by the
    service, which takes the head; ACP's handler gains none (plan 7b), and
    `session/file` is served by the service too, so the group has one rule;
  - `session/memory_list` lists what the session sees, and the TUI lists
    the project's own rows. A marshal client's `memory_list` and
    `memory_delete` are served by the service (plan 7b);
  - `session/plugins_install_scan` returns content counts, not the
    contents the TUI's review shows, and `plugins_install_confirm`
    neither checks the reviewed hash nor rolls back. A marshal client's
    plugin calls are served by the service, the adapter's logic (plan 7b);
  - `session/skills_*` find the global store with `skills.ScopeDir`,
    which ignores `MARSHAL_CONFIG_DIR` and `XDG_CONFIG_HOME`, remove a
    skill by name, and load without checking the store. A marshal client's
    `skills_list`, `skills_load` and `skills_remove` are served by the
    service, which keys a skill by scope and source (plan 7b);
  - `session/stack` and `session/stack_node` refuse a subagent ID that
    names no child with invalid params, where the service reads an empty
    stack; the client reads the refusal as the empty snapshot (plan 4b);
  - `session/stack` builds a session's trees with the agent's busy rule;
    `session/stack_busy` selects the TUI's (R4, plan 4b);
  - a marshal client gets no `session/request_permission` or
    `session/request_question` from a turn or a subagent: its requests
    are the `pending_requests` mirror (R5, plan 5).
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

The 1ms is for a call that changes nothing the agent projects. A call that
changes the transcript pays one flush of the stack before it answers (4.2ms
on the 2,000-call session after plan 4c), and is measured against 10ms.
`Stack`'s busy flip is one (plan 4b, R4): it happens twice a turn, at a
moment the transcript changes anyway. `AddNote` is the other (plan 7b): it
stays synchronous, for the order R6 gives, and in process the TUI paid the
same projection on its next refresh.

Plan 8 measures the key press. On the 2,000-call session, one typed
character's `Update` and `View` over the pipe take no longer than in
process, plus 1ms.

A plan that misses a budget stops and reports. It does not loosen the
budget.

### R11. Embedded mode

- One process, as today. The agent host and the TUI share the process's
  log file and its data directory.
- An agent-side panic still ends the process, as today. A pipe that
  closes unexpectedly ends the TUI with the agent's error, never with a
  hang. How (plan 8):
  - **The connection's end ends the program.** `launch` cancels the
    program's context when the client's connection ends, then returns the
    agent's error.
  - **A handler panic ends the embedding's connection.** The ACP server
    recovers a handler's panic and answers the call with an error, so a
    panicking agent would leave the TUI running. In process, a panic in a
    seam call ended the program. So the embedding's server ends the
    connection instead, and logs the panic with its stack.
- `marshal acp` and embedded `marshal` run the same agent host code. The
  only difference is the transport.
- **The session log gains one `acp dispatch` line per call the TUI makes**
  (plan 8): the method, its duration and its error, never its params. The
  server logs every request it serves, as `marshal acp` does.

## Design

| Area | Change |
|---|---|
| `internal/acpclient/rpc` (new) | The client, the mirror, the transport interface and the pipe. |
| `internal/acp` | Handlers for every new method; the per-session service moved from `inproc`; the new update kinds; cancellation, progress and response ordering in `Server`. |
| `internal/acpwire` | Params and results for the new methods; the update payloads. |
| `internal/launch` (new) | Embedded mode's composition root. |
| `internal/app` | Stops importing `tui`. `Run`'s TUI half moves to `launch`. |
| `internal/app/tui` | Asynchronous forms of the R6 calls. The test helpers build a pipe-connected agent over a test state. |
| `internal/acp/acptest` (new) | The test agent: a test state served over a pipe by the embedding's agent host, whose reads settle (V2). Only tests import it. |
| `internal/app/session/sessiontest` (new) | The 2,000-call session, shared by `internal/acp`'s budgets and the TUI's. |
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
   - **4b. The transcript read and the sheet** ([plan](plans/2026-11-07-p3-4b-transcript-plan.md)),
     after plans 3 and 4c, alongside plan 5. The session type's `Stack`,
     `StackNode` and `Callers`; the sheet (`Sheet` and `RefreshSheet`),
     whose subagent reads share the stack's subscribe-and-release rule;
     `ReleaseSubagent`, which the TUI calls when it pops a drill-in; the
     `busy` decision against the goldens (it stays, R4); stack reads that
     need no runner; and R4's cadence, decided with plan 4c's numbers (50ms
     stays).
   - **4c. The projection's cost** ([plan](plans/2026-11-07-p3-4c-projection-memo-plan.md)),
     alongside plan 3b. A memo of each node's wire form and encoding, so
     a projection after a change re-projects and re-encodes only the
     nodes that changed, in the agent's projector and in the service's
     `Stack`. `Build` stays whole: making it incremental needs stable
     transcript identities from `session.State`.
5. **Requests** ([plan](plans/2026-11-07-p3-5-requests-plan.md)),
   alongside plan 4b. `session/pending` and the request forwarder, the
   `pending_requests` update, the answer calls and `session/rollback`, all
   over the service's own request logic; the turn and the child collector
   leave a marshal client's requests to the mirror; and `rpc.Session`'s
   `Pending` mirror and answer calls (R5, decided 2026-11-07).
6. **Turns and runs**, in three plans:
   - **6a. The asynchronous calls** ([plan](plans/2026-11-07-p3-6a-async-calls-plan.md)),
     alongside layering. The R6 inventory, and the TUI's conversions of
     the calls it names, over `inproc`: they need only the TUI, which no
     other P3 plan before plan 8 changes. In process they still run on a
     goroutine, so the conversions and their ordering are proved before
     anything crosses a wire.
   - **6b. Turns and controls** ([plan](plans/2026-11-08-p3-6b-controls-plan.md)),
     after plans 4b and 5, alongside plan 7a. A marshal client's prompt,
     steering, interaction, effort and work calls served by the session's
     service through `marshalOr`; `session/stop` and the other new control
     calls; the work mirror and its `work_snapshot` forwarder; no
     watch-resume hook on a marshal connection; and a released service
     stopping its run (decided with the user, 2026-11-08).
   - **6c. Pipelines, the end flow and sessions** ([plan](plans/2026-11-09-p3-6c-ending-plan.md)),
     after plans 6b and 7a, alongside plan 7b. A marshal client's errors
     keeping their sentence (R2); swarm, SDD and plan authoring on the
     service; the end flow and its `end_work` mirror; a marshal client's
     `Close` and `ListSessions`; and the replacement, `session/new` with
     `replaces` (R7).
7. **The rest**, in two plans:
   - **7a. Config, providers and trust** ([plan](plans/2026-11-08-p3-7a-settings-plan.md)),
     after plans 4b and 5, alongside plan 6b. The host's settings calls,
     each naming the client's current session; the session's config writes
     and the MCP sign-in; the config mirror and its `session_config`
     forwarder, which carries the tools too; and the config package's JSON
     helpers, so a config crosses the wire unchanged.
   - **7b. Notes, commands, browsers and project data** ([plan](plans/2026-11-09-p3-7b-browsers-plan.md)),
     after plans 6b and 7a, alongside plan 6c. `AddNote`, which stays
     synchronous (R6, R10); the command list, read once; the commands and
     their row actions; the prompt history and the messages; the memory,
     plugin and skill browsers and the skill settings; and the project reads,
     all served by the service for a marshal client. Whichever of 6c and 7b
     merges second adds `Host.NewSession` and the checks that `rpc.Host` and
     `rpc.Session` implement the interfaces (R1).
8. **Switch** ([plan](plans/2026-11-09-p3-8-switch-plan.md)), after plans
   6c and 7b:
   - every change reaches the mirror (R4): the eight silent state calls
     publish, and the busy flag drives the idle flush;
   - `session/sync`;
   - `acp.Embedding` (R1, R11);
   - the test agent, `acptest`, whose reads settle against the service, and
     every TUI test over the pipe, its helpers taking the test (V2);
   - `launch` runs the TUI over the pipe;
   - `inproc` is deleted.

## Tests

- **V1. Conformance.** One table of tests per interface group, moved from
  `inproc`'s tests, runs against every implementation, from plan 3 on.
  Until plan 8 those are `inproc` and the client over a pipe to an agent
  host over a test state. After plan 8 the TUI has only the client. The
  harness keeps both arms: the service is still the agent's logic, and the
  two arms agreeing is what proves the wire. Plan 1 proves the protocol
  itself against the real server.
- **V2. TUI suite.** Every TUI test passes over the pipe, with only the
  helpers changed. T6's goldens are unchanged. How (plan 8):
  - **The helpers take the test,** so each test's agent closes with it.
  - **A test that changes the state directly sees the change.** Each mirror
    read of the test agent compares the mirror with the service's own answer.
    When they differ, it sends `session/sync` and returns the mirror's value.
  - **A state built without a session ID** is served under a fixed one,
    because every call names its session.
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
  imports `acpclient` but not `acpclient/rpc`. Only `launch` dials, and the
  test agent, `acptest`, which only tests import (plan 8).
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
- **`Build` and `SnapshotOf` are now most of a flush** (measured on plan 4c's
  branch, alone on the machine). With every node's wire form and encoding
  remembered, one flush of the 2,000-call session after a new tool call costs
  4.3ms. The snapshot (0.24ms) and the build (2.54ms) are about two thirds of
  that: the projection reuses every settled node — only the newly logged call,
  which is in flight, and its live beat re-project — but the turn tree is
  rebuilt from the transcript on every call. Making that incremental needs
  stable identities
  for transcript items, which `session.State` does not give — every
  `Transcript()` call copies every item, so nothing a build walks survives from
  one call to the next. Plan 4b decides R4's cadence with plan 4c's numbers, and
  plan 7b decides whether `AddNote` stays synchronous (R6, R10).
- **Some transcript changes would never reach the mirror** (by reading, while
  writing plan 8). The agent's idle loop projects a session's transcript only
  when a session event marked it dirty, and a fence flushes only what is dirty.
  - **Eight state calls publish nothing:** `Rewind`, `SwitchBranch`,
    `ClearMessages`, `LogThinking`, `SetTodos`, `AddRunEvent`,
    `ClearRunEvents` and `SetWorkspace`, the last on its own workspace broker
    only.
  - **A running call's streaming output publishes nothing either,** by design.

  In process the TUI re-projected on every refresh, so none of this showed.
  Over the pipe, a `/rewind` would leave the old transcript on screen until an
  unrelated event, and a running call's output would stop moving. Plan 8 fixes
  both (R4).

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

### Plan 4c — the projection's cost

- **`viewmodel.WireMemo`** (`internal/viewmodel/memo.go`) remembers each node's
  wire form, and its encoding once asked for, from the transcript's last
  projection. A node is reused when it is not live, its `Version` is not zero,
  and its ID, `Version`, `Live`, parent, children IDs and the working directory
  are what they were: the key names everything `wireNode` reads. Turn nodes and
  in-flight calls carry no version, so they are always projected afresh, and an
  entry captured while its node was live is never reused either — `Live` is part
  of the wire form and no version covers it, so a beat that settles with an
  unchanged version would otherwise keep the live flag. (The TUI's own
  `TestLiveBeatRendersInTheViewport`, run against the memo, found that during
  the run; `TestWireMemoDoesNotReuseAnEntryCapturedWhileLive` now pins it.) The
  memo holds only its last projection, and a reused node's slices and pointers
  are the ones handed out before, so a caller must not change a node it is
  given — what `rpc.Stacks` already requires of its readers.
- **`WireMemo.Encoded`** encodes a node on the first ask and keeps the bytes, so
  the agent's diff pays for the nodes that changed and no others. The service
  never asks, so the TUI's path pays no encoding at all.
- **`viewmodel.CheckWireMemo`** makes every memoized projection also project
  afresh and panic on a difference, naming the first node that differs. The
  `TestMain` of `viewmodel`, `acp`, `acp/service` and `app/tui` sets it, so the
  whole TUI suite — T6's goldens included — runs against it; the budget tests
  and benchmarks turn it off while they measure.
- **The projectors.** Each `stackProjector` (`internal/acp/stack.go`) projects
  through its own memo under its mutex, and `diff` and the activation take each
  node's encoding from it, the band aside (the band is not the memo's). Every
  path that projects still records what the client holds, so the memo's last
  projection is always the one `sent` describes.
- **The service's `Stack`** (`internal/acp/service/stack.go`) keeps two memos per
  session under their own mutex, not `mu`: one for the main transcript, one for
  the subagent read last, reset when another child is read. An ID that names no
  child still returns the empty snapshot before either memo is touched.
- **The numbers, at this branch's development machine**, on the 2,000-call
  session (`largeStackSession`, 1,201 nodes):
  - `BenchmarkStackFlushLargeSession`: 7.84ms/op and 23.1MB before → **4.3ms/op**
    (4.27–4.36 over three runs), 14.5MB, 28.7k allocations.
  - `BenchmarkProjectStateLargeSession`, the TUI's path before this plan: 6.5ms,
    20.3MB, 91.3k allocations. `BenchmarkWireMemoLargeSession`, the same
    projection through one memo: **4.1ms**, 14.4MB, 28.5k allocations.
  - `TestResponseBarrierBudget`: the changing call's median 8.47ms → **4.18ms**;
    the no-change median 13.4µs (budget 1ms).
  - `TestStackRoundTripBudget` 26.6ms (budget 50ms) and
    `TestStackPatchApplyBudget` 956µs (budget 2ms): neither got slower, since a
    first read encodes every node once, as before. That first read now carries
    its own 50ms budget (33.2ms measured): the memo makes the median warm, so
    the median alone would let a pathological activation through.
- **Proof.** `TestFlushedPatchesMatchAFreshProjection` compares what the client
  holds with a fresh `viewmodel.ProjectState` after every flush of a scripted
  turn, so a memo holding a stale encoding cannot hide behind the projector's
  own record of what it sent. `TestWireMemoMatchesAFreshProjection` drives one
  memo through a whole session — merged runs, an in-flight call, streaming
  reasoning, a settling call, a second turn, todos, a job exit, a subagent card
  and a moved active root — and `TestWireMemoReusesUnchangedNodes` pins the
  reuse. `TestStackReusesTheMainMemo`, `TestStackReusesTheSubagentMemo` and
  `TestStackResetsTheSubagentMemoForAnotherChild` pin the service's two memos.
- **What it leaves.** `Build` and `SnapshotOf` are now most of a flush (a finding
  above), and no node version, no `Build`, no `session.State` call and no wire
  byte changed.

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

### Plan 5 — agent requests

The third ACP handlers that call the client seam's service: the session's open
requests, the calls that answer them, and the client's mirror of them. For a
client without the capability nothing changed (R9); the TUI still runs on
`inproc`, in process, until plan 8.

- **`internal/acp/requests.go`** holds the connection's `RequestManager`: one
  `session/pending`, five answer calls and `session/rollback`, over `Lookup`
  (the session manager's `Service`), `Notify` (the connection's sink) and
  `Marshal` (the connection's capability record). Each handler decodes its
  params, refuses an empty `sessionId` as invalid params, resolves the session
  (`unknown session: <id>`), and returns the service's answer or its error
  unchanged, so `CodeRequestSettled` travels as error data.
- **The update carries two things.** `pendingState` calls the service's
  `ReconcileAccess` — which settles a typed access request that asks for
  nothing through the runtime's own reader — and then `Pending`, and returns
  both halves: `Requests` is the read, `Access` is what the access panel should
  present. `Pending` returns the first and `ReconcileAccess` the second, so both
  are the service's answers exactly.
- **The forwarder polls, one per session per connection.** It starts on the
  first `session/pending` from a marshal client, registers its fence before its
  goroutine starts, and wakes every `requestInterval` (50 ms) and on its fence.
  Each pass resolves the session through `Lookup`, reads the state, and sends a
  `pending_requests` update only when the encoding differs from the last one
  sent. Requests open and settle without a broker event this manager sees, so
  polling is what keeps the mirror current; the plan measured a pass at 0.13 µs
  on the largest fixture at its base commit (nothing re-measures it here). It
  never sends under its own mutex, and `Close` stops every forwarder.
- **`responseBarrier` is variadic now.** A `sessionFencer` interface names the
  one method the barrier calls, and the host passes `turns, works, watches,
  statuses, requests` in that order; a nil is skipped, so plan 3a's and 3b's
  call sites (untyped nils) compile unchanged. Never pass a typed nil manager.
- **An answer is a call whose response follows its update.** The barrier fences
  the request forwarder, so the update that removes an answered request is
  written before the answer's response, and a `Pending` read after the call no
  longer shows it.
- **The turn leaves a marshal client alone.** `TurnManager.isMarshal` (and
  `stackIntervals` now) reports the capability; `runTurn`'s `forward` returns
  right after the event's `session/update`, and `ensureChildApprovals` starts no
  collector for such a client. Neither denies for want of a bridge: the request
  stays open for the mirror, which reads the state, so a turn's, a subagent's, a
  pipeline's and a sudo prompt all reach the client.
- **`internal/acpclient/rpc/requests.go`** is the client's mirror: one
  `PendingState` entry, absent until its first read. `Pending` sends
  `session/pending` under a 5 s `pendingCallTimeout` on that first read (it
  takes no context), installs the result unless an update arrived first, and
  removes the entry on failure so the next read asks again; `ReconcileAccess`
  reads the same entry with the caller's context. `routeNotify` gained the
  `pending_requests` case beside `session_status`, applied to the attached
  session it names and dropped for one that is not attached; applying replaces
  the entry and fires `Changed`. The six answers send their methods; `AnswerSudo`
  zeroes the caller's slice when the call returns, whatever its outcome.
- **The conformance harness** (`internal/acp/conformance_requests_test.go`)
  opens the same two arms with eight more seam methods and a `serveRequests`
  that adds its manager to the pipe arm's barrier. Thirteen cases moved in from
  the service's tests or joined them, and each arm agrees, so no value needed a
  wire or service change: the session's requests are listed, a running
  subagent's approval is listed, a forced approval is marked, the state's access
  request is listed, the access request to present, once and deny, a child's
  approval settles on the child, an answer to a settled approval is
  request-settled, a question's answers are delivered, a skill gate's choice is
  recorded, an access answer resolves through the runtime, a sudo answer reaches
  the prompt, and a rollback restores and logs. The forwarder reconciles on
  every pass, so the service keeps the read-count assertion for
  `ReconcileAccess`; the skill gate's "unknown choice" row also stays in the
  service package, where the vocabulary check belongs.
- **The secret (V5).** `TestASudoPasswordCrossesTheWireOnce` wraps the agent's
  end of the pipe and the server's logger, answers with a canary password, and
  asserts its base64 form appears exactly once in the bytes the agent read —
  inside the `session/answer_sudo` request — and never in the bytes it wrote or
  in the log, while a `pending_requests` update listing the prompt was written.
  The canary caught a real defect in the first draft: the handler zeroed the
  decoded slice after answering, which is the same slice the service hands the
  prompt's handler, so the person's password was cleared before it was used.
  The transport handler must not touch it.
- **The budget (R10).** `TestAnswerCallBudget` drives 30 `AnswerApproval` calls
  over the 4a large session (2,000 calls, 1,201 nodes, a ~2.5 MB snapshot; the
  exact byte count moves with the fixture's timestamps); each call includes its
  barrier and its update. The median is **79.5 µs**, against R10's 1 ms. It
  skips under `-short` and with the race detector.
- **The counts moved** (re-measured on this branch): `acpclient.Methods` still
  has 110 rows, now 47 of them an existing ACP method (eight more: `Pending`,
  the five answers, `ReconcileAccess` and `Rollback`), 61 new in P3 carried by
  56 distinct new methods, and 2 carrying none; `acp.Server` registers 82
  methods (75 plus the seven).

### Plan 4b — the transcript read and the sheet

The transcript's four reads over one connection: the stack, a row's detail, a
node's callers and the sheet, with the busy flag the TUI used to own and a
release when a drill-in ends. For a client without the capability nothing
changed (R9); the TUI still runs on `inproc`, in process, until plan 8.

- **The reads need no runner.** `TurnManagerConfig` gained `Reads`, a lookup
  that returns the session's state and events without the prompt path's
  runner check; `Stack`, `treeFor` and both idle-loop lookups use it, and a
  nil `Reads` means `Lookup` at call time, so a caller that swaps `Lookup`
  after construction still reaches them. A session whose provider failed to
  build now serves `session/stack` and `session/stack_node`, where it answered
  "unknown session".
- **The agent projects at the state's clock.** `flushProjectorFor` and
  `stackSnapshot` take `now` from `State.Now()` instead of `time.Now()`, so the
  two arms agree under a test clock and nothing changes in production.
- **The busy flag stays, as a connection's flag for a session.** The
  connection-level `busy` field is gone: `session/stack_busy` sets the flag
  every stack of that session is projected with for this connection, in place
  of the agent's rule, and the client sends it only when it changes. The flag
  had to stay on the wire because `buildLive` and `buildTray` set `m.busy` with
  no run in flight, so an agent reading its own run slot would rebuild the
  `live` and `tray` goldens as idle. A flip re-projects every active stack of
  the session before it answers, clearing `final` on settled subagent
  projectors first, so the flip is read-your-writes without the barrier.
- **`internal/acp/sheet.go`** holds the connection's `SheetManager`: one
  `session/sheet`, a `sheet` forwarder per session a marshal client reads, and
  `session/callers`. The forwarder polls at `sheetInterval` (50 ms) and on its
  fence, because most of a sheet is computed from the state on every read and
  no event says it changed; it sends the whole sheet of each subscription only
  when its encoding moved. The session's own sheet is never released.
- **`registerTranscript`** owns the six transcript handlers, and
  `session/stack_unsubscribe` now releases the subagent's sheet with its stack,
  so leaving a drill-in stops both forwarders' work for that ID.
- **`responseBarrier` is variadic**, as plan 5 made it; this branch's
  registration order is `turns, works, watches, statuses, sheets, requests`.
- **`internal/acpclient/rpc`** mirrors what it reads. `Host` builds one
  `rpc.Stacks` in `Dial`, binds it to the connection and installs `OnChange` so
  every change it applies taps the attached session's `Changed`; `routeNotify`
  hands every `session/update` to it before decoding the kind, and applies a
  `sheet` update to the sheet mirror. `Stack` sends `session/stack_busy` on a
  flip and then reads the mirror, which fetches a tree on its first read; a
  refused read is an empty snapshot with that ID and keeps no entry, so the
  next read asks again. The sheet mirror keeps one entry per subagent ID read,
  installing the result unless an update replaced it mid-call.
- **`ReleaseSubagent`** is in the interface, the service and `popDrill`. Over
  the wire it forgets the sheet entry and sends `session/stack_unsubscribe`; in
  process it drops the service's subagent memo. Releasing 0 does nothing.
- **The conformance harness** (`internal/acp/conformance_transcript_test.go`)
  opens the same two arms with six more seam methods and a `serveTranscript`
  that adds both managers to the pipe arm's barrier. Seventeen cases run on
  each arm and agree; node lists compare by their JSON encodings, because the
  wire does not keep a nil slice apart from an empty one. One case needed a
  test-only concession: over the wire the client reads a mirror, and a band
  that goes stale with time alone pushes nothing, so "the stack reads the
  state's clock" flips the busy flag to make the agent re-project at the new
  clock. The gap it exposes — a time-only change never reaching a cached
  client — is plan 4a's caching design, not the wire type, the agent or the
  service, so it was left alone.
- **The budget (R10).** `TestStackBusyFlipBudget` alternates the flag over the
  2,000-call session (1,201 nodes, a 2,534,626-byte snapshot). The median is
  **4.1 ms** over 20 flips, against R10's 10 ms and plan 4c's 4.3 ms flush —
  the flip costs one flush, which is what it does.
- **The counts moved** (re-measured on the merged tree, after plan 5):
  `acpclient.Methods` has 111 rows, now 51 of them an existing ACP method
  (four more: `Sheet`, `RefreshSheet`, `Callers` and `ReleaseSubagent`), 58 new
  in P3 carried by 54 distinct new methods, and 2 carrying none; `acp.Server`
  registers 85 methods (82 plus `session/stack_busy`, `session/sheet` and
  `session/callers`).

### Plan 6b — turns and controls

- **A marshal client's turns and controls run on the session's service**
  (decided with the user, 2026-11-08). `marshalOr` (`host.go`) serves one
  method two ways: the service for a client that declared the capability,
  today's handler for every other client, and method-not-found when no other
  handler exists. Seven methods route through it: `session/prompt`, `steer`,
  `set_mode`, `set_effort`, `work_list`, `work_inspect` and `work_stop`.
  Nothing in the turn was rewritten, so the service's own tests moved into the
  conformance harness and pass on both arms.
- **The control manager** (`internal/acp/controls.go`) decodes each call's
  params, resolves the session's service and returns the service's answer or
  its error unchanged. It serves the turn calls; eleven new methods —
  `session/stop`, `review`, `agent_run`, `suggest`, `verify_proposal`,
  `steering_pop`, `steering_clear`, `set_sudo_unlock`, `subagent_cancel`,
  `sdd_clear` and `watch_resume_take`; and the work calls above.
- **`session/stop` is a call, `session/cancel` stays a notification.**
  A notification can carry no answer for the barrier to follow, and no cause.
  The method table's `Stop` row moved from `session/cancel` to `session/stop`.
- **The long calls are cancellable.** `session/prompt`, `review`,
  `agent_run`, `suggest` and `verify_proposal` take their context from
  `cancellable`, so the client's `$/cancel_request` stops the run, as in
  process.
- **The prompt's params are ACP's plus two optional fields.** The client sends
  its text as one text block, with `queued` and `watchResume`; the handler
  joins the block texts with a blank line and checks nothing, because the
  service decides what an empty text means and a watch resume has none.
- **`bindWatchResume` skips a marshal connection.** The TUI resumes a watch
  itself — it reads `Status().WatchResume`, takes the latch and sends a prompt
  that names the watch — so a bound hook would resume it twice. Every other
  connection keeps the binding.
- **`Release` stops the run in flight** with the cause `conversation ended`
  before it closes the session. The ACP host releases a service before the
  runtime closes, and the runtime's quiesce would otherwise wait for a run
  nobody can stop any more. With nothing running it does nothing.
- **The idle stack loop carries a service turn's transcript.** Nothing changed
  in the loop: it flushes while `HasActiveTurn` reports no turn, and the
  service's slot is not the `TurnManager`'s. A test pins that a marshal
  client's `stack_patch` arrives while its prompt is still in flight.
- **The work mirror's agent half.** One forwarder per session per connection
  polls the service's `Work()` every `workInterval` (50ms) and on its fence,
  and sends a `work_snapshot` update only when the encoding changed. The
  service owns the conversation and revision guard and the "serves no work"
  answer, so a marshal client reads exactly what the TUI reads in process.
  Other clients keep `work_state` and ACP's `Work*Wire` types (G4).
- **The barrier gained a fence.** `control`'s forwarders join the response
  barrier last, so a call returns only after the `work_snapshot` it caused is
  written (P3 R2, V3).
- **The client** (`internal/acpclient/rpc/turns.go`, `work.go`) sends the
  twenty seam calls (eighteen ACP methods) and mirrors the work in
  `rpc.Session`: an entry absent until
  the first read, its result installed unless an update arrived first, and
  `work_snapshot` replacing the state and tapping `Changed`. `session/set_mode`
  sends `system_access`, not `system` — `acpwire` allows only camelCase tags.
- **The conformance harness** (`internal/acp/conformance_controls_test.go`)
  adds the twenty seam methods and a `serveControls` for the pipe arm's
  manager and barrier. Thirty cases run on each arm and agree. Two moved
  cases could not carry an assertion across the wire, and each is narrower
  rather than stronger:
  - an unclassified error's *text* is replaced by the canonical "internal
    error" on the wire (F-SEC-37), so those cases assert the call fails with
    no agent code, not the message;
  - a refused `work_stop`'s summary cannot ride a JSON-RPC error response, so
    that assertion stays in `internal/acp/service/work_test.go` as a service
    rule, and the conformance case asserts the target naming both arms share.
    Putting it on the wire would need a coded error carrying the result.
- **The budget (R10).** `TestControlCallBudget` alternates 30
  `SetSudoUnlock` calls over the 2,000-call session; each includes its barrier
  and the `session_status` update. The median is **58 µs** against R10's 1ms.
- **Open point: two turn slots.** A marshal client's turn takes the service's
  slot, not the `TurnManager`'s, so a client without the capability on another
  connection could start a second turn on the same session. Embedded mode has
  one connection; P4's multi-viewer design decides how the two slots exclude
  each other.
- **The counts moved** (re-measured on this branch): `acpclient.Methods` has
  111 rows, now 61 of them an existing ACP method (ten more: `Review`,
  `RunAgent`, `Suggest`, `VerifyProposal`, `PopSteering`, `ClearSteering`,
  `SetSudoUnlock`, `CancelSubagent`, `ClearFinishedRun` and `TakeWatchResume`;
  `Stop` moved from one existing method to another), 48 new in P3 carried by
  44 distinct new methods, and 2 carrying none; `acp.Server` registers 96
  methods (85 plus the eleven), plus the `session/cancel` notification.

### Plan 7a — config, providers and trust

- **A host call names the client's current session.** `rpc.Host` remembers the
  session its last successful `Attach` bound, and every host call but
  `SharedFilesystem` carries that ID; a call before any attach fails with
  `rpc: no session attached` without sending. The agent resolves that
  runtime's service host, which acts on that session exactly as it does in
  process, and the barrier fences that session, so a read after a write sees
  the refreshed layers (R2).
- **`SessionManager` keeps the host with its session.** `services` holds a
  small unexported pair of the host and the session `service.New` returned;
  `Service` and the new `ServiceHost` share one lookup that builds the pair on
  first use under the rules `Service` already had (refusing a closing
  runtime), and `releaseService` releases the pair's session.
- **Two host reads are fetched once and kept.** `ProviderTemplates` and
  `TrustPrompt` send their method on their first read after an attach, under a
  5s timeout, and keep the answer; a failed read returns nothing and asks
  again next time. An empty catalog or no pending prompt is a kept answer too,
  so each read carries a set flag instead of inferring "unread" from nil.
- **`internal/acp/settings.go` holds the connection's `SettingsManager`**: the
  sixteen host methods, the seven session methods, `Close`, `fenceSession` and
  `sessionConfig`. `registerSettings` registers them; `config/probe_provider`
  and `config/set_provider_key` go through `marshalOr`, so a client without
  the capability reaches `ConfigManager`'s handler, unchanged (R9).
- **The config mirror.** `session/config` answers a `SessionConfig` and, for a
  marshal client, starts a forwarder per session that polls at
  `configInterval` (50ms) and on its fence, sending `session_config` with the
  whole value only when its encoding moved. `Tools` reads the same mirror, so
  the method table's row moved from `session/tools` to `session/config` and
  `session/tools` is not registered. `responseBarrier` gained the manager
  after `requests`.
- **The config package carries the JSON.** `internal/app/config/wirejson.go`
  adds `UnmarshalConfigJSON`, `UnmarshalLayersJSON` and
  `UnmarshalPresetsJSON`, each restoring a preset's `Pricing` from the
  `map[string]any` a JSON decode leaves to a `*pricing.ModelPricing` — the
  counterpart of `coercePresetPricing` — and `PathType`, the Go type of the
  `Config` field at a dotted TOML path with pointers removed.
  `Layers.HoistError` gained `json:"-"`: it is an `error`, which JSON cannot
  decode, and nothing outside the package reads it. `config/set_value` decodes
  its value into that field's own type, so `SaveUserValue` receives the value
  a working in-process call passes.
- **The reload flag rides in its result.** `session/reload_config` and
  `session/mcp_reconnect` answer a `ReloadResult` carrying the flag and the
  error as an `*acpwire.Error`, because a JSON-RPC error response carries no
  result (decision 6).
- **A sign-in's URL is progress.** `config/login_provider` and
  `session/mcp_login` report the URL as a `$/progress` value, handed to the
  client's `show` on its read goroutine, and take their context from
  `cancellable`, so the client's context ending stops the flow (R2). Probes and
  the preset setup are cancellable too.
- **A settings failure keeps its sentence.** The server writes every error that
  is not an `*acpwire.Error` as its code's fixed string (F-SEC-37), so a plain
  handler error arrives as `internal error`. The nine handlers whose sentences
  the client shows and the conformance cases read back (`trust/decide`,
  `config/set_value`, `config/save_providers`, `config/login_provider`,
  `config/probe_provider`, `config/set_provider_key`, `session/apply_config`,
  `session/save_project_config`, `session/mcp_login`) pass a failure through
  `settingsFailure`, which returns `&acpwire.Error{Message: err.Error()}` — the
  form the reload already gives its error — and leaves an `*acpwire.Error`, a
  JSON-RPC error and a cancellation unchanged. Nothing in process changes.
- **The client mirrors the session's config.**
  `internal/acpclient/rpc/settings.go` holds `Host`'s eighteen calls and
  `Session`'s ten. `Config` fetches the mirror on its first read and returns a
  clone; `Layers` decodes the raw layers on each read; `Tools` returns a copy;
  `routeNotify` applies `session_config` to the attached session, which
  replaces the entry and taps `Changed`. `SaveUserValue` refuses a nil or
  pointer value before sending, with the message an in-process call fails
  with.
- **The conformance harness has a host arm.** `openPipeArmsTB` returns the
  client's host as well as its session, `serveSettings` serves the settings
  manager on the pipe arm, and `hostArms` opens both. The new
  `conformance_settings_test.go` runs 37 cases on each arm and they agree; the
  cases moved out of `internal/acp/service` with their fixtures. Where a case
  compared a plain error with `errors.Is`, it compares the sentence instead,
  because only an `*acpwire.Error` keeps a code across the wire (plan 1);
  the reload's and the reconnect's errors ride in their result, so those keep
  exact equality.
- **The secrets (V5).** `TestASecretCrossesTheWireOnlyInItsCall` copies every
  byte the agent reads and writes: a saved key and a typed probe key each
  appear exactly once, inside its own call's params, and neither appears in
  what the agent writes or logs — the pushed `session_config` carries the
  mask.
- **The budget (R10).** `TestSettingsCallBudget` over the 2,000-call session:
  `ApplyConfig` medians **256µs**, and `SaveUserValue`'s pipe arm costs
  **101µs** more than its service arm, both against R10's 1ms.
- **The counts moved** (re-measured on this tree, after plan 5):
  `acpclient.Methods` has 111 rows, now 77 of them an existing ACP method (26
  more: the sixteen host rows from `TrustPrompt` to `SaveProvenance` other
  than `ProbeProvider` and `SaveProviderKey`, and `Tools`, `Config`, `Layers`,
  `ApplyConfig`, `ReloadConfig`, `ReconnectMCP`, `SaveProjectConfig`,
  `SaveVerifyCommands`, `PrepareMCPLogin` and `LoginMCP`), 32 new in P3
  carried by 32 distinct new methods, and 2 carrying none; `acp.Server`
  registers 106 methods (85 plus the 21).

### The counts after both plans

- **Re-measured on the merged tree.** `acpclient.Methods` has 111 rows: 87
  send an existing ACP method, 22 are carried by one of the 22 ACP methods
  new in P3, and 2 carry none. `acp.Server` registers 117 methods (85, plus
  plan 6b's eleven and plan 7a's 21), plus the `session/cancel` notification.
  Plans 6b and 7a each measured the counts on their own branch; these are the
  numbers after both.

### Plan 6c — pipelines, the end flow and sessions

- **A marshal client's error keeps its sentence.** `Server.dispatch` passes a
  handler's error through `settingsFailure` before it converts it, when the
  client declared the capability, so a plain error travels as
  `&acpwire.Error{Message: …}` with no code and every other error, JSON-RPC
  error and cancellation travels as before. The client rebuilds an
  `*acpwire.Error` from a `-32000` response's data when it carries a code *or*
  a message, so `err.Error()` is the sentence the TUI shows in process. The
  two cases plan 6b had narrowed — `promptPassesOtherErrorsThrough` and
  `reviewRunsOnTheRunPath` — assert their text again, and
  `TestSetEffortAnswersWhenItApplies`, `aStaleCursorIsCoded` and
  `TestSetProviderKeyForAMarshalClientRejectsAMask` were narrowed to expect
  the sentence instead of an opaque code.
- **The pipelines run on the service.** `registerControls` registers fourteen
  methods unconditionally and ten through `marshalOr`: `session/swarm_start`,
  `sdd_start` and `sdd_answer` route to the `ControlManager` over the session's
  service, and ACP's own handlers move into the `others` map, so a client
  without the capability keeps them byte for byte. Three new methods join
  them: `session/sdd_preflight`, `sdd_stop` and `sdd_author`. A gate crosses as
  `CodeHumanGateRequired`, the service's own `*acpwire.Error`.
- **The long calls are cancellable.** `swarm_start`, `sdd_start`,
  `sdd_answer`, `sdd_preflight`, `sdd_author`, `end_wait` and `exit_analysis`
  take `cancellable`'s context, so `$/cancel_request` ends the run in flight;
  `sdd_stop` does not, because it only drops a paused run.
- **The end flow is a manager beside the controls.**
  `internal/acp/ending.go` adds `EndManager` with `endInterval` (50ms), the
  five new methods (`session/end_work`, `end_accept`, `end_wait`,
  `exit_analysis`, `postmortem`), and `registerEnds`, which also routes
  `session/close` and `session/list` through `marshalOr`. The first
  `session/end_work` starts one forwarder per session, which polls every 50ms
  and when the barrier fences it, resolves its session on each pass and
  stops when the id goes, and sends `end_work` only when the membership's
  encoding changed — never under the manager's mutex.
- **The barrier has a new fence.** `responseBarrier` gains `ends` last, after
  `controls`, so every call's response follows the `end_work` it caused (R2,
  V3).
- **A marshal client's close ends the conversation.** `session/close` is
  served by the service's `Close`: the run cancelled, the requests settled,
  the state shut down. The runtime stays registered for the embedding to
  close (plan 8) or the host's `CloseAll`. A marshal client's `session/list`
  names its current session, whose service host answers with the stored
  sessions.
- **A replacement is `session/new` with `replaces`.** `SessionManager.replace`
  asks the session's service host for `NewSession`, which builds the new state
  inside the same runtime as in process, then re-keys the runtime under the
  new id and points the runtime's service pair at the new session. It holds
  `lifecycleMu` for the whole replacement. Without `replaces`, `session/new`
  starts a runtime, unchanged, for every client.
- **The client sends the group and mirrors the end flow.** `rpc.Session` gains
  the six pipeline calls (`pipelines.go`), the four end calls, `Close` and the
  end mirror (`ending.go`), which fetches `session/end_work` on its first read
  under a 5s timeout and is refreshed by each `end_work` update — an update
  for a session that has not read its membership is dropped. `rpc.Host` gains
  `ListSessions` and the unexported `replaceSession`, with `retire` behind both
  close paths and `Stacks.forget` for a replaced session's stacks. Plan 7b
  merged first, so this plan also adds `Host.NewSession`, a wrapper over
  `replaceSession`, and the two compile-time interface checks (decision 8).
- **The harness runs the group on both arms.** `conformance_ending_test.go`
  adds `serveEnds` for the pipe arm, `TestEndingConformance` (16 session cases
  over `seamArms()`, 2 host cases over `hostArms()`, including
  `listSessionsWithoutADatabaseIsCoded`) and `TestEndCallBudget`; the cases
  moved out of `internal/acp/service` with their fixtures. The tests that
  drive the in-process `swap` or read the paused run stay in the service's
  package.
- **The budget (R10).** `TestEndCallBudget` over the 2,000-call session:
  `AcceptEnd` medians **23.8µs** over 30 calls, its barrier and the end
  forwarder's fence included, against R10's 1ms.
- **The counts moved** (re-measured on the merged tree, plans 6c and 7b):
  `acpclient.Methods` has 111 rows, now 109 of them an existing ACP method
  (this plan's eight: `PlanPreflight`, `StopSDD`, `AuthorPlan`, `ActiveWork`,
  `AcceptEnd`, `WaitForEnd`, `RunExitAnalysis` and `Postmortem`), none carried
  by a method new in P3, and 2 carrying none; `acp.Server` registers 139
  methods (117 plus this plan's eight and plan 7b's fourteen), plus the
  `session/cancel` notification. These supersede the 6b-and-7a numbers above
  and plan 7b's branch measurement below, which was taken before this plan
  merged.
- **Open for P4.** A replacement holds `lifecycleMu` while it builds the
  runner, so a close or a load of another session waits for it; and a marshal
  client's `session/list` reads the project database for any `cwd` it names,
  where ACP's own handler checks the trusted roots (F-SEC-33).
### Plan 7b — notes, commands, browsers and project data

- **`internal/acp/browsers.go` holds the connection's `BrowserManager`**: the
  fourteen new methods (`session/note`, `session/command_action`,
  `session/prompt_history`, `prompt_history_save`, `messages`, `file_index`,
  `intelligence`, `plans`, `sdd_discard`, `sdd_scaffold`, `skills_install`,
  `skills_autoload`, `skill_gate_set`, `skill_gate_clear`) and, through
  `marshalOr`, the fourteen existing ones (`session/command`,
  `command_list`, `diff`, `file`, `memory_list`, `memory_delete`, the five
  `plugins_*`, `skills_list`, `skills_load`, `skills_remove`). It resolves a
  session's service with `RequestManager.service`'s two refusals, holds no
  forwarders and contributes nothing to the barrier.
- **Every browser handler keeps its sentence.** Each new method registers
  through `settingsFailureOf`, and each `marshalOr` method's marshal branch
  is wrapped in it, so a plain service error reaches the client as an
  `*acpwire.Error` carrying its text rather than the server's fixed
  `internal error` (F-SEC-37). A client without the capability keeps ACP's
  handler, byte for byte (R9): the fourteen lines it used to register are
  gone from `registerHandlers`, and the managers they belonged to are handed
  to `registerBrowsers` as the `others` map.
- **The two installs and the two scans are cancellable.** `plugins_install_scan`,
  `plugins_install_confirm` and `skills_install` take their context from
  `cancellable`, so the client's context ending stops a clone or an install
  (R2). No test can hold a clone open — `plugins.NewInstaller` offers no seam
  for it — so the service's `TestPluginScanWithAnEndedContextStagesNothing`
  keeps pinning what an ended context does.
- **A note is synchronous.** `AddNote` returns once the response barrier
  flushed the note's stack patch, so a read after it shows the note, as in
  process (R6). It stays a call rather than a push because the agent runs each
  request on its own goroutine: only a returned call orders the note before a
  later call's effect.
- **The command list is fetched once and kept.** `rpc.Session.Commands()`
  sends `session/command_list` on its first read under `commandsCallTimeout`
  (5s) and keeps the answer, a failed read not kept; the registry does not
  change while the runtime runs, and a replacement shares it. `commands_changed`
  was dropped for that reason, as plan 7a dropped `session/tools`.
- **`internal/acpclient/rpc/browsers.go` holds the session's twenty-eight
  calls**, each sending its method with its params type and the session's ID,
  as the turn calls do.
- **The harness's pipe arm serves the browsers and flushes the transcript.**
  `serveBrowsers` registers the manager over the served service with a nil
  `others`; `serveTranscript` now attaches its event broker to the served
  state (decision 6), without which a note marks nothing dirty and the idle
  stack loop never flushes it. `conformance_browsers_test.go` runs 31 cases on
  each arm and they agree; the cases moved out of `internal/acp/service` with
  their fixtures, and where one compared a plain error with `errors.Is` it
  compares the sentence instead, because only an `*acpwire.Error` keeps a code
  across the wire.
- **The budgets (R10).** Over the 2,000-call session, `AddNote` on the pipe arm
  medians **4.56ms** over 30 runs against R10's 10ms — one flush, which the TUI
  already paid in process: the in-process call and its read median **4.32ms**
  beside it. `TestBrowserCallBudget`'s `SetSkillGate`, whose answer carries a
  `session_status` update, medians **96µs** against R10's 1ms.
- **The counts moved** (re-measured on this branch, plan 6c not merged):
  `acpclient.Methods` has 111 rows, now 101 of them an existing ACP method
  (fourteen more: `AddNote`, `CommandAction`, `RecentPrompts`, `SavePrompt`,
  `Messages`, `FileIndex`, `Intelligence`, `Plans`, `DiscardCandidate`,
  `ScaffoldPlan`, `InstallSkill`, `SetSkillAutoload`, `SetSkillGate` and
  `ClearSkillGateDecision`), 8 new in P3 carried by 8 distinct new methods,
  and 2 carrying none; `acp.Server` registers 131 methods (117 plus the
  fourteen), plus the `session/cancel` notification.
