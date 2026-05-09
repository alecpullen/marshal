// Package context provides content-addressed storage for context entries
// with FTS5 full-text search and hybrid blob storage.
package context

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alecpullen/marshal/pkg/protocol"
)



// Store provides content-addressed context storage with FTS5 search.
type Store struct {
	db              *sql.DB
	sessionID       string
	blobDir         string
	inlineThreshold int
	indexSizeLimit  int
	timeNow         func() time.Time
	mu              sync.RWMutex
}

// StoreOption configures a Store.
type StoreOption func(*Store)

// WithInlineThreshold sets a custom inline storage threshold.
func WithInlineThreshold(threshold int) StoreOption {
	return func(s *Store) {
		s.inlineThreshold = threshold
	}
}

// WithIndexSizeLimit sets a custom FTS5 index size limit.
func WithIndexSizeLimit(limit int) StoreOption {
	return func(s *Store) {
		s.indexSizeLimit = limit
	}
}

// WithTimeFunc sets a custom time function (for testing).
func WithTimeFunc(fn func() time.Time) StoreOption {
	return func(s *Store) {
		s.timeNow = fn
	}
}

// NewStore creates a new context store.
// The blobDir should be an absolute path like /repo/.marshal/sessions/<id>/ctx/
func NewStore(db *sql.DB, sessionID string, blobDir string, opts ...StoreOption) (*Store, error) {
	// Ensure blob directory exists
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		return nil, fmt.Errorf("creating blob directory: %w", err)
	}

	s := &Store{
		db:              db,
		sessionID:       sessionID,
		blobDir:         blobDir,
		inlineThreshold: DefaultInlineThreshold,
		indexSizeLimit:  DefaultIndexSizeLimit,
		timeNow:         func() time.Time { return time.Now().UTC() },
	}

	for _, opt := range opts {
		opt(s)
	}

	// Ensure blob subdirectories exist
	if err := s.ensureBlobDirs(); err != nil {
		return nil, fmt.Errorf("ensuring blob directories: %w", err)
	}

	return s, nil
}

// ensureBlobDirs creates 00-ff subdirectories for blob sharding.
func (s *Store) ensureBlobDirs() error {
	for i := 0; i < BlobSubdirCount; i++ {
		subdir := filepath.Join(s.blobDir, fmt.Sprintf("%02x", i))
		if err := os.MkdirAll(subdir, 0755); err != nil {
			return fmt.Errorf("creating subdirectory %s: %w", subdir, err)
		}
	}
	return nil
}

// blobPath returns the file path for a blob.
func (s *Store) blobPath(hash string) string {
	if len(hash) < 2 {
		return filepath.Join(s.blobDir, hash[:1], hash+".blob")
	}
	return filepath.Join(s.blobDir, hash[:2], hash+".blob")
}

// Put stores content and returns a context reference.
func (s *Store) Put(content []byte, key protocol.EntryKey, producedBy string, tags []string, ttl time.Duration) (protocol.ContextRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Compute hash and determine storage type
	hash := protocol.ComputeHash(content)
	size := len(content)
	tokens := protocol.EstimateTokens(content)
	storageType := "inline"
	var inlineContent []byte

	if size > s.inlineThreshold {
		storageType = "file"
	} else {
		inlineContent = content
	}

	// Create context reference
	ref := protocol.NewContextRef(key.Kind(), key.Path(), content)

	// Convert tags to JSON array
	tagsJSON, _ := json.Marshal(tags)

	// Calculate TTL seconds
	ttlSeconds := 0
	if ttl > 0 {
		ttlSeconds = int(ttl.Seconds())
	}

	// Begin transaction
	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	// Insert entry
	_, err = tx.Exec(`
		INSERT INTO ctx_entries (
			ref, key, kind, content_hash, size, size_tokens,
			produced_by, produced_at, tags, ttl_seconds,
			session_id, storage_type, inline_content
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		ref.String(), key.String(), key.Kind().String(), hash, size, tokens,
		producedBy, s.timeNow().Format(time.RFC3339Nano), string(tagsJSON), ttlSeconds,
		s.sessionID, storageType, inlineContent,
	)
	if err != nil {
		return "", fmt.Errorf("inserting entry: %w", err)
	}

	// If file-backed, write blob
	if storageType == "file" {
		blobPath := s.blobPath(hash)
		
		// Check for deduplication
		if _, err := os.Stat(blobPath); os.IsNotExist(err) {
			// Write atomically
			tmpPath := blobPath + ".tmp"
			if err := os.WriteFile(tmpPath, content, 0644); err != nil {
				return "", fmt.Errorf("writing blob: %w", err)
			}
			if err := os.Rename(tmpPath, blobPath); err != nil {
				os.Remove(tmpPath)
				return "", fmt.Errorf("finalizing blob: %w", err)
			}
		}
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("committing transaction: %w", err)
	}

	return ref, nil
}

// Get retrieves an entry by reference.
func (s *Store) Get(ref protocol.ContextRef) (*protocol.ContextEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var entry protocol.ContextEntry
	var storageType string
	var inlineContent []byte
	var tagsJSON string
	var producedAtStr string
	var supersededByStr string

	err := s.db.QueryRow(`
		SELECT ref, key, kind, content_hash, size, size_tokens,
		       produced_by, produced_at, tags, ttl_seconds, superseded_by,
		       storage_type, inline_content
		FROM ctx_entries
		WHERE ref = ? AND session_id = ?
	`, ref.String(), s.sessionID).Scan(
		&entry.Ref, &entry.Key, &entry.Kind, &entry.ContentHash, &entry.Size, &entry.SizeTokens,
		&entry.ProducedBy, &producedAtStr, &tagsJSON, &entry.Metadata.TTL, &supersededByStr,
		&storageType, &inlineContent,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entry not found: %s", ref)
	}
	if err != nil {
		return nil, fmt.Errorf("querying entry: %w", err)
	}

	// Parse timestamp
	entry.ProducedAt, _ = time.Parse(time.RFC3339Nano, producedAtStr)
	
	// Parse tags
	if tagsJSON != "" {
		json.Unmarshal([]byte(tagsJSON), &entry.Metadata.Tags)
	}
	
	// Parse superseded_by
	if supersededByStr != "" {
		entry.Metadata.SupersededBy = protocol.ContextRef(supersededByStr)
	}

	// Load content
	if storageType == "inline" {
		entry.Content = inlineContent
	} else {
		blobPath := s.blobPath(entry.ContentHash)
		content, err := os.ReadFile(blobPath)
		if err != nil {
			return nil, fmt.Errorf("reading blob: %w", err)
		}
		// Verify hash
		if actualHash := protocol.ComputeHash(content); actualHash != entry.ContentHash {
			return nil, fmt.Errorf("hash mismatch: expected %s, got %s", entry.ContentHash, actualHash)
		}
		entry.Content = content
	}

	return &entry, nil
}

// GetByKey retrieves the latest entry for a key.
func (s *Store) GetByKey(key protocol.EntryKey) (*protocol.ContextEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var refStr string
	err := s.db.QueryRow(`
		SELECT ref FROM ctx_entries
		WHERE key = ? AND session_id = ? AND superseded_by IS NULL
		ORDER BY produced_at DESC
		LIMIT 1
	`, key.String(), s.sessionID).Scan(&refStr)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no entry found for key: %s", key)
	}
	if err != nil {
		return nil, fmt.Errorf("querying entry: %w", err)
	}

	ref, err := protocol.ParseContextRef(refStr)
	if err != nil {
		return nil, err
	}

	return s.Get(ref)
}

// List returns entries matching the query.
func (s *Store) List(query protocol.ListQuery) ([]protocol.ContextEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Build query dynamically
	whereParts := []string{"session_id = ?"}
	args := []interface{}{s.sessionID}

	if len(query.Kinds) > 0 {
		placeholders := make([]string, len(query.Kinds))
		for i, k := range query.Kinds {
			placeholders[i] = "?"
			args = append(args, k.String())
		}
		whereParts = append(whereParts, fmt.Sprintf("kind IN (%s)", strings.Join(placeholders, ",")))
	}

	if query.ProducedBy != "" {
		whereParts = append(whereParts, "produced_by = ?")
		args = append(args, query.ProducedBy)
	}

	if query.PathPrefix != "" {
		whereParts = append(whereParts, "key LIKE ?")
		args = append(args, query.PathPrefix+"%")
	}

	if query.LatestOnly {
		whereParts = append(whereParts, "superseded_by IS NULL")
	}

	if len(query.Tags) > 0 {
		for _, tag := range query.Tags {
			whereParts = append(whereParts, "tags LIKE ?")
			args = append(args, "%"+tag+"%")
		}
	}

	whereClause := strings.Join(whereParts, " AND ")

	sqlStr := fmt.Sprintf(`
		SELECT ref, key, kind, content_hash, size, size_tokens,
		       produced_by, produced_at, tags, ttl_seconds, superseded_by
		FROM ctx_entries
		WHERE %s
		ORDER BY produced_at DESC
	`, whereClause)

	if query.Limit > 0 {
		sqlStr += fmt.Sprintf(" LIMIT %d", query.Limit)
		if query.Offset > 0 {
			sqlStr += fmt.Sprintf(" OFFSET %d", query.Offset)
		}
	}

	rows, err := s.db.Query(sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("querying entries: %w", err)
	}
	defer rows.Close()

	var entries []protocol.ContextEntry
	for rows.Next() {
		var entry protocol.ContextEntry
		var tagsJSON string
		var producedAtStr string
		var supersededByStr string

		err := rows.Scan(
			&entry.Ref, &entry.Key, &entry.Kind, &entry.ContentHash, &entry.Size, &entry.SizeTokens,
			&entry.ProducedBy, &producedAtStr, &tagsJSON, &entry.Metadata.TTL, &supersededByStr,
		)
		if err != nil {
			continue
		}

		entry.ProducedAt, _ = time.Parse(time.RFC3339Nano, producedAtStr)
		if supersededByStr != "" {
			entry.Metadata.SupersededBy = protocol.ContextRef(supersededByStr)
		}
		json.Unmarshal([]byte(tagsJSON), &entry.Metadata.Tags)
		entries = append(entries, entry)
	}

	return entries, rows.Err()
}

// Search performs full-text search using FTS5.
func (s *Store) Search(query protocol.SearchQuery) ([]protocol.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if query.Query == "" {
		return nil, fmt.Errorf("search query cannot be empty")
	}

	// Build the FTS5 query
	ftsQuery := query.Query

	// Get limit
	limit := query.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	// Query FTS5 joined with ctx_entries
	// BM25 ranking is built into FTS5 (lower score = better match)
	rows, err := s.db.Query(`
		SELECT e.ref, e.key, e.kind, e.content_hash, e.size, e.size_tokens,
		       e.produced_by, e.produced_at, e.tags, e.ttl_seconds,
		       e.superseded_by, rank, snippet(ctx_entries_fts, 0, '<mark>', '</mark>', '...', 32)
		FROM ctx_entries_fts
		JOIN ctx_entries e ON ctx_entries_fts.rowid = e.rowid
		WHERE ctx_entries_fts MATCH ? AND e.session_id = ?
		ORDER BY rank
		LIMIT ?
	`, ftsQuery, s.sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("searching entries: %w", err)
	}
	defer rows.Close()

	var results []protocol.SearchResult
	for rows.Next() {
		var entry protocol.ContextEntry
		var tagsJSON string
		var producedAtStr string
		var supersededByStr string
		var result protocol.SearchResult

		err := rows.Scan(
			&entry.Ref, &entry.Key, &entry.Kind, &entry.ContentHash, &entry.Size, &entry.SizeTokens,
			&entry.ProducedBy, &producedAtStr, &tagsJSON, &entry.Metadata.TTL,
			&supersededByStr, &result.Score, &result.Snippet,
		)
		if err != nil {
			continue
		}

		entry.ProducedAt, _ = time.Parse(time.RFC3339Nano, producedAtStr)
		if supersededByStr != "" {
			entry.Metadata.SupersededBy = protocol.ContextRef(supersededByStr)
		}
		json.Unmarshal([]byte(tagsJSON), &entry.Metadata.Tags)

		// Apply post-filters
		if shouldIncludeEntry(entry, query) {
			result.Entry = entry
			results = append(results, result)
		}
	}

	return results, rows.Err()
}

// shouldIncludeEntry checks if an entry matches the query filters.
func shouldIncludeEntry(entry protocol.ContextEntry, query protocol.SearchQuery) bool {
	if query.LatestOnly && entry.Metadata.SupersededBy != "" {
		return false
	}

	if len(query.Kinds) > 0 {
		found := false
		for _, k := range query.Kinds {
			if entry.Kind == k {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	if query.ProducedBy != "" && entry.ProducedBy != query.ProducedBy {
		return false
	}

	// Tag filtering
	if len(query.Tags) > 0 {
		for _, qTag := range query.Tags {
			found := false
			for _, eTag := range entry.Metadata.Tags {
				if eTag == qTag {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}

	// PathGlob filtering (simple suffix/prefix matching)
	if query.PathGlob != "" {
		path := entry.Key.Path()
		if strings.HasPrefix(query.PathGlob, "*") {
			suffix := query.PathGlob[1:]
			if !strings.HasSuffix(path, suffix) {
				return false
			}
		} else if strings.HasSuffix(query.PathGlob, "*") {
			prefix := query.PathGlob[:len(query.PathGlob)-1]
			if !strings.HasPrefix(path, prefix) {
				return false
			}
		} else {
			if !strings.Contains(path, query.PathGlob) {
				return false
			}
		}
	}

	return true
}

// Supersede marks an entry as superseded by a newer version.
func (s *Store) Supersede(oldRef, newRef protocol.ContextRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		UPDATE ctx_entries
		SET superseded_by = ?
		WHERE ref = ? AND session_id = ?
	`, newRef.String(), oldRef.String(), s.sessionID)
	if err != nil {
		return fmt.Errorf("superseding entry: %w", err)
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("entry not found: %s", oldRef)
	}

	return nil
}

// Delete removes an entry and its blob if file-backed.
func (s *Store) Delete(ref protocol.ContextRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get entry info first
	var storageType string
	var contentHash string
	err := s.db.QueryRow(`
		SELECT storage_type, content_hash FROM ctx_entries
		WHERE ref = ? AND session_id = ?
	`, ref.String(), s.sessionID).Scan(&storageType, &contentHash)
	if err == sql.ErrNoRows {
		return fmt.Errorf("entry not found: %s", ref)
	}
	if err != nil {
		return fmt.Errorf("querying entry: %w", err)
	}

	// Delete from database (triggers will clean up FTS)
	_, err = s.db.Exec(`DELETE FROM ctx_entries WHERE ref = ? AND session_id = ?`, ref.String(), s.sessionID)
	if err != nil {
		return fmt.Errorf("deleting entry: %w", err)
	}

	// If file-backed, check if other entries reference the same blob before deleting
	if storageType == "file" {
		var count int
		err := s.db.QueryRow(`SELECT COUNT(*) FROM ctx_entries WHERE content_hash = ?`, contentHash).Scan(&count)
		if err == nil && count == 0 {
			// No other entries use this blob, safe to delete
			blobPath := s.blobPath(contentHash)
			os.Remove(blobPath)
		}
	}

	return nil
}

// detectLanguage attempts to detect the programming language from a file path.
func detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".rs":
		return "rust"
	case ".c":
		return "c"
	case ".cpp", ".cc", ".cxx":
		return "cpp"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".swift":
		return "swift"
	case ".kt":
		return "kotlin"
	case ".sh":
		return "shell"
	case ".md":
		return "markdown"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".sql":
		return "sql"
	default:
		return ""
	}
}

// Close closes the store and cleans up resources.
func (s *Store) Close() error {
	// Nothing to close currently, but provides extension point
	return nil
}
