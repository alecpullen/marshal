package db

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func openMemDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	return d
}

func twoProjects(t *testing.T, d *DB) (int64, int64) {
	t.Helper()
	a, err := d.GetOrCreateProject("/a", "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.GetOrCreateProject("/b", "b")
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestMemoryScopesMigrationDefaultsExistingRows(t *testing.T) {
	d := openMemDB(t)
	p, _ := d.GetOrCreateProject("/a", "a")
	// Simulate a pre-migration row: insert, then reset the new columns the
	// way an old row would read after ALTER TABLE ... DEFAULT.
	if err := d.SaveMemory(p, "fact", "old note", "", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	ms, err := d.GetMemories(p)
	if err != nil || len(ms) != 1 {
		t.Fatalf("memories = %v, err %v", ms, err)
	}
	m := ms[0]
	if m.Scope != "project" || m.ScopeKey != "" || m.OwnerID != "local" || m.LearnedAgent != "" || m.LearnedStep != 0 || len(m.ConfirmedBy) != 0 {
		t.Fatalf("defaults wrong: %+v", m)
	}
	if m.ProjectID != p {
		t.Fatalf("ProjectID = %d, want %d", m.ProjectID, p)
	}
}

func TestMigrateMemoryScopesUpgradesOldSchema(t *testing.T) {
	d := openMemDB(t)
	// Rebuild the v2-era table without the v3 columns and re-run the migration.
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_memories_scope`,
		`ALTER TABLE memories DROP COLUMN scope`,
		`ALTER TABLE memories DROP COLUMN scope_key`,
		`ALTER TABLE memories DROP COLUMN owner_id`,
		`ALTER TABLE memories DROP COLUMN learned_agent`,
		`ALTER TABLE memories DROP COLUMN learned_step`,
		`ALTER TABLE memories DROP COLUMN confirmed_by`,
	} {
		if _, err := d.sqlDB.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	p, _ := d.GetOrCreateProject("/a", "a")
	now := time.Unix(5, 0).UTC().Format(time.RFC3339)
	if _, err := d.sqlDB.Exec(`INSERT INTO memories (project_id, kind, content, content_hash, confidence, created_at, updated_at) VALUES (?, 'fact', 'legacy', ?, 'tentative', ?, ?)`, p, MemoryContentHash("legacy"), now, now); err != nil {
		t.Fatal(err)
	}
	tx, _ := d.sqlDB.Begin()
	if err := migrateMemoryScopes(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ms, err := d.GetMemories(p)
	if err != nil || len(ms) != 1 {
		t.Fatalf("memories = %v, err %v", ms, err)
	}
	if ms[0].Scope != "project" || ms[0].OwnerID != "local" || len(ms[0].ConfirmedBy) != 0 {
		t.Fatalf("legacy row = %+v", ms[0])
	}
}

func TestSaveMemoryWithProvenance(t *testing.T) {
	d := openMemDB(t)
	p, _ := d.GetOrCreateProject("/a", "a")
	now := time.Unix(10, 0).UTC()
	if err := d.SaveMemoryWith(p, MemoryInput{Kind: "fact", Content: "uses go", LearnedAgent: "reviewer", LearnedStep: 7, Now: now}); err != nil {
		t.Fatal(err)
	}
	// A refresh by another agent keeps the original provenance.
	if err := d.SaveMemoryWith(p, MemoryInput{Kind: "fact", Content: "Uses  Go", LearnedAgent: "other", LearnedStep: 9, Now: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	ms, _ := d.GetMemories(p)
	if len(ms) != 1 || ms[0].LearnedAgent != "reviewer" || ms[0].LearnedStep != 7 || ms[0].Scope != "project" {
		t.Fatalf("memories = %+v", ms)
	}
}

func TestGetScopedMemoriesOrder(t *testing.T) {
	d := openMemDB(t)
	a, b := twoProjects(t, d)
	now := time.Unix(10, 0).UTC()
	save := func(p int64, content string) int64 {
		if err := d.SaveMemory(p, "fact", content, "", now); err != nil {
			t.Fatal(err)
		}
		ms, _ := d.GetMemories(p)
		return ms[len(ms)-1].ID
	}
	g := save(b, "global note")
	w := save(b, "workspace note")
	other := save(b, "other workspace note")
	pr := save(a, "project note")
	save(b, "b private note")
	for _, step := range []struct {
		id       int64
		scope    string
		key      string
		wantName string
	}{{g, "global", "", ""}, {w, "workspace", "ws1", ""}, {other, "workspace", "ws2", ""}} {
		if err := d.PromoteMemory(step.id, step.scope, step.key, now); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.GetScopedMemories(a, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	var contents []string
	for _, m := range got {
		contents = append(contents, m.Content)
	}
	want := []string{"project note", "workspace note", "global note"}
	if !reflect.DeepEqual(contents, want) {
		t.Fatalf("contents = %v, want %v", contents, want)
	}
	if got[0].ID != pr {
		t.Fatalf("first id = %d, want %d", got[0].ID, pr)
	}
	noWS, _ := d.GetScopedMemories(a, "")
	if len(noWS) != 2 {
		t.Fatalf("no-workspace rows = %d, want 2 (project + global)", len(noWS))
	}
}

func TestMemorySuggestionsAcrossProjects(t *testing.T) {
	d := openMemDB(t)
	a, b := twoProjects(t, d)
	now := time.Unix(10, 0).UTC()
	d.SaveMemory(a, "fact", "shared fact", "", now)
	d.SaveMemory(b, "fact", "Shared  Fact", "", now)
	d.SaveMemory(a, "fact", "only in a", "", now)
	got, err := d.MemorySuggestions(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MatchProjectID != b || got[0].MatchRoot != "/b" || got[0].SuggestedScope != "global" {
		t.Fatalf("suggestions = %+v", got)
	}
	none, _ := d.MemorySuggestions(b + 100)
	if len(none) != 0 {
		t.Fatalf("suggestions for unknown project = %+v", none)
	}
}

func TestPromoteMemoryMergesDuplicates(t *testing.T) {
	d := openMemDB(t)
	a, b := twoProjects(t, d)
	now := time.Unix(10, 0).UTC()
	d.SaveMemoryWith(a, MemoryInput{Kind: "fact", Content: "shared fact", LearnedAgent: "alice", Now: now})
	d.SaveMemoryWith(b, MemoryInput{Kind: "fact", Content: "shared fact", LearnedAgent: "bob", Now: now})
	ms, _ := d.GetMemories(a)
	if err := d.PromoteMemory(ms[0].ID, "global", "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ := d.GetMemory(ms[0].ID)
	if got.Scope != "global" || !reflect.DeepEqual(got.ConfirmedBy, []string{"bob"}) {
		t.Fatalf("promoted = %+v", got)
	}
	bs, _ := d.GetMemories(b)
	if len(bs) != 0 {
		t.Fatalf("duplicate in b not deleted: %+v", bs)
	}
	if err := d.PromoteMemory(ms[0].ID, "workspace", "", now); err == nil {
		t.Fatal("workspace without key must fail")
	}
	if err := d.PromoteMemory(ms[0].ID, "galaxy", "", now); err == nil {
		t.Fatal("invalid scope must fail")
	}
}

func TestConfirmMemoryDeduplicatesAgents(t *testing.T) {
	d := openMemDB(t)
	p, _ := d.GetOrCreateProject("/a", "a")
	d.SaveMemory(p, "fact", "note", "", time.Unix(1, 0))
	ms, _ := d.GetMemories(p)
	for _, agent := range []string{"x", "y", "x", ""} {
		if err := d.ConfirmMemory(ms[0].ID, agent); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := d.GetMemory(ms[0].ID)
	if !reflect.DeepEqual(got.ConfirmedBy, []string{"x", "y"}) {
		t.Fatalf("ConfirmedBy = %v", got.ConfirmedBy)
	}
}

func TestProjectRoot(t *testing.T) {
	d := openMemDB(t)
	a, _ := twoProjects(t, d)
	if root, err := d.ProjectRoot(a); err != nil || root != "/a" {
		t.Fatalf("root = %q, err %v", root, err)
	}
}

func memoryByContent(t *testing.T, d *DB, project int64, content string) Memory {
	t.Helper()
	ms, err := d.GetMemories(project)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Content == content {
			return m
		}
	}
	t.Fatalf("no memory %q in project %d", content, project)
	return Memory{}
}

func TestPromoteToWorkspaceKeepsOtherProjectsCopies(t *testing.T) {
	d := openMemDB(t)
	a, b := twoProjects(t, d)
	now := time.Unix(10, 0).UTC()
	d.SaveMemoryWith(a, MemoryInput{Kind: "fact", Content: "shared", LearnedAgent: "alice", Now: now})
	d.SaveMemoryWith(b, MemoryInput{Kind: "fact", Content: "shared", LearnedAgent: "bob", Now: now})
	if err := d.PromoteMemory(memoryByContent(t, d, a, "shared").ID, "workspace", "ws1", now); err != nil {
		t.Fatal(err)
	}
	// b may not be in ws1: its project-scoped copy must survive.
	if got := memoryByContent(t, d, b, "shared"); got.Scope != "project" {
		t.Fatalf("b's copy = %+v", got)
	}
}

func TestPromoteToWorkspaceMergesSameWorkspaceTwin(t *testing.T) {
	d := openMemDB(t)
	a, b := twoProjects(t, d)
	now := time.Unix(10, 0).UTC()
	d.SaveMemoryWith(a, MemoryInput{Kind: "fact", Content: "shared", LearnedAgent: "alice", Now: now})
	d.SaveMemoryWith(b, MemoryInput{Kind: "fact", Content: "shared", LearnedAgent: "bob", Now: now})
	bID := memoryByContent(t, d, b, "shared").ID
	if err := d.PromoteMemory(bID, "workspace", "ws1", now); err != nil {
		t.Fatal(err)
	}
	// Same hash, different workspace: untouched when a's copy moves to ws2.
	aID := memoryByContent(t, d, a, "shared").ID
	if err := d.PromoteMemory(aID, "workspace", "ws2", now); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetMemory(bID); got.ScopeKey != "ws1" {
		t.Fatalf("ws1 twin damaged: %+v", got)
	}
	// Re-promote a's copy into ws1: the ws1 twin is covered and merged.
	if err := d.PromoteMemory(aID, "workspace", "ws1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetMemory(bID); err == nil {
		t.Fatal("same-workspace twin should have merged away")
	}
	if got, _ := d.GetMemory(aID); !reflect.DeepEqual(got.ConfirmedBy, []string{"bob"}) {
		t.Fatalf("ConfirmedBy = %v", got.ConfirmedBy)
	}
}

func TestPromoteToProjectDeletesNothing(t *testing.T) {
	d := openMemDB(t)
	a, b := twoProjects(t, d)
	now := time.Unix(10, 0).UTC()
	d.SaveMemoryWith(a, MemoryInput{Kind: "fact", Content: "shared", Now: now})
	d.SaveMemoryWith(b, MemoryInput{Kind: "fact", Content: "shared", Now: now})
	aID := memoryByContent(t, d, a, "shared").ID
	bID := memoryByContent(t, d, b, "shared").ID
	if err := d.PromoteMemory(bID, "global", "", now); err != nil {
		t.Fatal(err)
	}
	// Demote the global row back to its project: a's copy is gone (merged
	// into the global), but demotion itself must delete nothing further.
	d.SaveMemoryWith(a, MemoryInput{Kind: "fact", Content: "shared", Now: now})
	aID = memoryByContent(t, d, a, "shared").ID
	if err := d.PromoteMemory(bID, "project", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetMemory(aID); err != nil {
		t.Fatalf("demotion deleted a's copy: %v", err)
	}
	if got, _ := d.GetMemory(bID); got.Scope != "project" {
		t.Fatalf("demoted row = %+v", got)
	}
}

func TestPromoteToGlobalLeavesUnrelatedScopesAlone(t *testing.T) {
	d := openMemDB(t)
	a, b := twoProjects(t, d)
	now := time.Unix(10, 0).UTC()
	d.SaveMemoryWith(a, MemoryInput{Kind: "fact", Content: "keep", Now: now})
	d.SaveMemoryWith(b, MemoryInput{Kind: "fact", Content: "keep", Now: now})
	d.SaveMemoryWith(b, MemoryInput{Kind: "fact", Content: "other", Now: now})
	if err := d.PromoteMemory(memoryByContent(t, d, b, "other").ID, "global", "", now); err != nil {
		t.Fatal(err)
	}
	// A differently-hashed memory must never be touched.
	if got := memoryByContent(t, d, a, "keep"); got.Scope != "project" {
		t.Fatalf("a keep = %+v", got)
	}
	if got := memoryByContent(t, d, b, "keep"); got.Scope != "project" {
		t.Fatalf("b keep = %+v", got)
	}
}

func TestPruneKeepsPromotedRows(t *testing.T) {
	d := openMemDB(t)
	p, _ := d.GetOrCreateProject("/a", "a")
	now := time.Unix(10, 0).UTC()
	d.SaveMemory(p, "fact", "promoted oldest", "", now)
	pid := memoryByContent(t, d, p, "promoted oldest").ID
	if err := d.PromoteMemory(pid, "global", "", now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxMemoryRows+5; i++ {
		if err := d.SaveMemory(p, "fact", fmt.Sprintf("note %d", i), "", now); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := d.GetMemory(pid); err != nil || got.Scope != "global" {
		t.Fatalf("promoted row evicted: %+v, %v", got, err)
	}
	var n int
	d.sqlDB.QueryRow(`SELECT COUNT(*) FROM memories WHERE project_id = ? AND scope = 'project'`, p).Scan(&n)
	if n != maxMemoryRows {
		t.Fatalf("project rows = %d, want %d", n, maxMemoryRows)
	}
}

func TestSuggestionsCountGlobalTwin(t *testing.T) {
	d := openMemDB(t)
	a, b := twoProjects(t, d)
	now := time.Unix(10, 0).UTC()
	d.SaveMemoryWith(a, MemoryInput{Kind: "fact", Content: "shared", Now: now})
	d.SaveMemoryWith(b, MemoryInput{Kind: "fact", Content: "shared", Now: now})
	if err := d.PromoteMemory(memoryByContent(t, d, b, "shared").ID, "global", "", now); err != nil {
		t.Fatal(err)
	}
	// Promotion merged a's copy away, so recreate it as a fresh duplicate.
	d.SaveMemoryWith(a, MemoryInput{Kind: "fact", Content: "shared", Now: now})
	got, _ := d.MemorySuggestions(a)
	if len(got) != 1 || got[0].MatchProjectID != b {
		t.Fatalf("suggestions = %+v", got)
	}
}
