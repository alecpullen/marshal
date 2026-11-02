# P2 — TUI client seam

Parent: [`design.md`](design.md) §4.5. This spec replaces §4.5's sketch
where they differ, and settles §8's transcript-shape question.
Depends on: P1 (merged at `ee15d2ed`) and the TUI simplification
([`../tui-simplification/feature-spec.md`](../tui-simplification/feature-spec.md),
P-A–P-E, merged at `991c18d7`). Agent side and TUI only: no bridge
change, no new ACP method, no database schema change. ACP messages that
already exist change only by gaining optional fields (C6).
Status: drafted 2026-10-20; revised 2026-10-25 against `991c18d7`
and since, plan by plan (Revision record). Delivered as nine plans
(Migration order), each written against `main` once the previous one has
landed; plans 2 and 3 are split into sub-plans.

Decided with the user (2026-10-20):
- **Transcript on the wire:** `WireTree`. The TUI renders the same
  projection the web UI does. Gaps are closed with additive fields, and
  expanded rows fetch their detail with `session/stack_node`.
- **Scope:** the whole agent side, not only `session.State`. Runners,
  event brokers, factories, the database, the tool registry, config and
  credential editing, and the project's files and git all move behind the
  seam. Design §7's P2 done-when changes to match (Done when).

## Goal

After P2 the TUI reaches the agent side only through one client
interface. It holds no `*session.State`, runner, broker, database handle
or registry. It reads and writes no config file, project file or
keychain entry, and makes no provider or git call. An in-process adapter
implements the interface over today's runtime, so nothing a user sees
changes. P3 then implements the same interface over ACP and deletes the
adapter.

## Where the code is today

- **`tui.New` takes the session state** (`internal/app/tui/model.go:1698`).
  Non-test TUI code calls 91 distinct `session.State` methods and reads
  the `Config` (85 sites), `WorkingDir` (30) and `StartedAt` (1)
  fields. `sessionsheet`, the access, gate, plugins, skills, doctor and
  doc panels hold the state too.
- **37 other agent-side dependencies arrive as options.** `Run`
  passes 40 `tui.With*` options (`internal/app/app.go:2805-3021`, plus
  `accessTUIOptions` at `app.go:2147`). The client-local ones are the
  clipboard, the working directory for display and the open-connect
  flag. The rest are agent side:
  - the agent, swarm and pipeline runners and their factories, plus the
    plan-author, custom-agent, subagent, review and suggestion factories;
  - five event brokers (job, watch, steering, workspace, subagent) and
    the work service;
  - the database, the memory store, the tool registry, the command
    registry, the skill index and the file index;
  - config layers, the reloaders, the auth resolver, the model cache,
    the home and data directories and the trust callbacks;
  - the interaction, effort and access-request seams and the Send-now
    seam. These are bound to the runtime rather than to one state, so
    they keep working after a session swap;
  - the session swapper, the end controller and the reference finder.
- **The TUI polls.** It re-reads the state on a 150ms tick
  (`model.go:4785`) and on every broker event, and `refreshViewport`
  rebuilds the transcript from it (`viewport_render.go:54`).
- **The TUI applies policy itself.**
  - "Always allow" writes a permission rule to the user config, applies
    the new config and resets the runner's policy rules
    (`model.go:3456`).
  - The settings, connect, presets and agents panels write config files
    with `config.Save*`.
  - Approvals, questions, skill gates and sudo prompts are read with
    `Pending*` and answered with `Resolve*` on the owning state, the
    subagent's child state included.
  - The TUI also installs plugins (`git clone`), writes SDD starter
    plans and postmortems, and reads git state in the project
    (`tui/gitinfo`, `tui/changedfiles`, and the run-outcome branch diff
    in `runoutcome.go:173`). `internal/acp` already imports `gitinfo`
    and `changedfiles` (`acp/turn.go:17`).
  - After a connect, the TUI sets up presets itself: it loads the
    models.dev catalog cache and the preset provenance file from the data
    directory, runs `presetsetup.Apply` (`connect/connect.go:625-639`)
    and saves the provenance (`model.go:7601`).
- **The TUI writes into the transcript.** 139 of its 141 `AddMessage`
  calls append a system message, which is persisted with the session.
  The other two add the user's prompt.
- **Some seams already exist:** `AgentRunner`, `SessionSwapper`,
  `EndController` and `viewmodel.WorkService` are interfaces, and the
  work service already has ACP counterparts (`session/work_*`).
- **ACP already has 70 methods**, including `session/prompt`, `steer`
  (whose `now` delivery is Send now), `steering_status`, `set_mode`
  (mode, auto and system access), `access_state`, `set_effort`,
  `command`, `stack`, `stack_node`, `run`, `work_*`, `swarm_*`, `sdd_*`,
  `memory_*`, `skills_*`, `plugins_*`, `list`, `resume` and `config/*`.
  It cannot be imported by the TUI: `acp` imports `app`, which imports
  `tui`.
- **The boundary today.** Under C1's rule, 66 non-test TUI files make
  135 disallowed imports. The largest are `app/session` (40 files),
  `viewmodel` (23) and `tools/registry` (12).
- **TUI tests build state directly:** 210 `session.New` calls in 30 test
  files, and 225 `tui.New` calls.

## Transcript spike (design §8)

The question was whether the `WireTree` projection drops anything the
TUI renders. It does, and every gap can be closed by adding a field.
Re-checked on 2026-10-25 against the reworked renderer.

- **The TUI renders full records.** `viewmodel.Node` carries
  `*session.TranscriptItem` and `[]registry.AuditEvent`, and the
  renderer reads them directly (`transcript.go:565`, `steps.go:155`,
  `spine.go:98-160`). Subagent nodes are gone since P-B. Instead the
  tray reads each running subagent's view and child state
  (`livebox_view.go:99-117`).
- **Collapsed rows read fields the projection leaves out:**
  - a message's reasoning (`transcript.go:591`);
  - a beat's narration texts (`steps.go:155`), joined as one markdown
    document. `WireStep.headline` loses its emphasis and the separator
    before `rest`, so the two cannot rebuild the text;
  - from a call row on the beat's spine (`spine.go:98-160`):
    - its subject (`callSubject`), built from raw arguments: the shell
      command with a leading `cd` into the active root removed, an
      `agent.run` description, or the symbol or search subject.
      `WireCall.Args` is capped at `WireTextCap` (4096 bytes,
      `viewmodel/wire.go:21`), which cuts the JSON of a large edit;
    - the diff stat, counted over the full diff;
    - the hook indicator and the sandbox's kill reason;
    - a failed call's last three output lines (`failedTail`,
      `spine.go:242`). `WireCall.Output` keeps only the first 4096
      bytes, which loses the end of a long failing test run;
  - from a running call outside any beat, which renders through the older
    active-call renderer (`renderActiveToolCall`): its command line. A
    settled call never sits outside a beat (revision record against
    `75ee412d`), so the older settled head and its diff preview need
    nothing.
- **The tray and the drill-in breadcrumb read the subagent view.** The
  tray reads a running child's newest narration headline. The
  breadcrumb reads the child's model, provider and fallback flag
  (`view.go:181-194`). These are session status, not transcript (C5).
- **Expanded rows are covered.** `session/stack_node`
  (`acp/stacknode.go:22`) returns uncapped arguments, original arguments,
  output, diff, error, hooks, sandbox metadata, symbols, the finish
  reason, a beat's narration and its reasoning.
- **The cost** is porting about 4.5k lines: the renderer
  (`transcript.go`, `steps.go`, `spine.go`, `expand.go`, `symbolrow.go`,
  `receipt.go`, `callers.go`, the browse files and `viewport_render.go`),
  the tray's glue (`livebox_view.go`), and the tests that build session
  records to render. P-A deleted folding and the density ladder, so
  neither needs porting.

Decision: `WireTree`, with the C6 additions.

## In scope

1. The wire types, the client interface and the in-process adapter.
2. Every TUI use of `session.State` and of the ~36 agent-side options,
   moved behind the interface, group by group.
3. The transcript rendered from the wire projection.
4. Policy side effects the TUI applies today, moved into the adapter.
5. A boundary test that keeps the TUI on the seam.

## Out of scope (follow-ups)

- **F1.** ACP handlers for the methods marked "new in P3", the JSON-RPC
  client, its pipe transport, and embedded mode over the pipe. P3.
- **F2.** The bridge transport, `session/request_secret` brokering and
  daemon-side OAuth refresh. P4.
- **F3.** Splitting client preferences (`[tui]`) from agent config. P2
  reads them from the session config as today.
- **F4.** The web UI rendering the new wire fields. They are additive, so
  it keeps working unchanged.
- **F5.** MCP scope and the other open questions in design §8.
- **F6.** Device-code or forwarded OAuth for a remote daemon. P2's login
  flows assume the agent shares the client's machine, as today.

## Behaviour

### C1. Packages and the boundary

- **`internal/acpwire` (new).** The wire types: params, results, read
  values, update and request payloads, and error codes. It imports only
  the standard library, like `web/`. The transcript projection types
  (`WireTree`, `WireNode` and its payloads), the work types
  (`WorkItem`, `WorkSnapshot` and the rest) and the existing ACP types
  the client uses (`StackSnapshot`, `NodeDetail`, `PermissionRequest`,
  `CommandResult`, …) move here. `internal/viewmodel` and `internal/acp`
  use them from here, and their JSON does not change.
- **`internal/acpclient` (new).** The `Host` and `Session` interfaces
  (C2) and the error helpers. It imports the standard library,
  `acpwire` and the data packages below. P3 adds the JSON-RPC
  implementation here.
- **`internal/acpclient/inproc` (new, deleted in P3).** The adapter (C4).
- **`tui/gitinfo` and `tui/changedfiles`** move to
  `internal/repo/gitinfo` and `internal/repo/changedfiles`. They read
  the project, so they belong on the agent side, and `acp` already uses
  them.
- **The boundary.** Non-test Go files under `internal/app/tui/...` may
  import:
  - the standard library and third-party modules;
  - `internal/app/tui/...`, `internal/acpwire` and
    `internal/acpclient`;
  - the data and pure packages `app/config`, `llm/routing`,
    `llm/schema`, `interaction`, `app/clipboard`, `strutil`,
    `diffview`, `jsonextract` and `progress`.

  `llm/modelcatalog` and `app/presetsetup` are agent side. The first
  fetches and caches the models.dev catalog, and the second reads and
  writes the provenance file.

  From `app/config`, only types and pure functions may be used. Its
  functions that touch or locate files are denied by name: every
  `Save*` and `Load*` function, plus `UserDir`, `DataDir`,
  `UserConfigPath` and `ProjectConfigPath`. A write call returns the
  path it wrote, for the TUI's "persisted · <path>" notes. Pure helpers
  such as `LookupPath`, `Diagnose` and `Default` stay allowed. Every other
  `marshal/internal/...` package is agent side. `internal/viewmodel`
  is agent side too. Any pure text helper the TUI still needs from it
  moves to an allowed package. The TUI calls 28 `viewmodel` functions
  today:
  - text helpers such as `ScrubText`, `FirstSentence` and
    `StripEmphasis`, which move;
  - work predicates such as `WorkActive` and `WorkNeedsAttention`, which
    move with the work types;
  - the rest read audit events or nodes, such as `IsShellFamily`,
    `ElideWorkdir`, `JobStartID` and `ToolID`. They go away once rows
    take those values from the wire (C6).
- **What the boundary means at run time.** TUI code reads and writes no
  file in the project tree, the config directory or the data directory.
  It runs no subprocess against them and opens no network connection.
  The only exceptions are C12's client-local actions.

### C2. The client interface

- **Two interfaces.** `Host` covers the process and user level:
  sessions, config, providers, trust, history, memory, plugins and
  skills. `Session` covers one conversation. `tui.New(host, session,
  opts...)` replaces `tui.New(state, opts...)`. A swap (`/new`,
  `/clear`, resume) gets a new `Session` from the `Host`.
- **One ACP method per interface method.** Each method names its ACP
  method in its doc comment, as `// ACP: session/steer (exists)` or
  `// ACP: session/note (new in P3)`. A read names the request that
  fetches it and the `session/update` kind that refreshes it.
- **Reads are synchronous and local.** `Stack`, `Status`, `Pending`,
  `Config` and the other reads return the current value without
  blocking on I/O. The adapter computes them from the runtime. P3 serves
  them from a local mirror fed by notifications. The TUI keeps its
  render-time pattern of reading the current value.
- **Calls** take a `context.Context`, may block, and return `acpwire`
  results or an error.
- **`Session.Changed() <-chan struct{}`** is a coalescing signal with a
  buffer of 1. It fires after anything a read returns may have changed.
  The TUI re-reads when it fires and never infers what changed. The
  150ms tick stays as the fallback for clocks and for transcript growth.
- **Error codes.** Every error the TUI branches on today gets a code in
  `acpwire`, and the adapter maps the Go error to it. These are: the
  session quiescing, no turn-stop service, a human gate required, the
  keyring unavailable, auth required, a provider error with its fields,
  a stale work cursor, a request already settled, and busy. The TUI
  tests codes with `acpclient.IsCode`. Any other error is shown with
  `err.Error()`, as today.
- **Subagents** are addressed through the parent session by subagent ID:
  `Stack` and `Sheet` take an ID, and `Pending` tags each request with
  its owner (C7). No `Session` value exists for a child.

### C3. Wire rules

- JSON names are camelCase. Times are Unix milliseconds (0 means
  unset), durations are milliseconds, and enums are strings. Optional
  fields use `omitempty`.
- An `acpwire` field is exported and JSON-tagged. Its type is a bool,
  number or string, a slice, map or pointer of those or of `acpwire`
  types, or `json.RawMessage`.
- `acpclient` signatures may also use `config.Config` and its parts, the
  `routing` types and the `schema` types. These are data shared within
  the module, and their JSON round trip is tested (T3).
- **Secrets.** No read returns one. A literal API key in a config read is
  replaced by `""`, and its source (`config`, `env`, `keychain`, `oauth`
  or `none`) is reported alongside, as `config/get` does. Only two calls
  carry a secret: the provider-key write (C10) and the sudo answer (C7).
  Neither is logged.
- **Large text.** The transcript keeps `WireTextCap`. Every other
  result that can be large says how it is capped in its doc comment.

### C4. The adapter

- `inproc.New(inproc.Deps)` returns `(*inproc.Host, *inproc.Session)`,
  which implement `acpclient.Host` and `acpclient.Session`. `Deps`
  holds the state and everything `Run` passes as an agent-side TUI
  option today. `Run` builds `Deps` instead of those options.
- Until plan 5 replaces the swapper with `Host.NewSession` and
  `Host.Resume`, `(*Host).Swap(state)` serves today's swap path. It
  closes the old session and returns one over the new state, which
  `tui.SessionSwapResult.Session` carries to the TUI.
- It must not import `internal/app`. TUI tests import it, and `app`
  imports `tui`.
- The `Host` holds what `accessTUIOptions` binds to today (and what
  `WithSendNow` bound to until plan 2b deleted it): the runtime's current
  state and runner, read under the runtime gate. A `Session` handed out
  before a swap never reaches the session that replaced it.
- It behaves exactly like the code it replaces. Where the TUI applied a
  side effect itself, the adapter now does: writing and applying a
  permission rule, reloading config after a write, and logging a
  rollback's audit event.
- A `Session` replaced by a swap answers reads with its final values,
  and its calls return a closed-session error.
- `tui.WithLegacyState(*session.State)` is a temporary escape hatch for
  groups not yet moved. Plan 1 adds it, and Plan 9 deletes it.
- The adapter is deleted in P3.

### C5. Status

- `Session.Status() acpwire.SessionStatus` is one value carrying
  everything the status line, tray, footer, input gating and turn
  logic read today. It arrives over several plans (Migration order).
  The session ID is not in it: `Session.ID()` (plan 1) carries it.
  - **identity:** working directory, generation sequence, leaf ID and
    branch count. The start time is a `Sheet` field (plan 2c), because
    only the sheet reads it;
  - **route:** the active route, the turn route and whether it is
    running, turn usage and window, quota, and effort override;
  - **access:** the interaction state (mode, auto and system access),
    sudo unlock, trusted, and whether there is a backup;
  - **activity:** kind, label and start, and whether a subagent is
    running;
  - **workspace:** project root, active root, git info (branch,
    worktree and whether the root is a repository; plan 2b), and the
    number of files changed against the sheet's base, which the status
    line shows (plan 2c). HEAD itself stays agent side: the adapter keeps
    it as that base, and only the changed-files diff reads it;
  - **live state:** browser info. Sandbox info arrives with plan 4,
    because only the running-call and approval renderers read it, and
    both read the session's. The current notice arrives with plan 6,
    which also moves notice writes (C9);
  - **queues and runs** (plan 2b): the steering queue, the Send-now
    snapshot and the newest receipt for its turn, the running job count,
    subagent views, swarm and SDD progress (the tray's plan-run row), the
    SDD gate and a pending watch resume. The job list and the watch
    snapshot are not carried: the TUI caches both from their events
    today, but nothing reads them. The work snapshot keeps its own pump
    until the work group moves (plan 5). The todos reach the tray through
    the band (C6). Active skills, the loaded tool count, the tool budget,
    the session rules and the sandbox posture are read only by the
    session sheet, so they are `Sheet` fields (plan 2c).
  - **subagent views** leave out the child state. Each carries what
    the tray and the drill-in breadcrumb read today: the role, label,
    model, provider and fallback flag. While the child runs, it also
    carries the child's newest narration headline (`subagentHeadline`,
    `steps.go:703`). While the session keeps the child's state, it also
    carries `drill`: what a view drilled into the child reads about it,
    namely its active root, active route and activity (plan 3d).
- The job, watch, steering, workspace and subagent broker pumps go away.
  Their data are `Status` fields, and the adapter fires `Changed` when
  they publish and after a `SendNow`. A value no event announces (swarm
  and SDD progress, the SDD gate) reaches the screen on the tick, as
  today.
- `Session.Sheet(subagentID int64) acpwire.SessionSheet` carries what the
  session sheet's sections read (plan 2c). For the session, that is:
  - the start time;
  - the tool and file activity, aggregated per tool and per file from the
    tool-call log, so the log itself never crosses the seam;
  - the session rules and the active skills;
  - the tool budget, the loaded tool count and the sandbox posture;
  - the context pack's per-section sizes;
  - the files changed against the sheet's base;
  - the recent turn metrics and the session's usage totals;
  - the fleet rows and the repo's file and symbol counts.

  For a subagent it carries the subset `childSheetData` uses today: the
  child's own start time, activity, rules, skills, budget, loaded tools
  and sandbox posture, with the session's fleet rows and repo counts.
- **The sheet refreshes on request.** The values read from the database
  and git are cached agent side, and `Sheet` never blocks.
  `Session.RefreshSheet(ctx, flags)` re-reads the values its flags name,
  then fires `Changed`. Until plan 5 moves turns behind `Prompt`, the TUI
  owns the turn boundaries. It therefore calls `RefreshSheet` at the
  points where it re-reads those values today: startup, turn end, a
  workspace move, a new session and opening the sheet.
- ACP (P3): `session/status` with a `session_status` update, and
  `session/sheet`. `RefreshSheet` sends `session/sheet` with its flags,
  and the result fills the mirror that `Sheet` reads. Two parts already
  have reads: `session/access_state` serves the interaction state and
  `session/steering_status` the Send-now snapshot (both exist).

### C6. Transcript

- `Session.Stack(subagentID int64, busy bool) acpwire.StackSnapshot`
  returns the projected tree with the band node: the one projection
  `session/stack` already serves. P-B removed the legacy projection, so
  the stack needs no parameter to select one. `busy` is the TUI's own
  turn flag until plan 5 moves turns behind `Prompt` (revision record
  2026-11-02).
- `Session.StackNode(ctx, nodeID, subagentID)` returns
  `acpwire.NodeDetail`, the uncapped detail behind a row. ACP:
  `session/stack_node` (exists).
- **One projector.** The TUI (`viewport_render.go:66-81`) and ACP
  (`stackSnapshotOf`, `acp/stack.go:107`) already build the same
  snapshot from a state. Both then call `Build`, `Project`, `BandOf`
  and `ProjectBand` in the same order (`acp/stack.go:201-204` and
  `305-310`). The snapshot assembly and that sequence become one
  `internal/viewmodel` function, called by both `acp` and the adapter.
  ACP keeps its patch diffing.
- **`WireNode` gains `version`**, the hex of `Node.Version`, so a client
  caches rendered rows without hashing them.
- **Additive fields.** The projection carries every field a collapsed row
  reads, computed by the helpers the renderer uses now:
  - `WireMessage.reasoning`, capped, with `truncated` set when it is
    cut;
  - `WireStep` gains `narration`, the beat's trimmed narration texts in
    order. The renderer joins them as one markdown document, and
    `headline` (emphasis stripped, cut at the first sentence) plus
    `rest` cannot rebuild them;
  - `WireTool` gains `verb`, the word the row leads with;
  - `WireCall` gains:
    - `subject`, today's `callSubject`, built server-side against the
      active root and scrubbed;
    - `summaryLine`, the summary's first line;
    - `diffStat`, counted over the full diff;
    - `hookHint`, `killedReason` and `jobId`;
    - `tail`, a failed call's last three output lines;
  - `WireRunning` gains `subject`, `callId` and `args`;
  - `WireThinking` gains `startedAt`, for the live reasoning box;
  - `WireRunEvent` gains `bodyLines`, the whole body's line count.

  Plan 3a fixes the JSON names. Anything only an expanded row needs
  comes from `StackNode`, and plans 3b and 3c add to `NodeDetail` what
  their readers need. `WireTool.subagentId`, which drill-in needs,
  already exists (P-B), and `WireSubagent` is gone.
- **The renderer moves from `viewmodel.Node` to `acpwire.WireNode`.**
  Node identity becomes the wire ID and kind. Expansion, selection,
  browse and inspect work on wire nodes. So do the TUI's indexes over
  the tree (`completionIndex`, `rowBeats`) and the tray's inputs: the
  band's tasks, and the held rows, which find their completion event by
  `WorkKey`.
- **Callers.** The callers line uses
  `Session.Callers(ctx, nodeID) ([]string, error)`, cached per node as
  today. ACP: `session/callers` (new in P3).
- **Subagent drill-in** reads `Stack` with the subagent's ID, starting
  from a dispatch row's `subagentId` or a completion event.

### C7. Agent requests

- **`Session.Pending() acpwire.PendingRequests`** lists every open
  request in the session tree, subagents included.
  - Request kinds: approval, question, skill gate, sudo, mode access and
    the SDD human gate.
  - Each request carries:
    - an opaque key that stays stable while the request is open;
    - its owner: subagent ID and label, or 0 for the session itself;
    - the fields the TUI displays today. These include the approval's
      "why" line: the requesting step's owner and narration headline,
      which `approvalWhyFor` (`steps.go:686`) looks up by step ID in
      the owning state.
- **Answers:**
  - `AnswerApproval(ctx, key, ApprovalAnswer{Decision, Edited})`, where
    the decision is `once`, `session`, `always` or `deny`;
  - `AnswerQuestion(ctx, key, answers)`;
  - `AnswerSkillGate(ctx, key, choice)`;
  - `AnswerSudo(ctx, key, SudoAnswer)`;
  - `AnswerAccess(ctx, key, choice)` and `CancelAccess(ctx, key,
    reason)`;
  - `AnswerSDDGate(ctx, answer)`.
- **`Rollback(ctx)`** restores the backup and logs the audit event. The
  approval dialog stays open after it, as today.
- **Policy moves to the adapter:**
  - `always` saves the user rule, applies it to the live config and the
    runner's policy rules, and falls back to a session rule if the save
    fails;
  - `session` adds a session rule;
  - the skill gate's remembered choices are recorded agent side.
- **Settled requests.** A request settled elsewhere leaves `Pending`.
  Elsewhere means cancelled by steering, answered by another client in
  P3, or answered by shutdown. Answering a key that is no longer open
  returns the request-settled code, and the TUI closes its dialog,
  replacing today's `ClearPrompt` path.
- **The sudo password** travels only in `AnswerSudo`'s params, as
  `SudoAnswer{OK bool, Password []byte}`, mirroring
  `session.SudoAuthDecision`. The adapter hands the same slice to the
  handler, which zeroes it after use as today (`[]byte` is base64 in
  JSON). The password never appears in a read, an update or a log line.
- **ACP (P3):**
  - approval, skill gate and mode access go through
    `session/request_permission`, extended with the decision;
  - question through `session/request_question`;
  - sudo through `session/request_secret` (new);
  - the SDD gate through `session/sdd_answer` (exists) plus a request
    kind.

### C8. Turns and runs

- `Prompt(ctx, PromptParams) (PromptResult, error)` returns when the turn
  ends. Agent side, it begins and ends work, adds the user message and
  runs the runner. It replaces `runAgentCmd` and the two user
  `AddMessage` calls. ACP: `session/prompt` (exists).
- **Other runs:**
  - `Review` (new);
  - `RunAgent`, the custom-agent run-now (new);
  - `StartSwarm` with role overrides (`session/swarm_start` exists);
  - `StartSDD` with plan, strategy and overrides
    (`session/sdd_start` exists);
  - `AuthorPlan` (new);
  - `Suggest` (new);
  - `VerifyProposal`, the build/test proposal parsed from the last
    reply (new).

  Each carries what the TUI passes its factory today.
- **Turn control:**
  - `Stop(ctx, cause)` covers today's begin/request/end turn-stop
    sequence (`session/cancel` exists);
  - `Steer` and `SendNow` (`session/steer` exists; Send now is its
    `now` delivery), and `PopSteering` and `ClearSteering` (new) cover
    the steering queue. `SendNow` arrives early, in plan 2b, because
    the receipt it returns is part of `Status`;
  - `SetInteraction(ctx, patch)` replaces the interaction setter. It
    covers mode, auto and system access (`session/set_mode` exists and
    takes all three). `SetEffort` exists, and `SetSudoUnlock` is new;
  - `CancelSubagent` (new);
  - `ClearRunEvents`, `ClearSDDProgress` and `SetIdle` (new);
  - `TakeWatchResume` (new).
- **Work.** The work service's list, inspect, stop and acknowledge calls,
  and its snapshot, become `Session` methods (`session/work_*`
  exists). The work pump and the work types move with them (plan 5).
- **Ending a session.**
  - The `EndController` calls become `Session` methods: `ActiveWork`,
    `AcceptEnd`, `WaitForEnd` and `RunExitAnalysis`. `Postmortem(ctx)`
    writes the report agent side and returns its path.
  - `Close(ctx)` replaces `ResolvePendingForShutdown` and `Shutdown`
    (`session/close` exists).
  - `Host.NewSession(ctx, name)` and `Host.Resume(ctx, id)` replace the
    swapper (`session/new` and `session/resume` exist).

### C9. Notes, notices and commands

- **Notes.** `AddNote(ctx, text, contentType)` appends a system message
  exactly as `AddMessage(RoleSystem, …)` does today: persisted, in the
  transcript, and visible to every client. ACP: `session/note` (new).
- **Notices.** `SetNotice`, `ClearNotice(category)` and `DismissNotice`
  change the banner. The current notice is in `Status`. Error
  classification for notices (`noticeForError`) moves to the adapter,
  which returns the notice with the error code (C2).
- **Slash commands.**
  - TUI-only commands that open panels or change view state stay in the
    TUI.
  - Headless commands run through `Command(ctx, name, args)`, which
    uses the same registry agent side (`session/command` exists).
  - The completion list comes from `Commands()`
    (`session/command_list` exists).
  - The TUI renders the returned text or doc as it renders
    `commands.Result` today.
- **Logging.** The TUI logs through its own logger, not the session's.

### C10. Config, providers and trust

- **Reads.** `Session.Config()` returns the session's effective config
  with literal keys redacted (C3). `Session.Layers()` returns the layers
  with provenance, redacted the same way. `config.Diagnose` stays
  client-side: it is pure over those two values.
- **Writes** are `Host` calls, because they change files every session
  reads. Each save function the TUI calls today becomes one call with
  the same semantics:
  - user value, user providers, user presets, user rule, provider key;
  - project config and verify commands.

  After writing, the adapter reloads the config of the session it
  serves, as the TUI's `applyNewConfig` (`model.go:1314`), the config
  reloader and `RebaseInteractionConfig` do today. It returns the path
  written, the new config and its diagnostics.

  A provider write keeps any stored key it does not name, as
  `config/set_providers` does. A write during a turn returns the busy
  code, and the TUI shows today's "Stop the active turn before applying
  settings."
- **Models and providers.**
  - `ProviderTemplates()` is new.
  - `ListModels(ctx, provider)` lists models from the agent's model
    cache, and includes the thinking options the TUI resolves today
    (new).
  - `ProbeProvider` uses `config/probe_provider` (exists).
  - **Preset setup after a connect** runs agent side as one `Host` call
    (new). The call loads the models.dev catalog, reads the provenance
    file and runs `presetsetup.Apply`, then returns the proposed
    presets. Accepting them saves the presets and the provenance
    together. Today the TUI does all of this itself.
- **OAuth logins** (Codex connect, MCP auth) run agent side. The call
  reports the authorization URL in a progress update, the TUI opens it
  (C12), and the call returns when the callback lands. This matches how
  ACP's `/mcp auth` works today.
- **Trust.** `Host.DecideTrust(ctx, dir, decision)` replaces the trust
  prompt callback. A trust refresh happens inside the config reload.
- **Skill gate settings** (enabled flag, remembered decisions, clearing
  one) become `Session` calls.
- **Tools.** The agents panel's tool lookup becomes a `Tools()` read.

### C11. Project data

- **History.**
  - Session list for resume: `Host.ListSessions` (`session/list`
    exists).
  - Input history: `RecentPrompts` and `SavePrompt` (new).
  - Message queries the TUI makes today: the last assistant reply, user
    turns for rewind, and whether the session has a user turn. These use
    `Messages(ctx, filter)` (new).
- **Index facts.**
  - The `@` file index: `FileIndex()` (new).
  - The doctor's intelligence counts and detected language servers:
    `Intelligence()` (new).
- **Memory, plugins and skills panels** use the existing `memory_*`,
  `plugins_*` and `skills_*` methods.
- **Plans and worktrees.**
  - SDD plan discovery, the draft path, removing a candidate and writing
    a starter plan: `Plans()`, `DraftPlan` and `ScaffoldPlan` (new).
  - Run outcome: the run detail, branch diff and file reads use
    `session/run`, `session/diff` and `session/file` (exist). This
    replaces `runOutcomeDoc`'s own git calls (`runoutcome.go:32`).
  - Fleet rows are in `Sheet`.
- **Git info and changed files** arrive through `Status` and `Sheet`.

### C12. Client-local

These stay in the TUI:
- terminal I/O, rendering and the theme;
- key handling, the input box, expansion, browse and selection, and the
  clipboard (P0);
- the tray's view state: the held-row window (`heldSince`), the
  dismissed finished plan, the open card and the cursor;
- opening a URL in the browser;
- opening a project file in `$EDITOR`, offered only when
  `Host.SharedFilesystem()` is true. It is always true in P2, and true
  for P3's pipe.

## Design

| Area | Change |
|---|---|
| `internal/acpwire` (new) | Wire types and error codes. Projection, work and existing ACP types move here, with identical JSON. |
| `internal/acpclient` (new) | `Host`, `Session` and `IsCode`. |
| `internal/acpclient/inproc` (new) | The adapter. Gains each group's methods as its plan lands. |
| `internal/viewmodel` | Builds `acpwire` types. Gains the shared projector and the C6 fields. |
| `internal/acp` | Uses `acpwire` types and the shared projector. |
| `internal/repo/gitinfo`, `internal/repo/changedfiles` | Moved from `internal/app/tui/`. |
| `internal/app/app.go` | `Run` builds `inproc.Deps` and calls `tui.New(host, session, …)`. The agent-side `tui.With*` options are deleted as their groups move. |
| `internal/app/tui/...` | Every group moves onto `Host`/`Session`. The renderer moves to wire nodes. Test setup goes through shared helpers that build an adapter over a test state, so P3 changes only the helpers. |

## Migration order

The TUI works after every plan.

1. **Foundation** ([plan](plans/2026-10-25-p2-1-foundation-plan.md)).
   - `acpwire` with the error codes and T2, `acpclient`, an almost
     empty `inproc`, and `tui.New(host, session, …)` with
     `WithLegacyState`. Each group's plan moves its own types into
     `acpwire`: ACP's steering types in plan 2b, the projection types
     in plan 3, the request types in plan 4, the work types in plan 5
     and the command types in plan 6.
   - The test helpers, and every test moved onto them.
   - The gitinfo/changedfiles move.
   - The boundary test (T1), with an allowlist of every current
     violation by file and import.
   - The ACP mapping test (T4).
2. **Status and events** (C5), in three plans:
   - **2a. Status** ([plan](plans/2026-10-25-p2-2a-status-plan.md)).
     `Session.Status` with identity, route, access, activity, workspace
     roots and browser info: the reads the status line, footer and
     input gating take straight from the state. No event changes.
   - **2b. Runs, queues and events**
     ([plan](plans/2026-10-25-p2-2b-events-plan.md)). `Session.Changed`,
     and `Session.SendNow` from C8. The job, watch, steering, workspace
     and subagent pumps go away, and the adapter keeps what only their
     events carry: Send-now receipts and the pending watch resume. Also
     the steering queue, the running job count, git info, subagent views
     (with the tray's headline), swarm and SDD progress and the SDD
     gate. ACP's steering wire types move into `acpwire`. The work pump
     stays until plan 5.
   - **2c. Sheet** ([plan](plans/2026-10-26-p2-2c-sheet-plan.md)).
     `Session.Sheet(subagentID)` and `Session.RefreshSheet`. The sheet's
     database and git caches move into the adapter, along with HEAD and
     the changed files. The status line's changed-file count comes from
     `Status`.

   2a needs no events, because the TUI already re-reads on its tick.
   2b changes how the TUI learns that something changed, so it is
   kept apart from the reads. The sheet's data come from the database
   and git, so 2c keeps them off the render path.
3. **Transcript** (C6): the renderer, the TUI's indexes over the tree
   and the tray's band and held-row inputs. This is the largest and
   riskiest group, so it comes early, in four plans:
   - **3a. Projection** ([plan](plans/2026-10-27-p2-3a-projection-plan.md)).
     T6's golden files, recorded before anything changes. The projection
     types and their wire names move to `acpwire`, one shared projector
     serves `acp` and the TUI, the row helpers move to `viewmodel`, and
     the projection gains `version` and the collapsed-row fields. Agent
     side only, apart from wrappers and one snapshot call in the TUI.
   - **3b. Browse actions** ([plan](plans/2026-10-27-p2-3b-browse-plan.md)).
     `Session.StackNode` and `Session.Callers`. The TUI starts keeping a
     wire index of the tree it renders, which it builds and projects
     itself. The inspector, copy, open and the selection's full-subject
     copy read a node's wire form from that index and its uncapped detail
     from `StackNode`, instead of its payload. Callers lookups move agent
     side.
   - **3c. Rows** ([plan](plans/2026-11-01-p2-3c-rows-plan.md)). The row
     renderers take wire payloads from the same index. An expanded row the
     wire cut reads its `StackNode` detail, which the TUI fetches off the
     render path and caches by node version; a running row reads the wire
     alone. The settled-row renderer nothing reaches is deleted, and so is
     the TUI's index of `viewmodel` nodes.
   - **3d. Tree** ([plan](plans/2026-11-02-p2-3d-tree-plan.md)).
     `Session.Stack`. The TUI renders the snapshot it returns: its tree,
     node identity, expansion, regions, browse items, band and completion
     index come from wire nodes, and drill-in reads the subagent views in
     `Status`. The text helpers move to `strutil`, T1b gains `Subagent`,
     and the allowlist loses the transcript group's entries. The files
     that also hold approval, work or notice code keep their entries for
     plans 4, 5 and 6.
4. **Agent requests** (C7).
5. **Turns and runs** (C8).
6. **Notes, notices and commands** (C9).
7. **Config, providers and trust** (C10).
8. **Project data** (C11).
9. **Close-out.** Delete `WithLegacyState`, empty the allowlist, write
   the list of methods P3 must add, and update design §4.5, §7 and §8.

Each plan removes its group's allowlist entries.

## Tests

- **T1. Boundary.** A test in `internal/app/tui` parses every non-test
  file under the tree. It fails on any import or denied `config` function
  outside C1's rule that is not on the allowlist, and on any allowlist
  entry that is no longer needed. Plan 9 empties the allowlist.
- **T1b. Moved reads.** A test in `internal/app/tui` fails on any
  `<x>.state.<Name>` selector whose `Name` is in its list of `State`
  methods and fields that have moved behind the seam. Plan 2a starts the
  list, each later plan adds its own, and plan 9 deletes the test along
  with `WithLegacyState`. T1 only shrinks when a file loses its last
  agent-side import, and most files mix groups, so T1b is what shows a
  group has moved.
- **T2. Wire types.** `acpwire` imports only the standard library. A
  reflection walk over its exported types enforces C3's field rules.
- **T3. Shared data types.** A fully populated `config.Config` and the
  `routing` and `schema` values in `acpclient` signatures survive a JSON
  round trip unchanged.
- **T4. ACP mapping.** A table in `acpclient` names each method's ACP
  method and whether it exists. A test in `internal/acp` checks that
  every method marked existing is registered on `acp.Server`.
- **T5. Adapter parity.** For each group, adapter tests show each method
  does what the code it replaced did. For example, `always` writes the
  rule, applies it and resets the runner's rules. `AddNote` persists a
  system message. A swapped-out session's calls fail closed.
- **T6. Renderer equivalence.** Before porting, plan 3a records the old
  renderer's output for a fixture set as golden files. The set covers
  every node kind, collapsed and expanded. That includes beats with a
  todo tag, plan lines, completion events with their issue reports,
  receipts and a running call outside any beat. It also covers diffs,
  hooks, a killed sandbox, a failed call's tail, a running call, and the
  tray with running, held and plan-run rows. The goldens are rendered
  under `NO_COLOR` with the flat theme, as the tray's frames are, and the
  fixtures' state runs on a fixed clock. The ported renderer must match
  the goldens byte for byte.
- **T7. Secrets.** A config read has no literal key. A sudo answer's
  password appears in no `Status`, `Pending`, update or log output.
- **T8. Suite.** Every plan keeps the TUI and app suites green.

## Done when

- The TUI imports no agent-side package, holds no `*session.State`, and
  makes no project, config or keychain I/O. T1 passes with an empty
  allowlist, and `WithLegacyState` is gone.
- The transcript renders from the wire projection, and T6 passes.
  Design §8's transcript question is closed.
- Every `acpclient` method names its ACP method, and T4 passes. The
  methods marked new are listed for P3.
- `go test ./...` and `go vet ./...` pass, apart from the known local
  failures.
- A manual run behaves as before. It covers:
  - a turn with an approval, a question and a sudo prompt;
  - an Ask/Act switch;
  - a `/settings` change, and `/connect` with preset setup;
  - `/new` and resume;
  - a subagent and a background job in the tray, and a drill-in;
  - a swarm or SDD run;
  - exit with a postmortem.

## Assumptions

Made while drafting, without asking:

- **Typed interface.** The interface has typed methods with synchronous
  reads and a change signal, rather than a generic
  `Call(method, params)`. This keeps the TUI's render-time reads and
  its tests synchronous. Over ACP, the reads become a mirror fed by
  notifications.
- **Notes stay persisted.** The TUI's system notes stay persisted in the
  transcript, as today. Making some of them client-only would change
  behaviour, so it is left for later.
- **Notice state stays agent side**, because the runtime also sets
  notices.
- **`config.Config` stays the data type on the seam**, with secrets
  redacted and writes targeted. A wire-specific config view would mean
  rewriting the settings browser.
- **Adapter placement.** The adapter lives under `acpclient`, not
  `app`, so TUI tests can use it without an import cycle.
- **Plan order.** The transcript comes third, because it is the riskiest
  group.
- **`$EDITOR`** is offered only on a shared filesystem.

## Revision record

**2026-10-25, against `991c18d7`.** The draft predates three merges:
the TUI simplification (P-A–P-E), interaction modes and models.dev
presets. Changes:

- **C6.**
  - The `StackQuery.LiveWork` parameter is dropped: one projection
    remains, and ACP never had the parameter.
  - The `WireSubagent` additions are dropped: P-B removed subagent
    nodes and added `WireTool.subagentId`.
  - "One projector" now describes what is left to share.
  - Call rows take `subject`, `diffStat` and `tail` from the spine
    renderer. `head` and `diffFiles` remain only for calls outside a
    beat.
  - Density and folding are no longer ported (P-A deleted them).
- **C5.** The tray replaces the now bar. The interaction state replaces
  the mode, and subagent views carry the child's narration headline
  for the tray.
- **C8.** `SetInteraction` replaces `SetMode` and `SetSystemAccess`.
  Send now maps to `session/steer`'s `now` delivery, which exists.
- **C1.**
  - `llm/catalog` no longer exists and is removed from the allowlist.
  - `interaction` (pure) is added.
  - `llm/modelcatalog` and `app/presetsetup` are listed as agent side.
- **C10.** Preset setup after a connect moves agent side.
- **C11.** The run outcome uses `session/run`.
- **C7.** Each request carries the approval's "why" line.
- **C12.** The tray's view state stays client-local.
- **Other.**
  - The counts and line references in "Where the code is today" and the
    spike were re-measured with a type-checked scan of the TUI tree.
  - T6 and the manual check cover the tray.

**2026-10-25, against `4a81ef11` (plan 1 merged).**
- **Plan 2 splits into 2a (status), 2b (runs, queues and events) and 2c
  (sheet)** (Migration order). The handlers the five broker pumps feed do
  more than re-read:
  - the watch handler merges events into a list and starts a resume
    turn;
  - the steering handler keeps Send-now receipts;
  - the workspace handler re-reads git.

  Replacing the pumps is therefore its own plan, and the plain reads
  move first.
- **C5.**
  - The session ID comes from `Session.ID()`.
  - The pinned spinner label no longer exists.
  - Sandbox info waits for plans 3 and 4, and the notice for plan 6.
  - Active skills, the loaded tool count, the tool budget and session
    rules are sheet fields. The todos reach the tray through the band.
- **Tests.** T1b, the moved-reads guard, is added.

**2026-10-25, against `2268ce0b` (plan 2a merged).** Found while writing
plan 2b:
- **The work group moves in plan 5, not 2b** (C5, C8, Migration order).
  `acpwire` cannot hold `viewmodel`'s work types as they are. They have
  `time.Time` and `progress` fields, `WorkQuery` and `WorkTarget` carry
  no JSON tags, and `WorkAck` embeds `WorkTarget`. ACP already has a
  separate wire form (`acp/work.go`), but the tray and the tasks panel
  render `viewmodel.WorkItem` fields, `progress` snapshots and the work
  predicates. Moving the pump therefore means porting that rendering,
  and that work belongs with the work calls, which C8 already places
  in plan 5. The work pump stays until then.
- **The job list and the watch snapshot are dropped, not carried**
  (C5). The TUI caches both from their events, but no production code
  reads either cache. `Status` carries the running job count, which
  the state already holds: the job manager sets it just before each
  job event is published.
- **`SendNow` moves to plan 2b** (C8). `Status` carries the newest
  Send-now receipt. The TUI shows the receipt a submission returns on
  the very next frame, before its event arrives. A receipt in `Status`
  can only do the same if the adapter records it before the call
  returns, so the call has to move with the receipts.
- **HEAD moves with the sheet** (C5). Only the sheet's changed-files
  diff reads it. The status line needs only the branch, the worktree
  and whether the root is a repository.
- **ACP's steering wire types move into `acpwire`** (C1), with their
  conversion in `viewmodel`. Their JSON is unchanged.

**2026-10-26, against `726e1506` (plan 2b merged).** Found while writing
plan 2c:
- **The sheet refreshes on request** (C5). Its database and git values
  are cached agent side, and `Session.RefreshSheet(ctx, flags)` re-reads
  the ones its flags name. The TUI decides when, because it owns the turn
  boundaries until plan 5 moves turns behind `Prompt`. It calls the method
  at the points where it re-reads those values today. `Sheet` and
  `RefreshSheet` both map to `session/sheet`.
- **HEAD stays agent side** (C5). The adapter keeps it as the
  changed-files base and never returns it. `Status` carries the
  changed-file count instead, because the status line reads it.
- **The start time is a sheet field** (C5). Only the session sheet reads
  it, and a subagent's sheet needs the child's own.
- **The sheet carries aggregates, not the tool-call log** (C5). The tools
  and files sections read per-tool and per-file totals. The adapter
  computes them with the functions the sheet uses today, which move to
  `viewmodel`, so the log never crosses the seam.
- **The repo's language mix is dropped** (C5). Nothing has ever filled
  it.

**2026-10-27, against `75ee412d` (plan 2c merged).** Found while writing
plan 3a:
- **Plan 3 splits into 3a (projection), 3b (browse actions), 3c (rows)
  and 3d (tree)** (Migration order). The renderer, browse mode and the
  drill-in share one tree, so the tree itself has to move in one plan.
  But the reads of node payloads can move first: the browse actions to
  `StackNode`, then the row renderers to wire payloads over a tree the
  TUI still builds. Each step is checked against T6's goldens, which
  plan 3a records before anything changes.
- **No settled call renders outside a beat** (C6, spike). `Build` files
  every audit under a beat: an ID'd one, or a loose one for a call with
  no step (`viewmodel/stack.go:480-494`). It never hands an audit to
  `passthrough`. The older settled-row renderer (`toolRowHead`,
  `renderCompletedToolCallIn` and the collapsed diff preview in
  `toolBody`) is reached only by tests and by `nodeCopyText`'s item
  fallback, which no audit reaches. `head` and `diffFiles` are dropped,
  and plan 3c deletes that renderer. A *running* call can still stand
  outside a beat, and T6 covers it.
- **The collapsed-row fields change** (C6). These are added, each because
  a collapsed row reads it:
  - `WireTool.verb`. The subject's fallback uses the same verb table, so
    the table moves agent side and the verb travels with the row.
  - `WireCall.summaryLine`. The existing `summary` turns line breaks into
    spaces, so its first line cannot be recovered.
  - `WireCall.jobId`, for the "job started" result.
  - `WireRunning.subject`, `callId` and `args`. These are the running
    row's subject, the key of the work row it points at, and the stray
    running row's command line. `target` is not enough: the stray row
    shows the arguments even when the call has a path.
  - `WireThinking.startedAt`, for the live reasoning box's elapsed time.
  - `WireRunEvent.bodyLines`. The collapsed "… N more lines" counts the
    whole body, which `body` caps.

  `WireCall.stepId` and `WireRunning.stepId` are dropped. No collapsed
  row reads a step ID, and the inspector takes it from `stack_node`'s
  `CallDetail.StepID`, which exists.
- **T6** (Tests). The goldens are rendered under `NO_COLOR` with the
  flat theme, as the tray's frames are: colour comes from the theme, not
  from the wire. The fixtures run on a fixed clock. `session.WithClock`
  already exists, but nothing read it, so plan 3a makes the state stamp
  its transcript times from it.

**2026-10-27, against plan 3a (written while it ran).** Found while
writing plan 3b:
- **The TUI starts keeping a wire index in 3b, not 3c** (Migration
  order). The inspector numbers a call among its beat's calls, lists a
  beat's rows, and copies a row's full subject. Each of those reads the
  node's neighbours as well as the node. The TUI already builds the tree,
  so it projects it after its content check and keeps the wire nodes by
  ID. Browse actions read node fields from there and ask `StackNode` only
  for what the wire caps or leaves out. Plan 3c's renderers read the same
  index, and plan 3d swaps its source for `Session.Stack`.
- **`NodeDetail` moves to `acpwire` with its JSON unchanged** (C1, C3).
  `CallDetail.hooks` carries `registry.HookMetadata` as raw JSON: its
  `failed_open` name breaks C3's camelCase rule, and the raw bytes are
  what ACP sends today. `CallDetail.sandbox` is raw already.
  `acpwire.HookDetail` and `acpwire.SandboxDetail` decode the parts the
  TUI reads.
- **`NodeDetail` gains `step` and `completion`, and `CallDetail` gains
  `summary`** (C6). The inspector shows the actor and the "why" line of a
  call's own step, and a beat's stored todo. It also shows a completion's
  whole output and report, which the wire caps. Copy takes a call's
  summary when the call has no result, which `output` cannot say for an
  edit.
- **`WireTool` gains `callers`** (C6). It marks a row whose call changed
  a resolved symbol, which is the row a callers lookup asks about. The TUI
  asks `Callers` for those rows, and only while it is not drilled into a
  subagent, because the index then holds the child's transcript.

**2026-11-01, against `d12762c6` (plan 3b merged).** Found while writing
plan 3c:
- **Expanded rows fetch their detail off the render path** (C6). The
  renderer never calls the session. A row the wire cut asks for its node's
  detail, and the answer is cached for the node's version. Until it lands,
  the row draws its wire text. The TUI sends the fetches after each update,
  so in-process the answer lands on the next message, and over ACP (P3)
  the same code waits a round trip. The cache is keyed by wire node ID.
- **Only rows the wire cut ask:** a call whose arguments or output were
  cut, and a completion, run event or message the projection cut. Every
  other expanded row has its whole text on the wire: a beat's narration
  and thoughts, a group's subjects, a thinking row, a message's content, a
  plan row and a receipt.
- **Running rows never ask** (C6). A running call's output changes on
  every tick and its node carries no version, so a cached answer could not
  be validated and a fetch would repeat every frame. Its row shows the
  wire's last `WireTextCap` bytes of output, which is what a live tail
  reads. Once the call settles, its row asks like any other.
- **What the wire and the detail gain** (C6):
  - `WireMessage.truncated`, set when the reasoning is cut. A capped field
    already sets its node's flag everywhere else;
  - `NodeDetail.body`, a run event's whole body;
  - `CompletionDetail.truncated`, the completion's own "earlier output
    dropped" flag, which `WireCompletion.truncated` folds together with the
    projection's cuts;
  - `NodeDetail.thinking` carries a message's reasoning, as one entry, as
    it carries a beat's thoughts.
- **A cut call's arguments show compacted.** `CallDetail.args` is
  indented, and the expanded row's `args:` line shows it with
  `json.Compact`. The content is whole, and the spacing can differ from
  what the model wrote.
- **The settled-row renderer takes its helpers with it** (Migration
  order). `symbolrow.go`'s wrappers, `toolCategoryGlyph` and two `spine.go`
  wrappers were reached only from that renderer or from nothing. The
  `viewmodel` functions behind the wrappers keep their tests, moved to
  `viewmodel`.
- **The tree's shape stays with plan 3d** (Migration order). The turn
  walk, node identity, expansion, regions, browse items, the seen set, the
  completion index and `rowBeats` keep reading `viewmodel` nodes. Plan 3c
  moves only the payloads the renderers read.

**2026-11-02, against `025242f9` (plan 3c merged).** Found while writing
plan 3d:
- **`Stack` takes the busy flag until plan 5** (C6). `busy` decides a
  tree's live beat and whether its turn has a receipt. The TUI owns turns
  until plan 5, and builds every tree, a drilled child's included, with its
  own flag. So it passes that flag, and the adapter builds the tree with it.
  ACP's `session/stack` keeps its own rule: a subagent's tree is busy while
  the subagent runs. P3 drops the parameter, and a drilled view then shows a
  finished child's receipt while the parent's turn runs.
- **`Stack` is built on the state's clock.** The snapshot's time decides
  only the band's stale signal. The goldens run the state and the model on
  one clock, so the adapter reads the state's (`State.Now`, new), not the
  wall clock.
- **An ID that names no kept child reads as an empty snapshot**, unlike
  `Sheet`, which falls back to the session's own. A drilled frame must never
  show the session's transcript.
- **No projection cache.** Measured on a session of 2,000 calls: a live
  refresh already projects every frame (13.9 ms, of which the projection is
  5.5 ms), so `Stack` adds nothing while a turn runs. An idle refresh gains
  the 5.5 ms. P3 projects agent side.
- **Subagent views carry what a drilled view reads** (C5). `drill` holds the
  child's active root (completion rows elide it), its active route (a
  beat's model is compared against it) and its activity (the reconnect
  notice). It is set only while the session keeps the child's state, which
  is what `Stack` reads, so it also says the subagent can be drilled into.
  The route comes from the child's state, not from the view's `model` and
  `provider`, because `agent.run` records the provider object's name there.
  A child never dispatches subagents (`agent.run` is withheld from
  children), so drill-in is one level deep and the views do not nest.
- **The text helpers go to `strutil`** (C1): `ScrubText`, `ScrubBlock`,
  `ElideWorkdir`, `FirstSentence` and `StripEmphasis`. `viewmodel`, the
  adapter and the TUI call them there. `ReportTail` and `BeatRowCap`, which
  only the TUI read, move into the TUI. The TUI keeps its own
  `isShellFamily`, and a test holds it equal to `viewmodel.IsShellFamily`.
- **Node identity is the wire kind and ID** (C6). A final answer's identity
  kind becomes `final`, which its node's kind already was. The seen set
  holds the wire nodes only: nothing keys state by a beat's reasoning entry
  or by a call inside a merged row since plan 3c.
- **Sandbox info arrives with plan 4, not 3** (C5). The running-call row
  reads the session's. A drilled view reads none, as today, because only
  the root state records sandbox info. Plan 4 moves it with the approval
  panel, which reads the same value.
- **What stays for later plans.** `approvalWhyFor`'s step owner keeps
  `steps.go` on `viewmodel` and `session`, and `DisplayToolName` keeps
  `toolnames.go` on `viewmodel`, until plan 4. The work types keep their
  files until plan 5, and so does `jumpToWorkOrigin`'s match of a child by
  its session ID. The notice keeps `viewport_render.go` on `session` until
  plan 6.

## Implementation record

**Plan 1 (foundation), branch `p2-1-foundation`.** Added
`internal/acpwire` (the error codes and T2's field rules),
`internal/acpclient` (the `Host` and `Session` interfaces, the method
table and the error helpers), and `internal/acpclient/inproc` (the
in-process adapter). `tui.New` now takes a host and a session, the state
arrives through `WithLegacyState`, and `gitinfo` and `changedfiles` moved
to `internal/repo`. The boundary test's allowlist starts at 66 files,
140 import entries and 22 `config.*` entries.

**Plan 2a (status), branch `p2-2a-status`.** Added `acpwire.SessionStatus`
and its parts (`Route`, `Quota`, `Effort`, `Interaction`, `Activity`,
`Workspace`, `Browser`) with their JSON names fixed, plus `acpwire.Millis`
and `acpwire.Time` for the Unix-millisecond fields. `acpclient.Session`
gains `Status() acpwire.SessionStatus`, with its `session/status` row in
`Methods`, and the in-process adapter builds it from the state on every
call. The TUI now reads its identity, routes, access, activity, workspace
roots and browser state through `Status()`, and its session ID through
`ID()`: 89 reads in 47 functions moved off `m.state`. T1b, the
moved-reads guard, starts its list with these 18 names, all at plan `2a`:
`WorkingDir`, `Generation`, `LeafID`, `Branches`, `SessionID`,
`ActiveRoute`, `TurnRoute`, `TurnUsage`, `TurnQuota`, `EffortOverride`,
`InteractionState`, `SudoUnlockEnabled`, `Trusted`, `HasBackup`,
`Activity`, `HasRunningSubagent`, `Workspace` and `BrowserInfo`. T1's
allowlist did not change: no file lost its last agent-side import, because
most files mix groups.

**Plan 2b (runs, queues and events), branch `p2-2b-events`.** Added
`acpwire.Steering` and its parts (`SteeringState`, `SteeringOutcome`,
`SteeringReceipt`, `SendNowRequest`) plus `Subagent`, `SwarmProgress`,
`SwarmRole`, `SDDProgress`, `TaskTiming`, `SDDGate`, `Git` and
`WatchResume`, and eight `SessionStatus` fields (`Steering`,
`RunningJobs`, `Subagents`, `Swarm`, `SDD`, `SDDGate`, `Git`,
`WatchResume`). ACP's steering wire types moved out of `internal/acp` into
`acpwire`, with their JSON unchanged, and the projection lives in
`viewmodel.SteeringStateOf` and `viewmodel.SteeringReceiptOf` so the
adapter can share it. `acpclient.Session` gains `Changed()` (a coalescing
signal with a buffer of 1, closed when a swap replaces the session) and
`SendNow`, with its `session/steer` row in `Methods`. The in-process
adapter subscribes to the five session brokers, keeps the newest receipt
per request and the pending watch resume with its sequence, and fires
`Changed` after each event and after a successful `SendNow`.

The TUI's five broker pumps, their messages, their handlers and their
caches are gone: one pump on `Session.Changed()` replaces them, and the
job count, git info, subagent views, swarm and SDD progress, the SDD gate,
the steering queue and the Send-now receipt all come from `Status()`.
`WithSendNow` and the five broker options are deleted; `WithContext`
carries the program's context. The job list and the watch snapshot are
deleted rather than moved, because nothing read them. T1b gains seven
names, all at plan `2b`: `SteeringQueue`, `SteeringSnapshot`,
`SubmitSendNow`, `RunningJobsCount`, `SwarmProgress`, `SDDProgress` and
`SDDGate`. T1's allowlist lost 13 entries across 9 files — five files trimmed
(`commands_dispatch.go`, `events.go`, `model.go`, `pump.go`,
`sessionsheet/section.go`) and four keys dropped entirely (`eta.go`,
`gatepanel/gatepanel.go`, `run_panel.go`, `sessionsheet/section_swarm.go`).

**Plan 2c (the session sheet), branch `p2-2c-sheet`.** Added
`acpwire.SessionSheet` and its parts (`SheetContext`,
`SheetContextSection`, `SheetChangedFile`, `SheetTurn`, `SheetTotals`,
`SheetFleetRow`, `SheetRepo`, `SheetTool`, `SheetFile`, `SheetToolBudget`,
`SheetSandbox`) plus `SheetRefresh`, and one `SessionStatus` field,
`ChangedFiles`, which carries the count the status line shows. The start
time is not on `SessionStatus`: only the sheet reads it, so `SessionSheet`
carries it. `acpclient.Session` gains `Sheet(subagentID)` and
`RefreshSheet(ctx, req)`, both with their `session/sheet` row in `Methods`.
The per-tool and per-file aggregation moved out of `sessionsheet` into
`viewmodel.SheetToolsOf` and `viewmodel.SheetFilesOf`, returning wire types,
because P3's `session/sheet` handler needs them and the adapter is deleted
then.

The in-process adapter keeps the sheet's database and git values — HEAD as
the changed-files base, the changed files, the 24 most recent turn metrics,
the session's usage totals, the repo's file and symbol counts, and the agent
worktrees — in one cache under its mutex, re-reads the values a
`SheetRefresh` names, and stores each only when the workspace it was read
for is still the active one. `Sheet` returns the cache without copying, and
a subagent's sheet carries the session's fleet and repo counts but none of
its changed files, turns or totals.

The TUI's base-ref command, its message, its handler and its six sheet
caches are deleted; the status line's count and every sheet section now read
`Session.Status` and `Session.Sheet`, and the sheet refreshes on request at
today's trigger points. The repo section's language row and
`RepoStats.Languages` are deleted with it: nothing set the field outside
tests. T1b gains seven names, all at plan `2c`: `AuditLog`, `ContextPack`,
`SessionRules`, `ActiveSkills`, `ToolBudget`, `LoadedToolNames` and
`StartedAt`. T1's allowlist lost 14 entries across 7 files — two files
trimmed (`model.go`, `pump.go`) and five keys dropped entirely
(`sessionsheet/filestats.go`, `sessionsheet/section.go`,
`sessionsheet/section_session.go`, `sessionsheet/section_tools.go`,
`sessionsheet/telemetry.go`). `sessionsheet` now imports no agent-side
package.

**Plan 3a (projection), branch `p2-3a-projection`.** The state stamps every
transcript time from its clock: `State.clockNow` reads the one `WithClock`
set, or `time.Now` for a `State` built as a literal, and the ten sites that
stamped a message, a step, the in-progress reasoning, a tool call, a run
event, a subagent or a job exit now read it. Production is unchanged; a
fixture on a fixed clock builds the same transcript every time.

T6's goldens are recorded first, at the base: eleven files under
`internal/app/tui/testdata/transcript/`, rendered under `NO_COLOR` with the
flat theme at 100 columns, covering the collapsed and expanded variants of
each surface — messages, steps with their tool rows, run events, completions
and the live turn — plus the tray. A receipt or plan-update row is in no
scene, and a subagent is covered through the tool row it starts and the
completion it produces rather than as a row of its own. Each scene builds
twice on a fresh clock, state and root and must render identically; the
expanded scenes reach their state by pressing Ctrl+G through `Update`, so
the key route is pinned with the render; a scene's subagent ID is replaced
with `#100` so the process-wide counter cannot move the layout. No later
task runs `-update`.

The projection types moved to `internal/acpwire/stack.go` — `WireTextCap`,
`WireTree`, `WireNode`, `WireBand`, `WireTask`, `WireBandEntity`,
`WireStep`, `WireTodoLabel`, `WireThought`, `WireTool`, `WireCompletion`,
`WirePlan`, `WireCall`, `WireRunning`, `WireMessage`, `WireThinking`,
`WireRunEvent`, `WireReceipt` and `StackSnapshot` — with their JSON
unchanged, plus the wire names as constants: the fourteen `Kind*` values,
`BandID` and `LiveThinkingID`, the seven `RunEvent*` values, the two
`Completion*` sources and the five `Outcome*` values. `viewmodel` defines
its own names from them, so no caller changed.

One projector now serves both clients. `viewmodel.SnapshotOf` is the
snapshot `Build` reads; `viewmodel.ProjectState` runs `SnapshotOf`, `Build`,
`Project` and `BandOf` and appends the band node after the tree's nodes,
outside `Roots`. `acp`'s `flushProjector` and `stackSnapshot` call it and
read `Projection.Band.StaleAt`; `treeFor` builds with `SnapshotOf`;
`StackNode` projects its one node against the source state's active root.
The TUI's `refreshViewport` takes its snapshot from `SnapshotOf` and still
builds and bands it itself until plan 3d. `Project` gained the `workdir` a
call's subject elides.

The row helpers moved to `viewmodel/rows.go`: `CallVerb`,
`FallbackSubject`, `CallSubject`, `SubjectFirstTool`, `SearchQualifier`,
`SearchTarget`, `ArgString`, `HookHint`, `DiffStat` and `FailedTail`, with
`FailedTailLines`. Three compositions are new, so the TUI and the wire
share one source of truth: `RunningSubject`, `SummaryLine` and
`CallDiffStat`. The TUI's functions are one-line wrappers, and its inline
copies call the compositions.

Every wire node carries `version`, the lower-case hex of its payload hash,
omitted at zero. The collapsed-row fields are `WireTool.verb`;
`WireCall.subject`, `summaryLine`, `diffStat`, `hookHint`, `killedReason`,
`jobId` and `tail`; `WireRunning.subject`, `callId` and `args`;
`WireStep.narration`; `WireMessage.reasoning`; `WireThinking.startedAt`;
and `WireRunEvent.bodyLines`. `StepInfo.LiveSince` carries the live
reasoning's start. Every existing field keeps its name, type and value, with
one widened meaning: `running.truncated` now reports a cut in the args as
well as in the output, because a running call carries its args for the first
time.

T1's allowlist and T1b's list did not change: the TUI still imports
`viewmodel` in every file it did, and no read moved off the state.

**Plan 3b (browse actions), branch `p2-3b-browse`.** The detail transport
moved to `internal/acpwire/stacknode.go` — `NodeDetail`, `CallDetail`,
`ExecutionDetail`, `SymbolDetail`, `NoticeDetail`, `ThoughtDetail`,
`TodoDetail` and `RelationDetail` — with their JSON unchanged, plus
`StackNodeResult{Detail, Node}`, whose field order keeps the JSON the
handler's map produced, `ApprovalNotRequired`, and the two decode types the
TUI reads with: `HookDetail` and `SandboxDetail`. `CallDetail.Hooks` changed
type only: the hooks travel as the JSON `registry.HookMetadata` encodes to,
because its `failed_open` name breaks C3's camelCase rule in a typed field,
and `HookDetail` decodes the part a client shows.

One builder answers the request. `viewmodel.StackNodeOf` finds a node in a
built tree and returns its wire form with its parent's ID set and the detail
the projection caps or leaves out; `acp`'s `session/stack_node` handler keeps
its params check and `treeFor` and calls it, and the in-process adapter calls
it too. The detail gained `NodeDetail.step` (`StepDetail`: a call's own step,
or a beat's first, with the owner, model, provider, the narration's first
sentence as the "why" line and the stored todo), `NodeDetail.completion`
(`CompletionDetail`: a completion's retained output and its report, neither
capped), and `CallDetail.summary`, the call's own summary uncut — which is
what copy takes from an edit whose result is empty, because such a call has
no `output` or `diff`. `WireTool.callers` marks a one-call row whose call
changed a resolved symbol in a subject-first tool other than `file.read`;
`viewmodel.CallersSymbol` is that test.

`acpclient.Session` gains `StackNode` and `Callers`, with their rows in
`Methods` (`session/stack_node` exists; `session/callers` is new in P3). The
adapter answers `StackNode` from the session's own transcript, or from
`childState` for a subagent ID, and builds not busy: busy decides only which
beat is live and whether the receipt exists. A subagent ID that names no
child is an error, because a node ID like `step:3` exists in every session.
`Callers` walks the transcript for the audit the node ID names, applies
`CallersSymbol`, and asks `Deps.ReferenceFinder`; `app.go` sets it from the
runtime's LSP handle and leaves it nil when there is none, never assigning a
nil `*lsp.SymbolQuery` to the interface.

The TUI keeps a wire index of what it renders. `refreshViewport` projects the
tree it already built, after the content-signature check, and keeps the nodes
by ID in `m.wire`; the band node is not in it. The inspector, copy, open, the
selection's full-subject copy and the drill-in's subagent ID read node fields
from that index and ask `StackNode` only for what the wire caps or leaves
out. Callers lookups ask `Session.Callers` once per flagged row, and only
while the view is not drilled in, because the index then holds the child's
transcript; `handleCallers` keeps a nil answer, so a failed or empty lookup
never costs a second ask. The TUI holds no reference finder:
`ReferenceFinder`, `WithReferenceFinder`, `firstResolvedSymbol`, `refFinder`
and `app.go`'s wiring are deleted. T1b gains one name, at plan `3b`:
`Transcript`, whose last read was `callers.go`'s scan. T1's allowlist lost
five entries across three files — `session` and `registry` from
`browse_inspect.go`, `registry` from `browse_actions.go`, and `session` and
`registry` from `callers.go`. No key was dropped. Two edge cases differ from
before: the inspector's subject and a beat's copied rows use the wire's
scrubbed `target`, so a command with a line break shows on one line, and the
inspector's times are built from Unix milliseconds in local time.

**Plan 3c (the rows), branch `p2-3c-rows`.** The wire and the detail carry
the last four things an expanded row needed. `WireMessage.truncated` says the
reasoning was cut, beside the capped `reasoning`. `NodeDetail.body` is a run
event's whole body, and `NodeDetail.thinking` now also carries a message's
reasoning, as one entry with its think time. `CompletionDetail.truncated` is
the completion's own flag — the job dropped earlier output before it was
retained — which `WireCompletion.truncated` folds together with the
projection's two cuts, so a client cannot read the own flag from the node
alone. `viewmodel.StackNodeOf` answers all of it from the session's own
transcript.

The settled-row renderer nothing reached is deleted, with the helpers only it
used: `renderCompletedToolCall`, `renderCompletedToolCallIn`, `toolRowHead`,
the collapsed diff preview in `toolBody` with `maxDiffLinesPerFile` and
`maxDiffFiles`, the audit branch of `renderTranscriptItemIn`, all of
`symbolrow.go`, `toolCategoryGlyph` with its table, and `spine.go`'s
`toolTarget` and `fallbackSubject` wrappers. Their tests were ported to the
spine row, moved to `internal/viewmodel/subjects_test.go` against the
functions the wrappers composed, or deleted where they pinned the old head.

The TUI fetches an expanded row's detail off the render path. A render calls
`wantDetail(key, version)`: it returns the detail cached for that wire node ID
at that version, or records the want and returns nil, and the row draws the
wire's text until the answer lands. `Update` batches `detailQueryCmds` after
every message, which sends one `Session.StackNode` call per want not already
in flight at its version, carrying the subagent ID the want recorded; a failed
call is stored under the version asked for and is not asked again until the
node changes. `handleDetail` stores the answer under the version it carries
and refreshes, and a stored entry folds into the node's render signature, so
its block repaints without invalidating the transcript. `pruneDetails` drops
the entries and the fetches of nodes the wire index no longer holds. Only rows
the wire cut ask: a one-call row whose call is truncated, and a truncated
completion, run event or message. Running rows never ask, because their node
carries no version and their output changes on every tick: a running call's
row draws the wire's last `WireTextCap` bytes of output, and a running call
outside a beat its first `WireTextCap` bytes of arguments. In-process the
answer lands on the next message; over ACP (P3) the same code waits a round
trip while the row shows its wire text.

Every row renderer takes `acpwire` payloads from the same index: `renderStep`
and its head helpers, `renderCallRow`, `renderGroupRow`, `renderActiveCallRow`
and `renderPlanRow`, `renderCompletion` and its report, `renderItem`,
`renderMessage`, `renderFinalAnswer`, `renderRunEvent`, `renderReceipt` and
`renderActiveToolCall`. `drawNode` opens the node's wire form and returns
nothing when the index lacks it, so the live box reads `WireThinking` and the
beat's rows are its wire children, `viewmodel.NodeID{KindTool, key}` rebuilt
for expansion state and click regions. The turn walk, node identity,
expansion, regions, browse items, `collectSeen`, the completion index and
`rowBeats` still read `viewmodel` nodes; plan 3d moves them. The TUI keeps no
index of `viewmodel` nodes: `browseTree` and `indexTree` are gone, and
`setBrowseItems` takes the items alone.

T1b did not change: no state read moved. T1's allowlist lost eight entries
across five files, and three keys with them — `symbolrow.go`, whose two
entries went with the file, and `runevent.go` and `receipt.go`. `spine.go`
keeps only `viewmodel` and `transcript.go` only `session`.

Five edge cases differ from before. A beat's reasoning, a thinking row and an
expanded call's `error:` line lose control characters and carriage returns,
because the wire scrubs them. A cut call's `args:` line shows compacted JSON,
so its spacing can differ from what the model wrote. An expanded running call
shows its last `WireTextCap` bytes of output, and a running call outside a
beat its first `WireTextCap` bytes of arguments. A plan row whose active todo
holds a CRLF shows two spaces where it showed one. Durations are worked out
from Unix milliseconds, so a step duration or think time within a millisecond
of a rounding boundary can round the other way.

One limit is unchanged and worth naming: copying a run event or a thinking row
copies its *expanded render* at 120 columns, so a body line longer than that
arrives cut with an ellipsis even though a wider screen draws it whole. The
copy takes the whole body past `WireTextCap` (that is what the detail fetch
buys) but not past the display width it was rendered at.

**Plan 3d (the tree), branch `p2-3d-tree`.** `acpclient.Session` gains
`Stack(subagentID int64, busy bool) acpwire.StackSnapshot`, with its
`session/stack` row in `Methods` just before `StackNode`. The in-process
adapter answers it with `viewmodel.ProjectState(src, busy, src.Now())` —
the shared projector, at the source state's clock, with the busy flag it is
given — and an ID that names no kept child reads as an empty snapshot
carrying that `SubagentID`, never the session's own transcript; a closed
session still answers. `session.State` gains `Now()`, the one `WithClock`
clock or else `time.Now`. `acpwire.Subagent` gains `drill` (`SubagentDrill`,
holding the child's `activeRoot`, `activeRoute` and `activity`), set only
while the session keeps the child's state, which is what `Stack` reads, and
nil for a status-only card or a released child.

The five text helpers moved from `internal/viewmodel` to `internal/strutil`:
`ScrubText`, `ScrubBlock`, `ElideWorkdir`, `FirstSentence` and
`StripEmphasis`. `ReportTail` and `ReportTailLines` with `tailLines`, and
`BeatRowCap`, which only the TUI read, become the TUI's `reportTail`,
`reportTailLines`, `tailLines` and `beatRowCap` in `steps.go`, and the TUI
keeps its own `isShellFamily`, which a test holds equal to
`viewmodel.IsShellFamily`.

The TUI keys every per-node thing by a local `nodeID`, the wire kind and the
wire ID, which replaces `viewmodel.NodeID`; `livebox.PlanItem.First` is a
string and `livebox.Target.Node` is gone. `refreshViewport` takes the
snapshot first and sets `m.wire`, the band, the completion index and
`rowBeats` before the content-signature check, and the seen set, the render
cache and the browse items read the snapshot's wire nodes too.
`viewport_render.go` and `browse.go` import no agent-side package.

Drill-in goes by ID: `drillIntoSubagent(id)` looks the ID up in
`Status().Subagents` and pushes a view only when it carries a `drill`. The
view stack holds `acpwire.Subagent`, and a drilled view's facts — its root,
its route and its reconnect label — come from the view's `drill`, read live
each refresh and falling back to the snapshot. `transcriptSource` is
deleted, so the TUI holds no child state, and a child's shell rows show no
sandbox info until plan 4 decides.

T1b gains one name, at plan `3d`: `Subagent`. T1's allowlist lost ten
entries across nine files: `spine.go`, `browse_actions.go`, `callers.go`,
`click.go`, `expand.go`, `selection.go` and `browse.go` lost their keys
entirely, and `browse_inspect.go` and `viewport_render.go` were trimmed to
the entries that remain.

Decision 11's two edge cases differ from before. The tray's plan rows come
from the wire's band, which scrubs todo text with `strutil.ScrubText` rather
than `progress.Clean`, so a todo holding an escape sequence shows its
printable remainder and one holding a CRLF shows two spaces where it showed
one. A drilled-in child whose state the session releases now shows an empty
frame with the welcome banner, which happens only when more than ten newer
children finish while the view is drilled into an old finished one.
