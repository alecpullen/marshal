package db

import (
	"database/sql"
	"fmt"
	"time"
)

type TurnMetricsRow struct {
	ID               int64
	ProjectID        int64
	SessionID        string
	StartedAt        time.Time
	DurationMs       int64
	Class            string
	Role             string
	Provider         string
	Model            string
	Goal             string
	Iterations       int
	ToolCalls        int
	ToolErrors       int
	CacheHits        int
	ParseFailures    int
	SoftStalls       int
	HardStalls       int
	Outcome          string
	SalvageReason    string
	PromptTokens     int
	CompletionTokens int
}

func (db *DB) InsertTurnMetrics(row TurnMetricsRow) (int64, error) {
	var sessionID sql.NullString
	if row.SessionID != "" {
		sessionID = sql.NullString{String: row.SessionID, Valid: true}
	}
	res, err := db.sqlDB.Exec(
		`INSERT INTO turn_metrics (
			project_id, session_id, started_at, duration_ms, class, role,
			provider, model, goal, iterations, tool_calls, tool_errors,
			cache_hits, parse_failures, soft_stalls, hard_stalls, outcome,
			salvage_reason, prompt_tokens, completion_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ProjectID,
		sessionID,
		row.StartedAt.UTC().Format(time.RFC3339),
		row.DurationMs,
		row.Class,
		row.Role,
		row.Provider,
		row.Model,
		row.Goal,
		row.Iterations,
		row.ToolCalls,
		row.ToolErrors,
		row.CacheHits,
		row.ParseFailures,
		row.SoftStalls,
		row.HardStalls,
		row.Outcome,
		row.SalvageReason,
		row.PromptTokens,
		row.CompletionTokens,
	)
	if err != nil {
		return 0, fmt.Errorf("insert turn metrics: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("turn metrics insert id: %w", err)
	}
	return id, nil
}

func (db *DB) RecentTurnMetrics(projectID int64, limit int) ([]TurnMetricsRow, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, project_id, session_id, started_at, duration_ms, class,
			role, provider, model, goal, iterations, tool_calls, tool_errors,
			cache_hits, parse_failures, soft_stalls, hard_stalls, outcome,
			salvage_reason, prompt_tokens, completion_tokens
		 FROM turn_metrics
		 WHERE project_id = ?
		 ORDER BY id DESC
		 LIMIT ?`,
		projectID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query turn metrics: %w", err)
	}
	defer rows.Close()

	var out []TurnMetricsRow
	for rows.Next() {
		var r TurnMetricsRow
		var sessionID sql.NullString
		var started string
		if err := rows.Scan(
			&r.ID, &r.ProjectID, &sessionID, &started, &r.DurationMs, &r.Class,
			&r.Role, &r.Provider, &r.Model, &r.Goal, &r.Iterations, &r.ToolCalls,
			&r.ToolErrors, &r.CacheHits, &r.ParseFailures, &r.SoftStalls,
			&r.HardStalls, &r.Outcome, &r.SalvageReason, &r.PromptTokens,
			&r.CompletionTokens,
		); err != nil {
			return nil, fmt.Errorf("scan turn metrics row: %w", err)
		}
		if sessionID.Valid {
			r.SessionID = sessionID.String
		}
		parsed, err := time.Parse(time.RFC3339, started)
		if err != nil {
			return nil, fmt.Errorf("parse started_at: %w", err)
		}
		r.StartedAt = parsed.UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate turn metrics rows: %w", err)
	}
	return out, nil
}
