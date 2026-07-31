package db

import (
	"database/sql"
	"fmt"
	"time"
)

const maxMemoryRows = 1000

const (
	MemoryConfidenceTentative = "tentative"
	MemoryConfidenceConfirmed = "confirmed"
	MemoryConfidenceStale     = "stale"
)

type Memory struct {
	ID              int64
	Kind            string // "fact", "architecture", "decision"
	Content         string
	Confidence      string // "tentative", "confirmed", "stale"
	SourceSessionID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SaveMemory inserts a new memory row with confidence "tentative".
func (db *DB) SaveMemory(projectID int64, kind, content, sourceSessionID string, now time.Time) error {
	nowStr := now.UTC().Format(time.RFC3339)
	_, err := db.sqlDB.Exec(
		`INSERT INTO memories (project_id, kind, content, confidence, source_session_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectID, kind, content, MemoryConfidenceTentative, sourceSessionID, nowStr, nowStr,
	)
	if err != nil {
		return fmt.Errorf("save memory: %w", err)
	}
	if err := db.pruneMemories(projectID); err != nil {
		return err
	}
	return nil
}

func (db *DB) pruneMemories(projectID int64) error {
	_, err := db.sqlDB.Exec(
		`DELETE FROM memories WHERE id IN (
			SELECT id FROM memories WHERE project_id = ? ORDER BY id DESC LIMIT -1 OFFSET ?
		)`,
		projectID, maxMemoryRows,
	)
	if err != nil {
		return fmt.Errorf("prune memories: %w", err)
	}
	return nil
}

// scanMemory assembles a Memory from scanned column values and parses the
// RFC3339 timestamp strings into UTC time.Time values.
func scanMemory(id int64, kind, content, confidence string, sourceSessionID sql.NullString, createdAt, updatedAt string) (Memory, error) {
	m := Memory{
		ID:         id,
		Kind:       kind,
		Content:    content,
		Confidence: confidence,
	}
	if sourceSessionID.Valid {
		m.SourceSessionID = sourceSessionID.String
	}
	parsedCreated, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return m, fmt.Errorf("parse created_at: %w", err)
	}
	m.CreatedAt = parsedCreated.UTC()
	parsedUpdated, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return m, fmt.Errorf("parse updated_at: %w", err)
	}
	m.UpdatedAt = parsedUpdated.UTC()
	return m, nil
}

// GetMemories returns all memory rows for a project, ordered by id.
func (db *DB) GetMemories(projectID int64) ([]Memory, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, kind, content, confidence, source_session_id, created_at, updated_at
		 FROM memories
		 WHERE project_id = ?
		 ORDER BY id`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var (
			id                        int64
			kind, content, confidence string
			sourceSessionID           sql.NullString
			createdAt, updatedAt      string
		)
		if err := rows.Scan(&id, &kind, &content, &confidence, &sourceSessionID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan memory row: %w", err)
		}
		m, err := scanMemory(id, kind, content, confidence, sourceSessionID, createdAt, updatedAt)
		if err != nil {
			return nil, err
		}
		memories = append(memories, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory rows: %w", err)
	}
	return memories, nil
}

// GetMemory returns a single memory row by id.
func (db *DB) GetMemory(id int64) (Memory, error) {
	var (
		scannedID                 int64
		kind, content, confidence string
		sourceSessionID           sql.NullString
		createdAt, updatedAt      string
	)
	err := db.sqlDB.QueryRow(
		`SELECT id, kind, content, confidence, source_session_id, created_at, updated_at
		 FROM memories
		 WHERE id = ?`,
		id,
	).Scan(&scannedID, &kind, &content, &confidence, &sourceSessionID, &createdAt, &updatedAt)
	if err != nil {
		return Memory{}, fmt.Errorf("query memory: %w", err)
	}
	return scanMemory(scannedID, kind, content, confidence, sourceSessionID, createdAt, updatedAt)
}

// DeleteMemory removes a single memory row by id.
func (db *DB) DeleteMemory(id int64) error {
	_, err := db.sqlDB.Exec(`DELETE FROM memories WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	return nil
}

// SetMemoryConfidence updates a single memory's confidence state and
// updated_at timestamp.
func (db *DB) SetMemoryConfidence(id int64, confidence string, now time.Time) error {
	_, err := db.sqlDB.Exec(
		`UPDATE memories SET confidence = ?, updated_at = ? WHERE id = ?`,
		confidence, now.UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		return fmt.Errorf("set memory confidence: %w", err)
	}
	return nil
}
