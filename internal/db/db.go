package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type DB struct {
	sqlDB *sql.DB
}

func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	// The agent persists messages and tool calls from parallel goroutines
	// (parallel tool execution and the swarm runtime). SQLite permits only one
	// writer at a time, so pin the pool to a single connection to serialize
	// writers, and set a busy timeout so any residual contention (migrations,
	// external handles) waits instead of returning SQLITE_BUSY. Without this,
	// concurrent writes fail with "database is locked".
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if _, err := sqlDB.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("set sqlite busy_timeout: %w", err)
	}

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite db: %w", err)
	}

	return &DB{sqlDB: sqlDB}, nil
}

func (db *DB) SQLDB() *sql.DB { return db.sqlDB }

func (db *DB) Close() error {
	if db.sqlDB == nil {
		return nil
	}
	return db.sqlDB.Close()
}

func (db *DB) Migrate() error {
	_, err := db.sqlDB.Exec(schema)
	if err != nil {
		return fmt.Errorf("execute database schema migrations: %w", err)
	}

	// Backward-compatible schema extensions: add any columns introduced after
	// the initial tool_calls table creation. New databases already contain
	// these columns, so the additions are no-ops in that case.
	columns, err := db.tableColumns("tool_calls")
	if err != nil {
		return fmt.Errorf("inspect tool_calls columns: %w", err)
	}
	columnDefs := map[string]string{
		"command_exit_code":        "INTEGER",
		"files_changed":            "TEXT",
		"error":                    "TEXT",
		"sandbox_backend":          "TEXT",
		"sandbox_network_isolated": "INTEGER",
		"sandbox_limits_json":      "TEXT",
		"sandbox_killed_reason":    "TEXT",
		"duration_ms":              "INTEGER",
	}
	for name, def := range columnDefs {
		if columns[name] {
			continue
		}
		query := fmt.Sprintf("ALTER TABLE tool_calls ADD COLUMN %s %s", name, def)
		if _, err := db.sqlDB.Exec(query); err != nil {
			return fmt.Errorf("add column %s to tool_calls: %w", name, err)
		}
	}

	fileColumns, err := db.tableColumns("files")
	if err != nil {
		return fmt.Errorf("inspect files columns: %w", err)
	}
	if !fileColumns["summary"] {
		if _, err := db.sqlDB.Exec(`ALTER TABLE files ADD COLUMN summary TEXT`); err != nil {
			return fmt.Errorf("add column summary to files: %w", err)
		}
	}

	messageColumns, err := db.tableColumns("messages")
	if err != nil {
		return fmt.Errorf("inspect messages columns: %w", err)
	}
	messageColumnDefs := map[string]string{
		"content_type":      "TEXT",
		"reasoning":         "TEXT",
		"think_duration_ms": "INTEGER",
		"final":             "INTEGER DEFAULT 0",
	}
	for name, def := range messageColumnDefs {
		if messageColumns[name] {
			continue
		}
		query := fmt.Sprintf("ALTER TABLE messages ADD COLUMN %s %s", name, def)
		if _, err := db.sqlDB.Exec(query); err != nil {
			return fmt.Errorf("add column %s to messages: %w", name, err)
		}
	}

	// F14: message tree + session leaf.
	messageTreeCols, err := db.tableColumns("messages")
	if err != nil {
		return fmt.Errorf("inspect messages columns for parent_id: %w", err)
	}
	if !messageTreeCols["parent_id"] {
		if _, err := db.sqlDB.Exec(`ALTER TABLE messages ADD COLUMN parent_id INTEGER REFERENCES messages(id) ON DELETE SET NULL`); err != nil {
			return fmt.Errorf("add column parent_id to messages: %w", err)
		}
	}
	sessionCols, err := db.tableColumns("agent_sessions")
	if err != nil {
		return fmt.Errorf("inspect agent_sessions columns for leaf: %w", err)
	}
	if !sessionCols["leaf_message_id"] {
		if _, err := db.sqlDB.Exec(`ALTER TABLE agent_sessions ADD COLUMN leaf_message_id INTEGER`); err != nil {
			return fmt.Errorf("add column leaf_message_id to agent_sessions: %w", err)
		}
	}
	// Backfill existing linear rows: parent = previous row in the same session.
	if _, err := db.sqlDB.Exec(`
		UPDATE messages SET parent_id = (
			SELECT m2.id FROM messages m2
			WHERE m2.session_id = messages.session_id AND m2.id < messages.id
			ORDER BY m2.id DESC LIMIT 1
		) WHERE parent_id IS NULL
	`); err != nil {
		return fmt.Errorf("backfill parent_id: %w", err)
	}
	// Set each session's leaf to its max message id.
	if _, err := db.sqlDB.Exec(`
		UPDATE agent_sessions SET leaf_message_id = (
			SELECT MAX(id) FROM messages WHERE messages.session_id = agent_sessions.id
		) WHERE leaf_message_id IS NULL AND EXISTS (
			SELECT 1 FROM messages WHERE messages.session_id = agent_sessions.id
		)
	`); err != nil {
		return fmt.Errorf("backfill leaf_message_id: %w", err)
	}

	return nil
}

// tableColumns returns the set of column names for the given table.
func (db *DB) tableColumns(table string) (map[string]bool, error) {
	rows, err := db.sqlDB.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, fmt.Errorf("pragma table_info for %s: %w", table, err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &pk); err != nil {
			return nil, fmt.Errorf("scan table_info row: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate table_info rows: %w", err)
	}
	return columns, nil
}

func (db *DB) exec(query string, args ...any) (sql.Result, error) {
	return db.sqlDB.Exec(query, args...)
}

func (db *DB) queryRow(query string, args ...any) *sql.Row {
	return db.sqlDB.QueryRow(query, args...)
}
