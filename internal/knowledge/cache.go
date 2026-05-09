package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/alecpullen/marshal/pkg/protocol"
)

// QueryCache implements hybrid L1 (in-memory LRU) + L2 (persistent) caching.
type QueryCache struct {
	l1         *lru.Cache[string, *CacheEntry]
	l2         *PersistentCache
	mu         sync.RWMutex
	allEntries map[string]*CacheEntry // Track all for global invalidation
	maxL1Size  int
}

// NewQueryCache creates a new hybrid cache.
func NewQueryCache(l1Size int, l2Path string) (*QueryCache, error) {
	l1, err := lru.NewWithEvict[string, *CacheEntry](l1Size, func(key string, value *CacheEntry) {
		// On eviction from L1, ensure it's in L2
	})
	if err != nil {
		return nil, fmt.Errorf("creating L1 cache: %w", err)
	}

	l2, err := NewPersistentCache(l2Path)
	if err != nil {
		return nil, fmt.Errorf("creating L2 cache: %w", err)
	}

	return &QueryCache{
		l1:         l1,
		l2:         l2,
		allEntries: make(map[string]*CacheEntry),
		maxL1Size:  l1Size,
	}, nil
}

// Get retrieves from cache if not invalidated.
func (qc *QueryCache) Get(question string, scope string, currentTopResults []protocol.ContextRef) *KnowledgeAnswer {
	// Compute key with current search signature
	searchSig := qc.hashSearchResults(currentTopResults)
	key := qc.computeKey(question, nil, searchSig)

	// Try L1 first
	if entry, ok := qc.l1.Get(key.String()); ok {
		if !qc.isInvalidated(entry, currentTopResults) {
			return &entry.Answer
		}
		// Invalidated - remove from L1
		qc.l1.Remove(key.String())
	}

	// Try L2
	entry, err := qc.l2.Get(key)
	if err == nil {
		if !qc.isInvalidated(entry, currentTopResults) {
			// Promote to L1
			qc.l1.Add(key.String(), entry)
			return &entry.Answer
		}
		// Invalidated - remove from tracking
		qc.l2.Remove(key)
	}

	return nil
}

// Put stores result in cache.
func (qc *QueryCache) Put(
	question string,
	scope string,
	answer *KnowledgeAnswer,
	inspectedRefs []protocol.ContextRef,
	topSearchResults []protocol.ContextRef,
) {
	searchSig := qc.hashSearchResults(topSearchResults)
	key := qc.computeKey(question, answer.Citations, searchSig)

	entry := &CacheEntry{
		Key:              key,
		Answer:           *answer,
		InspectedRefs:    inspectedRefs,
		CitedRefs:        answer.Citations,
		TopSearchResults: topSearchResults,
		Timestamp:        time.Now().Unix(),
		Scope:            scope,
		Query:            question,
	}

	// Add to L1
	qc.l1.Add(key.String(), entry)

	// Add to L2 asynchronously
	go func() {
		qc.l2.Put(key, entry)
	}()

	// Track for global invalidation
	qc.mu.Lock()
	qc.allEntries[question] = entry
	qc.mu.Unlock()
}

// isInvalidated checks if cache entry is stale.
func (qc *QueryCache) isInvalidated(entry *CacheEntry, currentTopResults []protocol.ContextRef) bool {
	// Check 1: Any cited entry superseded?
	for _, ref := range entry.CitedRefs {
		if qc.isRefSuperseded(ref) {
			return true
		}
	}

	// Check 2: Search signature changed significantly?
	currentSig := qc.hashSearchResults(currentTopResults)
	if entry.Key.SearchSignature != currentSig {
		return qc.hasSearchResultsChangedSignificantly(entry.TopSearchResults, currentTopResults)
	}

	return false
}

// hasSearchResultsChangedSignificantly checks if >30% of top 10 results changed.
func (qc *QueryCache) hasSearchResultsChangedSignificantly(cached, current []protocol.ContextRef) bool {
	compareSize := 10
	if len(cached) < compareSize {
		compareSize = len(cached)
	}
	if len(current) < compareSize {
		compareSize = len(current)
	}

	if compareSize == 0 {
		return len(cached) != len(current)
	}

	changes := 0
	for i := 0; i < compareSize; i++ {
		if cached[i] != current[i] {
			changes++
		}
	}

	// Invalidate if >30% changed
	// Use changes*10 > compareSize*3 for more precision (equivalent to changes > compareSize*0.3)
	return changes*10 > compareSize*3
}

// isRefSuperseded checks if a reference has been superseded.
// This is a placeholder - actual implementation would check context store.
func (qc *QueryCache) isRefSuperseded(ref protocol.ContextRef) bool {
	// TODO: Implement by checking if entry has superseded_by set in context store
	return false
}

// InvalidateOnSuperseded removes entries with superseded citations.
func (qc *QueryCache) InvalidateOnSuperseded(supersededRefs []protocol.ContextRef) {
	qc.mu.Lock()
	defer qc.mu.Unlock()

	for question, entry := range qc.allEntries {
		for _, ref := range entry.CitedRefs {
			for _, superseded := range supersededRefs {
				if ref == superseded {
					// Invalidate this entry
					key := entry.Key.String()
					qc.l1.Remove(key)
					qc.l2.Remove(entry.Key)
					delete(qc.allEntries, question)
					break
				}
			}
		}
	}
}

// computeKey creates a cache key.
func (qc *QueryCache) computeKey(
	question string,
	citations []protocol.ContextRef,
	searchSig string,
) CacheKey {
	normalized := normalizeQuestion(question)
	qHash := hashString(normalized)

	var citedHash string
	if len(citations) > 0 {
		citedHash = hashRefs(citations)
	} else {
		citedHash = "pending"
	}

	return CacheKey{
		QuestionHash:       qHash,
		CitedEntriesHash:   citedHash,
		SearchSignature:    searchSig,
	}
}

// hashSearchResults creates a hash of top search results.
func (qc *QueryCache) hashSearchResults(results []protocol.ContextRef) string {
	if len(results) > 10 {
		results = results[:10]
	}
	return hashRefs(results)
}

// ClearOld removes old entries from both caches.
func (qc *QueryCache) ClearOld(maxAge time.Duration) (int, error) {
	return qc.l2.ClearOld(maxAge)
}

// Stats returns cache statistics.
func (qc *QueryCache) Stats() (l1Size int, l2Total int, l2AvgAccess float64, err error) {
	l1Size = qc.l1.Len()
	l2Total, l2AvgAccess, err = qc.l2.Stats()
	return
}

// Close closes the cache and its resources.
func (qc *QueryCache) Close() error {
	return qc.l2.Close()
}

// Helper functions

func normalizeQuestion(q string) string {
	return strings.ToLower(strings.TrimSpace(q))
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8]) // First 8 chars sufficient
}

func hashRefs(refs []protocol.ContextRef) string {
	if len(refs) == 0 {
		return ""
	}

	// Sort for consistent hashing
	sorted := make([]protocol.ContextRef, len(refs))
	copy(sorted, refs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})

	// Concatenate and hash
	var b strings.Builder
	for _, r := range sorted {
		b.WriteString(string(r))
	}
	return hashString(b.String())
}
