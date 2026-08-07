package agent

// historyBudget returns the cross-turn history budget in tokens for the
// resolved model window. When configured > 0, the caller's value wins
// (a hard ceiling, like MaxTurnContextTokens). When configured == 0,
// the budget derives from the window:
//
//	window ≤ 32000   → 4000  (small local models can't afford more)
//	32000 < window ≤ 1_024_000 → window / 8
//	window > 1_024_000 → 128000  (cap so big-cloud models don't drown the
//	                              prompt in old turns; the rest of the
//	                              window is reserved for current-turn work)
//
// The clamps exist for two reasons:
//
//   - On a 32k or smaller model, history beyond ~4k tokens eats into the
//     per-turn budget so aggressively that even tool-heavy turns trip
//     compaction. 4k is enough for ~10 user+assistant turns at our
//     typical message sizes, which covers the common debugging loop.
//
//   - On a 1M+ model, naive window/8 = 128k tokens of old turns is
//     pure noise: a model with that much context already has plenty
//     for the current turn. 128k leaves room for ~320 typical turns and
//     lets the prompt pack in current-turn work.
func historyBudget(window, configured int) int {
	const (
		min = 4000
		max = 128000
	)
	if configured > 0 {
		return configured
	}
	if window <= 32000 {
		return min
	}
	if window > 1_024_000 {
		return max
	}
	return window / 8
}
