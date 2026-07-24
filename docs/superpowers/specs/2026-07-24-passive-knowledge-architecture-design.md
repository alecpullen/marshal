# Passive Knowledge Architecture (Umbrella)

**Date:** 2026-07-24
**Status:** Design approved; subsystem specs to follow
**Type:** Umbrella architecture — defines seams and data model shared by three
subsystems (embedding foundation, semantic index, LSP integration). Each
subsystem gets its own spec → plan → build cycle. This document exists so those
specs can be written independently without conflicting.

## Motivation

Marshal's passive knowledge today is narrow:

- **Symbol extraction** is tree-sitter, **Go-only**, producing functions /
  methods / types / imports with signatures + line ranges into the SQLite
  `symbols` table (full-replace per project via the `repo.index` tool).
- **Symbol search** (`symbols.find`) is a name-substring + kind filter over
  SQL `LIKE`.
- **File index** hashes every file and records language.
- **Context packs** assemble budgeted sections; **the knowledge/memory agent**
  captures durable notes.

We want three things: (1) real cross-language symbols and IDE-grade signals via
**LSP**; (2) **semantic code search** using a local embedding model
(nomic-embed-text via Ollama) with room to grow into a managed local inference
service; (3) a coherent way for all of these to feed the agent's context —
*passively* — rather than as disconnected tools.

## Guiding decisions

These were settled during brainstorming and are binding on the subsystem specs:

1. **Fusion in code, conventional tools for the model.** A unified retrieval
   layer exists as an *internal Go abstraction* (a `Source` interface + a
   ranking/fusion function). It is **not** exposed to the model as a single
   opaque "knowledge query" tool. Models are trained on a conventional tool
   vocabulary (read, grep, glob, go-to-definition, references, and a
   Cursor/Kilo-style `codebase_search`); the model-facing surface stays granular
   and familiar. Fusion is used only by our own code assembling passive context.

2. **Incremental + background watcher.** The index stays fresh on its own. A
   background watcher (fs events + hash-diff on a debounce) re-indexes only
   changed files. All sources become hash-keyed and incremental. Explicit
   triggers (`repo.index`, session start) remain but run the same incremental
   engine.

3. **Vectors as float32 blobs in SQLite + brute-force Go cosine.** No new native
   dependency. Fast enough for single-repo scale (~10k–50k chunks). An ANN index
   can slot in later behind the same `Source` interface if that scale is ever
   exceeded.

4. **Separate `Embedder` interface + an `embedding` routing role.** Embedding is
   a distinct capability (returns vectors, not chat events; not every provider
   offers it), so it gets its own interface rather than extending `Provider`. It
   reuses the existing `[providers.*]` config and role-based routing — a future
   llama.cpp/Ollama managed service is then "just another provider entry."

5. **Layered symbols: LSP primary, tree-sitter fallback.** The `symbols` table
   gains a `source` column. LSP populates symbols for languages with a running
   server; tree-sitter fills in where no LSP is configured (zero-config
   fallback preserved). Live queries (definition / references / hover /
   diagnostics) go straight to the server and are never persisted.

6. **First-class extra sources/signals:** git churn/recency (a ranking signal),
   doc/comment/README chunks (a Source), and live diagnostics (context
   injection). The **call/reference graph is a documented extension** (Source
   #5), deferred to its own spec.

## Component architecture

| Package | Role |
|---|---|
| `internal/index/` | **Write path.** Indexing engine: background watcher (fs events + hash-diff on debounce), schedules per-file work, drives the per-source indexers, owns freshness. |
| `internal/retrieval/` | **Read path.** The `Source` interface, the fusion/ranking `Query()` entry point. Pure Go, called by the context-pack builder (passive) and by thin tool wrappers (active). |
| `internal/lsp/` | LSP client (JSON-RPC over stdio) + server lifecycle manager (spawn / health / shutdown per language). |
| `internal/llm/embedding/` | `Embedder` interface + Ollama-native (`/api/embed`) and OpenAI-compatible (`/v1/embeddings`) backends. |
| `internal/llm/routing/` | Gains an `embedding` role → resolves to a `[providers.*]` entry + model. |
| `internal/db/` | New tables (`chunks`, `embeddings`); `source` column on `symbols`. |
| `internal/contextpack/` | Calls `retrieval.Query()` for passive context; injects live diagnostics. |
| `internal/tools/native/` | New `codebase_search` + LSP tools (`definition` / `references` / `hover` / `diagnostics`); keeps existing `search` / `symbols.find`. |

### The `Source` interface (spine)

Each knowledge source implements a common retrieval shape:

```go
// internal/retrieval
type Candidate struct {
    FilePath   string
    StartLine  int
    EndLine    int
    Content    string  // snippet for context assembly
    Score      float64 // source-local relevance (0..1)
    SourceName string  // "symbols" | "semantic" | "lsp" | "doc"
}

type Source interface {
    Name() string
    Retrieve(ctx context.Context, q Query) ([]Candidate, error)
}
```

`Query()` fans out to enabled sources, **dedups by file/span**, and applies
ranking signals (git churn/recency boost) to produce a fused, ranked list.
This lives entirely in code — the model never calls it directly.

## Data flow

**Write path (indexing):**
watcher detects change → hash-diff vs. stored `files.hash` → for each changed
file, run enabled indexers:
- LSP `documentSymbol` *or* tree-sitter → `symbols` (tagged by `source`)
- symbol-aware chunker → `Embedder` → vector blobs (`chunks` + `embeddings`)
- doc extractor → `chunks` with `kind='doc'`

Upserts are keyed by `(file, hash)`; deletions purge the file's rows.
Everything is incremental.

**Read path (passive):**
context-pack builder calls `retrieval.Query()` → fans out to Sources → fusion
ranks with git-churn/recency → top-K become context sections, plus current LSP
diagnostics for in-play files.

**Read path (active):**
model calls conventional tools (`codebase_search`, `definition`, …) that are
thin wrappers over the same Sources / LSP.

## Data model (SQLite)

- **`symbols`** — add `source TEXT` (`'lsp'` | `'treesitter'`). Everything else
  unchanged. LSP rows win where a server is up; tree-sitter fills the rest.
- **`chunks`** — `id, project_id, file_path, hash, symbol_id (nullable), kind
  ('code'|'doc'), start_line, end_line, content, token_count`. Doc / README /
  comment chunks are just `kind='doc'` — no separate table.
- **`embeddings`** — `chunk_id, model, dim, vector BLOB` (float32
  little-endian). The vector is kept in its own table and **tagged with the
  model name**: a model change (or dimension mismatch) marks vectors stale and
  triggers re-embed, and two models can coexist during a switch.
- **Incremental diff** reuses the existing `files.hash` column — no new
  freshness bookkeeping.
- **Diagnostics are never persisted** — pulled live from the LSP server at
  pack-build time.

**Migration mechanism (already exists).** New tables (`chunks`, `embeddings`)
are added to the idempotent `schema` const (`CREATE TABLE IF NOT EXISTS`). The
additive `symbols.source` column uses the existing `migrationColumns` slice in
`internal/db/db.go`, applied by `Migrate()` as `ALTER TABLE symbols ADD COLUMN
source TEXT` and guarded by a `PRAGMA table_info` existence check — the same
path already used for backward-compatible column additions. No new migration
machinery is needed.

## Embedding seam

- `Embedder` interface: `Embed(ctx, texts []string) ([][]float32, error)`,
  `Model() string`, `Dims() int`.
- Backends: **Ollama-native** (`/api/embed`) and **OpenAI-compatible**
  (`/v1/embeddings`), constructed from the `embedding` routing role → a resolved
  `[providers.*]` entry. Batching + retry live here.
- **Future llama.cpp / Ollama service seam:** the `Embedder` is strictly
  *endpoint-driven* — it never manages a process. A future managed-service
  manager (out of scope) provisions the base URL and ensures the model is
  pulled, then hands the `Embedder` a URL. This is a named seam, not built now.
- **Chunking principle** (detail deferred to the semantic spec): symbol-aware —
  chunk on symbol boundaries from tree-sitter / LSP, not fixed windows; prose
  files chunk by heading / paragraph.

## Subsystem decomposition & build order

Each gets its own spec → plan → build cycle after this umbrella:

1. **Embedding foundation** — `Embedder` + `embedding` role + config. No
   dependencies. *(foundation)*
2. **Semantic index** — `chunks` / `embeddings` tables, symbol-aware chunker,
   incremental hash-diff index core, `codebase_search` tool, retrieval `Source`
   + fusion skeleton, wire into context packs. Also folds in **doc/README
   chunks** + **git-churn ranking**. Depends on 1.
3. **Background watcher** — fs events + debounce + lifecycle around the
   incremental core from 2.
4. **LSP integration** — client + server manager, layered `source`-column
   symbols, live tools (`definition` / `references` / `hover`), **diagnostics
   injection**. Independent of 1–3; plugs into retrieval + index.
   - **Reconcile with the existing `diagnostics.check` tool.** A native
     `diagnostics.check` tool already exists (`internal/tools/native/diagnostics.go`),
     backed by `t.diagnostics.Check` running configured external language
     checkers. The LSP work must extend/unify this surface, not add a competing
     `diagnostics` tool. Passive diagnostics injection into the context pack is
     new and does not collide; if an LSP-backed diagnostics *tool* is exposed,
     it either becomes an additional backend behind `diagnostics.check` or is
     clearly namespaced.

## Compatibility with current `main` (audited 2026-07-24)

Checked against the recent rollover / approval-modes / provider-repair work on
`main`. No blocking conflicts. Notes folded into this design:

- **DB** — migration mechanism confirmed present (see above). No conflict.
- **`diagnostics.check`** — pre-existing tool; LSP must reconcile (see #4).
- **Routing roles** — `AllRoles` enumerates *chat* roles for onboarding /
  settings; the `embedding` role is not a chat role. Spec #1 decides placement
  (see open questions).
- **Provider `toolcall_repair` / `openai_compatible`** — chat-path only; the
  `Embedder` uses `/v1/embeddings` or Ollama `/api/embed`, a separate path.
- **`contextpack`** — untouched by recent work; passive-injection changes land
  on stable code.

## Deferred / documented extensions

- **Call/reference graph** (Source #5) — who-calls-whom from LSP references;
  enables neighbor expansion and change-impact awareness.
- **Managed local inference service** — marshal spawning/health-checking an
  Ollama or llama.cpp process. The embedding seam is designed for it, but the
  lifecycle is out of scope here.
- **ANN index** — only if brute-force cosine ever outgrows single-repo scale.

## Testing strategy (umbrella level)

Each subsystem spec owns its own tests. At the seams:

- **`Source` interface** — fusion/ranking is pure Go over in-memory candidates:
  table-driven tests for dedup, score merging, and git-churn boosting with no
  DB or network.
- **`Embedder`** — an interface with a fake backend for the semantic-index and
  index-engine tests; real Ollama/OpenAI backends get contract tests behind a
  build tag or skipped when no endpoint is available (matching how existing
  provider integration tests gate on a live endpoint).
- **Incremental index core** — hash-diff behavior (added / changed / deleted
  files → correct upserts / purges) tested against a temp SQLite DB with a fake
  `Embedder`, no watcher goroutine.
- **Layered symbols** — a file with both an LSP result and a tree-sitter result
  resolves to `source='lsp'`; a file with no server falls back to
  `source='treesitter'`.

## Open questions handed to subsystem specs

- Exact chunk sizing / overlap and the prose-vs-code chunking split (semantic
  spec).
- Debounce interval, watcher backpressure, and behavior on large bulk changes
  (watcher spec).
- Which LSP servers ship as known defaults and how they're discovered /
  configured (LSP spec).
- Fusion weighting: how semantic vs. lexical vs. LSP scores combine, and how
  strong the git-churn boost is (semantic/retrieval spec, tuned empirically).
- **`embedding` role placement** — whether `RoleEmbedding` joins `routing.AllRoles`
  (and settings/onboarding treat it specially since it is not a chat role) or is
  resolved via a dedicated lookup outside `AllRoles` (embedding-foundation spec).
