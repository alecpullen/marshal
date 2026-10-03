# W1 · Foundation — phase spec

Parent design: [`docs/web-studio/design.md`](../design.md) (§9 roadmap, row W1).
Implementation plan: [`docs/web-studio/plans/2026-10-03-w1-foundation-plan.md`](../plans/2026-10-03-w1-foundation-plan.md).

## 1. Summary

W1 builds the base every later Web Studio phase sits on:

1. the transcript view model leaves the TUI (`internal/app/tui/stack` →
   `internal/viewmodel`) so the ACP server can use it;
2. a stack stream carries that model to the browser (a snapshot request plus
   incremental patches);
3. the web UI gets the Warm Sunset design system, a new shell (rail,
   grouped sidebar, ⌘K), a Home inbox, and a transcript that renders the
   stack with the TUI's browse keys;
4. agents carry owner and origin end to end, so W5's multi-user work needs
   no data-model change.

When W1 is done, a session in the web UI reads the same as in the TUI: the
same turns, tasks, step headlines, tool rows and receipts, grouped by the
same Go code.

## 2. Scope

### In scope

| Area | Deliverable |
|---|---|
| Go: view model | Package move; text helpers (`StepHeadline`, `ToolTarget`, …) move with it; JSON wire projection |
| Go: ACP | `session/stack` request; `stack_patch` session updates; `stackView` capability |
| Go: bridge | `GET /api/sessions/{id}/stack`; patches broadcast without entering the replay ring; `ownerId`/`origin`/`clientId` on `AgentStatus` |
| UI: design system | Warm Sunset tokens, Geist and Geist Mono, base components retuned |
| UI: shell | Rail, grouped sidebar, ⌘K palette, new default route |
| UI: Home | Inbox: Needs you, Ready to ship, Running, side panel |
| UI: transcript | Stack store, node renderers, density, browse keys, now bar, legacy fallback |
| UI: fix | The legacy chat store reads the wrong `session/update` shape (§6.6) |

### Out of scope (later phases)

- The session dock and its tabs, the inspector (`i`), open in editor (`o`)
  and subagent drill-in (`f`): W2.
- Live wall, New agent page, Review & ship: W2–W3. The existing Dashboard
  stays reachable at `#fleet` until the Live wall replaces it.
- Path-based routing. W1 keeps hash routing (§6.2).
- Budgets, warm pools, watches and automations in the inbox side panel.
  They need backends that don't exist yet.
- Accounts and per-user enforcement (design §5.4: designed for, built later).
- Full tool payloads beyond the wire cap (§4.3). They come with the
  inspector in W2.

## 3. The view model package

### 3.1 Move

`internal/app/tui/stack` becomes `internal/viewmodel`, with no behaviour
change. The package keeps its types (`Node`, `NodeID`, `Kind`, `Snapshot`,
`StepInfo`, `TaskInfo`, `ReceiptInfo`) and `Build`. The TUI imports it under
its new name. AGENTS.md's tree is updated.

### 3.2 Text helpers move with it

The browser must show the same words as the TUI, and `web/` can't import Go
packages. So the plain-text helpers the TUI uses to describe nodes move into
`internal/viewmodel/describe.go`, exported:

| TUI today (`internal/app/tui`) | `viewmodel` |
|---|---|
| `stepOwner`, `stepRoleWord` (`steps.go`) | `StepOwner`, `StepRoleWord` |
| `stepHeadline`, `firstSentence`, `stripEmphasis`, `inferHeadline`, `isSearchTool` (`steps.go`) | `StepHeadline`, `FirstSentence`, `StripEmphasis`, `InferHeadline`, `IsSearchTool` |
| `toolTarget` (`toolgroup.go`) | `ToolTarget` |
| `symbolSubject`, `symbolLabel`, `maxSubjectSymbols` (`symbolrow.go`) | `SymbolSubject`, `SymbolLabel`, `maxSubjectSymbols` |
| `DisplayToolName`, `toolDisplayNames` (`toolnames.go`) | `DisplayToolName`, `toolDisplayNames` |

The TUI keeps its unexported names as one-line wrappers, so its call sites
and tests don't change. Styling helpers (glyphs, colours, plurals of tool
names for group headings) stay in the TUI.

## 4. Wire projection

`internal/viewmodel/wire.go` turns a built tree into JSON-ready values.

### 4.1 Shape

```
WireTree { roots: [id…], nodes: [WireNode…] }      // parents before children

WireNode {
  id, kind, parent?, children?: [id…], live?,
  step? | tool? | message? | thinking? | subagent? | runEvent? | jobExit? | task? | receipt?
}
```

- `id` is the node's existing `NodeID.Key` (`turn:12`, `step:40`,
  `tool:40:call_1`, `task:turn:12:t3:1`, `receipt:turn:12`, …). The keys
  are already prefixed by kind and stable across rebuilds.
- `kind` is the `Kind` name: `turn`, `step`, `tool`, `message`, `final`,
  `subagent`, `runEvent`, `jobExit`, `thinking`, `passthrough`, `task`,
  `receipt`.
- At most one payload is set, chosen by kind; turn and passthrough nodes
  carry none. The user prompt is a `message` child of its turn, as in
  `Build`.
- Times are Unix milliseconds, with 0 meaning unset; durations are
  milliseconds.

### 4.2 Payloads

| Payload | Fields |
|---|---|
| `step` | `headline`, `rest`, `inferred`, `owner`, `roleWord`, `role`, `model`, `provider`, `todoId`, `heuristic`, `startedAt`, `endedAt`, `thoughts[{text,durationMs}]`, `liveThinking` |
| `tool` | `name`, `display`, `calls[]` (one per call; more than one is a merged run), `running?` |
| `tool.calls[]` | `callId`, `target`, `args`, `summary`, `output`, `error`, `exitCode`, `failed`, `approval`, `risk`, `files[]`, `role`, `notice`, `durationMs`, `at`, `truncated` |
| `tool.running` | `target`, `output` (tail), `startedAt`, `truncated` |
| `message` | `role`, `contentType`, `content`, `final`, `usage`, `salvaged`, `salvageReason`, `thinkMs`, `at` |
| `thinking` | `text`, `durationMs` |
| `subagent` | `label`, `status` (`running`/`done`/`failed`), `role`, `model`, `provider`, `toolCalls`, `currentTool`, `tokens`, `summary`, `error`, `salvaged`, `startedAt`, `endedAt`, `truncated` |
| `runEvent` | `kind` (`verifyFailed`, `gateSkipped`, `review`, `commit`, `retry`, `concern`, `taskDone`), `taskN`, `title`, `detail`, `body`, `severity`, `at`, `truncated` |
| `jobExit` | `jobId`, `command`, `exitCode`, `durationMs`, `output` (tail), `at`, `truncated` |
| `task` | `todoId`, `content`, `status`, `index`, `total`, `dropped`, `steps`, `workMs`, `tools`, `edits`, `unresolvedFailure`, `firstNarration`, `startedAt`, `completedAt` |
| `receipt` | `durationMs`, `tasks`, `steps`, `tools`, `files`, `usage`, `salvaged` |

Rules the TUI applies at render time are computed in Go and sent as fields:
`headline`/`inferred` (from `StepHeadline`), `target` (from `ToolTarget`),
`failed` (from `EventFailed`), `unresolvedFailure`, `todoId` (the effective
binding) and the receipt numbers. The browser renders them and doesn't
recompute them.

### 4.3 Size cap

Large text fields (tool `args` and `output`, subagent `summary`, run-event
`body`, job `output`) are capped at `WireTextCap` = 4096 bytes, cut on a
rune boundary, and the record sets `truncated`. Finished tool output keeps
its head. Running output and job output keep their tail, which is the part
worth reading. Full payloads come with the W2 inspector.

The cap keeps a patch small enough to send every flush. Message `content`
and step narration aren't capped: they are what the user reads.

## 5. Stack stream (ACP)

This refines design §5.2 in three ways:

- it follows the repo's ACP naming: requests under `session/…`, and every
  notification as `session/update` with a `kind`;
- the client asks for the snapshot, instead of getting it on attach;
- changes are found by comparing encoded nodes, not by version hashes.

The design doc's §5.2 is updated to match.

### 5.1 Snapshot request

```
→ session/stack { "sessionId": "s1" }
← { "sessionId": "s1", "rev": 7, "roots": ["turn:1", …], "nodes": [ WireNode… ] }
```

- Errors: an unknown session gives the same error `session/set_mode` gives.
- `initialize` advertises `sessionCapabilities.stackView: {}`.
- The first `session/stack` for a session activates its projector. Until
  then the agent emits no patches, so other ACP clients (editors) never pay
  for the stream.
- A snapshot always reflects exactly what the projector last recorded. If
  the tree changed since the last flush, the request flushes first (which
  emits a patch) and then answers with the new `rev`.

### 5.2 Patch notification

```
session/update {
  "sessionId": "s1",
  "update": { "kind": "stack_patch", "rev": 8, "baseRev": 7,
              "roots": ["turn:1", "turn:2"],
              "upsert": [ WireNode… ], "remove": ["think:live"] } }
```

- `upsert` holds full nodes: every node whose encoded JSON differs from
  what was last sent, including any parent whose `children` list changed.
- `remove` holds the IDs that were sent before and are gone now.
- `roots` is always present. It is short, and sending it every time makes
  turn order self-healing.
- A patch is sent only when something changed, and `rev` goes up by one per
  patch.

### 5.3 When the agent flushes

| Moment | Busy flag | Notes |
|---|---|---|
| Every 200 ms during a turn, if any session event arrived since the last flush | `true` | A ticker in `runTurn`'s select loop. `forward` marks the session dirty. |
| `finishTurn` | `false` | The final flush, so the receipt appears and live rows settle |
| `session/stack` request | live turn ⇒ `true` | Flush-then-answer (§5.1) |

- **Busy:** a turn is active for the session, or any tool is in flight, or
  reasoning is streaming. This matches the TUI's
  `m.busy || len(active) > 0 || len(reasoning) > 0`.
- **Idle changes:** a background subagent finishing while no turn runs
  isn't flushed in W1. The UI refetches the snapshot on `session_telemetry`
  (sent at every turn end) and when the fleet status for the agent changes.
  An idle ticker is W2 work.
- **Per-session state:** the projector lives on `TurnManager` in a map
  keyed by session, guarded by its own mutex. `TurnManager` has no
  session-close hook (its `baseRefs` cache is never pruned either), so a
  projector is dropped the next time it's touched and `Lookup` no longer
  finds the session.

### 5.4 Client rules

A client holds `rev`, `roots` and a node map.

- `patch.rev <= rev`: ignore it (a stale replay, or one already covered by
  a snapshot).
- `patch.baseRev != rev`: drop local state and refetch the snapshot.
- Otherwise: apply the upserts, delete the removed IDs, replace `roots`,
  then set `rev = patch.rev`.
- Rendering walks from `roots` through `children`. A node nothing reaches
  is garbage, and is collected after each patch.

## 6. Bridge

The bridge stays standard library only, with no shared Go types
(`TestWebIsStdlibOnly`).

### 6.1 Snapshot route

`GET /api/sessions/{id}/stack` → `Registry.Stack(ctx, sessionID)`, which
sends `session/stack` to the agent and returns the raw JSON result
unchanged.

- It uses the same auth and session lookup as `POST
  /api/sessions/{id}/mode` (`registryForSession`).
- If the agent answers method-not-found (an older agent image), the route
  responds `501` with `{"error":"stack_unsupported"}`. The UI then falls
  back to the legacy chat (§7.6).

### 6.2 Patches skip the replay ring

`Attach` stores every ACP notification in the session's 500-event ring,
which feeds SSE replay and the load endpoint's `Tail`. At five patches a
second, stack patches would push permission prompts and chat history out of
that ring within two minutes.

So `Attach` sends `session/update` notifications whose `update.kind` is
`stack_patch` through a new `EventLog.Broadcast(sessionID, payload)`. It
delivers to live subscribers with event ID 0 and stores nothing. SSE already
writes ID-0 events without an `id:` line, as it does for the overflow nudge.

A client that misses a patch (it lagged, or reconnected) sees a `baseRev`
gap, or gets the existing `replay_overflow` nudge, and refetches.

### 6.3 Owner and origin

`AgentStatus` gains `ownerId`, `origin` and `clientId` (omitempty), copied
from `Agent` in `Fleet.Snapshot`. `Agent` already stores them, and
`DefaultOwnerID` is `"local"`. The inbox's "Mine / Everyone" switch and the
origin avatars read these fields.

## 7. Web UI

### 7.1 Design system

- **Tokens.** `app.css`'s `@theme` is replaced with the Warm Sunset palette
  from design §6 (accent, violet, gold, ok, err, warn, info, and warm
  neutrals from `#121113` to `#2c2a30`), as Tailwind 4 colour tokens. It is
  dark-first, with a light variant under `[data-theme="light"]`.
- **Fonts.** Geist and Geist Mono are self-hosted through
  `@fontsource-variable/geist` and `@fontsource-variable/geist-mono`, since
  the Studio must work offline. Durations and counts use `tabular-nums`.
- **Glyphs.** The TUI glyph vocabulary is a small TS map
  (`src/lib/glyphs.ts`), so the two clients use the same symbols.
- **Components.** `Badge`, `Button`, `Card` and `Modal` are retuned to the
  tokens. New components: `Tag` (semantic and role colours), `Segmented`
  and `Kbd`.
- **Motion.** Only the live dot pulses, and never under
  `prefers-reduced-motion`.

### 7.2 Shell and routing

- **Layout:** a rail (52px) and a sidebar (256px), then the content.
- **Sidebar.** Collapsing it is remembered per browser under
  `marshal.ui.sidebar` in `localStorage`, wrapped in try/catch. It groups
  agents from the fleet SSE store as Needs you, Running, Ready to ship and
  Earlier. Within a group, agents sort by urgency, then by recency.
- **⌘K palette.** It jumps to an agent (fuzzy on name and project) or to a
  page. The SPA has no shared fuzzy matcher, so it uses a small
  subsequence-score helper.
- **Routes.** Hash routing stays. `#/` (empty) becomes Home. The existing
  routes keep working (`#chat/<id>`, `#new`, `#pending`, `#clients`,
  `#projects`, `#sessions…`, `#disk`, `#activity`). The old Dashboard moves
  to `#fleet`.
- **Rail.** Home, Agents and Projects are active. The other rail entries
  (Live, Runs, Workspaces, Watches, Library, Usage) are present but
  disabled, with a "coming in W*n*" tooltip. That way the layout doesn't
  shift as phases land.

### 7.3 Home inbox

- **Needs you:** agents whose `AgentStatus.pending` is set (permission or
  question), plus intake requests from `GET /api/pending`. Oldest first.
  - Each row shows the agent, project, origin avatar and the request's
    one-line "why".
  - Approvals answer in place, through the existing permission route.
  - Questions open the session.
- **Ready to ship:** idle agents with `changedFiles > 0` and no `prUrl`.
  Each row shows the branch, the diff stat if it's loaded, and actions:
  Review (opens the session for now) and Open PR (the existing exit panel).
- **Running:** agents whose status is running, with activity and elapsed
  time.
- **Side panel:** disk use (`GET /api/disk`) and the latest fleet activity.
- **Mine / Everyone:** filters on `ownerId === "local"`, so with one
  operator both views show everything. Origin avatars show either way.

### 7.4 Transcript

**Stack store** (`src/lib/stack.ts`): holds `{rev, roots, nodes: Map}` and
applies §5.4. It is fed by:

- `GET /api/sessions/{id}/stack` on open, on `replay_overflow`, on a
  `baseRev` gap, on `session_telemetry`, and when the agent's fleet status
  changes;
- `stack_patch` events from the session's SSE stream.

**Renderers** follow the TUI's layout:

| Element | Rendering |
|---|---|
| Turn | The prompt, then its children |
| Task row | Index/total, content, status glyph, step/tool/edit counts, work time; open, or folded when done and not `unresolvedFailure` |
| Step header | Owner tag in its role colour, headline (italic and tagged "inferred" when inferred), right-aligned duration; continuation (`rest`) as a muted line, expanded at higher density |
| Tool row | Category glyph, display name or target-first (as `subjectFirstTool`), summary, failure in `err`. A merged run shows `×n` and expands to one line per call. A running row shows a spinner and the output tail. |
| Others | Final message as markdown; thinking as "thought for Ns"; subagent card; run event; job exit; receipt line |

**Density** has three levels: outline, steps (the default) and full, as in
the TUI. A global control sits in the session header. `Enter` on a node
cycles that node's own override. Children inherit their parent's effective
density.

**Browse keys** (only active while the composer isn't focused; `Esc` blurs
the composer and enters browse):

| Key | Action |
|---|---|
| `j` / `k`, arrows | Next / previous visible node |
| `J` / `K`, `]` / `[` | Next / previous stop: a task header or a turn's first node |
| `g` / `G` | First / last node; `G` on a live node resumes follow |
| `Enter` | Toggle the node's density |
| `z` | Fold finished tasks on/off |
| `y` | Copy the node's text (headline + rest, a call's target and output, a message's content) |
| `Esc` | Leave browse, refocus the composer |

`i`, `o` and `f` are reserved for W2 and show a "coming soon" toast.

**Follow:** the view sticks to the bottom while live, until the user scrolls
or browses away. `G` resumes it.

**Now bar** sits above the composer while the agent is busy. It shows:

- the live step's owner and headline;
- the running tool's display name and target;
- elapsed time;
- Stop (the existing cancel route).

Pressing Ctrl+C twice within 1s also stops the agent, as in the TUI. `Esc`
never cancels.

### 7.5 Where the transcript lives

The transcript replaces the message list inside the existing `Chat` view, so
permissions, questions, mode, steer and the exit panel keep working
unchanged. The session page layout (dock, four states) is W2.

### 7.6 Legacy fallback and the chat-store fix

`store.ts`'s `applyACP` switches on the envelope's `method`
(`agent_message_chunk`, …), but the agent sends `method: "session/update"`
with the kind in `params.update.kind`. Streamed chat therefore never reaches
the legacy list, and the store's tests use the same wrong shape.

W1 fixes this: it unwraps `session/update` and dispatches on
`update.kind`, with tests built from the real envelope. The fixed store is
the fallback renderer when `/stack` returns `501`.

## 8. Testing

| Layer | Tests |
|---|---|
| `viewmodel` | Existing `Build` tests move unchanged. New wire tests: parent/child linkage and order, step headline and inferred headline, a running call is live, text cap on a rune boundary, every `Kind` has a name |
| `acp` | `session/stack` returns a snapshot for a known session and errors for an unknown one. The first flush after a change emits one `stack_patch` with `baseRev` = the previous `rev`. No change means no patch. A removed node appears in `remove`. No patches before activation. `finishTurn` emits a final patch with the receipt and no live nodes. The `initialize` result lists `stackView`. |
| bridge | `GET /stack` proxies the agent's result. Method-not-found → 501 `stack_unsupported`. A `stack_patch` notification reaches a live subscriber with ID 0 and is absent from `Tail`/`Replay`. Other `session/update`s are still stored. `AgentStatus` JSON carries `ownerId`/`origin`. `TestWebIsStdlibOnly` still passes. |
| UI (vitest) | Stack store: snapshot, patch apply, stale patch ignored, gap → refetch, garbage collection. Browse-key reducer: movement, stops, density toggle, fold. Legacy store with the real envelope. Sidebar grouping and sort. Palette scoring. |
| Manual | Run `marshal acp` behind the bridge, prompt an agent, and compare the web transcript with the TUI on the same session (`marshal --resume`): same turns, tasks, headlines and receipt |

## 9. Acceptance criteria

1. `go test ./...`, `go vet ./...` and `gofmt -l .` are clean, apart from
   failures already present on the base commit (listed in the plan).
2. `npm test`, `npx svelte-check` and `npm run build` in `web/ui` pass. The
   built assets in `web/bridge/static` are committed.
3. A session viewed in the web UI and in the TUI shows the same turns, task
   rows, step headlines, tool rows and receipt.
4. During a turn the transcript updates live without a reload. Reloading
   mid-turn resyncs from the snapshot.
5. The bridge's replay ring holds no `stack_patch` events.
6. With an agent that lacks `session/stack`, the session page still shows
   the streamed chat.
7. `AgentStatus` carries `ownerId` and `origin` for every agent.
