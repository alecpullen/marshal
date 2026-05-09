package context

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// GC manages garbage collection of expired context entries.
type GC struct {
	store       *Store
	interval    time.Duration
	stopCh      chan struct{}
	stoppedCh   chan struct{}
	mu          sync.Mutex
	running     bool
	timeNow     func() time.Time
	maxAge      time.Duration // Maximum age for any entry
	dryRun      bool          // If true, only log what would be deleted
}

// GCOption configures the garbage collector.
type GCOption func(*GC)

// WithGCInterval sets the collection interval.
func WithGCInterval(d time.Duration) GCOption {
	return func(g *GC) {
		g.interval = d
	}
}

// WithGCMaxAge sets the maximum age for entries.
func WithGCMaxAge(d time.Duration) GCOption {
	return func(g *GC) {
		g.maxAge = d
	}
}

// WithGCDryRun enables dry-run mode (logging only).
func WithGCDryRun(dryRun bool) GCOption {
	return func(g *GC) {
		g.dryRun = dryRun
	}
}

// WithGCTimeFunc sets a custom time function (for testing).
func WithGCTimeFunc(fn func() time.Time) GCOption {
	return func(g *GC) {
		g.timeNow = fn
	}
}

// NewGC creates a new garbage collector.
func NewGC(store *Store, opts ...GCOption) *GC {
	g := &GC{
		store:     store,
		interval:  5 * time.Minute, // Default: check every 5 minutes
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
		timeNow:   func() time.Time { return time.Now().UTC() },
		maxAge:    24 * time.Hour, // Default: max 24 hours
		dryRun:    false,
	}

	for _, opt := range opts {
		opt(g)
	}

	return g
}

// Start begins the garbage collection loop.
func (g *GC) Start() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.running {
		return
	}

	g.running = true
	go g.loop()
}

// Stop halts the garbage collection loop.
func (g *GC) Stop() {
	g.mu.Lock()
	if !g.running {
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()

	close(g.stopCh)
	<-g.stoppedCh
}

// loop is the main GC loop.
func (g *GC) loop() {
	defer close(g.stoppedCh)

	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()

	// Run immediately on start
	g.run()

	for {
		select {
		case <-g.stopCh:
			return
		case <-ticker.C:
			g.run()
		}
	}
}

// run performs one garbage collection pass.
func (g *GC) run() {
	ctx := context.Background()

	// 1. Collect expired entries (by TTL)
	if err := g.collectExpired(ctx); err != nil {
		// Log error but continue
	}

	// 2. Collect old entries (by max age)
	if err := g.collectOldEntries(ctx); err != nil {
		// Log error but continue
	}

	// 3. Collect orphaned blobs
	if err := g.collectOrphanedBlobs(); err != nil {
		// Log error but continue
	}
}

// collectExpired removes entries whose TTL has expired.
func (g *GC) collectExpired(ctx context.Context) error {
	now := g.timeNow()

	rows, err := g.store.db.Query(`
		SELECT ref, storage_type, content_hash
		FROM ctx_entries
		WHERE ttl_seconds > 0
		AND datetime(produced_at, '+' || ttl_seconds || ' seconds') < ?
		AND session_id = ?
	`, now.Format(time.RFC3339), g.store.sessionID)
	if err != nil {
		return fmt.Errorf("querying expired entries: %w", err)
	}
	defer rows.Close()

	var toDelete []struct {
		ref         string
		storageType string
		hash        string
	}

	for rows.Next() {
		var ref, storageType, hash string
		if err := rows.Scan(&ref, &storageType, &hash); err != nil {
			continue
		}
		toDelete = append(toDelete, struct {
			ref         string
			storageType string
			hash        string
		}{ref, storageType, hash})
	}

	for _, item := range toDelete {
		if g.dryRun {
			continue
		}

		// Delete entry
		_, err := g.store.db.Exec(`
			DELETE FROM ctx_entries
			WHERE ref = ? AND session_id = ?
		`, item.ref, g.store.sessionID)
		if err != nil {
			continue
		}

		// Clean up blob if file-backed and no other references
		if item.storageType == "file" {
			g.cleanupBlob(item.hash)
		}
	}

	return nil
}

// collectOldEntries removes entries older than maxAge.
func (g *GC) collectOldEntries(ctx context.Context) error {
	if g.maxAge == 0 {
		return nil
	}

	cutoff := g.timeNow().Add(-g.maxAge)

	rows, err := g.store.db.Query(`
		SELECT ref, storage_type, content_hash
		FROM ctx_entries
		WHERE produced_at < ?
		AND (ttl_seconds = 0 OR datetime(produced_at, '+' || ttl_seconds || ' seconds') < ?)
		AND session_id = ?
	`, cutoff.Format(time.RFC3339), g.timeNow().Format(time.RFC3339), g.store.sessionID)
	if err != nil {
		return fmt.Errorf("querying old entries: %w", err)
	}
	defer rows.Close()

	var toDelete []struct {
		ref         string
		storageType string
		hash        string
	}

	for rows.Next() {
		var ref, storageType, hash string
		if err := rows.Scan(&ref, &storageType, &hash); err != nil {
			continue
		}
		toDelete = append(toDelete, struct {
			ref         string
			storageType string
			hash        string
		}{ref, storageType, hash})
	}

	for _, item := range toDelete {
		if g.dryRun {
			continue
		}

		_, err := g.store.db.Exec(`
			DELETE FROM ctx_entries
			WHERE ref = ? AND session_id = ?
		`, item.ref, g.store.sessionID)
		if err != nil {
			continue
		}

		if item.storageType == "file" {
			g.cleanupBlob(item.hash)
		}
	}

	return nil
}

// collectOrphanedBlobs removes blob files with no database references.
func (g *GC) collectOrphanedBlobs() error {
	// Walk the blob directory
	entries, err := os.ReadDir(g.store.blobDir)
	if err != nil {
		return fmt.Errorf("reading blob directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != 2 {
			continue
		}

		subdir := filepath.Join(g.store.blobDir, entry.Name())
		subentries, err := os.ReadDir(subdir)
		if err != nil {
			continue
		}

		for _, subentry := range subentries {
			if subentry.IsDir() || !strings.HasSuffix(subentry.Name(), ".blob") {
				continue
			}

			hash := strings.TrimSuffix(subentry.Name(), ".blob")
			
			// Check if any entry references this blob
			var count int
			err := g.store.db.QueryRow(`
				SELECT COUNT(*) FROM ctx_entries WHERE content_hash = ?
			`, hash).Scan(&count)
			if err != nil || count > 0 {
				continue // Still referenced or error
			}

			if g.dryRun {
				continue
			}

			// Delete orphaned blob
			blobPath := filepath.Join(subdir, subentry.Name())
			os.Remove(blobPath)
		}
	}

	return nil
}

// cleanupBlob removes a blob file if no other entries reference it.
func (g *GC) cleanupBlob(hash string) {
	var count int
	err := g.store.db.QueryRow(`SELECT COUNT(*) FROM ctx_entries WHERE content_hash = ?`, hash).Scan(&count)
	if err != nil || count > 0 {
		return
	}

	blobPath := g.store.blobPath(hash)
	os.Remove(blobPath)
}

// Stats returns statistics about the context store.
func (g *GC) Stats() (Stats, error) {
	var stats Stats

	// Count entries
	err := g.store.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(size), 0), COALESCE(SUM(size_tokens), 0)
		FROM ctx_entries
		WHERE session_id = ?
	`, g.store.sessionID).Scan(&stats.EntryCount, &stats.TotalBytes, &stats.TotalTokens)
	if err != nil {
		return stats, fmt.Errorf("counting entries: %w", err)
	}

	// Count inline vs file
	var inlineCount, fileCount int
	g.store.db.QueryRow(`
		SELECT 
			COUNT(CASE WHEN storage_type = 'inline' THEN 1 END),
			COUNT(CASE WHEN storage_type = 'file' THEN 1 END)
		FROM ctx_entries
		WHERE session_id = ?
	`, g.store.sessionID).Scan(&inlineCount, &fileCount)
	stats.InlineCount = inlineCount
	stats.FileBackedCount = fileCount

	// Count superseded entries
	g.store.db.QueryRow(`
		SELECT COUNT(*) FROM ctx_entries WHERE superseded_by IS NOT NULL AND session_id = ?
	`, g.store.sessionID).Scan(&stats.SupersededCount)

	// Count expired entries
	now := g.timeNow()
	g.store.db.QueryRow(`
		SELECT COUNT(*) FROM ctx_entries
		WHERE ttl_seconds > 0
		AND datetime(produced_at, '+' || ttl_seconds || ' seconds') < ?
		AND session_id = ?
	`, now.Format(time.RFC3339), g.store.sessionID).Scan(&stats.ExpiredCount)

	return stats, nil
}

// Stats contains statistics about the context store.
type Stats struct {
	EntryCount      int
	InlineCount     int
	FileBackedCount int
	SupersededCount int
	ExpiredCount    int
	TotalBytes      int64
	TotalTokens     int64
}

// RunOnce performs a single garbage collection pass (for testing/manual use).
func (g *GC) RunOnce(ctx context.Context) error {
	return g.collectExpired(ctx)
}

// SessionCleanup removes all context entries for a session.
func SessionCleanup(db *sql.DB, sessionID string, blobDir string) error {
	// Get all file-backed blobs for this session
	rows, err := db.Query(`
		SELECT content_hash FROM ctx_entries
		WHERE session_id = ? AND storage_type = 'file'
	`, sessionID)
	if err != nil {
		return fmt.Errorf("querying blobs: %w", err)
	}
	defer rows.Close()

	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			continue
		}
		hashes = append(hashes, hash)
	}

	// Delete entries (cascades to FTS)
	_, err = db.Exec(`DELETE FROM ctx_entries WHERE session_id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("deleting entries: %w", err)
	}

	// Clean up unreferenced blobs
	for _, hash := range hashes {
		var count int
		err := db.QueryRow(`SELECT COUNT(*) FROM ctx_entries WHERE content_hash = ?`, hash).Scan(&count)
		if err == nil && count == 0 {
			if len(hash) >= 2 {
				blobPath := filepath.Join(blobDir, hash[:2], hash+".blob")
				os.Remove(blobPath)
			}
		}
	}

	return nil
}
