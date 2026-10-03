# Single Stack P2 — Steps and Ownership

Parent: `feature-spec.md` (feature spec, §5.2–5.6, §6.1–6.4, §7)

Depends on:
- The data half (in-scope items 1–6) depends on nothing and can merge
  before P1.
- The TUI half (in-scope items 7–11) depends on P1. The now bar exists and
  the rail is gone.

## Goal

Every tool call renders under the narration that explains it, every step
shows who owns it, and approvals show why the agent wants the action. This
is built on a real step identity that is persisted and survives resume,
not on timestamp adjacency.

## In scope

**Data and agent:**
1. Step identity and owner data model, with explicit stamping.
2. Persistence. Resume restores tool rows (fixes F6).
3. Runner stamping, owner population (F4), tool-call IDs (F7).
4. Envelope `rationale` becomes narration (F5).
5. Narration prompt and the intent-only-final nudge (feature spec §7).
6. ACP `toolCallId` pairing uses real tool-call IDs.

**TUI:**

7. `tui/stack` view model: Turn → Step → Row. A legacy heuristic covers
   sessions without step IDs. Stable node IDs.
8. Step renderer: headline, continuation, thinking row, tool rows, inferred
   headline, right-aligned meta.
9. Render cache and the `refreshViewport` rewrite.
10. Owner labels and colours. The subagent card headline. Now bar agent
    headlines and the live-mirror row.
11. Approval panel `why` and owner.

## Out of scope

- Tasks, folding and the turn receipt (P3).
- Density ladder, browse mode and inspector (P3). Ctrl+G keeps its current
  meaning, a global expand toggle.
- ACP contract fields for `stepId`/`actor` (follow-up). P2 only fixes the
  ID pairing.

## Data model

### `internal/tools/registry`

```go
type AuditEvent struct {
    // … existing fields …
    StepID     int64  // session step sequence; 0 = unknown (legacy, TUI-synthesised)
    ToolCallID string // provider call ID (native) or synthesised (envelope)
}
```

`registry` must not import `session`, so `StepID` is a plain `int64`.

### `internal/app/session`

```go
type StepID = int64

type Actor struct {
    Role     string // routing.AgentRole value; "" = orchestrator / general
    Label    string // "reviewer #1", "implementer", custom agent name
    Model    string
    Provider string
}

type Step struct {
    ID        StepID
    TurnMsgID int64     // in-memory ID of the turn's user message; 0 if none
    Actor     Actor
    TodoID    string    // reserved for P3; always "" in P2
    StartedAt time.Time
    EndedAt   time.Time // zero while open
}
```

New fields on existing types:

| Type | Field |
|---|---|
| `Message` | `StepID StepID`. Set only for `ContentTypeNarration`. |
| `ThinkingEntry` | `StepID StepID` |
| `ActiveToolCall` | `StepID StepID`, `ToolCallID string` |
| `PendingToolCall` | `StepID StepID` |

New `State` API:

| Method | Behaviour |
|---|---|
| `BeginStep(actor Actor) StepID` | Allocates the next sequence number. `TurnMsgID` is the most recent user-turn message on the active branch. A user-turn message is a `RoleUser` message whose content type is not a subagent report, watch report or steering. Use the same definition as `tui.isUserTurn`, and move that predicate into `session` as `IsUserTurnMessage`. Persists the step and publishes `EventStepChanged`. |
| `EndStep(id StepID)` | Sets `EndedAt`, persists, publishes `EventStepChanged`. Idempotent. |
| `Steps() []Step` | Steps on the active branch: `TurnMsgID == 0` or `TurnMsgID` is on the branch. Chronological. |
| `Step(id) (Step, bool)` | Lookup. |
| `AddNarration(step StepID, content string)` | Same as `AddMessage(RoleAssistant, content, ContentTypeNarration)` with `StepID` set. |
| `SetActiveToolCall(atc)` | Stores into `activeTools[atc.ToolCallID]`. An empty `ToolCallID` uses key `""`, which keeps today's single-slot behaviour. Also sets the "latest" pointer. |
| `ClearActiveToolCallID(id string)` | Removes one call. `ClearActiveToolCall()` still clears all of them. |
| `ActiveToolCalls() []ActiveToolCall` | All in-flight calls, sorted by `StartedAt`. `ActiveToolCall()` returns the latest, unchanged for existing callers. |

There is deliberately **no ambient "current step"** on `State`. The runner
owns the ID and stamps it explicitly. This keeps pipeline roles that log to
the parent `State` race-free.

`Transcript()` is unchanged in shape. It already carries the new fields
through the item pointers. Audits whose `StepID` refers to a step that is
not on the active branch are excluded, which gives the same branch
semantics as messages.

## Persistence

Migration appended to `db/migrations.go`:

```sql
CREATE TABLE IF NOT EXISTS steps (
  session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  turn_message_id INTEGER,
  actor_role TEXT, actor_label TEXT, model TEXT, provider TEXT,
  todo_id TEXT,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  PRIMARY KEY (session_id, seq)
);
ALTER TABLE messages     ADD COLUMN step_seq INTEGER;
ALTER TABLE tool_calls   ADD COLUMN step_seq INTEGER;
ALTER TABLE tool_calls   ADD COLUMN tool_call_id TEXT;
ALTER TABLE turn_metrics ADD COLUMN intent_nudges INTEGER NOT NULL DEFAULT 0;
```

- **`turn_message_id`** stores the message's **DB ID**. In memory,
  `TurnMsgID` is the in-memory ID. The conversion uses `msg.DBID` when
  saving and the load-time `dbIDToImID` map when loading.
- **New DB API:** `SaveStep`, `EndStep`, `GetSteps(sessionID)`.
  `SaveMessage` gains `stepSeq`. `SaveToolCall` writes the new columns, and
  `GetToolCalls` reads them.
- **Resume** (`session/messages.go` load path), before `dbIDToImID` is
  dropped:
  1. Load all steps. Set `nextStepSeq` to `max(seq) + 1`.
  2. Map `turn_message_id` to in-memory IDs. A step whose turn message is
     missing keeps `TurnMsgID = 0`.
  3. Load `GetToolCalls` rows with `step_seq` set whose step is on the
     active branch, and append them to `auditLog` in order.
  4. Rows without `step_seq` (legacy) are not restored. This is today's
     behaviour.
  5. Restored audits must not be re-persisted, and must not be added to
     `toolAuditThisTurn`. Use a dedicated `restoreAuditLog` path, not
     `LogToolCall`.
- **Thinking entries** are not persisted today, and this is unchanged.

## Runner

### Actor

- Add `Runner.ActorLabel string` (`Runner.Role` already exists).
- Add `func (r *Runner) actor(model, provider string) session.Actor`.
  - Role: `string(r.Role)`, except `RoleGeneral`, which maps to `""`.
  - Label: `r.ActorLabel`. If empty, a prettified role name
    (`sdd_reviewer` → `reviewer`, `sdd_branch_reviewer` → `branch
    reviewer`).
- Factories that set `ActorLabel`:

| Site | Label |
|---|---|
| `app.go` main runner (~864) | Leave empty (orchestrator). |
| `app.go` role runner factory (~1248), used by pipeline/swarm | The role label the dispatcher passes. `pipeline.Dispatcher.run` already has `label`. Thread it through `swarm.RunnerFactory` as an option, or set it on the returned runner before `Run`. |
| `app.go` subagent factory (~1859) | The dispatch description used for the `SubagentView.Label`. |
| `app.go` plan-author runner (~1505) | `plan author` |
| Custom agents (`buildCustomAgentRunner`) | The agent name. |

### Steps

- Add `r.curStep session.StepID`.
- `beginStep(model, provider)` ends any open `r.curStep`, then
  `r.curStep = r.State.BeginStep(r.actor(model, provider))`.
- `endOpenStep()` ends `r.curStep` if it is set. Call it with `defer` from
  the turn entry point.
- Call `beginStep` immediately before `r.chatWithRetry` in the main loop
  (`runner.go:1179`). The `plan_first` planning call (`runner.go:925`) is
  **not** a step.

### Stamping

| Site | Change |
|---|---|
| Narration (`runner.go:1346`) | `r.State.AddNarration(r.curStep, narration)` |
| `LogThinking` (`runner.go:1327`, `:1459`) | Set `StepID: r.curStep` |
| `SetActiveToolCall` (`execute.go:346`) | Set `StepID` and `ToolCallID` from the action |
| `PendingToolCall` construction (approval request site in `execute.go`) | Set `StepID` |
| `logToolCall` | Signature becomes `logToolCall(a ModelAction, event registry.AuditEvent)`. It stamps `StepID` (when zero), `ToolCallID` (from `a.ToolCallID`, when empty), `AgentRole` (`string(r.Role)`) and `Model` (the turn model; store it on the runner at `beginStep`). All seven call sites are updated. |
| `ClearActiveToolCall` call sites in `execute.go` | Use `ClearActiveToolCallID(a.ToolCallID)` |

**Envelope tool-call IDs:** in the envelope action loop, assign
`a.ToolCallID = fmt.Sprintf("s%d-a%d", r.curStep, i)` before dispatch when
it is empty.

### Envelope rationale (F5)

After `ParseActionRepairing` succeeds, if the action contains at least one
action of type tool_call, patch, `actions[]` or `ask_user`, and
`strings.TrimSpace(action.Rationale) != ""`, call
`r.State.AddNarration(r.curStep, rationale)`. Rationale on `final` and
`answer` actions is not shown.

### Narration prompt

- `SystemPromptOptions.Narration bool`. When it is set and `NativeTools` is
  true, `buildSystemPrompt` appends `narrationAddendum`. The text is the
  directive in feature spec §7.
- The runner gets a `NarrationPrompt bool` field. The factories set it from
  `cfg.Agent.NarrationPrompt`, and it is passed into prompt options
  wherever the runner builds them.

### Intent-only-final nudge

In the native text-only path, after the grounding-nudge block and before
the verification gate:

```go
if r.IntentNudge && !intentNudgeSent && toolCallCountThisTurn > 0 && looksLikeIntentOnly(res.Text) {
    intentNudgeSent = true
    budget.overhead++
    countIterations()
    r.withStats(func(s *turnStats) { s.m.IntentNudges++ })
    r.State.AddMessage(session.RoleSystem, intentNudgeMessage, session.ContentTypePlain)
    messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: intentNudgeMessage})
    continue
}
```

- `looksLikeIntentOnly(text)` returns true when **all** of these hold:
  - the trimmed text is ≤ 240 runes
  - it has ≤ 2 sentence terminators
  - it contains no code fence
  - it matches `(?i)^\s*(i'?ll|i will|let me|next,? i|now i|i'?m going to|going to)\b`
- `intentNudgeMessage` is: `You said what you would do next but did not
  call a tool. Call the tool now, or give your final answer.`
- `IntentNudges` is added to turn metrics and persisted in the new column.

### Config

```toml
[agent]
narration_prompt = true   # default
intent_nudge = true       # default
```

Added to `AgentConfig`, file types, merge, save, defaults and the settings
registry.

## ACP

`acp/turn.go`:
- `EventActiveToolChanged` uses `atc.ToolCallID` as `toolCallId` when it is
  non-empty.
- `EventAuditAdded` uses `ae.ToolCallID` when it is non-empty.
- Otherwise both keep the current fallback. This removes the name-based
  pairing guess for every event the runner produces.

## TUI

### `internal/app/tui/stack` (new package)

The package holds structure only, with no styling. It imports `session`,
`registry` and `routing`. Rendering stays in `tui`.

```go
type Kind int // KindTurn, KindStep, KindTool, KindMessage, KindFinal, KindSubagent,
              // KindRunEvent, KindJobExit, KindThinking, KindPassthrough

type NodeID struct { Kind Kind; Key string }

type Node struct {
    ID       NodeID
    Kind     Kind
    Children []*Node
    Live     bool   // re-render on spinner ticks
    Version  uint64 // hash of every payload field the renderer reads
    // payload (exactly one set, by Kind):
    Item     *session.TranscriptItem
    Step     *StepInfo
    Tools    []registry.AuditEvent  // len>1 = merged same-tool run within a step
    Active   *session.ActiveToolCall
}

type StepInfo struct {
    Step       session.Step // zero ID for heuristic steps
    Narration  []*session.Message
    Thinking   []*session.ThinkingEntry
    LiveThinking string // in-progress reasoning when this is the live step
    Heuristic  bool
}

type Snapshot struct {
    Items       []session.TranscriptItem
    Steps       []session.Step
    ActiveTools []session.ActiveToolCall
    InProgress  session.InProgressMessage
    Busy        bool
}

func Build(s Snapshot) []*Node // top-level: turn nodes (plus a preamble turn for items before the first user turn)
```

**Grouping rules:**

1. **Turns.** A user-turn message (`session.IsUserTurnMessage`) opens a
   turn node. Every item up to the next user turn is its child.
2. **Steps by ID.** Items with `StepID > 0` join the step node with that
   ID. Step nodes are ordered by `StartedAt`. The step's actor comes from
   `Steps`. A `StepID` with no `Step` record (for example a race at load)
   forms a step with a zero actor.
3. **Heuristic steps.** For narration, audit and thinking items with
   `StepID == 0`, within a turn:
   - A narration message opens a heuristic step.
   - Following audit and thinking items join it, until the next message of
     any other type or the next narration.
   - Audits or thinking before the first narration form one unnarrated
     heuristic step.
   - Key: `hstep:<first item timestamp UnixNano>`.
4. **Tool rows** inside a step are ordered by timestamp.
   - Consecutive mergeable same-tool audits merge, using today's
     `mergeableAuditEvent` rules moved into `stack`.
   - Active tool calls with the step's `StepID` (or, for a heuristic live
     step, any active call) append as live rows.
   - An active call whose `ToolCallID` already has an audit is dropped.
5. **Subagent cards** go inside the orchestrator step whose `[StartedAt,
   EndedAt or now]` contains the card's `StartedAt`. Otherwise they are a
   pass-through at turn level. The `agent.run` audit/active filtering in
   today's `refreshViewport` moves here unchanged.
6. **Live step.** When `Busy`, the newest open step (`EndedAt.IsZero()`)
   in the last turn is `Live`. `InProgress.Reasoning` attaches to it as
   `LiveThinking`. If there is no open step yet (before the first
   `BeginStep`), the live thinking renders as a pass-through, as today.
7. **Everything else** is a pass-through node in timestamp order at turn
   level: final answers, user messages, system notices, run events, job
   exits, skill tags, compaction and steering markers, auto-skill
   messages and plan blocks.
8. **Empty steps** (no narration, thinking, tools or cards) are omitted.

**Node keys:**

| Node | Key |
|---|---|
| Turn | `turn:<msgID>`, or `turn:pre` for the preamble |
| Step | `step:<seq>` or `hstep:<ns>` |
| Tool | `tool:<ToolCallID>`, else `tool:<ts ns>` |
| Merged tool run | `tools:<first key>` |
| Message | `msg:<ID>` |
| Subagent | `sub:<view ID>` |
| Run event | `run:<ts ns>` |
| Job exit | `job:<id>` |
| Live thinking | `think:live` |

### Rendering (in `tui`)

New `steps.go`:

- **`renderStep(n *stack.Node, ctx stepRenderCtx, width int) string`.**
  `stepRenderCtx` carries `expanded`, the spinner frame, `now`, the active
  route model and provider, the sandbox info and the callers map.
- **Header:** `gutterPrefix(stateGlyph, color) + headline`, with the right
  meta right-aligned (feature spec §5.3).
  - State glyph: `glyph.Error` if any tool row failed, else the spinner
    frame if live, else `glyph.OK` if it has tool rows, else
    `glyph.Ambient`.
  - Failed means `Error != ""`, `Approval == denied`, or `CommandExitCode
    != nil && != 0`.
- **`firstSentence(s string) (head, rest string)`.** The split is at the
  first `. `, `! `, `? ` or `\n` after at least 8 runes. Markdown
  emphasis markers are stripped from the head.
- **Headline:** rendered plain, in FGEmphasis. The continuation (`rest` +
  later narration messages in the step) is rendered through the existing
  markdown pipeline at `nestedContentWidth`, muted, **only when
  expanded**. Otherwise it is shown as one muted line truncated with `…`.
- **Inferred headline:** `inferHeadline(rows)` produces up to 2 clauses
  joined with ` · `, with `…` if there are more.

  | Tool | Clause |
  |---|---|
  | `file.read` | `read <n> file(s)` |
  | search tools | `searched "<q>"` |
  | `shell.run` / `test.run` | `ran <first word of cmd> …` |
  | edit/diff tools | `edited <basename>` |
  | `agent.run` | `dispatched <n> agent(s)` |
  | other | `<display tool name>` |

  It is rendered italic in `thinkingLineStyle`. The meta is prefixed
  `inferred · `.
- **Right meta:** `rightMeta(owner, model, dur string, leftWidth, width
  int) string`. Drop order: model, then duration, then the owner is
  shortened to the role word. The owner is dropped only when the headline
  would fall below 24 columns.
  - Model is shown only when `(Model, Provider)` differs from the active
    route.
  - Duration is `EndedAt - StartedAt`, or live elapsed.
- **Thinking rows:** at nested indent, `⚙ thought for Ns ▹`. Expanded,
  they render the reasoning behind `nestedRail()`. The live thinking
  region is `renderThinkingBox` at `nestedContentWidth`, indented by
  `continuation()`.
- **Tool rows:**
  - Extract the body of `renderCompletedToolCall` into
    `renderToolRow(event, expanded, callers, indent, width)`, where indent
    is `gutterWidth + 2` for rows inside a step.
  - Extract `renderActiveToolCall` into `renderActiveToolRow(atc, …,
    indent, width)` the same way.
  - `renderToolGroup` gets the same indent treatment.
  - At top level (legacy pass-through audits), these functions keep their
    current output byte-for-byte.
- **Owner colour:** `actorColor(role string) color.Color` picks from
  `{AccentSecondary, AccentTertiary, StatusInfo}` by FNV hash of the role.
  The orchestrator has no colour.

### Render cache and `refreshViewport`

- `Model.renderCache map[stack.NodeID]cachedNode`, where
  `cachedNode{version uint64; width int; expanded bool; out string}`.
- `refreshViewport`:
  1. Build the snapshot.
  2. Call `stack.Build`.
  3. Walk the turns. For each top-level block (turn separator, step,
     pass-through), reuse the cached output when the node is not `Live`
     and `version`, `width` and `expanded` all match. Otherwise render it
     and store the result.
  4. Record `[startLine, endLine)` per node into `m.nodeRegions` (replacing
     `clickRegions`). Tool rows record their own sub-ranges so a click on
     a tool row toggles that row.
  5. Prune cache entries for unseen nodes.
- Expansion state moves to `m.expanded map[stack.NodeID]bool`. Ctrl+G
  still sets a global default and clears the overrides.
- `itemKey`, `activeToolKey`, `transcriptHash`, `groupTranscript` and
  `clickTarget.key` are deleted. Region scroll offsets and high-water
  marks (`regionOffset`, `regionRows`) are re-keyed by `NodeID`.
- The turn separator and the welcome banner keep their current rules.

### Ownership surfaces

- **Subagent card (live):** the body shows the first sentence of
  `child.LatestNarrationLine()`, then `child.CurrentToolLabel()` with the
  tool glyph. When there is no narration it falls back to today's
  `subagentTailLines`. `SubagentActivityTail` is unchanged, because
  `agent.output` depends on it.
- **Now bar agent rows:** `⧉ <label>  <child headline>  <elapsed>`. The
  model is shown only if it differs from the parent route.
- **Now bar live-mirror row (new first row):** shown when
  `!m.viewportFollow` and a live step exists. Format: `↓ <live headline,
  truncated> · <spinner> <tool glyph>` with `End` right-aligned.
  `nowBarInput` gains `LiveHeadline` and `LiveToolGlyph`. The 4-row cap
  still applies, and actor rows overflow first.
- **Approval panel:** for the pending call's `StepID`, look up the step.
  - Owner line: `<owner> wants to run a command` (or `… to <tool>`), when
    the owner is not the orchestrator.
  - Why line: `why  "<narration first sentence>"`, only when the step has
    narration. Never an inferred headline.
  - Applies to both `approvalModel`'s summary and the fallback
    `renderApprovalPanel`.

## Acceptance criteria

1. **Native mode:** a turn where the model narrates and then calls two
   tools renders one step. The headline is the narration's first sentence,
   two tool rows are at nested indent, and the right meta shows the
   duration.
2. **No narration:** a step without narration renders an inferred
   headline in italic, with the meta prefixed `inferred`.
3. **Envelope mode:** a `rationale` on a tool_call appears as the step
   headline.
4. **Plan runs:** in an SDD run, implementer and reviewer steps show
   `implementer`/`reviewer` right-aligned in their role colours. With
   `NO_COLOR` the labels are still present.
5. **Persistence:** `tool_calls.agent_role`, `model`, `step_seq` and
   `tool_call_id` are populated for every runner-originated tool call.
6. **Resume:** resuming a session made after P2 shows the same steps and
   tool rows as before exit. Resuming a pre-P2 session renders as today,
   with heuristic grouping of narration and messages and no tool rows.
7. **Rewind:** rewinding to an earlier turn hides the steps and tool rows
   of the rewound turns.
8. **Approvals:** a pending approval shows `why "<narration>"` when the
   step narrated, and no why line otherwise.
9. **Intent nudge:** a scripted model that replies `I'll read parser.go
   next.` with no tool call, after one tool call, triggers exactly one
   nudge per turn. A second identical reply is accepted as final.
   `intent_nudges` = 1.
10. **Narration prompt:** `narration_prompt = false` removes the directive
    from the native system prompt. It is never present in envelope mode.
11. **Render cache:** a spinner tick with 200 settled steps re-renders
    only live nodes. Verify with a test hook that counts renderer calls.
12. **Frame invariants** hold. `go vet ./...` and `go test ./...` pass.

## Test plan

- **`session`:**
  - BeginStep/EndStep/Steps branch filtering, the active-tool map, and
    `AddNarration`
  - resume restoring audits, and the legacy no-op
  - migration upgrade from a DB created at `633fd21`'s schema
- **`db`:** round trips for steps and the new columns.
- **`agent`** (with `agenttest` stubs):
  - step begin/end per iteration
  - stamping on narration, thinking, active, pending and audit
  - envelope IDs and rationale
  - the nudge truth table (fires; does not fire with zero prior tool
    calls, with long text, with a code fence, or a second time)
  - prompt inclusion
- **`acp`:** pairing uses the IDs.
- **`tui/stack`:**
  - grouping table tests: ID, heuristic, mixed, subagent placement, live
    step, empty-step omission
  - node key stability across rebuilds
- **`tui`:**
  - golden step renders at 80, 100 and 140 columns, in NO_COLOR and 256
    colours: ok, failed, live, inferred, owner, model-differs, narrow
    drop order
  - render-cache hit test
  - click toggles a tool row
  - approval why line
  - now bar live-mirror row
