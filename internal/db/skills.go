package db

import (
	"context"
	"time"
)

// ProjectSkill records that a skill was loaded for a given project.
type ProjectSkill struct {
	ProjectID int64
	SkillName string
	LoadedAt  time.Time
	Scope     string // "global" or "project"
}

// RememberSkill records a skill as active for a project, or updates the
// timestamp and scope when the row already exists.
func (d *DB) RememberSkill(projectID int64, name, scope string) error {
	_, err := d.sqlDB.Exec(
		`INSERT INTO project_skills (project_id, skill_name, loaded_at, scope) VALUES (?, ?, ?, ?)
		 ON CONFLICT(project_id, skill_name) DO UPDATE SET loaded_at=excluded.loaded_at, scope=excluded.scope`,
		projectID, name, time.Now().UTC().Format(time.RFC3339), scope,
	)
	return err
}

// ForgetSkill removes a skill from a project's remembered list.
func (d *DB) ForgetSkill(projectID int64, name string) error {
	_, err := d.sqlDB.Exec(`DELETE FROM project_skills WHERE project_id = ? AND skill_name = ?`, projectID, name)
	return err
}

// ListProjectSkills returns the remembered skills for a project, newest first.
func (d *DB) ListProjectSkills(ctx context.Context, projectID int64) ([]ProjectSkill, error) {
	rows, err := d.sqlDB.QueryContext(ctx,
		`SELECT project_id, skill_name, loaded_at, scope FROM project_skills
		 WHERE project_id = ? ORDER BY loaded_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectSkill
	for rows.Next() {
		var s ProjectSkill
		var t string
		if err := rows.Scan(&s.ProjectID, &s.SkillName, &t, &s.Scope); err != nil {
			return nil, err
		}
		s.LoadedAt, _ = time.Parse(time.RFC3339, t)
		out = append(out, s)
	}
	return out, rows.Err()
}