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
