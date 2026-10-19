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
