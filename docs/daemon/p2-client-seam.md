# P2 — TUI client seam

Parent: [`design.md`](design.md) §4.5. This spec replaces §4.5's sketch
where they differ, and settles §8's transcript-shape question.
Depends on: P1 (merged at `ee15d2ed`). Agent side and TUI only: no bridge
change, no new ACP method, no database schema change. ACP messages that
already exist change only by gaining optional fields (C6).
Status: drafted 2026-10-20, awaiting review. Delivered as nine plans
(Migration order), each written against `main` once the previous one has
landed.

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

- **`tui.New` takes the session state** (`internal/app/tui/model.go:1702`).
  Non-test TUI code calls 97 distinct `session.State` methods and reads
  the `Config` (about 90 sites), `WorkingDir` (30) and `StartedAt`
  fields. `sessionsheet`, the plugins, skills, doctor and doc panels hold
  the state too.
- **About 36 other agent-side dependencies arrive as options.** `Run`
  passes 40 `tui.With*` options (`internal/app/app.go:2676-2899`). The
  client-local ones are the clipboard, the working directory for display
  and the open-connect flag. The rest are agent side:
  - the agent, swarm and pipeline runners and their factories, plus the
    plan-author, custom-agent, subagent, review and suggestion factories;
  - five event brokers (job, watch, steering, workspace, subagent) and
    the work service;
  - the database, the tool registry, the command registry, the skill
    index and the file index;
  - config layers, the reloaders, the auth resolver, the model cache and
    the trust callbacks;
  - the session swapper, the end controller and the reference finder.
- **The TUI polls.** It re-reads the state on a 150ms tick
  (`model.go:4786`) and on every broker event, and `refreshViewport`
  rebuilds the transcript from it (`viewport_render.go:53`).
- **The TUI applies policy itself.**
  - "Always allow" writes a permission rule to the user config, applies
    the new config and resets the runner's policy rules
    (`model.go:3447`).
  - The settings, connect, presets and agents panels write config files
    with `config.Save*`.
  - Approvals, questions, skill gates and sudo prompts are read with
    `Pending*` and answered with `Resolve*` on the owning state, the
    subagent's child state included.
  - The TUI also installs plugins (`git clone`), writes SDD starter
    plans and postmortems, and reads git state in the project
    (`tui/gitinfo`, `tui/changedfiles`). `internal/acp` already imports
    both of those packages.
- **The TUI writes into the transcript.** 137 of its 139 `AddMessage`
  calls append a system message, which is persisted with the session.
  The other two add the user's prompt.
- **Some seams already exist:** `AgentRunner`, `SessionSwapper`,
  `EndController` and `viewmodel.WorkService` are interfaces, and the
  work service already has ACP counterparts (`session/work_*`).
- **ACP already has 69 methods**, including `session/prompt`, `steer`,
  `set_mode`, `set_effort`, `command`, `stack`, `stack_node`, `work_*`,
  `swarm_*`, `sdd_*`, `memory_*`, `skills_*`, `plugins_*`, `list`,
  `resume` and `config/*`. It cannot be imported by the TUI: `acp`
  imports `app`, which imports `tui`.
- **TUI tests build state directly:** 214 `session.New` calls and 432
  `New(` calls across 34 test files.

## Transcript spike (design §8)

The question was whether the `WireTree` projection drops anything the
TUI renders. It does, and every gap can be closed by adding a field.

- **The TUI renders full records.** `viewmodel.Node` carries
  `*session.TranscriptItem`, `[]registry.AuditEvent` and the subagent's
  child `*State`. The renderer reads them directly
  (`transcript.go:565`, `steps.go:100`).
- **Collapsed rows read fields the projection leaves out:**
  - a message's reasoning;
  - the subagent's ID and fallback flag;
  - the running subagent card's latest child activity line;
  - the hook indicator, the sandbox's kill reason and the call's step ID;
  - the active call's step ID;
  - the tool row head. The TUI builds it from raw arguments: the shell
    subject with a leading `cd` into the active root removed, the symbol
    or search subject, and diff stats. `WireCall.Args` is capped at
    `WireTextCap` (4096 bytes, `viewmodel/wire.go:21`), which cuts the
    JSON of a large edit.
  - the diff preview: per-file `+N −M` stats and the first 12 lines of
    up to 6 files. These come from the full unified diff, and
    `WireCall.Output` keeps only its first 4096 bytes.
- **Expanded rows are covered.** `session/stack_node`
  (`acp/stacknode.go:22`) returns uncapped arguments, original arguments,
  output, diff, error, hooks, sandbox metadata, symbols and the finish
  reason.
- **The cost** is porting about 3k lines of renderer (`transcript.go`,
  `steps.go`, `toolgroup.go`, `symbolrow.go`, `receipt.go`, the browse
  files and `viewport_render.go`) and the tests that build session
  records to render.

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
    `llm/schema`, `llm/catalog`, `app/clipboard`, `strutil`,
    `diffview`, `jsonextract` and `progress`.

  From `app/config`, only types and pure functions may be used. Its
  functions that touch or locate files are denied by name: every
  `Save*` and `Load*` function, plus `UserDir`, `DataDir`,
  `UserConfigPath` and `ProjectConfigPath`. A write call returns the
  path it wrote, for the TUI's "persisted · <path>" notes. Pure helpers
  such as `LookupPath`, `Diagnose` and `Default` stay allowed. Every other
  `marshal/internal/...` package is agent side. `internal/viewmodel`
  is agent side too. Any pure text helper the TUI still needs from it
  moves to an allowed package.
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

- `inproc.New(inproc.Deps) (acpclient.Host, acpclient.Session)`. `Deps`
  holds the state and everything `Run` passes as an agent-side TUI
  option today. `Run` builds `Deps` instead of those options.
- It must not import `internal/app`. TUI tests import it, and `app`
  imports `tui`.
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
  everything the status line, now bar, footer, input gating and turn
  logic read today:
  - **identity:** session ID, working directory, start time, generation
    sequence, leaf ID and branch count;
  - **route:** the active route, the turn route and whether it is
    running, turn usage and window, quota, and effort override;
  - **access:** mode, system access, sudo unlock, trusted, and whether
    there is a backup;
  - **activity:** kind, label, start, the pinned spinner label, and
    whether a turn or a subagent is running;
  - **workspace:** project root, active root, and git info (branch,
    worktree, HEAD);
  - **live state:** browser info, sandbox info and the current notice;
  - **queues and runs:** the steering queue and Send-now snapshot,
    todos, jobs (the running count and the job list), the watch
    snapshot, subagent views without the child state, swarm and SDD
    progress, the SDD gate, active skills, loaded tool count, tool
    budget, session rules and a pending watch resume.
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
  `session/sheet`.

### C6. Transcript

- `Session.Stack(acpwire.StackQuery{SubagentID, LiveWork})
  acpwire.StackSnapshot` returns the projected tree with the band node.
  `LiveWork` selects the projection the TUI uses today. ACP's
  `session/stack` gains the same optional parameter, and leaving it out
  keeps today's projection.
- `Session.StackNode(ctx, nodeID, subagentID)` returns
  `acpwire.NodeDetail`, the uncapped detail behind a row. ACP:
  `session/stack_node` (exists).
- **One projector.** Build, project and band move from
  `acp/stack.go:112-213` into `internal/viewmodel`, and both `acp` and
  the adapter call it. ACP keeps its patch diffing.
- **`WireNode` gains `version`**, the hex of `Node.Version`, so a client
  caches rendered rows without hashing them.
- **Additive fields.** The projection carries every field a collapsed row
  reads, computed by the helpers the renderer uses now:
  - `WireMessage.reasoning`, capped;
  - `WireSubagent` gains `id`, `fallback` and `tail`, the running
    child's latest activity line;
  - `WireCall` gains `head`, `hookHint`, `killedReason`, `stepId` and
    `diffFiles`. `head` is today's `toolRowHead`, built server-side
    against the active root. Each `diffFiles` entry carries the path,
    added and removed counts, and the first 12 diff lines, for at most
    6 files, with the full count;
  - `WireRunning` gains `stepId` and the command line;
  - `WireStep` gains `narration`, the step's trimmed narration texts in
    order. The renderer joins them as one markdown document, and
    `headline` (emphasis stripped, cut at the first sentence) cannot
    rebuild them.

  The plan fixes the JSON names. Anything only an expanded row needs
  comes from `StackNode`.
- **The renderer moves from `viewmodel.Node` to `acpwire.WireNode`.**
  Node identity becomes the wire ID and kind. Density, folding,
  selection, browse and inspect work on wire nodes.
- **Callers.** The callers line uses
  `Session.Callers(ctx, nodeID) ([]string, error)`, cached per node as
  today. ACP: `session/callers` (new in P3).
- **Subagent drill-in** reads `Stack` with the subagent's ID.

### C7. Agent requests

- **`Session.Pending() acpwire.PendingRequests`** lists every open
  request in the session tree, subagents included.
  - Request kinds: approval, question, skill gate, sudo, mode access and
    the SDD human gate.
  - Each request carries:
    - an opaque key that stays stable while the request is open;
    - its owner: subagent ID and label, or 0 for the session itself;
    - the fields the TUI displays today.
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
  - `Steer`, `SendNow`, `PopSteering` and `ClearSteering` cover the
    steering queue (`session/steer` exists; the rest are new);
  - `SetMode` and `SetEffort` (exist), and `SetSystemAccess` and
    `SetSudoUnlock` (new);
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
  serves, as the TUI's `applyNewConfig` (`model.go:1312`), the config
  reloader and `RebaseSessionConfig` do today. It returns the path
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
  - Run-outcome diffs and file reads: `session/diff` and `session/file`
    (exist).
  - Fleet rows are in `Sheet`.
- **Git info and changed files** arrive through `Status` and `Sheet`.

### C12. Client-local

These stay in the TUI:
- terminal I/O, rendering and the theme;
- key handling, the input box, density, folding and selection, and the
  clipboard (P0);
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
| `internal/acp` | Uses `acpwire` types and the shared projector. `session/stack` takes `liveWork`. |
| `internal/repo/gitinfo`, `internal/repo/changedfiles` | Moved from `internal/app/tui/`. |
| `internal/app/app.go` | `Run` builds `inproc.Deps` and calls `tui.New(host, session, …)`. The agent-side `tui.With*` options are deleted as their groups move. |
| `internal/app/tui/...` | Every group moves onto `Host`/`Session`. The renderer moves to wire nodes. Test setup goes through shared helpers that build an adapter over a test state, so P3 changes only the helpers. |

## Migration order

The TUI works after every plan.

1. **Foundation.**
   - `acpwire` and `acpclient`, an empty `inproc`, and
     `tui.New(host, session, …)` with `WithLegacyState`.
   - The test helpers, and every test moved onto them.
   - The gitinfo/changedfiles move.
   - The boundary test (T1), with an allowlist of every current
     violation by file and import.
   - The ACP mapping test (T4).
2. **Status and events** (C5).
3. **Transcript** (C6). This is the largest and riskiest plan, so it
   comes early.
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
  every node kind, collapsed and expanded, plus diffs, hooks, a killed
  sandbox, jobs, subagent cards and a band. The ported renderer must
  match the goldens byte for byte.
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
- A manual run behaves as before. It covers a turn with an approval, a
  question and a sudo prompt, a `/settings` change, `/connect`, `/new`,
  resume, a subagent drill-in, a swarm or SDD run, and exit with a
  postmortem.

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
