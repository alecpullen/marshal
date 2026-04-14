// Package store provides SQLite persistence for Marshal sessions.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/alecpullen/marshal/internal/agents/planner"
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
	ThinkBlock       string // R1 reasoning content
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
    think_block TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (session_id) REFERENCES sessions(id),
    UNIQUE(session_id, round_number)
);

CREATE INDEX IF NOT EXISTS idx_rounds_session ON rounds(session_id);
CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status);
CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created_at);

CREATE TABLE IF NOT EXISTS pipeline_runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    feature    TEXT    NOT NULL,
    status     TEXT    NOT NULL DEFAULT 'PLANNED',
    plan_json  TEXT    NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS pipeline_tasks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    pipeline_id INTEGER NOT NULL REFERENCES pipeline_runs(id),
    task_id     TEXT    NOT NULL,
    description TEXT    NOT NULL,
    depends_on  TEXT    NOT NULL DEFAULT '[]',
    status      TEXT    NOT NULL DEFAULT 'PENDING',
    UNIQUE(pipeline_id, task_id)
);

CREATE INDEX IF NOT EXISTS idx_pipeline_tasks_pipeline ON pipeline_tasks(pipeline_id);
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_created   ON pipeline_runs(created_at);

	-- Conversation tables for agent-centric model
	CREATE TABLE IF NOT EXISTS conversations (
		id TEXT PRIMARY KEY,
		status TEXT NOT NULL DEFAULT 'active',
		state TEXT NOT NULL DEFAULT 'chatting',
		context_summary TEXT DEFAULT '',
		pending_tasks TEXT DEFAULT '[]',
		active_task_ids TEXT DEFAULT '[]',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		conversation_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		intent TEXT DEFAULT '',
		task_id TEXT DEFAULT '',
		metadata TEXT DEFAULT '{}',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id)
	);

	CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_conversations_status ON conversations(status);

	-- Model preferences for persisting user-selected models per role
	CREATE TABLE IF NOT EXISTS model_preferences (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		role TEXT NOT NULL UNIQUE,  -- 'executor', 'critic', 'marshal', 'planner'
		provider TEXT NOT NULL,
		model TEXT NOT NULL,
		base_url TEXT NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_model_preferences_role ON model_preferences(role);
`

	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Migration: add think_block column if it doesn't exist
	// SQLite doesn't support ADD COLUMN IF NOT EXISTS, so we ignore errors
	_ = s.addThinkBlockColumn()

	// Migration: add branch column to pipeline_tasks for M9
	_ = s.addPipelineTaskBranchColumn()

	return nil
}

// addThinkBlockColumn adds the think_block column to existing databases.
func (s *Store) addThinkBlockColumn() error {
	// Try to add the column - will fail if it already exists, which we ignore
	_, _ = s.db.Exec("ALTER TABLE rounds ADD COLUMN think_block TEXT DEFAULT ''")
	return nil
}

// addPipelineTaskBranchColumn adds the branch column to pipeline_tasks for existing databases.
func (s *Store) addPipelineTaskBranchColumn() error {
	// Try to add the column - will fail if it already exists, which we ignore
	_, _ = s.db.Exec("ALTER TABLE pipeline_tasks ADD COLUMN branch TEXT DEFAULT ''")
	return nil
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
    diff, verdict, summary, issue, fix, concerns, prompt_tokens, completion_tokens, think_block, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		r.ThinkBlock,
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
    diff, verdict, summary, issue, fix, concerns, prompt_tokens, completion_tokens, think_block, created_at
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
			&r.ThinkBlock,
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

// PipelineRun represents a planning run for a feature description.
type PipelineRun struct {
	ID        int64
	Feature   string
	Status    string // "PLANNED", "EXECUTING", "DONE", "FAILED"
	PlanJSON  string // raw TaskGraph JSON
	CreatedAt time.Time
}

// PipelineTask represents a single task row in the pipeline_tasks table.
type PipelineTask struct {
	ID          int64
	PipelineID  int64
	TaskID      string // e.g. "A", "B"
	Description string
	DependsOn   string // JSON array of IDs, e.g. `["A","B"]`
	Status      string // "PENDING", "RUNNING", "DONE", "FAILED"
	Branch      string // Isolation branch name for this task
}

// CreatePipelineRun inserts a new pipeline run record and sets its ID.
func (s *Store) CreatePipelineRun(run *PipelineRun) error {
	query := `
INSERT INTO pipeline_runs (feature, status, plan_json, created_at)
VALUES (?, ?, ?, ?)
`
	result, err := s.db.Exec(query, run.Feature, run.Status, run.PlanJSON, run.CreatedAt)
	if err != nil {
		return err
	}
	run.ID, _ = result.LastInsertId()
	return nil
}

// GetPipelineRun retrieves a pipeline run by ID.
func (s *Store) GetPipelineRun(id int64) (*PipelineRun, error) {
	query := `SELECT id, feature, status, plan_json, created_at FROM pipeline_runs WHERE id = ?`
	row := s.db.QueryRow(query, id)

	run := &PipelineRun{}
	err := row.Scan(&run.ID, &run.Feature, &run.Status, &run.PlanJSON, &run.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("pipeline run not found: %d", id)
	}
	return run, err
}

// ListPipelineRuns returns recent pipeline runs ordered by created_at descending.
func (s *Store) ListPipelineRuns(limit int) ([]PipelineRun, error) {
	query := `
SELECT id, feature, status, plan_json, created_at
FROM pipeline_runs
ORDER BY created_at DESC
LIMIT ?
`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []PipelineRun
	for rows.Next() {
		var run PipelineRun
		if err := rows.Scan(&run.ID, &run.Feature, &run.Status, &run.PlanJSON, &run.CreatedAt); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// UpdatePipelineRunStatus updates the status of a pipeline run.
func (s *Store) UpdatePipelineRunStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE pipeline_runs SET status = ? WHERE id = ?`, status, id)
	return err
}

// Conversation methods for agent-centric model

// CreateConversation inserts a new conversation
func (s *Store) CreateConversation(id string) error {
	query := `INSERT INTO conversations (id, status, state, created_at, updated_at) VALUES (?, 'active', 'chatting', ?, ?)`
	now := time.Now()
	_, err := s.db.Exec(query, id, now, now)
	return err
}

// UpdateConversation updates a conversation's state and summary
func (s *Store) UpdateConversation(id string, state string, summary string, pendingTasks []string, activeTaskIDs []string) error {
	pendingJSON, _ := json.Marshal(pendingTasks)
	activeJSON, _ := json.Marshal(activeTaskIDs)
	query := `UPDATE conversations SET state = ?, context_summary = ?, pending_tasks = ?, active_task_ids = ?, updated_at = ? WHERE id = ?`
	_, err := s.db.Exec(query, state, summary, string(pendingJSON), string(activeJSON), time.Now(), id)
	return err
}

// AddMessage adds a message to a conversation
func (s *Store) AddMessage(convID, role, content, intent, taskID string) (int64, error) {
	query := `INSERT INTO messages (conversation_id, role, content, intent, task_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`
	result, err := s.db.Exec(query, convID, role, content, intent, taskID, time.Now())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetMessages retrieves all messages for a conversation
func (s *Store) GetMessages(convID string) ([]struct {
	ID        int64
	Role      string
	Content   string
	Intent    string
	TaskID    string
	CreatedAt time.Time
}, error) {
	query := `SELECT id, role, content, intent, task_id, created_at FROM messages WHERE conversation_id = ? ORDER BY created_at ASC`
	rows, err := s.db.Query(query, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []struct {
		ID        int64
		Role      string
		Content   string
		Intent    string
		TaskID    string
		CreatedAt time.Time
	}

	for rows.Next() {
		var m struct {
			ID        int64
			Role      string
			Content   string
			Intent    string
			TaskID    string
			CreatedAt time.Time
		}
		err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.Intent, &m.TaskID, &m.CreatedAt)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// GetConversation retrieves a conversation by ID
func (s *Store) GetConversation(id string) (*struct {
	ID             string
	Status         string
	State          string
	ContextSummary string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}, error) {
	query := `SELECT id, status, state, context_summary, created_at, updated_at FROM conversations WHERE id = ?`
	row := s.db.QueryRow(query, id)

	var c struct {
		ID             string
		Status         string
		State          string
		ContextSummary string
		CreatedAt      time.Time
		UpdatedAt      time.Time
	}
	err := row.Scan(&c.ID, &c.Status, &c.State, &c.ContextSummary, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("conversation not found: %s", id)
	}
	return &c, err
}

// ListConversations returns recent conversations
func (s *Store) ListConversations(limit int) ([]struct {
	ID        string
	Status    string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}, error) {
	query := `SELECT id, status, state, created_at, updated_at FROM conversations ORDER BY updated_at DESC LIMIT ?`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var convs []struct {
		ID        string
		Status    string
		State     string
		CreatedAt time.Time
		UpdatedAt time.Time
	}

	for rows.Next() {
		var c struct {
			ID        string
			Status    string
			State     string
			CreatedAt time.Time
			UpdatedAt time.Time
		}
		err := rows.Scan(&c.ID, &c.Status, &c.State, &c.CreatedAt, &c.UpdatedAt)
		if err != nil {
			return nil, err
		}
		convs = append(convs, c)
	}
	return convs, rows.Err()
}

// CreatePipelineTasks inserts tasks for a pipeline run (bulk insert).
func (s *Store) CreatePipelineTasks(pipelineID int64, tasks []planner.Task) error {
	for _, task := range tasks {
		dependsOnJSON, err := json.Marshal(task.DependsOn)
		if err != nil {
			return fmt.Errorf("marshal depends_on for task %s: %w", task.ID, err)
		}

		query := `
		INSERT INTO pipeline_tasks (pipeline_id, task_id, description, depends_on, status, branch)
		VALUES (?, ?, ?, ?, 'PENDING', '')
		`
		_, err = s.db.Exec(query, pipelineID, task.ID, task.Description, string(dependsOnJSON))
		if err != nil {
			return fmt.Errorf("insert task %s: %w", task.ID, err)
		}
	}
	return nil
}

// GetPipelineTasks retrieves all tasks for a pipeline run.
func (s *Store) GetPipelineTasks(pipelineID int64) ([]PipelineTask, error) {
	query := `
	SELECT id, pipeline_id, task_id, description, depends_on, status, branch
	FROM pipeline_tasks
	WHERE pipeline_id = ?
	ORDER BY id ASC
	`
	rows, err := s.db.Query(query, pipelineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []PipelineTask
	for rows.Next() {
		t := PipelineTask{}
		err := rows.Scan(&t.ID, &t.PipelineID, &t.TaskID, &t.Description, &t.DependsOn, &t.Status, &t.Branch)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// UpdatePipelineTaskStatus updates a task's status.
func (s *Store) UpdatePipelineTaskStatus(pipelineID int64, taskID string, status string) error {
	query := `UPDATE pipeline_tasks SET status = ? WHERE pipeline_id = ? AND task_id = ?`
	_, err := s.db.Exec(query, status, pipelineID, taskID)
	return err
}

// UpdatePipelineTaskBranch records the isolation branch for a task.
func (s *Store) UpdatePipelineTaskBranch(pipelineID int64, taskID string, branch string) error {
	query := `UPDATE pipeline_tasks SET branch = ? WHERE pipeline_id = ? AND task_id = ?`
	_, err := s.db.Exec(query, branch, pipelineID, taskID)
	return err
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// ModelPreference stores the selected model for an agent role.
type ModelPreference struct {
	Role      string // "marshal", "executor", "critic", "planner"
	Model     string // model name/identifier
	Provider  string // "ollama", "fireworks", etc.
	BaseURL   string
	UpdatedAt time.Time
}

// GetModelPreference retrieves the preferred model for an agent role.
func (s *Store) GetModelPreference(agentRole string) (*ModelPreference, error) {
	row := s.db.QueryRow(
		"SELECT role, model, provider, base_url, updated_at FROM model_preferences WHERE role = ?",
		agentRole,
	)

	var p ModelPreference
	err := row.Scan(&p.Role, &p.Model, &p.Provider, &p.BaseURL, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil // No preference set
	}
	if err != nil {
		return nil, fmt.Errorf("get model preference: %w", err)
	}

	return &p, nil
}

// SetModelPreference saves the preferred model for an agent role.
func (s *Store) SetModelPreference(agentRole, model, provider, baseURL string) error {
	_, err := s.db.Exec(
		`INSERT INTO model_preferences (role, model, provider, base_url, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(role) DO UPDATE SET
		 model = excluded.model,
		 provider = excluded.provider,
		 base_url = excluded.base_url,
		 updated_at = excluded.updated_at`,
		agentRole, model, provider, baseURL, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("set model preference: %w", err)
	}
	return nil
}
