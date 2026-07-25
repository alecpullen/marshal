package limits

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Table is a merged provider/model-keyed limit table with lookup helpers.
type Table struct {
	entries map[string]Limit
}

// NewTable wraps a raw merged table.
func NewTable(entries map[string]Limit) Table {
	if entries == nil {
		entries = map[string]Limit{}
	}
	return Table{entries: entries}
}

// Lookup returns the best limit for a model using the hierarchy:
// 1. exact provider/model key
// 2. bare model name key (no provider prefix)
// 3. any provider/model key whose model suffix matches
// 4. false if nothing is known.
func (t Table) Lookup(providerName, modelID string) (Limit, bool) {
	// 1. Exact match.
	key := providerName + "/" + modelID
	if lim, ok := t.entries[key]; ok && (lim.ContextWindow != 0 || lim.MaxOutputTokens != 0) {
		return lim, true
	}
	// 2. Bare model name key (no provider prefix).
	if lim, ok := t.entries[modelID]; ok && (lim.ContextWindow != 0 || lim.MaxOutputTokens != 0) {
		return lim, true
	}
	// 3. Model-name-only fallback: find any key ending with "/<modelID>"
	//    that has the needed fields.
	suffix := "/" + modelID
	var best Limit
	var found bool
	for k, lim := range t.entries {
		if strings.HasSuffix(k, suffix) {
			if !found || (best.ContextWindow == 0 && lim.ContextWindow != 0) {
				best = lim
				found = true
			}
		}
	}
	return best, found
}

// Refresh fetches both public sources, merges them, and writes the cache.
// OpenRouter wins over LiteLLM for MaxOutputTokens; the largest non-zero
// ContextWindow wins.
func Refresh(ctx context.Context, dataDir string) error {
	orTable, err := Fetch(ctx, "openrouter")
	if err != nil {
		return fmt.Errorf("fetch openrouter: %w", err)
	}
	llmTable, err := Fetch(ctx, "litellm")
	if err != nil {
		return fmt.Errorf("fetch litellm: %w", err)
	}
	merged := merge(orTable, llmTable)
	return Save(dataDir, Cache{Table: merged, FetchedAt: time.Now().UTC()})
}

// LoadTable reads the on-disk cache and returns a lookup Table. If the
// cache is missing or expired, it attempts a Refresh. If Refresh fails and
// a stale cache exists, the stale cache is still returned.
func LoadTable(ctx context.Context, dataDir string, ttl time.Duration) (Table, error) {
	cache, err := Load(dataDir)
	if err != nil {
		return Table{}, err
	}
	if cache.FetchedAt.IsZero() || time.Since(cache.FetchedAt) > ttl {
		if refreshErr := Refresh(ctx, dataDir); refreshErr != nil {
			if cache.FetchedAt.IsZero() {
				return Table{}, refreshErr
			}
			// Return stale cache with the error attached but no failure.
			return NewTable(cache.Table), nil
		}
		cache, err = Load(dataDir)
		if err != nil {
			return Table{}, err
		}
	}
	return NewTable(cache.Table), nil
}

func merge(a, b map[string]Limit) map[string]Limit {
	out := make(map[string]Limit, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		existing, ok := out[k]
		if !ok {
			out[k] = v
			continue
		}
		if v.ContextWindow > existing.ContextWindow {
			existing.ContextWindow = v.ContextWindow
		}
		if v.MaxOutputTokens != 0 && (existing.MaxOutputTokens == 0 || v.MaxOutputTokens < existing.MaxOutputTokens) {
			// Prefer provider-specific smaller output limit when both exist.
			existing.MaxOutputTokens = v.MaxOutputTokens
		}
		out[k] = existing
	}
	return out
}
