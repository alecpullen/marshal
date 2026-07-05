package db

import (
	"database/sql"
	"errors"
	"fmt"
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

// SaveMessage appends a message to the session transcript. reasoning and
// thinkDuration are the model's captured reasoning/thinking text and how
// long it took, if any (empty string / zero duration otherwise) — both
// stored as SQL NULL so a message with no captured reasoning round-trips
// identically to how it did before these columns existed.
func (db *DB) SaveMessage(sessionID string, role string, content string, contentType string, createdAt time.Time, reasoning string, thinkDuration time.Duration, final bool) error {
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
	_, err := db.exec(
		`INSERT INTO messages (session_id, role, content, content_type, reasoning, think_duration_ms, created_at, final)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, role, content, contentTypeArg, reasoningArg, thinkDurationArg, createdAt.UTC().Format(time.RFC3339), final,
	)
	if err != nil {
		return fmt.Errorf("save message: %w", err)
	}
	return nil
}

// GetMessages returns all messages for a session in chronological order.
func (db *DB) GetMessages(sessionID string) ([]Message, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, role, content, content_type, reasoning, think_duration_ms, created_at, final
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
		var reasoning sql.NullString
		var thinkDurationMs sql.NullInt64
		var contentType sql.NullString
		var final sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &contentType, &reasoning, &thinkDurationMs, &created, &final); err != nil {
			return nil, fmt.Errorf("scan message row: %w", err)
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
