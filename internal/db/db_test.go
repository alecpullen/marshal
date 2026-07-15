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
