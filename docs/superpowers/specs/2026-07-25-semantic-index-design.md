# Semantic Index (Subsystem #2)

**Date:** 2026-07-25
**Status:** Design approved; ready for implementation plan
**Umbrella:** [Passive Knowledge Architecture](2026-07-24-passive-knowledge-architecture-design.md)
**Depends on:** [Embedding Foundation (Subsystem #1)](2026-07-24-embedding-foundation-design.md)
**Type:** Subsystem spec — semantic code search end-to-end, minus the passive
context-pack wiring (deliberately deferred to avoid colliding with in-flight
token-metrics work in `internal/contextpack`).

## Scope

**In:** the `chunks`/`embeddings` tables, a symbol-aware enriched chunker, an
incremental hash-diff embedding pass folded into the existing `repo.index`
tool, float32-blob vector storage with in-Go cosine KNN, a retrieval `Source`
interface with a single semantic implementation, and the model-facing
`codebase_search` tool.

**Out (later specs):**
- Passive context-pack injection and multi-source **fusion** + **git-churn
  ranking** — a later "retrieval fusion / passive context" spec, sequenced
  after the token-metrics work lands so it doesn't fight that churn in
  `internal/contextpack`.
- The background **watcher** (spec #3) — indexing here is triggered explicitly
  via `repo.index`.
- **LSP** symbols/diagnostics (spec #4).

**Why this split:** the user paused execution because token-metrics work is
modifying `internal/contextpack`. Scoping this subsystem to *not* touch
`contextpack` lets the whole index + active search tool be built and merged
safely now; the passive read-through lands once that work settles.

## Motivation

Symbol search today is lexical and Go-only. Developers ask questions like
"where do we resolve the embedding provider" that no substring match answers.
Semantic search over the codebase — local-first, via the nomic-embed-text
embedder from subsystem #1 — closes that gap. This subsystem delivers working
semantic search the model can call (`codebase_search`), while leaving the
passive "inject relevant code into every context pack" behavior to a later spec.

## Data model (SQLite)

Both tables are added to the idempotent `schema` const in
`internal/db/migrations.go` (`CREATE TABLE IF NOT EXISTS`) — no additive-column
migration needed.

```sql
CREATE TABLE IF NOT EXISTS chunks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    file_path   TEXT NOT NULL,
    file_hash   TEXT NOT NULL,          -- files.hash at index time; drives the diff
    kind        TEXT NOT NULL,          -- 'code' | 'doc'
    symbol_name TEXT,                   -- denormalized, nullable (display/filter)
    start_line  INTEGER NOT NULL,
    end_line    INTEGER NOT NULL,
    content     TEXT NOT NULL,          -- the enriched text that was embedded
    token_count INTEGER NOT NULL,
    created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chunks_project ON chunks(project_id);
CREATE INDEX IF NOT EXISTS idx_chunks_project_file ON chunks(project_id, file_path);

CREATE TABLE IF NOT EXISTS embeddings (
    chunk_id INTEGER NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    model    TEXT NOT NULL,
    dim      INTEGER NOT NULL,
    vector   BLOB NOT NULL,             -- float32 little-endian, dim*4 bytes
    PRIMARY KEY (chunk_id)
);
```

**Deliberate deviation from the umbrella:** the umbrella sketched a `symbol_id`
FK into `symbols`. But `db.SaveSymbols` does a full delete-and-reinsert per
index, so symbol row IDs churn every run and an FK would dangle. Instead the
chunk **denormalizes** `symbol_name` + `start_line`/`end_line`, which are stable
across re-indexing and sufficient for display and filtering.

The `vector` blob is `dim` little-endian float32 values (`dim*4` bytes),
encoded/decoded by helpers unit-tested for round-trip.

## Write path

New package `internal/index/` (the umbrella's write-path home).

### Chunker — `internal/index/chunker.go`

`func Chunk(file repo.ScannedFile, symbols []db.Symbol) []Chunk` — a pure
function (no DB, no network), so it is fully table-testable.

```go
type Chunk struct {
    Kind       string // "code" | "doc"
    SymbolName string // "" when none
    StartLine  int
    EndLine    int
    Content    string // the enriched text to embed
    TokenCount int
}
```

Rules:

- **Code file with symbols** — one chunk per `function` / `method` / `type`
  symbol (`import` symbols are skipped). `Content` is **enriched**: a header
  line `// <relative/path> — <receiver.>Signature`, then the leading doc comment
  if present, then the symbol body. `SymbolName`/`StartLine`/`EndLine` come from
  the symbol.
- **Oversized symbol** (> `maxChunkLines`, default **200**) — the body is split
  into consecutive line-windows, each re-carrying the header; line ranges track
  the sub-span.
- **Code file with no extractable symbols** (non-Go today; Go files that failed
  to parse) — fixed line-windows (default **60** lines, **10** overlap) with a
  path header; `Kind='code'`, `SymbolName=""`.
- **Prose file** (`.md` / `.markdown`) — one chunk per heading section (heading
  + content up to the next same-or-higher-level heading); `Kind='doc'`,
  `SymbolName` = the heading text.
- `TokenCount` uses a local rune/4 estimate (kept out of `contextpack` to avoid
  the write path depending on the passive layer).

### Incremental engine — `internal/index/indexer.go`

```go
type Indexer struct {
    db       *db.DB
    embedder embedding.Embedder // nil => embeddings not configured
}

type Stats struct {
    FilesEmbedded int
    ChunksWritten int
    FilesSkipped  int
    FilesPurged   int
}

func (ix *Indexer) Reindex(ctx context.Context, projectID int64, scanned []repo.ScannedFile) (Stats, error)
```

`Reindex` steps:

1. **Graceful-off:** if `embedder == nil`, return zero `Stats` immediately —
   no chunk changes.
2. Load current per-file state: `db.ChunkedFiles(projectID)` →
   `map[string]FileChunkState{ FileHash, Model }`.
3. For each scanned file, **re-index only when stale**: the stored `FileHash`
   differs from the scan's hash, **or** the stored `Model` differs from
   `embedder.Model()` (a model switch invalidates every file). Unchanged files
   are skipped (the cheap steady state).
4. For each stale file: `Chunk(...)` → `embedder.Embed(chunkContents)` (batched
   by subsystem #1) → `db.ReplaceFileChunks(projectID, path, fileHash,
   chunksWithVectors)` (delete this file's chunks + insert new chunks +
   embeddings, one transaction).
5. Files present in `ChunkedFiles` but absent from `scanned` are **purged** via
   `db.DeleteFileChunks(projectID, path)`.
6. Return `Stats`.

### DB helpers — `internal/db/chunks.go`

```go
type FileChunkState struct { FileHash string; Model string }
type ChunkWithVector struct { Chunk /* fields */ ; Model string; Dim int; Vector []float32 }

func (db *DB) ChunkedFiles(projectID int64) (map[string]FileChunkState, error)
func (db *DB) ReplaceFileChunks(projectID int64, filePath, fileHash string, chunks []ChunkWithVector) error
func (db *DB) DeleteFileChunks(projectID int64, filePath string) error
func (db *DB) LoadVectors(projectID int64, model string) ([]VectorRow, error)   // read path
func (db *DB) ChunkGeneration(projectID int64) (count int, maxID int64, err error)

func encodeVector(v []float32) []byte
func decodeVector(b []byte) []float32
```

`ReplaceFileChunks` locks per project (mirroring `SaveSymbols`) and runs in a
transaction. `VectorRow` carries `{ ChunkID, FilePath, StartLine, EndLine,
Content, Vector []float32 }`.

### `repo.index` wiring

`internal/tools/native/repo_index.go` constructs an `Indexer` and calls
`Reindex` **after** the existing file+symbol save, reusing the `scanned` slice
it already produced:

- Embedder is resolved via `router.ResolveEmbedding()` →
  `embedding.NewFromConfig(...)`. `ErrEmbeddingNotConfigured` (or any resolution
  error) → `embedder = nil` (graceful-off), logged at debug, not surfaced as a
  tool failure.
- The tool's output summary gains one line: `Embedded N files (M chunks)` — or
  `Semantic index: not configured` when off.
- No other `repo.index` behavior changes; nothing in `contextpack` changes.

The tool set (`internal/tools/native`) gains the `routing.Router` (or a
resolved embedder factory) as a dependency so the handler can build the
embedder. This is additive wiring in the native tool layer only.

## Read path

### Retrieval `Source` — `internal/retrieval/retrieval.go`

The umbrella's spine, introduced here with a **single** implementation. No
fusion function yet (fusion + git-churn belong to the later passive spec).

```go
type Candidate struct {
    FilePath   string
    StartLine  int
    EndLine    int
    Content    string
    Score      float64 // cosine similarity, 0..1
    SourceName string  // "semantic"
}

type Query struct {
    Text       string
    Limit      int    // top-K; default 10
    PathPrefix string // optional filter, "" = all
}

type Source interface {
    Name() string
    Retrieve(ctx context.Context, q Query) ([]Candidate, error)
}
```

### Semantic source — `internal/retrieval/semantic.go`

`semanticSource{ db, embedder, projectID }` implements `Source`:

1. Embed `q.Text` with the same embedder → query vector.
2. Read candidate vectors from a **lazily-refreshed in-memory cache**. The cache
   holds the project's `VectorRow`s and reloads (`db.LoadVectors`) only when the
   cheap generation signal `db.ChunkGeneration(projectID)` → `(count, maxID)`
   changes. This avoids re-reading tens of MB of blobs on every query while
   staying correct after a re-index.
3. Brute-force cosine of query-vs-all (skipping rows whose `FilePath` fails
   `PathPrefix`), partial-select the top `Limit`.
4. Return `Candidate`s (`SourceName = "semantic"`), highest score first.

Cosine and top-K selection are pure Go over slices, unit-tested with hand-seeded
vectors and no DB.

### `codebase_search` tool — `internal/tools/native/codebase_search.go`

- **Name** `codebase_search` — the Cursor/Kilo-style verb models already know.
- **Schema** `{ "query": string (required), "limit"?: integer, "path"?: string }`,
  `additionalProperties: false`.
- **Risk** `RiskReadOnly`.
- **Handler** resolves the embedder via `ResolveEmbedding`, builds a
  `semanticSource`, calls `Retrieve`, and formats each hit as
  `` `path:start-end` (score 0.xx) `` followed by the snippet, ranked.
- **Graceful-off — three cases, each a friendly `ToolResult` (not an error):**
  - `ErrEmbeddingNotConfigured` → "Semantic search is unavailable: no embedding
    model configured. Set an `embedding` role in your profile to enable it."
  - Empty index (`ChunkGeneration` count 0) → "No semantic index yet — run
    `repo.index` to build it."
  - Zero hits → "No semantic matches for that query."

## Testing strategy

- **Chunker** (`chunker_test.go`) — table tests: a Go file with
  functions/methods/types → per-symbol enriched chunks (assert header + doc +
  body, `SymbolName`, line ranges); an oversized symbol splits into windows; a
  parse-failed/non-Go file → line-windows with `SymbolName=""`; a markdown file
  → one chunk per heading section with `Kind='doc'`.
- **Engine** (`indexer_test.go`) — fake `Embedder` + temp DB: first `Reindex`
  embeds all files; a second run with one file's hash changed re-embeds only
  that file (others counted `FilesSkipped`); a removed file is purged; a changed
  `embedder.Model()` re-embeds all; a `nil` embedder is a no-op returning zero
  `Stats`.
- **DB** (`chunks_test.go`) — `encodeVector`/`decodeVector` round-trip (incl.
  empty and negative values); `ReplaceFileChunks` then `ChunkedFiles` reflects
  hash+model; `DeleteFileChunks` removes chunks and cascades embeddings;
  `LoadVectors` and `ChunkGeneration` return expected shapes.
- **Semantic source** (`semantic_test.go`) — seed chunks+vectors, a query vector
  returns nearest chunks in cosine order; `PathPrefix` filters; cache reloads
  after `ChunkGeneration` changes.
- **`codebase_search`** (`codebase_search_test.go`) — configured returns ranked
  hits with `path:line` formatting; each of the three graceful-off cases returns
  its message and no error.

## Open questions handed to implementation

- Default `Limit` (10) and `maxChunkLines` (200) / window size (60/10) — start
  with these, tune if retrieval quality argues otherwise.
- Whether the in-memory vector cache is per-`Indexer`/per-source instance or a
  process-level singleton — default to per-source instance for the single-tool
  read path; revisit when the passive spec adds a second reader.
- Exact enriched-header format (delimiter, whether to include package name) —
  a rendering detail the chunker owns; keep it one line and stable.
