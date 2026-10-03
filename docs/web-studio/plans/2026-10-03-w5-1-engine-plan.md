# W5.1 · Engine: hold, memory scopes, preview ports — implementation plan

**Spec:** [`docs/web-studio/specs/2026-10-03-w5-automations-and-ops-design.md`](../specs/2026-10-03-w5-automations-and-ops-design.md) §4
**Execution:** inline, task by task, with `marshal-executing-plans`.
**Track:** backend. See [`../README.md`](../README.md) for both tracks.
**Runs after:** [W4.3](2026-10-03-w4-3-bridge-secrets-egress-plan.md) (previous backend plan), with everything it depends on. Backend plans never depend on UI plans.
**Base:** a branch containing every plan listed under Runs after. Anchors into code from before
W1 were checked on `2ddc09e`. `internal/workspacecfg` is as W4.1 defines
it.
**Plan slug:** `w5-1-engine`. Commit each task as `w5-1-engine: task N — <title>`.

## Goal

The engine supports:

- holding a session between steps;
- memory scopes (project, workspace, global), with provenance and
  promotion;
- workspace-aware memory retrieval;
- the `[preview]` workspace section.

## Non-goals

- Bridge and UI (W5.2–W5.6).

## Assumptions

- From W3.1 and W3.2: in container mode, all agents share one data
  directory (`MARSHAL_DATA_DIR`), so one SQLite DB holds every project's
  memories.
- The bridge sets `MARSHAL_WORKSPACE=<name>` in agents spawned with a
  workspace. W5.2 Task 1 adds that env. This plan only reads it.

---

## Task 1: Session hold

**Goal:** `session/hold` pauses a running agent before its next tool or
model call (spec §4.1).

**Files:**
- `internal/app/session/hold.go` (new), `hold_test.go` (new)
- `internal/agent/runner.go` (loop-top steering drain `:1063`)
- `internal/agent/execute.go` (`executeToolCall` `:182`)
- `internal/acp/hold.go` (new), `hold_test.go` (new), `internal/acp/host.go`

**Steps:**

1. On `session.State`, add `hold bool` and `holdCh chan struct{}`, both
   guarded by `s.mu`. `holdCh` is closed and replaced on release. Add:
   - `SetHold(on bool)`, which publishes a session event of a new type
     `EventHoldChanged` through the existing broker (follow how
     `EventPendingApprovalChanged` is published);
   - `Held() bool`;
   - `WaitUnheld(ctx context.Context) error`.
2. In the runner, call `if err := r.State.WaitUnheld(ctx); err != nil { return …ctx error path… }`:
   - immediately before the `for _, msg := range r.State.DrainSteering()`
     loop at `runner.go:1063`;
   - as the first statement of `executeToolCall` (`execute.go:182`).

   Use the same ctx-cancel return the surrounding code uses.
3. ACP: add `TurnManager.Hold(ctx, params)` with params `{sessionId, on}`.
   It calls `rt.State.SetHold(on)` and returns `{held}`.
   - In `eventToSessionUpdate` (`turn.go:322`), map `EventHoldChanged` to
     `{kind:"hold", held}`.
   - Register `session/hold` and add the capability `holdControl`.
4. Tests:
   - **session:** `WaitUnheld` blocks while held, returns on release, and
     returns on cancel.
   - **agent:** with a fake provider issuing two tool calls, holding
     after the first call starts means the second waits until release.
     Use the agent test stubs in `internal/agent/agenttest`.
   - **acp:** hold and release emit `hold` updates.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/app/session/ ./internal/agent/ ./internal/acp/ -run 'Hold' -race -v
```

---

## Task 2: Memory scope migration

**Goal:** `memories` gains scope, owner and provenance columns
(spec §4.2).

**Files:**
- `internal/db/migrations.go` (`migrations` list `:305`, `init` `:307`)
- `internal/db/memories.go` (`Memory` `:33`, `SaveMemory` `:49`, `GetMemories` `:119`)
- `internal/db/memories_test.go`

**Steps:**

1. Append `migrateMemoryScopes` to `migrations` (it becomes version 3).
   - It adds the columns from spec §4.2, with their defaults, via
     `ALTER TABLE memories ADD COLUMN …`.
   - It creates `idx_memories_scope ON memories(scope, scope_key)`.
   - Follow `migrateMemoryContentHash` (`:315`) for style.
2. Extend `Memory` with `Scope`, `ScopeKey`, `OwnerID`, `LearnedAgent`,
   `LearnedStep` and `ConfirmedBy []string` (decoded from JSON), and read
   them in every query.
3. Add `SaveMemoryWith(projectID int64, in MemoryInput) error`, where
   `MemoryInput{Kind, Content, SourceSessionID, LearnedAgent string; LearnedStep int64; Now time.Time}`.
   `SaveMemory` becomes a wrapper around it, so its callers don't change.
   Scope defaults to `project`.
4. Add these methods:
   - `GetScopedMemories(projectID int64, workspace string) ([]Memory, error)`,
     which returns project rows, then workspace rows (when `workspace`
     isn't empty), then global rows, each group ordered as `GetMemories`
     does;
   - `MemorySuggestions(projectID int64) ([]Suggestion, error)`, where
     `Suggestion{MemoryID int64; MatchProjectID int64; MatchRoot string; SuggestedScope string}`.
     It joins on `content_hash` across projects for project-scoped rows,
     and suggests `global`;
   - `PromoteMemory(id int64, scope, scopeKey string, now time.Time) error`,
     which runs in one transaction:
     1. set the scope;
     2. find same-hash rows in other projects;
     3. append their `learned_agent` to `confirmed_by`;
     4. delete them;
   - `ConfirmMemory(id int64, agent string) error`.
5. Tests:
   - the migration on a DB holding pre-migration rows, which get the
     defaults;
   - save with provenance;
   - scoped retrieval order;
   - suggestions across two projects;
   - promote merges and deletes duplicates;
   - confirm de-duplicates agents.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/db/ -run 'Memory' -v && CGO_ENABLED=1 go test ./internal/db/
```

---

## Task 3: Retrieval and extraction provenance

**Goal:** the prompt context includes workspace and global memories, and
extraction records who learned each memory.

**Files:**
- `internal/app/app.go` (`dbMemoryProvider.Memories` `:391`)
- `internal/knowledge/knowledge.go` (`ExtractInput` `:46`, `SaveMemory` call `:98`)
- The knowledge call sites that build `ExtractInput` (`grep -rn "knowledge.ExtractInput{" internal`)
- tests in `internal/app` and `internal/knowledge`

**Steps:**

1. Retrieval. `dbMemoryProvider.Memories(projectID)` calls
   `GetScopedMemories(projectID, os.Getenv("MARSHAL_WORKSPACE"))`. Read
   the env var once, when the provider is constructed. Stale memories are
   still skipped.
2. Provenance.
   - Add `AgentLabel string` and `StepID int64` to `ExtractInput`.
   - Call sites set `AgentLabel` from the session's actor. Use the
     orchestrator's label, or `"marshal"` when it's empty, and use the
     last step ID from `State.Steps()`.
   - At `:98`, call `SaveMemoryWith` with `LearnedAgent` and
     `LearnedStep`.
3. Tests:
   - with `MARSHAL_WORKSPACE=ws1`, the provider returns project, then
     workspace, then global notes;
   - extraction saves `learned_agent`.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/knowledge/ ./internal/app/ -run 'Memory|Extract' -v
```

---

## Task 4: ACP memory methods — scopes, suggestions, promote, confirm

**Goal:** `memory_list` exposes scopes and provenance. The new methods are
`memory_suggestions`, `memory_promote` and `memory_confirm` (spec §4.2).

**Files:**
- `internal/acp/memory.go` (`MemoryEntry` `:45`, `MemoryList` `:61`), `memory_test.go` (`newMemoryTestDB` `:15`)
- `internal/acp/host.go` (`:364-367`)

**Steps:**

1. Extend `MemoryEntry` with:
   - `scope`, `scopeKey`, `ownerId`;
   - `learnedProjectRoot`, which needs a project-root lookup (add
     `db.ProjectRoot(id)` if none exists; `GetProjectByRoot` is the
     reverse, `projects.go:47`);
   - `learnedAgent`, `learnedStep`, `confirmedBy`.

   `MemoryList` accepts an optional `scope`. With no scope it returns
   `GetScopedMemories(projectID, os.Getenv("MARSHAL_WORKSPACE"))`.
2. New handlers:
   - `MemorySuggestions({sessionId})`;
   - `MemoryPromote({sessionId, id, scope, scopeKey?})`, which validates
     the scope (`project`, `workspace` or `global`) and requires
     `scopeKey` for `workspace`;
   - `MemoryConfirm({sessionId, id, agent})`.

   Register them next to the existing `memory_*` methods. Add the
   capability `memoryScopes`.
3. Tests, with `newMemoryTestDB`:
   - list shows a global memory saved from another project;
   - suggestions;
   - promote;
   - confirm;
   - an invalid scope is rejected.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/acp/ -run 'Memory' -v
```

---

## Task 5: `[preview]` workspace section

**Goal:** workspace files can declare preview ports (spec §4.3).

**Files:**
- `internal/workspacecfg/doc.go`, `parse.go`, `render.go`, tests

**Steps:**

1. Add `Preview{Ports []int}` (TOML key `preview`, JSON `preview`) to
   `Doc`.
2. Validation: each port is between 1 and 65535 and unique. Violations are
   error diagnostics on the `[preview]` header line.
3. Map the section to layer 0, the side panel (layer map from W4.1
   Task 1). Render it in canonical order after `[policy]`.
4. Tests: parse, invalid and duplicate ports, and that `Patch` for layer 0
   with a preview value inserts the section.

**Verify:**

```bash
CGO_ENABLED=1 go test ./internal/workspacecfg/ -v
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./... ; go vet ./... ; gofmt -l .
```

Expected: only the five known failures, and no `gofmt` output.

## Integration notes

- **DB migration:** it runs on first open. Every agent sharing the data
  directory runs the same migration. SQLite serializes it, and
  `schema_migrations` makes it idempotent.
- **TUI:** it sees global and workspace memories once they exist. Its
  memory panel lists them with no scope column until a later TUI change;
  that is acceptable because the rows are still valid memories.

## Self-review

| Check | Result |
|---|---|
| Self-contained, verifiable tasks? | Yes. |
| Anchors verified? | Checked on `2ddc09e`: `runner.go:1063` (the `DrainSteering` loop), `execute.go:182` (`executeToolCall`), `migrations`/`init`/`migrateMemoryContentHash` (`migrations.go:305/307/315`), `Memory`/`SaveMemory`/`GetMemories` (`memories.go:33/49/119`), `GetProjectByRoot` (`projects.go:47`), `ExtractInput` and the `SaveMemory` call (`knowledge.go:46/98`), `dbMemoryProvider.Memories` (`app.go:391`), `MemoryEntry`/`MemoryList` (`memory.go:45/61`), `newMemoryTestDB` (`memory_test.go:15`), `eventToSessionUpdate` (`turn.go:322`), the `host.go` memory registrations (`:364-367`). `workspacecfg` is from W4.1. |
| Code compilable in isolation? | No verbatim code. |
| Placeholders? | None. |
| Matches the spec? | §4. |
