package doctorpanel

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/db"
)

func testIntelligenceDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	return database, projectID
}

func checkByName(checks []Check, name string) Check {
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	return Check{}
}

func TestComputeIntelligenceEmptyProject(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no LSP servers on PATH, deterministically
	database, projectID := testIntelligenceDB(t)
	checks := ComputeIntelligence(config.Config{}, database, projectID)
	if len(checks) != 4 {
		t.Fatalf("checks = %d, want 4 (Index, Embeddings, LSP, Watcher)", len(checks))
	}
	if c := checkByName(checks, "Index"); c.Status != "off" {
		t.Fatalf("Index = %+v, want off (never indexed)", c)
	}
	if c := checkByName(checks, "Embeddings"); c.Status != "off" {
		t.Fatalf("Embeddings = %+v, want off (not configured)", c)
	}
	if c := checkByName(checks, "LSP"); c.Status != "off" {
		t.Fatalf("LSP = %+v, want off (nothing on PATH)", c)
	}
	if c := checkByName(checks, "Watcher"); c.Status != "warn" {
		t.Fatalf("Watcher = %+v, want warn (off with no embeddings)", c)
	}
}

func TestComputeIntelligencePopulatedIndex(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	database, projectID := testIntelligenceDB(t)
	indexedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := database.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "a.go", Language: "go", Hash: "h1", LastIndexedAt: indexedAt},
		{Path: "b.go", Language: "go", Hash: "h2", LastIndexedAt: indexedAt},
	}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}
	checks := ComputeIntelligence(config.Config{}, database, projectID)
	c := checkByName(checks, "Index")
	if c.Status != "ok" || !strings.Contains(c.Detail, "2 files") {
		t.Fatalf("Index = %+v, want ok with file count", c)
	}
}

func TestComputeIntelligenceEmbeddings(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	database, projectID := testIntelligenceDB(t)
	cfg := config.Config{}
	cfg.Indexing.EmbeddingPreset = "ollama/nomic-embed-text"

	// Configured but nothing embedded yet: warn.
	checks := ComputeIntelligence(cfg, database, projectID)
	if c := checkByName(checks, "Embeddings"); c.Status != "warn" {
		t.Fatalf("Embeddings = %+v, want warn (configured, no embeddings)", c)
	}

	// One embedded chunk: ok.
	if err := database.ReplaceFileChunks(projectID, "a.go", "h1", []db.ChunkWithVector{
		{Chunk: db.Chunk{FilePath: "a.go", FileHash: "h1", Kind: "code", StartLine: 1, EndLine: 3, Content: "package x", TokenCount: 2}, Model: "test-embed", Dim: 4, Vector: []float32{1, 2, 3, 4}},
	}); err != nil {
		t.Fatalf("replace file chunks: %v", err)
	}
	checks = ComputeIntelligence(cfg, database, projectID)
	if c := checkByName(checks, "Embeddings"); c.Status != "ok" {
		t.Fatalf("Embeddings = %+v, want ok after an embedding landed", c)
	}

	// Embeddings alone do NOT enable the watcher (matches config.WatchEnabled:
	// an explicit watch value wins, otherwise the watcher stays off).
	if c := checkByName(checks, "Watcher"); c.Status != "warn" {
		t.Fatalf("Watcher = %+v, want warn (embeddings alone do not enable it)", c)
	}

	// An explicit watch value enables it.
	on := true
	cfg.Indexing.Watch = &on
	checks = ComputeIntelligence(cfg, database, projectID)
	if c := checkByName(checks, "Watcher"); c.Status != "ok" {
		t.Fatalf("Watcher = %+v, want ok (explicit watch=true)", c)
	}
}

func TestComputeIntelligenceLSPConfiguredButMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	database, projectID := testIntelligenceDB(t)
	cfg := config.Config{}
	cfg.LSP.Servers = map[string]config.LSPServerConfig{
		"go": {Command: "definitely-not-a-real-binary-xyz"},
	}
	checks := ComputeIntelligence(cfg, database, projectID)
	c := checkByName(checks, "LSP")
	if c.Status != "warn" || !strings.Contains(c.Detail, "not on PATH") {
		t.Fatalf("LSP = %+v, want warn naming the missing server", c)
	}
}

func TestComputeIntelligenceNilDB(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	checks := ComputeIntelligence(config.Config{}, nil, 0)
	if c := checkByName(checks, "Index"); c.Status != "off" || !strings.Contains(c.Detail, "unavailable") {
		t.Fatalf("Index = %+v, want off/database-unavailable", c)
	}
}
