# Filetracker shared stale epoch — implementation plan

**Goal:** A write by one agent invalidates every other agent's view of that path; the newest writer may continue from the bytes it just produced; any read of the path clears the shared epoch for all agents. The stale error names the writer, its time, and the exact current region.

**Base:** `87c0ad10` (current main; includes `1aebaa68` and the `p4-5`/`p4-6` merges). The files this plan touches are unchanged between `1aebaa68` and `87c0ad10`. The approved design is [feature-spec.md](../feature-spec.md).

**Execution:** Tasks are sequential. Do not start implementation until this plan is approved. After each task, run its verification and commit with `filetrack-stale-epoch: task N — <title>`. Preserve unrelated workspace changes; the pre-existing untracked files under `docs/` and `scripts/lmstudio-bench/` are not part of this work.

**Non-goals:**

- Per-path locking or any change to the existing `swarm.WriteLock` gate.
- Content hashing or byte-level fingerprints.
- Strict per-agent read rows; the epoch is shared.
- A numeric subagent card number in the writer label.
- Changes to `todo.write` or `skill.load`.
- Automatic repair of external changes beyond showing the current region and writer.

**Assumptions:**

- SQLite serializes the revision upsert; no extra service-level lock is needed.
- Revision 0 is the “no epoch” value for pre-existing rows.
- One plan author is active per conversation, so `plan-author` is unique enough. Pipeline workers are concurrent and get unique IDs.
- `WorkID` is available before an `agent.run` child toolset is registered.
- The modification time remains the external-change detector.

## Contracts

Shared by Tasks 1–3. These declarations are the final shape; do not invent alternatives.

### File ledger types and methods

```go
package filetrack

type Actor struct {
    ID    string
    Label string
}

type ReadState struct {
    Revision int64
    At       time.Time
    OK       bool
}

type WriteState struct {
    Revision int64
    At       time.Time
    Actor    Actor
    OK       bool
}

func (s *Service) RecordRead(path string, at time.Time, revision int64) error
func (s *Service) ReadState(path string) (ReadState, error)

func (s *Service) RecordWrite(path string, at time.Time, actor Actor) error
func (s *Service) WriteState(path string) (WriteState, error)
```

Missing rows return `OK == false` and a zero time. A malformed stored timestamp returns an error with `OK == false`.

### Native seam

```go
type FileTracker interface {
    RecordRead(path string, at time.Time, revision int64) error
    ReadState(path string) (filetrack.ReadState, error)
    RecordWrite(path string, at time.Time, actor filetrack.Actor) error
    WriteState(path string) (filetrack.WriteState, error)
}

type Options struct {
    // ... existing fields ...
    AgentID    string
    AgentLabel string
}

func (t *toolSet) actor() filetrack.Actor
```

`actor()` returns `ID: AgentID` or, when that is empty, `ID: AgentLabel`; `Label` is always `AgentLabel`.

### Guard helpers

```go
type readStatus int

const (
    readAllowed readStatus = iota
    readNever
    readStale
)

// checkFileRead evaluates the shared epoch and writer exemption.
func (t *toolSet) checkFileRead(path string, info os.FileInfo) (filetrack.WriteState, readStatus, error)

// changedOnDiskError builds the stale error at the observed write revision.
func (t *toolSet) changedOnDiskError(path string, fp patch.FilePatch, write filetrack.WriteState, mod time.Time, refreshRead bool) error

// writerNote renders " (last written by <label-or-id> at <timestamp>...)".
func writerNote(write filetrack.WriteState, mod time.Time) string
```

### Subagent actor request

```go
type SubagentRequest struct {
    // ... existing fields ...
    ActorID    string
    ActorLabel string
}
```

The handler passes the child's `WorkID` as `ActorID` and `a subagent (<description>)` as `ActorLabel`.

### Pipeline actor IDs

```go
// in internal/app
func newWorkerActorID(prefix string) string
```

It uses an atomic counter; IDs only need to be unique within the process/conversation because the ledger is session-scoped.

## Task 1: Persist revisions and actor identities

**Goal:** The database, `filetrack.Service`, and native seam carry a per-path revision and actor identity; the tree compiles and existing guard behavior is preserved until Task 2 changes the semantics.

**Files:** `internal/db/migrations.go`, `internal/db/db.go`, `internal/db/db_test.go`, `internal/filetrack/service.go`, `internal/filetrack/service_test.go`, `internal/tools/native/native.go`, `internal/tools/native/file.go`, `internal/tools/native/file_test.go`.

### Steps

1. **Base schema.** In `internal/db/migrations.go`, change `file_reads` and `file_writes` to:

```sql
CREATE TABLE IF NOT EXISTS file_reads (
    session_id TEXT NOT NULL,
    path TEXT NOT NULL,
    read_at TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(session_id, path)
);

CREATE TABLE IF NOT EXISTS file_writes (
    session_id TEXT NOT NULL,
    path TEXT NOT NULL,
    written_at TEXT NOT NULL,
    written_by TEXT NOT NULL DEFAULT '',
    written_id TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(session_id, path)
);
```

2. **Existing databases.** In `internal/db/db.go`, add these to `migrationColumns`, immediately after the existing `file_writes.written_by` entry:

```go
{"file_reads", "revision", "INTEGER NOT NULL DEFAULT 0"},
{"file_writes", "written_id", "TEXT NOT NULL DEFAULT ''"},
{"file_writes", "revision", "INTEGER NOT NULL DEFAULT 0"},
```

Add `"file_reads": true` to `allowedTableInfo`.

3. **Replace `internal/filetrack/service.go`** with:

```go
package filetrack

import (
    "database/sql"
    "fmt"
    "time"
)

type Actor struct {
    ID    string
    Label string
}

type ReadState struct {
    Revision int64
    At       time.Time
    OK       bool
}

type WriteState struct {
    Revision int64
    At       time.Time
    Actor    Actor
    OK       bool
}

type Service struct {
    db        *sql.DB
    sessionID string
}

func New(db *sql.DB, sessionID string) *Service {
    return &Service{db: db, sessionID: sessionID}
}

func (s *Service) RecordRead(path string, at time.Time, revision int64) error {
    _, err := s.db.Exec(
        `INSERT INTO file_reads (session_id, path, read_at, revision) VALUES (?, ?, ?, ?)
         ON CONFLICT(session_id, path) DO UPDATE SET
            revision = MAX(file_reads.revision, excluded.revision),
            read_at = CASE
                WHEN excluded.revision >= file_reads.revision
                    THEN excluded.read_at
                ELSE file_reads.read_at
            END`,
        s.sessionID, path, at.UTC().Format(time.RFC3339Nano), revision)
    if err != nil {
        return fmt.Errorf("record read: %w", err)
    }
    return nil
}

func (s *Service) ReadState(path string) (ReadState, error) {
    var readAt string
    var revision int64
    err := s.db.QueryRow(
        `SELECT read_at, revision FROM file_reads WHERE session_id = ? AND path = ?`,
        s.sessionID, path).Scan(&readAt, &revision)
    if err == sql.ErrNoRows {
        return ReadState{}, nil
    }
    if err != nil {
        return ReadState{}, fmt.Errorf("query read state: %w", err)
    }
    t, err := time.Parse(time.RFC3339Nano, readAt)
    if err != nil {
        return ReadState{}, fmt.Errorf("parse last read time %q: %w", readAt, err)
    }
    return ReadState{Revision: revision, At: t.UTC(), OK: true}, nil
}

func (s *Service) RecordWrite(path string, at time.Time, actor Actor) error {
    _, err := s.db.Exec(
        `INSERT INTO file_writes (session_id, path, written_at, written_by, written_id, revision)
         VALUES (?, ?, ?, ?, ?, 1)
         ON CONFLICT(session_id, path) DO UPDATE SET
            written_at=excluded.written_at,
            written_by=excluded.written_by,
            written_id=excluded.written_id,
            revision=file_writes.revision + 1`,
        s.sessionID, path, at.UTC().Format(time.RFC3339Nano), actor.Label, actor.ID)
    if err != nil {
        return fmt.Errorf("record write: %w", err)
    }
    return nil
}

func (s *Service) WriteState(path string) (WriteState, error) {
    var writtenAt, writtenBy, writtenID string
    var revision int64
    err := s.db.QueryRow(
        `SELECT written_at, written_by, written_id, revision FROM file_writes WHERE session_id = ? AND path = ?`,
        s.sessionID, path).Scan(&writtenAt, &writtenBy, &writtenID, &revision)
    if err == sql.ErrNoRows {
        return WriteState{}, nil
    }
    if err != nil {
        return WriteState{}, fmt.Errorf("query write state: %w", err)
    }
    t, err := time.Parse(time.RFC3339Nano, writtenAt)
    if err != nil {
        return WriteState{}, fmt.Errorf("parse last write time %q: %w", writtenAt, err)
    }
    return WriteState{
        Revision: revision,
        At:       t.UTC(),
        Actor:    Actor{ID: writtenID, Label: writtenBy},
        OK:       true,
    }, nil
}
```

4. **Service tests.** In `internal/filetrack/service_test.go`:

- Add `revision INTEGER NOT NULL DEFAULT 0` to the `file_reads` fixture and `written_id TEXT NOT NULL DEFAULT ''` plus `revision INTEGER NOT NULL DEFAULT 0` to `file_writes`.
- Rename/replace the old read/write accessor tests:
  - `TestRecordReadAndLastReadTime` → `TestRecordReadAndReadState`: `RecordRead(..., 0)`, assert `ReadState` returns revision 0 and the stored time.
  - `TestLastReadTimeNotFound` → `TestReadStateNotFound`.
  - `TestLastReadTimeRejectsUnparseableTimestamp` → `TestReadStateRejectsUnparseableTimestamp`.
- Replace `TestLastWriterNamesTheAgent` with `TestWriteStateNamesTheAgent`: write with `Actor{ID: "main", Label: "the main agent"}`, assert `WriteState` returns the actor, time, and revision 1; upsert with `Actor{ID: "subagent-1", Label: "a subagent"}`, assert revision 2 and the replacement actor; assert an unwritten path has `OK == false`.
- Replace `TestRecordWriteUpsert` with `TestRecordWriteAdvancesRevision`: two writes produce revisions 1 and 2.
- Add `TestRecordWriteDoesNotCreateRead`: after `RecordWrite`, `ReadState` is `OK == false`.
- Add `TestRecordReadStoresObservedRevision`: write revision 1, `RecordRead(..., 1)`, assert `ReadState.Revision == 1`; then `RecordRead(..., 0)` and assert it stays 1 while `At` does not regress.
- Update `TestSessionIsolation` and every other `RecordRead` call with the new third argument.
- Add `TestMigrateAddsFileLedgerRevisions` to `internal/db/db_test.go`: create legacy `file_reads`/`file_writes` tables without the new columns, call `Migrate`, and assert `tableColumns("file_reads")` contains `revision` and `tableColumns("file_writes")` contains `written_id` and `revision`. A fresh `Open(":memory:")` + `Migrate` must also have them.

5. **Native seam.** In `internal/tools/native/native.go`:

- Add `marshal/internal/filetrack` to the imports.
- Replace the `FileTracker` interface with the declaration in Contracts.
- Add `AgentID string` to `Options` after `AgentLabel`, with a comment that it is the stable writer identity and falls back to the label.
- Add `agentID string` to `toolSet` after `agentLabel`, with the same comment.
- In `newToolSet`, set `agentID: opts.AgentID` after `agentLabel: opts.AgentLabel`.
- Add:

```go
func (t *toolSet) actor() filetrack.Actor {
    id := t.agentID
    if id == "" {
        id = t.agentLabel
    }
    return filetrack.Actor{ID: id, Label: t.agentLabel}
}
```

6. **Native read recording.** In `internal/tools/native/file.go`, in `readWorkspaceFile`, before `f, err := os.Open(path)`, observe the current write revision:

```go
var observedRevision int64
if t.fileTracker != nil {
    write, err := t.fileTracker.WriteState(path)
    if err != nil {
        slog.Warn("observe write revision", "path", path, "error", err)
    } else if write.OK {
        observedRevision = write.Revision
    }
}
```

Replace the final `_ = t.fileTracker.RecordRead(path, time.Now())` with `_ = t.fileTracker.RecordRead(path, time.Now(), observedRevision)`.

7. **Temporary shared-read preservation.** In `recordWrite`, use the new actor API, then deliberately refresh the shared read until Task 2 removes it:

```go
func (t *toolSet) recordWrite(path string) {
    if t.fileTracker == nil {
        return
    }
    now := time.Now()
    _ = t.fileTracker.RecordWrite(path, now, t.actor())
    // Temporary: Task 2 removes this refresh and replaces it with the writer
    // exemption plus a shared stale epoch.
    if write, err := t.fileTracker.WriteState(path); err == nil && write.OK {
        _ = t.fileTracker.RecordRead(path, now, write.Revision)
    }
}
```

8. **Native accessors and guards.** In `internal/tools/native/file.go`:

- `writerNote`: replace `writer, _, ok, err := t.fileTracker.LastWriter(path)` with `write, err := t.fileTracker.WriteState(path)`; return `""` when `!write.OK`; use `write.Actor.Label`, falling back to `write.Actor.ID`, and keep the current `" (last written by %s)"` text.
- `changedOnDiskError`: the refresh branch currently calls `t.fileTracker.RecordRead(path, time.Now())`. Query `WriteState(path)`, use its `Revision` (or 0 when absent), and call `RecordRead(path, time.Now(), revision)`.
- In `applyFilePatches`'s dry-run block, replace `lastRead, hasRead, err := t.fileTracker.LastReadTime(path)` with `read, err := t.fileTracker.ReadState(path)`, use `read.At` for the mtime check and `read.OK` for the never-read check.
- In `applyFilePatches`'s TOCTOU re-check, replace the `LastReadTime` call with `ReadState` and use `read.OK` / `read.At`.
- In `fileWriteTool`, do the same for the initial guard and the TOCTOU re-check.

9. **Native tests.** Update `interruptingTracker.RecordWrite` to `func (t *interruptingTracker) RecordWrite(path string, at time.Time, actor filetrack.Actor) error` and forward `actor`. Update every direct `RecordRead` call with the new revision argument and every `RecordWrite` call with `filetrack.Actor{...}`. In `TestStaleEditErrorNamesWriterAndLineRange`, use `RecordRead(filePath, time.Now().Add(-time.Hour), 0)` and `RecordWrite(filePath, time.Now(), filetrack.Actor{ID: "subagent", Label: "a subagent"})`.

**Verify:**

```bash
CGO_ENABLED=1 gofmt -w internal/db internal/filetrack internal/tools/native
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./internal/filetrack/... ./internal/db/... ./internal/tools/native/...
```

Expected: the tree builds, service revision tests pass, the existing stale-edit test still passes with its current assertions, and native tests are green. This task intentionally preserves the old shared-refresh behavior; Task 2 changes it.

**Commit:** `filetrack-stale-epoch: task 1 — persist revisions and actor identities`

## Task 2: Enforce the shared stale epoch and writer exemption

**Goal:** A sibling's write makes the path stale for every other agent, the newest writer can edit its own write, external changes still make the writer stale, and the stale error carries the exact writer/time/region contract.

**Files:** `internal/tools/native/file.go`, `internal/tools/native/file_test.go`.

### Steps

1. **Add the guard helper.** In `internal/tools/native/file.go`, add the `marshal/internal/filetrack` import if Task 1 did not need it, then add:

```go
type readStatus int

const (
    readAllowed readStatus = iota
    readNever
    readStale
)

func (t *toolSet) checkFileRead(path string, info os.FileInfo) (filetrack.WriteState, readStatus, error) {
    if t.fileTracker == nil {
        return filetrack.WriteState{}, readAllowed, nil
    }
    read, err := t.fileTracker.ReadState(path)
    if err != nil {
        return filetrack.WriteState{}, readAllowed, err
    }
    write, err := t.fileTracker.WriteState(path)
    if err != nil {
        return filetrack.WriteState{}, readAllowed, err
    }
    actorID := t.actor().ID
    if write.OK && actorID != "" && write.Actor.ID == actorID {
        if info.ModTime().After(write.At) {
            return write, readStale, nil
        }
        return write, readAllowed, nil
    }
    if !read.OK {
        return write, readNever, nil
    }
    if write.OK && write.Revision > read.Revision {
        return write, readStale, nil
    }
    if info.ModTime().After(read.At) {
        return write, readStale, nil
    }
    return write, readAllowed, nil
}
```

2. **Replace the dry-run guard.** In `applyFilePatches`, replace the post-Task-1 guard block that starts with `read, err := t.fileTracker.ReadState(path)` with:

```go
write, status, err := t.checkFileRead(path, info)
if err != nil {
    return registry.ToolResult{}, fmt.Errorf(
        "cannot verify read state for %s: %w; re-read it before editing", fp.Path, err)
}
switch status {
case readNever:
    return registry.ToolResult{}, fmt.Errorf(
        "file %s was never read this session; read it before editing", fp.Path)
case readStale:
    return registry.ToolResult{}, t.changedOnDiskError(path, fp, write, info.ModTime(), true)
}
```

3. **Replace the patch TOCTOU re-check.** In the per-file commit unit, after `writeStat, statErr := os.Stat(path)` succeeds, call `t.checkFileRead(path, writeStat)`. On a check error return the same “cannot verify read state” error; on `readNever` return the never-read error; on `readStale` return `t.changedOnDiskError(path, fp, write, writeStat.ModTime(), true)`. If `statErr != nil`, keep the existing skip behavior.

4. **Replace the `file.write` guards.** In `fileWriteTool`, use `t.checkFileRead(path, info)` for the initial existing-file guard and `t.checkFileRead(path, writeStat)` for the TOCTOU re-check. Both pass `refreshRead=false` to `changedOnDiskError`; use `info.ModTime()` and `writeStat.ModTime()` respectively. Keep the existing never-read message.

5. **Remove the temporary refresh.** In `recordWrite`, delete the `WriteState` + `RecordRead` block. The final body is:

```go
func (t *toolSet) recordWrite(path string) {
    if t.fileTracker == nil {
        return
    }
    _ = t.fileTracker.RecordWrite(path, time.Now(), t.actor())
}
```

6. **Exact stale error.** Replace the current `changedOnDiskError` and `writerNote` with:

```go
func writerNote(write filetrack.WriteState, mod time.Time) string {
    if !write.OK {
        return ""
    }
    label := write.Actor.Label
    if label == "" {
        label = write.Actor.ID
    }
    if label == "" {
        return ""
    }
    ts := write.At.UTC().Format("2006-01-02 15:04:05Z")
    if mod.After(write.At) {
        return fmt.Sprintf(" (last written by %s at %s; the file changed again after that write)", label, ts)
    }
    return fmt.Sprintf(" (last written by %s at %s)", label, ts)
}

func (t *toolSet) changedOnDiskError(path string, fp patch.FilePatch, write filetrack.WriteState, mod time.Time, refreshRead bool) error {
    var region string
    var start, end int
    if len(fp.Chunks) > 0 {
        data, err := os.ReadFile(path)
        if err == nil {
            search, _, failed := patch.FindFailingChunk(string(data), fp)
            if !failed {
                search = fp.Chunks[0].Search
            }
            region, start, end = patch.NearestRegionLines(
                string(data), search, patch.NearestRegionWindowSize(search))
        }
    }

    refresh := false
    if region != "" && refreshRead && t.fileTracker != nil {
        if err := t.fileTracker.RecordRead(path, time.Now(), write.Revision); err == nil {
            refresh = true
        }
    }

    suffix := "; re-read it before editing"
    if refresh {
        suffix = "; the current region is shown below"
    }
    base := fmt.Errorf("file %s changed on disk since last read%s%s",
        fp.Path, writerNote(write, mod), suffix)
    if region == "" {
        return base
    }
    header := fmt.Sprintf("%s lines %d-%d", currentContentHeader, start, end)
    if !refresh {
        return fmt.Errorf("%s\n\n%s\n%s", base, header, region)
    }
    return fmt.Errorf("%s\n\n%s\n%s\n\n%s", base, header, region,
        "Retry with an old_string taken from those lines; the read mark was refreshed with that region, so the edit is accepted without a separate file.read.")
}
```

7. **Tests.** Add `testFileLedger` and `testActorRegistry` helpers to `internal/tools/native/file_test.go`:

```go
func testFileLedger(t *testing.T) *filetrack.Service {
    t.Helper()
    database, err := db.Open(filepath.Join(t.TempDir(), "filetrack.db"))
    if err != nil {
        t.Fatalf("db.Open: %v", err)
    }
    t.Cleanup(func() { database.Close() })
    if err := database.Migrate(); err != nil {
        t.Fatalf("Migrate: %v", err)
    }
    return filetrack.New(database.SQLDB(), "test-session")
}

func testActorRegistry(t *testing.T, root string, tracker FileTracker, agentID, label string) *registry.Registry {
    t.Helper()
    reg := registry.New()
    if err := RegisterAll(reg, Options{
        WorkspaceRoot: root,
        CommandRunner: &fakeRunner{},
        FileTracker:   tracker,
        AgentID:       agentID,
        AgentLabel:    label,
    }); err != nil {
        t.Fatalf("RegisterAll: %v", err)
    }
    return reg
}
```

Add these tests, each asserting the named behavior. Every `file.write` of an existing file in these tests follows a `file.read` by that actor; that is the production guard.

- `TestSharedStaleEpochBlocksSibling`: parent and child toolsets share a ledger. Parent reads `app.txt`; child reads and writes the same content (bumping revision); parent's `file.edit` fails with `last written by a subagent (review)` and the line range. Assert it does **not** say `was never read`.
- `TestSharedReadClearsSiblingEpoch`: parent and third read the initial file; child writes the same content; parent reads again; third's `file.edit` succeeds without its own read.
- `TestEmbeddedRegionClearsSiblingEpoch`: parent reads, third reads, child writes the same content; parent's edit fails and refreshes the shared read; third's edit succeeds without a read.
- `TestNewerWriteAfterEmbeddedRegionLeavesStale`: after the parent's stale-error refresh, the child writes the same content again; third's edit is stale.
- `TestWriterExemptionAllowsOwnWrite`: child reads and writes, then edits its own output without another read; the edit succeeds.
- `TestExternalChangeAfterWriterIsStale`: child writes, `os.Chtimes` advances the file mtime, then the child's edit is stale.
- `TestWholeFileWriteDoesNotRefreshStaleRead`: parent reads, child writes, parent's `file.write` stale error does not contain `read mark was refreshed`; retrying `file.write` is still stale.

Update `TestStaleEditErrorNamesWriterAndLineRange` to assert the writer note includes `at `, the refresh suffix `the current region is shown below`, and the retry sentence. The retry must still apply without a separate read.

**Verify:**

```bash
CGO_ENABLED=1 gofmt -w internal/tools/native
CGO_ENABLED=1 go test ./internal/tools/native/...
```

Expected: all native tests pass, including the new cross-agent cases and the existing stale-edit retry case.

**Commit:** `filetrack-stale-epoch: task 2 — enforce the shared stale epoch`

## Task 3: Wire actor identities through production

**Goal:** Production toolsets write distinct actor IDs: main, plan author, pipeline workers, and `agent.run` children. The child request carries the unique `WorkID` and description label.

**Files:** `internal/agent/subagent.go`, `internal/agent/subagent_test.go`, `internal/app/app.go`, `internal/app/app_test.go`.

### Steps

1. **Subagent request.** In `internal/agent/subagent.go`, add to `SubagentRequest`:

```go
ActorID    string
ActorLabel string
```

Add a short comment: the actor ID is the child's end-gate `WorkID`; the label is the human writer name.

2. **Handler.** In the `agent.run` handler, before `child, childState, err := factory(...)`, build the label:

```go
actorLabel := "a subagent"
if args.Description != "" {
    actorLabel = fmt.Sprintf("a subagent (%s)", args.Description)
}
```

Pass both fields in the factory request:

```go
child, childState, err := factory(SubagentRequest{
    Agent: args.Agent, Model: args.Model,
    Temperature: args.Temperature, Thinking: args.Thinking, System: args.System,
    ActorID: string(workID), ActorLabel: actorLabel,
})
```

3. **Subagent factory.** In `buildSubagentFactoryWithLock` in `internal/app/app.go`, replace `childOpts.AgentLabel = "a subagent"` with:

```go
childOpts.AgentID = req.ActorID
childOpts.AgentLabel = req.ActorLabel
```

4. **Main and plan author.** In `buildAgentRunnerWithLock`, add `AgentID: "main"` next to `AgentLabel: "the main agent"`. In `buildPlanAuthorFactory`, add `AgentID: "plan-author"` next to `AgentLabel: "the plan author"`.

5. **Pipeline workers.** Add `"sync/atomic"` to `internal/app/app.go` imports. Add near the other small helpers:

```go
var workerActorSeq atomic.Int64

func newWorkerActorID(prefix string) string {
    return fmt.Sprintf("%s-%d", prefix, workerActorSeq.Add(1))
}
```

In `makePipelineRegistryFactory`, add `AgentID: newWorkerActorID("pipeline")` next to `AgentLabel: "a pipeline worker"`.

6. **Tests.** In `internal/agent/subagent_test.go`, add `TestAgentRunRequestCarriesActorIdentity`: register `NewSubagentTool` with a factory that captures `req`, returns `&Runner{State: state}, state, nil`, and uses `WithSubagentExec`; invoke the `agent.run` handler with `{"prompt":"review","description":"branch review"}`; assert `req.ActorID != ""` and `req.ActorLabel == "a subagent (branch review)"`.

In `internal/app/app_test.go`, add `TestNewWorkerActorIDIsUnique`: call `newWorkerActorID("pipeline")` twice and assert the values differ and both have the `pipeline-` prefix.

**Verify:**

```bash
CGO_ENABLED=1 gofmt -w internal/agent internal/app
CGO_ENABLED=1 go test ./internal/agent/... ./internal/app/... ./internal/tools/native/...
```

Expected: the new request/identity tests pass, existing subagent tests pass, and the app package compiles with the new literal options.

**Commit:** `filetrack-stale-epoch: task 3 — wire actor identities`

## Task 4: Document the shared epoch and verify the suite

**Goal:** The editing guide matches the shipped behavior and the full project suite is green.

**Files:** `docs/tools/editing.md`, `docs/filetrack/feature-spec.md`, `docs/filetrack/plans/2026-11-11-filetracker-stale-epoch-plan.md`.

### Steps

1. Replace the paragraph beginning `The ledger is **per conversation, not per agent**` through the `native.Options.AgentLabel` paragraph with documentation that states:

- the ledger is per conversation and now carries a per-path revision;
- every committed write advances the revision and invalidates every other agent;
- the newest writer is exempt from its own write, but an external mtime change after that write still makes it stale;
- any read (full `file.read` or the region embedded in a stale error) advances the shared read revision and clears the epoch for all agents;
- the stale error names the writer and time, pins the region to exact lines, and includes the retry sentence only for exact-match patches;
- whole-file `file.write` does not refresh from one region;
- `native.Options.AgentID` is the stable writer identity and `AgentLabel` the display name; empty `AgentID` falls back to the label.

2. Replace the stale `file.go:601` / `file.go:811` line references in that section with function names (`applyFilePatches`, `fileWriteTool`) so the doc does not rot on the next edit.

3. Keep the rest of `docs/tools/editing.md` unchanged, including the doubled-worktree-path section.

4. Stage the approved spec and this plan with the documentation commit:

```bash
git add docs/tools/editing.md docs/filetrack/feature-spec.md docs/filetrack/plans/2026-11-11-filetracker-stale-epoch-plan.md
```

Do not stage any pre-existing untracked files outside `docs/filetrack/`.

5. **Final verification:**

```bash
CGO_ENABLED=1 gofmt -w internal/
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./...
git diff --check
```

Expected: formatting is clean, the build passes, every package test passes, and `git diff --check` reports no whitespace errors. Report any pre-existing failure separately; do not hide it.

**Commit:** `filetrack-stale-epoch: task 4 — document the shared epoch`
