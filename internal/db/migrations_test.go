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

func TestMigrationMemoryContentHashIdempotent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("first Migrate failed: %v", err)
	}

	// Re-running the migration body must be a no-op, not an error.
	tx, err := db.sqlDB.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := migrateMemoryContentHash(tx); err != nil {
		tx.Rollback()
		t.Fatalf("second migrateMemoryContentHash should be no-op, got: %v", err)
	}
	tx.Commit()
}

func TestMigrationMemoryContentHashBackfillsAndDedupes(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Create the base schema WITHOUT the migration (the content_hash column
	// is only added by migrateMemoryContentHash). The schema constant
	// contains the projects/memories CREATE TABLEs but not content_hash.
	if _, err := db.sqlDB.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	// Pre-migration rows: insert by hand with no content_hash, one duplicate.
	// FK enforcement is ON, so create the referenced project and session
	// rows first (source_session_id references agent_sessions).
	if _, err := db.sqlDB.Exec(
		`INSERT INTO projects (root_path, name, created_at, updated_at) VALUES ('/tmp/proj-mig', 'proj-mig', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.sqlDB.Exec(
		`INSERT INTO agent_sessions (id, project_id, started_at) VALUES ('s', 1, '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	for _, content := range []string{"Alpha.", "alpha.", "Beta."} {
		if _, err := db.sqlDB.Exec(
			`INSERT INTO memories (project_id, kind, content, confidence, source_session_id, created_at, updated_at)
			 VALUES (1, 'fact', ?, 'tentative', 's', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, content,
		); err != nil {
			t.Fatalf("insert memory %q: %v", content, err)
		}
	}

	// Apply the memory content_hash migration body directly.
	tx, err := db.sqlDB.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := migrateMemoryContentHash(tx); err != nil {
		tx.Rollback()
		t.Fatalf("migrateMemoryContentHash failed: %v", err)
	}
	tx.Commit()

	var nulls int
	if err := db.sqlDB.QueryRow(`SELECT COUNT(*) FROM memories WHERE content_hash IS NULL`).Scan(&nulls); err != nil {
		t.Fatalf("count nulls: %v", err)
	}
	if nulls != 0 {
		t.Fatalf("content_hash NULL rows = %d, want 0", nulls)
	}
	var total int
	if err := db.sqlDB.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Fatalf("rows after dedup = %d, want 2 (oldest Alpha kept)", total)
	}
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
