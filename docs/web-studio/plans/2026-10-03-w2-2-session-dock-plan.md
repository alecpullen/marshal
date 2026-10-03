# W2.2 · Session dock — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w2-session-and-ship-design.md`](../specs/2026-10-03-w2-session-and-ship-design.md) §6.1–§6.5
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Track:** UI. See [`../README.md`](../README.md) for both tracks.
**Runs after:** [W1.2](2026-10-03-w1-2-ui-plan.md) (previous UI plan) and [W2.1](2026-10-03-w2-1-session-backend-plan.md) (this phase's backend gate), with everything those depend on.
**Base:** a branch containing every plan listed under Runs after. Anchors into code from before
W1 were checked on `2ddc09e`. Anchors into W1 and W2.1 code are named as
those plans define them.
**Plan slug:** `w2-2-session-dock`. Commit each task as
`w2-2-session-dock: task N — <title>`.

## Goal

The session page has the dock described in spec §6.2–§6.5:

- four states, follow and select modes;
- Inspect, Changes and Files tabs;
- pinning, URL state;
- `i`, `o`, `f` and the dock keys;
- subagent drill-in, and the now-bar mirror row.

## Non-goals

- Review & ship and New agent (W2.3).
- Terminal and Preview, which stay as disabled tabs (W5).

## Assumptions

- W1 created these:
  - `web/ui/src/lib/stack.ts` (`createStackStore`, `getStack`);
  - `lib/transcript/` (`Transcript.svelte`, the renderers, `density.ts`,
    `browse.ts`, `NowBar.svelte`);
  - `lib/glyphs.ts` and `lib/ui/{Tag,Segmented,Kbd}.svelte`;
  - the `Chat.svelte` integration.
- W2.1 created these bridge routes:
  - `GET /api/sessions/{id}/stack?subagent=`;
  - `GET /api/sessions/{id}/nodes/{nodeId}`, `…/last-request` and
    `…/step-diffs`;
  - `GET /api/agents/{id}/files`, `…/file`, `…/gate` and
    `POST …/verify`;
  - the fleet delta kind `gate`.
- The existing `GET /api/agents/{id}/diff` (`api.ts` `getDiff`) and
  `DiffView.svelte` (props `{agentId}`) are unchanged.
- In `web/ui`, use `npm test` (vitest), `npx svelte-check` and
  `npm run build`.

---

## Task 1: API client additions

**Goal:** typed client functions for every W2.1 route.

**Files:**
- `web/ui/src/lib/api.ts`
- `web/ui/src/lib/api.test.ts` (new)

**Steps:**

1. Add types mirroring W2.1 (spec §4):
   - `NodeDetailResponse {node: WireNode; detail: NodeDetail}` (`WireNode`
     is imported from `stack.ts`), with `NodeDetail`, `CallDetail`,
     `ThoughtDetail`, `TodoDetail` and `RelationDetail`;
   - `RequestJSON` (camelCase, per W2.1 Task 6);
   - `StepDiff`;
   - `FileList {root, path, entries: {name, dir, size}[]}`;
   - `FileView {path, size, binary, truncated, content}`;
   - `GateRecord {result: GateResultT; at: string}`. `GateResultT`
     matches the bridge `gateResult`: `ok`, `skipped`, `failedCommand?`,
     `output?`.
2. Add the functions. Each uses the same internal request helper and
   `APIError` handling as `getDiff`, and maps a 501 body
   `{"error":"<f>_unsupported"}` to the sentinel `'unsupported'`, as W1's
   `getStack` does:
   - `getNode(sessionId, nodeId, subagentId?)`, with `nodeId` passed
     through `encodeURIComponent`;
   - `getLastRequest(sessionId)` and `getStepDiffs(sessionId)`;
   - `listFiles(agentId, path)` and `readFile(agentId, path)`;
   - `getGate(agentId)`, which resolves `null` on 204, and
     `runGate(agentId)`;
   - `getStack` gains an optional `subagentId`.
3. In `api.test.ts`, use `vi.stubGlobal('fetch', …)`. For each function,
   check the URL and method, the 501 → `'unsupported'` mapping, and
   `getGate` 204 → `null`.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/api.test.ts && npx svelte-check
```

---

## Task 2: Stack store — subagent option and patch filtering

**Goal:** one stack store per (session, subagent). Each store applies only
its own patches.

**Files:**
- `web/ui/src/lib/stack.ts`, `web/ui/src/lib/stack.test.ts`

**Steps:**

1. Change the signature to
   `createStackStore(sessionId, opts: {subagentId?: number; fetcher?} = {})`.
   W1's positional `fetcher` argument moves into `opts`; update W1's
   callers and tests.
2. In `onEvent`, ignore a `stack_patch` whose `subagentId` (default 0)
   differs from the store's `subagentId ?? 0`. `load()` passes
   `subagentId` to `getStack`.
3. Tests:
   - a parent store ignores a patch with `subagentId: 3`;
   - a child store with id 3 applies it;
   - a child store ignores the parent's patches.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/stack.test.ts
```

---

## Task 3: Session URL state

**Goal:** `#chat/<id>` carries the dock state, the selection and the
review route (spec §6.2 URL state).

**Files:**
- `web/ui/src/lib/routes.ts`, `web/ui/src/lib/routes.test.ts`
- `web/ui/src/App.svelte` (the `chatMatch` derivation at `App.svelte:118`)

**Steps:**

1. Add `parseChatRoute(hash): {id; view: 'session'|'review'; node?; dock?; tab?} | null`.
   It accepts:
   - `#chat/<id>`;
   - `#chat/<id>/review`;
   - an optional `?node=…&dock=collapsed|docked|expanded&tab=inspect|changes|files`.
2. Add `formatChatRoute(r)`, the inverse.
   - Updating the dock or selection calls
     `history.replaceState(null, '', formatChatRoute(…))`, so it doesn't
     fill the history.
   - Changing the view (session ↔ review) assigns `location.hash`.
3. In `App.svelte`, replace the `chatMatch` regex with `parseChatRoute`.
   Pass `route` to `Chat`. W2.3 renders the review view; until then
   `view: 'review'` renders `Chat`.
4. Tests: round-trip for every field, bad values dropped, and the old
   `#chat/<id>` still parses.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/routes.test.ts && npx svelte-check
```

---

## Task 4: Dock state reducer

**Goal:** a pure reducer owns the dock's size, mode, tab, pin and
selection rules (spec §6.2).

**Files:**
- `web/ui/src/lib/dock/dock.ts` (new), `dock.test.ts` (new)

**Steps:**

1. State:
   `{size: 'collapsed'|'docked'|'expanded'; width: number; mode: 'follow'|'select'; tab: 'inspect'|'changes'|'files'; pinned: boolean; selected?: string; unseenChanges: boolean}`.
2. Actions, and how each changes the state:

   | Action | Effect |
   |---|---|
   | `cycleSize` | collapsed → docked → expanded → collapsed |
   | `setSize` | Sets the size |
   | `resize(px)` | Clamps the width to 320–720 |
   | `select(nodeId, kind)` | Sets `mode = 'select'`. Unless pinned, switches to `inspect` for tool, step, task and subagent kinds. A collapsed dock becomes `docked`. |
   | `backToLive` | Sets `mode = 'follow'` and clears `selected` |
   | `openTab(tab)` | Opens the tab; a collapsed dock becomes `docked`; opening `changes` clears `unseenChanges` |
   | `togglePin` | Toggles the pin |
   | `liveEdit` | In follow mode, not pinned and docked: switch to `changes`. Otherwise set `unseenChanges` when the tab isn't `changes`. |

3. Helpers:
   - `persistable(state)` picks `{size, width}`;
   - `load()` and `save()` use `localStorage['marshal.ui.dock']` inside
     try/catch, with defaults `docked` and 440.
4. Tests: one per action rule, plus the pin blocking auto-switch.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/dock/dock.test.ts
```

---

## Task 5: Dock shell and session layout

**Goal:** `Chat.svelte` lays out the transcript and dock, and the dock
renders its three sizes, the tab bar, the strip and the header.

**Files:**
- `web/ui/src/lib/dock/Dock.svelte` (new), `DockStrip.svelte` (new)
- `web/ui/src/views/Chat.svelte`, `web/ui/src/views/Chat.test.ts`

**Steps:**

1. **Layout in `Chat.svelte`.** Lay out a flex row: the transcript column
   (`max-w-[780px] mx-auto`), then `Dock`.
   - Move the W1 global density control into a page header. The header
     shows the name, project, branch, origin avatar and a **Review**
     link (`#chat/<id>/review`).
   - In the expanded size, pass an outline override to `Transcript`
     (W1's `density` prop). This doesn't change the saved global density.
2. **`Dock.svelte`.** Props: `{state, agentId, sessionId, stack, onAction}`.
   - **Docked and expanded:**
     - The header shows "● following agent" in follow mode, or
       "◆ <node label>" in select mode. Select mode adds a
       **Back to live** button.
     - A pin toggle.
     - A size toggle.
     - A tab bar of Inspect, Changes, Files, then Terminal and Preview
       disabled with `title="Coming in W5"`.
     - A body slot for the active tab.
     - The left edge is a drag handle that dispatches `resize`.
   - **Collapsed:** `DockStrip`, a 46px column of tab icons. Changes
     shows a red dot when the stored gate failed, and an accent dot when
     `unseenChanges` is set.
3. **Wiring.** Hold the dock state in `Chat` with `$state`, applying
   actions through the Task 4 reducer and saving through `save()`.
   - Restore the selection and size from the route (Task 3).
   - Write route changes back with `replaceState`.
4. **Tests** (`Chat.test.ts`):
   - the dock renders docked by default;
   - the strip renders when collapsed;
   - **Back to live** clears the selection;
   - Terminal is disabled.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Chat.test.ts && npx svelte-check
```

---

## Task 6: Inspect tab

**Goal:** the Inspect tab shows full node detail, relations and the last
model request (spec §6.3).

**Files:**
- `web/ui/src/lib/dock/InspectTab.svelte` (new)
- `web/ui/src/lib/dock/nodeCache.ts` (new), `nodeCache.test.ts` (new)

**Steps:**

1. `nodeCache.ts`: `createNodeCache(fetch = getNode)` with
   `get(sessionId, nodeId, wireNode, subagentId?)`.
   - The cache key is `nodeId + ':' + hash(JSON.stringify(wireNode))`,
     using a small FNV-1a over the string.
   - It holds at most 100 entries (LRU).
   - Test: same version gives one fetch; a changed wire node gives a
     refetch; LRU eviction.
2. **Follow mode:** `InspectTab` inspects the live step, which is the last
   `live` step node in the store, or the last step.
   **Select mode:** it inspects `state.selected`.
3. Render:
   - **Header:** the `glyphs.ts` glyph, headline or display + target, and
     status.
   - **Fields grid:** actor (owner `Tag`, model), why (the step's first
     narration sentence), started, duration, exit code, sandbox,
     approval, hooks count and call ID.
   - **Sub-tabs** (`Segmented`):
     - **Output:** a `<pre>` with a mono font. Edit calls show a Diff tab
       instead, rendered with the same `+`/`−` line classes `DiffView`
       uses.
     - **Args:** pretty-printed JSON.
     - **Narration** and **Thinking**, for steps.
   - **Relations:** "Likely fixed by" and "Likely caused by" lists. Each
     item is a link that dispatches `select(id)`.
   - **Last model request:** on the newest step of the latest turn only,
     a collapsible section loaded with `getLastRequest`. It shows:
     - provider/model, then an options line (thinking, streaming, max
       tokens);
     - messages (role, size, expandable content);
     - tools (name, description);
     - pack tokens against the window;
     - outcome status.
4. If `getNode` returns `'unsupported'` (an older agent), show the W1 wire
   node's capped fields with the note "Full detail needs a newer agent".

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/dock && npx svelte-check
```

Then check by hand, with an agent built from W2.1, that the tab shows
output longer than 4 KB.

---

## Task 7: Changes tab and gate strip

**Goal:** the Changes tab follows the agent's edits and the stored gate,
or filters to the selected step (spec §6.3).

**Files:**
- `web/ui/src/lib/dock/ChangesTab.svelte` (new), `GateStrip.svelte` (new)
- `web/ui/src/lib/fleet.ts`, `web/ui/src/lib/fleet.test.ts`

**Steps:**

1. In `fleet.ts`, add `'gate'` to `FleetDelta['kind']`, with an optional
   `gate?: GateRecord`.
   - `applyDeltaTo` stores `gate` on the row as `row.gate`.
   - `createFleetStore()` (`fleet.ts:100`) also exposes it.
   - Test: a `gate` delta updates the row.
2. **`GateStrip.svelte`.** Props: `{agentId, gate}`.
   - It shows ✓ passed, ✗ failed (the `failedCommand`, with the output
     tail expandable), or "skipped — proves nothing", plus how long ago it
     ran.
   - A **Run gate** button calls `runGate` and shows a spinner. The
     result also arrives through the fleet delta.
   - On mount it fetches the gate with `getGate` if the row has none.
3. **`ChangesTab.svelte`, follow mode:**
   - It renders `GateStrip`, then the file list from `getDiff(agentId)`.
   - It finds the latest edit call in the stack store (the last tool node
     with `tool.calls[*].files` set). That file is expanded with
     `getDiff(agentId, path)` and scrolled into view.
   - It reuses `DiffView`'s line rendering. Extract `DiffView`'s line loop
     into `lib/DiffLines.svelte` (props `{diff: string}`); `DiffView`
     itself keeps the same props.
4. **`ChangesTab.svelte`, select mode:**
   - It loads `getStepDiffs(sessionId)` once per open, refreshing on each
     `stack_patch` whose upserts include an edit tool node.
   - For a step selection it shows the entries whose `stepNode` equals
     the selection. For a task, it shows those whose `taskNode` does.
   - Each entry is a heading (the step headline) plus `DiffLines`.
5. **Errors:**
   - When `getDiff` errors with "not isolated" (the bridge's 502 body),
     show "Changes are tracked for isolated agents", followed by the
     telemetry `changedFiles` count from the fleet row.
   - When the stack store sees a new edit tool node, dispatch `liveEdit`.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/fleet.test.ts src/lib/dock && npx svelte-check
```

---

## Task 8: Files tab

**Goal:** browse the agent's worktree, and open files from nodes and `o`
(spec §6.3).

**Files:**
- `web/ui/src/lib/dock/FilesTab.svelte` (new), `fileTree.ts` (new), `fileTree.test.ts` (new)

**Steps:**

1. `fileTree.ts` is a store of expanded directories: a map from path to
   `entries | 'loading' | Error`. It has `toggle(path)` and
   `reveal(path)`, which expands every ancestor.
   - Tests: toggle loads once, and reveal expands ancestors in order.
2. **`FilesTab.svelte`:**
   - Left: the tree. Right: the viewer, loaded with `readFile`. It shows
     a mono font with line numbers. Binary and truncated files show a
     notice.
   - `open(path, line?)` reveals and selects the file, then scrolls to
     the line and highlights it.
   - In select mode, selecting a tool node whose target is a file path
     (any call with `files[0]`, or a `file.read` target) calls `open`.
3. Export `openFileInDock(path, line?)` through the `Chat` context, so
   Task 9's `o` key can use it.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/dock/fileTree.test.ts && npx svelte-check
```

---

## Task 9: Keys, drill-in, mirror row, rebuild static

**Goal:** `i`, `o`, `f`, `⌘J`/`\` and `⌘P` work. Subagent drill-in works.
The now bar shows the mirror row in select mode. The bundle is rebuilt.

**Files:**
- `web/ui/src/lib/transcript/browse.ts`, `browse.test.ts`
- `web/ui/src/lib/transcript/NowBar.svelte`
- `web/ui/src/views/Chat.svelte`, `Chat.test.ts`
- `web/bridge/static/**`

**Steps:**

1. In `browse.ts`, replace the W1 `toast` effect for `i`, `o` and `f` with
   `{inspect: id}`, `{openFile: id}` and `{drill: id}`. Update the W1
   tests to match.
2. In `Chat.svelte`, handle the effects:
   - **inspect:** dispatch `select(id, kind)` and `openTab('inspect')`.
   - **openFile:** find the node's file and line. The file is the first
     call's `files[0]`, or the target of `file.read` and `symbols.find`.
     The line comes from the target, if it has a `:line` suffix. Call
     `openTab('files')`, then `openFileInDock`. Without a file, show a
     toast: "No file for this row".
   - **drill:** find the subagent. That is the cursor node if its kind is
     `subagent`, otherwise the first `subagent` child of the cursor step.
     - The subagent ID is the numeric part of its node key, `sub:<id>`
       (W1 wire ID).
     - Push `{subagentId, label}` onto a `drillStack`, and create a child
       stack store with `createStackStore(sessionId, {subagentId})`.
     - A failed load (400 "no separate transcript") pops the stack, shows
       a toast, and selects the card instead.
3. **Breadcrumb:** `<agent> › <label> › …` above the transcript while
   drilled. `Backspace` in browse mode, or a click on a crumb, pops back
   and restores the parent's browse cursor. The dock passes `subagentId`
   to `getNode`.
4. **Global keys**, which are ignored while focus is in an input:
   - `⌘J`/`Ctrl+J`, and `\`, dispatch `cycleSize`;
   - `⌘P`/`Ctrl+P` dispatches `togglePin` and calls `preventDefault`, so
     the print dialog never opens;
   - `Esc` in browse mode keeps W1's behaviour, and also dispatches
     `backToLive`.
5. **`NowBar.svelte`:** in select mode, add the mirror row
   `↓ live: <live step headline>`. Clicking it scrolls the live step into
   view without changing the selection.
6. **`Chat.test.ts`:**
   - `i` selects and opens Inspect;
   - `f` on a subagent card creates a child store (with a mocked
     `getStack`) and shows the breadcrumb;
   - `Backspace` returns;
   - `⌘J` cycles the size;
   - the mirror row shows in select mode.
7. Run `npm run build` and commit `web/bridge/static` with this task.

**Verify:**

```bash
cd web/ui && npm test && npx svelte-check && npm run build
cd ../bridge && go test ./ -run 'TestAssets|TestWebIsStdlibOnly'
```

Then check by hand against an agent built from W2.1:
- browse with `Esc`; move with `j`/`k` and watch the dock follow;
- `i`, `o` and `f` work;
- `⌘J` cycles the size;
- **Run gate** updates every open tab.

---

## Final verification

```bash
cd web/ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
cd ../.. && CGO_ENABLED=1 go test ./web/... && go vet ./web/...
```

Expected: all pass, and the committed static bundle matches.

## Integration notes

- With an agent from before W2.1, every dock tab degrades: Inspect shows
  capped wire fields, Files and step filtering show "needs a newer agent",
  and Changes still works through the old diff route.
- The dock width and size persist under `marshal.ui.dock`. A future user
  record can take this key over (design §5.4).

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. Each task has a vitest target. Visual tasks also have a manual check. |
| Anchors verified? | Pre-W1 anchors were checked on `2ddc09e`: `App.svelte` `chatMatch` (`:118`), the `fleet.ts` exports (`createFleetStore` `:100`, `applyDeltaTo`), `api.ts` `getDiff`/`APIError`, `DiffView.svelte` props `{agentId}`. W1 and W2.1 names are as those plans define them. |
| Code compilable in isolation? | No verbatim code. Every step is prose, because each edit depends on W1/W2.1 files. |
| Verification per AGENTS.md and `package.json`? | Yes. |
| Placeholders? | None. |
| Matches the spec? | §6.1–§6.5. |
