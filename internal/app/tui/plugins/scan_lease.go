package plugins

import (
	"context"
	"os"
	"sync"
)

// scanLease owns the temporary directory a scan produces until exactly one
// claimant takes it, or the lease is closed.
//
// A scan runs as a Bubble Tea command, and its result message may never be
// delivered: the panel can be replaced or closed while the clone is still
// running, so nothing downstream would ever delete the directory. The lease is
// the owner that outlives that message — it cancels the scan and deletes what
// it published, so no cleanup path depends on a message arriving.
type scanLease struct {
	cancel context.CancelFunc

	mu      sync.Mutex
	closed  bool
	claimed bool
	parent  string
}

// newScanLease returns a lease that cancels its scan through cancel.
func newScanLease(cancel context.CancelFunc) *scanLease {
	return &scanLease{cancel: cancel}
}

// Publish transfers cleanup ownership of the complete temporary parent to the
// lease and reports whether it took it. A closed lease deletes dir immediately
// and reports false: the work the directory belongs to has been abandoned.
func (l *scanLease) Publish(dir string) bool {
	l.mu.Lock()
	if l.closed || l.claimed {
		// Closed: nobody is waiting for this scan. Claimed: a claimant already
		// owns the directory this publish would replace.
		l.mu.Unlock()
		os.RemoveAll(dir)
		return false
	}
	previous := l.parent
	l.parent = dir
	l.mu.Unlock()
	// Cleanup is outside the mutex: a slow delete must not block a concurrent
	// publish or claim.
	if previous != "" && previous != dir {
		os.RemoveAll(previous)
	}
	return true
}

// Claim takes ownership of the published parent. It succeeds once only, and
// only for a published lease that has not been closed; the caller is then
// responsible for deleting the directory.
func (l *scanLease) Claim() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.claimed || l.parent == "" {
		return "", false
	}
	l.claimed = true
	parent := l.parent
	l.parent = ""
	return parent, true
}

// Close cancels the scan and disposes of whatever the lease still owns: an
// unclaimed published parent is deleted, and a lease closed before its scan
// publishes deletes the directory when that publish arrives. A claimed parent
// belongs to its claimant and is never deleted here. Repeated calls are safe.
func (l *scanLease) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	cancel := l.cancel
	parent := ""
	if !l.claimed {
		parent = l.parent
		l.parent = ""
	}
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if parent != "" {
		os.RemoveAll(parent)
	}
}
