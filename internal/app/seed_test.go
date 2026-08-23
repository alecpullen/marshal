package app

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/db"
)

func TestSeedRepoContextEmptyIndexIsNoOp(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	projectID, err := database.GetOrCreateProject(t.TempDir(), "seed-test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	cfg := config.Default()
	cfg.Project.Name = "seed-test"
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})

	seedRepoContext(state, database, projectID)
	if !state.ContextPack().IsEmpty() {
		t.Fatal("empty index must seed nothing")
	}
}

func TestSeedRepoContextSeedsCardAndMap(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	projectID, err := database.GetOrCreateProject(t.TempDir(), "seed-test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := database.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "main.go", Language: "go"},
		{Path: "internal/x/x.go", Language: "go"},
	}); err != nil {
		t.Fatalf("save file index: %v", err)
	}
	if err := database.SaveSymbols(projectID, []db.Symbol{
		{FilePath: "main.go", Kind: "function", Name: "Main"},
	}); err != nil {
		t.Fatalf("save symbols: %v", err)
	}

	cfg := config.Default()
	cfg.Project.Name = "seed-test"
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})

	seedRepoContext(state, database, projectID)
	pack := state.ContextPack()
	if len(pack.Sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(pack.Sections))
	}
	if pack.Sections[0].Kind != contextpack.SectionRepoCard {
		t.Fatalf("first section = %s, want repo_card", pack.Sections[0].Kind)
	}
	if pack.Sections[1].Kind != contextpack.SectionRepoMap {
		t.Fatalf("second section = %s, want repo_map", pack.Sections[1].Kind)
	}

	// Re-seeding replaces, never duplicates.
	seedRepoContext(state, database, projectID)
	if got := len(state.ContextPack().Sections); got != 2 {
		t.Fatalf("after re-seed sections = %d, want 2", got)
	}
}

func TestSeedRepoContextNilArgs(t *testing.T) {
	seedRepoContext(nil, nil, 0) // must not panic
}

func TestSeedSessionSummaries(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	projectID, err := database.GetOrCreateProject(t.TempDir(), "seed-test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	t0 := time.Unix(100, 0)
	if err := database.CreateSession("prior-1", projectID, "Parser work", t0); err != nil {
		t.Fatalf("create prior session: %v", err)
	}
	if err := database.EndSession("prior-1", t0.Add(time.Hour), "Built the expression parser."); err != nil {
		t.Fatalf("end prior session: %v", err)
	}

	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), t0.Add(2*time.Hour), session.Persistence{})

	seedSessionSummaries(state, database, projectID)
	pack := state.ContextPack()
	found := false
	for _, s := range pack.Sections {
		if s.Kind == contextpack.SectionSessionSummaries {
			found = true
			if !strings.Contains(s.Content, "Parser work") || !strings.Contains(s.Content, "Built the expression parser.") {
				t.Fatalf("section content = %q", s.Content)
			}
		}
	}
	if !found {
		t.Fatal("no session_summaries section seeded")
	}

	// Re-seeding replaces, never duplicates (same contract as repo sections).
	seedSessionSummaries(state, database, projectID)
	n := 0
	for _, s := range state.ContextPack().Sections {
		if s.Kind == contextpack.SectionSessionSummaries {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("session_summaries sections = %d, want 1", n)
	}
}

func TestSeedSessionSummariesNoHistory(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	projectID, err := database.GetOrCreateProject(t.TempDir(), "seed-test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})

	seedSessionSummaries(state, database, projectID)
	if !state.ContextPack().IsEmpty() {
		t.Fatal("no history must seed nothing")
	}
}

func TestSeedSessionSummariesNilArgs(t *testing.T) {
	seedSessionSummaries(nil, nil, 0) // must not panic
}
