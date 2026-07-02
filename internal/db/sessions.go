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
