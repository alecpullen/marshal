package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"marshal/internal/tools/registry"
)

// SaveToolCall persists an audit event for a session, including the Milestone Q
// sandbox execution metadata (backend, network-isolated flag, applied limits,
// kill reason, duration) when present.
func (db *DB) SaveToolCall(sessionID string, event registry.AuditEvent) error {
	var exitCode sql.NullInt64
	if event.CommandExitCode != nil {
		exitCode = sql.NullInt64{Int64: int64(*event.CommandExitCode), Valid: true}
	}

	var normalizedInput any = event.FilesChanged
	if event.FilesChanged != nil {
		normalized := make([]string, len(event.FilesChanged))
		for i, p := range event.FilesChanged {
			normalized[i] = filepath.ToSlash(p)
		}
		normalizedInput = normalized
	}
	filesChanged, err := json.Marshal(normalizedInput)
	if err != nil {
		return fmt.Errorf("marshal files changed: %w", err)
	}

	hooksJSON, err := json.Marshal(event.Hooks)
	if err != nil {
		return fmt.Errorf("marshal hooks: %w", err)
	}

	var networkIsolated sql.NullInt64
	if event.Sandbox.Enabled {
		networkIsolated = sql.NullInt64{Int64: boolToInt(event.Sandbox.NetworkIsolated), Valid: true}
	}

	var backendVal, killedVal any
	if event.Sandbox.Enabled {
		backendVal = event.Sandbox.Backend
		if event.Sandbox.KilledReason != "" {
			killedVal = event.Sandbox.KilledReason
		}
	}
	var durVal any
	if event.Sandbox.DurationMS > 0 {
		durVal = event.Sandbox.DurationMS
	}

	// Guard LimitsJSON: non-sandbox tool calls shouldn't pay the map
	// alloc + JSON marshal. The column is nullable, so pass nil.
	var limitsJSON any
	if event.Sandbox.Enabled {
		s, mErr := event.Sandbox.LimitsJSON()
		if mErr != nil {
			// Best-effort: log and persist an empty object. The
			// audit row should not be lost because of a marshal
			// failure (which only happens for unmarshalable types
			// — defensive).
			log.Printf("tool_calls audit: LimitsJSON marshal failed: %v", mErr)
			s = "{}"
		}
		limitsJSON = s
	}

	_, err = db.sqlDB.Exec(
		`INSERT INTO tool_calls (session_id, agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, command_exit_code, files_changed, error, created_at,
		                          sandbox_backend, sandbox_network_isolated, sandbox_limits_json, sandbox_killed_reason, duration_ms, hooks_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		backendVal,
		networkIsolated,
		limitsJSON,
		killedVal,
		durVal,
		string(hooksJSON),
	)
	if err != nil {
		return fmt.Errorf("save tool call: %w", err)
	}
	return nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// GetToolCalls returns all audit events for a session in chronological order.
func (db *DB) GetToolCalls(sessionID string) ([]registry.AuditEvent, error) {
	rows, err := db.sqlDB.Query(
		`SELECT agent_role, model, tool_name, args_json, result_summary, risk_level, approval_state, command_exit_code, files_changed, error, created_at,
		        sandbox_backend, sandbox_network_isolated, sandbox_limits_json, sandbox_killed_reason, duration_ms, hooks_json
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
		var filesChanged sql.NullString
		var errorString sql.NullString
		var created string
		var sbBackend sql.NullString
		var sbNetwork sql.NullInt64
		var sbLimits sql.NullString
		var sbKilled sql.NullString
		var durMS sql.NullInt64
		var hooksJSON sql.NullString
		if err := rows.Scan(&e.AgentRole, &e.Model, &e.ToolName, &args, &e.ResultSummary, &risk, &approval, &exitCode, &filesChanged, &errorString, &created,
			&sbBackend, &sbNetwork, &sbLimits, &sbKilled, &durMS, &hooksJSON); err != nil {
			return nil, fmt.Errorf("scan tool call row: %w", err)
		}
		e.Args = []byte(args)
		e.Risk = registry.RiskLevel(risk)
		e.Approval = registry.ApprovalState(approval)
		if exitCode.Valid {
			code := int(exitCode.Int64)
			e.CommandExitCode = &code
		}
		if errorString.Valid {
			e.Error = errorString.String
		}
		if filesChanged.Valid && filesChanged.String != "" {
			if err := json.Unmarshal([]byte(filesChanged.String), &e.FilesChanged); err != nil {
				return nil, fmt.Errorf("unmarshal files changed: %w", err)
			}
		}
		parsed, err := time.Parse(time.RFC3339, created)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		e.Timestamp = parsed.UTC()
		if sbBackend.Valid && sbBackend.String != "" {
			e.Sandbox.Enabled = true
			e.Sandbox.Backend = sbBackend.String
		}
		if sbNetwork.Valid {
			e.Sandbox.NetworkIsolated = sbNetwork.Int64 == 1
		}
		if sbKilled.Valid {
			e.Sandbox.KilledReason = sbKilled.String
		}
		if durMS.Valid {
			e.Sandbox.DurationMS = durMS.Int64
		}
		if hooksJSON.Valid && hooksJSON.String != "" && hooksJSON.String != "[]" {
			if err := json.Unmarshal([]byte(hooksJSON.String), &e.Hooks); err != nil {
				return nil, fmt.Errorf("unmarshal hooks: %w", err)
			}
		}
		if sbLimits.Valid && sbLimits.String != "" {
			// Parse the JSON blob the SandboxMeta.LimitsJSON writer
			// produced on Save. Only the structured fields that were
			// written (limit values and capability flags) flow back;
			// Backend/NetworkIsolated/KilledReason/DurationMS above
			// are already populated from dedicated columns.
			var blob map[string]any
			if err := json.Unmarshal([]byte(sbLimits.String), &blob); err == nil {
				if v, ok := blob["memory_limit_bytes"]; ok {
					if f, fok := v.(float64); fok {
						e.Sandbox.MemoryLimitBytes = int64(f)
					}
				}
				if v, ok := blob["cpu_seconds"]; ok {
					if f, fok := v.(float64); fok {
						e.Sandbox.CPUSeconds = int(f)
					}
				}
				if v, ok := blob["max_processes"]; ok {
					if f, fok := v.(float64); fok {
						e.Sandbox.MaxProcesses = int(f)
					}
				}
				if v, ok := blob["network_isolated"]; ok {
					if b, bok := v.(bool); bok {
						e.Sandbox.NetworkIsolated = e.Sandbox.NetworkIsolated || b
					}
				}
				if v, ok := blob["filesystem_isolated"]; ok {
					if b, bok := v.(bool); bok {
						e.Sandbox.FilesystemIsolated = b
					}
				}
				if v, ok := blob["killed_reason"]; ok {
					if s, sok := v.(string); sok && e.Sandbox.KilledReason == "" {
						e.Sandbox.KilledReason = s
					}
				}
			}
		}
		events = append(events, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool call rows: %w", err)
	}
	return events, nil
}
