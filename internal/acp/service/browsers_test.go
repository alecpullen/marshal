package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
)

// browserDB opens a migrated database in a temporary directory, as sheet_test
// does.
func browserDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return database
}

// TestListSessionsReadsTheDatabase pins ListSessions' mapping: the sessions of
// one working directory, in order, with their IDs, titles and message counts,
// and none of another directory's.
func TestListSessionsReadsTheDatabase(t *testing.T) {
	database := browserDB(t)
	cwd := "/repo"
	projectID, err := database.GetOrCreateProject(cwd, "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	otherID, err := database.GetOrCreateProject("/other", "other")
	if err != nil {
		t.Fatalf("GetOrCreateProject other: %v", err)
	}

	if err := database.CreateSession("s1", projectID, "first session", time.Unix(1000, 0)); err != nil {
		t.Fatalf("CreateSession s1: %v", err)
	}
	if err := database.CreateSession("s2", projectID, "", time.Unix(2000, 0)); err != nil {
		t.Fatalf("CreateSession s2: %v", err)
	}
	if err := database.CreateSession("s3", otherID, "elsewhere", time.Unix(3000, 0)); err != nil {
		t.Fatalf("CreateSession s3: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := database.SaveMessage("s1", "user", "hello", "text", time.Unix(1000+int64(i), 0), "", 0, true, 0); err != nil {
			t.Fatalf("SaveMessage s1: %v", err)
		}
	}
	if _, err := database.SaveMessage("s2", "user", "hi", "text", time.Unix(2000, 0), "", 0, true, 0); err != nil {
		t.Fatalf("SaveMessage s2: %v", err)
	}

	state := session.New(config.Default(), cwd, time.Unix(500, 0), session.Persistence{DB: database, SessionID: "s1"})
	host, _ := New(Deps{State: state, ProjectID: projectID})

	got, err := host.ListSessions(context.Background(), cwd, 10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListSessions = %+v, want the two sessions of %q", got, cwd)
	}
	byID := map[string]acpwire.SessionEntry{}
	for _, entry := range got {
		byID[entry.SessionID] = entry
	}
	if _, ok := byID["s3"]; ok {
		t.Errorf("ListSessions listed s3, a session of another working directory")
	}
	first, ok := byID["s1"]
	if !ok {
		t.Fatalf("ListSessions = %+v, want s1", got)
	}
	if first.Title != "first session" {
		t.Errorf("s1 Title = %q, want %q", first.Title, "first session")
	}
	if first.MessageCount != 2 {
		t.Errorf("s1 MessageCount = %d, want 2", first.MessageCount)
	}
	second, ok := byID["s2"]
	if !ok {
		t.Fatalf("ListSessions = %+v, want s2", got)
	}
	if second.Title != "" {
		t.Errorf("s2 Title = %q, want empty", second.Title)
	}
	if second.MessageCount != 1 {
		t.Errorf("s2 MessageCount = %d, want 1", second.MessageCount)
	}
}

// TestBrowsersWithoutADatabase pins the code each browser call fails with when
// the session has no database.
func TestBrowsersWithoutADatabase(t *testing.T) {
	host, sess := New(Deps{State: newState(t, "s1")})
	ctx := context.Background()

	if _, err := host.ListSessions(ctx, "/repo", 10); !acpclient.IsCode(err, acpwire.CodeNoDatabase) {
		t.Errorf("ListSessions error = %v, want CodeNoDatabase", err)
	}
	if _, err := sess.Memories(ctx); !acpclient.IsCode(err, acpwire.CodeNoDatabase) {
		t.Errorf("Memories error = %v, want CodeNoDatabase", err)
	}
	if err := sess.DeleteMemory(ctx, 1); !acpclient.IsCode(err, acpwire.CodeNoDatabase) {
		t.Errorf("DeleteMemory error = %v, want CodeNoDatabase", err)
	}
}
