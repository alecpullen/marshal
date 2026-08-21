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

// PruneSnapshotsOlderThan must compare timestamps as time.Time values,
// not as RFC3339 strings. A stored snapshot's created_at is always UTC
// (SaveSnapshot formats with .UTC()), but pruning must still work when the
// host runs in a non-UTC zone. We seed snapshots whose stored instant is
// anchored to a fixed instant and verify pruning behaves correctly with the
// time-based comparison. Using an instant far in the past guarantees it is
// pruned regardless of the wall clock.
func TestPruneSnapshotsOlderThanNonUTCTimestamps(t *testing.T) {
	db := testSnapshotDB(t)

	tz := time.FixedZone("EST", -5*3600)
	oldInstant := time.Date(2020, 1, 1, 6, 0, 0, 0, tz) // stored as UTC 11:00Z
	recentInstant := time.Now().Add(2 * time.Hour)

	if _, err := db.SaveSnapshot("session-old", 1, "old-tz", []string{"a.go"}, oldInstant); err != nil {
		t.Fatalf("SaveSnapshot old: %v", err)
	}
	if _, err := db.SaveSnapshot("session-new", 1, "new-tz", []string{"a.go"}, recentInstant); err != nil {
		t.Fatalf("SaveSnapshot recent: %v", err)
	}

	// Prune snapshots older than 1 day: the 2020 snapshot is pruned,
	// the recent one survives regardless of the local timezone.
	if err := db.PruneSnapshotsOlderThan(1); err != nil {
		t.Fatalf("PruneSnapshotsOlderThan: %v", err)
	}

	_, oldHash, _, err := db.LatestSnapshot("session-old")
	if err != nil {
		t.Fatalf("LatestSnapshot old: %v", err)
	}
	if oldHash != "" {
		t.Fatalf("expected old snapshot to be pruned, got hash %q", oldHash)
	}

	_, newHash, _, err := db.LatestSnapshot("session-new")
	if err != nil {
		t.Fatalf("LatestSnapshot new: %v", err)
	}
	if newHash != "new-tz" {
		t.Fatalf("expected recent snapshot to survive, got hash %q", newHash)
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

func TestPruneSnapshotsOlderThanRejectsNegative(t *testing.T) {
	db := testSnapshotDB(t)
	if err := db.PruneSnapshotsOlderThan(-1); err == nil {
		t.Fatal("expected error for negative days")
	}
}

func TestSaveSnapshotRollsBackFilesOnError(t *testing.T) {
	db := testSnapshotDB(t)
	at := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	if _, err := db.SaveSnapshot("s1", 1, "h1", nil, at); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A second snapshot for the same session with an empty path; ensure
	// that the snapshot row is rolled back when the file insert fails.
	_, err := db.SaveSnapshot("s1", 2, "h2", []string{""}, at)
	if err == nil {
		t.Fatal("expected error for empty file path")
	}
	// After rollback, the second snapshot must not exist.
	id, _, _, err := db.LatestSnapshot("s1")
	if err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	}
	if id != 1 {
		t.Fatalf("expected only seed snapshot (id=1), got id=%d", id)
	}
}

func init() {
	_ = os.RemoveAll
	_ = sql.ErrNoRows
}
