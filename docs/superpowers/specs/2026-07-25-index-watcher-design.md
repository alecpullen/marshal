# Index Watcher (Subsystem #3)

**Date:** 2026-07-25
**Status:** Design approved; ready for implementation plan
**Umbrella:** [Passive Knowledge Architecture](2026-07-24-passive-knowledge-architecture-design.md)
**Depends on:** [Semantic Index (Subsystem #2)](2026-07-25-semantic-index-design.md)
**Type:** Subsystem spec — a background file watcher that keeps the passive
knowledge index fresh automatically, built on a minimal reusable background-worker
lifecycle seam.

## Scope

**In:**
- An fsnotify-based recursive workspace watcher that, on debounced change
  batches, triggers a full incremental index pass (file index + symbols +
  embeddings).
- A shared `index.Run` orchestrator extracted from the `repo.index` tool body,
  called by both the tool and the watcher.
- A minimal `Worker` lifecycle contract (`internal/worker`) the watcher is the
  first implementation of, wired shutdown-aware through `app.Run`.
- Config: `[indexing] watch` (tri-state via `*bool`) + `watch_debounce_ms`.
  Default enablement: **on when an embedding role is configured**, else off,
  always overridable by config.

**Out (later / documented extensions):**
- The general **background-task subsystem** — a worker registry/supervisor, an
  agent-facing dispatch tool (with approval/policy), scheduling, and a
  result/event channel to the TUI. This spec builds only the lifecycle *seam*;
  see "Extension seam" below.
- Passive context-pack injection, fusion, git-churn (later retrieval spec).
- LSP (spec #4).
- Watching for anything other than the index (security monitors etc.) — future.

## Motivation

Subsystem #2 keeps the index fresh only when `repo.index` is run explicitly.
"Passive knowledge" means it should stay current on its own. This subsystem adds
a background watcher so edits are reflected in `symbols.find` and
`codebase_search` without a manual step. It also establishes — deliberately
minimally — the lifecycle primitive that future background features (background
security monitoring, agent-dispatched monitoring tasks) will grow from, the same
way the embedding foundation left an endpoint-driven seam for a future local
inference service rather than building the service now.

## Design decisions (settled during brainstorming)

1. **Event source: fsnotify.** Add `github.com/fsnotify/fsnotify` as a direct
   dependency — the de-facto cross-platform Go fs-events library. Immediate,
   low idle cost (no periodic re-hash when nothing changes).
2. **Refresh scope: full incremental pass.** On change the watcher runs the same
   pass `repo.index` does (files + symbols + embeddings), so no passive
   knowledge drifts — not just embeddings.
3. **Enablement: auto-on when embeddings configured.** If `[indexing] watch` is
   unset, the watcher runs iff `ResolveEmbedding` succeeds; an explicit `watch`
   value always wins (so it can be forced on to keep symbols fresh even without
   embeddings, or forced off).
4. **Foundation, not framework.** Build one concrete `Worker` behind a small
   lifecycle contract; document the general background-task subsystem as an
   extension rather than building its registry/dispatch/policy now.

## The `Worker` lifecycle seam

New package `internal/worker/`:

```go
// Worker is a supervised, long-lived background duty. app.Run starts each
// Worker in its own goroutine bound to the shutdown context and logs a
// non-nil return. This is the seam a future background-task subsystem
// (registry/supervisor, agent dispatch, scheduling) will build on.
type Worker interface {
    // Name identifies the worker in logs.
    Name() string
    // Run blocks until ctx is cancelled, performing the background duty.
    // It returns nil on clean shutdown, or an error on abnormal termination.
    Run(ctx context.Context) error
}
```

That is the entire seam for now — no `Supervisor`, no registry. `app.Run`
starts the watcher worker directly.

### Extension seam (documented, not built)

A future "background monitors / agent-dispatched tasks" subsystem builds on
`worker.Worker` and would add, each in its own spec:

- a **Supervisor/registry** to run and health-check many workers;
- an agent-facing **dispatch tool** to start/stop background monitors — the hard
  part is **approval/policy**, since the agent spawning its own background work
  needs risk classification the built-in watcher does not;
- **scheduling** (periodic vs. event-driven workers);
- a **result/event channel** surfacing worker findings to the session/TUI.

None of that is in this spec. Keeping the seam to just `Worker` avoids designing
that API from a single use case.

## Shared index orchestrator

Subsystem #2 wired the embedding pass inline into `repo.index`. This subsystem
**extracts the whole pass** into a shared function so the tool and the watcher
share one code path.

`internal/index/run.go`:

```go
type Deps struct {
    DB       *db.DB
    Root     string
    Ignore   []string
    MaxBytes int64
    Embedder embedding.Embedder // nil => embeddings skipped (graceful-off)
}

type Report struct {
    Files         int
    Symbols       int
    FilesEmbedded int
    ChunksWritten int
    LangCounts    map[string]int
    Warnings      []string
}

// Run performs one full incremental index pass over the workspace: scan →
// file index (full replace) → tree-sitter symbols (full replace) → embeddings
// (incremental hash-diff via Indexer.Reindex). Cheap parts (scan, symbols) are
// full-replace; the expensive part (embeddings) stays incremental.
func Run(ctx context.Context, deps Deps, projectID int64) (Report, error)
```

`repo.index`'s handler becomes a thin caller of `index.Run` that formats the
`Report`. Behavior is unchanged from subsystem #2's end state; this is a
refactor plus a second caller.

**Why a full re-scan per pass is acceptable:** file-index and symbols are cheap
full-replaces, and embeddings are hash-diff incremental (only changed files get
re-embedded). So the watcher can trigger a whole-workspace pass without tracking
exact changed paths — fsnotify is used as a *change signal*, not for targeting.
Targeted per-path scanning is a future optimization (see open questions).

## The watcher

`internal/index/watcher.go`:

```go
type Watcher struct {
    root     string
    debounce time.Duration
    ignore   *repo.Gitignore // reuse existing ignore + Indexing.Ignore
    run      func(ctx context.Context) error // wraps index.Run for the workspace
    log      *slog.Logger
}

func NewWatcher(root string, debounce time.Duration, ignore *repo.Gitignore,
    run func(ctx context.Context) error, log *slog.Logger) *Watcher

func (w *Watcher) Name() string { return "index-watcher" }
func (w *Watcher) Run(ctx context.Context) error // implements worker.Worker
```

`Run` behavior:

1. Create an `fsnotify.Watcher`; walk `root` and add a watch per directory,
   skipping ignored dirs (`.git`, and anything the scanner's ignore rules
   exclude). Log-and-continue on per-directory watch-add failures (OS
   watch-descriptor limits); the pass is still correct because unwatched trees
   simply won't trigger events.
2. Event loop:
   - On a create/write/rename/remove event for a **non-ignored** path: set a
     `dirty` flag and (re)start the debounce timer.
   - On a newly created **directory**: add a watch for it (so new subtrees are
     covered).
   - On debounce fire with `dirty`: run the index pass **single-flight** — if a
     pass is already running, remember to run once more when it finishes
     (coalescing bursts into at most one queued re-run); clear `dirty`.
   - On `ctx.Done()`: close the fsnotify watcher, let any in-flight pass finish
     (bounded), return `nil`.
3. A pass calls `w.run(ctx)`, which invokes `index.Run` over the workspace.
   Errors are logged, not fatal — the watcher keeps running.

Concurrency: the watcher single-flights its own passes; a concurrent manual
`repo.index` is serialized at the DB layer (per-project locks in `SaveSymbols` /
`SaveFileIndex` / `ReplaceFileChunks`), and a manual pass overlapping a watcher
pass is harmless because the pass is idempotent.

## Config

Add to `IndexingConfig` (`internal/app/config/types.go`):

```go
	Watch           *bool `toml:"watch"`             // nil = auto (on iff embedding configured)
	WatchDebounceMs int   `toml:"watch_debounce_ms"` // default 1000
```

```toml
[indexing]
watch = true            # optional; omit for auto (on when embedding is configured)
watch_debounce_ms = 1000
```

Enablement decision (computed once at startup):

```
if cfg.Indexing.Watch != nil {
    enabled = *cfg.Indexing.Watch
} else {
    enabled = embeddingConfigured   // ResolveEmbedding() returns no ErrEmbeddingNotConfigured
}
```

`WatchDebounceMs <= 0` falls back to the 1000 ms default.

## app.Run wiring

- After config/db/router are available, compute `enabled` as above and resolve
  the embedder (nil when not configured).
- If `enabled`, construct the `Watcher` (its `run` closure captures `index.Run`
  + `Deps`) and start it via a shared `startWorker(ctx, w)` helper: a goroutine
  bound to the existing shutdown context that logs a non-nil `Run` return. This
  helper is where a future Supervisor would slot in.
- A functional option `WithWorker(worker.Worker)` (mirroring `WithKnowledgeHook`)
  lets tests inject a fake worker and assert it is started and stopped with the
  shutdown context, without real fs or indexing.

## Testing strategy

- **`worker.Worker`** — a trivial fake implementing the interface, used by the
  app-wiring test.
- **Watcher event handling** (`watcher_test.go`) — real `fsnotify` against a
  `t.TempDir()`, but with an **injected fake `run`** (not real indexing) so
  assertions are deterministic:
  - writing a file triggers exactly one `run` after the debounce;
  - a burst of rapid writes coalesces into a single `run` (single-flight +
    debounce);
  - a change under an ignored path (e.g. `.git/`) triggers no `run`;
  - creating a new subdirectory then a file in it triggers a `run` (recursive
    watch add);
  - `ctx` cancellation returns `nil` promptly and stops further `run`s.
  Use a generous debounce (e.g. 50 ms) and channel/timeout-based assertions to
  avoid flakiness; document these as timing-sensitive.
- **`index.Run`** (`run_test.go`) — fake `Embedder` + temp DB: one pass writes
  file-index rows, symbols, and embeddings; with a `nil` embedder it still
  writes files + symbols and skips embeddings; the returned `Report` counts are
  correct.
- **Enablement logic** (`app` test) — table test: `Watch=nil` follows
  `embeddingConfigured`; `Watch=&true` on; `Watch=&false` off.
- **app wiring** — inject a fake `worker.Worker` via `WithWorker`; assert `Run`
  is invoked and its context is cancelled on shutdown.

## Dependencies

Adds one direct dependency: `github.com/fsnotify/fsnotify` (latest stable). No
other new dependencies.

## Open questions handed to implementation

- Default debounce (1000 ms) — tune if it feels laggy or too eager.
- Targeted re-scan: fsnotify already reports exact changed paths, so a future
  optimization can scan only those instead of the whole tree. Default here is a
  whole-workspace pass (simple, correct); revisit if large-repo scan cost
  matters.
- Whether to also honor the pre-existing `IndexingConfig.UseEmbeddings` flag as
  an additional gate on the embedding pass — decide alongside subsystem #2's
  treatment of that flag; the watcher itself is gated by `Watch` /
  embedding-configured, independent of it.
