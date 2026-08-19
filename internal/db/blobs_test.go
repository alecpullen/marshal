package db

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPutBlob(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	hash, err := db.PutBlob("hello world", now)
	if err != nil {
		t.Fatalf("PutBlob failed: %v", err)
	}
	if len(hash) != 64 {
		t.Fatalf("expected 64-char hash, got %d: %s", len(hash), hash)
	}
	// sha256("hello world") is known
	const expected = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if hash != expected {
		t.Fatalf("unexpected hash: got %s, want %s", hash, expected)
	}
}

func TestPutBlobDeduplicates(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	hash1, err := db.PutBlob("deduplicate me", now)
	if err != nil {
		t.Fatalf("first PutBlob failed: %v", err)
	}
	hash2, err := db.PutBlob("deduplicate me", now.Add(time.Second))
	if err != nil {
		t.Fatalf("second PutBlob failed: %v", err)
	}
	if hash1 != hash2 {
		t.Fatalf("expected identical hashes for identical content: %s vs %s", hash1, hash2)
	}

	// Verify only one row exists.
	var count int
	err = db.sqlDB.QueryRow(`SELECT COUNT(*) FROM content_blobs WHERE hash = ?`, hash1).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}
}

func TestGetBlobRoundTrip(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	content := "round-trip content"
	hash, err := db.PutBlob(content, now)
	if err != nil {
		t.Fatalf("PutBlob failed: %v", err)
	}

	got, err := db.GetBlob(hash)
	if err != nil {
		t.Fatalf("GetBlob failed: %v", err)
	}
	if got != content {
		t.Fatalf("GetBlob returned wrong content: got %q, want %q", got, content)
	}
}

func TestVacuumContentBlobsRemovesOrphans(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	projectID, _ := db.GetOrCreateProject("/r", "r")
	sid := "sess-vac"
	if err := db.CreateSession(sid, projectID, "vacuum test", now); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Put an orphan blob.
	hashOrphan, err := db.PutBlob("orphan content", now)
	if err != nil {
		t.Fatalf("PutBlob orphan: %v", err)
	}

	// Put a referenced blob via ArchiveTurns (threshold=4 so 18-char content becomes a blob).
	if err := db.BeginGeneration(Generation{
		ID: "gen-vac", SessionID: sid, Seq: 1, StartedAt: now,
	}); err != nil {
		t.Fatalf("BeginGeneration: %v", err)
	}
	if err := db.ArchiveTurns("gen-vac", []ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: "referenced content", CreatedAt: now},
	}, 4, now); err != nil {
		t.Fatalf("ArchiveTurns: %v", err)
	}

	// Find the blob hash that was stored.
	var refHash string
	if err := db.sqlDB.QueryRow(
		`SELECT content_blob_hash FROM generation_turns WHERE generation_id = ?`, "gen-vac",
	).Scan(&refHash); err != nil {
		t.Fatalf("query ref hash: %v", err)
	}
	if refHash == "" {
		t.Fatal("expected content_blob_hash to be set — test setup wrong")
	}

	// Vacuum should remove only the orphan.
	removed, err := db.VacuumContentBlobs()
	if err != nil {
		t.Fatalf("VacuumContentBlobs: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}

	// Orphan should be gone.
	if _, err := db.GetBlob(hashOrphan); err == nil {
		t.Fatal("expected orphan blob to be removed")
	}
	// Referenced blob should remain.
	if _, err := db.GetBlob(refHash); err != nil {
		t.Fatalf("expected referenced blob to remain: %v", err)
	}
}

func TestDeleteSessionBlobsRemovesOnlySessionBlobs(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	projectID, _ := db.GetOrCreateProject("/r", "r")

	// Create two sessions, each with a blob.
	sid1 := "sess-blob-1"
	sid2 := "sess-blob-2"
	if err := db.CreateSession(sid1, projectID, "s1", now); err != nil {
		t.Fatalf("CreateSession 1: %v", err)
	}
	if err := db.CreateSession(sid2, projectID, "s2", now); err != nil {
		t.Fatalf("CreateSession 2: %v", err)
	}

	// Archive turns with large content (threshold=4) so they become blobs.
	for i, sid := range []string{sid1, sid2} {
		genID := fmt.Sprintf("gen-blob-%d", i+1)
		if err := db.BeginGeneration(Generation{
			ID: genID, SessionID: sid, Seq: 1, StartedAt: now,
		}); err != nil {
			t.Fatalf("BeginGeneration %s: %v", sid, err)
		}
		content := fmt.Sprintf("content for session %s", sid)
		if err := db.ArchiveTurns(genID, []ArchivedTurn{
			{TurnSeq: 1, Role: "user", Content: content, CreatedAt: now},
		}, 4, now); err != nil {
			t.Fatalf("ArchiveTurns %s: %v", sid, err)
		}
	}

	// Get blob hashes for each session.
	var hash1, hash2 string
	if err := db.sqlDB.QueryRow(
		`SELECT gt.content_blob_hash FROM generation_turns gt
		 JOIN session_generations sg ON sg.generation_id = gt.generation_id
		 WHERE sg.session_id = ?`, sid1,
	).Scan(&hash1); err != nil {
		t.Fatalf("query hash1: %v", err)
	}
	if err := db.sqlDB.QueryRow(
		`SELECT gt.content_blob_hash FROM generation_turns gt
		 JOIN session_generations sg ON sg.generation_id = gt.generation_id
		 WHERE sg.session_id = ?`, sid2,
	).Scan(&hash2); err != nil {
		t.Fatalf("query hash2: %v", err)
	}

	// Delete blobs for session 1 in a transaction (simulating DeleteSession).
	// First delete the session's turns so the blob becomes unreferenced.
	tx, err := db.sqlDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(
		`DELETE FROM generation_turns WHERE generation_id IN (
			SELECT generation_id FROM session_generations WHERE session_id = ?
		)`, sid1); err != nil {
		t.Fatalf("delete turns: %v", err)
	}
	if err := db.DeleteSessionBlobs(tx); err != nil {
		t.Fatalf("DeleteSessionBlobs: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Blob from session 1 should be gone.
	if _, err := db.GetBlob(hash1); err == nil {
		t.Fatal("expected session 1 blob to be deleted")
	}
	// Blob from session 2 should remain.
	if _, err := db.GetBlob(hash2); err != nil {
		t.Fatalf("expected session 2 blob to remain: %v", err)
	}
}

func TestGetBlobNotFound(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	_, err := db.GetBlob(strings.Repeat("a", 64))
	if err == nil {
		t.Fatal("expected error for missing blob")
	}
	if !strings.Contains(err.Error(), ErrBlobNotFound.Error()) {
		t.Fatalf("expected error wrapping ErrBlobNotFound, got: %v", err)
	}
}
