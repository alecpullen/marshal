# Single Stack TUI — Feature Specification

Status: approved direction (B, "Steps and tasks"), 2026-10-03
Mockups: [`mockups.html`](mockups.html) (open in a browser). The page's "Decisions for you"
section predates §4 below; §4 is authoritative.
Base commit: `633fd21`
Phase specs:
- P1 `p1-layout-and-keys.md`
- P2 `p2-steps-and-ownership.md`
- P3 `p3-tasks-and-navigation.md`

Execution plans: not yet written. Each phase spec is self-contained so its
plan can be written and run independently.

---

## 1. Summary

Marshal's TUI becomes a single-column stack designed for terminals that cover
half a screen or less. The side rail is removed. The transcript is
restructured from a flat list of sibling rows into a hierarchy:

```
turn → task → step → tool call
```

A **step** is one model response. Its narration becomes the step headline,
and the tool calls from that response sit indented beneath it. A **task** is
a todo-list item: when the agent marks it in progress, its steps group under
a task header, and the task folds to one line when it completes. Each step
shows who did it (orchestrator, subagent, or pipeline role) in a
right-aligned label.

Everything live is consolidated into one **now bar** above the input. Every
item has more detail available through four density levels and a keyboard
browse mode entered with Esc. Cancelling a turn moves from a single Esc to a
double Ctrl+C, and quitting also needs a double Ctrl+C.

## 2. Goals and non-goals

### Goals

1. **Single column at every width.** Works from the existing 80×24 minimum
   (`minTerminalWidth`/`minTerminalHeight`, `tui/model.go:148`) up. There is
   no side column at any width.
2. **Progression is visible.** You can tell where the agent is in its plan
   from the bottom of the screen (now bar) and from the scrollback (task
   headers).
3. **Ownership is visible.** Every step shows its owner when the owner is
   not the orchestrator. Subagent cards show what the subagent says it is
   doing.
4. **Tool calls are attached to narration.** Each tool call renders under
   the sentence that explains it. If no sentence exists, Marshal writes a
   labelled inferred headline.
5. **Detail on demand, everywhere.** Every item can be expanded, inspected
   or copied from the keyboard. The mouse remains optional.
6. **Hard to cancel or quit by accident.** Both need two deliberate
   presses.

### Non-goals

- A split-pane or multi-column layout (direction C was rejected).
- Changing the input box, the approval/question form mechanics, or the
  slash command set beyond the keys listed in §6.
- ACP / web UI / `/export` adoption of the step tree. This is a follow-up
  (§10). The data model is designed so they can adopt it later.
- Theming changes. The existing Warm Sunset palette and `tui/glyph`
  vocabulary are used. The only new glyphs are `▰ ▱` (U+25B0/25B1).

## 3. Background: findings in the current code

These facts shaped the design. File references are at `633fd21`.

| # | Finding | Where |
|---|---|---|
| F1 | The rail renders only at ≥120 columns (`min_width = 120`, 25% width). In a half-width window it never appears. | `tui/sidepanel/geometry.go`, `config/defaults.go:188` |
| F2 | Progress is split across six surfaces: run bar, turn spinner, todo panel, live strip, activity lane and the status-line cluster. Each renderer has a separate `*Rows()` height helper that must agree with it. `resize()` subtracts nine heights, and `clipLeftColumn` exists to recover from mismatches. | `tui/view.go:72–110`, `tui/model.go:1558` |
| F3 | Narration and tool calls are unrelated rows. `State.Transcript()` merges six logs sorted by timestamp. Only consecutive same-tool calls are grouped. | `session/session.go:1584`, `tui/toolgroup.go` |
| F4 | `AuditEvent.AgentRole` and `AuditEvent.Model` exist, but **no code populates them**. Narration messages have no actor at all. | `tools/registry/audit.go:19`, `agent/execute.go:844` |
| F5 | In envelope (JSON action) mode, the model is *required* to send a `rationale`, but the runner discards it, so envelope-mode models never show narration. | `agent/protocol.go:159`, `agent/envelope_schema.go:49` |
| F6 | Resumed sessions restore messages only. `GetToolCalls` exists but nothing loads tool calls back into `auditLog`, so a resumed transcript shows narration with no tool rows. | `session/messages.go:160–230`, `db/audits.go:175` |
| F7 | Envelope mode runs read-only actions concurrently (`MaxParallelActions`), but `State` tracks a single `ActiveToolCall`. ACP pairs running calls to finished calls by tool name. | `agent/execute.go:813`, `acp/turn.go:349–375` |
| F8 | Expanding a single item needs a mouse click. The keyboard only has Ctrl+G (expand everything). Expand state is keyed on `(timestamp, kind)`. | `tui/expand.go`, `tui/click.go` |
| F9 | `TodoItem` is `{Content, Status}`, with no ID and no timestamps. | `db/todos.go:9` |
| F10 | A single Esc cancels a running turn. When idle, the first Ctrl+C quits immediately. | `tui/keypress.go:147–176`, `tui/model.go:1720–1745` |

## 4. Decisions

| Question | Decision |
|---|---|
| Direction | B: steps and tasks. |
| Fold finished tasks by default | Yes. A task with an unresolved failure, or one the user expanded, stays open. |
| Narration prompt | Always included by default (`agent.narration_prompt = true`). An intent-only-final nudge guards against the main failure mode (§7). Inferred headlines cover models that still don't narrate. |
| Browse mode key | **Esc**, when no overlay is open. Esc never cancels a turn. |
| Cancel turn | **Ctrl+C twice** within 3 s while busy. |
| Quit | **Ctrl+C twice** within 3 s while idle. `/quit` and `/exit` are unchanged. |
| Owner label placement | Right-aligned on the step header row. |
| Rail | Deleted. Sections are re-hosted in a session sheet (Ctrl+B). |
| Phase independence | P1 is TUI-only and ships alone. P2 adds the data model. P3 depends on P2. |

## 5. UX specification

### 5.1 Screen zones

From top to bottom, there are no side columns:

1. **Transcript.** Scrollable viewport, the only flexible-height zone.
2. **Now bar.** 0–4 rows, live work only. Height 0 when idle.
3. **Dock panel**, when one is open (unchanged host: `tui/dock`).
4. **Input area.** Unchanged, including approval/question/skill-gate takeovers.
5. **Status line.** One row.

The run bar (`renderRunPanel`) no longer renders above the transcript. It
moves into the now bar.

### 5.2 Transcript hierarchy

```
❯ user prompt                                     (turn)
  ✓ 1/4 Read the parser…        3 steps · 41s ▹   (task, folded)
  ─ 3/4 Cover the guard with tests ───── 1m18s    (task, open)
  ✗ Running the package tests…           6s       (step)
        › go test ./internal/parser/… · exit 1 ▹  (tool row)
  ▍ final answer                                  (final message)
  ✓ done · 6m40s · 4 tasks · 11 steps · …         (turn receipt)
```

Indentation follows the existing contract in `tui/transcript.go`:

- Step headers sit at the gutter (`gutterWidth = 3`).
- Tool rows, thinking rows and narration continuation sit at
  `nestedBodyIndent = 5`.
- Expanded bodies sit behind `nestedRail()`.

Items that are not steps keep their current renderers and positions. These
are user messages, final answers, system notices, run events, job exits,
compaction markers, steering markers and skill tags. A run event is shown as
a controller-owned step in plan runs (P2).

### 5.3 Step anatomy

```
 <state> <headline>                                 <owner> · <duration>
         <narration continuation, muted>
     ⚙   thought for 2s ▹
     <cat> <tool subject> · <result summary>            <meta> <disclosure>
```

- **State glyph:** `⠋` (spinner frame) while running, `✓` settled ok, `✗`
  if any tool row in the step failed (error, non-zero exit, denial), `·`
  for a step with no tool calls.
- **Headline:** the first sentence of the narration, in FGEmphasis. A
  sentence ends at the first `. `, `! `, `? `, or newline. When the rest
  is longer than a single sentence it is shown as a muted continuation at
  the steps density.
- **Inferred headline:** used when the step has no narration. It is built
  from the step's tool rows (for example `read 2 files · searched
  "ErrEmpty"`), rendered dim italic, and the right meta starts with
  `inferred`.
- **Right meta:** `owner · duration`.
  - The owner is omitted for the orchestrator.
  - The model is added (`owner · model @ provider`) only when it differs
    from the session's active route.
  - Drop order as width shrinks: model, duration, then owner (shortened to
    the role name).
  - The meta is never allowed to push the headline below 24 columns. Only
    in that case is the owner dropped.
- **Tool rows:** reuse the current row logic in `renderCompletedToolCall`
  (subject-first shapes, diff stats, callers line, hook hints). Repeated
  same-tool calls merge within a step only.
- **Thinking:** shown as a row inside the step, not as a separate block.
  While live, the bounded `liveregion` thinking box renders inside the live
  step at nested indent.
- **Empty steps** (no narration, no tools, no thinking) are not rendered.
  These come from length-finish retries and nudges.

### 5.4 Tasks (P3)

- A task header is rendered for each todo that had at least one step while
  it was in progress.
  - Open: `─ N/M <content> ──── <elapsed>`
  - Folded: `✓ N/M <content>  <k> steps · <tools/edits> · <duration> ▹`
- Folding rule: a task folds when its todo is completed unless (a) the user
  expanded it, or (b) it has an **unresolved failure**: its last step that
  ran a shell/test tool exited non-zero, or its final step contains a tool
  error.
- Steps taken while no todo was in progress sit directly under the turn.
- Todos removed by a todo rewrite keep their header, shown as `dropped`.
- `todo.write` calls are not shown as tool rows. The task headers replace
  them.
- **Turn receipt:** after the final answer, one line:
  `✓ done · <duration> · <tasks> · <steps> · <tools> · ±<files> · <tokens>`.

### 5.5 Ownership

- **Actor identity:** `Actor{Role, Label, Model, Provider}`.
  - Orchestrator: empty role.
  - Subagents: `reviewer #1` (label from the dispatch).
  - Pipeline roles: `implementer`, `reviewer`, `branch reviewer`.
  - Custom agents: the agent name.
- **Actor colour:** each non-orchestrator role gets a stable colour from
  `{AccentSecondary, AccentTertiary, StatusInfo}`, chosen by role-name
  hash. The label text is always rendered, so NO_COLOR works.
- **Subagent cards (P2):** the live card body is the child's latest step
  headline and a one-line tool summary, replacing the raw
  `SubagentActivityTail`. A settled card shows the child's final summary
  headline.
- **Approvals (P2):** the approval panel shows the owner and the
  requesting step's headline as `why  "<headline>"`. If the step has no
  narration, the row is omitted. It is never filled with an inferred
  headline, because inferred text isn't the agent's reason.

### 5.6 Now bar

The now bar is one component. A single function produces a **row plan**
that is used both to render the bar and to measure its height.

| Row | Shown when | Content |
|---|---|---|
| Live mirror | The viewport is scrolled away from the live step (`!viewportFollow`) and a step is running | `↓ <live step headline> · <spinner> <tool glyph>` … `End` |
| Progress | A plan run is active, otherwise a todo list exists with unfinished items | Plan run: `▰▰▰▱▱▱▱ task 4/7 · verifying 2m14s · fix 1/2 · ~6–25m left` (+ optional stage row). Todos: `▰▰▰▱ 3/4 <in-progress content> … elapsed` |
| Turn | Busy, no progress row and no live step visible | `⠋ working · step N · <current tool>` … elapsed |
| Actors | Running subagents, jobs, watches | One row each, `⧉ reviewer #1  <headline>  38s`, `┆ job-3 …`, `○ watch …` |
| Overflow | More actors than fit | `… N more` |

- **Height:** at most 4 rows.
- **Short frames:** below 30 rows, the bar collapses to one summary row:
  `▰▰▰▱ 3/4 <short> · ⧉2 ┆1 · 4m21s`.
- **Idle:** with no live work, the bar has 0 rows.
- Every row wears the dim `▍` rail, like the current lanes.

### 5.7 Status line

- **Kept:** mode, untrusted, route, local, think, ctx %, branch, dir.
- **Added (P1):** `±N files` from the changed-files cache, priority
  between ctx and branch.
- **Hint cluster:** updated for the new keys.
- **Ctrl+C armed:** the right cluster is replaced by a warning-coloured
  `Ctrl+C again to stop the turn` / `Ctrl+C again to quit`.

### 5.8 Session sheet (Ctrl+B)

The session sheet is a docked panel (`dock.Docked`) that renders the former
rail sections stacked vertically.

- **Sections:** context, changed, session usage, skills, rules, worktrees
  (fleet), working set, tools, repo, plus swarm/sdd when active.
- **Navigation:** ↑/↓ moves between sections. Enter runs the section's
  related command (`/context`, `/diff`, `/worktrees`, `/skills`, `/log`,
  `/run`, `/agents`). Esc closes the sheet.
- **Data:** cached on turn boundaries, as the rail's data is today. Opening
  the sheet triggers one refresh.

### 5.9 Detail ladder (P3)

| Density | Step | Tool row | Task |
|---|---|---|---|
| outline | Headline only, with a right-aligned tool count | hidden | Header only |
| steps (default) | Headline + continuation | One row each. A failure shows up to its first 3 output lines. | Open/folded per rule |
| full | Full narration + full thinking | Full output/diff | Open |

- **Global level:** Ctrl+G cycles outline → steps → full.
- **Per-node override:** Enter in browse mode, or a mouse click. It
  persists until the next Ctrl+G.
- **Inspector:** a full-frame dock panel for one step or tool call. It shows:
  - actor, model/provider, the step headline as "why"
  - started-at, duration, exit code
  - sandbox backend/network, approval state and rule, hooks, rewritten args
  - finish reason, tool-call ID, pretty-printed args, full output
  - keys: Tab moves between sections, `y` copies, `o` opens the file, `n`/`p` go to the next/previous node.

### 5.10 Browse mode (P3)

- **Enter:** Esc with no overlay to close and an empty completion state.
  The input text is preserved. The cursor starts on the newest step, or on
  the newest node if there are no steps. The status line mode segment
  shows `BROWSE`.
- **Keys:**

| Key | Action |
|---|---|
| `j`/`k`, `↓`/`↑` | Next/previous node (step, tool row, subagent card, message, run event) |
| `J`/`K`, `]`/`[` | Next/previous task header or turn |
| `g`/`G` | First/last node |
| `Enter` | Cycle this node's density (subagent card: drill in) |
| `i` | Inspector |
| `y` | Copy the node's plain text (output, diff, or message) via OSC 52 |
| `o` | Open the node's file at line in `$EDITOR` (only when `$EDITOR` is set and the node has a path) |
| `f` | Drill into the subagent under the cursor |
| `z` | Toggle folding of finished tasks for the session |
| `Esc` | Leave browse mode |
| any printable key | Leave browse mode and type that key into the input |

- **Scrolling:** the viewport scrolls to keep the cursor visible. While in
  browse mode, follow mode is off until the user presses `G` or `End`.
- **Mouse:** a click toggles the node under it, as today. In browse mode
  the click also moves the cursor there. A click alone never enters
  browse mode.

### 5.11 Keybindings (final state)

| Key | Before | After | Phase |
|---|---|---|---|
| Esc | Cancel turn (busy); close popup/drill/notice | Close the topmost overlay (popup → suggestion → drill → notice). With nothing to close: P1/P2 no-op, P3 enters browse mode. **Never cancels.** | P1, P3 |
| Ctrl+C | Busy: interrupt on 1st press, quit on 2nd. Idle: quit on 1st press | Busy: arm on 1st press, stop turn on 2nd within 3 s. Idle: arm on 1st, quit on 2nd within 3 s. Any other key disarms. | P1 |
| Ctrl+G | Expand all on/off | P1–P2 unchanged. P3: cycle density | P3 |
| Ctrl+T | Todo panel expanded/collapsed/hidden | Open the Tasks dock panel (full todo list with status and timings) | P1 |
| Ctrl+B | Rail show/hide | Session sheet open/close | P1 |
| ↑/↓ on empty input | Move agent-lane cursor (F6) when the lane is shown | Prompt history only. The lane is gone, so drill in with Ctrl+F or browse `f`. | P1 |
| Ctrl+F, Ctrl+X, Ctrl+R, Ctrl+S, Ctrl+O, Ctrl+P, Ctrl+K, `?` | — | Unchanged | — |
| Esc in approval / question / skill gate | deny / skip / — | Unchanged (form-local) | — |

## 6. Architecture

### 6.1 Data model (P2, P3)

```go
// internal/app/session
type StepID int64 // 1-based sequence within a session; 0 = none

type Actor struct {
    Role     string // routing.AgentRole; "" = orchestrator
    Label    string // display label: "reviewer #1", "implementer"
    Model    string
    Provider string
}

type Step struct {
    ID        StepID
    TurnMsgID int64 // in-memory ID of the user message that opened the turn
    Actor     Actor
    TodoID    string // P3: in-progress todo when the step began
    StartedAt time.Time
    EndedAt   time.Time
}
```

- **New fields:**
  - `Message.StepID` (narration only)
  - `registry.AuditEvent.StepID`
  - `registry.AuditEvent.ToolCallID`
  - `ThinkingEntry.StepID`
  - `ActiveToolCall.StepID`
  - `ActiveToolCall.ToolCallID`
- **Population:** `AuditEvent.AgentRole` and `AuditEvent.Model` become
  populated (F4).
- **Explicit stamping, no ambient "current step":**
  - `State.BeginStep(actor) StepID` returns an ID.
  - The runner stamps that ID onto every event it creates.
  - `State.EndStep(id)`.
  - This is race-free when several runners share one `State` (pipeline
    roles that log to the parent session).
- **Active tool calls:** `State` tracks a map of active tool calls keyed by
  `ToolCallID` (F7). `ActiveToolCall()` keeps its current
  most-recent-wins behaviour for existing callers.
- **TodoItem (P3):** `{ID, Content, Status, StartedAt, CompletedAt}`. The
  JSON encoding stays compatible with old rows.

### 6.2 Persistence (P2)

Migration appended to `db/migrations.go`. It follows the existing
`ALTER TABLE … ADD COLUMN` pattern.

```sql
CREATE TABLE IF NOT EXISTS steps (
  session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  turn_message_id INTEGER,            -- messages.id (DB id) of the turn's user message
  actor_role TEXT, actor_label TEXT, model TEXT, provider TEXT,
  todo_id TEXT,                        -- P3
  started_at TEXT NOT NULL, ended_at TEXT,
  PRIMARY KEY (session_id, seq)
);
ALTER TABLE messages   ADD COLUMN step_seq INTEGER;
ALTER TABLE tool_calls ADD COLUMN step_seq INTEGER;
ALTER TABLE tool_calls ADD COLUMN tool_call_id TEXT;
```

- **Resume (fixes F6):** load the steps whose `turn_message_id` is on the
  active branch, then load the `tool_calls` rows with those `step_seq`
  values into `auditLog`.
- **Legacy rows:** tool calls with no `step_seq` are not restored. This
  matches today's behaviour.

### 6.3 Runner (P2)

- `Runner.Actor` is set by each runner factory: the main agent,
  `pipeline.Dispatcher`, the swarm orchestrator, the subagent factory and
  custom agents.
- Each loop iteration that calls the model is one step:
  1. `BeginStep` before the request.
  2. Stamp the step ID on narration, thinking, the active tool call and
     audits.
  3. `EndStep` after tool execution returns.
- `logToolCall` (the existing single choke point) stamps `StepID`,
  `AgentRole` and `Model`.
- **Envelope mode:** a non-empty `rationale` on a non-final action is stored
  as `ContentTypeNarration` (fixes F5).
- **Narration prompt:** a short directive in `buildSystemPrompt`, native
  mode only. Envelope mode already requires `rationale`.
- **Intent-only-final nudge (§7):** one nudge per turn, in the same pattern
  as `groundingNudgeMessage` and `verificationNudgeMessage`.

### 6.4 TUI view model (P2)

New package `internal/app/tui/stack`:

- `Build(Snapshot) *Tree` is pure. It turns session data into
  `Turn → [Task →] Step → Row` nodes plus pass-through nodes (messages,
  run events, job exits, subagent cards).
- **Grouping by `StepID`:** with a heuristic fallback for items that have
  none (legacy sessions):
  - A narration message opens a step.
  - Following audits and thinking entries join it, until the next
    non-narration message or the next narration.
  - Audits before the first narration in a turn form an unnarrated step.
- **Node IDs:** stable `NodeID{Kind, Key}` with keys `turn:<msgID>`,
  `task:<todoID>`, `step:<seq>` or `hstep:<firstItemTs>`,
  `tool:<toolCallID|ts>`, `msg:<id>`, `sub:<id>`, `run:<ts>`, `job:<id>`.
  These replace `itemKey{ts, kind}`.
- **Render cache:** keyed by `NodeID`. An entry is reused when
  `(version, width, density, expanded)` is unchanged. Live nodes are marked
  `live` and re-render on spinner ticks. Settled nodes render once.
- **Output:** `refreshViewport` concatenates the cached output and records
  `[startLine, endLine)` per node for click and browse hit-testing.

### 6.5 Now bar (P1)

New file `internal/app/tui/nowbar.go`. It lives in the `tui` package
because it reuses the run-panel and ETA formatters (`runPanelSummaryLine`,
`estimateRemaining`, `formatETA`), which are package-private.

- A pure `planNowBar(nowBarInput) nowBarPlan` and
  `renderNowBar(nowBarPlan, width) string`. This is the same plan/render
  split as `lanePlan`.
- `nowBarInput` is a value snapshot built by `Model.nowBarInput()`. It
  holds SDD progress, swarm progress, todos, running subagent views, job
  and watch texts, the busy flag, spinner frame, now, active tool, live
  step headline (P2), `viewportFollow`, and frame width and height.
- Replaces:
  - `renderTurnSpinner`, `renderTodoPanel`, `renderLiveStrip`,
    `renderActivityLane`, `renderRunPanel`
  - `turnSpinnerRows`, `todoPanelRows`, `liveStripRows`, `laneRows`,
    `runPanelRows`

The name is "now bar" in code to avoid colliding with the existing `dock`
package. The mockups call it "now dock".

### 6.6 Removed or relocated

- **Deleted:**
  - `sidepanel.Rail`, `fit.go`, `geometry.go`, `telemetry.go`
    (`telemetry.go` only if it is used by the rail alone)
  - `Model.railWidth`, `Model.railHidden`, `Model.rail`
  - `laneCursor`, `laneCursorActive`
  - `todoPanelMode`
- **Moved:** the section implementations move to
  `internal/app/tui/sessionsheet`.
- **Renamed:** the rail caches (`railTurns`, `railTotals`, `railChanged`,
  `railFleet`, `railRepoStats`, `railBaseRef`) are renamed `sheet*`.
  `railChanged` also feeds the `±N files` status segment.
- **Width:** `leftWidth` always equals `width`. Renaming it is a mechanical
  follow-up, not required.
- **Config:** `[tui.side_panel]` is still parsed. A config diagnostic says
  it is ignored and can be removed.

### 6.7 Config

| Key | Default | Phase |
|---|---|---|
| `[tui.side_panel]` | deprecated, ignored, diagnostic | P1 |
| `agent.narration_prompt` | `true` | P2 |
| `agent.intent_nudge` | `true` | P2 |
| `tui.transcript.density` | `"steps"` (`outline`, `steps`, `full`) | P3 |
| `tui.transcript.fold_finished_tasks` | `true` | P3 |

All new keys get `/settings` fields in the settings registry.

## 7. Narration prompt: risks and mitigations

Including the prompt up front is cheap and recommended. Expected costs and
failure modes:

1. **Token cost: negligible.** The directive is about 50 input tokens and
   is prompt-cached with the system prompt. Narration costs roughly 10–30
   output tokens per step, a small fraction of a typical tool result.
2. **Intent-only finals (the real risk).** Some local models obey "say what
   you're about to do" and then stop. They emit only the sentence, with
   `finish_reason=stop` and no tool call. Today the runner treats a
   text-only native response as the final answer (`runner.go:1272–1302`),
   so the turn ends early. Mitigations:
   - The directive says the sentence must be in the *same response* as the
     tool calls, and is never sent on its own.
   - The **intent-only-final nudge** fires at most once per turn, when a
     text-only response is short (≤ 240 chars, ≤ 2 sentences), the turn
     has already made at least one tool call, and the text matches a
     forward-intent pattern (`^(I'll|I will|Let me|Next,? I|Now I|I'm going
     to|Going to)\b`, case-insensitive). The nudge says: `You said what you
     would do next but did not call a tool. Call the tool now, or give your
     final answer.` A model that really was finished just repeats its
     answer, costing one round trip.
   - Turn metrics count `IntentNudges`, so `/doctor` can flag a model that
     triggers the nudge often.
3. **Templates that drop content alongside tool calls.** Some chat
   templates discard `content` when `tool_calls` are present. Narration is
   then silently absent. The inferred headline covers this. No harm.
4. **Reasoning models** may keep intent in the reasoning channel and emit
   no text. This is the same as (3). Reasoning is not promoted to a
   headline because it is private and unreliable as a summary.
5. **Verbosity.** Some models narrate a paragraph. The headline is the
   first sentence, and the rest folds.
6. **Envelope mode** already requires `rationale`. The only change is to
   render it (F5). Its prompt is unchanged.
7. **Provider quirks.** Kimi rejects empty assistant content, so narration
   helps there. No provider is known to reject content alongside tool
   calls.

Draft directive (native mode):

> When you call tools, begin that same response with one short sentence
> saying what you are about to do and why — for example: "Reading the parser
> to find where empty input is handled." Never send that sentence on its
> own without the tool calls. Do not add it to your final answer.

## 8. Phasing

| Phase | Scope | Depends on | Ships alone? |
|---|---|---|---|
| **P1 Layout and keys** | Rail removal, session sheet, now bar, status `±N files`, Tasks panel (Ctrl+T), Esc/Ctrl+C rebinding, config deprecation | — | Yes. TUI only, no schema change. |
| **P2 Steps and ownership** | Step data model and persistence, resume restores tool rows, runner stamping, actor population, envelope rationale, narration prompt and nudge, `tui/stack` tree with node cache, step renderer, inferred headlines, owner labels, subagent headlines, approvals with "why", now bar live mirror and actor headlines, ACP `toolCallId` pairing | P1 (now bar). The data-model half does not depend on P1. | Yes |
| **P3 Tasks and navigation** | Todo IDs and timestamps, step→todo binding, task headers and folding, turn receipt, density ladder, browse mode on Esc, inspector, `y`/`o`/`z` | P2 | Yes |

P2 splits cleanly into a data/agent half and a TUI half. The data half
(step model, persistence, runner stamping, narration prompt, ACP pairing)
does not depend on P1 and is invisible until the renderer uses it, so it
can merge first and let P2 land as two PRs.

## 9. Testing strategy

- **Keep and extend** the frame invariant tests (`frame_invariants_test.go`,
  `frame_width_test.go`, `frame_geometry_test.go`): the left column never
  exceeds `height - statusLineRows`. This is the main regression guard for
  the now bar's single row plan.
- **Golden-style tests** for the step, task and now bar renderers at 80, 100
  and 140 columns, in both NO_COLOR and 256-colour modes. Follow the
  `depth_golden_test.go` pattern.
- **Pure-function tests** for `stack.Build` (StepID grouping, heuristic
  grouping, mixed legacy and new sessions, rewind/branch filtering) and for
  `nowbar.Plan` (row selection and height caps).
- **Runner tests** (agent package, `agenttest` stubs):
  - step begin/end
  - stamping on every event
  - envelope rationale stored as narration
  - nudge fires exactly once and only under its conditions
- **Persistence:** migration-upgrade test from a pre-P2 database, and a
  resume test that restores tool rows.
- **Keybinding tests** for the Ctrl+C arming windows, using the injected
  `now()`. Esc must never call `cancelTurn`.
- **Removed tests:** the sidepanel geometry and fit tests, the lane cursor
  tests, and todo panel mode tests.

## 10. Follow-ups (out of scope)

- ACP JSON contract gains `stepId`/`actor` on `tool_call` and
  `agent_message_chunk` updates, so the web UI can group by step. `web/`
  must stay standard-library only.
- `/export` HTML renders the step tree.
- Rename `leftWidth` to `contentWidth` across the TUI package.
