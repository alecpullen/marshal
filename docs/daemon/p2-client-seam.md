# P2 — TUI client seam

Parent: [`design.md`](design.md) §4.5. This spec replaces §4.5's sketch
where they differ, and settles §8's transcript-shape question.
Depends on: P1 (merged at `ee15d2ed`) and the TUI simplification
([`../tui-simplification/feature-spec.md`](../tui-simplification/feature-spec.md),
P-A–P-E, merged at `991c18d7`). Agent side and TUI only: no bridge
change, no new ACP method, no database schema change. ACP messages that
already exist change only by gaining optional fields (C6).
Status: drafted 2026-10-20; revised 2026-10-25 against `991c18d7`
(Revision record); awaiting review. Delivered as nine plans (Migration
order), each written against `main` once the previous one has landed.

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
  - from a call outside any beat, which still renders through the older
    head (`toolRowHead`, `transcript.go:815`): that head, plus the diff
    preview, which shows per-file `+N −M` stats and the first 12 lines of
    up to 6 files (`transcript.go:1044`);
  - the active call's step ID and its command line.
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
- The `Host` holds what `accessTUIOptions` and `WithSendNow` bind to
  today: the runtime's current state and runner, read under the runtime
  gate. A `Session` handed out before a swap never reaches the session
  that replaced it.
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
  logic read today:
  - **identity:** session ID, working directory, start time, generation
    sequence, leaf ID and branch count;
  - **route:** the active route, the turn route and whether it is
    running, turn usage and window, quota, and effort override;
  - **access:** the interaction state (mode, auto and system access),
    sudo unlock, trusted, and whether there is a backup;
  - **activity:** kind, label, start, the pinned spinner label, and
    whether a turn or a subagent is running;
  - **workspace:** project root, active root, and git info (branch,
    worktree, HEAD);
  - **live state:** browser info, sandbox info and the current notice;
  - **queues and runs:** the steering queue and Send-now snapshot,
    todos, jobs (the running count and the job list), the watch
    snapshot, subagent views, swarm and SDD progress (the tray's
    plan-run row), the SDD gate, active skills, loaded tool count, tool
    budget, session rules and a pending watch resume.
  - **subagent views** leave out the child state. Each carries what
    the tray and the drill-in breadcrumb read today: the role, label,
    model, provider and fallback flag. While the child runs, it also
    carries the child's newest narration headline (`subagentHeadline`,
    `steps.go:703`).
- The job, watch, steering, workspace and subagent broker pumps go away.
  Their data are `Status` fields, and the adapter fires `Changed` when
  they publish.
- `Session.Sheet(subagentID int64) acpwire.SessionSheet` carries what the
  session sheet's sections read. For the session, that is the tool-call
  log fields the sections use, the context-pack summary, changed files
  against the sheet's base ref, turn metrics, usage totals, the fleet
  rows and repo stats. For a subagent it carries the same subset
  `childSheetData` uses today. Values the TUI caches on turn boundaries
  today stay cached that way, so the adapter computes them off the
  render path, as the TUI does now.
- ACP (P3): `session/status` with a `session_status` update, and
  `session/sheet`. Two parts already have reads: `session/access_state`
  serves the interaction state and `session/steering_status` the
  Send-now snapshot (both exist).

### C6. Transcript

- `Session.Stack(subagentID int64) acpwire.StackSnapshot` returns the
  projected tree with the band node: the one projection `session/stack`
  already serves. P-B removed the legacy projection, so the stack needs
  no parameter to select one.
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
  - `WireMessage.reasoning`, capped;
  - `WireStep` gains `narration`, the beat's trimmed narration texts in
    order. The renderer joins them as one markdown document, and
    `headline` (emphasis stripped, cut at the first sentence) plus
    `rest` cannot rebuild them;
  - `WireCall` gains:
    - `subject`, today's `callSubject`, built server-side against the
      active root and scrubbed;
    - `diffStat`, counted over the full diff;
    - `hookHint` and `killedReason`;
    - `tail`, a failed call's last three output lines;
    - `stepId`;
  - a call outside any beat also gets `head` (today's `toolRowHead`) and
    `diffFiles`. Each `diffFiles` entry carries the path, the added and
    removed counts and the first 12 diff lines, for at most 6 files,
    plus the full count;
  - `WireRunning` gains `stepId` and the command line.

  The plan fixes the JSON names. Anything only an expanded row needs
  comes from `StackNode`. `WireTool.subagentId`, which drill-in needs,
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
    the steering queue;
  - `SetInteraction(ctx, patch)` replaces the interaction setter. It
    covers mode, auto and system access (`session/set_mode` exists and
    takes all three). `SetEffort` exists, and `SetSudoUnlock` is new;
  - `CancelSubagent` (new);
  - `ClearRunEvents`, `ClearSDDProgress` and `SetIdle` (new);
  - `TakeWatchResume` (new).
- **Work.** The work service's list, inspect, stop and acknowledge calls,
  and its snapshot, become `Session` methods (`session/work_*`
  exists).
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
     `acpwire`: the work types in plan 2, the projection types in plan
     3, and the request and command types in plans 4 and 6.
   - The test helpers, and every test moved onto them.
   - The gitinfo/changedfiles move.
   - The boundary test (T1), with an allowlist of every current
     violation by file and import.
   - The ACP mapping test (T4).
2. **Status and events** (C5), including the subagent data the tray
   reads.
3. **Transcript** (C6): the renderer, the TUI's indexes over the tree
   and the tray's band and held-row inputs. This is the largest and
   riskiest plan, so it comes early.
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
- **T6. Renderer equivalence.** Before porting, Plan 3 records the old
  renderer's output for a fixture set as golden files. The set covers
  every node kind, collapsed and expanded. That includes beats with a
  todo tag, plan lines, completion events with their issue reports,
  receipts and a call outside any beat. It also covers diffs, hooks, a
  killed sandbox, a failed call's tail, a running call, and the tray
  with running, held and plan-run rows. The ported renderer must match
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
