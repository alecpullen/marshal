package limits

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// MatchKind reports how a limit was found, so callers can distinguish a
// keyed hit from an inference across differently-named entries.
type MatchKind int

const (
	MatchNone    MatchKind = iota // nothing known
	MatchExact                    // keyed hit on provider/model, model, or /model
	MatchVariant                  // inferred from a differently-named entry
)

// Table is a merged provider/model-keyed limit table with lookup helpers.
type Table struct {
	entries map[string]Limit
	index   *normIndex
}

// normIndex is the lazily built normalized view of a Table. It is a pointer
// so Table keeps a value receiver while still memoizing the build.
type normIndex struct {
	once  sync.Once
	byKey map[string]Limit
}

// NewTable wraps a raw merged table.
func NewTable(entries map[string]Limit) Table {
	if entries == nil {
		entries = map[string]Limit{}
	}
	return Table{entries: entries, index: &normIndex{}}
}

// normalized returns the normalized index, building it once. Colliding
// normalized keys resolve to the smallest non-zero figure per field:
// under-budgeting is recoverable, exceeding a real limit is a hard failure.
func (t Table) normalized() map[string]Limit {
	if t.index == nil {
		return nil
	}
	t.index.once.Do(func() {
		built := make(map[string]Limit, len(t.entries))
		for key, lim := range t.entries {
			n := norm(key)
			if n == "" {
				continue
			}
			if existing, ok := built[n]; ok {
				built[n] = smallest(existing, lim)
				continue
			}
			built[n] = lim
		}
		t.index.byKey = built
	})
	return t.index.byKey
}

// smallest merges two candidate limits field-by-field, preferring the
// smaller non-zero token-limit value and combining ToolCalling via
// mergeToolCalling.
func smallest(a, b Limit) Limit {
	out := a
	if a.ContextWindow == 0 || (b.ContextWindow != 0 && b.ContextWindow < a.ContextWindow) {
		out.ContextWindow = b.ContextWindow
	}
	if a.MaxOutputTokens == 0 || (b.MaxOutputTokens != 0 && b.MaxOutputTokens < a.MaxOutputTokens) {
		out.MaxOutputTokens = b.MaxOutputTokens
	}
	out.ToolCalling = mergeToolCalling(a.ToolCalling, b.ToolCalling)
	return out
}

func known(lim Limit) bool {
	return lim.ContextWindow != 0 || lim.MaxOutputTokens != 0 || lim.ToolCalling != nil
}

// mergeToolCalling combines two possibly-nil tool-calling signals: any
// confirmed true wins, false only if every reporting source says false,
// nil only if neither source reported anything.
func mergeToolCalling(a, b *bool) *bool {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	trueVal, falseVal := true, false
	if *a || *b {
		return &trueVal
	}
	return &falseVal
}

// Lookup returns the best limit for a model and how it was found:
//  1. exact provider/model key            → MatchExact
//  2. bare model name key                 → MatchExact
//  3. any provider/model key with a matching model suffix → MatchExact
//  4. normalized-id match                 → MatchVariant
//  5. unambiguous prefix match after role-suffix stripping → MatchVariant
//  6. nothing                             → MatchNone
func (t Table) Lookup(providerName, modelID string) (Limit, MatchKind) {
	if modelID == "" || t.entries == nil {
		return Limit{}, MatchNone
	}

	// 1. Exact match.
	if lim, ok := t.entries[providerName+"/"+modelID]; ok && known(lim) {
		return lim, MatchExact
	}
	// 2. Bare model name key (no provider prefix).
	if lim, ok := t.entries[modelID]; ok && known(lim) {
		return lim, MatchExact
	}
	// 3. Model-name-only fallback: any key ending with "/<modelID>".
	suffix := "/" + modelID
	var best Limit
	var found bool
	for key, lim := range t.entries {
		if !strings.HasSuffix(key, suffix) {
			continue
		}
		if !found {
			best, found = lim, true
			continue
		}
		best = smallest(best, lim)
	}
	if found && known(best) {
		return best, MatchExact
	}

	index := t.normalized()
	if len(index) == 0 {
		return Limit{}, MatchNone
	}

	// 4. Normalized match.
	query := norm(modelID)
	if query == "" {
		return Limit{}, MatchNone
	}
	if lim, ok := index[query]; ok && known(lim) {
		return lim, MatchVariant
	}

	// 5. Unambiguous prefix match. Short queries are rejected outright —
	//    a 3-character stem is a prefix of far too much to be evidence.
	stem := stripRoleSuffix(query)
	if len(stem) < 4 {
		return Limit{}, MatchNone
	}
	var candidate Limit
	matches := 0
	for key, lim := range index {
		keyStem := stripRoleSuffix(key)
		if len(keyStem) < 4 || !known(lim) {
			continue
		}
		if strings.HasPrefix(stem, keyStem) || strings.HasPrefix(keyStem, stem) {
			candidate = lim
			matches++
			if matches > 1 {
				return Limit{}, MatchNone
			}
		}
	}
	if matches == 1 {
		return candidate, MatchVariant
	}
	return Limit{}, MatchNone
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
		existing.ToolCalling = mergeToolCalling(existing.ToolCalling, v.ToolCalling)
		out[k] = existing
	}
	return out
}
