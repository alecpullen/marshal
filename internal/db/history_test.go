package db

import (
	"fmt"
	"testing"
	"time"
)

func openHistoryTestDB(t *testing.T) (*DB, int64) {
	t.Helper()
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	return database, projectID
}

func TestSaveAndRecentPrompts(t *testing.T) {
	database, projectID := openHistoryTestDB(t)
	now := time.Unix(100, 0)
	for _, p := range []string{"first", "second", "third"} {
		if err := database.SavePrompt(projectID, p, now); err != nil {
			t.Fatalf("SavePrompt(%q): %v", p, err)
		}
	}
	got, err := database.RecentPrompts(projectID, 200)
	if err != nil {
		t.Fatalf("RecentPrompts: %v", err)
	}
	want := []string{"third", "second", "first"}
	if len(got) != len(want) {
		t.Fatalf("RecentPrompts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSavePromptCollapsesConsecutiveDuplicates(t *testing.T) {
	database, projectID := openHistoryTestDB(t)
	now := time.Unix(100, 0)
	for _, p := range []string{"same", "same", "other", "same"} {
		if err := database.SavePrompt(projectID, p, now); err != nil {
			t.Fatalf("SavePrompt(%q): %v", p, err)
		}
	}
	got, err := database.RecentPrompts(projectID, 200)
	if err != nil {
		t.Fatalf("RecentPrompts: %v", err)
	}
	// Only consecutive duplicates collapse: same, other, same.
	if len(got) != 3 || got[0] != "same" || got[1] != "other" || got[2] != "same" {
		t.Fatalf("RecentPrompts = %v, want [same other same]", got)
	}
}

func TestSavePromptSkipsEmpty(t *testing.T) {
	database, projectID := openHistoryTestDB(t)
	if err := database.SavePrompt(projectID, "   ", time.Unix(100, 0)); err != nil {
		t.Fatalf("SavePrompt: %v", err)
	}
	got, err := database.RecentPrompts(projectID, 200)
	if err != nil {
		t.Fatalf("RecentPrompts: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("RecentPrompts = %v, want no entries", got)
	}
}

func TestRecentPromptsLimitAndProjectScope(t *testing.T) {
	database, projectID := openHistoryTestDB(t)
	otherID, err := database.GetOrCreateProject("/other", "other")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	now := time.Unix(100, 0)
	for i := 0; i < 5; i++ {
		if err := database.SavePrompt(projectID, fmt.Sprintf("p%d", i), now); err != nil {
			t.Fatalf("SavePrompt: %v", err)
		}
	}
	if err := database.SavePrompt(otherID, "foreign", now); err != nil {
		t.Fatalf("SavePrompt: %v", err)
	}
	got, err := database.RecentPrompts(projectID, 2)
	if err != nil {
		t.Fatalf("RecentPrompts: %v", err)
	}
	if len(got) != 2 || got[0] != "p4" || got[1] != "p3" {
		t.Fatalf("RecentPrompts(limit=2) = %v, want [p4 p3]", got)
	}
}

func TestSavePromptPrunesOldEntries(t *testing.T) {
	database, projectID := openHistoryTestDB(t)
	now := time.Unix(100, 0)
	for i := 0; i < maxPromptHistoryRows+2; i++ {
		if err := database.SavePrompt(projectID, fmt.Sprintf("prompt-%d", i), now); err != nil {
			t.Fatalf("SavePrompt %d: %v", i, err)
		}
	}
	got, err := database.RecentPrompts(projectID, maxPromptHistoryRows+10)
	if err != nil {
		t.Fatalf("RecentPrompts: %v", err)
	}
	if len(got) != maxPromptHistoryRows {
		t.Fatalf("after prune got %d rows, want %d", len(got), maxPromptHistoryRows)
	}
	wantNewest := fmt.Sprintf("prompt-%d", maxPromptHistoryRows+1)
	wantOldest := "prompt-2"
	if got[0] != wantNewest {
		t.Errorf("newest = %q, want %q", got[0], wantNewest)
	}
	if got[len(got)-1] != wantOldest {
		t.Errorf("oldest retained = %q, want %q", got[len(got)-1], wantOldest)
	}
}
