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
