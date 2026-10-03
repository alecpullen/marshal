# W3.3 · Runs page and Live wall — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w3-runs-and-control-design.md`](../specs/2026-10-03-w3-runs-and-control-design.md) §6.1–§6.3
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Track:** UI. See [`../README.md`](../README.md) for both tracks.
**Runs after:** [W2.3](2026-10-03-w2-3-review-and-new-agent-plan.md) (previous UI plan) and [W3.2](2026-10-03-w3-2-bridge-control-plan.md) (this phase's backend gate), with everything those depend on.
**Base:** a branch containing every plan listed under Runs after. Code from before W1 was checked
on `2ddc09e`. W1–W3.2 names are as those plans define them.
**Plan slug:** `w3-3-runs-and-live`. Commit each task as
`w3-3-runs-and-live: task N — <title>`.

## Goal

- `#runs` lists plan and swarm runs and starts new ones.
- `#runs/<agentId>` shows lanes, graph and timeline views with the W2
  dock.
- `#live` is the Live wall.
- The rail's Runs and Live entries are enabled.

## Non-goals

- Library, Models, Usage, Watches (W3.4).

## Assumptions

- **Bridge (W3.2):** `GET /api/sessions/{id}/roster`, `GET /api/runs`, `GET /api/runs/{agentId}`,
  `POST /api/runs`, `POST /api/runs/{agentId}/answer`, and the fleet
  deltas `run`, `budget` and `reroute`. A 429 `budget_exceeded` response
  exists.
- **Engine (W3.1):** the `RunDetail` JSON shape.
- **UI from W1 and W2:** `Rail.svelte` (disabled entries), `routes.ts`,
  `createStackStore`, `Transcript`, the dock (`lib/dock/*`), and Home's
  inline approval actions.

---

## Task 1: Types, API and fleet deltas

**Goal:** typed client functions for runs, and the fleet store handling
the `run`, `budget` and `reroute` deltas.

**Files:**
- `web/ui/src/lib/api.ts`, `api.test.ts`
- `web/ui/src/lib/fleet.ts`, `fleet.test.ts`

**Steps:**

1. Add types mirroring W3.1 Task 2: `RunDetail`, `SDDRun`, `RunTask`,
   `RunStage` and `SwarmRun`.
2. Add these functions:
   - `listRuns()`;
   - `getRun(agentId)`, with 501 mapped to `'unsupported'`;
   - `startRun(req)`;
   - `answerRun(agentId, answer)`;
   - `undoReroute(id)`.
3. Add a shared `BudgetError` class. `api.ts`'s request helper throws it
   on a 429 whose body has `error: "budget_exceeded"`. Composer, spawn
   and runs callers catch it and show "Budget reached (<scope>)".
4. In `fleet.ts`:
   - `run` deltas set `row.run` and `row.runAt`;
   - `budget` deltas update a top-level `budget` state;
   - `reroute` deltas push onto a `notices` list, which Home renders with
     an Undo button.
5. Tests: one per delta, plus the `BudgetError` mapping.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/api.test.ts src/lib/fleet.test.ts
```

---

## Task 2: Run model helpers

**Goal:** pure functions for lanes, DAG layout, the critical path and
timeline bars.

**Files:**
- `web/ui/src/lib/runs/model.ts` (new), `model.test.ts` (new)

**Steps:**

1. `lanes(run: SDDRun)` returns
   `{n, title, deps, cells: Record<'implement'|'verify'|'review'|'commit', {state, detail, fixRounds?, sha?}>}[]`.
   Cells come straight from `task.stages`. The commit cell's `sha` is
   `commit.head.slice(0, 7)`.
2. `layout(tasks: {n; dependsOn: number[]}[])` returns
   `{nodes: {n, layer, index}[], edges: {from, to}[]}`.
   - A node's layer is the length of the longest path from a root.
   - Within a layer, nodes are ordered by `n`.
3. `criticalPath(tasks, now)` returns the chain of task numbers with the
   largest summed duration (`endedAt - startedAt`, or `now - startedAt`
   while active, or 0 when pending). It uses DP over a topological order.
4. `roleBars(nodes: WireNode[], from, to)` returns
   `{role, segments: {start, end, stepId}[]}[]` from step nodes with a
   `step.role` inside the range. An open step ends at `now`.
5. Tests:
   - a lanes fixture;
   - layout of a diamond graph (1 → 2, 1 → 3, 2 and 3 → 4);
   - the critical path picks the longer branch;
   - role bars clip to the range.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/runs/model.test.ts
```

---

## Task 3: Runs list and New run

**Goal:** `#runs` lists runs and can start one.

**Files:**
- `web/ui/src/views/Runs.svelte` (new), `Runs.test.ts` (new)
- `web/ui/src/lib/runs/NewRunModal.svelte` (new)
- `web/ui/src/App.svelte`, `web/ui/src/lib/Rail.svelte`, `web/ui/src/lib/routes.ts`

**Steps:**

1. Routes. `routes.ts` parses:
   - `#runs`;
   - `#runs/<agentId>?view=lanes|graph|timeline&node=&dock=`.

   Enable the Runs ⋔ entry on the rail. Remove its `disabled` flag and
   "Coming in" tooltip.
2. `Runs.svelte`:
   - Load `listRuns()` and keep it fresh from `run` fleet deltas.
   - Each row shows the agent name, the plan name or goal, a progress bar
     (`doneTasks/totalTasks`), the phase, elapsed time, and an open-gate
     badge.
   - Filter with a `Segmented` control: Running, Finished, Needs you.
3. `NewRunModal.svelte` (W1's `Modal`):
   - Target: an existing agent, or a new agent in a project.
   - Kind: Plan or Swarm.
   - Plan: a textarea, or a path field. Swarm: a goal field.
   - Submit calls `startRun` and navigates to `#runs/<agentId>`. A
     `BudgetError` shows inline.
4. Tests: list rendering, filters, and the modal posting the right body.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Runs.test.ts && npx svelte-check
```

---

## Task 4: Run page — lanes, graph and timeline

**Goal:** `#runs/<agentId>` with the three views, the header and gate
answer, and the W2 dock filtered by stage.

**Files:**
- `web/ui/src/views/Run.svelte` (new), `Run.test.ts` (new)
- `web/ui/src/lib/runs/{Lanes,Graph,Timeline}.svelte` (new)

**Steps:**

1. `Run.svelte`:
   - Load `getRun(agentId)` and update it from `run` deltas.
   - Hold a `createStackStore(sessionId)` for the timeline and the dock.
   - **Header:** plan name, branch, progress, phase, tokens against max,
     and the swarm goal.
   - **Open gate:** the gate question, with an answer textarea that calls
     `answerRun`.
   - A `Segmented` control switches views, and the choice is written to
     the URL.
   - On `'unsupported'`, show "Run detail needs a newer agent", with a
     link to the session.
2. `Lanes.svelte`:
   - A table: `n`, title, then four stage cells. Each cell shows a state
     glyph from `glyphs.ts` and the state colour, plus the fix-round
     count and short SHA.
   - Clicking a cell dispatches `select({task: n, stage})`.
   - **Roles legend:** shows each role's model. Add
     `getRoster(sessionId)` to `api.ts`, calling W3.2's
     `GET /api/sessions/{id}/roster` (W3.2 Task 9).
3. `Graph.svelte`:
   - SVG from `layout()`. Nodes are 180×56 rectangles, edges are cubic
     paths, and edges on `criticalPath()` are drawn dashed in accent.
   - The SVG pans with drag and zooms with the wheel.
   - Clicking a node selects that task's active stage.
4. `Timeline.svelte`:
   - Rows of `roleBars()` over the run range, with a time axis.
   - Below, a cumulative-tokens line from `run` delta samples collected
     while the page is open, plus `(startedAt, 0)` and the current
     total.
5. **Dock filtering.** On selection, compute the step IDs whose
   `step.role` matches the stage's role (`implement` →
   `sdd_implementer`, `review` → `sdd_reviewer`, `verify` → steps with
   shell-family tool calls inside the task window, `commit` → none) and
   whose `startedAt` falls inside the task's `startedAt..endedAt`.
   - Render the W2 dock with `Inspect` on the first such step.
   - Show a filtered `Transcript`, with a prop that limits it to those
     step IDs and their ancestors, in the dock's Changes slot. Add an
     `onlyNodes?: Set<string>` prop to `Transcript.svelte`.
6. Tests:
   - lanes render from a fixture;
   - clicking the Verify cell of task 2 selects the expected step IDs;
   - the graph draws one dashed path;
   - the gate answer posts.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Run.test.ts src/lib/runs && npx svelte-check
```

---

## Task 5: Live wall

**Goal:** `#live` shows paged, live tiles with inline decisions
(spec §6.3).

**Files:**
- `web/ui/src/views/Live.svelte` (new), `Live.test.ts` (new)
- `web/ui/src/lib/live/Tile.svelte` (new)
- `web/ui/src/App.svelte`, `Rail.svelte`

**Steps:**

1. Enable Live ◉ on the rail. `#live?project=&runs=1&page=N`.
2. `Live.svelte`:
   - Agents come from the fleet store, filtered by project and "Runs
     only" (`row.run` set).
   - They are sorted needs-you first, then running, then by `updatedAt`.
   - 12 per page, in a 3-column grid that becomes 4 columns at ≥ 1600px.
3. `Tile.svelte`:
   - Use an `IntersectionObserver`. When the tile enters view, create
     `createStackStore(agent.id)` and `load()`; when it leaves, dispose of
     it (unsubscribe, and drop it).
   - **Content:**
     - name, project and elapsed time;
     - progress segments: one per task node in the latest turn, coloured
       by status;
     - the last three step headlines (outline).
   - **Chips:** model, from the latest step's `step.model`; gate, from
     `row.gate`; workspace, empty until W4 fills it in.
   - **Needs you:** a `warn` tint, with the pending request and the same
     approve and deny controls as Home's Needs-you rows. Extract them
     from `Home.svelte` into `lib/inbox/PendingActions.svelte` and reuse
     them in both places.
   - Clicking anywhere outside the controls navigates to
     `#chat/<id>?dock=collapsed`.
4. Tests:
   - paging;
   - a tile's store is created on intersect and disposed on leave (mock
     `IntersectionObserver`);
   - inline approve calls the permission API;
   - the runs-only filter.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Live.test.ts && npx svelte-check
```

---

## Task 6: Rebuild static

**Goal:** the committed bundle includes W3.3.

**Steps:**

1. Run `cd web/ui && npm run build`.
2. Commit `web/bridge/static`.

**Verify:**

```bash
cd web/ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
cd ../bridge && go test ./ -run 'TestAssets|TestWebIsStdlibOnly'
```

Check by hand:
- start an SDD run on a small plan with a `Depends on:` line;
- watch the lanes update;
- open the graph;
- answer a gate;
- open `#live` with several agents running.

---

## Final verification

```bash
cd web/ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
cd ../.. && cd web/bridge && go test ./...
```

## Integration notes

- The Live wall opens one session SSE stream per visible tile, at most 12.
  This is the design's "one stream per session" decision (§8.3). Revisit
  it past about 20 agents.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. Pure helpers (Task 2) come before views. |
| Anchors verified? | Pre-W1 UI files were checked on `2ddc09e`. The roster route comes from W3.2 Task 9. Later names are as defined. |
| Code compilable in isolation? | No verbatim code. |
| Placeholders? | None. |
| Matches the spec? | §6.1–§6.3. |
