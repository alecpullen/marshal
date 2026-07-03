package db

import (
	"database/sql"
	"fmt"
	"time"
)

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
	_, err := db.exec(
		`INSERT INTO memories (project_id, kind, content, confidence, source_session_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectID, kind, content, MemoryConfidenceTentative, sourceSessionID, nowStr, nowStr,
	)
	if err != nil {
		return fmt.Errorf("save memory: %w", err)
	}
	return nil
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
		var m Memory
		var sourceSessionID sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&m.ID, &m.Kind, &m.Content, &m.Confidence, &sourceSessionID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan memory row: %w", err)
		}
		if sourceSessionID.Valid {
			m.SourceSessionID = sourceSessionID.String
		}
		parsedCreated, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		m.CreatedAt = parsedCreated.UTC()
		parsedUpdated, err := time.Parse(time.RFC3339, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}
		m.UpdatedAt = parsedUpdated.UTC()
		memories = append(memories, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory rows: %w", err)
	}
	return memories, nil
}

// SetMemoryConfidence updates a single memory's confidence state and
// updated_at timestamp.
func (db *DB) SetMemoryConfidence(id int64, confidence string, now time.Time) error {
	_, err := db.exec(
		`UPDATE memories SET confidence = ?, updated_at = ? WHERE id = ?`,
		confidence, now.UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		return fmt.Errorf("set memory confidence: %w", err)
	}
	return nil
}
