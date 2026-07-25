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
