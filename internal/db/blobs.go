package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ErrBlobNotFound is returned by GetBlob when the hash does not exist.
var ErrBlobNotFound = errors.New("blob not found")

// PutBlob stores content addressed by its sha256 hash. Identical content
// deduplicates to a single row. Returns the 64-char hex hash.
func (db *DB) PutBlob(content string, at time.Time) (string, error) {
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	_, err := db.sqlDB.Exec(
		`INSERT INTO content_blobs (hash, content, size_bytes, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(hash) DO NOTHING`,
		hash, content, len(content), at.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return "", fmt.Errorf("put blob: %w", err)
	}
	return hash, nil
}

// GetBlob retrieves content by its sha256 hash. Returns ErrBlobNotFound if
// the hash does not exist.
func (db *DB) GetBlob(hash string) (string, error) {
	var content string
	row := db.sqlDB.QueryRow(`SELECT content FROM content_blobs WHERE hash = ?`, hash)
	if err := row.Scan(&content); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: %s", ErrBlobNotFound, hash)
		}
		return "", fmt.Errorf("get blob: %w", err)
	}
	return content, nil
}
