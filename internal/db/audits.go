package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"marshal/internal/tools/registry"
)

// SaveToolCall persists an audit event for a session.
func (db *DB) SaveToolCall(sessionID string, event registry.AuditEvent) error {
	var exitCode sql.NullInt64
	if event.CommandExitCode != nil {
		exitCode = sql.NullInt64{Int64: int64(*event.CommandExitCode), Valid: true}
	}

	filesChanged, err := json.Marshal(event.FilesChanged)
	if err != nil {
		return fmt.Errorf("marshal files changed: %w", err)
	}

	_, err = db.exec(
		`INSERT INTO tool_calls (session_id, agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, command_exit_code, files_changed, error, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID,
		event.AgentRole,
		event.Model,
		event.ToolName,
		string(event.Args),
		event.ResultSummary,
		string(event.Risk),
		string(event.Approval),
		exitCode,
		string(filesChanged),
		event.Error,
		event.Timestamp.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("save tool call: %w", err)
	}
	return nil
}

// GetToolCalls returns all audit events for a session in chronological order.
func (db *DB) GetToolCalls(sessionID string) ([]registry.AuditEvent, error) {
	rows, err := db.sqlDB.Query(
		`SELECT agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, command_exit_code, files_changed, error, created_at
		 FROM tool_calls
		 WHERE session_id = ?
		 ORDER BY id ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query tool calls: %w", err)
	}
	defer rows.Close()

	var events []registry.AuditEvent
	for rows.Next() {
		var e registry.AuditEvent
		var args string
		var risk string
		var approval string
		var exitCode sql.NullInt64
		var filesChanged string
		var created string
		if err := rows.Scan(&e.AgentRole, &e.Model, &e.ToolName, &args, &e.ResultSummary, &risk, &approval, &exitCode, &filesChanged, &e.Error, &created); err != nil {
			return nil, fmt.Errorf("scan tool call row: %w", err)
		}
		e.Args = []byte(args)
		e.Risk = registry.RiskLevel(risk)
		e.Approval = registry.ApprovalState(approval)
		if exitCode.Valid {
			code := int(exitCode.Int64)
			e.CommandExitCode = &code
		}
		if err := json.Unmarshal([]byte(filesChanged), &e.FilesChanged); err != nil {
			return nil, fmt.Errorf("unmarshal files changed: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, created)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		e.Timestamp = parsed.UTC()
		events = append(events, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool call rows: %w", err)
	}
	return events, nil
}
