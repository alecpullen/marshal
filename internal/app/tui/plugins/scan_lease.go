package plugins

import (
	"context"
	"sync"
)

// scanLease owns the token naming a staged plugin scan until exactly one
// claimant takes it, or the lease is closed.
//
// A scan runs as a Bubble Tea command, and its result message may never be
// delivered: the panel can be replaced or closed while the clone is still
// running, so nothing downstream would ever discard the staged clone. The
// lease is the owner that outlives that message — it cancels the scan and
// discards what it published, so no cleanup path depends on a message
// arriving.
type scanLease struct {
	cancel  context.CancelFunc
	discard func(token string)

	mu      sync.Mutex
	closed  bool
	claimed bool
	token   string
}

// newScanLease returns a lease that cancels its scan through cancel and
// discards an abandoned token through discard. A nil discard is a lease whose
// tokens nobody can release; production always passes the session's
// DiscardPluginScan.
func newScanLease(cancel context.CancelFunc, discard func(token string)) *scanLease {
	return &scanLease{cancel: cancel, discard: discard}
}

// Publish transfers cleanup ownership of the staged scan the token names to
// the lease and reports whether it took it. A closed lease discards the token
// immediately and reports false: the work it belongs to has been abandoned.
// Cleanup runs outside the mutex, because a slow round trip must not block a
// concurrent publish or claim.
func (l *scanLease) Publish(token string) bool {
	l.mu.Lock()
	if l.closed || l.claimed {
		// Closed: nobody is waiting for this scan. Claimed: a claimant already
		// owns the token this publish would replace.
		l.mu.Unlock()
		l.discardToken(token)
		return false
	}
	previous := l.token
	l.token = token
	l.mu.Unlock()
	if previous != "" && previous != token {
		l.discardToken(previous)
	}
	return true
}

// Claim takes ownership of the published token. It succeeds once only, and
// only for a published lease that has not been closed; the caller is then
// responsible for installing or discarding that staged scan.
func (l *scanLease) Claim() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.claimed || l.token == "" {
		return "", false
	}
	l.claimed = true
	token := l.token
	l.token = ""
	return token, true
}

// Close cancels the scan and disposes of whatever the lease still owns: an
// unclaimed published token is discarded, and a lease closed before its scan
// publishes discards the token when that publish arrives. A claimed token
// belongs to its claimant and is never discarded here. Repeated calls are
// safe.
func (l *scanLease) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	cancel := l.cancel
	token := ""
	if !l.claimed {
		token = l.token
		l.token = ""
	}
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if token != "" {
		l.discardToken(token)
	}
}

// discardToken releases one staged scan, outside every lock the lease holds.
func (l *scanLease) discardToken(token string) {
	if token == "" || l.discard == nil {
		return
	}
	l.discard(token)
}
