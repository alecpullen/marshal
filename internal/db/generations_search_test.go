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
		{TurnSeq: 4, Role: "user", Content: "hello OR world test", ToolCalls: "", CreatedAt: now},
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
		// "hello" appears in gen-search-1 (turns 1 and 4) and gen-search-2,
		// but scoped to gen-search-1 we should only get the gen-search-1 matches.
		if len(results) != 2 {
			t.Fatalf("expected 2 results scoped to gen-search-1, got %d", len(results))
		}
	})

	t.Run("FTS5 operator characters in queries are treated as literal text", func(t *testing.T) {
		// "OR" is an FTS5 boolean operator. ftsPhrase wraps the query in
		// double quotes so "hello OR world" is treated as a literal phrase
		// matching only the turn with that exact content, rather than as
		// a boolean OR matching any turn containing "hello" or "world".
		results, err := db.SearchArchivedTurns("", "hello OR world", "", 10)
		if err != nil {
			t.Fatalf("SearchArchivedTurns failed: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected exactly 1 result for literal phrase 'hello OR world', got %d", len(results))
		}
		if results[0].Turn.Content != "hello OR world test" {
			t.Fatalf("expected turn with literal 'hello OR world test', got %q", results[0].Turn.Content)
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
