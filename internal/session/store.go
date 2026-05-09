package session

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // register "sqlite" driver

	"github.com/alecpullen/marshal/internal/context"
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id                 TEXT PRIMARY KEY,
    target_branch      TEXT NOT NULL,
    target_start_sha   TEXT NOT NULL,
    staging_branch     TEXT NOT NULL,
    started_at         TEXT NOT NULL,
    shipped_at         TEXT,
    shipped_target_sha TEXT
);

CREATE TABLE IF NOT EXISTS tasks (
    id                 TEXT PRIMARY KEY,
    session_id         TEXT NOT NULL REFERENCES sessions(id),
    prompt             TEXT NOT NULL,
    parent_staging_sha TEXT NOT NULL,
    staging_sha        TEXT,
    status             TEXT NOT NULL DEFAULT 'running',
    started_at         TEXT NOT NULL,
    ended_at           TEXT,
    summary            TEXT
);

CREATE TABLE IF NOT EXISTS rounds (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id        TEXT    NOT NULL,
    task_id           TEXT    NOT NULL,
    round             INTEGER NOT NULL,
    role              TEXT    NOT NULL,
    model             TEXT    NOT NULL,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    duration_ms       INTEGER NOT NULL DEFAULT 0,
    content           TEXT    NOT NULL DEFAULT '',
    verdict_json      TEXT,
    think_blocks      TEXT
);

CREATE INDEX IF NOT EXISTS idx_tasks_session ON tasks(session_id);
CREATE INDEX IF NOT EXISTS idx_rounds_task   ON rounds(task_id);

CREATE TABLE IF NOT EXISTS plans (
    id           TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    description  TEXT,
    source_task  TEXT NOT NULL,
    goals_json   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_plans_session ON plans(session_id);

CREATE TABLE IF NOT EXISTS goals (
    id              TEXT PRIMARY KEY,
    plan_id         TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    session_id      TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    title           TEXT NOT NULL,
    description     TEXT,
    priority        TEXT NOT NULL,
    files_json      TEXT,
    depends_on_json TEXT,
    status          TEXT NOT NULL DEFAULT 'pending',
    created_at      TEXT NOT NULL,
    started_at      TEXT,
    completed_at    TEXT,
    task_id         TEXT,
    findings_json   TEXT
);
CREATE INDEX IF NOT EXISTS idx_goals_plan ON goals(plan_id);
CREATE INDEX IF NOT EXISTS idx_goals_session ON goals(session_id);
CREATE INDEX IF NOT EXISTS idx_goals_status ON goals(status);

CREATE TABLE IF NOT EXISTS read_only_files (
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    path        TEXT NOT NULL,
    added_at    TEXT NOT NULL,
    PRIMARY KEY (session_id, path)
);
CREATE INDEX IF NOT EXISTS idx_rof_session ON read_only_files(session_id);
`

// Store is a SQLite-backed ledger for sessions, tasks, and rounds.
type Store struct {
	db           *sql.DB
	contextStore *context.Store
	blobDir      string
}

// Open opens (or creates) the SQLite database at path and runs the schema
// migration. The repoRoot is required for context store blob storage.
func Open(path string, repoRoot string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// Single writer; WAL mode for better read/write concurrency.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("pragma: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	// Run migrations to handle schema changes from older versions.
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migration: %w", err)
	}

	// Apply v2 schema if needed
	if err := migrateToV2(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("v2 migration: %w", err)
	}

	// Initialize blob directory (but context store is per-session)
	var blobDir string
	if repoRoot != "" {
		blobDir = filepath.Join(repoRoot, ".marshal", "sessions")
	}

	return &Store{db: db, blobDir: blobDir}, nil
}

// OpenWithSession opens the store and initializes context store for a specific session.
func (s *Store) OpenWithSession(sessionID string) error {
	if s.blobDir == "" {
		return fmt.Errorf("blob directory not configured")
	}

	sessionBlobDir := filepath.Join(s.blobDir, sessionID, "ctx")
	ctxStore, err := context.NewStore(s.db, sessionID, sessionBlobDir)
	if err != nil {
		return fmt.Errorf("initializing context store: %w", err)
	}

	s.contextStore = ctxStore
	return nil
}

// ContextStore returns the context store (if initialized).
func (s *Store) ContextStore() *context.Store {
	return s.contextStore
}

// runMigrations applies schema changes for existing databases.
// SQLite doesn't support ALTER COLUMN, but we can add missing columns.
func runMigrations(db *sql.DB) error {
	// Check if task_id column exists in rounds table (added in a later version).
	var taskIDColCount int
	row := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('rounds') WHERE name = 'task_id'`)
	if err := row.Scan(&taskIDColCount); err != nil {
		return fmt.Errorf("checking task_id column: %w", err)
	}
	if taskIDColCount == 0 {
		// task_id column is missing - add it
		if _, err := db.Exec(`ALTER TABLE rounds ADD COLUMN task_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("adding task_id column: %w", err)
		}
		// Also need to drop and recreate the index since it depends on task_id
		if _, err := db.Exec(`DROP INDEX IF EXISTS idx_rounds_task`); err != nil {
			return fmt.Errorf("dropping old index: %w", err)
		}
		if _, err := db.Exec(`CREATE INDEX idx_rounds_task ON rounds(task_id)`); err != nil {
			return fmt.Errorf("creating task index: %w", err)
		}
	}
	return nil
}

// migrateToV2 migrates the schema to version 2 with context store tables.
func migrateToV2(db *sql.DB) error {
	// Check current schema version
	var version int
	err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
	if err != nil {
		// Table doesn't exist yet, create it
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
			return fmt.Errorf("creating schema_version table: %w", err)
		}
	}

	if version >= 2 {
		// Already at v2 or higher
		return nil
	}

	// Apply v2 schema
	if _, err := db.Exec(SchemaV2); err != nil {
		return fmt.Errorf("applying v2 schema: %w", err)
	}

	// Record migration
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (2, ?)`, now); err != nil {
		return fmt.Errorf("recording v2 migration: %w", err)
	}

	return nil
}

// Close releases the database connection and context store.
func (s *Store) Close() error {
	if s.contextStore != nil {
		s.contextStore.Close()
	}
	return s.db.Close()
}

// --- Sessions ----------------------------------------------------------------

func (s *Store) InsertSession(sess *Session) error {
	_, err := s.db.Exec(`
		INSERT INTO sessions(id, target_branch, target_start_sha, staging_branch, started_at)
		VALUES (?, ?, ?, ?, ?)`,
		sess.ID, sess.TargetBranch, sess.TargetStartSHA, sess.StagingBranch,
		sess.StartedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) ShipSession(id, newStagingBranch, shippedTargetSHA string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`
		UPDATE sessions
		SET shipped_at=?, shipped_target_sha=?, staging_branch=?
		WHERE id=?`,
		now, shippedTargetSHA, newStagingBranch, id,
	)
	return err
}

func (s *Store) GetSession(id string) (*Session, error) {
	row := s.db.QueryRow(`
		SELECT id, target_branch, target_start_sha, staging_branch,
		       started_at, shipped_at, shipped_target_sha
		FROM sessions WHERE id=?`, id)
	return scanSession(row)
}

// --- Tasks -------------------------------------------------------------------

func (s *Store) InsertTask(t *Task) error {
	_, err := s.db.Exec(`
		INSERT INTO tasks(id, session_id, prompt, parent_staging_sha, status, started_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.SessionID, t.Prompt, t.ParentStagingSHA,
		string(t.Status), t.StartedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) UpdateTask(id string, upd TaskUpdate) error {
	endedAt := ""
	if upd.EndedAt != nil {
		endedAt = upd.EndedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		UPDATE tasks
		SET status=?, staging_sha=?, ended_at=?, summary=?
		WHERE id=?`,
		string(upd.Status), upd.StagingSHA, nullStr(endedAt), upd.Summary, id,
	)
	return err
}

func (s *Store) GetTask(id string) (*Task, error) {
	row := s.db.QueryRow(`
		SELECT id, session_id, prompt, parent_staging_sha, staging_sha,
		       status, started_at, ended_at, summary
		FROM tasks WHERE id=?`, id)
	return scanTask(row)
}

// TasksForSession returns all tasks for a session ordered by start time.
func (s *Store) TasksForSession(sessionID string) ([]*Task, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, prompt, parent_staging_sha, staging_sha,
		       status, started_at, ended_at, summary
		FROM tasks WHERE session_id=? ORDER BY started_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// --- Rounds ------------------------------------------------------------------

func (s *Store) InsertRound(r *Round) error {
	_, err := s.db.Exec(`
		INSERT INTO rounds(session_id, task_id, round, role, model,
		                   prompt_tokens, completion_tokens, duration_ms,
		                   content, verdict_json, think_blocks)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.SessionID, r.TaskID, r.Round, r.Role, r.Model,
		r.PromptTokens, r.CompletionTokens, r.DurationMS,
		r.Content, r.VerdictJSON, r.ThinkBlocks,
	)
	return err
}

// RoundsForSession returns all rounds for all tasks in a session ordered by
// task ID and round number.
func (s *Store) RoundsForSession(sessionID string) ([]*Round, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, task_id, round, role, model,
		       prompt_tokens, completion_tokens, duration_ms,
		       content, verdict_json, think_blocks
		FROM rounds WHERE session_id=? ORDER BY task_id, round`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*Round
	for rows.Next() {
		r := &Round{}
		err := rows.Scan(&r.ID, &r.SessionID, &r.TaskID, &r.Round,
			&r.Role, &r.Model, &r.PromptTokens, &r.CompletionTokens,
			&r.DurationMS, &r.Content, &r.VerdictJSON, &r.ThinkBlocks)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) RoundsForTask(taskID string) ([]*Round, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, task_id, round, role, model,
		       prompt_tokens, completion_tokens, duration_ms,
		       content, verdict_json, think_blocks
		FROM rounds WHERE task_id=? ORDER BY round`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*Round
	for rows.Next() {
		r := &Round{}
		err := rows.Scan(&r.ID, &r.SessionID, &r.TaskID, &r.Round,
			&r.Role, &r.Model, &r.PromptTokens, &r.CompletionTokens,
			&r.DurationMS, &r.Content, &r.VerdictJSON, &r.ThinkBlocks)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// --- Read-only files ---------------------------------------------------------

// AddReadOnlyFile adds a file to the session's read-only list.
func (s *Store) AddReadOnlyFile(sessionID, path string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO read_only_files(session_id, path, added_at) VALUES (?, ?, ?)`,
		sessionID, path, now,
	)
	return err
}

// RemoveReadOnlyFile removes a file from the session's read-only list.
func (s *Store) RemoveReadOnlyFile(sessionID, path string) error {
	_, err := s.db.Exec(
		`DELETE FROM read_only_files WHERE session_id = ? AND path = ?`,
		sessionID, path,
	)
	return err
}

// GetReadOnlyFiles returns all read-only files for a session.
func (s *Store) GetReadOnlyFiles(sessionID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT path FROM read_only_files WHERE session_id = ? ORDER BY added_at`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		files = append(files, path)
	}
	return files, rows.Err()
}

// ClearReadOnlyFiles removes all read-only files for a session.
func (s *Store) ClearReadOnlyFiles(sessionID string) error {
	_, err := s.db.Exec(
		`DELETE FROM read_only_files WHERE session_id = ?`,
		sessionID,
	)
	return err
}

// --- Scan helpers ------------------------------------------------------------

type scanner interface {
	Scan(dest ...any) error
}

func scanSession(row scanner) (*Session, error) {
	s := &Session{}
	var startedAt string
	var shippedAt, shippedSHA sql.NullString
	err := row.Scan(&s.ID, &s.TargetBranch, &s.TargetStartSHA, &s.StagingBranch,
		&startedAt, &shippedAt, &shippedSHA)
	if err != nil {
		return nil, err
	}
	s.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
	if shippedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, shippedAt.String)
		s.ShippedAt = &t
	}
	if shippedSHA.Valid {
		s.ShippedTargetSHA = &shippedSHA.String
	}
	return s, nil
}

func scanTask(row scanner) (*Task, error) {
	t := &Task{}
	var startedAt string
	var endedAt, stagingSHA, summary sql.NullString
	err := row.Scan(&t.ID, &t.SessionID, &t.Prompt, &t.ParentStagingSHA,
		&stagingSHA, (*string)(&t.Status), &startedAt, &endedAt, &summary)
	if err != nil {
		return nil, err
	}
	t.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
	if endedAt.Valid {
		ts, _ := time.Parse(time.RFC3339Nano, endedAt.String)
		t.EndedAt = &ts
	}
	if stagingSHA.Valid {
		t.StagingSHA = &stagingSHA.String
	}
	if summary.Valid {
		t.Summary = &summary.String
	}
	return t, nil
}

// nullStr returns nil if s is empty, otherwise &s — for nullable TEXT columns.
func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// --- Plan and Goal methods ---------------------------------------------------

// Plan represents a generated fix plan from audit findings
type Plan struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	SourceTask  string    `json:"source_task"`
	GoalsJSON   string    `json:"goals_json"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Goal represents a discrete fix task
type Goal struct {
	ID           string     `json:"id"`
	PlanID       string     `json:"plan_id"`
	SessionID    string     `json:"session_id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Priority     string     `json:"priority"`
	Status       string     `json:"status"`
	FilesJSON    string     `json:"files_json"`
	DependsJSON  string     `json:"depends_on_json"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	TaskID       *string    `json:"task_id,omitempty"`
	FindingsJSON string     `json:"findings_json"`
}

// InsertPlan inserts a new plan.
func (st *Store) InsertPlan(p *Plan) error {
	_, err := st.db.Exec(`
		INSERT INTO plans (id, session_id, name, description, source_task, goals_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, p.ID, p.SessionID, p.Name, p.Description, p.SourceTask, p.GoalsJSON,
		p.CreatedAt.Format(time.RFC3339Nano), p.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// InsertGoal inserts a single goal.
func (st *Store) InsertGoal(g *Goal) error {
	_, err := st.db.Exec(`
		INSERT INTO goals (id, plan_id, session_id, title, description, priority, status,
		                   files_json, depends_on_json, created_at, started_at, completed_at, task_id, findings_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, g.ID, g.PlanID, g.SessionID, g.Title, g.Description, g.Priority, g.Status,
		g.FilesJSON, g.DependsJSON, g.CreatedAt.Format(time.RFC3339Nano),
		nullTime(g.StartedAt), nullTime(g.CompletedAt), nullStrPtr(g.TaskID), g.FindingsJSON)
	return err
}

// UpdateGoalStatus updates a goal's status and timestamps.
func (st *Store) UpdateGoalStatus(goalID string, status string, taskID *string) error {
	now := time.Now()
	var startedAt, completedAt interface{}

	if status == "active" {
		startedAt = now.Format(time.RFC3339Nano)
	}
	if status == "completed" || status == "failed" || status == "skipped" {
		completedAt = now.Format(time.RFC3339Nano)
	}

	_, err := st.db.Exec(`
		UPDATE goals SET status = ?, started_at = ?, completed_at = ?, task_id = ?
		WHERE id = ?
	`, status, startedAt, completedAt, nullStrPtr(taskID), goalID)
	if err != nil {
		return err
	}

	// Update plan's updated_at timestamp
	_, err = st.db.Exec(`
		UPDATE plans SET updated_at = ?
		WHERE id = (SELECT plan_id FROM goals WHERE id = ?)
	`, now.Format(time.RFC3339Nano), goalID)
	return err
}

// GetActivePlanForSession retrieves the most recently updated plan for a session.
func (st *Store) GetActivePlanForSession(sessionID string) (*Plan, error) {
	row := st.db.QueryRow(`
		SELECT id, session_id, name, description, source_task, goals_json, created_at, updated_at
		FROM plans WHERE session_id = ? ORDER BY updated_at DESC LIMIT 1
	`, sessionID)

	p := &Plan{}
	var createdAt, updatedAt string
	err := row.Scan(&p.ID, &p.SessionID, &p.Name, &p.Description, &p.SourceTask,
		&p.GoalsJSON, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return p, nil
}

// GetGoalsForPlan retrieves all goals for a plan.
func (st *Store) GetGoalsForPlan(planID string) ([]*Goal, error) {
	rows, err := st.db.Query(`
		SELECT id, plan_id, session_id, title, description, priority, status,
		       files_json, depends_on_json, created_at, started_at, completed_at, task_id, findings_json
		FROM goals WHERE plan_id = ? ORDER BY created_at ASC
	`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var goals []*Goal
	for rows.Next() {
		g := &Goal{}
		var createdAt string
		var startedAt, completedAt sql.NullString
		var taskID sql.NullString
		err := rows.Scan(&g.ID, &g.PlanID, &g.SessionID, &g.Title, &g.Description,
			&g.Priority, &g.Status, &g.FilesJSON, &g.DependsJSON, &createdAt,
			&startedAt, &completedAt, &taskID, &g.FindingsJSON)
		if err != nil {
			continue
		}
		g.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		if startedAt.Valid {
			t, _ := time.Parse(time.RFC3339Nano, startedAt.String)
			g.StartedAt = &t
		}
		if completedAt.Valid {
			t, _ := time.Parse(time.RFC3339Nano, completedAt.String)
			g.CompletedAt = &t
		}
		if taskID.Valid {
			g.TaskID = &taskID.String
		}
		goals = append(goals, g)
	}
	return goals, rows.Err()
}

// GetPendingGoalsCount returns the number of pending/active goals in a plan.
func (st *Store) GetPendingGoalsCount(planID string) (int, error) {
	var count int
	err := st.db.QueryRow(`
		SELECT COUNT(*) FROM goals
		WHERE plan_id = ? AND status IN ('pending', 'active')
	`, planID).Scan(&count)
	return count, err
}

// nullTime returns nil if t is nil, otherwise formatted time — for nullable DATETIME columns.
func nullTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339Nano)
}

// nullStrPtr returns nil if s is nil, otherwise *s — for nullable TEXT columns.
func nullStrPtr(s *string) interface{} {
	if s == nil {
		return nil
	}
	return *s
}
