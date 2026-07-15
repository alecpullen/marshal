# Domain E3 — DB & Search Performance, Scaling, and Isolation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close 6 findings in `internal/db/`, `internal/repo/`, and `internal/tools/native/` by enabling SQLite WAL mode with a small read pool, batching bulk `SaveSymbols` / `SaveFileIndex` inserts, bounding `GetSymbols` / `GetFileIndex` callers, adding file-size caps to the search and scanner, short-circuiting `searchFiles` once the cap is hit, and introducing a project-level mutex for `SaveFileIndex` / `SaveSymbols`. Findings: F-PERF-112, F-PERF-113, F-PERF-117, F-PERF-118, F-PERF-119, F-SEC-120.

**Architecture:** Two performance levers (WAL + read pool, batched inserts) plus two correctness levers (project-level mutex, file-size cap) and two usability levers (bounded result sets, early-exit search). The WAL change is the highest-leverage one — it unblocks parallel reads and lets the `MaxOpenConns` move from 1 to a small read pool. The batched-insert change is a SQL-rewrite; the project-level mutex is a per-process `sync.Mutex` keyed by `projectID`. The file-size cap is shared between scanner and search via a new `repo.MaxIndexableFileBytes` config flag.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, `sync`. No new dependencies.

**Source audit:** `docs/14-codebase-improvement-audit-2026-07-14.md`, domain E (items F-PERF-112, F-PERF-113, F-PERF-117, F-PERF-118, F-PERF-119, F-SEC-120).

**Pre-resolved (handled in earlier batches, do not redo):** F-BUG-102, F-POL-130, F-SEC-122, F-SEC-123.

## Global Constraints

- `go build ./...` must succeed after every task.
- `go test ./...` must pass after every task.
- `gofmt -w .` after any file change.
- This batch assumes **E1** and **E2** have merged. E1 adds the `internal/db/sqlutil/` package and the new `tool_calls` columns; E2 wires `Indexing.Ignore` to the scanner. If E1/E2 have not merged yet, port the small helpers (`escapeLike`, the `Config` plumbing) locally and remove the duplicate when the other batches land.
- The WAL change is the only one with user-visible behavior implications: existing on-disk DB files will be transparently upgraded to WAL on next open. The DB still serializes writes (we keep `SetMaxOpenConns(1)` for the writer), but allows concurrent readers.
- Concurrent test discipline: any test that exercises a project-level mutex must release the mutex on test exit (`defer mu.Unlock()` or `t.Cleanup`).
- New exported symbols have doc comments. New error paths return wrapped errors.

---

## File structure

### New files

| Path | Responsibility |
|---|---|
| `internal/db/dbpool.go` | `OpenWithPool(path, maxReaders)` helper that opens the DB with WAL + a small read pool |
| `internal/db/dbpool_test.go` | Tests for the WAL + read pool configuration |
| `internal/db/sqlutil/batch.go` | `BuildValues(n, cols) string` — produces a `(?,?,?),(?,?,?),…` multi-row `VALUES` placeholder for batching |
| `internal/db/sqlutil/batch_test.go` | Tests for the batch placeholder builder |
| `internal/db/locks.go` | `type ProjectLocks struct{ … }; Lock(id); Unlock(id)` — process-local per-project mutex registry |
| `internal/db/locks_test.go` | Tests for `ProjectLocks` |

### Modified files

- `internal/db/db.go` — `Open` becomes a thin wrapper around `OpenWithPool`; `SetMaxOpenConns` becomes 1 for writes + a configurable number of readers (default 4)
- `internal/db/symbols.go` — `SaveSymbols` uses multi-row `VALUES`; `GetSymbols` takes an optional limit (callers pass `repoMapMaxFiles` or `0` for unbounded)
- `internal/db/files.go` — `SaveFileIndex` uses multi-row `VALUES`; `GetFileIndex` takes an optional limit
- `internal/repo/scanner.go` — skip files larger than `MaxIndexableFileBytes` (warn-and-skip, not error)
- `internal/tools/native/repo_index.go` — pass the new size cap to the scanner
- `internal/tools/native/search.go` — `searchFiles` early-exits when the cap is hit; `searchFile` rejects files larger than `MaxIndexableFileBytes`
- `internal/tools/native/repo_map.go` — pass an explicit limit to `GetFileIndex` / `GetSymbols`
- `internal/app/config/config.go` — add `Indexing.MaxFileBytes` and `Indexing.MaxIndexableFileBytes` (default 50 MiB and 25 MiB respectively)

---

# Task 1: F-PERF-119 — Enable SQLite WAL mode and a small read pool

**Files:**
- Modify: `internal/db/db.go:14-43`
- New: `internal/db/dbpool.go`
- New: `internal/db/dbpool_test.go`

- [ ] **Step 1: Add the writer-only / reader-many config**

The standard pattern for SQLite + `database/sql` is: one writer connection (so the busy-timeout actually applies) plus N reader connections. We achieve this by opening two `*sql.DB` instances — one for writes (the existing `db.sqlDB`), and one for reads (`db.readDB`). The current public API is a single `Open(path string) *DB`; extend it to:

```go
type DB struct {
    sqlDB   *sql.DB
    readDB  *sql.DB
    path    string
}

func Open(path string) (*DB, error) {
    return OpenWithPool(path, defaultReadPoolSize)
}

const defaultReadPoolSize = 4

func OpenWithPool(path string, readPoolSize int) (*DB, error) {
    if readPoolSize < 1 {
        readPoolSize = 1
    }
    sqlDB, err := openOneConnection(path)
    if err != nil { return nil, err }
    readDB, err := sql.Open("sqlite", path)
    if err != nil { sqlDB.Close(); return nil, fmt.Errorf("open read pool: %w", err) }
    readDB.SetMaxOpenConns(readPoolSize)
    readDB.SetMaxIdleConns(readPoolSize)
    if _, err := readDB.Exec("PRAGMA busy_timeout = 5000"); err != nil {
        sqlDB.Close(); readDB.Close()
        return nil, fmt.Errorf("set read busy_timeout: %w", err)
    }
    return &DB{sqlDB: sqlDB, readDB: readDB, path: path}, nil
}
```

- [ ] **Step 2: Refactor the per-DB pragmas into a shared helper**

Move the `busy_timeout` / `foreign_keys` / `journal_mode=WAL` setup into `openOneConnection`:
```go
func openOneConnection(path string) (*sql.DB, error) {
    sqlDB, err := sql.Open("sqlite", path)
    if err != nil { return nil, fmt.Errorf("open sqlite db: %w", err) }
    sqlDB.SetMaxOpenConns(1)
    sqlDB.SetMaxIdleConns(1)
    pragmas := []string{
        "PRAGMA busy_timeout = 5000",
        "PRAGMA foreign_keys = ON",
        "PRAGMA journal_mode = WAL",
        "PRAGMA synchronous = NORMAL",
    }
    for _, p := range pragmas {
        if _, err := sqlDB.Exec(p); err != nil {
            sqlDB.Close()
            return nil, fmt.Errorf("%s: %w", p, err)
        }
    }
    if err := sqlDB.Ping(); err != nil { sqlDB.Close(); return nil, fmt.Errorf("ping sqlite: %w", err) }
    return sqlDB, nil
}
```

- [ ] **Step 3: Add a Close that closes both pools**

```go
func (db *DB) Close() error {
    var first error
    if db.sqlDB != nil { if err := db.sqlDB.Close(); err != nil { first = err } }
    if db.readDB != nil { if err := db.readDB.Close(); err != nil && first == nil { first = err } }
    return first
}
```

- [ ] **Step 4: Add a test that verifies WAL is enabled**

```go
func TestOpenEnablesWAL(t *testing.T) {
    dir := t.TempDir()
    db, err := Open(filepath.Join(dir, "wal.db"))
    if err != nil { t.Fatalf("Open: %v", err) }
    defer db.Close()
    if err := db.Migrate(); err != nil { t.Fatalf("Migrate: %v", err) }
    var mode string
    if err := db.sqlDB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
        t.Fatalf("query journal_mode: %v", err)
    }
    if mode != "wal" {
        t.Errorf("expected journal_mode=wal, got %q", mode)
    }
}
```

- [ ] **Step 5: Build & test**

```bash
go build ./...
go test ./internal/db/
```

- [ ] **Step 6: Commit**

```bash
git add internal/db/db.go internal/db/dbpool.go internal/db/dbpool_test.go
git commit -m "db: enable SQLite WAL with writer + read pool (F-PERF-119)"
```

---

# Task 2: F-PERF-112 — Batched multi-row inserts in `SaveSymbols` and `SaveFileIndex`

**Files:**
- New: `internal/db/sqlutil/batch.go`
- New: `internal/db/sqlutil/batch_test.go`
- Modify: `internal/db/symbols.go:22-51`
- Modify: `internal/db/files.go:25-82`

- [ ] **Step 1: Add the `BuildValues` helper**

```go
// BuildValues returns a "(?,?,?),(?,?,?),…" string of len(cols)*n
// placeholders, suitable for a multi-row INSERT INTO ... VALUES ….
func BuildValues(n, cols int) string {
    if n <= 0 || cols <= 0 {
        return ""
    }
    var b strings.Builder
    row := "(" + strings.Repeat("?,", cols)
    row = row[:len(row)-1] + ")"
    for i := 0; i < n; i++ {
        if i > 0 {
            b.WriteByte(',')
        }
        b.WriteString(row)
    }
    return b.String()
}
```

- [ ] **Step 2: Add a unit test**

```go
func TestBuildValues(t *testing.T) {
    cases := []struct {
        n, cols int
        want    string
    }{
        {0, 3, ""},
        {1, 2, "(?,?)"},
        {3, 2, "(?,?),(?,?),(?,?)"},
        {2, 1, "(?),(?)"},
    }
    for _, c := range cases {
        if got := BuildValues(c.n, c.cols); got != c.want {
            t.Errorf("BuildValues(%d,%d)=%q, want %q", c.n, c.cols, got, c.want)
        }
    }
}
```

- [ ] **Step 3: Rewrite `SaveSymbols` to chunk inserts**

```go
const symbolInsertBatch = 200

func (db *DB) SaveSymbols(projectID int64, symbols []Symbol) error {
    tx, err := db.sqlDB.Begin()
    if err != nil { return fmt.Errorf("begin save symbols: %w", err) }
    defer tx.Rollback()
    if _, err := tx.Exec(`DELETE FROM symbols WHERE project_id = ?`, projectID); err != nil {
        return fmt.Errorf("delete existing symbols: %w", err)
    }
    for start := 0; start < len(symbols); start += symbolInsertBatch {
        end := start + symbolInsertBatch
        if end > len(symbols) { end = len(symbols) }
        chunk := symbols[start:end]
        placeholders := sqlutil.BuildValues(len(chunk), 8)
        args := make([]any, 0, len(chunk)*8)
        for _, s := range chunk {
            args = append(args, projectID, s.FilePath, s.Kind, s.Name, s.Receiver, s.Signature, s.LineStart, s.LineEnd)
        }
        if _, err := tx.Exec(`INSERT INTO symbols (project_id, file_path, kind, name, receiver, signature, line_start, line_end) VALUES `+placeholders, args...); err != nil {
            return fmt.Errorf("insert symbols batch [%d:%d]: %w", start, end, err)
        }
    }
    if err := tx.Commit(); err != nil { return fmt.Errorf("commit save symbols: %w", err) }
    return nil
}
```

- [ ] **Step 4: Apply the same pattern to `SaveFileIndex`**

Chunk size `200`. 7 columns: `project_id, path, language, hash, size_bytes, last_indexed_at, summary`. Preserve the existing summary-merge logic (read existing rows inside the transaction; merge in-memory; DELETE; INSERT in chunks).

- [ ] **Step 5: Add a perf-style test**

Insert 1000 synthetic symbols via `SaveSymbols` and time it against the previous per-row loop (if the previous per-row loop is still in git, benchmark it as a baseline). Assert the new code is at least 3× faster on `:memory:`. The exact ratio is hardware-dependent; the assertion exists to detect regressions if the batching is later broken.

- [ ] **Step 6: Build & test**

```bash
go test ./internal/db/ -run 'SaveSymbols|SaveFileIndex'
```

- [ ] **Step 7: Commit**

```bash
git add internal/db/sqlutil/batch.go internal/db/sqlutil/batch_test.go internal/db/symbols.go internal/db/files.go
git commit -m "db: batch SaveSymbols/SaveFileIndex inserts (F-PERF-112)"
```

---

# Task 3: F-PERF-113 — Bound `GetSymbols` and `GetFileIndex` callers

**Files:**
- Modify: `internal/db/symbols.go:55-80` (add `limit` parameter)
- Modify: `internal/db/files.go:85-121` (add `limit` parameter)
- Modify: `internal/tools/native/repo_map.go:26-39` (pass the limit)

- [ ] **Step 1: Add a `limit` parameter to both functions**

```go
// GetFileIndex returns up to `limit` file rows for a project. limit <= 0
// means "all rows" (preserves the previous behaviour for internal callers
// like the file tracker that genuinely want the full set).
func (db *DB) GetFileIndex(projectID int64, limit int) ([]FileIndex, error) { ... }

func (db *DB) GetSymbols(projectID int64, limit int) ([]Symbol, error) { ... }
```

Append `LIMIT ?` (with a single `?` arg) when `limit > 0`.

- [ ] **Step 2: Update existing callers**

- `internal/tools/native/repo_map.go:26,36` — pass `repoMapMaxFiles`
- Any other caller found by `grep -rn 'GetFileIndex\|GetSymbols' internal/`

- [ ] **Step 3: Build & test**

```bash
go build ./...
go test ./internal/db/ ./internal/tools/native/
```

- [ ] **Step 4: Commit**

```bash
git add internal/db/symbols.go internal/db/files.go internal/tools/native/repo_map.go
git commit -m "db: bound GetSymbols/GetFileIndex result sets (F-PERF-113)"
```

---

# Task 4: F-PERF-118 — File-size cap for scanning and searching

**Files:**
- Modify: `internal/app/config/config.go` — add `Indexing.MaxIndexableFileBytes` (default 25 MiB) and `Indexing.MaxSearchableFileBytes` (default 1 MiB)
- Modify: `internal/repo/scanner.go:140-150` — skip files larger than `MaxIndexableFileBytes` (warn-and-skip)
- Modify: `internal/tools/native/repo_index.go:31` — pass the cap
- Modify: `internal/tools/native/search.go:130-150` — reject files larger than `MaxSearchableFileBytes` in `searchFile`

- [ ] **Step 1: Add the config fields**

In `internal/app/config/config.go` `IndexingConfig`:
```go
type IndexingConfig struct {
    ...
    MaxIndexableFileBytes int64 `toml:"max_indexable_file_bytes"`
    MaxSearchableFileBytes int64 `toml:"max_searchable_file_bytes"`
}
```
Default values: 25 MiB / 1 MiB.

- [ ] **Step 2: Plumb the cap through the scanner**

Add `MaxIndexableFileBytes int64` to `repo.Config`. In `Scan`, before hashing:
```go
if info, err := entry.Info(); err == nil && info.Size() > s.config.MaxIndexableFileBytes {
    s.skipped = append(s.skipped, skippedEntry{Path: rel, Reason: fmt.Sprintf("file too large (%d bytes)", info.Size())})
    return nil
}
```

- [ ] **Step 3: Plumb the cap through `searchFile`**

In `internal/tools/native/search.go`:
```go
if info, err := os.Stat(path); err == nil && info.Size() > t.maxSearchableFileBytes {
    return nil
}
```

- [ ] **Step 4: Add a test**

```go
func TestScannerSkipsOversizedFiles(t *testing.T) {
    dir := t.TempDir()
    big := filepath.Join(dir, "huge.bin")
    if err := os.WriteFile(big, make([]byte, 1024), 0o644); err != nil { t.Fatal(err) }
    s := NewScanner(Config{Root: dir, MaxIndexableFileBytes: 512, SkipGitignore: true})
    files, err := s.Scan()
    if err != nil { t.Fatalf("Scan: %v", err) }
    if len(files) != 0 { t.Errorf("expected 0 files, got %d", len(files)) }
    if len(s.Skipped()) == 0 { t.Errorf("expected a skip entry for the oversized file") }
}
```

- [ ] **Step 5: Build & test**

```bash
go test ./internal/repo/ ./internal/tools/native/
```

- [ ] **Step 6: Commit**

```bash
git add internal/repo/scanner.go internal/tools/native/repo_index.go internal/tools/native/search.go internal/app/config/config.go internal/repo/scanner_test.go
git commit -m "repo: cap file size for scanning and searching (F-PERF-118)"
```

---

# Task 5: F-PERF-117 — `searchFiles` early-exits on cap

**Files:**
- Modify: `internal/tools/native/search.go:77-128`

- [ ] **Step 1: Move the cap check into the walk callback**

Currently the function walks the entire tree into `files` and then iterates `files` to search. The fix is to short-circuit once `len(matches) >= limit` and to stop materializing the file list when the cap is already hit.

```go
func (t *toolSet) searchFiles(ctx context.Context, start, query string, limit int) (matches []string, capped bool, walkErrs []error, err error) {
    err = filepath.WalkDir(start, func(path string, entry fs.DirEntry, walkErr error) error {
        if walkErr != nil { walkErrs = append(walkErrs, fmt.Errorf("%s: %w", path, walkErr)); ... }
        if err := ctx.Err(); err != nil { return err }
        if len(matches) >= limit { return filepath.SkipAll } // stop the walk
        // ... existing checks ...
        if entry.Type().IsRegular() {
            fileMatches := t.searchFile(path, query, limit-len(matches))
            matches = append(matches, fileMatches...)
            if len(matches) >= limit { capped = true }
        }
        return nil
    })
    if errors.Is(err, filepath.SkipAll) { err = nil }
    return
}
```

- [ ] **Step 2: Remove the now-dead `files` slice and the post-walk sort + iterate**

- [ ] **Step 3: Build & test**

```bash
go test ./internal/tools/native/ -run Search
```

- [ ] **Step 4: Commit**

```bash
git add internal/tools/native/search.go
git commit -m "native: searchFiles early-exits once cap is reached (F-PERF-117)"
```

---

# Task 6: F-SEC-120 — Per-project mutex around `SaveFileIndex` / `SaveSymbols`

**Files:**
- New: `internal/db/locks.go`
- New: `internal/db/locks_test.go`
- Modify: `internal/db/files.go:25-82` — acquire `db.locks.Lock(projectID)` at the top of `SaveFileIndex`
- Modify: `internal/db/symbols.go:22-51` — same for `SaveSymbols`
- Modify: `internal/db/db.go` — add `locks *ProjectLocks` to `DB`; initialize in `Open` and `OpenWithPool`

- [ ] **Step 1: Add the lock registry**

```go
type ProjectLocks struct {
    mu sync.Mutex
    locks map[int64]*sync.Mutex
}

func NewProjectLocks() *ProjectLocks { return &ProjectLocks{locks: map[int64]*sync.Mutex{}} }

func (p *ProjectLocks) Lock(id int64) func() {
    p.mu.Lock()
    m, ok := p.locks[id]
    if !ok { m = &sync.Mutex{}; p.locks[id] = m }
    p.mu.Unlock()
    m.Lock()
    return m.Unlock
}
```

- [ ] **Step 2: Use it in `SaveFileIndex` / `SaveSymbols`**

```go
func (db *DB) SaveFileIndex(projectID int64, files []FileIndex) error {
    unlock := db.locks.Lock(projectID)
    defer unlock()
    ...
}
```

- [ ] **Step 3: Add a concurrency test**

```go
func TestProjectLocksAreExclusive(t *testing.T) {
    locks := NewProjectLocks()
    unlockA := locks.Lock(1)
    defer unlockA()
    done := make(chan struct{})
    go func() {
        unlockB := locks.Lock(1)
        defer unlockB()
        close(done)
    }()
    select {
    case <-done:
        t.Fatal("second Lock(1) returned while first was held")
    case <-time.After(50 * time.Millisecond):
        // expected: blocked
    }
    unlockA()
    select {
    case <-done:
    case <-time.After(time.Second):
        t.Fatal("second Lock(1) did not return after first unlock")
    }
}
```

- [ ] **Step 4: Add a SaveFileIndex concurrency test**

```go
func TestSaveFileIndexSerialisesByProject(t *testing.T) {
    db, projectID := openMetricsTestDB(t)
    var wg sync.WaitGroup
    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()
            if err := db.SaveFileIndex(projectID, []FileIndex{{Path: fmt.Sprintf("f%d.go", i), Language: "go", Hash: "h", SizeBytes: 1, LastIndexedAt: time.Now().UTC()}}); err != nil {
                t.Errorf("save: %v", err)
            }
        }(i)
    }
    wg.Wait()
    files, err := db.GetFileIndex(projectID, 0)
    if err != nil { t.Fatalf("get: %v", err) }
    if len(files) != 1 {
        t.Errorf("expected exactly 1 file (last writer wins), got %d", len(files))
    }
}
```

- [ ] **Step 5: Build & test**

```bash
go test ./internal/db/ -race
```

- [ ] **Step 6: Commit**

```bash
git add internal/db/locks.go internal/db/locks_test.go internal/db/files.go internal/db/symbols.go internal/db/db.go
git commit -m "db: per-project mutex around SaveFileIndex/SaveSymbols (F-SEC-120)"
```

---

# Task 7: Full sweep

- [ ] **Step 1: Run the entire test suite, with the race detector**

```bash
go test ./... -race
```

- [ ] **Step 2: Run the benchmark suite to confirm no regressions**

```bash
go test ./internal/db/ -bench=. -benchtime=2x
```

- [ ] **Step 3: Update the audit doc**

Edit `docs/14-codebase-improvement-audit-2026-07-14.md` and add a new "Resolution status" subsection at the bottom of the file:

```markdown
### Batch 6 (E3 — DB & search performance / scaling / isolation): RESOLVED on branch `feature/domain-e3-db-perf`
| Finding | Status | Notes |
|---|---|---|
| F-PERF-119 | RESOLVED | SQLite WAL + small read pool; existing on-disk DBs upgraded transparently |
| F-PERF-112 | RESOLVED | SaveSymbols / SaveFileIndex use multi-row VALUES batches of 200 |
| F-PERF-113 | RESOLVED | GetSymbols / GetFileIndex take an optional limit; repo.map passes repoMapMaxFiles |
| F-PERF-118 | RESOLVED | Indexing.MaxIndexableFileBytes / MaxSearchableFileBytes caps added |
| F-PERF-117 | RESOLVED | searchFiles short-circuits the walk when the cap is reached |
| F-SEC-120 | RESOLVED | Process-local per-project mutex around SaveFileIndex / SaveSymbols |
```

- [ ] **Step 4: Commit the audit update**

```bash
git add docs/14-codebase-improvement-audit-2026-07-14.md
git commit -m "docs: mark domain-E3 findings resolved"
```

- [ ] **Step 5: Push / open PR (only if requested)**
