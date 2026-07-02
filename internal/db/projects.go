package db

import (
	"fmt"
	"time"
)

type Project struct {
	ID        int64
	RootPath  string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GetOrCreateProject returns the project ID for rootPath, creating the row
// if it does not exist. The root_path column is UNIQUE and is the identity
// key; name is updated on conflict so later calls can refresh metadata.
func (db *DB) GetOrCreateProject(rootPath string, name string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := db.exec(
		`INSERT INTO projects (root_path, name, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(root_path) DO UPDATE SET name=excluded.name, updated_at=excluded.updated_at`,
		rootPath, name, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert project: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get last project id: %w", err)
	}

	if id == 0 {
		row := db.queryRow(`SELECT id FROM projects WHERE root_path = ?`, rootPath)
		if scanErr := row.Scan(&id); scanErr != nil {
			return 0, fmt.Errorf("lookup project id: %w", scanErr)
		}
	}

	return id, nil
}
