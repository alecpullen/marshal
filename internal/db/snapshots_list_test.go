package db

import (
	"testing"
	"time"
)

func TestListSnapshotsOrdersOldestFirst(t *testing.T) {
	d := testSnapshotDB(t)
	base := time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)
	if _, err := d.SaveSnapshot("s1", 2, "hash-b", []string{"b.go"}, base.Add(2*time.Minute)); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := d.SaveSnapshot("s1", 1, "hash-a", []string{"a.go"}, base); err != nil {
		t.Fatalf("save: %v", err)
	}
	rows, err := d.ListSnapshots("s1")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].Hash != "hash-a" || rows[1].Hash != "hash-b" {
		t.Fatalf("want oldest first, got %s then %s", rows[0].Hash, rows[1].Hash)
	}
}

func TestListSnapshotsCarriesFiles(t *testing.T) {
	d := testSnapshotDB(t)
	if _, err := d.SaveSnapshot("s1", 1, "h", []string{"a.go", "b.go"}, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	rows, err := d.ListSnapshots("s1")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(rows) != 1 || len(rows[0].Files) != 2 {
		t.Fatalf("want one row with 2 files, got %+v", rows)
	}
}

func TestListSnapshotsIsSessionScoped(t *testing.T) {
	d := testSnapshotDB(t)
	if _, err := d.SaveSnapshot("s1", 1, "mine", nil, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := d.SaveSnapshot("s2", 1, "theirs", nil, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	rows, _ := d.ListSnapshots("s1")
	if len(rows) != 1 || rows[0].Hash != "mine" {
		t.Fatalf("must not leak other sessions, got %+v", rows)
	}
}

func TestListSnapshotsEmptySession(t *testing.T) {
	d := testSnapshotDB(t)
	rows, err := d.ListSnapshots("nobody")
	if err != nil {
		t.Fatalf("an empty session must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want no rows, got %+v", rows)
	}
}

// A snapshot with no recorded files is legitimate (a mutating call that
// changed nothing) and must not be dropped by the files join.
func TestListSnapshotsKeepsFilelessRows(t *testing.T) {
	d := testSnapshotDB(t)
	if _, err := d.SaveSnapshot("s1", 1, "h", nil, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	if rows, _ := d.ListSnapshots("s1"); len(rows) != 1 {
		t.Fatalf("a fileless snapshot must still be listed, got %+v", rows)
	}
}
