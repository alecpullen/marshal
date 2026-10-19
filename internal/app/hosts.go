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
