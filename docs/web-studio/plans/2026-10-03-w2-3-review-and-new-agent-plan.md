# W2.3 · Review & ship, New agent — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w2-session-and-ship-design.md`](../specs/2026-10-03-w2-session-and-ship-design.md) §6.6–§6.7
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Track:** UI. See [`../README.md`](../README.md) for both tracks.
**Runs after:** [W2.2](2026-10-03-w2-2-session-dock-plan.md) (previous UI plan), with everything it depends on.
**Base:** a branch containing every plan listed under Runs after. Code from before W1 was checked
on `2ddc09e`. W1 and W2.x names are as those plans define them.
**Plan slug:** `w2-3-review-and-new-agent`. Commit each task as
`w2-3-review-and-new-agent: task N — <title>`.

## Goal

`#chat/<id>/review` is a PR-style review page:

- gate, files, summary and Ship;
- unified and split diffs with viewed marks;
- line comments sent to the agent, and a "by step" view.

`#new` is the prompt-first New agent page.

## Non-goals

- The review-bot toggle stays disabled (W5).
- Workspace, model and recipes on New agent (W4, W3, W5).

## Assumptions

- **From W2.1** (bridge routes):
  - `GET /api/agents/{id}/commit-draft`;
  - `GET`/`POST /api/agents/{id}/review/comments`, plus `…/{cid}/resolve`;
  - `GET /api/prompts/recent`;
  - `POST /api/agents/{id}/verify` and `GET …/gate`;
  - `GET /api/sessions/{id}/step-diffs`.
- **From W2.2** (UI):
  - `api.ts` has `getStepDiffs`, `runGate` and `getGate`;
  - `parseChatRoute` and `formatChatRoute` exist;
  - `lib/DiffLines.svelte` and `lib/dock/GateStrip.svelte` exist;
  - `fleet.ts` handles `gate` deltas.
- **Unchanged from before W1:**
  - `GateResult.svelte` (props `{result, onOverride}`);
  - `ExitPanel.svelte` (props `{agentId, onDone}`);
  - `exit.ts` `exitDestination`;
  - `diff.ts` `diffTotals` and `mergeRefusalMessage`;
  - `api.ts`: `getDiff`, `mergeAgent`, `discardAgent`, `exitAgent`,
    `patchUrl`, `spawnAgent`, `listProjects`, `getConfig`;
  - `IssuePicker.svelte` (props `{repoId?}`);
  - `NewAgent.svelte` (props `{onDone}`).

---

## Task 1: API additions for review and new agent

**Goal:** client functions for commit drafts, review comments and recent
prompts.

**Files:**
- `web/ui/src/lib/api.ts`, `web/ui/src/lib/api.test.ts`

**Steps:**

1. Add the type `ReviewComment` with fields `id`, `agentId`, `path`,
   `line`, `side`, `quote`, `body`, `createdAt`, `sentAt` and
   `resolvedAt?`. These are W2.1 Task 12's JSON names.
2. Add these functions:
   - `getCommitDraft(agentId)`, which resolves the `message` string, or
     `'unsupported'` on 501;
   - `listReviewComments(agentId)`;
   - `postReviewComment(agentId, {path, line, side, quote, body})`;
   - `resolveReviewComment(agentId, id)`;
   - `recentPrompts(project, limit = 20)`.
3. Add tests that mock `fetch`. Check each function's URL and body, and
   the 501 mapping for the draft.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/api.test.ts
```

---

## Task 2: Unified-diff model

**Goal:** pure helpers that parse diffs, build split view, hash a file for
viewed marks, and detect hunks rewritten by a later step.

**Files:**
- `web/ui/src/lib/review/unified.ts` (new), `unified.test.ts` (new)

**Steps:**

1. **Types and parsing.** Define the types:

   ```ts
   type Line = { kind: 'ctx'|'add'|'del'; old?: number; new?: number; text: string }
   type Hunk = { oldStart: number; oldLines: number; newStart: number; newLines: number; lines: Line[] }
   type FileDiff = { path: string; hunks: Hunk[] }
   ```

   Then add `parseUnified(diff: string): FileDiff[]`.
   - It handles `diff --git`, `---` and `+++` headers, `@@ -a,b +c,d @@`
     headers, `\ No newline at end of file`, and new or deleted files
     (where the path is `/dev/null`).
   - `path` is the `b/` path, or the `a/` path for a deletion.
2. **Split view.** `toSplit(h: Hunk): {left?: Line; right?: Line}[]`
   pairs each run of deletions with the run of additions that follows it,
   row by row. Context lines appear on both sides.
3. **Viewed marks.** `fileHash(f: FileDiff): string` is an FNV-1a hash of
   the file's hunks. `viewedKey(agentId, path)` gives
   `marshal.review.viewed.<agentId>.<path>`.
   `isViewed(agentId, f)` and `setViewed(agentId, f, on)` store the hash
   in `localStorage`, inside try/catch. A stored hash that no longer
   matches counts as not viewed.
4. **Rewritten hunks.** `rewrittenBy(steps: {stepNode; files: FileDiff[]}[]): Map<string, string>`
   maps a hunk key (`stepNode|path|newStart`) to the later `stepNode` that
   rewrote it. A hunk counts as rewritten when a later step has a hunk in
   the same file whose old range overlaps the earlier hunk's new range.
5. **Tests:**
   - parse a two-file diff, including a new file;
   - split pairing with uneven runs;
   - a hash change resets the viewed mark (stub `localStorage`);
   - overlap detection, both overlap and adjacent-but-not-overlapping.

**Verify:**

```bash
cd web/ui && npx vitest run src/lib/review/unified.test.ts
```

---

## Task 3: Review page — layout, left panel and Ship

**Goal:** `#chat/<id>/review` renders the left panel: gate, files, summary
and Ship.

**Files:**
- `web/ui/src/views/Review.svelte` (new), `Review.test.ts` (new)
- `web/ui/src/lib/review/ShipPanel.svelte` (new)
- `web/ui/src/App.svelte`

**Steps:**

1. In `App.svelte`, when `parseChatRoute(hash)?.view === 'review'`, render
   `Review` with `{agentId: id}` in place of `Chat`.
2. **`Review.svelte` layout.**
   - A header holds the agent name, a "← Session" link (`#chat/<id>`) and
     a `Segmented` control for Net effect / By step.
   - Below it, a left column (320px) sits beside the diff area.
3. **Left column:**
   - **Gate:** `GateStrip` (W2.2), with `GateResult.svelte`'s override
     form shown when the gate failed or was skipped.
   - **Files:** each file from `getDiff(agentId).files` with its
     `+added −removed` count and a viewed tick (`isViewed`). Clicking a
     file scrolls the diff to it.
   - **Summary:** final messages from a stack store (W1
     `createStackStore`), one per turn, newest first, with the content as
     markdown (`markdown.ts`).
4. **`ShipPanel.svelte`.** Props: `{agent: AgentRow}`.
   - On mount, prefill a textarea from `getCommitDraft`. On
     `'unsupported'` or an error, leave it empty with the placeholder
     "Commit message".
   - Show "Open a pull request", checked and disabled, when
     `exitDestination(agent) === 'push'`, with the note "PR is created on
     push". Show "Request review bot", disabled, with the title
     "Coming in W5".
   - Actions by destination:

     | Destination | Action | Calls |
     |---|---|---|
     | `merge` | **Merge locally** | `mergeAgent(id, message)`; a refusal shows `mergeRefusalMessage` |
     | `push` | **Push & open PR** | `exitAgent(id, {commitMessage})`; `blocked` shows the gate and the override, which re-calls with `override:{reason}` |
     | `patch` | **Download patch** | links to `patchUrl(id)` |
     | all | **Discard** | `discardAgent` after a confirm `Modal` |

   - A success shows the PR link or "Merged", and returns to the session
     page.
5. **Tests:**
   - the left column renders from fixtures;
   - the draft prefills;
   - the push path calls `exitAgent` with the edited message;
   - a blocked result shows the override;
   - the discard confirm.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Review.test.ts && npx svelte-check
```

---

## Task 4: Review diff — unified/split, viewed and comments

**Goal:** the diff area renders every file with viewed marks, unified or
split view, and inline comment threads.

**Files:**
- `web/ui/src/lib/review/FileDiff.svelte` (new), `CommentThread.svelte` (new)
- `web/ui/src/views/Review.svelte`, `Review.test.ts`

**Steps:**

1. **Loading the diff.** `Review` loads each file's diff with
   `getDiff(agentId, path)` lazily, as it scrolls into view (an
   `IntersectionObserver`, with a fallback to loading everything when it's
   unavailable). It parses each diff with `parseUnified`. A Unified /
   Split `Segmented` control is persisted under
   `marshal.ui.review.view`.
2. **`FileDiff.svelte`.** Props: `{agentId, file: FileDiff, view, comments, onComment, onResolve}`.
   - **Header:** the path, `+/−` counts and a viewed checkbox
     (`setViewed`). A viewed file collapses.
   - **Body:** a unified table, or split rows from `toSplit`.
   - **Commenting:** line numbers are buttons. A click opens an inline
     composer under that line, with side `new` for added and context
     lines and `old` for deletions. The composer's quote is the clicked
     line plus up to 5 lines before it.
3. **`CommentThread.svelte`.** It renders, in order:
   1. The comment body.
   2. "Sent to agent · <time>".
   3. The reply. This is the first `final` node in the stack store whose
      `message.at` is later than `sentAt`. It shows as markdown with a
      link to `#chat/<id>?node=<nodeId>`. Until then it shows "Waiting
      for the agent…".
   4. "N new commits": from a diff refresh, triggered by the reply.
   5. A **Resolve** button, which calls `resolveReviewComment`. A resolved
      thread collapses to one line.
4. **Data.** `Review` loads `listReviewComments` and posts with
   `postReviewComment`. After posting, it refreshes the comments, and it
   refreshes the diff when a `session_telemetry` event arrives.
5. **Tests:**
   - unified and split render a fixture;
   - ticking a file as viewed collapses it;
   - clicking a line opens the composer and posts the right path, line,
     side and quote;
   - the thread shows a reply once the stack fixture holds a later
     `final` node.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Review.test.ts src/lib/review && npx svelte-check
```

---

## Task 5: By-step view

**Goal:** the By step toggle groups hunks under task and step headings,
and marks hunks that a later step rewrote.

**Files:**
- `web/ui/src/lib/review/ByStep.svelte` (new)
- `web/ui/src/views/Review.svelte`, `Review.test.ts`

**Steps:**

1. `ByStep` loads `getStepDiffs(sessionId)` and parses each entry's `diff`
   with `parseUnified`. It groups the entries by `taskNode`, using the
   task content from the stack store, or "No task" when `taskNode` is
   empty, then by step.
2. **Headings:**
   - a task heading shows `index/total` and the content;
   - a step heading shows the owner `Tag`, the headline, and a link to
     `#chat/<id>?node=<stepNode>`.
3. Each hunk renders with `FileDiff` in unified view and no viewed
   checkbox. A hunk in `rewrittenBy(...)` gets the badge "rewritten in
   <later step headline>", linking to that step.
4. If `getStepDiffs` returns `'unsupported'`, show "By step needs a newer
   agent" and fall back to Net effect.
5. **Test:** two steps editing overlapping lines of one file; the earlier
   hunk is badged.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/Review.test.ts
```

---

## Task 6: New agent (prompt first)

**Goal:** `#new` is the prompt-first page in spec §6.7.

**Files:**
- `web/ui/src/views/NewAgent.svelte` (rewritten; props `{onDone}` kept)
- `web/ui/src/lib/newagent/Chip.svelte` (new), `newAgent.ts` (new), `newAgent.test.ts` (new)
- `web/ui/src/views/NewAgent.test.ts` (new)

**Steps:**

1. **`newAgent.ts`:**
   - `defaults(projects: ProjectStatus[], remembered)` returns
     `{project, mode: 'edit', isolated, branch: '', baseRef: ''}`, where
     `isolated` is the project's `isolation` support.
   - `remember(choice)` and `loadRemembered()` use
     `marshal.ui.newagent`, inside try/catch.
   - `MODES = ['plan','default','edit','copilot','auto']`. These are the
     ACP set; don't reuse `store.ts`'s `MODES`, which includes `read`.
   - Tests: defaults, remembered project and mode, and the mode list.
2. **`Chip.svelte`.** Props: `{label, value, children}`. It renders a
   button showing `label: value`, which opens a popover slot. `Esc` and
   an outside click close it.
3. **`NewAgent.svelte`.**
   - **Prompt:** a large autofocused textarea; `⌘Enter`/`Ctrl+Enter`
     spawns.
   - **Chips:**
     - Project: a list from `listProjects`, unavailable projects
       disabled.
     - Branch: `branch` and `baseRef` inputs. These are disabled when
       isolation is off.
     - Mode: a radio list.
     - Isolation: a toggle, disabled with an explanation when the
       project's `isolation` is unsupported.
   - **Tabs:**
     - Issues: `IssuePicker`, with `repoId` set to the project's
       registered repo ID if `getConfig` lists one. Picking an issue sets
       the prompt to the issue title and body. If `IssuePicker` lacks a
       selection callback, add an optional `onPick(issue)` prop to it,
       keeping today's behaviour when the prop is absent.
     - Recent prompts: `recentPrompts(project)`. A click fills the
       prompt.
   - **Spawn:** call
     `spawnAgent({project, prompt, mode, isolated, branch, baseRef})`,
     then `remember`, then `onDone(agentId)`. App navigates to
     `#chat/<id>`. A `warning` shows as a toast. An error shows inline
     and keeps the prompt.
4. **`NewAgent.test.ts`:**
   - chips show the defaults;
   - `⌘Enter` calls `spawnAgent` with the chosen values;
   - a recent prompt fills the textarea;
   - isolation is disabled for an unsupported project.

**Verify:**

```bash
cd web/ui && npx vitest run src/views/NewAgent.test.ts src/lib/newagent && npx svelte-check
```

---

## Task 7: Rebuild static and final check

**Goal:** the committed bundle includes W2.3.

**Files:** `web/bridge/static/**`

**Steps:**

1. Run `cd web/ui && npm run build`.
2. Commit `web/bridge/static`.

**Verify:**

```bash
cd web/ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
cd ../bridge && go test ./ -run 'TestAssets|TestWebIsStdlibOnly'
```

Check by hand:
- open Review on an isolated agent with edits;
- leave a line comment and watch the agent respond;
- toggle By step;
- Ship through Merge locally on a local agent;
- spawn from `#new`.

---

## Final verification

```bash
CGO_ENABLED=1 go test ./... ; go vet ./... ; gofmt -l .
cd web/ui && npm test && npx svelte-check && npm run build && git status --porcelain ../bridge/static
```

Expected:
- Go fails only on the five known tests;
- the UI passes;
- the bundle is clean.

## Integration notes

- Review comments persist on the bridge (W2.1, workspace v7). Viewed
  marks are per browser.
- Phases that change these screens later:
  - W3 adds the model chip on New agent;
  - W4 adds the workspace chip and picker;
  - W5 enables "Request review bot" and adds the Recipes tab.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. Pure logic (Task 2) comes before the components that use it. Each task has a vitest target. |
| Anchors verified? | Pre-W1 UI files and props were checked on `2ddc09e`: `GateResult.svelte`, `ExitPanel.svelte`, `exit.ts`, `diff.ts`, the `api.ts` functions, `IssuePicker.svelte` `{repoId?}`, `NewAgent.svelte` `{onDone}`. The `store.ts` `MODES` includes `read`. W2.x names are as defined. |
| Code compilable in isolation? | The only verbatim block is three TypeScript type declarations in Task 2, with explicit types, so they compile under `strict`. Everything else is prose. |
| Placeholders? | None. Disabled controls name the phase that enables them. |
| Matches the spec? | §6.6–§6.7. |
