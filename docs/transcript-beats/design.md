# Transcript beats: fact-driven transcript, todos as labels

Date: 2026-10-10
Status: design approved in brainstorm; awaiting spec review

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
Your todo list hasn't changed in 6m / 9 responses; "<active item>" is still
in progress. Update it with todo.write to reflect what's done, or clear it if
the plan changed.
</system-reminder>
```

- Sent once per list version; a `todo.write` resets the clock.
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
- Narration word-joining ("aslot", "thetodos", "finalverification"): first
  confirm whether streamed narration chunks lose a boundary space in our join
  or the provider emits them that way; fix at the confirmed cause.

### 6. Wire contract

`internal/viewmodel/wire.go`:

- **Removed:** node kinds `task`, `queue`, `jobExit`; step fields `inferred`,
  `heuristic`.
- **Changed:** `step` → `beat` with `stepIds: []`, `todo?: {id, index, total,
  content}` (transition beats only), `folded: bool`.
- **New:** `marker` node `{refs: [cardKey], status, ended}`.
- **Card fields:** subagent and job cards gain `status`, `exit`, `ended`.
- **Band payload:** `band: {progress, active, stale?, next[], entities[]}`
  alongside the tree.

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
