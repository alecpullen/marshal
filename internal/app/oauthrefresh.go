package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/oauthworker"
	"marshal/internal/worker"
)

// The refresh helper's timings (P4 D12, decisions 2, 4 and 6).
const (
	// oauthLeaseRetry is how often a process retries the refresh lease while
	// another process holds it.
	oauthLeaseRetry = 30 * time.Second
	// oauthConfigPoll is how often the helper compares the user config's
	// stamp, and how often an embedded runtime re-reads it, while the lease is
	// not held.
	oauthConfigPoll = 5 * time.Second
	// oauthHandoffWait is how long a runtime waits for its predecessor's
	// refresh work to return and release the lease.
	oauthHandoffWait = 2 * time.Second
)

// oauthLease holds the refresh lease around work (decision 2): the one
// process-wide lock file that says which process refreshes OAuth tokens ahead
// of their expiry.
type oauthLease struct {
	tryLease func() (release func(), held bool, err error)
	retry    time.Duration
	log      *slog.Logger
}

// hold runs work while the lease is held, trying every retry until it is. It
// returns once ctx has ended and work has returned, with the lease released.
//
// A lease that cannot be taken for a reason other than another holder is
// logged, and work runs without it: refreshing twice costs requests but breaks
// nothing, as a second index pass does.
func (l oauthLease) hold(ctx context.Context, work func(ctx context.Context)) {
	log := l.log
	if log == nil {
		log = slog.Default()
	}
	retry := l.retry
	if retry <= 0 {
		retry = oauthLeaseRetry
	}
	waiting := false
	for {
		if ctx.Err() != nil {
			return
		}
		release, held, err := l.tryLease()
		switch {
		case err != nil:
			log.Warn("oauth refresh lease unavailable; refreshing without it", "error", err)
			work(ctx)
			return
		case held:
			work(ctx)
			release()
			return
		}
		// Another process holds the lease: wait and try again, so this
		// process takes over when that one exits. Said once, not on every
		// retry: at one attempt a minute a repeating line is noise.
		if !waiting {
			log.Info("oauth refresh: another process holds the refresh lease; waiting for it", "retry", retry)
			waiting = true
		}
		t := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// runWorkers starts each worker on ctx and returns once every one has
// returned.
func runWorkers(ctx context.Context, workers []worker.Worker, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	var wg sync.WaitGroup
	for _, w := range workers {
		startWorker(ctx, &wg, w, log)
	}
	wg.Wait()
}

// configStamp is path's modification time and size, or "absent" when there is
// no such file.
//
// The helper polls this instead of watching the file: an editor's atomic
// rename replaces the file a watch was on, so the watch would go deaf after
// the first save (decision 6).
func configStamp(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "absent"
	}
	return fmt.Sprintf("%d/%d", info.ModTime().UnixNano(), info.Size())
}

// OAuthRefreshOptions configures RunOAuthRefresh.
type OAuthRefreshOptions struct {
	HomeDir string                // required: whose user config and data dir
	Auth    provider.AuthResolver // required: the credential store the workers refresh through
	Logger  *slog.Logger          // nil means slog.Default()

	// Test seams; their zero values are the production ones.
	tryLease func() (release func(), held bool, err error)
	workers  func(cfg config.Config) []worker.Worker
	retry    time.Duration
	poll     time.Duration
}

// RunOAuthRefresh is `marshal daemon refresh` (P4 D12): it refreshes the user
// config's OAuth providers under the refresh lease until ctx ends.
//
// The config is read for its providers only, and it is user-global, so a
// project cannot change who refreshes. A config that names no OAuth provider
// keeps no lease: the helper waits for the file's stamp to change instead
// (decision 6).
func RunOAuthRefresh(ctx context.Context, opts OAuthRefreshOptions) error {
	if opts.HomeDir == "" {
		return errors.New("oauth refresh: a home directory is required")
	}
	if opts.Auth == nil {
		return ErrAuthResolverRequired
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	dataDir := config.DataDir(opts.HomeDir)
	configPath := config.UserConfigPath(opts.HomeDir)

	tryLease := opts.tryLease
	if tryLease == nil {
		tryLease = func() (func(), bool, error) { return oauthworker.TryLease(dataDir) }
	}
	build := opts.workers
	if build == nil {
		build = func(cfg config.Config) []worker.Worker {
			return buildOAuthWorkers(cfg, log, opts.Auth)
		}
	}
	retry := opts.retry
	if retry <= 0 {
		retry = oauthLeaseRetry
	}
	poll := opts.poll
	if poll <= 0 {
		poll = oauthConfigPoll
	}

	// load builds the workers the user config names. A config that cannot be
	// read is logged and counts as naming none: the provider still refreshes
	// inline when it is used, so a helper that gives up its lease is safe.
	load := func() []worker.Worker {
		cfg, err := config.Load(config.LoadOptions{HomeDir: opts.HomeDir, SkipProjectConfig: true})
		if err != nil {
			log.Warn("oauth refresh: cannot read the user config", "path", configPath, "error", err)
			return nil
		}
		return build(cfg)
	}

	lease := oauthLease{tryLease: tryLease, retry: retry, log: log}
	for ctx.Err() == nil {
		stamp := configStamp(configPath)
		workers := load()
		if len(workers) == 0 {
			if !waitForConfigStamp(ctx, configPath, stamp, poll) {
				break
			}
			continue
		}
		lease.hold(ctx, func(ctx context.Context) {
			refreshUnderLease(ctx, configPath, stamp, workers, load, poll, log)
		})
	}
	return nil
}

// refreshUnderLease runs the workers the user config names, and rebuilds them
// when the config changes, all while the caller holds the lease (decision 6).
//
// It returns when ctx ends, or when a changed config names no OAuth provider:
// that return releases the lease, so another process can take it over.
func refreshUnderLease(ctx context.Context, configPath, stamp string, workers []worker.Worker, load func() []worker.Worker, poll time.Duration, log *slog.Logger) {
	// The config may have changed while another process held the lease, so a
	// set built before the wait is not trusted: rebuild it before starting
	// anything, rather than refreshing a token for a provider that is gone.
	if configStamp(configPath) != stamp {
		workers = load()
	}
	if len(workers) == 0 {
		return
	}
	log.Info("oauth refresh: holding the refresh lease", "providers", len(workers))
	defer log.Info("oauth refresh: released the refresh lease", "providers", len(workers))

	for ctx.Err() == nil {
		changed := runGeneration(ctx, workers, configPath, poll, log)
		if !changed {
			// ctx ended; the caller's release follows this return.
			return
		}
		// The config changed: the previous set has stopped, so read the new
		// one and never refresh the same provider from two sets at once.
		workers = load()
		if len(workers) == 0 {
			return
		}
		log.Info("oauth refresh: rebuilding for a changed config", "providers", len(workers))
	}
}

// runGeneration runs one set of workers until the user config's stamp changes
// or ctx ends, and reports whether the config changed. It always stops and
// joins its workers before returning.
func runGeneration(ctx context.Context, workers []worker.Worker, configPath string, poll time.Duration, log *slog.Logger) bool {
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, w := range workers {
		startWorker(runCtx, &wg, w, log)
	}
	changed := waitForConfigStamp(ctx, configPath, configStamp(configPath), poll)
	cancel()
	wg.Wait()
	return changed
}

// waitForConfigStamp waits until path's stamp differs from stamp, checking
// every poll. It reports false when ctx ended first.
func waitForConfigStamp(ctx context.Context, path, stamp string, poll time.Duration) bool {
	for {
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
		if configStamp(path) != stamp {
			return true
		}
	}
}
