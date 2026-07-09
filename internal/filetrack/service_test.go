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

func TestListReadFiles(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	now := time.Now()

	svc.RecordRead("a.go", now)
	svc.RecordRead("b.go", now)
	svc.RecordRead("c.go", now)

	paths, err := svc.ListReadFiles()
	if err != nil {
		t.Fatalf("ListReadFiles: %v", err)
	}
	if len(paths) != 3 {
		t.Fatalf("expected 3 paths, got %d", len(paths))
	}
	if paths[0] != "a.go" || paths[1] != "b.go" || paths[2] != "c.go" {
		t.Fatalf("unexpected paths: %v", paths)
	}
}

func TestListReadFilesEmpty(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")

	paths, err := svc.ListReadFiles()
	if err != nil {
		t.Fatalf("ListReadFiles: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("expected 0 paths, got %d", len(paths))
	}
}

func TestRecordWriteAndListWrittenFiles(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	now := time.Now()

	svc.RecordWrite("src/main.go", now)
	svc.RecordWrite("src/lib.go", now)

	paths, err := svc.ListWrittenFiles()
	if err != nil {
		t.Fatalf("ListWrittenFiles: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(paths))
	}
}

func TestSessionIsolation(t *testing.T) {
	db := testDB(t)
	svc1 := New(db, "session-1")
	svc2 := New(db, "session-2")
	now := time.Now()

	svc1.RecordRead("a.go", now)
	svc2.RecordRead("b.go", now)

	paths1, _ := svc1.ListReadFiles()
	paths2, _ := svc2.ListReadFiles()

	if len(paths1) != 1 || paths1[0] != "a.go" {
		t.Fatalf("session-1 unexpected paths: %v", paths1)
	}
	if len(paths2) != 1 || paths2[0] != "b.go" {
		t.Fatalf("session-2 unexpected paths: %v", paths2)
	}
}

func TestRecordWriteUpsert(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	first := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	second := time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)

	svc.RecordWrite("src/main.go", first)
	svc.RecordWrite("src/main.go", second)

	paths, _ := svc.ListWrittenFiles()
	if len(paths) != 1 {
		t.Fatalf("expected 1 path after upsert, got %d", len(paths))
	}
}

func init() {
	// Suppress "no test files" warning for the temp dir cleanup
	_ = os.RemoveAll
}
