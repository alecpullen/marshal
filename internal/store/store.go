// Package store provides SQLite persistence for Marshal sessions.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store provides SQLite persistence for sessions and rounds.
type Store struct {
	db *sql.DB
}

// Session represents a Marshal session.
type Session struct {
	ID               string
	RepoRoot         string
	Task             string
	Status           string // "SUCCESS", "EXHAUSTED", "FAILED"
	BaseBranch       string
	IsolationBranch  string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	CompletedAt      *time.Time
	PromptTokens     int
	CompletionTokens int
}

// RoundRecord represents a single round of the loop.
type RoundRecord struct {
	ID               int64
	SessionID        string
	RoundNumber      int
	ExecutorRequest  string
	ExecutorResponse string
	Diff             string
	Verdict          string // "PASS" or "FAIL"
	Summary          string
	Issue            string
	Fix              string
	Concerns         []string
	PromptTokens     int
	CompletionTokens int
	CreatedAt        time.Time
}

// New creates a new Store with the given database path.
// Creates the database and tables if they don't exist.
func New(dbPath string) (*Store, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	// Enable foreign key support
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	s := &Store{db: db}

	// Run migrations
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	return s, nil
}

// migrate creates the database tables if they don't exist.
func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    repo_root TEXT NOT NULL,
    task TEXT NOT NULL,
    status TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    isolation_branch TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    prompt_tokens INTEGER DEFAULT 0,
    completion_tokens INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS rounds (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    round_number INTEGER NOT NULL,
    executor_request TEXT NOT NULL,
    executor_response TEXT,
    diff TEXT,
    verdict TEXT NOT NULL,
    summary TEXT,
    issue TEXT,
    fix TEXT,
    concerns TEXT,
    prompt_tokens INTEGER DEFAULT 0,
    completion_tokens INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (session_id) REFERENCES sessions(id),
    UNIQUE(session_id, round_number)
);

CREATE INDEX IF NOT EXISTS idx_rounds_session ON rounds(session_id);
CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status);
CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created_at);
`

	_, err := s.db.Exec(schema)
	return err
}

// CreateSession inserts a new session into the database.
func (s *Store) CreateSession(sess *Session) error {
	query := `
INSERT INTO sessions (id, repo_root, task, status, base_branch, isolation_branch,
    created_at, updated_at, prompt_tokens, completion_tokens)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`
	_, err := s.db.Exec(query,
		sess.ID,
		sess.RepoRoot,
		sess.Task,
		sess.Status,
		sess.BaseBranch,
		sess.IsolationBranch,
		sess.CreatedAt,
		sess.UpdatedAt,
		sess.PromptTokens,
		sess.CompletionTokens,
	)
	return err
}

// UpdateSession updates an existing session.
func (s *Store) UpdateSession(sess *Session) error {
	query := `
UPDATE sessions SET
    status = ?,
    updated_at = ?,
    completed_at = ?,
    prompt_tokens = ?,
    completion_tokens = ?
WHERE id = ?
`
	var completedAt interface{}
	if sess.CompletedAt != nil {
		completedAt = *sess.CompletedAt
	} else {
		completedAt = nil
	}

	_, err := s.db.Exec(query,
		sess.Status,
		sess.UpdatedAt,
		completedAt,
		sess.PromptTokens,
		sess.CompletionTokens,
		sess.ID,
	)
	return err
}

// GetSession retrieves a session by ID.
func (s *Store) GetSession(id string) (*Session, error) {
	query := `
SELECT id, repo_root, task, status, base_branch, isolation_branch,
    created_at, updated_at, completed_at, prompt_tokens, completion_tokens
FROM sessions WHERE id = ?
`
	row := s.db.QueryRow(query, id)

	sess := &Session{}
	var completedAt sql.NullTime

	err := row.Scan(
		&sess.ID,
		&sess.RepoRoot,
		&sess.Task,
		&sess.Status,
		&sess.BaseBranch,
		&sess.IsolationBranch,
		&sess.CreatedAt,
		&sess.UpdatedAt,
		&completedAt,
		&sess.PromptTokens,
		&sess.CompletionTokens,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	if completedAt.Valid {
		sess.CompletedAt = &completedAt.Time
	}

	return sess, nil
}

// ListSessions returns recent sessions ordered by created_at descending.
func (s *Store) ListSessions(limit int) ([]Session, error) {
	query := `
SELECT id, repo_root, task, status, base_branch, isolation_branch,
    created_at, updated_at, completed_at, prompt_tokens, completion_tokens
FROM sessions
ORDER BY created_at DESC
LIMIT ?
`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		sess := Session{}
		var completedAt sql.NullTime

		err := rows.Scan(
			&sess.ID,
			&sess.RepoRoot,
			&sess.Task,
			&sess.Status,
			&sess.BaseBranch,
			&sess.IsolationBranch,
			&sess.CreatedAt,
			&sess.UpdatedAt,
			&completedAt,
			&sess.PromptTokens,
			&sess.CompletionTokens,
		)
		if err != nil {
			return nil, err
		}

		if completedAt.Valid {
			sess.CompletedAt = &completedAt.Time
		}

		sessions = append(sessions, sess)
	}

	return sessions, rows.Err()
}

// CreateRound inserts a new round record.
func (s *Store) CreateRound(r *RoundRecord) error {
	// Serialize concerns to JSON
	concernsJSON, err := json.Marshal(r.Concerns)
	if err != nil {
		return fmt.Errorf("marshal concerns: %w", err)
	}

	query := `
INSERT INTO rounds (session_id, round_number, executor_request, executor_response,
    diff, verdict, summary, issue, fix, concerns, prompt_tokens, completion_tokens, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`
	result, err := s.db.Exec(query,
		r.SessionID,
		r.RoundNumber,
		r.ExecutorRequest,
		r.ExecutorResponse,
		r.Diff,
		r.Verdict,
		r.Summary,
		r.Issue,
		r.Fix,
		string(concernsJSON),
		r.PromptTokens,
		r.CompletionTokens,
		r.CreatedAt,
	)
	if err != nil {
		return err
	}

	r.ID, _ = result.LastInsertId()
	return nil
}

// GetRounds retrieves all rounds for a session.
func (s *Store) GetRounds(sessionID string) ([]RoundRecord, error) {
	query := `
SELECT id, session_id, round_number, executor_request, executor_response,
    diff, verdict, summary, issue, fix, concerns, prompt_tokens, completion_tokens, created_at
FROM rounds
WHERE session_id = ?
ORDER BY round_number ASC
`
	rows, err := s.db.Query(query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rounds []RoundRecord
	for rows.Next() {
		r := RoundRecord{}
		var concernsJSON string

		err := rows.Scan(
			&r.ID,
			&r.SessionID,
			&r.RoundNumber,
			&r.ExecutorRequest,
			&r.ExecutorResponse,
			&r.Diff,
			&r.Verdict,
			&r.Summary,
			&r.Issue,
			&r.Fix,
			&concernsJSON,
			&r.PromptTokens,
			&r.CompletionTokens,
			&r.CreatedAt,
		)
		if err != nil {
			return nil, err
		}

		// Deserialize concerns
		if concernsJSON != "" {
			json.Unmarshal([]byte(concernsJSON), &r.Concerns)
		}

		rounds = append(rounds, r)
	}

	return rounds, rows.Err()
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}
