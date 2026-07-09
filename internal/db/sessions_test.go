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

	if _, err := db.SaveMessage(sessionID, "user", "hello", "plain", now.Add(time.Second), "", 0, false, 0); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}
	if _, err := db.SaveMessage(sessionID, "assistant", "hi there", "markdown", now.Add(2*time.Second), "considering the greeting", 4*time.Second, false, 0); err != nil {
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
	if messages[0].Reasoning != "" || messages[0].ThinkDurationMs != 0 {
		t.Errorf("message 0 should have no reasoning: %+v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "hi there" {
		t.Errorf("message 1 mismatch: %+v", messages[1])
	}
	if messages[1].Reasoning != "considering the greeting" {
		t.Errorf("message 1 reasoning = %q, want %q", messages[1].Reasoning, "considering the greeting")
	}
	if messages[1].ThinkDurationMs != 4000 {
		t.Errorf("message 1 think duration = %d ms, want 4000", messages[1].ThinkDurationMs)
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

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	return db
}

func createTestSession(t *testing.T, db *DB) string {
	t.Helper()
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	sessionID := "test-session-1"
	if err := db.CreateSession(sessionID, projectID, "test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	return sessionID
}

func TestSaveMessageWithFinalFlag(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	sessionID := createTestSession(t, db)

	now := time.Now().UTC()
	if _, err := db.SaveMessage(sessionID, "assistant", "the answer", "markdown", now, "", 0, true, 0); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	msgs, err := db.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(msgs))
	}
	if !msgs[0].Final {
		t.Fatal("Final = false, want true")
	}
}

func TestSaveMessageWithoutFinalFlag(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	sessionID := createTestSession(t, db)

	now := time.Now().UTC()
	if _, err := db.SaveMessage(sessionID, "user", "hello", "plain", now, "", 0, false, 0); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	msgs, err := db.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(msgs))
	}
	if msgs[0].Final {
		t.Fatal("Final = true, want false")
	}
}

func TestMessageTreeBranching(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	projectID, _ := db.GetOrCreateProject("/r", "r")
	sid := "sess-tree"
	if err := db.CreateSession(sid, projectID, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	id1, _ := db.SaveMessage(sid, "user", "turn1", "plain", time.Now().UTC(), "", 0, false, 0)
	id2, _ := db.SaveMessage(sid, "assistant", "a1", "markdown", time.Now().UTC(), "", 0, true, id1)
	id3, _ := db.SaveMessage(sid, "user", "turn2", "plain", time.Now().UTC(), "", 0, false, id2)
	id4, _ := db.SaveMessage(sid, "assistant", "a2", "markdown", time.Now().UTC(), "", 0, true, id3)
	id5, _ := db.SaveMessage(sid, "user", "turn3", "plain", time.Now().UTC(), "", 0, false, id2)
	if err := db.SetBranchLeaf(sid, id5); err != nil {
		t.Fatal(err)
	}

	branches, err := db.ListBranches(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 2 {
		t.Fatalf("branches = %v, want 2 leaves (id4, id5)", branches)
	}

	branchMsgs, err := db.MessagesOnBranch(sid, id5)
	if err != nil {
		t.Fatal(err)
	}
	if len(branchMsgs) != 3 || branchMsgs[2].Content != "turn3" {
		t.Fatalf("branch path = %+v, want turn1->a1->turn3", branchMsgs)
	}

	orig, err := db.MessagesOnBranch(sid, id4)
	if err != nil {
		t.Fatal(err)
	}
	if len(orig) != 4 || orig[3].Content != "a2" {
		t.Fatalf("original branch = %+v, want 4 msgs ending a2", orig)
	}
}

func TestUpdateSessionTitle(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	projectID, _ := db.GetOrCreateProject("/r", "r")
	sid := "sess-title"
	if err := db.CreateSession(sid, projectID, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSessionTitle(sid, "Fix parser bug"); err != nil {
		t.Fatalf("UpdateSessionTitle: %v", err)
	}
	s, err := db.GetSession(sid)
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Fix parser bug" {
		t.Fatalf("title = %q, want %q", s.Title, "Fix parser bug")
	}
}
