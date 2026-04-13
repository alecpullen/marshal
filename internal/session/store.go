package session

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // register "sqlite" driver
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
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and runs the schema
// migration.
func Open(path string) (*Store, error) {
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
	return &Store{db: db}, nil
}

// Close releases the database connection.
func (s *Store) Close() error { return s.db.Close() }

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
