package db

import (
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
