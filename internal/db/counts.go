package db

import (
	"database/sql"
	"fmt"
	"time"
)

// CountFiles returns the number of indexed files for a project.
func (db *DB) CountFiles(projectID int64) (int, error) {
	return db.countRows(`SELECT COUNT(*) FROM files WHERE project_id = ?`, projectID)
}

// CountSymbols returns the number of indexed symbols for a project.
func (db *DB) CountSymbols(projectID int64) (int, error) {
	return db.countRows(`SELECT COUNT(*) FROM symbols WHERE project_id = ?`, projectID)
}

// CountEmbeddedChunks returns the number of chunks that have an embedding.
// Chunks are written even when embeddings are disabled, so only the join
// proves the embedding phase actually ran.
func (db *DB) CountEmbeddedChunks(projectID int64) (int, error) {
	return db.countRows(`SELECT COUNT(*) FROM embeddings e JOIN chunks c ON c.id = e.chunk_id WHERE c.project_id = ?`, projectID)
}

// LatestIndexedAt returns the most recent files.last_indexed_at for a
// project, or the zero time when the index has never run.
func (db *DB) LatestIndexedAt(projectID int64) (time.Time, error) {
	var raw sql.NullString
	if err := db.sqlDB.QueryRow(`SELECT MAX(last_indexed_at) FROM files WHERE project_id = ?`, projectID).Scan(&raw); err != nil {
		return time.Time{}, err
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse last_indexed_at: %w", err)
	}
	return t.UTC(), nil
}

func (db *DB) countRows(query string, args ...any) (int, error) {
	var n int
	if err := db.sqlDB.QueryRow(query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// SeedEmbeddedChunkForTest inserts one chunk with a matching embedding row.
// It exists so tests outside this package can set up embedding-presence
// state without reaching unexported internals.
func (db *DB) SeedEmbeddedChunkForTest(projectID int64, path string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.sqlDB.Exec(
		`INSERT INTO chunks (project_id, file_path, file_hash, kind, start_line, end_line, content, token_count, created_at)
		 VALUES (?, ?, 'h1', 'func', 1, 3, 'package x', 2, ?)`, projectID, path, now)
	if err != nil {
		return err
	}
	chunkID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = db.sqlDB.Exec(
		`INSERT INTO embeddings (chunk_id, model, dim, vector) VALUES (?, 'test-embed', 4, ?)`,
		chunkID, []byte{1, 2, 3, 4})
	return err
}
