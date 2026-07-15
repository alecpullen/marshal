package acp

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"marshal/internal/db"
)

// cachedDB holds an open database handle and the time it was opened so
// that perCwdLister can detect staleness and re-open.
type cachedDB struct {
	mu     sync.Mutex
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
// the TTL. Otherwise it opens the database, runs Migrate exactly once,
// and caches the handle.
//
// The entry is populated (db + opened) BEFORE being published to the
// cache map so that concurrent callers never observe a partially-
// initialised entry. A double-check pattern handles the rare case where
// two goroutines race past the initial map lookup.
func (l *perCwdLister) getOrOpen(cwd string) (*db.DB, error) {
	l.mu.Lock()
	entry, ok := l.cache[cwd]
	l.mu.Unlock()

	if ok {
		entry.mu.Lock()
		defer entry.mu.Unlock()

		if time.Since(entry.opened) < l.ttl {
			return entry.db, nil
		}
		// Stale — close and reopen below.
		_ = entry.db.Close()
		entry.db = nil
	} else {
		entry = &cachedDB{}
	}

	dbPath := filepath.Join(cwd, ".marshal", "marshal.db")
	d, err := db.Open(dbPath)
	if err != nil {
		return nil, err
	}
	if err := d.Migrate(); err != nil {
		_ = d.Close()
		return nil, err
	}

	// Populate the entry BEFORE publishing it to the cache so
	// concurrent callers see a fully-initialised entry.
	entry.mu.Lock()
	entry.db = d
	entry.opened = time.Now()
	entry.mu.Unlock()

	if !ok {
		l.mu.Lock()
		// Double-check: another goroutine may have raced us.
		if existing, present := l.cache[cwd]; present {
			l.mu.Unlock()
			// They beat us; close our DB and use theirs.
			_ = d.Close()
			return existing.db, nil
		}
		l.cache[cwd] = entry
		l.mu.Unlock()
	}

	return d, nil
}

// ListSessions returns an empty list without opening or creating the
// database if the per-cwd database file does not exist. Otherwise it
// uses the cached (or freshly opened) handle.
func (l *perCwdLister) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error) {
	dbPath := filepath.Join(cwd, ".marshal", "marshal.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
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
	dbPath := filepath.Join(cwd, ".marshal", "marshal.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return false, err
	}
	d, err := l.getOrOpen(cwd)
	if err != nil {
		return false, err
	}
	return d.DeleteSession(ctx, sessionID)
}
