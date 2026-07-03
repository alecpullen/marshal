package db

import (
	"testing"
	"time"
)

func TestCreateSessionAndMessages(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	sessionID := "session-abc"
	now := time.Now().UTC()
	if err := db.CreateSession(sessionID, projectID, "test session", now); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if err := db.SaveMessage(sessionID, "user", "hello", now.Add(time.Second)); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}
	if err := db.SaveMessage(sessionID, "assistant", "hi there", now.Add(2*time.Second)); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	messages, err := db.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Content != "hello" {
		t.Errorf("message 0 mismatch: %+v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "hi there" {
		t.Errorf("message 1 mismatch: %+v", messages[1])
	}
}

func TestGetMessagesEmptySession(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	messages, err := db.GetMessages("nonexistent")
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(messages))
	}
}

func TestEndSessionSetsEndedAtAndSummary(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	sessionID := "sess-end"
	startedAt := time.Unix(100, 0).UTC()
	if err := db.CreateSession(sessionID, projectID, "", startedAt); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	endedAt := time.Unix(200, 0).UTC()
	if err := db.EndSession(sessionID, endedAt, "Fixed the login bug."); err != nil {
		t.Fatalf("EndSession failed: %v", err)
	}

	got, err := db.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.Summary != "Fixed the login bug." {
		t.Fatalf("Summary = %q, want %q", got.Summary, "Fixed the login bug.")
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(endedAt) {
		t.Fatalf("EndedAt = %v, want %v", got.EndedAt, endedAt)
	}
	if got.ProjectID != projectID {
		t.Fatalf("ProjectID = %d, want %d", got.ProjectID, projectID)
	}
}

func TestGetSessionBeforeEndHasNilEndedAt(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	sessionID := "sess-open"
	if err := db.CreateSession(sessionID, projectID, "", time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	got, err := db.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt != nil {
		t.Fatalf("EndedAt = %v, want nil", got.EndedAt)
	}
	if got.Summary != "" {
		t.Fatalf("Summary = %q, want empty", got.Summary)
	}
}

func TestGetSessionNotFound(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	_, err = db.GetSession("does-not-exist")
	if err == nil {
		t.Fatal("expected an error for a missing session")
	}
}
