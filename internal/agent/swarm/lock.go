// internal/agent/swarm/lock.go
package swarm

import "sync"

// WriteLock is the swarm's single write path: one shared instance is set
// as the WriteGate on every role runner, so at most one agent executes a
// non-read-only tool at a time (docs/07 swarm safety rules). Read-only
// tools never touch it, so parallel scouts are unaffected.
type WriteLock struct {
	mu sync.Mutex
}

// Acquire blocks until the lock is free and returns the release func.
// The returned release func is idempotent: calling it more than once is a
// no-op after the first call. It implements agent.WriteGate.
func (l *WriteLock) Acquire() (release func()) {
	l.mu.Lock()
	var once sync.Once
	return func() { once.Do(l.mu.Unlock) }
}
