package db

import (
	"encoding/json"
	"fmt"
)

type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
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

