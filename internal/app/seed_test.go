package app

import (
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
