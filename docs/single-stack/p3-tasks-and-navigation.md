# Single Stack P3 — Tasks and Navigation

Parent: `feature-spec.md` (feature spec, §5.4, §5.9–5.11, §6.1)
Depends on: P2. It needs step identity, the `tui/stack` tree, node IDs and the render cache.

## Goal

1. Make the agent's progress readable in the scrollback. Todos become task
   headers that fold when they finish, and each turn ends with a one-line
   receipt.
2. Make every item reachable from the keyboard. There are three ways in:
   - a density ladder for the whole transcript;
   - browse mode, entered with Esc;
   - an inspector for a single step or tool call.

## In scope

1. Todo identity and timing: `ID`, `StartedAt` and `CompletedAt`.
2. Binding each step to the todo that was in progress when it started.
3. Task nodes in the tree, task headers, the folding rule and the turn receipt.
4. The density ladder (outline / steps / full) on Ctrl+G, with per-node overrides.
5. Browse mode on Esc. Cursor, keys, copy, open file and drill-in.
6. The inspector panel.
7. Config: `tui.transcript.density` and `tui.transcript.fold_finished_tasks`.
8. Now bar and Tasks panel: show task elapsed time from `StartedAt`.

## Out of scope

- ACP/export adoption of tasks.
- Agent-side changes to how models plan. The `todo.write` contract only
  gains an optional `id`.

## Data model

### Todos

```go
// internal/db/todos.go
type TodoItem struct {
    ID          string    `json:"id,omitempty"`
    Content     string    `json:"content"`
    Status      string    `json:"status"`
    StartedAt   time.Time `json:"started_at,omitzero"`
    CompletedAt time.Time `json:"completed_at,omitzero"`
}
```

The stored JSON stays compatible in both directions. Old rows decode with
zero values for the new fields. Old binaries ignore unknown keys.

The `todo.write` tool (`tools/native/todos.go`) works as follows.

- **Schema:** each item accepts an optional `"id"`. Neither the tool
  description nor the todo prompt addendum tells the model to send it.
  Models that do send it get exact matching.
- **Reconciliation** happens on every write, against the previous list, in
  this order:
  1. An item with an `id` matches the previous item with that ID.
  2. Otherwise it matches the first unmatched previous item with exactly
     the same `content`.
  3. Otherwise it matches the first unmatched previous item whose content
     is equal after case-folding and trimming.
  4. Otherwise it is new and gets `ID = "t<n>"`, where `n` is a
     per-session counter. The counter is derived as max existing numeric
     suffix + 1, so no extra state needs persisting.
- **Timestamps** are set by the store, never by the model:
  - `StartedAt` is set on the first transition into `in_progress`.
  - `CompletedAt` is set on the transition into `completed`, and cleared
    if the item leaves `completed`.
  - Matched items keep their timestamps.
- **Dropped todos:** previous items that match nothing are removed from
  the list as today. Their IDs live on in step records, which the
  renderer treats as dropped (see Task headers).
- **Subagents:** each subagent keeps its own rebound todo store
  (`app.go:1857`), so subagent steps bind to the child's todos.

### Step binding

`State.BeginStep` sets `Step.TodoID` to the ID of the first `in_progress`
todo in this `State`'s list at that moment, or `""` if none. The value is
persisted to `steps.todo_id`, a column reserved in P2.

A step is bound to the task that was active when it **started**. The usual
pattern is narrate, then mark the todo in progress with `todo.write`, then
work. That puts the `todo.write` call in a step bound to the previous task.
Two rules handle this:

- `todo.write` audit rows are never rendered as tool rows. The task headers
  replace them.
- A step whose only tool calls are `todo.write`, and which has no
  narration, is omitted. A step with narration plus only `todo.write` is
  re-bound for rendering. It belongs to the todo its write set to
  `in_progress`, read from the audit `Args`. This affects rendering only.
  The stored binding is unchanged.

## Tree changes (`tui/stack`)

### Task nodes

- **New kind `KindTask`** sits between Turn and Step.
- **Grouping:** within a turn, consecutive steps with the same non-empty
  effective `TodoID` group under one task node.
  - The effective ID includes the render-time re-binding above.
  - If a task resumes after another task, it gets a second segment.
- **Ungrouped content:** steps with an empty `TodoID` stay direct
  children of the turn. Pass-through nodes, including final answers, also
  stay at turn level, positioned by timestamp. A pass-through that falls
  between two steps of the same task splits the task into segments. This
  keeps the chronology honest.
- **Key:** `task:<turnKey>:<todoID>:<segment>`, where segment counts up
  from 1 within the turn.
- **Payload:** `TaskInfo{TodoID, Content, Status, Index, Total, Dropped,
  StartedAt, CompletedAt, Steps, Tools, Edits, UnresolvedFailure}`.
  - Content and status come from the current todo list.
  - `Dropped` is true when the ID is missing from the current list. Content
    then falls back to the narration headline of the task's first step.
    If there is none, it is `task <id>`.
- **`Snapshot`** gains `Todos []native.TodoItem`. Use `db.TodoItem` to
  keep `stack` free of the tools package.

### Unresolved failure

A task has an unresolved failure when either of these holds:

- Its most recent tool row from the shell family (`shell.run`, `test.run`,
  or any tool `isShellFamily` matches) has a non-zero exit code.
- Its last step contains any failed tool row: an error, a denial, or a
  non-zero exit.

### Turn receipt

A `KindReceipt` node is appended to a turn that has a final answer and at
least one step. It is not shown for a turn that is still running. Its
fields:

- **duration:** final message `CreatedAt` minus the user message
  `CreatedAt`.
- **tasks:** the number of distinct todo IDs that completed in this turn.
  Shown as `k tasks`, and omitted when 0.
- **steps:** the number of step nodes.
- **tools:** the number of tool calls, excluding `todo.write`.
- **files:** the number of unique `FilesChanged` paths across the turn's
  audits. Shown as `±n files`, and omitted when 0.
- **tokens:** the final message's `Usage` string, when set.

Rendered as:

```
 ✓ done · 6m40s · 4 tasks · 11 steps · 19 tools · ±3 files · 212k tok
```

The glyph is `glyph.OK` in `StatusSuccess`. When the final message is
salvaged, it is `glyph.Warning` with `salvaged` instead of `done`.

## Rendering

### Task header

- **Open:** `chrome.Header`-style rule: `─ N/M <content> ──── <elapsed>`.
  - `N/M` is `Index/Total` from the current list.
  - Content is bold.
  - Elapsed is `CompletedAt - StartedAt`, or live time since `StartedAt`.
  - A dropped task shows `dropped` in place of `N/M`.
- **Folded:** a single gutter row:
  `✓ N/M <content>` with right meta `<k> steps · <tools or ✎ +a −r> · <duration> ▹`.
  - The meta uses edits when the task changed files. The diff stat sums the
    `diffStat` of its edit rows.
  - The glyph is `✓` (StatusSuccess). A folded task never has a failure,
    because of the folding rule.
- **Live:** the in-progress task is never folded. Its header glyph is the
  spinner.

### Folding rule

A task node renders folded when **all** of these hold:

- `fold_finished_tasks` is on (session toggle `z`, default from config);
- the todo status is `completed`;
- `!UnresolvedFailure`;
- there is no user override for the node (Enter or a click sets one).

Dropped tasks follow the same rule, keyed on whether their last step has
ended.

### Density ladder

Global density lives in `m.density` (`densityOutline`, `densitySteps` or
`densityFull`). Its initial value comes from `tui.transcript.density`.
Ctrl+G cycles outline → steps → full and clears per-node overrides.

Per-node overrides are kept in `m.nodeDensity map[stack.NodeID]density`,
which replaces P2's `m.expanded`. A P2-style "expanded" node is
`densityFull`.

| Node | outline | steps | full |
|---|---|---|---|
| Task (not folded) | header + its steps at outline | header + steps at steps | header + steps at full |
| Step | header only, right meta `n tools` | header + one muted continuation line + tool rows (collapsed); failed rows show up to 3 output lines | header + full narration (markdown) + thinking expanded + tool rows expanded |
| Tool row | hidden | collapsed row | full result / diff |
| Thinking row | hidden | `⚙ thought for Ns ▹` | reasoning text |
| Subagent card | settled: one row; live: header row only | as P2 | settled card expanded with summary |
| Pass-through (system notices, run events, job exits) | unchanged | unchanged | expanded where they support it |
| User message, final answer | always full | always full | always full |

A child with no override inherits its parent's effective density. Enter on
a node cycles that node's override only.

The render-cache key adds `density`.

## Browse mode

### State

New `Model` fields:

- `browsing bool`
- `cursor stack.NodeID`
- `browseNodes []stack.NodeID`

`browseNodes` is the flattened navigable order, rebuilt in
`refreshViewport` from the rendered tree. It contains:

- task headers;
- steps;
- tool rows of steps rendered at steps density or above;
- subagent cards;
- pass-through nodes;
- receipts.

Turn separators are not navigable. Each navigable node has a line range in
`m.nodeRegions`, added in P2.

### Entering and leaving

**Enter** with Esc when nothing else consumes it. This is the final step of
the P1 Esc chain, after popup, suggestion, drill and notice. Approval,
question, skill-gate and dock-panel Esc handling run earlier and are
unchanged. Entering browse mode does the following:

- puts the cursor on the newest step, or the newest node when no step
  exists;
- blurs the textarea and keeps its text;
- turns `viewportFollow` off;
- sets the status-line mode segment to `browse` (bold violet);
- switches the footer to browse hints.

**Leave** with Esc, a printable key that has no binding, Enter on an empty
browse list, or submitting from a command. On leaving:

- the textarea is focused;
- the viewport stays where it is;
- `viewportFollow` comes back only if the viewport is at the bottom.

An unbound printable key is also inserted into the input, so typing just
works.

### Keys

| Key | Action |
|---|---|
| `j` / `↓` | next node in `browseNodes` |
| `k` / `↑` | previous node |
| `J` / `]` | next task header or turn's first node |
| `K` / `[` | previous task header or turn's first node |
| `g` / `G` | first / last node (`G` also re-enables follow if last node is live) |
| `PgUp` / `PgDn`, `Ctrl+U` / `Ctrl+D` | scroll as today; cursor moves to the first fully visible node |
| `Enter` | subagent card: drill in (as Ctrl+F does for that card). Otherwise: cycle the node's density override (steps → full → outline → steps) |
| `i` | open the inspector on the node (steps and tool rows; other kinds: no-op with notice) |
| `y` | copy the node's plain text (see Copy) |
| `o` | open the node's file in `$EDITOR` (see Open) |
| `f` | drill into the subagent under the cursor, or the subagent owning the cursor's step |
| `z` | toggle task folding for the session |
| `Esc` | leave browse mode |
| `?` | show browse key help (docpanel) |

### Cursor rendering

The cursor node's line range is painted with `BGSelection` using the
existing `chrome` selection helpers (`chrome/selection.go`). When the
cursor is on a step, the step's own tool rows get a lighter overlay
(`BGOverlay`). Painting happens after the cached block is joined, so
cached output stays cursor-free and the cache is not invalidated by cursor
moves.

After every cursor move the viewport scrolls so the node's first line is
visible. If the node fits, it scrolls far enough to show the whole node.

### Copy (`y`)

Copying uses OSC 52 through Bubble Tea v2's clipboard command
(`tea.SetClipboard`; confirm the API at implementation). The text copied
depends on the node:

| Node | Copied text |
|---|---|
| Tool row | `ResultContent` (diff for edit tools), else `ResultSummary` |
| Step | headline + full narration + one line per tool subject |
| Message / final | raw `Content` |
| Task | content + its steps' headlines |
| Receipt | its text |

ANSI escape sequences are stripped. Afterwards a notice reads
`Copied <n> lines`. If the terminal is known not to support OSC 52, the
notice reads `Copy sent (OSC 52); your terminal may not support it` the
first time only.

### Open (`o`)

1. Resolve a path from the node, taking the first that applies:
   - the audit `Args` `path`;
   - `FilesChanged[0]`;
   - for shell rows, the first `path:line` in the output that resolves
     inside `Workspace().ActiveRoot`.
2. If `$EDITOR` is unset or no path resolves, show a notice and do
   nothing.
3. Otherwise run `tea.ExecProcess` with `$EDITOR <path>`. When the line is
   known and the editor's basename is one of `vi`, `vim`, `nvim`, `nano`,
   `hx`, `kak`, `emacs`, `micro` or `code`, pass the line. Use `+<line>`
   for all of these except `code`, which takes `-g <path>:<line>`.

### Mouse

Mouse behaviour is unchanged from P2: a click toggles the node, which in
P3 cycles its density override. In browse mode a click also moves the
cursor to the clicked node. A click never enters browse mode on its own.

## Inspector

The inspector is a new package, `internal/app/tui/inspector`. It provides
`Panel`, which implements `dock.Panel` with `Sizing() == dock.FullFrame`.
It renders a `Detail` value built by `tui` and imports nothing from
`session`:

```go
type Detail struct {
    Title    string        // "step 3.2 · tool 1/1" or "step 14"
    Subject  string        // command / tool subject / step headline
    Fields   []Field       // ordered key/value rows
    Sections []Section     // "args" (pretty JSON), "output", "narration", "thinking", "diff"
}
type Field   struct{ Key, Value string }
type Section struct{ Name, Body string }
```

**Fields for a tool row**, each shown only when present:

- actor (owner and model @ provider);
- why (the step narration's first sentence);
- started (time), duration, exit code;
- sandbox (backend, network, cwd, killed reason);
- approval (state and matched rule);
- hooks (`hookIndicatorText` plus decisions);
- rewritten (yes, with the original args shown as their own section);
- finish reason;
- tool call ID;
- notice.

**Fields for a step:** actor, why, started, duration, task, number of
tools, and model @ provider.

**Keys:**

- Tab / Shift+Tab move between sections.
- ↑ / ↓ / PgUp / PgDn scroll the current section.
- `y` copies the current section.
- `o` opens the file, as in browse mode.
- `n` / `p` emit `NavigateMsg{Delta: ±1}`. The model moves the browse
  cursor and rebuilds the `Detail`.
- Esc closes the inspector and returns to browse mode with the cursor
  unchanged.

## Now bar and Tasks panel

**Now bar:** the todo progress row's right-aligned elapsed time becomes the
in-progress task's elapsed time (`now - StartedAt`). When nothing is in
progress, it falls back to the turn elapsed time.

**Tasks panel (Ctrl+T, from P1)** rows gain:

- for completed tasks: their duration;
- for the in-progress task: its live elapsed time;
- for each task: a step count, read from `State.Steps()` grouped by
  `TodoID`.

## Config

```toml
[tui.transcript]
density = "steps"            # outline | steps | full
fold_finished_tasks = true
```

Both keys are added to `TUIConfig`, file types, merge, save, defaults and
the settings registry. Any other value for `density` produces a diagnostic
and falls back to `steps`.

## Help and status

- **Footer while browsing:** `j/k move · ↵ detail · i inspect · y copy · o open · esc back`.
- **Idle footer:** adds `Esc browse`.
- **`/help` cheatsheet:** gains a browse-mode section, and Ctrl+G now reads
  `cycle detail (outline/steps/full)`.
- **Ctrl+G:** shows a transient notice, `Detail: outline`.

## Acceptance criteria

1. **Task headers and folding.** Use a scripted turn with 3 todos that are
   completed one by one, each with 2 steps. While running, the first two
   tasks fold as they complete and the third stays open. After the final
   answer, all three are folded, followed by the final answer and the
   receipt `✓ done · … · 3 tasks · 6 steps · …`.
2. **Failures stay open.** A completed task whose last test run exited 1
   stays open (unresolved failure).
3. **Toggling folds.** `z` unfolds all tasks; `z` again refolds them.
4. **Rewritten todo lists.** Rewriting the todo list with one item renamed
   (changed case) keeps that item's ID and timestamps. A removed item's
   steps render under a `dropped` header.
5. **No raw `todo.write` rows.** `todo.write` never appears as a tool row.
6. **Density cycling.** Ctrl+G cycles outline → steps → full. At outline,
   a 10-step turn renders exactly 10 step rows plus headers.
7. **Esc while busy.** Esc with nothing open enters browse mode, and does
   not cancel the turn. The cursor sits on the newest step and the mode
   segment reads `browse`.
8. **Navigation.**
   - `j`/`k` move the cursor.
   - `J`/`K` jump between task headers.
   - `Enter` on a tool row expands its output.
   - `i` opens the inspector with the actor, why, exit code and full
     output.
   - `n` moves to the next node.
   - Esc returns to browse mode.
9. **Copy.** `y` on a failed test row sends an OSC 52 clipboard sequence
   containing the output (assert on the emitted command).
10. **Leaving browse mode.** Typing `h` in browse mode (unbound) leaves
    browse mode and the input contains `h`. Text typed before entering is
    preserved.
11. **Old sessions.** A pre-P3 session (todos without IDs, steps without
    `todo_id`) renders with no task headers and no errors.
12. **Frame invariants** hold with the inspector open and while browsing.
    `go vet ./...` and `go test ./...` pass.

## Test plan

- **`tools/native`:** the reconciliation truth table (ID, exact,
  case-folded, new), the timestamp transitions, and the ID counter
  derivation.
- **`session`:** `BeginStep` binding `TodoID`, and its persistence round
  trip.
- **`tui/stack`:**
  - task grouping and segmentation;
  - the render-time `todo.write` re-binding;
  - dropped tasks;
  - the unresolved-failure table;
  - receipt arithmetic.
- **`tui`:**
  - golden renders of the open, folded, live and dropped task headers and
    the receipt at 80 and 140 columns;
  - the density matrix;
  - browse key handling (every key in the table);
  - cursor painting does not invalidate the cache (renderer call count);
  - copy and open command construction, using a fake `$EDITOR`.
- **`tui/inspector`:** section switching, scrolling, `NavigateMsg`, and the
  close path.
