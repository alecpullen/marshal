package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

func (db *DB) SaveSnapshot(sessionID string, turnIndex int, hash string, files []string, at time.Time) (int64, error) {
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin save snapshot: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO snapshots (session_id, turn_index, hash, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, turnIndex, hash, at.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("insert snapshot: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("snapshot id: %w", err)
	}

	for _, f := range files {
		if _, err := tx.Exec(
			`INSERT INTO snapshot_files (snapshot_id, path) VALUES (?, ?)`,
			id, f,
		); err != nil {
			return 0, fmt.Errorf("insert snapshot file: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit save snapshot: %w", err)
	}
	return id, nil
}

func (db *DB) LatestSnapshot(sessionID string) (id int64, hash string, files []string, err error) {
	row := db.sqlDB.QueryRow(
		`SELECT id, hash FROM snapshots WHERE session_id = ? ORDER BY turn_index DESC, id DESC LIMIT 1`,
		sessionID)
	if err := row.Scan(&id, &hash); err != nil {
		if err == sql.ErrNoRows {
			return 0, "", nil, nil
		}
		return 0, "", nil, fmt.Errorf("query latest snapshot: %w", err)
	}
	rows, err := db.sqlDB.Query(
		`SELECT path FROM snapshot_files WHERE snapshot_id = ? ORDER BY path`, id)
	if err != nil {
		return id, hash, nil, fmt.Errorf("query snapshot files: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return 0, "", nil, fmt.Errorf("scan snapshot file: %w", err)
		}
		files = append(files, p)
	}
	if err := rows.Err(); err != nil {
		return 0, "", nil, fmt.Errorf("iterate snapshot files: %w", err)
	}
	return id, hash, files, nil
}

func (db *DB) SnapshotBefore(sessionID string, turnIndex int) (hash string, err error) {
	row := db.sqlDB.QueryRow(
		`SELECT hash FROM snapshots WHERE session_id = ? AND turn_index < ? ORDER BY turn_index DESC, id DESC LIMIT 1`,
		sessionID, turnIndex)
	if err := row.Scan(&hash); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("query snapshot before: %w", err)
	}
	return hash, nil
}

func (db *DB) PruneSnapshotsOlderThan(days int) error {
	if days < 0 {
		return fmt.Errorf("prune snapshots: days must be >= 0, got %d", days)
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	// Load all snapshots and compare parsed timestamps, not RFC3339 strings.
	// String comparison breaks with non-UTC timezone offsets.
	rows, err := db.sqlDB.Query(`SELECT id, created_at FROM snapshots`)
	if err != nil {
		return fmt.Errorf("prune snapshots: query: %w", err)
	}
	defer rows.Close()

	var toDelete []int64
	for rows.Next() {
		var id int64
		var createdAt string
		if err := rows.Scan(&id, &createdAt); err != nil {
			return fmt.Errorf("prune snapshots: scan: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			// Skip unparseable timestamps with a warning rather than
			// failing the entire prune.
			slog.Warn("prune snapshots: skipping unparseable timestamp", "value", createdAt, "id", id)
			continue
		}
		if parsed.Before(cutoff) {
			toDelete = append(toDelete, id)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("prune snapshots: iterate: %w", err)
	}

	if len(toDelete) == 0 {
		return nil
	}

	// Delete every pruned snapshot in a single statement so the prune is
	// atomic: an error cannot leave a partially-pruned set, and we avoid
	// one DELETE round-trip per snapshot.
	placeholders := make([]string, len(toDelete))
	args := make([]any, len(toDelete))
	for i, id := range toDelete {
		placeholders[i] = "?"
		args[i] = id
	}
	stmt := `DELETE FROM snapshots WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	if _, err := db.sqlDB.Exec(stmt, args...); err != nil {
		return fmt.Errorf("prune snapshots: delete %d snapshots: %w", len(toDelete), err)
	}
	return nil
}
