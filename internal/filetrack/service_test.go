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
			revision INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(session_id, path)
		);
		CREATE TABLE IF NOT EXISTS file_writes (
			session_id TEXT NOT NULL,
			path TEXT NOT NULL,
			written_at TEXT NOT NULL,
			written_by TEXT NOT NULL DEFAULT '',
			written_id TEXT NOT NULL DEFAULT '',
			revision INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(session_id, path)
		);
	`)
	if err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return db
}

func TestRecordReadAndReadState(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	err := svc.RecordRead("src/main.go", now, 0)
	if err != nil {
		t.Fatalf("RecordRead: %v", err)
	}

	got, err := svc.ReadState("src/main.go")
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if !got.OK {
		t.Fatal("expected file to be found")
	}
	if got.Revision != 0 {
		t.Fatalf("revision = %d, want 0", got.Revision)
	}
	if !got.At.Equal(now) {
		t.Fatalf("expected %v, got %v", now, got.At)
	}
}

func TestReadStateNotFound(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")

	got, err := svc.ReadState("nonexistent.go")
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if got.OK {
		t.Fatal("expected file not found")
	}
}

func TestRecordReadUpsert(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	first := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	second := time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)

	if err := svc.RecordRead("src/main.go", first, 0); err != nil {
		t.Fatalf("first RecordRead: %v", err)
	}
	if err := svc.RecordRead("src/main.go", second, 0); err != nil {
		t.Fatalf("second RecordRead: %v", err)
	}

	got, err := svc.ReadState("src/main.go")
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if !got.OK {
		t.Fatal("expected file to be found")
	}
	if !got.At.Equal(second) {
		t.Fatalf("expected %v, got %v", second, got.At)
	}
}

func TestRecordWrite(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	now := time.Now()

	if err := svc.RecordWrite("src/main.go", now, Actor{ID: "main", Label: "the main agent"}); err != nil {
		t.Fatalf("RecordWrite: %v", err)
	}
	if err := svc.RecordWrite("src/lib.go", now, Actor{ID: "subagent-1", Label: "a subagent"}); err != nil {
		t.Fatalf("RecordWrite: %v", err)
	}
}

// TestWriteStateNamesTheAgent pins the writer attribution a stale-file error
// now reports: the reader is told which agent invalidated its view, and a path
// the session never wrote reports no writer rather than a guess.
func TestWriteStateNamesTheAgent(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	first := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	second := time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)

	if err := svc.RecordWrite("src/main.go", first, Actor{ID: "main", Label: "the main agent"}); err != nil {
		t.Fatalf("first RecordWrite: %v", err)
	}
	write, err := svc.WriteState("src/main.go")
	if err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	if !write.OK || write.Actor != (Actor{ID: "main", Label: "the main agent"}) || !write.At.Equal(first) {
		t.Fatalf("WriteState = %+v, want the main agent @ %v", write, first)
	}
	if write.Revision != 1 {
		t.Fatalf("revision = %d, want 1", write.Revision)
	}

	// An upsert must replace the writer and advance the revision.
	if err := svc.RecordWrite("src/main.go", second, Actor{ID: "subagent-1", Label: "a subagent"}); err != nil {
		t.Fatalf("second RecordWrite: %v", err)
	}
	write, err = svc.WriteState("src/main.go")
	if err != nil {
		t.Fatalf("WriteState after upsert: %v", err)
	}
	if !write.OK || write.Actor.Label != "a subagent" || !write.At.Equal(second) {
		t.Fatalf("WriteState = %+v, want a subagent @ %v", write, second)
	}
	if write.Actor.ID != "subagent-1" {
		t.Fatalf("actor ID = %q, want subagent-1", write.Actor.ID)
	}
	if write.Revision != 2 {
		t.Fatalf("revision after upsert = %d, want 2", write.Revision)
	}

	// A path written outside the session has no session writer.
	if write, err := svc.WriteState("src/untouched.go"); err != nil || write.OK {
		t.Fatalf("WriteState for an unwritten path = %+v, err %v; want OK=false, err=nil", write, err)
	}
}

// TestRecordWriteAdvancesRevision pins the monotonic write counter: each
// committed write bumps it, so a reader can tell which write its view predates.
func TestRecordWriteAdvancesRevision(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	first := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	second := time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)

	if err := svc.RecordWrite("src/main.go", first, Actor{ID: "main", Label: "the main agent"}); err != nil {
		t.Fatalf("first RecordWrite: %v", err)
	}
	write, err := svc.WriteState("src/main.go")
	if err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	if write.Revision != 1 {
		t.Fatalf("revision after first write = %d, want 1", write.Revision)
	}

	if err := svc.RecordWrite("src/main.go", second, Actor{ID: "subagent-1", Label: "a subagent"}); err != nil {
		t.Fatalf("second RecordWrite: %v", err)
	}
	write, err = svc.WriteState("src/main.go")
	if err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	if write.Revision != 2 {
		t.Fatalf("revision after second write = %d, want 2", write.Revision)
	}
}

// TestRecordWriteDoesNotCreateRead pins that a write is not a read: the writer
// exemption (not a fabricated read mark) is what lets the writer edit again.
func TestRecordWriteDoesNotCreateRead(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")

	if err := svc.RecordWrite("src/main.go", time.Now(), Actor{ID: "main", Label: "the main agent"}); err != nil {
		t.Fatalf("RecordWrite: %v", err)
	}
	read, err := svc.ReadState("src/main.go")
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if read.OK {
		t.Fatalf("ReadState after RecordWrite = %+v, want OK=false", read)
	}
}

// TestRecordReadStoresObservedRevision pins the conservative read semantics: the
// read records the revision it observed, an existing higher revision is never
// regressed, and a lower-revision read does not move the timestamp forward.
func TestRecordReadStoresObservedRevision(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	first := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	second := time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)

	if err := svc.RecordWrite("src/main.go", first, Actor{ID: "main", Label: "the main agent"}); err != nil {
		t.Fatalf("RecordWrite: %v", err)
	}
	if err := svc.RecordRead("src/main.go", first, 1); err != nil {
		t.Fatalf("RecordRead at revision 1: %v", err)
	}
	read, err := svc.ReadState("src/main.go")
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if !read.OK || read.Revision != 1 {
		t.Fatalf("ReadState = %+v, want revision 1", read)
	}
	if !read.At.Equal(first) {
		t.Fatalf("read time = %v, want %v", read.At, first)
	}

	// A later read that observed an older revision must not regress the
	// recorded revision, and must not move the timestamp forward either.
	if err := svc.RecordRead("src/main.go", second, 0); err != nil {
		t.Fatalf("RecordRead at revision 0: %v", err)
	}
	read, err = svc.ReadState("src/main.go")
	if err != nil {
		t.Fatalf("ReadState after lower-revision read: %v", err)
	}
	if read.Revision != 1 {
		t.Fatalf("revision = %d, want it to stay 1", read.Revision)
	}
	if read.At.After(first) {
		t.Fatalf("read time = %v, want it not to regress past %v", read.At, first)
	}
}

func TestSessionIsolation(t *testing.T) {
	db := testDB(t)
	svc1 := New(db, "session-1")
	svc2 := New(db, "session-2")
	now := time.Now()

	svc1.RecordRead("a.go", now, 0)
	svc2.RecordRead("b.go", now, 0)

	// svc1 should see a.go but not b.go
	read1, _ := svc1.ReadState("a.go")
	if !read1.OK {
		t.Fatal("session-1 should have a.go")
	}
	read1b, _ := svc1.ReadState("b.go")
	if read1b.OK {
		t.Fatal("session-1 should NOT have b.go")
	}

	// svc2 should see b.go but not a.go
	read2, _ := svc2.ReadState("b.go")
	if !read2.OK {
		t.Fatal("session-2 should have b.go")
	}
	read2b, _ := svc2.ReadState("a.go")
	if read2b.OK {
		t.Fatal("session-2 should NOT have a.go")
	}
}

func init() {
	// Suppress "no test files" warning for the temp dir cleanup
	_ = os.RemoveAll
}

// TestReadStateRejectsUnparseableTimestamp covers a row whose read_at is not
// valid RFC3339. The parse error used to be discarded, so the caller was handed
// a zero time reported as a genuine read time — and the read-before-write
// staleness gate has no way to tell that apart from a real timestamp.
func TestReadStateRejectsUnparseableTimestamp(t *testing.T) {
	db := testDB(t)
	svc := New(db, "session-1")
	if _, err := db.Exec(
		`INSERT INTO file_reads (session_id, path, read_at) VALUES (?, ?, ?)`,
		"session-1", "/tmp/x.go", "not-a-timestamp"); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	got, err := svc.ReadState("/tmp/x.go")
	if err == nil {
		t.Fatal("expected an error for an unparseable read_at, got nil")
	}
	if got.OK {
		t.Error("OK = true, want false when the timestamp cannot be parsed")
	}
	if !got.At.IsZero() {
		t.Errorf("time = %v, want zero", got.At)
	}
}
