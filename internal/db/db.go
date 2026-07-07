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
		"command_exit_code": "INTEGER",
		"files_changed":     "TEXT",
		"error":             "TEXT",
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
