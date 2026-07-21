package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps a single-writer *sql.DB connection. Domain queries live in
// per-topic files (sessions.go, symbols.go, files.go, memories.go, …) as
// methods on *DB; SQLDB() exposes the raw *sql.DB for the few call sites
// that need it. Every query call site controls its own context lifecycle.
type DB struct {
	sqlDB *sql.DB
	locks *ProjectLocks
}

// Open opens a SQLite database with WAL mode and a single connection.
func Open(path string) (*DB, error) {
	sqlDB, err := openOneConnection(path)
	if err != nil {
		return nil, err
	}
	return &DB{sqlDB: sqlDB, locks: NewProjectLocks()}, nil
}

func (db *DB) SQLDB() *sql.DB { return db.sqlDB }

func (db *DB) Close() error {
	if db.sqlDB != nil {
		return db.sqlDB.Close()
	}
	return nil
}

// columnAdd is one backward-compatible column addition applied by Migrate
// when the column is absent (new databases already have it via schema).
type columnAdd struct {
	table string
	name  string
	def   string
}

var migrationColumns = []columnAdd{
	{"tool_calls", "command_exit_code", "INTEGER"},
	{"tool_calls", "files_changed", "TEXT"},
	{"tool_calls", "error", "TEXT"},
	{"tool_calls", "sandbox_backend", "TEXT"},
	{"tool_calls", "sandbox_network_isolated", "INTEGER"},
	{"tool_calls", "sandbox_limits_json", "TEXT"},
	{"tool_calls", "sandbox_killed_reason", "TEXT"},
	{"tool_calls", "duration_ms", "INTEGER"},
	{"tool_calls", "hooks_json", "TEXT NOT NULL DEFAULT '[]'"},
	{"tool_calls", "original_args_json", "TEXT"},
	{"tool_calls", "rewritten", "INTEGER DEFAULT 0"},
	{"tool_calls", "sandbox_enabled", "INTEGER NOT NULL DEFAULT 0"},
	{"tool_calls", "resource_limits", "INTEGER NOT NULL DEFAULT 0"},
	{"tool_calls", "output_truncated", "INTEGER NOT NULL DEFAULT 0"},
	{"files", "summary", "TEXT"},
	{"messages", "content_type", "TEXT"},
	{"messages", "reasoning", "TEXT"},
	{"messages", "think_duration_ms", "INTEGER"},
	{"messages", "final", "INTEGER DEFAULT 0"},
	// F14: message tree + session leaf.
	{"messages", "parent_id", "INTEGER REFERENCES messages(id) ON DELETE SET NULL"},
	{"agent_sessions", "leaf_message_id", "INTEGER"},
}

func (db *DB) Migrate() error {
	_, err := db.sqlDB.Exec(schema)
	if err != nil {
		return fmt.Errorf("execute database schema migrations: %w", err)
	}

	// Backward-compatible schema extensions: add any columns introduced
	// after the initial table creation. New databases already contain
	// these columns, so the additions are no-ops in that case. Each table
	// is introspected once.
	checked := map[string]map[string]bool{}
	for _, c := range migrationColumns {
		cols, ok := checked[c.table]
		if !ok {
			var err error
			cols, err = db.tableColumns(c.table)
			if err != nil {
				return fmt.Errorf("inspect %s columns: %w", c.table, err)
			}
			checked[c.table] = cols
		}
		if cols[c.name] {
			continue
		}
		query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", c.table, c.name, c.def)
		if _, err := db.sqlDB.Exec(query); err != nil {
			return fmt.Errorf("add column %s to %s: %w", c.name, c.table, err)
		}
		cols[c.name] = true
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

// allowedTableInfo lists the tables whose schema may be introspected via
// tableColumns. The list is the union of every table referenced by
// Migrate()'s column-add backfill branches.
var allowedTableInfo = map[string]bool{
	"tool_calls":     true,
	"files":          true,
	"messages":       true,
	"agent_sessions": true,
}

// tableColumns returns the set of column names for the given table.
func (db *DB) tableColumns(table string) (map[string]bool, error) {
	if !allowedTableInfo[table] {
		return nil, fmt.Errorf("tableColumns: table %q is not in the introspection allowlist", table)
	}
	// The table name is now provably constant; the Sprintf is still used
	// for clarity but the value is no longer user-controllable.
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
