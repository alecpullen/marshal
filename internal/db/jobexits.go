package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// JobExitRecord is one background job's exit, stored so a resumed session's
// job cards settle the way they did before exit. The list lives in
// session_state as one JSON value, like todos: it is small (one bounded
// record per job) and only ever read whole.
type JobExitRecord struct {
	ID         string    `json:"id"`
	Command    string    `json:"command,omitempty"`
	ExitCode   int       `json:"exit_code"`
	DurationMs int64     `json:"duration_ms,omitempty"`
	Output     string    `json:"output,omitempty"`
	At         time.Time `json:"at"`
}

// SaveJobExits replaces the session's stored job exits.
func (db *DB) SaveJobExits(sessionID string, exits []JobExitRecord) error {
	data, err := json.Marshal(exits)
	if err != nil {
		return fmt.Errorf("marshal job exits: %w", err)
	}
	_, err = db.sqlDB.Exec(
		`INSERT INTO session_state (session_id, key, value) VALUES (?, 'job_exits', ?)
		 ON CONFLICT(session_id, key) DO UPDATE SET value=excluded.value`,
		sessionID, string(data),
	)
	if err != nil {
		return fmt.Errorf("save job exits: %w", err)
	}
	return nil
}

// LoadJobExits restores the session's job exits; a session with none returns
// nil.
func (db *DB) LoadJobExits(sessionID string) ([]JobExitRecord, error) {
	row := db.sqlDB.QueryRow(
		`SELECT value FROM session_state WHERE session_id=? AND key='job_exits'`,
		sessionID,
	)
	var raw string
	if err := row.Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load job exits: %w", err)
	}
	var exits []JobExitRecord
	if err := json.Unmarshal([]byte(raw), &exits); err != nil {
		return nil, fmt.Errorf("unmarshal job exits: %w", err)
	}
	return exits, nil
}
