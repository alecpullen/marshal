package agent

import (
	"sort"
	"sync"

	"marshal/internal/db"
)

// UsageAggregator accumulates TurnMetrics into session-scope totals and
// per-model breakdowns. It is safe for concurrent use.
type UsageAggregator struct {
	mu      sync.Mutex
	totals  db.UsageTotals
	byModel map[string]*db.ModelBreakdown
}

// NewUsageAggregator returns a new UsageAggregator with its per-model map
// initialised.
func NewUsageAggregator() *UsageAggregator {
	return &UsageAggregator{
		byModel: make(map[string]*db.ModelBreakdown),
	}
}

// Observe adds the token counts and cost from m to the aggregator's totals
// and per-model breakdown. It also updates the breakdown's Reported bitmask
// to reflect which token categories were present.
func (a *UsageAggregator) Observe(m TurnMetrics) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.totals.PromptTokens += int64(m.PromptTokens)
	a.totals.CompletionTokens += int64(m.CompletionTokens)
	a.totals.ReasoningTokens += int64(m.ReasoningTokens)
	a.totals.CacheReadTokens += int64(m.CacheReadTokens)
	a.totals.CacheWriteTokens += int64(m.CacheWriteTokens)
	a.totals.EstimatedCostCents += m.EstimatedCostCents
	a.totals.Turns++

	key := m.Provider + "/" + m.Model
	b, ok := a.byModel[key]
	if !ok {
		b = &db.ModelBreakdown{
			Provider: m.Provider,
			Model:    m.Model,
		}
		a.byModel[key] = b
	}

	b.PromptTokens += int64(m.PromptTokens)
	b.CompletionTokens += int64(m.CompletionTokens)
	b.ReasoningTokens += int64(m.ReasoningTokens)
	b.CacheReadTokens += int64(m.CacheReadTokens)
	b.CacheWriteTokens += int64(m.CacheWriteTokens)
	b.EstimatedCostCents += m.EstimatedCostCents
	b.Turns++

	// Always set prompt and completion.
	b.Reported |= db.CatPrompt
	b.Reported |= db.CatCompletion

	if m.ReasoningTokens > 0 {
		b.Reported |= db.CatReasoning
	}
	if m.CacheReadTokens > 0 {
		b.Reported |= db.CatCacheRead
	}
	if m.CacheWriteTokens > 0 {
		b.Reported |= db.CatCacheWrite
	}
}

// Snapshot returns a copy of the grand totals and a sorted slice of per-model
// breakdowns. Breakdowns are sorted by EstimatedCostCents descending, with
// (Provider, Model) as a stable tie-breaker.
func (a *UsageAggregator) Snapshot() (db.UsageTotals, []db.ModelBreakdown) {
	a.mu.Lock()
	defer a.mu.Unlock()

	totals := a.totals

	breakdowns := make([]db.ModelBreakdown, 0, len(a.byModel))
	for _, b := range a.byModel {
		breakdowns = append(breakdowns, *b)
	}

	sort.SliceStable(breakdowns, func(i, j int) bool {
		// Primary: cost descending.
		if breakdowns[i].EstimatedCostCents != breakdowns[j].EstimatedCostCents {
			return breakdowns[i].EstimatedCostCents > breakdowns[j].EstimatedCostCents
		}
		// Tie-breaker: provider+model ascending.
		ki := breakdowns[i].Provider + "/" + breakdowns[i].Model
		kj := breakdowns[j].Provider + "/" + breakdowns[j].Model
		return ki < kj
	})

	return totals, breakdowns
}
