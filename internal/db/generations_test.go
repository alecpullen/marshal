package db

import (
	"strings"
	"testing"
	"time"
)

// seedSession creates a project and session, returning the session ID.
func seedSession(t *testing.T, database *DB) string {
	t.Helper()
	projectID, err := database.GetOrCreateProject("/test/repo", "test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	sessionID := "test-session-gen"
	if err := database.CreateSession(sessionID, projectID, "test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	return sessionID
}

func TestGenerationLifecycle(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	sessionID := seedSession(t, db)
	now := time.Now().UTC()

	// Begin a generation.
	g := Generation{
		ID:           "gen-1",
		SessionID:    sessionID,
		Seq:          1,
		StartedAt:    now,
		SeedDigest:   "abc123",
		DigestSource: "user",
	}
	if err := db.BeginGeneration(g); err != nil {
		t.Fatalf("BeginGeneration failed: %v", err)
	}

	// Archive small turns (inline content).
	smallTurns := []ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: "hello", ToolCalls: "", CreatedAt: now},
		{TurnSeq: 2, Role: "assistant", Content: "hi there", ToolCalls: `{"name":"test"}`, CreatedAt: now},
	}
	if err := db.ArchiveTurns("gen-1", smallTurns, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns (small) failed: %v", err)
	}

	// Archive large turns (blob ref).
	largeContent := strings.Repeat("A", 5000)
	largeTurns := []ArchivedTurn{
		{TurnSeq: 3, Role: "assistant", Content: largeContent, ToolCalls: "", CreatedAt: now},
	}
	if err := db.ArchiveTurns("gen-1", largeTurns, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns (large) failed: %v", err)
	}

	// End the generation.
	if err := db.EndGeneration("gen-1", now.Add(time.Minute), "completed"); err != nil {
		t.Fatalf("EndGeneration failed: %v", err)
	}

	// Verify GenerationsForSession.
	gens, err := db.GenerationsForSession(sessionID)
	if err != nil {
		t.Fatalf("GenerationsForSession failed: %v", err)
	}
	if len(gens) != 1 {
		t.Fatalf("expected 1 generation, got %d", len(gens))
	}
	if gens[0].ID != "gen-1" {
		t.Fatalf("expected gen-1, got %s", gens[0].ID)
	}
	if gens[0].Seq != 1 {
		t.Fatalf("expected seq 1, got %d", gens[0].Seq)
	}
	if gens[0].SeedDigest != "abc123" {
		t.Fatalf("expected seed_digest abc123, got %s", gens[0].SeedDigest)
	}
	if gens[0].DigestSource != "user" {
		t.Fatalf("expected digest_source user, got %s", gens[0].DigestSource)
	}
	if gens[0].EndReason != "completed" {
		t.Fatalf("expected end_reason completed, got %s", gens[0].EndReason)
	}
	if gens[0].EndedAt == nil {
		t.Fatal("expected EndedAt to be set")
	}

	// Verify TurnsForGeneration resolves both inline and blob content.
	turns, err := db.TurnsForGeneration("gen-1")
	if err != nil {
		t.Fatalf("TurnsForGeneration failed: %v", err)
	}
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(turns))
	}
	if turns[0].Content != "hello" {
		t.Fatalf("turn 0 content: expected 'hello', got %q", turns[0].Content)
	}
	if turns[1].Content != "hi there" {
		t.Fatalf("turn 1 content: expected 'hi there', got %q", turns[1].Content)
	}
	if turns[1].ToolCalls != `{"name":"test"}` {
		t.Fatalf("turn 1 tool_calls: expected %q, got %q", `{"name":"test"}`, turns[1].ToolCalls)
	}
	if turns[2].Content != largeContent {
		t.Fatalf("turn 2 content (blob): expected large content, got %d bytes", len(turns[2].Content))
	}

	// Verify GenerationTurnCount.
	count, err := db.GenerationTurnCount("gen-1")
	if err != nil {
		t.Fatalf("GenerationTurnCount failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 turns, got %d", count)
	}
}

func TestReconcileOpenGenerations(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	sessionID := seedSession(t, db)
	now := time.Now().UTC()

	// Begin two generations.
	g1 := Generation{
		ID:        "gen-open-1",
		SessionID: sessionID,
		Seq:       1,
		StartedAt: now,
	}
	if err := db.BeginGeneration(g1); err != nil {
		t.Fatalf("BeginGeneration failed: %v", err)
	}

	g2 := Generation{
		ID:        "gen-open-2",
		SessionID: sessionID,
		Seq:       2,
		StartedAt: now.Add(time.Second),
	}
	if err := db.BeginGeneration(g2); err != nil {
		t.Fatalf("BeginGeneration failed: %v", err)
	}

	// End only the second one.
	if err := db.EndGeneration("gen-open-2", now.Add(2*time.Second), "completed"); err != nil {
		t.Fatalf("EndGeneration failed: %v", err)
	}

	// Verify both appear in GenerationsForSession.
	gens, err := db.GenerationsForSession(sessionID)
	if err != nil {
		t.Fatalf("GenerationsForSession failed: %v", err)
	}
	if len(gens) != 2 {
		t.Fatalf("expected 2 generations, got %d", len(gens))
	}

	// gen-open-1 should still be open (EndedAt nil).
	if gens[0].ID == "gen-open-1" && gens[0].EndedAt != nil {
		t.Fatal("expected gen-open-1 to be open (EndedAt nil)")
	}
	if gens[0].ID == "gen-open-2" && gens[0].EndedAt == nil {
		t.Fatal("expected gen-open-2 to be closed (EndedAt set)")
	}

	// Idempotent re-close: closing gen-open-1 twice should be a no-op.
	if err := db.EndGeneration("gen-open-1", now.Add(3*time.Second), "interrupted"); err != nil {
		t.Fatalf("first EndGeneration failed: %v", err)
	}
	if err := db.EndGeneration("gen-open-1", now.Add(4*time.Second), "interrupted-again"); err != nil {
		t.Fatalf("second EndGeneration (idempotent) failed: %v", err)
	}

	// Verify the end_reason is the first one written.
	gens, err = db.GenerationsForSession(sessionID)
	if err != nil {
		t.Fatalf("GenerationsForSession failed: %v", err)
	}
	for _, g := range gens {
		if g.ID == "gen-open-1" {
			if g.EndReason != "interrupted" {
				t.Fatalf("expected end_reason 'interrupted' (first close), got %q", g.EndReason)
			}
			break
		}
	}
}
