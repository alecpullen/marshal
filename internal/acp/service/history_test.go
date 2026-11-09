package service

import (
	"path/filepath"
	"testing"

	"marshal/internal/db"
)

// openHistoryTestDB opens a migrated database with one project, the way the
// sheet tests build theirs.
func openHistoryTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	return database, projectID
}
