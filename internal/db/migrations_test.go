package db

import (
	"testing"
)

func TestMigrationToolAuditAddContentIdempotent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("first Migrate failed: %v", err)
	}

	// Manually run the content-column migration a second time to simulate
	// a re-run. It should be a no-op, not an error.
	tx, err := db.sqlDB.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := migrationToolAuditAddContent(tx); err != nil {
		tx.Rollback()
		t.Fatalf("second migrationToolAuditAddContent should be no-op, got: %v", err)
	}
	tx.Commit()
}

func TestMigrations(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	// Verify the migration tracking table was created.
	var name string
	err = db.sqlDB.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&name)
	if err != nil {
		t.Fatalf("schema_migrations table not found: %v", err)
	}

	// Verify scratchpad_entries table exists with the expected columns.
	rows, err := db.sqlDB.Query("PRAGMA table_info(scratchpad_entries)")
	if err != nil {
		t.Fatalf("pragma table_info(scratchpad_entries) failed: %v", err)
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var colName, typ string
		var notNull, pk int
		var dfltValue interface{}
		if err := rows.Scan(&cid, &colName, &typ, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		cols[colName] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info rows: %v", err)
	}

	expected := []string{"session_id", "entry_key", "content", "format", "updated", "size_bytes"}
	for _, col := range expected {
		if !cols[col] {
			t.Errorf("expected column %q in scratchpad_entries", col)
		}
	}

	// Verify the migration was recorded.
	var version int
	err = db.sqlDB.QueryRow("SELECT version FROM schema_migrations WHERE version = 1").Scan(&version)
	if err != nil {
		t.Fatalf("migration version 1 not recorded: %v", err)
	}

	// Idempotency: running Migrate again should succeed and be a no-op.
	if err := db.Migrate(); err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}
}
