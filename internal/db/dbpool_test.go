package db

import (
	"path/filepath"
	"testing"
)

func TestOpenWithPoolClampsNonPositivePoolSize(t *testing.T) {
	// readPoolSize = 0 should clamp to 1.
	path0 := filepath.Join(t.TempDir(), "test0.db")
	db0, err := OpenWithPool(path0, 0)
	if err != nil {
		t.Fatalf("OpenWithPool(path, 0): %v", err)
	}
	defer db0.Close()

	var v int
	if err := db0.readDB.QueryRow("SELECT 1").Scan(&v); err != nil {
		t.Fatalf("SELECT 1 on read pool after OpenWithPool(path, 0): %v", err)
	}
	if v != 1 {
		t.Errorf("expected 1, got %d", v)
	}

	// readPoolSize = -5 should clamp to 1.
	pathNeg := filepath.Join(t.TempDir(), "test_neg.db")
	dbNeg, err := OpenWithPool(pathNeg, -5)
	if err != nil {
		t.Fatalf("OpenWithPool(path, -5): %v", err)
	}
	defer dbNeg.Close()

	if err := dbNeg.readDB.QueryRow("SELECT 1").Scan(&v); err != nil {
		t.Fatalf("SELECT 1 on read pool after OpenWithPool(path, -5): %v", err)
	}
	if v != 1 {
		t.Errorf("expected 1, got %d", v)
	}
}

func TestCloseOnFreshDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close on fresh DB: %v", err)
	}
}

func TestOpenEnablesWAL(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "wal.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var mode string
	if err := db.sqlDB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", mode)
	}
}
