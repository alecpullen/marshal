# W2.1 · Session backend — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w2-session-and-ship-design.md`](../specs/2026-10-03-w2-session-and-ship-design.md) §4–§5
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Track:** backend. See [`../README.md`](../README.md) for both tracks.
**Runs after:** [W1.1](2026-10-03-w1-1-backend-plan.md) (previous backend plan), with everything it depends on. Backend plans never depend on UI plans.
**Base:** a branch containing every plan listed under Runs after. Anchors into code from before W1
were checked on `2ddc09e`. Anchors into code that W1.1 creates are named as
the W1.1 plan defines them (W1.1 Tasks 2–8).
**Plan slug:** `w2-1-session-backend`. Commit each task as
`w2-1-session-backend: task N — <title>`.

## Goal

The agent and bridge serve everything the W2 session page and review page
need:

- full node detail, the last model request, subagent stacks, and stack
  flushing while no turn runs;
- step relations, worktree files, a commit draft, and per-step diffs;
- a stored verify gate, review comments, and recent prompts.

## Non-goals

- Any UI work. That is W2.2 and W2.3.
- Terminal, Preview and the review bot (W5); workspaces (W4); model
  selection (W3).

## Assumptions

- W1 is merged. In particular, all of these exist as W1 defines them:
  - `internal/viewmodel/{describe,wire}.go`;
  - `internal/acp/stack.go`, with `stackProjector`, `TurnManager.stacks`,
    `stacksMu`, `stackFor`, `markStackDirty`, `flushStack`,
    `flushDirtyStack`, `stackSnapshotOf`, `Stack`, and the
    `StackSnapshot`/`stackPatch` types;
  - on the bridge: `Registry.Stack`, `ErrStackUnsupported`,
    `Server.sessionStack` and `EventLog.Broadcast`.
- The five tests that already fail (W1.1 plan, Assumptions) still fail for
  reasons outside this work. Verify steps treat them as known.
- `CGO_ENABLED=1` for Go builds.

---

## Task 1: Step relations in `viewmodel`

**Goal:** `viewmodel.Relations` derives "likely fixed by" and "likely
caused by" links from a transcript, as in spec §4.5.

**Files:**
- `internal/viewmodel/relations.go` (new)
- `internal/viewmodel/relations_test.go` (new)

**Steps:**

1. Define:

   ```go
   type Relation struct{ CausedBy, FixedBy []NodeID }
   type RelationIndex map[NodeID]Relation
   ```

   Keys are tool row IDs (`ToolID(ev)`) and step IDs
   (`NodeID{KindStep, "step:<id>"}`).
2. Add `func Relations(items []session.TranscriptItem) RelationIndex`:
   - Walk the audits (`it.Audit != nil`) in timestamp order.
   - Group shell-family calls (`IsShellFamily`) by command
     (`ToolTarget(ev)`).
   - Classify each call as failed or passed with `EventFailed`.
   - An "edit" is any audit with `len(FilesChanged) > 0` or `isDiffTool`.
   - Rules (spec §4.5):
     - **fixed by:** for each failed call `F`, the steps of the edits
       strictly between `F` and the next passing call with the same
       command. Leave it empty if the command never passes again.
     - **caused by:** for each failed call `F` with an earlier passing
       call `P` of the same command, the steps of the edits strictly
       between `P` and `F`.
     - A step's relation is the union of its calls' relations,
       de-duplicated, keeping order of first appearance.
   - Step IDs come from `ev.StepID`. Skip edits with `StepID == 0`: their
     heuristic step IDs are not stable.
3. Write the tests first. Use the fixtures in `stack_test.go` (`audit`,
   `at`), plus a local helper that sets `CommandExitCode` and
   `FilesChanged`:
   - `TestRelationsFixedBy`: fail(go test) at t1 → edit step 8 at t2 →
     pass(go test) at t3. The failing row's FixedBy is `[step:8]`.
   - `TestRelationsCausedBy`: pass at t1 → edit step 5 at t2 → fail at t3.
     The failing row's CausedBy is `[step:5]`.
   - `TestRelationsNeverRepassed`: fail, then an edit, with no pass.
     FixedBy is empty.
   - `TestRelationsStepUnion`: a step containing two failing calls gets
     both calls' relations, de-duplicated.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/viewmodel/ -run TestRelations -v && go vet ./internal/viewmodel/
```

---

## Task 2: Step diffs and node lookup in `viewmodel`

**Goal:** pure helpers to find a node by key and to list per-step diffs
from a built tree.

**Files:**
- `internal/viewmodel/find.go`, `find_test.go` (new)

**Steps:**

1. `func Find(turns []*Node, key string) (n *Node, parent *Node)` does a
   depth-first search on `ID.Key` and returns the node and its parent. A
   turn's parent is nil.
2. Define:

   ```go
   type StepDiff struct {
       StepNode, TurnNode, TaskNode, Headline string
       At                                     time.Time
       Files                                  []string
       Diff                                   string
   }
   ```

3. Add `func StepDiffs(turns []*Node) []StepDiff`. It walks turn → (task) →
   step:
   - For each step node, collect child tool rows whose
     `Tools[i].ToolName` passes `isDiffTool` (`stack.go`).
   - `Diff` is the calls' `ResultContent` joined with `"\n"`.
   - `Files` is the union of their `FilesChanged`, in order.
   - `Headline` comes from `StepHeadline(n.Step, n.Children)`.
   - `At` is the first call's `Timestamp`.
   - `TaskNode` is set when the step sits under a task node.
   - Steps with no diff call are skipped. Order is tree order.
4. Tests:
   - `TestFindReturnsParent`
   - `TestStepDiffsGroupsByStep`: two edit calls in one step and one in
     another give two entries with joined diffs and the right
     `TaskNode`.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/viewmodel/ -run 'TestFind|TestStepDiffs' -v
```

---

## Task 3: ACP `session/stack_node`

**Goal:** the agent returns a node's wire form plus uncapped detail, as in
spec §4.1.

**Files:**
- `internal/acp/stacknode.go` (new), `internal/acp/stacknode_test.go` (new)
- `internal/acp/host.go`

**Steps:**

1. Define `StackNodeParams{SessionID "sessionId"; NodeID "nodeId"; SubagentID int64 "subagentId,omitempty"}`.
2. Define `NodeDetail`, with all JSON names in camelCase:
   - `Calls []CallDetail`;
   - `Narration []string`;
   - `Thinking []ThoughtDetail`;
   - `Todo *TodoDetail`;
   - `Relations *RelationDetail`.

   `CallDetail` holds:
   - `CallID`, `StepID int64`, `ToolName`;
   - `Args` and `OriginalArgs`, as indented JSON strings (use
     `json.Indent`, falling back to the raw text);
   - `Output`, `Diff` (set instead of `Output` when the tool is a diff
     tool), `Error`, `ExitCode *int`;
   - `Model`, `FinishReason`, `Rewritten bool`;
   - `Hooks []registry.HookMetadata`;
   - `Sandbox json.RawMessage`, which is `json.Marshal(ev.Sandbox)`;
   - `Symbols []SymbolDetail{File, Name, Kind}`;
   - `Notice *NoticeDetail{Kind, Text}`.

   This mirrors the fields `toolDetail` in
   `internal/app/tui/browse_inspect.go:91` shows.
3. Add `func (m *TurnManager) StackNode(ctx, params json.RawMessage) (any, error)`:
   - Decode with `decodeParams(params, &p, "session/stack_node")`
     (`session.go:306`).
   - Look up the session as `Stack` does. Resolve the source state: the
     parent `rt.State`, or the child state when `SubagentID != 0`. The
     child resolution is added in Task 4; for now, any non-zero value
     returns `invalidParamsError("subagent stacks not supported")`, and
     Task 4 replaces that.
   - Build the tree with
     `viewmodel.Build(stackSnapshotOf(state, m.HasActiveTurn(id), time.Now()))`,
     then call `viewmodel.Find`. A miss returns
     `invalidParamsError("unknown node: %s", p.NodeID)`.
   - Fill the detail by kind:
     - tool node: from `n.Tools` (finished calls) or `n.Active` (running:
       `Output` is `Active.Output`, uncapped);
     - step node: `n.Step.Narration` content, `n.Step.Thinking`, and the
       todo from `state.Todos()` matching `n.Step.TodoID`;
     - task node: the todo only.
   - Fill relations from
     `viewmodel.Relations(state.Transcript())[n.ID]`, mapping IDs to keys.
   - Return `{ "node": viewmodel.Project([]*viewmodel.Node{n}).Nodes[0], "detail": detail }`.
     `Project` treats the node as a root, so clear the returned `Parent`
     and use the `parent` from `Find` instead.
4. In `host.go`:
   - register `srv.Handle("session/stack_node", turns.StackNode)` after
     W1's `session/stack`;
   - add `"stackNode": map[string]any{}` to `sessionCapabilities`.
5. Tests, using the W1 test manager helper:
   - a tool node returns its full output when that output is longer than
     `viewmodel.WireTextCap`;
   - a step node returns its narration and thinking;
   - an unknown node gives an error containing "unknown node";
   - an unknown session gives an error containing "unknown session".

   Extend `TestRunInitializeCapabilities` to assert `stackNode`.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'TestStackNode|TestRunInitializeCapabilities' -v && go vet ./internal/acp/
```

---

## Task 4: Subagent stacks

**Goal:** `session/stack` and `session/stack_node` accept `subagentId`,
and child projectors emit patches tagged with it (spec §4.3).

**Files:**
- `internal/acp/stack.go`, `internal/acp/stacknode.go`
- `internal/acp/stack_test.go`
- `internal/acp/host.go`

**Steps:**

1. Add `SubagentID int64 "subagentId,omitempty"` to W1's `StackParams`,
   `StackSnapshot` and `stackPatch`.
2. Add `func (m *TurnManager) stackSource(rt *TurnRuntime, subagentID int64) (*session.State, bool, error)`:
   - With `subagentID == 0`, it returns `(rt.State, false, nil)`.
   - Otherwise it calls `rt.State.Subagent(subagentID)`
     (`session/subagents.go:313`).
     - Not found: `invalidParamsError("unknown subagent: %d", id)`.
     - `Child == nil`:
       `invalidParamsError("subagent has no separate transcript")`.
     - Otherwise it returns `(v.Child, true, nil)`, where the bool is
       `drilled`.
3. Give `stackSnapshotOf` a `drilled bool` parameter and set
   `Snapshot.Drilled`, updating W1's callers to pass `false`.
4. Key projectors with `stackKey(sessionID string, subagentID int64) string`:
   the session ID when 0, else `sessionID + "#" + strconv.FormatInt(id, 10)`.
   - `Stack` and `flushStack` use the key.
   - `flushStack` takes the subagent ID.
   - A child projector's patches set `SubagentID`. The notify still uses
     the parent `sessionId`.
5. Add `func (m *TurnManager) flushChildStacks(sessionID string, st *session.State)`.
   It flushes every active projector whose key starts with `sessionID+"#"`,
   using `stackSource` to find the child state, and deletes projectors
   whose subagent can no longer be found. Call it right after each parent
   flush:
   - in W1's ticker branch (`flushDirtyStack`);
   - in `finishTurn`;
   - in `Stack` when it flushes.
6. In `StackNode`, replace the Task 3 placeholder with `stackSource`.
7. In `host.go`, add `"subagentStacks": map[string]any{}`.
8. Tests:
   - Register a subagent with a child state
     (`state.RegisterSubagent("worker", child)`,
     `session/subagents.go:103`), add a message to the child, and call
     `Stack` with its ID. The snapshot holds the child's turn.
   - Add another child message, then flush. The patch has
     `subagentId == id`.
   - A subagent with a nil child gives an error containing "no separate
     transcript".

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'TestStack' -race -v && go vet ./internal/acp/
```

---

## Task 5: Idle stack flushing

**Goal:** once a session's stack is active, changes that happen while no
turn is running reach clients within about 500 ms (spec §4.4).

**Files:**
- `internal/acp/stack.go`, `internal/acp/stack_test.go`

**Steps:**

1. Add `stackIdleInterval = 500 * time.Millisecond`, and a field
   `idleCancel context.CancelFunc` on the parent `stackProjector`.
2. When `Stack` first activates a parent projector, call
   `m.startStackIdle(sessionID, rt)`. That function:
   - creates `ctx, cancel := context.WithCancel(context.Background())`
     and stores `cancel`;
   - subscribes with `rt.Events.Subscribe(ctx)`
     (`internal/pubsub/broker.go:237`);
   - runs a goroutine whose loop selects on:
     - an event: `m.markStackDirty(sessionID)`;
     - the ticker: if `!m.HasActiveTurn(sessionID)`, call
       `m.lookup(sessionID)`. If that fails, cancel and delete every
       projector for the session (the parent key and every
       `sessionID+"#…"` key), then return. Otherwise call
       `m.flushDirtyStack(sessionID, rt.State)` followed by
       `m.flushChildStacks(sessionID, rt.State)`;
     - `ctx.Done()`: return.
3. Tests:
   - `TestStackIdleFlushWithoutTurn`: activate, publish a session event
     the way the existing turn tests do (`publishToolEvents`,
     `turn_test.go:3280`), and mutate state. A `stack_patch` arrives
     within 2s.
   - `TestStackIdleStopsWhenSessionGone`: make the test `Lookup` start
     returning false. The projector entries are removed within 2s.
   - Run both under `-race`.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'TestStackIdle' -race -v
```

---

## Task 6: ACP `session/last_request`

**Goal:** the newest model request is available as JSON (spec §4.2).

**Files:**
- `internal/acp/lastrequest.go` (new), `lastrequest_test.go` (new)
- `internal/acp/host.go`

**Steps:**

1. Define JSON mirror types with camelCase tags for:
   - `session.RequestInspection` and its sub-types `InspectionMessage`,
     `InspectionToolCall`, `InspectionTool`, `InspectionOptions` and
     `InspectionOutcome`;
   - every field listed in `internal/app/session/request_inspection.go`.

   Write a converter `toRequestJSON(r session.RequestInspection) RequestJSON`.
   `InspectionStatus` encodes as its string value.
2. Add `func (m *TurnManager) LastRequest(ctx, params) (any, error)`. It
   decodes `sessionIDParams` (`turn.go:1424`) and looks up the session.
   - If `rt.State.RequestInspection()` reports `ok`, it returns
     `{"request": toRequestJSON(r)}`.
   - Otherwise it returns `{"request": null}`.
3. Register `session/last_request` and add `"lastRequest": map[string]any{}`.
4. Tests:
   - after `SetRequestInspection(...)` (`request_inspection.go:202`), the
     result carries the provider, model, messages and status;
   - with none set, `request` is null.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run TestLastRequest -v
```

---

## Task 7: ACP `session/files` and `session/file`

**Goal:** the agent lists and reads files under its active root, safely
(spec §4.6).

**Files:**
- `internal/acp/files.go` (new), `files_test.go` (new)
- `internal/acp/host.go`

**Steps:**

1. Define `FilesManager{lookup func(string) (*session.State, bool)}`.
   - Constructor: `NewFilesManager(lookup)`.
   - In `host.go`, construct it next to `exitMgr` (`host.go:401`), with a
     lookup through `manager.Get(sessionID)` that returns `rt.State`.
2. `Files(ctx, params)` takes `{sessionId, path}`:
   - The root is `state.Workspace().ActiveRoot` (`session/workspace.go:37`).
   - The target is `root` for an empty path, otherwise
     `native.SafeResolve(root, path)` (`internal/tools/native/helpers.go:68`).
     A resolve error returns `invalidParamsError("%v", err)`.
   - It runs `os.ReadDir` and drops `.git`.
   - It returns `{root, path, entries:[{name, dir, size}]}`, sorted
     directories first, then by name.
3. `File(ctx, params)` takes `{sessionId, path}`:
   - The path is resolved the same way, and must name a regular file.
   - It reads at most `maxFileView = 1 << 20` bytes plus one, to detect
     truncation.
   - `binary` is true when the first 8 KiB contain a NUL byte; then
     `content` is empty.
   - It returns `{path, size, binary, truncated, content}`.
4. Register `session/files` and `session/file`, and add
   `"filesView": map[string]any{}`.
5. Tests, against a temp dir as the active root (`newWorktreeTestState`,
   `worktree_test.go:28`, or set the workspace directly):
   - listing hides `.git` and sorts correctly;
   - `../x` is rejected;
   - an absolute path is rejected;
   - a symlink pointing outside the root is rejected;
   - a binary file is flagged;
   - a file of 1 MiB + 10 bytes is truncated.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'TestFiles|TestFile' -v
```

---

## Task 8: ACP `session/commit_draft` and `session/step_diffs`

**Goal:** a commit message can be drafted without committing, and the
per-step diffs are listed (spec §4.7–§4.8).

**Files:**
- `internal/acp/exit.go`, `internal/acp/exit_test.go`
- `internal/acp/stepdiffs.go` (new), `stepdiffs_test.go` (new)
- `internal/acp/host.go`

**Steps:**

1. In `exit.go`, add `func (m *ExitManager) CommitDraft(ctx, params) (any, error)`:
   - It decodes `{sessionId}` and looks the session up as `Commit` does
     (`exit.go:98`).
   - It calls `m.draftMessage(ctx, rt)` (the field set in
     `NewExitManager`, `exit.go:76`), wrapping errors as `Commit` does,
     and returns `{"message": msg}`.
   - It never touches git.
2. Test with `testExitManager` (`exit_test.go:28`): a fake drafter's text
   is returned, and the fake git saw no `CommitAll`.
3. `stepdiffs.go`: `func (m *TurnManager) StepDiffs(ctx, params) (any, error)`:
   - It decodes `sessionIDParams` and builds the tree as `StackNode` does.
   - It returns `{"steps": [...]}`, mapping each `viewmodel.StepDiff` to
     JSON fields `stepNode`, `turnNode`, `taskNode`, `headline`, `at`
     (Unix ms), `files` and `diff`.
4. Test: an audit of `file.write_patch` with a diff in `ResultContent`
   produces one entry carrying that diff.
5. Register `session/commit_draft` (on `exitMgr`) and `session/step_diffs`
   (on `turns`). Add `"commitDraft"` and `"stepDiffs"` to the capabilities.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'TestCommitDraft|TestStepDiffs|TestRunInitializeCapabilities' -v
CGO_ENABLED=1 go test ./internal/acp/ && go vet ./internal/acp/
```

---

## Task 9: Bridge — generic unsupported error and session proxy routes

**Goal:** the bridge proxies `stack?subagent`, `nodes/{nodeId}`,
`last-request` and `step-diffs`, with one shared "unsupported" mapping
(spec §5).

**Files:**
- `web/bridge/registry.go`, `web/bridge/http.go`
- `web/bridge/registry_test.go`, `web/bridge/http_test.go`

**Steps:**

1. Replace W1's `ErrStackUnsupported` with
   `type ErrUnsupported struct{ Feature string }`. Its `Error()` returns
   `Feature + "_unsupported"`.
   - Add `func isMethodNotFound(err error) bool`, which checks
     `errors.As(err, &*rpcError)` with `Code == -32601`.
   - In `writeErr`, add a case for `errors.As(err, &ErrUnsupported{})` →
     501 `{"error": e.Error()}`.
   - `Registry.Stack` now returns `ErrUnsupported{"stack"}`. Update W1's
     tests that asserted the old error value.
2. Add a private
   `func (r *Registry) call(ctx, id, method, feature string, params map[string]any) (json.RawMessage, error)`.
   It returns `ErrUnknownSession` for an unknown id, sets
   `params["sessionId"] = id`, maps method-not-found to `ErrUnsupported`,
   and otherwise returns the raw result. Reimplement `Stack` on it, adding
   a `subagentID int64` argument that is sent only when non-zero.
3. Add `StackNode(ctx, id, nodeID string, subagentID int64)`,
   `LastRequest(ctx, id)` and `StepDiffs(ctx, id)`, each one line on
   `call`. Their features are `stack_node`, `last_request` and
   `step_diffs`.
4. In `http.go`, next to W1's stack route, register:
   - `GET /api/sessions/{id}/nodes/{nodeId}` → `sessionNode`;
   - `GET /api/sessions/{id}/last-request` → `sessionLastRequest`;
   - `GET /api/sessions/{id}/step-diffs` → `sessionStepDiffs`.

   Extend `sessionStack` to parse an optional `?subagent=` with
   `strconv.ParseInt`; a bad value gives 400.

   Each handler copies `sessionStack`: `registryForSession`, then the
   call, then the raw JSON written with `Content-Type: application/json`.
   `r.PathValue("nodeId")` is already unescaped by `net/http`.
5. Tests:
   - for each route: 200 with a pass-through body, 404 for an unknown
     session, and 501 with `stack_node_unsupported` and so on, using the
     same fake child and server helper W1's `TestSessionStackRoute` uses;
   - a node ID containing `:` survives the round trip when sent as
     `tool%3A40%3Acall_1`.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestRegistry|TestSession' -v && go test ./ && go vet ./
```

---

## Task 10: Bridge — agent files and commit draft

**Goal:** `GET /api/agents/{id}/files`, `…/file` and `…/commit-draft` work
for fleet agents.

**Files:**
- `web/bridge/fleet.go`, `web/bridge/http.go`, `web/bridge/fleet_test.go`, `web/bridge/http_test.go`

**Steps:**

1. In `fleet.go`, add `Files(ctx, id, path)`, `File(ctx, id, path)` and
   `CommitDraft(ctx, id)`, modelled on `Fleet.Diff` (`fleet.go:931`):
   - `RuntimeForSession(id)`;
   - `params := map[string]any{"sessionId": f.sessionIDFor(rt), "path": path}`;
   - `rt.child.Request(ctx, "session/files" | "session/file" | "session/commit_draft", params)`;
   - method-not-found maps to `ErrUnsupported{"files"}` (`file`,
     `commit_draft`) through `isMethodNotFound`.
2. In `http.go`, next to `GET /api/agents/{id}/diff` (`http.go:109`),
   register the three routes:
   - `agentFiles` and `agentFile` read `?path=` from the query;
   - each writes the raw JSON, or the result of `writeErr`.
3. Tests: pass-through, 501 on method-not-found, 404 for an unknown agent.
   Use the fleet test helpers (`testFleet`, `fleet_test.go:13`) and the
   server builder that `agentDiff`'s tests use.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestAgentFiles|TestAgentFile|TestCommitDraft' -v && go test ./
```

---

## Task 11: Bridge — verify route and stored gate

**Goal:** `POST /api/agents/{id}/verify` runs the gate and stores the
result. `GET /api/agents/{id}/gate` returns the stored result. Each new
result is broadcast on the fleet stream.

**Files:**
- `web/bridge/fleet.go` (or a new `web/bridge/gate.go`), `web/bridge/http.go`, `web/bridge/fleetevents.go`
- tests

**Steps:**

1. Add a `gates` field to `Fleet`: a map from agent ID to
   `gateRecord{Result *gateResult "result"; At time.Time "at"}`, guarded
   by `f.mu` or its own mutex. It is in memory only; on restart the gate
   is "not run".
2. `func (f *Fleet) RunGate(ctx, id string) (gateRecord, error)`:
   - `RuntimeForSession`, then `f.verifySession(ctx, rt)` (`exit.go:151`);
   - store the record;
   - `f.fleetLog.Append(fleetStreamKey, fleetDelta{Kind: "gate", SessionID: id, Gate: &rec})`.

   Add an optional `Gate *gateRecord "gate,omitempty"` field to
   `fleetDelta` (`fleetevents.go:9`). `liveState.apply` ignores the
   `gate` kind.
3. Also store the gate result when `Exit` runs its verify step
   (`exit.go:54`, right after `verifySession`), so a ship attempt updates
   the live gate.
4. Routes:
   - `POST /api/agents/{id}/verify` → `agentVerify`, returning the
     record;
   - `GET /api/agents/{id}/gate` → `agentGate`, returning the record, or
     204 when none is stored.
5. Tests, using `testFleetWithGate` (`exit_test.go:149`):
   - verify stores the record and appends a `gate` delta that
     `FleetLog().Tail` shows;
   - GET returns it;
   - GET before any run returns 204.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestAgentVerify|TestAgentGate|TestExit' -v && go test ./
```

---

## Task 12: Bridge — review comments

**Goal:** review comments are stored per agent and sent to the agent as
steering or a prompt (spec §5.1).

**Files:**
- `web/bridge/workspace.go`, `web/bridge/review.go` (new), `web/bridge/http.go`, `web/bridge/audit.go`
- `web/bridge/review_test.go` (new), `web/bridge/workspace_test.go`

**Steps:**

1. In `workspace.go`:
   - set `workspaceVersion = 7`;
   - add `Reviews map[string][]ReviewComment "reviews,omitempty"` to
     `workspaceFile` (`workspace.go:152`), and a `reviews` field on
     `Workspace`, wired through `Load`/`save` like `pending`;
   - `migrateWorkspace` needs no data change; leave `Reviews` nil until
     first use.
   - Add the following, each saving like `PutAgent`:
     - `PutReviewComment(c ReviewComment) error`;
     - `ReviewComments(agentID string) []ReviewComment`, returning a copy
       ordered by `CreatedAt`;
     - `ResolveReviewComment(agentID, id string, at time.Time) error`.
   - Make `RemoveAgent` delete `reviews[agentID]`.
2. In `review.go`, define `ReviewComment` with JSON names `id`, `agentId`,
   `path`, `line`, `side`, `quote`, `body`, `createdAt`, `sentAt`,
   `resolvedAt,omitempty` and `ownerId`.
3. Add `func (f *Fleet) AddReviewComment(ctx, agentID string, in ReviewComment) (ReviewComment, error)`:
   - **Validate:** `path` and `body` are non-empty, `line > 0`, and
     `side` is `old` or `new`.
   - **Fill:** `id` from `newAgentID()` (`fleet.go:360`), `ownerId` as
     `DefaultOwnerID`, and `createdAt`.
   - **Build the message** in the spec format, keeping at most 6 quote
     lines.
   - **Send it:** look up the runtime. If
     `rt.reg.Sessions()[sid].Busy` (`registry.go:255`, `sessionInfo.Busy`)
     is true, use `rt.reg.Steer`. Otherwise use `rt.reg.Prompt` in a
     goroutine, as `spawnAgent` does (`http.go:287`).
   - **Record:** set `sentAt` and store the comment.
   - **Audit:** add `AuditReviewComment = "review_comment"` to the
     `audit.go` constants and log it with `f.auditf` (`fleet.go:287`):
     agent ID, and the path and line in `Detail`.
4. Routes:
   - `GET /api/agents/{id}/review/comments`;
   - `POST /api/agents/{id}/review/comments` (decode with `decodeJSON`);
   - `POST /api/agents/{id}/review/comments/{cid}/resolve`.
5. Tests:
   - create while idle calls Prompt, and create while busy calls Steer;
     use the fake child's recorded methods, as the existing
     prompt/steer tests do;
   - list order;
   - resolve;
   - removing the agent drops its comments;
   - a v6 `fleet.json` loads with version 7 and no reviews;
   - an audit entry is written (`hasEvent`, `audit_test.go:153`);
   - validation errors return 400.

**Verify:**

```bash
cd web/bridge && go test ./ -run 'TestReview|TestWorkspace' -v && go test ./
```

---

## Task 13: Bridge — recent prompts

**Goal:** `GET /api/prompts/recent?project=&limit=` lists distinct recent
prompts for a project.

**Files:**
- `web/bridge/http.go`, `web/bridge/fleet.go`, tests

**Steps:**

1. Add `func (f *Fleet) RecentPrompts(project string, limit int) []string`:
   - It iterates `f.ws.Agents()` with a matching `Project`, or every agent
     when `project == ""`.
   - It sorts by `CreatedAt` descending, trims each prompt, and skips
     empty ones and duplicates.
   - `limit` defaults to 20, with a maximum of 100.
2. Register the route `GET /api/prompts/recent` → `recentPrompts`. It
   returns `{"prompts": [...]}`.
3. Test with three agents, two sharing a prompt: the result has two
   entries, newest first.

**Verify:**

```bash
cd web/bridge && go test ./ -run TestRecentPrompts -v && go test ./ && go vet ./
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./...
go vet ./...
gofmt -l .
cd web/bridge && go test ./... -race
```

Expected results:
- `go test ./...` fails only on the five known tests;
- `gofmt -l .` prints nothing;
- the bridge passes under `-race`, including `TestWebIsStdlibOnly`.

## Integration notes

- **Capabilities:** the agent now advertises `stackNode`, `lastRequest`,
  `subagentStacks`, `filesView`, `commitDraft` and `stepDiffs`.
- **Older agents:** they produce 501 `<feature>_unsupported`, and the W2.2
  UI hides the matching features.
- **Workspace upgrade:** the version moves to 7. An older bridge refuses
  to load a v7 `fleet.json` only if it checks the version. The
  `Load`/`migrateWorkspace` path accepts older versions and upgrades them,
  so rolling forward is safe; rolling back loses review comments.
- **Gates:** stored gates are in memory only, and are lost on bridge
  restart by design.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. Each task has a focused test run. The `viewmodel` tasks come before the ACP tasks that use them, and the ACP tasks before the bridge. |
| Anchors verified? | Pre-W1 anchors were checked on `2ddc09e`: `decodeParams` (`session.go:306`), `invalidParamsError`/`serverErrorf` (`protocol.go:88/92`), `sessionIDParams` (`turn.go:1424`), `State.Subagent` (`subagents.go:313`), `RegisterSubagent` (`:103`), `RequestInspection`/`SetRequestInspection` (`request_inspection.go:202/219`), `Workspace().ActiveRoot`, `native.SafeResolve` (`helpers.go:68`), `ExitManager.draftMessage` (`exit.go:49/76/113`), `exitMgr` construction (`host.go:401`), `pubsub.Broker.Subscribe` (`broker.go:237`), `publishToolEvents` (`turn_test.go:3280`), `Fleet.Diff` (`fleet.go:931`), `verifySession` (`exit.go:151`), `fleetDelta`, `liveState`, `Registry.Sessions`/`sessionInfo.Busy`, `migrateWorkspace`, `newAgentID`, `auditf`, `hasEvent`, `testFleetWithGate`. W1.1 symbols are named as the W1.1 plan defines them. |
| Code compilable in isolation? | No verbatim code. Every step is prose with exact signatures, because each edit depends on W1 code that doesn't exist yet. |
| Verification per AGENTS.md? | Yes. |
| Placeholders? | One, deliberate: Task 3 returns an error for `subagentId`, and Task 4 replaces it. |
| Matches the spec? | §4 and §5, item for item. |
