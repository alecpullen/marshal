package history

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"marshal/internal/db"
)

func TestListGenerations(t *testing.T) {
	database := newTestDB(t)
	defer database.Close()

	sessionID := seedSession(t, database)
	now := time.Now().UTC()

	// Create two generations, one live, one ended.
	g1 := db.Generation{
		ID: "gen-list-1", SessionID: sessionID, Seq: 1,
		StartedAt: now, SeedDigest: "digest-one",
	}
	if err := database.BeginGeneration(g1); err != nil {
		t.Fatalf("BeginGeneration: %v", err)
	}
	// Archive a turn for gen 1.
	if err := database.ArchiveTurns("gen-list-1", []db.ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: "hello", CreatedAt: now},
	}, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns: %v", err)
	}

	g2 := db.Generation{
		ID: "gen-list-2", SessionID: sessionID, Seq: 2,
		StartedAt: now.Add(time.Minute), SeedDigest: "digest-two",
	}
	if err := database.BeginGeneration(g2); err != nil {
		t.Fatalf("BeginGeneration: %v", err)
	}
	// End gen 2.
	if err := database.EndGeneration("gen-list-2", now.Add(2*time.Minute), "completed"); err != nil {
		t.Fatalf("EndGeneration: %v", err)
	}

	summaries, err := ListGenerations(context.Background(), database, sessionID)
	if err != nil {
		t.Fatalf("ListGenerations: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}

	// Gen 1: live, 1 turn, digest-one
	if summaries[0].Seq != 1 {
		t.Errorf("seq 1 expected, got %d", summaries[0].Seq)
	}
	if !summaries[0].Live {
		t.Errorf("gen 1 should be live")
	}
	if summaries[0].TurnCount != 1 {
		t.Errorf("gen 1 turn count: expected 1, got %d", summaries[0].TurnCount)
	}
	if summaries[0].SeedDigest != "digest-one" {
		t.Errorf("gen 1 digest: expected digest-one, got %s", summaries[0].SeedDigest)
	}

	// Gen 2: ended, 0 turns, digest-two
	if summaries[1].Seq != 2 {
		t.Errorf("seq 2 expected, got %d", summaries[1].Seq)
	}
	if summaries[1].Live {
		t.Errorf("gen 2 should be ended")
	}
	if summaries[1].TurnCount != 0 {
		t.Errorf("gen 2 turn count: expected 0, got %d", summaries[1].TurnCount)
	}
	if summaries[1].SeedDigest != "digest-two" {
		t.Errorf("gen 2 digest: expected digest-two, got %s", summaries[1].SeedDigest)
	}
}

func TestDumpGeneration(t *testing.T) {
	database := newTestDB(t)
	defer database.Close()

	sessionID := seedSession(t, database)
	now := time.Now().UTC()

	g := db.Generation{
		ID: "gen-dump-1", SessionID: sessionID, Seq: 1,
		StartedAt: now, SeedDigest: "dump-digest",
	}
	if err := database.BeginGeneration(g); err != nil {
		t.Fatalf("BeginGeneration: %v", err)
	}
	if err := database.ArchiveTurns("gen-dump-1", []db.ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: "first turn", CreatedAt: now},
		{TurnSeq: 2, Role: "assistant", Content: "second turn", ToolCalls: `{"fn":"test"}`, CreatedAt: now},
	}, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns: %v", err)
	}
	if err := database.EndGeneration("gen-dump-1", now.Add(time.Hour), "completed"); err != nil {
		t.Fatalf("EndGeneration: %v", err)
	}

	out, err := DumpGeneration(context.Background(), database, DumpOptions{
		SessionID: sessionID, GenerationSeq: 1,
	})
	if err != nil {
		t.Fatalf("DumpGeneration: %v", err)
	}

	// Check key parts of the output.
	if !strings.Contains(out, "Generation 1 (ended)") {
		t.Errorf("output missing generation header: %s", out)
	}
	if !strings.Contains(out, "first turn") {
		t.Errorf("output missing turn content: %s", out)
	}
	if !strings.Contains(out, "second turn") {
		t.Errorf("output missing second turn: %s", out)
	}
	if !strings.Contains(out, `{"fn":"test"}`) {
		t.Errorf("output missing tool_calls: %s", out)
	}
	if !strings.Contains(out, "dump-digest") {
		t.Errorf("output missing digest: %s", out)
	}
}

func TestDumpGenerationUnknownSeq(t *testing.T) {
	database := newTestDB(t)
	defer database.Close()

	sessionID := seedSession(t, database)

	_, err := DumpGeneration(context.Background(), database, DumpOptions{
		SessionID: sessionID, GenerationSeq: 99,
	})
	if err == nil {
		t.Fatal("expected error for unknown generation seq")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestSearch(t *testing.T) {
	database := newTestDB(t)
	defer database.Close()

	sessionID := seedSession(t, database)
	now := time.Now().UTC()

	g := db.Generation{
		ID: "gen-search-1", SessionID: sessionID, Seq: 1,
		StartedAt: now,
	}
	if err := database.BeginGeneration(g); err != nil {
		t.Fatalf("BeginGeneration: %v", err)
	}
	if err := database.ArchiveTurns("gen-search-1", []db.ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: "hello world this is a test", CreatedAt: now},
		{TurnSeq: 2, Role: "assistant", Content: "the answer is forty two", CreatedAt: now},
	}, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns: %v", err)
	}

	out, err := Search(context.Background(), database, sessionID, "forty two", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(out, "Found 1 match") {
		t.Errorf("expected 1 match, got: %s", out)
	}
	if !strings.Contains(out, "gen 1, turn 2") {
		t.Errorf("expected gen 1, turn 2 in output: %s", out)
	}
	if !strings.Contains(out, "forty two") {
		t.Errorf("expected snippet in output: %s", out)
	}
}

func TestSearchNoMatches(t *testing.T) {
	database := newTestDB(t)
	defer database.Close()

	sessionID := seedSession(t, database)
	now := time.Now().UTC()

	g := db.Generation{
		ID: "gen-search-2", SessionID: sessionID, Seq: 1,
		StartedAt: now,
	}
	if err := database.BeginGeneration(g); err != nil {
		t.Fatalf("BeginGeneration: %v", err)
	}
	if err := database.ArchiveTurns("gen-search-2", []db.ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: "some content", CreatedAt: now},
	}, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns: %v", err)
	}

	out, err := Search(context.Background(), database, sessionID, "nonexistent", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if out != "No matches found." {
		t.Errorf("expected 'No matches found.', got: %s", out)
	}
}

func TestSearchUTF8SnippetTruncation(t *testing.T) {
	database := newTestDB(t)
	defer database.Close()

	sessionID := seedSession(t, database)
	now := time.Now().UTC()

	g := db.Generation{
		ID: "gen-search-utf8", SessionID: sessionID, Seq: 1,
		StartedAt: now,
	}
	if err := database.BeginGeneration(g); err != nil {
		t.Fatalf("BeginGeneration: %v", err)
	}

	// 80 "a " tokens (160 runes / 160 bytes) plus 100 é runes (200 bytes).
	// Total 260 runes / 360 bytes, so truncation happens and byte 200 falls
	// inside the é rune sequence, which a naive byte-200 slice would split.
	content := strings.Repeat("a ", 80) + strings.Repeat("é", 100)
	if err := database.ArchiveTurns("gen-search-utf8", []db.ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: content, CreatedAt: now},
	}, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns: %v", err)
	}

	out, err := Search(context.Background(), database, sessionID, "a", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if !strings.Contains(out, "Found 1 match") {
		t.Errorf("expected 1 match, got: %s", out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("Search output is not valid UTF-8: %q", out)
	}

	// Extract the indented snippet line that follows the gen/turn header.
	var snippet string
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.Contains(line, "gen 1, turn 1") && i+1 < len(lines) {
			snippet = strings.TrimPrefix(lines[i+1], "    ")
			break
		}
	}

	// The snippet should be 200 runes of content plus the truncation marker.
	runes := []rune(snippet)
	if len(runes) != 203 {
		t.Errorf("expected 203 runes (200 content + ...), got %d", len(runes))
	}
	if string(runes[200:203]) != "..." {
		t.Errorf("expected trailing ..., got %q", string(runes[200:203]))
	}
}

// newTestDB creates an in-memory test database.
func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return database
}

// seedSession creates a project and session, returning the session ID.
func seedSession(t *testing.T, database *db.DB) string {
	t.Helper()
	projectID, err := database.GetOrCreateProject("/test/repo", "test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	sessionID := "test-session-" + t.Name()
	if err := database.CreateSession(sessionID, projectID, "test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return sessionID
}
