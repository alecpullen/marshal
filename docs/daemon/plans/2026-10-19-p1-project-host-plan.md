# P1 project host and headless indexing — implementation plan

Date: 2026-10-19. Status: executed.
Base: `f637b8edd42b6529b0334c0ffae37ebe100966c2` (`main`). Also applies unchanged at `42f04028`, whose two newer commits touch only docs.
Approved design: [p1-project-host.md](../p1-project-host.md), parent [design.md](../design.md) §4.2.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** sessions in one process share their project's database setup, index work and language servers through a `ProjectHost`, indexing runs on every production path (so a headless agent keeps its project's embeddings current with no TUI running), and two processes on one tree run one indexer between them.

**Non-goals:** worktree indexes, routing a project's fleet agents into one shared `marshal acp` child, pinned projects, idle language-server timeouts, OAuth refresh outside the TUI, watching config files, changing the `indexing.watch` default, sharing the snapshot service or the skill index (spec F1–F7). No change to the bridge, the TUI's data flow, the database schema or the ACP protocol.

**Assumptions:**
- Release targets are linux and darwin (`.goreleaser.yaml`), so the lease's `flock` binding covers them. Other platforms compile a stub that reports an error, and the supervisor then indexes without the lease (spec H4).
- `flock` locks belong to the open file description, so two `TryLease` calls in one process exclude each other the way two processes do. The lease and supervisor tests rely on this; it holds on Linux and macOS.
- On macOS, `t.TempDir()` under the default `/var` TMPDIR breaks two existing snapshot tests in `internal/app`, and a long TMPDIR overflows the unix-socket path in two `internal/acp` listen tests. Every test command below therefore runs with `TMPDIR=/private/tmp/p1-suite`. Create it first: `mkdir -p /private/tmp/p1-suite`.

**How this plan was checked:**
- A full prototype was built task by task on a fresh worktree at the base commit. After each task its Verify commands passed and `gofmt -l` was clean. The code blocks below are that prototype's code, not sketches.
- The touched packages (`internal/app`, `internal/acp`, `internal/lsp`, `internal/index`) pass under `-race`. The full suite passes apart from the known local failures listed in Task 9.
- 33 mutations, each removing one behaviour these tests guard, all made a test fail. One survivor during authoring (a rebind that released its old manager before acquiring the new one) led to the private-registry case in `TestReloadKeepsLanguageServersUnlessTheServerSetChanges`.
- A built `marshal acp` with two sessions on one git project created the index lease and indexed the project (Task 9's smoke check). The base binary indexed nothing.

Execute tasks in order. Run each task's Verify and commit it before starting the next, as `p1-project-host: task N — <title>`. Tasks 1, 2 and 3 are parallel-safe with each other, and Task 2 is also parallel-safe with Task 4. All other tasks are sequential (Tasks 3 to 7 share the `internal/app` package). Never commit unrelated working-tree changes. The spec and plan travel with the work (Task 9).

**Encoding:** new files are given in full. Edits to existing files are given as unified diffs against the state after the previous tasks, each with a short description of the change. Apply them exactly, by hand or with `git apply`. Every hunk's context was checked against the base.

## Contracts

Declarations that more than one task uses. Each is created by the task named, verbatim as shown in that task's file.

**Task 1, `internal/index/lease.go`: the cross-process index lease.**

```go
const LeaseFile = ".marshal/index.lock"

// held is false with a nil error when another holder has the lease; release
// is non-nil only when held is true and is safe to call more than once.
func TryLease(root string) (release func(), held bool, err error)
```

**Task 2, `internal/lsp/handle.go`.**

```go
// Set points the handle at m, which may be nil, and returns the manager it
// pointed at before. It neither runs m nor stops old.
func (h *Handle) Set(m *Manager) (old *Manager)
```

**Task 3, `internal/app/lsp_pool.go`: the shared language-server pool.**

```go
type lspPool struct { /* ctx, log, linger; entries keyed by lspPoolKey */ }
type lspLease struct { /* pool, entry, once */ }

func newLSPPool(ctx context.Context, log *slog.Logger, linger time.Duration) *lspPool
func (p *lspPool) acquire(root string, servers map[string]lsp.ServerSpec) *lspLease // nil for no servers or a closed pool
func (p *lspPool) running(root string) *lsp.Manager
func (p *lspPool) close(ctx context.Context) error
func (l *lspLease) manager() *lsp.Manager // nil-safe
func (l *lspLease) release()              // nil-safe; only the first call counts
func lspServersFor(cfg config.Config, root string) map[string]lsp.ServerSpec
```

**Task 4, `internal/app/host_index.go`: the index supervisor.**

```go
const indexLeaseRetry = 30 * time.Second

type indexSupervisor struct {
	root     string
	watch    bool
	retry    time.Duration
	log      *slog.Logger
	tryLease func(root string) (release func(), held bool, err error)
	scan     func(ctx context.Context) error
	watcher  func(ctx context.Context) error
}

func (s indexSupervisor) run(ctx context.Context)
```

**Task 5, `internal/app/hosts.go`: the registry and the project host.**

```go
const hostLinger = 30 * time.Second
const hostCloseTimeout = 5 * time.Second

type Hosts struct {
	indexing   bool
	workers    []worker.Worker // replaces each host's index supervisor when set
	linger     time.Duration
	leaseRetry time.Duration
	tryLease   func(root string) (release func(), held bool, err error)
	// mu, hosts, closing, closed
}

func NewHosts() *Hosts                                  // indexing on, hostLinger
func newHosts(indexing bool, linger time.Duration) *Hosts // private registries: newHosts(false, 0)
func WithHosts(h *Hosts) Option
func (hs *Hosts) Close(ctx context.Context) error
func (hs *Hosts) acquire(root string, now time.Time) (*ProjectHost, error)

type ProjectHost struct {
	db      *db.DB
	lsp     *lspPool
	reports *pubsub.Broker[index.Report]
	// root, hosts, refs, timer, ready, openErr, ctx, cancel, log, logFile,
	// indexMu, mu, sessions, cfg, projectID, attached, indexKey, indexStop, closed
}

func (h *ProjectHost) Root() string
func (h *ProjectHost) release()
func (h *ProjectHost) attach(state *session.State, cfg config.Config, projectID int64)
func (h *ProjectHost) detach(state *session.State)
func (h *ProjectHost) noteConfig(cfg config.Config)
```

The `options` struct in `app.go` gains `hosts *Hosts` (Task 5).

**Task 6, `internal/app/runtime.go` and `app.go`: runtime wiring.**

```go
// Runtime gains:
Host      *ProjectHost
lspMu     sync.Mutex
lspLease  *lspLease
lspClosed bool

func (rt *Runtime) bindLSP(cfg config.Config, root string)
func (rt *Runtime) unbindLSP()

// buildAgentRunnerWithLock gains `lspHandle *lsp.Handle` after `service` and
// no longer returns a *lsp.Handle (12 results plus error).
```

**Shared test helpers** (package `app`): `hostRoot`, `testHosts`, `mustAcquire` and `hostClosed` from `hosts_test.go` (Task 5) are used again by `runtime_host_test.go` (Task 6). `hostedProject` and `startOn` (Task 6) are used only there. Existing helpers: `fakeTrustResolver` in `app_test.go`, `must` in `app.go`; in `internal/acp`, `writeMarshalConfig`, `noopCancel` and `fakeTrustResolver` in `session_test.go`.

## Task 1: Index lease

**Goal:** `index.TryLease` takes a non-blocking exclusive `flock` on `<root>/.marshal/index.lock`, so one process at a time can index a tree (spec H4).

**Files:** `internal/index/lease.go` (new), `internal/index/lease_unix.go` (new), `internal/index/lease_other.go` (new), `internal/index/lease_test.go` (new).

Parallel-safe with: Tasks 2 and 3.

**Steps:**

1. Create `internal/index/lease.go` with exactly this content:

```go
package index

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LeaseFile is the lock file, relative to a project root, that a process
// holds while it indexes that root. One holder at a time keeps two processes
// on one tree (the TUI and a fleet agent, or two agents) from scanning and
// embedding the same files twice.
const LeaseFile = ".marshal/index.lock"

// TryLease tries to take root's index lease without blocking.
//
// held is false, with a nil error, when another holder has the lease. Any
// other failure (the lock file cannot be opened, or the platform has no lock
// binding) is returned as err, and the caller decides whether to index
// without the lease. release is non-nil only when held is true; it is safe to
// call more than once.
func TryLease(root string) (release func(), held bool, err error) {
	f, err := os.OpenFile(filepath.Join(root, LeaseFile), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, fmt.Errorf("open index lease: %w", err)
	}
	ok, err := tryFlock(f)
	if err != nil {
		_ = f.Close()
		return nil, false, fmt.Errorf("lock index lease: %w", err)
	}
	if !ok {
		_ = f.Close()
		return nil, false, nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = releaseFlock(f)
			_ = f.Close()
		})
	}, true, nil
}
```

2. Create `internal/index/lease_unix.go` with exactly this content:

```go
//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris

package index

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryFlock attempts an exclusive, non-blocking flock. It returns (false, nil)
// when another open file description holds the lock.
func tryFlock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return false, err
}

// releaseFlock unlocks f. Closing f would drop the lock too; unlocking first
// keeps the release independent of descriptor lifetime.
func releaseFlock(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil && !errors.Is(err, unix.EBADF) {
		return err
	}
	return nil
}
```

The binding mirrors `internal/snapshot/lock_unix.go`; the snapshot package's copy is unexported, so the index package keeps its own.

3. Create `internal/index/lease_other.go` with exactly this content:

```go
//go:build !linux && !darwin && !freebsd && !openbsd && !dragonfly && !netbsd && !solaris

package index

import (
	"errors"
	"os"
)

// errLeaseUnsupported reports a platform with no verified advisory-lock
// binding. TryLease returns it as an error, so callers index without the
// lease rather than trusting a lock that excludes nobody.
var errLeaseUnsupported = errors.New("advisory file locking is not supported on this platform")

func tryFlock(*os.File) (bool, error) { return false, errLeaseUnsupported }

func releaseFlock(*os.File) error { return nil }
```

4. Create `internal/index/lease_test.go` with exactly this content:

```go
package index

import (
	"os"
	"path/filepath"
	"testing"
)

func leaseRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTryLeaseExcludesASecondHolderUntilReleased(t *testing.T) {
	root := leaseRoot(t)
	release, held, err := TryLease(root)
	if err != nil || !held {
		t.Fatalf("first TryLease = held %v, err %v; want held", held, err)
	}

	// flock belongs to the open file description, so a second open in this
	// process stands in for a second process.
	again, held, err := TryLease(root)
	if err != nil || held || again != nil {
		t.Fatalf("second TryLease while held = held %v, release %v, err %v; want not held, no error", held, again != nil, err)
	}

	release()
	release() // a second release is a no-op

	after, held, err := TryLease(root)
	if err != nil || !held {
		t.Fatalf("TryLease after release = held %v, err %v; want held", held, err)
	}
	after()
}

func TestTryLeaseReportsAnUnopenableLockFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	release, held, err := TryLease(root)
	if err == nil || held || release != nil {
		t.Fatalf("TryLease(missing root) = held %v, release %v, err %v; want an error", held, release != nil, err)
	}
}
```

| Test | Asserts |
|---|---|
| `TestTryLeaseExcludesASecondHolderUntilReleased` | a second open is refused while held, release is idempotent, the lease is free afterwards |
| `TestTryLeaseReportsAnUnopenableLockFile` | a missing root is an error, not `held = false` |

**Verify:**

```bash
gofmt -l internal/index/                       # no output
go vet ./internal/index/
TMPDIR=/private/tmp/p1-suite go test ./internal/index/ -run TestTryLease -count=1   # ok
```

## Task 2: `Handle.Set`

**Goal:** an `lsp.Handle` can be pointed at a manager someone else owns, so a runtime keeps one handle while the manager behind it is shared (spec H5).

**Files:** `internal/lsp/handle.go`, `internal/lsp/handle_test.go`.

Parallel-safe with: Tasks 1, 3 and 4.

**Steps:**

1. Add `Set` above `Restart`, and update the type comment: `Set` swaps in an owned manager; `Restart`'s single-caller note now applies to `Restart` alone.

Apply this change to `internal/lsp/handle.go`:

```diff
diff --git a/internal/lsp/handle.go b/internal/lsp/handle.go
index 99f50763..13f2325a 100644
--- a/internal/lsp/handle.go
+++ b/internal/lsp/handle.go
@@ -7,16 +7,15 @@ import (
 )
 
 // Handle is a swappable reference to the active Manager. Tool and index
-// adapters hold a Handle so the manager can be restarted against a new
-// root (when the session enters or leaves a worktree) without rewiring
+// adapters hold a Handle so the manager can be replaced (when the session
+// enters or leaves a worktree, or its server set changes) without rewiring
 // them.
 //
-// Handle has a single consumer at a time (the startRuntime restarter
-// goroutine, which serializes on the workspace broker's Subscribe
-// channel). Restart therefore builds newM outside the write lock for
-// latency: two concurrent Restarts would each construct a manager, and
-// the loser would discard its newM silently. If Handle ever fans out
-// to multiple consumers, move the NewManager call inside the write lock.
+// Set swaps in a manager someone else owns, such as one shared between
+// sessions. Restart builds its own: it constructs newM outside the write
+// lock for latency, so two concurrent Restarts would each construct a
+// manager and the loser would discard its newM silently. If Restart ever
+// gains concurrent callers, move the NewManager call inside the write lock.
 type Handle struct {
 	mu      sync.RWMutex
 	m       *Manager
@@ -57,6 +56,16 @@ func (h *Handle) Root() string {
 	return m.Root()
 }
 
+// Set points the handle at m, which may be nil, and returns the manager it
+// pointed at before. It neither runs m nor stops old: the caller owns both.
+func (h *Handle) Set(m *Manager) (old *Manager) {
+	h.mu.Lock()
+	old = h.m
+	h.m = m
+	h.mu.Unlock()
+	return old
+}
+
 // Restart builds a new Manager rooted at root, swaps it in, and returns
 // both managers. The caller shuts old down and runs newM.
 func (h *Handle) Restart(root string) (newM, old *Manager) {
```

2. Add the test.

Apply this change to `internal/lsp/handle_test.go`:

```diff
diff --git a/internal/lsp/handle_test.go b/internal/lsp/handle_test.go
index 710258cd..d39d9824 100644
--- a/internal/lsp/handle_test.go
+++ b/internal/lsp/handle_test.go
@@ -19,3 +19,23 @@ func TestHandleRestartSwapsManager(t *testing.T) {
 		t.Fatal("Get after Restart did not return the new manager")
 	}
 }
+
+func TestHandleSetSwapsInAnOwnedManager(t *testing.T) {
+	h := NewHandle(nil, nil, nil)
+	if h.Get() != nil || h.Root() != "" {
+		t.Fatal("an empty handle should have no manager and no root")
+	}
+	m := NewManager("/a", map[string]ServerSpec{}, nil)
+	if old := h.Set(m); old != nil {
+		t.Fatalf("Set on an empty handle returned old = %p, want nil", old)
+	}
+	if h.Get() != m || h.Root() != "/a" {
+		t.Fatalf("after Set, Get = %p root %q; want %p root /a", h.Get(), h.Root(), m)
+	}
+	if old := h.Set(nil); old != m {
+		t.Fatalf("Set(nil) returned old = %p, want %p", old, m)
+	}
+	if _, ok := h.ServerFor("go"); ok {
+		t.Fatal("ServerFor on a cleared handle reported a server")
+	}
+}
```

| Test | Asserts |
|---|---|
| `TestHandleSetSwapsInAnOwnedManager` | an empty handle has no manager or root; `Set` swaps in and returns the previous manager; a cleared handle reports no server |

**Verify:**

```bash
gofmt -l internal/lsp/                         # no output
TMPDIR=/private/tmp/p1-suite go test ./internal/lsp/ -run TestHandle -count=1   # ok
```

## Task 3: Language-server pool

**Goal:** a pool of `lsp.Manager`s keyed by root and server set, with reference-counted leases and a linger after the last release (spec H5, H6).

**Files:** `internal/app/lsp_pool.go` (new), `internal/app/lsp_pool_test.go` (new).

Parallel-safe with: Tasks 1 and 2.

**Steps:**

1. Create `internal/app/lsp_pool.go` with exactly this content:

```go
package app

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/lsp"
)

// lspPool shares language-server managers between the sessions of one
// project host. A manager is keyed by its root and the exact server set it
// runs: sessions whose configs agree share one set of server processes, and
// a session whose config differs gets its own.
//
// A released manager keeps running for the pool's linger, so a session that
// is replaced (ACP session/load, TUI resume) picks the warm servers back up.
type lspPool struct {
	ctx    context.Context
	log    *slog.Logger
	linger time.Duration

	mu      sync.Mutex
	entries map[string]*lspEntry
	closed  bool
	// wg tracks every manager's Run loop, including ones already stopped but
	// not yet returned, so close can wait for all of them.
	wg sync.WaitGroup
}

// lspEntry is one pooled manager and its Run loop.
type lspEntry struct {
	key    string
	root   string
	m      *lsp.Manager
	refs   int
	cancel context.CancelFunc
	timer  *time.Timer
}

// lspLease is one session's reference to a pooled manager. A nil lease (no
// server applies) is valid: it has no manager, and releasing it does nothing.
type lspLease struct {
	pool  *lspPool
	entry *lspEntry
	once  sync.Once
}

func newLSPPool(ctx context.Context, log *slog.Logger, linger time.Duration) *lspPool {
	if log == nil {
		log = slog.Default()
	}
	return &lspPool{ctx: ctx, log: log, linger: linger, entries: map[string]*lspEntry{}}
}

// manager returns the leased manager, or nil for a nil lease.
func (l *lspLease) manager() *lsp.Manager {
	if l == nil {
		return nil
	}
	return l.entry.m
}

// release drops the lease's reference. Only the first call counts.
func (l *lspLease) release() {
	if l == nil {
		return
	}
	l.once.Do(func() { l.pool.release(l.entry) })
}

// acquire returns a lease on the manager for root and servers, starting one
// when none runs. It returns nil when servers is empty or the pool is closed.
func (p *lspPool) acquire(root string, servers map[string]lsp.ServerSpec) *lspLease {
	if p == nil || len(servers) == 0 {
		return nil
	}
	key := lspPoolKey(root, servers)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	e := p.entries[key]
	if e == nil {
		ctx, cancel := context.WithCancel(p.ctx)
		e = &lspEntry{key: key, root: root, m: lsp.NewManager(root, servers, p.log), cancel: cancel}
		p.entries[key] = e
		p.wg.Add(1)
		go func(m *lsp.Manager) {
			defer p.wg.Done()
			if err := m.Run(ctx); err != nil {
				p.log.Warn("worker exited", "worker", m.Name(), "err", err)
			}
		}(e.m)
	}
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	e.refs++
	return &lspLease{pool: p, entry: e}
}

// release drops one reference to e. The last one stops e's manager after the
// linger, or at once when the pool has none.
func (p *lspPool) release(e *lspEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e.refs--
	if e.refs > 0 || p.entries[e.key] != e {
		return
	}
	if p.linger <= 0 {
		p.stopLocked(e)
		return
	}
	e.timer = time.AfterFunc(p.linger, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if e.refs == 0 && p.entries[e.key] == e {
			p.stopLocked(e)
		}
	})
}

// stopLocked removes e from the pool and cancels its Run loop. p.mu is held.
func (p *lspPool) stopLocked(e *lspEntry) {
	delete(p.entries, e.key)
	e.cancel()
}

// running returns a manager the pool runs for root, or nil. The index pass
// borrows it for document symbols without taking a reference.
func (p *lspPool) running(root string) *lsp.Manager {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var found *lspEntry
	for _, e := range p.entries {
		if e.root == root && (found == nil || e.key < found.key) {
			found = e
		}
	}
	if found == nil {
		return nil
	}
	return found.m
}

// close stops every manager, ignoring the linger, and waits for their Run
// loops to return or ctx to end. Later acquires return nil.
func (p *lspPool) close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	p.closed = true
	for _, e := range p.entries {
		if e.timer != nil {
			e.timer.Stop()
		}
		p.stopLocked(e)
	}
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("language servers did not stop: %w", ctx.Err())
	}
}

// lspPoolKey identifies a manager: its root plus every language, command and
// argument it runs, in a stable order.
func lspPoolKey(root string, servers map[string]lsp.ServerSpec) string {
	langs := make([]string, 0, len(servers))
	for lang := range servers {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	var b strings.Builder
	b.WriteString(root)
	for _, lang := range langs {
		spec := servers[lang]
		b.WriteString("\x00" + lang + "\x00" + spec.Command)
		for _, arg := range spec.Args {
			b.WriteString("\x01" + arg)
		}
	}
	return b.String()
}

// lspServersFor is the server set a session configured with cfg runs at root:
// nil when LSP is disabled or no server applies to root's languages.
func lspServersFor(cfg config.Config, root string) map[string]lsp.ServerSpec {
	if !lspEnabled(cfg.LSP) {
		return nil
	}
	servers := lsp.DetectServers(toServerSpecs(cfg.LSP.Servers), disabledLangs(cfg.LSP.Servers))
	if len(servers) == 0 {
		return nil
	}
	forRoot := lsp.ServersForRoot(servers, root)
	if len(forRoot) == 0 {
		return nil
	}
	return forRoot
}
```

`lspServersFor` holds the rule `buildAgentRunnerWithLock` applies today (`lspEnabled`, `lsp.DetectServers`, `lsp.ServersForRoot`), returning nil instead of an empty manager when no server applies.

2. Create `internal/app/lsp_pool_test.go` with exactly this content:

```go
package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/lsp"
)

// absentServer is a server set whose command does not exist: the manager
// still exists and is pooled, its one language just never comes up.
func absentServer(t *testing.T, args ...string) map[string]lsp.ServerSpec {
	t.Helper()
	return map[string]lsp.ServerSpec{"go": {Command: filepath.Join(t.TempDir(), "no-such-server"), Args: args}}
}

func testLSPPool(t *testing.T, linger time.Duration) *lspPool {
	t.Helper()
	p := newLSPPool(context.Background(), logging.New(io.Discard, slog.LevelInfo, false), linger)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.close(ctx)
	})
	return p
}

func waitForStoppedManager(t *testing.T, p *lspPool, root string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for p.running(root) != nil {
		if time.Now().After(deadline) {
			t.Fatalf("manager for %s still pooled after the linger", root)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLSPPoolSharesAManagerPerRootAndServerSet(t *testing.T) {
	p := testLSPPool(t, time.Minute)
	servers := absentServer(t)

	a := p.acquire("/repo", servers)
	b := p.acquire("/repo", servers)
	if a.manager() == nil || a.manager() != b.manager() {
		t.Fatalf("two leases on one root and server set got managers %p and %p; want one shared manager", a.manager(), b.manager())
	}
	if other := p.acquire("/repo/.marshal/worktrees/x", servers); other.manager() == a.manager() {
		t.Fatal("a different root shared the root's manager")
	}
	changed := servers["go"]
	changed.Args = []string{"-remote=auto"}
	if diff := p.acquire("/repo", map[string]lsp.ServerSpec{"go": changed}); diff.manager() == a.manager() {
		t.Fatal("a different server set shared the manager")
	}
	if p.running("/repo") == nil {
		t.Fatal("running did not report the root's manager")
	}
}

func TestLSPPoolKeepsAReleasedManagerThroughTheLinger(t *testing.T) {
	p := testLSPPool(t, 100*time.Millisecond)
	servers := absentServer(t)

	first := p.acquire("/repo", servers)
	m := first.manager()
	first.release()
	first.release() // only the first release counts

	again := p.acquire("/repo", servers)
	if again.manager() != m {
		t.Fatal("an acquire inside the linger started a new manager")
	}
	again.release()
	waitForStoppedManager(t, p, "/repo")

	if fresh := p.acquire("/repo", servers); fresh.manager() == m {
		t.Fatal("an acquire after the linger reused the stopped manager")
	}
}

func TestLSPPoolWithoutLingerStopsOnLastRelease(t *testing.T) {
	p := testLSPPool(t, 0)
	servers := absentServer(t)
	a := p.acquire("/repo", servers)
	b := p.acquire("/repo", servers)
	a.release()
	if p.running("/repo") == nil {
		t.Fatal("the manager stopped while a lease was still held")
	}
	b.release()
	if p.running("/repo") != nil {
		t.Fatal("the last release did not stop the manager")
	}
}

func TestLSPPoolWithoutServersLeasesNothing(t *testing.T) {
	p := testLSPPool(t, time.Minute)
	lease := p.acquire("/repo", nil)
	if lease != nil || lease.manager() != nil {
		t.Fatal("an empty server set produced a lease")
	}
	lease.release() // a nil lease is safe to release
}

func TestLSPPoolCloseStopsEveryManager(t *testing.T) {
	p := newLSPPool(context.Background(), logging.New(io.Discard, slog.LevelInfo, false), time.Minute)
	servers := absentServer(t)
	p.acquire("/a", servers)
	released := p.acquire("/b", servers)
	released.release() // lingering, not yet stopped

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if p.running("/a") != nil || p.running("/b") != nil {
		t.Fatal("close left a manager pooled")
	}
	if p.acquire("/a", servers) != nil {
		t.Fatal("acquire after close returned a lease")
	}
}

func TestLSPServersForFollowsConfigAndRootLanguages(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.LSP.Servers = map[string]config.LSPServerConfig{"go": {Command: "/opt/gopls-test"}}

	got := lspServersFor(cfg, root)
	if got["go"].Command != "/opt/gopls-test" {
		t.Fatalf("lspServersFor = %v, want the configured go server", got)
	}
	if bare := lspServersFor(cfg, t.TempDir()); bare != nil {
		t.Fatalf("lspServersFor on a root with no Go files = %v, want nil", bare)
	}
	off := false
	cfg.LSP.Enabled = &off
	if disabled := lspServersFor(cfg, root); disabled != nil {
		t.Fatalf("lspServersFor with LSP disabled = %v, want nil", disabled)
	}
}
```

| Test | Asserts |
|---|---|
| `TestLSPPoolSharesAManagerPerRootAndServerSet` | one root and server set share a manager; another root or another argument list does not |
| `TestLSPPoolKeepsAReleasedManagerThroughTheLinger` | an acquire inside the linger reuses the manager; after it, a new one starts |
| `TestLSPPoolWithoutLingerStopsOnLastRelease` | with no linger the last release stops the manager, and not before |
| `TestLSPPoolWithoutServersLeasesNothing` | an empty server set gives a nil lease that is safe to use |
| `TestLSPPoolCloseStopsEveryManager` | close stops held and lingering managers and refuses later acquires |
| `TestLSPServersForFollowsConfigAndRootLanguages` | a configured server applies only where its language occurs, and not when LSP is disabled |

**Verify:**

```bash
gofmt -l internal/app/                         # no output
go vet ./internal/app/
TMPDIR=/private/tmp/p1-suite go test ./internal/app/ -run 'TestLSPPool|TestLSPServersFor' -count=1   # ok
```

## Task 4: Index supervisor

**Goal:** the lease-aware index loop of spec H4 exists as a value with injected lease, scan and watcher functions, testable without a host.

**Files:** `internal/app/host_index.go` (new), `internal/app/host_index_test.go` (new).

Depends on: Task 1 (`TestIndexSupervisorsOnOneRootRunOneIndexer` uses `index.TryLease`). Parallel-safe with: Task 2.

**Steps:**

1. Create `internal/app/host_index.go` with exactly this content:

```go
package app

import (
	"context"
	"log/slog"
	"time"
)

// indexLeaseRetry is how often a host whose watcher is on retries an index
// lease another process holds.
const indexLeaseRetry = 30 * time.Second

// indexSupervisor runs one project root's index work under the cross-process
// index lease (index.TryLease): a startup scan, then the watcher when it is
// on. Only the lease holder indexes, so two processes on one tree never scan
// and embed the same files twice.
type indexSupervisor struct {
	root string
	// watch reports whether the watcher runs after the startup scan.
	watch bool
	// retry is how long a watching supervisor waits before trying a held
	// lease again.
	retry    time.Duration
	log      *slog.Logger
	tryLease func(root string) (release func(), held bool, err error)
	// scan runs one index pass: the startup scan.
	scan func(ctx context.Context) error
	// watcher watches root and runs a pass after each change, until ctx ends.
	watcher func(ctx context.Context) error
}

// run does the supervisor's work and returns when it is done or ctx ends.
//
// A held lease means another process is indexing now: without the watcher
// there is nothing left to do, and with it the supervisor waits to take over
// when that process exits. A lease that cannot be taken for any other reason
// is logged and ignored, because indexing twice costs work but breaks nothing.
func (s indexSupervisor) run(ctx context.Context) {
	for {
		release, held, err := s.tryLease(s.root)
		if err != nil {
			s.log.Warn("index lease unavailable; indexing without it", "root", s.root, "error", err)
			release, held = func() {}, true
		}
		if held {
			s.indexHeld(ctx)
			release()
			return
		}
		if !s.watch {
			s.log.Info("another process holds the index lease; skipping the startup scan", "root", s.root)
			return
		}
		t := time.NewTimer(s.retry)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// indexHeld runs the startup scan and, after a successful one, the watcher.
// A failed scan ends the supervisor, so a process that cannot index (an
// unreachable embedder, say) releases the lease to one that can.
func (s indexSupervisor) indexHeld(ctx context.Context) {
	if err := s.scan(ctx); err != nil {
		if ctx.Err() == nil {
			s.log.Warn("startup index scan failed; releasing the index lease", "root", s.root, "error", err)
		}
		return
	}
	if !s.watch {
		return
	}
	if err := s.watcher(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("index watcher stopped", "root", s.root, "error", err)
	}
}
```

2. Create `internal/app/host_index_test.go` with exactly this content:

```go
package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/logging"
	"marshal/internal/index"
)

// fakeLease stands in for index.TryLease: it reports the lease held by
// another process for the first busy tries, then free.
type fakeLease struct {
	mu       sync.Mutex
	busy     int
	err      error
	tries    int
	releases int
}

func (f *fakeLease) try(string) (func(), bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tries++
	if f.err != nil {
		return nil, false, f.err
	}
	if f.tries <= f.busy {
		return nil, false, nil
	}
	return func() {
		f.mu.Lock()
		f.releases++
		f.mu.Unlock()
	}, true, nil
}

func (f *fakeLease) counts() (tries, releases int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tries, f.releases
}

// countingSupervisor is a supervisor over l whose scan and watcher only count
// their calls. The watcher returns as soon as it starts.
func countingSupervisor(l *fakeLease, watch bool, scanErr error, scans, watches *int32) indexSupervisor {
	return indexSupervisor{
		root:     "/repo",
		watch:    watch,
		retry:    time.Millisecond,
		log:      logging.New(io.Discard, slog.LevelInfo, false),
		tryLease: l.try,
		scan: func(context.Context) error {
			atomic.AddInt32(scans, 1)
			return scanErr
		},
		watcher: func(context.Context) error {
			atomic.AddInt32(watches, 1)
			return nil
		},
	}
}

func TestIndexSupervisorWithoutWatcherSkipsAHeldLease(t *testing.T) {
	l := &fakeLease{busy: 1}
	var scans, watches int32
	countingSupervisor(l, false, nil, &scans, &watches).run(context.Background())
	if tries, _ := l.counts(); scans != 0 || watches != 0 || tries != 1 {
		t.Fatalf("scans %d, watches %d, tries %d; want no scan and one try", scans, watches, tries)
	}
}

func TestIndexSupervisorWithoutWatcherScansOnceAndReleases(t *testing.T) {
	l := &fakeLease{}
	var scans, watches int32
	countingSupervisor(l, false, nil, &scans, &watches).run(context.Background())
	if _, releases := l.counts(); scans != 1 || watches != 0 || releases != 1 {
		t.Fatalf("scans %d, watches %d, releases %d; want one scan, no watcher, one release", scans, watches, releases)
	}
}

func TestIndexSupervisorWithWatcherWaitsForTheLeaseThenScansAndWatches(t *testing.T) {
	l := &fakeLease{busy: 2}
	var scans, watches int32
	countingSupervisor(l, true, nil, &scans, &watches).run(context.Background())
	tries, releases := l.counts()
	if tries != 3 || scans != 1 || watches != 1 || releases != 1 {
		t.Fatalf("tries %d, scans %d, watches %d, releases %d; want 3 tries, then one scan, one watcher, one release", tries, scans, watches, releases)
	}
}

func TestIndexSupervisorWithWatcherStopsWaitingWhenCancelled(t *testing.T) {
	l := &fakeLease{busy: 1 << 30}
	var scans, watches int32
	s := countingSupervisor(l, true, nil, &scans, &watches)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor kept waiting for the lease after cancellation")
	}
	if scans != 0 {
		t.Fatalf("scans %d; want none without the lease", scans)
	}
}

func TestIndexSupervisorFailedScanReleasesTheLeaseAndStops(t *testing.T) {
	l := &fakeLease{}
	var scans, watches int32
	countingSupervisor(l, true, errors.New("embedding probe failed"), &scans, &watches).run(context.Background())
	if tries, releases := l.counts(); tries != 1 || scans != 1 || watches != 0 || releases != 1 {
		t.Fatalf("tries %d, scans %d, watches %d, releases %d; want one failed scan, released, no watcher", tries, scans, watches, releases)
	}
}

func TestIndexSupervisorIndexesWithoutAnUnusableLease(t *testing.T) {
	l := &fakeLease{err: errors.New("open index lease: permission denied")}
	var scans, watches int32
	countingSupervisor(l, false, nil, &scans, &watches).run(context.Background())
	if scans != 1 {
		t.Fatalf("scans %d; want the scan to run without the lease", scans)
	}
}

// Two supervisors on one root with the real lease stand in for two processes:
// while the first watches, the second skips its scan.
func TestIndexSupervisorsOnOneRootRunOneIndexer(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := logging.New(io.Discard, slog.LevelInfo, false)
	watching := make(chan struct{})
	var firstScans, secondScans int32
	first := indexSupervisor{root: root, watch: true, retry: time.Hour, log: log, tryLease: index.TryLease,
		scan: func(context.Context) error { atomic.AddInt32(&firstScans, 1); return nil },
		watcher: func(ctx context.Context) error {
			close(watching)
			<-ctx.Done()
			return nil
		},
	}
	second := indexSupervisor{root: root, watch: false, retry: time.Hour, log: log, tryLease: index.TryLease,
		scan: func(context.Context) error { atomic.AddInt32(&secondScans, 1); return nil },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		first.run(ctx)
		close(done)
	}()
	<-watching
	skipped := make(chan struct{})
	go func() {
		second.run(context.Background())
		close(skipped)
	}()
	select {
	case <-skipped:
	case <-time.After(5 * time.Second):
		t.Fatal("the second supervisor waited for the held lease instead of skipping its scan")
	}
	cancel()
	<-done

	if firstScans != 1 || secondScans != 0 {
		t.Fatalf("first scanned %d times, second %d; want only the lease holder to scan", firstScans, secondScans)
	}
	release, held, err := index.TryLease(root)
	if err != nil || !held {
		t.Fatalf("lease after the holder stopped: held %v, err %v; want it free", held, err)
	}
	release()
}
```

| Test | Asserts |
|---|---|
| `TestIndexSupervisorWithoutWatcherSkipsAHeldLease` | watcher off and lease held: no scan, one try |
| `TestIndexSupervisorWithoutWatcherScansOnceAndReleases` | watcher off and lease free: one scan, no watcher, released |
| `TestIndexSupervisorWithWatcherWaitsForTheLeaseThenScansAndWatches` | watcher on: retries a held lease, then scans, watches and releases |
| `TestIndexSupervisorWithWatcherStopsWaitingWhenCancelled` | cancellation ends the wait without a scan |
| `TestIndexSupervisorFailedScanReleasesTheLeaseAndStops` | a failed startup scan releases the lease and never starts the watcher |
| `TestIndexSupervisorIndexesWithoutAnUnusableLease` | a lease error is logged and indexing goes ahead |
| `TestIndexSupervisorsOnOneRootRunOneIndexer` | with the real lease, a second supervisor skips while the first watches, and the lease is free after both stop |

**Verify:**

```bash
gofmt -l internal/app/                         # no output
go vet ./internal/app/
TMPDIR=/private/tmp/p1-suite go test ./internal/app/ -run TestIndexSupervisor -count=1   # ok
```

## Task 5: Project hosts

**Goal:** a process-wide `Hosts` registry with one `ProjectHost` per project root: database bootstrap once per host, the language-server pool, index work started by the first attached session and restarted only when the watcher setting changes, progress fan-out, the linger, and close (spec H1–H3, H6, H7). Nothing uses it yet.

**Files:** `internal/app/hosts.go` (new), `internal/app/app.go`, `internal/app/hosts_test.go` (new).

Depends on: Tasks 1, 3 and 4.

**Steps:**

1. Create `internal/app/hosts.go` with exactly this content:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/dbmigrate"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/index"
	"marshal/internal/llm/embedding"
	"marshal/internal/llm/routing"
	"marshal/internal/lsp"
	"marshal/internal/pubsub"
	"marshal/internal/worker"
)

// hostLinger is how long a project host, and each language server it pools,
// stays up after its last session leaves. ACP session/load and
// session/resume, and TUI resume, close the old runtime before starting the
// new one; inside the linger the new one reuses the open database, the
// language servers and the index.
const hostLinger = 30 * time.Second

// hostCloseTimeout bounds how long closing a host, or restarting its index
// work, waits for that work and for the language servers to stop.
const hostCloseTimeout = 5 * time.Second

// errHostsClosed is returned when a session starts on a closed registry.
var errHostsClosed = errors.New("app: project hosts are closed")

// Hosts is a process's registry of project hosts, one per project root.
// acp.Run, acp.ListenAndServe and Run each create one, pass it to every
// runtime with WithHosts, and close it after every session has closed.
type Hosts struct {
	// indexing starts each host's index work. A runtime's private registry
	// leaves it off.
	indexing bool
	// workers, when set, replace each host's index supervisor (Run's
	// WithWorker test seam).
	workers []worker.Worker
	linger  time.Duration
	// leaseRetry and tryLease feed each host's index supervisor.
	leaseRetry time.Duration
	tryLease   func(root string) (release func(), held bool, err error)

	mu    sync.Mutex
	hosts map[string]*ProjectHost
	// closing holds a channel per root whose retired host is still closing;
	// it is closed when that close finishes.
	closing map[string]chan struct{}
	closed  bool
}

// NewHosts returns a registry whose hosts index their projects.
func NewHosts() *Hosts { return newHosts(true, hostLinger) }

func newHosts(indexing bool, linger time.Duration) *Hosts {
	return &Hosts{
		indexing:   indexing,
		linger:     linger,
		leaseRetry: indexLeaseRetry,
		tryLease:   index.TryLease,
		hosts:      map[string]*ProjectHost{},
		closing:    map[string]chan struct{}{},
	}
}

// WithHosts starts the runtime on hosts' project host for its root, so it
// shares that project's database setup, index and language servers with the
// process's other sessions. Without it a runtime gets a private registry,
// whose host does not index and closes with the runtime.
func WithHosts(h *Hosts) Option {
	return func(o *options) { o.hosts = h }
}

// ProjectHost is the part of a runtime that belongs to a project root rather
// than to one session: database setup, the index and the pooled language
// servers. Sessions attach to it and release it; see Hosts.
type ProjectHost struct {
	root  string
	hosts *Hosts
	// refs counts the runtimes holding the host and timer is the linger after
	// the last release. Both are guarded by hosts.mu.
	refs  int
	timer *time.Timer
	// ready is closed once open has finished; openErr is its result.
	ready   chan struct{}
	openErr error

	ctx     context.Context
	cancel  context.CancelFunc
	db      *db.DB
	log     *slog.Logger
	logFile *os.File
	lsp     *lspPool
	reports *pubsub.Broker[index.Report]

	// indexMu serializes starting, restarting and stopping the index work. It
	// is never held by an index pass, so stopping a pass under it cannot wait
	// on itself.
	indexMu sync.Mutex

	mu        sync.Mutex
	sessions  map[*session.State]struct{}
	cfg       config.Config
	projectID int64
	attached  bool
	indexKey  string
	indexStop func(context.Context) error
	closed    bool
}

// Root returns the project root the host serves.
func (h *ProjectHost) Root() string { return h.root }

// acquire returns root's host with a reference taken, opening it on first
// use. A root whose previous host is still closing waits for that close, so
// its index lease and language servers are released first.
func (hs *Hosts) acquire(root string, now time.Time) (*ProjectHost, error) {
	for {
		hs.mu.Lock()
		if hs.closed {
			hs.mu.Unlock()
			return nil, errHostsClosed
		}
		if h := hs.hosts[root]; h != nil {
			h.refs++
			if h.timer != nil {
				h.timer.Stop()
				h.timer = nil
			}
			hs.mu.Unlock()
			<-h.ready
			if h.openErr != nil {
				return nil, h.openErr
			}
			return h, nil
		}
		if wait := hs.closing[root]; wait != nil {
			hs.mu.Unlock()
			<-wait
			continue
		}
		h := &ProjectHost{root: root, hosts: hs, refs: 1, ready: make(chan struct{})}
		hs.hosts[root] = h
		hs.mu.Unlock()

		h.openErr = h.open(now)
		if h.openErr != nil {
			// A failed host is not kept: the next start tries again.
			hs.mu.Lock()
			if hs.hosts[root] == h {
				delete(hs.hosts, root)
			}
			hs.mu.Unlock()
		}
		close(h.ready)
		if h.openErr != nil {
			return nil, h.openErr
		}
		return h, nil
	}
}

// open sets up the project's database once for every session the host will
// serve, and the host's own log handle, language-server pool and index
// broker.
func (h *ProjectHost) open(now time.Time) error {
	if err := dbmigrate.AdoptStrayProjectDB(h.root, slog.Default()); err != nil {
		slog.Warn("stray .marshal migration", "error", err)
	}
	database, err := db.Open(db.Path(h.root))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	if err := database.Migrate(); err != nil {
		_ = database.Close()
		return fmt.Errorf("migrate database: %w", err)
	}
	// Reconcile generations a crashed process left open. This runs once per
	// host, not once per session: a second session in this process must not
	// end the first one's live generation.
	if _, err := database.ReconcileOpenGenerations(now); err != nil {
		_ = database.Close()
		return fmt.Errorf("reconcile open generations: %w", err)
	}

	// The host logs through its own handle: a session's log file closes with
	// that session, and the host outlives it.
	logWriter := io.Discard
	if f, err := os.OpenFile(logPath(h.root), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		h.logFile = f
		logWriter = f
	}
	h.log = logging.New(logWriter, slog.LevelInfo, false)
	h.db = database
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.lsp = newLSPPool(h.ctx, h.log, h.hosts.linger)
	h.reports = pubsub.NewBroker[index.Report]()
	h.sessions = map[*session.State]struct{}{}
	return nil
}

// release drops one runtime's reference. The last one closes the host after
// the registry's linger, or at once when it has none.
func (h *ProjectHost) release() {
	if h == nil {
		return
	}
	hs := h.hosts
	hs.mu.Lock()
	h.refs--
	if h.refs > 0 || hs.closed || hs.hosts[h.root] != h {
		hs.mu.Unlock()
		return
	}
	if hs.linger <= 0 {
		hs.retireLocked(h)
		return
	}
	h.timer = time.AfterFunc(hs.linger, func() {
		hs.mu.Lock()
		if h.refs > 0 || hs.closed || hs.hosts[h.root] != h {
			hs.mu.Unlock()
			return
		}
		hs.retireLocked(h)
	})
	hs.mu.Unlock()
}

// retireLocked removes h from the registry and closes it. hs.mu is held on
// entry and released before the close, which can take hostCloseTimeout.
func (hs *Hosts) retireLocked(h *ProjectHost) {
	delete(hs.hosts, h.root)
	done := make(chan struct{})
	hs.closing[h.root] = done
	hs.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), hostCloseTimeout)
	if err := h.close(ctx); err != nil {
		h.log.Warn("project host close", "root", h.root, "error", err)
	}
	cancel()

	hs.mu.Lock()
	delete(hs.closing, h.root)
	hs.mu.Unlock()
	close(done)
}

// Close closes every host at once, ignoring the linger, and waits for hosts
// that were already closing. Sessions started afterwards fail.
func (hs *Hosts) Close(ctx context.Context) error {
	if hs == nil {
		return nil
	}
	hs.mu.Lock()
	hs.closed = true
	open := make([]*ProjectHost, 0, len(hs.hosts))
	for root, h := range hs.hosts {
		if h.timer != nil {
			h.timer.Stop()
			h.timer = nil
		}
		open = append(open, h)
		delete(hs.hosts, root)
	}
	closing := make([]chan struct{}, 0, len(hs.closing))
	for _, ch := range hs.closing {
		closing = append(closing, ch)
	}
	hs.mu.Unlock()

	var errs []error
	for _, h := range open {
		<-h.ready
		if h.openErr != nil {
			continue
		}
		if err := h.close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close project host %s: %w", h.root, err))
		}
	}
	for _, ch := range closing {
		select {
		case <-ch:
		case <-ctx.Done():
			return errors.Join(append(errs, ctx.Err())...)
		}
	}
	return errors.Join(errs...)
}

// close stops the index work and the language servers, waiting for them up
// to ctx, then closes the index broker, the database connection and the log
// file. Only the first call does anything.
func (h *ProjectHost) close(ctx context.Context) error {
	h.indexMu.Lock()
	defer h.indexMu.Unlock()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	stop := h.indexStop
	h.indexStop = nil
	h.mu.Unlock()

	h.cancel()
	var errs []error
	if stop != nil {
		if err := stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if err := h.lsp.close(ctx); err != nil {
		errs = append(errs, err)
	}
	h.reports.Close()
	if err := h.db.Close(); err != nil {
		errs = append(errs, fmt.Errorf("db close: %w", err))
	}
	if h.logFile != nil {
		_ = h.logFile.Close()
	}
	return errors.Join(errs...)
}

// attach registers a session on the host and makes cfg the host's current
// config. The first session's project starts the index work.
func (h *ProjectHost) attach(state *session.State, cfg config.Config, projectID int64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	if state != nil {
		h.sessions[state] = struct{}{}
	}
	h.cfg = cfg
	if !h.attached {
		h.attached = true
		h.projectID = projectID
	}
	h.mu.Unlock()
	h.refreshIndex()
}

// detach stops sending index progress to state.
func (h *ProjectHost) detach(state *session.State) {
	if h == nil || state == nil {
		return
	}
	h.mu.Lock()
	delete(h.sessions, state)
	h.mu.Unlock()
}

// noteConfig makes cfg the host's current config after a session's config
// reload. Index passes read it from then on.
func (h *ProjectHost) noteConfig(cfg config.Config) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.cfg = cfg
	h.mu.Unlock()
	h.refreshIndex()
}

// refreshIndex starts the index work once a session has attached, and
// restarts it when the current config changes whether the watcher runs or
// its debounce. Other config changes need no restart: every pass reads the
// current config.
func (h *ProjectHost) refreshIndex() {
	if !h.hosts.indexing {
		return
	}
	h.indexMu.Lock()
	defer h.indexMu.Unlock()

	h.mu.Lock()
	if h.closed || !h.attached {
		h.mu.Unlock()
		return
	}
	cfg := h.cfg
	key := indexKeyFor(cfg)
	if h.indexStop != nil && key == h.indexKey {
		h.mu.Unlock()
		return
	}
	stop := h.indexStop
	h.indexStop = nil
	h.mu.Unlock()

	if stop != nil {
		ctx, cancel := context.WithTimeout(context.Background(), hostCloseTimeout)
		if err := stop(ctx); err != nil {
			h.log.Warn("index work did not stop before its restart", "root", h.root, "error", err)
		}
		cancel()
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.indexKey = key
	h.indexStop = h.startIndex(cfg)
}

// startIndex starts the host's index work for cfg and returns a func that
// stops it and waits for it to return.
func (h *ProjectHost) startIndex(cfg config.Config) func(context.Context) error {
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan struct{})
	if workers := h.hosts.workers; len(workers) > 0 {
		var wg sync.WaitGroup
		for _, w := range workers {
			startWorker(ctx, &wg, w, h.log)
		}
		go func() {
			wg.Wait()
			close(done)
		}()
	} else {
		sup := h.supervisor(cfg)
		go func() {
			defer close(done)
			sup.run(ctx)
		}()
	}
	return func(stopCtx context.Context) error {
		cancel()
		select {
		case <-done:
			return nil
		case <-stopCtx.Done():
			return fmt.Errorf("index work did not stop: %w", stopCtx.Err())
		}
	}
}

// supervisor is the index supervisor for cfg: a startup scan, then the
// watcher when config.WatchEnabled allows it.
func (h *ProjectHost) supervisor(cfg config.Config) indexSupervisor {
	debounce := time.Duration(cfg.Indexing.WatchDebounceMs) * time.Millisecond
	return indexSupervisor{
		root:     h.root,
		watch:    config.WatchEnabled(cfg.Indexing.Watch, embeddingRouteResolves(cfg)),
		retry:    h.hosts.leaseRetry,
		log:      h.log,
		tryLease: h.hosts.tryLease,
		scan:     h.indexPass,
		watcher: func(ctx context.Context) error {
			return index.NewWatcher(h.root, debounce, h.indexPass, h.log).Run(ctx)
		},
	}
}

// indexKeyFor is the part of cfg the running index work was built from.
func indexKeyFor(cfg config.Config) string {
	return fmt.Sprintf("watch=%t debounce=%d", config.WatchEnabled(cfg.Indexing.Watch, embeddingRouteResolves(cfg)), cfg.Indexing.WatchDebounceMs)
}

// embeddingRouteResolves reports whether cfg names an embedding route that
// resolves.
func embeddingRouteResolves(cfg config.Config) bool {
	_, err := routing.NewStaticRouter(cfg.RoutingConfig()).ResolveEmbedding()
	return err == nil
}

// indexPass runs one index pass over the host root with the current config:
// scan, file index, symbols and embeddings. It is the startup scan and every
// watcher rerun. A successful pass is published on the host's report broker.
func (h *ProjectHost) indexPass(c context.Context) error {
	h.mu.Lock()
	cfg, projectID := h.cfg, h.projectID
	h.mu.Unlock()

	embedder := resolveEmbedderFromConfig(cfg)
	if embedder != nil {
		probeCtx, cancel := context.WithTimeout(c, 30*time.Second)
		defer cancel()
		if _, err := embedding.Probe(probeCtx, embedder); err != nil {
			return fmt.Errorf("embedding probe failed: %w", err)
		}
	}

	passCtx, cancel := context.WithTimeout(c, 10*time.Minute)
	defer cancel()
	deps := index.Deps{
		DB:         h.db,
		Root:       h.root,
		Ignore:     cfg.Indexing.Ignore,
		MaxBytes:   cfg.Indexing.MaxIndexableFileBytes,
		Embedder:   embedder,
		OnProgress: h.progress,
	}
	// Document symbols come from a language server the pool already runs for
	// the root, if any; the pass never starts one.
	if m := h.lsp.running(h.root); m != nil {
		deps.LSP = lsp.NewSymbolAdapter(lsp.NewHandle(m, nil, h.log))
	}
	rep, err := index.Run(passCtx, deps, projectID)
	if err == nil {
		h.reports.Publish(indexEventCompleted, rep)
	}
	return err
}

// progress shows an index pass's progress message on every attached session.
func (h *ProjectHost) progress(msg string) {
	h.mu.Lock()
	states := make([]*session.State, 0, len(h.sessions))
	for s := range h.sessions {
		states = append(states, s)
	}
	h.mu.Unlock()
	for _, s := range states {
		s.SetActivity(session.Activity{Kind: session.ActivityTool, Label: msg})
	}
}
```

Lock order: `indexMu` above `mu`; `Hosts.mu` is never held while a host closes. An index pass takes only `mu` (for its config and progress), so stopping the index under `indexMu` cannot wait on itself.

2. Add the registry to the runtime options, after `sessionRouting`.

Apply this change to `internal/app/app.go`:

```diff
diff --git a/internal/app/app.go b/internal/app/app.go
index 0e3f71b1..f2dfdaa0 100644
--- a/internal/app/app.go
+++ b/internal/app/app.go
@@ -105,6 +105,9 @@ type options struct {
 	workers           []worker.Worker
 	configReloader    func(config.Config) error
 	sessionRouting    config.SessionRouting
+	// hosts is the process's project-host registry (WithHosts). Nil gives the
+	// runtime a private one.
+	hosts *Hosts
 	// authResolver is the credential/OAuth dependency the runtime hands to
 	// every credential-backed consumer it builds: provider construction, the
 	// OAuth refresh workers and the MCP managers. authResolverSet records an
```

3. Create `internal/app/hosts_test.go` with exactly this content:

```go
package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/index"
)

// hostRoot is a project root with the .marshal directory startRuntime would
// already have created.
func hostRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func testHosts(t *testing.T, indexing bool, linger time.Duration) *Hosts {
	t.Helper()
	hs := newHosts(indexing, linger)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Close(ctx)
	})
	return hs
}

func mustAcquire(t *testing.T, hs *Hosts, root string) *ProjectHost {
	t.Helper()
	h, err := hs.acquire(root, time.Now())
	if err != nil {
		t.Fatalf("acquire(%s): %v", root, err)
	}
	return h
}

func hostClosed(h *ProjectHost) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

func waitForHostClosed(t *testing.T, h *ProjectHost) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !hostClosed(h) {
		if time.Now().After(deadline) {
			t.Fatal("host still open after the linger")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHostsShareOneHostPerRoot(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	root, other := hostRoot(t), hostRoot(t)

	a := mustAcquire(t, hs, root)
	b := mustAcquire(t, hs, root)
	if a != b {
		t.Fatal("two acquires on one root opened two hosts")
	}
	if c := mustAcquire(t, hs, other); c == a {
		t.Fatal("a different root shared the host")
	}
	if a.Root() != root {
		t.Fatalf("Root() = %q, want %q", a.Root(), root)
	}
}

// openGeneration starts session id in the host's project with one generation
// left open, the state a live session has.
func openGeneration(t *testing.T, database *db.DB, root, id string) {
	t.Helper()
	projectID, err := database.GetOrCreateProject(root, "hosts-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession(id, projectID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := database.BeginGeneration(db.Generation{ID: id + "-gen", SessionID: id, Seq: 1, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func generationOpen(t *testing.T, database *db.DB, id string) bool {
	t.Helper()
	gens, err := database.GenerationsForSession(id)
	if err != nil || len(gens) != 1 {
		t.Fatalf("generations for %s = %v, %v", id, gens, err)
	}
	return gens[0].EndedAt == nil
}

func TestHostReconcilesOpenGenerationsOncePerHost(t *testing.T) {
	hs := testHosts(t, false, 0)
	root := hostRoot(t)

	first := mustAcquire(t, hs, root)
	openGeneration(t, first.db, root, "sess_live")
	second := mustAcquire(t, hs, root)
	if !generationOpen(t, second.db, "sess_live") {
		t.Fatal("a second session on an open host ended the first session's live generation")
	}

	first.release()
	second.release()
	if !hostClosed(first) {
		t.Fatal("without a linger, the last release did not close the host")
	}
	reopened := mustAcquire(t, hs, root)
	if generationOpen(t, reopened.db, "sess_live") {
		t.Fatal("a fresh host did not reconcile a generation left open")
	}
}

func TestHostLingersAfterItsLastRelease(t *testing.T) {
	hs := testHosts(t, false, 100*time.Millisecond)
	root := hostRoot(t)

	h := mustAcquire(t, hs, root)
	h.release()
	if again := mustAcquire(t, hs, root); again != h {
		t.Fatal("an acquire inside the linger opened a new host")
	}
	h.release()
	waitForHostClosed(t, h)
	if fresh := mustAcquire(t, hs, root); fresh == h {
		t.Fatal("an acquire after the linger reused the closed host")
	}
}

func TestHostsCloseClosesAttachedHostsAtOnce(t *testing.T) {
	hs := newHosts(false, time.Minute)
	h := mustAcquire(t, hs, hostRoot(t))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hs.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !hostClosed(h) {
		t.Fatal("Close left an attached host open")
	}
	h.release() // a release after Close is harmless
	if _, err := hs.acquire(hostRoot(t), time.Now()); !errors.Is(err, errHostsClosed) {
		t.Fatalf("acquire after Close: err = %v, want errHostsClosed", err)
	}
}

func TestHostOpenWaitsForItsRootsPreviousClose(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	root := hostRoot(t)
	closing := make(chan struct{})
	hs.mu.Lock()
	hs.closing[root] = closing
	hs.mu.Unlock()

	got := make(chan *ProjectHost, 1)
	go func() {
		h, _ := hs.acquire(root, time.Now())
		got <- h
	}()
	select {
	case <-got:
		t.Fatal("acquire opened a host while the root's previous host was closing")
	case <-time.After(50 * time.Millisecond):
	}
	hs.mu.Lock()
	delete(hs.closing, root)
	hs.mu.Unlock()
	close(closing)
	select {
	case h := <-got:
		if h == nil {
			t.Fatal("acquire returned no host")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acquire did not open the host after the previous close finished")
	}
}

func TestHostOpenFailureIsNotKept(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	root := t.TempDir()
	// A file where the .marshal directory belongs makes the database
	// unopenable.
	if err := os.WriteFile(filepath.Join(root, ".marshal"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.acquire(root, time.Now()); err == nil || !strings.Contains(err.Error(), "open database") {
		t.Fatalf("acquire on an unopenable database: err = %v, want an open database error", err)
	}
	if err := os.Remove(filepath.Join(root, ".marshal")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.acquire(root, time.Now()); err != nil {
		t.Fatalf("acquire after fixing the root: %v", err)
	}
}

func TestHostProgressReachesEveryAttachedSession(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	root := hostRoot(t)
	h := mustAcquire(t, hs, root)
	cfg := config.Default()
	one := session.New(cfg, root, time.Now(), session.Persistence{})
	two := session.New(cfg, root, time.Now(), session.Persistence{})
	h.attach(one, cfg, 1)
	h.attach(two, cfg, 1)

	h.progress("indexing 3 files")
	if one.Activity().Label != "indexing 3 files" || two.Activity().Label != "indexing 3 files" {
		t.Fatalf("activities = %q, %q; want the progress message on both", one.Activity().Label, two.Activity().Label)
	}
	h.detach(two)
	h.progress("embedding 1 file")
	if one.Activity().Label != "embedding 1 file" || two.Activity().Label != "indexing 3 files" {
		t.Fatalf("after detach, activities = %q, %q; want only the attached session updated", one.Activity().Label, two.Activity().Label)
	}
}

func TestHostIndexPassPublishesAReportAndFillsTheFileIndex(t *testing.T) {
	hs := testHosts(t, true, time.Minute)
	root := hostRoot(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := mustAcquire(t, hs, root)
	projectID, err := h.db.GetOrCreateProject(root, "hosts-index-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reports := h.reports.Subscribe(ctx)

	cfg := config.Default()
	h.attach(session.New(cfg, root, time.Now(), session.Persistence{}), cfg, projectID)

	select {
	case ev := <-reports:
		if ev.Type != indexEventCompleted || ev.Payload.Files == 0 {
			t.Fatalf("report = %q with %d files; want a completed pass over the file", ev.Type, ev.Payload.Files)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the startup scan published no report")
	}
	files, err := h.db.GetFileIndex(projectID, 0)
	if err != nil || len(files) == 0 {
		t.Fatalf("file index after the startup scan: %d files, err %v", len(files), err)
	}
}

func TestHostStartsTheWatcherOnlyWhenConfigured(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	h := mustAcquire(t, hs, hostRoot(t))
	cfg := config.Default() // watch unset: the watcher stays off
	if h.supervisor(cfg).watch {
		t.Fatal("the watcher runs without indexing.watch")
	}
	on := true
	cfg.Indexing.Watch = &on
	if !h.supervisor(cfg).watch {
		t.Fatal("the watcher does not run with indexing.watch = true")
	}
}

// countingWorker counts its runs and blocks until cancelled, standing in for
// the index work.
type countingWorker struct{ runs *int32 }

func (countingWorker) Name() string { return "counting-index" }
func (w countingWorker) Run(ctx context.Context) error {
	atomic.AddInt32(w.runs, 1)
	<-ctx.Done()
	return nil
}

func TestHostRestartsIndexWorkOnlyWhenTheWatcherSettingChanges(t *testing.T) {
	hs := testHosts(t, true, time.Minute)
	var runs int32
	hs.workers = append(hs.workers, countingWorker{runs: &runs})
	root := hostRoot(t)
	h := mustAcquire(t, hs, root)
	cfg := config.Default()
	h.attach(session.New(cfg, root, time.Now(), session.Persistence{}), cfg, 1)
	waitForRuns(t, &runs, 1)

	ignored := cfg
	ignored.Indexing.Ignore = append([]string{"tmp/**"}, cfg.Indexing.Ignore...)
	h.noteConfig(ignored)
	h.attach(session.New(cfg, root, time.Now(), session.Persistence{}), cfg, 1)
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("index work ran %d times after changes that keep the watcher setting; want 1", got)
	}

	on := true
	watching := cfg
	watching.Indexing.Watch = &on
	h.noteConfig(watching)
	waitForRuns(t, &runs, 2)
}

func waitForRuns(t *testing.T, runs *int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(runs) != want {
		if time.Now().After(deadline) {
			t.Fatalf("index work ran %d times, want %d", atomic.LoadInt32(runs), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHostWithoutIndexingStartsNoIndexWork(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	var runs int32
	hs.workers = append(hs.workers, countingWorker{runs: &runs})
	root := hostRoot(t)
	h := mustAcquire(t, hs, root)
	cfg := config.Default()
	h.attach(session.New(cfg, root, time.Now(), session.Persistence{}), cfg, 1)
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != 0 {
		t.Fatalf("a host without indexing ran index work %d times", got)
	}
	release, held, err := index.TryLease(root)
	if err != nil || !held {
		t.Fatalf("lease on a host without indexing: held %v, err %v; want it free", held, err)
	}
	release()
}
```

| Test | Asserts |
|---|---|
| `TestHostsShareOneHostPerRoot` | one host per root; `Root()` reports it |
| `TestHostReconcilesOpenGenerationsOncePerHost` | a second acquire leaves a live generation open; a fresh host reconciles it |
| `TestHostLingersAfterItsLastRelease` | an acquire inside the linger reuses the host; after it, a new host opens |
| `TestHostsCloseClosesAttachedHostsAtOnce` | `Close` closes an attached host and refuses later acquires |
| `TestHostOpenWaitsForItsRootsPreviousClose` | an acquire waits while the root's previous host is closing |
| `TestHostOpenFailureIsNotKept` | an `open database` failure is returned and the next acquire tries again |
| `TestHostProgressReachesEveryAttachedSession` | progress reaches every attached session and stops at detach |
| `TestHostIndexPassPublishesAReportAndFillsTheFileIndex` | the startup scan publishes a completed report and fills the file index |
| `TestHostStartsTheWatcherOnlyWhenConfigured` | the watcher follows `indexing.watch` |
| `TestHostRestartsIndexWorkOnlyWhenTheWatcherSettingChanges` | other config changes keep the index work; turning the watcher on restarts it |
| `TestHostWithoutIndexingStartsNoIndexWork` | a private registry runs no index work and leaves the lease free |

`TestHostIndexPassPublishesAReportAndFillsTheFileIndex` and `TestHostStartsTheWatcherOnlyWhenConfigured` replace the two tests in `index_worker_test.go`, which Task 7 deletes.

**Verify:**

```bash
gofmt -l internal/app/                         # no output
go vet ./internal/app/
TMPDIR=/private/tmp/p1-suite go test ./internal/app/ -run TestHost -count=1   # ok
```

## Task 6: Runtimes on the project host

**Goal:** every runtime runs on a project host: the host does the database bootstrap, and one LSP handle per runtime points at the host's pooled servers for the session's active root, rebinding on workspace events, reloads and `NewSession` (spec H2, H5, H6). Without `WithHosts`, a runtime gets a private registry (no index work, no linger), so existing callers behave as today.

**Files:** `internal/app/runtime.go`, `internal/app/app.go`, `internal/app/runtime_host_test.go` (new).

Depends on: Tasks 2 and 5.

**Steps:**

1. In `runtime.go`:
   - **Fields.** Replace `lspCancel` with `Host`, `lspMu`, `lspLease` and `lspClosed`, and rewrite the `LSPManager` comment: the handle now lives for the runtime's whole life.
   - **`runLSPManager` becomes `bindLSP` and `unbindLSP`.** `bindLSP` acquires the new lease, sets the handle, then releases the old lease, so an unchanged key keeps its manager even on a private registry. `unbindLSP` sets `lspClosed`, so a `WorkspaceEvent` bind still in flight cannot rebind after `Close`.
   - **`Close`.** Step 10, after the state shutdown: `unbindLSP`, then detach from and release the host.
   - **`startRuntime`.** Replace the stray-database adoption, `Migrate` and `ReconcileOpenGenerations` with `hosts.acquire` (a private `newHosts(false, 0)` without `WithHosts`), keeping the session's own `db.Open`. A `started` flag releases the host on every failure return. Create the empty handle before `buildAgentRunnerWithLock` and pass it in. After the runtime literal, `host.attach`, `bindLSP`, and a `WorkspaceEvent` goroutine that rebinds with the current `rt.Config`; this replaces the old start-and-restart block.
   - **`newSessionLocked`.** Pass `rt.LSPManager` to the build; drop `oldLSP`, the handle swap and the `lspCancel` block; detach the old state, attach the new one and rebind at its active root.
   - Drop the `dbmigrate` import.

Apply this change to `internal/app/runtime.go`:

```diff
diff --git a/internal/app/runtime.go b/internal/app/runtime.go
index a84ad4dc..932f4d03 100644
--- a/internal/app/runtime.go
+++ b/internal/app/runtime.go
@@ -19,7 +19,6 @@ import (
 	"marshal/internal/agent"
 	"marshal/internal/agent/swarm"
 	"marshal/internal/app/config"
-	"marshal/internal/app/dbmigrate"
 	"marshal/internal/app/logging"
 	"marshal/internal/app/session"
 	"marshal/internal/app/tui"
@@ -187,15 +186,22 @@ type Runtime struct {
 	// applied because the trust question was deferred to the TUI.
 	TrustPromptPending bool
 
-	// LSPManager is the optional LSP server manager handle. When non-nil,
-	// the current manager's worker loop is started in startRuntime (shared
-	// by Run and StartRuntime) and the manager is restarted against the
-	// active root on every WorkspaceEvent.
+	// LSPManager is the runtime's one LSP handle, created in startRuntime
+	// and kept for the runtime's whole life. bindLSP points it at the host's
+	// pooled manager for the session's active root and server set (nil when
+	// none applies), so tools, the diagnostics checker and the TUI's
+	// reference finder never need rewiring.
 	LSPManager *lsp.Handle
-	// lspCancel stops the current LSP manager's Run loop. It is managed by
-	// startRuntime and reloadAgentRuntime so the manager can be hot-swapped
-	// when config reload changes the LSP server set.
-	lspCancel context.CancelFunc
+	// Host is the project host this runtime's session runs on: the
+	// project's database setup, index and pooled language servers, shared
+	// with the process's other sessions on the same root (see Hosts).
+	Host *ProjectHost
+	// lspMu serializes bindLSP; lspLease is the pooled manager LSPManager
+	// points at, released on the next bind or at Close. lspClosed is set by
+	// Close, so a WorkspaceEvent bind still in flight cannot rebind after it.
+	lspMu     sync.Mutex
+	lspLease  *lspLease
+	lspClosed bool
 
 	// reloadMu serializes whole-runtime config reloads. A reload rebuilds the
 	// runner, the MCP manager, the registries, and the cleanup generation;
@@ -1122,17 +1128,37 @@ func (h *WatchResumeHook) Invoke(r watch.Report) {
 	}
 }
 
-// runLSPManager starts m's Run loop under a child of rt.workCtx and returns a
-// cancel function that stops it. It mirrors the inline runManager closure in
-// startRuntime but can be reused during config reload.
-func (rt *Runtime) runLSPManager(m *lsp.Manager) context.CancelFunc {
-	mctx, cancel := context.WithCancel(rt.workCtx)
-	go func() {
-		if err := m.Run(mctx); err != nil {
-			rt.Logger.Warn("worker exited", "worker", m.Name(), "err", err)
-		}
-	}()
-	return cancel
+// bindLSP points the runtime's LSP handle at the host's pooled manager for
+// root and cfg's server set, then releases the manager it pointed at before.
+// The pool hands back the same manager when neither changed, so a reload that
+// keeps the server set keeps its running servers.
+func (rt *Runtime) bindLSP(cfg config.Config, root string) {
+	if rt.LSPManager == nil || rt.Host == nil {
+		return
+	}
+	rt.lspMu.Lock()
+	defer rt.lspMu.Unlock()
+	if rt.lspClosed {
+		return
+	}
+	lease := rt.Host.lsp.acquire(root, lspServersFor(cfg, root))
+	old := rt.lspLease
+	rt.lspLease = lease
+	rt.LSPManager.Set(lease.manager())
+	old.release()
+}
+
+// unbindLSP clears the runtime's LSP handle, releases its pooled manager and
+// refuses later binds.
+func (rt *Runtime) unbindLSP() {
+	rt.lspMu.Lock()
+	defer rt.lspMu.Unlock()
+	rt.lspClosed = true
+	if rt.LSPManager != nil {
+		rt.LSPManager.Set(nil)
+	}
+	rt.lspLease.release()
+	rt.lspLease = nil
 }
 
 // AdditionalDirectories returns the list of extra workspace roots registered
@@ -1347,6 +1373,8 @@ func (rt *Runtime) stopAndJoinConversationWork(ctx context.Context) error {
 //  7. database
 //  8. reverse-order resourceClosers
 //  9. idempotent state shutdown
+//  10. the LSP binding and the project host, which outlive this session's
+//     own database connection
 func (rt *Runtime) Close(ctx context.Context) error {
 	_ = rt.Quiesce(ctx)
 
@@ -1490,6 +1518,15 @@ func (rt *Runtime) Close(ctx context.Context) error {
 			rt.State.Shutdown()
 		}
 
+		// 10. release the pooled language servers and the project host. The
+		// last session on a host leaves it lingering (or closes it, for a
+		// private registry).
+		rt.unbindLSP()
+		if rt.Host != nil {
+			rt.Host.detach(rt.State)
+			rt.Host.release()
+		}
+
 		rt.closeErr = errors.Join(errs...)
 	})
 	return errors.Join(rt.quiesceErr, rt.closeErr)
@@ -1623,26 +1660,31 @@ func startRuntime(ctx context.Context, runOpts options) (*Runtime, error) {
 		slog.Warn("could not write .marshal/.gitignore", "error", err)
 	}
 
-	if err := dbmigrate.AdoptStrayProjectDB(workingDir, slog.Default()); err != nil {
-		slog.Warn("stray .marshal migration", "error", err)
+	// The project host sets the database up once for every session in this
+	// process (stray-database adoption, migration, and reconciling the
+	// generations a crashed process left open); this session then opens its
+	// own connection. Without WithHosts the runtime gets a private registry
+	// whose host does not index and closes with the runtime.
+	hosts := runOpts.hosts
+	if hosts == nil {
+		hosts = newHosts(false, 0)
 	}
+	host, err := hosts.acquire(workingDir, runOpts.now())
+	if err != nil {
+		return nil, err
+	}
+	started := false
+	defer func() {
+		if !started {
+			host.release()
+		}
+	}()
 
 	database, err := db.Open(db.Path(workingDir))
 	if err != nil {
 		return nil, fmt.Errorf("open database: %w", err)
 	}
 
-	if err := database.Migrate(); err != nil {
-		_ = database.Close()
-		return nil, fmt.Errorf("migrate database: %w", err)
-	}
-
-	// Reconcile any generations left open by a crashed process.
-	if _, err := database.ReconcileOpenGenerations(runOpts.now()); err != nil {
-		_ = database.Close()
-		return nil, fmt.Errorf("reconcile open generations: %w", err)
-	}
-
 	var projectID int64
 	var now time.Time
 	sessionID := runOpts.sessionID
@@ -1757,7 +1799,10 @@ func startRuntime(ctx context.Context, runOpts options) (*Runtime, error) {
 	// closure built inside it captures the cell, and the OnFire closure is
 	// built before the Runtime exists.
 	watchResumeCell := &WatchResumeHook{}
-	runner, toolReg, swarmRunner, mcpMgr, snapSvc, commandService, watchMgr, desktopCloser, subagentFactory, lspHandle, pipelineFactory, planAuthorFactory, swarmOverrideFactory, err := buildAgentRunnerWithLock(workCtx, cfg, state, database, projectID, skillIndex, dataDir, runOpts.additionalDirs, jobBroker, watchBroker, watchResumeCell, runOpts.configReloader, homeDir, writeLock, nil, authResolver)
+	// The runtime's one LSP handle. It starts empty; bindLSP points it at
+	// the host's pooled servers once the runtime exists.
+	lspHandle := lsp.NewHandle(nil, nil, logger)
+	runner, toolReg, swarmRunner, mcpMgr, snapSvc, commandService, watchMgr, desktopCloser, subagentFactory, pipelineFactory, planAuthorFactory, swarmOverrideFactory, err := buildAgentRunnerWithLock(workCtx, cfg, state, database, projectID, skillIndex, dataDir, runOpts.additionalDirs, jobBroker, watchBroker, watchResumeCell, runOpts.configReloader, homeDir, writeLock, nil, lspHandle, authResolver)
 	if err == nil && state.Trusted() && len(cfg.Hooks.Entries) > 0 {
 		runner.HookRunner = hooks.NewRunnerFromConfig(cfg.Hooks)
 	}
@@ -1827,6 +1872,7 @@ func startRuntime(ctx context.Context, runOpts options) (*Runtime, error) {
 		CommandRegistry:      cmdReg,
 		TrustPromptPending:   trustPromptPending,
 		LSPManager:           lspHandle,
+		Host:                 host,
 		ConfigReloader:       runOpts.configReloader,
 		AuthResolver:         authResolver,
 		additionalDirs:       runOpts.additionalDirs,
@@ -1847,31 +1893,24 @@ func startRuntime(ctx context.Context, runOpts options) (*Runtime, error) {
 	if err == nil {
 		rt.startSnapshotMaintenance(snapSvc, runner, state, cfg.Snapshots.RetentionDays)
 	}
-	// Start the LSP manager's worker loop here so both Run (TUI) and
-	// StartRuntime (ACP/headless) get it, and restart the manager against
-	// the session's active root on every WorkspaceEvent so hover /
-	// references / definition keep working inside a worktree. Each manager
-	// generation gets its own child context: a restart cancels the old
-	// generation (Run shuts its servers down cleanly) and starts the new
-	// one. workCancel (from Quiesce) ends the subscription and the current
-	// generation. The closure references rt.LSPManager (not the local
-	// lspHandle) so config reload can swap in a new handle and have the
-	// next restart use it.
-	if rt.LSPManager != nil {
-		rt.lspCancel = rt.runLSPManager(rt.LSPManager.Get())
-		go func() {
-			ch := workspaceBroker.Subscribe(rt.workCtx)
-			for {
-				ev, ok := <-ch
-				if !ok {
-					return
-				}
-				rt.lspCancel()
-				newM, _ := rt.LSPManager.Restart(ev.Payload.Workspace.ActiveRoot)
-				rt.lspCancel = rt.runLSPManager(newM)
-			}
-		}()
-	}
+	// Attach the session to its host (index progress, the host's current
+	// config; the first session starts the index) and point the LSP handle
+	// at the host's pooled servers for the session's active root. Re-point it
+	// on every WorkspaceEvent, so hover / references / definition follow the
+	// session into and out of a worktree, sharing servers with any other
+	// session on the same root. workCancel (from Quiesce) ends the
+	// subscription; Close releases the last binding.
+	host.attach(state, cfg, projectID)
+	rt.bindLSP(cfg, state.Workspace().ActiveRoot)
+	go func() {
+		ch := workspaceBroker.Subscribe(rt.workCtx)
+		for ev := range ch {
+			rt.mu.Lock()
+			current := rt.Config
+			rt.mu.Unlock()
+			rt.bindLSP(current, ev.Payload.Workspace.ActiveRoot)
+		}
+	}()
 	// Assign MCP and snapshot only on success AND only when the underlying
 	// concrete pointer is non-nil. A nil *mcp.Manager assigned to an MCPCloser
 	// interface would create a non-nil interface wrapping nil — causing the
@@ -1900,6 +1939,7 @@ func startRuntime(ctx context.Context, runOpts options) (*Runtime, error) {
 		SubagentEvents: subagentBroker,
 		SessionEvents:  eventBroker,
 	}, runOpts.now)
+	started = true
 	return rt, nil
 }
 
@@ -2015,8 +2055,8 @@ func (rt *Runtime) newSessionLocked(name string, replacing bool) (*session.State
 	if lock == nil {
 		lock = &swarm.WriteLock{}
 	}
-	newRunner, newReg, newSwarmRunner, newMCP, newSnap, newJobMgr, newWatchMgr, newDesktopCloser, newSubagentFactory, newLSPHandle, newPipelineFactory, newPlanAuthorFactory, newSwarmOverrideFactory, err := buildAgentRunnerWithLock(
-		rt.workCtx, rt.Config, newState, db, rt.ProjectID, rt.SkillIndex, rt.DataDir, rt.additionalDirs, jb, must[*pubsub.Broker[watch.Event]](rt.WatchBroker), rt.WatchResume, rt.ConfigReloader, rt.HomeDir, lock, nil, rt.AuthResolver,
+	newRunner, newReg, newSwarmRunner, newMCP, newSnap, newJobMgr, newWatchMgr, newDesktopCloser, newSubagentFactory, newPipelineFactory, newPlanAuthorFactory, newSwarmOverrideFactory, err := buildAgentRunnerWithLock(
+		rt.workCtx, rt.Config, newState, db, rt.ProjectID, rt.SkillIndex, rt.DataDir, rt.additionalDirs, jb, must[*pubsub.Broker[watch.Event]](rt.WatchBroker), rt.WatchResume, rt.ConfigReloader, rt.HomeDir, lock, nil, rt.LSPManager, rt.AuthResolver,
 	)
 	if err != nil {
 		// Roll back the empty session row so /sessions stays clean.
@@ -2071,7 +2111,6 @@ func (rt *Runtime) newSessionLocked(name string, replacing bool) (*session.State
 	}
 	oldWatchMgr := rt.WatchManager
 	oldDesktopCloser := rt.DesktopCloser
-	oldLSP := rt.LSPManager
 
 	rt.State = newState
 	rt.SessionID = sessionID
@@ -2106,9 +2145,6 @@ func (rt *Runtime) newSessionLocked(name string, replacing bool) (*session.State
 	} else {
 		rt.Snapshot = nil
 	}
-	if newLSPHandle != nil {
-		rt.LSPManager = newLSPHandle
-	}
 	rt.mu.Unlock()
 
 	if oldMCP != nil {
@@ -2130,14 +2166,13 @@ func (rt *Runtime) newSessionLocked(name string, replacing bool) (*session.State
 	if oldRunner != nil && oldRunner.Rollover != nil {
 		_ = oldRunner.Rollover.Close(context.Background())
 	}
-	if oldLSP != nil && oldLSP != newLSPHandle {
-		// Restart the LSP worker against the new handle, matching the
-		// reloadAgentRuntime cleanup pattern.
-		rt.lspCancel()
-		if newLSPHandle != nil {
-			rt.lspCancel = rt.runLSPManager(rt.LSPManager.Get())
-		}
+	// The replacement session starts at the project root: move the host's
+	// progress to it and point the LSP handle at the root's pooled servers.
+	if rt.Host != nil {
+		rt.Host.detach(oldState)
+		rt.Host.attach(newState, rt.Config, rt.ProjectID)
 	}
+	rt.bindLSP(rt.Config, newState.Workspace().ActiveRoot)
 	// Close the replaced conversation's projection. Its sources are stopped
 	// above, so this reconciles and finalizes the last durable facts while the
 	// old State is still live, then cancels, joins and closes its broker. Doing
```

2. In `app.go`:
   - **`buildAgentRunner`** (test-only wrapper) keeps its signature: it creates `lsp.NewHandle(nil, nil, state.Logger())`, passes it and returns it.
   - **`buildAgentRunnerWithLock`** takes `lspHandle *lsp.Handle` after `service`, documents it, stops creating a manager, and drops the handle from its results, so every early return loses one `nil`.
   - **`reloadAgentRuntime`** passes `rt.LSPManager`, drops `oldLSP`, the handle swap and the `lspCancel` block, and after the cleanup calls `rt.bindLSP(cfg, …)` and `rt.Host.noteConfig(cfg)`.

Apply this change to `internal/app/app.go`:

```diff
diff --git a/internal/app/app.go b/internal/app/app.go
index f2dfdaa0..6af9c216 100644
--- a/internal/app/app.go
+++ b/internal/app/app.go
@@ -826,8 +826,11 @@ func (s *conversationCommandService) commitLaunchPolicy(policy native.LaunchPoli
 func buildAgentRunner(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index, dataDir string, additionalDirs []string, jobBroker *pubsub.Broker[native.JobEvent], configReloader func(config.Config) error, homeDir string, auth ...provider.AuthResolver) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, *mcp.Manager, *snapshot.Rooted, *native.JobManager, func(), agent.SubagentRunnerFactory, *lsp.Handle, func(planPath string, overrides map[routing.AgentRole]string) tui.AgentRunner, sddauthor.Factory, tui.SwarmOverrideFactory, error) {
 	// The watch manager is owned by the runtime (via buildAgentRunnerWithLock);
 	// this wrapper discards it so the ~25 test call sites that use
-	// buildAgentRunner directly do not need to change.
-	runner, reg, swarmRunner, mcpMgr, snapSvc, commandService, _, desktopCloser, subagentFactory, lspHandle, pipelineFactory, planAuthorFactory, swarmOverrideFactory, err := buildAgentRunnerWithLock(ctx, cfg, state, database, projectID, skillIndex, dataDir, additionalDirs, jobBroker, nil, nil, configReloader, homeDir, &swarm.WriteLock{}, nil, auth...)
+	// buildAgentRunner directly do not need to change. The LSP handle is the
+	// runtime's too: the wrapper wires the tools to an empty one and returns
+	// it.
+	lspHandle := lsp.NewHandle(nil, nil, state.Logger())
+	runner, reg, swarmRunner, mcpMgr, snapSvc, commandService, _, desktopCloser, subagentFactory, pipelineFactory, planAuthorFactory, swarmOverrideFactory, err := buildAgentRunnerWithLock(ctx, cfg, state, database, projectID, skillIndex, dataDir, additionalDirs, jobBroker, nil, nil, configReloader, homeDir, &swarm.WriteLock{}, nil, lspHandle, auth...)
 	var jobMgr *native.JobManager
 	if commandService != nil {
 		jobMgr = commandService.manager
@@ -858,12 +861,17 @@ func buildAgentRunner(ctx context.Context, cfg config.Config, state *session.Sta
 // service's defaults for future launches. That commit is the last thing the
 // function does, so a candidate that fails validation leaves the previous
 // generation — and the service — exactly as they were.
-func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index, dataDir string, additionalDirs []string, jobBroker *pubsub.Broker[native.JobEvent], watchBroker *pubsub.Broker[watch.Event], watchResumeCell *WatchResumeHook, configReloader func(config.Config) error, homeDir string, writeLock *swarm.WriteLock, service *conversationCommandService, auth ...provider.AuthResolver) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, *mcp.Manager, *snapshot.Rooted, *conversationCommandService, *watch.Manager, func(), agent.SubagentRunnerFactory, *lsp.Handle, func(planPath string, overrides map[routing.AgentRole]string) tui.AgentRunner, sddauthor.Factory, tui.SwarmOverrideFactory, error) {
+//
+// lspHandle is the runtime's LSP handle, which the tools, the symbol query
+// and the diagnostics checker are wired to. The build never creates or
+// starts a language server: the runtime points the handle at the project
+// host's pooled servers (Runtime.bindLSP). A nil handle wires no LSP.
+func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index, dataDir string, additionalDirs []string, jobBroker *pubsub.Broker[native.JobEvent], watchBroker *pubsub.Broker[watch.Event], watchResumeCell *WatchResumeHook, configReloader func(config.Config) error, homeDir string, writeLock *swarm.WriteLock, service *conversationCommandService, lspHandle *lsp.Handle, auth ...provider.AuthResolver) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, *mcp.Manager, *snapshot.Rooted, *conversationCommandService, *watch.Manager, func(), agent.SubagentRunnerFactory, func(planPath string, overrides map[routing.AgentRole]string) tui.AgentRunner, sddauthor.Factory, tui.SwarmOverrideFactory, error) {
 	authResolver := effectiveAuthResolver(firstAuthResolver(auth))
 	resolver := newRoutedProviderResolver(cfg, dataDir, authResolver).withSessionID(state.SessionID())
 	route, resolvedProvider, err := resolver.Resolve("edit")
 	if err != nil {
-		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
+		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
 	}
 
 	// Resolve rollover enablement against the route once, up front: when
@@ -892,7 +900,7 @@ func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *ses
 	if sbErr != nil {
 		// Unknown backend string: surface as a startup error rather than
 		// silently downgrading — the user should fix their config.
-		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("build sandbox: %w", sbErr)
+		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("build sandbox: %w", sbErr)
 	}
 	caps := commandRunner.Capabilities()
 	state.SetSandboxInfo(session.SandboxInfo{
@@ -1057,15 +1065,8 @@ func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *ses
 	if len(additionalDirs) > 0 {
 		nativeOpts.AdditionalRoots = additionalDirs
 	}
-	// Build LSP manager and wire adapters BEFORE RegisterAll so the
-	// toolSet and diagnostics checker receive non-nil LSP fields.
-	var lspHandle *lsp.Handle
-	if lspEnabled(cfg.LSP) {
-		servers := lsp.DetectServers(toServerSpecs(cfg.LSP.Servers), disabledLangs(cfg.LSP.Servers))
-		if len(servers) > 0 {
-			lspHandle = lsp.NewHandle(lsp.NewManager(state.Workspace().ActiveRoot, lsp.ServersForRoot(servers, state.Workspace().ActiveRoot), state.Logger()), servers, state.Logger())
-		}
-	}
+	// Wire the LSP adapters BEFORE RegisterAll so the toolSet and
+	// diagnostics checker receive non-nil LSP fields.
 	// symbols answers through the language servers and falls back to the
 	// symbol index, so it is wired unconditionally; lspHandle may be nil.
 	nativeOpts.Symbols = lsp.NewSymbolQuery(lspHandle, database, projectID)
@@ -1077,7 +1078,7 @@ func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *ses
 	}
 	if err := native.RegisterAll(reg, nativeOpts); err != nil {
 		buildErr = err
-		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
+		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
 	}
 	skills.RegisterTool(reg, skillIndex, state, skills.SkillsToolOptions{
 		HomeDir:    homeDir,
@@ -1109,7 +1110,7 @@ func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *ses
 		cleanup = append(cleanup, func() { _ = mcpMgr.Close() })
 		if err := mcpMgr.RegisterTools(reg); err != nil {
 			buildErr = err
-			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
+			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
 		}
 	}
 	router := routing.NewStaticRouter(cfg.RoutingConfig())
@@ -1129,7 +1130,7 @@ func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *ses
 		agent.WithSubagentParentProvider(resolvedProvider.Name()),
 	)); err != nil {
 		buildErr = err
-		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.run: %w", err)
+		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.run: %w", err)
 	}
 	// agent.await and agent.kill both cover the same two classes of
 	// background work: subagents and shell jobs. Both take the job adapter so
@@ -1138,19 +1139,19 @@ func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *ses
 		agent.WithAwaitJobs(jobAwaitAdapter{m: jobManager}),
 	)); err != nil {
 		buildErr = err
-		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.await: %w", err)
+		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.await: %w", err)
 	}
 	if err := reg.Register(agent.NewSubagentKillTool(state,
 		agent.WithAwaitJobs(jobAwaitAdapter{m: jobManager}),
 	)); err != nil {
 		buildErr = err
-		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.kill: %w", err)
+		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.kill: %w", err)
 	}
 	// agent.steer sends an instruction into a running subagent. Main agent
 	// only: SubtaskScopeView excludes it, so a child cannot steer a sibling.
 	if err := reg.Register(agent.NewSubagentSteerTool(state)); err != nil {
 		buildErr = err
-		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.steer: %w", err)
+		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.steer: %w", err)
 	}
 	runner := agent.NewRunner(resolvedProvider, reg, pol, state, route.Preset.Model)
 	runner.WriteGate = writeLock
@@ -1313,7 +1314,7 @@ func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *ses
 	}
 	if rolloverCtrl, rerr := NewRolloverController(state.SessionID(), cfg.Session.Rollover, database, route.Preset.LocalOnly, modelCtxWindow, digestProvider, usageCounter); rerr != nil {
 		buildErr = rerr
-		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("new rollover controller: %w", rerr)
+		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("new rollover controller: %w", rerr)
 	} else if rolloverCtrl != nil {
 		runner.Rollover = &agent.Rollover{
 			Controller: rolloverCtrl,
@@ -1322,7 +1323,7 @@ func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *ses
 		// Start generation 0.
 		if err := rolloverCtrl.Start(ctx); err != nil {
 			buildErr = err
-			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("rollover start: %w", err)
+			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("rollover start: %w", err)
 		}
 		// Record generation 0 in session state.
 		genID, genSeq, genSeed := rolloverCtrl.Current()
@@ -1430,7 +1431,7 @@ func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *ses
 		closer, err := desktop.RegisterAll(reg, desktopOpts)
 		if err != nil {
 			buildErr = err
-			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register desktop tools: %w", err)
+			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register desktop tools: %w", err)
 		}
 		desktopCloser = closer
 	}
@@ -1447,7 +1448,7 @@ func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *ses
 	// launches only. A failure returned earlier leaves the service, its
 	// callbacks and the previous generation exactly as they were.
 	commandService.commitLaunchPolicy(commandLaunchPolicy(cfg, state, commandService.nextGeneration(), commandRunner, native.ResourcesOf(commandRunner)), lspSync)
-	return runner, reg, swarmRunner, mcpMgr, snapSvc, commandService, watchManager, desktopCloser, subagentFactory, lspHandle, pipelineFactory, planAuthorFactory, swarmOverrideFactory, nil
+	return runner, reg, swarmRunner, mcpMgr, snapSvc, commandService, watchManager, desktopCloser, subagentFactory, pipelineFactory, planAuthorFactory, swarmOverrideFactory, nil
 }
 
 // beginSessionStop registers the conversation-level stop group for one parent
@@ -3092,7 +3093,7 @@ func reloadAgentRuntime(ctx context.Context, cfg config.Config, rt *Runtime) err
 	// The build uses effectiveCfg, not cfg: a session mode override must
 	// survive the reload, so the rebuilt policy engine has to agree with what
 	// the session reports.
-	newRunner, newReg, newSwarmRunner, newMCP, newSnap, newJobMgr, newWatchMgr, newDesktopCloser, newSubagentFactory, newLSPHandle, newPipelineFactory, newPlanAuthorFactory, newSwarmOverrideFactory, err := buildAgentRunnerWithLock(rt.workCtx, effectiveCfg, rt.State, db, rt.ProjectID, rt.SkillIndex, rt.DataDir, rt.additionalDirs, jb, must[*pubsub.Broker[watch.Event]](rt.WatchBroker), rt.WatchResume, rt.ConfigReloader, rt.HomeDir, lock, service, rt.AuthResolver)
+	newRunner, newReg, newSwarmRunner, newMCP, newSnap, newJobMgr, newWatchMgr, newDesktopCloser, newSubagentFactory, newPipelineFactory, newPlanAuthorFactory, newSwarmOverrideFactory, err := buildAgentRunnerWithLock(rt.workCtx, effectiveCfg, rt.State, db, rt.ProjectID, rt.SkillIndex, rt.DataDir, rt.additionalDirs, jb, must[*pubsub.Broker[watch.Event]](rt.WatchBroker), rt.WatchResume, rt.ConfigReloader, rt.HomeDir, lock, service, rt.LSPManager, rt.AuthResolver)
 	if err != nil {
 		// A failed dry run leaves the configured baseline, the override, the
 		// system flag and the existing runner exactly as they were.
@@ -3118,7 +3119,6 @@ func reloadAgentRuntime(ctx context.Context, cfg config.Config, rt *Runtime) err
 	oldMCP := rt.MCPManager
 	oldWatchMgr := rt.WatchManager
 	oldDesktopCloser := rt.DesktopCloser
-	oldLSP := rt.LSPManager
 
 	// Copy in-place fields. When the startup provider build failed there is
 	// no live runner to mutate — adopt the rebuilt one wholesale (mirroring
@@ -3177,11 +3177,6 @@ func reloadAgentRuntime(ctx context.Context, cfg config.Config, rt *Runtime) err
 	} else {
 		rt.Snapshot = nil
 	}
-	if newLSPHandle != nil {
-		rt.LSPManager = newLSPHandle
-	} else {
-		rt.LSPManager = nil
-	}
 	rt.JobManager = newJobMgr.manager
 	rt.CommandService = newJobMgr
 	rt.WatchManager = newWatchMgr
@@ -3224,14 +3219,11 @@ func reloadAgentRuntime(ctx context.Context, cfg config.Config, rt *Runtime) err
 	if oldDesktopCloser != nil {
 		oldDesktopCloser()
 	}
-	if oldLSP != nil && oldLSP != newLSPHandle {
-		// Stop the old LSP manager generation. Start a new one if the reload
-		// produced a handle, otherwise leave the slot empty.
-		rt.lspCancel()
-		if newLSPHandle != nil {
-			rt.lspCancel = rt.runLSPManager(rt.LSPManager.Get())
-		}
-	}
+	// The server set may have changed: re-point the LSP handle (the pool
+	// keeps the running manager when it did not), and give the host the new
+	// config for its index passes.
+	rt.bindLSP(cfg, rt.State.Workspace().ActiveRoot)
+	rt.Host.noteConfig(cfg)
 
 	// Update the active route so the status bar reflects the new model
 	// immediately, without waiting for the next turn to call resolveRoute.
```

3. Create `internal/app/runtime_host_test.go` with exactly this content:

```go
package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/lsp"
	"marshal/internal/trust"
)

// hostedProjectConfig is a minimal trusted project config with a mock
// provider and a language server for shell scripts. The server is a stub
// that exits at once: its manager is still created and pooled, which is all
// these tests compare.
const hostedProjectConfig = `[project]
name = "hosted"

[profile]
default = "mock_profile"

[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"

[models.presets."mock/mock-model"]
local_only = true

[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"

[lsp]
enabled = true

[lsp.servers.shell]
command = "STUB"
`

// hostedProject writes a git repository whose root holds the project config
// and an LSP stub script (which also gives the root its shell language).
func hostedProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(root, "lsp-stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := []byte(strings.ReplaceAll(hostedProjectConfig, "STUB", stub))
	if err := os.WriteFile(filepath.Join(root, ".marshal", "config.toml"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// startOn starts a runtime in dir on hs (nil for a private registry) and
// closes it when the test ends.
func startOn(t *testing.T, hs *Hosts, home, dir string) *Runtime {
	t.Helper()
	opts := []Option{
		WithWorkingDir(dir),
		WithHomeDir(home),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
	}
	if hs != nil {
		opts = append(opts, WithHosts(hs))
	}
	rt, err := StartRuntime(context.Background(), opts...)
	if err != nil {
		t.Fatalf("StartRuntime(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	return rt
}

func waitForManager(t *testing.T, rt *Runtime, want func(*lsp.Manager) bool, what string) *lsp.Manager {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if m := rt.LSPManager.Get(); want(m) {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRuntimesOnOneRootShareTheProjectHost(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	home := t.TempDir()
	root, other := hostedProject(t), hostedProject(t)
	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	a := startOn(t, hs, home, root)
	b := startOn(t, hs, home, sub)
	if a.Host == nil || a.Host != b.Host {
		t.Fatal("runtimes on one root (one from a subdirectory) got different hosts")
	}
	if c := startOn(t, hs, home, other); c.Host == a.Host {
		t.Fatal("a runtime on another root shared the host")
	}
	if p, q := startOn(t, nil, home, root), startOn(t, nil, home, root); p.Host == q.Host || p.Host == a.Host {
		t.Fatal("runtimes without WithHosts shared a host")
	}
}

func TestSecondRuntimeOnAHostKeepsTheFirstSessionsGenerationOpen(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	home := t.TempDir()
	root := hostedProject(t)

	first := startOn(t, hs, home, root)
	database := must[*db.DB](first.DB)
	gen := db.Generation{ID: first.SessionID + "-live", SessionID: first.SessionID, Seq: 99, StartedAt: time.Now()}
	if err := database.BeginGeneration(gen); err != nil {
		t.Fatal(err)
	}
	isOpen := func() bool {
		gens, err := database.GenerationsForSession(first.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range gens {
			if g.ID == gen.ID {
				return g.EndedAt == nil
			}
		}
		t.Fatal("live generation missing")
		return false
	}

	startOn(t, hs, home, root)
	if !isOpen() {
		t.Fatal("starting a second session on the host ended the first session's live generation")
	}
	// A runtime with a private registry still reconciles on start, as every
	// runtime did before project hosts.
	startOn(t, nil, home, root)
	if isOpen() {
		t.Fatal("a private-registry start did not reconcile open generations")
	}
}

func TestRuntimesShareLanguageServersByActiveRoot(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	home := t.TempDir()
	root := hostedProject(t)
	a := startOn(t, hs, home, root)
	b := startOn(t, hs, home, root)

	shared := a.LSPManager.Get()
	if shared == nil || b.LSPManager.Get() != shared {
		t.Fatalf("runtimes on one root got managers %p and %p; want one shared manager", shared, b.LSPManager.Get())
	}

	worktree := filepath.Join(root, ".marshal", "worktrees", "feature")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "run.sh"), []byte("echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.State.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: worktree, Branch: "feature"})
	inWorktree := waitForManager(t, a, func(m *lsp.Manager) bool { return m != nil && m != shared }, "the worktree's manager")
	if inWorktree.Root() != worktree {
		t.Fatalf("worktree manager root = %q, want %q", inWorktree.Root(), worktree)
	}
	if b.LSPManager.Get() != shared {
		t.Fatal("one session entering a worktree moved the other session's servers")
	}

	a.State.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: root})
	waitForManager(t, a, func(m *lsp.Manager) bool { return m == shared }, "the shared root manager again")
}

func TestReloadKeepsLanguageServersUnlessTheServerSetChanges(t *testing.T) {
	// A private registry has no linger, so it also checks that a rebind takes
	// the new manager before it releases the old one.
	for _, shared := range []bool{true, false} {
		t.Run(fmt.Sprintf("shared=%t", shared), func(t *testing.T) {
			var hs *Hosts
			if shared {
				hs = testHosts(t, false, time.Minute)
			}
			rt := startOn(t, hs, t.TempDir(), hostedProject(t))
			handle := rt.LSPManager
			before := handle.Get()
			if before == nil {
				t.Fatal("no manager bound at start")
			}

			if err := reloadAgentRuntime(context.Background(), rt.Config, rt); err != nil {
				t.Fatalf("reload: %v", err)
			}
			if rt.LSPManager != handle {
				t.Fatal("a reload replaced the runtime's LSP handle")
			}
			if rt.LSPManager.Get() != before {
				t.Fatal("a reload with an unchanged server set restarted the language servers")
			}

			changed := rt.Config
			spec := changed.LSP.Servers["shell"]
			spec.Args = []string{"--stdio"}
			changed.LSP.Servers = map[string]config.LSPServerConfig{"shell": spec}
			if err := reloadAgentRuntime(context.Background(), changed, rt); err != nil {
				t.Fatalf("reload with a changed server: %v", err)
			}
			if after := rt.LSPManager.Get(); after == nil || after == before {
				t.Fatal("a reload that changed the server set kept the old manager")
			}
		})
	}
}

func TestNewSessionKeepsTheLSPHandleAndHost(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	rt := startOn(t, hs, t.TempDir(), hostedProject(t))
	handle, host, before := rt.LSPManager, rt.Host, rt.LSPManager.Get()

	if _, _, _, _, _, _, _, err := rt.NewSession("next"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if rt.LSPManager != handle || rt.Host != host {
		t.Fatal("NewSession replaced the runtime's LSP handle or host")
	}
	if rt.LSPManager.Get() != before {
		t.Fatal("NewSession restarted the root's language servers")
	}
}

func TestCloseReleasesThePrivateHost(t *testing.T) {
	root := hostedProject(t)
	rt, err := StartRuntime(context.Background(), WithWorkingDir(root), WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatal(err)
	}
	host := rt.Host
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !hostClosed(host) {
		t.Fatal("closing a runtime left its private host open")
	}
	if rt.LSPManager.Get() != nil {
		t.Fatal("Close left the LSP handle bound")
	}
}

// A WorkspaceEvent bind still in flight when Close runs must not rebind: on a
// lingering host it would hold a pooled manager nobody releases.
func TestBindAfterCloseTakesNoLease(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	root := hostedProject(t)
	rt := startOn(t, hs, t.TempDir(), root)
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rt.bindLSP(rt.Config, root)
	if rt.LSPManager.Get() != nil || rt.lspLease != nil {
		t.Fatal("a bind after Close took a language-server lease")
	}
}
```

| Test | Asserts |
|---|---|
| `TestRuntimesOnOneRootShareTheProjectHost` | runtimes on one git root, one from a subdirectory, share a host; another root and private registries do not |
| `TestSecondRuntimeOnAHostKeepsTheFirstSessionsGenerationOpen` | a second start on a shared host leaves the first session's live generation open; a private-registry start still reconciles |
| `TestRuntimesShareLanguageServersByActiveRoot` | two sessions share the root's manager; one entering a worktree gets the worktree's and returns to the shared one |
| `TestReloadKeepsLanguageServersUnlessTheServerSetChanges` | on shared and private registries, a reload keeps the handle and, with unchanged servers, the manager; changed servers get a new manager |
| `TestNewSessionKeepsTheLSPHandleAndHost` | `NewSession` keeps the handle, the host and the root's manager |
| `TestCloseReleasesThePrivateHost` | `Close` closes a private host and clears the handle |
| `TestBindAfterCloseTakesNoLease` | a bind after `Close` on a lingering host takes no lease |

**Verify:**

```bash
gofmt -l internal/app/                         # no output
go vet ./internal/app/ ./internal/acp/
TMPDIR=/private/tmp/p1-suite go test ./internal/app/ ./internal/acp/ -count=1   # ok
```

## Task 7: The TUI on `Hosts`; index work leaves the runtime

**Goal:** `Run` creates one registry for the TUI process, so a resume or a trust reload reuses the host inside its linger; the index workers run only on hosts; `buildIndexWorkers`, `namedWorker` and `Runtime.IndexBroker` are removed (spec H1, H3). The done-when test proves a headless runtime re-embeds an edit.

**Files:** `internal/app/app.go`, `internal/app/runtime.go`, `internal/app/index_worker_test.go` (deleted), `internal/app/host_refresh_test.go` (new).

Depends on: Task 6.

**Steps:**

1. In `app.go`:
   - In `Run`, before the deferred `rt.Close`, create the registry when the caller gave none, hand it `runOpts.workers` (the `WithWorker` seam), and defer its `Close`. Deferred after the runtime's close, it runs last.
   - In the worker lifecycle block, keep the embedding startup hint (still only when no workers were injected) and start only `buildOAuthWorkers`.
   - Delete `buildIndexWorkers` and `namedWorker`, update the `indexEventCompleted` comment, and drop the `index` import.

Apply this change to `internal/app/app.go`:

```diff
diff --git a/internal/app/app.go b/internal/app/app.go
index 6af9c216..7c4c66a6 100644
--- a/internal/app/app.go
+++ b/internal/app/app.go
@@ -29,7 +29,6 @@ import (
 	"marshal/internal/db"
 	"marshal/internal/filetrack"
 	"marshal/internal/hooks"
-	"marshal/internal/index"
 	"marshal/internal/llm/embedding"
 	"marshal/internal/llm/pricing"
 	"marshal/internal/llm/provider"
@@ -251,21 +250,10 @@ func startWorker(ctx context.Context, wg *sync.WaitGroup, w worker.Worker, log *
 	}()
 }
 
-// indexEventCompleted is published on Runtime.IndexBroker after every
-// successful index pass (startup scan or watcher rerun).
+// indexEventCompleted is published on a project host's report broker after
+// every successful index pass (startup scan or watcher rerun).
 const indexEventCompleted = "index.completed"
 
-// namedWorker adapts a function to the worker.Worker contract for production
-// background workers. (app_test.go defines its own workerFunc for the test
-// seam; the names must not collide.)
-type namedWorker struct {
-	name string
-	run  func(ctx context.Context) error
-}
-
-func (w namedWorker) Name() string                  { return w.name }
-func (w namedWorker) Run(ctx context.Context) error { return w.run(ctx) }
-
 // WithHomeDir overrides the home directory used to resolve the user config
 // and data directories. When empty, the process's real home directory is
 // used. Intended for tests so config loads (and the project→user hoist) do
@@ -2507,58 +2495,6 @@ func fallbackResponseFormat(caps schema.ProviderCapabilities) *schema.ResponseFo
 	return nil
 }
 
-// buildIndexWorkers assembles the indexing background workers: the
-// fsnotify watcher (only when config.WatchEnabled allows) and the one-shot
-// startup scan (AI-02 — always; hashes+symbols even without embeddings so
-// repo.map/symbols work on a fresh project). Every successful pass
-// publishes indexEventCompleted on indexBroker, which triggers the
-// context-pack re-seed in startRuntime.
-func buildIndexWorkers(cfg config.Config, state *session.State, database *db.DB, projectID int64, workingDir string, lspAdapter index.LSPSymbols, indexBroker *pubsub.Broker[index.Report], logger *slog.Logger) []worker.Worker {
-	runPass := func(c context.Context) error {
-		cfg := state.Config
-
-		embedder := resolveEmbedderFromConfig(cfg)
-		if embedder != nil {
-			probeCtx, cancel := context.WithTimeout(c, 30*time.Second)
-			defer cancel()
-			if _, err := embedding.Probe(probeCtx, embedder); err != nil {
-				return fmt.Errorf("embedding probe failed: %w", err)
-			}
-		}
-
-		passCtx, cancel := context.WithTimeout(c, 10*time.Minute)
-		defer cancel()
-		rep, err := index.Run(passCtx, index.Deps{
-			DB:       database,
-			Root:     workingDir,
-			Ignore:   cfg.Indexing.Ignore,
-			MaxBytes: cfg.Indexing.MaxIndexableFileBytes,
-			Embedder: embedder,
-			LSP:      lspAdapter,
-			OnProgress: func(msg string) {
-				state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: msg})
-			},
-		}, projectID)
-		if err == nil {
-			indexBroker.Publish(indexEventCompleted, rep)
-		}
-		return err
-	}
-
-	var workers []worker.Worker
-	embeddingConfigured := false
-	embedRouter := routing.NewStaticRouter(cfg.RoutingConfig())
-	if _, err := embedRouter.ResolveEmbedding(); err == nil {
-		embeddingConfigured = true
-	}
-	if config.WatchEnabled(cfg.Indexing.Watch, embeddingConfigured) {
-		debounce := time.Duration(cfg.Indexing.WatchDebounceMs) * time.Millisecond
-		workers = append(workers, index.NewWatcher(workingDir, debounce, runPass, logger))
-	}
-	workers = append(workers, namedWorker{name: "startup-index-scan", run: runPass})
-	return workers
-}
-
 // buildOAuthWorkers returns one token-refresh worker per provider entry that
 // authenticates with OAuth. Both the worker and the provider backend get their
 // engine from the same auth resolver, so a proactive refresh and an inline
@@ -2689,6 +2625,23 @@ func Run(ctx context.Context, stdout io.Writer, opts ...Option) error {
 		_ = trust.NewStore(trustStoreDir).RefreshConfigHash(abs, hash)
 	}
 
+	// One project-host registry for the TUI process. A resume or a trust
+	// reload closes the runtime and starts the next one on the same host
+	// inside its linger, so the database, index and language servers carry
+	// over. Injected workers (WithWorker) replace the host's index work. The
+	// registry closes after the last runtime (deferred below, so it runs
+	// first).
+	if runOpts.hosts == nil {
+		hosts := NewHosts()
+		hosts.workers = runOpts.workers
+		runOpts.hosts = hosts
+		defer func() {
+			closeCtx, cancel := context.WithTimeout(context.Background(), hostCloseTimeout)
+			defer cancel()
+			_ = hosts.Close(closeCtx)
+		}()
+	}
+
 	defer func() {
 		if rt != nil {
 			_ = rt.Close(context.Background())
@@ -2882,10 +2835,10 @@ func Run(ctx context.Context, stdout io.Writer, opts ...Option) error {
 		logger.Info("marshal started", "project", cfg.Project.Name, "working_dir", workingDir)
 
 		// ── Worker lifecycle ──────────────────────────────────────────────
-		// Start injected workers (test seam) or construct the index watcher
-		// when embeddings are configured and the watcher is enabled.
-		workers := runOpts.workers
-		if len(workers) == 0 {
+		// The index work runs on the project host, which startRuntime
+		// attached this session to; the language servers are the host's too.
+		// Only the OAuth refresh workers stay per runtime.
+		if len(runOpts.workers) == 0 {
 			// Surface a hint when embedding indexing is enabled but the
 			// embedding route cannot resolve — semantic search reports
 			// "unavailable" until it does. embeddingStartupWarning
@@ -2896,21 +2849,9 @@ func Run(ctx context.Context, stdout io.Writer, opts ...Option) error {
 				logger.Warn("embedding route not resolvable while use_embeddings is enabled")
 				state.AddMessage(session.RoleSystem, msg, session.ContentTypePlain)
 			}
-
-			// Build the LSP symbol adapter for the index pass.
-			var lspAdapter index.LSPSymbols
-			if rt.LSPManager != nil {
-				lspAdapter = lsp.NewSymbolAdapter(rt.LSPManager)
-			}
-
-			workers = buildIndexWorkers(cfg, state, database, projectID, workingDir, lspAdapter,
-				must[*pubsub.Broker[index.Report]](rt.IndexBroker), logger)
 		}
-		workers = append(workers, buildOAuthWorkers(cfg, logger, rt.AuthResolver)...)
-		// LSPManager is started inside startRuntime (shared by Run and
-		// StartRuntime) — see runtime.go. Do not start it again here.
 		var workerWG sync.WaitGroup
-		for _, w := range workers {
+		for _, w := range buildOAuthWorkers(cfg, logger, rt.AuthResolver) {
 			startWorker(rt.workCtx, &workerWG, w, logger)
 		}
 		// ── End worker lifecycle ──────────────────────────────────────────
```

2. In `runtime.go`, remove `IndexBroker`: the field, its broker in `startRuntime`, the literal entry, Close step 5.7, and the `index` import.

Apply this change to `internal/app/runtime.go`:

```diff
diff --git a/internal/app/runtime.go b/internal/app/runtime.go
index 932f4d03..27ad6a0a 100644
--- a/internal/app/runtime.go
+++ b/internal/app/runtime.go
@@ -25,7 +25,6 @@ import (
 	"marshal/internal/commands"
 	"marshal/internal/db"
 	"marshal/internal/hooks"
-	"marshal/internal/index"
 	"marshal/internal/llm/provider"
 	"marshal/internal/llm/routing"
 	"marshal/internal/lsp"
@@ -137,7 +136,6 @@ type Runtime struct {
 	EventBroker       BrokerCloser
 	WorkspaceBroker   BrokerCloser
 	SubagentBroker    BrokerCloser
-	IndexBroker       BrokerCloser
 	MCPManager        MCPCloser
 	Snapshot          SnapshotCloser
 	Logger            *slog.Logger
@@ -1425,11 +1423,6 @@ func (rt *Runtime) Close(ctx context.Context) error {
 			rt.SubagentBroker.Close()
 		}
 
-		// 5.7. index broker.
-		if rt.IndexBroker != nil {
-			rt.IndexBroker.Close()
-		}
-
 		// 5b. rollover close — end the live generation with session_end.
 		if rt.Runner != nil && rt.Runner.Rollover != nil {
 			if err := rt.Runner.Rollover.Close(ctx); err != nil {
@@ -1785,7 +1778,6 @@ func startRuntime(ctx context.Context, runOpts options) (*Runtime, error) {
 	eventBroker := pubsub.NewBroker[session.Event]()
 	workspaceBroker := pubsub.NewBroker[session.WorkspaceEvent]()
 	subagentBroker := pubsub.NewBroker[session.SubagentEvent]()
-	indexBroker := pubsub.NewBroker[index.Report]()
 	state.SetSteeringBroker(steeringBroker)
 	state.SetEventBroker(eventBroker)
 	state.SetWorkspaceBroker(workspaceBroker)
@@ -1855,7 +1847,6 @@ func startRuntime(ctx context.Context, runOpts options) (*Runtime, error) {
 		EventBroker:          eventBroker,
 		WorkspaceBroker:      workspaceBroker,
 		SubagentBroker:       subagentBroker,
-		IndexBroker:          indexBroker,
 		CommandService:       commandService,
 		JobManager:           commandServiceManager(commandService),
 		WatchManager:         watchMgr,
```

3. Delete `internal/app/index_worker_test.go` (`git rm`). Its two tests are replaced by Task 5's `TestHostIndexPassPublishesAReportAndFillsTheFileIndex` and `TestHostStartsTheWatcherOnlyWhenConfigured`.

4. Create `internal/app/host_refresh_test.go` with exactly this content:

```go
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/db"
	"marshal/internal/trust"
)

// embeddingServer is an OpenAI-compatible /v1/embeddings endpoint that
// returns a small fixed-width vector for every input.
func embeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/embeddings") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type item struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		var out struct {
			Data []item `json:"data"`
		}
		for i := range req.Input {
			out.Data = append(out.Data, item{Embedding: []float32{1, float32(i), 0.5}, Index: i})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const headlessIndexConfig = `[project]
name = "headless-index"

[profile]
default = "p"

[providers.embed]
type = "openai_compatible"
base_url = "%s"
api_key = "k"

[models.presets."embed/e"]
local_only = true

[agent_profiles.p]
implementer = "embed/e"
planner = "embed/e"
repo_scout = "embed/e"
tester = "embed/e"
reviewer = "embed/e"

[lsp]
enabled = false

[indexing]
watch = true
watch_debounce_ms = 50
embedding_preset = "embed/e"
`

func chunkedFile(t *testing.T, database *db.DB, projectID int64, path string) bool {
	t.Helper()
	files, err := database.ChunkedFiles(projectID)
	if err != nil {
		t.Fatalf("ChunkedFiles: %v", err)
	}
	_, ok := files[path]
	return ok
}

// The P1 done-when: with indexing.watch on, an edit in a headless session's
// tree refreshes its embeddings with no TUI running.
func TestHeadlessRuntimeRefreshesEmbeddingsAfterAnEdit(t *testing.T) {
	srv := embeddingServer(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".marshal", "config.toml"), []byte(fmt.Sprintf(headlessIndexConfig, srv.URL)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hosts := NewHosts()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hosts.Close(ctx)
	})
	rt, err := StartRuntime(context.Background(), WithWorkingDir(root), WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}), WithHosts(hosts))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	database := must[*db.DB](rt.DB)

	deadline := time.Now().Add(30 * time.Second)
	for !chunkedFile(t, database, rt.ProjectID, "main.go") {
		if time.Now().After(deadline) {
			t.Fatal("the startup scan never embedded main.go")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Rewrite the new file until the watcher has picked it up: the watcher
	// starts only after the startup scan returns, so a single write could
	// land before it is watching.
	added := filepath.Join(root, "added.go")
	for i := 0; !chunkedFile(t, database, rt.ProjectID, "added.go"); i++ {
		if time.Now().After(deadline) {
			t.Fatal("an edit in the tree was never embedded")
		}
		src := fmt.Sprintf("package main\n\n// Added%d is new.\nfunc Added%d() {}\n", i, i)
		if err := os.WriteFile(added, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
```

| Test | Asserts |
|---|---|
| `TestHeadlessRuntimeRefreshesEmbeddingsAfterAnEdit` | a `StartRuntime` on `NewHosts()` with `indexing.watch = true` embeds the existing file, then a new file written in the tree (the done-when) |

**Verify:**

```bash
gofmt -l internal/app/                         # no output
go vet ./internal/app/
grep -rn 'IndexBroker\|buildIndexWorkers\|namedWorker' internal/ cmd/   # no output
TMPDIR=/private/tmp/p1-suite go test ./internal/app/ ./internal/acp/ -count=1   # ok
```

## Task 8: ACP entry points

**Goal:** `acp.Run` (stdio) and `acp.ListenAndServe` create one registry per process, pass it to every runtime, and close it after the agent host has closed every session (spec H1).

**Files:** `internal/acp/run.go`, `internal/acp/listen.go`, `internal/acp/hosts_test.go` (new).

Depends on: Task 7.

**Steps:**

1. In `run.go`, create the registry, add `app.WithHosts(hosts)` to the start options, and join `closeHosts(hosts)` into the result. `closeHosts` bounds the close by `connectionShutdownTimeout`.

Apply this change to `internal/acp/run.go`:

```diff
diff --git a/internal/acp/run.go b/internal/acp/run.go
index 7531e41e..2b3c1f0f 100644
--- a/internal/acp/run.go
+++ b/internal/acp/run.go
@@ -78,13 +78,25 @@ func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
 		return fmt.Errorf("acp: find home directory: %w", err)
 	}
 	trustStore := trust.NewStore(config.DataDir(home))
-	return runWithConfig(ctx, stdin, stdout, runConfig{
+	// One project-host registry for the process: sessions on one project
+	// share its database setup, index and language servers. It closes after
+	// every session has.
+	hosts := app.NewHosts()
+	err = runWithConfig(ctx, stdin, stdout, runConfig{
 		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
-			opts = append(opts, app.WithTrustResolver(trust.NewHeadlessResolver(trustStore, log)))
+			opts = append(opts, app.WithTrustResolver(trust.NewHeadlessResolver(trustStore, log)), app.WithHosts(hosts))
 			return app.StartRuntime(ctx, opts...)
 		},
 		lister:   newPerCwdLister(),
 		shutdown: connectionShutdownTimeout,
 		logger:   log,
 	})
+	return errors.Join(err, closeHosts(hosts))
+}
+
+// closeHosts closes the process's project hosts within the shutdown bound.
+func closeHosts(hosts *app.Hosts) error {
+	ctx, cancel := context.WithTimeout(context.Background(), connectionShutdownTimeout)
+	defer cancel()
+	return hosts.Close(ctx)
 }
```

2. The same in `listen.go`, reusing `closeHosts`.

Apply this change to `internal/acp/listen.go`:

```diff
diff --git a/internal/acp/listen.go b/internal/acp/listen.go
index 0998f976..5e4cae77 100644
--- a/internal/acp/listen.go
+++ b/internal/acp/listen.go
@@ -54,15 +54,19 @@ func ListenAndServe(ctx context.Context, network, addr string, stderr io.Writer)
 		}
 	}
 
-	return listenAndServeWithConfig(ctx, ln, runConfig{
+	// One project-host registry for the process, shared by every
+	// connection's sessions and closed after all of them (see Run).
+	hosts := app.NewHosts()
+	err = listenAndServeWithConfig(ctx, ln, runConfig{
 		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
-			opts = append(opts, app.WithTrustResolver(trust.NewHeadlessResolver(trustStore, log)))
+			opts = append(opts, app.WithTrustResolver(trust.NewHeadlessResolver(trustStore, log)), app.WithHosts(hosts))
 			return app.StartRuntime(ctx, opts...)
 		},
 		lister:   newPerCwdLister(),
 		shutdown: connectionShutdownTimeout,
 		logger:   log,
 	})
+	return errors.Join(err, closeHosts(hosts))
 }
 
 // listenAndServeWithConfig owns the accept loop. Exactly one connection
```

3. Create `internal/acp/hosts_test.go` with exactly this content:

```go
package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app"
	"marshal/internal/trust"
)

// sharedHostProject writes a project whose config runs a stub language
// server for shell scripts, so its sessions have a manager to share. The stub
// exits at once; the manager is pooled all the same.
func sharedHostProject(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	if err := writeMarshalConfig(t, tmp); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(tmp, "lsp-stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(tmp, ".marshal", "config.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "[lsp]\nenabled = true\n[lsp.servers.shell]\ncommand = %q\n", stub); err != nil {
		t.Fatal(err)
	}
	return tmp
}

func TestSessionsOnOneProjectShareTheHostAndLanguageServers(t *testing.T) {
	tmp := sharedHostProject(t)
	ctx := context.Background()
	hosts := app.NewHosts()
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_ = hosts.Close(closeCtx)
	})
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: app.StartRuntime,
		CloseRuntime: func(ctx context.Context, rt *app.Runtime) error { return rt.Close(ctx) },
		Notify:       func(method string, params any) error { return nil },
		Options: []app.Option{
			app.WithHomeDir(t.TempDir()),
			app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
			app.WithHosts(hosts),
		},
	})
	m.SetTurnCanceller(noopCancel())
	t.Cleanup(func() { _ = m.CloseAll(ctx) })

	create := func() *app.Runtime {
		t.Helper()
		res, err := m.Create(ctx, json.RawMessage(`{"cwd":"`+tmp+`","mcpServers":[]}`))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		rt, ok := m.Get(res.(SessionResponse).SessionID)
		if !ok {
			t.Fatal("created session is not registered")
		}
		return rt
	}
	a, b := create(), create()
	if a.SessionID == b.SessionID {
		t.Fatal("two session/new calls returned one session")
	}
	if a.Host == nil || a.Host != b.Host {
		t.Fatal("two sessions on one project got different project hosts")
	}
	if a.LSPManager.Get() == nil || a.LSPManager.Get() != b.LSPManager.Get() {
		t.Fatalf("two sessions on one project got managers %p and %p; want one shared manager", a.LSPManager.Get(), b.LSPManager.Get())
	}
}
```

| Test | Asserts |
|---|---|
| `TestSessionsOnOneProjectShareTheHostAndLanguageServers` | two `session/new` calls through the `SessionManager` share one host and one manager |

The entry points themselves are checked with a built binary in Task 9.

**Verify:**

```bash
gofmt -l internal/acp/                         # no output
go vet ./internal/acp/
CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p1-suite go test ./internal/acp/ -count=1   # ok
```

## Task 9: Full verification, smoke check and docs

**Goal:** the repository builds, vets and tests clean apart from known local failures; a built `marshal acp` indexes headlessly; the spec and plan are committed with the work.

**Files:** `docs/daemon/p1-project-host.md`, `docs/daemon/plans/2026-10-19-p1-project-host-plan.md`, `docs/daemon/design.md`.

**Steps:**

1. Run the repository's checks (AGENTS.md):

   ```bash
   gofmt -l .                                    # no output
   CGO_ENABLED=1 go build ./cmd/marshal
   go vet ./...
   TMPDIR=/private/tmp/p1-suite go test -race ./internal/app/ ./internal/acp/ ./internal/lsp/ ./internal/index/ -count=1
   TMPDIR=/private/tmp/p1-suite go test ./... -count=1
   ```

   In the full run, only these known local failures are acceptable:
   - `TestStatusLineUntrustedIsWarningColored` (`internal/app/tui`);
   - `TestStandaloneBackendNavigateAndRead` (`internal/tools/desktop/browser`);
   - `TestCLIGitOpsCommitAllWithoutARepoIdentity` (`internal/worktree`);
   - the flaky `TestRunManagedCancellationKillsTree` (`internal/snapshot`).

   Confirm any other failure against a clean worktree at the base commit before treating it as caused by this change.

2. Smoke-check the built binary. Save this script outside the repository, for example as `/private/tmp/p1-suite/acp_smoke.py`. It runs `marshal acp` with an isolated `HOME`, `XDG_CONFIG_HOME` and `XDG_DATA_HOME` (so it never reads your config or keychain), opens two sessions on a fresh git project and reads the project database:

   ```python
   """Smoke-check headless indexing in a real `marshal acp` binary.

   Usage: python3 -I acp_smoke.py <marshal-binary> <scratch-dir>
   Starts `marshal acp` with an isolated HOME/XDG, opens two sessions on one
   git project, waits, then reports the index lease file, the file index row
   count and the host's log lines.
   """
   import json, os, subprocess, sys, time, shutil, sqlite3

   binary, scratch = sys.argv[1], sys.argv[2]
   base = os.path.join(scratch, 'smoke')
   shutil.rmtree(base, ignore_errors=True)
   home = os.path.join(base, 'home'); proj = os.path.join(base, 'proj')
   os.makedirs(os.path.join(home, '.config', 'marshal')); os.makedirs(proj)
   open(os.path.join(home, '.config', 'marshal', 'config.toml'), 'w').write('''[profile]
   default = "p"
   [providers.mock]
   type = "openai_compatible"
   base_url = "http://127.0.0.1:9/v1"
   api_key = "k"
   [models.presets."mock/m"]
   local_only = true
   [agent_profiles.p]
   implementer = "mock/m"
   planner = "mock/m"
   repo_scout = "mock/m"
   tester = "mock/m"
   reviewer = "mock/m"
   [lsp]
   enabled = false
   ''')
   subprocess.run(['git', 'init', '-q', proj], check=True)
   open(os.path.join(proj, 'main.go'), 'w').write('package main\n\nfunc main() {}\n')
   env = {'PATH': os.environ['PATH'], 'HOME': home, 'TMPDIR': base,
          'XDG_CONFIG_HOME': os.path.join(home, '.config'), 'XDG_DATA_HOME': os.path.join(home, '.local', 'share')}
   p = subprocess.Popen([binary, 'acp'], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, cwd=proj, text=True)
   def call(i, method, params):
       p.stdin.write(json.dumps({'jsonrpc': '2.0', 'id': i, 'method': method, 'params': params}) + '\n'); p.stdin.flush()
       while True:
           msg = json.loads(p.stdout.readline())
           if msg.get('id') == i:
               return msg
   call(1, 'initialize', {'protocolVersion': 1})
   s1 = call(2, 'session/new', {'cwd': proj, 'mcpServers': []})
   s2 = call(3, 'session/new', {'cwd': proj, 'mcpServers': []})
   print('session/new results:', 'error' not in s1, 'error' not in s2)
   time.sleep(3)
   lock = os.path.join(proj, '.marshal', 'index.lock')
   print('index.lock exists:', os.path.exists(lock))
   db = sqlite3.connect('file:' + os.path.join(proj, '.marshal', 'marshal.db') + '?mode=ro', uri=True)
   print('files rows:', db.execute('select count(*) from files').fetchone()[0])
   db.close()
   p.stdin.close()
   try:
       p.wait(timeout=20)
   except subprocess.TimeoutExpired:
       p.kill(); print('acp did not exit after stdin closed')
   print('acp exit code:', p.returncode)
   log = open(os.path.join(proj, '.marshal', 'marshal.log')).read().splitlines()
   print('log lines mentioning index:', [l[:160] for l in log if 'index' in l.lower()][:5])
   ```

   Run `python3 -I /private/tmp/p1-suite/acp_smoke.py ./marshal /private/tmp/p1-suite`. Expected: both `session/new` results `True`, `index.lock exists: True`, `files rows: 1`, `acp exit code: 0`. (Before this change the same script prints `False` and `0`.)

3. Add an "Implementation record" section at the end of `docs/daemon/p1-project-host.md`: the commit range, the step 1 and step 2 results, and any deviation from this plan. Set this plan's Status line to executed. In `docs/daemon/design.md`, update the status line and mark P1 done in §7.

4. Commit `docs/daemon/` as `p1-project-host: task 9 — verification and docs`.

**Verify:** step 1's commands give the results stated, step 2 prints the expected lines, step 3's record is written, and `git status` shows no stray files from this work.

## Integration notes

- **Every runtime has a host now.** `Runtime.Host` is never nil for a runtime built by `startRuntime`. Tests that build a `Runtime` literal leave it nil; `bindLSP`, `unbindLSP`, `Close` and `noteConfig` all tolerate that.
- **The LSP handle is permanent.** `Runtime.LSPManager` is non-nil for every started runtime even with LSP disabled; its `Get()` is what can be nil. Never replace the handle: point it with `bindLSP`, or `Set` it in tests.
- **Reloads keep running servers** when the server set is unchanged, instead of restarting them. The TUI's reference finder, bound once at startup, keeps working after a reload.
- **Index work runs once per tree.** A TUI that does not hold the index lease neither indexes nor shows index progress, and a resume with the watcher off no longer rescans. Quitting the TUI waits up to 5s for index work to stop; passes are cancelled, so it is normally immediate.
- **Headless indexing is new.** Every `marshal acp` process now runs the startup scan (and the watcher when `indexing.watch = true`), under the lease. On Docker Desktop for macOS `flock` does not cross into the VM, so a containerized agent and a host process can both index one tree (spec H4).
- **`Handle.Restart` has no app caller** left; it stays for its own tests.
- **Signature change:** `buildAgentRunnerWithLock` takes the handle and returns 12 results plus an error. `buildAgentRunner` is unchanged for its test callers.

## Plan self-review

- **Self-contained and independently verifiable:** yes. Built task by task on a fresh worktree at the base, with each task's Verify passing after it.
- **Cold-executor test:** every new file is complete and every edit is an exact diff with its intent stated. No step asks the executor to make a design decision.
- **Cross-task declarations:** all are in Contracts, with the names matching the code.
- **Parallel-safe markers:** Tasks 1, 2 and 3 touch different files and packages (Task 3 is the only one in `internal/app`), and none uses another's output. Task 2 and Task 4 likewise. Task 4 uses Task 1's `TryLease`, so it follows Task 1.
- **Anchors:** confirmed at the base: `buildAgentRunner`, `buildAgentRunnerWithLock`, `reloadAgentRuntime`, `Run`'s worker block, `buildIndexWorkers`, `namedWorker`, `startRuntime`'s database block and LSP block, `newSessionLocked`, `Runtime.Close`, `runLSPManager`, `acp.Run`, `acp.ListenAndServe`.
- **Verification commands:** match AGENTS.md, with the TMPDIR override explained under Assumptions.
- **Placeholders:** none.
- **Against the spec:** the spec was revised during planning so that H3, H6, the design table and T3, T4 and T8 describe this implementation: index work starts at the first attach, `buildIndexWorkers` is removed rather than reworked, and the ACP test drives the `SessionManager`.
