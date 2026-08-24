package db

import (
	"fmt"
	"time"
)

// SnapshotRow is one recorded snapshot with the files it captured.
type SnapshotRow struct {
	ID        int64
	TurnIndex int
	Hash      string
	CreatedAt time.Time
	Files     []string
}

// ListSnapshots returns every snapshot for a session, oldest first.
//
// The existing lookups (LatestSnapshot, SnapshotBefore) answer point
// questions; the timeline needs the whole series so it can map each user
// turn to the snapshot that preceded it.
//
// Ordering is by created_at, not turn_index: turn_index is a monotonic
// session counter that does not decrease across a rewind, so two snapshots
// can share one turn index while time still separates them correctly.
func (db *DB) ListSnapshots(sessionID string) ([]SnapshotRow, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, turn_index, hash, created_at FROM snapshots
         WHERE session_id = ? ORDER BY created_at ASC, id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	defer rows.Close()

	var out []SnapshotRow
	byID := map[int64]int{}
	for rows.Next() {
		var r SnapshotRow
		var created string
		if err := rows.Scan(&r.ID, &r.TurnIndex, &r.Hash, &created); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		t, err := time.Parse(time.RFC3339, created)
		if err != nil {
			return nil, fmt.Errorf("parse snapshot time %q: %w", created, err)
		}
		r.CreatedAt = t.UTC()
		byID[r.ID] = len(out)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}

	// Files are fetched separately rather than by joining, so a snapshot
	// that captured no files is still listed.
	frows, err := db.sqlDB.Query(
		`SELECT sf.snapshot_id, sf.path FROM snapshot_files sf
         JOIN snapshots s ON s.id = sf.snapshot_id
         WHERE s.session_id = ? ORDER BY sf.path ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list snapshot files: %w", err)
	}
	defer frows.Close()
	for frows.Next() {
		var id int64
		var path string
		if err := frows.Scan(&id, &path); err != nil {
			return nil, fmt.Errorf("scan snapshot file: %w", err)
		}
		if i, ok := byID[id]; ok {
			out[i].Files = append(out[i].Files, path)
		}
	}
	return out, frows.Err()
}
