package acp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
)

func newMemoryTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := d.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	// Create a session so SaveMemory's FK on source_session_id works.
	if err := d.CreateSession("sess_a", projectID, "", time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return d, projectID
}

func TestMemoryListReturnsSeededEntries(t *testing.T) {
	d, projectID := newMemoryTestDB(t)
	now := time.Now()
	if err := d.SaveMemory(projectID, "fact", "uses Bubble Tea", "sess_a", now); err != nil {
		t.Fatalf("SaveMemory: %v", err)
	}

	mgr := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) {
			return &MemoryRuntime{DB: d, ProjectID: projectID}, true
		},
	})

	raw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	res, err := mgr.MemoryList(context.Background(), raw)
	if err != nil {
		t.Fatalf("MemoryList: %v", err)
	}
	result, ok := res.(MemoryListResult)
	if !ok {
		t.Fatalf("MemoryList result type = %T, want MemoryListResult", res)
	}
	if len(result.Entries) != 1 || result.Entries[0].Content != "uses Bubble Tea" || result.Entries[0].Confidence != "tentative" {
		t.Fatalf("MemoryList entries = %+v, unexpected", result.Entries)
	}
}

func TestMemoryListEmptyReturnsEmptySliceNotNil(t *testing.T) {
	d, projectID := newMemoryTestDB(t)
	mgr := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) {
			return &MemoryRuntime{DB: d, ProjectID: projectID}, true
		},
	})
	raw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	res, err := mgr.MemoryList(context.Background(), raw)
	if err != nil {
		t.Fatalf("MemoryList: %v", err)
	}
	if res.(MemoryListResult).Entries == nil {
		t.Fatal("MemoryList Entries = nil, want a non-nil empty slice")
	}
}

func TestMemoryListRequiresSessionID(t *testing.T) {
	mgr := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) { return nil, false },
	})
	_, err := mgr.MemoryList(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("MemoryList with no sessionId: got nil error, want an error")
	}
}

func TestMemoryListRejectsUnknownSession(t *testing.T) {
	mgr := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) { return nil, false },
	})
	raw, _ := json.Marshal(map[string]any{"sessionId": "no_such_session"})
	_, err := mgr.MemoryList(context.Background(), raw)
	if err == nil {
		t.Fatal("MemoryList for unknown session: got nil error, want an error")
	}
}

func TestMemoryListRejectsNilDB(t *testing.T) {
	mgr := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) {
			return &MemoryRuntime{DB: nil}, true
		},
	})
	raw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	_, err := mgr.MemoryList(context.Background(), raw)
	if err == nil {
		t.Fatal("MemoryList with nil DB: got nil error, want an error")
	}
}

func TestMemoryDeleteRemovesEntry(t *testing.T) {
	d, projectID := newMemoryTestDB(t)
	if err := d.SaveMemory(projectID, "fact", "to be deleted", "sess_a", time.Now()); err != nil {
		t.Fatalf("SaveMemory: %v", err)
	}
	rows, err := d.GetMemories(projectID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("setup: GetMemories = %v, %v", rows, err)
	}
	id := rows[0].ID

	mgr := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) {
			return &MemoryRuntime{DB: d, ProjectID: projectID}, true
		},
	})
	raw, _ := json.Marshal(MemoryDeleteParams{SessionID: "sess_1", ID: id})
	if _, err := mgr.MemoryDelete(context.Background(), raw); err != nil {
		t.Fatalf("MemoryDelete: %v", err)
	}

	rows, err = d.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories after delete: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("GetMemories after delete = %+v, want empty", rows)
	}
}

func TestMemoryDeleteRejectsZeroID(t *testing.T) {
	mgr := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) { return nil, false },
	})
	raw, _ := json.Marshal(MemoryDeleteParams{SessionID: "sess_1", ID: 0})
	_, err := mgr.MemoryDelete(context.Background(), raw)
	if err == nil {
		t.Fatal("MemoryDelete with id=0: got nil error, want an error")
	}
}

func TestMemorySetConfidenceUpdatesRow(t *testing.T) {
	d, projectID := newMemoryTestDB(t)
	if err := d.SaveMemory(projectID, "fact", "x", "sess_a", time.Now()); err != nil {
		t.Fatalf("SaveMemory: %v", err)
	}
	rows, _ := d.GetMemories(projectID)
	id := rows[0].ID

	mgr := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) {
			return &MemoryRuntime{DB: d, ProjectID: projectID}, true
		},
	})
	raw, _ := json.Marshal(MemorySetConfidenceParams{SessionID: "sess_1", ID: id, Confidence: "confirmed"})
	if _, err := mgr.MemorySetConfidence(context.Background(), raw); err != nil {
		t.Fatalf("MemorySetConfidence: %v", err)
	}

	rows, _ = d.GetMemories(projectID)
	if rows[0].Confidence != "confirmed" {
		t.Fatalf("Confidence after set = %q, want %q", rows[0].Confidence, "confirmed")
	}
}

func TestMemorySetConfidenceRejectsInvalidValue(t *testing.T) {
	mgr := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) { return nil, false },
	})
	raw, _ := json.Marshal(MemorySetConfidenceParams{SessionID: "sess_1", ID: 1, Confidence: "definitely-maybe"})
	_, err := mgr.MemorySetConfidence(context.Background(), raw)
	if err == nil {
		t.Fatal("MemorySetConfidence with invalid confidence: got nil error, want an error")
	}
}

func TestAgentsRosterResolvesConfiguredRoles(t *testing.T) {
	state := &session.State{}
	cfg := config.Default()
	state.Config = cfg

	mgr := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) {
			return &MemoryRuntime{State: state}, true
		},
	})
	raw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	res, err := mgr.AgentsRoster(context.Background(), raw)
	if err != nil {
		t.Fatalf("AgentsRoster: %v", err)
	}
	result, ok := res.(AgentsRosterResult)
	if !ok {
		t.Fatalf("AgentsRoster result type = %T, want AgentsRosterResult", res)
	}
	if len(result.Roles) != len(routing.AllRoles) {
		t.Fatalf("len(Roles) = %d, want %d (one per routing.AllRoles entry)", len(result.Roles), len(routing.AllRoles))
	}
	if result.SwarmBudget.MaxTotalTokens != cfg.Swarm.Budget.MaxTotalTokens {
		t.Errorf("SwarmBudget.MaxTotalTokens = %d, want %d", result.SwarmBudget.MaxTotalTokens, cfg.Swarm.Budget.MaxTotalTokens)
	}
	if result.SDDBudget.MaxTotalTokens != cfg.SDD.MaxTotalTokens {
		t.Errorf("SDDBudget.MaxTotalTokens = %d, want %d", result.SDDBudget.MaxTotalTokens, cfg.SDD.MaxTotalTokens)
	}
}

func TestAgentsRosterRequiresSessionID(t *testing.T) {
	mgr := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) { return nil, false },
	})
	_, err := mgr.AgentsRoster(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("AgentsRoster with no sessionId: got nil error, want an error")
	}
}
