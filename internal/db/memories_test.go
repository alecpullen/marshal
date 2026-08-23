package db

import (
	"testing"
	"time"
)

func TestSaveAndGetMemories(t *testing.T) {
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
	if err := db.CreateSession("sess-1", projectID, "", time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	now := time.Unix(100, 0).UTC()
	if err := db.SaveMemory(projectID, "fact", "Uses SQLite for persistence", "sess-1", now); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	if err := db.SaveMemory(projectID, "architecture", "TUI built with Bubble Tea", "sess-1", now.Add(time.Second)); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}

	memories, err := db.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	if len(memories) != 2 {
		t.Fatalf("len(memories) = %d, want 2: %#v", len(memories), memories)
	}
	if memories[0].Kind != "fact" || memories[0].Content != "Uses SQLite for persistence" {
		t.Fatalf("memories[0] = %#v", memories[0])
	}
	if memories[0].Confidence != MemoryConfidenceTentative {
		t.Fatalf("memories[0].Confidence = %q, want %q", memories[0].Confidence, MemoryConfidenceTentative)
	}
	if memories[0].SourceSessionID != "sess-1" {
		t.Fatalf("memories[0].SourceSessionID = %q, want %q", memories[0].SourceSessionID, "sess-1")
	}
	if !memories[0].CreatedAt.Equal(now) || !memories[0].UpdatedAt.Equal(now) {
		t.Fatalf("memories[0] timestamps = %#v, want created=updated=%s", memories[0], now)
	}
	if memories[1].Kind != "architecture" {
		t.Fatalf("memories[1].Kind = %q, want %q", memories[1].Kind, "architecture")
	}
}

func TestGetMemoriesEmptyProject(t *testing.T) {
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

	memories, err := db.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("expected 0 memories, got %d", len(memories))
	}
}

func TestSaveMemoryDeduplicatesByContentHash(t *testing.T) {
	db := openMigratedTest(t)
	projectID, err := db.GetOrCreateProject("/tmp/proj-dedup", "proj-dedup")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := db.CreateSession("sess-1", projectID, "", time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CreateSession sess-1: %v", err)
	}
	if err := db.CreateSession("sess-2", projectID, "", time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CreateSession sess-2: %v", err)
	}
	first := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := db.SaveMemory(projectID, "fact", "The  repo   uses SQLite.", "sess-1", first); err != nil {
		t.Fatalf("first SaveMemory: %v", err)
	}
	// Whitespace/case variant of the same content must NOT insert a new row.
	second := first.Add(time.Hour)
	if err := db.SaveMemory(projectID, "fact", "the repo uses sqlite.", "sess-2", second); err != nil {
		t.Fatalf("second SaveMemory: %v", err)
	}
	memories, err := db.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}
	m := memories[0]
	if m.SourceSessionID != "sess-2" {
		t.Errorf("SourceSessionID = %q, want sess-2 (refreshed)", m.SourceSessionID)
	}
	if !m.UpdatedAt.Equal(second) {
		t.Errorf("UpdatedAt = %v, want %v (refreshed)", m.UpdatedAt, second)
	}
	if !m.CreatedAt.Equal(first) {
		t.Errorf("CreatedAt = %v, want %v (unchanged)", m.CreatedAt, first)
	}
}

func TestSaveMemoryRefreshPromotesKind(t *testing.T) {
	db := openMigratedTest(t)
	projectID, err := db.GetOrCreateProject("/tmp/proj-kind", "proj-kind")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := db.CreateSession("sess-1", projectID, "", time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CreateSession sess-1: %v", err)
	}
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := db.SaveMemory(projectID, "fact", "uses SQLite for persistence", "sess-1", now); err != nil {
		t.Fatalf("first SaveMemory: %v", err)
	}
	// Same normalized content re-classified as architecture must NOT stay fact.
	if err := db.SaveMemory(projectID, "architecture", "uses SQLite for persistence", "sess-1", now.Add(time.Hour)); err != nil {
		t.Fatalf("reclassified SaveMemory: %v", err)
	}
	memories, err := db.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1 (still deduped)", len(memories))
	}
	if memories[0].Kind != "architecture" {
		t.Errorf("Kind = %q, want %q (kind promoted on refresh)", memories[0].Kind, "architecture")
	}
}

func TestSaveMemoryRefreshDoesNotRegressUpdatedAt(t *testing.T) {
	db := openMigratedTest(t)
	projectID, err := db.GetOrCreateProject("/tmp/proj-ts", "proj-ts")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := db.CreateSession("sess-1", projectID, "", time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CreateSession sess-1: %v", err)
	}
	first := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := db.SaveMemory(projectID, "fact", "content", "sess-1", first); err != nil {
		t.Fatalf("first SaveMemory: %v", err)
	}
	// A later save with an earlier clock must not move updated_at backwards.
	if err := db.SaveMemory(projectID, "fact", "content", "sess-1", first.Add(-time.Hour)); err != nil {
		t.Fatalf("regressed-clock SaveMemory: %v", err)
	}
	memories, err := db.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}
	if !memories[0].UpdatedAt.Equal(first) {
		t.Errorf("UpdatedAt = %v, want %v (must not regress)", memories[0].UpdatedAt, first)
	}
}

func TestSaveMemoryDistinctContentStillInserts(t *testing.T) {
	db := openMigratedTest(t)
	projectID, err := db.GetOrCreateProject("/tmp/proj-distinct", "proj-distinct")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := db.CreateSession("sess", projectID, "", time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	now := time.Now()
	if err := db.SaveMemory(projectID, "fact", "Alpha.", "sess", now); err != nil {
		t.Fatalf("SaveMemory alpha: %v", err)
	}
	if err := db.SaveMemory(projectID, "fact", "Beta.", "sess", now); err != nil {
		t.Fatalf("SaveMemory beta: %v", err)
	}
	memories, err := db.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories: %v", err)
	}
	if len(memories) != 2 {
		t.Fatalf("len(memories) = %d, want 2", len(memories))
	}
}

func TestMemoryContentHashNormalization(t *testing.T) {
	a := MemoryContentHash("The  Repo\nuses\tSQLite.")
	b := MemoryContentHash("the repo uses sqlite.")
	if a != b {
		t.Fatalf("hashes differ for normalized-equal content: %q vs %q", a, b)
	}
	if c := MemoryContentHash("different"); c == a {
		t.Fatal("hashes equal for different content")
	}
}

func openMigratedTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	return db
}

func TestSetMemoryConfidenceTransitions(t *testing.T) {
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
	if err := db.CreateSession("sess-1", projectID, "", time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	now := time.Unix(100, 0).UTC()
	if err := db.SaveMemory(projectID, "fact", "content", "sess-1", now); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}
	memories, err := db.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	id := memories[0].ID

	later := now.Add(time.Hour)
	if err := db.SetMemoryConfidence(id, MemoryConfidenceStale, later); err != nil {
		t.Fatalf("SetMemoryConfidence failed: %v", err)
	}

	memories, err = db.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	if memories[0].Confidence != MemoryConfidenceStale {
		t.Fatalf("Confidence = %q, want %q", memories[0].Confidence, MemoryConfidenceStale)
	}
	if !memories[0].UpdatedAt.Equal(later) {
		t.Fatalf("UpdatedAt = %s, want %s", memories[0].UpdatedAt, later)
	}
	if !memories[0].CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt changed: %s, want unchanged %s", memories[0].CreatedAt, now)
	}
}
