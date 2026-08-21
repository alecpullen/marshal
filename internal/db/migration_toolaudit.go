package db

import (
	"database/sql"
	"fmt"
)

// migrationToolAudit creates the turn_tool_audit table used by the
// cross-turn ledger (Task 3/A1 of the context-management plan). Each
// row records one tool call's compact summary plus tokens spent, scoped
// to the assistant message DB id that contains the call. Best-effort
// persistence: the caller (Runner / session.State) is expected to
// log-and-continue on failure, never to break the turn.
func migrationToolAudit(tx *sql.Tx) error {
	_, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS turn_tool_audit (
			session_id    TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
			message_db_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
			seq           INTEGER NOT NULL,
			tool          TEXT NOT NULL,
			summary       TEXT NOT NULL,
			ok            INTEGER NOT NULL DEFAULT 1,
			tokens        INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (session_id, message_db_id, seq)
		)
	`)
	return err
}

// init is registered from migrations.go via direct append.
func init() {
	migrations = append(migrations, migrationToolAudit)
	migrations = append(migrations, migrationToolAuditAddContent)
}

// migrationToolAuditAddContent adds a content column to the
// turn_tool_audit table so the full text of agent.run results can be
// replayed across turns. It checks whether the column already exists so
// re-running the migration is a no-op rather than an error.
func migrationToolAuditAddContent(tx *sql.Tx) error {
	// Check if the column already exists before running ALTER TABLE.
	// This makes the migration idempotent.
	rows, err := tx.Query("PRAGMA table_info(turn_tool_audit)")
	if err != nil {
		return fmt.Errorf("check turn_tool_audit columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan table_info: %w", err)
		}
		if name == "content" {
			return nil // Column already exists — no-op.
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table_info: %w", err)
	}
	_, err = tx.Exec(`ALTER TABLE turn_tool_audit ADD COLUMN content TEXT NOT NULL DEFAULT ''`)
	return err
} // ToolAuditEntry is the compact summary of a single tool call inside a
// turn, persisted into the turn_tool_audit table. The Summary field
// holds a tool-specific one-line description (e.g. "a.go:1-80 (412t)"
// for a file.read, "go test ./..." for a shell.run). Ok is false for
// tools that returned an error or were denied. Tokens is an optional
// hint of how big the result was — used to keep the ledger informative
// without storing the full result content. Content holds the full
// agent.run result text when available.
type ToolAuditEntry struct {
	Seq     int
	Tool    string
	Summary string
	Ok      bool
	Tokens  int
	Content string
}

// SaveTurnToolAudit replaces the audit entries for a single
// (session, message) pair. The runner calls this once at the end of
// each turn (or whenever it transitions message). Best-effort: caller
// is expected to wrap any non-nil error in a log line and continue.
//
// Implementation note: we DELETE then INSERT so a turn that reissued
// calls (after a corrupt response, say) replaces stale rows. This is
// cheaper than diffing and is fine because the table is per-message
// and message is the natural rollup unit.
func (db *DB) SaveTurnToolAudit(sessionID string, messageDBID int64, entries []ToolAuditEntry) error {
	if db == nil || db.sqlDB == nil {
		return nil
	}
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin save turn tool audit: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM turn_tool_audit WHERE session_id = ? AND message_db_id = ?`, sessionID, messageDBID); err != nil {
		return fmt.Errorf("delete turn tool audit: %w", err)
	}
	for i, e := range entries {
		seq := e.Seq
		if seq == 0 {
			seq = i + 1
		}
		ok := 0
		if e.Ok {
			ok = 1
		}
		if _, err := tx.Exec(
			`INSERT INTO turn_tool_audit (session_id, message_db_id, seq, tool, summary, ok, tokens, content) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sessionID, messageDBID, seq, e.Tool, e.Summary, ok, e.Tokens, e.Content,
		); err != nil {
			return fmt.Errorf("insert turn tool audit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit save turn tool audit: %w", err)
	}
	return nil
}

// LoadTurnToolAudit returns the audit entries for one message in
// order. Empty slice and nil error when no rows exist (so a turn that
// never made a tool call is signalled as an empty ledger; callers can
// decide whether that means "show the legacy placeholder" or
// "show nothing").
func (db *DB) LoadTurnToolAudit(sessionID string, messageDBID int64) ([]ToolAuditEntry, error) {
	if db == nil || db.sqlDB == nil {
		return nil, nil
	}
	rows, err := db.sqlDB.Query(
		`SELECT seq, tool, summary, ok, tokens, content FROM turn_tool_audit WHERE session_id = ? AND message_db_id = ? ORDER BY seq ASC`,
		sessionID, messageDBID,
	)
	if err != nil {
		return nil, fmt.Errorf("query turn tool audit: %w", err)
	}
	defer rows.Close()
	var out []ToolAuditEntry
	for rows.Next() {
		var e ToolAuditEntry
		var ok int
		if err := rows.Scan(&e.Seq, &e.Tool, &e.Summary, &ok, &e.Tokens, &e.Content); err != nil {
			return nil, fmt.Errorf("scan turn tool audit: %w", err)
		}
		e.Ok = ok == 1
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate turn tool audit: %w", err)
	}
	return out, nil
}

// LoadAllTurnToolAudit flattens every turn's audit for a session,
// grouped by message DB id in chronological order. Used by buildHistoryMessages
// to feed the ledger reconstructor.
func (db *DB) LoadAllTurnToolAudit(sessionID string) (map[int64][]ToolAuditEntry, error) {
	if db == nil || db.sqlDB == nil {
		return map[int64][]ToolAuditEntry{}, nil
	}
	rows, err := db.sqlDB.Query(
		`SELECT message_db_id, seq, tool, summary, ok, tokens, content FROM turn_tool_audit WHERE session_id = ? ORDER BY message_db_id ASC, seq ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query all turn tool audit: %w", err)
	}
	defer rows.Close()
	out := make(map[int64][]ToolAuditEntry)
	for rows.Next() {
		var msgDB int64
		var e ToolAuditEntry
		var ok int
		if err := rows.Scan(&msgDB, &e.Seq, &e.Tool, &e.Summary, &ok, &e.Tokens, &e.Content); err != nil {
			return nil, fmt.Errorf("scan all turn tool audit: %w", err)
		}
		e.Ok = ok == 1
		out[msgDB] = append(out[msgDB], e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate all turn tool audit: %w", err)
	}
	return out, nil
}

// Suppress unused-symbol linter: this file's only callers are the
// agent package and tests in db/, both of which import db via the
// regular path. The agenttest / registry imports belong in those
// files, not here.
