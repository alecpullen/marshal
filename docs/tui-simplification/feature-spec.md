# TUI simplification — feature spec

Status: P-A, P-B, P-C and P-D landed. P-E (the tray, replacing P-C's live box
after testing) designed and planned 2026-10-25. Updated 2026-10-24 with the
decisions the P-A and P-B plans pinned down, and 2026-10-25 with P-C's, P-D's
and round 5's. P-A
landed on
`main` before this branch; P-B landed in `d9975303..9ecfe2ee`, merged as
`24792d61`; P-C landed in `9fa6097e..65ffc3fa`; P-D landed in
`ee9b6626..913b62bf`.
Base commit: `87fa8f0e` (main, after the interaction-modes merge).
Design record: [`alternatives.md`](alternatives.md) (layout),
[`console-variants.md`](console-variants.md) (dock), and
[`framed-console.md`](framed-console.md) (the box P-C built, with frames), and
[`pinned-work.md`](pinned-work.md) (round 5: the tray that replaces it in P-E).
Mockups: [`framed-console.html`](framed-console.html),
[`pinned-work.html`](pinned-work.html).
Execution plans: one per phase, each against `main` once the previous phase
has landed. P-A: [`plans/2026-10-24-p-a-renderer-foundation-plan.md`](plans/2026-10-24-p-a-renderer-foundation-plan.md).
P-B: [`plans/2026-10-24-p-b-lifecycle-plan.md`](plans/2026-10-24-p-b-lifecycle-plan.md).
P-C: [`plans/2026-10-24-p-c-live-box-plan.md`](plans/2026-10-24-p-c-live-box-plan.md).
P-D: [`plans/2026-10-25-p-d-progress-skill-plan.md`](plans/2026-10-25-p-d-progress-skill-plan.md).
P-E: [`plans/2026-10-25-p-e-tray-plan.md`](plans/2026-10-25-p-e-tray-plan.md).

## 1. Summary

The TUI keeps its single column: the transcript, then a tray of pinned work
above the input.

- **History** holds what has happened, and at its end the step in progress. It
  only grows: once a row is drawn, it never changes shape or content except
  when the reader expands it. The one exception is the newest beat while it
  runs: its spinner and its newest call change in place until it settles.
- **The tray** sits directly above the input, with no frame. It holds running
  work (subagents, background jobs, plan runs and progress cards), finished
  work until your next prompt, and the plan. (P-E. P-C built a bordered live
  box that also held the step in progress; testing found it out of step with
  the rest of the TUI, see [`pinned-work.md`](pinned-work.md).)

Each tool call hangs on a spine under the narration that explains it. Todo
progress is marked where it happened, with no dividers. Running work is drawn
in one place while it runs and leaves one completion event in history when it
ends. The live-work progress cards (downloads, transfers, tests, custom work)
move into the tray and gain the measured bar their spec drew. A built-in skill
teaches the agent to produce them.

The overall goal is simplification. This spec removes beat folding, the density
ladder, the now bar's six row types and three size layouts, in-place card
settling, completion-marker merging and the todo gating rules. It replaces them
with one rule for history and one tray with a fixed shed order.

## 2. Goals and non-goals

### Goals

1. **A transcript you can follow.** Calls are visibly tied to their narration.
   Nothing in history folds, settles or reflows on its own.
2. **Todos always visible.** The plan shows whenever a list exists, busy or
   idle, at any length.
3. **Running work pinned at the bottom of the transcript, and unmistakable.**
   Subagents and background jobs sit in the tray while they run. They are the
   only tinted rows on screen.
4. **Progress cards in the same place.** Download, transfer, test and custom
   progress render as running rows with a measured bar.
5. **Fewer rules.** Every removed mechanism in §9 stays removed.

### Non-goals

- **Keyboard design.** The user will rework the shortcuts later. The keys in
  §5.8 are provisional and chosen only so every feature is reachable.
- **Web UI layout.** `web/ui` must render the changed projection (§6.2), but its
  layout is not redesigned here.
- **A new progress protocol.** `events-v1`, the profiles and the Go test adapter
  stay as live work defined them.
- **Theming.** No new palette slots. Every colour is in the Warm Sunset palette.

## 3. Decisions

| Question | Decision | Round |
|---|---|---|
| Layout | Console: append-only history plus one live box above the input (the tray since round 5). | 1 |
| Box treatment | Framed: a bordered box with labelled sections. | 3 |
| Split mode | Optional. The plan moves to a right-hand column inside the box. | 3 |
| Plan modes | Ctrl+T cycles stacked → split → folded (provisional key). `[tui.live_box] layout` sets the starting mode. | 4 |
| Todo dividers | Removed. | 3 |
| Progress in history | A plan line where the list was updated, plus a numbered tag (`▸ N/M`) on the first step of each todo. | 4 |
| Progress widgets | Live work's cards move into the box's running section (the tray's running rows since round 5). | 4 |
| Failed background work | A red completion event in history. Expanding it shows a fuller issue report. Live work's attention row and acknowledgement go. | 4 |
| Teaching the agent | A built-in `progress-reporting` skill. | 4 |
| Tasks panel shortcut | Ctrl+T leaves the Tasks panel. The Work list stays on `/work` and the `+N more` row. | 4 |
| The live box (after testing P-C) | Replaced by **the tray**: running rows, then the full plan list, directly above the input. No frame, no rule, no section headers. The step in progress goes back to the end of history. Supersedes the box treatment, split mode and plan modes above. | 5 |
| Plan modes | Gone with the box: no split or folded mode, Ctrl+T unbound, `[tui.live_box]` deprecated. | 5 |
| Finished items | Keep their tray row until the next prompt: untinted, with their outcome and stopped clock. Their completion event still lands in history when they finish. | 5 |
| Beat head meta | Always on the narration's first line. A narration too long to sit beside it wraps narrower; the meta never takes a line of its own. | 5 |
| Plan run | An SDD or swarm run is a pinned row like a job, with no action of its own. (Assumed from "the other plan information"; not yet confirmed.) | 5 |

Two decisions were taken as defaults from round 1 without objection. They are
listed so a reviewer can overturn them:

- **Narration shows in full** (wrapped). The first-sentence headline with a
  muted continuation goes.
- **No auto-folding and no density ladder.** One level of detail, with
  per-row expand and an expand-all toggle.

## 4. Background: the code today

References are at `87fa8f0e`.

| # | Finding | Where |
|---|---|---|
| B1 | The tree is built once per refresh by `viewmodel.Build`. Both the TUI and ACP use it, and P2 moves the TUI onto its wire form. | `internal/viewmodel/stack.go`, `internal/acp/stack.go` |
| B2 | A beat (`KindStep`) already carries `Todo *TodoLabel`, which marks the beat where work moves to a new todo. It also carries `TodoWrites`, which are never rendered. The TUI draws the label as a divider. | `viewmodel/stack.go` `StepInfo`, `tui/steps.go:136` `renderTodoDivider` |
| B3 | `labelTransitions` skips lists shorter than `MinTaskTodos` (2). `DrivenByTodos` hides the band's plan rows unless the turn touched the list. | `viewmodel/labels.go`, `tasks.go:19` |
| B4 | `foldBeats` folds every finished beat except the newest `FoldKeep` (3). `z` and `tui.transcript.fold_finished_tasks` toggle it. Ctrl+G cycles three densities (`density.go`, `tui.transcript.density`). | `viewmodel/labels.go`, `tui/density.go` |
| B5 | `BeatRowCap` (8) collapses a long beat's older rows. It stays. | `viewmodel/beats.go` |
| B6 | With the live-work service wired (`Snapshot.LiveWork`, true in the app), the transcript keeps each dispatch row once and omits in-place subagent cards. Running work shows in the now bar. Without it (the legacy projection, which ACP still serves by default), cards render in place. **Removed in P-B** (§6.1, §9): one projection, no cards. | `tui/viewport_render.go:78`, `viewmodel/stack.go` `Snapshot.LiveWork` |
| B7 | A background job's dispatch row is settled in place with its exit (`settleJob`). Completions also emit `KindMarker` rows, and `coalesceMarkers` merges adjacent ones. **Removed in P-B** (§6.1, §9): the exit becomes a completion event. | `viewmodel/cards.go` |
| B8 | The now bar is one planner for progress, checklist, turn spinner, live mirror, browser, SDD and swarm rows, work cards (two rows collapsed, six expanded), attention and receipt rows, and overflow. It has a compact layout below 30 rows and an open checklist from 44. | `tui/nowbar.go` (1,304 lines) |
| B9 | Work cards render measurements as text only. Live work's spec drew a bar, but none is rendered. | `tui/nowbar.go` `workMetricLine` |
| B10 | `shell.run` and `test.run` accept `progress {profile, source, title}`. Nothing in the model's context describes the `@@marshal-progress/v1` record, so the agent cannot write a producer. The examples live in `docs/live-work/examples/`, which only exists in Marshal's own repository. | `tools/native/command.go:63,100` |
| B11 | Ctrl+T opens the Tasks panel (Plan and Work tabs). `/work` opens Work directly. | `tui/keypress.go:222`, `commands/commands.go:119` |
| B12 | Skills are listed in the system prompt by name and description and loaded on demand with `skill.load`. | `internal/skills/tool.go` |

## 5. UX specification

### 5.1 Screen zones

From top to bottom:

1. **History.** The scrollable viewport, and the only flexible-height zone.
2. **The tray.** Zero rows when nothing runs, nothing finished is held and no
   plan is shown.
3. **Dock panel,** when one is open (unchanged: `tui/dock`).
4. **Input area.** Unchanged, including the approval, question and sudo
   takeovers.
5. **Status line.** Gains the turn state and the stop hint while busy (§5.7).

### 5.2 History grammar

```
● Pointing the eval config at qwen3-coder.                    thought 2s · ▸ 2/4
  ├ edit    evals/config.toml                                            +3 −3
  └ agent   review the config change                              ⧉ #1 started

● Checking the tokenizer settings match the new model.
  ├ read    evals/tokenizer.toml                                      18 lines
  └ plan    ✓ ✓ ▸ ·  3/4  Run the eval harness unit tests
```

- **Beat head.** `●` (muted), then the narration wrapped in full, in emphasis.
  The right meta holds the owner when it is not the orchestrator, the model when
  it differs from the route, `thought Ns`, the duration from 15 s, and the todo
  tag. These are today's meta rules plus the thinking time and the tag; the
  separate "thought for Ns" row is gone. **The meta always sits on the
  narration's first line.** When the narration does not fit beside it, the
  narration wraps at the content width less the meta's width and a two-cell
  gap. The meta never takes a line of its own; before P-E it dropped to a line
  under a wrapped narration. The one exception is a first line with no break
  point, which keeps the old meta-only line as a fallback.
- **A beat without narration** gets no invented title. Its head is the glyph
  and a muted call count (`● 3 calls`), with the meta right-aligned, so its
  calls still hang off a head.
- **Expanding a beat** shows its reasoning under the head and opens every row
  in it that has no choice of its own. A row's own choice wins.
- **Todo tag.** `▸ N/M` in accent, on the beat that carries a `TodoLabel`. That
  is the first beat bound to a todo that differs from the previous bound beat's.
  Later beats under the same todo carry no tag. The tag drops last when width
  runs short, after model, duration and thought.
- **Calls.** One row per call on the spine: `├` for every call except the last,
  which takes `└`. There are three columns: a verb, the subject, and the result
  right-aligned. The verb replaces the category glyph:

  | Verb | Tools |
  |---|---|
  | `read` | file.read, symbol and LSP reads |
  | `search` | repo.search, search.text, symbol search |
  | `edit` / `write` | file.edit, file.write (results show `+N −M`) |
  | `shell` | shell.run, test.run |
  | `agent` | agent.run and other dispatches |
  | `web` | web tools |
  | `plan` | todo.write (the plan line) |
  | `mcp` | MCP tools (`mcp:<server>` when width allows) |

  Other tools use the first segment of their name as the verb (`git status`).
  When a call's arguments name no subject, the rest of the tool's name stands
  in, unless it only repeats the verb.
- **Plan line.** Each beat that wrote the todo list shows one `plan` row at the
  position of its last `todo.write`. It holds a stepper (`✓` done, `▸` in
  progress, `·` waiting), the position `N/M` and the in-progress item. Several
  writes in one beat show only the final state. A write that clears the list
  shows `plan  cleared`.
- **Launch results.** A call that starts background work names it in its kind's
  colour (`┆ job-1 started`, `⧉ #1 started`). The row never changes after that.
- **Failure.** A failed call turns the beat glyph to `✗` (error colour) and
  shows the last three lines of its output under the call. This is today's
  rule.
- **Edits** are one line, `+N −M` as the result. The diff shows when the row is
  expanded. (Today a collapsed edit row shows up to 12 diff lines per file.)
- **Expanded detail** hangs under the subject column. A row that is not the
  last carries the spine (`│`) past its detail.
- **Same-tool runs** merge into one row, as today: the subjects joined, `×N`
  as the result. **Long beats** keep `BeatRowCap`, and the older rows collapse
  to a `… N earlier calls` row on the spine.
- **Running calls** never print raw arguments: the row names the path or the
  summarised argument (a running `file.write` names its path), with a spinner
  and the elapsed time as the result. A command's sandbox line and the last six
  lines of its output stay under the row while it runs. A running call whose
  work has a running row in the tray points to it (`→ #1`) instead of repeating
  its activity (P-C).
- **Copying.** Dragging over a call row copies its subject: the connector, the
  verb and the right-aligned result are not copied, and the detail under the
  row copies from the subject's column. Browse mode's `y` still copies the
  call's result.
- **No dividers, no folding.** One level of detail. Enter in browse mode, or a
  click, expands one row; Ctrl+G toggles expand-all.
- **Streaming reasoning sits at the bottom.** Reasoning in progress is never
  drawn inside a beat, where it would push the beat's calls apart. It is one
  bounded row (`⚙ thinking 4s`, scrollable) at the bottom of the transcript,
  below every beat, call row and completion event, for a reader to glance at or
  ignore. A step that holds nothing but reasoning so far has no row of its own.
  Finished reasoning stays with its beat: `thought Ns` in the head, the text
  when the beat is expanded. (P-B; found in testing. P-C moved the row into the
  box; P-E returns it here.)
- **A skill the agent loads shows once,** as its call row. The load also writes
  a hidden body message and a one-line tag message; neither carries a step ID,
  so as rows they drew a duplicate under the beat and split it. The projection
  drops the body always, and drops a tag when the same turn holds a successful
  `skill.load` of that skill. A tag from a load with no call row (the skills
  panel, an ACP client) stays. (P-C; found in testing.)

### 5.3 Glyph vocabulary

Each glyph has one meaning.

| Glyph | Means |
|---|---|
| `❯` | your prompt, and the input |
| `●` | a settled beat |
| spinner frame | running now (the live beat's head, a call, a running row) |
| `✗` / `✓` | failed / done |
| `├ └` | this call belongs to the beat above |
| `⧉ #n` | subagent *n* (`AccentSecondary`) |
| `┆ job-n` | background job *n* (`StatusInfo`) |
| `○` | watch |
| `▸` / `·` | plan item in progress / waiting |
| `━─` | measured progress (heavy in the kind's colour over a dim light track) |
| tint and `▌` edge | running work, pinned. A finished row held until the next prompt keeps its edge and loses the tint. Nothing else in the UI has a background. |

Retired: `▸` as "running", `·` as "settled step", the call category glyphs
`✎ ≡ › ◈`, and the `▍` rail on now-bar rows. Under NO_COLOR the edge, glyph, id
and the heavy/light bar characters keep every state distinguishable.

`●` is `glyph.Beat`, which shares the rune of `glyph.Brand` (the banner and
picker badge, never drawn in the transcript). The spine is `glyph.SpineMid`,
`glyph.SpineEnd` and `glyph.Spine`. After P-A the category glyphs no longer
appear in history; their constants stay for the now bar, the in-place
subagent cards and the standalone call renderer until P-B and P-C delete
those.

### 5.4 Running work: launch, row, completion

1. **Launch.** The dispatching call is an ordinary call row (§5.2).
2. **Running.** One row in the tray (§5.5, §5.6) for as long as the work is
   live.
3. **Done.** The row stays in the tray, finished, until your next prompt
   (§5.5), and one **completion event** is appended to history at that moment:

   ```
   ┆ job-3 Harness tests · exit 1 · 210 passed, 2 failed · 48s              ▹
       --- FAIL: TestThresholdsForCoderModels/context_window
   ```

   - The head shows the glyph and id in the kind's colour, the label, the
     outcome (`done` in success colour, or `exit N`, `stopped`, `lost` or
     `failed` in error colour), the final measurement, and the duration.
   - The summary line is the subagent's report headline, a job's last useful
     output line, or a progress card's detail.
   - There is one event per finished item. `coalesceMarkers` goes.
   - **Where it lands.** An event follows the beat whose row launched its work.
     Any other event that ends while a beat is still open lands before that
     beat, because the beat joins history only when it settles (the next beat
     starts, or the turn ends). One that ends after a finished turn's last
     beat follows it.
   - **Outcomes.** `done`, `exit N`, `stopped`, `lost` or `failed`, plus
     `outcome unknown` (warning colour) for a record with no observed exit and
     no stop or failure state. A missing exit never reads as success. A lost
     job (synthesised on resume, stamped with its start) gets an event that
     follows the beat that launched it.
   - **Summary line**, first match wins: a subagent's report headline, else its
     error's first line; for a job, the first `--- FAIL` line, else the
     progress detail (not `go test`), else the last non-blank output line.
   - **Expanded, an event becomes an issue report.** It shows the outcome and
     stop cause, the command or task, cwd and duration, the final progress
     snapshot, the bounded output tail (up to 20 lines, `--- FAIL` lines first),
     and, for a subagent, its report. It renders from the projection
     (`CompletionInfo`), so resume shows the same report. Enter or a click
     expands it; `i` opens the inspector with the full retained output; `f`
     drills into a subagent. `o` is not bound on an event. There is no jump
     from an event to its launch row. Failed events are not acknowledged or
     dismissed; they are part of history. `WorkService.AcknowledgeWork` loses
     its TUI caller in P-C, with the now bar's attention row.

### 5.5 The tray

```
▌┆ job-3  Download qwen3-coder weights  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB        2m 0s ⠋
▌⧉ #1     review the config change  done · context_window matches the model card  1m 48s ✓
 ✓ Add retry with backoff to the downloader     ▸ Check the eval thresholds
 ✓ Start the weights download                   · Run the harness tests
▍❯ Ask Marshal...
 Act · ⠋ working 4m 12s · qwen3-coder @ ollama · local                       Ctrl+C stop
```

(P-E. It replaces P-C's live box; the frames are in
[`pinned-work.md`](pinned-work.md).)

- **What it holds, top to bottom:** the plan-run row, running and held rows,
  then the plan. There is no frame, no rule and no section header. The tint on
  running rows and the plan's marks set the tray apart from the transcript.
- **What it never holds:** the step in progress, and the browser session. The
  status line carries the browser's URL whenever a session is open.
- **The step in progress is history's last beat.** `SplitLive`'s live part
  renders at the end of the transcript, after history, with the streaming
  reasoning row last (§5.2). When the beat settles it simply stops changing;
  nothing moves. A streamed final answer appends to history as it streams.
- **Running work comes only from the live-work snapshot** (the app always
  attaches the work service).
- **The plan-run row.** While an SDD run or a swarm is active, the tray's first
  row is that run, in the accent colour: `▌▸ run  <plan>  task 2/5 · implement
  3m · 20% · ~12–16m left  14m 2s ⠋`, the run summary the box's top border
  showed. It has no action: it is not a browse stop, and a click on it does
  nothing.
- **Held rows.** A finished item keeps its row until you send the next prompt.
  The row loses its tint, shows `✓` or `✗` where the spinner was, shows the
  outcome and the completion's summary line in place of the measurement, and
  stops its clock at the duration. Sending a prompt drops every held row.
- **Order.** Rows sort by start time, so a row keeps its place while it is in
  the tray, held or not, and new rows join at the end. Becoming blocked does
  not move a row. When a shed step caps the rows, blocked rows are kept first,
  still drawn in start order.
- **Columns.** Running and held rows put their `▌` edge in column 0 and their
  glyph in column 1, under history's glyphs. Plan rows are indented one cell so
  their marks sit in the same column.
- **The plan** is the full list in two columns, as the box's stacked mode drew
  it, without its header. A list longer than 6 shows a window around the active
  item, with `✓ N done` and `+N more`. A stale plan gains a last entry,
  `stale 4m`, in the warning colour (the box put it in its header). A finished
  list is one row, `✓ Plan 4/4 done`, until the next prompt; after that the plan
  leaves the tray until a list has open items again.
- **Budget and shed order.** The tray takes at most
  `min(height×2/5, height − scroll hint − breadcrumb − status line − dock −
  input chrome − 1 − minTranscriptRows)` rows. When it would take more, it
  sheds in this order until it fits:
  1. The plan becomes one row in the plan line's grammar
     (`✓ ✓ ▸ ·  3/4  <active todo>`).
  2. Held rows fold into one row (`✓ 2 finished`).
  3. Running rows cap at 3, with `+N more · /work`.
  4. An expanded card collapses.
  5. Running rows cap at 1, with the more row.
  6. The tray becomes one plain summary row: the kind counts and the plan's
     done/total (`⧉2 ┆2 ○1 plan 2/4`).
- **Idle** is the same tray: running work that outlives the turn, held rows and
  the plan stay. With none of them, the tray is zero rows.
- **Controls.** Running and held rows, the more row and plan items are browse
  stops after the last history row, and clicks do what Enter does (§5.8). The
  plan-run row and the folded `✓ N finished` row have no action. There is no
  layout key.

### 5.6 Running rows and progress cards

A running row is one row:
`▌` edge, glyph, id, title, measurement, elapsed time, spinner. Its tint is
`BGSurface`, painted at every depth including `flat` (the one exception to
"flat paints nothing", because the tint is what marks running work), and its
edge and id are in the kind's colour: `⧉` subagents in `AccentSecondary`, `┆`
commands in `StatusInfo`, `○` watches muted. A measured rate and ETA need two
observations, so the TUI keeps each item's previous progress snapshot (it never
did before P-C).

| Item | Measurement |
|---|---|
| Subagent | latest activity: the child's newest narration headline, or its running call |
| Subagent awaiting input | `⚠ needs approval` / `an answer` / `access` / `a skill decision` / `a password`, by the request's kind, in warning colour |
| Generic command | the latest useful output line |
| Tests (`go-test-json`) | `N passed · N failed`, plus `N skipped` when nonzero; the failed count in error colour when nonzero; never a percentage (the snapshot has no package count, so none is shown) |
| Download / transfer, total known | bar, percentage, `current/total` and ETA (rate when width allows) |
| Download / transfer, total unknown | `current · rate · size unknown`; no bar |
| Custom with a phase | `N/M <phase>`, a bar and `% of phase` |
| Stale (no valid measurement for 30 s) | the bar holds, rate and ETA hide, `⚠ no update for 42s` |
| 100% reported while still running | full bar, `reported done · process still running` |

Live work's truthfulness rules apply unchanged: no invented denominators,
phase-local percentages labelled, outdated totals hide percentage and ETA.

**Expanded card.** One item at a time opens to at most five rows: the header
with the profile word and elapsed time; a full-width bar with percentage; the
measurement with rate and ETA; the detail line or checklist (`✓ ⠋ ·`, at most
four items inline); and actions (`↵ collapse · s stop · f open · /work list`,
each only when it applies). The other
rows stay at one row each.

### 5.7 Status line

While busy, the turn state follows the mode cue (and the `untrusted` cue when
there is one): the spinner, `working`, the elapsed time and the activity label
when there is one. P-C drew these in the box's top border (the status line
itself never held a busy timer; the now bar's turn row did). The mode cue stays
first because the status line never drops its first segment.

The busy hints on the right already include `Ctrl+C stop`, but the status line
drops its whole hint cluster when it does not fit, which happens at 96 columns
while busy. Before dropping it, the cluster now shrinks to `Ctrl+C stop` alone,
so the stop hint the box's border always showed stays on screen.

During an SDD run or a swarm the state stays `working`; the run's summary is the
tray's plan-run row. Scrolled up, the step in progress is out of view, and the
turn state and activity label stand in for it. The status line always carries
the browser URL while a browser session is open.

### 5.8 Keys (provisional)

Chosen only so everything is reachable. The shortcut rework will replace them.

| Key | Action |
|---|---|
| Ctrl+T | unbound (P-E removed the plan layouts it cycled; it no longer opens a panel either) |
| Ctrl+G | toggle expand-all (replaces the density cycle) |
| Esc, then `j`/`k` | browse mode, as today. `j` past the last history row moves into the tray: the running and held rows, the more row, then plan items. |
| Enter (browse, on a running row) | expand or collapse that card; on a held row, scroll history to its completion event; on a plan item, scroll history to its first beat; on `+N more`, open the Work list |
| `s` (browse, on a running row) | request stop, when the item's capabilities allow it |
| `f` (browse, on a subagent row) | drill into that subagent |
| `G` / End (browse) | move to the end and resume following |
| click | a running row expands its card; a held row scrolls to its completion event; a plan item scrolls to its beat; `+N more` opens the Work list |
| `/work` | the Work list: the Tasks panel without its Plan tab, and without "Mark reviewed" or "Dismiss" |
| `z` | removed (folding is gone) |

The step in progress is history again (P-E), so its rows are ordinary browse
stops and click targets. (In P-C its rows in the box were click targets only.)

## 6. Architecture

### 6.1 Projection (`internal/viewmodel`)

- **Folding:** delete `foldBeats`, `FoldKeep` and `StepInfo.Folded`.
- **Messages that are not rows:** skill bodies, and skill tags that a
  `skill.load` call in the same turn already shows (§5.2), never become nodes.
- **Todo labels:** `labelTransitions` drops the `MinTaskTodos` gate, so any list
  labels its transitions. Labels for todos no longer in the list stay
  suppressed. The `TodoLabel` payload is unchanged; only its rendering moves
  from a divider to the tag.
- **Plan updates:** add `KindPlanUpdate`, a row child of its beat placed at the
  beat's last `todo.write` that changed the list (a failed or denied write
  changed nothing). Its payload is `PlanUpdate{Status []string; Active int;
  ActiveText string; Cleared bool}` with `Done()` and `Total()`, derived from
  the write's arguments. `todo.write` keeps unfinished items the model left
  out and reports the full length in its summary ("tasks updated · N items");
  those carried items have no status in the arguments, so they are appended as
  waiting. `TodoWrites` stays as the source. A narrationless step whose only
  call is a write is no longer dropped as empty: it joins the beat before it,
  or heads its own.
- **Completion events:** `MarkerInfo` becomes `CompletionInfo`:
  - source (`job` or `subagent`), id, subagent ID, label and outcome;
  - exit code or stop cause, start and end times, duration;
  - a final measurement summary (`MeasureSummary`, from the job exit's
    retained final progress);
  - a summary line;
  - the report fields: command, cwd, retained output, subagent report and
    error;
  - references (`WorkKey`, execution ID, subagent report message ID) and
    `Launch`, the row that launched the work.

  `KindMarker` is renamed `KindCompletion` in place. Delete `coalesceMarkers`.
  `mergeBeats` places events by the rule in §5.4.
- **History reads only the transcript.** A settled row must not change when
  the todo list is rewritten, so:
  - the todo tag's `N/M` and text come from the plan in effect: the plan left
    by the write that re-bound the beat, else the newest plan line before it.
    The current list is the fallback only when no plan with an active item is
    known;
  - a narrated beat whose only call is a `todo.write` is re-bound to the stored
    todo of the same actor's next step in the same turn, not to the list item
    the write named;
  - the receipt's task count is the number of items the turn's plan lines
    moved to completed.
- **One projection.** Drop the legacy `LiveWork == false` path:
  - in-place subagent cards in the parent view;
  - `settleJob`, which settles dispatch rows in place;
  - the `RunningSubagent` suppression.

  A dispatch row is always just its call row (a background start reads
  `┆ job-n started`). `Snapshot.LiveWork`, `RunningSubagent` and `Drilled` are
  removed. Correction: ACP never had a `session/stack` parameter selecting the
  live-work projection; it always served the legacy one, and switches in the
  same phase as the web. No tree carries subagent nodes any more.
- **Band:** `BandOf` drops the `DrivenByTodos` gate. The band always carries the
  full current list (`BandTask`) when one exists, plus the stale signal. Delete
  `MinTaskTodos` once no caller remains. `BandInfo.Todos` keeps its "busy with
  work left" gate until P-C's idle state. The running entities the web lists
  come from the snapshot's items (running subagent views, background starts
  without an exit), since the in-place cards they were read from are gone.
- **The live beat:** add `SplitLive(turns, busy) (history []*Node, live
  []*Node)`. While busy, the live part is the newest turn's last beat, whether
  or not its current step has ended (a narrationless step can still merge into
  it), plus the events after it, which are only ever events of work that beat
  launched, plus the streaming reasoning node, which `Build` always makes the
  newest turn's last child (`LiveThinkingID`; no beat carries in-progress
  reasoning). P-C rendered `history` in the viewport and `live` in the box;
  P-E renders both in the viewport, `live` last. The split stays because the
  append-only property tests (`history_test.go`) are defined over it; after P-E
  it has no production caller.
- **The property to test:** for successive snapshots of one session, every
  history node of the earlier tree keeps its ID and `Version` in the later tree,
  apart from appended nodes (§8).

### 6.2 Wire and web

- `WireNode` changes:
  - `KindPlanUpdate` (`"planUpdate"`) carries `plan`, a `WirePlan{status,
    done, total, active, activeText, cleared}`. This lands in P-A because the
    projection gains the node there; the web skips kinds it does not know, so
    it renders nothing until P-B;
  - `WireStep.folded` goes in P-A with folding. The web's fold handling is then
    inert, and P-B removes it;
  - `WireMarker` becomes `WireCompletion` (`completion`), with the fields
    above;
  - `WireJob` goes, and `WireTool` gains `subagentId`, the subagent an
    `agent.run` row started, so a client can drill in from the row;
  - `WireSubagent` goes: no tree carries subagent nodes;
  - `WireTodoLabel` is unchanged.

  Coordinate the JSON names with daemon P2 C6, which must drop its planned
  `LiveWork` stack parameter and its `WireSubagent` additions.
- `web/ui` renders the changed kinds in the same phase (P-B). The completion
  event is a one-line row that expands into the issue report; the plan line is
  a call row. The web loses folding, the job card and the subagent card, and
  drills into subagents from events and dispatch rows. Its now bar keeps
  reading the band. `TurnNode.svelte`, `ToolRow.svelte`, `stack.ts`, `browse.ts`
  and `Chat.svelte` are the touch points.

### 6.3 TUI

- **New package `internal/app/tui/livebox`.** Pure `Plan(Input) Layout` and
  `Render(Layout, width) string`, the same split as today's
  `planNowBar`/`renderNowBar`. `Input` holds the live part already rendered by
  the TUI (lines with their node targets, plus a short form of the beat's head
  and current call), the live work items, previous progress observations,
  subagent headlines, the plan items, the mode, the expanded key, the cursor,
  width, the row budget and now. `Layout` records each row's frame, tint and
  hit target, and the browse stops, so the click and browse maps come from the
  same plan as the drawing. The package never imports `internal/app/tui`.
- **`view.go`:** compose history, then the box, the dock, the input and the
  status line. The box's height comes from its plan.
- **Delete from `nowbar.go`:**
  - the progress and checklist rows, turn spinner row and live mirror row;
  - the compact and tall layouts;
  - the attention, receipt and combined summary rows;
  - in-bar expansion and the legacy agent, job and watch rows.

  Port to `livebox`: `workPrimaryMetric`, `workMetricCount`, `workGoCounts`,
  `workRateText`, `workBytesIEC`, `progressBlocks` and the ETA formatting.
  `nowbar.go` is then deleted.
- **`steps.go`:**
  - the head meta gains the todo tag, and `renderTodoDivider` goes;
  - call rows switch to the verb columns;
  - `renderMarker` becomes `renderCompletion`, with an expanded issue report;
  - `renderJobCard` goes;
  - the headline/continuation split goes.
- **Also delete:** `density.go` and the density plumbing in `stepRenderCtx`; the
  `foldTasks` toggle and `z`; the Tasks panel's Plan tab (its Work tab stays
  behind `/work`); and the Ctrl+T binding to the Tasks panel.
- **In-place subagent cards** (the projection without live work) render below
  a beat's spine at the rows' indent until P-B removes them.
- **Bar renderer.** `measuredBar(fraction float64, cells int, kind color)` draws
  `━` cells in the kind's colour, `╸` for a half cell, and a dim `─` track. Under
  NO_COLOR it is the same characters, which still read as filled and empty.
- **Browse.** Box rows become browse targets after the last history node.
  Enter, `s` and Esc act on them.
- **P-E, the tray.** The bullets above describe P-C. P-E changes them:
  - the viewport renders `SplitLive`'s history and live part together again,
    and the live part's rows become ordinary history targets;
  - `livebox` keeps the running rows, the card, the progress helpers, the
    stacked plan's rows, the browse stops and the budget. It loses the frame
    and its section headers, the modes (split, folded, `Next`, `ParseMode`),
    the live part and the shed steps that acted on them. It gains held rows,
    the plan-run row and the new shed order (§5.5);
  - the status line takes the turn state and the stop hint (§5.7);
  - `steps.go`'s head keeps its meta on the narration's first line (§5.2).

### 6.4 The `progress-reporting` skill

- **File:** `internal/skills/builtin/progress-reporting.md`, with risk
  `workspace_write`. It has the agent write producer scripts, and the built-in
  skills that lead to writes declare that risk. (Changed from `read_only` on
  2026-10-25; the risk is informational and gates nothing.)
- **Description (it decides when the skill is loaded):** "Show a live progress
  bar for a long download, transfer, test run or phased job, using shell.run's
  progress option." (114 bytes; the listing line is 142.)
- **Body:**
  - when a card helps, and when the generic card is enough;
  - the `progress` object, profiles and sources;
  - the record format, with the prefix, a complete snapshot per line, an
    increasing sequence, the metric, phase, row and detail limits, and never
    inventing a total, with one complete example record;
  - what the card does with records: a stale warning after 30 seconds
    without one, `detail` shown before the row checklist, and the last
    record's measurement and detail on the completion event;
  - **embedded** minimal producers, adapted from `docs/live-work/examples/`.
    They are a shared helper (`marshal_progress.py`) and producers for a
    download, a transfer and a phased custom job. They must be embedded because
    user projects do not contain that directory, and the body never names it;
  - the Go test JSON path, which needs no producer.
- **Where producers live.** The agent writes them with `file.write` under
  `.marshal/progress/`, reusing any already there. Writes must stay in the
  workspace, and `.marshal/` is Marshal's own directory. The `.marshal/.gitignore`
  written for new projects gains `progress/`. In an older project the folder
  shows as untracked, and the agent says so instead of editing the ignore file.
  The producers need `python3`; without it the agent uses the generic card.
- **Schema pointer:** the `progress` property in the `shell.run` and `test.run`
  schemas gains the description "live progress card; see skill
  progress-reporting". `TestShellToolTextDocumentsWaitContinuationAndStop` is
  extended to pin it.
- **Budgets.** At `d7d9b39c` the system prompt was 9,110 bytes (over its 9,000
  ceiling) and the model-facing tool schemas 14,975 (ceiling 15,000).
  - The skill's listing line fits because the postmortem skill's description
    shrinks from 383 bytes to 121. Its one caller already names the skill
    ("Run the postmortem skill: …"), so the trigger text was unused. The prompt
    ends near 8,990, and `TestDefaultPromptBudget` passes again.
  - The pointer fits because `shell.run`'s description drops "Commands are
    subject to conservative guardrails.", which gave the model nothing to act
    on. The schemas end near 14,991. `test.run` is internal and not counted.
- **Nothing is added to the system prompt** beyond the skill's listing line.

### 6.5 Config and settings

- **New in P-C:** `[tui.live_box] layout = "stacked" | "split" | "folded"`,
  default `"stacked"`, with a `/settings` field. The mockups called this
  `[tui.dock] layout`; it is renamed because `tui/dock` already names the
  docked-panel host.
- **Deprecated in P-E:** `[tui.live_box] layout` is still parsed but no longer
  merged or saved, `/settings` drops its field, and one warning at path
  `tui.live_box` says it is ignored, as `tui.transcript` does.
- **Deprecated:** `tui.transcript.density` and
  `tui.transcript.fold_finished_tasks` are still parsed but no longer merged or
  saved, and `/settings` drops both fields. One warning at path
  `tui.transcript` says they are ignored, however many layers set them, as
  `[tui.side_panel]` does today.

### 6.6 Persistence

None new. Completion events derive from persisted job exits (with retained
final progress), subagent report messages and work-view results. Plan lines
derive from persisted `todo.write` tool calls. Resume renders the same history.

## 7. Phases

| Phase | Scope | Depends on | Ships alone |
|---|---|---|---|
| **P-A Renderer foundation** | Spine and verbs; glyph cleanup; the todo tag replaces the divider; `KindPlanUpdate` and the plan line (with its wire kind); no folding; Ctrl+G expand-all; humanized running calls; config deprecations. TUI plus small projection changes. | — | yes |
| **P-B Lifecycle in the projection** ([plan](plans/2026-10-24-p-b-lifecycle-plan.md)) | `CompletionInfo` and the expandable issue report; dispatch rows never settle; one projection (legacy path removed); band gating removed; `SplitLive`; wire changes; `web/ui` adoption. | P-A | yes |
| **P-C Live box** | `livebox` package; the live beat in the box; running rows with bars and the expanded card; stacked, split and folded plus Ctrl+T; budget and shed order; idle state; browse into the box; `nowbar.go` and the Tasks Plan tab deleted; `[tui.live_box]`. | P-B | yes |
| **P-D Progress-reporting skill** ([plan](plans/2026-10-25-p-d-progress-skill-plan.md)) | The skill and its producers, the schema pointer and the pinned schema test, the budget room (§6.4). | — | yes, any time |
| **P-E The tray** ([design](pinned-work.md), [plan](plans/2026-10-25-p-e-tray-plan.md)) | The step in progress back at the end of history; the tray with plan-run, running and held rows and the plan list, without frame, rule, headers or modes; held rows until the next prompt; the new shed order; head meta on the first line; turn state and stop hint in the status line; `[tui.live_box]` deprecated; Ctrl+T unbound. | P-C | yes |

**Ordering with daemon P2.** P2 plan 3 moves the renderer onto `WireNode` and
ports "density, folding, selection, browse and inspect". Land P-A and P-B before
P2 plan 3, so plan 3 ports a smaller renderer onto a settled wire shape. P-C
reads the band and work snapshot, which P2 plans 2 and 3 carry. It can land
before P2 plan 3, or be written against `WireNode` after it, whichever comes
first.

## 8. Testing

- **Append-only property (P-B).** Build trees from a scripted session at
  successive points (calls finishing, a job exiting, a subagent reporting, todos
  rewritten, the live beat settling). Assert that every history node of the
  earlier tree keeps its ID and `Version` in the later one, and that new nodes
  only append. This guards the main rule.
- **Projection unit tests:**
  - plan update derivation (merged writes, cleared list, unknown active item);
  - tag placement (first beat per todo, none after, none for a one-item list's
    repeats);
  - one completion event per finished item, with outcome and measurement;
  - no settled dispatch rows;
  - `SplitLive` boundaries.
- **`livebox` planner tests** (pure):
  - each shed step at each threshold, and the 40% cap;
  - split skipped below 90 columns;
  - the window for long plans;
  - one expanded card;
  - idle and empty states;
  - hit targets match the rendered rows.
- **Golden frames** at 80×24, 96×34 and 120×40 in NO_COLOR, as text files
  under `internal/app/tui/livebox/testdata/` with an `-update` flag (the repo
  had no file-based goldens; `depth_golden_test.go` compares properties):
  stacked, split, folded, expanded, idle, compact, and every row shape in
  §5.6. Each generated frame is reviewed against §5.5 and §5.6 before it is
  committed.
- **Frame invariants** (`frame_invariants_test.go`): the left column never
  exceeds `height - statusLineRows` with the box (the tray, after P-E) at its maximum.
- **P-E:**
  - the tray planner: held rows keep their place until the next prompt, the
    plan-run row comes first, each new shed step at its threshold, and no
    frame or header rows;
  - the golden frames regenerated for the tray (busy, held, idle, a card open,
    each at 80×24, 96×34 and 120×40), and every row shape in §5.6 kept;
  - the live part renders in the viewport while busy (the reverse of P-C's
    `TestLiveBeatRendersInTheBoxNotTheViewport`);
  - a wrapped narration keeps its tag on the first line at 80 and 96 columns,
    and no head writes a meta-only line;
  - the status line keeps the turn state and `Ctrl+C stop` at 80 and 96
    columns while busy, with the mode cue first.
- **Web:** `Transcript.test.ts` and `NowBar.test.ts` cover the new kinds, and
  the web boundary test stays green.
- **Skill:**
  - the prompt and schema budgets (`TestDefaultPromptBudget`);
  - the schema text pin;
  - a skill-load test that pins the description, risk and key rules;
  - every example record in the body decodes with the real decoder;
  - the embedded download and custom producers run under `python3`, skipped
    when it is absent. Their records decode in sequence, and their totals match
    the bytes and files they really handled.
- **Mutation check each phase:** revert one behaviour (append-only, shed order,
  tag placement) and confirm a test fails.

## 9. Removed mechanisms

| Removed | Replaced by |
|---|---|
| Beat folding, `FoldKeep`, `z`, `fold_finished_tasks` | nothing; history holds still |
| Density ladder, `tui.transcript.density` | per-row expand and Ctrl+G expand-all |
| Todo dividers | the plan line and the numbered tag |
| `MinTaskTodos` and `DrivenByTodos` gating | the plan shows whenever a list exists |
| In-place subagent cards; `settleJob` | the launch row plus the completion event |
| Completion-marker merging | one event per item |
| The now bar's row types and size layouts | the box's three sections and one shed order (the tray's, after P-E) |
| The live mirror row | the status line's turn state and activity label (P-E; in P-C, the box) |
| Receipt and attention rows; acknowledgement | the completion event |
| The legacy `LiveWork == false` projection | one projection |
| The Tasks panel's Plan tab | the plan section |
| The live box's frame, section headers and plan modes; Ctrl+T's cycle; `[tui.live_box]` (P-E) | the tray |
| The step in progress inside the box (P-E) | the end of history |

## 10. Risks and open points

- **Web adoption cost** in P-B. The web renders the same kinds, so the change is
  a few components, but it must land with the projection change.
- **ACP clients other than the web** that rely on the legacy projection: none
  are known. The `session/stack` parameter is removed only after the web adopts
  the change.
- **The step in progress is history's last beat** (P-E), so the end of history
  changes in place while it runs: its spinner and its newest call. Nothing
  above it changes.
- **The tray's maximum height** on busy swarms is held by the 40% cap and
  `+N more`. A swarm view stays out of scope.
- **No rule above the tray** (P-E). If the plan rows read as transcript text
  in testing, a one-cell rail on them is the fallback, not the rule.
- **Keyboard:** every key in §5.8 is provisional.
