package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testSnapshotDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestSaveAndLatestSnapshot(t *testing.T) {
	db := testSnapshotDB(t)
	at := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	id, err := db.SaveSnapshot("session-1", 1, "abc123", []string{"a.go", "b.go"}, at)
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if id == 0 {
		t.Fatal("expected snapshot id")
	}

	gotID, hash, files, err := db.LatestSnapshot("session-1")
	if err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	}
	if gotID != id {
		t.Fatalf("expected id %d, got %d", id, gotID)
	}
	if hash != "abc123" {
		t.Fatalf("expected hash abc123, got %s", hash)
	}
	if len(files) != 2 || files[0] != "a.go" || files[1] != "b.go" {
		t.Fatalf("unexpected files: %v", files)
	}
}

func TestSnapshotBefore(t *testing.T) {
	db := testSnapshotDB(t)
	at := time.Now()

	db.SaveSnapshot("session-1", 1, "hash1", []string{"a.go"}, at)
	db.SaveSnapshot("session-1", 2, "hash2", []string{"a.go"}, at)
	db.SaveSnapshot("session-1", 3, "hash3", []string{"a.go"}, at)

	hash, err := db.SnapshotBefore("session-1", 3)
	if err != nil {
		t.Fatalf("SnapshotBefore: %v", err)
	}
	if hash != "hash2" {
		t.Fatalf("expected hash2 before turn 3, got %s", hash)
	}

	hash, err = db.SnapshotBefore("session-1", 1)
	if err != nil {
		t.Fatalf("SnapshotBefore: %v", err)
	}
	if hash != "" {
		t.Fatalf("expected no snapshot before turn 1, got %s", hash)
	}
}

func TestPruneSnapshotsOlderThan(t *testing.T) {
	db := testSnapshotDB(t)
	old := time.Now().AddDate(0, 0, -10)
	recent := time.Now()

	db.SaveSnapshot("session-1", 1, "old", []string{"a.go"}, old)
	db.SaveSnapshot("session-1", 2, "recent", []string{"a.go"}, recent)

	if err := db.PruneSnapshotsOlderThan(7); err != nil {
		t.Fatalf("PruneSnapshotsOlderThan: %v", err)
	}

	_, hash, _, err := db.LatestSnapshot("session-1")
	if err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	}
	if hash != "recent" {
		t.Fatalf("expected recent to survive, got %s", hash)
	}
}

func TestLatestSnapshotNotFound(t *testing.T) {
	db := testSnapshotDB(t)
	id, hash, files, err := db.LatestSnapshot("session-1")
	if err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	}
	if id != 0 || hash != "" || len(files) != 0 {
		t.Fatalf("expected empty result, got id=%d hash=%q files=%v", id, hash, files)
	}
}

func init() {
	_ = os.RemoveAll
	_ = sql.ErrNoRows
}
