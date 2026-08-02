package db

import (
	"testing"
)

func TestScratchpadEntryRoundTrip(t *testing.T) {
	db := newTestDB(t)
	sessionID := "session-1"

	e1 := NewScratchpadEntry("audit", "results", "json")
	if err := db.SaveScratchpadEntry(sessionID, e1); err != nil {
		t.Fatalf("save: %v", err)
	}

	entries, err := db.LoadScratchpad(sessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Key != "audit" || entries[0].Content != "results" || entries[0].Format != "json" {
		t.Fatalf("entry mismatch: %+v", entries[0])
	}
}

func TestScratchpadEntryOverwrite(t *testing.T) {
	db := newTestDB(t)
	sessionID := "session-1"

	db.SaveScratchpadEntry(sessionID, NewScratchpadEntry("key", "v1", "text"))
	db.SaveScratchpadEntry(sessionID, NewScratchpadEntry("key", "v2", "text"))

	entries, _ := db.LoadScratchpad(sessionID)
	if len(entries) != 1 || entries[0].Content != "v2" {
		t.Fatalf("expected single overwritten entry with v2, got %+v", entries)
	}
}

func TestScratchpadEntryDelete(t *testing.T) {
	db := newTestDB(t)
	sessionID := "session-1"

	db.SaveScratchpadEntry(sessionID, NewScratchpadEntry("key", "value", "text"))
	if err := db.DeleteScratchpadEntry(sessionID, "key"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	entries, _ := db.LoadScratchpad(sessionID)
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestScratchpadLoadOrdersByUpdatedDesc(t *testing.T) {
	db := newTestDB(t)
	sessionID := "session-1"

	e1 := NewScratchpadEntry("old", "old", "text")
	e1.Updated = 1
	e2 := NewScratchpadEntry("new", "new", "text")
	e2.Updated = 2

	db.SaveScratchpadEntry(sessionID, e1)
	db.SaveScratchpadEntry(sessionID, e2)

	entries, _ := db.LoadScratchpad(sessionID)
	if entries[0].Key != "new" || entries[1].Key != "old" {
		t.Fatalf("expected newest first, got %+v", entries)
	}
}
