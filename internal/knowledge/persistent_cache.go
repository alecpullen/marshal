package knowledge

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// PersistentCache provides L2 (disk) caching for knowledge queries.
type PersistentCache struct {
	db *sql.DB
}

// NewPersistentCache creates a new persistent cache.
func NewPersistentCache(dbPath string) (*PersistentCache, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating cache directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening cache db: %w", err)
	}

	// Create table
	schema := `
	CREATE TABLE IF NOT EXISTS knowledge_cache (
		question_hash TEXT NOT NULL,
		cited_hash TEXT NOT NULL,
		search_sig TEXT NOT NULL,
		answer_json TEXT NOT NULL,
		inspected_refs_json TEXT NOT NULL,
		cited_refs_json TEXT NOT NULL,
		top_results_json TEXT NOT NULL,
		scope TEXT,
		query TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		access_count INTEGER DEFAULT 0,
		last_accessed INTEGER,
		PRIMARY KEY (question_hash, cited_hash, search_sig)
	);
	CREATE INDEX IF NOT EXISTS idx_scope ON knowledge_cache(scope);
	CREATE INDEX IF NOT EXISTS idx_created ON knowledge_cache(created_at);
	CREATE INDEX IF NOT EXISTS idx_last_accessed ON knowledge_cache(last_accessed);
	`

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("creating schema: %w", err)
	}

	return &PersistentCache{db: db}, nil
}

// Get retrieves a cache entry.
func (pc *PersistentCache) Get(key CacheKey) (*CacheEntry, error) {
	var entry CacheEntry
	var answerJSON, inspectedJSON, citedJSON, topResultsJSON string
	var createdAt int64

	err := pc.db.QueryRow(`
		SELECT answer_json, inspected_refs_json, cited_refs_json, top_results_json,
			   scope, query, created_at
		FROM knowledge_cache
		WHERE question_hash = ? AND cited_hash = ? AND search_sig = ?
	`, key.QuestionHash, key.CitedEntriesHash, key.SearchSignature).Scan(
		&answerJSON, &inspectedJSON, &citedJSON, &topResultsJSON,
		&entry.Scope, &entry.Query, &createdAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("not found")
	}
	if err != nil {
		return nil, fmt.Errorf("querying cache: %w", err)
	}

	// Deserialize
	if err := json.Unmarshal([]byte(answerJSON), &entry.Answer); err != nil {
		return nil, fmt.Errorf("unmarshaling answer: %w", err)
	}
	if err := json.Unmarshal([]byte(inspectedJSON), &entry.InspectedRefs); err != nil {
		return nil, fmt.Errorf("unmarshaling inspected refs: %w", err)
	}
	if err := json.Unmarshal([]byte(citedJSON), &entry.CitedRefs); err != nil {
		return nil, fmt.Errorf("unmarshaling cited refs: %w", err)
	}
	if err := json.Unmarshal([]byte(topResultsJSON), &entry.TopSearchResults); err != nil {
		return nil, fmt.Errorf("unmarshaling top results: %w", err)
	}

	entry.Timestamp = createdAt
	entry.Key = key

	// Update access stats (fire-and-forget)
	go pc.updateAccessStats(key)

	return &entry, nil
}

// Put stores a cache entry.
func (pc *PersistentCache) Put(key CacheKey, entry *CacheEntry) error {
	answerJSON, err := json.Marshal(entry.Answer)
	if err != nil {
		return fmt.Errorf("marshaling answer: %w", err)
	}
	inspectedJSON, err := json.Marshal(entry.InspectedRefs)
	if err != nil {
		return fmt.Errorf("marshaling inspected refs: %w", err)
	}
	citedJSON, err := json.Marshal(entry.CitedRefs)
	if err != nil {
		return fmt.Errorf("marshaling cited refs: %w", err)
	}
	topResultsJSON, err := json.Marshal(entry.TopSearchResults)
	if err != nil {
		return fmt.Errorf("marshaling top results: %w", err)
	}

	_, err = pc.db.Exec(`
		INSERT OR REPLACE INTO knowledge_cache (
			question_hash, cited_hash, search_sig,
			answer_json, inspected_refs_json, cited_refs_json, top_results_json,
			scope, query, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		key.QuestionHash, key.CitedEntriesHash, key.SearchSignature,
		string(answerJSON), string(inspectedJSON), string(citedJSON), string(topResultsJSON),
		entry.Scope, entry.Query, entry.Timestamp,
	)

	if err != nil {
		return fmt.Errorf("inserting cache entry: %w", err)
	}

	return nil
}

// Remove deletes a cache entry.
func (pc *PersistentCache) Remove(key CacheKey) error {
	_, err := pc.db.Exec(`
		DELETE FROM knowledge_cache
		WHERE question_hash = ? AND cited_hash = ? AND search_sig = ?
	`, key.QuestionHash, key.CitedEntriesHash, key.SearchSignature)
	return err
}

// ClearOld removes entries older than maxAge.
func (pc *PersistentCache) ClearOld(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).Unix()
	result, err := pc.db.Exec(`DELETE FROM knowledge_cache WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// Stats returns cache statistics.
func (pc *PersistentCache) Stats() (total int, avgAccessCount float64, err error) {
	var count int64
	var totalAccess int64

	row := pc.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(access_count), 0) FROM knowledge_cache
	`)
	if err := row.Scan(&count, &totalAccess); err != nil {
		return 0, 0, err
	}

	if count > 0 {
		avgAccessCount = float64(totalAccess) / float64(count)
	}

	return int(count), avgAccessCount, nil
}

func (pc *PersistentCache) updateAccessStats(key CacheKey) {
	pc.db.Exec(`
		UPDATE knowledge_cache 
		SET access_count = access_count + 1, last_accessed = ?
		WHERE question_hash = ? AND cited_hash = ? AND search_sig = ?
	`, time.Now().Unix(), key.QuestionHash, key.CitedEntriesHash, key.SearchSignature)
}

// Close closes the database connection.
func (pc *PersistentCache) Close() error {
	return pc.db.Close()
}
