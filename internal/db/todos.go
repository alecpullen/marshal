package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// TodoItem is one entry of the session task list. The stored JSON is
// compatible in both directions: rows written before ID and the timestamps
// existed decode with zero values, and older binaries ignore the new keys.
type TodoItem struct {
	ID          string    `json:"id,omitempty"`
	Content     string    `json:"content"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at,omitzero"`
	CompletedAt time.Time `json:"completed_at,omitzero"`
}

func (db *DB) SaveTodos(sessionID string, todos []TodoItem) error {
	data, err := json.Marshal(todos)
	if err != nil {
		return fmt.Errorf("marshal todos: %w", err)
	}
	_, err = db.sqlDB.Exec(
		`INSERT INTO session_state (session_id, key, value) VALUES (?, 'todos', ?)
		 ON CONFLICT(session_id, key) DO UPDATE SET value=excluded.value`,
		sessionID, string(data),
	)
	if err != nil {
		return fmt.Errorf("save todos: %w", err)
	}
	return nil
}

// LoadTodos restores the todo list for a session from session_state.
func (db *DB) LoadTodos(sessionID string) ([]TodoItem, error) {
	row := db.sqlDB.QueryRow(
		`SELECT value FROM session_state WHERE session_id=? AND key='todos'`,
		sessionID,
	)
	var raw string
	if err := row.Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load todos: %w", err)
	}
	var todos []TodoItem
	if err := json.Unmarshal([]byte(raw), &todos); err != nil {
		return nil, fmt.Errorf("unmarshal todos: %w", err)
	}
	return todos, nil
}
