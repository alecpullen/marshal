# Milestone N: Knowledge Agent v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Marshal a persistent project memory: at session end, a cheap "knowledge" model call summarizes the session, extracts durable project memories, and summarizes session-touched files; the memories are browsable/manually-curatable in the TUI and feed back into future context packs.

**Architecture:** A new `internal/knowledge` package owns a one-shot, best-effort `EndSession` pass wired into `internal/app/app.go`'s shutdown path. It writes to three new/extended DB surfaces (`memories` table, `files.summary` column, `agent_sessions.ended_at`/`summary`), and its output is read back by `internal/agent.Runner` (via `contextpack.MergeMemories`) and a new `internal/app/tui/memory` browser overlay.

**Tech Stack:** Go, `modernc.org/sqlite`, Bubble Tea (`charmbracelet/bubbletea`), the existing provider/schema/routing abstractions in `internal/llm`.

## Global Constraints

- Full design spec: `docs/superpowers/specs/2026-07-03-milestone-n-knowledge-agent-design.md`. Read it if any task below is ambiguous.
- `internal/knowledge` must never import `internal/agent`, and `internal/agent` must never import `internal/knowledge` — this would create an import cycle. Shared shapes (`RouteResolver`, `MemoryNote`) are declared independently in each package (or, for `MemoryNote`, in `internal/contextpack`, which both already depend on). Do not "clean this up" by importing across those two packages.
- The memory browser's TUI keybinding is **`Ctrl+K`**, not `Ctrl+M` — `Ctrl+M` is byte-identical to Enter in raw terminal input (bubbletea's `KeyCtrlM` and `KeyEnter` share the same `KeyType` value) and would break chat submission.
- All new `_test.go` files follow this codebase's existing style: `db.Open(":memory:")` + `db.Migrate()` + `db.GetOrCreateProject(...)` for DB tests; `session.New(config.Default(), dir, now, session.Persistence{})` for session-backed tests; table-free `t.Fatalf` assertions (no assertion library).
- Every task ends with `go build ./cmd/marshal` and `go test ./...` passing, and a commit.

---

### Task 1: `memories` table and CRUD

**Files:**
- Modify: `internal/db/migrations.go`
- Create: `internal/db/memories.go`
- Create: `internal/db/memories_test.go`

**Interfaces:**
- Produces: `db.Memory{ID int64, Kind, Content, Confidence, SourceSessionID string, CreatedAt, UpdatedAt time.Time}`; `db.MemoryConfidenceTentative/Confirmed/Stale` string constants; `(*db.DB) SaveMemory(projectID int64, kind, content, sourceSessionID string, now time.Time) error`; `(*db.DB) GetMemories(projectID int64) ([]Memory, error)`; `(*db.DB) SetMemoryConfidence(id int64, confidence string, now time.Time) error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/db/memories_test.go`:

```go
package db

import (
	"testing"
	"time"
)

func TestSaveAndGetMemories(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	now := time.Unix(100, 0).UTC()
	if err := db.SaveMemory(projectID, "fact", "Uses SQLite for persistence", "sess-1", now); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	if err := db.SaveMemory(projectID, "architecture", "TUI built with Bubble Tea", "sess-1", now.Add(time.Second)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}

	memories, err := db.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	if len(memories) != 2 {
		t.Fatalf("len(memories) = %d, want 2: %#v", len(memories), memories)
	}
	if memories[0].Kind != "fact" || memories[0].Content != "Uses SQLite for persistence" {
		t.Fatalf("memories[0] = %#v", memories[0])
	}
	if memories[0].Confidence != MemoryConfidenceTentative {
		t.Fatalf("memories[0].Confidence = %q, want %q", memories[0].Confidence, MemoryConfidenceTentative)
	}
	if memories[0].SourceSessionID != "sess-1" {
		t.Fatalf("memories[0].SourceSessionID = %q, want %q", memories[0].SourceSessionID, "sess-1")
	}
	if !memories[0].CreatedAt.Equal(now) || !memories[0].UpdatedAt.Equal(now) {
		t.Fatalf("memories[0] timestamps = %#v, want created=updated=%s", memories[0], now)
	}
	if memories[1].Kind != "architecture" {
		t.Fatalf("memories[1].Kind = %q, want %q", memories[1].Kind, "architecture")
	}
}

func TestGetMemoriesEmptyProject(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	memories, err := db.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("expected 0 memories, got %d", len(memories))
	}
}

func TestSetMemoryConfidenceTransitions(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	now := time.Unix(100, 0).UTC()
	if err := db.SaveMemory(projectID, "fact", "content", "sess-1", now); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	memories, err := db.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	id := memories[0].ID

	later := now.Add(time.Hour)
	if err := db.SetMemoryConfidence(id, MemoryConfidenceStale, later); err != nil {
		t.Fatalf("SetMemoryConfidence failed: %v", err)
	}

	memories, err = db.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	if memories[0].Confidence != MemoryConfidenceStale {
		t.Fatalf("Confidence = %q, want %q", memories[0].Confidence, MemoryConfidenceStale)
	}
	if !memories[0].UpdatedAt.Equal(later) {
		t.Fatalf("UpdatedAt = %s, want %s", memories[0].UpdatedAt, later)
	}
	if !memories[0].CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt changed: %s, want unchanged %s", memories[0].CreatedAt, now)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/db/... -run TestSaveAndGetMemories -v`
Expected: FAIL (`db.Memory` / `db.SaveMemory` undefined, or `no such table: memories`)

- [ ] **Step 3: Add the `memories` table to the schema**

In `internal/db/migrations.go`, add to the `schema` string (after the `symbols` table's index, before the closing backtick):

```sql

CREATE TABLE IF NOT EXISTS memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    content TEXT NOT NULL,
    confidence TEXT NOT NULL,
    source_session_id TEXT REFERENCES agent_sessions(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project_id);
```

- [ ] **Step 4: Implement `internal/db/memories.go`**

```go
package db

import (
	"database/sql"
	"fmt"
	"time"
)

const (
	MemoryConfidenceTentative = "tentative"
	MemoryConfidenceConfirmed = "confirmed"
	MemoryConfidenceStale     = "stale"
)

type Memory struct {
	ID              int64
	Kind            string // "fact", "architecture", "decision"
	Content         string
	Confidence      string // "tentative", "confirmed", "stale"
	SourceSessionID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SaveMemory inserts a new memory row with confidence "tentative".
func (db *DB) SaveMemory(projectID int64, kind, content, sourceSessionID string, now time.Time) error {
	nowStr := now.UTC().Format(time.RFC3339)
	_, err := db.exec(
		`INSERT INTO memories (project_id, kind, content, confidence, source_session_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectID, kind, content, MemoryConfidenceTentative, sourceSessionID, nowStr, nowStr,
	)
	if err != nil {
		return fmt.Errorf("save memory: %w", err)
	}
	return nil
}

// GetMemories returns all memory rows for a project, ordered by id.
func (db *DB) GetMemories(projectID int64) ([]Memory, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, kind, content, confidence, source_session_id, created_at, updated_at
		 FROM memories
		 WHERE project_id = ?
		 ORDER BY id`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		var sourceSessionID sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&m.ID, &m.Kind, &m.Content, &m.Confidence, &sourceSessionID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan memory row: %w", err)
		}
		if sourceSessionID.Valid {
			m.SourceSessionID = sourceSessionID.String
		}
		parsedCreated, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		m.CreatedAt = parsedCreated.UTC()
		parsedUpdated, err := time.Parse(time.RFC3339, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}
		m.UpdatedAt = parsedUpdated.UTC()
		memories = append(memories, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory rows: %w", err)
	}
	return memories, nil
}

// SetMemoryConfidence updates a single memory's confidence state and
// updated_at timestamp.
func (db *DB) SetMemoryConfidence(id int64, confidence string, now time.Time) error {
	_, err := db.exec(
		`UPDATE memories SET confidence = ?, updated_at = ? WHERE id = ?`,
		confidence, now.UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		return fmt.Errorf("set memory confidence: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/db/... -run "TestSaveAndGetMemories|TestGetMemoriesEmptyProject|TestSetMemoryConfidenceTransitions" -v`
Expected: PASS

- [ ] **Step 6: Run the full package test suite**

Run: `go test ./internal/db/... -v`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add internal/db/migrations.go internal/db/memories.go internal/db/memories_test.go
git commit -m "feat(db): add memories table and CRUD for Milestone N"
```

---

### Task 2: `files.summary` column and carry-forward logic

**Files:**
- Modify: `internal/db/migrations.go`
- Modify: `internal/db/db.go`
- Modify: `internal/db/files.go`
- Modify: `internal/db/files_test.go`

**Interfaces:**
- Consumes: `db.tableColumns(table string) (map[string]bool, error)` (existing, `internal/db/db.go`).
- Produces: `db.FileIndex.Summary string` (new field); `(*db.DB) SaveFileIndex` (behavior change: preserves `Summary` when path+hash unchanged); `(*db.DB) UpdateFileSummary(projectID int64, path, summary string) error`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/db/files_test.go`:

```go
func TestSaveFileIndexPreservesSummaryWhenHashUnchanged(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	files := []FileIndex{
		{Path: "main.go", Hash: "v1", SizeBytes: 1, LastIndexedAt: time.Now().UTC()},
	}
	if err := db.SaveFileIndex(projectID, files); err != nil {
		t.Fatalf("SaveFileIndex failed: %v", err)
	}
	if err := db.UpdateFileSummary(projectID, "main.go", "Entry point"); err != nil {
		t.Fatalf("UpdateFileSummary failed: %v", err)
	}

	if err := db.SaveFileIndex(projectID, files); err != nil {
		t.Fatalf("second SaveFileIndex failed: %v", err)
	}

	got, err := db.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "Entry point" {
		t.Fatalf("expected summary preserved, got %#v", got)
	}
}

func TestSaveFileIndexClearsSummaryWhenHashChanges(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	if err := db.SaveFileIndex(projectID, []FileIndex{
		{Path: "main.go", Hash: "v1", SizeBytes: 1, LastIndexedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("SaveFileIndex failed: %v", err)
	}
	if err := db.UpdateFileSummary(projectID, "main.go", "Entry point"); err != nil {
		t.Fatalf("UpdateFileSummary failed: %v", err)
	}

	if err := db.SaveFileIndex(projectID, []FileIndex{
		{Path: "main.go", Hash: "v2", SizeBytes: 2, LastIndexedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("second SaveFileIndex failed: %v", err)
	}

	got, err := db.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "" {
		t.Fatalf("expected summary cleared after hash change, got %#v", got)
	}
}

func TestUpdateFileSummaryNoOpForMissingPath(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	if err := db.UpdateFileSummary(projectID, "does-not-exist.go", "should not error"); err != nil {
		t.Fatalf("UpdateFileSummary returned error for missing path: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/db/... -run "TestSaveFileIndexPreservesSummary|TestSaveFileIndexClearsSummary|TestUpdateFileSummaryNoOp" -v`
Expected: FAIL (`UpdateFileSummary` undefined, or `no such column: summary`)

- [ ] **Step 3: Add the `files.summary` column via backward-compatible migration**

In `internal/db/db.go`, after the existing `tool_calls` column block in `Migrate()` (after the `for name, def := range columnDefs { ... }` loop, before `return nil`), add:

```go
	fileColumns, err := db.tableColumns("files")
	if err != nil {
		return fmt.Errorf("inspect files columns: %w", err)
	}
	if !fileColumns["summary"] {
		if _, err := db.sqlDB.Exec(`ALTER TABLE files ADD COLUMN summary TEXT`); err != nil {
			return fmt.Errorf("add column summary to files: %w", err)
		}
	}
```

- [ ] **Step 4: Update `internal/db/files.go`**

Replace the entire file with:

```go
package db

import (
	"database/sql"
	"fmt"
	"time"
)

type FileIndex struct {
	Path          string
	Language      string
	Hash          string
	SizeBytes     int64
	LastIndexedAt time.Time
	Summary       string
}

// SaveFileIndex replaces the file index for a project. It deletes all
// existing files for the project and inserts the provided rows, preserving
// each file's stored Summary when its path+hash is unchanged from the
// existing index. A changed hash (or a new path) means the old summary no
// longer describes the file's current content, so it is dropped; the
// knowledge pass (internal/knowledge) fills it back in later via
// UpdateFileSummary.
func (db *DB) SaveFileIndex(projectID int64, files []FileIndex) error {
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin save file index transaction: %w", err)
	}
	defer tx.Rollback()

	existing := map[string]FileIndex{}
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
		f := FileIndex{Path: path, Hash: hash}
		if summary.Valid {
			f.Summary = summary.String
		}
		existing[path] = f
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate existing file rows: %w", err)
	}
	rows.Close()

	if _, err := tx.Exec(`DELETE FROM files WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("delete existing files: %w", err)
	}

	stmt, err := tx.Prepare(`INSERT INTO files (project_id, path, language, hash, size_bytes, last_indexed_at, summary)
							 VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare file insert: %w", err)
	}
	defer stmt.Close()

	for _, f := range files {
		summary := f.Summary
		if prior, ok := existing[f.Path]; ok && prior.Hash == f.Hash {
			summary = prior.Summary
		}
		_, err := stmt.Exec(projectID, f.Path, f.Language, f.Hash, f.SizeBytes, f.LastIndexedAt.UTC().Format(time.RFC3339), summary)
		if err != nil {
			return fmt.Errorf("insert file %s: %w", f.Path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit save file index: %w", err)
	}
	return nil
}

// GetFileIndex returns all file rows for a project, ordered by path.
func (db *DB) GetFileIndex(projectID int64) ([]FileIndex, error) {
	rows, err := db.sqlDB.Query(
		`SELECT path, language, hash, size_bytes, last_indexed_at, summary
		 FROM files
		 WHERE project_id = ?
		 ORDER BY path`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("query file index: %w", err)
	}
	defer rows.Close()

	var files []FileIndex
	for rows.Next() {
		var f FileIndex
		var lastIndexed string
		var summary sql.NullString
		if err := rows.Scan(&f.Path, &f.Language, &f.Hash, &f.SizeBytes, &lastIndexed, &summary); err != nil {
			return nil, fmt.Errorf("scan file row: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, lastIndexed)
		if err != nil {
			return nil, fmt.Errorf("parse last_indexed_at: %w", err)
		}
		f.LastIndexedAt = parsed.UTC()
		if summary.Valid {
			f.Summary = summary.String
		}
		files = append(files, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file rows: %w", err)
	}
	return files, nil
}

// UpdateFileSummary sets Summary for a single existing file row, without
// touching hash/language/size/last_indexed_at. It is a no-op (not an error)
// if no row matches project_id+path.
func (db *DB) UpdateFileSummary(projectID int64, path, summary string) error {
	_, err := db.exec(
		`UPDATE files SET summary = ? WHERE project_id = ? AND path = ?`,
		summary, projectID, path,
	)
	if err != nil {
		return fmt.Errorf("update file summary: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/db/... -run "TestSaveFileIndexPreservesSummary|TestSaveFileIndexClearsSummary|TestUpdateFileSummaryNoOp|TestSaveAndGetFileIndex|TestSaveFileIndexUpdatesExisting" -v`
Expected: PASS

- [ ] **Step 6: Run the full package test suite**

Run: `go test ./internal/db/... -v`
Expected: all PASS

- [ ] **Step 7: Run the full build to confirm no downstream breakage**

Run: `go build ./...`
Expected: succeeds (no other package constructs `db.FileIndex` positionally, so the new field is additive)

- [ ] **Step 8: Commit**

```bash
git add internal/db/migrations.go internal/db/db.go internal/db/files.go internal/db/files_test.go
git commit -m "feat(db): add files.summary column with hash-based carry-forward"
```

---

### Task 3: Session-end persistence (`EndSession` / `GetSession`)

**Files:**
- Modify: `internal/db/sessions.go`
- Modify: `internal/db/sessions_test.go`

**Interfaces:**
- Produces: `db.Session{ID string, ProjectID int64, Title string, StartedAt time.Time, EndedAt *time.Time, Summary string}`; `(*db.DB) GetSession(sessionID string) (Session, error)`; `(*db.DB) EndSession(sessionID string, endedAt time.Time, summary string) error`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/db/sessions_test.go`:

```go
func TestEndSessionSetsEndedAtAndSummary(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	sessionID := "sess-end"
	startedAt := time.Unix(100, 0).UTC()
	if err := db.CreateSession(sessionID, projectID, "", startedAt); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	endedAt := time.Unix(200, 0).UTC()
	if err := db.EndSession(sessionID, endedAt, "Fixed the login bug."); err != nil {
		t.Fatalf("EndSession failed: %v", err)
	}

	got, err := db.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.Summary != "Fixed the login bug." {
		t.Fatalf("Summary = %q, want %q", got.Summary, "Fixed the login bug.")
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(endedAt) {
		t.Fatalf("EndedAt = %v, want %v", got.EndedAt, endedAt)
	}
	if got.ProjectID != projectID {
		t.Fatalf("ProjectID = %d, want %d", got.ProjectID, projectID)
	}
}

func TestGetSessionBeforeEndHasNilEndedAt(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	sessionID := "sess-open"
	if err := db.CreateSession(sessionID, projectID, "", time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	got, err := db.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt != nil {
		t.Fatalf("EndedAt = %v, want nil", got.EndedAt)
	}
	if got.Summary != "" {
		t.Fatalf("Summary = %q, want empty", got.Summary)
	}
}

func TestGetSessionNotFound(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	_, err = db.GetSession("does-not-exist")
	if err == nil {
		t.Fatal("expected an error for a missing session")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/db/... -run "TestEndSessionSetsEndedAtAndSummary|TestGetSessionBeforeEndHasNilEndedAt|TestGetSessionNotFound" -v`
Expected: FAIL (`db.EndSession` / `db.GetSession` undefined)

- [ ] **Step 3: Implement `GetSession` and `EndSession`**

Replace `internal/db/sessions.go` with:

```go
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Message struct {
	ID        int64
	Role      string
	Content   string
	CreatedAt time.Time
}

type Session struct {
	ID        string
	ProjectID int64
	Title     string
	StartedAt time.Time
	EndedAt   *time.Time
	Summary   string
}

// CreateSession inserts a new agent_sessions row. The session id is generated
// by the caller (session.State) and is the primary key.
func (db *DB) CreateSession(sessionID string, projectID int64, title string, startedAt time.Time) error {
	_, err := db.exec(
		`INSERT INTO agent_sessions (id, project_id, title, started_at)
		 VALUES (?, ?, ?, ?)`,
		sessionID, projectID, title, startedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSession returns the session row for the given ID.
func (db *DB) GetSession(sessionID string) (Session, error) {
	var s Session
	var startedAt string
	var endedAt, summary sql.NullString
	row := db.queryRow(
		`SELECT id, project_id, title, started_at, ended_at, summary FROM agent_sessions WHERE id = ?`,
		sessionID,
	)
	if err := row.Scan(&s.ID, &s.ProjectID, &s.Title, &startedAt, &endedAt, &summary); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, fmt.Errorf("session not found: %s", sessionID)
		}
		return Session{}, fmt.Errorf("load session: %w", err)
	}
	parsedStarted, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return Session{}, fmt.Errorf("parse started_at: %w", err)
	}
	s.StartedAt = parsedStarted.UTC()
	if endedAt.Valid {
		parsedEnded, err := time.Parse(time.RFC3339, endedAt.String)
		if err != nil {
			return Session{}, fmt.Errorf("parse ended_at: %w", err)
		}
		parsedEnded = parsedEnded.UTC()
		s.EndedAt = &parsedEnded
	}
	if summary.Valid {
		s.Summary = summary.String
	}
	return s, nil
}

// EndSession sets ended_at and summary on an existing session row.
func (db *DB) EndSession(sessionID string, endedAt time.Time, summary string) error {
	_, err := db.exec(
		`UPDATE agent_sessions SET ended_at = ?, summary = ? WHERE id = ?`,
		endedAt.UTC().Format(time.RFC3339), summary, sessionID,
	)
	if err != nil {
		return fmt.Errorf("end session: %w", err)
	}
	return nil
}

// SaveMessage appends a message to the session transcript.
func (db *DB) SaveMessage(sessionID string, role string, content string, createdAt time.Time) error {
	_, err := db.exec(
		`INSERT INTO messages (session_id, role, content, created_at)
		 VALUES (?, ?, ?, ?)`,
		sessionID, role, content, createdAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("save message: %w", err)
	}
	return nil
}

// GetMessages returns all messages for a session in chronological order.
func (db *DB) GetMessages(sessionID string) ([]Message, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, role, content, created_at
		 FROM messages
		 WHERE session_id = ?
		 ORDER BY id ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		var created string
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &created); err != nil {
			return nil, fmt.Errorf("scan message row: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, created)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		m.CreatedAt = parsed.UTC()
		messages = append(messages, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message rows: %w", err)
	}
	return messages, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/db/... -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/db/sessions.go internal/db/sessions_test.go
git commit -m "feat(db): add GetSession and EndSession for Milestone N"
```

---

### Task 4: Route the "knowledge" task class to `RoleKnowledge`

**Files:**
- Modify: `internal/llm/routing/router.go`
- Modify: `internal/llm/routing/router_test.go`

**Interfaces:**
- Consumes: `routing.RoleKnowledge` (existing constant, `internal/llm/routing/types.go:7`, currently unused by the router).
- Produces: `roleForTaskClass("knowledge") == RoleKnowledge`; `Resolve(TaskProfile{Class: "knowledge"})` falls back to `RoleImplementer` when `RoleKnowledge` is not configured for the active profile (via the existing generic fallback in `StaticRouter.Resolve`, unchanged).

- [ ] **Step 1: Write the failing tests**

Add to `internal/llm/routing/router_test.go`:

```go
func TestResolveKnowledgeUsesKnowledgeRoleWhenConfigured(t *testing.T) {
	router := NewStaticRouter(Config{
		DefaultProfile: "local_balanced",
		RemoteAllowed:  false,
		Presets: map[string]ModelPreset{
			"tiny": {Name: "tiny", Provider: "ollama", Model: "qwen2.5:0.5b", LocalOnly: true},
		},
		Profiles: map[string]AgentProfile{
			"local_balanced": {
				Name: "local_balanced",
				Roles: map[AgentRole]string{
					RoleKnowledge: "tiny",
				},
			},
		},
	})

	route, err := router.Resolve(TaskProfile{Class: "knowledge"})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if route.Role != RoleKnowledge {
		t.Fatalf("Role = %q, want %q", route.Role, RoleKnowledge)
	}
	if route.Preset.Name != "tiny" {
		t.Fatalf("Preset = %#v, want tiny", route.Preset)
	}
}

func TestResolveKnowledgeFallsBackToImplementerWhenNotConfigured(t *testing.T) {
	route, err := testRouter().Resolve(TaskProfile{Class: "knowledge"})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if route.Role != RoleImplementer {
		t.Fatalf("Role = %q, want %q (fallback)", route.Role, RoleImplementer)
	}
	if route.Preset.Name != "coder" {
		t.Fatalf("Preset = %#v, want coder", route.Preset)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/routing/... -run "TestResolveKnowledge" -v`
Expected: FAIL (`route.Role` will be `RoleImplementer` for the first test since `roleForTaskClass` doesn't yet map `"knowledge"`, so `route.Preset.Name` will be empty and the test fails)

- [ ] **Step 3: Add the class mapping**

In `internal/llm/routing/router.go`, change `roleForTaskClass`:

```go
func roleForTaskClass(class string) AgentRole {
	switch class {
	case "question":
		return RoleRepoScout
	case "knowledge":
		return RoleKnowledge
	case "edit", "command":
		return RoleImplementer
	default:
		return RoleImplementer
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/llm/routing/... -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/llm/routing/router.go internal/llm/routing/router_test.go
git commit -m "feat(routing): map the knowledge task class to RoleKnowledge"
```

---

### Task 5: Context pack `memory` section and `MergeMemories`

**Files:**
- Modify: `internal/contextpack/contextpack.go`
- Modify: `internal/contextpack/builder.go`
- Modify: `internal/contextpack/contextpack_test.go`

**Interfaces:**
- Produces: `contextpack.SectionMemory SectionKind = "memory"`; `contextpack.MemoryNote{Kind, Content string}`; `contextpack.MergeMemories(pack Pack, memories []MemoryNote, maxTokens int, now func() time.Time) Pack`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/contextpack/contextpack_test.go`:

```go
func TestMergeMemoriesInsertsBeforePlanAndSnippets(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: SectionPlan, Title: "Current Plan", Content: "1. Inspect", EstimatedTokens: 3},
			{Kind: SectionFileSnippet, Title: "internal/app/app.go", Content: "package app", EstimatedTokens: 3},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 10},
	}

	memories := []MemoryNote{
		{Kind: "fact", Content: "Uses SQLite for persistence"},
		{Kind: "architecture", Content: "TUI built with Bubble Tea"},
	}

	updated := MergeMemories(pack, memories, 12000, func() time.Time { return now })

	if len(updated.Sections) != 4 {
		t.Fatalf("len(updated.Sections) = %d, want 4: %#v", len(updated.Sections), updated.Sections)
	}
	wantKinds := []SectionKind{SectionRepoCard, SectionMemory, SectionPlan, SectionFileSnippet}
	for i, want := range wantKinds {
		if updated.Sections[i].Kind != want {
			t.Fatalf("section %d kind = %q, want %q", i, updated.Sections[i].Kind, want)
		}
	}
	if !strings.Contains(updated.Sections[1].Content, "Uses SQLite for persistence") {
		t.Fatalf("memory section missing content: %q", updated.Sections[1].Content)
	}
	if !strings.Contains(updated.Sections[1].Content, "TUI built with Bubble Tea") {
		t.Fatalf("memory section missing content: %q", updated.Sections[1].Content)
	}
	if updated.GeneratedAt != now {
		t.Fatalf("GeneratedAt = %s, want %s", updated.GeneratedAt, now)
	}
}

func TestMergeMemoriesReplacesExistingMemorySection(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: SectionMemory, Title: "Project Memories", Content: "[fact] stale note", EstimatedTokens: 3},
			{Kind: SectionPlan, Content: "1. Inspect", EstimatedTokens: 3},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 10},
	}

	updated := MergeMemories(pack, []MemoryNote{{Kind: "fact", Content: "fresh note"}}, 12000, func() time.Time { return time.Unix(300, 0).UTC() })

	if len(updated.Sections) != 3 {
		t.Fatalf("len(updated.Sections) = %d, want 3: %#v", len(updated.Sections), updated.Sections)
	}
	if updated.Sections[1].Kind != SectionMemory || strings.Contains(updated.Sections[1].Content, "stale note") {
		t.Fatalf("memory section not replaced: %#v", updated.Sections[1])
	}
	if !strings.Contains(updated.Sections[1].Content, "fresh note") {
		t.Fatalf("memory section missing new content: %q", updated.Sections[1].Content)
	}
}

func TestMergeMemoriesEmptyIsNoOp(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Content: "Project: marshal", EstimatedTokens: 4},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	}

	updated := MergeMemories(pack, nil, 12000, func() time.Time { return time.Unix(300, 0).UTC() })

	if len(updated.Sections) != 1 || updated.Sections[0].Kind != SectionRepoCard {
		t.Fatalf("expected pack unchanged, got %#v", updated.Sections)
	}
}

func TestMergeMemoriesRespectsMaxTokensAndMarksTruncated(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Content: strings.Repeat("r", 8), EstimatedTokens: 2},
		},
		TokenUsage: TokenUsage{MaxTokens: 8, EstimatedTokens: 2},
	}

	updated := MergeMemories(pack, []MemoryNote{{Kind: "fact", Content: strings.Repeat("m", 64)}}, 8, func() time.Time { return time.Unix(300, 0).UTC() })

	if updated.TokenUsage.EstimatedTokens > updated.TokenUsage.MaxTokens {
		t.Fatalf("estimated tokens %d exceeds max %d", updated.TokenUsage.EstimatedTokens, updated.TokenUsage.MaxTokens)
	}
	if !updated.TokenUsage.Truncated {
		t.Fatal("TokenUsage.Truncated = false, want true")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/contextpack/... -run TestMergeMemories -v`
Expected: FAIL (`SectionMemory` / `MemoryNote` / `MergeMemories` undefined)

- [ ] **Step 3: Add `SectionMemory` and `MemoryNote`**

In `internal/contextpack/contextpack.go`, change the `SectionKind` const block and add the new type:

```go
const (
	SectionRepoCard    SectionKind = "repo_card"
	SectionMemory      SectionKind = "memory"
	SectionPlan        SectionKind = "plan"
	SectionFileSnippet SectionKind = "file_snippet"
	SectionToolOutput  SectionKind = "tool_output"
)
```

Add after the `ToolOutput` struct:

```go
// MemoryNote is contextpack's own view of a durable memory — just enough
// to render a section. It is declared here (not imported from
// internal/knowledge) so that internal/agent, which already depends on
// contextpack, never needs to depend on internal/knowledge (the two
// packages must not import each other — see the Milestone N design doc).
type MemoryNote struct {
	Kind    string
	Content string
}
```

- [ ] **Step 4: Implement `MergeMemories` in `internal/contextpack/builder.go`**

Add:

```go
func newMemorySection(memories []MemoryNote) (Section, bool) {
	var lines []string
	for _, m := range memories {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if m.Kind != "" {
			lines = append(lines, fmt.Sprintf("[%s] %s", m.Kind, content))
		} else {
			lines = append(lines, content)
		}
	}
	if len(lines) == 0 {
		return Section{}, false
	}
	return Section{
		Kind:     SectionMemory,
		Title:    "Project Memories",
		Priority: 15,
		Content:  strings.Join(lines, "\n"),
	}, true
}

// MergeMemories replaces any existing memory section in pack with a single
// new section built from memories (joined newline-separated), inserted
// immediately before the first plan/file-snippet/tool-output section (or
// appended if none exist), then rebuilds the pack within maxTokens. Mirrors
// RefreshPlanWithBudget's replace-and-rebuild shape.
func MergeMemories(pack Pack, memories []MemoryNote, maxTokens int, now func() time.Time) Pack {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	generatedAt := pack.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	if now != nil {
		generatedAt = now().UTC()
	}

	memorySection, hasMemory := newMemorySection(memories)
	sections := make([]Section, 0, len(pack.Sections)+1)
	insertedMemory := false
	for _, section := range pack.Sections {
		if section.Kind == SectionMemory {
			continue
		}
		if hasMemory && !insertedMemory &&
			(section.Kind == SectionPlan || section.Kind == SectionFileSnippet || section.Kind == SectionToolOutput) {
			sections = append(sections, memorySection)
			insertedMemory = true
		}
		sections = append(sections, section)
	}
	if hasMemory && !insertedMemory {
		sections = append(sections, memorySection)
	}

	return buildPackFromSections(sections, maxTokens, generatedAt)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/contextpack/... -v`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/contextpack/contextpack.go internal/contextpack/builder.go internal/contextpack/contextpack_test.go
git commit -m "feat(contextpack): add memory section and MergeMemories"
```

---

### Task 6: `internal/knowledge` extraction protocol

**Files:**
- Create: `internal/knowledge/protocol.go`
- Create: `internal/knowledge/protocol_test.go`

**Interfaces:**
- Produces: `knowledge.MemoryNote{Kind, Content string}`; `knowledge.Extraction{SessionSummary string, Memories []MemoryNote, FileSummaries map[string]string}`; `knowledge.ParseExtraction(raw string) (Extraction, error)`; `knowledge.ErrNoExtractionFound`.

- [ ] **Step 1: Write the failing tests**

Create `internal/knowledge/protocol_test.go`:

```go
package knowledge

import (
	"errors"
	"testing"
)

func TestParseExtractionValid(t *testing.T) {
	raw := `{"session_summary":"Fixed the login bug.","memories":[{"kind":"fact","content":"Uses SQLite for persistence"},{"kind":"architecture","content":"TUI built with Bubble Tea"}],"file_summaries":{"internal/foo/bar.go":"Handles login validation"}}`

	extraction, err := ParseExtraction(raw)
	if err != nil {
		t.Fatalf("ParseExtraction returned error: %v", err)
	}
	if extraction.SessionSummary != "Fixed the login bug." {
		t.Fatalf("SessionSummary = %q", extraction.SessionSummary)
	}
	if len(extraction.Memories) != 2 {
		t.Fatalf("len(Memories) = %d, want 2: %#v", len(extraction.Memories), extraction.Memories)
	}
	if extraction.Memories[0].Kind != "fact" || extraction.Memories[0].Content != "Uses SQLite for persistence" {
		t.Fatalf("Memories[0] = %#v", extraction.Memories[0])
	}
	if extraction.FileSummaries["internal/foo/bar.go"] != "Handles login validation" {
		t.Fatalf("FileSummaries = %#v", extraction.FileSummaries)
	}
}

func TestParseExtractionStripsMarkdownFence(t *testing.T) {
	raw := "```json\n{\"session_summary\":\"s\",\"memories\":[],\"file_summaries\":{}}\n```"

	extraction, err := ParseExtraction(raw)
	if err != nil {
		t.Fatalf("ParseExtraction returned error: %v", err)
	}
	if extraction.SessionSummary != "s" {
		t.Fatalf("SessionSummary = %q, want %q", extraction.SessionSummary, "s")
	}
}

func TestParseExtractionDefaultsMissingKindToFact(t *testing.T) {
	raw := `{"session_summary":"s","memories":[{"content":"no kind given"}],"file_summaries":{}}`

	extraction, err := ParseExtraction(raw)
	if err != nil {
		t.Fatalf("ParseExtraction returned error: %v", err)
	}
	if len(extraction.Memories) != 1 || extraction.Memories[0].Kind != "fact" {
		t.Fatalf("Memories = %#v, want kind defaulted to fact", extraction.Memories)
	}
}

func TestParseExtractionSkipsBlankMemoryContent(t *testing.T) {
	raw := `{"session_summary":"s","memories":[{"kind":"fact","content":"   "}],"file_summaries":{}}`

	extraction, err := ParseExtraction(raw)
	if err != nil {
		t.Fatalf("ParseExtraction returned error: %v", err)
	}
	if len(extraction.Memories) != 0 {
		t.Fatalf("Memories = %#v, want empty (blank content skipped)", extraction.Memories)
	}
}

func TestParseExtractionRejectsNoJSONObject(t *testing.T) {
	_, err := ParseExtraction("I don't know what happened.")
	if !errors.Is(err, ErrNoExtractionFound) {
		t.Fatalf("err = %v, want ErrNoExtractionFound", err)
	}
}

func TestParseExtractionRejectsMalformedJSON(t *testing.T) {
	_, err := ParseExtraction(`{"session_summary": "s", "memories": [`)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/knowledge/... -v`
Expected: FAIL (package `internal/knowledge` does not exist yet)

- [ ] **Step 3: Implement `internal/knowledge/protocol.go`**

```go
package knowledge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrNoExtractionFound = errors.New("knowledge: no JSON extraction object found in model output")

// MemoryNote is knowledge's own view of an extracted memory candidate. It
// is a separate, identically-shaped type from contextpack.MemoryNote (not
// shared) so that internal/agent and internal/knowledge never need to
// import each other — see the Milestone N design doc.
type MemoryNote struct {
	Kind    string
	Content string
}

// Extraction is the parsed form of the knowledge pass's JSON response.
type Extraction struct {
	SessionSummary string
	Memories       []MemoryNote
	FileSummaries  map[string]string
}

type extractionEnvelope struct {
	SessionSummary string            `json:"session_summary"`
	Memories       []memoryPayload   `json:"memories"`
	FileSummaries  map[string]string `json:"file_summaries"`
}

type memoryPayload struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// ParseExtraction extracts and validates the single JSON object the
// knowledge prompt instructs the model to return. It tolerates a leading/
// trailing ```json fence, since local models frequently wrap JSON in
// markdown even when told not to (same tolerance as agent.ParseAction).
func ParseExtraction(raw string) (Extraction, error) {
	jsonText, err := extractJSONObject(raw)
	if err != nil {
		return Extraction{}, err
	}

	var envelope extractionEnvelope
	if err := json.Unmarshal([]byte(jsonText), &envelope); err != nil {
		return Extraction{}, fmt.Errorf("knowledge: malformed extraction JSON: %w", err)
	}

	memories := make([]MemoryNote, 0, len(envelope.Memories))
	for _, m := range envelope.Memories {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		kind := strings.TrimSpace(m.Kind)
		if kind == "" {
			kind = "fact"
		}
		memories = append(memories, MemoryNote{Kind: kind, Content: content})
	}

	return Extraction{
		SessionSummary: strings.TrimSpace(envelope.SessionSummary),
		Memories:       memories,
		FileSummaries:  envelope.FileSummaries,
	}, nil
}

func extractJSONObject(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start == -1 || end == -1 || end < start {
		return "", ErrNoExtractionFound
	}
	return trimmed[start : end+1], nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/knowledge/... -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/knowledge/protocol.go internal/knowledge/protocol_test.go
git commit -m "feat(knowledge): add extraction protocol parsing"
```

---

### Task 7: `internal/knowledge` extraction prompt

**Files:**
- Create: `internal/knowledge/prompts.go`
- Create: `internal/knowledge/prompts_test.go`

**Interfaces:**
- Consumes: `session.Message{Role session.Role, Content string, CreatedAt time.Time}` (existing, `internal/app/session/session.go`); `registry.AuditEvent{ToolName, ResultSummary string, FilesChanged []string, ...}` (existing, `internal/tools/registry/audit.go`); `schema.ChatMessage{Role schema.Role, Content string}` (existing, `internal/llm/schema`).
- Produces: `knowledge.BuildExtractionPrompt(messages []session.Message, auditLog []registry.AuditEvent, touchedFiles map[string]string) schema.ChatMessage`.

- [ ] **Step 1: Write the failing tests**

Create `internal/knowledge/prompts_test.go`:

```go
package knowledge

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestBuildExtractionPromptIncludesTranscriptActivityAndFiles(t *testing.T) {
	messages := []session.Message{
		{Role: session.RoleUser, Content: "Fix the login bug", CreatedAt: time.Unix(100, 0)},
		{Role: session.RoleAssistant, Content: "Fixed it.", CreatedAt: time.Unix(101, 0)},
	}
	auditLog := []registry.AuditEvent{
		{ToolName: "file.write_patch", ResultSummary: "applied patch to internal/foo/bar.go"},
	}
	touchedFiles := map[string]string{
		"internal/foo/bar.go": "package foo\n\nfunc Bar() {}\n",
	}

	msg := BuildExtractionPrompt(messages, auditLog, touchedFiles)

	for _, want := range []string{
		"Fix the login bug",
		"Fixed it.",
		"file.write_patch -> applied patch to internal/foo/bar.go",
		"internal/foo/bar.go",
		"func Bar() {}",
	} {
		if !strings.Contains(msg.Content, want) {
			t.Fatalf("prompt missing %q:\n%s", want, msg.Content)
		}
	}
}

func TestBuildExtractionPromptHandlesEmptyInputs(t *testing.T) {
	msg := BuildExtractionPrompt(nil, nil, nil)

	for _, want := range []string{"(no messages)", "(no tool activity)", "(no files touched)"} {
		if !strings.Contains(msg.Content, want) {
			t.Fatalf("prompt missing %q:\n%s", want, msg.Content)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/knowledge/... -run TestBuildExtractionPrompt -v`
Expected: FAIL (`BuildExtractionPrompt` undefined)

- [ ] **Step 3: Implement `internal/knowledge/prompts.go`**

```go
package knowledge

import (
	"fmt"
	"sort"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

const extractionPromptTemplate = `You are Marshal's knowledge agent. Your job is to review what happened in a coding session and extract durable, evidence-backed information worth remembering about this project.

Session transcript:
%s

Tool activity:
%s

Files touched this session:
%s

Respond with exactly one JSON object and nothing else, in this shape:
{"session_summary": "short paragraph summarizing what happened this session", "memories": [{"kind": "fact", "content": "..."}, {"kind": "architecture", "content": "..."}, {"kind": "decision", "content": "..."}], "file_summaries": {"path/to/file.go": "one-line summary of what this file does"}}

Rules:
- Only extract memories you have direct evidence for from the transcript or tool activity above. Do not invent facts.
- Prefer few, high-value memories over many trivial ones. It is fine to return an empty memories list.
- Only include file_summaries entries for files listed under "Files touched this session" above.
- kind must be one of "fact", "architecture", "decision".`

// BuildExtractionPrompt builds the single user-turn prompt sent to the
// knowledge model. messages is the session transcript, auditLog is the
// session's tool-call history, and touchedFiles maps each session-touched
// file path (from AuditEvent.FilesChanged) to its current content.
func BuildExtractionPrompt(messages []session.Message, auditLog []registry.AuditEvent, touchedFiles map[string]string) schema.ChatMessage {
	var transcript strings.Builder
	if len(messages) == 0 {
		transcript.WriteString("(no messages)")
	}
	for _, m := range messages {
		fmt.Fprintf(&transcript, "%s: %s\n", m.Role, m.Content)
	}

	var activity strings.Builder
	if len(auditLog) == 0 {
		activity.WriteString("(no tool activity)")
	}
	for _, event := range auditLog {
		fmt.Fprintf(&activity, "%s -> %s\n", event.ToolName, event.ResultSummary)
	}

	var files strings.Builder
	paths := make([]string, 0, len(touchedFiles))
	for path := range touchedFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		files.WriteString("(no files touched)")
	}
	for _, path := range paths {
		fmt.Fprintf(&files, "--- %s ---\n%s\n", path, touchedFiles[path])
	}

	return schema.ChatMessage{
		Role:    schema.RoleUser,
		Content: fmt.Sprintf(extractionPromptTemplate, transcript.String(), activity.String(), files.String()),
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/knowledge/... -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/knowledge/prompts.go internal/knowledge/prompts_test.go
git commit -m "feat(knowledge): add extraction prompt builder"
```

---

### Task 8: `EndSession` orchestration

**Files:**
- Create: `internal/knowledge/knowledge.go`
- Create: `internal/knowledge/knowledge_test.go`

**Interfaces:**
- Consumes: `session.State.Messages() []session.Message`, `session.State.AuditLog() []registry.AuditEvent` (existing, `internal/app/session/session.go`); `db.SaveMemory`, `db.EndSession`, `db.UpdateFileSummary` (Tasks 1-3); `BuildExtractionPrompt` (Task 7); `ParseExtraction` (Task 6); `provider.Provider.Chat` (existing, `internal/llm/provider/provider.go`); `routing.TaskProfile{Class string}` (existing).
- Produces: `knowledge.RouteResolver` interface; `knowledge.EndSessionInput`; `knowledge.EndSession(ctx context.Context, in EndSessionInput)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/knowledge/knowledge_test.go`:

```go
package knowledge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

type fakeProvider struct {
	response string
	err      error
}

func (p *fakeProvider) Name() string { return "fake" }
func (p *fakeProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}
func (p *fakeProvider) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
	return schema.EmbedResponse{}, nil
}
func (p *fakeProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}
func (p *fakeProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	ch := make(chan schema.ChatEvent, 2)
	if p.err != nil {
		ch <- schema.ChatEvent{Type: schema.ChatEventError, Err: p.err}
		close(ch)
		return ch, nil
	}
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: p.response}
	ch <- schema.ChatEvent{Type: schema.ChatEventDone}
	close(ch)
	return ch, nil
}

type fakeRouteResolver struct {
	route routing.Route
	prov  provider.Provider
	err   error
}

func (r *fakeRouteResolver) Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error) {
	if r.err != nil {
		return routing.Route{}, nil, r.err
	}
	return r.route, r.prov, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	return database, projectID
}

func newTestState(t *testing.T, workingDir string) *session.State {
	t.Helper()
	return session.New(config.Default(), workingDir, time.Unix(100, 0), session.Persistence{})
}

func knowledgeRoute() routing.Route {
	return routing.Route{Role: routing.RoleKnowledge, Preset: routing.ModelPreset{Name: "tiny", Model: "tiny-model"}}
}

func TestEndSessionPersistsSummaryMemoriesAndFileSummaries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bar.go"), []byte("package foo\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}

	database, projectID := newTestDB(t)
	sessionID := "sess-1"
	if err := database.CreateSession(sessionID, projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	state := newTestState(t, dir)
	state.AddMessage(session.RoleUser, "Fix the bug in bar.go")
	state.LogToolCall(registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultSummary: "applied patch",
		FilesChanged:  []string{"bar.go"},
	})

	response := `{"session_summary":"Fixed the bug.","memories":[{"kind":"fact","content":"Uses SQLite for persistence"}],"file_summaries":{"bar.go":"Defines package foo"}}`
	resolver := &fakeRouteResolver{route: knowledgeRoute(), prov: &fakeProvider{response: response}}

	EndSession(context.Background(), EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: resolver,
		WorkingDir:    dir,
		Now:           func() time.Time { return time.Unix(200, 0) },
		Logger:        testLogger(),
	})

	memories, err := database.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	if len(memories) != 1 || memories[0].Content != "Uses SQLite for persistence" {
		t.Fatalf("memories = %#v, want one fact memory", memories)
	}
	if memories[0].Confidence != db.MemoryConfidenceTentative {
		t.Fatalf("Confidence = %q, want tentative", memories[0].Confidence)
	}
	if memories[0].SourceSessionID != sessionID {
		t.Fatalf("SourceSessionID = %q, want %q", memories[0].SourceSessionID, sessionID)
	}

	gotSession, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if gotSession.Summary != "Fixed the bug." {
		t.Fatalf("Summary = %q, want %q", gotSession.Summary, "Fixed the bug.")
	}
	if gotSession.EndedAt == nil {
		t.Fatal("EndedAt is nil, want set")
	}

	if err := database.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "bar.go", Hash: "h1", LastIndexedAt: time.Unix(100, 0)},
	}); err != nil {
		t.Fatalf("SaveFileIndex failed: %v", err)
	}
	if err := database.UpdateFileSummary(projectID, "bar.go", "Defines package foo"); err != nil {
		t.Fatalf("UpdateFileSummary failed: %v", err)
	}
}

func TestEndSessionSkipsWhenNoUserMessages(t *testing.T) {
	database, projectID := newTestDB(t)
	sessionID := "sess-empty"
	if err := database.CreateSession(sessionID, projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	state := newTestState(t, t.TempDir())
	resolver := &fakeRouteResolver{err: errors.New("should not be called")}

	EndSession(context.Background(), EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: resolver,
		WorkingDir:    t.TempDir(),
		Now:           func() time.Time { return time.Unix(200, 0) },
		Logger:        testLogger(),
	})

	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt != nil || got.Summary != "" {
		t.Fatalf("expected no session-end write, got %#v", got)
	}
}

func TestEndSessionSwallowsRouteResolutionError(t *testing.T) {
	database, projectID := newTestDB(t)
	sessionID := "sess-route-err"
	if err := database.CreateSession(sessionID, projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	state := newTestState(t, t.TempDir())
	state.AddMessage(session.RoleUser, "hello")

	EndSession(context.Background(), EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: &fakeRouteResolver{err: errors.New("no route configured")},
		WorkingDir:    t.TempDir(),
		Now:           func() time.Time { return time.Unix(200, 0) },
		Logger:        testLogger(),
	})

	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt != nil {
		t.Fatalf("expected no session-end write after route error, got %#v", got)
	}
}

func TestEndSessionSwallowsChatError(t *testing.T) {
	database, projectID := newTestDB(t)
	sessionID := "sess-chat-err"
	if err := database.CreateSession(sessionID, projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	state := newTestState(t, t.TempDir())
	state.AddMessage(session.RoleUser, "hello")
	resolver := &fakeRouteResolver{route: knowledgeRoute(), prov: &fakeProvider{err: errors.New("connection refused")}}

	EndSession(context.Background(), EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: resolver,
		WorkingDir:    t.TempDir(),
		Now:           func() time.Time { return time.Unix(200, 0) },
		Logger:        testLogger(),
	})

	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt != nil {
		t.Fatalf("expected no session-end write after chat error, got %#v", got)
	}
}

func TestEndSessionSwallowsParseFailure(t *testing.T) {
	database, projectID := newTestDB(t)
	sessionID := "sess-parse-err"
	if err := database.CreateSession(sessionID, projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	state := newTestState(t, t.TempDir())
	state.AddMessage(session.RoleUser, "hello")
	resolver := &fakeRouteResolver{route: knowledgeRoute(), prov: &fakeProvider{response: "not json at all"}}

	EndSession(context.Background(), EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: resolver,
		WorkingDir:    t.TempDir(),
		Now:           func() time.Time { return time.Unix(200, 0) },
		Logger:        testLogger(),
	})

	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt != nil {
		t.Fatalf("expected no session-end write after parse failure, got %#v", got)
	}
}

func TestEndSessionIgnoresFileSummaryOutsideTouchedFiles(t *testing.T) {
	dir := t.TempDir()
	database, projectID := newTestDB(t)
	sessionID := "sess-untouched"
	if err := database.CreateSession(sessionID, projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if err := database.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "untouched.go", Hash: "h1", LastIndexedAt: time.Unix(100, 0)},
	}); err != nil {
		t.Fatalf("SaveFileIndex failed: %v", err)
	}

	state := newTestState(t, dir)
	state.AddMessage(session.RoleUser, "hello")
	// No tool calls logged, so FilesChanged is empty — nothing is "touched".

	response := `{"session_summary":"did nothing","memories":[],"file_summaries":{"untouched.go":"should be ignored"}}`
	resolver := &fakeRouteResolver{route: knowledgeRoute(), prov: &fakeProvider{response: response}}

	EndSession(context.Background(), EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: resolver,
		WorkingDir:    dir,
		Now:           func() time.Time { return time.Unix(200, 0) },
		Logger:        testLogger(),
	})

	files, err := database.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(files) != 1 || files[0].Summary != "" {
		t.Fatalf("files = %#v, want summary untouched (empty)", files)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/knowledge/... -run TestEndSession -v`
Expected: FAIL (`EndSession` / `EndSessionInput` / `RouteResolver` undefined)

- [ ] **Step 3: Implement `internal/knowledge/knowledge.go`**

```go
package knowledge

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

// RouteResolver is declared locally rather than imported from
// internal/agent, even though it is structurally identical to
// agent.RouteResolver: internal/agent's MemoryProvider needs
// contextpack.MemoryNote, and internal/knowledge needs a route resolver
// here, so a type shared in either direction would create an import cycle
// between the two packages. Go's structural interfaces mean the same
// *routedProviderResolver constructed in internal/app/app.go satisfies
// both without either package importing the other.
type RouteResolver interface {
	Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error)
}

type EndSessionInput struct {
	DB            *db.DB
	ProjectID     int64
	SessionID     string
	State         *session.State
	RouteResolver RouteResolver
	WorkingDir    string
	Now           func() time.Time
	Logger        *slog.Logger
}

// EndSession summarizes the session, extracts durable memories, and
// summarizes session-touched files. It is best-effort: every internal
// failure is logged and swallowed, never returned to the caller, so a
// failed knowledge pass never affects Marshal's process exit. It is a
// no-op if the session has no user messages.
func EndSession(ctx context.Context, in EndSessionInput) {
	messages := in.State.Messages()
	if !hasUserMessage(messages) {
		return
	}

	now := in.Now
	if now == nil {
		now = time.Now
	}

	route, p, err := in.RouteResolver.Resolve(routing.TaskProfile{Class: "knowledge"})
	if err != nil {
		in.Logger.Error("knowledge: resolve route failed", "error", err, "session_id", in.SessionID)
		return
	}

	auditLog := in.State.AuditLog()
	touchedFiles := readTouchedFiles(in.WorkingDir, auditLog)

	prompt := BuildExtractionPrompt(messages, auditLog, touchedFiles)
	raw, err := chatOnce(ctx, p, route.Preset.Model, prompt)
	if err != nil {
		in.Logger.Error("knowledge: chat call failed", "error", err, "session_id", in.SessionID)
		return
	}

	extraction, err := ParseExtraction(raw)
	if err != nil {
		in.Logger.Error("knowledge: parse extraction failed", "error", err, "session_id", in.SessionID)
		return
	}

	if err := in.DB.EndSession(in.SessionID, now(), extraction.SessionSummary); err != nil {
		in.Logger.Error("knowledge: save session summary failed", "error", err, "session_id", in.SessionID)
	}

	for _, memory := range extraction.Memories {
		if err := in.DB.SaveMemory(in.ProjectID, memory.Kind, memory.Content, in.SessionID, now()); err != nil {
			in.Logger.Error("knowledge: save memory failed", "error", err, "session_id", in.SessionID)
		}
	}

	for path, summary := range extraction.FileSummaries {
		if _, touched := touchedFiles[path]; !touched {
			continue
		}
		if err := in.DB.UpdateFileSummary(in.ProjectID, path, summary); err != nil {
			in.Logger.Error("knowledge: update file summary failed", "error", err, "session_id", in.SessionID, "path", path)
		}
	}
}

func hasUserMessage(messages []session.Message) bool {
	for _, m := range messages {
		if m.Role == session.RoleUser {
			return true
		}
	}
	return false
}

// readTouchedFiles reads the current content of every distinct path in
// auditLog's FilesChanged. Files that no longer exist or cannot be read are
// silently skipped: the knowledge pass runs best-effort at process exit and
// must not fail the whole extraction over one unreadable file.
func readTouchedFiles(workingDir string, auditLog []registry.AuditEvent) map[string]string {
	seen := map[string]bool{}
	files := map[string]string{}
	for _, event := range auditLog {
		for _, path := range event.FilesChanged {
			if seen[path] {
				continue
			}
			seen[path] = true
			content, err := os.ReadFile(filepath.Join(workingDir, path))
			if err != nil {
				continue
			}
			files[path] = string(content)
		}
	}
	return files
}

func chatOnce(ctx context.Context, p provider.Provider, model string, message schema.ChatMessage) (string, error) {
	events, err := p.Chat(ctx, schema.ChatRequest{
		Model:    model,
		Messages: []schema.ChatMessage{message},
		Stream:   true,
	})
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for event := range events {
		switch event.Type {
		case schema.ChatEventDelta:
			sb.WriteString(event.Delta)
		case schema.ChatEventError:
			return "", event.Err
		case schema.ChatEventDone:
			return sb.String(), nil
		}
	}
	return sb.String(), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/knowledge/... -v`
Expected: all PASS

- [ ] **Step 5: Run the full test suite**

Run: `go test ./...`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/knowledge/knowledge.go internal/knowledge/knowledge_test.go
git commit -m "feat(knowledge): add EndSession orchestration"
```

---

### Task 9: Wire memories into `agent.Runner`

**Files:**
- Modify: `internal/agent/runner.go`
- Modify: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `contextpack.MergeMemories`, `contextpack.MemoryNote`, `contextpack.DefaultMaxTokens` (Task 5).
- Produces: `agent.Runner.MemoryProvider MemoryProvider` (new field); `agent.Runner.ProjectID int64` (new field); `agent.MemoryProvider` interface `{ Memories(projectID int64) ([]contextpack.MemoryNote, error) }`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/agent/runner_test.go`:

```go
type fakeMemoryProvider struct {
	memories []contextpack.MemoryNote
	err      error
}

func (f *fakeMemoryProvider) Memories(projectID int64) ([]contextpack.MemoryNote, error) {
	return f.memories, f.err
}

func TestRunMergesMemoriesIntoContextPackBeforeFirstMessage(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"done"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.MemoryProvider = &fakeMemoryProvider{memories: []contextpack.MemoryNote{
		{Kind: "fact", Content: "Uses SQLite for persistence"},
	}}
	runner.ProjectID = 7

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	pack := state.ContextPack()
	found := false
	for _, section := range pack.Sections {
		if section.Kind == contextpack.SectionMemory && strings.Contains(section.Content, "Uses SQLite for persistence") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a memory section in context pack, got %#v", pack.Sections)
	}
}

func TestRunWithoutMemoryProviderLeavesContextPackEmpty(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"done"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	pack := state.ContextPack()
	if !pack.IsEmpty() {
		t.Fatalf("expected empty context pack when MemoryProvider is nil, got %#v", pack.Sections)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/... -run "TestRunMergesMemories|TestRunWithoutMemoryProvider" -v`
Expected: FAIL (`runner.MemoryProvider` / `runner.ProjectID` undefined)

- [ ] **Step 3: Add `MemoryProvider` and wire the merge call**

In `internal/agent/runner.go`, after the `RouteResolver` interface declaration, add:

```go
// MemoryProvider supplies durable project memories for injection into the
// context pack at the start of each turn. It returns contextpack.MemoryNote
// (not a type from internal/knowledge) so that internal/agent never needs
// to import internal/knowledge — the two packages must not import each
// other (see the Milestone N design doc).
type MemoryProvider interface {
	Memories(projectID int64) ([]contextpack.MemoryNote, error)
}
```

Change the `Runner` struct to add two fields:

```go
type Runner struct {
	Provider          provider.Provider
	Registry          *registry.Registry
	Policy            *policy.PolicyEngine
	State             *session.State
	Model             string
	RouteResolver     RouteResolver
	MemoryProvider    MemoryProvider
	ProjectID         int64
	Now               func() time.Time
	MaxToolIterations int
	MaxRetries        int
}
```

In `Run`, call the merge right after route resolution:

```go
	task := NewTask(goal, r.Now())
	task.Class = Classify(goal)
	turnProvider, turnModel, route := r.resolveRoute(task)
	r.mergeMemories()
```

Add the new method near `resolveRoute`:

```go
// mergeMemories injects the project's current non-stale memories into the
// context pack, if a MemoryProvider is configured. Failures are ignored: a
// missing or failing memory source should never block a turn.
func (r *Runner) mergeMemories() {
	if r.MemoryProvider == nil {
		return
	}
	memories, err := r.MemoryProvider.Memories(r.ProjectID)
	if err != nil || len(memories) == 0 {
		return
	}
	current := r.State.ContextPack()
	maxTokens := current.TokenUsage.MaxTokens
	if maxTokens <= 0 {
		maxTokens = contextpack.DefaultMaxTokens
	}
	r.State.SetContextPack(contextpack.MergeMemories(current, memories, maxTokens, r.Now))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/... -v`
Expected: all PASS

- [ ] **Step 5: Run the full test suite**

Run: `go test ./...`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "feat(agent): inject durable memories into the context pack each turn"
```

---

### Task 10: TUI memory browser package

**Files:**
- Create: `internal/app/tui/memory/model.go`
- Create: `internal/app/tui/memory/view.go`
- Create: `internal/app/tui/memory/messages.go`
- Create: `internal/app/tui/memory/model_test.go`

**Interfaces:**
- Consumes: `db.Memory`, `db.MemoryConfidenceConfirmed/Stale`, `(*db.DB) GetMemories`, `(*db.DB) SetMemoryConfidence` (Task 1).
- Produces: `memory.Model`; `memory.New(database *db.DB, projectID int64) Model`; `memory.ClosedMsg{}`.

- [ ] **Step 1: Write the failing tests**

Create `internal/app/tui/memory/model_test.go`:

```go
package memory

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/db"
)

func newTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	return database, projectID
}

func TestNewLoadsMemories(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}

	m := New(database, projectID)

	if len(m.memories) != 1 || m.memories[0].Content != "Uses SQLite" {
		t.Fatalf("memories = %#v, want one loaded memory", m.memories)
	}
}

func TestEscReturnsClosedMsg(t *testing.T) {
	database, projectID := newTestDB(t)
	m := New(database, projectID)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected a command")
	}
	if _, ok := cmd().(ClosedMsg); !ok {
		t.Fatalf("expected ClosedMsg, got %T", cmd())
	}
}

func TestCursorMovesWithinBounds(t *testing.T) {
	database, projectID := newTestDB(t)
	for i, content := range []string{"first", "second"} {
		if err := database.SaveMemory(projectID, "fact", content, "sess-1", time.Unix(int64(100+i), 0)); err != nil {
			t.Fatalf("SaveMemory failed: %v", err)
		}
	}
	m := New(database, projectID)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (clamped at top)", m.cursor)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (clamped at bottom)", m.cursor)
	}
}

func TestSKeyMarksSelectedMemoryStale(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	m := New(database, projectID)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = updated.(Model)

	if m.memories[0].Confidence != db.MemoryConfidenceStale {
		t.Fatalf("in-memory confidence = %q, want stale", m.memories[0].Confidence)
	}

	stored, err := database.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	if stored[0].Confidence != db.MemoryConfidenceStale {
		t.Fatalf("stored confidence = %q, want stale", stored[0].Confidence)
	}
}

func TestCKeyMarksSelectedMemoryConfirmed(t *testing.T) {
	database, projectID := newTestDB(t)
	if err := database.SaveMemory(projectID, "fact", "Uses SQLite", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	m := New(database, projectID)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(Model)

	if m.memories[0].Confidence != db.MemoryConfidenceConfirmed {
		t.Fatalf("in-memory confidence = %q, want confirmed", m.memories[0].Confidence)
	}
}

func TestViewShowsMemoriesAndEmptyState(t *testing.T) {
	database, projectID := newTestDB(t)
	m := New(database, projectID)

	view := m.View()
	if !strings.Contains(view, "No memories yet.") {
		t.Fatalf("View() missing empty-state message:\n%s", view)
	}

	if err := database.SaveMemory(projectID, "architecture", "TUI built with Bubble Tea", "sess-1", time.Unix(100, 0)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	m = New(database, projectID)
	view = m.View()
	if !strings.Contains(view, "TUI built with Bubble Tea") {
		t.Fatalf("View() missing memory content:\n%s", view)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/memory/... -v`
Expected: FAIL (package does not exist yet)

- [ ] **Step 3: Implement `internal/app/tui/memory/messages.go`**

```go
package memory

type ClosedMsg struct{}
```

- [ ] **Step 4: Implement `internal/app/tui/memory/model.go`**

```go
package memory

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/db"
)

type Model struct {
	db        *db.DB
	projectID int64
	memories  []db.Memory
	cursor    int
	footer    string
}

func New(database *db.DB, projectID int64) Model {
	m := Model{db: database, projectID: projectID}
	memories, err := database.GetMemories(projectID)
	if err != nil {
		m.footer = "Load failed: " + err.Error()
		return m
	}
	m.memories = memories
	return m
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.Type {
	case tea.KeyEsc:
		return m, func() tea.Msg { return ClosedMsg{} }
	case tea.KeyUp:
		m.moveCursor(-1)
		return m, nil
	case tea.KeyDown:
		m.moveCursor(1)
		return m, nil
	}

	switch keyMsg.String() {
	case "k":
		m.moveCursor(-1)
	case "j":
		m.moveCursor(1)
	case "c":
		m.setConfidence(db.MemoryConfidenceConfirmed)
	case "s":
		m.setConfidence(db.MemoryConfidenceStale)
	}
	return m, nil
}

func (m *Model) moveCursor(delta int) {
	if len(m.memories) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.memories) {
		m.cursor = len(m.memories) - 1
	}
}

func (m *Model) setConfidence(confidence string) {
	if m.cursor < 0 || m.cursor >= len(m.memories) {
		return
	}
	selected := m.memories[m.cursor]
	if err := m.db.SetMemoryConfidence(selected.ID, confidence, time.Now()); err != nil {
		m.footer = "Update failed: " + err.Error()
		return
	}
	selected.Confidence = confidence
	m.memories[m.cursor] = selected
	m.footer = ""
}

func (m Model) Footer() string {
	return m.footer
}
```

- [ ] **Step 5: Implement `internal/app/tui/memory/view.go`**

```go
package memory

import (
	"fmt"
	"strings"
)

func (m Model) View() string {
	var b strings.Builder
	b.WriteString("┌── Project Memories ─────────────────────────────────────────────┐\n")
	if len(m.memories) == 0 {
		b.WriteString("│ No memories yet.                                                 │\n")
	}
	for i, mem := range m.memories {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		line := fmt.Sprintf("%s[%s] (%s) %s", cursor, mem.Kind, mem.Confidence, mem.Content)
		b.WriteString(fmt.Sprintf("│ %s\n", line))
	}
	b.WriteString("├───────────────────────────────────────────────────────────────┤\n")
	if m.footer != "" {
		b.WriteString(fmt.Sprintf("│ %s\n", m.footer))
	}
	b.WriteString("│ [↑/k ↓/j] Move  [c] Confirm  [s] Mark Stale  [Esc] Close         │\n")
	b.WriteString("└───────────────────────────────────────────────────────────────┘\n")
	return b.String()
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/app/tui/memory/... -v`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add internal/app/tui/memory/
git commit -m "feat(tui): add memory browser model"
```

---

### Task 11: Wire the knowledge pass and memory browser into `app.go` and `tui.Model`

**Files:**
- Modify: `internal/app/tui/model.go`
- Modify: `internal/app/tui/model_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Interfaces:**
- Consumes: `memory.New`, `memory.Model`, `memory.ClosedMsg` (Task 10); `knowledge.EndSession`, `knowledge.EndSessionInput` (Task 8); `agent.Runner.MemoryProvider`/`.ProjectID` (Task 9); `contextpack.MemoryNote` (Task 5); `db.Memory`, `db.MemoryConfidenceStale`, `(*db.DB) GetMemories` (Task 1).
- Produces: `tui.WithMemoryStore(database *db.DB, projectID int64) Option`.

- [ ] **Step 1: Write the failing TUI tests**

Add to `internal/app/tui/model_test.go`, and add `"marshal/internal/app/tui/memory"` and `"marshal/internal/db"` to its import block:

```go
func TestCtrlKOpensMemoryBrowser(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	m := New(state, WithMemoryStore(database, projectID))
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = updated.(Model)
	if !m.memoryOpen {
		t.Fatal("expected memoryOpen to be true")
	}
	if !strings.Contains(m.View(), "Project Memories") {
		t.Fatalf("View() missing memory browser:\n%s", m.View())
	}
}

func TestMemoryClosedMsgClosesOverlay(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	m := New(state, WithMemoryStore(database, projectID))
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = updated.(Model)
	if !m.memoryOpen {
		t.Fatal("expected memoryOpen")
	}
	updated, _ = m.Update(memory.ClosedMsg{})
	m = updated.(Model)
	if m.memoryOpen {
		t.Fatal("expected memoryOpen to be false after ClosedMsg")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/tui/... -run "TestCtrlKOpensMemoryBrowser|TestMemoryClosedMsgClosesOverlay" -v`
Expected: FAIL (`WithMemoryStore` / `m.memoryOpen` undefined)

- [ ] **Step 3: Wire the overlay into `internal/app/tui/model.go`**

Add imports:

```go
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/settings"
	"marshal/internal/db"
```

(insert `"marshal/internal/app/tui/memory"` and `"marshal/internal/db"` alongside the existing `"marshal/internal/app/tui/settings"` import, keeping the block alphabetized)

Add fields to `Model`:

```go
type Model struct {
	state          *session.State
	input          textinput.Model
	editingCommand bool
	runner         AgentRunner
	ctx            context.Context
	busy           bool
	settingsOpen   bool
	settingsModel  settings.Model
	configReloader ConfigReloader
	memoryOpen     bool
	memoryModel    memory.Model
	memoryDB       *db.DB
	memoryProject  int64
}
```

Add the option:

```go
// WithMemoryStore configures the memory browser overlay (Ctrl+K) with the
// project database it should read from. Without this option, the overlay
// still opens but memory.New receives a nil *db.DB, which callers must not
// do outside of tests that don't press Ctrl+K.
func WithMemoryStore(database *db.DB, projectID int64) Option {
	return func(m *Model) {
		m.memoryDB = database
		m.memoryProject = projectID
	}
}
```

In `Update`, add a case for `memory.ClosedMsg` alongside the existing `settings.CancelledMsg` case:

```go
	case memory.ClosedMsg:
		m.memoryOpen = false
		return m, nil
```

Right after the existing `if m.settingsOpen { ... }` block inside the `tea.KeyMsg` case (before the `if tc != nil` check), add:

```go
		if m.memoryOpen {
			updated, cmd := m.memoryModel.Update(msg)
			m.memoryModel = updated.(memory.Model)
			return m, cmd
		}
```

In the non-approval, non-overlay key switch (alongside `case tea.KeyCtrlO:`), add:

```go
			case tea.KeyCtrlK:
				m.memoryModel = memory.New(m.memoryDB, m.memoryProject)
				m.memoryOpen = true
				return m, nil
```

In `View`, add the short-circuit alongside the settings one:

```go
func (m Model) View() string {
	if m.settingsOpen {
		return m.settingsModel.View()
	}
	if m.memoryOpen {
		return m.memoryModel.View()
	}
```

- [ ] **Step 4: Run the TUI package tests**

Run: `go test ./internal/app/tui/... -v`
Expected: all PASS

- [ ] **Step 5: Write the failing `app.go` wiring tests**

Add to `internal/app/app_test.go`, and add `"fmt"`, `"marshal/internal/app/tui"`, and `"marshal/internal/db"` to its import block:

```go
func TestRunTriggersKnowledgeEndSessionButSkipsWithNoMessages(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	now := time.Unix(100, 0)
	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	database, dberr := db.Open(dbPath(dir))
	if dberr != nil {
		t.Fatalf("open db: %v", dberr)
	}
	defer database.Close()

	sessionID := fmt.Sprintf("sess_%d", now.UnixNano())
	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt != nil || got.Summary != "" {
		t.Fatalf("expected no session-end write with no messages, got %#v", got)
	}
}

func TestRunWiresMemoryBrowserOpensWithCtrlK(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	var view string
	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			m := model.(tui.Model)
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
			m = updated.(tui.Model)
			view = m.View()
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(view, "Project Memories") {
		t.Fatalf("view missing memory browser after Ctrl+K:\n%s", view)
	}
}
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/app/... -run "TestRunTriggersKnowledgeEndSession|TestRunWiresMemoryBrowser" -v`
Expected: FAIL (`db.GetSession` for a session that was never ended will actually pass trivially only once wiring exists — before wiring, `TestRunWiresMemoryBrowserOpensWithCtrlK` fails because Ctrl+K does nothing / `tui.Model` has no such behavior yet, and the view lacks "Project Memories")

- [ ] **Step 7: Wire `internal/app/app.go`**

Add imports:

```go
	"marshal/internal/contextpack"
	"marshal/internal/knowledge"
```

(insert alongside the existing `"marshal/internal/db"` and `"marshal/internal/llm/provider"` imports, keeping the block alphabetized)

Add the adapter type, near `routedProviderResolver`:

```go
// dbMemoryProvider adapts internal/db's stored memories to
// contextpack.MemoryNote for injection into context packs, filtering out
// memories marked stale.
type dbMemoryProvider struct {
	db *db.DB
}

func (p *dbMemoryProvider) Memories(projectID int64) ([]contextpack.MemoryNote, error) {
	memories, err := p.db.GetMemories(projectID)
	if err != nil {
		return nil, err
	}
	notes := make([]contextpack.MemoryNote, 0, len(memories))
	for _, m := range memories {
		if m.Confidence == db.MemoryConfidenceStale {
			continue
		}
		notes = append(notes, contextpack.MemoryNote{Kind: m.Kind, Content: m.Content})
	}
	return notes, nil
}
```

In `buildAgentRunner`, right after `runner.RouteResolver = resolver`, add:

```go
	runner.MemoryProvider = &dbMemoryProvider{db: database}
	runner.ProjectID = projectID
```

In `Run`, right after `var tuiOpts []tui.Option`, add (unconditionally, so the memory browser works even if the agent runner failed to build):

```go
	tuiOpts = append(tuiOpts, tui.WithMemoryStore(database, projectID))
```

Replace the function's final two lines:

```go
	return runOpts.programRunner(ctx, tui.New(state, tuiOpts...), stdout)
}
```

with:

```go
	knowledgeResolver := newRoutedProviderResolver(cfg)
	progErr := runOpts.programRunner(ctx, tui.New(state, tuiOpts...), stdout)
	knowledge.EndSession(context.Background(), knowledge.EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: knowledgeResolver,
		WorkingDir:    workingDir,
		Now:           runOpts.now,
		Logger:        logger,
	})
	return progErr
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/app/... -v`
Expected: all PASS

- [ ] **Step 9: Run the full test suite and build**

Run: `go test ./...`
Run: `go build ./cmd/marshal`
Expected: all PASS, build succeeds

- [ ] **Step 10: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go internal/app/app.go internal/app/app_test.go
git commit -m "feat(app): wire the knowledge pass and memory browser into Run and the TUI"
```

---

### Task 12: Mark Milestone N complete

**Files:**
- Modify: `docs/10-mvp-implementation-checklist.md`

- [ ] **Step 1: Check off the Milestone N items**

In `docs/10-mvp-implementation-checklist.md`, change:

```markdown
## Milestone N: Knowledge agent v1

- [ ] Summarise session at end
- [ ] Summarise changed files
- [ ] Store durable project memories
- [ ] Add confidence state
- [ ] Add memory browser in TUI
- [ ] Mark stale memories manually
```

to:

```markdown
## Milestone N: Knowledge agent v1

- [x] Summarise session at end
- [x] Summarise changed files
- [x] Store durable project memories
- [x] Add confidence state
- [x] Add memory browser in TUI
- [x] Mark stale memories manually
```

- [ ] **Step 2: Run the full test suite one final time**

Run: `go test ./...`
Run: `go vet ./...`
Run: `gofmt -l .` (expect no output — no unformatted files)
Run: `go build ./cmd/marshal`
Expected: all pass, no gofmt diffs, build succeeds

- [ ] **Step 3: Commit**

```bash
git add docs/10-mvp-implementation-checklist.md
git commit -m "docs: mark Milestone N knowledge agent v1 complete"
```
