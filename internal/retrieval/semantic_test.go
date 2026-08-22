package retrieval

import (
	"context"
	"testing"

	"marshal/internal/db"
)

type fakeEmbedder struct{}

func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	return [][]float32{{1, 0}}, nil // query vector
}
func (fakeEmbedder) Model() string { return "m" }
func (fakeEmbedder) Dims() int     { return 2 }

func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return database
}

func mustCreateProject(t *testing.T, database *db.DB, path string) int64 {
	t.Helper()
	pid, err := database.GetOrCreateProject(path, "test")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	return pid
}

func TestSemanticRetrieveOrdersByCosine(t *testing.T) {
	database := newTestDB(t)
	pid := mustCreateProject(t, database, "/tmp/p")
	// near = aligned with query {1,0}; far = orthogonal.
	_ = database.ReplaceFileChunks(pid, "far.go", "h", []db.ChunkWithVector{{
		Chunk: db.Chunk{FilePath: "far.go", FileHash: "h", Kind: "code", StartLine: 1, EndLine: 1, Content: "far", TokenCount: 1}, Model: "m", Dim: 2, Vector: []float32{0, 1}}})
	_ = database.ReplaceFileChunks(pid, "near.go", "h", []db.ChunkWithVector{{
		Chunk: db.Chunk{FilePath: "near.go", FileHash: "h", Kind: "code", StartLine: 1, EndLine: 1, Content: "near", TokenCount: 1}, Model: "m", Dim: 2, Vector: []float32{1, 0}}})

	src := NewSemanticSource(database, fakeEmbedder{}, pid)
	got, err := src.Retrieve(context.Background(), Query{Text: "q", Limit: 1})
	if err != nil || len(got) != 1 || got[0].FilePath != "near.go" {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	if got[0].SourceName != "semantic" {
		t.Fatalf("source name = %q", got[0].SourceName)
	}
}

func TestVectorsFullReloadOnModelChange(t *testing.T) {
	database := newTestDB(t)
	pid := mustCreateProject(t, database, "/tmp/p")

	_ = database.ReplaceFileChunks(pid, "a.go", "h", []db.ChunkWithVector{{
		Chunk: db.Chunk{FilePath: "a.go", FileHash: "h", Kind: "code", StartLine: 1, EndLine: 1, Content: "a", TokenCount: 1},
		Model: "modelA", Dim: 2, Vector: []float32{1, 0},
	}})

	// Use modelA embedder — first load populates cache.
	srcA := NewSemanticSource(database, modelEmbedder{"modelA"}, pid)
	rows, err := srcA.vectors()
	if err != nil || len(rows) != 1 || rows[0].FilePath != "a.go" {
		t.Fatalf("first vectors() = %#v err=%v", rows, err)
	}

	// Switch to modelB embedder — should trigger full reload, returning 0 rows
	// because no chunks have modelB embeddings.
	srcB := NewSemanticSource(database, modelEmbedder{"modelB"}, pid)
	rows, err = srcB.vectors()
	if err != nil {
		t.Fatalf("modelB vectors() err: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("modelB vectors() = %d rows, want 0 (no modelB embeddings)", len(rows))
	}
}

type modelEmbedder struct{ name string }

func (m modelEmbedder) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return [][]float32{{1, 0}}, nil
}
func (m modelEmbedder) Model() string { return m.name }
func (m modelEmbedder) Dims() int     { return 2 }

func TestVectorsNoChangesReturnsCache(t *testing.T) {
	database := newTestDB(t)
	pid := mustCreateProject(t, database, "/tmp/p")

	_ = database.ReplaceFileChunks(pid, "a.go", "h", []db.ChunkWithVector{{
		Chunk: db.Chunk{FilePath: "a.go", FileHash: "h", Kind: "code", StartLine: 1, EndLine: 1, Content: "a", TokenCount: 1},
		Model: "m", Dim: 2, Vector: []float32{1, 0},
	}})

	src := NewSemanticSource(database, fakeEmbedder{}, pid)

	// First load.
	rows1, err := src.vectors()
	if err != nil || len(rows1) != 1 {
		t.Fatalf("first vectors() = %#v err=%v", rows1, err)
	}

	// Second load with no changes — should return the same slice (cache hit).
	rows2, err := src.vectors()
	if err != nil {
		t.Fatalf("second vectors() err: %v", err)
	}
	if len(rows2) != 1 || rows2[0].FilePath != "a.go" {
		t.Fatalf("second vectors() = %#v, want same cached a.go", rows2)
	}
	// Verify it's the same cached slice (pointer identity proves no reload).
	if &rows1[0] != &rows2[0] {
		t.Fatalf("second vectors() returned a different slice — cache was not reused")
	}
}
