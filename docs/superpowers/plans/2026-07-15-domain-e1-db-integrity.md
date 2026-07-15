# Domain E1 — DB Integrity, Query Correctness & Code Hygiene

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close 18 findings in `internal/db/`, `internal/db/audits.go`, and `internal/db/todos.go` by introducing transactional snapshot writes, escaping SQL `LIKE` wildcards, batching large `IN (...)` queries, restoring sandbox audit round-trip, and removing dead / fragile code paths. Findings: F-BUG-103, F-BUG-104, F-BUG-105, F-BUG-106, F-BUG-107, F-BUG-108, F-BUG-109, F-BUG-115, F-BUG-116, F-BUG-135, F-PERF-114, F-SEC-121, F-SEC-124, F-POL-125, F-POL-126, F-POL-127, F-POL-128, F-POL-136.

**Architecture:** Each task fixes one or two tightly-related findings in a single Go file. The plan author relies on the existing `*sql.Tx` pattern (already used by `SaveSymbols` and `SaveFileIndex`) as the model for the new transactional `SaveSnapshot`. SQL helpers (`escapeLike`, `chunkedPlaceholders`, `parseColumnSet`) are added to a new `internal/db/sqlutil/` package so the same primitives can be reused by E2 (`FindSymbols` / `FilesMatchingBasename` already need `escapeLike`) and E3 (`SaveSymbols` / `SaveFileIndex` already need batched inserts).

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (already in `go.sum`), `database/sql`. No new dependencies.

**Source audit:** `docs/14-codebase-improvement-audit-2026-07-14.md`, domain E (items F-BUG-103, F-BUG-104, F-BUG-105, F-BUG-106, F-BUG-107, F-BUG-108, F-BUG-109, F-BUG-115, F-BUG-116, F-BUG-135, F-PERF-114, F-SEC-121, F-SEC-124, F-POL-125, F-POL-126, F-POL-127, F-POL-128, F-POL-136).

**Pre-resolved (handled in earlier batches, do not redo):** F-BUG-102, F-POL-130, F-SEC-122, F-SEC-123. Listed here for tracking only.

## Global Constraints

- `go build ./...` must succeed after every task.
- `go test ./...` must pass after every task.
- `gofmt -w .` after any file change.
- No TUI / config / public API changes. DB-layer behavior changes (transactional writes, larger IN clauses, restored audit fields) are user-visible only in the audit trail — document them in the final task's release-note paragraph in `docs/04-tooling-and-shell-safety.md` if they affect user-facing audit output.
- Sandbox test isolation: tests that open a DB must use `t.TempDir()` or `:memory:` and never touch `os.Getenv("HOME")`.
- New exported symbols have doc comments. New error paths return wrapped errors (`fmt.Errorf("...: %w", err)`).
- Migration columns are added via the existing `tableColumns` introspection pattern in `db.Migrate()`; never `DROP` / `RENAME` existing tables.
- Backward compatibility: the on-disk format of `tool_calls.files_changed`, `tool_calls.sandbox_*`, `messages.*`, `symbols.*`, `files.*`, `agent_sessions.*`, `projects.*`, `snapshots.*`, `snapshot_files.*`, `turn_metrics.*` is **append-only** for this batch. New columns are added; existing column semantics do not change.

---

## File structure

### New files

| Path | Responsibility |
|---|---|
| `internal/db/sqlutil/like.go` | `escapeLike(s string) string` — escapes `%`, `_`, `\` for SQL `LIKE` patterns |
| `internal/db/sqlutil/like_test.go` | Unit tests for `escapeLike` |
| `internal/db/sqlutil/placeholders.go` | `ChunkedPlaceholders(n, max int) []string` — splits a list of `?`-placeholders into chunks ≤ `max` to respect SQLite's 999-host-parameter cap |
| `internal/db/sqlutil/placeholders_test.go` | Unit tests for `ChunkedPlaceholders` |
| `internal/db/migrations_002.go` | Migration helper invoked from `Migrate()` to add new `tool_calls` columns and new `idx_files_project` / `idx_symbols_project` indexes |

### Modified files

- `internal/db/snapshots.go` — `SaveSnapshot` is transactional, `LatestSnapshot` returns zero-values on partial failure, `PruneSnapshotsOlderThan` validates `days`
- `internal/db/projects.go` — `GetOrCreateProject` uses `SELECT id FROM projects WHERE root_path = ?` directly instead of relying on `LastInsertId() == 0`
- `internal/db/sessions.go` — `MessagesOnBranch` walks the branch in a single recursive CTE, eliminating the N+1 and the unbounded `IN (...)` (closes both F-BUG-105 and F-BUG-115)
- `internal/db/turnmetrics.go` — `RecentTurnMetrics` clamps `limit <= 0` to a default
- `internal/db/audits.go` — add `sandbox_enabled INTEGER NOT NULL DEFAULT 0`, `resource_limits INTEGER NOT NULL DEFAULT 0`, `output_truncated INTEGER NOT NULL DEFAULT 0` columns; persist them; restore `ResourceLimits` / `OutputTruncated` on read
- `internal/db/symbols.go` — extract `scanSymbol(rows) (Symbol, error)`; `FindSymbols` and `GetSymbols` use it
- `internal/db/files.go` — `SaveFileIndex` uses `defer rows.Close()`; `SaveFileIndex` still uses prepared-statement per-row (batched insert deferred to E3)
- `internal/db/todos.go` — wrap every error
- `internal/db/db.go` — remove `DB.exec` / `DB.queryRow` wrappers, update all callers to call `db.sqlDB` directly
- `internal/db/audits.go` — add `sandbox_enabled INTEGER NOT NULL DEFAULT 0` to the `Migrate()` column-add map
- `internal/db/migrations.go` — add `CREATE INDEX IF NOT EXISTS idx_files_project ON files(project_id);` and `CREATE INDEX IF NOT EXISTS idx_symbols_project ON symbols(project_id);` after the existing `idx_symbols_project_name`
- `internal/db/db.go` — `tableColumns` accepts the table name via an explicit allowlist; rejects any other input

> Note: The `sandbox_enabled` / `resource_limits` / `output_truncated` columns and the new indexes are added in **E1** rather than E3 so the existing test suite keeps passing on legacy DB files. E3 then upgrades `SaveFileIndex` / `SaveSymbols` to batched inserts.

---

# Task 1: F-POL-126 — Drop `DB.exec` / `DB.queryRow` thin wrappers

**Files:**
- Modify: `internal/db/db.go` (delete the wrappers; add comments)
- Modify: every caller listed in the audit grep `db\.exec|db\.queryRow` (`audits.go`, `sessions.go`, `projects.go`, `files.go`, `turnmetrics.go`, `memories.go`) — replace each call with `db.sqlDB.Exec(...)` / `db.sqlDB.QueryRow(...)`

**Interfaces:**
- Consumes: the existing `DB` struct; `db.sqlDB` is the private `*sql.DB` that the wrappers forward to
- Produces: two deleted methods; all callers use `db.sqlDB` directly. New code can pass contexts.

- [ ] **Step 1: Audit every caller**

Run:
```bash
grep -rn 'db\.exec\|db\.queryRow' internal/db
```
to confirm the file list. Expected callers (per the audit): `audits.go:65`, `sessions.go:37, 53, 84, 96, 130, 152, 164, 175, 191`, `projects.go:22, 46, 71, 87`, `files.go:165`, `turnmetrics.go:38`, `memories.go:28, 85`.

- [ ] **Step 2: Replace `db.exec(query, args...)` → `db.sqlDB.Exec(query, args...)` and `db.queryRow(query, args...)` → `db.sqlDB.QueryRow(query, args...)`**

Mechanically edit each caller. Verify `go build ./...` still succeeds.

- [ ] **Step 3: Delete the wrappers**

In `internal/db/db.go`, remove the `func (db *DB) exec` and `func (db *DB) queryRow` definitions. Also remove the now-unused imports in the modified files (run `goimports -w .`).

- [ ] **Step 4: Build & test**

```bash
go build ./...
go test ./internal/db/...
```
Both must pass.

- [ ] **Step 5: Commit**

```bash
git add internal/db/
git commit -m "db: drop thin exec/queryRow wrappers (F-POL-126)"
```

---

# Task 2: F-POL-125 — Remove dead `joinSnapshotFiles`

**Files:**
- Modify: `internal/db/snapshots.go` — delete the `joinSnapshotFiles` function (lines 101-104)

**Interfaces:**
- Consumes: nothing (function is unused; the audit grep `joinSnapshotFiles` only returns the definition)
- Produces: the function no longer exists

- [ ] **Step 1: Verify it is unused**

```bash
grep -rn 'joinSnapshotFiles' .
```
Expected output: only the definition in `internal/db/snapshots.go`.

- [ ] **Step 2: Delete the function and its only import of `strings` if no longer used in the file**

Edit `internal/db/snapshots.go` to remove the function. Check whether `strings` is still imported by other code in the file; if not, remove from the import block.

- [ ] **Step 3: Build & test**

```bash
go build ./...
go test ./internal/db/...
```

- [ ] **Step 4: Commit**

```bash
git add internal/db/snapshots.go
git commit -m "db: remove dead joinSnapshotFiles helper (F-POL-125)"
```

---

# Task 3: F-POL-127 — Use `defer rows.Close()` in `SaveFileIndex`

**Files:**
- Modify: `internal/db/files.go:25-82` — replace the manual `rows.Close()` call after the existing-files scan with `defer rows.Close()` and remove the two manual close-on-error paths

**Interfaces:**
- Consumes: the `*sql.Rows` returned by `tx.Query(...)`
- Produces: clean, idiomatic Go that uses `defer rows.Close()` and never leaks the rows handle on error paths

- [ ] **Step 1: Rewrite the existing-files scan loop**

Replace:
```go
rows, err := tx.Query(`SELECT path, hash, summary FROM files WHERE project_id = ?`, projectID)
if err != nil {
    return fmt.Errorf("query existing file index: %w", err)
}
for rows.Next() {
    var path, hash string
    var summary sql.NullString
    if err := rows.Scan(&path, &hash, &summary); err != nil {
        rows.Close()
        return fmt.Errorf("scan existing file row: %w", err)
    }
    ...
}
if err := rows.Err(); err != nil {
    rows.Close()
    return fmt.Errorf("iterate existing file rows: %w", err)
}
rows.Close()
```
with:
```go
rows, err := tx.Query(`SELECT path, hash, summary FROM files WHERE project_id = ?`, projectID)
if err != nil {
    return fmt.Errorf("query existing file index: %w", err)
}
defer rows.Close()
for rows.Next() {
    var path, hash string
    var summary sql.NullString
    if err := rows.Scan(&path, &hash, &summary); err != nil {
        return fmt.Errorf("scan existing file row: %w", err)
    }
    ...
}
if err := rows.Err(); err != nil {
    return fmt.Errorf("iterate existing file rows: %w", err)
}
```

- [ ] **Step 2: Build & test**

```bash
go build ./...
go test ./internal/db/...
```
The existing `TestSaveFileIndexPreservesSummary` and `TestFilesMatchingBasename` must still pass.

- [ ] **Step 3: Commit**

```bash
git add internal/db/files.go
git commit -m "db: defer rows.Close in SaveFileIndex (F-POL-127)"
```

---

# Task 4: F-POL-128 — Extract `scanSymbol` helper

**Files:**
- Modify: `internal/db/symbols.go` — extract the row-scanning loop in `GetSymbols` and `FindSymbols` into a single helper

**Interfaces:**
- Produces: `func scanSymbol(rows *sql.Rows) (Symbol, error)` — reads one `*sql.Rows` row in the same column order as the existing `SELECT` in `GetSymbols`/`FindSymbols`

- [ ] **Step 1: Add the helper**

```go
// scanSymbol reads the next row from rows in the column order used by both
// GetSymbols and FindSymbols (id, file_path, kind, name, receiver,
// signature, line_start, line_end).
func scanSymbol(rows *sql.Rows) (Symbol, error) {
    var s Symbol
    if err := rows.Scan(&s.ID, &s.FilePath, &s.Kind, &s.Name, &s.Receiver, &s.Signature, &s.LineStart, &s.LineEnd); err != nil {
        return Symbol{}, err
    }
    return s, nil
}
```

- [ ] **Step 2: Use it in `GetSymbols` and `FindSymbols`**

Replace each `if err := rows.Scan(&s.ID, ...); err != nil { return ..., fmt.Errorf("scan symbol row: %w", err) }` block with `s, err := scanSymbol(rows); if err != nil { return ..., fmt.Errorf("scan symbol row: %w", err) }`. Add `"database/sql"` to the import block if not already present.

- [ ] **Step 3: Build & test**

```bash
go build ./...
go test ./internal/db/...
```
`TestSaveAndGetSymbols`, `TestSaveSymbolsReplacesExisting`, `TestFindSymbolsFiltersByNameAndKind`, `TestFindSymbolsLimitDefaultsAndClamps` must still pass.

- [ ] **Step 4: Commit**

```bash
git add internal/db/symbols.go
git commit -m "db: extract scanSymbol helper for Get/FindSymbols (F-POL-128)"
```

---

# Task 5: F-POL-136 — Wrap errors in `todos.go`

**Files:**
- Modify: `internal/db/todos.go` — wrap every `json.Marshal`, `Exec`, `QueryRow`, and `json.Unmarshal` error

- [ ] **Step 1: Wrap each error**

Replace:
```go
data, err := json.Marshal(todos)
if err != nil { return err }
...
if err != nil { return err }
```
with `fmt.Errorf("marshal todos: %w", err)`, `fmt.Errorf("save todos: %w", err)`, `fmt.Errorf("load todos: %w", err)`, `fmt.Errorf("unmarshal todos: %w", err)`. Add the `fmt` import.

- [ ] **Step 2: Build & test**

```bash
go build ./...
go test ./internal/db/...
```

- [ ] **Step 3: Commit**

```bash
git add internal/db/todos.go
git commit -m "db: wrap errors in todos (F-POL-136)"
```

---

# Task 6: F-BUG-103 / F-BUG-104 — Transactional `SaveSnapshot`, zero-value `LatestSnapshot` on error

**Files:**
- Modify: `internal/db/snapshots.go:10-29` (transactional insert) and `:31-55` (zero on error)

**Interfaces:**
- Produces: `SaveSnapshot` wraps both the `snapshots` row insert and every `snapshot_files` row insert in a single `*sql.Tx`. `LatestSnapshot` returns `(0, "", nil, err)` on any error during the `snapshot_files` scan (it currently returns the partial `id, hash, nil, err`).

- [ ] **Step 1: Add a failing test for `SaveSnapshot` rollback**

In `internal/db/snapshots_test.go`, add:
```go
func TestSaveSnapshotRollsBackFilesOnError(t *testing.T) {
    db := testSnapshotDB(t)
    at := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
    if _, err := db.SaveSnapshot("s1", 1, "h1", nil, at); err != nil {
        t.Fatalf("seed: %v", err)
    }
    // A second snapshot for the same session with an empty path; ensure
    // that the snapshot row is rolled back when the file insert fails.
    _, err := db.SaveSnapshot("s1", 2, "h2", []string{""}, at)
    if err == nil {
        t.Fatal("expected error for empty file path")
    }
    // After rollback, the second snapshot must not exist.
    id, _, _, err := db.LatestSnapshot("s1")
    if err != nil { t.Fatalf("LatestSnapshot: %v", err) }
    if id != 1 {
        t.Fatalf("expected only seed snapshot (id=1), got id=%d", id)
    }
}
```
The test will fail under the current non-transactional code only if `snapshot_files.path` has a `CHECK (length(path) > 0)` constraint; without one the test passes whether the code is transactional or not. **Add a `CHECK (length(path) > 0)` constraint on `snapshot_files.path` in the migration** so the rollback is observable.

> Why a check constraint: SQLite enforces constraints inside the transaction, so the rollback path is exercised by an actual SQL error rather than a Go-side panic.

In `internal/db/migrations.go`, change:
```sql
CREATE TABLE IF NOT EXISTS snapshot_files (
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    PRIMARY KEY(snapshot_id, path)
);
```
to:
```sql
CREATE TABLE IF NOT EXISTS snapshot_files (
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    path TEXT NOT NULL CHECK (length(path) > 0),
    PRIMARY KEY(snapshot_id, path)
);
```

- [ ] **Step 2: Rewrite `SaveSnapshot` to use a transaction**

```go
func (db *DB) SaveSnapshot(sessionID string, turnIndex int, hash string, files []string, at time.Time) (int64, error) {
    tx, err := db.sqlDB.Begin()
    if err != nil {
        return 0, fmt.Errorf("begin save snapshot: %w", err)
    }
    defer tx.Rollback()

    res, err := tx.Exec(
        `INSERT INTO snapshots (session_id, turn_index, hash, created_at) VALUES (?, ?, ?, ?)`,
        sessionID, turnIndex, hash, at.UTC().Format(time.RFC3339),
    )
    if err != nil {
        return 0, fmt.Errorf("insert snapshot: %w", err)
    }
    id, err := res.LastInsertId()
    if err != nil {
        return 0, fmt.Errorf("snapshot id: %w", err)
    }

    for _, f := range files {
        if _, err := tx.Exec(
            `INSERT INTO snapshot_files (snapshot_id, path) VALUES (?, ?)`,
            id, f,
        ); err != nil {
            return 0, fmt.Errorf("insert snapshot file: %w", err)
        }
    }

    if err := tx.Commit(); err != nil {
        return 0, fmt.Errorf("commit save snapshot: %w", err)
    }
    return id, nil
}
```

- [ ] **Step 3: Make `LatestSnapshot` return zero values on error**

Replace:
```go
if err := rows.Scan(&p); err != nil {
    return id, hash, nil, err
}
files = append(files, p)
...
return id, hash, files, rows.Err()
```
with:
```go
if err := rows.Scan(&p); err != nil {
    return 0, "", nil, fmt.Errorf("scan snapshot file: %w", err)
}
files = append(files, p)
...
if err := rows.Err(); err != nil {
    return 0, "", nil, fmt.Errorf("iterate snapshot files: %w", err)
}
return id, hash, files, nil
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/db/ -run Snapshot
```
Both `TestSaveAndLatestSnapshot` and the new `TestSaveSnapshotRollsBackFilesOnError` must pass.

- [ ] **Step 5: Commit**

```bash
git add internal/db/snapshots.go internal/db/migrations.go internal/db/snapshots_test.go
git commit -m "db: transactional SaveSnapshot and zero-on-error LatestSnapshot (F-BUG-103, F-BUG-104)"
```

---

# Task 7: F-BUG-106 — Validate `days` in `PruneSnapshotsOlderThan`

**Files:**
- Modify: `internal/db/snapshots.go:70-81`

- [ ] **Step 1: Add a failing test**

```go
func TestPruneSnapshotsOlderThanRejectsNegative(t *testing.T) {
    db := testSnapshotDB(t)
    if err := db.PruneSnapshotsOlderThan(-1); err == nil {
        t.Fatal("expected error for negative days")
    }
}
```

- [ ] **Step 2: Validate `days`**

```go
func (db *DB) PruneSnapshotsOlderThan(days int) error {
    if days < 0 {
        return fmt.Errorf("prune snapshots: days must be >= 0, got %d", days)
    }
    ...
}
```

- [ ] **Step 3: Build & test**

```bash
go build ./...
go test ./internal/db/ -run Prune
```

- [ ] **Step 4: Commit**

```bash
git add internal/db/snapshots.go internal/db/snapshots_test.go
git commit -m "db: reject negative days in PruneSnapshotsOlderThan (F-BUG-106)"
```

---

# Task 8: F-BUG-107 — Stop relying on `LastInsertId() == 0`

**Files:**
- Modify: `internal/db/projects.go:68-94`

- [ ] **Step 1: Add a failing test**

Open an in-memory DB, monkey-patch the `upsert` path: the cleanest way is to test against a `GetOrCreateProject` call after a row already exists for the same `root_path` and verify the returned id is the existing row's id, not 0.

```go
func TestGetOrCreateProjectReturnsExistingID(t *testing.T) {
    db, err := Open(":memory:")
    if err != nil { t.Fatalf("Open: %v", err) }
    defer db.Close()
    if err := db.Migrate(); err != nil { t.Fatalf("Migrate: %v", err) }
    id1, err := db.GetOrCreateProject("/r", "first")
    if err != nil { t.Fatalf("create: %v", err) }
    // The audit finding: LastInsertId() can return 0 after a no-op UPSERT.
    // The fix must look up the existing id explicitly when LastInsertId is 0.
    id2, err := db.GetOrCreateProject("/r", "second")
    if err != nil { t.Fatalf("update: %v", err) }
    if id1 != id2 {
        t.Fatalf("expected existing id %d, got %d", id1, id2)
    }
}
```

- [ ] **Step 2: Always fall back to the `SELECT id` path**

Replace:
```go
res, err := db.sqlDB.Exec(`INSERT ... ON CONFLICT(root_path) DO UPDATE ...`)
if err != nil { return 0, fmt.Errorf("upsert project: %w", err) }
id, err := res.LastInsertId()
if err != nil { return 0, fmt.Errorf("get last project id: %w", err) }
if id == 0 {
    row := db.sqlDB.QueryRow(`SELECT id FROM projects WHERE root_path = ?`, rootPath)
    if scanErr := row.Scan(&id); scanErr != nil { return 0, fmt.Errorf("lookup project id: %w", scanErr) }
}
return id, nil
```
with the unconditionally-fall-back version:
```go
if _, err := db.sqlDB.Exec(`INSERT INTO projects (root_path, name, created_at, updated_at)
                            VALUES (?, ?, ?, ?)
                            ON CONFLICT(root_path) DO UPDATE SET name=excluded.name, updated_at=excluded.updated_at`,
    rootPath, name, now, now,
); err != nil {
    return 0, fmt.Errorf("upsert project: %w", err)
}
var id int64
if err := db.sqlDB.QueryRow(`SELECT id FROM projects WHERE root_path = ?`, rootPath).Scan(&id); err != nil {
    return 0, fmt.Errorf("lookup project id: %w", err)
}
return id, nil
```

- [ ] **Step 3: Build & test**

```bash
go test ./internal/db/ -run Project
```

- [ ] **Step 4: Commit**

```bash
git add internal/db/projects.go internal/db/projects_test.go
git commit -m "db: GetOrCreateProject selects id after upsert (F-BUG-107)"
```

---

# Task 9: F-BUG-135 — Clamp `limit <= 0` in `RecentTurnMetrics`

**Files:**
- Modify: `internal/db/turnmetrics.go:76-87`

- [ ] **Step 1: Add a failing test**

```go
func TestRecentTurnMetricsClampsNonPositiveLimit(t *testing.T) {
    db, projectID := openMetricsTestDB(t)
    if err := db.InsertTurnMetrics(sampleRow(projectID, "s1")); err != nil {
        t.Fatalf("Insert: %v", err)
    }
    for _, limit := range []int{0, -1, -100} {
        rows, err := db.RecentTurnMetrics(projectID, limit)
        if err != nil { t.Fatalf("limit=%d: %v", limit, err) }
        if len(rows) != 1 {
            t.Errorf("limit=%d: expected 1 row, got %d", limit, len(rows))
        }
    }
}
```

- [ ] **Step 2: Clamp the limit**

```go
const recentTurnMetricsDefaultLimit = 50
const recentTurnMetricsMaxLimit     = 200

func (db *DB) RecentTurnMetrics(projectID int64, limit int) ([]TurnMetricsRow, error) {
    if limit <= 0 {
        limit = recentTurnMetricsDefaultLimit
    }
    if limit > recentTurnMetricsMaxLimit {
        limit = recentTurnMetricsMaxLimit
    }
    ...
}
```

- [ ] **Step 3: Build & test**

```bash
go test ./internal/db/ -run TurnMetrics
```

- [ ] **Step 4: Commit**

```bash
git add internal/db/turnmetrics.go internal/db/turnmetrics_test.go
git commit -m "db: clamp RecentTurnMetrics limit (F-BUG-135)"
```

---

# Task 10: F-SEC-121 — Allowlist `tableColumns` table names

**Files:**
- Modify: `internal/db/db.go:162-184`
- Modify: `internal/db/db.go:54-159` (callers that pass table names: `tool_calls`, `files`, `messages`, `agent_sessions`)

**Interfaces:**
- Produces: an internal allowlist constant + a guard in `tableColumns` that returns an error if the caller passes anything not in the allowlist.

- [ ] **Step 1: Add the allowlist**

```go
// allowedTableInfo lists the tables whose schema may be introspected via
// tableColumns. The list is the union of every table referenced by
// Migrate()'s column-add backfill branches.
var allowedTableInfo = map[string]bool{
    "tool_calls":     true,
    "files":          true,
    "messages":       true,
    "agent_sessions": true,
}

func (db *DB) tableColumns(table string) (map[string]bool, error) {
    if !allowedTableInfo[table] {
        return nil, fmt.Errorf("tableColumns: table %q is not in the introspection allowlist", table)
    }
    // The table name is now provably constant; the Sprintf is still used
    // for clarity but the value is no longer user-controllable.
    rows, err := db.sqlDB.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
    ...
}
```

- [ ] **Step 2: Build & test**

```bash
go test ./internal/db/
```
`Migrate()` exercises all four allowed tables; existing tests cover the column-add paths.

- [ ] **Step 3: Commit**

```bash
git add internal/db/db.go
git commit -m "db: allowlist tableColumns table names (F-SEC-121)"
```

---

# Task 11: F-SEC-124 — Normalize `files_changed` JSON in audit

**Files:**
- Modify: `internal/db/audits.go:21-23, 67-68`

- [ ] **Step 1: Add a test**

```go
func TestSaveToolCallNormalizesFilesChanged(t *testing.T) {
    db, err := Open(":memory:")
    if err != nil { t.Fatalf("Open: %v", err) }
    defer db.Close()
    if err := db.Migrate(); err != nil { t.Fatalf("Migrate: %v", err) }

    raw := `a\b/c.go`
    ev := registry.AuditEvent{
        ToolName:    "shell.run",
        FilesChanged: []string{raw},
        Timestamp:   time.Now().UTC(),
    }
    if err := db.SaveToolCall("s1", ev); err != nil { t.Fatalf("Save: %v", err) }
    rows, err := db.GetToolCalls("s1")
    if err != nil { t.Fatalf("Get: %v", err) }
    if len(rows) != 1 { t.Fatalf("expected 1 row, got %d", len(rows)) }
    if rows[0].FilesChanged[0] != filepath.ToSlash(raw) {
        t.Errorf("expected slash-normalized path, got %q", rows[0].FilesChanged[0])
    }
}
```

- [ ] **Step 2: Normalize before marshal**

In `SaveToolCall`, replace:
```go
filesChanged, err := json.Marshal(event.FilesChanged)
```
with:
```go
normalized := make([]string, len(event.FilesChanged))
for i, p := range event.FilesChanged {
    normalized[i] = filepath.ToSlash(p)
}
filesChanged, err := json.Marshal(normalized)
```
Add `path/filepath` to the import block.

- [ ] **Step 3: Build & test**

```bash
go test ./internal/db/ -run ToolCall
```

- [ ] **Step 4: Commit**

```bash
git add internal/db/audits.go internal/db/audits_test.go
git commit -m "db: normalize files_changed paths to forward slashes (F-SEC-124)"
```

---

# Task 12: F-BUG-108 / F-BUG-109 — Persist and restore `Sandbox.Enabled`, `ResourceLimits`, `OutputTruncated`

**Files:**
- Modify: `internal/db/migrations.go` — append three columns to the `tool_calls` `CREATE TABLE` (only effective on fresh DBs; the `Migrate()` backfill adds them on existing DBs)
- Modify: `internal/db/db.go:67-86` — extend the `columnDefs` map with `sandbox_enabled`, `resource_limits`, `output_truncated`
- Modify: `internal/db/audits.go` — write the three new columns in `SaveToolCall`; read them in `GetToolCalls` and populate `e.Sandbox.Enabled` / `e.Sandbox.ResourceLimits` / `e.Sandbox.OutputTruncated`

- [ ] **Step 1: Add the new `tool_calls` columns to the `columnDefs` map**

```go
columnDefs := map[string]string{
    ...
    "sandbox_enabled":  "INTEGER NOT NULL DEFAULT 0",
    "resource_limits":  "INTEGER NOT NULL DEFAULT 0",
    "output_truncated": "INTEGER NOT NULL DEFAULT 0",
}
```

- [ ] **Step 2: Persist the three new fields in `SaveToolCall`**

Extend the `INSERT INTO tool_calls (...)` column list and the parameter list. For `Sandbox.Enabled`, `event.Sandbox.Backend != ""` is the existing heuristic; the new column is the authoritative source of truth. For `ResourceLimits` and `OutputTruncated`, write `boolToInt(event.Sandbox.ResourceLimits)` and `boolToInt(event.Sandbox.OutputTruncated)`.

- [ ] **Step 3: Restore them in `GetToolCalls`**

Add three new `sql.NullInt64` scan targets. After the existing `if sbBackend.Valid && sbBackend.String != ""` block, set:
```go
if sbEnabled.Valid { e.Sandbox.Enabled = sbEnabled.Int64 == 1 }
if rl.Valid { e.Sandbox.ResourceLimits = rl.Int64 == 1 }
if ot.Valid { e.Sandbox.OutputTruncated = ot.Int64 == 1 }
```

> Migration safety: existing rows that lack the new columns will get `0` from the `NOT NULL DEFAULT 0` clause. This is correct: the read path only flips them on when the column is non-zero. A legacy audit row where the backend was non-empty will still show `Sandbox.Enabled = false` on read, which is a small but unavoidable migration cost. Document the caveat in the release note.

- [ ] **Step 4: Add a round-trip test**

```go
func TestSaveToolCallRoundTripsSandboxMeta(t *testing.T) {
    db, err := Open(":memory:")
    if err != nil { t.Fatalf("Open: %v", err) }
    defer db.Close()
    if err := db.Migrate(); err != nil { t.Fatalf("Migrate: %v", err) }

    ev := registry.AuditEvent{
        ToolName:  "shell.run",
        Timestamp: time.Now().UTC(),
        Sandbox: registry.SandboxMeta{
            Enabled:         true,
            Backend:         "restricted",
            NetworkIsolated: true,
            ResourceLimits:  true,
            OutputTruncated: true,
        },
    }
    if err := db.SaveToolCall("s1", ev); err != nil { t.Fatalf("Save: %v", err) }
    rows, err := db.GetToolCalls("s1")
    if err != nil { t.Fatalf("Get: %v", err) }
    if len(rows) != 1 { t.Fatalf("expected 1 row, got %d", len(rows)) }
    sm := rows[0].Sandbox
    if !sm.Enabled || sm.Backend != "restricted" {
        t.Errorf("Enabled/Backend not preserved: %+v", sm)
    }
    if !sm.ResourceLimits || !sm.OutputTruncated {
        t.Errorf("ResourceLimits/OutputTruncated not preserved: %+v", sm)
    }
}
```

- [ ] **Step 5: Build & test**

```bash
go test ./internal/db/ -run ToolCall
```

- [ ] **Step 6: Commit**

```bash
git add internal/db/audits.go internal/db/db.go internal/db/migrations.go internal/db/audits_test.go
git commit -m "db: persist Sandbox.Enabled/ResourceLimits/OutputTruncated (F-BUG-108, F-BUG-109)"
```

---

# Task 13: F-PERF-114 — Add `idx_files_project` / `idx_symbols_project`

**Files:**
- Modify: `internal/db/migrations.go` — add two `CREATE INDEX IF NOT EXISTS` statements after the existing `idx_symbols_project_name`

- [ ] **Step 1: Add the indexes**

After `CREATE INDEX IF NOT EXISTS idx_symbols_project_name ON symbols(project_id, name);`, append:
```sql
CREATE INDEX IF NOT EXISTS idx_files_project ON files(project_id);
CREATE INDEX IF NOT EXISTS idx_symbols_project ON symbols(project_id);
```

- [ ] **Step 2: Verify migration is idempotent**

The `IF NOT EXISTS` clause makes the migration safe to re-run. Run `TestMigrate` to confirm.

- [ ] **Step 3: Build & test**

```bash
go test ./internal/db/
```

- [ ] **Step 4: Commit**

```bash
git add internal/db/migrations.go
git commit -m "db: add idx_files_project and idx_symbols_project (F-PERF-114)"
```

---

# Task 14: F-BUG-105 / F-BUG-115 — Single-recursive-CTE branch walk

**Files:**
- Modify: `internal/db/sessions.go:181-261` — replace the iterative `parent_id` walk + `IN (...)` query with a single `WITH RECURSIVE` CTE

**Interfaces:**
- Produces: `MessagesOnBranch(sessionID, leafID)` returns the same `[]Message` in the same chronological order, but does so with **one** SQL round-trip instead of N+1, and the result set is bounded by the tree depth (no `IN (...)` host-parameter limit).

- [ ] **Step 1: Write the failing test**

```go
func TestMessagesOnBranchLongChain(t *testing.T) {
    db, err := Open(":memory:")
    if err != nil { t.Fatalf("Open: %v", err) }
    defer db.Close()
    if err := db.Migrate(); err != nil { t.Fatalf("Migrate: %v", err) }
    if _, err := db.GetOrCreateProject("/r", "r"); err != nil { t.Fatalf("project: %v", err) }
    if _, err := db.CreateSession(...); err != nil { t.Fatalf(...) } // use the existing CreateSession helper
    // Insert 1500 messages in a linear chain (parent_id = previous id).
    // ...
    // Then call MessagesOnBranch on the last id and verify the entire
    // chain comes back in order.
}
```

- [ ] **Step 2: Rewrite the query**

```go
const branchCTE = `
WITH RECURSIVE chain(id, parent_id) AS (
    SELECT id, parent_id FROM messages
     WHERE id = ? AND session_id = ?
    UNION ALL
    SELECT m.id, m.parent_id FROM messages m
      JOIN chain c ON m.id = c.parent_id
     WHERE m.session_id = ?
)
SELECT m.id, m.role, m.content, m.content_type, m.reasoning, m.think_duration_ms,
       m.created_at, m.final, m.parent_id
  FROM messages m
  JOIN chain c ON m.id = c.id
 ORDER BY m.id ASC`

func (db *DB) MessagesOnBranch(sessionID string, leafID int64) ([]Message, error) {
    if leafID <= 0 {
        return nil, nil
    }
    rows, err := db.sqlDB.Query(branchCTE, leafID, sessionID, sessionID)
    if err != nil {
        return nil, fmt.Errorf("query branch messages: %w", err)
    }
    defer rows.Close()

    var out []Message
    for rows.Next() {
        var m Message
        var created string
        var reasoning sql.NullString
        var thinkDurationMs sql.NullInt64
        var contentType sql.NullString
        var final sql.NullInt64
        var parentID sql.NullInt64
        if err := rows.Scan(&m.ID, &m.Role, &m.Content, &contentType, &reasoning, &thinkDurationMs, &created, &final, &parentID); err != nil {
            return nil, fmt.Errorf("scan branch message: %w", err)
        }
        if contentType.Valid { m.ContentType = contentType.String }
        if reasoning.Valid { m.Reasoning = reasoning.String }
        if thinkDurationMs.Valid { m.ThinkDurationMs = thinkDurationMs.Int64 }
        m.Final = final.Valid && final.Int64 != 0
        if parentID.Valid { m.ParentID = parentID.Int64 }
        parsed, err := time.Parse(time.RFC3339, created)
        if err != nil { return nil, fmt.Errorf("parse created_at: %w", err) }
        m.CreatedAt = parsed.UTC()
        out = append(out, m)
    }
    return out, rows.Err()
}
```

- [ ] **Step 3: Build & test**

```bash
go test ./internal/db/ -run Branch
go test ./internal/db/ -run Session
```

- [ ] **Step 4: Commit**

```bash
git add internal/db/sessions.go internal/db/sessions_test.go
git commit -m "db: MessagesOnBranch uses recursive CTE (F-BUG-105, F-BUG-115)"
```

---

# Task 15: F-BUG-116 — Replace correlated subqueries in `ListSessions` with a join

**Files:**
- Modify: `internal/db/sessions.go:370-380` (the `listSessionsSQL` constant)

- [ ] **Step 1: Write the new query**

Replace the two correlated `(SELECT MAX(...))` / `(SELECT COUNT(*))` subqueries with a single LEFT JOIN against a pre-aggregated CTE:
```sql
WITH session_stats AS (
    SELECT session_id, MAX(created_at) AS updated_at, COUNT(*) AS message_count
      FROM messages
     GROUP BY session_id
)
SELECT s.id, p.root_path, s.title,
       COALESCE(ss.updated_at, s.started_at) AS updated_at,
       COALESCE(ss.message_count, 0)        AS message_count
  FROM agent_sessions s
  JOIN projects p ON p.id = s.project_id
  LEFT JOIN session_stats ss ON ss.session_id = s.id
 WHERE p.root_path = ?
 ORDER BY updated_at DESC, s.id DESC
 LIMIT ? OFFSET ?
```

- [ ] **Step 2: Build & test**

```bash
go test ./internal/db/ -run Session
```

- [ ] **Step 3: Commit**

```bash
git add internal/db/sessions.go
git commit -m "db: ListSessions joins against aggregated CTE (F-BUG-116)"
```

---

# Task 16: Full sweep

- [ ] **Step 1: Run the entire test suite**

```bash
go test ./...
```

- [ ] **Step 2: Update the audit doc**

Edit `docs/14-codebase-improvement-audit-2026-07-14.md` and add a new "Resolution status" subsection at the bottom of the file:

```markdown
### Batch 4 (E1 — DB integrity, query correctness, code hygiene): RESOLVED on branch `feature/domain-e1-db-integrity`
| Finding | Status | Notes |
|---|---|---|
| F-POL-126 | RESOLVED | DB.exec / DB.queryRow wrappers removed; callers use sqlDB directly |
| F-POL-125 | RESOLVED | joinSnapshotFiles removed |
| F-POL-127 | RESOLVED | SaveFileIndex uses defer rows.Close() |
| F-POL-128 | RESOLVED | scanSymbol helper extracted |
| F-POL-136 | RESOLVED | todos.go errors wrapped |
| F-BUG-103 | RESOLVED | SaveSnapshot is transactional; snapshot_files.path has CHECK (length > 0) |
| F-BUG-104 | RESOLVED | LatestSnapshot returns zero values on scan/iter error |
| F-BUG-106 | RESOLVED | PruneSnapshotsOlderThan rejects days < 0 |
| F-BUG-107 | RESOLVED | GetOrCreateProject always SELECTs id after upsert |
| F-BUG-135 | RESOLVED | RecentTurnMetrics clamps limit <= 0 |
| F-SEC-121 | RESOLVED | tableColumns table names are allowlisted |
| F-SEC-124 | RESOLVED | SaveToolCall normalizes FilesChanged via filepath.ToSlash |
| F-BUG-108 | RESOLVED | sandbox_enabled / resource_limits / output_truncated columns added; round-trip preserved |
| F-BUG-109 | RESOLVED | ResourceLimits / OutputTruncated read back from new columns |
| F-PERF-114 | RESOLVED | idx_files_project, idx_symbols_project added |
| F-BUG-105 | RESOLVED | MessagesOnBranch uses recursive CTE; no IN-clause limit |
| F-BUG-115 | RESOLVED | Same change eliminates N+1 |
| F-BUG-116 | RESOLVED | ListSessions joins against aggregated CTE |
```

- [ ] **Step 3: Commit the audit update**

```bash
git add docs/14-codebase-improvement-audit-2026-07-14.md
git commit -m "docs: mark domain-E1 findings resolved"
```

- [ ] **Step 4: Push / open PR (only if requested)**
