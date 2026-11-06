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
landed; plans 2, 3, 5 and 7 are split into sub-plans.

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
  (`WireTree`, `WireNode` and its payloads) and the existing ACP types
  the client uses (`StackSnapshot`, `NodeDetail`, `CommandResult`, …)
  move here. `internal/viewmodel` and `internal/acp`
  use them from here, and their JSON does not change. The work types
  (`WorkItem`, `WorkSnapshot` and the rest) are new here (plan 5a):
  ACP's `Work*Wire` types encode times as RFC 3339 strings, which C3
  forbids, so they stay in `internal/acp` until P3.
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
  - work predicates such as `WorkActive` and `WorkNeedsAttention`. The
    TUI keeps its own copies over the wire types (plan 5a), and
    `viewmodel` keeps its for the agent side;
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
  results or an error. A call that reports progress before it returns
  takes a function the agent calls with each update, as the sign-ins
  take `show(url)` (plan 7a). P3 feeds it from the call's progress
  notifications.
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
  `err.Error()`, as today. A failed run's provider or login error
  carries the notice it calls for in its data, an `acpwire.ErrorData`,
  which `acpclient.NoticeOf` reads (C9, plan 6).
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
  the module, and their JSON round trip is tested (T3). One value is
  `any`: the user-value write's, which carries what `config.LookupPath`
  returns or a bool or string list (plan 7b). P3 sends it as JSON and
  converts it by the field's type.
- **Secrets.** No read returns one. A literal secret in a config read
  (each provider's API key, the web search key and the desktop CDP
  URL's userinfo: `config.MaskSecrets`' set) is replaced by its mask,
  the `••••` and last four characters the settings browser shows
  (`strutil.MaskKey`), or the URL with its userinfo masked. No real
  secret begins with the bullets, and an empty secret stays empty, so
  every reader that asks whether a key is set reads the mask correctly.
  A secret's source is already in the config, as `api_key_env` or
  `auth`. Every config the client hands back (an install, a project
  save, a user write, a probe) has each mask replaced agent side by the
  stored secret it stands for; a mask with nothing behind it fails the
  call (plan 7d). Only these calls carry a secret: the provider-key write (C10), the sudo
  answer (C7), the probes, whose provider config may hold a key typed
  but not yet saved, as `config/probe_provider`'s `config` does (plan
  7a), and the user-value write when its path is a key's, as the
  settings browser's key rows write it (plan 7b). None is logged.
- **Large text.** The transcript keeps `WireTextCap`. Every other
  result that can be large says how it is capped in its doc comment.

### C4. The adapter

- `inproc.New(inproc.Deps)` returns `(*inproc.Host, *inproc.Session)`,
  which implement `acpclient.Host` and `acpclient.Session`. `Deps`
  holds the state and everything `Run` passes as an agent-side TUI
  option today. `Run` builds `Deps` instead of those options.
- `Host.NewSession` (plan 5e) asks the runtime for a new session
  through `Deps.NewSession`, closes the old session and returns one over
  the new state. Until then `(*Host).Swap(state)` served the swapper,
  which `tui.SessionSwapResult.Session` carried to the TUI; plan 5e makes
  it the unexported `swap`.
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
  groups not yet moved. Plan 1 adds it, and Plan 9 deletes it. Its
  partner `tui.WithLegacySession` (plan 5e) re-reads the runtime's
  current state and tool registry after `Host.NewSession`, and goes with
  it.
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
  - **activity:** kind, label and start, whether a subagent is running,
    and whether the session's agent runner is built (`agentReady`, plan
    5c), which input gating reads before it starts a run;
  - **workspace:** project root, active root, git info (branch,
    worktree and whether the root is a repository; plan 2b), and the
    number of files changed against the sheet's base, which the status
    line shows (plan 2c). HEAD itself stays agent side: the adapter keeps
    it as that base, and only the changed-files diff reads it;
  - **live state:** browser info, and the sandbox info (plan 4): the
    backend, whether it isolates the network, and the shell's configured
    network allowance. Only the running-call and approval renderers read
    it, and both read the session's. The current notice (plan 6), which
    the runtime sets too, and which the TUI changes through the session
    (C9);
  - **queues and runs** (plan 2b): the steering queue, the Send-now
    snapshot and the newest receipt for its turn, the running job count,
    subagent views, swarm and SDD progress (the tray's plan-run row), the
    SDD gate and a pending watch resume. The job list and the watch
    snapshot are not carried: the TUI caches both from their events
    today, but nothing reads them. The work snapshot keeps its own pump
    until the work group moves (plan 5a). The todos reach the tray through
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
  then fires `Changed`. The TUI sees every turn boundary: since plan 5c a
  turn ends when its `Prompt` returns. It therefore calls `RefreshSheet`
  at the points where it re-reads those values today: startup, turn end,
  a workspace move, a new session and opening the sheet.
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
  turn flag, set while its `Prompt` or one of its other runs is in
  flight, and P3 drops it (revision records 2026-11-02 and 2026-11-03).
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
  request a person can answer: the session's own, and the approvals of
  its running subagents. Only approvals come from subagents: children get
  no `question.ask`, and no child state is secret-prompt capable.
  - Request kinds, one field each: approvals (a list, the session's
    first, then each running subagent's in registration order), the
    question, the skill gate, the sudo prompt and the typed access
    request. The SDD human gate stays in `Status` (C8).
  - Each request carries:
    - a key that stays stable while the request is open: the registry
      ID, the access request's ID for a typed access request, or a key
      the adapter mints for a prompt with no registry entry;
    - an approval's owner: subagent ID and label, or none for the
      session itself;
    - the fields the TUI displays today. An approval also carries what
      the TUI derived from the owning state: the "why" line (the
      requesting step's owner and narration headline), the tool's
      display name, and whether it is a forced prompt that only an exact
      decision may answer.
  - `Pending` settles nothing, so the TUI may read it while rendering.
    `Changed` does not fire for requests; the TUI reads them on every
    update and tick, as it read the state's slots.
- **Answers:**
  - `AnswerApproval(ctx, key, ApprovalAnswer{Decision, Edited})`, where
    the decision is `once`, `session`, `always` or `deny`;
  - `AnswerQuestion(ctx, key, answers)`;
  - `AnswerSkillGate(ctx, key, choice)`;
  - `AnswerSudo(ctx, key, SudoAnswer)`;
  - `ReconcileAccess(ctx)` settles a typed access request that no longer
    asks for anything, then returns the one to present. The TUI calls it
    once per update, where it read the runtime's access request before;
  - `AnswerAccess(ctx, choice)`: the choice names its request.
- **`Rollback(ctx)`** restores the backup and logs the audit event. The
  approval dialog stays open after it, as today.
- **Policy moves to the adapter:**
  - a `session` or `always` answer to a forced approval is a deny;
  - `always` saves the user rule and applies it to the session's live
    config, which the policy engine reads, and falls back to a session
    rule if the save fails;
  - `session` adds a session rule;
  - both rules land on the session, whichever subagent asked, as today;
  - the skill gate's remembered choices are recorded agent side;
  - without the runtime's access callbacks, a typed access request is
    settled as failed rather than presented.
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
  - rollback through `session/rollback` (new);
  - `ReconcileAccess` has no ACP method: the agent settles such a
    request itself, and the client reads its mirror.

### C8. Turns and runs

- `Prompt(ctx, PromptParams) (PromptResult, error)` returns when the turn
  ends (plan 5c). Agent side, it takes the session's one run slot, begins
  work, registers the turn's stop group, runs the runner, which adds the
  user message, and at the end sets the session idle and ends the stop
  group and the work. A start it refuses comes back coded (`busy`,
  `quiescing` or `no_runner`), and a prompt marked `queued` then goes back
  on the steering queue. It replaces `runAgentCmd`: for prompt turns in
  plan 5c, and for swarm and plan runs in plan 5d. Of the two user
  `AddMessage` calls, the SDD gate's moves into `AnswerSDDGate` (plan
  5d), and the no-runner echo with the notes (plan 6). ACP:
  `session/prompt` (exists).
- **Other runs:**
  - `Review` (`session/review`, new);
  - `RunAgent`, the custom-agent run-now, which runs the agent as a
    subagent the session shows (`session/agent_run`, new);
  - `StartSwarm` with role overrides (`session/swarm_start` exists);
  - `PlanPreflight`, the plan's inspection and the verify gate a plan
    run would use, which the `/sdd` preflight renders
    (`session/sdd_preflight`, new);
  - `StartSDD` with plan, strategy, overrides and the verify commands a
    proposal filled in (`session/sdd_start` exists). A run its human
    gate pauses returns `human_gate_required`;
  - `AuthorPlan`, which resolves the draft path, runs the plan author
    and returns the candidate for review (`session/sdd_author`, new);
  - `Suggest`, the model call behind the next-reply suggestion
    (`session/suggest`, new);
  - `VerifyProposal`, the build/test proposal parsed from its own
    subagent's reply (`session/verify_proposal`, new).

  Each carries what the TUI passed its factories: role overrides keyed
  by role name, the strategy, and the proposed verify commands. The
  runtime applies them for the one run. `Review`, `RunAgent`,
  `StartSwarm`, `StartSDD`, `AnswerSDDGate` and `AuthorPlan` take
  `Prompt`'s run path; `Suggest`, `VerifyProposal` and `PlanPreflight`
  are plain calls (plans 5c and 5d).
- **Turn control:**
  - `Stop(ctx, cause)` covers today's begin/request/end turn-stop
    sequence (`session/cancel` exists). It stops the session's own run,
    the commands attached to it first, and does nothing when none runs;
  - `Steer` and `SendNow` (`session/steer` exists; Send now is its
    `now` delivery), and `PopSteering` and `ClearSteering` (new) cover
    the steering queue. `SendNow` arrives early, in plan 2b, because
    the receipt it returns is part of `Status`;
  - `SetInteraction(ctx, patch)` replaces the interaction setter. It
    covers mode, auto and system access (`session/set_mode` exists and
    takes all three). The patch is an `acpwire.InteractionPatch`, and
    the result the `acpwire.Interaction` that `Status` carries.
    `SetEffort` exists, and `SetSudoUnlock` is new;
  - `CancelSubagent` (new);
  - `ClearFinishedRun` (new) clears a finished run's progress and its
    event log together, the two clears the TUI always makes at once;
  - `TakeWatchResume` (new);
  - no `SetIdle`: the TUI sets the session idle only where a run it owns
    ends, and plans 5c and 5d move those ends agent side;
  - `AnswerSDDGate(ctx, answer)` records the answer as the user's
    message and resumes the paused plan run (`session/sdd_answer`
    exists), and `StopSDD(ctx)` abandons it and clears the gate
    (`session/sdd_stop`, new). The session keeps the paused run, so a
    swap drops it and a new `StartSDD` replaces it. Answering the SDD
    human gate resumes the pipeline run, so it moves with the runs, not
    with C7's requests.
- **Work** (plan 5a). `Work()` returns the session's live work snapshot
  and whether the session serves work at all. `ListWork`, `InspectWork`
  and `StopWork` are the work service's list, inspect and stop calls
  (`session/work_list`, `session/work_inspect` and `session/work_stop`
  exist). The TUI never acknowledges work, so no acknowledge call is
  added. The work pump moves into the adapter, which fires `Changed`
  for each snapshot it keeps, and the work types arrive in `acpwire`.
- **Ending a session** (plan 5e).
  - The `EndController` calls become `Session` methods over the
    runtime's end service. `ActiveWork` is a read: the warning renders
    it, and `AcceptEnd` checks the user's choice against the live
    membership, refusing to stop work the user was not shown
    (`session/end_work`, new, refreshed by an `end_work` update).
    `AcceptEnd`, `WaitForEnd` and `RunExitAnalysis` are calls
    (`session/end_accept`, `session/end_wait` and
    `session/exit_analysis`, new). `EndAccepted` is dropped: the TUI
    never called it. `Postmortem(ctx)` writes the report agent side and
    returns its path (`session/postmortem`, new). A session without an
    end service has nothing to stop: it accepts and settles at once, and
    refuses the exit analysis with `no_runner`.
  - `Close(ctx)` replaces `ResolvePendingForShutdown` and `Shutdown`
    (`session/close` exists). It cancels the run in flight, settles
    every pending request as shut down and shuts the session down.
    Closing a closed session does nothing.
  - `Host.NewSession(ctx, name)` replaces the swapper (`session/new`
    exists). Resume stays a restart of the program: the TUI reports the
    session to resume, and `Run` starts a runtime on it, because the
    runtime cannot open a stored session in place. P3 decides whether
    resume moves onto `session/resume`.

### C9. Notes, notices and commands

- **Notes.** `AddNote(ctx, note)` appends an `acpwire.Note` exactly as
  `AddMessage` does today: persisted, in the transcript, and visible to
  every client. A note is a system message unless its role is user,
  which only the no-runner echo uses: with no agent runner, Enter still
  records the user's text and starts nothing (plan 6). ACP:
  `session/note` (new).
- **Notices.**
  - `SetNotice`, `ClearNotice(category)` and `DismissNotice` change the
    banner, and the current notice is in `Status`. None fires
    `Changed`: the TUI repaints after each, and the runtime's own
    notices reach the screen on the tick, as today. ACP:
    `session/notice_set`, `session/notice_clear` and
    `session/notice_dismiss` (new).
  - The TUI still decides when a notice is set or cleared: when a turn
    begins, fails or succeeds, after its reloads, on Esc and when the
    banner's time is up.
  - Error classification moves agent side. A run whose error is a
    provider's, or an OAuth login the provider needs, returns it coded
    `provider_error` or `auth_required`, with its own text and the
    notice in its data (C2). `noticeForError` takes that notice and
    stamps its source, and falls back to an internal notice as before.
    An error a cancellation caused comes back unchanged.
- **Slash commands.**
  - TUI-only commands that open panels or change view state stay in the
    TUI.
  - The completion list and the dispatch read `Commands()`
    (`session/command_list` exists). Each entry carries what the
    dispatch branches on: the prompt body, whether the command has a
    handler and whether it is TUI-only. `/trust` is TUI-only and also
    has a handler.
  - Handlers run through `Command(ctx, name, args)`, which uses the same
    registry agent side (`session/command` exists). Unlike ACP's, it
    does not refuse during a turn: the TUI never did.
  - A doc's row actions carry IDs, and `CommandAction(ctx, id)` runs one
    agent side (`session/command_action`, new). The adapter keeps the
    actions of its 16 most recent results that have any.
  - The doc panel renders an `acpwire.WireDoc` and is given its actions:
    a command doc's run through `CommandAction`, and a doc the TUI builds
    itself (the `/run` outcome) runs its own until plan 8.
  - ACP's command types move to `acpwire` with their JSON unchanged
    (plan 6), and gain fields only the client reads.
- **Logging.** The TUI logs through its own logger, set with
  `WithLogger`, not the session's.

### C10. Config, providers and trust

- **Reads.** `Session.Config()` returns the session's effective config
  with its secrets masked (C3, plan 7d). `Session.Layers()` returns the
  layers with provenance, masked the same way. `config.Diagnose` stays
  client-side: it is pure over those two values, and its key checks
  only ask whether a key is set, which a mask answers.
- **Writes** (plan 7b). Each save function the TUI calls today becomes
  one call with the same arguments minus the path, and the same
  semantics:
  - user value, user providers, user presets and provider key are `Host`
    calls, because they change files every session reads;
  - project config and verify commands are `Session` calls, because they
    follow the session's working directory and diff against its layers.

  (Plan 4 already moved the user-rule write into the approval answer.) A
  write does not reload: a connect makes three writes before one reload,
  so the TUI keeps its order of saves, reloads and notes. After any
  successful write, the adapter refreshes the session's layers, and
  after a project save the permanent-trust hash, as the TUI's
  `reloadLayers` and `persistAndReload` did. `SessionStatus` names the
  user and project config files, for the TUI's notes. The TUI keeps its
  own busy check ("Stop the active turn before applying settings"), so
  no write returns the busy code.

  A provider write restores each masked key to the stored key it stands
  for. The settings browser names the providers it renamed, so a key
  follows its provider (plan 7d).
- **Reloads** (plan 7b) are `Session` calls. `ApplyConfig` installs a
  config without saving or rebuilding, the agent half of the TUI's
  `applyNewConfig`. `ReloadConfig` rebuilds the runtime from a config,
  and reports when the session has no reload path, as a TUI built
  without a reloader had none. `ReconnectMCP` is the reload after an MCP
  sign-in: it clears the provider and config notices first, so a fresh
  notice the rebuild sets survives.
- **Models and providers** (plan 7a, apart from preset setup).
  - `ProviderTemplates()` reads the catalog (new).
  - `ProbeProvider` lists a provider's models (`config/probe_provider`
    exists). `CanProbeModel` and `ProbeModel` check and run a model's
    capability probe (new). Each takes the provider's name and config,
    which need not be saved, and the adapter builds the client.
  - The model cache is `ListModels`, `SaveModels`, `ForgetModels` and
    `RenameModels` (new), keyed agent side by the provider's config. The
    TUI keeps its in-session map of discovered models and decides when
    to save or forget an entry, as today.
  - Thinking options stay a TUI computation: the pure helpers move to
    `llm/schema`.
  - **Preset setup after a connect** (plan 7b) runs agent side as one
    `Host` call, `PreparePresets` (new). The call loads the models.dev
    catalog, reads the provenance file and runs `presetsetup.Apply`,
    then returns the proposed presets, the names the provenance manages
    and the updated provenance, which the TUI holds as raw JSON.
    Accepting them saves the presets with the user writes and the
    provenance with `SaveProvenance` (new). Until then the TUI did all
    of this itself.
- **OAuth logins** (Codex connect, MCP auth) run agent side (plan 7a).
  A check (`PrepareProviderLogin`, `PrepareMCPLogin`) runs what must
  succeed before the panel opens, and returns today's sentence when it
  fails. The call (`LoginProvider`, `LoginMCP`) reports the
  authorization URL through its `show` function (C2), the TUI opens it
  (C12), and the call returns when the callback lands. This matches how
  ACP's `/mcp auth` works today. The reload that reconnects an MCP
  server after its sign-in is `ReconnectMCP` (plan 7b).
- **Trust** (plan 7c). `Host.TrustPrompt()` reads the directory whose
  trust decision is pending, and `Host.DecideTrust(ctx, dir, decision)`
  answers it, replacing the trust prompt callback. A trust refresh
  happens inside the project save (plan 7b).
- **Skill gate settings** (enabled flag, remembered decisions, clearing
  one) move with the skills panel (plan 8, decided with the user).
- **Tools** (plan 7c). The agents panel's tool lookup becomes a
  `Session.Tools()` read of the runtime's current registry.

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
  - SDD plan discovery, removing an authored candidate and writing a
    starter plan: `Plans()`, `DiscardCandidate` and `ScaffoldPlan`
    (new). The draft path is resolved inside `AuthorPlan` (C8, plan 5d).
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
     in plan 3, the request types in plan 4, the work types in plan 5a
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
     stays until plan 5a.
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
4. **Agent requests** (C7) ([plan](plans/2026-11-02-p2-4-requests-plan.md)).
   `Session.Pending`, the answer calls, `ReconcileAccess` and `Rollback`,
   with the request types in `acpwire` and the sandbox info in `Status`.
   The TUI's approval, question, skill-gate, sudo and access surfaces read
   and answer through them, and the policy the TUI applied on an answer
   moves into the adapter. The SDD gate's answer waits for plan 5, because
   answering it resumes the run.
5. **Turns and runs** (C8), in five plans:
   - **5a. Work** ([plan](plans/2026-11-02-p2-5a-work-plan.md)).
     `Session.Work`, `ListWork`, `InspectWork` and `StopWork`, with the
     work types in `acpwire`. The work pump moves into the adapter, and
     the tray, the live box and the Tasks panel render the wire types. A
     work item's subagent is found through `Status`.
   - **5b. Turn control** ([plan](plans/2026-11-02-p2-5b-turn-control-plan.md)).
     `Steer`, `PopSteering`, `ClearSteering`, `SetInteraction`,
     `SetEffort`, `SetSudoUnlock`, `CancelSubagent`, `ClearFinishedRun`
     and `TakeWatchResume`. The interaction and effort setter options
     go, and the adapter's `Deps` carry the runtime's setters.
   - **5c. Prompt** ([plan](plans/2026-11-03-p2-5c-prompt-plan.md)).
     `Prompt` replaces `runAgentCmd` and `startAgentRun`'s dispatch for
     prompt turns. `Stop` moves with it, because the turn's stop is
     registered before dispatch. `Review`, `RunAgent`, `Suggest` and
     `VerifyProposal` move too, and the runtime's runner and calls move
     into the adapter's `Deps`. A run's end sets the session idle agent
     side, and `Status` says whether a runner is built.
   - **5d. Pipelines** ([plan](plans/2026-11-03-p2-5d-pipelines-plan.md)).
     `StartSwarm`, `PlanPreflight`, `StartSDD`, `AnswerSDDGate`,
     `StopSDD` and `AuthorPlan`. Swarm, plan and authoring runs take
     `Prompt`'s run path, so the TUI's own stop registration goes. The
     cast list's overrides, the strategy and proposed verify commands
     travel as parameters, the preflight reads the plan's inspection from
     the session, the session keeps a plan run its gate paused, and the
     runtime's orchestrator and factories move into the adapter's `Deps`.
   - **5e. Ending and swapping** ([plan](plans/2026-11-03-p2-5e-ending-plan.md)).
     The `EndController` calls and `Postmortem` become `Session` methods
     over the runtime's end service, `Close` replaces
     `ResolvePendingForShutdown` and `Shutdown`, and `Host.NewSession`
     replaces the swapper. Resume stays a restart of the program. A
     replacement leaves the TUI's end idle again.

   5c, 5d and 5e run in that order: each run kind reuses 5c's finish
   path, and the swap result carries the runners until 5d. 5a and 5b
   depend on nothing in plan 5.
6. **Notes, notices and commands** (C9)
   ([plan](plans/2026-11-04-p2-6-notes-plan.md)). `AddNote`, the notice
   calls with the notice in `Status`, `Commands`, `Command` and
   `CommandAction`, with the note, notice and command types in `acpwire`.
   The TUI's transcript writes, banner changes and slash commands go
   through the session, a failed run's error carries its notice, and doc
   panels render wire docs. The TUI gets its own logger, and compares
   wire strings with `acpwire`'s constants.
7. **Config, providers and trust** (C10), in four plans:
   - **7a. Providers and sign-ins** ([plan](plans/2026-11-05-p2-7a-providers-plan.md)).
     `ProviderTemplates`, the probes, the model cache calls and the two
     sign-ins, with the resolver in the adapter's `Deps`. The TUI builds
     no provider client and opens no credential store.
   - **7b. Config** ([plan](plans/2026-11-05-p2-7b-config-plan.md)).
     `Session.Config` and `Session.Layers`, the user and project writes,
     the three reload calls and preset setup. The adapter refreshes the
     layers and the trust hash after each write. Keys stay literal. The
     bridge's frame cap rides along (decided with the user).
   - **7c. Trust and tools** ([plan](plans/2026-11-05-p2-7c-trust-tools-plan.md)).
     `Host.TrustPrompt`, `Host.DecideTrust` and `Session.Tools`. The
     skill-gate settings moved to plan 8.
   - **7d. Secrets** ([plan](plans/2026-11-05-p2-7d-secrets-plan.md)).
     `Config` and `Layers` carry each secret's mask in its place.
     Installs, project saves, user writes and probes restore the stored
     secret a mask stands for, and the settings browser names its
     renames so a key follows its provider. T7.

   7a comes first: 7d's redacted read leaves the TUI without literal
   keys, so every reader of one must already be agent side. 7c and 7d
   depend only on 7b, and touch different TUI files.
8. **Project data** (C11), with the skills panel's skill-gate settings
   (decided with the user).
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
  rule and applies it to the session's live config. `AddNote` persists a
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

**2026-11-02, against plan 3d (written while it ran).** Found while
writing plan 4:
- **Only approvals come from subagents** (C7). Children get no
  `question.ask`, no child state is secret-prompt capable, and both the
  TUI and ACP collect only a running child's approval. So `Pending` lists
  the session's own requests of every kind plus its running subagents'
  approvals, and only an approval carries an owner.
- **A request's key is its registry ID** (C7), which work items already
  name prompts by. A typed access request in the approval slot has no
  registry key, so it is keyed by its access request ID. The model-cost
  consent and many test fixtures set prompts without registering them, so
  the adapter mints a key for each such prompt while it stays open.
- **The adapter derives what the TUI computed from the owning state**
  (C7): an approval's "why" line, its tool label and whether it is
  forced. A session or always answer to a forced approval is a deny, which
  the adapter enforces in place of the TUI's guard.
- **Both rules land on the session** (C7, T5). The TUI added a session
  rule and saved an always rule on the session's state even for a
  subagent's approval, and the adapter keeps that. The always rule is
  applied to the session's live config, which the policy engine reads: no
  runner state is reset, so T5's example is corrected. The rule is saved
  where the TUI's `userConfigDir` puts it, which moves into the adapter
  unchanged.
- **`Pending` settles nothing; `ReconcileAccess` is new** (C7). The TUI
  reads the access request two ways: a plain read while rendering, and
  the runtime's read once per update, which settles a request that no
  longer asks for anything. A read that settled while rendering could
  change the frame between its height budget and its drawing, so
  `Pending` is the plain read and `ReconcileAccess(ctx)` the runtime's.
  Without the runtime's callbacks, `ReconcileAccess` settles the request
  as failed, which was the TUI's fail-closed branch. `CancelAccess` is
  not added: that branch was its only use.
- **`AnswerAccess(ctx, choice)`** takes no separate key: the choice names
  its request and session.
- **The SDD gate's answer moves to C8** (plan 5). Answering it resumes the
  pipeline run, which the TUI owns until `Prompt` and `StartSDD` exist.
  The gate itself stays in `Status`.
- **ACP's request types stay in `internal/acp`** (C1). Nothing on the TUI
  side reads `PermissionRequest`, and its `ModeAccessState` encodes
  `system_access` and `access_revision`, which break C3. The new
  `acpwire` request types serve `Pending`, and P3 decides what crosses
  when it extends `session/request_permission`.
- **The sandbox info carries the shell's network allowance** (C5). The
  isolation line weighs the backend, its network isolation and
  `tools.shell.allow_network` together. A drilled view still shows no
  isolation line.

**2026-11-02, against `80532121` (plan 4 merged).** Found while writing
plan 5a:
- **Plan 5 splits into 5a to 5e** (Migration order). C8 is the largest
  group: 15 of the TUI's 35 options, about 20 turn-control calls on the
  state, the work group, the end flow and the swapper. Work and turn
  control depend on nothing else in C8. Prompt comes before the
  pipelines, which reuse its finish path, and ending and swapping come
  last, because the swap result carries the runners until the pipelines
  move.
- **The work types are new in `acpwire`; ACP's stay** (C1, C3). ACP's
  `Work*Wire` types are the JSON of `viewmodel`'s, so their times are
  RFC 3339 strings. The `acpwire` types keep ACP's field names and use
  Unix milliseconds. `viewmodel` converts its types into them, and P3
  decides whether ACP's move, as for the request types.
- **`Work()` reports whether the session serves work** (C8). `Run`
  attaches the work service only when startup found a working provider,
  in the same block as the runners. Without it the TUI's work lane is
  inactive and `/work` leaves a note instead of opening the panel. The
  adapter serves work under the same condition, and `Work` reports it.
- **The work pump moves into the adapter** (C5, C8). The adapter
  subscribes to the work stream when a session is made, binds the
  conversation from the seed snapshot, keeps each newer snapshot, and
  fires `Changed`, as the TUI's pump did. The TUI applies the snapshot
  on its next `Changed`. When two snapshots land before the TUI reads
  one, a progress rate is measured across both.
- **No acknowledge call** (C8). Nothing in the TUI acknowledges work. P3
  adds `session/work_acknowledge` to the client when a client needs it.
- **The work predicates stay TUI-local** (C1). The live box keeps copies
  of `WorkActive` and `WorkNeedsAttention` over the wire types, pinned to
  `viewmodel`'s by a test, as plan 3d did with `isShellFamily`. The live
  box converts a wire progress observation back to `progress.Snapshot`,
  an allowed package, to measure a rate and an ETA.
- **A work item's subagent is found through `Status`** (C5).
  `SubagentDrill` gains the child's session ID, which a work item's
  reference names, so the TUI no longer searches the child states.
- **A session names its conversation by its own ID** (C8). The adapter
  passes its state's session ID as the work conversation, as ACP passes
  the request's.

**2026-11-02, against `80532121` (written while plan 5a ran).** Found
while writing plan 5b:
- **No `SetIdle`** (C8, Migration order). The TUI sets the session idle
  in three places, and all three are where a run it owns ends: an agent
  turn (twice, the SDD gate's branch included) and a plan-authoring run.
  Plans 5c and 5d move those ends agent side, so a `SetIdle` added now
  would be deleted by them. The three calls stay until then.
- **`ClearFinishedRun` replaces `ClearRunEvents` and `ClearSDDProgress`**
  (C8). The TUI's one caller clears both together, on the user's next
  turn after a finished run.
- **`SetInteraction` takes `acpwire` types** (C3, C8). `acpclient`
  signatures may use only `acpwire` and C3's shared data packages, and
  `interaction` is not one of them. `acpwire.InteractionPatch` carries
  the patch, and the result is `Status`'s `acpwire.Interaction`. Both
  spell system access `system`. ACP's `session/set_mode` keeps
  `system_access`, and P3 maps between them.
- **The runtime's setters move into the adapter's `Deps`** (C4).
  `inproc.Deps` gains `SetInteraction` and `SetEffort`, bound to the
  runtime as `accessTUIOptions` bound the TUI options, so they keep
  working after a swap. The options and `accessTUIOptions` go.
- **`Steer` is the queued delivery** (C8). In process it queues even when
  no turn runs: the TUI puts a refused follow-up back on the queue.
  ACP's `session/steer` needs an active turn, and that put-back moves
  agent side with `Prompt` in plan 5c.
- **The new ACP methods are named** `session/steering_pop`,
  `session/steering_clear`, `session/set_sudo_unlock`,
  `session/subagent_cancel`, `session/sdd_clear` and
  `session/watch_resume_take`.

**2026-11-03, against `90488446` (plans 5a and 5b merged).** Found while
writing plan 5c:
- **One run at a time, agent side** (C4, C8). The adapter keeps one run
  slot that `Prompt`, `Review` and `RunAgent` take, and refuses a second
  run with `busy`, as ACP's `runTurn` refuses a second turn.
- **One run path** (C8). Every run takes the slot, begins work, registers
  its stop group, runs, sets the session idle, and ends the stop group
  and the work: the order of ACP's `runTurn` and of the TUI's
  `startAgentRun`.
- **A refused start comes back from the call** (C2, C8). The run reports
  `busy`, `quiescing`, `no_runner` (new) or `session_closed`, and the
  TUI writes today's refusal note. A prompt marked `queued` goes back on
  the steering queue agent side, which settles the put-back plan 5b left
  open.
- **`Stop` acts on the session's own run** (C8). It stops the run's stop
  group, the attached commands first, and then the run. Before the run
  has registered, it marks the run to be skipped. With nothing running it
  does nothing and records no pending stop, so a late stop cannot cancel
  the next turn. Swarm, SDD and plan-authoring runs keep the TUI's own
  registration until plan 5d.
- **A stopped run is not an error** (C8). `Prompt` reports `cancelled`,
  as ACP's `session/prompt` does.
- **A run's end sets the session idle agent side** (C8). The TUI's
  turn-end handler stops setting it, and the TUI's run command sets it
  for swarm and SDD runs until plan 5d.
- **`Status` says whether the agent runner is built** (C5). `agentReady`
  replaces the TUI's checks for a runner. `Prompt` without one records
  nothing; the TUI's echo of a prompt it cannot run stays until plan 6.
- **The runtime's runner and calls move into `Deps`** (C4). They read the
  runtime's current runner, state and factory, so they keep working
  after a swap and after a reload builds the runner. The runner source
  and its adoption go. Review, Run-now, the suggestion and the proposal
  now work after a `/connect` recovery without a restart.
- **`ReviewParams` carries the base and the range apart** (C3). The
  runtime encodes the base for its range resolver.
- **`Suggest` is the model call only** (C8). The deterministic rules, and
  the choice of when to ask, stay in the TUI.
- **Two bugs are fixed on the way** (C8). Run-now started its run on a
  stale copy of the model, so the live model never knew it was busy, and
  ran the agent on a child state nothing showed. It now runs on the live
  model as a subagent the session shows. The verify proposal parsed the
  parent session's transcript rather than its subagent's, and now parses
  its own.
- **The new ACP methods are named** `session/review`, `session/agent_run`,
  `session/suggest` and `session/verify_proposal`.

**2026-11-03, against `cc07ed11` (plan 5c merged).** Found while
writing plan 5d:
- **Swarm, plan and authoring runs take the run path** (C8). `StartSwarm`,
  `StartSDD`, `AnswerSDDGate` and `AuthorPlan` take 5c's slot, so every
  run the TUI starts registers its work, its stop group and its idle
  reset in the session, and the TUI's own stop registration goes.
- **Per-run choices are parameters** (C3, C8). The cast list's role
  overrides (keyed by role name), the strategy and the proposed verify
  commands travel in `SwarmParams` and `SDDParams`. The runtime applies
  them for the one run and restores the swarm's factory when it returns.
- **A plan run is built when it starts** (C8). The `/sdd` preflight reads
  `PlanPreflight`, an inspection that writes nothing, plus the verify gate
  a run would use. A plan the inspection rejects is refused at `/sdd`.
- **The human gate is a coded error** (C2, C8). A gated `StartSDD` or
  `AnswerSDDGate` returns `human_gate_required`. The session keeps the
  paused run: `AnswerSDDGate` records the answer and resumes it, `StopSDD`
  drops it and clears the gate, and an answer refused at admission leaves
  it paused.
- **Authoring returns its candidate** (C8, C11). `AuthorPlan` resolves the
  draft path, runs the author and returns the path, the auto-accept and
  the inspection, with `cancelled` when stopped. The draft path leaves
  C11's list. The TUI keeps choosing the design until plan 8 moves the
  message query behind `--from-last-plan`.
- **The runtime's orchestrator and factories move into `Deps`** (C4).
  `/swarm`, `/sdd` and `/sdd new` now work after a `/connect` recovery,
  and pick up config a reload applied, where the TUI's copies kept the
  startup config until `/new`.
- **Three bugs are fixed on the way** (C8). A plan the factory could not
  build crashed the TUI: the factory returned a nil adapter inside a
  non-nil interface, which ACP's `sdd_start` also missed. Proposed verify
  commands never reached the run they were proposed for, because its
  controller had already resolved the old ones. And a stopped plan
  authoring was reported as a failure, and left the TUI marking the next
  turn as cancelled.
- **Two authoring notes change** (C8). A draft path or author that cannot
  be built now fails the authoring run ("✘ Plan authoring failed: …"),
  because both happen inside the run.
- **The wire carries what the TUI renders** (C3). `PlanInspection` holds
  the strategy, the block and strict flags, the operation counts and the
  diagnostics, not the plan's IR or text.
- **The new ACP methods are named** `session/sdd_preflight`,
  `session/sdd_stop` and `session/sdd_author`. P3 extends
  `session/swarm_start` and `session/sdd_start` with the new parameters.
- **The header's plan count is corrected.** Plan 5c's review changed it to
  five plans and twelve documents. The Migration order has nine plans,
  and 5d, 5e and 6 to 9 are still to come.

**2026-11-03, against `bf211f49` (plan 5d merged).** Found while
writing plan 5e:
- **The end calls sit over a runtime end service** (C4, C8). `Deps.End`
  carries the runtime's membership, acceptance, settlement and exit
  analysis in wire types, and `bindTurnDeps` binds it. The TUI's
  `EndController` and its mirror types go, and so does `EndAccepted`,
  which the TUI never called.
- **`ActiveWork` is a read** (C2, C8). The warning need not be
  authoritative: the TUI always accepts with `RequireDisplayed`, and the
  runtime refuses to stop work the user was not shown, returning the
  membership to ask about again.
- **A session without an end service ends at once** (C8). Its membership
  is empty, it accepts and settles at once, and it refuses the exit
  analysis. The TUI's no-controller shortcuts go; only tests built such
  a model, because `Run` always wires the controller.
- **`Close` cancels, settles and shuts down** (C8). It cancels the run in
  flight, settles every pending request as shut down and shuts the state
  down. Closing a closed session does nothing, and the run path refuses
  a closed session when it takes the slot.
- **`Host.NewSession` replaces the swapper, and resume stays a restart**
  (C4, C8). The spec's `Host.Resume` is not added: the runtime has no
  in-place resume, since only `startRuntime` loads a stored session, and
  the TUI's other agent-side options are bound to the runtime a resume
  replaces. The TUI keeps reporting the session to resume as its program
  result. `WithLegacySession` hands the model the new state and tool
  registry until plan 9.
- **A bug is fixed on the way** (C8). After `/new` or `/clear` the TUI's
  end stage stayed at finishing, so every run was refused with "The
  conversation is ending; this request was not started.", a second
  `/new` quit Marshal, `/exit` and Ctrl+C quit without the warning or the
  postmortem question, and `/resume` exited instead of resuming. The
  swap now returns the end to idle, whether or not it succeeded.
- **The wire carries what the TUI renders** (C3). An end work item has
  its ID, label and group, not the runtime's phase, kind, parent flag,
  subagent ID or targets.
- **The new ACP methods are named** `session/end_work`,
  `session/end_accept`, `session/end_wait`, `session/exit_analysis` and
  `session/postmortem`. `Close` and `Host.NewSession` use
  `session/close` and `session/new`.

**2026-11-04, against `fd0e778a` (plan 5e merged).** Found while
writing plan 6:
- **A note carries a role** (C9). Chosen with the user. With no agent
  runner, Enter appends the user's text as a user message and starts
  nothing, which tests pin. `AddNote` takes an `acpwire.Note` whose role
  is system when empty, and accepts only system or user, so the echo
  keeps its behaviour.
- **Notes and notice calls fire no `Changed`** (C2, C9). The TUI repaints
  after each, as today. A note on a closed session is dropped.
- **The TUI keeps deciding when a notice changes** (C9). Only the calls
  move. The TTL dismissal still reads, then dismisses; P3 needs a
  conditional form once the mirror can change in between.
- **Classification moves onto the run's error** (C2, C9). The three
  error types `noticeForError` matched are agent side, and their Go
  types do not survive a wire. The run path returns a provider or login
  error coded, with its text unchanged and the notice in an
  `ErrorData`. A cancellation's error is left alone, as the TUI's own
  check expects. `ErrorData` is the envelope plan 7 extends with the
  provider's fields.
- **The command types move, and ACP's JSON stays** (C1, C9). ACP fills
  only the fields it fills today. Its `kindOf` and `toWireDoc` become
  `commands.Command.Kind` and `commands.WireDocOf`.
- **`Commands()` carries the dispatch fields** (C9). ACP's `kind` cannot
  drive the dispatch: `/trust` is TUI-only with a handler, and ACP
  reports some TUI-only commands as headless.
- **`Command` keeps the TUI's semantics** (C9). It makes no busy check,
  and stays synchronous in the dispatch, so its text is noted before any
  agent goal starts.
- **Row actions get IDs** (C9). `/timeline`, `/branches` and `/context`
  return docs whose actions are closures over the state. The adapter
  mints `a1`, `a2` and on, depth first, keeps the actions of the 16 most
  recent results that minted any, and runs one with `CommandAction`.
- **Doc panels take their actions** (C9, C12). `docpanel.New` takes a
  wire doc and a function from action ID to command. The `/run` outcome
  doc's two actions stay TUI side until plan 8. `SetSource` had no
  caller and goes.
- **The TUI has its own logger** (C9): `WithLogger`, fed the runtime's
  logger by `Run`.
- **The wire names the constants the TUI compares** (C1): roles, content
  types, activity kinds, notice categories and severities, pinned to the
  session's by a test.
- **The new ACP methods are named** `session/note`,
  `session/notice_set`, `session/notice_clear`, `session/notice_dismiss`
  and `session/command_action`. `Commands` and `Command` use
  `session/command_list` and `session/command`.

**2026-11-05, against `301e19c8` (plan 6 merged).** Found while
writing plan 7a:
- **Plan 7 is three plans** (Migration order): 7a providers and
  sign-ins, 7b config, 7c trust, skill gates and tools. Providers go
  first because 7b's redacted config read leaves the TUI without
  literal keys, which the probes and sign-ins read today.
- **A probe takes the provider's config from the client** (C3, C10). The
  connect wizard and the settings browser probe configs not saved yet,
  so the probes carry a config that may hold a typed key, as
  `config/probe_provider` already does. The adapter builds the client
  with the runtime's resolver, the model cache's directory and the
  session's thinking margin, which no probe reads.
- **Capability probing has a check** (C10). The settings browser decides
  on the Update goroutine whether to show "detecting…", so
  `CanProbeModel` answers that and `ProbeModel` runs the probe.
- **The model cache is four calls keyed agent side** (C10). The TUI keeps
  its discovery map and its newest-attempt guards, so a stale probe
  still moves neither the map nor the disk. The cache key hashes the
  literal key, so the adapter reads the provider's config itself.
- **Thinking options stay a TUI computation** (C10). The helpers are pure
  over `schema` types and move to `llm/schema`; `ListModels` carries no
  thinking options. `UniqueName` moves to `strutil`.
- **A sign-in is a check and a call** (C2, C10). The check keeps today's
  synchronous failures: a store or engine that cannot open, or an MCP
  server that does not resolve, shows its sentence and starts nothing.
  The call reports its URL through a `show` function, and the engine's
  own `Open` stays nil agent side, so the TUI opens the browser (C12).
  C2 gains the rule for calls that report progress.
- **The resolver moves into the adapter's `Deps`** (C4). Nil means the
  production resolver, the substitution `tui.New` made. `app`'s wiring
  test reads it from the host.
- **`ErrorData` gains no provider fields** (C2). Plan 6 expected them for
  plan 7, but no TUI code reads them.
- **The new ACP methods are named** `config/provider_templates`,
  `config/probe_model`, `config/models_list`, `config/models_save`,
  `config/models_forget`, `config/models_rename`,
  `config/login_provider` and `session/mcp_login`. `ProbeProvider` uses
  `config/probe_provider`, which exists.

**2026-11-05, against `341f1941` (plan 7a merged).** Found while
writing plan 7b; the base suite passed (117 packages):
- **Plan 7 is four plans** (Migration order). Redaction moves from 7b to
  a new 7d. It needs its own rules: a write must keep a stored key it
  does not name, through a provider rename too; a cleared key must stay
  cleared; the settings browser shows a key's last four characters; and
  the probes need a saved provider's key. Nothing else in 7b depends on
  it.
- **Writes keep their shape and do not reload** (C10). Each save becomes
  one call with today's arguments minus the path. The spec's "the
  adapter reloads after writing and returns the path, the new config and
  its diagnostics" is replaced: a connect makes three writes before one
  reload, and the settings browser two, so the TUI keeps its own order.
  User-global writes are `Host` calls; the project writes are `Session`
  calls, because they follow the session's working directory and its
  layers. No write returns the busy code: the TUI's own check runs
  first, as today.
- **Layers and trust follow the writes agent side** (C10). After any
  successful write the adapter refreshes the session's layers, and after
  a project save the trust hash first. So the TUI's layer refresh and
  trust hook go, and the panels lose the layer snapshots they kept only
  for their own project saves.
- **Reloads are three session calls** (C10): `ApplyConfig`,
  `ReloadConfig` and `ReconnectMCP`. `ReloadConfig` and `ReconnectMCP`
  report when there is no reload path, which only a test builds.
- **Paths come from the status** (C5, C10). `SessionStatus` gains
  `UserConfigPath` and `ProjectConfigPath`, for the TUI's notes and the
  connect summary.
- **`SaveUserValue` takes `any`** (C3).
- **Preset setup is `PreparePresets`, and the provenance is opaque**
  (C10). The TUI holds the updated sidecar as raw JSON and hands it back
  to `SaveProvenance`.
- **The panels' duplicate project saves stay.** The settings browser and
  the agents roster save the project file, and the model's handler then
  saves it again. Removing the first would change when the browser's
  retry gesture retries.
- **The skills panel stays for plan 8.** It reads and writes
  `p.state.Config` directly, so T1b does not gain `Config` until then.
- **The new ACP methods are named** `session/config`,
  `session/apply_config`, `session/reload_config`,
  `session/mcp_reconnect`, `session/save_project_config`,
  `session/save_verify_commands`, `config/set_value`,
  `config/save_providers`, `config/save_presets`,
  `config/prepare_presets` and `config/save_provenance`.
  `SaveProviderKey` uses `config/set_provider_key`, which exists.
- **The bridge's frame cap** (P3 spec G5) is fixed in plan 7b, as
  decided with the user: 64 MiB, and an oversized frame fails the child.

**2026-11-05, against `d9db66e7` (plan 7b merged).** Found while
writing plans 7c and 7d, which run in parallel (decided with the user):
- **Skill-gate settings move to plan 8** (C10, Migration order). They
  live in the skills panel, which plan 8 moves as a whole, so 7c and
  plan 8 stay disjoint (decided with the user).
- **The pending trust prompt is a host read** (C10). `Host.TrustPrompt`
  returns the directory waiting for a decision, which does not change
  while the host runs: declining left `/trust` able to ask again.
  `DecideTrust` refuses a decision for another directory, with nothing
  pending, or with an unknown value.
- **`Tools` reads the runtime's current registry** (C10). It reports
  false while no runner is built. A reload that builds the runner now
  reaches the agents roster at once; before, it waited for `/new`.
- **The new ACP methods are named** `trust/prompt`, `trust/decide` and
  `session/tools`.
- **A secret travels as its mask, in its own place** (C3, C10; decided
  with the user that nothing visible changes). This replaces "replaced
  by `""`, its source reported alongside". With the mask in the key's
  place, the "key stored" label, the wizard's label, the connect flow's
  env clearing, `config.Diagnose` and the browser's receipts all read it
  unchanged. The source is already in the config.
- **Restoring follows a fixed order** (C3). The same path in the live
  config, then in the last-loaded layers; for a provider the call names
  as renamed, its old name; then the one stored key whose mask it is. A
  literal or empty value is kept, so a cleared key stays cleared. A mask
  with nothing behind it fails a write or install, and a probe drops the
  key instead.
- **`SaveUserProviders` gains the renames** (C10). The settings browser
  records each rename since its baseline.
- **MCP `env` and `headers` stay in the clear**, a recorded follow-up:
  they are outside `config.MaskSecrets`' set and C3's.

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

**Plan 4 (agent requests), branch `p2-4-requests`.** Added
`internal/acpwire/requests.go`: `PendingRequests` with one field per kind
(`Approvals`, `Question`, `SkillGate`, `Sudo`, `Access`), `ApprovalRequest`
and `ApprovalAnswer` with the four `Approval*` decisions, `QuestionRequest`
with `Question`/`QuestionOption`/`Answer` and `AnswerUnanswered`,
`SkillGateRequest` with the four `SkillGate*` choices, `SudoRequest` and
`SudoAnswer`, and `AccessRequest`/`AccessChoice`/`AccessResult` with the
seven `Access*` statuses. `acpwire.Sandbox` (the backend, its network
isolation, and the shell's configured network allowance) arrives as
`SessionStatus.sandbox`, and the status doc's forward reference now names
only plan 6's current notice.

`acpclient.Session` gains eight methods after `Callers`, each with its ACP
line and its `Methods` row: `Pending` (`session/request_permission`),
`AnswerApproval` (`session/request_permission`), `AnswerQuestion`
(`session/request_question`), `AnswerSkillGate`
(`session/request_permission`), `AnswerSudo` (`session/request_secret`),
`ReconcileAccess` (no ACP method, so an empty string), `AnswerAccess`
(`session/request_permission`) and `Rollback` (`session/rollback`).

The in-process adapter does the work the TUI used to do. `Pending` keys every
open request by its registry ID, by an access request's `RequestID` when a
typed request occupies the approval slot, or by a minted `local-N` for an
unregistered prompt, which it keeps while that prompt stays open. It lists
the session's own approval first, then each running subagent's in
registration order, and derives the "why" line from the state that raised the
request (the child's own for a subagent's approval), the tool label from
`viewmodel.DisplayToolName`, and the forced class from the request itself.
`Pending` settles nothing and never calls a runtime callback, and a closed
session keeps answering it. The answer calls settle on the owning state's
registry, or on the prompt's own channel for an unregistered fixture, and an
"always" or "session" rule always lands on the session, whichever subagent
asked; "always" saves to the user config and applies the new config through
`RebaseInteractionConfig`, falling back to a session rule when the save
fails, and a standing answer to a forced prompt is a deny the adapter
enforces. `AnswerSkillGate` records the sticky choice, `AnswerSudo` hands the
password slice straight through, `Rollback` restores the backup and logs the
audit row, and access goes through the runtime's `AccessRequest` and
`ResolveAccess` callbacks in `ReconcileAccess` and `AnswerAccess`, both of
which settle the request as failed and report none when either callback is
missing.

The TUI's approval, question, skill-gate, sudo and access surfaces now hold
the wire values, name a request by a `promptRef` (its kind and its wire key)
rather than by a pointer, read `Pending` once per function, and answer
through `AnswerQuestion`, `AnswerSkillGate`, `AnswerSudo` and
`AnswerApproval`; `settleApproval` is one call to the session. The approval
form takes an `acpwire.ApprovalRequest`, so `approvalWhyFor`,
`forcedPromptClass`, `approvalOwner`, `isModeElevationApproval`,
`userConfigDir` and `toolnames.go` are gone, and the running shell row and
the approval panel read the sandbox from `Status().sandbox`.
`tui.WithAccessRequests` and its two fields are deleted: the adapter's `Deps`
carry the runtime's access seam.

T1b gains 19 names, all at plan `4`: `AccessRequest`, `CancelAccess`,
`PendingQuestion`, `SetPendingQuestion`, `ResolveQuestion`,
`PendingSkillGate`, `SetPendingSkillGate`, `ResolveSkillGate`,
`SkillGateRecordAllow`, `PendingSudoAuth`, `SetPendingSudoAuth`,
`ResolveSudoAuth`, `PendingApproval`, `SetPendingApproval`,
`ResolveApproval`, `ClearPrompt`, `SandboxInfo`, `RollbackBackup` and
`LogToolCall`. T1's allowlist loses ten entries across nine files:
`accesspanel/accesspanel.go`, `question.go`, `skillgate.go`, `sudoauth.go`,
`approval.go` and `toolnames.go` lose their keys entirely, `keypress.go`
drops `tools/registry`, `steps.go` drops `app/session`, and `model.go` drops
`config.SaveUserConfigRule` and `internal/permissions`.

Decision 12's edge cases, one line each: a subagent's approval can now be
reached by its key, so a work item naming it navigates to it and Alt+Enter's
composer tracks it; the model-cost consent has a minted key, so the composer
tracks it too; a subagent's mode-elevation `file.write` approval no longer
offers "always" or "session", which both denied anyway; and a new request
rebuilds the approval form even when it reuses a tool-call ID, so two
model-cost consents in a row no longer share the first one's form.

**Plan 5a (work), branch `p2-5a-work`.** Added `internal/acpwire/work.go`:
`WorkSnapshot`, `WorkItem`, `WorkOrigin`, `WorkReference`,
`WorkCapabilities`, `WorkPrompt`, `WorkProgressConfig`, `WorkProgress`,
`WorkPhase`, `WorkMetric`, `WorkRow`, `WorkQuery`, `WorkPage`, `WorkDetail`
and `WorkStopResult`. Every time is an `int64` Unix millisecond with
`omitempty` — a call's `startedAt` and `endedAt`, the snapshot's
`observedAt`, and the progress observation's `observedAt` and `measuredAt` —
written and read with `acpwire.Millis` and `acpwire.Time`, because C3
forbids RFC 3339 strings. ACP's own `Work*Wire` types stay in `internal/acp`,
and `WorkTarget` and `WorkAck` are not added to the wire, because the TUI
never acknowledges work. `acpwire.SubagentDrill` gains `sessionId`, the
child's session ID, which a work item's reference names.

`internal/viewmodel/workwire.go` holds six conversions: `WorkSnapshotOf`,
`WorkItemOf`, `WorkProgressOf`, `WorkPageOf`, `WorkDetailOf` and
`WorkStopResultOf`. Each makes fresh values — a nil slice or pointer stays
nil, every non-nil pointer is a new pointer to a copy, and a zero time is
0 — and `WorkProgressOf` was exported for the live box's temporary bridge, which
Task 5 removed; it stays exported so `livebox`'s test can round-trip the
conversion through `progressOf`, pinning that it is lossless. `viewmodel` keeps
its own work types and predicates, which the work coordinator and ACP use.

`acpclient.Session` gains four methods, each with its `Methods` row:
`Work() (acpwire.WorkSnapshot, bool)` and
`ListWork(ctx, acpwire.WorkQuery) (acpwire.WorkPage, error)` on
`session/work_list`, `InspectWork(ctx, taskKey string, raw bool)
(acpwire.WorkDetail, error)` on `session/work_inspect`, and
`StopWork(ctx, taskKey, reason string) (acpwire.WorkStopResult, error)` on
`session/work_stop`. All four methods exist in ACP, and no acknowledge call
is added: P3 adds `session/work_acknowledge` when a client needs it.

The in-process adapter subscribes to the work stream before it reads the seed
snapshot when a session is made, so nothing published between the two is
lost, and the seed binds the conversation. It keeps a published snapshot
unless it names another conversation than the bound one or is older than the
kept snapshot of that conversation, and fires `Changed` only for a snapshot
it keeps; `Changed` now fires after anything `Status` or `Work` returns may
have changed. It names its conversation by its state's session ID, maps
`viewmodel.ErrWorkStaleCursor` to `CodeStaleWorkCursor`, answers a closed
session's `Work` from the last snapshot it kept, and fails the three calls on
a closed session. It serves work only when `Run` found a working provider:
`Deps.Work` is the runtime only when the startup notice is not
`session.NoticeProvider`.

The TUI applies the session's snapshot on `Changed`, keeps its previous
progress observations, and renders the wire types through `livebox`'s own
copies of `WorkActive` and `WorkNeedsAttention` and through `progressOf`,
which turns a wire observation back into a `progress.Snapshot` so
`progress.EstimateFor` can measure it. A work item's subagent comes from
`Status().Subagents` by `Drill.SessionID`. `tui.WithWork`, the TUI's work
subscription and its work pump are gone.

T1b gains one name, at plan `5a`: `Subagents`, because a work item's
subagent is found through it. T1's allowlist lost 11 entries across 10 files
and eight keys with them — `events.go`, `livebox/box.go`,
`livebox/livebox.go`, `livebox/running.go`, `livebox_view.go`, `pump.go`,
`steps.go` and `view.go` — and `internal/app/tui` imports no `viewmodel`
outside its tests, which stand in for the runtime's service.

Decision 12's edge cases, one sentence each: when two work snapshots land
before the TUI reads one, a card's rate is measured across both, so it
averages over the longer window, where the pump delivered every event; and
times lose their sub-millisecond part, so two progress observations within
the same millisecond measure no rate.

**Plan 5b (turn control), branch `p2-5b-turn-control`.** Added
`acpwire.InteractionPatch` — `Mode`, `Auto` and `System`, each a nil-able
field with `omitempty`, so revoking system access (`false`) and leaving it
alone (`nil`) are different patches — and the two effort timing values
`acpwire.EffortAppliesNextStep` and `EffortAppliesNextTurn`, which equal
`session.EffortAppliesNextStep` and `session.EffortAppliesNextTurn`.

`acpclient.Session` gains nine methods, each with its `Methods` row: `Steer`
on `session/steer` (existing), `SetInteraction` on `session/set_mode`
(existing) and `SetEffort` on `session/set_effort` (existing), plus six that
are new in P3 — `PopSteering` on `session/steering_pop`, `ClearSteering` on
`session/steering_clear`, `SetSudoUnlock` on `session/set_sudo_unlock`,
`CancelSubagent` on `session/subagent_cancel`, `ClearFinishedRun` on
`session/sdd_clear` and `TakeWatchResume` on `session/watch_resume_take`.

The in-process adapter forwards each call to the state, adding no policy of
its own: `Steer`, `PopSteering`, `ClearSteering`, `SetSudoUnlock`,
`CancelSubagent`, `ClearFinishedRun` (progress then event log, the order the
TUI's helper used) and `TakeWatchResume` reach `session.State` directly.
`SetInteraction` and `SetEffort` go through two new `inproc.Deps` fields
bound to `rt.SetInteraction` and `rt.SetSessionEffort` in `Run`'s `Deps`
literal; each reads the runtime's current state, so the same `Deps` keep
working after a swap, and a call without its setter fails with
`errNoInteraction` or `errNoEffort`. `Status` now builds its interaction
through `interactionOf`, which `SetInteraction` shares. A closed session
refuses every call: the error-returning ones answer `CodeSessionClosed`, the
bool-reporting ones report their zero values, and none of them touch the
replaced state.

The TUI makes all nine calls through the session: Enter while busy queues
with `Steer`, Ctrl+X undrilled clears with `ClearSteering`, a follow-up pops
with `PopSteering`, the drilled Ctrl+X stops with `CancelSubagent`, the next
turn clears a finished run with `ClearFinishedRun`, the turn end takes the
latch with `TakeWatchResume`, `/mode`, `/auto` and `/system` submit with
`SetInteraction`, `/effort` and Alt+E with `SetEffort`, and `/system unlock`
toggles with `SetSudoUnlock`. `tui.WithInteractionSetter`,
`tui.WithSessionEffortSetter`, the two model fields behind them and `app`'s
`accessTUIOptions` are deleted, along with the two "unavailable" notes they
printed: production always wires both setters through `Deps`, so the TUI can
no longer tell a missing setter from a failing one. The three idle resets
(`handleAgentFinished` twice, `handlePlanAuthorFinished` once) stay until
plans 5c and 5d move those turn ends agent side.

T1b gains eight names, all at plan `5b`: `PushSteering`, `PopSteering`,
`ClearSteering`, `CancelSubagent`, `ClearRunEvents`, `ClearSDDProgress`,
`TakeWatchResume` and `SetSudoUnlockEnabled`. T1's allowlist did not change:
every file this plan edits keeps its `marshal/internal/app/session` import
for the system notes it still writes, which plan 6 moves.

Two details the plan left open, recorded here rather than silently settled.
`flushQueuedSteering` ignores the error from the `Steer` call that puts a
refused follow-up back: the old `PushSteering` could not fail, but the seam
call can, and a closed session therefore drops the text instead of reporting
it. That matches the plan's decision to leave the prompt-side put-back to
plan 5c; surfacing it now would add a note 5c has to remove. And the
replacement model in `TestAdapterAccessFollowsNewSession`'s post-swap `/act`
check carries `WithCommandRegistry`, which the plan's step did not mention:
without it `/act` never dispatches, so the check would have asserted nothing.

**Plan 5c (prompt), branch `p2-5c-prompt`.** Added `internal/acpwire/turns.go`:
`PromptParams` (`Text`, `Queued`), `PromptResult` (`StopReason`) with the two
stop reasons `StopReasonEndTurn` and `StopReasonCancelled`, `ReviewParams`
(`Focus`, `Model`, `Range`, `Base`), `AgentRunParams` (`Agent`, `Goal`) and
`VerifyCommands` (`Build`, `Test`). `errors.go` gains `CodeNoRunner`, and
`SessionStatus` gains `AgentReady`, the only new status field: it reports
whether the session's runner is built, so the TUI can drop its own
`m.runner == nil` checks.

`acpclient.Session` gains six methods with their `Methods` rows: `Prompt` on
`session/prompt` (exists), `Stop` on `session/cancel` (exists), and four new
in P3 — `Review` on `session/review`, `RunAgent` on `session/agent_run`,
`Suggest` on `session/suggest` and `VerifyProposal` on
`session/verify_proposal`.

The adapter's run path is one path, and it is where admission now lives. One
turn slot guards the session: a second `Prompt`, `Review` or `RunAgent` while
one runs gets `CodeBusy`, as ACP's `runTurn` refuses a second turn. Every run
takes the slot, calls `BeginWork`, registers its stop group with
`BeginTurnStop` (after `BeginWork`, which stamps the turn onto the work item
it opened), runs, sets the session idle, releases the slot and ends the stop
group and the work. A refusal comes back from the call: `CodeBusy`,
`CodeQuiescing` (any `session.ErrSessionQuiescing`, the conversation's ending
included), `CodeNoRunner` or `CodeSessionClosed`, and a `Queued` prompt's text
goes back on the steering queue then, which settles the put-back plan 5b left
here. A stopped run is not an error — `Prompt` returns `StopReasonCancelled`,
`Review` and `RunAgent` return nil. `Stop` acts on the slot only: it stops the
registered group first (service acceptance before agent cancellation) and then
cancels the run's context; with nothing running it does nothing, and it records
no pending cause, so a late stop can never cancel the next turn. A panic in a
run is recovered into `panic in agent run: …`. `Suggest` and `VerifyProposal`
take no slot, no work and no stop registration.

`inproc.Deps` gains `Runner` and the four calls `Review`, `RunAgent`,
`Suggest` and `VerifyProposal`, bound in `Run` by `bindTurnDeps` to
`Runtime.TurnRunner`, `RunReview`, `RunCustomAgent`, `SuggestNextReply` and
`ProposeVerifyCommands`. Each reads the runtime's current runner, state and
factory under `rt.mu`, so they keep working after a swap and after a reload
builds a runner — `adoptRunner` and the runner source are gone. `review.go`'s
`runReviewSubagent` body from the factory call inward moved into
`runSubagentTask`, which takes the error prefix as `kind`; the review and the
custom-agent run share it, so Run-now's agent is a registered subagent labelled
`agent · <name>` whose summary lands in the transcript.

The TUI starts every prompt, review and Run-now through the session
(`startSessionRun`, `startPrompt`), stops them through it (`cancelTurn` calls
`Stop`, and reaches the state's own registration only for a run the TUI still
registers itself), and asks it for the suggestion and the proposal.
`handleAgentFinished`'s idle reset and the SDD gate branch's are gone: the run
path sets the session idle, and `runAgentCmd` sets it for the swarm, SDD and
plan-authoring runs that keep the TUI's path until plan 5d. A refused start is
reported from the run's end as `agentFinishedMsg{refusal: …}`, so the notes
`Cannot start work: …` and `Cannot start review: …` are written once.
`tui.WithRunner`, `WithRunnerSource`, `WithReviewDispatcher`,
`WithSuggestionProvider`, `WithSubagentFactory` and
`WithCustomAgentRunnerFactory` are deleted, along with the six model fields
behind them, `SessionSwapResult.Runner` and `.ReviewDispatcher`,
`buildCustomAgentRunner`, `proposalFromSession` and `runReviewCmd`; `app`
deletes `beginSessionStop` too, whose only callers were the two review
dispatchers.

The two fixes. Run-now ran on a stale model: the roster's dispatch closure
captured `m`, so `busy`, `agentCancel` and the stop teardown were set on a copy
`Update` had already returned, while the run itself went ahead — the live model
stayed idle, Ctrl+C and `/stop` could not reach it, and a second prompt could
start beside it. The dispatch now captures nothing (`runNowDispatch` returns a
`runAgentMsg`) and the live model starts the run. The note for an agent that
cannot be built changed with it: it is now a failed turn
(`✘ Turn failed: run agent: build subagent: …`), not
`Cannot run custom agent …`, and the swarm-runner fallback is gone because
production always wired the factory. The verify proposal parsed the wrong
transcript: its runner writes to a child state, but the parse scanned the
parent session's messages, so it failed unless the conversation's last reply
happened to hold a build/test object. `ProposeVerifyCommands` parses the child
state's messages through `verifyCommandsFrom`, which carries the same error
texts.

T1b and the allowlist did not change. Every state call the prompt path stops
making — `BeginWork`, `EndWork`, `BeginTurnStop`, `EndTurnStop`,
`RequestTurnStop`, `SetActivity` — is still made by the swarm, SDD and
plan-authoring paths until plan 5d, and each edited file on the allowlist still
uses every entry it has, most of them `marshal/internal/app/session` for the
notes it writes until plan 6.

Decisions recorded here rather than silently settled. `RunAgent` treats a
whitespace-only agent name as empty, not only `""`, which matches how the roster
reads its own names. The panic recovery is a small `callRun` helper so
`runTurn`'s single defer stays the cleanup defer rather than gaining a second
ordering dependency. `errNoAgentName` is checked after the missing-`runAgent`
check, both before the slot is taken. And `bindTurnDeps` runs after
`deps.Work` is assigned, on the fully built `Deps` and still before
`inproc.New`.

Three refinements after review. `runTurn` reports whether the run's work
actually began, so `Prompt` can tell a stop that skipped the turn from one that
cancelled it: a queued text is put back only when the model never saw it, and a
stopped run that had started keeps its text. `BeginWork` failing with
`ErrConversationEnding` is reported as `CodeQuiescing`, not as a codeless
error, so the TUI names an admission refusal instead of a failed turn. And
`RunReview`/`RunCustomAgent` refuse with `CodeNoRunner` when the runtime has no
factory, which is what the `Session` docs already promised.

**Plan 5d (pipelines), branch `p2-5d-pipelines`.** Added
`internal/acpwire/pipelines.go`: the three strategy constants `StrategyAgent`,
`StrategyAdaptive` and `StrategyStrict`; `SwarmParams` (`Goal`, `Overrides`),
`SDDParams` (`PlanPath`, `Strategy`, `Overrides`, `Verify`),
`PlanPreflightParams` (`PlanPath`, `Strategy`), `PlanPreflight`
(`Inspection`, `Verify`), `VerifyGate` (`Build`, `Test`, `Known`, `Detected`),
`PlanInspection` (the effective strategy, `HasBlocks`, `StrictBlocked`, the
operation totals, each task's counts and the diagnostics), `PlanTask`,
`PlanDiagnostic` with `DiagnosticError` and `DiagnosticWarning`, `AuthorPlanParams`
(`Goal`, `Design`) and `AuthoredPlan` (`StopReason`, `PlanPath`, `AutoAccepted`,
`Inspection`). The wire carries what the TUI renders and no more: no plan IR
and no plan text, and role overrides keyed by the role's name.

`acpclient.Session` gains six methods with their `Methods` rows: `StartSwarm`
on `session/swarm_start`, `StartSDD` on `session/sdd_start` and `AnswerSDDGate`
on `session/sdd_answer`, all three on existing ACP methods, and three new in
P3 — `PlanPreflight` on `session/sdd_preflight`, `StopSDD` on
`session/sdd_stop` and `AuthorPlan` on `session/sdd_author`.

The adapter runs the three new runs on plan 5c's run path. `PlanRun` is the
adapter's plan-execution surface: `Run` runs or resumes the run and `Answer`
delivers the human's answer. `StartSwarm`, `StartSDD`, `AnswerSDDGate` and
`AuthorPlan` each take the slot, begin work, register the stop group and go
idle at their end, and a stopped run is not an error. A plan run its human gate
pauses is kept as the session's paused run and reported as
`CodeHumanGateRequired`; `settlePlan` records it, a new `StartSDD` replaces it,
a refused answer leaves it paused, and `StopSDD` drops it and clears the
question. `AnswerSDDGate` records the answer as the user's message and resumes
the run. `AuthorPlan` returns the candidate and reports a stopped run as
`StopReasonCancelled`. `StartSDD` and `AnswerSDDGate` check the strategy with
`checkStrategy`, and the preflight takes no slot.

The runtime answers the four calls. `RunSwarm`, `NewPlanRun`, `PlanPreflight`
and `AuthorPlan` read the runtime's current orchestrator, factories and state
under `rt.mu`, and `bindTurnDeps` binds them as `Deps.Swarm`, `Deps.NewPlanRun`,
`Deps.PlanPreflight` and `Deps.AuthorPlan`, so a `/connect` recovery and a
reload are picked up without a `/new`. `RunSwarm` installs the cast list's
overrides for one run and restores the factory when it returns, whatever the
outcome. `NewPlanRun` sets the controller's strategy and replaces its verify
commands when the params carry them. The runtime owns its runner types:
`app.PipelineRunner` and `app.SwarmOverrideFactory` replace `tui.AgentRunner`
and `tui.SwarmOverrideFactory` in its fields and builders, and ACP's
`AgentRunner` still converts the factory's result. The pipeline factory closure
now returns a true nil for a plan it cannot build.

The TUI starts every run through the session. `/swarm` and `/sdd` gate on
`Status().AgentReady` instead of a nil orchestrator or factory; `/sdd` asks the
session for its preflight and reports a plan it rejects as
`Cannot start plan run: …`; the confirm carries the strategy, the role
overrides and the proposed verify commands as parameters; the gate's answer and
stop go through `AnswerSDDGate` and `StopSDD`; and `/sdd new` runs through
`AuthorPlan`. `WithSwarmRunner`, `WithPipelineFactory`, `WithSwarmOverrideFactory`
and `WithPlanAuthorFactory` are deleted with the six model fields and the
`SessionSwapResult` fields behind them, which now holds only `State`, `Session`
and `ToolRegistry`; `startAgentRun`, `runAgentCmd`, `beginTurnStopRegistration`,
`releaseTurnStop` and `stopTurnWork` go, and `cancelTurn` simply stops the
session's run and then cancels. `sddreview.New` takes the wire's
`AuthoredPlan`.

Three fixes. A plan the factory cannot build no longer crashes the TUI: the
closure returned a non-nil interface wrapping a nil controller, so `/sdd
missing.md` wrote its note and then dereferenced nil, and ACP's `sdd_start`
answered "internal error: handler panicked". The closure returns nil and
`NewPlanRun` reports "cannot build a runner". Proposed verify commands now
reach the run: the controller had been built at dispatch from the startup
config and nothing reloads after the confirm persists the proposal, so the run
kept the old commands — and in a repository with no manifest that meant no gate
at all. A stopped plan authoring is not a failure: `handlePlanAuthorFinished`
never cleared `cancelling`, so a stop wrote "✘ Plan authoring failed: context
canceled" and the next normal turn reported "Agent turn cancelled."; it now
writes "Agent turn cancelled." once and clears the flag. The authoring notes
changed with them: a failed path resolution or author build is now
"✘ Plan authoring failed: …" inside the run, and a refused start is
"Cannot start work: …".

T1b gains seven names: `ClearSDDGate`, and `BeginWork`, `EndWork`,
`BeginTurnStop`, `EndTurnStop`, `RequestTurnStop` and `SetActivity`, all at plan
`5d`. The allowlist loses `model.go`'s `marshal/internal/agent/swarm` and
`marshal/internal/pipeline`, `commands_dispatch.go`'s and `sdd_command.go`'s
`marshal/internal/pipeline`, and the `sdd_authoring.go` and
`sddreview/review.go` entries; `model.go` keeps `sddplans` and `sddauthor` for
plan 8 and `config.SaveVerifyCommands` for plan 7.

**Plan 5e (ending and swapping), branch `p2-5e-ending`.** Added
`internal/acpwire/ending.go`: the intent constants `EndIntentExit` and
`EndIntentReplace`; `EndWork` with its `EndWorkItem`s (`ID`, `Label`, `Group`);
`EndAcceptParams` (`Intent`, `DisplayedIDs`, `RequireDisplayed`); and
`EndDecision` (`Accepted`, `Expanded`, `Work`). The wire carries what the
warning renders and no more: an item's phase, kind, parent flag, subagent ID and
the stoppable targets stay agent side.

`acpclient.Session` gains six methods with their `Methods` rows: `ActiveWork` on
`session/end_work`, `AcceptEnd` on `session/end_accept`, `WaitForEnd` on
`session/end_wait`, `RunExitAnalysis` on `session/exit_analysis` and
`Postmortem` on `session/postmortem`, all five new in P3, and `Close` on
`session/close`, which exists. `acpclient.Host` gains `NewSession` on
`session/new`, which exists.

The adapter serves them over `inproc.EndService`, which `Deps.End` carries.
A session without one ends at once: `ActiveWork` is empty, `AcceptEnd` accepts,
`WaitForEnd` settles immediately and `RunExitAnalysis` refuses with
`CodeNoRunner`, so only a test builds one. `Close` marks the session closed,
drops a paused plan run and the run in flight, settles every pending request as
shut down, shuts the state down and closes the change signal; it cancels the
run only after releasing its lock, and closing a closed session does nothing.
`runTurn` refuses a session that closed between `Prompt`'s check and the slot,
so no run can start between `Close` and its cancel. `Postmortem` builds and
writes the report agent side, where the TUI's exit path wrote it: the same home
lookup, project slug and file name. `Deps.NewSession` carries the runtime's
new-session factory; `Host.NewSession` closes the session it replaces and
returns `swap` over the new state, and `swap` is unexported now that nothing
outside the package calls it.

The runtime answers the calls through the new `internal/app/ending.go`:
`endService` converts the runtime's shapes to the wire's and leaves every
decision to the runtime, and `bindTurnDeps` binds it as `Deps.End` together with
`Deps.NewSession`. `rt.NewSession` and `newSessionLocked` return only the state;
the runner, the orchestrator, the factories and the registry are installed on
the runtime and reached from there. `Run`'s swapper goes through
`seamHost.NewSession` and reads the state and registry under `rt.mu` after it
returns, and the exit analysis reads `rt.TurnRunner()` under that lock rather
than `rt.Runner`, which a reload may replace.

The TUI ends and swaps through the session. `beginEnd`, `acceptEnd`,
`settleEnd`, `refreshEndWarning`, the postmortem extraction, the scoped exit
analysis and the postmortem answer all reach `m.sess`, and the model's snapshot
is the wire's `EndWork`. `EndController`, `WithEndController`, `EndWorkSnapshot`,
`EndWorkItem`, `EndAcceptRequest`, `EndDecision`, `EndIntentExit`,
`EndIntentReplace` and `writePostmortemFor` are deleted, with `app.go`'s
`tuiEndController` and its snapshot converter. `finishQuit` calls `Close` where
it called `ResolvePendingForShutdown()` and no longer calls `Shutdown()`.
`SessionSwapResult`, `SessionSwapper`, `SessionSwapperFunc`,
`WithSessionSwapper` and the `sessionSwapper` field are deleted: `/new` and
`/clear` ask `Host.NewSession`, and `WithLegacySession` re-reads the runtime's
current state and tool registry for the groups plans 6 to 8 move. A host
without `NewSession` fails the swap with "Failed to start new session: inproc:
new sessions are not available" instead of the deleted "New session is not
available in this build." branch.

A replacement leaves the end idle (the spec's decision 10). Since `e57821a4`,
`finishReplacement` set the end stage to finishing and nothing reset it, so
after a `/new` or `/clear` every run was refused with "The conversation is
ending; this request was not started.", a second `/new` quit Marshal without
starting a session, `/exit` and Ctrl+C quit at once with no warning and no
postmortem question, and `/resume` exited instead of resuming.
`startNewSession` now returns the end to idle before it asks the host, whether
the new session starts or fails, so a retried `/new` goes through acceptance
again.

T1b gains two names, `ResolvePendingForShutdown` and `Shutdown`, at plan `5e`.
The allowlist loses `model.go`'s `marshal/internal/postmortem`;
`commands_dispatch.go` keeps `marshal/internal/app/session` for its notes and
`model.go` keeps `marshal/internal/tools/registry` for the roster's registry.

**Plan 6 (notes, notices and commands), branch `p2-6-notes`.** Added
`internal/acpwire/notes.go`: the three roles, the fifteen content types, the six
activity kinds, the four notice categories, the two severities, `Note`,
`Notice` and `ErrorData`. Added `internal/acpwire/commands.go`: the five command
types — `CommandInfo`, `CommandListResult`, `CommandResult`, `WireDoc` and
`WireRow` — moved out of `internal/acp` with their JSON unchanged, and the three
command kinds. `CommandInfo` gains `hidden`, `tuiOnly`, `hasHandler` and
`promptBody`, `CommandResult` gains `agentGoal`, and `WireRow` gains `actionId`;
ACP leaves all five unset, reporting exactly what it reports today.
`SessionStatus` gains `Notice`, and `CodeProviderError` and `CodeAuthRequired`
document the notice their data carries.

`internal/commands/wire.go` converts a command's result for the wire:
`Command.Kind`, `commands.Info`, `commands.WireResultOf` and
`commands.WireDocOf`. The last two take a mint function that names each row
action, depth first in row order; a nil mint leaves every row without an ID.
ACP's `CommandList` builds its entries from `commands.Info`, and its `Command`
converts with a nil mint, so its JSON does not change.

`acpclient.Session` gains seven methods with their `Methods` rows: `AddNote` on
`session/note`, `SetNotice` on `session/notice_set`, `ClearNotice` on
`session/notice_clear`, `DismissNotice` on `session/notice_dismiss` and
`CommandAction` on `session/command_action`, all five new in P3, and `Commands`
on `session/command_list` and `Command` on `session/command`, which exist.
`acpclient.NoticeOf` reads the notice an `*acpwire.Error`'s data carries.

The adapter serves them over `Deps.Commands`, the runtime's registry. `AddNote`
appends a system message unless the note's role is user, and refuses any other
role; the note and notice calls do not fire `Changed`. The adapter converts
notices both ways, and mints an ID for each row action of a result it returns,
keeping the actions of the 16 most recent results that minted any; an ID it no
longer keeps returns an error. `runError` classifies a run's failure agent side:
a provider failure or a login requirement comes back as an `*acpwire.Error`
coded `provider_error` or `auth_required`, its data carrying the notice the TUI
used to build there. A cancellation and every other error pass through
unchanged.

The TUI writes its notes, sets and clears its banner, and runs its commands
through the session. Every `m.state.AddMessage` write in the TUI is now a
session `AddNote` (138 call sites at this commit), with the no-runner echo kept
as a user note; the plugins panel takes a note function instead of the state;
and the model logs through `WithLogger`.
The banner is read from `Status().Notice` and changed through `SetNotice`,
`ClearNotice` and `DismissNotice`, and `noticeForError` takes the notice off the
error. `docpanel` renders an `acpwire.WireDoc` and runs a row's action ID through
the act it is given: a command doc acts through `CommandAction` on the session
it came from, while the `/run` outcome doc carries local actions and the browse
help is a wire doc with none. `WithCommandRegistry`, `docpanel.ActionMsg`,
`SetSource` and `browserStripText` are deleted. `transcript.go` and
`viewport_render.go` compare the wire's role, content-type and activity
constants, and `activeSpinnerFrame` takes a string.

T1b gains six names at plan `6`: `AddMessage` and `Logger` with the notes, and
`Notice`, `SetNotice`, `ClearNotice` and `DismissNotice` with the banner. The
allowlist loses 18 entries across 17 files, ten keys entirely.

**Plan 7a (providers and sign-ins), branch `p2-7a-providers`.** Added
`internal/acpwire/providers.go`: `ProviderTemplate`, the provider catalog's
twelve fields on the wire, and `ModelCapabilities`, the three fields a per-model
probe reports beyond the model list. `internal/llm/schema` gained
`thinking.go`: `ResolveThinkingOptions`, `CloneThinkingOptions` and
`PreserveThinkingOptions` moved out of `provider` with their bodies unchanged,
so the TUI's options stay a pure computation. `strutil.UniqueName` moved out of
`provider/templates.go` with its body unchanged.

`acpclient.Host` gains ten methods with their `Methods` rows: `ProviderTemplates`
on `config/provider_templates`, `ProbeProvider` on `config/probe_provider`, which
exists, `CanProbeModel` and `ProbeModel` on `config/probe_model`, `ListModels`,
`SaveModels`, `ForgetModels` and `RenameModels` on `config/models_list`,
`..._save`, `..._forget` and `..._rename`, and `PrepareProviderLogin` and
`LoginProvider` on `config/login_provider`. `acpclient.Session` gains
`PrepareMCPLogin` and `LoginMCP`, both on `session/mcp_login`. Eleven of the
twelve ride eight new ACP methods; only `ProbeProvider` reuses one that exists.

The adapter serves them over `Deps.Auth`, the runtime's resolver or
`provider.DefaultAuthResolver()` when nil, and `Deps.ModelCacheDir`, the data
directory the TUI passed to the cache. A probe builds its client here, with the
current session's thinking budget margin, so probing a config that is not saved
yet — the wizard's typed key, a provider being edited — still works.
`Host.AuthResolver` exposes the selected resolver for app's wiring test and goes
with the adapter in P3. The cache is four explicit calls keyed agent side: the
adapter looks the provider's config up itself, because the entry's hash covers
the literal key the TUI's config loses in plan 7b. Without a cache directory all
four do nothing and `ListModels` finds nothing. A sign-in is a check followed by
a call: the check returns today's sentence when the sign-in cannot start, and the
call takes a `show` function the adapter invokes once with the authorization URL.
The engine's `Open` stays nil agent side; the TUI opens the browser and then
records the URL, the engine's order. A cancelled sign-in still wraps
`context.Canceled` and stores nothing.

The TUI's probes, model cache, catalog and both sign-ins go through the seam.
`probe.Provider` takes the host; `presetflow.ProbeCapabilitiesCmdWithCancel`
takes it with the provider's name and config; the connect wizard reads the
catalog once from `ProviderTemplates` and looks templates up by ID; the settings
browser's confirm screen asks `CanProbeModel` before showing "detecting…". The
wizard and the settings browser keep their in-session discovery maps and their
newest-attempt guards, so a stale probe moves neither the map nor the disk. The
wizard's sign-in runs `PrepareProviderLogin` and then `LoginProvider`, and
`/mcp auth` runs `PrepareMCPLogin` and then `LoginMCP`; the MCP reconnect after a
successful sign-in stays in the TUI until plan 7b.

Deleted: the model's `WithAuthResolver`, `AuthResolver`, `auth` field, unused
`dataDir` and `WithDataDir`; the settings browser's `WithAuthResolver`,
`WithDataDir`, `AuthResolver`, `probeProviderFor` and dead `badgeForTemplate`,
and the settings state's `auth` and `dataDir`; the wizard's `Opts.Auth`, `auth`
field and `AuthResolver`; `probe.ProviderWithAuth`, the default-resolver form and
`presetflow.ProbeCapabilitiesCmd`; and the two engine builders `mcpOAuthEngine`
and `codexOAuthEngine`. `app.Run` now hands the runtime's resolver to the adapter
alone. T1 stays green throughout and T1b gained no names, because no `State` read
moved. T1's allowlist lost 14 entries across 9 files, five keys entirely.

**Plan 7b (config through the seam), branch `p2-7b-config`.** Added
`internal/acpwire/config.go`: `PresetSetup`, the preset setup's three counts plus
the models.dev provenance sidecar as opaque JSON with the sorted names it
manages. `acpwire.SessionStatus` gains `UserConfigPath` and `ProjectConfigPath`,
the two files a config write changes.

`acpclient.Host` gains six methods with their `Methods` rows: `SaveUserValue` on
`config/set_value`, `SaveUserProviders` on `config/save_providers`,
`SaveUserPresets` on `config/save_presets`, `SaveProviderKey` on
`config/set_provider_key`, which exists, `PreparePresets` on
`config/prepare_presets` and `SaveProvenance` on `config/save_provenance`.
`acpclient.Session` gains seven: `Config` and `Layers`, both on `session/config`,
`ApplyConfig` on `session/apply_config`, `ReloadConfig` on
`session/reload_config`, `ReconnectMCP` on `session/mcp_reconnect`,
`SaveProjectConfig` on `session/save_project_config` and `SaveVerifyCommands` on
`session/save_verify_commands`. Twelve of the thirteen ride eleven new ACP
methods; only `SaveProviderKey` reuses one that exists.

`inproc.Deps` gains `ReloadConfig`, `ReloadLayers`, `TrustRefresh`, `HomeDir` and
`UserConfigPath`, and `tui.Model` gains `Session()`, which app's wiring test
drives the seam through. A write does not reload — a connect makes three writes
before one reload — but every successful write refreshes the session's layers
through `ReloadLayers`, keeping the old snapshot when the read fails, and a
project save also advances the permanent-trust hash through `TrustRefresh`
first, the order `persistAndReload` used.

The TUI's config reads, writes and reloads now go through the seam, apart from
the skills panel's, which plan 8 moves. Reads are `Session.Config` and
`Session.Layers`, one call per function; writes are the six host calls and the
two session calls, each keeping the TUI's own order of saves, notes and its busy
check; reloads are `ApplyConfig`, `ReloadConfig` and `ReconnectMCP`, whose false
report replaces the TUI's nil-reloader checks. Preset setup runs agent side: the
wizard keeps its capability probe and hands the folded models to
`PreparePresets`, which copies the preset map, loads the catalog only when
consent allows, reads the sidecar and runs `presetsetup.Apply`; the provenance
comes back as raw JSON that the TUI only stores, through `SaveProvenance`. The
data directory stays where it was: `PreparePresets` reads `Deps.ModelCacheDir`
as given, and `SaveProvenance` falls back to `config.DataDir` of the home.

The bridge now reads frames up to 64 MiB, since a long session's
`session/stack` result or first `stack_patch` passes 2 MiB, and a larger frame
fails every pending request with `bridge: read child output: …` and kills the
generation, so `supervise` respawns it instead of the connection wedging.

Deleted: the model's `WithConfigReloader`, `WithConfigLayers`,
`WithLayerReloader`, `WithTrustRefresh`, `WithModelCache`, `ConfigReloader`,
`configReloader`, `configLayers`, `layerReloader`, `trustRefresh`,
`modelCacheDir`, `reloadLayers`, `adoptLayers` and `projectConfigPath`;
`connect.Opts.DataDir` and the wizard's `dataDir` field; the settings browser's
`cfgPath`, `layers`, `SetLayers` and `Layers`, the `NewBrowser` path parameter
and its `os.UserHomeDir` fallback; and the roster's `cfgPath`, `layers`,
`SetLayers` and `Layers`, replaced by the session its constructors now take.
`Run` stops passing all five removed options, so the adapter alone holds the
reload path, the layer reloader and the trust refresh.

T1 stays green throughout. T1b gained three names at plan `7b` — `Layers`,
`SetLayers` and `RebaseInteractionConfig` — because the model reads the layers
through the session and the adapter is what refreshes them. T1's allowlist lost
21 entries across 6 files, four keys entirely (`connect/connect.go`,
`doctor_secret.go`, `model_options.go` and `settings/browser.go`).

**Plan 7d (secrets), branch `p2-7d-secrets`.** `internal/strutil` gains `MaskKey`, moved
from `listpanel`, which keeps the name as a variable bound to it, and `IsMasked`: true for
a value that begins with the mask's bullets, which `MaskKey` returns unchanged, so masking
a mask is stable. No real secret begins with them.

`internal/app/config/mask.go` gains the pair each side of the seam needs. `RedactSecrets`
and `RedactLayers` replace each provider's API key and the web search key with
`strutil.MaskKey`'s output and the desktop CDP URL with its userinfo masked, leaving an
empty secret empty. `RestoreSecrets` and `RestoreProviderKey` put the stored secret back:
the same path in the session's live config first, then in the last-loaded layers' merged
config, then, for a provider the call names as renamed, its old name, then the one stored
key whose mask it is. A literal or empty value is kept, so a typed key is written as given
and a cleared one stays cleared. A mask matching nothing fails with
`config: <path>: no stored secret matches its mask`, naming the secret's TOML path; a
probe drops the key instead and runs unkeyed, as an unconfigured provider would.

`Session.Config` and `Session.Layers` mask what they return (C3), so no config read through
the seam carries a literal secret. Every config the TUI hands back is restored before it is
used: `ApplyConfig` before installing it, `ReloadConfig` before the rebuild,
`SaveProjectConfig` before the diff, `SaveUserValue` when its path is a secret's,
`SaveUserProviders` for every provider, and `Host.client` for a probe. Each reads the
stored configs through `stored`, over `storedConfigs`, which takes the state's live config
and then the layers' merged config.

`SaveUserProviders` gains `renamed`, mapping a provider's name in the write to its name in
the stored configs, so a renamed provider's key follows it. The settings browser records
each rename it makes as new name → original, collapsing chains and dropping a rename back
to the original, and clears the map once the user-config flush succeeds — after both the
providers and presets writes — so a retry does not resend it. `config/save_providers`
carries the map in P3. A mask reaching `SaveProviderKey` (the doctor's key write) is
rejected with the same error rather than persisted, and a stored value that is itself a
mask is never restored as though it were the secret.

T7 is `TestConfigReadsCarryNoSecret`: both reads, JSON-encoded, hold neither a key nor a
password but do hold the mask. The restore is pinned per call in
`internal/acpclient/inproc/secrets_test.go`.

Accepted differences: a rename the browser did not make, such as one through `/set`,
reaches the agent without a name map, so two stored keys sharing their last four characters
fail the write instead of picking one. Follow-up: an MCP server's `env` and `headers` can
hold tokens and still travel in the clear, since they are outside `config.MaskSecrets`' set
and C3's. No ACP method changes name.

