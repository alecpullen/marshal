package db

import (
	"database/sql"
	"fmt"
	"time"
)

type FileIndex struct {
	Path          string
	Language      string
	Hash          string
	SizeBytes     int64
	LastIndexedAt time.Time
	Summary       string
}

// SaveFileIndex replaces the file index for a project. It deletes all
// existing files for the project and inserts the provided rows, preserving
// each file's stored Summary when its path+hash is unchanged from the
// existing index. A changed hash (or a new path) means the old summary no
// longer describes the file's current content, so it is dropped; the
// knowledge pass (internal/knowledge) fills it back in later via
// UpdateFileSummary.
func (db *DB) SaveFileIndex(projectID int64, files []FileIndex) error {
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin save file index transaction: %w", err)
	}
	defer tx.Rollback()

	existing := map[string]FileIndex{}
	rows, err := tx.Query(`SELECT path, hash, summary FROM files WHERE project_id = ?`, projectID)
	if err != nil {
		return fmt.Errorf("query existing file index: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var path, hash string
		var summary sql.NullString
		if err := rows.Scan(&path, &hash, &summary); err != nil {
			return fmt.Errorf("scan existing file row: %w", err)
		}
		f := FileIndex{Path: path, Hash: hash}
		if summary.Valid {
			f.Summary = summary.String
		}
		existing[path] = f
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate existing file rows: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM files WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("delete existing files: %w", err)
	}

	stmt, err := tx.Prepare(`INSERT INTO files (project_id, path, language, hash, size_bytes, last_indexed_at, summary)
							 VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare file insert: %w", err)
	}
	defer stmt.Close()

	for _, f := range files {
		summary := f.Summary
		if prior, ok := existing[f.Path]; ok && prior.Hash == f.Hash {
			summary = prior.Summary
		}
		_, err := stmt.Exec(projectID, f.Path, f.Language, f.Hash, f.SizeBytes, f.LastIndexedAt.UTC().Format(time.RFC3339), summary)
		if err != nil {
			return fmt.Errorf("insert file %s: %w", f.Path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit save file index: %w", err)
	}
	return nil
}

// GetFileIndex returns all file rows for a project, ordered by path.
func (db *DB) GetFileIndex(projectID int64) ([]FileIndex, error) {
	rows, err := db.sqlDB.Query(
		`SELECT path, language, hash, size_bytes, last_indexed_at, summary
		 FROM files
		 WHERE project_id = ?
		 ORDER BY path`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("query file index: %w", err)
	}
	defer rows.Close()

	var files []FileIndex
	for rows.Next() {
		var f FileIndex
		var lastIndexed string
		var summary sql.NullString
		if err := rows.Scan(&f.Path, &f.Language, &f.Hash, &f.SizeBytes, &lastIndexed, &summary); err != nil {
			return nil, fmt.Errorf("scan file row: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, lastIndexed)
		if err != nil {
			return nil, fmt.Errorf("parse last_indexed_at: %w", err)
		}
		f.LastIndexedAt = parsed.UTC()
		if summary.Valid {
			f.Summary = summary.String
		}
		files = append(files, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file rows: %w", err)
	}
	return files, nil
}

// FilesMatchingBasename returns up to limit file paths for the given project
// where the basename (e.g. "main.go") appears anywhere in the path. Results
// are ordered with exact basename matches first, then by ascending path
// length so the shortest candidate paths surface first.
func (db *DB) FilesMatchingBasename(projectID int64, basename string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 5
	}
	pattern := "%" + basename + "%"
	rows, err := db.sqlDB.Query(
		`SELECT path
		 FROM files
		 WHERE project_id = ? AND path LIKE ?
		 ORDER BY
		   CASE WHEN path = ? OR substr(path, length(path) - length(?) + 1) = ? THEN 0 ELSE 1 END,
		   LENGTH(path)
		 LIMIT ?`,
		projectID, pattern, basename, basename, basename, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query files matching basename: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scan file basename row: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file basename rows: %w", err)
	}
	return paths, nil
}

// UpdateFileSummary sets Summary for a single existing file row, without
// touching hash/language/size/last_indexed_at. It is a no-op (not an error)
// if no row matches project_id+path.
func (db *DB) UpdateFileSummary(projectID int64, path, summary string) error {
	_, err := db.sqlDB.Exec(
		`UPDATE files SET summary = ? WHERE project_id = ? AND path = ?`,
		summary, projectID, path,
	)
	if err != nil {
		return fmt.Errorf("update file summary: %w", err)
	}
	return nil
}
