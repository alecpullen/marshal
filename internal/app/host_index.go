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
