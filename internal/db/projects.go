package db

import (
	"database/sql"
	"errors"
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

// scanProject scans a single project row and parses its timestamps.
// notFound is the error text returned when no row exists.
func scanProject(row *sql.Row, notFound string) (Project, error) {
	var p Project
	var createdAt, updatedAt string
	if err := row.Scan(&p.ID, &p.RootPath, &p.Name, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, errors.New(notFound)
		}
		return Project{}, fmt.Errorf("load project: %w", err)
	}
	var err error
	if p.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
		return Project{}, fmt.Errorf("parse created_at: %w", err)
	}
	if p.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt); err != nil {
		return Project{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return p, nil
}

// GetProject returns the project row for the given ID.
func (db *DB) GetProject(id int64) (Project, error) {
	row := db.sqlDB.QueryRow(`SELECT id, root_path, name, created_at, updated_at FROM projects WHERE id = ?`, id)
	return scanProject(row, fmt.Sprintf("project not found: %d", id))
}

// GetProjectByRoot returns the project row for the given root path.
// Returns a "project not found" error if no row exists. Never creates a row.
func (db *DB) GetProjectByRoot(rootPath string) (Project, error) {
	row := db.sqlDB.QueryRow(`SELECT id, root_path, name, created_at, updated_at FROM projects WHERE root_path = ?`, rootPath)
	return scanProject(row, fmt.Sprintf("project not found: %s", rootPath))
}

// GetOrCreateProject returns the project ID for rootPath, creating the row
// if it does not exist. The root_path column is UNIQUE and is the identity
// key; name is updated on conflict so later calls can refresh metadata.
func (db *DB) GetOrCreateProject(rootPath string, name string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := db.sqlDB.Exec(
		`INSERT INTO projects (root_path, name, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(root_path) DO UPDATE SET name=excluded.name, updated_at=excluded.updated_at`,
		rootPath, name, now, now,
	); err != nil {
		return 0, fmt.Errorf("upsert project: %w", err)
	}

	var id int64
	if err := db.sqlDB.QueryRow(`SELECT id FROM projects WHERE root_path = ?`, rootPath).Scan(&id); err != nil {
		return 0, fmt.Errorf("lookup project id: %w", err)
	}

	return id, nil
}
