package filetrack

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS file_reads (
			session_id TEXT NOT NULL,
			path TEXT NOT NULL,
			read_at TEXT NOT NULL,
			PRIMARY KEY(session_id, path)
		);
		CREATE TABLE IF NOT EXISTS file_writes (
			session_id TEXT NOT NULL,
			path TEXT NOT NULL,
			written_at TEXT NOT NULL,
			PRIMARY KEY(session_id, path)
		);
	`)
	if err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return db
}

func TestRecordReadAndLastReadTime(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	err := svc.RecordRead("src/main.go", now)
	if err != nil {
		t.Fatalf("RecordRead: %v", err)
	}

	got, ok, err := svc.LastReadTime("src/main.go")
	if err != nil {
		t.Fatalf("LastReadTime: %v", err)
	}
	if !ok {
		t.Fatal("expected file to be found")
	}
	if !got.Equal(now) {
		t.Fatalf("expected %v, got %v", now, got)
	}
}

func TestLastReadTimeNotFound(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")

	_, ok, err := svc.LastReadTime("nonexistent.go")
	if err != nil {
		t.Fatalf("LastReadTime: %v", err)
	}
	if ok {
		t.Fatal("expected file not found")
	}
}

func TestRecordReadUpsert(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	first := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	second := time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)

	if err := svc.RecordRead("src/main.go", first); err != nil {
		t.Fatalf("first RecordRead: %v", err)
	}
	if err := svc.RecordRead("src/main.go", second); err != nil {
		t.Fatalf("second RecordRead: %v", err)
	}

	got, ok, err := svc.LastReadTime("src/main.go")
	if err != nil {
		t.Fatalf("LastReadTime: %v", err)
	}
	if !ok {
		t.Fatal("expected file to be found")
	}
	if !got.Equal(second) {
		t.Fatalf("expected %v, got %v", second, got)
	}
}

func TestRecordWrite(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	now := time.Now()

	if err := svc.RecordWrite("src/main.go", now); err != nil {
		t.Fatalf("RecordWrite: %v", err)
	}
	if err := svc.RecordWrite("src/lib.go", now); err != nil {
		t.Fatalf("RecordWrite: %v", err)
	}
}

func TestSessionIsolation(t *testing.T) {
	db := testDB(t)
	svc1 := New(db, "session-1")
	svc2 := New(db, "session-2")
	now := time.Now()

	svc1.RecordRead("a.go", now)
	svc2.RecordRead("b.go", now)

	// svc1 should see a.go but not b.go
	_, ok1, _ := svc1.LastReadTime("a.go")
	if !ok1 {
		t.Fatal("session-1 should have a.go")
	}
	_, ok1b, _ := svc1.LastReadTime("b.go")
	if ok1b {
		t.Fatal("session-1 should NOT have b.go")
	}

	// svc2 should see b.go but not a.go
	_, ok2, _ := svc2.LastReadTime("b.go")
	if !ok2 {
		t.Fatal("session-2 should have b.go")
	}
	_, ok2b, _ := svc2.LastReadTime("a.go")
	if ok2b {
		t.Fatal("session-2 should NOT have a.go")
	}
}

func TestRecordWriteUpsert(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	first := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	second := time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)

	if err := svc.RecordWrite("src/main.go", first); err != nil {
		t.Fatalf("first RecordWrite: %v", err)
	}
	if err := svc.RecordWrite("src/main.go", second); err != nil {
		t.Fatalf("second RecordWrite: %v", err)
	}
}

func init() {
	// Suppress "no test files" warning for the temp dir cleanup
	_ = os.RemoveAll
}

// TestLastReadTimeRejectsUnparseableTimestamp covers a row whose read_at is
// not valid RFC3339. The parse error used to be discarded, so the caller was
// handed a zero time reported as a genuine read time — and the read-before-
// write staleness gate has no way to tell that apart from a real timestamp.
func TestLastReadTimeRejectsUnparseableTimestamp(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	if _, err := db.Exec(
		`INSERT INTO file_reads (session_id, path, read_at) VALUES (?, ?, ?)`,
		"session-1", "/tmp/x.go", "not-a-timestamp"); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	got, ok, err := svc.LastReadTime("/tmp/x.go")
	if err == nil {
		t.Fatal("expected an error for an unparseable read_at, got nil")
	}
	if ok {
		t.Error("ok = true, want false when the timestamp cannot be parsed")
	}
	if !got.IsZero() {
		t.Errorf("time = %v, want zero", got)
	}
}
