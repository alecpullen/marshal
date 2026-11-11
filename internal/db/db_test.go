package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAndClose(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	if db.sqlDB == nil {
		t.Fatal("sql.DB is nil")
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("Database file was not created on disk")
	}
}

func TestMigrateCreatesTables(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// Verify tables exist
	tables := []string{"projects", "files", "agent_sessions", "messages", "tool_calls"}
	for _, table := range tables {
		var name string
		err := db.sqlDB.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s not found: %v", table, err)
		}
	}
}

// TestMigrateAddsFileLedgerRevisions covers the upgrade path for a database
// whose file ledger predates revisions and stable writer identities: Migrate
// must add the columns in place, and a fresh database must already have them.
func TestMigrateAddsFileLedgerRevisions(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Legacy ledger: no revision, no written_id.
	if _, err := db.sqlDB.Exec(`
		CREATE TABLE file_reads (
			session_id TEXT NOT NULL,
			path TEXT NOT NULL,
			read_at TEXT NOT NULL,
			PRIMARY KEY(session_id, path)
		);
		CREATE TABLE file_writes (
			session_id TEXT NOT NULL,
			path TEXT NOT NULL,
			written_at TEXT NOT NULL,
			written_by TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(session_id, path)
		);
	`); err != nil {
		t.Fatalf("create legacy tables: %v", err)
	}

	assertLedgerRevisions(t, db, "upgraded")

	fresh, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	defer fresh.Close()
	if err := fresh.Migrate(); err != nil {
		t.Fatalf("Migrate fresh: %v", err)
	}
	assertLedgerRevisions(t, fresh, "fresh")
}

// assertLedgerRevisions migrates db if it has not been migrated yet and asserts
// both ledger tables carry the revision columns.
func assertLedgerRevisions(t *testing.T, db *DB, name string) {
	t.Helper()
	if err := db.Migrate(); err != nil {
		t.Fatalf("%s: Migrate failed: %v", name, err)
	}
	readCols, err := db.tableColumns("file_reads")
	if err != nil {
		t.Fatalf("%s: tableColumns(file_reads): %v", name, err)
	}
	if !readCols["revision"] {
		t.Fatalf("%s: file_reads lacks revision; columns = %v", name, readCols)
	}
	writeCols, err := db.tableColumns("file_writes")
	if err != nil {
		t.Fatalf("%s: tableColumns(file_writes): %v", name, err)
	}
	for _, col := range []string{"written_id", "revision"} {
		if !writeCols[col] {
			t.Fatalf("%s: file_writes lacks %s; columns = %v", name, col, writeCols)
		}
	}
}

func TestTableColumnsRejectsUnlistedTable(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	tests := []struct {
		name  string
		table string
	}{
		{"completely unknown table", "attacker"},
		{"SQL injection attempt", "tool_calls; DROP TABLE x; --"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cols, err := db.tableColumns(tt.table)
			if err == nil {
				t.Fatalf("tableColumns(%q) = %v, expected error", tt.table, cols)
			}
			if !strings.Contains(err.Error(), tt.table) {
				t.Errorf("tableColumns(%q) error = %q, expected it to contain the table name", tt.table, err.Error())
			}
		})
	}
}
