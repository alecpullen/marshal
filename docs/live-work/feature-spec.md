# Live work: pinned gutter cards and structured progress

Status: written specification approved; implementation plan prepared for review.
Date: 2026-10-18.
Source baseline: `320c7a8f2cac87af57499086a63f1f6ee22fa1be`.
Implementation: not started.
Workflow: [Marshal brainstorming](../../internal/skills/builtin/brainstorming.md); [implementation plan](plans/2026-10-18-live-work-plan.md) authored with Marshal's internal plan-writing skill.

## Context and approved decisions

The original testing observations requested subagent boxes pinned below the transcript, consistent treatment of background commands, progress cards for downloads/transfers and finished jobs/watchers that leave the live UI. The user selected **gutter cards**, then approved examples for plain commands, structured test suites, downloads, LAN transfers, custom work and mixed work.

The user also selected automatic retirement of successful work after a brief notice, with failures leaving a compact attention indicator until reviewed or dismissed. Results remain accessible after their cards retire.

The approved approach uses one card system. The agent supplies titles, phases, measurements and detail rows. Marshal owns layout, height, navigation, actions and lifecycle. Running commands supply progress while the agent is waiting; updating a card must not depend on additional model turns.

The original pinned-area work is a bounded UI change. The complete approved scope is an architectural extension because it adds a command-bound progress observation protocol shared by execution, projection, persistence and clients. It does not add another process supervisor.

Related contracts:

- [Architecture](../ARCHITECTURE.md): rendering-only TUI, shared viewmodel, provider flexibility and stable prompt prefix.
- [Reliable background execution](../background-execution/feature-spec.md): conversation-owned execution service, wait-only timeouts, stable execution identity and explicit stops.
- [System-access approval](../system-access-approval/feature-spec.md): canonical prompt settlement and grants applied outside transport rendering.
- [Send now](../send-now/feature-spec.md): operation interruption, surviving detached work, truthful stop outcomes and temporary composer ownership.
- [Tasks and navigation](../single-stack/p3-tasks-and-navigation.md): existing todo progress, transcript navigation and Tasks panel.

Background execution and access approval are merged at this baseline. Send now has been submitted separately; this spec consumes its behavior without claiming that its production APIs are already merged.

## Current implementation evidence

- `internal/app/tui/view.go` already places a pinned now bar between the transcript and input. `nowbar.go` plans its height, collapses it below 30 terminal rows and combines todos, agents, browser state, jobs and watches. Extend this surface.
- `internal/viewmodel/band.go` already projects todo progress and running entities. Its current entity fields are insufficient for common lifecycle, actions and measured progress.
- `internal/viewmodel/stack.go` places subagent cards inside their spawning beat and suppresses their dispatch rows in some parent views. `internal/app/tui/steps.go` renders those cards. Dispatch and live presentation need distinct ownership.
- `internal/app/tui/jobs.go` correctly filters jobs to detached, nonterminal executions, including stopping work. Its watch path returns the entire cached list. `handleWatchMsg` replaces/appends events without authoritative removal.
- `internal/watch/watch.go` auto-removes once watches after firing. Repeat fires remain active; sampling errors can recover. Its current event does not independently express membership/removal.
- `internal/tools/native/jobs_manager.go` owns launch, attachment, output, stopping and terminal results. `jobSink` continues retaining output after detachment but stops forwarding to the foreground observer. Progress observation must sit before this display-only attachment gate.
- `internal/app/session/jobexit.go` and `internal/db/jobexits.go` already persist command results and stable execution provenance as JSON in `session_state`. Keep that evidence.
- `internal/app/tui/todos.go` implements Ctrl+T as a todo-only Tasks panel. Ctrl+B already opens the session sheet; Ctrl+F drills into an agent. Do not silently repurpose those shortcuts.
- `internal/acp/stack.go` projects the existing band; `internal/acp/watch.go` exposes watch lists/events. Add shared data through JSON contracts, retaining those existing clients.

Historical postmortem analysis supports long waits and repeated work but did not independently establish the card-retention defect. The five reports added since that analysis contain no new direct observation of pinned-card behavior. The concrete UI and watch findings above remain source evidence.

## Scope and delivery stages

All three stages belong to this feature; execution may deliver them sequentially.

1. **Shared live-work UI:** canonical work snapshots, pinned gutter cards, chronological dispatch/result access, navigation, failure attention and watch reconciliation. Plain command cards work without structured progress.
2. **Progress transport and custom cards:** opt-in command metadata, a bounded structured-event decoder, attachment-independent observation, immutable progress snapshots and final-progress retention.
3. **Reusable profiles:** a Go JSON test adapter and test/download/transfer/custom presentation profiles, all using the same progress values and lifecycle.

Included work kinds are independently running subagents, detached managed commands and watches. Command profiles do not create additional tasks: a test suite or custom download card is a view of its existing execution.

Existing todo, SDD/swarm, browser and parent-turn summaries keep their current roles within the shared row budget. Pipeline role cards are not independently running subagents and do not become duplicate live-work entries. This feature does not redesign workflow-controller progress or steer those controllers.

## Runtime ownership and identities

The command service owns commands. Session/subagent state owns agents. The watch manager owns watch membership and sampling. A runtime projection coordinator consumes their snapshots/events; it cannot launch, extend, terminate or resurrect work.

Every item has a canonical key scoped to its owning conversation:

| Kind | Identity |
| --- | --- |
| Command | Full managed `ExecutionID`, preserving runtime generation and origin; never display `job-N` alone. |
| Subagent | Conversation plus the registration's stable agent identity/generation; never provider call ID alone. |
| Watch | Conversation/runtime generation plus watch registration ID. A reused name is a different watch. |

Preserve parent/child owner, originating turn/step/call, start/end timestamps, attachment and available result references. Viewing a child does not change ownership or widen an action's target.

An agent and a command it owns are distinct entities. Each execution appears once; owner metadata does not create another copy of the same command or count its progress again inside a synthetic aggregate task.

Attached foreground commands remain their active tool rows. Their progress is inspectable there. On detachment, the same execution appears in the pinned region without a relaunch, new ID or second progress producer. No detached command remains a second live box at its dispatch location.

All ordinary command-capable paths may supply the same captured progress metadata. Initial agent-authored configuration is exposed on `shell.run` and `test.run`. Privileged password entry is not a progress source, and this work does not add background sudo.

### Canonical work snapshot

Define one immutable projection shared by TUI and ACP. Its logical fields are:

- Snapshot: schema version `1`, conversation ID, runtime epoch, monotonically increasing revision, observation time, complete active membership, at most 32 recent terminal item details, total attention count and current receipt counts. Older results are paged from history, not resent with every progress update.
- Item: canonical key, display ID, kind, label, owner, origin references, lifecycle, optional outcome/exit code, attachment, start/end times, per-item source revision, stable terminal-result revision and optional progress snapshot.
- Presentation facts: blocked-request kind/reference, latest activity, bounded output tail, retained-result reference, reported-progress warning and supported runtime actions.
- Attention: none, transient warning or terminal result needing acknowledgment. Acknowledgment targets a canonical key plus its terminal-result revision.

Terminal-result revisions are stored with their results and remain stable across rebroadcasts/resume; they are not the live snapshot revision. For legacy evidence, derive a stable revision from the retained identity and terminal facts. Select the at-most-32 terminal details by descending end time and canonical key. Receipt/attention totals include retained results outside that detail window.

Lifecycle is `running`, `blocked`, `watching`, `stopping`, `completed`, `failed`, `stopped` or `unknown`. Preserve finer native outcomes separately. A live watch's recoverable sampling error is a warning, not a terminal lifecycle.

Map success only from an actual successful agent outcome, a command completed with an observed zero exit, or a once watch whose condition fired successfully. Missing exit/outcome evidence is never success. A stopped receipt requires an accepted explicit stop and matching source settlement; an unexplained signal/vanished owner remains failed/unknown evidence.

Capability fields describe available actions; they never contain executable callbacks, shell text to run, permission grants or agent-supplied key bindings.

Source revisions and membership are authoritative. Subscribe and seed from a source snapshot with a revision boundary, then apply only newer events from the same runtime epoch. Seed finished history from existing persisted source records. Reconcile again after reconnect/reload or a detected gap. Publish copied snapshots with complete active membership; a lost intermediate update must not leave a permanent ghost row. A retained item from an earlier runtime epoch is read-only history and cannot receive actions against the current runtime.

Collect source data and publish outside source/session locks. The projection does not hold its mutex while querying managers, persisting data or invoking actions. Old-conversation callbacks cannot update the replacement conversation.

Dependency-neutral progress value types/validation belong below registry, session and viewmodel, in `internal/progress`. They must not import those consumers or own workers/processes. Work projection types belong in viewmodel; app wiring supplies source snapshots and action facades. The TUI never imports `internal/agent`.

## Pinned gutter-card presentation

The pinned region stays below the transcript and above the editor/dock according to the existing layout host. Scrolling the transcript does not scroll live cards out of view. Gutter, type, glyph and status labels use existing semantic theme slots and flat-background behavior.

A collapsed card uses two text rows: identity/status/elapsed, then latest useful activity or a compact metric summary. An explicitly expanded card uses at most six rows: header, phase/measurement summary, optional bar, detail/checklist, freshness and runtime actions as space permits. Full metrics/output remain in the inspector.

Examples below are illustrative values, not promises that a total is available:

```text
▍ ▸ Review changes                         agent · running  2m14s
▍   Reading runner.go · checking cancellation

▍ ▾ Background test suite                     tests · running
▍   12/18 packages complete · 184 passed · 0 failed
▍   ████████████░░░░░░  67% of declared packages
▍   Now: internal/agent · TestSendNowPromptPairing
▍   Details / output    Request stop

▍ ▾ Download local model                   download · running
▍   6.8 / 12.0 GiB · 72 MiB/s · ~1m14s remaining
▍   ██████████░░░░░░░░  57% bytes received
▍   model.gguf → ./models/model.gguf
▍   Details / output    Request stop

▍ ▾ Copy dataset to LAN server             transfer · running
▍   3.7 / 8.0 GiB · 92 MiB/s · ~48s remaining
▍   ████████░░░░░░░░░░  46% bytes transferred
▍   142/216 files · workstation → media-box.local
▍   Details / output    Request stop

▍ ▾ Prepare model bundle                     custom · running
▍   Phase 2/4: verify checksums · 1,280/2,048 files
▍   ███████████░░░░░░░  63% files verified in this phase
▍   ✓ Extract archive  ⠋ Verify files  · Build index  · Package
▍   Current: weights/layer-028.bin
▍   Details / output    Request stop
```

Plain command fallback shows the command, elapsed time and useful output tail. It never infers test counts, throughput, percentages or success from arbitrary prose.

### Height, ordering and overflow

- Retain the existing minimum size gate at 80×24. Below 30 terminal rows, the pinned region collapses to one summary row with active/attention counts and access to the Work list.
- At taller sizes the total pinned budget is `min(10, floor(terminalHeight/4))` rows, reduced further by required editor/dock/prompt chrome while preserving the existing four-row transcript minimum. This budget includes todo/workflow/browser summaries, receipts, attention and overflow.
- Default cards are collapsed. At most one is expanded; selection/expansion is local UI state. While one is expanded, other visible cards become single-row summaries. Reduce detail before hiding identity/status.
- Reserve an attention summary row when required. Keep the explicitly selected live card visible when possible, then blocked work, then remaining active items ordered by start time and canonical key. Do not reorder peers on every output update.
- A final `+ N more · Work` row accounts for every hidden work item. Complete records remain reachable in the Work list. Row counts and click regions derive from the same layout plan.
- Retiring cards do not reset scroll position or automatically move focus. A shrinking focused row transfers focus to a stable neighbor or its retained result, never to a hidden stop action.

At 80×24 the Work list/inspector supplies rich details. At 120×40 and 200×60 cards may expand within the fixed budget; width/height never produce an unbounded dashboard.

## Navigation, results and actions

Extend the Ctrl+T Tasks surface with **Plan** and **Work** views. Preserve the viewed session's todo content and statistics in Plan. Work is explicitly scoped to the owning parent conversation, including its independent child-owned executions.

Ctrl+T opens the last selected view; on first use select Plan if it has todos, otherwise Work. A second Ctrl+T closes the Tasks surface. Add a TUI-only `/work` command that opens Work directly. Ctrl+B, Ctrl+F, transcript browse mode and the existing one-panel dock retain their meanings.

Within Tasks, Left/Right select Plan/Work when its list owns input; Up/Down or j/k select a row; Enter opens detail; Space expands/collapses the selected live card; Esc returns/closes through existing navigation. Search/filter input owns its typing keys normally. Mouse clicks offer the same selection/detail paths. Contextual footers list only applicable actions.

Work lists active items, results needing attention and finished history, searchable by label, kind and owner. Attention/history queries use pages of 50, at most 100, with a revision-bound cursor; changed ordering invalidates the cursor and requests a refresh instead of skipping/duplicating rows. They run outside the UI handler and do not join the high-frequency live snapshot. Work remains available with no active jobs and no todos. Overflow and the compact attention indicator open the corresponding Work view.

Dispatch stays chronological:

- Show the actual `agent.run` call and returned handle once. Remove its separate live transcript box and the heuristic that hides the real dispatch row.
- Preserve subagent reports/results; the dispatch row or Work history opens the child transcript/final report even after retirement.
- Show a command's dispatch/result row once, settled with its real outcome and linked inspector/output. Do not append a second detailed progress transcript every tick.
- A watch's final/fired report and retained status remain inspectable after its pinned presence ends.

Inspection is read-only. Explicit **Mark reviewed** or **Dismiss** acknowledges terminal attention; merely opening a panel does not acknowledge unseen failures. Acknowledgment never deletes results, changes exit codes or clears a newer result revision.

Runtime supplies actions by exact canonical target:

- Command stop calls the existing execution service's targeted stop.
- Agent stop uses the existing explicit child-stop contract. Display its actual target/scope; do not silently turn it into a conversation-wide stop.
- Watch stop removes/stops that registration through its manager.
- Resolve approval/question opens the existing canonical prompt flow. It grants nothing.

Stop runs asynchronously outside TUI handlers. Accepted stop shows `stopping` until the backend settles; it is not completion. Repeated requests are idempotent. A stale/missing target returns an explicit result and does not retarget by name/display ID. No stop occurs because a card retires, loses focus, stops receiving progress or reaches 100%.

Send now, parent stop, targeted stop and exit remain distinct runtime operations. The temporary steering composer keeps keyboard ownership; work updates continue without replacing it or answering a suspended prompt.

## Retirement, warnings and watch membership

Display retention is independent from service retention:

| Event | Pinned behavior |
| --- | --- |
| Actual successful completion | Coalesce into a concise success receipt; retire at terminal timestamp + 5 seconds. |
| Explicitly stopped work | Neutral stopped receipt with the actual cause; retire after the same five seconds. |
| Actual failed/startup-failed/lost or unknown terminal result | Collapse the full card to compact attention until its exact result is acknowledged. |
| Reported test/checklist error with exit 0 | Preserve completed process lifecycle, identify the discrepancy and require result review. Do not rewrite the exit code. |
| No recent progress event | Keep actual lifecycle; show last-update age and suppress stale rate/ETA. |
| Live repeat-watch fire | Update fire count/latest sample; keep watching. |
| Recoverable watch sampling error | Keep the watch live with a warning; clear that warning on recovery. |
| Once-watch fire, explicit removal or final watch failure | Remove active membership and apply the appropriate success/stopped/failure receipt. |

Use one aggregate attention row for multiple results, not one permanently pinned failure box per result. Older successes never replay their five-second receipt on opening Work, reload or resume. Dismissal may hide a terminal notice early; active work can only collapse, not be hidden as finished.

Watch updates gain explicit liveness, source revision and terminal/removal reason; a fired event is not sufficient evidence of removal. Final removal and retained result are committed before publication. Authoritative membership reconciliation removes stale cache entries after dropped events. Transient error and repeat-fire events cannot remove an active watch.

Legacy watches whose liveness cannot be recovered are shown as retained/unknown history, not manufactured as currently watching. No old task is resurrected just because its last persisted status was running.

## Command-bound progress configuration

Add optional `progress` to `shell.run` and `test.run`. Other ordinary command entry points may forward the same captured metadata internally. Omission preserves existing launch/result behavior and the generic command card.

The complete initial configuration shape is:

```json
{
  "command": "python prepare_bundle.py --progress-events",
  "background": true,
  "progress": {
    "profile": "custom",
    "source": "events-v1",
    "title": "Prepare model bundle"
  }
}
```

`test.run` retains its existing arguments and accepts the same optional progress object; the example's `background` belongs to `shell.run`.

Profiles are `generic`, `tests`, `download`, `transfer` and `custom`. Sources are `none`, `go-test-json` and `events-v1`. Default profile/source is generic/none. `go-test-json` requires the tests profile; `events-v1` supports the four rich profiles. A nonempty title is optional and bounded to 80 Unicode characters. Invalid configuration is rejected before launch.

Source none is valid with any profile and uses the plain-command fallback for that execution; it never infers fields from the profile name. Keep the model tool schema to profile/source/title. The event schema belongs in documentation/examples, not a dynamically rewritten tool definition or a large per-turn prompt.

Configuration is captured at admission with the existing owner, execution ID, cwd, sandbox and generation. It creates no second execution/card or background capacity reservation. Reload does not change an existing task's source/profile.

The initial scope attaches rich cards at launch. Decorating an arbitrary already-running process, custom cards without an execution, multi-execution aggregate cards and agent-defined UI actions/layout code are excluded.

Profiles never rewrite a command, add flags, launch discovery commands or change approval classification. A Go JSON source requires the caller to choose a command that actually emits Go JSON. Agent-created wrappers/scripts still use ordinary reviewed file writes and approved shell execution.

### Structured events: `events-v1`

A process emits UTF-8 stdout records beginning with the exact ASCII prefix `@@marshal-progress/v1 `, followed by one JSON object and a newline. Ordinary stdout and stderr remain ordinary output. Initial custom control records are stdout-only.

Each record is a **complete progress snapshot**, not a patch:

```json
{
  "version": 1,
  "sequence": 17,
  "phase": {"id": "verify", "label": "Verify checksums", "index": 2, "total": 4},
  "primaryMetricId": "files",
  "metrics": [
    {"id": "files", "label": "Files verified", "unit": "files", "current": 1280, "total": 2048}
  ],
  "rows": [
    {"id": "extract", "label": "Extract archive", "state": "done"},
    {"id": "verify", "label": "Verify files", "state": "running"},
    {"id": "index", "label": "Build index", "state": "pending"},
    {"id": "package", "label": "Package", "state": "pending"}
  ],
  "detail": "weights/layer-028.bin"
}
```

Contract:

- Version is exactly `1`. Sequence is a positive, strictly increasing integer per execution, no greater than `2^53-1`. Duplicate/older sequences are ignored; a process cannot update another execution.
- Phase is optional. Its ID/label identify the phase; optional index/total are positive integers with index ≤ total. A phase count is not a measured duration or overall completion percentage.
- There are at most four metrics with unique stable IDs; primaryMetricId must name one, or the first is primary. Each has label, unit, finite nonnegative current, optional positive total and optional finite nonnegative rate in its units/second. Numeric values are at most `2^53-1`; counts in bytes/files/tests/packages/items use integer current/total values, while rate may be fractional.
- Units include bytes, files, tests, packages and items, or a plain custom unit of at most 16 characters. IDs are bounded to 64 characters; metric/phase/row labels to 80.
- There are at most eight checklist rows, with states pending/running/done/error. These describe reported work, not runtime lifecycle.
- Detail is bounded to 1,024 UTF-8 bytes. Omitted optional fields clear their previous values; timestamps/owner/title/lifecycle are not inherited from a previous phase's data.
- Unknown fields cannot alter identity, capabilities, execution state, permissions or actions. Invalid version/shape/limits rejects the record; malformed records retain the last valid progress and produce one bounded diagnostic in detail, not a repeated transcript error.
- Missing/null total means indeterminate. If current exceeds the reported total, retain current, mark the denominator outdated and hide percentage/ETA until a valid total arrives. Never silently turn this into 100% completion.
- Marshal stamps observation/freshness times. Producer timestamps cannot move retention, supervision or stop clocks.

The producer is bound by the command's output stream, not by a client-supplied execution ID in the record. A child may report only through its own stream. Structured text is untrusted display data: scrub terminal controls/ANSI/OSC links and apply existing redaction before display, persistence or wire projection.

### Ingestion, limits and detachment

Tap command bytes independently of foreground forwarding and retained-output caps. Parsing continues after handoff, output-tail truncation, parent awaiting, normal config reload and TUI panel changes.

Do not reuse the foreground observer as the background progress owner. The execution service owns the observer's lifetime; it closes/drains it after the runner actually returns, before final progress is persisted/published. Progress configuration and final state travel with the same execution's captured evidence, including foreground-to-background handoff.

Bound custom records at 16 KiB per line. Bound Go JSON lines at 64 KiB. Oversize lines are skipped through their newline without unbounded buffering. Raw command capture retains its existing semantics/limits; progress failures never fail or stop the command.

Custom snapshots may coalesce to the latest pending complete record; retain at most one pending snapshot per execution. Process all relevant Go test actions in bounded per-record work at the stream boundary before coalescing the resulting aggregate, since dropping those actions would corrupt counts. Do not place them in an unbounded asynchronous queue. Cap the Go adapter's case-identity cache at 20,000 entries; exceeding it marks counts partial and disables percentage/ETA rather than claiming complete coverage.

Publish presentation updates at most five times per second per conversation; batch all changed items into a snapshot. Source completion, stop and blocked-request transitions bypass cosmetic throttling. No SQLite writes, model calls or slow subscribers run in command output callbacks.

Keep raw output available under its existing retention policy. Maintain an additional bounded 8 KiB display/output tail that omits valid progress-control records, so metric chatter cannot replace useful diagnostics. For the Go source, populate that tail from decoded Output fields rather than raw JSON. Malformed records remain ordinary output. The normal tool result uses the filtered tail plus one bounded progress summary for opted-in streams; raw output remains an explicit inspector choice.

On process completion, drain accepted progress, persist the final snapshot with the actual outcome, then announce terminal status. A progress event saying 100% or all rows done can show “reported work complete; process still running”; it cannot retire a live execution.

## Profiles and truthful measurements

All profiles use the same event fields, renderer and actions. They supply useful labels/field selection, not new executors.

**Generic command:** lifecycle, elapsed time, command label and output tail. No inferred percentage. This is the fallback when no usable structured snapshot exists; a later malformed record does not erase an earlier valid snapshot.

**Tests:** consume Go `test -json` actions, keyed by package/test invocation. Package events have an empty Test field and must not count as an extra test. Run starts an invocation; pause/cont adjust its active state; pass/fail/skip settle it once. A new run after settlement starts the next invocation, including repeated tests. Exact repeated timestamped events are ignored; absent Time remains valid for cached records. A cached terminal event without a preceding run creates one settled invocation, not a running case. Treat build failures as package failures, and preserve benchmark output without inferring a test pass from bench/output. Distinguish package counts from test counts. A failed test can appear while the suite is still running.

Go JSON alone does not establish the complete suite's test/package total. Default to observed counts plus activity. Only show a package denominator when a producer supplies a declared complete manifest/scope, labeled as declared; do not run an extra discovery command just for the card. Dynamic discovery or partial parser coverage removes a definitive percentage. Progress does not override the command's final exit/cause.

**Downloads:** primary bytes received, optional total, measured rate and ETA; secondary file/detail. Format bytes in IEC units. Without a total show bytes/rate and elapsed, with no percentage/ETA.

**Transfers:** primary bytes sent/copied; optional file counts, source/destination and current item. Label a byte-based percentage as bytes, not files. Already-copied files remain reported after an interrupted transfer; no automatic retry/cleanup is implied.

**Custom:** agent-selected title, phase, primary/secondary metrics, checklist and detail over the same fixed layout. A phase-local percentage is labeled for that phase. Extra fields appear in detail, not as an agent-defined panel.

Download and transfer presets initially consume `events-v1` from a measured producer/wrapper. This spec does not promise automatic parsing of arbitrary curl/rsync terminal meters or a new downloader/SSH implementation. Without a producer they remain useful generic command cards. Ship protocol examples for a download producer, transfer producer and phased script; the examples must expose real measurements, not timer-generated progress.

Rates may come from a valid measured producer or successive observed current values for the same phase/metric/unit, using Marshal's observation clock. Reset estimates on counter reset, phase/unit change or outdated totals. Hide rate/ETA after 30 seconds without a valid measurement and show last-update age instead; this is display freshness, never an execution timeout. ETA uses remaining/rate only with a valid total and positive recent rate, is labeled approximate, and is hidden while stopping. Nonfinite or overflowing estimates are omitted; a duration greater than 365 days is displayed as unknown rather than converted into an overflowing timer.

## Persistence, history and model context

Do not persist ticks or add them to chat history.

- Keep existing command execution/audit metadata and `job_exits` as the source of command outcomes. Add optional versioned progress configuration/final snapshot and bounded parser-coverage notes to their JSON evidence; preserve legacy decoding.
- Keep subagent transcript/report persistence as the source of child results. Presentation changes do not delete subagent notices or mutate assistant narration.
- Add a versioned `work_view_v1` JSON value in the existing `session_state` store for terminal watch receipts, lightweight subagent result references and acknowledged terminal-result keys. Store one terminal receipt per watch registration, not one per repeat fire. Serialize updates so an older write cannot overwrite a newer acknowledgment/result.
- The work-view envelope is `{version:1, watchResults:[...], agentResults:[...], acknowledged:[...]}`; agentResults is optional for older records. Watch results carry canonical key, owner/origin, label, terminal result revision, outcome/timestamps and bounded final detail. Agent entries retain stable identity/origin and exact persisted report-message references, while existing transcript messages remain the report body. This index is needed because the live SubagentView registry is not restored on restart. Acknowledgments carry canonical key plus terminal-result revision. Existing session/history retention applies; changing display visibility does not evict evidence.
- Retain live progress in memory. A restart has no live command/watch merely because its stored record says running. Recover final evidence where present; otherwise show unknown/lost history under the existing ownership contract.
- Reopening the same conversation restores acknowledgments and final-progress details, not live producers or receipt timers. Different conversations never inherit them.

A launch result includes its canonical identity/configuration. Completion includes the actual result and bounded final progress summary. If model context needs intermediate progress, use a current bounded snapshot only in the volatile working-state tail, at most 2 KiB for the selected/relevant work. Never inject progress ahead of history, rewrite the stable system prefix or issue provider calls to update a card.

The UI may render a rate/age on a local tick without publishing another model/history message. Custom cards do not create todos or alter repetitive-tool-failure coaching.

## ACP and compatibility

Add runtime-backed `session/work_list`, `session/work_inspect`, `session/work_stop` and `session/work_acknowledge` methods. All requests require sessionId. Inspect/stop/acknowledge also require canonical taskKey; acknowledgment additionally requires terminalResultRevision. Stop has an optional human reason and no command/permission payload.

`work_list` accepts view `live` (default), `attention` or `history`. Live returns the immutable snapshot with complete active membership, up to 32 recent terminal details, and total receipt/attention counts. Attention/history accept optional query/cursor/limit and return normalized result pages plus snapshot revision and nextCursor, using the same pagination bounds as the Work panel. Query is limited to 200 characters and searches safe label/kind/owner fields. `work_inspect` returns the selected item, origin/result references and bounded output/progress detail. Runtime validates session/epoch/identity, preserves existing stop authorization and returns explicit stale/unsupported results.

Publish additive `session/update` kind `work_state` with the same snapshot. Snapshot revisions are scoped by conversation/runtime epoch; clients discard older epochs/revisions and can recover after missed updates through work_list. Duplicate deliveries do not create duplicate receipts or resurrect retired entries.

Keep existing queued steering, mode/access, stack, job and watch contracts. Extend stack-detail/command evidence with optional progress fields. Deprecate the old live-entity band's duplicated listing when the new work projection is present; retain its todo/workflow summary and backward-compatible fields for older clients.

The web tree remains standard-library-only and receives explicit JSON DTOs, never internal types. This feature exposes shared data but does not include a fleet SPA redesign. TUI behavior does not depend on ACP being connected.

## Error handling and accessibility

- No valid progress yet: show the generic command/activity view.
- Invalid/truncated/unsupported records: keep the process and last valid snapshot, mark coverage/freshness honestly, expose bounded diagnostics and continue consuming output.
- Source event gap: replace from an authoritative snapshot. Do not infer success from absence alone; removal requires the source's membership/result boundary.
- Failed stop: retain actual running/stopping state and show the error. Do not substitute parent/conversation cancellation.
- Failed result/acknowledgment persistence: keep the in-memory result/attention, surface an existing diagnostic and retry through the serialized persistence path. Never falsely report a durable acknowledgment.
- Missing inspected result: explain unavailable/expired evidence, keep a truthful terminal summary and do not relaunch the work.
- Secret/escape-bearing output: use existing redaction and terminal-safe text rendering. Producers cannot choose color, HTML, escape sequences, URLs as executable actions, terminal modes or keyboard handlers.

Color reinforces text/glyphs; it never carries state alone. Preserve existing no-color/16-color semantics, terminal transparency/depth, resize behavior and mouse-release selection. Test 80×24, 120×40 and 200×60, narrow resize, prompt/composer focus, keyboard-only use and stable scroll position.

## Acceptance and verification

Use hermetic source snapshots, fake clocks and channel-driven command fixtures. No real download, LAN host, developer provider, keychain or privileged command is required.

1. A subagent dispatch renders once in chronology; its live card stays pinned while scrolling and its final report remains reachable.
2. Plain detached tests/downloads show useful generic cards without adapters. Attached-to-detached handoff preserves identity, output and one card.
3. Agents, commands and watches use the same density, navigation, owner labeling and row budget; many simultaneous tasks remain reachable through overflow.
4. Successful/stopped work retires after five seconds without resetting its deadline on refresh. Failures aggregate into attention and remain inspectable after acknowledgment.
5. Repeat fires and recoverable watch errors remain live; once/removal/final failure retires membership. Dropped/reordered events reconcile without ghost cards.
6. Custom progress updates while the parent is awaiting, held, idle or viewing another panel. Detachment and raw-output caps do not freeze metrics.
7. Structured test counts reflect real actions, including duplicates, skipped cases, repeated invocations, unknown totals, partial parser coverage and failed tests during a running suite.
8. Download/transfer unknown totals suppress percentages/ETA. Changed totals/counters/phases reset estimates correctly; stale measurements do not imply process failure.
9. Agent-configured title/phases/metrics/rows use one command's card. No second task, capacity reservation, permission change, arbitrary rendering or separate stop action is introduced.
10. Split/coalesced writes, CRLF, malformed/oversize JSON, invalid numbers, escape sequences, secret redaction, sequence replay and foreign identity fields cannot corrupt runtime state or make memory/history unbounded.
11. A reported 100%/done event cannot complete a live process. Stop-request/exit races retain the true outcome and partial progress/output.
12. Completion flushes/persists final progress before notification. Resume restores result evidence and acknowledgments without phantom work or replayed success receipts.
13. Tasks Plan preserves todo statistics and viewed-child semantics; Work has clear conversation scope. Inspecting/collapsing never stops work or acknowledges a prompt.
14. Canonical prompt navigation, Send now composer ownership, Ctrl+C, child stop, exit warning and optional postmortem phases retain their contracts.
15. ACP list/event/recovery/inspect/stop/acknowledge use the same runtime identities and data. Legacy stack/watch clients remain compatible; web imports stay within the boundary.
16. Progress bursts coalesce presentation but retain Go test aggregate correctness and final state. No per-tick SQLite/model work or cache-prefix churn occurs.

Authoring-baseline checks:

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app/tui ./internal/viewmodel ./internal/tools/native -run 'NowBar|RunningJobs|Band|HandleWatchMsgUpdatesSnapshot|Wait.*(Cancel|Expiry)' -count=1
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/watch -run 'FireOnce|FireRepeat|ErrorBudget|SuccessRestoresStateFromError|WaitFireWakesOnErrorRecovery' -count=1
```

Focused baseline checks passed in TUI, viewmodel, native tools and watch. The installed Go toolchain's `go doc cmd/test2json` was also read to verify the JSON event contract, including cached events with no Time and package/build/benchmark distinctions. These checks exercise existing behavior, not the proposed feature. No full suite was run for this documentation-only step.

The implementation plan must name new tests and narrow package checks for each stage, then run full Go tests, race checks for source/projection/observer ownership, vet, cgo build and appropriate scripted usability checks. Live credentials/services remain opt-in and are not used for this feature.

## Decisions, assumptions and non-goals

Approved decisions are gutter cards, one shared rendering system, the six reviewed work examples, command-fed progress and successful-retirement/failure-attention behavior.

Conservative details selected while making that design executable are the five-second receipt, fixed row budgets, one expanded card, the Plan/Work navigation extension, command-bound launch configuration, the versioned stdout protocol, resource limits, freshness threshold and persistence/wire shapes above. They are explicit defaults for written-spec review, rather than additional user statements.

The examples are profiles over shared values, not separate widget implementations. Direct event data was selected over arbitrary agent-generated rendering because it preserves consistent sizing, terminal safety, output retention and runtime control. Explicit opt-in was selected over heuristic parsing so an unrelated command's text cannot silently become UI state.

Non-goals:

- Another process/watch/subagent supervisor, background survival after Marshal exits or changed execution deadlines.
- Automatically stopping stale/quiet work, restarting failed transfers/tests or rolling back completed file copies.
- Arbitrary agent-supplied UI code, colors, layout, HTML, actions, key bindings or plugin execution.
- Unattached custom widgets, retrofitting arbitrary external processes or multi-execution aggregate dashboards.
- Universal curl/rsync/third-party test parsers; download/transfer data comes from measured structured producers.
- General transcript/narration deduplication, provider-retry changes, latency tuning, new permission grants or controller steering.
- A fleet web UI redesign, telemetry warehouse or persistence of every progress sample.

Open questions: none. The linked implementation plan is ready for review; production implementation has not started.
