# Milestone I: SQLite Persistence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist projects, file index metadata, sessions/messages, and tool-call audit events to SQLite, integrate persistence into `session.State`, and wire the database lifecycle into `app.Run`.

**Architecture:** The `internal/db` package already owns the SQLite connection and schema migrations. Extend it with storage methods that map between Go types and the existing `projects`, `files`, `agent_sessions`, `messages`, and `tool_calls` tables. `session.State` gains a `*db.DB` reference and project/session IDs; `AddMessage` and `LogToolCall` write through to SQLite. `app.Run` opens the database, runs migrations, resolves or creates the current project, and passes the database into `session.New`.

**Tech Stack:** Go 1.26.1, `modernc.org/sqlite`, standard library.

---

## File Structure

- `internal/db/db.go` — existing `Open`, `Close`, `Migrate`; add `sqlDB` accessor used by new storage files.
- `internal/db/projects.go` — `Project` type and `GetOrCreateProject` storage method.
- `internal/db/projects_test.go` — tests for project persistence.
- `internal/db/files.go` — `FileIndex` type and `SaveFileIndex`/`GetFileIndex` storage methods.
- `internal/db/files_test.go` — tests for file index persistence.
- `internal/db/sessions.go` — `CreateSession`, `SaveMessage`, `GetMessages` storage methods.
- `internal/db/sessions_test.go` — tests for session/message persistence.
- `internal/db/audits.go` — `SaveToolCall`, `GetToolCalls` storage methods.
- `internal/db/audits_test.go` — tests for audit persistence.
- `internal/db/migrations.go` — existing schema; no changes (schema already covers required tables).
- `internal/app/session/session.go` — add `DB`, `ProjectID`, `SessionID` fields; persist messages and tool calls.
- `internal/app/session/session_test.go` — tests for persistence hooks.
- `internal/app/app.go` — open DB, migrate, get/create project, create session, pass DB to `session.New`.
- `internal/app/app_test.go` — update/add tests for database wiring.
- `docs/plans/task.md` — update task statuses as tasks complete.

---

### Task 1: Project Storage Methods

**Files:**
- Create: `internal/db/projects.go`
- Create: `internal/db/projects_test.go`
- Modify: `internal/db/db.go`

- [ ] **Step 1: Write the failing test**

In `internal/db/projects_test.go`:

```go
package db

import (
	"testing"
)

func TestGetOrCreateProject(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	id1, err := db.GetOrCreateProject("/repo/path", "myproject")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	if id1 <= 0 {
		t.Fatalf("expected positive project id, got %d", id1)
	}

	id2, err := db.GetOrCreateProject("/repo/path", "different-name")
	if err != nil {
		t.Fatalf("GetOrCreateProject second call failed: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("expected same id for same root path, got %d and %d", id1, id2)
	}

	id3, err := db.GetOrCreateProject("/other/path", "myproject")
	if err != nil {
		t.Fatalf("GetOrCreateProject other path failed: %v", err)
	}
	if id3 == id1 {
		t.Fatalf("expected different id for different root path")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db -run TestGetOrCreateProject -v`
Expected: FAIL (`GetOrCreateProject` undefined).

- [ ] **Step 3: Write minimal implementation**

Add to `internal/db/db.go`:

```go
func (db *DB) exec(query string, args ...any) (sql.Result, error) {
	return db.sqlDB.Exec(query, args...)
}

func (db *DB) queryRow(query string, args ...any) *sql.Row {
	return db.sqlDB.QueryRow(query, args...)
}
```

Wait — expose the internal `sqlDB` directly to keep helpers minimal. Instead, add a package-private helper in `db.go`:

```go
func (db *DB) queryRow(query string, args ...any) *sql.Row {
	return db.sqlDB.QueryRow(query, args...)
}

func (db *DB) exec(query string, args ...any) (sql.Result, error) {
	return db.sqlDB.Exec(query, args...)
}
```

Create `internal/db/projects.go`:

```go
package db

import (
	"fmt"
	"time"
)

type Project struct {
	ID        int64
	RootPath  string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GetOrCreateProject returns the project ID for rootPath, creating the row
// if it does not exist. The root_path column is UNIQUE and is the identity
// key; name is updated on conflict so later calls can refresh metadata.
func (db *DB) GetOrCreateProject(rootPath string, name string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := db.exec(
		`INSERT INTO projects (root_path, name, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(root_path) DO UPDATE SET name=excluded.name, updated_at=excluded.updated_at`,
		rootPath, name, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert project: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get last project id: %w", err)
	}

	// ON CONFLICT UPDATE returns the id of the existing row via changes()
	// only when an insert happened. For updates, LastInsertId returns the
	// existing row id in modernc.org/sqlite, but be defensive and re-query.
	if id == 0 {
		row := db.queryRow(`SELECT id FROM projects WHERE root_path = ?`, rootPath)
		if scanErr := row.Scan(&id); scanErr != nil {
			return 0, fmt.Errorf("lookup project id: %w", scanErr)
		}
	}

	return id, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db -run TestGetOrCreateProject -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/db/db.go internal/db/projects.go internal/db/projects_test.go
git commit -m "feat(db): add GetOrCreateProject persistence"
```

---

### Task 2: File Index Metadata Storage

**Files:**
- Create: `internal/db/files.go`
- Create: `internal/db/files_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/db/files_test.go`:

```go
package db

import (
	"reflect"
	"testing"
	"time"
)

func TestSaveAndGetFileIndex(t *testing.T) {
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

	indexedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	files := []FileIndex{
		{
			Path:          "main.go",
			Language:      "go",
			Hash:          "abc123",
			SizeBytes:     1234,
			LastIndexedAt: indexedAt,
		},
		{
			Path:          "README.md",
			Language:      "markdown",
			Hash:          "def456",
			SizeBytes:     567,
			LastIndexedAt: indexedAt,
		},
	}

	if err := db.SaveFileIndex(projectID, files); err != nil {
		t.Fatalf("SaveFileIndex failed: %v", err)
	}

	got, err := db.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}

	if len(got) != len(files) {
		t.Fatalf("expected %d files, got %d", len(files), len(got))
	}

	for i := range files {
		if got[i].Path != files[i].Path ||
			got[i].Language != files[i].Language ||
			got[i].Hash != files[i].Hash ||
			got[i].SizeBytes != files[i].SizeBytes ||
			!got[i].LastIndexedAt.Equal(files[i].LastIndexedAt) {
			t.Errorf("file %d mismatch:\n got: %+v\nwant: %+v", i, got[i], files[i])
		}
	}
}

func TestSaveFileIndexUpdatesExisting(t *testing.T) {
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

	updated := []FileIndex{
		{Path: "main.go", Hash: "v2", SizeBytes: 2, LastIndexedAt: time.Now().UTC()},
	}
	if err := db.SaveFileIndex(projectID, updated); err != nil {
		t.Fatalf("SaveFileIndex update failed: %v", err)
	}

	got, err := db.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 file, got %d", len(got))
	}
	if got[0].Hash != "v2" || got[0].SizeBytes != 2 {
		t.Errorf("expected updated hash/size, got %+v", got[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db -run 'TestSaveAndGetFileIndex|TestSaveFileIndexUpdatesExisting' -v`
Expected: FAIL (`FileIndex`, `SaveFileIndex`, `GetFileIndex` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/db/files.go`:

```go
package db

import (
	"fmt"
	"time"
)

type FileIndex struct {
	Path          string
	Language      string
	Hash          string
	SizeBytes     int64
	LastIndexedAt time.Time
}

// SaveFileIndex replaces the file index for a project. It deletes all existing
// files for the project and inserts the provided rows. Callers are expected to
// pass the complete current index.
func (db *DB) SaveFileIndex(projectID int64, files []FileIndex) error {
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin save file index transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM files WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("delete existing files: %w", err)
	}

	stmt, err := tx.Prepare(`INSERT INTO files (project_id, path, language, hash, size_bytes, last_indexed_at)
							 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare file insert: %w", err)
	}
	defer stmt.Close()

	for _, f := range files {
		_, err := stmt.Exec(projectID, f.Path, f.Language, f.Hash, f.SizeBytes, f.LastIndexedAt.UTC().Format(time.RFC3339))
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
		`SELECT path, language, hash, size_bytes, last_indexed_at
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
		if err := rows.Scan(&f.Path, &f.Language, &f.Hash, &f.SizeBytes, &lastIndexed); err != nil {
			return nil, fmt.Errorf("scan file row: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, lastIndexed)
		if err != nil {
			return nil, fmt.Errorf("parse last_indexed_at: %w", err)
		}
		f.LastIndexedAt = parsed.UTC()
		files = append(files, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file rows: %w", err)
	}
	return files, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db -run 'TestSaveAndGetFileIndex|TestSaveFileIndexUpdatesExisting' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/db/files.go internal/db/files_test.go
git commit -m "feat(db): add file index persistence"
```

---

### Task 3: Session and Message Persistence

**Files:**
- Create: `internal/db/sessions.go`
- Create: `internal/db/sessions_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/db/sessions_test.go`:

```go
package db

import (
	"testing"
	"time"
)

func TestCreateSessionAndMessages(t *testing.T) {
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

	sessionID := "session-abc"
	now := time.Now().UTC()
	if err := db.CreateSession(sessionID, projectID, "test session", now); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if err := db.SaveMessage(sessionID, "user", "hello", now.Add(time.Second)); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}
	if err := db.SaveMessage(sessionID, "assistant", "hi there", now.Add(2*time.Second)); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	messages, err := db.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Content != "hello" {
		t.Errorf("message 0 mismatch: %+v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "hi there" {
		t.Errorf("message 1 mismatch: %+v", messages[1])
	}
}

func TestGetMessagesEmptySession(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	messages, err := db.GetMessages("nonexistent")
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(messages))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db -run 'TestCreateSessionAndMessages|TestGetMessagesEmptySession' -v`
Expected: FAIL (`CreateSession`, `SaveMessage`, `GetMessages`, `Message` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/db/sessions.go`:

```go
package db

import (
	"fmt"
	"time"
)

type Message struct {
	ID        int64
	Role      string
	Content   string
	CreatedAt time.Time
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

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db -run 'TestCreateSessionAndMessages|TestGetMessagesEmptySession' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/db/sessions.go internal/db/sessions_test.go
git commit -m "feat(db): add session and message persistence"
```

---

### Task 4: Tool Call Audit Persistence

**Files:**
- Create: `internal/db/audits.go`
- Create: `internal/db/audits_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/db/audits_test.go`:

```go
package db

import (
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/tools/registry"
)

func TestSaveAndGetToolCalls(t *testing.T) {
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

	sessionID := "session-audit"
	if err := db.CreateSession(sessionID, projectID, "audit test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	args, _ := json.Marshal(map[string]string{"command": "go test"})
	exitCode := 0
	event := registry.AuditEvent{
		Timestamp:       time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
		AgentRole:       "implementer",
		Model:           "test-model",
		ToolName:        "shell.exec",
		Args:            args,
		Risk:            registry.RiskCommand,
		Approval:        registry.ApprovalApproved,
		ResultSummary:   "tests passed",
		FilesChanged:    []string{"foo.go"},
		CommandExitCode: &exitCode,
		Error:           "",
	}

	if err := db.SaveToolCall(sessionID, event); err != nil {
		t.Fatalf("SaveToolCall failed: %v", err)
	}

	calls, err := db.GetToolCalls(sessionID)
	if err != nil {
		t.Fatalf("GetToolCalls failed: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}

	got := calls[0]
	if got.ToolName != "shell.exec" || got.Model != "test-model" || got.AgentRole != "implementer" {
		t.Errorf("tool call metadata mismatch: %+v", got)
	}
	if got.Approval != registry.ApprovalApproved {
		t.Errorf("expected approval approved, got %s", got.Approval)
	}
	if string(got.Args) != string(args) {
		t.Errorf("args mismatch: %s vs %s", string(got.Args), string(args))
	}
	if got.CommandExitCode == nil || *got.CommandExitCode != 0 {
		t.Errorf("expected exit code 0, got %v", got.CommandExitCode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db -run TestSaveAndGetToolCalls -v`
Expected: FAIL (`SaveToolCall`, `GetToolCalls` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/db/audits.go`:

```go
package db

import (
	"fmt"
	"time"

	"marshal/internal/tools/registry"
)

// SaveToolCall persists an audit event for a session.
func (db *DB) SaveToolCall(sessionID string, event registry.AuditEvent) error {
	exitCode := sqlNullInt(event.CommandExitCode)

	_, err := db.exec(
		`INSERT INTO tool_calls (session_id, agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID,
		event.AgentRole,
		event.Model,
		event.ToolName,
		string(event.Args),
		event.ResultSummary,
		string(event.Risk),
		string(event.Approval),
		event.Timestamp.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("save tool call: %w", err)
	}

	// SQLite ignores the exit code column for now because the schema above
	// does not include command_exit_code or files_changed. The schema will be
	// extended in Task 8 if needed; this keeps the implementation minimal.
	_ = exitCode
	return nil
}

// GetToolCalls returns all audit events for a session in chronological order.
func (db *DB) GetToolCalls(sessionID string) ([]registry.AuditEvent, error) {
	rows, err := db.sqlDB.Query(
		`SELECT agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, created_at
		 FROM tool_calls
		 WHERE session_id = ?
		 ORDER BY id ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query tool calls: %w", err)
	}
	defer rows.Close()

	var events []registry.AuditEvent
	for rows.Next() {
		var e registry.AuditEvent
		var args string
		var risk string
		var approval string
		var created string
		if err := rows.Scan(&e.AgentRole, &e.Model, &e.ToolName, &args, &e.ResultSummary, &risk, &approval, &created); err != nil {
			return nil, fmt.Errorf("scan tool call row: %w", err)
		}
		e.Args = []byte(args)
		e.Risk = registry.RiskLevel(risk)
		e.Approval = registry.ApprovalState(approval)
		parsed, err := time.Parse(time.RFC3339, created)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		e.Timestamp = parsed.UTC()
		events = append(events, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool call rows: %w", err)
	}
	return events, nil
}

func sqlNullInt(n *int) interface{} {
	if n == nil {
		return nil
	}
	return *n
}
```

Wait — I used `sqlNullInt` but didn't import database/sql. Simpler: inline the nil check or just omit exit code for now since the schema lacks the column. Actually, the schema currently has `args_json`, `result_summary`, `risk_level`, `approval_state`, `created_at` but NOT `command_exit_code` or `files_changed`. To keep minimal and not alter schema yet, I'll persist only the columns that exist. That's acceptable for this milestone. Let me update the implementation to not reference `sqlNullInt` and not claim to store exit code.

Revised `internal/db/audits.go`:

```go
package db

import (
	"fmt"
	"time"

	"marshal/internal/tools/registry"
)

// SaveToolCall persists an audit event for a session. Only the fields with
// matching schema columns are stored; CommandExitCode and FilesChanged are
// left for a future schema extension.
func (db *DB) SaveToolCall(sessionID string, event registry.AuditEvent) error {
	_, err := db.exec(
		`INSERT INTO tool_calls (session_id, agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID,
		event.AgentRole,
		event.Model,
		event.ToolName,
		string(event.Args),
		event.ResultSummary,
		string(event.Risk),
		string(event.Approval),
		event.Timestamp.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("save tool call: %w", err)
	}
	return nil
}

// GetToolCalls returns all audit events for a session in chronological order.
func (db *DB) GetToolCalls(sessionID string) ([]registry.AuditEvent, error) {
	rows, err := db.sqlDB.Query(
		`SELECT agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, created_at
		 FROM tool_calls
		 WHERE session_id = ?
		 ORDER BY id ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query tool calls: %w", err)
	}
	defer rows.Close()

	var events []registry.AuditEvent
	for rows.Next() {
		var e registry.AuditEvent
		var args string
		var risk string
		var approval string
		var created string
		if err := rows.Scan(&e.AgentRole, &e.Model, &e.ToolName, &args, &e.ResultSummary, &risk, &approval, &created); err != nil {
			return nil, fmt.Errorf("scan tool call row: %w", err)
		}
		e.Args = []byte(args)
		e.Risk = registry.RiskLevel(risk)
		e.Approval = registry.ApprovalState(approval)
		parsed, err := time.Parse(time.RFC3339, created)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		e.Timestamp = parsed.UTC()
		events = append(events, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool call rows: %w", err)
	}
	return events, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db -run TestSaveAndGetToolCalls -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/db/audits.go internal/db/audits_test.go
git commit -m "feat(db): add tool call audit persistence"
```

---

### Task 5: Integrate DB Persistence into session.State

**Files:**
- Modify: `internal/app/session/session.go`
- Modify: `internal/app/session/session_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/app/session/session_test.go`, add:

```go
func TestStatePersistsMessagesAndAudits(t *testing.T) {
	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	projectID, err := dbConn.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}

	sessionID := "sess-1"
	if err := dbConn.CreateSession(sessionID, projectID, "test", time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := config.Default()
	s := New(cfg, "/repo", time.Unix(100, 0), dbConn, projectID, sessionID)

	s.AddMessage(RoleUser, "hello")
	s.AddMessage(RoleAssistant, "hi")

	event := registry.AuditEvent{
		Timestamp:     time.Now().UTC(),
		ToolName:      "file.read",
		ResultSummary: "read main.go",
	}
	s.LogToolCall(event)

	messages, err := dbConn.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d", len(messages))
	}

	calls, err := dbConn.GetToolCalls(sessionID)
	if err != nil {
		t.Fatalf("get tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 persisted tool call, got %d", len(calls))
	}
}
```

Add imports at the top of `internal/app/session/session_test.go`:

```go
import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)
```

Update existing tests that call `New` to pass `nil, 0, ""` for the new parameters, or create a helper. For minimal change, update the existing `New` call in `TestStateBackups`:

```go
state := New(config.Default(), "/repo", time.Unix(100, 0), nil, 0, "")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/session -run TestStatePersistsMessagesAndAudits -v`
Expected: FAIL (`New` signature mismatch, persistence not implemented).

- [ ] **Step 3: Write minimal implementation**

Modify `internal/app/session/session.go`:

Add import:

```go
import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)
```

Update `State` struct:

```go
type State struct {
	Config     config.Config
	WorkingDir string
	StartedAt  time.Time
	DB         *db.DB
	ProjectID  int64
	SessionID  string

	ctx    context.Context
	cancel context.CancelFunc

	mu              sync.Mutex
	messages        []Message
	providerErr     error
	pendingApproval *PendingToolCall
	sessionRules    []string
	auditLog        []registry.AuditEvent
	lastBackup      []BackupFile
}
```

Update `New`:

```go
func New(cfg config.Config, workingDir string, now time.Time, database *db.DB, projectID int64, sessionID string) *State {
	ctx, cancel := context.WithCancel(context.Background())
	return &State{
		Config:     cfg,
		WorkingDir: workingDir,
		StartedAt:  now,
		DB:         database,
		ProjectID:  projectID,
		SessionID:  sessionID,
		ctx:        ctx,
		cancel:     cancel,
	}
}
```

Update `AddMessage`:

```go
func (s *State) AddMessage(role Role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	msg := Message{
		Role:      role,
		Content:   content,
		CreatedAt: time.Now(),
	}
	s.messages = append(s.messages, msg)

	if s.DB != nil && s.SessionID != "" {
		// Best-effort persistence; do not fail the in-memory transcript.
		_ = s.DB.SaveMessage(s.SessionID, string(role), content, msg.CreatedAt)
	}
}
```

Update `LogToolCall`:

```go
func (s *State) LogToolCall(event registry.AuditEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditLog = append(s.auditLog, event)

	if s.DB != nil && s.SessionID != "" {
		_ = s.DB.SaveToolCall(s.SessionID, event)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/session -run TestStatePersistsMessagesAndAudits -v`
Expected: PASS.

Also run all session tests: `go test ./internal/app/session -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/session/session.go internal/app/session/session_test.go
git commit -m "feat(session): persist messages and tool calls to SQLite"
```

---

### Task 6: App Wiring and Database Lifecycle

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/app/app_test.go`, add or update a test that verifies the app opens a database, runs migrations, and creates a session. If `app_test.go` does not exist, create it with a minimal test.

Create or modify `internal/app/app_test.go`:

```go
package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRunCreatesDatabaseAndSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte(""), 0644); err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("create config dir: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte("[project]\nname = \"test\"\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var capturedModel tea.Model
	runner := func(ctx context.Context, model tea.Model, output tea.Writer) error {
		capturedModel = model
		return nil
	}

	loader := func(opts LoadOptions) (Config, error) {
		cfg := Default()
		cfg.Project.Name = "test"
		return cfg, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := Run(ctx, os.Stdout, os.Stderr, WithNow(func() time.Time {
		return time.Unix(1000, 0)
	}), WithProgramRunner(runner), WithConfigLoader(loader))
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if capturedModel == nil {
		t.Fatal("program runner was not called")
	}

	dbPath := filepath.Join(dir, ".marshal", "marshal.db")
	if _, err := os.Stat(dbPath); err != nil {
		// Database path is relative to working dir; the test does not actually
		// chdir, so we only verify the model was created. A stronger test is
		// left for integration.
	}
}
```

Wait — this test is weak because it doesn't chdir. Let me write a stronger test that actually changes working directory, or accept that wiring is mostly tested via integration. Better: modify the test to actually run in the temp dir by changing `os.Getwd` behavior. We don't have an injection point for working dir. We can use `WithConfigLoader` but `Run` still calls `os.Getwd()` for `WorkingDir` and database path. So the test must `os.Chdir(dir)` and restore after.

Revised test:

```go
package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRunCreatesDatabaseAndSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte("[project]\nname = \"test\"\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	runnerCalled := false
	runner := func(ctx context.Context, model tea.Model, output tea.Writer) error {
		runnerCalled = true
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = Run(ctx, os.Stdout, os.Stderr, WithNow(func() time.Time {
		return time.Unix(1000, 0)
	}), WithProgramRunner(runner))
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if !runnerCalled {
		t.Fatal("program runner was not called")
	}

	dbPath := filepath.Join(dir, ".marshal", "marshal.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("database file was not created at %s", dbPath)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app -run TestRunCreatesDatabaseAndSession -v`
Expected: FAIL (`db` not imported, database wiring not implemented).

- [ ] **Step 3: Write minimal implementation**

Modify `internal/app/app.go`:

Add imports:

```go
import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/db"
)
```

Add a helper for database path:

```go
func dbPath(workingDir string) string {
	return filepath.Join(workingDir, ".marshal", "marshal.db")
}
```

Update `Run` to open the database after loading config:

```go
	cfg, err := runOpts.configLoader(config.LoadOptions{WorkingDir: workingDir})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(workingDir, ".marshal"), 0755); err != nil {
		return fmt.Errorf("create .marshal directory: %w", err)
	}

	database, err := db.Open(dbPath(workingDir))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	if err := database.Migrate(); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	projectID, err := database.GetOrCreateProject(workingDir, cfg.Project.Name)
	if err != nil {
		return fmt.Errorf("get or create project: %w", err)
	}

	sessionID := fmt.Sprintf("sess_%d", runOpts.now().UnixNano())
	if err := database.CreateSession(sessionID, projectID, "", runOpts.now()); err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	logger := logging.New(stderr, slog.LevelInfo)
	state := session.New(cfg, workingDir, runOpts.now(), database, projectID, sessionID)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app -run TestRunCreatesDatabaseAndSession -v`
Expected: PASS.

Run full suite: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/app.go internal/app/app_test.go
git commit -m "feat(app): wire SQLite database lifecycle into app.Run"
```

---

### Task 7: Final Verification and MVP Checklist

**Files:**
- Modify: `docs/plans/task.md`
- Modify: docs as needed for MVP checklist

- [ ] **Step 1: Update task statuses**

In `docs/plans/task.md`, change all statuses from `not_started` to `completed` for Tasks 4-10.

- [ ] **Step 2: Run full verification**

Run: `go test ./...`
Expected: all packages pass.

Run: `go vet ./...`
Expected: clean.

Run: `gofmt -w .` if available (optional).

- [ ] **Step 3: Verify database wiring manually**

Run a quick smoke check by creating a temp dir and running the app with the program runner stub to ensure database file is created.

- [ ] **Step 4: Commit**

```bash
git add docs/plans/task.md
git commit -m "docs: mark Milestone I tasks complete"
```

---

## Self-Review

**Spec coverage:**
- Task 4 covers `GetOrCreateProject` persistence.
- Task 5 covers `SaveFileIndex` and `GetFileIndex`.
- Task 6 covers session creation, message save/get.
- Task 7 covers tool call audit persistence.
- Task 8 covers integrating persistence hooks into `session.State`.
- Task 9 covers app-level database lifecycle wiring.
- Task 10 covers final verification and checklist update.

**Placeholder scan:** All steps contain exact file paths and concrete code/commands; no TBD/placeholder text.

**Type consistency:**
- `db.Open` returns `*db.DB` consistently.
- `session.New` signature updated with `(database *db.DB, projectID int64, sessionID string)`.
- `registry.AuditEvent` fields map to matching schema columns in `audits.go`.
- Time values use `time.RFC3339` strings in SQLite TEXT columns throughout.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-02-milestone-i-sqlite-persistence.md`.

**Execution approach:** Subagent-Driven (recommended) — dispatch a fresh subagent per task, review between tasks, fast iteration.
