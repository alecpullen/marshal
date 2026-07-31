package db

import (
	"context"
	"fmt"
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

func TestGetLeafMessageIDFallsBackToMax(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pid, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	sid := "leaf-fallback"
	if err := db.CreateSession(sid, pid, "t", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// No messages yet → 0.
	got, err := db.GetLeafMessageID(sid)
	if err != nil {
		t.Fatalf("GetLeafMessageID: %v", err)
	}
	if got != 0 {
		t.Fatalf("empty session leaf = %d, want 0", got)
	}

	// Add a few messages but never call SetBranchLeaf — leaf_message_id
	// stays NULL. Fallback should return MAX(id).
	if _, err := db.SaveMessage(sid, "user", "a", "plain", time.Now().UTC(), "", 0, false, 0); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if _, err := db.SaveMessage(sid, "assistant", "b", "plain", time.Now().UTC(), "", 0, false, 0); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	got, err = db.GetLeafMessageID(sid)
	if err != nil {
		t.Fatalf("GetLeafMessageID: %v", err)
	}
	all, _ := db.GetMessages(sid)
	if got != all[len(all)-1].ID {
		t.Fatalf("fallback leaf = %d, want MAX id %d", got, all[len(all)-1].ID)
	}

	// After SetBranchLeaf, returns the explicit value.
	if err := db.SetBranchLeaf(sid, all[0].ID); err != nil {
		t.Fatalf("SetBranchLeaf: %v", err)
	}
	got, err = db.GetLeafMessageID(sid)
	if err != nil {
		t.Fatalf("GetLeafMessageID: %v", err)
	}
	if got != all[0].ID {
		t.Fatalf("explicit leaf = %d, want %d", got, all[0].ID)
	}
}

func TestListSessions(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cwd := "/home/user/project"
	pid, err := d.GetOrCreateProject(cwd, "project")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}

	otherCwd := "/home/user/other"
	pid2, err := d.GetOrCreateProject(otherCwd, "other")
	if err != nil {
		t.Fatalf("get or create project 2: %v", err)
	}
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if err := d.CreateSession("sess_old", pid, "", t0); err != nil {
		t.Fatalf("create old: %v", err)
	}
	if err := d.CreateSession("sess_new", pid, "Implement list", t0.Add(2*time.Hour)); err != nil {
		t.Fatalf("create new: %v", err)
	}
	if _, err := d.SaveMessage("sess_new", "user", "hello", "plain", t0.Add(3*time.Hour), "", 0, false, 0); err != nil {
		t.Fatalf("save msg: %v", err)
	}
	// Session in a different project must never appear.
	if err := d.CreateSession("sess_other", pid2, "other", t0.Add(time.Hour)); err != nil {
		t.Fatalf("create other: %v", err)
	}

	// Filter by cwd, default limit.
	got, next, err := d.ListSessions(context.Background(), cwd, "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if next != "" {
		t.Fatalf("nextCursor = %q, want empty", next)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].SessionID != "sess_new" || got[1].SessionID != "sess_old" {
		t.Fatalf("order = %+v, want [sess_new sess_old]", got)
	}
	if got[0].Title != "Implement list" {
		t.Fatalf("title = %q", got[0].Title)
	}
	if got[0].Cwd != cwd {
		t.Fatalf("cwd = %q", got[0].Cwd)
	}
	if !got[0].UpdatedAt.Equal(t0.Add(3 * time.Hour)) {
		t.Fatalf("updatedAt = %v, want %v", got[0].UpdatedAt, t0.Add(3*time.Hour))
	}
	if got[0].MessageCount != 1 {
		t.Fatalf("messageCount = %d, want 1", got[0].MessageCount)
	}
	// old session has no messages: updatedAt falls back to started_at.
	if !got[1].UpdatedAt.Equal(t0) {
		t.Fatalf("updatedAt fallback = %v, want %v", got[1].UpdatedAt, t0)
	}
	if got[1].MessageCount != 0 {
		t.Fatalf("messageCount = %d, want 0", got[1].MessageCount)
	}

	// Unknown cwd returns empty slice, no error.
	unknown, _, err := d.ListSessions(context.Background(), "/no/such/project", "", 0)
	if err != nil {
		t.Fatalf("unknown cwd: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown cwd returned %+v", unknown)
	}
}

func TestDeleteSessionCascadesMessages(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	sessionID := createTestSession(t, db)

	now := time.Now().UTC()
	if _, err := db.SaveMessage(sessionID, "user", "msg1", "plain", now, "", 0, false, 0); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if _, err := db.SaveMessage(sessionID, "assistant", "msg2", "markdown", now.Add(time.Second), "", 0, false, 0); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	var before int
	if err := db.sqlDB.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", sessionID).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	if before != 2 {
		t.Fatalf("expected 2 messages before delete, got %d", before)
	}

	existed, err := db.DeleteSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if !existed {
		t.Fatal("expected existed=true")
	}

	var after int
	if err := db.sqlDB.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", sessionID).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != 0 {
		t.Fatalf("expected 0 messages after cascade delete, got %d", after)
	}

	var sessionCount int
	if err := db.sqlDB.QueryRow("SELECT COUNT(*) FROM agent_sessions WHERE id = ?", sessionID).Scan(&sessionCount); err != nil {
		t.Fatalf("session count: %v", err)
	}
	if sessionCount != 0 {
		t.Fatal("expected session row to be deleted")
	}
}

func TestDeleteSessionUnknownIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	existed, err := db.DeleteSession(context.Background(), "nonexistent-session-id")
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if existed {
		t.Fatal("expected existed=false for unknown session")
	}
}

// TestDeleteSessionCleansOrphanedData verifies that deleting a session removes
// rows in tables that do not FK to agent_sessions, and that the contentless
// FTS5 index for generation turns is cleaned up via the delete trigger.
func TestDeleteSessionCleansOrphanedData(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	projectID, _ := db.GetOrCreateProject("/r", "r")
	sid := "sess-cleanup"
	if err := db.CreateSession(sid, projectID, "cleanup", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	now := time.Now().UTC()

	if _, err := db.SaveSnapshot(sid, 1, "hash1", []string{"a.go"}, now); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := db.SaveTodos(sid, []TodoItem{{Content: "todo", Status: "open"}}); err != nil {
		t.Fatalf("SaveTodos: %v", err)
	}
	if _, err := db.sqlDB.Exec(
		"INSERT INTO file_reads (session_id, path, read_at) VALUES (?, ?, ?)",
		sid, "read.go", now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert file_reads: %v", err)
	}
	if _, err := db.sqlDB.Exec(
		"INSERT INTO file_writes (session_id, path, written_at) VALUES (?, ?, ?)",
		sid, "write.go", now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert file_writes: %v", err)
	}

	if err := db.BeginGeneration(Generation{
		ID:        "gen-cleanup",
		SessionID: sid,
		Seq:       1,
		StartedAt: now,
	}); err != nil {
		t.Fatalf("BeginGeneration: %v", err)
	}
	if err := db.ArchiveTurns("gen-cleanup", []ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: "find me", CreatedAt: now},
	}, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns: %v", err)
	}

	var ftsBefore int
	if err := db.sqlDB.QueryRow("SELECT COUNT(*) FROM generation_turns_fts").Scan(&ftsBefore); err != nil {
		t.Fatalf("count fts before: %v", err)
	}
	if ftsBefore != 1 {
		t.Fatalf("fts rows before delete = %d, want 1", ftsBefore)
	}

	existed, err := db.DeleteSession(context.Background(), sid)
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if !existed {
		t.Fatal("expected existed=true")
	}

	cases := []struct {
		name  string
		query string
	}{
		{"snapshots", "SELECT COUNT(*) FROM snapshots WHERE session_id = ?"},
		{"file_reads", "SELECT COUNT(*) FROM file_reads WHERE session_id = ?"},
		{"file_writes", "SELECT COUNT(*) FROM file_writes WHERE session_id = ?"},
		{"session_state", "SELECT COUNT(*) FROM session_state WHERE session_id = ?"},
		{"session_generations", "SELECT COUNT(*) FROM session_generations WHERE session_id = ?"},
		{"agent_sessions", "SELECT COUNT(*) FROM agent_sessions WHERE id = ?"},
	}
	for _, c := range cases {
		var count int
		if err := db.sqlDB.QueryRow(c.query, sid).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", c.name, err)
		}
		if count != 0 {
			t.Errorf("%s still has %d row(s) after DeleteSession", c.name, count)
		}
	}

	var ftsAfter int
	if err := db.sqlDB.QueryRow("SELECT COUNT(*) FROM generation_turns_fts").Scan(&ftsAfter); err != nil {
		t.Fatalf("count fts after: %v", err)
	}
	if ftsAfter != 0 {
		t.Errorf("generation_turns_fts still has %d row(s) after DeleteSession", ftsAfter)
	}
}

func TestMessagesOnBranchLongChain(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/r", "r")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	sid := "long-chain"
	if err := db.CreateSession(sid, projectID, "long chain test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	const n = 1500
	var prevID int64
	baseTime := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		now := baseTime.Add(time.Duration(i) * time.Second)
		id, err := db.SaveMessage(sid, "user", fmt.Sprintf("msg-%d", i), "plain", now, "", 0, false, prevID)
		if err != nil {
			t.Fatalf("SaveMessage %d: %v", i, err)
		}
		prevID = id
	}

	// MessagesOnBranch from the leaf should return all 1500.
	msgs, err := db.MessagesOnBranch(sid, prevID)
	if err != nil {
		t.Fatalf("MessagesOnBranch: %v", err)
	}
	if len(msgs) != n {
		t.Fatalf("got %d messages, want %d", len(msgs), n)
	}
	for i, m := range msgs {
		if m.Content != fmt.Sprintf("msg-%d", i) {
			t.Fatalf("message %d content = %q, want %q", i, m.Content, fmt.Sprintf("msg-%d", i))
		}
	}
}

func TestListSessionsPagination(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cwd := "/home/user/pp"
	pid, err := d.GetOrCreateProject(cwd, "pp")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := d.CreateSession(fmt.Sprintf("sess_%d", i), pid, "", t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Page size 2 from the top (newest first).
	page1, next1, err := d.ListSessions(context.Background(), cwd, "", 2)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 || page1[0].SessionID != "sess_4" || page1[1].SessionID != "sess_3" {
		t.Fatalf("page1 = %+v", page1)
	}
	if next1 == "" {
		t.Fatalf("expected nextCursor on page1")
	}
	page2, next2, err := d.ListSessions(context.Background(), cwd, next1, 2)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 || page2[0].SessionID != "sess_2" || page2[1].SessionID != "sess_1" {
		t.Fatalf("page2 = %+v", page2)
	}
	if next2 == "" {
		t.Fatalf("expected nextCursor on page2")
	}
	page3, next3, err := d.ListSessions(context.Background(), cwd, next2, 2)
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3) != 1 || page3[0].SessionID != "sess_0" {
		t.Fatalf("page3 = %+v", page3)
	}
	if next3 != "" {
		t.Fatalf("next3 = %q, want empty", next3)
	}

	// Invalid cursor is an error.
	if _, _, err := d.ListSessions(context.Background(), cwd, "not-base64!!!", 2); err == nil {
		t.Fatalf("expected error for invalid cursor")
	}
}
