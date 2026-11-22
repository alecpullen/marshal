package index

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// failAfterEmbedder succeeds for the first call then fails, to exercise the
// incremental-persistence path where a later batch failure must not discard
// files already embedded in earlier batches.
type failAfterEmbedder struct {
	model string
	calls int
}

func (f *failAfterEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.calls > 1 {
		return nil, fmt.Errorf("boom")
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(len(texts[i])), 1}
	}
	return out, nil
}
func (f *failAfterEmbedder) Model() string { return f.model }
func (f *failAfterEmbedder) Dims() int     { return 2 }

func TestReindexPersistsEarlierBatchesOnLaterFailure(t *testing.T) {
	database := newTestDB(t)
	pid := mustCreateProject(t, database, "/tmp/p")
	e := &failAfterEmbedder{model: "m"}
	ix := NewIndexer(database, e)

	// a.go fills batch 1 exactly (64 chunks via the 50-line window step);
	// b.go starts batch 2, which fails. a.go must already be persisted.
	line := "func A(){}"
	files := []repo.ScannedFile{
		scanned("a.go", "h1", strings.Repeat(line+"\n", 64*50)),
		scanned("b.go", "h2", strings.Repeat(line+"\n", 60)),
	}
	_, err := ix.Reindex(context.Background(), pid, files, nil)
	if err == nil {
		t.Fatal("expected embed failure on second batch")
	}

	// The first file must already be persisted despite the overall failure.
	state, err := database.ChunkedFiles(pid)
	if err != nil {
		t.Fatalf("ChunkedFiles: %v", err)
	}
	if _, ok := state["a.go"]; !ok {
		t.Fatal("a.go was not persisted before the later batch failed")
	}
	if _, ok := state["b.go"]; ok {
		t.Fatal("b.go should not be persisted (its batch failed)")
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

// unreadable is a path that is present in the scan but whose content could
// not be read (a permission race, a transient I/O error).
func unreadable(path, hash string) repo.ScannedFile {
	return repo.ScannedFile{
		FileIndex: db.FileIndex{Path: path, Language: "go", Hash: hash},
		ReadErr:   errors.New("permission denied"),
	}
}

// omitted is a path whose content the scan chose not to retain: the total
// content budget was exhausted, but the file itself is present and indexed.
func omitted(path, hash string) repo.ScannedFile {
	return repo.ScannedFile{
		FileIndex:      db.FileIndex{Path: path, Language: "go", Hash: hash, SizeBytes: 16},
		ContentOmitted: true,
	}
}

// TestReindexTransientReadErrorKeepsExistingChunks is the AUD-11 completion
// condition: a file that is still on disk but unreadable during a reindex
// keeps its previously valid chunks, because only paths absent from the scan
// entirely are purged.
func TestReindexTransientReadErrorKeepsExistingChunks(t *testing.T) {
	database := newTestDB(t)
	defer database.Close()
	pid := mustCreateProject(t, database, "/tmp/p")
	ix := NewIndexer(database, &fakeEmbedder{model: "m"})
	ctx := context.Background()

	st, err := ix.Reindex(ctx, pid, []repo.ScannedFile{scanned("a.go", "h1", "func F(){}")}, syms("a.go"))
	if err != nil {
		t.Fatalf("initial Reindex: %v", err)
	}
	if st.FilesEmbedded != 1 {
		t.Fatalf("initial FilesEmbedded = %d, want 1", st.FilesEmbedded)
	}
	before, err := database.ChunkedFiles(pid)
	if err != nil {
		t.Fatalf("ChunkedFiles: %v", err)
	}
	if _, ok := before["a.go"]; !ok {
		t.Fatalf("a.go missing from ChunkedFiles after the initial index: %#v", before)
	}

	// Same path, same hash, content now unreadable: a transient failure,
	// not a deletion.
	st, err = ix.Reindex(ctx, pid, []repo.ScannedFile{unreadable("a.go", "h1")}, syms("a.go"))
	if err != nil {
		t.Fatalf("Reindex after read error: %v", err)
	}
	if st.FilesPurged != 0 {
		t.Fatalf("FilesPurged = %d after a transient read error, want 0", st.FilesPurged)
	}
	after, err := database.ChunkedFiles(pid)
	if err != nil {
		t.Fatalf("ChunkedFiles: %v", err)
	}
	if _, ok := after["a.go"]; !ok {
		t.Fatalf("a.go was purged by a transient read error: %#v", after)
	}
}

// TestReindexContentOmittedKeepsExistingChunks covers the other half of the
// same rule: a file the scan indexed without retaining its bytes is present,
// not empty, so its existing chunks must survive the pass.
func TestReindexContentOmittedKeepsExistingChunks(t *testing.T) {
	database := newTestDB(t)
	defer database.Close()
	pid := mustCreateProject(t, database, "/tmp/p")
	ix := NewIndexer(database, &fakeEmbedder{model: "m"})
	ctx := context.Background()

	first := []repo.ScannedFile{scanned("a.go", "h1", "func F(){}")}
	if _, err := ix.Reindex(ctx, pid, first, syms("a.go")); err != nil {
		t.Fatalf("initial Reindex: %v", err)
	}
	state, err := database.ChunkedFiles(pid)
	if err != nil {
		t.Fatalf("ChunkedFiles: %v", err)
	}
	before, ok := state["a.go"]
	if !ok {
		t.Fatal("a.go has no chunks after the first pass")
	}

	second := []repo.ScannedFile{omitted("a.go", before.FileHash)}
	stats, err := ix.Reindex(ctx, pid, second, nil)
	if err != nil {
		t.Fatalf("Reindex with omitted content: %v", err)
	}
	if stats.FilesPurged != 0 {
		t.Errorf("purged %d files, want 0", stats.FilesPurged)
	}
	after, err := database.ChunkedFiles(pid)
	if err != nil {
		t.Fatalf("ChunkedFiles: %v", err)
	}
	got, ok := after["a.go"]
	if !ok {
		t.Fatal("a.go lost its chunks: an omitted file must keep them")
	}
	if got.FileHash != before.FileHash {
		t.Errorf("hash = %q, want %q", got.FileHash, before.FileHash)
	}
}

// TestReindexDeletedFileIsPurged is the counterweight to the AUD-11 fix: a
// path absent from the scan entirely is still purged.
func TestReindexDeletedFileIsPurged(t *testing.T) {
	database := newTestDB(t)
	defer database.Close()
	pid := mustCreateProject(t, database, "/tmp/p")
	ix := NewIndexer(database, &fakeEmbedder{model: "m"})
	ctx := context.Background()

	if _, err := ix.Reindex(ctx, pid, []repo.ScannedFile{scanned("a.go", "h1", "func F(){}")}, syms("a.go")); err != nil {
		t.Fatalf("initial Reindex: %v", err)
	}

	st, err := ix.Reindex(ctx, pid, nil, nil)
	if err != nil {
		t.Fatalf("Reindex with an empty scan: %v", err)
	}
	if st.FilesPurged != 1 {
		t.Fatalf("FilesPurged = %d for a deleted file, want 1", st.FilesPurged)
	}
	after, err := database.ChunkedFiles(pid)
	if err != nil {
		t.Fatalf("ChunkedFiles: %v", err)
	}
	if _, ok := after["a.go"]; ok {
		t.Fatalf("a.go still indexed after it was deleted: %#v", after)
	}
}

// TestReindexAfterTransientReadErrorRecoversFile covers the recovery path:
// once the file is readable again at a new hash it is re-embedded, not left
// holding stale chunks from before the read error.
func TestReindexAfterTransientReadErrorRecoversFile(t *testing.T) {
	database := newTestDB(t)
	defer database.Close()
	pid := mustCreateProject(t, database, "/tmp/p")
	ix := NewIndexer(database, &fakeEmbedder{model: "m"})
	ctx := context.Background()

	if _, err := ix.Reindex(ctx, pid, []repo.ScannedFile{scanned("a.go", "h1", "func F(){}")}, syms("a.go")); err != nil {
		t.Fatalf("initial Reindex: %v", err)
	}
	if _, err := ix.Reindex(ctx, pid, []repo.ScannedFile{unreadable("a.go", "h1")}, syms("a.go")); err != nil {
		t.Fatalf("Reindex after read error: %v", err)
	}

	st, err := ix.Reindex(ctx, pid, []repo.ScannedFile{scanned("a.go", "h2", "func F(){}")}, syms("a.go"))
	if err != nil {
		t.Fatalf("Reindex after recovery: %v", err)
	}
	if st.FilesEmbedded != 1 {
		t.Fatalf("FilesEmbedded = %d after recovery, want 1", st.FilesEmbedded)
	}
	after, err := database.ChunkedFiles(pid)
	if err != nil {
		t.Fatalf("ChunkedFiles: %v", err)
	}
	state, ok := after["a.go"]
	if !ok {
		t.Fatalf("a.go missing from ChunkedFiles after recovery: %#v", after)
	}
	if state.FileHash != "h2" {
		t.Fatalf("a.go file hash = %q, want h2 (the file was not re-embedded)", state.FileHash)
	}
}
