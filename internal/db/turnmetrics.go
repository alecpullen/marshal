package db

import (
	"database/sql"
	"fmt"
	"time"
)

const recentTurnMetricsDefaultLimit = 50
const recentTurnMetricsMaxLimit = 200

// TokenCategorySet is a bitmask of token categories present in a turn.
type TokenCategorySet uint8

const (
	CatPrompt TokenCategorySet = 1 << iota
	CatCompletion
	CatReasoning
	CatCacheRead
	CatCacheWrite
)

// UsageTotals holds aggregate token and cost counts across one or more turns.
type UsageTotals struct {
	PromptTokens       int64
	CompletionTokens   int64
	ReasoningTokens    int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	EstimatedCostCents int64
	Turns              int
}

// ModelBreakdown is a UsageTotals scoped to a single (provider, model) pair.
type ModelBreakdown struct {
	UsageTotals
	Provider string
	Model    string
	Reported TokenCategorySet
}

type TurnMetricsRow struct {
	ID                 int64
	ProjectID          int64
	SessionID          string
	StartedAt          time.Time
	DurationMs         int64
	Class              string
	Role               string
	Provider           string
	Model              string
	Goal               string
	Iterations         int
	ToolCalls          int
	ToolErrors         int
	CacheHits          int
	ParseFailures      int
	HardStalls         int
	Outcome            string
	SalvageReason      string
	PromptTokens       int
	CompletionTokens   int
	ReasoningTokens    int
	CacheReadTokens    int
	CacheWriteTokens   int
	EstimatedCostCents int64
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
			salvage_reason, prompt_tokens, completion_tokens,
			reasoning_tokens, cache_read_tokens, cache_write_tokens, estimated_cost_cents
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		0, // soft_stalls - always 0 after removal
		row.HardStalls,
		row.Outcome,
		row.SalvageReason,
		row.PromptTokens,
		row.CompletionTokens,
		row.ReasoningTokens,
		row.CacheReadTokens,
		row.CacheWriteTokens,
		row.EstimatedCostCents,
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
	if limit <= 0 {
		limit = recentTurnMetricsDefaultLimit
	}
	if limit > recentTurnMetricsMaxLimit {
		limit = recentTurnMetricsMaxLimit
	}
	rows, err := db.sqlDB.Query(
		`SELECT id, project_id, session_id, started_at, duration_ms, class,
			role, provider, model, goal, iterations, tool_calls, tool_errors,
			cache_hits, parse_failures, hard_stalls, outcome,
			salvage_reason, prompt_tokens, completion_tokens,
			reasoning_tokens, cache_read_tokens, cache_write_tokens, estimated_cost_cents
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
			&r.ToolErrors, &r.CacheHits, &r.ParseFailures,
			&r.HardStalls, &r.Outcome, &r.SalvageReason, &r.PromptTokens,
			&r.CompletionTokens,
			&r.ReasoningTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &r.EstimatedCostCents,
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

// AggregateTurnMetrics returns grand totals and per-model breakdowns for a project.
func (db *DB) AggregateTurnMetrics(projectID int64) (UsageTotals, []ModelBreakdown, error) {
	rows, err := db.sqlDB.Query(
		`SELECT provider, model,
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(completion_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_write_tokens), 0),
			COALESCE(SUM(estimated_cost_cents), 0),
			COUNT(*)
		 FROM turn_metrics
		 WHERE project_id = ?
		 GROUP BY provider, model
		 ORDER BY SUM(estimated_cost_cents) DESC`,
		projectID,
	)
	if err != nil {
		return UsageTotals{}, nil, fmt.Errorf("aggregate turn metrics: %w", err)
	}
	defer rows.Close()

	var totals UsageTotals
	var breakdown []ModelBreakdown
	for rows.Next() {
		var b ModelBreakdown
		if err := rows.Scan(
			&b.Provider, &b.Model,
			&b.PromptTokens, &b.CompletionTokens,
			&b.ReasoningTokens, &b.CacheReadTokens, &b.CacheWriteTokens,
			&b.EstimatedCostCents, &b.Turns,
		); err != nil {
			return UsageTotals{}, nil, fmt.Errorf("scan aggregate row: %w", err)
		}
		totals.PromptTokens += b.PromptTokens
		totals.CompletionTokens += b.CompletionTokens
		totals.ReasoningTokens += b.ReasoningTokens
		totals.CacheReadTokens += b.CacheReadTokens
		totals.CacheWriteTokens += b.CacheWriteTokens
		totals.EstimatedCostCents += b.EstimatedCostCents
		totals.Turns += b.Turns
		breakdown = append(breakdown, b)
	}
	if err := rows.Err(); err != nil {
		return UsageTotals{}, nil, fmt.Errorf("iterate aggregate rows: %w", err)
	}
	return totals, breakdown, nil
}
