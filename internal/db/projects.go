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

// GetProject returns the project row for the given ID.
func (db *DB) GetProject(id int64) (Project, error) {
	var p Project
	var createdAt, updatedAt string
	row := db.sqlDB.QueryRow(`SELECT id, root_path, name, created_at, updated_at FROM projects WHERE id = ?`, id)
	if err := row.Scan(&p.ID, &p.RootPath, &p.Name, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, fmt.Errorf("project not found: %d", id)
		}
		return Project{}, fmt.Errorf("load project: %w", err)
	}
	var parseErr error
	p.CreatedAt, parseErr = time.Parse(time.RFC3339, createdAt)
	if parseErr != nil {
		return Project{}, fmt.Errorf("parse created_at: %w", parseErr)
	}
	p.UpdatedAt, parseErr = time.Parse(time.RFC3339, updatedAt)
	if parseErr != nil {
		return Project{}, fmt.Errorf("parse updated_at: %w", parseErr)
	}
	return p, nil
}

// GetProjectByRoot returns the project row for the given root path.
// Returns a "project not found" error if no row exists. Never creates a row.
func (db *DB) GetProjectByRoot(rootPath string) (Project, error) {
	var p Project
	var createdAt, updatedAt string
	row := db.sqlDB.QueryRow(`SELECT id, root_path, name, created_at, updated_at FROM projects WHERE root_path = ?`, rootPath)
	if err := row.Scan(&p.ID, &p.RootPath, &p.Name, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, fmt.Errorf("project not found: %s", rootPath)
		}
		return Project{}, fmt.Errorf("load project by root: %w", err)
	}
	var parseErr error
	p.CreatedAt, parseErr = time.Parse(time.RFC3339, createdAt)
	if parseErr != nil {
		return Project{}, fmt.Errorf("parse created_at: %w", parseErr)
	}
	p.UpdatedAt, parseErr = time.Parse(time.RFC3339, updatedAt)
	if parseErr != nil {
		return Project{}, fmt.Errorf("parse updated_at: %w", parseErr)
	}
	return p, nil
}

// GetOrCreateProject returns the project ID for rootPath, creating the row
// if it does not exist. The root_path column is UNIQUE and is the identity
// key; name is updated on conflict so later calls can refresh metadata.
func (db *DB) GetOrCreateProject(rootPath string, name string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := db.sqlDB.Exec(
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
		row := db.sqlDB.QueryRow(`SELECT id FROM projects WHERE root_path = ?`, rootPath)
		if scanErr := row.Scan(&id); scanErr != nil {
			return 0, fmt.Errorf("lookup project id: %w", scanErr)
		}
	}

	return id, nil
}
