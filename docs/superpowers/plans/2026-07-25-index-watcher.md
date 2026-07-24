# Index Watcher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep the passive knowledge index fresh automatically — a background fsnotify watcher that runs a debounced full incremental index pass (files + symbols + embeddings) on change, built on a minimal reusable `worker.Worker` lifecycle seam.

**Architecture:** Extract the `repo.index` body into a shared `index.Run` orchestrator called by both the tool and the watcher. A new `internal/worker` package defines the `Worker` lifecycle contract; the watcher is its first implementation. `app.Run` starts it shutdown-aware, auto-enabled when embeddings are configured.

**Tech Stack:** Go, one new dependency (`github.com/fsnotify/fsnotify`), plus existing `internal/index` (subsystem #2), `internal/repo`, `internal/db`, `internal/app/config`.

**Spec:** [docs/superpowers/specs/2026-07-25-index-watcher-design.md](../specs/2026-07-25-index-watcher-design.md)

## Global Constraints

- **Depends on subsystem #2** (semantic index): `index.NewIndexer`/`Reindex`, the chunker, and the `repo.index` embedding wiring. Build #2 first.
- **One new dependency:** `github.com/fsnotify/fsnotify` (latest stable). No others.
- **Graceful-off:** the watcher only auto-runs when embeddings are configured; the index pass itself no-ops embeddings when the embedder is nil.
- **Format/vet before commit:** `gofmt -w .` and `go vet ./...` must pass.
- **Foundation not framework:** build only `worker.Worker` + the watcher; no supervisor/registry/dispatch.

---

### Task 1: worker.Worker lifecycle seam

**Files:**
- Create: `internal/worker/worker.go`
- Test: `internal/worker/worker_test.go`

**Interfaces:**
- Produces: `type Worker interface { Name() string; Run(ctx context.Context) error }`.

- [ ] **Step 1: Write the failing test**

Create `internal/worker/worker_test.go`:

```go
package worker

import (
	"context"
	"errors"
	"testing"
)

type stub struct{ ran bool }

func (s *stub) Name() string { return "stub" }
func (s *stub) Run(ctx context.Context) error {
	s.ran = true
	<-ctx.Done()
	return ctx.Err()
}

func TestWorkerRunsUntilCancel(t *testing.T) {
	var w Worker = &stub{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	if w.Name() != "stub" {
		t.Fatalf("Name = %q", w.Name())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/worker/ -v`
Expected: FAIL — `undefined: Worker`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/worker/worker.go`:

```go
// Package worker defines the minimal lifecycle contract for supervised,
// long-lived background duties. app.Run starts each Worker in its own goroutine
// bound to the shutdown context. This is the seam a future background-task
// subsystem (supervisor/registry, agent dispatch, scheduling) will build on;
// for now the index watcher is the only implementation.
package worker

import "context"

type Worker interface {
	// Name identifies the worker in logs.
	Name() string
	// Run blocks until ctx is cancelled, performing the background duty. It
	// returns nil on clean shutdown, or an error on abnormal termination.
	Run(ctx context.Context) error
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/worker/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/worker/ && go vet ./internal/worker/
git add internal/worker/
git commit -m "feat(worker): add minimal Worker lifecycle seam"
```

---

### Task 2: shared index.Run orchestrator

**Files:**
- Create: `internal/index/run.go`
- Modify: `internal/tools/native/repo_index.go` (call `index.Run`)
- Test: `internal/index/run_test.go`

**Interfaces:**
- Consumes: `repo.NewScanner`/`ScanDetailed`, `repo.ExtractSymbols`, `db.SaveFileIndex`/`SaveSymbols`, `index.NewIndexer`/`Reindex` (#2), `embedding.Embedder`.
- Produces: `type Deps`, `type Report`, `func Run(ctx context.Context, deps Deps, projectID int64) (Report, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/index/run_test.go`:

```go
package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunIndexesFilesSymbolsEmbeddings(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package p\nfunc F(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	database := newTestDB(t)
	pid := mustCreateProject(t, database, root)

	rep, err := Run(context.Background(), Deps{
		DB: database, Root: root, Embedder: &fakeEmbedder{model: "m"},
	}, pid)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Files < 1 || rep.Symbols < 1 || rep.FilesEmbedded < 1 {
		t.Fatalf("report = %+v", rep)
	}

	// nil embedder still indexes files+symbols, skips embeddings.
	rep2, err := Run(context.Background(), Deps{DB: database, Root: root, Embedder: nil}, pid)
	if err != nil || rep2.Symbols < 1 || rep2.FilesEmbedded != 0 {
		t.Fatalf("nil-embedder report = %+v err=%v", rep2, err)
	}
}
```

(Reuse the `fakeEmbedder` from `indexer_test.go` — same package.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/index/ -run TestRunIndexes -v`
Expected: FAIL — `undefined: Run`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/index/run.go`:

```go
package index

import (
	"context"
	"fmt"
	"time"

	"marshal/internal/db"
	"marshal/internal/embedding"
	"marshal/internal/repo"
)

type Deps struct {
	DB       *db.DB
	Root     string
	Ignore   []string
	MaxBytes int64
	Embedder embedding.Embedder // nil => embeddings skipped
}

type Report struct {
	Files         int
	Symbols       int
	FilesEmbedded int
	ChunksWritten int
	LangCounts    map[string]int
	Warnings      []string
}

// Run performs one full incremental index pass: scan → file index (full
// replace) → tree-sitter symbols (full replace) → embeddings (incremental).
func Run(ctx context.Context, deps Deps, projectID int64) (Report, error) {
	rep := Report{LangCounts: map[string]int{}}

	scanner := repo.NewScanner(repo.Config{Root: deps.Root, Ignore: deps.Ignore, MaxIndexableFileBytes: deps.MaxBytes})
	scanned, err := scanner.ScanDetailed()
	if err != nil {
		return rep, fmt.Errorf("scan repo: %w", err)
	}

	files := make([]db.FileIndex, len(scanned))
	now := time.Now().UTC()
	for i, sf := range scanned {
		files[i] = sf.FileIndex
		files[i].LastIndexedAt = now
		if files[i].Language != "" {
			rep.LangCounts[files[i].Language]++
		}
	}
	if err := deps.DB.SaveFileIndex(projectID, files); err != nil {
		return rep, fmt.Errorf("save file index: %w", err)
	}
	rep.Files = len(files)

	var symbols []db.Symbol
	symbolsByFile := map[string][]db.Symbol{}
	for _, sf := range scanned {
		if sf.ReadErr != nil {
			rep.Warnings = append(rep.Warnings, sf.Path+": read error")
			continue
		}
		if sf.Language != "go" {
			continue
		}
		fileSyms, extractErr := repo.ExtractSymbols(ctx, sf.Path, sf.Content)
		if extractErr != nil {
			rep.Warnings = append(rep.Warnings, sf.Path+": parse error")
			continue
		}
		symbols = append(symbols, fileSyms...)
		symbolsByFile[sf.Path] = fileSyms
	}
	if err := deps.DB.SaveSymbols(projectID, symbols); err != nil {
		return rep, fmt.Errorf("save symbols: %w", err)
	}
	rep.Symbols = len(symbols)

	st, err := NewIndexer(deps.DB, deps.Embedder).Reindex(ctx, projectID, scanned, symbolsByFile)
	if err != nil {
		rep.Warnings = append(rep.Warnings, "embedding: "+err.Error())
	}
	rep.FilesEmbedded = st.FilesEmbedded
	rep.ChunksWritten = st.ChunksWritten
	return rep, nil
}
```

Refactor `internal/tools/native/repo_index.go`'s handler to call `index.Run` with `Deps` built from `t.config.Indexing` + the resolved embedder, then format `Report` into the existing summary (languages, symbols, embedded/not-configured line, warnings). Delete the now-duplicated scan/save logic from the handler.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/index/ ./internal/tools/native/ -run 'TestRunIndexes|TestRepoIndex' -v`
Expected: PASS (both the new orchestrator test and the existing repo.index tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/index/ internal/tools/native/ && go vet ./internal/index/ ./internal/tools/native/
git add internal/index/run.go internal/index/run_test.go internal/tools/native/repo_index.go
git commit -m "refactor(index): extract shared index.Run; repo.index calls it"
```

---

### Task 3: fsnotify watcher

**Files:**
- Modify: `go.mod` / `go.sum` (add fsnotify)
- Create: `internal/index/watcher.go`
- Test: `internal/index/watcher_test.go`

**Interfaces:**
- Consumes: `worker.Worker` (Task 1), an injected `run func(ctx) error`, `repo` ignore rules.
- Produces: `type Watcher`, `func NewWatcher(root string, debounce time.Duration, run func(ctx context.Context) error, log *slog.Logger) *Watcher` implementing `worker.Worker`.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/fsnotify/fsnotify@latest`
Expected: `go.mod`/`go.sum` updated.

- [ ] **Step 2: Write the failing test**

Create `internal/index/watcher_test.go`:

```go
package index

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func startWatcher(t *testing.T, root string, runs *int32) (context.CancelFunc, chan error) {
	t.Helper()
	w := NewWatcher(root, 40*time.Millisecond, func(ctx context.Context) error {
		atomic.AddInt32(runs, 1)
		return nil
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(60 * time.Millisecond) // let the watch set up
	return cancel, done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

func TestWatcherTriggersOnChange(t *testing.T) {
	root := t.TempDir()
	var runs int32
	cancel, done := startWatcher(t, root, &runs)
	defer func() { cancel(); <-done }()

	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&runs) >= 1 })
}

func TestWatcherCoalescesBurst(t *testing.T) {
	root := t.TempDir()
	var runs int32
	cancel, done := startWatcher(t, root, &runs)
	defer func() { cancel(); <-done }()

	for i := 0; i < 5; i++ {
		_ = os.WriteFile(filepath.Join(root, "a.go"), []byte{byte(i)}, 0o644)
		time.Sleep(5 * time.Millisecond)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&runs) >= 1 })
	time.Sleep(150 * time.Millisecond)
	if n := atomic.LoadInt32(&runs); n > 2 {
		t.Fatalf("burst produced %d runs, expected coalescing to ~1", n)
	}
}

func TestWatcherStopsOnCancel(t *testing.T) {
	root := t.TempDir()
	var runs int32
	w := NewWatcher(root, 40*time.Millisecond, func(context.Context) error { atomic.AddInt32(&runs, 1); return nil }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}
```

These tests are timing-sensitive; the debounce (40 ms) and waits are chosen for headroom.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/index/ -run TestWatcher -v`
Expected: FAIL — `undefined: NewWatcher`.

- [ ] **Step 4: Write minimal implementation**

Create `internal/index/watcher.go`:

```go
package index

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	root     string
	debounce time.Duration
	run      func(ctx context.Context) error
	log      *slog.Logger
}

func NewWatcher(root string, debounce time.Duration, run func(ctx context.Context) error, log *slog.Logger) *Watcher {
	if debounce <= 0 {
		debounce = time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{root: root, debounce: debounce, run: run, log: log}
}

func (w *Watcher) Name() string { return "index-watcher" }

func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fsw.Close()
	w.addRecursive(fsw, w.root)

	timer := time.NewTimer(w.debounce)
	timer.Stop()
	dirty := false
	running := false
	rerun := false
	finished := make(chan struct{}, 1)

	fire := func() {
		running = true
		dirty = false
		go func() {
			if err := w.run(ctx); err != nil {
				w.log.Warn("index watcher pass failed", "err", err)
			}
			finished <- struct{}{}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			if w.ignored(ev.Name) {
				continue
			}
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					w.addRecursive(fsw, ev.Name)
				}
			}
			dirty = true
			timer.Reset(w.debounce)
		case <-timer.C:
			if !dirty {
				continue
			}
			if running {
				rerun = true
				continue
			}
			fire()
		case <-finished:
			running = false
			if rerun || dirty {
				rerun = false
				fire()
			}
		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.log.Warn("index watcher fsnotify error", "err", err)
		}
	}
}

func (w *Watcher) addRecursive(fsw *fsnotify.Watcher, dir string) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if w.ignored(path) {
			return filepath.SkipDir
		}
		if err := fsw.Add(path); err != nil {
			w.log.Debug("index watcher could not watch dir", "dir", path, "err", err)
		}
		return nil
	})
}

func (w *Watcher) ignored(path string) bool {
	base := filepath.Base(path)
	return base == ".git" || strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) ||
		base == "node_modules" || base == ".marshal"
}
```

Note: the ignore check is intentionally minimal (`.git`, `node_modules`, `.marshal`). A follow-up can thread the full `repo` gitignore rules through; the spec's open questions cover this.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/index/ -run TestWatcher -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/index/ && go vet ./internal/index/
git add go.mod go.sum internal/index/watcher.go internal/index/watcher_test.go
git commit -m "feat(index): add fsnotify debounced watcher (single-flight)"
```

---

### Task 4: config fields + enablement

**Files:**
- Modify: `internal/app/config/types.go` (add `Watch`, `WatchDebounceMs`)
- Create: `internal/app/config/watch.go` (enablement helper) — or place the helper in `app`
- Test: `internal/app/config/config_test.go`

**Interfaces:**
- Produces: `IndexingConfig.Watch *bool`, `IndexingConfig.WatchDebounceMs int`; `func WatchEnabled(watch *bool, embeddingConfigured bool) bool`.

- [ ] **Step 1: Write the failing test**

Append to `internal/app/config/config_test.go`:

```go
func TestWatchEnabled(t *testing.T) {
	tt := true
	ff := false
	cases := []struct {
		watch       *bool
		embedding   bool
		want        bool
	}{
		{nil, true, true},   // auto: on when embedding configured
		{nil, false, false}, // auto: off otherwise
		{&tt, false, true},  // explicit on wins
		{&ff, true, false},  // explicit off wins
	}
	for _, c := range cases {
		if got := WatchEnabled(c.watch, c.embedding); got != c.want {
			t.Fatalf("WatchEnabled(%v,%v)=%v want %v", c.watch, c.embedding, got, c.want)
		}
	}
}

func TestLoadWatchConfig(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", "[indexing]\nwatch = true\nwatch_debounce_ms = 500\n")
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Indexing.Watch == nil || !*cfg.Indexing.Watch || cfg.Indexing.WatchDebounceMs != 500 {
		t.Fatalf("indexing = %#v", cfg.Indexing)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/config/ -run 'TestWatchEnabled|TestLoadWatchConfig' -v`
Expected: FAIL — `cfg.Indexing.Watch undefined` / `undefined: WatchEnabled`.

- [ ] **Step 3: Write minimal implementation**

In `internal/app/config/types.go`, add to `IndexingConfig`:

```go
	Watch           *bool `toml:"watch"`             // nil = auto (on iff embedding configured)
	WatchDebounceMs int   `toml:"watch_debounce_ms"` // default 1000
```

Create `internal/app/config/watch.go`:

```go
package config

// WatchEnabled applies the watcher enablement rule: an explicit watch value
// wins; otherwise the watcher is on iff an embedding role is configured.
func WatchEnabled(watch *bool, embeddingConfigured bool) bool {
	if watch != nil {
		return *watch
	}
	return embeddingConfigured
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/config/ -run 'TestWatchEnabled|TestLoadWatchConfig' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/config/ && go vet ./internal/app/config/
git add internal/app/config/types.go internal/app/config/watch.go internal/app/config/config_test.go
git commit -m "feat(config): add indexing.watch tri-state and enablement rule"
```

---

### Task 5: app.Run wiring

**Files:**
- Modify: `internal/app/app.go` (construct + start the watcher; `WithWorker` option; `startWorker` helper)
- Test: `internal/app/app_test.go`

**Interfaces:**
- Consumes: `worker.Worker` (Task 1), `index.NewWatcher`/`index.Run` (Tasks 2/3), `config.WatchEnabled` (Task 4), `routing.ResolveEmbedding` (#1).
- Produces: `func WithWorker(w worker.Worker) Option`; a `startWorker` helper; the watcher started shutdown-aware when enabled.

- [ ] **Step 1: Write the failing test**

Append to `internal/app/app_test.go` a test using the existing DI options (`WithConfigLoader`, `WithProgramRunner`, `WithNow`) that injects a fake worker via `WithWorker` and asserts it is started and its context cancelled on shutdown:

```go
func TestInjectedWorkerStartsAndStops(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	fake := workerFunc{name: "fake", run: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil
	}}
	// Run the app with the existing test options (fake config loader + program
	// runner that returns immediately), plus WithWorker(fake).
	runAppForTest(t, WithWorker(fake)) // adapt to the existing app_test harness
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker not started")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("worker not stopped on shutdown")
	}
}

type workerFunc struct {
	name string
	run  func(context.Context) error
}

func (w workerFunc) Name() string                    { return w.name }
func (w workerFunc) Run(ctx context.Context) error   { return w.run(ctx) }
```

Adapt `runAppForTest` to however `app_test.go` currently invokes `Run` with options.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestInjectedWorker -v`
Expected: FAIL — `undefined: WithWorker`.

- [ ] **Step 3: Write minimal implementation**

In `internal/app/app.go`:

Add the option + field (mirroring `WithKnowledgeHook`):

```go
// WithWorker registers a background worker started shutdown-aware by Run.
// Primarily a test seam; production wiring constructs the index watcher.
func WithWorker(w worker.Worker) Option {
	return func(o *options) { o.workers = append(o.workers, w) }
}
```

Add `workers []worker.Worker` to the `options` struct.

Add a `startWorker` helper and start both injected and production workers, bound to the run/shutdown context:

```go
func startWorker(ctx context.Context, wg *sync.WaitGroup, w worker.Worker, log *slog.Logger) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := w.Run(ctx); err != nil {
			log.Warn("worker exited", "worker", w.Name(), "err", err)
		}
	}()
}
```

In `Run`, after config/db/router are available and before handing off to the program, decide enablement and construct the watcher when no worker was injected:

```go
	workers := runOpts.workers
	if len(workers) == 0 {
		embeddingConfigured := false
		if _, err := router.ResolveEmbedding(); err == nil {
			embeddingConfigured = true
		}
		if config.WatchEnabled(cfg.Indexing.Watch, embeddingConfigured) {
			debounce := time.Duration(cfg.Indexing.WatchDebounceMs) * time.Millisecond
			runPass := func(c context.Context) error {
				_, err := index.Run(c, index.Deps{
					DB: database, Root: workingDir, Ignore: cfg.Indexing.Ignore,
					MaxBytes: cfg.Indexing.MaxIndexableFileBytes, Embedder: resolveEmbedder(),
				}, projectID)
				return err
			}
			workers = append(workers, index.NewWatcher(workingDir, debounce, runPass, logger))
		}
	}
	var workerWG sync.WaitGroup
	for _, w := range workers {
		startWorker(runCtx, &workerWG, w, logger)
	}
	// ... existing program run ...
	// on shutdown: cancel runCtx (already wired), then workerWG.Wait() with a bounded timeout.
```

Use the existing shutdown context (`runCtx` / whatever the file already cancels on quit) so workers stop with the app; wait for them (bounded) during teardown. `resolveEmbedder()` mirrors the native tool-set resolver (nil on `ErrEmbeddingNotConfigured`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/ -run TestInjectedWorker -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/ && go vet ./internal/app/
git add internal/app/app.go internal/app/app_test.go
git commit -m "feat(app): start index watcher shutdown-aware; add WithWorker"
```

---

## Final verification

- [ ] `go test ./...` — Expected: PASS.
- [ ] `go vet ./...` — Expected: no errors.
- [ ] `gofmt -l internal/worker/ internal/index/ internal/app/config/ internal/app/ internal/tools/native/` — Expected: no files listed.
- [ ] Manual smoke (optional): with an `embedding` role configured, `go run ./cmd/marshal`, edit a file, and confirm (via logs) a debounced index pass runs.

## Spec coverage map

- `worker.Worker` seam → Task 1
- shared `index.Run` orchestrator + repo.index refactor → Task 2
- fsnotify debounced single-flight watcher → Task 3
- `[indexing] watch`/`watch_debounce_ms` + enablement rule → Task 4
- shutdown-aware wiring + auto-enable + `WithWorker` → Task 5

## Notes for the implementer

- The watcher tests are timing-sensitive by nature; keep the 40 ms debounce and the generous `waitFor` deadline. If flaky on CI, widen the deadline, not the debounce.
- Task 5 must reuse the app's existing shutdown context and teardown ordering — do not introduce a second lifecycle. The watcher goroutine must be joined (bounded) before the DB closes.
- Keep the ignore rules minimal for now (Task 3); threading full gitignore is a documented follow-up, not part of this plan.
