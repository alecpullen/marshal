package db

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type Message struct {
	ID              int64
	Role            string
	Content         string
	ContentType     string
	Reasoning       string
	ThinkDurationMs int64
	CreatedAt       time.Time
	Final           bool
	ParentID        int64
}

type Session struct {
	ID        string
	ProjectID int64
	Title     string
	StartedAt time.Time
	EndedAt   *time.Time
	Summary   string
	// ActiveRoot and WorktreeBranch record the session's worktree rebind.
	// Both empty when the session operates at the project root.
	ActiveRoot     string
	WorktreeBranch string
	// WorktreeTargetBranch is the project branch the isolated session's
	// worktree branch is destined to merge into. Persisted so a later
	// session/merge can reject a caller-supplied target that differs.
	// Empty when the session operates at the project root.
	WorktreeTargetBranch string
	// WorktreeBaseSha is the commit the worktree branch started from.
	// Persisted so a resumed isolated session can diff stably against the
	// original base rather than recomputing a merge-base that may have
	// moved. Empty when the session operates at the project root.
	WorktreeBaseSha string
}

// CreateSession inserts a new agent_sessions row. The session id is generated
// by the caller (session.State) and is the primary key.
func (db *DB) CreateSession(sessionID string, projectID int64, title string, startedAt time.Time) error {
	_, err := db.sqlDB.Exec(
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
	var endedAt, summary, activeRoot, worktreeBranch, worktreeTargetBranch, worktreeBaseSha sql.NullString
	row := db.sqlDB.QueryRow(
		`SELECT id, project_id, title, started_at, ended_at, summary, active_root, worktree_branch, worktree_target_branch, worktree_base_sha FROM agent_sessions WHERE id = ?`,
		sessionID,
	)
	if err := row.Scan(&s.ID, &s.ProjectID, &s.Title, &startedAt, &endedAt, &summary, &activeRoot, &worktreeBranch, &worktreeTargetBranch, &worktreeBaseSha); err != nil {
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
	if activeRoot.Valid {
		s.ActiveRoot = activeRoot.String
	}
	if worktreeBranch.Valid {
		s.WorktreeBranch = worktreeBranch.String
	}
	if worktreeTargetBranch.Valid {
		s.WorktreeTargetBranch = worktreeTargetBranch.String
	}
	if worktreeBaseSha.Valid {
		s.WorktreeBaseSha = worktreeBaseSha.String
	}
	return s, nil
}

// UpdateSessionTitle sets the title on an existing session row (F13).
func (db *DB) UpdateSessionTitle(sessionID string, title string) error {
	_, err := db.sqlDB.Exec(
		`UPDATE agent_sessions SET title = ? WHERE id = ?`,
		title, sessionID,
	)
	if err != nil {
		return fmt.Errorf("update session title: %w", err)
	}
	return nil
}

// EndSession sets ended_at and summary on an existing session row.
func (db *DB) EndSession(sessionID string, endedAt time.Time, summary string) error {
	_, err := db.sqlDB.Exec(
		`UPDATE agent_sessions SET ended_at = ?, summary = ? WHERE id = ?`,
		endedAt.UTC().Format(time.RFC3339), summary, sessionID,
	)
	if err != nil {
		return fmt.Errorf("end session: %w", err)
	}
	return nil
}

// UpdateSessionWorkspace records the session's active workspace root,
// worktree branch, the branch the worktree is destined to merge into, and
// the commit the worktree branch started from. All four are stored empty
// when the session operates at the project root.
func (db *DB) UpdateSessionWorkspace(sessionID, activeRoot, branch, targetBranch, baseSha string) error {
	_, err := db.sqlDB.Exec(
		`UPDATE agent_sessions SET active_root = ?, worktree_branch = ?, worktree_target_branch = ?, worktree_base_sha = ? WHERE id = ?`,
		activeRoot, branch, targetBranch, baseSha, sessionID,
	)
	if err != nil {
		return fmt.Errorf("update session workspace: %w", err)
	}
	return nil
}

// SaveMessage appends a message to the session transcript. reasoning and
// thinkDuration are the model's captured reasoning/thinking text and how
// long it took, if any (empty string / zero duration otherwise) — both
// stored as SQL NULL so a message with no captured reasoning round-trips
// identically to how it did before these columns existed. parentID is the
// parent message's DB id (0 = root, stored as NULL). Returns the inserted
// row id.
func (db *DB) SaveMessage(sessionID string, role string, content string, contentType string, createdAt time.Time, reasoning string, thinkDuration time.Duration, final bool, parentID int64) (int64, error) {
	var reasoningArg sql.NullString
	if reasoning != "" {
		reasoningArg = sql.NullString{String: reasoning, Valid: true}
	}
	var thinkDurationArg sql.NullInt64
	if thinkDuration > 0 {
		thinkDurationArg = sql.NullInt64{Int64: thinkDuration.Milliseconds(), Valid: true}
	}
	var contentTypeArg sql.NullString
	if contentType != "" && contentType != "plain" {
		contentTypeArg = sql.NullString{String: contentType, Valid: true}
	}
	var parentArg sql.NullInt64
	if parentID > 0 {
		parentArg = sql.NullInt64{Int64: parentID, Valid: true}
	}
	res, err := db.sqlDB.Exec(
		`INSERT INTO messages (session_id, role, content, content_type, reasoning, think_duration_ms, created_at, final, parent_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, role, content, contentTypeArg, reasoningArg, thinkDurationArg, createdAt.UTC().Format(time.RFC3339), final, parentArg,
	)
	if err != nil {
		return 0, fmt.Errorf("save message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	return id, nil
}

// GetLeafMessageID returns the session's current active-branch leaf id.
// If leaf_message_id was never recorded (e.g. a session created in the
// same process where SetBranchLeaf was never called), falls back to
// MAX(id) of the session's messages so cold-start reconstruction still
// finds the right tip. Returns 0 if the session has no messages.
func (db *DB) GetLeafMessageID(sessionID string) (int64, error) {
	var leaf sql.NullInt64
	row := db.sqlDB.QueryRow(`SELECT leaf_message_id FROM agent_sessions WHERE id = ?`, sessionID)
	if err := row.Scan(&leaf); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("get leaf message id: %w", err)
	}
	if leaf.Valid && leaf.Int64 > 0 {
		return leaf.Int64, nil
	}
	// Fallback: newest message id in the session.
	var maxID sql.NullInt64
	if err := db.sqlDB.QueryRow(`SELECT MAX(id) FROM messages WHERE session_id = ?`, sessionID).Scan(&maxID); err != nil {
		return 0, fmt.Errorf("get max message id: %w", err)
	}
	if !maxID.Valid {
		return 0, nil
	}
	return maxID.Int64, nil
}

// SetBranchLeaf records leafID as the session's current active branch tip.
func (db *DB) SetBranchLeaf(sessionID string, leafID int64) error {
	if _, err := db.sqlDB.Exec(`UPDATE agent_sessions SET leaf_message_id = ? WHERE id = ?`, leafID, sessionID); err != nil {
		return fmt.Errorf("set branch leaf: %w", err)
	}
	return nil
}

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

// scanMessage scans one message row, resolving nullable columns and
// parsing the RFC3339 timestamp.
func scanMessage(rows *sql.Rows) (Message, error) {
	var m Message
	var created string
	var reasoning sql.NullString
	var thinkDurationMs sql.NullInt64
	var contentType sql.NullString
	var final sql.NullInt64
	var parentID sql.NullInt64
	if err := rows.Scan(&m.ID, &m.Role, &m.Content, &contentType, &reasoning, &thinkDurationMs, &created, &final, &parentID); err != nil {
		return Message{}, fmt.Errorf("scan message: %w", err)
	}
	if contentType.Valid {
		m.ContentType = contentType.String
	}
	if reasoning.Valid {
		m.Reasoning = reasoning.String
	}
	if thinkDurationMs.Valid {
		m.ThinkDurationMs = thinkDurationMs.Int64
	}
	m.Final = final.Valid && final.Int64 != 0
	if parentID.Valid {
		m.ParentID = parentID.Int64
	}
	parsed, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return Message{}, fmt.Errorf("parse created_at: %w", err)
	}
	m.CreatedAt = parsed.UTC()
	return m, nil
}

// MessagesOnBranch returns the message rows from the root down to leafID
// (inclusive), following parent_id links. The slice is in chronological order.
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
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListBranches returns the distinct leaf message ids reachable in the session
// (every message with no children is a leaf = a branch tip). The current leaf
// is included.
func (db *DB) ListBranches(sessionID string) ([]int64, error) {
	rows, err := db.sqlDB.Query(
		`SELECT m.id FROM messages m
		 WHERE m.session_id = ?
		   AND NOT EXISTS (SELECT 1 FROM messages c WHERE c.session_id = ? AND c.parent_id = m.id)
		 ORDER BY m.id ASC`,
		sessionID, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}
	defer rows.Close()
	var leaves []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		leaves = append(leaves, id)
	}
	return leaves, rows.Err()
}

// GetMessages returns all messages for a session in chronological order.
func (db *DB) GetMessages(sessionID string) ([]Message, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, role, content, content_type, reasoning, think_duration_ms, created_at, final, parent_id
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
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message rows: %w", err)
	}
	return messages, nil
}

// DeleteSession deletes a session and its messages (via CASCADE). It also
// removes session-scoped rows in tables that do not declare an FK back to
// agent_sessions: snapshots, file_reads, file_writes, and session_state.
// FTS rows for generation turns are removed by the AFTER DELETE trigger on
// generation_turns (see migrations.go).
func (db *DB) DeleteSession(ctx context.Context, sessionID string) (bool, error) {
	tx, err := db.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("delete session: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Explicitly clean up tables without a foreign-key relationship to
	// agent_sessions so deleting a session does not leave orphaned metadata.
	if _, err := tx.ExecContext(ctx, "DELETE FROM snapshots WHERE session_id = ?", sessionID); err != nil {
		return false, fmt.Errorf("delete session snapshots: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM file_reads WHERE session_id = ?", sessionID); err != nil {
		return false, fmt.Errorf("delete session file reads: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM file_writes WHERE session_id = ?", sessionID); err != nil {
		return false, fmt.Errorf("delete session file writes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM session_state WHERE session_id = ?", sessionID); err != nil {
		return false, fmt.Errorf("delete session state: %w", err)
	}

	res, err := tx.ExecContext(ctx, "DELETE FROM agent_sessions WHERE id = ?", sessionID)
	if err != nil {
		return false, fmt.Errorf("delete session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete session rows affected: %w", err)
	}
	// Clean up content_blobs that are no longer referenced by any
	// generation_turn. The cascade delete above removed this session's
	// turns, so any blobs only referenced by this session are now orphans.
	if err := db.DeleteSessionBlobs(tx); err != nil {
		return false, fmt.Errorf("delete session blobs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("delete session: commit: %w", err)
	}
	return n > 0, nil
}

// SessionEntry is a single row returned by ListSessions, projected from an
// agent_sessions row joined to its project root and message activity.
type SessionEntry struct {
	SessionID    string
	Cwd          string
	Title        string
	UpdatedAt    time.Time
	MessageCount int
	EndedAt      *time.Time
}

const (
	listSessionsDefaultLimit = 50
	listSessionsMaxLimit     = 200
)

const listSessionsSQL = `
WITH session_stats AS (
    SELECT session_id, MAX(created_at) AS updated_at, COUNT(*) AS message_count
      FROM messages
     GROUP BY session_id
)
SELECT s.id, p.root_path, s.title,
       COALESCE(ss.updated_at, s.started_at) AS updated_at,
       COALESCE(ss.message_count, 0)        AS message_count,
       s.ended_at
  FROM agent_sessions s
  JOIN projects p ON p.id = s.project_id
  LEFT JOIN session_stats ss ON ss.session_id = s.id
 WHERE p.root_path = ?
 ORDER BY updated_at DESC, s.id DESC
 LIMIT ? OFFSET ?`

// ListSessions returns sessions whose project root matches cwd, newest
// activity first. cursor is an opaque base64-encoded offset from a previous
// nextCursor; pass "" to start from the beginning. limit defaults to 50 and
// is clamped to [1, 200]. The returned nextCursor is empty when no more rows
// remain. A cursor that cannot be decoded returns an error.
func (db *DB) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]SessionEntry, string, error) {
	if limit <= 0 {
		limit = listSessionsDefaultLimit
	}
	if limit > listSessionsMaxLimit {
		limit = listSessionsMaxLimit
	}

	offset, err := decodeListCursor(cursor)
	if err != nil {
		return nil, "", fmt.Errorf("decode session list cursor: %w", err)
	}

	rows, err := db.sqlDB.QueryContext(ctx, listSessionsSQL, cwd, limit+1, offset)
	if err != nil {
		return nil, "", fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var out []SessionEntry
	for rows.Next() {
		var (
			e         SessionEntry
			updatedAt string
			title     sql.NullString
			endedAt   sql.NullString
		)
		if err := rows.Scan(&e.SessionID, &e.Cwd, &title, &updatedAt, &e.MessageCount, &endedAt); err != nil {
			return nil, "", fmt.Errorf("scan session row: %w", err)
		}
		if title.Valid {
			e.Title = title.String
		}
		parsed, perr := time.Parse(time.RFC3339, updatedAt)
		if perr != nil {
			return nil, "", fmt.Errorf("parse updated_at %q: %w", updatedAt, perr)
		}
		e.UpdatedAt = parsed.UTC()
		if endedAt.Valid {
			et, perr := time.Parse(time.RFC3339, endedAt.String)
			if perr != nil {
				return nil, "", fmt.Errorf("parse ended_at %q: %w", endedAt.String, perr)
			}
			et = et.UTC()
			e.EndedAt = &et
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate session rows: %w", err)
	}

	var nextCursor string
	if len(out) > limit {
		nextCursor = encodeListCursor(offset + limit)
		out = out[:limit]
	}
	return out, nextCursor, nil
}

func encodeListCursor(offset int) string {
	return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeListCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	dec, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(string(dec))
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("negative cursor offset: %d", n)
	}
	return n, nil
}
