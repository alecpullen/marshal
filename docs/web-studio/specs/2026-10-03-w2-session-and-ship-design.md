# W2 · Session & ship — phase spec

Parent design: [`docs/web-studio/design.md`](../design.md) (§8.4, §8.5, §8.6; §9 row W2).
Previous phase: [W1 · Foundation](2026-10-03-w1-foundation-design.md), assumed complete.
Plans, executed in this order:
1. [W2.1 · Session backend](../plans/2026-10-03-w2-1-session-backend-plan.md)
2. [W2.2 · Session dock](../plans/2026-10-03-w2-2-session-dock-plan.md)
3. [W2.3 · Review & ship, New agent](../plans/2026-10-03-w2-3-review-and-new-agent-plan.md)

## 1. Summary

W2 turns the W1 transcript into a working session page and adds two flows:

- **A dock beside the transcript.** It has Inspect, Changes and Files tabs,
  in the design's four states: collapsed, docked or expanded, each either
  following the agent or following the user's selection.
- **A PR-style Review & ship page.** It has a "by step" view, line comments
  that reach the agent as steering, and the existing exit pipeline behind
  one Ship panel.
- **A prompt-first New agent page.**

The W1 pieces left unfinished are done here:

- `i`, `o` and `f` in browse mode;
- full payloads beyond the 4 KB wire cap;
- subagent transcripts;
- flushing the stack while no turn runs.

## 2. What W1 left in place

This spec builds on these without restating them:

| From W1 | Used for |
|---|---|
| `internal/viewmodel` (`Build`, `Project`, `WireNode`, `WireTextCap`, `StepHeadline`, `ToolTarget`, `EventFailed`) | Node detail, relations, step diffs |
| `internal/acp/stack.go`, which provides: <br>• the `stackProjector` type; <br>• `TurnManager.stacks`, `stackFor`, `markStackDirty`, `flushStack`, `flushDirtyStack`; <br>• `session/stack`, the `stack_patch` update, `stackFlushInterval`; <br>• the `stackView` capability | Subagent stacks and the idle loop extend these |
| Bridge: <br>• `Registry.Stack`; <br>• `GET /api/sessions/{id}/stack`; <br>• `ErrStackUnsupported`; <br>• `EventLog.Broadcast`; <br>• `Attach`'s `stack_patch` routing | The new routes copy this pattern |
| UI: `lib/stack.ts` (`createStackStore`), `lib/transcript/*` (`Transcript`, the renderers, `density.ts`, `browse.ts`, `NowBar`), `Tag`/`Segmented`/`Kbd`, `glyphs.ts` | The dock and review page reuse them |
| UI: the `Chat.svelte` integration and its legacy fallback | The session page grows around it |

## 3. Scope

### In scope

- ACP:
  - node detail (`session/stack_node`);
  - the last model request (`session/last_request`);
  - subagent stacks;
  - idle flushing;
  - step relations (fixed by / caused by);
  - files (`session/files`, `session/file`);
  - commit-message draft (`session/commit_draft`);
  - per-step diffs (`session/step_diffs`).
- Bridge:
  - proxy routes for all of the above;
  - `POST /api/agents/{id}/verify`, with the last gate result kept;
  - review comments, stored per agent;
  - recent prompts.
- UI:
  - the session layout and dock with Inspect, Changes and Files;
  - browse keys `i`, `o`, `f`;
  - dock keys (`⌘J`, `\`, `⌘P`);
  - URL state;
  - Review & ship;
  - New agent.

### Out of scope

| Item | Phase |
|---|---|
| Terminal and Preview tabs. They show as disabled tabs marked "W5". | W5 |
| The "Request review bot" toggle on Ship | W5 |
| The workspace chip and picker on New agent | W4 |
| The model chip on New agent | W3 |
| The Recipes tab on New agent | W5 |
| Running tasks in parallel | Not planned |

## 4. Engine (ACP)

Every new request follows the W1 rules:

- it lives under `session/…`;
- it uses `decodeParams`;
- an unknown session returns `serverErrorf("unknown session: %s", id)`;
- it is advertised in `initialize` → `sessionCapabilities`.

### 4.1 Node detail — `session/stack_node`

```
→ { "sessionId": "s1", "nodeId": "tool:40:call_1", "subagentId": 3? }
← { "node": WireNode, "detail": NodeDetail }
```

The agent builds the tree and looks the node up by `NodeID.Key`. That is
the same tree the projector serves, from the parent or, when `subagentId`
is given, from the subagent's child state (§4.3). An unknown ID returns
`invalidParamsError("unknown node: …")`.

`NodeDetail` is uncapped. Its size is bounded by what the engine already
bounds: tool results by the registry's result cap, narration by the model.

| Field | For | Content |
|---|---|---|
| `calls[]` | tool | Per call:<br>• full `args` and `originalArgs` (pretty-printed JSON);<br>• full `output`, or `diff` for a diff tool;<br>• `model`, `finishReason`, `rewritten`;<br>• `hooks[]` (the `HookMetadata` JSON);<br>• `sandbox` (`SandboxMeta` as JSON);<br>• `symbols[]` `{file,name,kind}`;<br>• `notice` `{kind,text}`;<br>• `stepId` |
| `narration[]` | step | Each narration message's full text |
| `thinking[]` | step | `{text, durationMs}`, plus `live` |
| `todo` | step, task | `{id, content, status}` from the current todo list |
| `relations` | tool, step | `{causedBy: [nodeId], fixedBy: [nodeId]}`, see §4.5 |

This mirrors the TUI inspector (`browse_inspect.go` `toolDetail`,
`stepDetail`). The TUI keeps its own inspector, and both read the same
`session.State`.

### 4.2 Last model request — `session/last_request`

```
→ { "sessionId": "s1" }
← { "request": RequestInspectionJSON | null }
```

This returns `State.RequestInspection()` projected into a JSON type that
lives in `internal/acp`. The session type has no JSON tags, so the field
names are camelCase copies. The engine keeps only the latest attempt, so
the Inspect tab shows it only for the newest step of the latest turn.

### 4.3 Subagent stacks

`session/stack` and `session/stack_node` take an optional
`subagentId int64`. With it, the agent:

- finds the subagent with `State.Subagent(id)` (`session/subagents.go:313`);
- builds from `SubagentView.Child`, with `Drilled: true`;
- keys the projector as `"<sessionId>#<subagentId>"`.

A `stack_patch` from a child projector also carries `"subagentId"`. A
subagent with no child state (pipeline roles that share the parent state)
returns `invalidParamsError("subagent has no separate transcript")`. The UI
then selects the subagent card instead of drilling in.

### 4.4 Idle flushing

W1 flushes only during turns. In W2, activating a projector starts one idle
loop per session:

- It subscribes to `rt.Events` and marks the session dirty on every event.
- Every `stackIdleInterval` (500 ms) it flushes the parent projector if
  the session is dirty and `HasActiveTurn` is false. The turn loop owns
  flushing while a turn runs.
- Child projectors of the session are flushed on every parent flush
  (turn ticker, idle tick, `finishTurn`), whether or not they are dirty.
  Child trees are small, and a child state publishes no events of its own.
- The loop ends, and the projector map entries for the session are
  removed, when `Lookup` stops finding the session.

### 4.5 Relations: fixed by / caused by

Add `viewmodel.Relations(items []session.TranscriptItem) RelationIndex`.
It is pure and tested with fixtures. It is keyed by command, the
shell-family target (`ToolTarget`):

- **fixed by:** for a failed shell-family call `F`, the steps containing
  edits after `F` and before the next successful call with the same
  command.
- **caused by:** for a failed call `F` whose command succeeded earlier,
  the steps containing edits after that last success and before `F`.
- **A step's relations** are the union of its calls' relations.
- **Nothing to show:** a command that never failed, or never passed again,
  has empty lists.

These are heuristics and are labelled as such in the UI ("likely fixed by").

### 4.6 Files — `session/files`, `session/file`

```
→ session/files { sessionId, path: "" | "internal/app" }
← { "root": "/abs/active/root", "path": "internal/app",
    "entries": [ { "name": "app.go", "dir": false, "size": 18342 }, … ] }

→ session/file { sessionId, path: "internal/app/app.go" }
← { "path": "…", "size": 18342, "binary": false, "truncated": false, "content": "…" }
```

- Paths resolve against the session's `Workspace().ActiveRoot` through
  `native.SafeResolve`. That function rejects absolute paths, `..`, and
  symlinks that escape the root. Because the agent resolves its own paths,
  no container path translation is needed.
- `.git` is never listed.
- Entries sort directories first, then by name.
- Content is capped at 1 MiB. A larger file returns its first 1 MiB with
  `truncated: true`.
- A file is binary if it contains a NUL byte within its first 8 KiB.

### 4.7 Commit draft — `session/commit_draft`

```
→ { sessionId }
← { "message": "…" }
```

This calls `ExitManager.draftMessage` (the same drafting `session/commit`
does for an empty message) and commits nothing. The Ship panel calls it to
prefill the message.

### 4.8 Step diffs — `session/step_diffs`

```
→ { sessionId }
← { "steps": [ { "stepNode": "step:40", "turnNode": "turn:12", "taskNode": "task:turn:12:t3:1" | "",
                 "headline": "…", "at": 1696…, "files": ["a.go"], "diff": "unified diff…" } ] }
```

- The entries are the session's edit calls (`file.write_patch`,
  `patch.apply`), grouped by step in time order.
- `diff` joins the calls' `ResultContent`.
- Node IDs and headline come from the built tree, so the UI can link each
  entry to its transcript node.

### 4.9 Capabilities

`initialize` adds the following keys, each mapping to `{}`:

- `stackNode`
- `lastRequest`
- `subagentStacks`
- `filesView`
- `commitDraft`
- `stepDiffs`

## 5. Bridge

Every proxy route below follows the W1 `sessionStack` pattern:

- `registryForSession`, then a `Registry` method that sends the ACP request
  and returns the raw result;
- method-not-found maps to `501 {"error":"<feature>_unsupported"}`, using
  the shared `ErrUnsupported{Feature}` type that replaces W1's
  `ErrStackUnsupported` (which becomes `ErrUnsupported{"stack"}`).

| Route | ACP |
|---|---|
| `GET /api/sessions/{id}/stack?subagent=N` | `session/stack` (+`subagentId`) |
| `GET /api/sessions/{id}/nodes/{nodeId}?subagent=N` | `session/stack_node`. `nodeId` is path-escaped. |
| `GET /api/sessions/{id}/last-request` | `session/last_request` |
| `GET /api/sessions/{id}/step-diffs` | `session/step_diffs` |
| `GET /api/agents/{id}/files?path=` | `session/files` on the agent's session |
| `GET /api/agents/{id}/file?path=` | `session/file` |
| `GET /api/agents/{id}/commit-draft` | `session/commit_draft` |

New bridge-owned routes:

| Route | Behaviour |
|---|---|
| `POST /api/agents/{id}/verify` | Runs `verifySession` (`exit.go`) and stores the result as the agent's live gate (`liveState.gate`, with a timestamp). Returns the `gateResult`. Also broadcast on the fleet stream as `{kind:"gate", sessionId, gate}`. |
| `GET /api/agents/{id}/gate` | The stored gate result and when it ran, or 204 if none |
| `GET /api/agents/{id}/review/comments` | The agent's review threads |
| `POST /api/agents/{id}/review/comments` | Body `{path, line, side, quote, body}`. Stores the comment, then sends it to the agent: by steering if a turn is active, otherwise as a new prompt. Audited as `review_comment`. |
| `POST /api/agents/{id}/review/comments/{cid}/resolve` | Marks the comment resolved |
| `GET /api/prompts/recent?project=&limit=20` | Distinct non-empty `Agent.Prompt` values for the project, newest first |

### 5.1 Review comments

`ReviewComment` has these fields:

- `id`, `agentId`, `path`, `line`, `side` (`old` or `new`), `quote`,
  `body`;
- `createdAt`, `sentAt`, `resolvedAt`;
- `ownerId` (always `DefaultOwnerID`).

Storage:
- They are stored in `fleet.json` as `reviews` (a map from agent ID to
  comments). This needs a workspace migration from 6 to 7.
- Removing an agent removes its comments.

The text sent to the agent is:

```
Review comment on <path>:<line> (<side>):
> <quote, up to 6 lines>
<body>
```

The bridge doesn't track the agent's reply. The UI shows the first
`final` stack node after `sentAt` as the reply (§6.6).

## 6. Web UI

### 6.1 Session layout

- `#chat/<id>` keeps the W1 transcript in a centred column (max 780px),
  with a dock on the right.
- The page header shows:
  - the agent's name, project, branch and origin avatar;
  - the W1 global density control;
  - a **Review** button that links to `#chat/<id>/review`.

### 6.2 Dock: states and modes

**Width** (`dockSize`):

| State | Width |
|---|---|
| `collapsed` | 46px strip |
| `docked` | 440px default, resizable 320–720px by dragging the left edge |
| `expanded` | 65% of the content width |

**Mode** (`dockMode`):
- `follow`: nothing is selected, and the dock tracks the agent.
- `select`: the dock tracks the selected node.

**Tabs:** Inspect, Changes, Files, plus Terminal and Preview disabled with
"W5".

**Pinning.** A pinned tab doesn't switch when the selection changes. `⌘P`
toggles the pin on the active tab.

**Entering and leaving select mode:**
- `Esc` from the composer enters browse (W1). The browse cursor *is* the
  selection: moving the cursor updates the dock.
- Clicking a node also selects it.
- `Esc` in browse, or the dock's "Back to live" button, returns to
  follow mode.

**In select mode:**
- The W1 now bar shows a mirror row, `↓ live: <live step headline>`.
- Clicking the mirror row scrolls to the live step without leaving select
  mode.

**Collapsed strip:**
- one icon per tab;
- badges: a red dot on Changes when the stored gate failed, and an
  unread dot on Changes when files changed since the tab was last seen;
- clicking an icon docks the dock and opens that tab.

**Expanded:**
- The transcript switches to outline density, as a local override that
  doesn't change the saved global density.
- The composer stays under the outline.
- Picking an outline entry selects it.

**Auto-switching tabs** (unless pinned):
- Selecting a tool or step node switches to Inspect.
- In follow mode, a new edit call switches to Changes if the dock is
  docked and not pinned.

**Keys:**

| Key | Action |
|---|---|
| `⌘J`, or `\` outside inputs | Cycle collapsed → docked → expanded |
| `i` (browse) | Select the cursor node and open Inspect |
| `o` (browse) | Open the node's file in Files, at the line when known |
| `f` (browse) | Drill into the subagent under the cursor (§6.5) |
| `⌘P` | Pin or unpin the active tab |

**URL state:** `#chat/<id>?node=<nodeId>&dock=<state>&tab=<tab>`.
- `routes.ts` gains the query parsing.
- Loading such a URL restores the selection and dock state.
- Width and default state persist per browser under `marshal.ui.dock`
  (try/catch, as in W1).

### 6.3 Dock tabs

**Inspect** shows a node, loaded with `GET …/nodes/{nodeId}`. The data is
cached per `{nodeId, version}`; the version is the hash of the W1 wire
node's JSON, so a changed node is refetched. It shows:
- **Header:** the kind glyph, headline or target, and status.
- **Fields:**
  - actor (owner tag and model);
  - why (the step's first narration sentence);
  - started, duration, exit code;
  - sandbox and approval;
  - hooks;
  - call ID.
- **Sub-tabs:** Output (or Diff for edits), Args, Narration, Thinking.
- **Relations:** "Likely fixed by" and "Likely caused by", as node links
  that select that node.
- **Last model request** (only on the newest step of the latest turn). A
  collapsible section showing:
  - provider and model, then an option summary;
  - the message list (role, size and collapsible content);
  - the tool list;
  - pack tokens against the window;
  - the outcome.

**Changes** shows the working-tree diff.
- Data comes from the existing `GET /api/agents/{id}/diff`, using the
  `DiffView` row style and lazy per-file loading.
- **Follow mode:**
  - The most recently edited file, from the latest edit call's `files`,
    is expanded and scrolled to.
  - A gate strip at the top shows the stored gate result (pass, fail with
    the failing command, or skipped), how long ago it ran, and a **Run
    gate** button.
- **Select mode:**
  - The diff is filtered to the selected step's hunks, from
    `GET …/step-diffs`.
  - A task shows the union of its steps' hunks.
- **Non-isolated sessions:** the diff route returns an error, so the tab
  shows "Changes are tracked for isolated agents" with the session's
  changed-file count from telemetry.

**Files** shows the worktree tree.
- It lazy-loads directories with `GET …/files?path=`.
- The viewer (`GET …/file?path=`) shows a monospace view with line
  numbers, and highlights a line when opened at one.
- Binary and truncated files show a notice.
- In select mode, selecting a node whose call targets a file opens that
  file.
- `o` uses the same path.

### 6.4 Now bar in select mode

The mirror row is described in §6.2. Stop and double Ctrl+C work as in W1.

### 6.5 Subagent drill-in

When the browse cursor is on a subagent card, or on a step whose children
include one, `f` drills in:
- The transcript is replaced by that subagent's stack
  (`createStackStore(sessionId, {subagentId})`).
- A breadcrumb shows `<agent> › <subagent label>`.
- `Backspace`, or the breadcrumb, returns to the parent at the same
  cursor.
- The dock works inside the drilled view, using `subagent=` on node
  requests.
- A 400 "no separate transcript" error shows a toast and selects the card
  instead.

### 6.6 Review & ship (`#chat/<id>/review`)

**Left panel (320px):**
- **Gate checklist:** the stored gate result with a **Run gate** button,
  plus the existing override-with-reason (`GateResult.svelte`).
- **Files:** changed-file list with `+/−` counts and "viewed" ticks.
- **Summary:** the agent's final messages, one per turn (from the stack
  store's `final` nodes), newest first.
- **Ship:**
  - The commit message is prefilled from `GET …/commit-draft`, and is
    editable.
  - A toggle "Open a pull request" appears only for push destinations
    (`exitDestination`).
  - A disabled toggle "Request review bot (W5)".
  - Actions:

    | Action | Calls | Shown for |
    |---|---|---|
    | Discard | `discardAgent`, with a confirm | all |
    | Merge locally | `mergeAgent` | merge destination |
    | Push & open PR | `exitAgent` | push destination |
    | Download patch | `patchUrl` | patch destination |

    A merge refusal shows `mergeRefusalMessage`. A blocked exit shows the
    gate and the override.

**Diff (centre):**
- **Unified or split** (Segmented). Split view is built client-side from
  the unified diff.
- **Viewed marks:** a per-file "viewed" checkbox, keyed by agent, path and
  a hash of the file's diff, in `localStorage`. A file changed since it
  was viewed un-ticks itself.
- **Line comments:** clicking a line number opens a comment box, which
  posts through the review-comments route.
  - Threads show inline under their line.
  - A thread lists:
    - the comment;
    - "sent to agent";
    - the reply, which is the first `final` stack node after `sentAt`
      (linked into the transcript);
    - the agent's commits since then (from a diff refresh).
  - **Resolve** resolves the thread.
- **By step:** a toggle switches to hunks grouped by task → step, from
  `step-diffs`, with step headlines as group headings.
  - A hunk later rewritten by another step is marked "rewritten in step N".
    This is decided client-side: the same file, and its line range
    overlaps a later step's hunk.
  - "Net effect" (the working-tree diff) stays the default view.

### 6.7 New agent (`#new`)

The existing `NewAgent.svelte` is rewritten prompt-first.

- **Prompt box:** a large textarea with autofocus. `⌘Enter` spawns.
- **Chips under the prompt**, each opening a popover:

  | Chip | Default | Choices |
  |---|---|---|
  | **Project** | Last used | Projects from `listProjects` |
  | **Branch** | Isolation on, generated branch name | Editable `branch` and `baseRef` |
  | **Mode** | `edit` | `plan`, `default`, `edit`, `copilot`, `auto` (the ACP set; `read` from `store.ts` is not offered) |
  | **Isolation** | On when the project supports it (`ProjectStatus.isolation`) | On or off |

  The last-used project and mode are remembered per browser.
- **Tabs under the prompt:**
  - **Issues:** the existing `IssuePicker`, for the project's registered
    repo. Picking an issue fills the prompt with the issue's text and
    keeps the link.
  - **Recent prompts:** from `GET /api/prompts/recent`. Clicking one fills
    the prompt.
- **Spawn:** calls `spawnAgent`, then navigates to `#chat/<agentId>`. A
  warning in the response shows as a toast.

## 7. Testing

| Layer | Tests |
|---|---|
| `viewmodel` | `Relations`: fixed-by, caused-by, never-repassed, and the step union. A step-diff grouping helper, if it lives in `viewmodel`. |
| `acp` | Each new method: success, unknown session, bad params. `stack_node` for tool, step and task nodes, and the subagent variant. Subagent projector patches carry `subagentId`. The idle loop flushes after an event with no turn, and stops when the session is gone. `session/files` rejects `..`, absolute paths and escaping symlinks, and hides `.git`. `session/file` handles binary and truncated files. `commit_draft` commits nothing. `step_diffs` groups by step. Capabilities are listed. |
| bridge | Each proxy route: success, 404, 501. Verify stores the gate and broadcasts it. Review comments: create (steer vs prompt), list, resolve, removed with the agent, the v6→v7 migration, and the audit entry. Recent prompts: dedup and order. `TestWebIsStdlibOnly`. |
| UI | Dock reducer: states, modes, pin, auto-switch, URL round-trip. Inspect rendering from a fixture. Changes in follow vs select mode. Files tree and viewer. Drill-in breadcrumb. Review: split-view builder, viewed-hash reset, the rewritten-hunk marker, a comment posts and renders its thread. New agent: chip defaults, spawn calls the API and navigates. |

## 8. Acceptance criteria

1. In browse mode, `i`, `o` and `f` all work. The dock follows the cursor,
   and `Esc` returns to live.
2. In follow mode, the Changes tab shows the file being edited and the
   stored gate. **Run gate** updates it on every open browser through the
   fleet stream.
3. Inspect shows full output beyond 4 KB, plus relations where the
   heuristics apply, and the last model request on the newest step.
4. A subagent with its own transcript can be drilled into, and it updates
   live.
5. A background change while no turn is running reaches the transcript
   within a second.
6. On the review page, a line comment reaches the agent (by steering or as
   a prompt) and its reply appears in the thread. By step groups hunks
   under the right step headlines. Ship runs the existing exit, merge or
   patch path.
7. New agent spawns from the prompt box with the chosen chips and opens the
   session.
8. All Go and UI suites pass as in W1's final verification.
