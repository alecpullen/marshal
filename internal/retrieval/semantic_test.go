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
	if err := database.ReplaceFileChunks(pid, "far.go", "h", []db.ChunkWithVector{{
		Chunk: db.Chunk{FilePath: "far.go", FileHash: "h", Kind: "code", StartLine: 1, EndLine: 1, Content: "far", TokenCount: 1}, Model: "m", Dim: 2, Vector: []float32{0, 1}}}); err != nil {
		t.Fatalf("ReplaceFileChunks far.go: %v", err)
	}
	if err := database.ReplaceFileChunks(pid, "near.go", "h", []db.ChunkWithVector{{
		Chunk: db.Chunk{FilePath: "near.go", FileHash: "h", Kind: "code", StartLine: 1, EndLine: 1, Content: "near", TokenCount: 1}, Model: "m", Dim: 2, Vector: []float32{1, 0}}}); err != nil {
		t.Fatalf("ReplaceFileChunks near.go: %v", err)
	}

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

	if err := database.ReplaceFileChunks(pid, "a.go", "h", []db.ChunkWithVector{{
		Chunk: db.Chunk{FilePath: "a.go", FileHash: "h", Kind: "code", StartLine: 1, EndLine: 1, Content: "a", TokenCount: 1},
		Model: "modelA", Dim: 2, Vector: []float32{1, 0},
	}}); err != nil {
		t.Fatalf("ReplaceFileChunks: %v", err)
	}

	// Use modelA embedder — first load populates cache.
	src := NewSemanticSource(database, modelEmbedder{"modelA"}, pid)
	rows, err := src.vectors()
	if err != nil || len(rows) != 1 || rows[0].FilePath != "a.go" {
		t.Fatalf("first vectors() = %#v err=%v", rows, err)
	}

	// Mutate the same source to modelB — should trigger full reload, returning
	// 0 rows because no chunks have modelB embeddings.
	src.setEmbedder(modelEmbedder{"modelB"})
	rows, err = src.vectors()
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

	if err := database.ReplaceFileChunks(pid, "a.go", "h", []db.ChunkWithVector{{
		Chunk: db.Chunk{FilePath: "a.go", FileHash: "h", Kind: "code", StartLine: 1, EndLine: 1, Content: "a", TokenCount: 1},
		Model: "m", Dim: 2, Vector: []float32{1, 0},
	}}); err != nil {
		t.Fatalf("ReplaceFileChunks: %v", err)
	}

	src := NewSemanticSource(database, fakeEmbedder{}, pid)

	// First load.
	rows1, err := src.vectors()
	if err != nil || len(rows1) != 1 {
		t.Fatalf("first vectors() = %#v err=%v", rows1, err)
	}

	// Second load with no changes — should return equivalent data (cache hit).
	rows2, err := src.vectors()
	if err != nil {
		t.Fatalf("second vectors() err: %v", err)
	}
	if len(rows2) != 1 || rows2[0].FilePath != "a.go" {
		t.Fatalf("second vectors() = %#v, want same cached a.go", rows2)
	}

	// vectors() must return a defensive copy: mutating the returned slice
	// must not corrupt the internal cache.
	rows1[0].FilePath = "corrupted"
	rows3, err := src.vectors()
	if err != nil {
		t.Fatalf("third vectors() err: %v", err)
	}
	if len(rows3) != 1 || rows3[0].FilePath != "a.go" {
		t.Fatalf("caller mutation leaked into cache: %#v", rows3)
	}
}

func TestVectorsFileReplace(t *testing.T) {
	database := newTestDB(t)
	pid := mustCreateProject(t, database, "/tmp/p")

	// Insert a.go with content "a".
	if err := database.ReplaceFileChunks(pid, "a.go", "h1", []db.ChunkWithVector{{
		Chunk: db.Chunk{FilePath: "a.go", FileHash: "h1", Kind: "code", StartLine: 1, EndLine: 1, Content: "a", TokenCount: 1},
		Model: "m", Dim: 2, Vector: []float32{1, 0},
	}}); err != nil {
		t.Fatalf("ReplaceFileChunks: %v", err)
	}

	src := NewSemanticSource(database, fakeEmbedder{}, pid)

	// First load.
	rows, err := src.vectors()
	if err != nil || len(rows) != 1 || rows[0].Content != "a" {
		t.Fatalf("first vectors() = %#v err=%v", rows, err)
	}

	// Replace a.go with different content (gets new chunk IDs > old max).
	if err := database.ReplaceFileChunks(pid, "a.go", "h2", []db.ChunkWithVector{{
		Chunk: db.Chunk{FilePath: "a.go", FileHash: "h2", Kind: "code", StartLine: 1, EndLine: 1, Content: "a-replaced", TokenCount: 1},
		Model: "m", Dim: 2, Vector: []float32{1, 1},
	}}); err != nil {
		t.Fatalf("ReplaceFileChunks: %v", err)
	}

	// Second load — old a.go entry should be removed, new one present.
	rows, err = src.vectors()
	if err != nil {
		t.Fatalf("second vectors() err: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("second vectors() = %d rows, want 1", len(rows))
	}
	if rows[0].Content != "a-replaced" {
		t.Fatalf("second vectors() content = %q, want %q", rows[0].Content, "a-replaced")
	}
}

func TestVectorsDeletedChunks(t *testing.T) {
	database := newTestDB(t)
	pid := mustCreateProject(t, database, "/tmp/p")

	// Insert two files.
	if err := database.ReplaceFileChunks(pid, "a.go", "h", []db.ChunkWithVector{{
		Chunk: db.Chunk{FilePath: "a.go", FileHash: "h", Kind: "code", StartLine: 1, EndLine: 1, Content: "a", TokenCount: 1},
		Model: "m", Dim: 2, Vector: []float32{1, 0},
	}}); err != nil {
		t.Fatalf("ReplaceFileChunks a.go: %v", err)
	}
	if err := database.ReplaceFileChunks(pid, "b.go", "h", []db.ChunkWithVector{{
		Chunk: db.Chunk{FilePath: "b.go", FileHash: "h", Kind: "code", StartLine: 1, EndLine: 1, Content: "b", TokenCount: 1},
		Model: "m", Dim: 2, Vector: []float32{0, 1},
	}}); err != nil {
		t.Fatalf("ReplaceFileChunks b.go: %v", err)
	}

	src := NewSemanticSource(database, fakeEmbedder{}, pid)

	// First load — both files.
	rows, err := src.vectors()
	if err != nil || len(rows) != 2 {
		t.Fatalf("first vectors() = %#v err=%v", rows, err)
	}

	// Delete a.go's chunks (count decreases).
	if err := database.DeleteFileChunks(pid, "a.go"); err != nil {
		t.Fatalf("DeleteFileChunks: %v", err)
	}

	// Second load — a.go should be gone, b.go should remain.
	rows, err = src.vectors()
	if err != nil {
		t.Fatalf("second vectors() err: %v", err)
	}
	if len(rows) != 1 || rows[0].FilePath != "b.go" {
		t.Fatalf("second vectors() = %#v, want only b.go", rows)
	}
}
