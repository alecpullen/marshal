# Milestone I: SQLite Persistence Implementation Plan

> **For Antigravity:** REQUIRED WORKFLOW: Use `.agent/workflows/execute-plan.md` to execute this plan in single-flow mode.

**Goal:** Implement SQLite database persistence in Marshal to store projects, files metadata, agent sessions, messages, and tool calls, allowing session restoration and project activity logging.

**Architecture:** Create an `internal/db` package that encapsulates SQLite connection, database migrations, and repository queries (projects, files, sessions, messages, and tool calls). Integrate the DB client optionally into `session.State` (falling back to memory-only when DB is nil to maintain unit test compatibility), and wire the database lifecycle in `internal/app/app.go`.

**Tech Stack:** Go 1.26.1, SQLite 3 (using `modernc.org/sqlite` driver for CGO-free setup), standard library `database/sql`.

---

## Task 1: Add SQLite Driver to `go.mod`

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

**Step 1: Add SQLite dependency**

Propose running `go get` to fetch the pure Go SQLite driver:
Run: `go get modernc.org/sqlite`
Expected: downloads the package and adds it to `go.mod`.

**Step 2: Verify compilation**

Run: `go test ./...`
Expected: PASS

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore(db): add modernc.org/sqlite dependency"
```

---

## Task 2: Create DB package and connection lifecycle

**Files:**
- Create: `internal/db/db.go`
- Create: `internal/db/db_test.go`

**Interfaces:**
- Produces: `type DB struct { sqlDB *sql.DB }`; `func Open(path string) (*DB, error)`; `func (db *DB) Close() error`.

**Step 1: Write failing tests for DB connection**

Create `internal/db/db_test.go`:
```go
package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAndClose(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	if db.sqlDB == nil {
		t.Fatal("sql.DB is nil")
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("Database file was not created on disk")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/db`
Expected: FAIL - package/methods not defined.

**Step 3: Implement DB open/close**

Create `internal/db/db.go`:
```go
package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type DB struct {
	sqlDB *sql.DB
}

func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite db: %w", err)
	}

	return &DB{sqlDB: sqlDB}, nil
}

func (db *DB) Close() error {
	if db.sqlDB == nil {
		return nil
	}
	return db.sqlDB.Close()
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/db`
Expected: PASS

**Step 5: Commit**

```bash
gofmt -w internal/db
git add internal/db/db.go internal/db/db_test.go
git commit -m "feat(db): implement SQLite database open and close lifecycle"
```

---

## Task 3: Database Migrations

**Files:**
- Modify: `internal/db/db.go`
- Create: `internal/db/migrations.go`
- Modify: `internal/db/db_test.go`

**Interfaces:**
- Produces: `func (db *DB) Migrate() error`.

**Step 1: Write failing tests for migrations**

Add to `internal/db/db_test.go`:
```go
func TestMigrateCreatesTables(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// Verify tables exist
	tables := []string{"projects", "files", "agent_sessions", "messages", "tool_calls"}
	for _, table := range tables {
		var name string
		err := db.sqlDB.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s not found: %v", table, err)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/db`
Expected: FAIL - `Migrate` undefined.

**Step 3: Implement database migrations**

Create `internal/db/migrations.go`:
```go
package db

const schema = `
CREATE TABLE IF NOT EXISTS projects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    root_path TEXT UNIQUE NOT NULL,
    name TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    language TEXT,
    hash TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    last_indexed_at TEXT NOT NULL,
    UNIQUE(project_id, path)
);

CREATE TABLE IF NOT EXISTS agent_sessions (
    id TEXT PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    summary TEXT
);

CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tool_calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT REFERENCES agent_sessions(id) ON DELETE CASCADE,
    agent_role TEXT,
    model TEXT,
    tool_name TEXT,
    args_json TEXT,
    result_summary TEXT,
    risk_level TEXT,
    approval_state TEXT,
    created_at TEXT NOT NULL
);
`
```

Modify `internal/db/db.go` to add `Migrate()`:
```go
func (db *DB) Migrate() error {
	_, err := db.sqlDB.Exec(schema)
	if err != nil {
		return fmt.Errorf("execute database schema migrations: %w", err)
	}
	return nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/db`
Expected: PASS

**Step 5: Commit**

```bash
gofmt -w internal/db
git add internal/db/migrations.go internal/db/db.go internal/db/db_test.go
git commit -m "feat(db): add migrations for core schema tables"
```

---

## Task 4: Project Storage Methods

**Files:**
- Create: `internal/db/projects.go`
- Modify: `internal/db/db_test.go`

**Interfaces:**
- Produces: `type Project struct { ID int; RootPath string; Name string; CreatedAt time.Time; UpdatedAt time.Time }`; `func (db *DB) GetOrCreateProject(rootPath string, name string) (*Project, error)`.

**Step 1: Write failing test for projects**

Add to `internal/db/db_test.go`:
```go
func TestGetOrCreateProject(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	_ = db.Migrate()

	p1, err := db.GetOrCreateProject("/workspace/path", "test-project")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	if p1.RootPath != "/workspace/path" || p1.Name != "test-project" {
		t.Fatalf("p1 invalid: %+v", p1)
	}

	p2, err := db.GetOrCreateProject("/workspace/path", "another-name")
	if err != nil {
		t.Fatalf("second GetOrCreateProject failed: %v", err)
	}
	if p1.ID != p2.ID {
		t.Fatalf("expected same project ID, got %d and %d", p1.ID, p2.ID)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/db`
Expected: FAIL - `GetOrCreateProject` undefined.

**Step 3: Implement projects table database operations**

Create `internal/db/projects.go`:
```go
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Project struct {
	ID        int
	RootPath  string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (db *DB) GetOrCreateProject(rootPath string, name string) (*Project, error) {
	now := time.Now().Format(time.RFC3339)

	var p Project
	var createdStr, updatedStr string
	err := db.sqlDB.QueryRow("SELECT id, root_path, name, created_at, updated_at FROM projects WHERE root_path = ?", rootPath).
		Scan(&p.ID, &p.RootPath, &p.Name, &createdStr, &updatedStr)

	if err == nil {
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
		return &p, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("query project: %w", err)
	}

	res, err := db.sqlDB.Exec("INSERT INTO projects (root_path, name, created_at, updated_at) VALUES (?, ?, ?, ?)", rootPath, name, now, now)
	if err != nil {
		return nil, fmt.Errorf("insert project: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}

	parsedTime, _ := time.Parse(time.RFC3339, now)
	return &Project{
		ID:        int(id),
		RootPath:  rootPath,
		Name:      name,
		CreatedAt: parsedTime,
		UpdatedAt: parsedTime,
	}, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/db`
Expected: PASS

**Step 5: Commit**

```bash
gofmt -w internal/db
git add internal/db/projects.go internal/db/db_test.go
git commit -m "feat(db): implement GetOrCreateProject persistence"
```

---

## Task 5: File Index Metadata Storage

**Files:**
- Create: `internal/db/files.go`
- Modify: `internal/db/db_test.go`

**Interfaces:**
- Produces: `type FileIndex struct { Path string; Language string; Hash string; SizeBytes int64; LastIndexedAt time.Time }`; `func (db *DB) SaveFileIndex(projectID int, index FileIndex) error`; `func (db *DB) GetFileIndex(projectID int, path string) (*FileIndex, error)`.

**Step 1: Write failing tests for file index metadata**

Add to `internal/db/db_test.go`:
```go
func TestFileIndexPersistence(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	_ = db.Migrate()

	p, _ := db.GetOrCreateProject("/workspace", "test")
	idx := FileIndex{
		Path:          "main.go",
		Language:      "go",
		Hash:          "abc123hash",
		SizeBytes:     500,
		LastIndexedAt: time.Unix(100, 0),
	}

	if err := db.SaveFileIndex(p.ID, idx); err != nil {
		t.Fatalf("SaveFileIndex failed: %v", err)
	}

	retrieved, err := db.GetFileIndex(p.ID, "main.go")
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if retrieved.Hash != "abc123hash" || retrieved.SizeBytes != 500 {
		t.Fatalf("retrieved index mismatch: %+v", retrieved)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/db`
Expected: FAIL - `SaveFileIndex` undefined.

**Step 3: Implement files table database operations**

Create `internal/db/files.go`:
```go
package db

import (
	"database/sql"
	"errors"
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

func (db *DB) SaveFileIndex(projectID int, index FileIndex) error {
	lastIndexed := index.LastIndexedAt.Format(time.RFC3339)

	_, err := db.sqlDB.Exec(`
		INSERT INTO files (project_id, path, language, hash, size_bytes, last_indexed_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, path) DO UPDATE SET
			language = excluded.language,
			hash = excluded.hash,
			size_bytes = excluded.size_bytes,
			last_indexed_at = excluded.last_indexed_at
	`, projectID, index.Path, index.Language, index.Hash, index.SizeBytes, lastIndexed)

	if err != nil {
		return fmt.Errorf("save file index: %w", err)
	}
	return nil
}

func (db *DB) GetFileIndex(projectID int, path string) (*FileIndex, error) {
	var idx FileIndex
	var lastIndexedStr string

	err := db.sqlDB.QueryRow(`
		SELECT path, language, hash, size_bytes, last_indexed_at
		FROM files WHERE project_id = ? AND path = ?
	`, projectID, path).Scan(&idx.Path, &idx.Language, &idx.Hash, &idx.SizeBytes, &lastIndexedStr)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get file index: %w", err)
	}

	idx.LastIndexedAt, _ = time.Parse(time.RFC3339, lastIndexedStr)
	return &idx, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/db`
Expected: PASS

**Step 5: Commit**

```bash
gofmt -w internal/db
git add internal/db/files.go internal/db/db_test.go
git commit -m "feat(db): implement file index metadata persistence"
```

---

## Task 6: Session and Message Persistence

**Files:**
- Create: `internal/db/sessions.go`
- Modify: `internal/db/db_test.go`

**Interfaces:**
- Produces: `type Session struct { ID string; ProjectID int; Title string; StartedAt time.Time; EndedAt *time.Time; Summary string }`; `type DBMessage struct { Role string; Content string; CreatedAt time.Time }`; `func (db *DB) CreateSession(session Session) error`; `func (db *DB) UpdateSessionSummary(sessionID string, summary string, endedAt time.Time) error`; `func (db *DB) SaveMessage(sessionID string, msg DBMessage) error`; `func (db *DB) GetSessionMessages(sessionID string) ([]DBMessage, error)`.

**Step 1: Write failing tests for sessions and messages**

Add to `internal/db/db_test.go`:
```go
func TestSessionAndMessagePersistence(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	_ = db.Migrate()

	p, _ := db.GetOrCreateProject("/workspace", "test")
	sess := Session{
		ID:        "session-abc",
		ProjectID: p.ID,
		Title:     "fixing CLI bug",
		StartedAt: time.Unix(100, 0),
	}

	if err := db.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	msg := DBMessage{
		Role:      "user",
		Content:   "Hello database",
		CreatedAt: time.Unix(200, 0),
	}

	if err := db.SaveMessage(sess.ID, msg); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	msgs, err := db.GetSessionMessages(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionMessages failed: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "Hello database" {
		t.Fatalf("messages mismatch: %+v", msgs)
	}

	endTime := time.Unix(300, 0)
	if err := db.UpdateSessionSummary(sess.ID, "fixed bug", endTime); err != nil {
		t.Fatalf("UpdateSessionSummary failed: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/db`
Expected: FAIL - `CreateSession` undefined.

**Step 3: Implement session and message database operations**

Create `internal/db/sessions.go`:
```go
package db

import (
	"fmt"
	"time"
)

type Session struct {
	ID        string
	ProjectID int
	Title     string
	StartedAt time.Time
	EndedAt   *time.Time
	Summary   string
}

type DBMessage struct {
	Role      string
	Content   string
	CreatedAt time.Time
}

func (db *DB) CreateSession(session Session) error {
	startedAt := session.StartedAt.Format(time.RFC3339)

	_, err := db.sqlDB.Exec(`
		INSERT INTO agent_sessions (id, project_id, title, started_at, summary)
		VALUES (?, ?, ?, ?, ?)
	`, session.ID, session.ProjectID, session.Title, startedAt, session.Summary)

	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (db *DB) UpdateSessionSummary(sessionID string, summary string, endedAt time.Time) error {
	endedStr := endedAt.Format(time.RFC3339)

	_, err := db.sqlDB.Exec(`
		UPDATE agent_sessions SET summary = ?, ended_at = ? WHERE id = ?
	`, summary, endedStr, sessionID)

	if err != nil {
		return fmt.Errorf("update session summary: %w", err)
	}
	return nil
}

func (db *DB) SaveMessage(sessionID string, msg DBMessage) error {
	createdStr := msg.CreatedAt.Format(time.RFC3339)

	_, err := db.sqlDB.Exec(`
		INSERT INTO messages (session_id, role, content, created_at)
		VALUES (?, ?, ?, ?)
	`, sessionID, msg.Role, msg.Content, createdStr)

	if err != nil {
		return fmt.Errorf("save message: %w", err)
	}
	return nil
}

func (db *DB) GetSessionMessages(sessionID string) ([]DBMessage, error) {
	rows, err := db.sqlDB.Query(`
		SELECT role, content, created_at FROM messages
		WHERE session_id = ? ORDER BY id ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session messages: %w", err)
	}
	defer rows.Close()

	var msgs []DBMessage
	for rows.Next() {
		var msg DBMessage
		var createdStr string
		if err := rows.Scan(&msg.Role, &msg.Content, &createdStr); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		msg.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		msgs = append(msgs, msg)
	}
	return msgs, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/db`
Expected: PASS

**Step 5: Commit**

```bash
gofmt -w internal/db
git add internal/db/sessions.go internal/db/db_test.go
git commit -m "feat(db): implement session and message persistence"
```

---

## Task 7: Tool Call Audit Persistence

**Files:**
- Create: `internal/db/tool_calls.go`
- Modify: `internal/db/db_test.go`

**Interfaces:**
- Produces: `type DBToolCall struct { SessionID string; AgentRole string; Model string; ToolName string; ArgsJSON string; ResultSummary string; RiskLevel string; ApprovalState string; CreatedAt time.Time }`; `func (db *DB) SaveToolCall(call DBToolCall) error`; `func (db *DB) GetSessionToolCalls(sessionID string) ([]DBToolCall, error)`.

**Step 1: Write failing tests for tool calls**

Add to `internal/db/db_test.go`:
```go
func TestToolCallPersistence(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	_ = db.Migrate()

	p, _ := db.GetOrCreateProject("/workspace", "test")
	sess := Session{ID: "session-xyz", ProjectID: p.ID, StartedAt: time.Unix(100, 0)}
	_ = db.CreateSession(sess)

	call := DBToolCall{
		SessionID:     sess.ID,
		AgentRole:     "implementer",
		Model:         "qwen",
		ToolName:      "shell.run",
		ArgsJSON:      `{"command":"echo"}`,
		ResultSummary: "ran successfully",
		RiskLevel:     "command",
		ApprovalState: "approved",
		CreatedAt:     time.Unix(200, 0),
	}

	if err := db.SaveToolCall(call); err != nil {
		t.Fatalf("SaveToolCall failed: %v", err)
	}

	calls, err := db.GetSessionToolCalls(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionToolCalls failed: %v", err)
	}
	if len(calls) != 1 || calls[0].ToolName != "shell.run" {
		t.Fatalf("tool calls mismatch: %+v", calls)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/db`
Expected: FAIL - `SaveToolCall` undefined.

**Step 3: Implement tool calls database operations**

Create `internal/db/tool_calls.go`:
```go
package db

import (
	"fmt"
	"time"
)

type DBToolCall struct {
	SessionID     string
	AgentRole     string
	Model         string
	ToolName      string
	ArgsJSON      string
	ResultSummary string
	RiskLevel     string
	ApprovalState string
	CreatedAt     time.Time
}

func (db *DB) SaveToolCall(call DBToolCall) error {
	createdStr := call.CreatedAt.Format(time.RFC3339)

	_, err := db.sqlDB.Exec(`
		INSERT INTO tool_calls (session_id, agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, call.SessionID, call.AgentRole, call.Model, call.ToolName, call.ArgsJSON, call.ResultSummary, call.RiskLevel, call.ApprovalState, createdStr)

	if err != nil {
		return fmt.Errorf("save tool call: %w", err)
	}
	return nil
}

func (db *DB) GetSessionToolCalls(sessionID string) ([]DBToolCall, error) {
	rows, err := db.sqlDB.Query(`
		SELECT session_id, agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, created_at
		FROM tool_calls WHERE session_id = ? ORDER BY id ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session tool calls: %w", err)
	}
	defer rows.Close()

	var calls []DBToolCall
	for rows.Next() {
		var call DBToolCall
		var createdStr string
		err := rows.Scan(
			&call.SessionID, &call.AgentRole, &call.Model, &call.ToolName,
			&call.ArgsJSON, &call.ResultSummary, &call.RiskLevel, &call.ApprovalState,
			&createdStr,
		)
		if err != nil {
			return nil, fmt.Errorf("scan tool call: %w", err)
		}
		call.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		calls = append(calls, call)
	}
	return calls, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/db`
Expected: PASS

**Step 5: Commit**

```bash
gofmt -w internal/db
git add internal/db/tool_calls.go internal/db/db_test.go
git commit -m "feat(db): implement tool call audit log persistence"
```

---

## Task 8: Integrate DB Persistence into `session.State`

**Files:**
- Modify: `internal/app/session/session.go`
- Modify: `internal/app/session/session_test.go`

**Interfaces:**
- Produces: `func (s *State) SetDB(database *db.DB, projectID int, sessionID string)` so the state can save data dynamically; update `AddMessage` and `LogToolCall` to write to SQLite when DB is configured.

**Step 1: Write failing tests in `session_test.go`**

Add to `internal/app/session/session_test.go`:
```go
import (
	"marshal/internal/db"
)

func TestStateDatabaseIntegration(t *testing.T) {
	tempDB, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open temp db failed: %v", err)
	}
	defer tempDB.Close()
	_ = tempDB.Migrate()

	proj, _ := tempDB.GetOrCreateProject("/repo", "test-project")
	sessID := "session-uuid"
	_ = tempDB.CreateSession(db.Session{ID: sessID, ProjectID: proj.ID, StartedAt: time.Now()})

	state := New(config.Default(), "/repo", time.Now())
	state.SetDB(tempDB, proj.ID, sessID)

	state.AddMessage(RoleUser, "Database integration message")
	event := registry.NewAuditEvent(time.Now(), registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly}, registry.ToolCall{Name: "file.read", Args: []byte(`{"path":"a"}`)}, registry.ToolResult{Summary: "read file"}, registry.ApprovalNotRequired, nil)
	state.LogToolCall(event)

	// Verify database contains the record
	msgs, err := tempDB.GetSessionMessages(sessID)
	if err != nil || len(msgs) != 1 || msgs[0].Content != "Database integration message" {
		t.Fatalf("message was not persisted to SQLite: %v, messages: %+v", err, msgs)
	}

	calls, err := tempDB.GetSessionToolCalls(sessID)
	if err != nil || len(calls) != 1 || calls[0].ToolName != "file.read" {
		t.Fatalf("tool call was not persisted to SQLite: %v, calls: %+v", err, calls)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/app/session`
Expected: FAIL - `SetDB` undefined.

**Step 3: Modify `session.State` to support SQLite integration**

In `internal/app/session/session.go`, add fields to `State`:
```go
	db        *db.DB
	projectID int
	sessionID string
```

Add helper `SetDB` to `internal/app/session/session.go`:
```go
func (s *State) SetDB(database *db.DB, projectID int, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db = database
	s.projectID = projectID
	s.sessionID = sessionID
}

func (s *State) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}
```

Modify `AddMessage` in `internal/app/session/session.go` to save to DB:
```go
func (s *State) AddMessage(role Role, content string) {
	s.mu.Lock()
	dbClient := s.db
	sessID := s.sessionID
	s.messages = append(s.messages, Message{
		Role:      role,
		Content:   content,
		CreatedAt: time.Now(),
	})
	s.mu.Unlock()

	if dbClient != nil && sessID != "" {
		_ = dbClient.SaveMessage(sessID, db.DBMessage{
			Role:      string(role),
			Content:   content,
			CreatedAt: time.Now(),
		})
	}
}
```

Modify `LogToolCall` in `internal/app/session/session.go` to save to DB:
```go
func (s *State) LogToolCall(event registry.AuditEvent) {
	s.mu.Lock()
	dbClient := s.db
	sessID := s.sessionID
	s.auditLog = append(s.auditLog, event)
	s.mu.Unlock()

	if dbClient != nil && sessID != "" {
		_ = dbClient.SaveToolCall(db.DBToolCall{
			SessionID:     sessID,
			ToolName:      event.ToolName,
			ArgsJSON:      string(event.Args),
			ResultSummary: event.ResultSummary,
			RiskLevel:     string(event.Risk),
			ApprovalState: string(event.Approval),
			CreatedAt:     event.Timestamp,
		})
	}
}
```

Make sure imports in `internal/app/session/session.go` are updated to include `"marshal/internal/db"`.

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/session`
Expected: PASS

**Step 5: Commit**

```bash
gofmt -w internal/app/session
git add internal/app/session/session.go internal/app/session/session_test.go
git commit -m "feat(session): persist messages and tool calls to database when available"
```

---

## Task 9: App Wiring and Database Lifecycle

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Step 1: Write tests to verify database initialization in app.go**

Modify `internal/app/app_test.go` to ensure `Run` successfully initializes config database lifecycle without crashing.

**Step 2: Wire SQLite connection and migration lifecycle into `Run`**

In `internal/app/app.go`, import `marshal/internal/db`. Update the beginning of `Run` to load the database, run migrations, and associate the database with `session.State`.

```go
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}

	dbDir := filepath.Join(homeDir, ".config", "marshal")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("create config/database directory: %w", err)
	}

	database, err := db.Open(filepath.Join(dbDir, "marshal.db"))
	if err != nil {
		return fmt.Errorf("open sqlite database: %w", err)
	}
	defer database.Close()

	if err := database.Migrate(); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	project, err := database.GetOrCreateProject(workingDir, cfg.Project.Name)
	if err != nil {
		return fmt.Errorf("get or create project record: %w", err)
	}

	sessionID := fmt.Sprintf("sess_%d", runOpts.now().UnixNano())
	err = database.CreateSession(db.Session{
		ID:        sessionID,
		ProjectID: project.ID,
		Title:     fmt.Sprintf("Session at %s", runOpts.now().Format(time.RFC822)),
		StartedAt: runOpts.now(),
	})
	if err != nil {
		return fmt.Errorf("create agent session: %w", err)
	}

	state.SetDB(database, project.ID, sessionID)
```

Make sure that on shutdown (or return of `Run`), `database.UpdateSessionSummary` is called with a summary of the session:
```go
	defer func() {
		// Log end of session
		messages := state.Messages()
		summary := "Empty session"
		if len(messages) > 0 {
			summary = fmt.Sprintf("Chat with %d messages", len(messages))
		}
		_ = database.UpdateSessionSummary(sessionID, summary, time.Now())
	}()
```

**Step 3: Run full app tests to verify they pass**

Run: `go test ./internal/app`
Expected: PASS

**Step 4: Commit**

```bash
gofmt -w internal/app
git add internal/app/app.go internal/app/app_test.go
git commit -m "feat(app): wire SQLite database lifecycle, migrations, and session persistence"
```

---

## Task 10: Final Verification and MVP Checklist

**Files:**
- Modify: `docs/10-mvp-implementation-checklist.md`

**Step 1: Check off Milestone I items**

In `docs/10-mvp-implementation-checklist.md`, mark all items under `Milestone I: SQLite persistence` as complete `[x]`.

**Step 2: Run verification commands**

Run:
```bash
gofmt -l .
go build ./...
go vet ./...
go test ./... -race
```
Expected: formatting has zero output, build succeeds silently, vet succeeds silently, and all tests pass with no race conditions detected.

**Step 3: Commit**

```bash
git add docs/10-mvp-implementation-checklist.md
git commit -m "docs: complete Milestone I checklist items"
```
