package db

// CountFiles returns the number of indexed files for a project.
func (db *DB) CountFiles(projectID int64) (int, error) {
	return db.countRows(`SELECT COUNT(*) FROM files WHERE project_id = ?`, projectID)
}

// CountSymbols returns the number of indexed symbols for a project.
func (db *DB) CountSymbols(projectID int64) (int, error) {
	return db.countRows(`SELECT COUNT(*) FROM symbols WHERE project_id = ?`, projectID)
}

func (db *DB) countRows(query string, args ...any) (int, error) {
	var n int
	if err := db.sqlDB.QueryRow(query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
