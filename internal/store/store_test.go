package store

import (
	"path/filepath"
	"testing"
	"time"
)

func setupTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	return store, dbPath
}

func TestStore_CreateAndGetSession(t *testing.T) {
	store, _ := setupTestStore(t)
	defer store.Close()

	session := &Session{
		ID:              "test-session-123",
		RepoRoot:        "/tmp/repo",
		Task:            "Write a test function",
		Status:          "RUNNING",
		BaseBranch:      "main",
		IsolationBranch: "marshal-session-test",
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	retrieved, err := store.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	if retrieved.ID != session.ID {
		t.Errorf("Expected ID %s, got %s", session.ID, retrieved.ID)
	}
	if retrieved.Task != session.Task {
		t.Errorf("Expected Task %s, got %s", session.Task, retrieved.Task)
	}
	if retrieved.Status != session.Status {
		t.Errorf("Expected Status %s, got %s", session.Status, retrieved.Status)
	}
}

func TestStore_UpdateSession(t *testing.T) {
	store, _ := setupTestStore(t)
	defer store.Close()

	session := &Session{
		ID:              "update-test",
		RepoRoot:        "/tmp/repo",
		Task:            "Update test",
		Status:          "RUNNING",
		BaseBranch:      "main",
		IsolationBranch: "marshal-session-update",
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Update session
	completedAt := time.Now()
	session.Status = "SUCCESS"
	session.CompletedAt = &completedAt
	session.PromptTokens = 1000
	session.CompletionTokens = 500
	session.UpdatedAt = time.Now()

	if err := store.UpdateSession(session); err != nil {
		t.Fatalf("UpdateSession failed: %v", err)
	}

	retrieved, err := store.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	if retrieved.Status != "SUCCESS" {
		t.Errorf("Expected Status SUCCESS, got %s", retrieved.Status)
	}
	if retrieved.PromptTokens != 1000 {
		t.Errorf("Expected 1000 prompt tokens, got %d", retrieved.PromptTokens)
	}
	if retrieved.CompletedAt == nil {
		t.Error("Expected CompletedAt to be set")
	}
}

func TestStore_ListSessions(t *testing.T) {
	store, _ := setupTestStore(t)
	defer store.Close()

	// Create multiple sessions
	for i := 0; i < 5; i++ {
		session := &Session{
			ID:         "list-test-" + string(rune('a'+i)),
			RepoRoot:   "/tmp/repo",
			Task:       "Task " + string(rune('A'+i)),
			Status:     "SUCCESS",
			BaseBranch: "main",
			CreatedAt:  time.Now().Add(time.Duration(-i) * time.Hour),
			UpdatedAt:  time.Now(),
		}
		if err := store.CreateSession(session); err != nil {
			t.Fatalf("CreateSession failed: %v", err)
		}
	}

	sessions, err := store.ListSessions(3)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}

	if len(sessions) != 3 {
		t.Errorf("Expected 3 sessions, got %d", len(sessions))
	}

	// Should be ordered by created_at desc (newest first)
	if sessions[0].ID != "list-test-a" {
		t.Errorf("Expected first session to be 'list-test-a', got %s", sessions[0].ID)
	}
}

func TestStore_CreateRound(t *testing.T) {
	store, _ := setupTestStore(t)
	defer store.Close()

	// Create session first
	session := &Session{
		ID:         "round-test-session",
		RepoRoot:   "/tmp/repo",
		Task:       "Round test",
		Status:     "RUNNING",
		BaseBranch: "main",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Create round
	round := &RoundRecord{
		SessionID:        session.ID,
		RoundNumber:      1,
		ExecutorRequest:  "Write a function",
		ExecutorResponse: "```go\nfunc test() {}\n```",
		Diff:             "diff --git a/test.go b/test.go",
		Verdict:          "PASS",
		Summary:          "Added test function",
		Issue:            "",
		Fix:              "",
		Concerns:         []string{"Minor: add docs"},
		PromptTokens:     100,
		CompletionTokens: 50,
		CreatedAt:        time.Now(),
	}

	if err := store.CreateRound(round); err != nil {
		t.Fatalf("CreateRound failed: %v", err)
	}

	if round.ID == 0 {
		t.Error("Expected round ID to be set after insert")
	}
}

func TestStore_GetRounds(t *testing.T) {
	store, _ := setupTestStore(t)
	defer store.Close()

	// Create session
	session := &Session{
		ID:         "rounds-test-session",
		RepoRoot:   "/tmp/repo",
		Task:       "Rounds test",
		Status:     "RUNNING",
		BaseBranch: "main",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Create multiple rounds
	for i := 1; i <= 3; i++ {
		round := &RoundRecord{
			SessionID:        session.ID,
			RoundNumber:      i,
			ExecutorRequest:  "Task",
			ExecutorResponse: "Response",
			Diff:             "diff",
			Verdict:          "PASS",
			Summary:          "Round " + string(rune('0'+i)),
			PromptTokens:     100 * i,
			CompletionTokens: 50 * i,
			CreatedAt:        time.Now(),
		}
		if err := store.CreateRound(round); err != nil {
			t.Fatalf("CreateRound failed: %v", err)
		}
	}

	rounds, err := store.GetRounds(session.ID)
	if err != nil {
		t.Fatalf("GetRounds failed: %v", err)
	}

	if len(rounds) != 3 {
		t.Errorf("Expected 3 rounds, got %d", len(rounds))
	}

	// Should be ordered by round_number
	for i, r := range rounds {
		if r.RoundNumber != i+1 {
			t.Errorf("Expected round %d, got %d", i+1, r.RoundNumber)
		}
	}
}

func TestStore_SessionRoundsForeignKey(t *testing.T) {
	store, _ := setupTestStore(t)
	defer store.Close()

	// Try to create round for non-existent session
	round := &RoundRecord{
		SessionID:   "non-existent",
		RoundNumber: 1,
		Verdict:     "PASS",
		CreatedAt:   time.Now(),
	}

	err := store.CreateRound(round)
	if err == nil {
		t.Error("Expected error when creating round for non-existent session")
	}
}

func TestStore_GetSession_NotFound(t *testing.T) {
	store, _ := setupTestStore(t)
	defer store.Close()

	_, err := store.GetSession("non-existent-id")
	if err == nil {
		t.Error("Expected error for non-existent session")
	}
}
