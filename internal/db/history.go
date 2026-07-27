package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// SavePrompt records one submitted prompt for a project. Empty submissions
// are skipped and consecutive duplicates are collapsed, so mashing enter on
// the same prompt does not fill history with copies.
func (db *DB) SavePrompt(projectID int64, content string, createdAt time.Time) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	var latest string
	err := db.sqlDB.QueryRow(
		`SELECT content FROM prompt_history WHERE project_id = ? ORDER BY id DESC LIMIT 1`,
		projectID,
	).Scan(&latest)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("query latest prompt: %w", err)
	}
	if err == nil && latest == content {
		return nil
	}
	if _, err := db.sqlDB.Exec(
		`INSERT INTO prompt_history (project_id, content, created_at) VALUES (?, ?, ?)`,
		projectID, content, createdAt.UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("insert prompt: %w", err)
	}
	return nil
}

// RecentPrompts returns the most recent prompts for a project, newest first.
func (db *DB) RecentPrompts(projectID int64, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := db.sqlDB.Query(
		`SELECT content FROM prompt_history WHERE project_id = ? ORDER BY id DESC LIMIT ?`,
		projectID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent prompts: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return nil, fmt.Errorf("scan prompt: %w", err)
		}
		out = append(out, content)
	}
	return out, rows.Err()
}
