package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"marshal/internal/db"
)

// cachedDB holds an open database handle and the time it was opened so
// that perCwdLister can detect staleness and re-open.
type cachedDB struct {
	db     *db.DB
	opened time.Time
}

// perCwdLister implements SessionLister by caching per-cwd database
// handles with a TTL. ListSessions never creates the database; it
// returns an empty result if the file does not exist. DeleteSession is
// the only path that creates the directory and opens the DB.
type perCwdLister struct {
	mu    sync.Mutex
	cache map[string]*cachedDB
	ttl   time.Duration
}

func newPerCwdLister() *perCwdLister {
	return &perCwdLister{
		cache: make(map[string]*cachedDB),
		ttl:   30 * time.Second,
	}
}

// getOrOpen returns a cached *db.DB for cwd if one exists and is within
// the TTL. Otherwise it closes the stale handle, opens the database, runs
// Migrate, and caches the new handle.
//
// The whole check/open/store runs under l.mu. Opens are rare (TTL-cached),
// and the single lock removes both hazards of the old two-mutex scheme:
// the self-deadlock on TTL expiry (re-locking a held entry mutex) and the
// unsynchronized read of a handle another goroutine could be closing.
func (l *perCwdLister) getOrOpen(cwd string) (*db.DB, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if entry, ok := l.cache[cwd]; ok {
		if time.Since(entry.opened) < l.ttl {
			return entry.db, nil
		}
		// stale
		_ = entry.db.Close()
		delete(l.cache, cwd)
	}

	path := db.Path(cwd)
	d, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	if err := d.Migrate(); err != nil {
		_ = d.Close()
		return nil, err
	}
	l.cache[cwd] = &cachedDB{db: d, opened: time.Now()}
	return d, nil
}

// ListSessions returns an empty list without opening or creating the
// database if the per-cwd database file does not exist. Otherwise it
// uses the cached (or freshly opened) handle.
func (l *perCwdLister) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error) {
	path := db.Path(cwd)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, "", nil // empty list, no error
	}
	d, err := l.getOrOpen(cwd)
	if err != nil {
		return nil, "", err
	}
	return d.ListSessions(ctx, cwd, cursor, limit)
}

// DeleteSession creates the directory if necessary, opens (or reuses)
// the cached database handle, and deletes the session.
func (l *perCwdLister) DeleteSession(ctx context.Context, cwd, sessionID string) (bool, error) {
	path := db.Path(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	d, err := l.getOrOpen(cwd)
	if err != nil {
		return false, err
	}
	return d.DeleteSession(ctx, sessionID)
}

// Close closes all cached database handles and clears the cache. It is
// idempotent: calling Close with an already-empty cache returns nil.
func (l *perCwdLister) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var errs []error
	for cwd, entry := range l.cache {
		if err := entry.db.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close db for %s: %w", cwd, err))
		}
		delete(l.cache, cwd)
	}
	return errors.Join(errs...)
}
