# Lexical codebase search — implementation plan

Date: 2026-11-14
Status: approved for execution; not yet executed.
Base: `011113b4` (`main`, with PR #71 merged). This plan and its specification
travel together in PR #72; implement on a branch cut from `main` after it
merges.
Specification: [feature-spec.md](../feature-spec.md).

**Goal:** `codebase_search` answers concept queries on a project with **no embedding
model, no daemon and no weights** — a lexical BM25 fallback over the same chunks —
while semantic search stays the sole source whenever it can answer.

**Architecture:** Chunks become durable (persisted with or without vectors) and
vectors become a per-model overlay written separately, so a model change re-embeds
without re-chunking and the lexical index never churns. The lexical index is a
contentless FTS5 table keyed by chunk id, maintained by a trigger, holding derived
search text so camelCase identifiers are findable by their parts. `codebase_search`
is always registered and names its source per call.

**Non-goals (from the spec):** no hybrid fusion, score blending, or side-by-side
result sets; no lexical results in the agent's passive context injection; no change
to `retrieval.Source`, the semantic source, or its cache; no change to `repo.search`;
no new user-facing configuration; no new dependency; no second chunk store.

**Assumptions:**
- The bundled SQLite (3.53.2) supports `contentless_delete=1` — verified by probe.
- The `chunks` table and the `embeddings` table keep their schemas. `embeddings`
  keeps `PRIMARY KEY (chunk_id)`, so a project still holds one model at a time.
- `internal/db` gains no new dependency. FTS5 is already compiled in.
- The semantic source is never held across an index pass (see the spec's
  "The semantic source's cache"). Both production call sites build a fresh
  `SemanticSource` per query, so the cache is cold at every query.

**Working method:** each task is one reviewable unit — run its Verify, then commit
as `lexical-search: task N — <title>`. Tasks run sequentially unless marked
parallel-safe. Every anchor below was checked against the tree at `011113b4`.

---

## Contracts

The names and shapes below are fixed; later tasks consume them without
renaming. Task 1 owns the `internal/db/lexical.go` block, Task 2 the
`internal/db/chunks.go` blocks, and Task 4 the `internal/retrieval/lexical.go`
one.

```go
// internal/db/lexical.go

// LexicalHit is one chunk matched by a lexical search.
type LexicalHit struct {
	FilePath  string
	StartLine int
	EndLine   int
	Content   string
	Score     float64 // negated bm25: higher is better
}

// SearchChunksLexical searches a project's chunk index with FTS5 bm25. It
// reduces the query to keywords, matches them with AND, and retries with OR
// when the AND pass returns nothing. A limit of zero or less means ten, as
// in the semantic source.
func (db *DB) SearchChunksLexical(projectID int64, query, pathPrefix string, limit int) ([]LexicalHit, error)
```

```go
// internal/db/chunks.go

// ChunkFileState is a file's stored chunk state: the hash its chunks were
// written from, and the model of the vectors written for it.
type ChunkFileState struct {
	FileHash string
	Model    string // empty when the file has no vectors
}

// ChunkFileStates returns each file's chunk hash and vector model. A file
// with chunks but no vectors appears with an empty Model.
func (db *DB) ChunkFileStates(projectID int64) (map[string]ChunkFileState, error)

// ChunkRow is a stored chunk row with its id, for the vector phase.
type ChunkRow struct {
	ID      int64
	Content string
}

// FileChunkRows returns a file's stored chunk rows, in id order.
func (db *DB) FileChunkRows(projectID int64, filePath string) ([]ChunkRow, error)

// SetVectors writes vector rows for the given chunk ids, replacing any rows
// already stored for them. ids and vectors are parallel: ids[i] receives
// vectors[i].
func (db *DB) SetVectors(projectID int64, model string, ids []int64, vectors [][]float32) error

// CountModelVectors returns the number of vectors a project holds for a
// model. Zero means semantic search has nothing to search.
func (db *DB) CountModelVectors(projectID int64, model string) (int, error)
```

```go
// internal/db/chunks.go — the chunk write loses its vector argument.

// ReplaceFileChunks replaces a file's chunk rows and their lexical index
// entries, in one transaction. It writes no vectors: the derived search text
// is computed here, from the chunk content.
func (db *DB) ReplaceFileChunks(projectID int64, filePath, fileHash string, chunks []Chunk) error
```

```go
// internal/retrieval/lexical.go

// LexicalSource is a retrieval.Source backed by the FTS5 index over chunks.
type LexicalSource struct { /* db, projectID */ }

// NewLexicalSource returns a LexicalSource over a project's chunk index.
func NewLexicalSource(database *db.DB, projectID int64) *LexicalSource
```

`LexicalSource` implements the existing `retrieval.Source` interface
(`internal/retrieval/retrieval.go:26`) — `Name() string` and
`Retrieve(ctx context.Context, q Query) ([]Candidate, error)` — with
`Name()` returning `"lexical"`.

---

## Task 1: the lexical index in the database

**Goal:** a project has a searchable lexical index over its chunks — the FTS5 table
exists, chunk writes populate it, a chunk delete removes its entry, and existing
chunk rows are backfilled.

**Files:**
- `internal/db/migrations.go` — schema const + migration
- `internal/db/lexical.go` (new) — derived text, query
- `internal/db/chunks.go` — chunk write populates the index
- `internal/db/lexical_test.go` (new)

**Steps:**

1. **Schema.** In the `schema` const, immediately **after** the `chunks` table
   (`internal/db/migrations.go:241`) and before `embeddings`, add:

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

   **The order is load-bearing.** The whole schema is one `Exec`
   (`internal/db/db.go:104`); SQLite resolves a trigger's target table when the
   trigger is created, so a trigger declared before `chunks` exists fails with
   `no such table: main.chunks` and no database opens at all. Verified by probe.

2. **Migration.** Append a backfill to the `migrations` list in `init()`
   (`internal/db/migrations.go:309-314`) — it becomes version 4 — and add the
   function next to the other migrations:

   ```go
   // migrateChunkLexicalIndex (version 4) populates chunk_fts for the chunk
   // rows written before the lexical index existed. Their file hashes have
   // not changed, so the chunk phase will not rewrite them.
   func migrateChunkLexicalIndex(tx *sql.Tx) error {
       rows, err := tx.Query(`SELECT id, content FROM chunks`)
       if err != nil {
           return fmt.Errorf("read chunks for lexical backfill: %w", err)
       }
       type row struct {
           id      int64
           content string
       }
       var pending []row
       for rows.Next() {
           var r row
           if err := rows.Scan(&r.id, &r.content); err != nil {
               rows.Close()
               return fmt.Errorf("scan chunk for lexical backfill: %w", err)
           }
           pending = append(pending, r)
       }
       if err := rows.Err(); err != nil {
           rows.Close()
           return fmt.Errorf("read chunks for lexical backfill: %w", err)
       }
       rows.Close()
       for _, r := range pending {
           if _, err := tx.Exec(`DELETE FROM chunk_fts WHERE rowid = ?`, r.id); err != nil {
               return fmt.Errorf("clear lexical index for chunk %d: %w", r.id, err)
           }
           if _, err := tx.Exec(`INSERT INTO chunk_fts(rowid, search_text) VALUES (?, ?)`, r.id, deriveSearchText(r.content)); err != nil {
               return fmt.Errorf("backfill chunk %d into chunk_fts: %w", r.id, err)
           }
       }
       return nil
   }
   ```

   Collect the rows before inserting: `tx` is one connection, so an insert while
   the select's rows are open fails.

   `internal/db/migrations.go` imports only `database/sql` today — add `fmt` to
   the import block for the `fmt.Errorf` calls.

3. **New file `internal/db/lexical.go`** with the derived text, the keyword
   extraction, the MATCH construction, and the query. Verbatim:

   ```go
   package db

   import (
       "fmt"
       "strings"
       "unicode"
   )

   // LexicalHit is one chunk matched by a lexical search.
   type LexicalHit struct {
       FilePath  string
       StartLine int
       EndLine   int
       Content   string
       Score     float64 // negated bm25: higher is better
   }

   // lexicalStopwords are dropped from a natural-language query before it
   // reaches FTS5. The list is deliberately small: over-filtering removes
   // meaningful code words.
   var lexicalStopwords = map[string]bool{
       "the": true, "a": true, "an": true, "and": true, "or": true,
       "of": true, "to": true, "in": true, "is": true, "are": true,
       "was": true, "were": true, "be": true, "how": true, "what": true,
       "where": true, "which": true, "who": true, "why": true, "do": true,
       "does": true, "did": true, "i": true, "we": true, "you": true,
       "it": true, "this": true, "that": true, "with": true, "for": true,
       "on": true, "at": true, "by": true, "from": true, "as": true,
       "if": true, "then": true, "than": true, "so": true, "but": true,
       "not": true, "no": true, "my": true, "our": true, "your": true,
       "their": true,
   }

   // deriveSearchText returns the text indexed for a chunk: its content plus
   // the split forms of the camelCase identifiers it contains, so a query for
   // "resolve embedding" matches "ResolveEmbedding". The unicode61 tokenizer
   // splits on non-alphanumerics but not on case boundaries, so the split
   // forms are added here rather than in the tokenizer.
   func deriveSearchText(content string) string {
       var extra []string
       for _, word := range identifierWords(content) {
           parts := splitIdentifier(word)
           if len(parts) < 2 {
               continue
           }
           extra = append(extra, parts...)
       }
       if len(extra) == 0 {
           return content
       }
       return content + " " + strings.Join(extra, " ")
   }

   // identifierWords returns the identifier-like runs in text: letters, digits
   // and underscores, keeping only those that contain a letter.
   func identifierWords(text string) []string {
       var out []string
       var cur strings.Builder
       flush := func() {
           if cur.Len() == 0 {
               return
           }
           word := cur.String()
           cur.Reset()
           if strings.ContainsFunc(word, unicode.IsLetter) {
               out = append(out, word)
           }
       }
       for _, r := range text {
           if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
               cur.WriteRune(r)
               continue
           }
           flush()
       }
       flush()
       return out
   }

   // splitIdentifier splits a camelCase or PascalCase identifier into its
   // words: "ResolveEmbedding" becomes ["Resolve", "Embedding"] and
   // "HTTPClient" becomes ["HTTP", "Client"].
   func splitIdentifier(id string) []string {
       runes := []rune(id)
       var parts []string
       start := 0
       for i := 1; i < len(runes); i++ {
           cur, prev := runes[i], runes[i-1]
           if !unicode.IsUpper(cur) {
               continue
           }
           if unicode.IsLower(prev) || unicode.IsDigit(prev) {
               parts = append(parts, string(runes[start:i]))
               start = i
               continue
           }
           // An uppercase run followed by a lowercase letter ends a word:
           // HTTPClient is HTTP plus Client.
           if i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
               parts = append(parts, string(runes[start:i]))
               start = i
           }
       }
       return append(parts, string(runes[start:]))
   }

   // lexicalTerms reduces a natural-language query to FTS5 terms: lowercased
   // alphanumeric runs, with stopwords and one-character tokens removed.
   func lexicalTerms(query string) []string {
       fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
           return !unicode.IsLetter(r) && !unicode.IsDigit(r)
       })
       var terms []string
       seen := map[string]bool{}
       for _, f := range fields {
           if len(f) < 2 || lexicalStopwords[f] || seen[f] {
               continue
           }
           seen[f] = true
           terms = append(terms, f)
       }
       return terms
   }

   // lexicalMatchExpr builds an FTS5 MATCH expression over terms joined by op
   // ("AND" or "OR"). Terms are quoted so FTS5 treats them literally. A term
   // is an alphanumeric run, so it can never contain a quote itself.
   func lexicalMatchExpr(terms []string, op string) string {
       quoted := make([]string, 0, len(terms))
       for _, t := range terms {
           quoted = append(quoted, `"`+t+`"`)
       }
       return strings.Join(quoted, " "+op+" ")
   }

   // SearchChunksLexical searches a project's chunk index with FTS5 bm25.
   func (db *DB) SearchChunksLexical(projectID int64, query, pathPrefix string, limit int) ([]LexicalHit, error) {
       if limit <= 0 {
           limit = 10
       }
       terms := lexicalTerms(query)
       if len(terms) == 0 {
           return nil, nil
       }
       hits, err := db.searchChunkText(projectID, terms, "AND", pathPrefix, limit)
       if err != nil {
           return nil, err
       }
       if len(hits) == 0 && len(terms) > 1 {
           hits, err = db.searchChunkText(projectID, terms, "OR", pathPrefix, limit)
           if err != nil {
               return nil, err
           }
       }
       return hits, nil
   }

   // searchChunkText runs one FTS5 pass over a project's chunks.
   func (db *DB) searchChunkText(projectID int64, terms []string, op, pathPrefix string, limit int) ([]LexicalHit, error) {
       query := `SELECT c.file_path, c.start_line, c.end_line, c.content, bm25(chunk_fts)
                    FROM chunk_fts JOIN chunks c ON c.id = chunk_fts.rowid
                   WHERE chunk_fts MATCH ? AND c.project_id = ?`
       args := []any{lexicalMatchExpr(terms, op), projectID}
       if pathPrefix != "" {
           // A prefix test, not LIKE: a path containing % or _ must not
           // widen the filter.
           query += ` AND instr(c.file_path, ?) = 1`
           args = append(args, pathPrefix)
       }
       query += ` ORDER BY bm25(chunk_fts) LIMIT ?`
       args = append(args, limit)
       rows, err := db.sqlDB.Query(query, args...)
       if err != nil {
           return nil, fmt.Errorf("search chunk text: %w", err)
       }
       defer rows.Close()
       var hits []LexicalHit
       for rows.Next() {
           var h LexicalHit
           var score float64
           if err := rows.Scan(&h.FilePath, &h.StartLine, &h.EndLine, &h.Content, &score); err != nil {
               return nil, fmt.Errorf("scan lexical hit: %w", err)
           }
           // bm25 is negative and lower is better; the score convention is
           // higher is better.
           h.Score = -score
           hits = append(hits, h)
       }
       if err := rows.Err(); err != nil {
           return nil, fmt.Errorf("iterate lexical hits: %w", err)
       }
       return hits, nil
   }
   ```

4. **Chunk write populates the index.** In `ReplaceFileChunks`
   (`internal/db/chunks.go:74`), keep the existing delete-then-insert transaction
   and its per-project lock, and after inserting each chunk row insert its index
   row with the same transaction. The chunk's id is the FTS rowid. Verbatim, the
   body of the function becomes:

   ```go
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
           if _, err := tx.Exec(
               `INSERT INTO chunk_fts(rowid, search_text) VALUES (?, ?)`,
               chunkID, deriveSearchText(c.Content)); err != nil {
               return fmt.Errorf("insert chunk_fts row: %w", err)
           }
       }
       return tx.Commit()
   }
   ```

   This task keeps the existing `[]ChunkWithVector` signature; Task 2 splits it.

5. **Tests** — new file `internal/db/lexical_test.go`, package `db`, using the
   package's existing helpers `newTestDB` (`internal/db/sessions_test.go:256`)
   and `mustCreateProject` (`internal/db/chunks_test.go:9`):

   - `TestChunkWriteIsSearchable` — write one chunk containing
     "retry backoff", search "retry", expect one hit with the right path and
     lines.
   - `TestChunkDeleteRemovesItsIndexEntry` — write a chunk, delete it via
     `DeleteFileChunks`, then search for its unique token; expect zero hits.
     This is the test that keeps the delete form honest: the `NULL`-content
     form of the existing archive trigger removes nothing.
   - `TestCascadedProjectDeleteClearsTheIndex` — write a chunk, delete the
     project row, search for its unique token; expect zero hits. The pool sets
     `PRAGMA foreign_keys = ON` (`internal/db/dbpool.go:20`), so the cascade
     fires and the trigger must remove the index entry.
   - `TestDerivedSearchTextMatchesSplitIdentifiers` — write a chunk whose
     content is "func ResolveEmbedding()", search "resolve embedding"; expect a
     hit.
   - `TestLexicalSearchFallsBackFromAndToOr` — write two chunks, one
     containing only "retry" and one containing only "backoff", so no single
     chunk holds both terms. Search "retry backoff": under AND nothing matches,
     so the query retries with OR and returns both chunks. Assert two hits, and
     that neither came from an AND match.
   - `TestLexicalSearchWithNoKeywordsReturnsNothing` — search "the a of" (all
     stopwords or one character); expect no hits and no error.
   - `TestLexicalSearchFiltersByPathPrefix` — two files, search with a prefix;
     expect only the prefixed one.
   - `TestLexicalSearchIsolatesProjects` — two projects, the same token in each;
     a search for one project must not return the other's chunk.
   - `TestLexicalSearchRanksByBm25` — write two chunks that both contain
     "retry", one of them containing it twice and the other once. Search
     "retry" and assert the chunk with two occurrences comes first, so the
     ranking is bm25's and not insertion order.

**Verify:**
```bash
CGO_ENABLED=1 go build ./... && \
CGO_ENABLED=1 go test ./internal/db/ -run 'Lexical|ChunkWrite|ChunkDelete|Cascaded|Derived' -count=1 -v
```
Expected: build clean; every named test passes.

**Notes for the executor:** the derived text is what the index stores; it is
recomputed from `chunks.content` and never persisted. Do not add a column for it.

---

## Task 2: split the chunk write from the vector write

**Goal:** chunk rows and their lexical index entries are written without vectors,
and vectors are written separately against existing chunk ids.

**Files:**
- `internal/db/chunks.go` — new API, `ChunkWithVector` and `ChunkedFiles` removed
- `internal/db/chunks_test.go` — updated call sites
- `internal/retrieval/semantic_test.go` — updated call sites
- `internal/tools/native/codebase_search_test.go` — updated call site
- `internal/acp/service/intelligence_test.go` — updated call site
- `internal/index/indexer.go` — `persist` adapted (the phase restructure is Task 3)

**Steps:**

1. **In `internal/db/chunks.go`**, change `ReplaceFileChunks` to the
   chunk-only signature from the Contracts, dropping the `embeddings` insert and
   keeping the `chunk_fts` insert:

   ```go
   func (db *DB) ReplaceFileChunks(projectID int64, filePath, fileHash string, chunks []Chunk) error {
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
               `INSERT INTO chunk_fts(rowid, search_text) VALUES (?, ?)`,
               chunkID, deriveSearchText(c.Content)); err != nil {
               return fmt.Errorf("insert chunk_fts row: %w", err)
           }
       }
       return tx.Commit()
   }
   ```

2. **Add the new API** to `internal/db/chunks.go`, beside the chunk functions.
   Verbatim:

   ```go
   // ChunkFileState is a file's stored chunk state: the hash its chunks were
   // written from, and the model of the vectors written for it.
   type ChunkFileState struct {
       FileHash string
       Model    string // empty when the file has no vectors
   }
   // ChunkFileStates returns each file's chunk hash and vector model. A file
   // with chunks but no vectors appears with an empty Model. Unlike the join
   // this replaces, it sees a file that has chunks but no vectors.
   func (db *DB) ChunkFileStates(projectID int64) (map[string]ChunkFileState, error) {
       rows, err := db.sqlDB.Query(
           `SELECT c.file_path,
                   MAX(c.file_hash),
                   CASE WHEN COUNT(e.chunk_id) = COUNT(*) THEN MAX(e.model) ELSE '' END
              FROM chunks c LEFT JOIN embeddings e ON e.chunk_id = c.id
             WHERE c.project_id = ?
             GROUP BY c.file_path`, projectID)
       if err != nil {
           return nil, fmt.Errorf("query chunk file states: %w", err)
       }
       defer rows.Close()
       out := map[string]ChunkFileState{}
       for rows.Next() {
           var path string
           var st ChunkFileState
           if err := rows.Scan(&path, &st.FileHash, &st.Model); err != nil {
               return nil, fmt.Errorf("scan chunk file state: %w", err)
           }
           out[path] = st
       }
       if err := rows.Err(); err != nil {
           return nil, fmt.Errorf("iterate chunk file states: %w", err)
       }
       return out, nil
   }

   // ChunkRow is a stored chunk row with its id, for the vector phase.
   type ChunkRow struct {
       ID      int64
       Content string
   }

   // FileChunkRows returns a file's stored chunk rows, in id order.
   func (db *DB) FileChunkRows(projectID int64, filePath string) ([]ChunkRow, error) {
       rows, err := db.sqlDB.Query(
           `SELECT id, content FROM chunks WHERE project_id = ? AND file_path = ? ORDER BY id`,
           projectID, filePath)
       if err != nil {
           return nil, fmt.Errorf("query file chunk rows: %w", err)
       }
       defer rows.Close()
       var out []ChunkRow
       for rows.Next() {
           var r ChunkRow
           if err := rows.Scan(&r.ID, &r.Content); err != nil {
               return nil, fmt.Errorf("scan file chunk row: %w", err)
           }
           out = append(out, r)
       }
       if err := rows.Err(); err != nil {
           return nil, fmt.Errorf("iterate file chunk rows: %w", err)
       }
       return out, nil
   }

   // SetVectors writes vector rows for the given chunk ids, replacing any rows
   // already stored for them. ids and vectors are parallel: ids[i] receives
   // vectors[i].
   func (db *DB) SetVectors(projectID int64, model string, ids []int64, vectors [][]float32) error {
       if len(ids) != len(vectors) {
           return fmt.Errorf("set vectors: %d ids but %d vectors", len(ids), len(vectors))
       }
       unlock := db.locks.Lock(projectID)
       defer unlock()

       tx, err := db.sqlDB.Begin()
       if err != nil {
           return fmt.Errorf("begin set vectors: %w", err)
       }
       defer tx.Rollback()

       for i, id := range ids {
           if _, err := tx.Exec(`DELETE FROM embeddings WHERE chunk_id = ?`, id); err != nil {
               return fmt.Errorf("delete vector for chunk %d: %w", id, err)
           }
           if _, err := tx.Exec(
               `INSERT INTO embeddings (chunk_id, model, dim, vector) VALUES (?, ?, ?, ?)`,
               id, model, len(vectors[i]), encodeVector(vectors[i])); err != nil {
               return fmt.Errorf("insert vector for chunk %d: %w", id, err)
           }
       }
       return tx.Commit()
   }

   // CountModelVectors returns the number of vectors a project holds for a
   // model. Zero means semantic search has nothing to search.
   func (db *DB) CountModelVectors(projectID int64, model string) (int, error) {
       return db.countRows(
           `SELECT COUNT(*) FROM embeddings e JOIN chunks c ON c.id = e.chunk_id
             WHERE c.project_id = ? AND e.model = ?`, projectID, model)
   }
   ```

   `countRows` already exists (`internal/db/counts.go:43`).

3. **Remove `ChunkWithVector`, `FileChunkState` and `ChunkedFiles`** from
   `internal/db/chunks.go` once nothing uses them (step 5 confirms).
   `ChunkedFiles`' join cannot see a file that has chunks but no vectors, which
   is exactly the state the new design creates, and `FileChunkState` exists
   only to serve it.

4. **Update `internal/index/indexer.go`** so the package still compiles and
   keeps its behaviour. Two places change:

   a. `Reindex`'s state lookup (`internal/index/indexer.go:53`) moves from
      `ix.db.ChunkedFiles(projectID)` to `ix.db.ChunkFileStates(projectID)`.
      The skip test becomes `prev.FileHash == sf.Hash && prev.Model == model`.
      A file with chunks but no vectors now reads an empty `Model`, so it is
      re-chunked and re-embedded rather than skipped — wasteful but correct,
      and Task 3 replaces this logic properly.

   b. `persist` (`internal/index/indexer.go:163`) writes chunk rows and then
      vectors, as two steps. The ids come from `FileChunkRows`, which orders by
      id, and the chunk ids follow insertion order, so `ids[i]` is the chunk
      that `it.chunks[i]` describes and `it.vectors[i]` belongs to.

   ```go
   func (ix *Indexer) persist(projectID int64, model string, it *workItem) (int, error) {
       chunks := make([]db.Chunk, len(it.chunks))
       for i, c := range it.chunks {
           chunks[i] = c
       }
       if err := ix.db.ReplaceFileChunks(projectID, it.path, it.hash, chunks); err != nil {
           return 0, err
       }
       rows, err := ix.db.FileChunkRows(projectID, it.path)
       if err != nil {
           return 0, err
       }
       ids := make([]int64, len(rows))
       for i, r := range rows {
           ids[i] = r.ID
       }
       if err := ix.db.SetVectors(projectID, model, ids, it.vectors); err != nil {
           return 0, err
       }
       if ix.onProgress != nil {
           ix.onProgress(fmt.Sprintf("embedded %s (%d chunks)", it.path, len(it.chunks)))
       }
       return len(it.chunks), nil
   }
   ```

   Task 3 replaces both with the two-phase indexer, so no new public helper is
   added here.

5. **Update the call sites** in the four test files. Every call of the form
   `ReplaceFileChunks(projectID, path, hash, []db.ChunkWithVector{{Chunk: c, Model: m, Dim: d, Vector: v}, ...})`
   becomes two calls: `ReplaceFileChunks(projectID, path, hash, []db.Chunk{c, ...})`
   followed by `SetVectors(projectID, m, ids, [][]float32{v, ...})`, where `ids`
   comes from `FileChunkRows(projectID, path)` immediately after the chunk
   write. Files and the call sites:
   - `internal/db/chunks_test.go` (10 sites)
   - `internal/retrieval/semantic_test.go` (9 sites)
   - `internal/tools/native/codebase_search_test.go` (1 site)
   - `internal/acp/service/intelligence_test.go` (1 site)

   Inside `internal/db`'s own tests the ids come from `FileChunkRows` too.

6. **Add a test** to `internal/db/chunks_test.go`:
   `TestChunksSurviveWithoutVectors` — write chunk rows with no vectors; assert
   `ChunkFileStates` reports the file with an empty `Model`, and assert
   `CountModelVectors` is zero.

7. **Add a test** to `internal/retrieval/semantic_test.go`:
   `TestSemanticRetrieveSeesVectorsForUnchangedChunkIds` — write chunk rows
   with no vectors, then write vectors for those same chunk ids with
   `SetVectors`, then retrieve with the semantic source and expect a hit. This
   is the vector-only-write case the semantic source's cache cannot see, and it
   pins that a fresh source sees the vectors.

**Verify:**
```bash
CGO_ENABLED=1 go build ./... && \
CGO_ENABLED=1 go test ./internal/db/ ./internal/retrieval/ ./internal/tools/native/ ./internal/acp/service/ ./internal/index/ -count=1
```
Expected: build clean; every package's tests pass.

---

## Task 3: chunk phase always, vector phase conditional

**Goal:** an index pass writes chunks and their lexical index entries for every
project, embedder or not, and writes vectors only where they are missing or stale.

**Files:**
- `internal/index/indexer.go`
- `internal/index/run.go`
- `internal/index/indexer_test.go`

**Steps:**

1. **`index.Stats` gains a `Warnings []string` field** so a per-file failure is
   reported instead of aborting the pass. The chunk phase and the vector phase
   are independent: a chunk-phase failure leaves the previous chunks in place and
   the pass continues with the remaining files; a vector-phase failure leaves
   the chunks in place (so lexical search still works) and the pass continues.

2. **Restructure `Reindex`** (`internal/index/indexer.go:47`) into two phases.

   *Chunk phase* — runs for every scanned file, embedder or not:
   - Read the per-file state with `ix.db.ChunkFileStates(projectID)`.
   - For each scanned file whose stored `FileHash` differs from the file's hash
     (or which has no state at all), compute `Chunk(sf, symbolsByFile[sf.Path])`
     and write the chunk rows with `ix.db.ReplaceFileChunks`. A file whose hash
     is unchanged keeps its chunk rows untouched.
   - A file that yields no chunks gets `ix.db.DeleteFileChunks` (the current
     behaviour for an emptied file).
   - Count `st.ChunksWritten` for the chunks written.
   - Purge files that were present before and are no longer scanned, with
     `ix.db.DeleteFileChunks` (the existing purge loop, `internal/index/indexer.go:92`).

   *Vector phase* — runs only when `ix.embedder != nil`:
   - Re-read the per-file state with `ix.db.ChunkFileStates(projectID)`. Read it
     again, after the chunk phase, because the chunk phase changed it: a file
     whose chunks were just rewritten has new chunk rows and no vectors, so its
     state now reads an empty `Model`.
   - A file needs vectors when its state's `Model` differs from
     `ix.embedder.Model()`. That single test covers all three cases: a file
     whose chunks were just rewritten (empty model), a file whose vectors are
     from an older model, and a file that has chunks but never had vectors.
   - For each such file, read the chunk rows back with
     `ix.db.FileChunkRows(projectID, path)` and embed their contents. Reading
     back rather than reusing the chunker's in-memory output means a file
     whose hash did not change is embedded from exactly the rows stored in the
     database.
   - Keep the existing batching: at most 64 inputs per `Embed` call, across
     files (`internal/index/indexer.go:58`).
   - Write the vectors for a file with `ix.db.SetVectors(projectID, model, ids,
     vectors)` once all of that file's chunks are embedded — the current
     per-file persistence point (`internal/index/indexer.go:147`), so a later
     batch failure does not discard earlier work.
   - Count `st.FilesEmbedded` for the files whose vectors were written.

   The two phases are separate loops. The chunk phase's writes are visible to
   the vector phase because each file's chunks are committed by its own
   `ReplaceFileChunks` transaction before the vector phase runs.

   `SetVectors` replaces a chunk's vector row in place, so a model change keeps
   the chunk ids stable and leaves the lexical index untouched — which is the
   point of the overlay.

3. **Failures.** A chunk-phase failure is per-file: record it in
   `st.Warnings` and continue with the next file, so one bad file does not
   cost the pass the rest of the repository. The file keeps its previous
   chunks — `ReplaceFileChunks` is one transaction, so a failed write leaves
   the old rows in place.

   A vector-phase failure is a phase failure: an embedder that is down or
   returns a mismatched dimension fails every batch, so record **one**
   warning naming the error and end the vector phase. The chunk phase's work
   is already committed and durable, so the lexical index is intact and the
   semantic index is simply behind. The next pass retries.

4. **Counters.** `index.Stats` keeps its fields; their meaning moves with the
   phases:
   - `st.ChunksWritten` counts chunk rows written, in the chunk phase — so it
     is non-zero for a project with no embedder.
   - `st.FilesEmbedded` counts files whose vectors were written, in the vector
     phase — so it stays zero for a project with no embedder.
   - `st.FilesSkipped` counts files whose chunk rows the chunk phase left
     untouched, because their hash was current.
   - `st.FilesPurged` counts files deleted because they were present before
     and are no longer scanned.

5. **`index.Run`** (`internal/index/run.go:44`) copies the new warnings into the
   report: after `ix.Reindex`, append `st.Warnings` to `rep.Warnings`. The
   report already carries a `Warnings []string` field
   (`internal/index/run.go:39`), and `Run` already seeds it from
   `Deps.Warnings` (`internal/index/run.go:30`) — the channel PR #71 added for
   the embedder warning. The existing "embedding: "+err wrapping for a returned
   error stays as it is; the per-file warnings are added alongside it.

6. **Tests** — `internal/index/indexer_test.go`:
   - `TestIndexWritesChunksWithoutAnEmbedder` — index a file with a nil
     embedder; assert the file's chunks exist (`ChunkFileStates` reports its
     hash) and `CountModelVectors` is zero.
   - `TestIndexWritesVectorsWhenAnEmbedderIsPresent` — index with a fake
     embedder; assert `CountModelVectors` equals the chunk count.
   - `TestModelChangeReembedsWithoutRechunking` — index with model A, then
     index again with model B over the same content; assert the chunk rows kept
     their ids (`FileChunkRows` returns the same ids as before) and
     `CountModelVectors(projectID, "B")` is non-zero while
     `CountModelVectors(projectID, "A")` is zero.
   - `TestIndexContinuesAfterOneFileFails` — a file whose write fails (use a
     chunk count that breaks the index insert, or a file path the chunk write
     rejects); assert the other file is still indexed and the failure appears
     in the report warnings.

**Verify:**
```bash
CGO_ENABLED=1 go build ./... && \
CGO_ENABLED=1 go test ./internal/index/ -count=1 -v
```
Expected: build clean; every named test passes; the pre-existing index tests
still pass.

---

## Task 4: the lexical retrieval source

**Goal:** `retrieval` gains a `Source` that answers a query from the lexical
index, ranked by bm25.

**Files:**
- `internal/retrieval/lexical.go` (new)
- `internal/retrieval/lexical_test.go` (new)

**Steps:**

1. **New file `internal/retrieval/lexical.go`**, verbatim:

   ```go
   package retrieval

   import (
       "context"

       "marshal/internal/db"
   )

   // LexicalSource is a retrieval.Source backed by the FTS5 index over chunks.
   type LexicalSource struct {
       db        *db.DB
       projectID int64
   }

   // NewLexicalSource returns a LexicalSource over a project's chunk index.
   func NewLexicalSource(database *db.DB, projectID int64) *LexicalSource {
       return &LexicalSource{db: database, projectID: projectID}
   }

   // Name identifies the source in the candidates it returns.
   func (s *LexicalSource) Name() string { return "lexical" }

   // Retrieve answers a query from the lexical index. The score is the negated
   // bm25, so higher is better, as in the semantic source.
   func (s *LexicalSource) Retrieve(ctx context.Context, q Query) ([]Candidate, error) {
       hits, err := s.db.SearchChunksLexical(s.projectID, q.Text, q.PathPrefix, q.Limit)
       if err != nil {
           return nil, err
       }
       out := make([]Candidate, 0, len(hits))
       for _, h := range hits {
           out = append(out, Candidate{
               FilePath: h.FilePath, StartLine: h.StartLine, EndLine: h.EndLine,
               Content: h.Content, Score: h.Score, SourceName: "lexical",
           })
       }
       return out, nil
   }
   ```

2. **Tests** — new file `internal/retrieval/lexical_test.go`, package
   `retrieval`, reusing the package's existing `newTestDB` and
   `mustCreateProject` helpers (`internal/retrieval/semantic_test.go:18` and
   `:27`):
   - `TestLexicalRetrieveReturnsRankedCandidates` — write two chunks, query
     them; expect candidates with `SourceName == "lexical"`, the better match
     first, and `Score` higher-is-better.
   - `TestLexicalRetrieveFiltersByPathPrefix` — two files, a prefix in the
     query; expect only the prefixed file's candidate.
   - `TestLexicalRetrieveReturnsNothingWithoutAnIndex` — an empty project;
     expect no candidates and no error.

**Verify:**
```bash
CGO_ENABLED=1 go build ./... && \
CGO_ENABLED=1 go test ./internal/retrieval/ -count=1 -v
```
Expected: build clean; every named test passes; the semantic tests still pass.

---

## Task 5: `codebase_search` does double duty

**Goal:** the tool is always registered, and each call answers from semantic
when it can and from lexical when it cannot, naming its source.

**Files:**
- `internal/tools/native/codebase_search.go`
- `internal/tools/native/native.go`
- `internal/tools/native/codebase_search_test.go`

**Steps:**

1. **Register unconditionally.** In `internal/tools/native/native.go:305`,
   delete the `if _, err := routing.NewStaticRouter(...).ResolveEmbedding(); err == nil {`
   guard and append `tools.codebaseSearchTool()` unconditionally. The `routing`
   import stays: it is still used at `internal/tools/native/native.go:481` for
   the embedder resolver.

2. **Rewrite the handler** in `internal/tools/native/codebase_search.go` so it
   applies the spec's five rules, in this order:

   1. Resolve the embedder via `t.resolveEmbedder()`. When it returns
      `routing.ErrEmbeddingNotConfigured` (or no embedder), go to the lexical
      path.
   2. With an embedder, run the semantic source
      (`retrieval.NewSemanticSource(t.db, embedder, t.projectID)`) and retrieve.
      - On error → lexical path, and the result says semantic was unavailable.
      - On hits → return the semantic hits.
      - On zero hits → ask `t.db.CountModelVectors(t.projectID, embedder.Model())`.
        - Zero vectors for that model → lexical path (semantic had nothing to
          search).
        - Some vectors → semantic's no-match is authoritative: return the
          "no matches" content.
   3. The lexical path: `retrieval.NewLexicalSource(t.db, t.projectID)`, then
      retrieve. If the project has no chunks at all, return the "no index yet"
      content instead of searching.

   The count query runs only on the zero-hit path, so the common case pays
   nothing for it.

3. **Content and summary.** Keep the existing rendering of hits (the
   `path:start-end` block with the score) and add the source to the summary:
   `"%d matches (semantic)"` or `"%d matches (lexical)"`. When the semantic
   attempt failed and lexical answered, the content names the reason in one
   sentence before the hits. When the project has no chunks, the content is
   "No index yet — the background indexer builds it." Replace the two existing
   content strings ("Semantic search is not configured: …" at
   `internal/tools/native/codebase_search.go:42` and "No semantic index yet — …"
   at `:49`) — the first no longer occurs, because search is always configured.

4. **Update the tool description** at
   `internal/tools/native/codebase_search.go:25` to: "Search the indexed
   codebase by concept — semantically when an embedding model is configured,
   lexically otherwise. For literal strings use repo.search; this is for
   concepts."

5. **Tests** — `internal/tools/native/codebase_search_test.go`:
   - `TestCodebaseSearchRegisteredWithoutEmbeddings` — replaces
     `TestCodebaseSearchRegisteredOnlyWithEmbeddings`
     (`internal/tools/native/codebase_search_test.go:122`): register with
     `config.Default()` (no embedding route) and assert the tool is registered.
   - `TestCodebaseSearchUsesLexicalWithoutAnEmbedder` — no embedder; assert
     the result is lexical hits, and the summary names the lexical source.
   - `TestCodebaseSearchPrefersSemanticHits` — an embedder whose vectors
     match; assert the result is semantic hits.
   - `TestCodebaseSearchFallsBackToLexicalWhenSemanticFails` — an embedder whose
     `Embed` returns an error; assert lexical hits come back and the content
     says semantic was unavailable.
   - `TestCodebaseSearchFallsBackToLexicalWithoutVectorsForTheModel` — an
     embedder whose model has no vectors in the project; assert lexical hits
     come back.
   - `TestCodebaseSearchKeepsSemanticNoMatchAuthoritative` — an embedder with
     vectors for its model that match nothing; assert the result is "no
     matches", not lexical hits.
   - Update `TestCodebaseSearchNotConfigured` (`:31`) and
     `TestCodebaseSearchEmptyIndex` (`:44`) to the new behaviour: the first now
     expects lexical, the second the "no index yet" content.

   The tool set helper is `newTestToolSetWithRepo` (`:134`); the embedder is
   injected through the `resolveEmbedder` field on `toolSet`
   (`internal/tools/native/native.go:256`).

**Verify:**
```bash
CGO_ENABLED=1 go build ./... && \
CGO_ENABLED=1 go test ./internal/tools/native/ -run 'CodebaseSearch' -count=1 -v
```
Expected: build clean; every named test passes.

---

## Task 6: wording and docs

**Goal:** every surface that reports the semantic state says so without implying
that search is broken, and the tool documentation matches the tool.

**Files:**
- `internal/app/embedding_warning.go`
- `internal/app/embedding_warning_test.go`
- `internal/acp/service/intelligence.go`
- `internal/acp/service/intelligence_test.go`
- `docs/tools/read-search.md`

**Steps:**

1. **Startup warning** (`internal/app/embedding_warning.go:22`). Keep each
   branch's diagnosis and its fix; add one sentence to every branch that
   fires: "Code search works without it, using lexical matching." The four
   branches are the not-configured, preset-not-found, remote-blocked and
   default cases at `internal/app/embedding_warning.go:29-46`. The warning still
   fires only when `cfg.Indexing.UseEmbeddings` is true and the route does not
   resolve.

2. **The `/doctor` Embeddings row** (`internal/acp/service/intelligence.go:59`).
   Report the semantic state and say lexical search is available:
   - not configured → status "off", detail "semantic off — code search uses
     lexical matching".
   - configured but no embeddings → status "warn", detail "semantic on but no
     embeddings written yet — code search uses lexical matching".
   - working → status "ok", detail "semantic on — N embedded chunks (model);
     M chunks indexed lexically", where N comes from the existing
     `database.CountEmbeddedChunks(projectID)` and M from
     `database.ChunkGeneration(projectID)`'s count (the chunk count
     `ChunkGeneration` already returns; see `internal/db/chunks.go:236`).

3. **Tests**:
   - `internal/app/embedding_warning_test.go`: the four branches that assert a
     message (`TestEmbeddingStartupWarningSaysNotConfiguredWhenNothingSet` at
     `:43`, `…NamesUnresolvablePreset` at `:60`, `…MentionsRemoteGate` at
     `:78`, `…LegacyProfileBindingNamesPreset` at `:100`) now also assert the
     lexical sentence is present.
   - `internal/acp/service/intelligence_test.go`:
     `TestIntelligenceChecksEmbeddings` (`:82`) asserts the new detail text;
     `TestIntelligenceChecksEmptyProject` (`:44`) asserts the "semantic off"
     wording.

4. **`docs/tools/read-search.md`**: rewrite the `codebase_search` section
   (lines 119-144). The registration note ("Registered only when an embedding
   route resolves (`native.go:230-233`)") becomes "Registered unconditionally;
   it uses semantic ranking when an embedding model is configured and lexical
   matching otherwise". The two quoted failure-mode strings become the new
   content from Task 5. Correct the stale anchor: the registration gate was at
   `internal/tools/native/native.go:303-306`, and the tool surface is at
   `internal/tools/native/codebase_search.go:24`. Keep the file's first line —
   the `<!-- tools: … -->` index line — unchanged; `TestToolsDocumentationDrift`
   (`internal/app/tools_doc_test.go:38`) parses it and checks every registered
   tool is documented.

**Verify:**
```bash
CGO_ENABLED=1 go build ./... && \
CGO_ENABLED=1 go test ./internal/app/ ./internal/acp/service/ -count=1 && \
CGO_ENABLED=1 go test ./internal/app/ -run 'TestToolsDocumentationDrift' -count=1 -v
```
Expected: build clean; the packages' tests pass; the drift test passes.

---

## Task 7: final verification

**Goal:** the whole repository builds, vets, formats and tests clean, and the
feature works end to end.

**Files:** none — verification only.

**Steps:**

1. Run the repository's own commands (AGENTS.md):

   ```bash
   CGO_ENABLED=1 go build ./...
   CGO_ENABLED=1 go vet ./...
   gofmt -l .
   CGO_ENABLED=1 go test ./...
   ```

2. Confirm the feature's behaviour end to end by reading the code paths once
   more against the spec's five rules, in `internal/tools/native/codebase_search.go`:
   - no embedder → lexical;
   - embedder with hits → semantic;
   - embedder erroring → lexical, with the reason in the content;
   - embedder with no vectors for its model → lexical;
   - embedder with vectors and no hits → "no matches".

3. Confirm the spec's two obligations hold in the code:
   - a semantic query sees vectors written for chunk rows whose ids did not
     change (the vector-only-write case the semantic source's cache cannot see).
     The retrieval layer's test
     `TestSemanticRetrieveSeesVectorsForUnchangedChunkIds`, added in Task 2,
     writes vectors for an existing chunk id with `SetVectors` and then
     retrieves, expecting a hit.
   - no caller holds a `SemanticSource` across an index pass. Check the two call
     sites (`internal/agent/route.go:460`, `internal/tools/native/codebase_search.go:51`):
     each builds a fresh source per query.

4. Note in the task's commit message that `generation_turns_fts`'s delete
   trigger is a separate, still-open bug (it passes `NULL` as the content, which
   removes no tokens). It is not fixed here.

**Verify:** the four commands above, all clean.

---

## Plan authoring checks

- Every task states a goal, names its files, gives concrete steps, and carries a
  runnable Verify block.
- Cross-task types and signatures are declared in Contracts; Task 1 owns the
  lexical ones, Task 2 the chunk ones, Task 4 the retrieval one.
- Anchors were checked at `011113b4`: `ReplaceFileChunks`
  (`internal/db/chunks.go:74`), `ChunkedFiles` (`:121`), `ChunkGeneration`
  (`:236`), `countRows` (`internal/db/counts.go:43`), the schema const and the
  migrations list (`internal/db/migrations.go:306-314`), `generation_turns_fts`
  (`:210`) and its trigger (`:222`), the `chunks` table (`:241`),
  `PRAGMA foreign_keys = ON` (`internal/db/dbpool.go:20`), `db.Migrate`
  (`internal/db/db.go:103`), `ftsPhrase` and the bm25 ordering
  (`internal/db/generations_search.go:46`, `:117`), `Indexer.Reindex`
  (`internal/index/indexer.go:47`), `persist` (`:163`), `index.Run`
  (`internal/index/run.go:44`), `retrieval.Source`
  (`internal/retrieval/retrieval.go:26`), `NewSemanticSource`
  (`internal/retrieval/semantic.go:26`), the semantic cache's staleness check
  (`internal/retrieval/semantic.go:54`, `:69`), the `Source` call sites
  (`internal/agent/route.go:460`, `internal/tools/native/codebase_search.go:51`),
  the registration gate (`internal/tools/native/native.go:305`), the embedder
  resolver (`:480`), the tool surface and its content strings
  (`internal/tools/native/codebase_search.go:25`, `:42`, `:49`), the tool
  tests (`:31`, `:44`, `:57`, `:101`, `:122`, `:134`), the warning
  (`internal/app/embedding_warning.go:22`), the doctor row
  (`internal/acp/service/intelligence.go:59`), the drift test
  (`internal/app/tools_doc_test.go:38`), and the tool doc
  (`docs/tools/read-search.md:119-144`).
- The FTS5 behaviour was probed against the bundled SQLite (3.53.2) rather than
  assumed: `contentless_delete=1` permits the plain `DELETE` the trigger needs;
  the `NULL`-content form removes no tokens; a trigger declared before its
  target table fails; `bm25` discriminates; `unicode61` does not split
  camelCase.
- No task contradicts the spec. Two details the spec leaves to the plan are
  decided here and recorded: Task 2's `persist` reads ids with
  `FileChunkRows` rather than adding a new public helper (Task 3 replaces that
  function), and Task 6's doctor row takes its chunk count from the existing
  `ChunkGeneration`.
