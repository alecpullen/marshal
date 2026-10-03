package db

import (
	"database/sql"
	"fmt"
	"time"
)

// StepRow is one persisted step: a single model response and the actor that
// produced it. Seq is per-session. TurnMessageID is the DB id of the user
// message that opened the turn (0 = none). A zero EndedAt means still open.
type StepRow struct {
	Seq           int64
	TurnMessageID int64
	ActorRole     string
	ActorLabel    string
	Model         string
	Provider      string
	TodoID        string
	StartedAt     time.Time
	EndedAt       time.Time
}

// SaveStep inserts a step, or replaces it if the (session, seq) row exists.
func (db *DB) SaveStep(sessionID string, st StepRow) error {
	var turn sql.NullInt64
	if st.TurnMessageID > 0 {
		turn = sql.NullInt64{Int64: st.TurnMessageID, Valid: true}
	}
	var ended sql.NullString
	if !st.EndedAt.IsZero() {
		ended = sql.NullString{String: st.EndedAt.UTC().Format(time.RFC3339Nano), Valid: true}
	}
	_, err := db.sqlDB.Exec(
		`INSERT OR REPLACE INTO steps (session_id, seq, turn_message_id, actor_role, actor_label, model, provider, todo_id, started_at, ended_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, st.Seq, turn, st.ActorRole, st.ActorLabel, st.Model, st.Provider, st.TodoID,
		st.StartedAt.UTC().Format(time.RFC3339Nano), ended,
	)
	if err != nil {
		return fmt.Errorf("save step: %w", err)
	}
	return nil
}

// EndStep stamps a step's end time. Ending a missing step is a no-op.
func (db *DB) EndStep(sessionID string, seq int64, endedAt time.Time) error {
	_, err := db.sqlDB.Exec(
		`UPDATE steps SET ended_at = ? WHERE session_id = ? AND seq = ?`,
		endedAt.UTC().Format(time.RFC3339Nano), sessionID, seq,
	)
	if err != nil {
		return fmt.Errorf("end step: %w", err)
	}
	return nil
}

// GetSteps returns every step of a session in sequence order.
func (db *DB) GetSteps(sessionID string) ([]StepRow, error) {
	rows, err := db.sqlDB.Query(
		`SELECT seq, turn_message_id, actor_role, actor_label, model, provider, todo_id, started_at, ended_at
		 FROM steps WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query steps: %w", err)
	}
	defer rows.Close()
	var out []StepRow
	for rows.Next() {
		var st StepRow
		var turn sql.NullInt64
		var role, label, model, provider, todo, ended sql.NullString
		var started string
		if err := rows.Scan(&st.Seq, &turn, &role, &label, &model, &provider, &todo, &started, &ended); err != nil {
			return nil, fmt.Errorf("scan step: %w", err)
		}
		st.TurnMessageID = turn.Int64
		st.ActorRole, st.ActorLabel = role.String, label.String
		st.Model, st.Provider, st.TodoID = model.String, provider.String, todo.String
		t, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, fmt.Errorf("parse step started_at: %w", err)
		}
		st.StartedAt = t.UTC()
		if ended.Valid {
			if e, err := time.Parse(time.RFC3339Nano, ended.String); err == nil {
				st.EndedAt = e.UTC()
			}
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
