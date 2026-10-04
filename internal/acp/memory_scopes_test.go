package acp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"marshal/internal/db"
)

// scopeFixture holds two projects in one DB with a memory manager serving the
// first as the current session.
type scopeFixture struct {
	d       *db.DB
	mine    int64
	other   int64
	mgr     *MemoryManager
	otherID int64 // memory id in the other project
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	d, mine := newMemoryTestDB(t)
	other, err := d.GetOrCreateProject("/other", "other")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	if err := d.SaveMemoryWith(other, db.MemoryInput{Kind: "fact", Content: "shared fact", LearnedAgent: "bob", LearnedStep: 3, Now: now}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveMemory(mine, "fact", "shared fact", "sess_a", now); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveMemory(mine, "fact", "mine only", "sess_a", now); err != nil {
		t.Fatal(err)
	}
	rows, _ := d.GetMemories(other)
	return &scopeFixture{
		d: d, mine: mine, other: other, otherID: rows[0].ID,
		mgr: NewMemoryManager(MemoryManagerConfig{Lookup: func(string) (*MemoryRuntime, bool) {
			return &MemoryRuntime{DB: d, ProjectID: mine}, true
		}}),
	}
}

func call(t *testing.T, fn func(context.Context, json.RawMessage) (any, error), v map[string]any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(v)
	return fn(context.Background(), raw)
}

func TestMemoryListShowsGlobalFromOtherProject(t *testing.T) {
	f := newScopeFixture(t)
	if err := f.d.PromoteMemory(f.otherID, "global", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err := call(t, f.mgr.MemoryList, map[string]any{"sessionId": "s"})
	if err != nil {
		t.Fatal(err)
	}
	entries := res.(MemoryListResult).Entries
	// The promotion merged the duplicate in "mine", leaving: mine only, global.
	if len(entries) != 2 || entries[0].Content != "mine only" || entries[1].Scope != "global" {
		t.Fatalf("entries = %+v", entries)
	}
	g := entries[1]
	if g.LearnedProjectRoot != "/other" || g.LearnedAgent != "bob" || g.LearnedStep != 3 || g.OwnerID != "local" ||
		len(g.ConfirmedBy) != 0 {
		t.Fatalf("global entry = %+v", g)
	}
	only, err := call(t, f.mgr.MemoryList, map[string]any{"sessionId": "s", "scope": "global"})
	if err != nil {
		t.Fatal(err)
	}
	if got := only.(MemoryListResult).Entries; len(got) != 1 || got[0].Scope != "global" {
		t.Fatalf("scope filter = %+v", got)
	}
}

func TestMemoryListRejectsInvalidScopeFilter(t *testing.T) {
	f := newScopeFixture(t)
	if _, err := call(t, f.mgr.MemoryList, map[string]any{"sessionId": "s", "scope": "galaxy"}); err == nil {
		t.Fatal("want error")
	}
}

func TestMemorySuggestionsOverACP(t *testing.T) {
	f := newScopeFixture(t)
	res, err := call(t, f.mgr.MemorySuggestions, map[string]any{"sessionId": "s"})
	if err != nil {
		t.Fatal(err)
	}
	got := res.(map[string]any)["suggestions"].([]MemorySuggestionEntry)
	if len(got) != 1 || got[0].MatchProjectRoot != "/other" || got[0].SuggestedScope != "global" {
		t.Fatalf("suggestions = %+v", got)
	}
}

func TestMemoryPromoteOverACP(t *testing.T) {
	f := newScopeFixture(t)
	mine, _ := f.d.GetMemories(f.mine)
	if _, err := call(t, f.mgr.MemoryPromote, map[string]any{"sessionId": "s", "id": mine[0].ID, "scope": "workspace", "scopeKey": "ws1"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.d.GetMemory(mine[0].ID)
	if got.Scope != "workspace" || got.ScopeKey != "ws1" || !reflect.DeepEqual(got.ConfirmedBy, []string{"bob"}) {
		t.Fatalf("promoted = %+v", got)
	}
	if rows, _ := f.d.GetMemories(f.other); len(rows) != 0 {
		t.Fatalf("duplicate survived: %+v", rows)
	}
}

func TestMemoryPromoteRejectsBadInput(t *testing.T) {
	f := newScopeFixture(t)
	for name, p := range map[string]map[string]any{
		"invalid scope":         {"sessionId": "s", "id": 1, "scope": "galaxy"},
		"workspace without key": {"sessionId": "s", "id": 1, "scope": "workspace"},
		"missing id":            {"sessionId": "s", "scope": "global"},
		"missing session":       {"id": 1, "scope": "global"},
	} {
		if _, err := call(t, f.mgr.MemoryPromote, p); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestMemoryConfirmOverACP(t *testing.T) {
	f := newScopeFixture(t)
	mine, _ := f.d.GetMemories(f.mine)
	for i := 0; i < 2; i++ {
		if _, err := call(t, f.mgr.MemoryConfirm, map[string]any{"sessionId": "s", "id": mine[0].ID, "agent": "carol"}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := f.d.GetMemory(mine[0].ID)
	if !reflect.DeepEqual(got.ConfirmedBy, []string{"carol"}) {
		t.Fatalf("ConfirmedBy = %v", got.ConfirmedBy)
	}
	if _, err := call(t, f.mgr.MemoryConfirm, map[string]any{"sessionId": "s", "id": mine[0].ID}); err == nil {
		t.Fatal("missing agent must fail")
	}
}
