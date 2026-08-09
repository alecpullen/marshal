package index

import (
	"context"
	"testing"

	"marshal/internal/db"
	"marshal/internal/repo"
)

type fakeEmbedder struct {
	model string
	calls int
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(len(texts[i])), 1}
	}
	return out, nil
}
func (f *fakeEmbedder) Model() string { return f.model }
func (f *fakeEmbedder) Dims() int     { return 2 }

func scanned(path, hash, content string) repo.ScannedFile {
	return repo.ScannedFile{FileIndex: db.FileIndex{Path: path, Language: "go", Hash: hash}, Content: []byte(content)}
}
func syms(path string) map[string][]db.Symbol {
	return map[string][]db.Symbol{path: {{FilePath: path, Kind: "function", Name: "F", Signature: "func F()", LineStart: 1, LineEnd: 1}}}
}

func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	return database
}

func mustCreateProject(t *testing.T, database *db.DB, path string) int64 {
	t.Helper()
	id, err := database.GetOrCreateProject(path, path)
	if err != nil {
		t.Fatalf("GetOrCreateProject(%q): %v", path, err)
	}
	return id
}

func TestReindexIncremental(t *testing.T) {
	database := newTestDB(t)
	pid := mustCreateProject(t, database, "/tmp/p")
	e := &fakeEmbedder{model: "m"}
	ix := NewIndexer(database, e)

	files := []repo.ScannedFile{scanned("a.go", "h1", "func F(){}")}
	st, err := ix.Reindex(context.Background(), pid, files, syms("a.go"))
	if err != nil || st.FilesEmbedded != 1 {
		t.Fatalf("first: %+v err=%v", st, err)
	}

	// Unchanged hash → skipped, no new embed call.
	before := e.calls
	st, _ = ix.Reindex(context.Background(), pid, files, syms("a.go"))
	if st.FilesSkipped != 1 || e.calls != before {
		t.Fatalf("skip: st=%+v calls delta=%d", st, e.calls-before)
	}

	// Changed hash → re-embed.
	files2 := []repo.ScannedFile{scanned("a.go", "h2", "func F(){ return }")}
	st, _ = ix.Reindex(context.Background(), pid, files2, syms("a.go"))
	if st.FilesEmbedded != 1 {
		t.Fatalf("change: %+v", st)
	}

	// Removed file → purged.
	st, _ = ix.Reindex(context.Background(), pid, nil, nil)
	if st.FilesPurged != 1 {
		t.Fatalf("purge: %+v", st)
	}
}

type batchingEmbedder struct {
	model string
	calls int
	sizes []int
}

func (b *batchingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	b.calls++
	b.sizes = append(b.sizes, len(texts))
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(len(texts[i])), 1}
	}
	return out, nil
}
func (b *batchingEmbedder) Model() string { return b.model }
func (b *batchingEmbedder) Dims() int     { return 2 }

func symsFor(paths ...string) map[string][]db.Symbol {
	m := map[string][]db.Symbol{}
	for _, p := range paths {
		m[p] = []db.Symbol{{
			FilePath:  p,
			Kind:      "function",
			Name:      "F",
			Signature: "func F()",
			LineStart: 1,
			LineEnd:   1,
		}}
	}
	return m
}

func TestReindexBatchesAcrossFiles(t *testing.T) {
	database := newTestDB(t)
	pid := mustCreateProject(t, database, "/tmp/p")
	e := &batchingEmbedder{model: "m"}
	ix := NewIndexer(database, e)

	files := []repo.ScannedFile{
		scanned("a.go", "h1", "func A(){}"),
		scanned("b.go", "h2", "func B(){}"),
		scanned("c.go", "h3", "func C(){}"),
	}
	st, err := ix.Reindex(context.Background(), pid, files, symsFor("a.go", "b.go", "c.go"))
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if st.FilesEmbedded != 3 {
		t.Fatalf("FilesEmbedded = %d, want 3", st.FilesEmbedded)
	}
	if e.calls != 1 {
		t.Fatalf("expected one batched embed call, got %d", e.calls)
	}
	if len(e.sizes) != 1 || e.sizes[0] != 3 {
		t.Fatalf("expected one batch of 3 inputs, got sizes %v", e.sizes)
	}
}

func TestReindexEmitsProgress(t *testing.T) {
	database := newTestDB(t)
	pid := mustCreateProject(t, database, "/tmp/p")
	e := &fakeEmbedder{model: "m"}
	ix := NewIndexer(database, e)

	var msgs []string
	ix.onProgress = func(msg string) { msgs = append(msgs, msg) }

	files := []repo.ScannedFile{scanned("a.go", "h", "func F(){}")}
	_, err := ix.Reindex(context.Background(), pid, files, syms("a.go"))
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected progress messages")
	}
}

func TestReindexNilEmbedderIsNoop(t *testing.T) {
	database := newTestDB(t)
	pid := mustCreateProject(t, database, "/tmp/p")
	ix := NewIndexer(database, nil)
	st, err := ix.Reindex(context.Background(), pid, []repo.ScannedFile{scanned("a.go", "h", "x")}, syms("a.go"))
	if err != nil || (st != Stats{}) {
		t.Fatalf("nil embedder should no-op: %+v err=%v", st, err)
	}
}
