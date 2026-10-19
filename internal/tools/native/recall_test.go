package native

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/db"
)

// newTestRecallDB creates an in-memory SQLite DB with a session and a
// generation, then archives a single turn with the given content. It returns
// the open DB (caller must Close).
//
// The recall_history tool was folded into transcript_read in the
// context-diet Tier 2a cut, so this fixture now feeds the shared search
// helper (searchArchivedTurns) directly.
func newTestRecallDB(t *testing.T, content string) *db.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/test/repo", "test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	sessionID := "recall-trunc-test-session"
	if err := database.CreateSession(sessionID, projectID, "test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	now := time.Now().UTC()
	gen := db.Generation{
		ID:        "recall-trunc-gen",
		SessionID: sessionID,
		Seq:       1,
		StartedAt: now,
	}
	if err := database.BeginGeneration(gen); err != nil {
		t.Fatalf("BeginGeneration failed: %v", err)
	}
	turns := []db.ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: content, ToolCalls: "", CreatedAt: now},
	}
	if err := database.ArchiveTurns(gen.ID, turns, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns failed: %v", err)
	}
	return database
}

// TestSearchArchivedTurnsTruncatesOversizedHits: one enormous archived turn
// would otherwise blow the context window the search exists to protect. The
// helper must cap per-hit excerpts and the overall result.
func TestSearchArchivedTurnsTruncatesOversizedHits(t *testing.T) {
	giantContent := strings.Repeat("giant tool output line\n", 5000)
	database := newTestRecallDB(t, giantContent)
	defer database.Close()

	result, err := searchArchivedTurns(database, "giant", 5)
	if err != nil {
		t.Fatalf("searchArchivedTurns returned error: %v", err)
	}
	if len(result.Content) > recallMaxChars+1000 {
		t.Fatalf("result length = %d, want <= %d (bounded)", len(result.Content), recallMaxChars+1000)
	}
	if !strings.Contains(result.Content, "[truncated]") {
		t.Fatalf("result should contain the '[truncated]' marker for an oversized hit; got:\n%s", result.Content)
	}
}

// TestSearchArchivedTurnsNoMatches reports the empty case without erroring,
// and names the scope so a query about the current session's own work does not
// read as "the phrase is absent from the archive".
func TestSearchArchivedTurnsNoMatches(t *testing.T) {
	database := newTestRecallDB(t, "nothing relevant here")
	defer database.Close()

	result, err := searchArchivedTurns(database, "unfindable-string-xyz", 5)
	if err != nil {
		t.Fatalf("searchArchivedTurns returned error: %v", err)
	}
	if !strings.Contains(result.Content, "No matching archived turns found.") {
		t.Fatalf("result = %q, want the no-matches notice", result.Content)
	}
	if !strings.Contains(result.Content, "not the current session's live turns") {
		t.Fatalf("result = %q, want it to name the search scope", result.Content)
	}
}
