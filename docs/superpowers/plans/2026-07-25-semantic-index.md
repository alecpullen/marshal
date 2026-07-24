# Semantic Index Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Semantic code search end-to-end — chunk + embed the repo, store vectors in SQLite, expose `codebase_search`, and inject relevant code into the context pack passively.

**Architecture:** A new `internal/index/` package chunks files (symbol-aware, enriched) and drives an incremental hash-diff embedding pass folded into `repo.index`. Vectors live as float32 blobs in SQLite; a new `internal/retrieval/` package does brute-force cosine KNN behind a `Source` interface consumed by both the `codebase_search` tool and passive context-pack injection.

**Tech Stack:** Go, standard library + existing `internal/db`, `internal/repo`, `internal/embedding` (subsystem #1), `internal/contextpack`, `internal/tools`. No new dependencies.

**Spec:** [docs/superpowers/specs/2026-07-25-semantic-index-design.md](../specs/2026-07-25-semantic-index-design.md)

## Global Constraints

- **Depends on subsystem #1** (embedding foundation): `embedding.Embedder`, `embedding.NewFromConfig`, `routing.ResolveEmbedding`, `routing.ErrEmbeddingNotConfigured`. Build #1 first.
- **No new dependencies:** standard library only.
- **Graceful-off:** every embedding-dependent path must be a no-op / friendly message when no embedder is configured or the index is empty — never an error.
- **Format/vet before commit:** `gofmt -w .` and `go vet ./...` must pass.
- Vectors are float32 little-endian, `dim*4` bytes.
- `SaveSymbols`-style per-project locking (`db.locks.Lock(projectID)`) for chunk writes.

---

### Task 1: chunks/embeddings schema + vector codec

**Files:**
- Modify: `internal/db/migrations.go` (append two tables to the `schema` const)
- Create: `internal/db/chunks.go` (vector codec + row types)
- Test: `internal/db/chunks_test.go`

**Interfaces:**
- Produces: `func encodeVector(v []float32) []byte`, `func decodeVector(b []byte) []float32`; types `Chunk`, `ChunkWithVector`, `FileChunkState`, `VectorRow`.

- [ ] **Step 1: Write the failing test**

Create `internal/db/chunks_test.go`:

```go
package db

import (
	"math"
	"testing"
)

func TestVectorCodecRoundTrip(t *testing.T) {
	cases := [][]float32{
		{},
		{0},
		{1.5, -2.25, 0, 3.14159},
		{float32(math.MaxFloat32), float32(-math.MaxFloat32)},
	}
	for _, want := range cases {
		got := decodeVector(encodeVector(want))
		if len(got) != len(want) {
			t.Fatalf("len = %d, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("v[%d] = %v, want %v", i, got[i], want[i])
			}
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestVectorCodec -v`
Expected: FAIL — `undefined: encodeVector`.

- [ ] **Step 3: Write minimal implementation**

Append to the `schema` const in `internal/db/migrations.go` (before the closing backtick):

```sql

CREATE TABLE IF NOT EXISTS chunks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    file_path   TEXT NOT NULL,
    file_hash   TEXT NOT NULL,
    kind        TEXT NOT NULL,
    symbol_name TEXT,
    start_line  INTEGER NOT NULL,
    end_line    INTEGER NOT NULL,
    content     TEXT NOT NULL,
    token_count INTEGER NOT NULL,
    created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chunks_project ON chunks(project_id);
CREATE INDEX IF NOT EXISTS idx_chunks_project_file ON chunks(project_id, file_path);

CREATE TABLE IF NOT EXISTS embeddings (
    chunk_id INTEGER NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    model    TEXT NOT NULL,
    dim      INTEGER NOT NULL,
    vector   BLOB NOT NULL,
    PRIMARY KEY (chunk_id)
);
```

Create `internal/db/chunks.go`:

```go
package db

import (
	"encoding/binary"
	"math"
)

// Chunk is one embeddable unit of a file.
type Chunk struct {
	FilePath   string
	FileHash   string
	Kind       string // "code" | "doc"
	SymbolName string
	StartLine  int
	EndLine    int
	Content    string
	TokenCount int
}

// ChunkWithVector pairs a Chunk with its embedding for insertion.
type ChunkWithVector struct {
	Chunk
	Model  string
	Dim    int
	Vector []float32
}

// FileChunkState is the stored (hash, model) for a file's chunks, used to
// decide whether a file needs re-embedding.
type FileChunkState struct {
	FileHash string
	Model    string
}

// VectorRow is one chunk's vector plus enough to render a retrieval hit.
type VectorRow struct {
	ChunkID   int64
	FilePath  string
	StartLine int
	EndLine   int
	Content   string
	Vector    []float32
}

// encodeVector serializes a float32 slice as little-endian bytes.
func encodeVector(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeVector reverses encodeVector.
func decodeVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db/ -run TestVectorCodec -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/db/ && go vet ./internal/db/
git add internal/db/migrations.go internal/db/chunks.go internal/db/chunks_test.go
git commit -m "feat(db): add chunks/embeddings schema and float32 vector codec"
```

---

### Task 2: chunk CRUD + read helpers

**Files:**
- Modify: `internal/db/chunks.go`
- Test: `internal/db/chunks_test.go`

**Interfaces:**
- Consumes: Task 1 types, `db.locks.Lock`, `buildValues`.
- Produces: `ReplaceFileChunks`, `ChunkedFiles`, `DeleteFileChunks`, `LoadVectors`, `ChunkGeneration` on `*DB`.

- [ ] **Step 1: Write the failing test**

Append to `internal/db/chunks_test.go`:

```go
func TestChunkCRUD(t *testing.T) {
	database := newTestDB(t) // existing helper used across db tests
	projectID := mustCreateProject(t, database, "/tmp/proj")

	cwv := []ChunkWithVector{{
		Chunk:  Chunk{FilePath: "a.go", FileHash: "h1", Kind: "code", SymbolName: "Foo", StartLine: 1, EndLine: 3, Content: "x", TokenCount: 1},
		Model:  "nomic", Dim: 2, Vector: []float32{0.1, 0.2},
	}}
	if err := database.ReplaceFileChunks(projectID, "a.go", "h1", cwv); err != nil {
		t.Fatalf("ReplaceFileChunks: %v", err)
	}

	states, err := database.ChunkedFiles(projectID)
	if err != nil || states["a.go"].FileHash != "h1" || states["a.go"].Model != "nomic" {
		t.Fatalf("ChunkedFiles = %#v err=%v", states, err)
	}

	count, _, err := database.ChunkGeneration(projectID)
	if err != nil || count != 1 {
		t.Fatalf("ChunkGeneration count=%d err=%v", count, err)
	}

	rows, err := database.LoadVectors(projectID, "nomic")
	if err != nil || len(rows) != 1 || rows[0].FilePath != "a.go" || len(rows[0].Vector) != 2 {
		t.Fatalf("LoadVectors = %#v err=%v", rows, err)
	}

	if err := database.DeleteFileChunks(projectID, "a.go"); err != nil {
		t.Fatalf("DeleteFileChunks: %v", err)
	}
	states, _ = database.ChunkedFiles(projectID)
	if len(states) != 0 {
		t.Fatalf("after delete states=%#v", states)
	}
}
```

Note: reuse whatever DB test bootstrap the package already uses (e.g. `newTestDB`/`mustCreateProject`). If the helpers have different names, match them — check `internal/db/files_test.go` for the pattern.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestChunkCRUD -v`
Expected: FAIL — `undefined: (*DB).ReplaceFileChunks`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/db/chunks.go` (add imports `database/sql`, `fmt`, `time`):

```go
// ReplaceFileChunks deletes a file's existing chunks and inserts the given
// chunks + embeddings in one transaction. Per-project locked.
func (db *DB) ReplaceFileChunks(projectID int64, filePath, fileHash string, chunks []ChunkWithVector) error {
	unlock := db.locks.Lock(projectID)
	defer unlock()

	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin replace chunks: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM chunks WHERE project_id = ? AND file_path = ?`, projectID, filePath); err != nil {
		return fmt.Errorf("delete file chunks: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, c := range chunks {
		res, err := tx.Exec(
			`INSERT INTO chunks (project_id, file_path, file_hash, kind, symbol_name, start_line, end_line, content, token_count, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			projectID, filePath, fileHash, c.Kind, c.SymbolName, c.StartLine, c.EndLine, c.Content, c.TokenCount, now)
		if err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
		chunkID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("chunk id: %w", err)
		}
		if _, err := tx.Exec(
			`INSERT INTO embeddings (chunk_id, model, dim, vector) VALUES (?, ?, ?, ?)`,
			chunkID, c.Model, c.Dim, encodeVector(c.Vector)); err != nil {
			return fmt.Errorf("insert embedding: %w", err)
		}
	}
	return tx.Commit()
}

// DeleteFileChunks removes a file's chunks (embeddings cascade).
func (db *DB) DeleteFileChunks(projectID int64, filePath string) error {
	unlock := db.locks.Lock(projectID)
	defer unlock()
	if _, err := db.sqlDB.Exec(`DELETE FROM chunks WHERE project_id = ? AND file_path = ?`, projectID, filePath); err != nil {
		return fmt.Errorf("delete file chunks: %w", err)
	}
	return nil
}

// ChunkedFiles returns the stored (hash, model) per file for a project.
func (db *DB) ChunkedFiles(projectID int64) (map[string]FileChunkState, error) {
	rows, err := db.sqlDB.Query(
		`SELECT DISTINCT c.file_path, c.file_hash, e.model
		   FROM chunks c JOIN embeddings e ON e.chunk_id = c.id
		  WHERE c.project_id = ?`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query chunked files: %w", err)
	}
	defer rows.Close()
	out := map[string]FileChunkState{}
	for rows.Next() {
		var path string
		var st FileChunkState
		if err := rows.Scan(&path, &st.FileHash, &st.Model); err != nil {
			return nil, err
		}
		out[path] = st
	}
	return out, rows.Err()
}

// LoadVectors returns all chunk vectors for a project+model (read path).
func (db *DB) LoadVectors(projectID int64, model string) ([]VectorRow, error) {
	rows, err := db.sqlDB.Query(
		`SELECT c.id, c.file_path, c.start_line, c.end_line, c.content, e.vector
		   FROM chunks c JOIN embeddings e ON e.chunk_id = c.id
		  WHERE c.project_id = ? AND e.model = ?`, projectID, model)
	if err != nil {
		return nil, fmt.Errorf("load vectors: %w", err)
	}
	defer rows.Close()
	var out []VectorRow
	for rows.Next() {
		var r VectorRow
		var blob []byte
		if err := rows.Scan(&r.ChunkID, &r.FilePath, &r.StartLine, &r.EndLine, &r.Content, &blob); err != nil {
			return nil, err
		}
		r.Vector = decodeVector(blob)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ChunkGeneration returns (row count, max chunk id) for a project — a cheap
// signal a reader caches on to detect index changes.
func (db *DB) ChunkGeneration(projectID int64) (int, int64, error) {
	var count int
	var maxID sql.NullInt64
	err := db.sqlDB.QueryRow(
		`SELECT COUNT(*), MAX(id) FROM chunks WHERE project_id = ?`, projectID).Scan(&count, &maxID)
	if err != nil {
		return 0, 0, fmt.Errorf("chunk generation: %w", err)
	}
	return count, maxID.Int64, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db/ -run TestChunkCRUD -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/db/ && go vet ./internal/db/
git add internal/db/chunks.go internal/db/chunks_test.go
git commit -m "feat(db): add chunk CRUD, ChunkedFiles, LoadVectors, ChunkGeneration"
```

---

### Task 3: symbol-aware enriched chunker

**Files:**
- Create: `internal/index/chunker.go`
- Test: `internal/index/chunker_test.go`

**Interfaces:**
- Consumes: `repo.ScannedFile` (`db.FileIndex{Path,Language,Hash}` + `Content []byte`), `db.Symbol`.
- Produces: `type Chunk` (index-local) and `func Chunk(file repo.ScannedFile, symbols []db.Symbol) []db.Chunk`.

- [ ] **Step 1: Write the failing test**

Create `internal/index/chunker_test.go`:

```go
package index

import (
	"strings"
	"testing"

	"marshal/internal/db"
	"marshal/internal/repo"
)

func TestChunkCodeFileWithSymbols(t *testing.T) {
	src := "package p\n\n// Foo does a thing.\nfunc Foo(x int) int {\n\treturn x\n}\n"
	file := repo.ScannedFile{FileIndex: db.FileIndex{Path: "p.go", Language: "go", Hash: "h"}, Content: []byte(src)}
	symbols := []db.Symbol{{FilePath: "p.go", Kind: "function", Name: "Foo", Signature: "func Foo(x int) int", LineStart: 4, LineEnd: 6}}

	chunks := Chunk(file, symbols)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	c := chunks[0]
	if c.Kind != "code" || c.SymbolName != "Foo" || c.FileHash != "h" {
		t.Fatalf("chunk = %#v", c)
	}
	if !strings.Contains(c.Content, "p.go") || !strings.Contains(c.Content, "func Foo") || !strings.Contains(c.Content, "return x") {
		t.Fatalf("enriched content missing parts: %q", c.Content)
	}
	if c.TokenCount <= 0 {
		t.Fatal("token count not set")
	}
}

func TestChunkMarkdownByHeading(t *testing.T) {
	src := "# Title\n\nintro\n\n## Section A\n\nbody a\n\n## Section B\n\nbody b\n"
	file := repo.ScannedFile{FileIndex: db.FileIndex{Path: "README.md", Language: "markdown", Hash: "h"}, Content: []byte(src)}

	chunks := Chunk(file, nil)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple heading sections, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Kind != "doc" {
			t.Fatalf("markdown chunk kind = %q", c.Kind)
		}
	}
}

func TestChunkSymbollessFileWindows(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 150; i++ {
		b.WriteString("line\n")
	}
	file := repo.ScannedFile{FileIndex: db.FileIndex{Path: "big.txt", Language: "", Hash: "h"}, Content: []byte(b.String())}
	chunks := Chunk(file, nil)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple windows, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.SymbolName != "" || c.Kind != "code" {
			t.Fatalf("window chunk = %#v", c)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/index/ -run TestChunk -v`
Expected: FAIL — `undefined: Chunk`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/index/chunker.go`:

```go
package index

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"marshal/internal/db"
	"marshal/internal/repo"
)

const (
	maxChunkLines = 200 // oversized symbol split threshold
	windowLines   = 60  // symbol-less fallback window
	windowOverlap = 10
)

// estimateTokens is a local rune/4 estimate (kept out of contextpack so the
// write path does not depend on the passive layer).
func estimateTokens(s string) int {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

// Chunk splits a scanned file into embeddable chunks. symbols are the file's
// extracted symbols (may be empty).
func Chunk(file repo.ScannedFile, symbols []db.Symbol) []db.Chunk {
	lines := strings.Split(string(file.Content), "\n")
	switch {
	case isProse(file.Language, file.Path):
		return chunkProse(file, lines)
	case len(codeSymbols(symbols)) > 0:
		return chunkBySymbols(file, lines, codeSymbols(symbols))
	default:
		return chunkWindows(file, lines)
	}
}

func isProse(lang, path string) bool {
	return lang == "markdown" || strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".markdown")
}

// codeSymbols keeps only function/method/type symbols (imports excluded).
func codeSymbols(symbols []db.Symbol) []db.Symbol {
	var out []db.Symbol
	for _, s := range symbols {
		if s.Kind == "function" || s.Kind == "method" || s.Kind == "type" {
			out = append(out, s)
		}
	}
	return out
}

func header(file repo.ScannedFile, s db.Symbol) string {
	recv := ""
	if s.Receiver != "" {
		recv = s.Receiver + "."
	}
	sig := s.Signature
	if sig == "" {
		sig = recv + s.Name
	}
	return fmt.Sprintf("// %s — %s\n", file.Path, sig)
}

func sliceLines(lines []string, start, end int) string {
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

func chunkBySymbols(file repo.ScannedFile, lines []string, symbols []db.Symbol) []db.Chunk {
	var out []db.Chunk
	for _, s := range symbols {
		h := header(file, s)
		if s.LineEnd-s.LineStart+1 <= maxChunkLines {
			body := sliceLines(lines, s.LineStart, s.LineEnd)
			out = append(out, newChunk(file, "code", s.Name, s.LineStart, s.LineEnd, h+body))
			continue
		}
		for start := s.LineStart; start <= s.LineEnd; start += maxChunkLines {
			end := start + maxChunkLines - 1
			if end > s.LineEnd {
				end = s.LineEnd
			}
			body := sliceLines(lines, start, end)
			out = append(out, newChunk(file, "code", s.Name, start, end, h+body))
		}
	}
	return out
}

func chunkWindows(file repo.ScannedFile, lines []string) []db.Chunk {
	var out []db.Chunk
	h := fmt.Sprintf("// %s\n", file.Path)
	step := windowLines - windowOverlap
	if step < 1 {
		step = windowLines
	}
	for start := 1; start <= len(lines); start += step {
		end := start + windowLines - 1
		if end > len(lines) {
			end = len(lines)
		}
		body := sliceLines(lines, start, end)
		if strings.TrimSpace(body) == "" {
			continue
		}
		out = append(out, newChunk(file, "code", "", start, end, h+body))
		if end == len(lines) {
			break
		}
	}
	return out
}

func chunkProse(file repo.ScannedFile, lines []string) []db.Chunk {
	var out []db.Chunk
	sectionStart := 1
	heading := ""
	flush := func(end int) {
		body := sliceLines(lines, sectionStart, end)
		if strings.TrimSpace(body) == "" {
			return
		}
		out = append(out, newChunk(file, "doc", heading, sectionStart, end, body))
	}
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			if i+1 > sectionStart {
				flush(i) // flush previous section up to the line before this heading
			}
			sectionStart = i + 1
			heading = strings.TrimLeft(strings.TrimSpace(line), "# ")
		}
	}
	flush(len(lines))
	return out
}

func newChunk(file repo.ScannedFile, kind, symbol string, start, end int, content string) db.Chunk {
	return db.Chunk{
		FilePath:   file.Path,
		FileHash:   file.Hash,
		Kind:       kind,
		SymbolName: symbol,
		StartLine:  start,
		EndLine:    end,
		Content:    content,
		TokenCount: estimateTokens(content),
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/index/ -run TestChunk -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/index/ && go vet ./internal/index/
git add internal/index/chunker.go internal/index/chunker_test.go
git commit -m "feat(index): add symbol-aware enriched chunker"
```

---

### Task 4: incremental embedding engine

**Files:**
- Create: `internal/index/indexer.go`
- Test: `internal/index/indexer_test.go`

**Interfaces:**
- Consumes: `Chunk` (Task 3), `db` chunk helpers (Task 2), `embedding.Embedder` (#1), `repo.ScannedFile`.
- Produces: `type Indexer`, `type Stats`, `func NewIndexer(database *db.DB, e embedding.Embedder) *Indexer`, `func (ix *Indexer) Reindex(ctx, projectID int64, scanned []repo.ScannedFile, symbolsByFile map[string][]db.Symbol) (Stats, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/index/indexer_test.go`:

```go
package index

import (
	"context"
	"testing"

	"marshal/internal/db"
	"marshal/internal/repo"
)

type fakeEmbedder struct{ model string; calls int }

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

func TestReindexNilEmbedderIsNoop(t *testing.T) {
	database := newTestDB(t)
	pid := mustCreateProject(t, database, "/tmp/p")
	ix := NewIndexer(database, nil)
	st, err := ix.Reindex(context.Background(), pid, []repo.ScannedFile{scanned("a.go", "h", "x")}, syms("a.go"))
	if err != nil || (st != Stats{}) {
		t.Fatalf("nil embedder should no-op: %+v err=%v", st, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/index/ -run TestReindex -v`
Expected: FAIL — `undefined: NewIndexer`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/index/indexer.go`:

```go
package index

import (
	"context"
	"fmt"

	"marshal/internal/db"
	"marshal/internal/embedding"
	"marshal/internal/repo"
)

type Stats struct {
	FilesEmbedded int
	ChunksWritten int
	FilesSkipped  int
	FilesPurged   int
}

type Indexer struct {
	db       *db.DB
	embedder embedding.Embedder
}

func NewIndexer(database *db.DB, e embedding.Embedder) *Indexer {
	return &Indexer{db: database, embedder: e}
}

// Reindex re-embeds only stale files (changed hash or changed model), purges
// files no longer present, and skips unchanged files. A nil embedder makes it
// a no-op.
func (ix *Indexer) Reindex(ctx context.Context, projectID int64, scanned []repo.ScannedFile, symbolsByFile map[string][]db.Symbol) (Stats, error) {
	var st Stats
	if ix.embedder == nil {
		return st, nil
	}
	model := ix.embedder.Model()

	state, err := ix.db.ChunkedFiles(projectID)
	if err != nil {
		return st, err
	}

	seen := map[string]bool{}
	for _, sf := range scanned {
		if sf.ReadErr != nil {
			continue
		}
		seen[sf.Path] = true
		prev, ok := state[sf.Path]
		if ok && prev.FileHash == sf.Hash && prev.Model == model {
			st.FilesSkipped++
			continue
		}
		chunks := Chunk(sf, symbolsByFile[sf.Path])
		if len(chunks) == 0 {
			// Nothing to embed; clear any stale chunks for this file.
			if ok {
				if err := ix.db.DeleteFileChunks(projectID, sf.Path); err != nil {
					return st, err
				}
			}
			continue
		}
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Content
		}
		vecs, err := ix.embedder.Embed(ctx, texts)
		if err != nil {
			return st, fmt.Errorf("embed %s: %w", sf.Path, err)
		}
		if len(vecs) != len(chunks) {
			return st, fmt.Errorf("embed %s: %d vecs for %d chunks", sf.Path, len(vecs), len(chunks))
		}
		cwv := make([]db.ChunkWithVector, len(chunks))
		for i, c := range chunks {
			cwv[i] = db.ChunkWithVector{Chunk: c, Model: model, Dim: len(vecs[i]), Vector: vecs[i]}
		}
		if err := ix.db.ReplaceFileChunks(projectID, sf.Path, sf.Hash, cwv); err != nil {
			return st, err
		}
		st.FilesEmbedded++
		st.ChunksWritten += len(chunks)
	}

	for path := range state {
		if !seen[path] {
			if err := ix.db.DeleteFileChunks(projectID, path); err != nil {
				return st, err
			}
			st.FilesPurged++
		}
	}
	return st, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/index/ -run TestReindex -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/index/ && go vet ./internal/index/
git add internal/index/indexer.go internal/index/indexer_test.go
git commit -m "feat(index): add incremental hash-diff embedding engine"
```

---

### Task 5: wire the embedding pass into repo.index

**Files:**
- Modify: `internal/tools/native/repo_index.go`
- Modify: `internal/tools/native/native.go` (toolSet gains a router/embedder resolver)
- Test: `internal/tools/native/repo_index_test.go`

**Interfaces:**
- Consumes: `Indexer` (Task 4), `routing.ResolveEmbedding`/`ErrEmbeddingNotConfigured` (#1), `embedding.NewFromConfig` (#1).
- Produces: `repo.index` now builds embeddings after saving symbols; summary reflects it.

- [ ] **Step 1: Write the failing test**

Add to `internal/tools/native/repo_index_test.go` a test asserting that with an embedder configured (inject a fake resolver returning a fake embedder), `repo.index` output contains an "Embedded" line, and with none it contains "not configured". Match the existing repo_index_test.go construction of a `toolSet`; wire a new field (see Step 3) for embedder resolution so the test can inject a fake.

```go
func TestRepoIndexEmbeds(t *testing.T) {
	// Arrange a toolSet with db+projectID (as existing tests do) and inject a
	// fake embedder resolver that returns a 2-dim fake embedder.
	ts := newTestToolSetWithRepo(t) // existing helper or adapt from repo_index_test.go
	ts.resolveEmbedder = func() (embedding.Embedder, error) { return &fakeEmbedder{model: "m"}, nil }

	res, err := ts.repoIndexTool().Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Content, "Embedded") {
		t.Fatalf("expected embedded line, got: %s", res.Content)
	}
}
```

Reuse the `fakeEmbedder` shape from Task 4 (define a local one in the native test package). If `newTestToolSetWithRepo` doesn't exist, build the `toolSet` inline mirroring the existing `repo_index_test.go` setup.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/native/ -run TestRepoIndexEmbeds -v`
Expected: FAIL — `ts.resolveEmbedder undefined`.

- [ ] **Step 3: Write minimal implementation**

In `internal/tools/native/native.go`, add a field to `toolSet`:

```go
	// resolveEmbedder returns the configured Embedder or an error. Injected so
	// tests can supply a fake; production wiring resolves via the router.
	resolveEmbedder func() (embedding.Embedder, error)
```

Populate it in the tool-set constructor (where other deps are wired) using the router:

```go
	ts.resolveEmbedder = func() (embedding.Embedder, error) {
		route, err := router.ResolveEmbedding()
		if err != nil {
			return nil, err // includes routing.ErrEmbeddingNotConfigured
		}
		pc := opts.Config.Providers[route.Preset.Provider]
		return embedding.NewFromConfig(route.Preset.Provider, pc, route.Preset.Model)
	}
```

(Thread the `routing.Router`/`routing.Config` into the constructor if not already present, mirroring how other config-derived deps are passed.)

In `internal/tools/native/repo_index.go`, after `t.db.SaveSymbols(...)`, add the embedding pass:

```go
	// Build the symbols-by-file map from the symbols just extracted.
	symbolsByFile := map[string][]db.Symbol{}
	for _, s := range symbols {
		symbolsByFile[s.FilePath] = append(symbolsByFile[s.FilePath], s)
	}

	var embedder embedding.Embedder
	if t.resolveEmbedder != nil {
		if e, err := t.resolveEmbedder(); err == nil {
			embedder = e
		}
	}
	indexer := index.NewIndexer(t.db, embedder)
	st, err := indexer.Reindex(ctx, t.projectID, scanned, symbolsByFile)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("embedding: %v", err))
	}
	if embedder == nil {
		fmt.Fprintf(&b, "\nSemantic index: not configured\n")
	} else {
		fmt.Fprintf(&b, "\nEmbedded %d files (%d chunks)\n", st.FilesEmbedded, st.ChunksWritten)
	}
```

(Add imports `marshal/internal/index`, `marshal/internal/embedding`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools/native/ -run TestRepoIndex -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tools/native/ && go vet ./internal/tools/native/
git add internal/tools/native/repo_index.go internal/tools/native/native.go internal/tools/native/repo_index_test.go
git commit -m "feat(native): build embeddings in repo.index (graceful-off)"
```

---

### Task 6: retrieval Source + semantic KNN

**Files:**
- Create: `internal/retrieval/retrieval.go` (interface + cosine)
- Create: `internal/retrieval/semantic.go` (semantic source + cache)
- Test: `internal/retrieval/retrieval_test.go`, `internal/retrieval/semantic_test.go`

**Interfaces:**
- Consumes: `db.LoadVectors`/`ChunkGeneration` (Task 2), `embedding.Embedder` (#1).
- Produces: `Candidate`, `Query`, `Source`, `func cosine(a, b []float32) float64`, `func NewSemanticSource(database *db.DB, e embedding.Embedder, projectID int64) *SemanticSource`.

- [ ] **Step 1: Write the failing test**

Create `internal/retrieval/retrieval_test.go`:

```go
package retrieval

import "testing"

func TestCosine(t *testing.T) {
	if got := cosine([]float32{1, 0}, []float32{1, 0}); got < 0.999 {
		t.Fatalf("identical vectors cosine=%v", got)
	}
	if got := cosine([]float32{1, 0}, []float32{0, 1}); got > 0.001 {
		t.Fatalf("orthogonal cosine=%v", got)
	}
}
```

Create `internal/retrieval/semantic_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/retrieval/ -v`
Expected: FAIL — `undefined: cosine` / `NewSemanticSource`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/retrieval/retrieval.go`:

```go
package retrieval

import (
	"context"
	"math"
)

type Candidate struct {
	FilePath   string
	StartLine  int
	EndLine    int
	Content    string
	Score      float64
	SourceName string
}

type Query struct {
	Text       string
	Limit      int
	PathPrefix string
}

type Source interface {
	Name() string
	Retrieve(ctx context.Context, q Query) ([]Candidate, error)
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
```

Create `internal/retrieval/semantic.go`:

```go
package retrieval

import (
	"context"
	"sort"
	"strings"
	"sync"

	"marshal/internal/db"
	"marshal/internal/embedding"
)

type SemanticSource struct {
	db        *db.DB
	embedder  embedding.Embedder
	projectID int64

	mu       sync.Mutex
	cache    []db.VectorRow
	cacheGen int64 // maxID last loaded
	cacheN   int   // count last loaded
}

func NewSemanticSource(database *db.DB, e embedding.Embedder, projectID int64) *SemanticSource {
	return &SemanticSource{db: database, embedder: e, projectID: projectID}
}

func (s *SemanticSource) Name() string { return "semantic" }

func (s *SemanticSource) vectors() ([]db.VectorRow, error) {
	count, maxID, err := s.db.ChunkGeneration(s.projectID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil || count != s.cacheN || maxID != s.cacheGen {
		rows, err := s.db.LoadVectors(s.projectID, s.embedder.Model())
		if err != nil {
			return nil, err
		}
		s.cache, s.cacheN, s.cacheGen = rows, count, maxID
	}
	return s.cache, nil
}

func (s *SemanticSource) Retrieve(ctx context.Context, q Query) ([]Candidate, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	qv, err := s.embedder.Embed(ctx, []string{q.Text})
	if err != nil {
		return nil, err
	}
	if len(qv) != 1 {
		return nil, nil
	}
	rows, err := s.vectors()
	if err != nil {
		return nil, err
	}
	cands := make([]Candidate, 0, len(rows))
	for _, r := range rows {
		if q.PathPrefix != "" && !strings.HasPrefix(r.FilePath, q.PathPrefix) {
			continue
		}
		cands = append(cands, Candidate{
			FilePath: r.FilePath, StartLine: r.StartLine, EndLine: r.EndLine,
			Content: r.Content, Score: cosine(qv[0], r.Vector), SourceName: "semantic",
		})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Score > cands[j].Score })
	if len(cands) > limit {
		cands = cands[:limit]
	}
	return cands, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/retrieval/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/retrieval/ && go vet ./internal/retrieval/
git add internal/retrieval/
git commit -m "feat(retrieval): add Source interface and semantic cosine KNN source"
```

---

### Task 7: codebase_search tool

**Files:**
- Create: `internal/tools/native/codebase_search.go`
- Modify: `internal/tools/native/native.go` (register the tool)
- Test: `internal/tools/native/codebase_search_test.go`

**Interfaces:**
- Consumes: `retrieval.NewSemanticSource` (Task 6), `resolveEmbedder` (Task 5), `db.ChunkGeneration`.
- Produces: `codebase_search` registered tool.

- [ ] **Step 1: Write the failing test**

Create `internal/tools/native/codebase_search_test.go` with three cases: configured (returns ranked hits), `ErrEmbeddingNotConfigured` (friendly message), empty index (friendly message). Build the `toolSet` inline as other native tests do; inject `resolveEmbedder`.

```go
func TestCodebaseSearchNotConfigured(t *testing.T) {
	ts := newTestToolSetWithRepo(t)
	ts.resolveEmbedder = func() (embedding.Embedder, error) { return nil, routing.ErrEmbeddingNotConfigured }
	res, err := ts.codebaseSearchTool().Handler(context.Background(),
		registry.ToolCall{Args: json.RawMessage(`{"query":"x"}`)})
	if err != nil || !strings.Contains(res.Content, "not configured") {
		t.Fatalf("res=%q err=%v", res.Content, err)
	}
}
```

Add a configured-hit case seeding chunks via `ReplaceFileChunks` and a fake embedder, asserting the output contains the seeded file path.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/native/ -run TestCodebaseSearch -v`
Expected: FAIL — `ts.codebaseSearchTool undefined`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/tools/native/codebase_search.go`:

```go
package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/embedding"
	"marshal/internal/llm/routing"
	"marshal/internal/retrieval"
	"marshal/internal/tools/registry"
)

type codebaseSearchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
	Path  string `json:"path"`
}

func (t *toolSet) codebaseSearchTool() registry.Tool {
	tool := registry.Tool{
		Name:        "codebase_search",
		Description: "Semantic search over the indexed codebase. Returns the most relevant code/doc snippets for a natural-language query.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"},"path":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[codebaseSearchArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		var embedder embedding.Embedder
		if t.resolveEmbedder != nil {
			embedder, err = t.resolveEmbedder()
		} else {
			err = routing.ErrEmbeddingNotConfigured
		}
		if errors.Is(err, routing.ErrEmbeddingNotConfigured) || embedder == nil {
			return registry.ToolResult{Summary: "semantic search unavailable",
				Content: "Semantic search is unavailable: no embedding model configured. Set an `embedding` role in your profile to enable it."}, nil
		}
		if err != nil {
			return registry.ToolResult{}, err
		}
		if count, _, _ := t.db.ChunkGeneration(t.projectID); count == 0 {
			return registry.ToolResult{Summary: "no semantic index",
				Content: "No semantic index yet — run `repo.index` to build it."}, nil
		}
		src := retrieval.NewSemanticSource(t.db, embedder, t.projectID)
		hits, err := src.Retrieve(ctx, retrieval.Query{Text: args.Query, Limit: args.Limit, PathPrefix: args.Path})
		if err != nil {
			return registry.ToolResult{}, err
		}
		if len(hits) == 0 {
			return registry.ToolResult{Summary: "no matches", Content: "No semantic matches for that query."}, nil
		}
		var b strings.Builder
		for _, h := range hits {
			fmt.Fprintf(&b, "`%s:%d-%d` (score %.2f)\n%s\n\n", h.FilePath, h.StartLine, h.EndLine, h.Score, h.Content)
		}
		return registry.ToolResult{Summary: fmt.Sprintf("%d matches", len(hits)), Content: strings.TrimSpace(b.String())}, nil
	}
	return tool
}
```

Register `t.codebaseSearchTool()` in `native.go` wherever the tool list is assembled (next to `repoIndexTool`, `symbolsFind`, etc.).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools/native/ -run TestCodebaseSearch -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tools/native/ && go vet ./internal/tools/native/
git add internal/tools/native/codebase_search.go internal/tools/native/native.go internal/tools/native/codebase_search_test.go
git commit -m "feat(native): add codebase_search semantic tool (graceful-off)"
```

---

### Task 8: contextpack SectionSemantic + MergeSemanticContext

**Files:**
- Modify: `internal/contextpack/contextpack.go` (new SectionKind)
- Modify: `internal/contextpack/builder.go` (MergeSemanticContext + section constructor)
- Test: `internal/contextpack/contextpack_test.go`

**Interfaces:**
- Consumes: existing `Pack`, `Section`, `FileSnippet`, `replaceSection`, `resolvePackParams`, `buildPackFromSections`.
- Produces: `const SectionSemantic`, `func MergeSemanticContext(pack Pack, snippets []FileSnippet, maxTokens int, now func() time.Time) Pack`.

- [ ] **Step 1: Write the failing test**

Append to `internal/contextpack/contextpack_test.go`:

```go
func TestMergeSemanticContext(t *testing.T) {
	pack := Pack{}
	snips := []FileSnippet{{Path: "a.go", StartLine: 1, EndLine: 2, Content: "func A(){}"}}
	got := MergeSemanticContext(pack, snips, DefaultMaxTokens, nil)

	var found *Section
	for i := range got.Sections {
		if got.Sections[i].Kind == SectionSemantic {
			found = &got.Sections[i]
		}
	}
	if found == nil || found.Priority != 35 || !strings.Contains(found.Content, "a.go") {
		t.Fatalf("semantic section = %#v", found)
	}

	// Empty snippets removes the section.
	got2 := MergeSemanticContext(got, nil, DefaultMaxTokens, nil)
	for _, s := range got2.Sections {
		if s.Kind == SectionSemantic {
			t.Fatal("empty snippets should remove the semantic section")
		}
	}
}
```

(Ensure `strings` is imported in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/contextpack/ -run TestMergeSemanticContext -v`
Expected: FAIL — `undefined: SectionSemantic` / `MergeSemanticContext`.

- [ ] **Step 3: Write minimal implementation**

In `internal/contextpack/contextpack.go`, add to the SectionKind const block:

```go
	SectionSemantic SectionKind = "semantic"
```

In `internal/contextpack/builder.go`, add:

```go
func newSemanticSection(snippets []FileSnippet) (Section, bool) {
	var parts []string
	for _, s := range snippets {
		content, ok := trimSectionContent(s.Content)
		if !ok {
			continue
		}
		src := s.Path
		if s.StartLine > 0 && s.EndLine > 0 {
			src = fmt.Sprintf("%s:%d-%d", s.Path, s.StartLine, s.EndLine)
		}
		parts = append(parts, fmt.Sprintf("%s\n%s", src, content))
	}
	if len(parts) == 0 {
		return Section{}, false
	}
	return Section{
		Kind:     SectionSemantic,
		Title:    "Relevant Code",
		Priority: 35,
		Content:  strings.Join(parts, "\n\n"),
	}, true
}

// MergeSemanticContext replaces any existing semantic section with one built
// from snippets, inserted before file-snippet/tool-output sections, then
// rebudgets within maxTokens. Empty snippets removes the section.
func MergeSemanticContext(pack Pack, snippets []FileSnippet, maxTokens int, now func() time.Time) Pack {
	maxTokens, generatedAt := resolvePackParams(pack, maxTokens, now)
	sec, ok := newSemanticSection(snippets)
	sections := replaceSection(pack.Sections, SectionSemantic, sec, ok, SectionFileSnippet, SectionToolOutput)
	return buildPackFromSections(sections, maxTokens, generatedAt)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/contextpack/ -run TestMergeSemanticContext -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/contextpack/ && go vet ./internal/contextpack/
git add internal/contextpack/contextpack.go internal/contextpack/builder.go internal/contextpack/contextpack_test.go
git commit -m "feat(contextpack): add SectionSemantic and MergeSemanticContext"
```

---

### Task 9: passive injection wiring in the agent

**Files:**
- Create: `internal/agent/semantic.go`
- Modify: `internal/agent/route.go` (call it alongside MergeMemories)
- Test: `internal/agent/semantic_test.go`

**Interfaces:**
- Consumes: `retrieval` Source, `contextpack.MergeSemanticContext`, `Runner` (has `State`, `Now`, embedder resolution), `extractPinnedFiles` pattern in `atfile.go`.
- Produces: `func retrieveSemanticContext(ctx context.Context, goal string, src retrieval.Source) []contextpack.FileSnippet`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/semantic_test.go`:

```go
package agent

import (
	"context"
	"testing"

	"marshal/internal/contextpack"
	"marshal/internal/retrieval"
)

type fakeSource struct{ cands []retrieval.Candidate }

func (f fakeSource) Name() string { return "semantic" }
func (f fakeSource) Retrieve(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
	return f.cands, nil
}

func TestRetrieveSemanticContext(t *testing.T) {
	src := fakeSource{cands: []retrieval.Candidate{{FilePath: "a.go", StartLine: 1, EndLine: 2, Content: "func A(){}"}}}
	snips := retrieveSemanticContext(context.Background(), "find A", src)
	if len(snips) != 1 || snips[0].Path != "a.go" {
		t.Fatalf("snips = %#v", snips)
	}

	// nil source → nil snippets (graceful-off).
	if got := retrieveSemanticContext(context.Background(), "x", nil); got != nil {
		t.Fatalf("nil source should yield nil, got %#v", got)
	}

	// Empty snippets produce no semantic section.
	pack := contextpack.MergeSemanticContext(contextpack.Pack{}, retrieveSemanticContext(context.Background(), "x", nil), contextpack.DefaultMaxTokens, nil)
	for _, s := range pack.Sections {
		if s.Kind == contextpack.SectionSemantic {
			t.Fatal("expected no semantic section for nil source")
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestRetrieveSemanticContext -v`
Expected: FAIL — `undefined: retrieveSemanticContext`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/semantic.go`:

```go
package agent

import (
	"context"

	"marshal/internal/contextpack"
	"marshal/internal/retrieval"
)

// semanticRetrievalLimit bounds how many snippets passive injection adds so it
// never dominates the pack.
const semanticRetrievalLimit = 5

// retrieveSemanticContext runs one semantic query for the goal and maps the
// hits to context-pack snippets. A nil source (embeddings unconfigured / empty
// index) yields nil — graceful-off.
func retrieveSemanticContext(ctx context.Context, goal string, src retrieval.Source) []contextpack.FileSnippet {
	if src == nil || goal == "" {
		return nil
	}
	hits, err := src.Retrieve(ctx, retrieval.Query{Text: goal, Limit: semanticRetrievalLimit})
	if err != nil || len(hits) == 0 {
		return nil
	}
	out := make([]contextpack.FileSnippet, 0, len(hits))
	for _, h := range hits {
		out = append(out, contextpack.FileSnippet{
			Path: h.FilePath, StartLine: h.StartLine, EndLine: h.EndLine, Content: h.Content,
		})
	}
	return out
}
```

In `internal/agent/route.go`, where `MergeMemories` is called (around line 102), add semantic injection. Construct the source from the runner's resolved embedder (nil when unconfigured — reuse the same resolution the native tool set uses; add a `Runner` helper `semanticSource(projectID) retrieval.Source` returning nil on `ErrEmbeddingNotConfigured`), then:

```go
	goal := r.currentGoal() // the goal string already available to the runner/route
	snips := retrieveSemanticContext(ctx, goal, r.semanticSource(projectID))
	packWithSemantic := contextpack.MergeSemanticContext(r.State.ContextPack(), snips, maxTokens, r.Now)
	r.State.SetContextPack(packWithSemantic)
```

Wire `r.semanticSource` to build a `retrieval.NewSemanticSource` from the router-resolved embedder, returning `nil` when `routing.ErrEmbeddingNotConfigured`. Run it only when the goal changes (guard on the runner's existing per-goal state so repeated turns don't re-embed) — mirror how memories are merged once per route rather than per turn.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestRetrieveSemanticContext -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/ && go vet ./internal/agent/
git add internal/agent/semantic.go internal/agent/route.go internal/agent/semantic_test.go
git commit -m "feat(agent): inject passive semantic context into the pack (graceful-off)"
```

---

## Final verification

- [ ] `go test ./...` — Expected: PASS.
- [ ] `go vet ./...` — Expected: no errors.
- [ ] `gofmt -l internal/db/ internal/index/ internal/retrieval/ internal/contextpack/ internal/tools/native/ internal/agent/` — Expected: no files listed.
- [ ] Manual smoke (optional, needs Ollama + `embedding` role): `go run ./cmd/marshal`, run `repo.index`, then `codebase_search` for a concept and confirm ranked hits.

## Spec coverage map

- chunks/embeddings tables + vector codec → Task 1
- chunk CRUD / read helpers → Task 2
- symbol-aware enriched chunker → Task 3
- incremental hash-diff engine (graceful-off, model-change, purge) → Task 4
- repo.index embedding pass → Task 5
- retrieval Source + semantic cosine KNN + cache → Task 6
- codebase_search tool + three graceful-off cases → Task 7
- SectionSemantic + MergeSemanticContext → Task 8
- passive injection wiring (retrieveSemanticContext, route.go) → Task 9

## Notes for the implementer

- Reuse the existing `internal/db` test bootstrap (`newTestDB`/`mustCreateProject` or the equivalent in `files_test.go`) rather than inventing one.
- Task 5 and 9 both need the router→embedder resolution; implement it once (Task 5's `resolveEmbedder` closure) and reuse the pattern in the runner for Task 9.
- Keep `internal/index` free of any `contextpack` import (write path must not depend on the passive layer) — that is why `estimateTokens` is duplicated locally.
