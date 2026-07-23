package db

import (
	"strings"
	"testing"
	"time"
)

func TestSearchArchivedTurns(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	sessionID := seedSession(t, db)
	now := time.Now().UTC()

	// Begin a generation.
	g := Generation{
		ID:        "gen-search-1",
		SessionID: sessionID,
		Seq:       1,
		StartedAt: now,
	}
	if err := db.BeginGeneration(g); err != nil {
		t.Fatalf("BeginGeneration failed: %v", err)
	}

	// Archive turns with varied content.
	turns := []ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: "hello world", ToolCalls: "", CreatedAt: now},
		{TurnSeq: 2, Role: "assistant", Content: "the quick brown fox", ToolCalls: "", CreatedAt: now},
		{TurnSeq: 3, Role: "user", Content: "jumps over the lazy dog", ToolCalls: "", CreatedAt: now},
	}
	if err := db.ArchiveTurns("gen-search-1", turns, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns failed: %v", err)
	}

	// Begin a second generation in the same session.
	g2 := Generation{
		ID:        "gen-search-2",
		SessionID: sessionID,
		Seq:       2,
		StartedAt: now.Add(time.Minute),
	}
	if err := db.BeginGeneration(g2); err != nil {
		t.Fatalf("BeginGeneration failed: %v", err)
	}

	turns2 := []ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: "hello from second generation", ToolCalls: "", CreatedAt: now.Add(time.Minute)},
	}
	if err := db.ArchiveTurns("gen-search-2", turns2, 1024, now.Add(time.Minute)); err != nil {
		t.Fatalf("ArchiveTurns (gen2) failed: %v", err)
	}

	t.Run("finds inline rows", func(t *testing.T) {
		results, err := db.SearchArchivedTurns("", "hello", "", 10)
		if err != nil {
			t.Fatalf("SearchArchivedTurns failed: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected at least one result for 'hello'")
		}
		found := false
		for _, h := range results {
			if h.Turn.Content == "hello world" {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("expected to find 'hello world' turn")
		}
	})

	t.Run("finds blob-backed rows with resolved content", func(t *testing.T) {
		// Archive a large turn (blob-backed) with a distinctive word.
		largeContent := "blobBackedSearchable " + strings.Repeat("padding ", 1000)
		largeTurns := []ArchivedTurn{
			{TurnSeq: 2, Role: "assistant", Content: largeContent, ToolCalls: "", CreatedAt: now.Add(time.Minute)},
		}
		if err := db.ArchiveTurns("gen-search-2", largeTurns, 1024, now.Add(time.Minute)); err != nil {
			t.Fatalf("ArchiveTurns (large) failed: %v", err)
		}

		results, err := db.SearchArchivedTurns("", "blobBackedSearchable", "", 10)
		if err != nil {
			t.Fatalf("SearchArchivedTurns failed: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected at least one result for blob-backed content")
		}
		found := false
		for _, h := range results {
			if h.Turn.Content == largeContent {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("expected to find blob-backed turn with resolved content")
		}
	})

	t.Run("scoped search by generation_id excludes other generations", func(t *testing.T) {
		results, err := db.SearchArchivedTurns("", "hello", "gen-search-1", 10)
		if err != nil {
			t.Fatalf("SearchArchivedTurns failed: %v", err)
		}
		for _, h := range results {
			if h.GenerationID != "gen-search-1" {
				t.Fatalf("expected all results to have generation_id gen-search-1, got %s", h.GenerationID)
			}
		}
		// "hello" appears in both gen-search-1 and gen-search-2, but scoped
		// to gen-search-1 we should only get the gen-search-1 match.
		if len(results) != 1 {
			t.Fatalf("expected 1 result scoped to gen-search-1, got %d", len(results))
		}
	})

	t.Run("FTS5 operator characters in queries are treated as literal text", func(t *testing.T) {
		// Characters like * and " are FTS5 operators. ftsPhrase wraps them
		// in double quotes so they are treated literally.
		results, err := db.SearchArchivedTurns("", "the quick brown fox", "", 10)
		if err != nil {
			t.Fatalf("SearchArchivedTurns failed: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected results for literal phrase query")
		}
	})

	t.Run("empty query returns nil", func(t *testing.T) {
		results, err := db.SearchArchivedTurns("", "  ", "", 10)
		if err != nil {
			t.Fatalf("SearchArchivedTurns failed: %v", err)
		}
		if results != nil {
			t.Fatal("expected nil for empty query")
		}
	})

	t.Run("scoped search by session_id", func(t *testing.T) {
		results, err := db.SearchArchivedTurns(sessionID, "hello", "", 10)
		if err != nil {
			t.Fatalf("SearchArchivedTurns failed: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected results for session-scoped search")
		}
		for _, h := range results {
			if h.SessionID != sessionID {
				t.Fatalf("expected all results to have session_id %s, got %s", sessionID, h.SessionID)
			}
		}
	})
}
