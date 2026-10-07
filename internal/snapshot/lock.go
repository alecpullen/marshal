package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LockFileName is the permanent, package-level advisory lock file. It lives
// directly under the snapshots root, OUTSIDE any generation directory, so
// generation cleanup can never remove it. Nothing in this package deletes it:
// the file is created once and reused for the lifetime of the store.
const LockFileName = ".store.lock"

// lockPollInterval is how often a blocked acquirer retries the advisory lock
// while also checking its context. Polling (rather than a blocking flock) is
// what makes the wait cancellable.
const lockPollInterval = 25 * time.Millisecond

// inProcLock serialises ownership among goroutines of one process. The OS lock
// alone is not enough: flock is per-open-file-description, so two independent
// os.Open calls in the same process (and on some platforms two goroutines
// sharing one description) can both observe success. A process-local
// semaphore closes that gap.
type inProcLock struct {
	// sem has capacity 1. A channel is used rather than a sync.Mutex so a
	// waiting goroutine can give up when its context ends.
	sem chan struct{}
}

func newInProcLock() *inProcLock { return &inProcLock{sem: make(chan struct{}, 1)} }

// acquire takes the process-local lock, returning ctx.Err() if ctx ends first.
func (l *inProcLock) acquire(ctx context.Context) error {
	select {
	case l.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *inProcLock) release() { <-l.sem }

// inProcLocks owns one inProcLock per canonical lock-file path.
var inProcLocks = struct {
	mu sync.Mutex
	m  map[string]*inProcLock
}{m: make(map[string]*inProcLock)}

// inProcLockFor returns the process-local lock for path, creating it on first
// use. Entries are retained for the process lifetime: the set of snapshot roots
// a session touches is tiny and bounded.
func inProcLockFor(path string) *inProcLock {
	key := filepath.Clean(path)
	inProcLocks.mu.Lock()
	defer inProcLocks.mu.Unlock()
	l, ok := inProcLocks.m[key]
	if !ok {
		l = newInProcLock()
		inProcLocks.m[key] = l
	}
	return l
}

// StoreLock is an acquired, exclusive ownership of one snapshots root. It is
// held across processes via an OS advisory lock and across goroutines via a
// process-local semaphore.
//
// The OS releases the advisory lock when the holding process dies, so a crash
// cannot leave the store permanently locked — but it also means a crashed
// process's lock is gone without any cleanup, which is exactly what the
// ownership tests assert.
type StoreLock struct {
	path  string
	file  *os.File
	local *inProcLock

	mu       sync.Mutex
	released bool
	// drain runs before the OS lock is released and must leave no writer
	// alive. The manager registers its live subprocess trees here.
	drain []func() error
}

// AcquireStoreLock takes exclusive ownership of the snapshots root, creating
// the root directory and the permanent lock file if they do not exist.
//
// The wait is cancellable: while another owner holds the lock this polls at
// lockPollInterval and returns a ReasonLockTimeout error as soon as ctx ends.
func AcquireStoreLock(ctx context.Context, root string) (*StoreLock, error) {
	if !platformSupportsOwnership {
		return nil, storeErrorf(ReasonUnsupportedPlatform, "advisory store locking")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, storeErrorf(ReasonUnreadableFile, "create snapshots root %s: %v", root, err)
	}
	path := filepath.Join(root, LockFileName)
	local := inProcLockFor(path)
	if err := local.acquire(ctx); err != nil {
		return nil, storeError(ReasonLockTimeout, err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		local.release()
		return nil, storeErrorf(ReasonUnreadableFile, "open lock file %s: %v", path, err)
	}

	lock := &StoreLock{path: path, file: f, local: local}
	acq, err := pollFlock(ctx, f)
	if err != nil {
		f.Close()
		local.release()
		return nil, err
	}
	_ = acq
	return lock, nil
}

// pollFlock retries the platform advisory lock until it is granted or ctx ends.
func pollFlock(ctx context.Context, f *os.File) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, storeError(ReasonLockTimeout, err)
		}
		got, err := tryFlock(f)
		if err != nil {
			return false, storeErrorf(ReasonUnreadableFile, "advisory lock %s: %v", f.Name(), err)
		}
		if got {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, storeError(ReasonLockTimeout, ctx.Err())
		case <-time.After(lockPollInterval):
		}
	}
}

// OnRelease registers a drain step that runs before ownership is released. A
// drain that fails aborts the release: freeing the lock while a writer may
// still be alive would let a second owner race the first.
func (l *StoreLock) OnRelease(fn func() error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.drain = append(l.drain, fn)
}

// Path is the lock file path.
func (l *StoreLock) Path() string { return l.path }

// Release drops ownership. Drains run first, newest first. It is idempotent and
// never deletes the lock file.
func (l *StoreLock) Release() error {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil
	}
	l.released = true
	drains := make([]func() error, len(l.drain))
	copy(drains, l.drain)
	l.drain = nil
	l.mu.Unlock()

	var firstErr error
	for i := len(drains) - 1; i >= 0; i-- {
		if err := drains[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		// Keep holding: the caller must not hand the store to another writer
		// while one of its own may still be running. The file and the
		// process-local semaphore stay held, and a later Release retries.
		l.mu.Lock()
		l.released = false
		l.mu.Unlock()
		return firstErr
	}

	var err error
	if l.file != nil {
		// Closing the descriptor releases the advisory lock. The file itself
		// stays on disk permanently.
		err = errors.Join(releaseFlock(l.file), l.file.Close())
		l.file = nil
	}
	l.local.release()
	return err
}
