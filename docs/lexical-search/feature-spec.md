# Lexical codebase search — feature spec

Status: approved for implementation (brainstorming, 2026-11-14).
Supersedes nothing. Related: PR #71 (`codex/embedding-fail-soft`), which makes an
unreachable embedding backend non-fatal to the index pass; this spec builds on it.

## Context

`codebase_search` answers "where is retry backoff handled" — a question with no
known identifier. Today it answers that question with **embeddings**: the tool
is registered only when an embedding route resolves
(`internal/tools/native/native.go:303-306`), it embeds the query, and it ranks
chunks by cosine similarity against vectors produced by an external service
(Ollama, or any OpenAI-compatible endpoint).

That makes search contingent on a service the user may not run. The setup cost
is three configuration edits before the tool exists at all: an `[indexing]
embedding_preset`, a `[providers.<name>]` entry, and a `[models.presets]` entry.
Without them the model has no concept-search tool; `repo.search` (literal
regex over files) is not a substitute, and the corpus shows agents reaching for
`shell.run` with `grep` instead.

The deeper structural fact: **there is nothing to search lexically without
embeddings.** Chunk rows and their vectors are written together in one
transaction (`internal/db/chunks.go:74`), `Reindex` returns immediately when
there is no embedder (`internal/index/indexer.go:52`), and the per-file staleness
record is a join of chunks to embeddings (`internal/db/chunks.go:121`). So a
project with no embedding service has no chunk rows at all — not "chunks without
vectors", but no chunks. A lexical index over that corpus is impossible, not
merely unbuilt.

The repository already has the pieces a lexical index needs:

- SQLite FTS5 is compiled in and in use — `generation_turns_fts` is a contentless
  FTS5 table with `bm25()` ranking (`internal/db/migrations.go:210`,
  `internal/db/generations_search.go:46`).
- `retrieval.Source` is an interface with exactly one implementation
  (`internal/retrieval/retrieval.go:26`) — a seam for a second source.
- The chunker already produces symbol-aware units
  (`internal/index/chunker.go:30`) — the right granularity to search.

## Approved design

### Scope

Give `codebase_search` a lexical (BM25) implementation over the same chunks,
used when semantic search cannot answer. Semantic remains the sole source
whenever it can answer; lexical is the floor, not a peer.

The rule, in order, evaluated per call:

1. An embedder is configured and semantic returns hits → **semantic**. Lexical is
   not consulted.
2. An embedder is configured and semantic fails (backend down, timeout,
   dimension mismatch) → **lexical**.
3. An embedder is configured, semantic returns zero hits, and the project holds
   **no vectors for the active model** → **lexical** (semantic had nothing to
   search).
4. An embedder is configured, semantic returns zero hits, and the project holds
   vectors for the active model → semantic's no-match is **authoritative**; the
   tool reports no matches.
5. No embedder is configured → **lexical**.

Case 3 is distinct from case 4 and is the reason for the "no vectors for the
active model" check: "the concept is absent from this codebase" and "this
project was never embedded" must not look the same.

### Schema

One new virtual table and one trigger, both in the `schema` const
(`internal/db/migrations.go`), alongside the existing `generation_turns_fts`:

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS chunk_fts USING fts5(
    search_text,
    content='',
    contentless_delete=1,
    tokenize='unicode61'
);

CREATE TRIGGER IF NOT EXISTS trg_chunks_fts_delete
AFTER DELETE ON chunks
BEGIN
    DELETE FROM chunk_fts WHERE rowid = OLD.id;
END;
```

`rowid` is `chunks.id`. The table is **contentless** (`content=''`), so it
stores only the inverted index — no second copy of the chunk text — and a query
joins back to `chunks` for the display content.

Both statements go **after** the `chunks` table in the `schema` const. SQLite
resolves a trigger's target table when the trigger is created, so a trigger
declared before `chunks` exists fails with `no such table: main.chunks` — and
the whole schema is one `Exec`, so that failure would stop every database from
opening. The `chunks` table is at `internal/db/migrations.go:241`.

`contentless_delete=1` is load-bearing: it permits a plain
`DELETE FROM chunk_fts WHERE rowid = ?`, which is what the trigger needs. The
repo's existing `generation_turns_fts` trigger instead passes `NULL` as the
content, which removes no tokens — see "Out of scope" below.

`tokenize='unicode61'` is the tokenizer, without porter stemming. It lowercases
and splits on non-alphanumeric characters, so `resolve_embedding` indexes as
`resolve` + `embedding`, but it does **not** split camelCase:
`ResolveEmbedding` is a single token.

### Backfill

`chunk_fts` is created by the `schema` const, which `Migrate` runs on every
open, so existing databases get the table and the trigger. But a database that
already indexed a project with embeddings holds chunk rows with **no** FTS
entries — and the chunk phase will not rewrite them, because their file hash
has not changed. Left alone, lexical search would return nothing for exactly
the projects that already have an index.

A migration therefore backfills: for every existing chunk row, insert the
derived search text under that row's id. It runs once, inside the migration
transaction, and is idempotent (`INSERT OR REPLACE` semantics on the same
rowid). The derived text is computed in Go, in the same package as the
migration, so the backfill and the write path cannot disagree about what the
indexed text is.

### Derived search text

Because `unicode61` does not split camelCase, the indexed text is not the chunk
content but a derived form: the content, followed by the split forms of the
identifiers it contains.

For each identifier-like token in the content (letters, digits and underscore),
if it contains a camelCase or PascalCase boundary, append the split parts.
`ResolveEmbedding` appends `Resolve Embedding`; `HTTPClient` appends
`HTTP Client`. Boundaries are lower→upper and upper-run→upper+lower, the
standard camel splitter. `snake_case` needs no help — `unicode61` already splits
it.

So the indexed text for a chunk is the content *plus* the split identifier
forms, which makes both of these match:

- the query `resolve embedding` (two keywords) against a chunk containing only
  `ResolveEmbedding`;
- the query `ResolveEmbedding` (one keyword) against the same chunk, because the
  original token is still in the indexed text.

The derived text is computed in Go at write time and inserted into `chunk_fts`
in the same transaction as the chunk row, so a chunk row and its index entry
cannot diverge. It is never stored: it is recomputed from `chunks.content`
whenever it is needed (the backfill migration, and a rebuild).

### Durable chunks and the vector overlay

Chunk rows and vector rows stop being written together.

**Chunk phase (always).** For every scanned file whose stored chunk hash differs
from the file's current hash — or which has no chunks yet — the chunker produces
chunks and the file's chunk rows are replaced: old rows deleted, new rows
inserted, and one `chunk_fts` row per chunk written in the same transaction.
A file whose hash is unchanged keeps its chunk rows untouched.

**Vector phase (only with an embedder).** For every file whose stored vector
model differs from the active embedder's model — or which has no vectors — the
chunk contents are read back from the database and embedded, and the vector rows
for those chunk ids are written. The chunk rows are not touched.

The vector phase reads from the database rather than from the chunker's
in-memory output, so it behaves identically for a file whose chunks were just
rewritten and for one whose chunks were already current.

Two consequences, both intended:

- A model change re-embeds without re-chunking. Chunk ids stay stable and the
  lexical index does not churn.
- A file's chunk rows exist whether or not vectors do, so the lexical corpus
  exists for every project.

When the chunk phase replaces a file's chunk rows, the old rows' vector rows go
with them: `embeddings.chunk_id` references `chunks(id)` with `ON DELETE
CASCADE`, so replacing chunk rows cannot leave an orphaned vector behind.

Because chunk ids stay stable across a model change, the vector rows for a
chunk are **replaced in place** (the `embeddings` table keeps `PRIMARY KEY
(chunk_id)` and its `model` column — no schema change). The semantic source
already handles this: it reloads fully when `model != cacheModel`
(`internal/retrieval/semantic.go:69`).

### Per-file state

The staleness record is no longer a join of chunks to embeddings — a file with
chunks but no vectors would vanish from it. One query returns both facts per
file, over chunks with a LEFT JOIN to embeddings:

- the file's chunk hash (`chunks.file_hash`);
- the model of its stored vectors, empty when it has none.

The indexer compares the hash against the scanned file's hash (chunk phase) and
the model against the embedder's model (vector phase).

### Retrieval layer

`internal/retrieval/lexical.go` adds `LexicalSource`, implementing the existing
`retrieval.Source` interface. The interface is unchanged.

The FTS5 query lives in `internal/db`, beside the existing `ftsPhrase` helper
(`internal/db/generations_search.go:112`), so the tokenizer policy stays in one
place. The retrieval layer maps hits to `Candidate`s.

### Query construction

The tool's `query` is natural language, so it is reduced to keywords before it
reaches FTS5:

1. Lowercase; split on whitespace and punctuation.
2. Drop stopwords (a small English list: `the`, `a`, `an`, `and`, `or`, `of`,
   `to`, `in`, `is`, `are`, `was`, `were`, `be`, `how`, `what`, `where`, `which`,
   `who`, `why`, `do`, `does`, `did`, `i`, `we`, `you`, `it`, `this`, `that`,
   `with`, `for`, `on`, `at`, `by`, `from`, `as`, `if`, `then`, `than`, `so`,
   `but`, `not`, `no`, `my`, `our`, `your`, `their`).
3. Drop tokens shorter than two characters.
4. Quote each keyword (`"retry"`), escaping embedded double quotes by doubling
   them — the same treatment `ftsPhrase` gives a whole query. Quoting keeps
   FTS5 operators inside a term literal.

Then:

- **AND pass.** `"kw1" AND "kw2" AND ...`. An FTS5 query with several bare
  terms is implicitly AND, but the explicit form is clearer and lets the OR
  pass differ.
- **OR pass.** If the AND pass returns no rows and there is more than one
  keyword, retry with `"kw1" OR "kw2" OR ...`.

Ordering is `ORDER BY bm25(chunk_fts)` — bm25 is negative and lower is better,
so ascending order is best-first. The `Candidate.Score` convention is
higher-is-better (the semantic source uses cosine similarity), so the reported
score is the negated bm25.

A query with no keywords (all stopwords) builds no MATCH expression and returns
no hits, rather than issuing an invalid query.

Both passes filter by `chunks.project_id` and by the path prefix, and take the
limit. The prefix is a plain prefix test (`instr(c.file_path, ?) = 1`), not a
`LIKE` pattern, so a path containing `%` or `_` cannot widen the filter.

### Tool layer

`codebase_search` is registered unconditionally.

Its handler:

1. Resolves the embedder.
2. With no embedder → lexical.
3. With an embedder → runs semantic.
   - Semantic errors (backend down, timeout, dimension mismatch) → lexical, and
     the result says semantic was unavailable.
   - Semantic returns hits → semantic.
   - Semantic returns no hits → the project is checked for vectors of the
     active model.
     - No vectors for that model → lexical (semantic had nothing to search).
     - Vectors for that model → semantic's no-match is authoritative; the tool
       reports no matches.

The vector count is asked only on the zero-hit path, where it can change the
answer, so the common case — semantic returns hits — pays nothing for it.

The tool renders results the same way it does today, with the source named in
the summary — `N matches (semantic)` or `N matches (lexical)` — so the model
can tell a conceptual ranking from a keyword ranking. When semantic failed and
lexical answered, the content says so.

The "no index yet" content path applies when the project has no chunks at all.

### Interfaces

The names and shapes below are fixed; later tasks consume them without
renaming.

```go
// internal/db/chunks.go

// ChunkFileState is a file's stored chunk hash and the model of the vectors
// written for it.
type ChunkFileState struct {
	FileHash string // chunks.file_hash
	Model    string // embeddings.model; empty when the file has no vectors
}

// ChunkFileStates returns each file's chunk hash and vector model. A file
// with chunks but no vectors appears with an empty Model.
func (db *DB) ChunkFileStates(projectID int64) (map[string]ChunkFileState, error)

// ReplaceFileChunks replaces a file's chunk rows and their lexical index
// entries, in one transaction. It writes no vectors: the derived search text
// is computed here, from the chunk content.
func (db *DB) ReplaceFileChunks(projectID int64, filePath, fileHash string, chunks []Chunk) error

// SetVectors writes vector rows for the given chunk ids, replacing any rows
// already stored for them. A model change reuses the same chunk ids with a
// new model.
func (db *DB) SetVectors(projectID int64, model string, ids []int64, vectors [][]float32) error

// CountModelVectors returns the number of vectors a project holds for a
// model. Zero means semantic search has nothing to search.
func (db *DB) CountModelVectors(projectID int64, model string) (int, error)

// internal/db/lexical.go

// LexicalHit is one chunk matched by a lexical search.
type LexicalHit struct {
	FilePath  string
	StartLine int
	EndLine   int
	Content   string
	Score     float64 // negated bm25; higher is better
}

// SearchChunksLexical searches a project's chunk index with FTS5 bm25. It
// reduces the query to keywords, matches them with AND, and retries with OR
// when the AND pass returns nothing. A limit of zero or less means ten, as
// in the semantic source.
func (db *DB) SearchChunksLexical(projectID int64, query, pathPrefix string, limit int) ([]LexicalHit, error)

// internal/retrieval/lexical.go

// LexicalSource is a retrieval.Source backed by the FTS5 index.
type LexicalSource struct { /* db, projectID */ }

func NewLexicalSource(database *db.DB, projectID int64) *LexicalSource
```

`ChunkWithVector` is superseded by the split and is removed once nothing uses
it. `ChunkedFiles` is superseded by `ChunkFileStates` for the same reason — its
join cannot see a file that has chunks but no vectors — and is removed once
nothing uses it.

The doctor row's two counts come from existing helpers, not new ones:
`CountModelVectors` for "N embedded chunks (model)", and the chunk count
`ChunkGeneration` already returns (`internal/db/chunks.go:236`) for "M chunks
indexed lexically".

### Report counters

`index.Report` gains no fields, but two of its counters change meaning
because chunks are now written without vectors:

- `ChunksWritten` counts chunk rows written, including for a project with no
  embedder.
- `FilesEmbedded` counts files whose vectors were written, and stays zero for a
  project with no embedder.

The report is published on the host's report broker
(`internal/app/hosts.go:541`), whose only subscriber today is a test, so this
is a description of intent rather than a user-visible change.

### What does not change

- The agent's passive semantic context injection stays semantic-only. The
  lexical source is a tool surface and never enters a context pack
  (`internal/agent/route.go:448`).
- `retrieval.Source` keeps its shape; the semantic source, its cache, and its
  model-change reload are untouched.
- `repo.search` is untouched.
- No new user-facing configuration. Lexical is not a knob.
- No new dependency: FTS5 is already in the SQLite build.

## Error handling

**Index time.** The two phases fail independently and neither failure loses the
other phase's work.

- A chunk-phase failure (DB error, FTS insert error) is recorded as a warning
  and the pass continues with the remaining files. A file whose chunk write
  failed keeps its previous chunks.
- A vector-phase failure (embedder down, dimension mismatch) is a warning. The
  chunk rows are already durable, so the lexical index is intact and the
  semantic index is simply behind. PR #71 is what makes this non-fatal to the
  pass.
- Chunk rows and their `chunk_fts` rows are written in one transaction, so a
  lexical-index failure cannot leave a chunk without a search row.

**Query time.**

- A semantic failure falls back to lexical, and the result says so.
- A lexical failure (DB error) is a real error, returned to the model as an
  error rather than swallowed.
- A query with no keywords returns no matches; no invalid MATCH expression is
  issued.
- A project with no chunks returns content ("no index yet"), not an error.

## User-visible wording

The semantic state is still reported — it is useful — but no surface may imply
that search is broken, because search now works.

| Surface | Today | Becomes |
|---|---|---|
| Startup warning (`internal/app/embedding_warning.go:22`) | "Semantic search is enabled (indexing.use_embeddings) but no embedding preset is configured. Set one with: [indexing] embedding_preset = '<provider>/<model>' — or via /settings → Indexing." | Keeps the diagnosis and the fix, and adds that code search works without it: "… Set one with: [indexing] embedding_preset = '<provider>/<model>' — or via /settings → Indexing. Code search works without it, using lexical matching." |
| `/doctor` Embeddings row (`internal/acp/service/intelligence.go:59`) | "semantic search disabled — set indexing.embedding_preset" | Reports the semantic state only, and says lexical search is available, with the indexed chunk count: "semantic off — code search uses lexical matching" / "semantic on — N embedded chunks (model), M chunks indexed lexically". |
| Tool content (`internal/tools/native/codebase_search.go:42`) | "Semantic search is not configured: no embedding model configured. Set an `embedding` role in your profile to enable it." | Replaced by the lexical path; the "not configured" content no longer occurs, because search is configured. |
| Tool content (`internal/tools/native/codebase_search.go:49`) | "No semantic index yet — the background indexer builds it once embeddings are configured." | "No index yet — the background indexer builds it." |
| Tool description (`internal/tools/native/codebase_search.go:25`) | "Semantic search over the indexed codebase… For literal strings use repo.search; this is for concepts." | "Search the indexed codebase by concept — semantically when an embedding model is configured, lexically otherwise. For literal strings use repo.search; this is for concepts." |
| `docs/tools/read-search.md:119-144` | "Registered only when an embedding route resolves (`native.go:230-233`)" | Registration is unconditional; the failure modes and the registration note are rewritten. The cited line numbers are already stale (the gate is at `internal/tools/native/native.go:303-306`) and are corrected. |

The four `embeddingStartupWarning` branches (not configured / preset not found /
remote blocked / other) each keep their own diagnosis and gain the same
lexical sentence.

## Decisions and trade-offs

1. **Fallback only, not fusion.** Lexical answers when semantic cannot; the two
   never mix in one result set. *Trade-off:* a query that semantically matches
   nothing but lexically matches something returns no matches, even though a
   lexical hit existed. Accepted: a lexical hit for an absent concept is
   loosely-related noise, and blending the two scores would need a fusion
   policy and a normalisation rule for two incomparable scales (negative bm25
   versus cosine in [0,1]).
2. **Always persist chunks.** Every project stores chunk rows and an FTS5 index,
   embedder or not. *Trade-off:* storage and write cost for every project,
   including those that never use embeddings. Accepted, because the fallback
   only works if the corpus exists before anyone needs it.
3. **`codebase_search` does double duty.** One tool, always registered, choosing
   its source per call. *Trade-off:* the tool's behaviour depends on project
   state, so its description must name both modes. Accepted: the model gets one
   search tool instead of two near-identical ones it must choose between.
4. **Durable chunks, vectors as an overlay.** Chunk rows outlive vectors; a
   model change re-embeds without re-chunking. *Trade-off:* the index pass now
   has two phases with different staleness rules, and the vector write path
   must key on chunk ids rather than re-deriving chunks. Accepted: it keeps
   chunk ids stable, so the lexical index does not churn on every model change.
5. **Tool-only surface.** Passive context injection stays semantic-only.
   *Trade-off:* a project with no embedder gets no automatic context injection,
   even though it now has a lexical index. Accepted: BM25 hits on a
   goal-shaped query are noisier than semantic ones, and injecting them
   unbidden spends context budget on every turn.
6. **AND, then OR.** *Trade-off:* the AND pass can miss a document that
   mentions the concept's parts separately; the OR pass then returns
   loosely-related hits instead of nothing. Accepted: precision first, recall as
   a fallback.
7. **Derived search text with split identifiers.** *Trade-off:* the indexed text
   is larger than the content, and the derivation is a heuristic — it splits
   identifiers that look camelCase. Accepted: `unicode61` alone would make
   `ResolveEmbedding` unsearchable by "resolve" or "embedding", which is most of
   what a code search is for.
8. **Contentless FTS5 keyed by chunk id.** *Trade-off:* a contentless table
   cannot serve its own content, so every query joins back to `chunks`; and
   `contentless_delete=1` is required for the trigger's plain DELETE to
   actually remove tokens. Accepted: it stores no second copy of the chunk
   text.

## Testing

**Database layer** (`internal/db`) — the FTS5 index is new machinery with a
known trap, so it carries the most coverage:

- A chunk write creates a searchable row; a chunk delete removes it (the
  `NULL`-content form does not; the plain-`DELETE` form does — this test is what
  keeps that honest).
- Deleting a project cascades to chunks and clears the index (the pool sets
  `PRAGMA foreign_keys = ON`, `internal/db/dbpool.go:20`).
- Chunk rows survive without vectors, and vectors arrive separately.
- A model change replaces vectors without rewriting chunk rows, so chunk ids and
  index rows are unchanged.
- The AND-then-OR rule: a query matching nothing under AND still matches under
  OR; a query with no keywords returns nothing rather than erroring.
- Path-prefix filtering, and per-project isolation (a term from another project
  never matches).
- The derived search text makes `resolve embedding` match `ResolveEmbedding`.

**Index layer** (`internal/index`):

- The chunk phase runs with no embedder and leaves chunks and index rows behind.
- Chunk rows and index rows are written in one transaction, so a failure cannot
  leave them divergent.
- A vector-phase failure leaves the chunks intact.
- A model change re-embeds without re-chunking.

**Retrieval layer** (`internal/retrieval`):

- `LexicalSource` returns ranked candidates with `SourceName: "lexical"`.
- Score ordering is higher-is-better.

**Tool layer** (`internal/tools/native`):

- The tool is registered with no embedding route.
- Each of the five fallback rules, one case each: semantic hits win; a semantic
  error falls back; an unconfigured embedder uses lexical; a semantic no-match
  over a populated index stays authoritative; no vectors for the active model
  falls back.

**Wording:**

- `internal/app/embedding_warning_test.go` and
  `internal/acp/service/intelligence_test.go` assert the new text.
- `internal/app/tools_doc_test.go` keeps the docs-drift check honest.

**One test inverts.** `TestCodebaseSearchRegisteredOnlyWithEmbeddings`
(`internal/tools/native/codebase_search_test.go:122`) asserts the opposite of
the new rule and becomes "registered without an embedding route".

## Touched code seams

| Seam | Change |
|---|---|
| `internal/db/migrations.go` | `chunk_fts` + delete trigger in the `schema` const; a backfill migration |
| `internal/db/chunks.go` | chunk write split from vector write; per-file state query; vector count by model; lexical search |
| `internal/db/lexical.go` (new) | derived search text, keyword extraction, MATCH construction, the lexical query |
| `internal/index/indexer.go` | chunk phase always; vector phase conditional |
| `internal/index/run.go` | per-file warnings collected instead of aborting the pass |
| `internal/retrieval/lexical.go` (new) | `LexicalSource` |
| `internal/tools/native/codebase_search.go` | source selection; always available |
| `internal/tools/native/native.go` | unconditional registration |
| `internal/app/embedding_warning.go` | reworded |
| `internal/acp/service/intelligence.go` | reworded doctor row |
| `docs/tools/read-search.md` | rewritten `codebase_search` section |

## Assumptions

Choices made without user input, where the repository could not answer:

- **Stopword list.** A small English list, given above, lives beside the FTS5
  query code in `internal/db`. It is deliberately not a large list: over-filtering
  removes meaningful code words.
- **Minimum keyword length.** Two characters. Single-character terms match
  almost nothing in code and only add noise to the AND pass.
- **Derived-text placement.** The split identifier forms are appended to the
  content rather than replacing it, so a query for the literal identifier still
  matches.
- **Prefix matching.** `instr(file_path, ?) = 1`, matching the semantic
  source's `strings.HasPrefix` behaviour (`internal/retrieval/semantic.go:152`)
  without `LIKE` wildcard semantics.
- **Chunk-row replacement on content change.** A file whose hash changed has its
  chunk rows deleted and reinserted, so its chunk ids change. That preserves
  the monotonic-chunk-id invariant the semantic source's incremental cache
  depends on (`internal/db/chunks.go:173`).
- **Vector replacement in place.** Vector rows for a chunk id are replaced
  (same `PRIMARY KEY (chunk_id)`, new `model`), so the `embeddings` table keeps
  its schema and the one-model-at-a-time invariant.
- **Score normalisation.** The reported lexical score is the negated bm25, so
  higher is better and the tool's `(score %.2f)` rendering stays meaningful.
- **Summary suffix.** `N matches (semantic)` / `N matches (lexical)`.

## Explicit non-goals

- No hybrid fusion, score blending, or side-by-side result sets.
- No lexical results in passive context injection.
- No change to `retrieval.Source`, the semantic source, or its cache.
- No change to `repo.search`.
- No new user-facing configuration; lexical is not a knob.
- No new dependency, and no embedding runtime or model weights bundled into
  Marshal.
- No second chunk store: the lexical index is an index over the same chunk rows.
- No change to the archive-search FTS5 table (see below).

## Out of scope, but worth reporting

`generation_turns_fts` — the FTS5 table behind archived-turn search — has a
delete trigger that passes `NULL` as the content
(`internal/db/migrations.go:222`). On a contentless FTS5 table that form removes
no tokens: the row keeps matching. The search joins to `generation_turns`, so
deleted turns do not surface as results, but the index grows without bound and
its statistics drift. This is a separate bug in a separate table; this spec does
not touch it, and it should be filed on its own.

## Open questions

None. Every decision above is settled.
