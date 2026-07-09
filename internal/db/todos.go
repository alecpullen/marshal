package db

import (
	"encoding/json"
)

type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

func (db *DB) SaveTodos(sessionID string, todos []TodoItem) error {
	data, err := json.Marshal(todos)
	if err != nil {
		return err
	}
	_, err = db.sqlDB.Exec(
		`INSERT INTO session_state (session_id, key, value) VALUES (?, 'todos', ?)
		 ON CONFLICT(session_id, key) DO UPDATE SET value=excluded.value`,
		sessionID, string(data),
	)
	return err
}

func (db *DB) LoadTodos(sessionID string) ([]TodoItem, error) {
	var raw string
	err := db.sqlDB.QueryRow(
		`SELECT value FROM session_state WHERE session_id = ? AND key = 'todos'`,
		sessionID,
	).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var todos []TodoItem
	if err := json.Unmarshal([]byte(raw), &todos); err != nil {
		return nil, err
	}
	return todos, nil
}
