# Transcript beats: fact-driven transcript, todos as labels

Date: 2026-10-10
Status: implemented on `transcript-beats`; see "Decisions during implementation"

## Problem

The transcript's structure depends on two things the model controls loosely,
and both fail in practice:

1. **Todo-driven grouping.** `viewmodel.groupTasks` wraps consecutive steps
   that share a todo under task nodes. Models do not maintain `todo.write`
   strictly, so the structure is not merely missing but *wrong*:
   - A todo left `in_progress` (e.g. "Task 0 — create worktree" during a
     whole subagent orchestration) re-prints its header with ruler for every
     segment, because each turn-level interruption (subagent notice,
     pass-through) starts a new segment.
   - The queue lists tasks as waiting and the band shows `0/9` while those
     tasks' subagents have already committed: progress comes only from todos.
   - Dropped todos title themselves from `FirstNarration`, so the header
     repeats the narration directly beneath it.
2. **Inferred step headlines.** A `session.Step` is one model response. When
   response N narrates and response N+1 is thinking plus a tool call without
   narration, N+1 renders as its own step with a headline synthesised from
   its tool rows (`ran git …`, tagged `inferred`). Tool calls detach from the
   narration that motivated them. Separately, step-less items form
   `Heuristic` steps that also bypass task grouping.

Further duplication: a subagent appears as dispatch + card + `Waiting… ·
subagent N completed` await result + `✓ subagent N completed` notice; a
background job appears as a start row plus a turn-level `KindJobExit` row;
`Waiting…` renders as both an inferred step headline and the await row.

The user mostly **watches runs live**; the design optimises for that.

## Goals

- The transcript is never structurally wrong, whatever the model does with
  its todos.
- Every tool call sits under the narration that motivated it.
- Each subagent/job is represented once in place, plus one short completion
  marker at the moment it finishes.
- Todo state is visible but advisory, with an explicit signal when it is not
  being maintained, and a nudge to the model when it goes stale.
- TUI and web render the same tree from the same `viewmodel` projection.

## Non-goals

- Changing `session.Step` storage (it stays one model response).
- Inferring todo progress from subagent titles or completions.
- A database migration: beats are derived at build time, so existing sessions
  replay in the new style.

## Design

### 1. Beats replace steps as the render unit

A **beat** is a narrated model response plus every subsequent narrationless
response by the same actor.

- A new beat starts on: new narration, a user prompt or steering message, or
  an actor change. Thinking, tool results, awaits and subagent completions do
  **not** start a beat.
- The beat headline is always the first narration line, the agent's own
  words. Inferred headlines are deleted: `viewmodel.StepHeadline`'s tool-row
  inference, the `inferred` tag, and the TUI copy in
  `internal/app/tui/steps.go` (`stepHeadline` and the "inferred headline"
  section).
- Narrationless responses that open a turn render their rows with **no
  headline** — no placeholder.
- The `Heuristic` step type is removed. Step-less items attach to the beat
  open at their timestamp. Implementation first audits which producers still
  write step-less items; any live producer is stamped at the source, and the
  attach-by-time fallback remains for legacy sessions.
- Beat node IDs derive from the first step's ID (`beat:<stepID>`) so expand
  state, scroll offsets and the render cache carry over.
- A beat binds to its first step's todo, with `effectiveTodoID`'s rebinding
  rule (a narrated step whose only call is `todo.write` binds to the todo it
  set in progress) applied to that first step.
- Long beats (models that rarely narrate) collapse older rows into
  `+N earlier calls`, keeping the live tail visible.

### 2. Todos as transition labels

- Removed: `KindTask`, `groupTasks`, segment numbering, dropped headers,
  `TaskInfo` and its per-task stats, and the transcript `KindQueue` node.
  A turn's children are beats, cards, markers and pass-throughs in time
  order.
- A **transition marker** — one dim divider, `── 3/9 <todo content> ──` —
  precedes the first beat whose bound todo differs from the previous bound
  beat's todo.
- "Previous todo" persists across interruptions within a turn, so a stuck
  todo yields exactly one marker. It resets per turn: the first bound beat of
  each turn gets a marker.
- No marker for dropped todos, or when the list is shorter than
  `MinTaskTodos` (2).
- Completion is not shown inline; it shows in the band. The receipt's task
  count is unchanged.

**Folding (live turn):** all finished beats except the newest 3 fold to a
one-line header, `<narration> · 4 tools · 12s ▸`, expandable. A beat holding
a running card never auto-folds.

### 3. In-place lifecycle cards and tail markers

| Entity | Card anchored at | Updated in place by (no longer a row) |
|---|---|---|
| Subagent | its `agent.run` call (existing `KindSubagent` card) | the subagent report message, joined by subagent ID |
| Background job | the `shell.run` that started it | `KindJobExit`, joined by job ID |
| Await | its `agent.await` call | its own result |

- **Subagent, running:** title line `#3 <label> · 1m21s` with model/tokens/
  calls as meta, and one dim latest-activity line. No blank padding; the
  drill-in hint shows only when the card is focused.
- **Subagent, finished:** one line `✓ #3 <label> · 23 calls · 1m21s ▸`; the
  expanded result summary is markdown-stripped.
- **Job:** one line `job-4 · <command first line…> · running 1m`, settling to
  `✓ exit 0 · 2m02s` / `✗ exit -1`. The full command shows when expanded.
- **Await:** `waiting on #3 #5 · 18s` while live, settling to `waited 18s`
  (it no longer names completions).
- **Tail marker:** each completion emits a one-line dim `marker` node at its
  completion timestamp, in time order: `✓ #3 <label> done · 1m21s`,
  `✗ job-6 exit -1 · 2m02s`. Completions landing together merge into one
  marker (`✓ #3 #5 done`). Selecting a marker jumps to its card.
- The joins happen in `viewmodel` so both clients get them.

### 4. Pinned band

At most 1 + 1 + 4 lines above the input:

```
▰▰▰▱▱▱▱▱▱ 3/9 · Task 2 — AccessibleModal initialFocus     stale 12m · 2m34s
  ✓ 2 done · ▸ next: Task 3 — LoRA picker · Task 4 — VerdictControl  +3
  #3  Pass 1 Task 1 — compact bar   Recording the red run. · 1m21s
  job-4  pytest -q --junitxml=…   running · 1m02s
```

- Line 1: progress from todos only (count, active todo, elapsed), plus the
  stale tag (§4b) in dim amber.
- Line 2: collapsed done count, next waiting items on one line, `+N`. The
  full list opens via the session sheet.
- Entity rows: running subagents **and** background jobs, one line each, max
  4, then `+N more`. A row disappears when its entity finishes.
- Lines 1–2 are omitted when the list is shorter than 2 or the turn never
  touched it (`DrivenByTodos`); entity rows still show.
- The band is computed in `viewmodel` and shipped as its own payload, not
  derived separately by the TUI (`todo_strip.go`, `nowbar.go`) and `web/ui`.

### 4b. Stale rule and model nudge

One shared definition in `internal/app/session` (for example
`session.TodoStaleness(todos, steps, now)`), used by both the band and the
runner:

- **Stale** when the turn is busy, the list has an `in_progress` item, and at
  least **5 minutes and 8 model responses (steps)** have passed since the last
  `todo.write`.
- Not stale for an empty list, an all-completed list, or no in-progress item.

**Nudge:** on the first tool result after the threshold is crossed, the
runner appends, following the existing `repeatReminder` pattern
(`internal/agent/progress.go`):

```
<system-reminder>
Your todo list hasn't changed in 6m / 9 responses; "Task 2 — …" is still
in progress. Update it with todo.write to reflect what's done, or clear it if
the plan changed.
</system-reminder>
```

- Sent once per list version; a `todo.write` resets the clock. The latch is
  mutex-guarded, because read-only tool calls run in parallel.
- The quoted item is `%q`-quoted with its angle brackets escaped and truncated
  at 120 runes, so it cannot close the fence early or ride along whole; see
  Post-review hardening.
- Only the actor that owns the list (the orchestrator); never subagents.
- Not a transcript row. The band's stale tag clears on the next write.

### 5. Output polish

- Shell output: omit `stdout:`/`stderr:` labels when only one stream has
  content; never render an empty `stderr:` block; trim leading/trailing blank
  lines.
- Command previews (tool rows, job cards, band rows) truncate at the
  **first** line with `…`, never partway through a wrapped line.
- Collapsed shell rows show one line; a leading `cd <session worktree> && `
  is elided to `⌂ `.
- Expanded diffs keep their file header (`AGENTS.md +3 -2`) as the row title.
- Narration word-joining ("aslot", "thetodos", "finalverification"):
  investigated; the endpoint emitted it. See Findings.

### 6. Wire contract

`internal/viewmodel/wire.go`:

- **Removed:** node kinds `task`, `queue` and `jobExit`; step fields `inferred`
  and `heuristic`; the `WireJobExit` payload.
- **Changed:** the wire kind stays `step`, and a beat keeps the key
  `step:<firstStepID>` — a beat is a `KindStep` node that merged responses, and
  keeping the key preserved expand state and the render cache for free. A beat
  adds `stepIds: []` (the merged responses, first first), `todo?: {id, index,
  total, content}` (transition beats only) and `folded: bool`.
- **New:** a `marker` node `{refs: [cardKey], labels: [], failed?, at?,
  durationMs?}` recording the moment a card finished. Markers with nothing
  between them merge, and a marker that lands inside an open beat is held until
  the beat ends so it cannot split the work around it.
- **Tool rows:** a background job's card is its own `shell.run` row, settled in
  place, carrying `tool.job: {id, command, running?, exitCode, durationMs?,
  output?, truncated?}`.
- **Band:** travels as its own node — ID `band`, kind `band`, deliberately not
  in `roots`, so a transcript walk ignores it while the existing stack-patch
  protocol carries it unchanged. `{todos?, done, total, active?, stale?,
  staleAgeMs?, next[], entities: [{ref, label, detail?, startedAt?}]}`.
- **Subagent cards:** unchanged on the wire. They now render as one activity
  line while running, and the completion notice they used to duplicate arrives
  as a `ContentTypeSubagentNotice` message that the tree drops.

No compatibility shim: `cmd/webbridge` embeds the SPA built from the same
commit. `web/ui` updates `src/lib/stack.ts`,
`src/lib/transcript/StepHeader.svelte`, `src/lib/transcript/TurnNode.svelte`
and the band rendering, then `npm run build`. `web/` stays standard-library
only and consumes the JSON contract.

## Testing

- `viewmodel` table tests built from the screenshot scenarios:
  - narration → thinking → narrationless tool call stays one beat;
  - a todo stuck `in_progress` across 20 beats with interleaved subagent
    notices yields exactly one transition marker;
  - a dropped todo yields no marker;
  - job start + `JobExit` → one card + one tail marker;
  - a subagent report message joins its card; simultaneous completions merge
    into one marker;
  - folding keeps the newest 3 beats and any beat with a running card.
- `session` staleness boundary tests (4m59s / 5m, 7 / 8 responses, no
  in-progress item, all completed).
- Runner nudge: sent once per list version, never to subagents, appended to a
  tool result.
- TUI: update goldens (`depth_golden_test.go` and related); frame tests for
  the band line cap and first-line preview truncation.
- Each anti-duplication fix gets a mutation check: revert it and confirm a
  test fails.
- Full `CGO_ENABLED=1 go test ./...` locally before declaring done (CI runs the
  full suite only on release tags).

## Rollout

Each stage lands independently, with `web/ui` parity in the same change as
any wire change:

1. Beats; delete inferred headlines and heuristic steps.
2. Delete task/queue nodes; transition markers; folding.
3. Lifecycle cards and tail markers.
4. Band payload and stale tag; then the runner nudge.
5. Output polish.

## Decisions during implementation

The design above describes the intent; these are the places the code
deliberately differs from it.

1. **The wire kind stays `step`; beat keys stay `step:<firstStepID>`.** The
   design said `beat` / `beat:<id>`. Renaming would have touched ~15 web sites
   for no user-visible gain, and keeping the key preserved expand state and the
   render cache across the change. A beat is a `KindStep` node whose
   `StepInfo.StepIDs` lists every response it merged.
2. **The band ships as an extra wire node** with ID `band` and kind `band`,
   not as a field on the tree. It is not in `roots`, so transcript rendering
   ignores it and the existing ACP diff/patch machinery carries it with no
   protocol change.
3. **The TUI band is the existing now bar** (`internal/app/tui/nowbar.go`),
   which already held a progress row plus subagent/job/watch rows. The top todo
   strip (`renderTodoStrip`) and the waiting band (`renderTodoBand`) were
   deleted; the band's checklist row replaces both. `todo_strip.go` is gone.
4. **Job exits are recorded agent-side.** `JobManager.SetOnExit` is wired in
   `internal/app/app.go` to `state.AddJobExit`, replacing the TUI's job-count
   diff, so an ACP or web session sees a card settle too.
5. **The drill-in footer is gone from running subagent cards** rather than
   shown on focus: a card that is a status row should not carry a keyboard
   hint. `ctrl+f` stays documented in the browse help.
6. **`[tui.transcript] fold_finished_tasks` and the `z` toggle are kept.**
   They now gate beat folding; only the flash text changed
   (`Fold finished beats: on/off`).
7. **Run-together narration words were not ours to fix.** See Findings.

## Post-review hardening

A code and security review of the finished branch raised four issues; all
four are fixed. None of them changed the transcript's structure, the shape of
the wire contract, or any user-visible layout.

### Terminal control sequences in model-authored text

Display text in the transcript comes from the model: a todo item's content, a
subagent's label, the command a job runs, a job's own output. It was rendered
verbatim, so a model that emitted a terminal escape sequence was writing
straight into the terminal — OSC 52 (clipboard write), OSC 0 (window title),
OSC 8 (hyperlink), CSI 2J (screen clear) — and the transcript repaints once
per frame.

`viewmodel` gains two scrubbers, applied at the projection boundary so the
TUI, ACP and web all receive the same text:

- `ScrubText` for single-line rows. It also flattens `\n`, `\r` and `\t` to
  spaces, so a todo containing a line break cannot split a one-line band row
  and make the height budget undercount.
- `ScrubBlock` for text whose line structure *is* the content — an expanded
  job's output, a subagent's summary. It keeps `\n` and `\t` and drops `\r`,
  so a CRLF collapses to one break instead of double-spacing.

Both drop C0 (including ESC), DEL and C1, leave every printable byte alone (a
path, an emoji and a CJK glyph survive byte-for-byte), and are idempotent:
`Build` scrubs once and the renderers scrub again at their own boundary.
Neither is used to compute a node ID, a key or a filter — identity always
comes from the raw value, so scrubbing can never merge two rows into one.

The TUI renderers that read the session directly rather than through the
projection scrub at their own boundary too: the job card, the completion
marker, the todo divider, the subagent card (a live region — it repaints on
every spinner tick) and the band's rows. `web/ui` needs no change: it renders
the wire payload, which is already scrubbed.

### Guards on exported helpers

Three exported helpers indexed or dereferenced data that only `Build`
guarantees:

- `BandOf` read `n.Tools[0].Timestamp` for a running job. `Build` always
  attaches exactly one call to a job card, but a tree that has been rebuilt,
  filtered or merged need not. The new `Node.JobStart()` returns the zero time
  instead, and the row renders with its elapsed time unknown.
- `markerNode` took `m.Refs[0].Key` and paired each ref with `m.Labels[i]`. A
  marker with no refs now falls back to a key derived from its own timestamp
  (rather than the empty key every such marker would otherwise share), and the
  version hash tolerates a label slice shorter than the ref slice.
- `StepHeadline` called `strings.TrimSpace(m.Content)` on every narration
  entry; a nil entry is now skipped.

### The nudge's fence, its bound and its latch

Three changes to the stale-todo reminder (§4b):

- The item's content was interpolated into a literal `<system-reminder>` fence
  with `%q`, which does not escape angle brackets. An item containing the
  closing tag ended the fence early, and everything after it was read as
  marshal's own voice. `nudgeQuote` now writes `<` and `>` as `\u003c` and
  `\u003e`, so the fence closes exactly once, at the end.
- The same content was unbounded, and the reminder is appended once per list
  version, so one enormous item rode along whole every time it fired. The
  quoted item is now truncated at 120 runes.
- `todoClock` and `todoNudged` were plain fields read and written from the
  parallel read-only tool phase, so two calls in one batch could each pass the
  latch and append the reminder twice. Both are now guarded by `todoMu`,
  following `trackerMu`'s convention for per-turn state reached from tool
  goroutines.

Tests: `TestRenderersDropTerminalControlSequences` asserts on the **raw**
render — never through `ansi.Strip`, which removes escape sequences and would
therefore pass whether or not a renderer leaked them — and bans ESC-introduced
sequences other than the renderer's own SGR styling, which a correct render is
full of. `internal/viewmodel/scrub_test.go` pins the scrubbers' byte-fidelity
and idempotence, `hardening_test.go` the three guards, and
`todonudge_fence_test.go` the fence, the bound, and 16 concurrent callers
firing the reminder exactly once.

## Findings

### Run-together narration words (Task 22)

**Verdict: the endpoint emitted them. Nothing in marshal drops the space, and
no client-side fix could tell a dropped space from one the model never sent.**

Checked for a join in our own path, in the order a delta travels:

- `openai_compatible.go` (`streamChatEvents`) forwards `choice.Delta.Content`
  to the think parser and emits the returned content unchanged. No trimming.
- `think_parser.go` only ever splits its buffer into "emit now" and "hold back
  in case this is a split tag". Both halves are preserved and in order, so the
  holdback cannot swallow a leading space.
- `ollama_native.go` and `anthropic.go` emit `Content` / `text_delta` text
  verbatim; the reasoning shapes are a separate channel.
- `agent/chat.go` accumulates content deltas with `sb.WriteString(event.Delta)`
  and thinking deltas on their own buffer. No per-chunk `TrimSpace` anywhere in
  the delta path (a repo-wide search for one comes back empty).
- `runner.go` stores narration as `strings.TrimSpace(res.Text)`, which trims
  only the ends of the whole message, never interior text.
- There is no content reassembly or re-stitching path that could rejoin split
  tokens without a separator.

The stored data agrees. Across the project database, 5,559 narration rows hold
14 matches for a probe list of known joins (`thetests`, `thetest.`, `thecode.`,
`theplan`, `thefile`, `thetask`, `thetodos`, `aslot`, `finalverification`), in
two sessions, both of which ran fast chat models on a hosted endpoint. Every
one sits mid-sentence, e.g. `… not the real errors. Let me verify the API names
I used and run thetests.` and `… my test's precondition was wrong, not thecode.`

Two tests pin the client side so a future regression cannot be mistaken for
this: `TestChatStreamingPreservesSpaceAcrossChunkBoundary` (a space arriving on
a chunk boundary survives the SSE path) and
`TestInlineThinkParserPreservesSpacesAroundReasoningBlocks` (the tag holdback
does not eat adjacent spaces). Both live in
`internal/llm/provider/openai_compatible_test.go`.

If it recurs, settle it with raw bytes rather than inference: set
`MARSHAL_WIRE_CAPTURE` (response) and `MARSHAL_WIRE_CAPTURE_REQUESTS` (request)
and compare the captured chunks with the stored narration. That shows the
endpoint's exact deltas for the suspect message.
